package portal

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// RelayMetricsConfig makes the portal read each relay's Prometheus metrics
// over SSH for the admin dashboard. A relay's /metrics listens on loopback
// only, so the portal logs in with a key the relay
// restricts to one forced command in authorized_keys and
// reads its output; nothing on the relay is opened to the network.
type RelayMetricsConfig struct {
	KeyFile        string        // private key (OpenSSH format)
	KnownHostsFile string        // pinned host keys; unknown hosts are refused
	Interval       time.Duration // between scrapes (default 1m)
	Timeout        time.Duration // per relay (default 20s)
	Targets        []RelayMetricsTarget
}

// RelayMetricsTarget names where to read one registered relay's metrics.
type RelayMetricsTarget struct {
	Relay   string // the relay's name in the registry
	SSH     string // user@host[:port]
	Command string // run on the relay (the key's forced command wins anyway); default curl of 127.0.0.1:8093/metrics
}

const defaultMetricsCommand = "curl -s --max-time 8 http://127.0.0.1:8093/metrics"

// RelayNetMetrics is one access network as one relay sees it.
type RelayNetMetrics struct {
	Network         string `json:"network"`
	Reserved        int64  `json:"coordinators_reserved"`
	Expected        int64  `json:"coordinators_expected"`
	Reservations    int64  `json:"reservations"`
	MaxReservations int64  `json:"max_reservations"`
	Denied          int64  `json:"reservations_denied_total"`
	// Clients are the devices holding a reservation, by the build their
	// portal-signed pass names; Mismatches those whose libp2p agent disagrees.
	Clients    []RelayClientCount `json:"clients"`
	Mismatches int64              `json:"client_mismatches"`
}

// RelayClientCount is how many peers of one build a relay sees.
type RelayClientCount struct {
	Program string `json:"program,omitempty"` // agents only: client, coordinator, relay, other
	Version string `json:"version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	Count   int64  `json:"count"`
}

// RelayMetrics is the last scrape of one relay.
type RelayMetrics struct {
	Relay      string `json:"relay"`
	ScrapedAt  int64  `json:"scraped_at"`        // unix nanos of the last attempt
	OKAt       int64  `json:"ok_at,omitempty"`   // last successful scrape
	Error      string `json:"error,omitempty"`   // the last attempt's failure
	ScrapeMs   int64  `json:"scrape_ms"`         // how long the last attempt took
	Version    string `json:"version,omitempty"` // build_info
	PeerID     string `json:"peer_id,omitempty"`
	Service    bool   `json:"service_enabled"`
	Peers      int64  `json:"peers"`
	Conns      int64  `json:"connections"`
	MaxConns   int64  `json:"max_connections"`
	Resv       int64  `json:"reservations"`
	MaxResv    int64  `json:"max_reservations"`
	Circuits   int64  `json:"circuits"`
	ResvDenied int64  `json:"reservations_denied_total"`
	CircDenied int64  `json:"circuits_denied_total"`
	CircOpened int64  `json:"circuits_opened_total"`
	PassesBad  int64  `json:"passes_rejected_total"`
	Relayed    int64  `json:"relayed_bytes_total"`
	// Rates over the last two successful scrapes; -1 when unknown.
	RelayedBps   float64           `json:"relayed_bps"`
	ResvDeniedPM float64           `json:"reservations_denied_per_min"`
	CPUPercent   float64           `json:"cpu_percent"`
	RSSBytes     int64             `json:"rss_bytes"`
	OpenFDs      int64             `json:"open_fds"`
	MaxFDs       int64             `json:"max_fds"`
	StartedAt    int64             `json:"started_at,omitempty"` // unix nanos
	NetRxBytes   int64             `json:"net_rx_bytes_total"`
	NetTxBytes   int64             `json:"net_tx_bytes_total"`
	NetRxBps     float64           `json:"net_rx_bps"` // -1 when unknown
	NetTxBps     float64           `json:"net_tx_bps"`
	Networks     []RelayNetMetrics `json:"networks"`
	// Agents are the connected peers by their libp2p identify agent (unsigned).
	Agents     []RelayClientCount `json:"agents"`
	cpuSeconds float64
	okMono     time.Time
}

// relayMetrics scrapes the configured relays in the background.
type relayMetrics struct {
	cfg    RelayMetricsConfig
	client *ssh.ClientConfig
	now    func() time.Time
	log    *slog.Logger
	save   func(RelayMetrics) // called with every successful scrape, outside mu

	mu   sync.Mutex
	last map[string]*RelayMetrics // by relay name
}

func newRelayMetrics(cfg RelayMetricsConfig, now func() time.Time, lg *slog.Logger) (*relayMetrics, error) {
	if len(cfg.Targets) == 0 {
		return nil, nil
	}
	if cfg.Interval <= 0 {
		cfg.Interval = time.Minute
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 20 * time.Second
	}
	pem, err := os.ReadFile(cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("relay metrics key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(pem)
	if err != nil {
		return nil, fmt.Errorf("relay metrics key %s: %w", cfg.KeyFile, err)
	}
	if cfg.KnownHostsFile == "" {
		return nil, errors.New("relay metrics: known_hosts_file is required (host keys are pinned)")
	}
	hostKeys, err := knownhosts.New(cfg.KnownHostsFile)
	if err != nil {
		return nil, fmt.Errorf("relay metrics known hosts: %w", err)
	}
	algos, err := pinnedAlgorithms(cfg.KnownHostsFile)
	if err != nil {
		return nil, err
	}
	for i, t := range cfg.Targets {
		if t.Relay == "" || t.SSH == "" {
			return nil, fmt.Errorf("relay metrics target %d: relay and ssh are required", i+1)
		}
		if _, _, err := splitSSHTarget(t.SSH); err != nil {
			return nil, fmt.Errorf("relay metrics target %s: %w", t.Relay, err)
		}
	}
	return &relayMetrics{cfg: cfg, now: now, log: lg, last: map[string]*RelayMetrics{},
		client: &ssh.ClientConfig{Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: hostKeys, HostKeyAlgorithms: algos, Timeout: cfg.Timeout}}, nil
}

// pinnedAlgorithms lists the host key algorithms of the pinned keys. A server
// offers its preferred kind of host key, and a kind that is not pinned fails
// as a mismatch even when the pinned kind would have matched.
func pinnedAlgorithms(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("relay metrics known hosts: %w", err)
	}
	seen := map[string]bool{}
	var out []string
	addAlgo := func(a ...string) {
		for _, x := range a {
			if !seen[x] {
				seen[x] = true
				out = append(out, x)
			}
		}
	}
	for len(data) > 0 {
		_, _, key, _, rest, err := ssh.ParseKnownHosts(data)
		if err != nil {
			break // io.EOF at the end
		}
		data = rest
		if key.Type() == ssh.KeyAlgoRSA {
			addAlgo(ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA)
		} else {
			addAlgo(key.Type())
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("relay metrics known hosts %s: no host keys", path)
	}
	return out, nil
}

// splitSSHTarget parses user@host[:port].
func splitSSHTarget(s string) (user, addr string, err error) {
	user, host, ok := strings.Cut(s, "@")
	if !ok || user == "" || host == "" {
		return "", "", errors.New("ssh must be user@host[:port]")
	}
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(strings.Trim(host, "[]"), "22")
	}
	return user, host, nil
}

func (m *relayMetrics) run(ctx context.Context) {
	for {
		var wg sync.WaitGroup
		for _, t := range m.cfg.Targets {
			wg.Add(1)
			go func(t RelayMetricsTarget) { defer wg.Done(); m.scrapeOne(ctx, t) }(t)
		}
		wg.Wait()
		select {
		case <-ctx.Done():
			return
		case <-time.After(m.cfg.Interval):
		}
	}
}

// snapshot returns copies of the last scrapes by relay name.
func (m *relayMetrics) snapshot() map[string]RelayMetrics {
	out := map[string]RelayMetrics{}
	if m == nil {
		return out
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, v := range m.last {
		c := *v
		c.Networks = append([]RelayNetMetrics(nil), v.Networks...)
		out[k] = c
	}
	return out
}

func (m *relayMetrics) scrapeOne(ctx context.Context, t RelayMetricsTarget) {
	start := time.Now()
	body, err := m.fetch(ctx, t)
	var cur *RelayMetrics
	if err == nil {
		cur, err = parseRelayMetrics(body)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	prev := m.last[t.Relay]
	at := m.now().UnixNano()
	if err != nil {
		if prev == nil {
			prev = &RelayMetrics{Relay: t.Relay, RelayedBps: -1, ResvDeniedPM: -1, CPUPercent: -1, NetRxBps: -1, NetTxBps: -1}
			m.last[t.Relay] = prev
		}
		if prev.Error != err.Error() {
			m.log.Warn("relay metrics unavailable", "relay", t.Relay, "err", err)
		}
		prev.ScrapedAt, prev.Error, prev.ScrapeMs = at, err.Error(), time.Since(start).Milliseconds()
		return
	}
	if prev == nil || prev.Error != "" {
		m.log.Info("relay metrics read", "relay", t.Relay, "version", cur.Version, "ms", time.Since(start).Milliseconds())
	}
	cur.Relay, cur.ScrapedAt, cur.OKAt, cur.ScrapeMs, cur.okMono = t.Relay, at, at, time.Since(start).Milliseconds(), time.Now()
	cur.RelayedBps, cur.ResvDeniedPM, cur.CPUPercent, cur.NetRxBps, cur.NetTxBps = -1, -1, -1, -1, -1
	if prev != nil && prev.OKAt != 0 && !prev.okMono.IsZero() && prev.StartedAt == cur.StartedAt {
		if dt := cur.okMono.Sub(prev.okMono).Seconds(); dt > 1 {
			cur.RelayedBps = nonNeg(float64(cur.Relayed-prev.Relayed) / dt)
			cur.ResvDeniedPM = nonNeg(float64(cur.ResvDenied-prev.ResvDenied) / dt * 60)
			cur.CPUPercent = nonNeg((cur.cpuSeconds - prev.cpuSeconds) / dt * 100)
			cur.NetRxBps = nonNeg(float64(cur.NetRxBytes-prev.NetRxBytes) / dt)
			cur.NetTxBps = nonNeg(float64(cur.NetTxBytes-prev.NetTxBytes) / dt)
		}
	}
	m.last[t.Relay] = cur
	if m.save != nil {
		go m.save(*cur)
	}
}

func nonNeg(f float64) float64 {
	if f < 0 {
		return -1
	}
	return f
}

const maxMetricsBody = 1 << 20

func (m *relayMetrics) fetch(ctx context.Context, t RelayMetricsTarget) ([]byte, error) {
	user, addr, _ := splitSSHTarget(t.SSH)
	cfg := *m.client
	cfg.User = user
	ctx, cancel := context.WithTimeout(ctx, m.cfg.Timeout)
	defer cancel()
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(m.cfg.Timeout))
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, &cfg)
	if err != nil {
		return nil, err
	}
	client := ssh.NewClient(c, chans, reqs)
	defer client.Close()
	sess, err := client.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	cmd := t.Command
	if cmd == "" {
		cmd = defaultMetricsCommand
	}
	var out limitedBuffer
	out.max = maxMetricsBody
	sess.Stdout = &out
	if err := sess.Run(cmd); err != nil {
		return nil, fmt.Errorf("metrics command: %w", err)
	}
	if out.over {
		return nil, errors.New("metrics output too large")
	}
	return out.Bytes(), nil
}

type limitedBuffer struct {
	bytes.Buffer
	max  int
	over bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.max {
		b.over = true
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

// parseRelayMetrics reads the Prometheus text a relay serves.
func parseRelayMetrics(body []byte) (*RelayMetrics, error) {
	r := &RelayMetrics{}
	nets := map[string]*RelayNetMetrics{}
	netOf := func(n string) *RelayNetMetrics {
		if nets[n] == nil {
			nets[n] = &RelayNetMetrics{Network: n}
		}
		return nets[n]
	}
	seen := false
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		name, labels, val, ok := parsePromLine(line)
		if !ok {
			continue
		}
		iv := int64(val)
		switch name {
		case "sharetoken_relay_build_info":
			r.Version, seen = labels["version"], true
		case "sharetoken_relay_info":
			r.PeerID, seen = labels["peer_id"], true
		case "sharetoken_relay_service_enabled":
			r.Service = val == 1
		case "sharetoken_relay_peers":
			r.Peers = iv
		case "sharetoken_relay_connections":
			r.Conns = iv
		case "sharetoken_relay_max_connections":
			r.MaxConns = iv
		case "sharetoken_relay_reservations":
			r.Resv = iv
		case "sharetoken_relay_max_reservations":
			r.MaxResv = iv
		case "sharetoken_relay_circuits":
			r.Circuits = iv
		case "sharetoken_relay_reservations_denied_total":
			r.ResvDenied = iv
		case "sharetoken_relay_circuits_denied_total":
			r.CircDenied = iv
		case "sharetoken_relay_circuits_opened_total":
			r.CircOpened = iv
		case "sharetoken_relay_passes_rejected_total":
			r.PassesBad = iv
		case "sharetoken_relay_relayed_bytes_total":
			r.Relayed = iv
		case "sharetoken_relay_network_coordinators_reserved":
			netOf(labels["network"]).Reserved = iv
		case "sharetoken_relay_network_coordinators_expected":
			netOf(labels["network"]).Expected = iv
		case "sharetoken_relay_network_reservations":
			netOf(labels["network"]).Reservations = iv
		case "sharetoken_relay_network_max_reservations":
			netOf(labels["network"]).MaxReservations = iv
		case "sharetoken_relay_network_reservations_denied_total":
			netOf(labels["network"]).Denied = iv
		case "sharetoken_relay_network_client_mismatches":
			netOf(labels["network"]).Mismatches = iv
		case "sharetoken_relay_network_clients":
			n := netOf(labels["network"])
			n.Clients = append(n.Clients, RelayClientCount{Version: labels["version"], OS: labels["os"], Arch: labels["arch"], Count: iv})
		case "sharetoken_relay_agents":
			r.Agents = append(r.Agents, RelayClientCount{Program: labels["program"], Version: labels["version"], OS: labels["os"], Arch: labels["arch"], Count: iv})
		case "process_cpu_seconds_total":
			r.cpuSeconds = val
		case "process_resident_memory_bytes":
			r.RSSBytes = iv
		case "process_open_fds":
			r.OpenFDs = iv
		case "process_max_fds":
			r.MaxFDs = iv
		case "process_start_time_seconds":
			r.StartedAt = int64(val * 1e9)
		case "process_network_receive_bytes_total":
			r.NetRxBytes = iv
		case "process_network_transmit_bytes_total":
			r.NetTxBytes = iv
		}
	}
	if !seen {
		return nil, errors.New("no relay metrics in the output (relay down or too old)")
	}
	r.Networks = make([]RelayNetMetrics, 0, len(nets))
	for _, n := range nets {
		if n.Clients == nil {
			n.Clients = []RelayClientCount{}
		}
		sortCounts(n.Clients)
		r.Networks = append(r.Networks, *n)
	}
	if r.Agents == nil {
		r.Agents = []RelayClientCount{}
	}
	sortCounts(r.Agents)
	sort.Slice(r.Networks, func(i, j int) bool { return r.Networks[i].Network < r.Networks[j].Network })
	return r, nil
}

// sortCounts puts the most common builds first.
func sortCounts(c []RelayClientCount) {
	sort.Slice(c, func(i, j int) bool {
		if c[i].Count != c[j].Count {
			return c[i].Count > c[j].Count
		}
		return c[i].Program+"/"+c[i].Version+"/"+c[i].OS+"/"+c[i].Arch < c[j].Program+"/"+c[j].Version+"/"+c[j].OS+"/"+c[j].Arch
	})
}

// parsePromLine splits `name{k="v",...} value [timestamp]`.
func parsePromLine(line string) (name string, labels map[string]string, val float64, ok bool) {
	rest := line
	if i := strings.IndexByte(line, '{'); i >= 0 {
		j := strings.LastIndexByte(line, '}')
		if j < i {
			return "", nil, 0, false
		}
		name, rest = line[:i], strings.TrimSpace(line[j+1:])
		labels = map[string]string{}
		for _, kv := range strings.Split(line[i+1:j], ",") {
			k, v, found := strings.Cut(kv, "=")
			if !found {
				continue
			}
			labels[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"`)
		}
	} else {
		sp := strings.IndexAny(line, " \t")
		if sp < 0 {
			return "", nil, 0, false
		}
		name, rest = line[:sp], strings.TrimSpace(line[sp:])
	}
	if f := strings.Fields(rest); len(f) > 0 {
		v, err := strconv.ParseFloat(f[0], 64)
		if err != nil {
			return "", nil, 0, false
		}
		return name, labels, v, true
	}
	return "", nil, 0, false
}
