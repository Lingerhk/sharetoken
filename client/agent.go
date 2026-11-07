package client

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"

	"sharetoken/attest"
	"sharetoken/attest/hw"
	"sharetoken/buyer"
	"sharetoken/internal/cli"
	"sharetoken/node"
	"sharetoken/p2p"
	"sharetoken/portal"
	"sharetoken/relaypass"
)

// Agent is a running client.
type Agent struct {
	// Timing of the background work; set before Start (zero: defaults).
	HealthEvery  time.Duration // check the path to the coordinator (10s)
	MeasureEvery time.Duration // re-measure relays (5m)
	RefreshEvery time.Duration // refresh the relay list from the portal (5m)
	// HostOptions are extra libp2p options for the agent's host (embedding,
	// tests); set before Start.
	HostOptions []libp2p.Option

	home string
	log  *slog.Logger

	mu       sync.Mutex
	cfg      Config
	keys     *p2p.Keys
	quote    *attest.Quote
	h        host.Host
	portal   *PortalClient
	netinfo  portal.NetworkInfo
	coord    peer.ID
	relays   []p2p.RelayRTT // last measurement, best first
	current  string         // relay used to reach the coordinator
	measured time.Time
	link     *p2p.LinkMonitor // live pings of the current relay
	buyer    *buyer.Client
	proxy    *buyer.Proxy
	share    *shareState
	warnings map[string]string

	passMu sync.Mutex
	pass   *relaypass.Pass // admits this device's relay reservations

	ctx     context.Context
	cancel  context.CancelFunc
	srv     *http.Server
	addr    string
	started time.Time
	kick    chan struct{}
}

type shareState struct {
	cfg      ShareConfig
	node     *node.Service
	reserver *p2p.Reserver
	cancel   context.CancelFunc
}

// NewAgent prepares an agent with the configuration in home.
func NewAgent(home string, cfg Config) *Agent {
	cfg.fill()
	return &Agent{home: home, cfg: cfg, log: slog.Default().With("component", "client"), warnings: map[string]string{}, kick: make(chan struct{}, 1),
		link: p2p.NewLinkMonitor(linkWindow)}
}

// Start joins the network and serves the local API; it returns once the
// agent is up. Close stops it.
func (a *Agent) Start(ctx context.Context) error {
	if a.cfg.Portal == "" || a.cfg.Token == "" {
		return errors.New("not logged in: run `sharetoken login` first")
	}
	if err := a.cfg.Share.Validate(); err != nil {
		return err
	}
	if a.HealthEvery == 0 {
		a.HealthEvery = 10 * time.Second
	}
	if a.MeasureEvery == 0 {
		a.MeasureEvery = 5 * time.Minute
	}
	if a.RefreshEvery == 0 {
		a.RefreshEvery = 5 * time.Minute
	}
	a.ctx, a.cancel = context.WithCancel(ctx)
	a.started = time.Now()
	a.portal = NewPortalClient(a.cfg.Portal, a.cfg.Token, a.cfg.PortalPin)
	if InsecurePortalURL(a.cfg.Portal) {
		a.warn("portal-http", "the portal is plain HTTP: your token crosses the network unencrypted")
	}

	if err := a.loadKeys(); err != nil {
		return err
	}
	if err := a.refreshNetwork(a.ctx); err != nil {
		return err
	}
	if err := a.bind(a.ctx); err != nil {
		return err
	}
	h, err := p2p.NewHost(a.keys, p2p.HostConfig{Listen: a.cfg.Listen, NAT: a.cfg.NAT, Options: a.HostOptions})
	if err != nil {
		return err
	}
	a.h = h
	if err := a.setupBuyer(); err != nil {
		_ = h.Close()
		return err
	}
	a.selectRelay(a.ctx, false)
	a.mu.Lock()
	if a.cfg.Share.Enabled {
		if err := a.startShareLocked(a.cfg.Share); err != nil {
			a.warn("share", err.Error())
		}
	}
	a.mu.Unlock()

	ln, err := net.Listen("tcp", a.cfg.HTTP)
	if err != nil {
		a.Close()
		return fmt.Errorf("local API on %s: %w", a.cfg.HTTP, err)
	}
	a.addr = ln.Addr().String()
	a.srv = &http.Server{Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := a.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			a.log.Error("local API stopped", "err", err)
		}
	}()
	go a.maintain()
	a.writeRunFile()
	return nil
}

// RunFile records a running client's API address, for the CLI.
const RunFile = "run.json"

// RunInfo is the content of RunFile.
type RunInfo struct {
	API string `json:"api"` // host:port of the local API
	PID int    `json:"pid"`
}

func (a *Agent) writeRunFile() {
	data, _ := json.Marshal(RunInfo{API: a.addr, PID: os.Getpid()})
	_ = os.WriteFile(filepath.Join(a.home, RunFile), data, 0o600)
}

// APIAddr returns the local API address of the client running with home:
// the address it recorded at start-up, else the configured one.
func APIAddr(home string, cfg Config) string {
	var ri RunInfo
	if data, err := os.ReadFile(filepath.Join(home, RunFile)); err == nil && json.Unmarshal(data, &ri) == nil && ri.API != "" {
		return ri.API
	}
	return cfg.HTTP
}

// Run starts the agent and blocks until ctx ends.
func (a *Agent) Run(ctx context.Context) error {
	if err := a.Start(ctx); err != nil {
		return err
	}
	<-ctx.Done()
	a.Close()
	return nil
}

// Close stops sharing, the local API and the host.
func (a *Agent) Close() {
	if a.srv != nil {
		_ = os.Remove(filepath.Join(a.home, RunFile))
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = a.srv.Shutdown(sctx)
		cancel()
	}
	a.mu.Lock()
	a.stopShareLocked()
	b := a.buyer
	a.mu.Unlock()
	if b != nil {
		wctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_ = b.WaitDisputes(wctx) // let disputes in flight reach the coordinator
		cancel()
	}
	if a.cancel != nil {
		a.cancel()
	}
	if a.h != nil {
		_ = a.h.Close()
	}
}

// Addr is the local API's address (host:port).
func (a *Agent) Addr() string { return a.addr }

// PeerID is this device's peer ID.
func (a *Agent) PeerID() string { return a.keys.ID() }

// Host is the agent's libp2p host (tests).
func (a *Agent) Host() host.Host { return a.h }

func (a *Agent) warn(key, msg string) {
	if msg == "" {
		delete(a.warnings, key)
		return
	}
	if a.warnings[key] != msg {
		a.log.Warn(msg, "about", key)
	}
	a.warnings[key] = msg
}

func (a *Agent) loadKeys() error {
	tee := a.cfg.Share.TEE
	if !a.cfg.Share.Enabled || tee == "" || tee == "off" {
		if err := os.MkdirAll(a.home, 0o700); err != nil {
			return err
		}
		k, err := p2p.LoadOrCreateKeys(filepath.Join(a.home, KeyFile))
		a.keys = k
		return err
	}
	// In a confidential VM the keys exist only in guest memory (a key file
	// would be readable by whoever controls the disk), so the peer ID changes
	// every boot and the device is re-bound at start-up.
	k, err := p2p.NewKeys()
	if err != nil {
		return err
	}
	q, err := attestKeys(tee, k)
	if err != nil {
		return err
	}
	a.keys, a.quote = k, q
	return nil
}

func attestKeys(backend string, keys *p2p.Keys) (*attest.Quote, error) {
	att, err := hw.New(backend)
	if err != nil {
		return nil, err
	}
	q, err := att.Attest(attest.ReportData(keys.ReceiptKey(), keys.Box.Public()))
	if err != nil {
		return nil, err
	}
	claims, err := cli.TEEVerifier(false).Verify(q)
	if err != nil {
		return nil, fmt.Errorf("own quote does not verify: %w", err)
	}
	slog.Info("attested", "backend", q.Backend, "measurement", hex.EncodeToString(claims.Measurement), "debug", claims.Debug)
	return q, nil
}

// refreshNetwork fetches the coordinator and relay list from the portal,
// falling back to the last copy when the portal is unreachable.
func (a *Agent) refreshNetwork(ctx context.Context) error {
	cache := filepath.Join(a.home, NetworkFile)
	n, err := a.portal.Network(ctx)
	if err != nil {
		data, rerr := os.ReadFile(cache)
		if rerr != nil || json.Unmarshal(data, n) != nil || n.Coordinator == "" {
			return fmt.Errorf("cannot get the network from the portal: %w", err)
		}
		a.mu.Lock()
		a.warn("portal", "portal unreachable, using the cached relay list: "+err.Error())
		a.mu.Unlock()
	} else if data, err := json.Marshal(n); err == nil {
		_ = os.WriteFile(cache, data, 0o600)
		a.mu.Lock()
		a.warn("portal", "")
		a.mu.Unlock()
	}
	id, err := peer.Decode(n.Coordinator)
	if err != nil {
		return fmt.Errorf("portal returned a bad coordinator id: %w", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.coord != "" && a.coord != id {
		a.log.Warn("the coordinator changed; restart the client to follow it", "old", a.coord, "new", id)
		return nil
	}
	a.coord, a.netinfo = id, *n
	return nil
}

// bind (re)binds this device's key to the logged-in user. Binding is
// idempotent; an expired login stops the agent.
func (a *Agent) bind(ctx context.Context) error {
	name := a.cfg.DeviceName
	if name == "" {
		name, _ = os.Hostname()
	}
	_, err := a.portal.BindDevice(ctx, a.keys, a.cfg.User, name)
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case err == nil:
		a.warn("device", "")
	case IsUnauthorized(err):
		return errors.New("the portal rejected the saved login (expired or revoked): run `sharetoken login` again")
	default:
		a.warn("device", "could not confirm the device binding: "+err.Error())
	}
	return nil
}

func (a *Agent) setupBuyer() error {
	b := buyer.New(a.h, a.keys, peer.AddrInfo{ID: a.coord})
	b.Evidence = a.cfg.Buy.Evidence
	if v := a.cfg.Buy.FirstByteSeconds; v > 0 {
		b.FirstByteTimeout = time.Duration(v) * time.Second
	}
	if v := a.cfg.Buy.IdleSeconds; v > 0 {
		b.IdleTimeout = time.Duration(v) * time.Second
	}
	switch q := a.cfg.Buy.QueueSeconds; {
	case q < 0:
		b.QueueWait = 0
	case q > 0:
		b.QueueWait = time.Duration(q) * time.Second
	}
	if a.cfg.Buy.TEEPolicy != "" {
		p, err := attest.LoadPolicy(a.cfg.Buy.TEEPolicy)
		if err != nil {
			return err
		}
		b.Attestation = &buyer.Attestation{Verifier: cli.TEEVerifier(a.cfg.Buy.TEEOnline), Policy: p}
	} else if a.cfg.Buy.RequireTEE {
		return errors.New("buy.require_tee needs buy.tee_policy: the list of node images you trust")
	}
	a.buyer = b
	a.proxy = &buyer.Proxy{Client: b, BudgetMicros: a.cfg.Buy.BudgetMicros, MaxPriceIn: a.cfg.Buy.MaxPriceIn,
		MaxPriceOut: a.cfg.Buy.MaxPriceOut, RequireTEE: a.cfg.Buy.RequireTEE}
	return nil
}

// relayAddrs is the candidate relay list: the configured override, else the
// portal's.
func (a *Agent) relayAddrs() []string {
	if len(a.cfg.Relays) > 0 {
		return slices.Clone(a.cfg.Relays)
	}
	out := make([]string, 0, len(a.netinfo.Relays))
	for _, r := range a.netinfo.Relays {
		out = append(out, r.Addr)
	}
	return out
}

func (a *Agent) coordinatorOK(addr string) bool {
	for _, r := range a.netinfo.Relays {
		if r.Addr == addr {
			return r.OK
		}
	}
	return len(a.cfg.Relays) > 0 // overrides carry no status: assume yes
}

// selectRelay measures every relay and routes coordinator traffic through
// the best one. It keeps the current relay unless it failed or another is
// clearly faster, so that small jitter does not cause flapping.
func (a *Agent) selectRelay(ctx context.Context, currentFailed bool) {
	a.mu.Lock()
	addrs := a.relayAddrs()
	a.mu.Unlock()
	res := p2p.MeasureRelays(ctx, a.h, addrs, 3)

	a.mu.Lock()
	defer a.mu.Unlock()
	a.relays, a.measured = res, time.Now()
	best := chooseRelay(res, a.current, currentFailed, a.coordinatorOK)
	if best == nil {
		a.warn("relay", "no relay is reachable; retrying")
		return
	}
	a.warn("relay", "")
	if best.Addr != a.current {
		a.log.Info("using relay", "relay", best.Addr, "rtt", best.RTT.Round(100*time.Microsecond))
		ps := a.h.Peerstore()
		ps.ClearAddrs(a.coord)
		ps.AddAddrs(a.coord, p2p.CircuitAddrs(best.Info()), peerstore.PermanentAddrTTL)
		if a.current != "" {
			if currentFailed {
				_ = a.h.Network().ClosePeer(a.coord) // reconnect through the new relay
			} else {
				// The old path still works: let requests in flight on it finish
				// (closing it at once fails them, and orphans any ticket being
				// issued), and move over once they are done.
				go a.closeOldCoordinatorConns(best.Info().ID, relaySwitchGrace)
			}
		}
		a.current = best.Addr
		a.link.Reset(best.Addr)
	}
	if a.share != nil {
		_ = a.share.reserver.SetRelays(a.shareRelaysLocked(a.share.cfg))
	}
}

// chooseRelay picks the relay for coordinator traffic from a measurement
// (best first): the fastest reachable relay, preferring those the coordinator
// is known to be reachable through. It keeps current unless current failed or
// the best is faster by more than max(5ms, 20%), so jitter causes no flapping.
func chooseRelay(res []p2p.RelayRTT, current string, currentFailed bool, coordinatorOK func(string) bool) *p2p.RelayRTT {
	var best *p2p.RelayRTT
	for _, wantOK := range []bool{true, false} {
		for i := range res {
			if res[i].OK() && (!wantOK || coordinatorOK(res[i].Addr)) {
				best = &res[i]
				break
			}
		}
		if best != nil {
			break
		}
	}
	if best == nil || current == "" || current == best.Addr || currentFailed {
		return best
	}
	for i := range res {
		if r := &res[i]; r.Addr == current && r.OK() {
			if best.RTT+max(5*time.Millisecond, r.RTT/5) >= r.RTT {
				return r // not clearly better: stay
			}
			break
		}
	}
	return best
}

// shareRelaysLocked picks the relays to hold reservations on: the best
// reachable ones, the coordinator's relay first.
func (a *Agent) shareRelaysLocked(sc ShareConfig) []string {
	out := []string{}
	if a.current != "" {
		out = append(out, a.current)
	}
	for _, r := range a.relays {
		if len(out) >= sc.Relays {
			break
		}
		if r.OK() && !slices.Contains(out, r.Addr) {
			out = append(out, r.Addr)
		}
	}
	return out
}

// CoordinatorReachable dials the coordinator through the current relay.
func (a *Agent) CoordinatorReachable(ctx context.Context) bool {
	if a.h.Network().Connectedness(a.coord) == network.Connected {
		return true
	}
	// After a few failed dials (coordinator restarting, relay down) libp2p
	// backs off from the peer for up to minutes and Connect fails without
	// dialing. This check runs only every HealthEvery, so clearing the backoff
	// costs nothing and lets the client reconnect as soon as the coordinator
	// is back.
	if sw, ok := a.h.Network().(*swarm.Swarm); ok {
		sw.Backoff().Clear(a.coord)
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return a.h.Connect(network.WithAllowLimitedConn(cctx, "sharetoken-ctrl"), peer.AddrInfo{ID: a.coord}) == nil
}

func (a *Agent) maintain() {
	health := time.NewTicker(a.HealthEvery)
	defer health.Stop()
	lastRefresh := time.Now()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-health.C:
		case <-a.kick:
		}
		if time.Since(lastRefresh) > a.RefreshEvery {
			lastRefresh = time.Now()
			if err := a.refreshNetwork(a.ctx); err != nil {
				a.log.Warn("refreshing the network failed", "err", err)
			}
			if err := a.bind(a.ctx); err != nil {
				a.mu.Lock()
				a.warn("login", err.Error())
				a.mu.Unlock()
			}
		}
		a.mu.Lock()
		stale := time.Since(a.measured) > a.MeasureEvery || a.current == ""
		current := a.current
		a.mu.Unlock()
		a.pingCurrent(current)
		if !a.CoordinatorReachable(a.ctx) {
			a.mu.Lock()
			a.warn("coordinator", "coordinator unreachable through "+a.current+"; switching relay")
			a.mu.Unlock()
			if err := a.refreshNetwork(a.ctx); err == nil { // the portal knows which relays work
				lastRefresh = time.Now()
			}
			a.selectRelay(a.ctx, true)
			continue
		}
		a.mu.Lock()
		a.warn("coordinator", "")
		a.mu.Unlock()
		if stale {
			a.selectRelay(a.ctx, false)
		}
	}
}

// relaySwitchGrace is how long the old coordinator connection is kept after
// switching to a better relay.
const relaySwitchGrace = 30 * time.Second

// closeOldCoordinatorConns closes, after grace, the coordinator connections
// that do not run through relay keep; new streams then dial the new relay.
func (a *Agent) closeOldCoordinatorConns(keep peer.ID, grace time.Duration) {
	select {
	case <-a.ctx.Done():
		return
	case <-time.After(grace):
	}
	for _, c := range a.h.Network().ConnsToPeer(a.coord) {
		if !strings.Contains(c.RemoteMultiaddr().String(), "/p2p/"+keep.String()+"/p2p-circuit") {
			_ = c.Close()
		}
	}
}

// linkWindow is how many health-check pings of the current relay are kept
// (5 minutes at the default 10 s interval).
const linkWindow = 30

// pingCurrent pings the relay carrying coordinator traffic once, for the
// live latency, jitter and loss shown in the status.
func (a *Agent) pingCurrent(relay string) {
	if relay == "" {
		return
	}
	ai, err := p2p.ParseRelay(relay)
	if err != nil {
		return
	}
	rtt, err := p2p.PingRelay(a.ctx, a.h, ai, min(5*time.Second, max(a.HealthEvery, time.Second)))
	if a.ctx.Err() != nil {
		return
	}
	a.link.Add(relay, rtt, err, time.Now())
}

// Remeasure re-measures the relays now.
func (a *Agent) Remeasure(ctx context.Context) []p2p.RelayRTT {
	a.selectRelay(ctx, false)
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.relays)
}

// ---- sharing ----

func (a *Agent) startShareLocked(sc ShareConfig) error {
	if err := sc.Validate(); err != nil {
		return err
	}
	if !sc.Enabled {
		return nil
	}
	if u, err := url.Parse(sc.Upstream); err != nil || u.Scheme == "" {
		return fmt.Errorf("bad upstream URL %q", sc.Upstream)
	} else if u.Scheme == "http" && !cli.IsLocalURL(sc.Upstream) {
		a.warn("upstream", "prompts travel unencrypted to "+sc.Upstream+"; use https or keep the model server local")
	}
	var apiKey string
	if sc.UpstreamKeyEnv != "" {
		apiKey = os.Getenv(sc.UpstreamKeyEnv)
	}
	ctx, cancel := context.WithCancel(a.ctx)
	st := &shareState{cfg: sc, cancel: cancel}
	st.reserver = p2p.NewReserver(a.h, func() {
		if st.node != nil {
			st.node.Announce() // advertise the new relay addresses at once
		}
	})
	st.reserver.Prepare = a.presentPass
	_ = st.reserver.SetRelays(a.shareRelaysLocked(sc))
	go st.reserver.Run(ctx)
	h := a.h
	svc, err := node.NewService(ctx, h, a.keys, node.Config{
		Coordinator:    peer.AddrInfo{ID: a.coord},
		Models:         sc.Models,
		Upstream:       &node.OpenAICompat{BaseURL: sc.Upstream, APIKey: apiKey},
		MaxConcurrency: sc.Concurrency,
		Quote:          a.quote,
		Addrs: func() []string {
			out := p2p.AddrStrings(h)
			for _, c := range st.reserver.CircuitAddrs() {
				out = append(out, c.String())
			}
			return out
		},
	})
	if err != nil {
		cancel()
		return err
	}
	st.node = svc
	a.share = st
	a.warn("share", "")
	for _, m := range sc.Models {
		a.log.Info("sharing", "model", m.ID, "upstream", m.UpstreamModel, "in", m.PriceInMicros, "out", m.PriceOutMicros)
	}
	return nil
}

// renewPassBefore fetches a new relay pass when the current one has less
// than this left (passes last an hour by default).
const renewPassBefore = 15 * time.Minute

// relayPass returns a valid relay pass from the portal, renewing it ahead of
// expiry. A nil pass with a nil error means the portal issues none (open
// relays).
func (a *Agent) relayPass(ctx context.Context) (*relaypass.Pass, error) {
	a.passMu.Lock()
	defer a.passMu.Unlock()
	if a.pass != nil && time.Until(a.pass.Expires()) > renewPassBefore {
		return a.pass, nil
	}
	p, err := a.portal.RelayPass(ctx, a.keys.ID())
	switch {
	case err == nil:
		a.pass = p
		return p, nil
	case isCode(err, "passes_disabled"):
		return nil, nil
	case a.pass != nil && time.Now().Before(a.pass.Expires()):
		return a.pass, nil // portal unreachable: keep using the pass until it expires
	}
	return nil, err
}

// presentPass shows the relay this device's pass before a reservation. It
// never fails the attempt: an open relay needs no pass, and a relay that
// requires one refuses the reservation itself.
func (a *Agent) presentPass(ctx context.Context, relay peer.AddrInfo) error {
	p, err := a.relayPass(ctx)
	if err != nil {
		a.mu.Lock()
		a.warn("relay-pass", "cannot get a relay pass from the portal: "+err.Error())
		a.mu.Unlock()
		return nil
	}
	a.mu.Lock()
	a.warn("relay-pass", "")
	a.mu.Unlock()
	if p == nil {
		return nil
	}
	if err := relaypass.Present(ctx, a.h, relay, p); err != nil {
		a.log.Debug("relay did not take the pass", "relay", relay.ID, "err", err)
	}
	return nil
}

func isCode(err error, code string) bool {
	pe, ok := err.(*PortalError)
	return ok && pe.Code == code
}

func (a *Agent) stopShareLocked() {
	if a.share == nil {
		return
	}
	a.share.node.Close()
	_ = a.share.reserver.SetRelays(nil)
	a.share.cancel()
	a.share = nil
}

// SetShare applies and saves new sharing settings.
func (a *Agent) SetShare(sc ShareConfig) error {
	if sc.Upstream == "" {
		sc.Upstream = DefaultUpstream
	}
	if sc.Concurrency <= 0 {
		sc.Concurrency = 2
	}
	if sc.Relays <= 0 {
		sc.Relays = 2
	}
	if sc.TEE == "" {
		sc.TEE = "off"
	}
	if err := sc.Validate(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if sc.Enabled && sc.TEE != "off" && a.quote == nil {
		return errors.New("TEE sharing must be configured before start-up (restart the client)")
	}
	a.stopShareLocked()
	a.cfg.Share = sc
	if err := a.startShareLocked(sc); err != nil {
		a.warn("share", err.Error())
		return err
	}
	return a.saveLocked()
}

// ReloadShare re-reads the sharing settings from the config file.
func (a *Agent) ReloadShare() error {
	c, err := LoadConfig(a.home)
	if err != nil {
		return err
	}
	return a.SetShare(c.Share)
}

func (a *Agent) saveLocked() error {
	// Keep fields other tools may have changed (e.g. a new login token).
	disk, err := LoadConfig(a.home)
	if err != nil {
		return err
	}
	disk.Share = a.cfg.Share
	return SaveConfig(a.home, disk)
}
