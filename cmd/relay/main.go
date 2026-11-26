// Command relay runs a sharetoken relay: a public host that only forwards
// encrypted libp2p traffic between users, and between users and the
// coordinator. It keeps no accounts and sees no plaintext.
//
//	relay -config /etc/sharetoken/relay.yaml          (configs/relay.example.yaml)
//
// The file's access.networks lists the deployments the relay serves, each with
// its portal key and coordinator. Without any, the relay is open.
//
//	relay init -portal https://sharetoken.app -account user:3 -out relay.yaml
//
// writes the configuration of a private relay: one that serves only the
// devices of the accounts named (private.go).
//
// Register the printed address with the portal (portal relay add <addr>);
// the coordinator then reserves a slot on it and users start measuring it.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.yaml.in/yaml/v3"

	"sharetoken/internal/config"
	"sharetoken/internal/p2p"
	"sharetoken/internal/relay"
	"sharetoken/internal/relaypass"
	"sharetoken/internal/version"
)

// File is relay.yaml (configs/relay.example.yaml).
type File struct {
	Version  int `yaml:"version"`
	Identity struct {
		KeyFile string `yaml:"key_file"`
	} `yaml:"identity"`
	Network struct {
		Listen   []string `yaml:"listen"`
		Announce []string `yaml:"announce"`
		MaxConns int      `yaml:"max_conns"`
	} `yaml:"network"`
	Access struct {
		Networks []NetworkFile `yaml:"networks"`
	} `yaml:"access"`
	Limits struct {
		MaxReservations      int             `yaml:"max_reservations"`
		MaxReservationsPerIP int             `yaml:"max_reservations_per_ip"`
		MaxCircuits          int             `yaml:"max_circuits"`
		MaxCircuitsPerPeer   int             `yaml:"max_circuits_per_peer"`
		MaxCircuitsPerIP     int             `yaml:"max_circuits_per_ip"`
		ReservationTTL       config.Duration `yaml:"reservation_ttl"`
		CircuitBuffer        config.Size     `yaml:"circuit_buffer"`
		// Memory caps what peers may reserve with libp2p's resource manager
		// (0: three quarters of the machine's or the cgroup's memory).
		Memory config.Size `yaml:"memory"`
	} `yaml:"limits"`
	Status struct {
		Listen        string          `yaml:"listen"`
		StatsInterval config.Duration `yaml:"stats_interval"`
	} `yaml:"status"`
	Log config.Log `yaml:"log"`
}

// NetworkFile is one entry of access.networks.
type NetworkFile struct {
	Name              string   `yaml:"name"`
	PortalKeys        []string `yaml:"portal_keys"`
	AllowPeers        []string `yaml:"allow_peers"`
	MaxDevicesPerUser int      `yaml:"max_devices_per_user"`
	MaxReservations   int      `yaml:"max_reservations"` // 0: only limits.max_reservations
	// AllowAccounts makes the relay private to these accounts (private.go).
	AllowAccounts []string `yaml:"allow_accounts"`
	// PortalURL, if set, is where the relay reads the network's pass key,
	// in addition to PortalKeys.
	PortalURL string `yaml:"portal_url"`
}

// checkYAML decodes a configuration held in memory the way config.Load
// decodes a file.
func checkYAML(text string, f *File) error {
	*f = defaults()
	dec := yaml.NewDecoder(strings.NewReader(text))
	dec.KnownFields(true)
	return dec.Decode(f)
}

func defaults() File {
	var f File
	f.Version = config.Version
	f.Identity.KeyFile = "relay.key"
	f.Network.Listen = []string{"/ip4/0.0.0.0/tcp/4001", "/ip4/0.0.0.0/udp/4001/quic-v1", "/ip6/::/tcp/4001", "/ip6/::/udp/4001/quic-v1"}
	f.Network.MaxConns = 8192
	f.Limits.MaxReservations, f.Limits.MaxReservationsPerIP, f.Limits.MaxCircuits = 4096, 64, 4096
	f.Limits.MaxCircuitsPerPeer, f.Limits.MaxCircuitsPerIP = 32, 128
	f.Limits.ReservationTTL, f.Limits.CircuitBuffer = config.D("1h"), 16<<10
	f.Status.StatsInterval = config.D("5m")
	f.Log = config.DefaultLog()
	return f
}

// load builds the settings: defaults, then the -config file, then explicit flags.
func load(args []string) (File, bool, error) {
	f, check, _, err := loadArgs(args)
	return f, check, err
}

// loadArgs is load, and also reports whether -print-id was given.
func loadArgs(args []string) (File, bool, bool, error) {
	f := defaults()
	if path := config.ConfigPath(args); path != "" {
		if err := config.Load(path, &f); err != nil {
			return f, false, false, err
		}
		config.Resolve(config.Dir(path), &f.Identity.KeyFile, &f.Log.File)
	}
	fs := flag.NewFlagSet("relay", flag.ExitOnError)
	fl := config.Flags{FlagSet: fs}
	fs.String("config", "", "YAML configuration file (configs/relay.example.yaml); flags given explicitly override it")
	check := fs.Bool("check-config", false, "validate the configuration, print the effective settings and exit")
	printID := fs.Bool("print-id", false, "print the peer ID of the identity key (created if missing) and exit; a portal registers the relay by it")
	fs.StringVar(&f.Identity.KeyFile, "key", f.Identity.KeyFile, "identity seed file (created with mode 0600 if missing); keep it: the address users know includes the peer ID")
	fl.List(&f.Network.Listen, "listen", "comma-separated listen multiaddrs (IPv6 is skipped on hosts without it)")
	fl.List(&f.Network.Announce, "announce", "comma-separated public multiaddrs to advertise instead of the interface addresses (/dns4/<name>/..., or /ip4/ behind 1:1 NAT)")
	fs.StringVar(&f.Status.Listen, "status", f.Status.Listen, "serve JSON stats at http://<addr>/ and Prometheus metrics at /metrics; a loopback address such as 127.0.0.1:8093")
	fs.IntVar(&f.Limits.MaxReservations, "max-reservations", f.Limits.MaxReservations, "maximum reservations held at once")
	fs.IntVar(&f.Limits.MaxReservationsPerIP, "max-reservations-per-ip", f.Limits.MaxReservationsPerIP, "maximum reservations from one IP address")
	fs.IntVar(&f.Limits.MaxCircuits, "max-circuits", f.Limits.MaxCircuits, "maximum open circuits to one destination peer (the coordinator receives one per online user)")
	fs.IntVar(&f.Network.MaxConns, "max-conns", f.Network.MaxConns, "connection-manager high watermark")
	if err := fs.Parse(args); err != nil {
		return f, false, false, err
	}
	return f, *check, *printID, nil
}

// relayConfig validates f and converts it.
func relayConfig(f File) (relay.Config, error) {
	cfg := relay.Config{
		Listen: f.Network.Listen, Announce: f.Network.Announce,
		MaxReservations: f.Limits.MaxReservations, MaxReservationsPerIP: f.Limits.MaxReservationsPerIP,
		MaxCircuits: f.Limits.MaxCircuits, MaxConns: f.Network.MaxConns,
		MaxCircuitsPerPeer: f.Limits.MaxCircuitsPerPeer, MaxCircuitsPerIP: f.Limits.MaxCircuitsPerIP,
		ReservationTTL: f.Limits.ReservationTTL.T(), BufferSize: int(f.Limits.CircuitBuffer),
		Memory: int64(f.Limits.Memory),
	}
	var errs []error
	if len(cfg.Listen) == 0 {
		errs = append(errs, errors.New("network.listen is empty"))
	}
	for _, a := range append(append([]string{}, cfg.Listen...), cfg.Announce...) {
		if _, err := ma.NewMultiaddr(a); err != nil {
			errs = append(errs, fmt.Errorf("address %q: %w", a, err))
		}
	}
	for _, nf := range f.Access.Networks {
		n := relay.Network{Name: nf.Name, MaxDevicesPerAccount: nf.MaxDevicesPerUser, MaxReservations: nf.MaxReservations,
			AllowAccounts: nf.AllowAccounts}
		if nf.PortalURL != "" {
			if u, err := url.Parse(nf.PortalURL); err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Host))) {
				errs = append(errs, fmt.Errorf("access.networks %s: portal_url %q must be an https URL", nf.Name, nf.PortalURL))
			}
		}
		for _, k := range nf.PortalKeys {
			pub, err := relaypass.ParseKey(k)
			if err != nil {
				errs = append(errs, fmt.Errorf("access.networks %s: portal_keys: %w", nf.Name, err))
				continue
			}
			n.PortalKeys = append(n.PortalKeys, ed25519.PublicKey(pub))
		}
		for _, a := range nf.AllowPeers {
			id, err := peer.Decode(a)
			if err != nil {
				errs = append(errs, fmt.Errorf("access.networks %s: allow_peers %s: %w", nf.Name, a, err))
				continue
			}
			n.AllowPeers = append(n.AllowPeers, id)
		}
		cfg.Networks = append(cfg.Networks, n)
	}
	if len(errs) == 0 {
		if err := relay.ValidateNetworks(cfg.Networks); err != nil {
			errs = append(errs, fmt.Errorf("access.networks: %w", err))
		}
	}
	for name, v := range map[string]int{"limits.max_reservations": cfg.MaxReservations, "limits.max_reservations_per_ip": cfg.MaxReservationsPerIP,
		"limits.max_circuits": cfg.MaxCircuits, "network.max_conns": cfg.MaxConns,
		"limits.max_circuits_per_peer": cfg.MaxCircuitsPerPeer, "limits.max_circuits_per_ip": cfg.MaxCircuitsPerIP,
		"limits.circuit_buffer": cfg.BufferSize} {
		if v <= 0 {
			errs = append(errs, fmt.Errorf("%s must be positive", name))
		}
	}
	if cfg.ReservationTTL < time.Minute {
		errs = append(errs, errors.New("limits.reservation_ttl must be at least 1m"))
	}
	if f.Status.Listen != "" && !isLoopback(f.Status.Listen) {
		errs = append(errs, fmt.Errorf("status.listen %q must be a loopback address such as 127.0.0.1:8093: stats and metrics have no authentication", f.Status.Listen))
	}
	return cfg, errors.Join(errs...)
}

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "version" || os.Args[1] == "-version") {
		fmt.Println(version.String("relay"))
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "init" {
		if err := initPrivate(os.Args[2:], os.Stdout); err != nil {
			log.Fatal(err)
		}
		return
	}
	f, check, printID, err := loadArgs(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
	if printID {
		keys, err := p2p.LoadOrCreateKeys(f.Identity.KeyFile)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(keys.ID())
		return
	}
	cfg, err := relayConfig(f)
	if err != nil {
		log.Fatalf("configuration:\n%v", err)
	}
	if check {
		_ = yaml.NewEncoder(os.Stdout).Encode(f)
		return
	}
	// The resource manager may let peers reserve more than a quarter of the
	// memory (relay.Config.Memory); the garbage collector then works harder
	// near the machine's limit instead of the kernel killing the relay.
	if os.Getenv("GOMEMLIMIT") == "" {
		if m := p2p.MachineMemory(); m > 0 {
			debug.SetMemoryLimit(int64(m / 100 * 85))
		}
	}
	_, closeLog, err := f.Log.Setup()
	if err != nil {
		log.Fatal(err)
	}
	defer closeLog()
	log.Print(version.String("relay"))
	if len(cfg.Networks) == 0 {
		log.Print("WARNING: no access.networks: open relay, anyone may reserve; list the portal's key to admit only registered users")
	}
	private := len(cfg.Networks) > 0
	for _, n := range cfg.Networks {
		private = private && n.Private()
		switch {
		case n.Private() && len(n.AllowPeers) > 0:
			log.Printf("WARNING: network %s lists allow_accounts and allow_peers: a private relay should not carry a coordinator, which serves every user", n.Name)
		case n.Private():
			log.Printf("network %s: private, for %d account(s)", n.Name, len(n.AllowAccounts))
		case len(n.AllowPeers) == 0:
			log.Printf("WARNING: network %s has no allow_peers: its coordinator cannot reserve here, so its users cannot reach it through this relay", n.Name)
		}
	}

	keys, err := p2p.LoadOrCreateKeys(f.Identity.KeyFile)
	if err != nil {
		log.Fatal(err)
	}
	r, err := relay.New(keys, cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer r.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Println("relay", keys.ID())
	for _, a := range r.Addrs() {
		fmt.Println("  ", a)
	}
	if private {
		fmt.Printf("add it to the devices that share through it:\n   sharetoken relays add '%s'\n", registerAddr(r.Host.ID(), r.Host.Addrs()))
	} else {
		fmt.Printf("register it with:\n   portal relay add '%s' [name] [region]\n", registerAddr(r.Host.ID(), r.Host.Addrs()))
	}
	go refreshKeys(ctx, r, f.Access.Networks)
	if !hasIPv6(r.Host.Addrs()) {
		log.Print("note: no IPv6 address; IPv6-only users cannot reach this relay")
	}

	if f.Status.Listen != "" {
		srv := &http.Server{Addr: f.Status.Listen, ReadHeaderTimeout: 5 * time.Second, Handler: statusHandler(r)}
		go func() {
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("status server: %v", err)
			}
		}()
		defer srv.Close()
	}
	var tick <-chan time.Time
	if f.Status.StatsInterval > 0 {
		t := time.NewTicker(f.Status.StatsInterval.T())
		defer t.Stop()
		tick = t.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
			st := r.Stats()
			log.Printf("peers=%d reservations=%d circuits=%d relayed=%dB denied(res/circ)=%d/%d passes(ok/bad)=%d/%d networks=%+v",
				len(r.Host.Network().Peers()), st.Reservations, st.Circuits, st.BytesRelayed, st.ReservationsDenied, st.CircuitsDenied,
				st.PassesAccepted, st.PassesRejected, st.Networks)
		}
	}
}

// isLoopback reports whether a host:port listen address only accepts local connections.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// statusHandler serves Prometheus metrics at /metrics and JSON stats elsewhere.
// The metrics are the relay's own (sharetoken_relay_*) plus what the default
// registry holds: libp2p's swarm metrics and the Go and process collectors.
func statusHandler(r *relay.Relay) http.Handler {
	reg := prometheus.NewRegistry()
	reg.MustRegister(r.Collector())
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(prometheus.Gatherers{reg, prometheus.DefaultGatherer}, promhttp.HandlerOpts{}))
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": version.Version, "peer": r.Host.ID(), "addrs": r.Addrs(), "peers": len(r.Host.Network().Peers()), "stats": r.Stats(),
		})
	})
	return mux
}

// registerAddr is the relay's address for `portal relay add`: for IPv4 and
// for IPv6, the public addresses if there are any, else the private ones;
// loopback only when there is nothing else (a local test). Behind 1:1 NAT pass
// -announce, and the hint is exact. Announced host names (/dns4, /dns6,
// /dns) win over IPs.
func registerAddr(id peer.ID, addrs []ma.Multiaddr) string {
	var pick []ma.Multiaddr
	for _, a := range addrs { // announced host names: register those, not IPs
		if isDNS(a) {
			pick = append(pick, a)
		}
	}
	if len(pick) > 0 {
		return p2p.RelayAddr(peer.AddrInfo{ID: id, Addrs: pick})
	}
	for _, fam := range []int{ma.P_IP4, ma.P_IP6} {
		var public, private []ma.Multiaddr
		for _, a := range addrs {
			if _, err := a.ValueForProtocol(fam); err != nil || manet.IsIPLoopback(a) || manet.IsIP6LinkLocal(a) {
				continue
			}
			if manet.IsPublicAddr(a) {
				public = append(public, a)
			} else {
				private = append(private, a)
			}
		}
		if len(public) > 0 {
			pick = append(pick, public...)
		} else {
			pick = append(pick, private...)
		}
	}
	if len(pick) == 0 {
		pick = addrs
	}
	return p2p.RelayAddr(peer.AddrInfo{ID: id, Addrs: pick})
}

func isDNS(a ma.Multiaddr) bool {
	for _, p := range []int{ma.P_DNS, ma.P_DNS4, ma.P_DNS6} {
		if _, err := a.ValueForProtocol(p); err == nil {
			return true
		}
	}
	return false
}

func hasIPv6(addrs []ma.Multiaddr) bool {
	for _, a := range addrs {
		if _, err := a.ValueForProtocol(ma.P_DNS6); err == nil {
			return true
		}
		if _, err := a.ValueForProtocol(ma.P_IP6); err == nil && !manet.IsIP6LinkLocal(a) {
			return true
		}
	}
	return false
}
