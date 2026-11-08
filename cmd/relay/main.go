// Command relay runs a sharetoken relay: a public host that only forwards
// encrypted libp2p traffic between users, and between users and the
// coordinator. It keeps no accounts and sees no plaintext.
//
//	relay -config /etc/sharetoken/relay.yaml          (configs/relay.example.yaml)
//
// The file's access.networks lists the deployments the relay serves, each with
// its portal key and coordinator. Without any, the relay is open.
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
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"go.yaml.in/yaml/v3"

	"sharetoken/internal/config"
	"sharetoken/p2p"
	"sharetoken/relay"
	"sharetoken/relaypass"
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
		ReservationTTL       config.Duration `yaml:"reservation_ttl"`
		CircuitBuffer        config.Size     `yaml:"circuit_buffer"`
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
}

func defaults() File {
	var f File
	f.Version = config.Version
	f.Identity.KeyFile = "relay.key"
	f.Network.Listen = []string{"/ip4/0.0.0.0/tcp/4001", "/ip4/0.0.0.0/udp/4001/quic-v1", "/ip6/::/tcp/4001", "/ip6/::/udp/4001/quic-v1"}
	f.Network.MaxConns = 8192
	f.Limits.MaxReservations, f.Limits.MaxReservationsPerIP, f.Limits.MaxCircuits = 4096, 64, 4096
	f.Limits.ReservationTTL, f.Limits.CircuitBuffer = config.D("1h"), 16<<10
	f.Status.StatsInterval = config.D("5m")
	f.Log = config.DefaultLog()
	return f
}

// load builds the settings: defaults, then the -config file, then explicit flags.
func load(args []string) (File, bool, error) {
	f := defaults()
	if path := config.ConfigPath(args); path != "" {
		if err := config.Load(path, &f); err != nil {
			return f, false, err
		}
		config.Resolve(config.Dir(path), &f.Identity.KeyFile, &f.Log.File)
	}
	fs := flag.NewFlagSet("relay", flag.ExitOnError)
	fl := config.Flags{FlagSet: fs}
	fs.String("config", "", "YAML configuration file (configs/relay.example.yaml); flags given explicitly override it")
	check := fs.Bool("check-config", false, "validate the configuration, print the effective settings and exit")
	fs.StringVar(&f.Identity.KeyFile, "key", f.Identity.KeyFile, "identity seed file (created with mode 0600 if missing); keep it: the address users know includes the peer ID")
	fl.List(&f.Network.Listen, "listen", "comma-separated listen multiaddrs (IPv6 is skipped on hosts without it)")
	fl.List(&f.Network.Announce, "announce", "comma-separated public multiaddrs to advertise instead of the interface addresses (/dns4/<name>/..., or /ip4/ behind 1:1 NAT)")
	fs.StringVar(&f.Status.Listen, "status", f.Status.Listen, "serve JSON stats at http://<addr>/ (e.g. 127.0.0.1:8093; keep it private)")
	fs.IntVar(&f.Limits.MaxReservations, "max-reservations", f.Limits.MaxReservations, "maximum reservations held at once")
	fs.IntVar(&f.Limits.MaxReservationsPerIP, "max-reservations-per-ip", f.Limits.MaxReservationsPerIP, "maximum reservations from one IP address")
	fs.IntVar(&f.Limits.MaxCircuits, "max-circuits", f.Limits.MaxCircuits, "maximum open circuits to one destination peer (the coordinator receives one per online user)")
	fs.IntVar(&f.Network.MaxConns, "max-conns", f.Network.MaxConns, "connection-manager high watermark")
	if err := fs.Parse(args); err != nil {
		return f, false, err
	}
	return f, *check, nil
}

// relayConfig validates f and converts it.
func relayConfig(f File) (relay.Config, error) {
	cfg := relay.Config{
		Listen: f.Network.Listen, Announce: f.Network.Announce,
		MaxReservations: f.Limits.MaxReservations, MaxReservationsPerIP: f.Limits.MaxReservationsPerIP,
		MaxCircuits: f.Limits.MaxCircuits, MaxConns: f.Network.MaxConns,
		ReservationTTL: f.Limits.ReservationTTL.T(), BufferSize: int(f.Limits.CircuitBuffer),
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
		n := relay.Network{Name: nf.Name, MaxDevicesPerAccount: nf.MaxDevicesPerUser, MaxReservations: nf.MaxReservations}
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
		"limits.circuit_buffer": cfg.BufferSize} {
		if v <= 0 {
			errs = append(errs, fmt.Errorf("%s must be positive", name))
		}
	}
	if cfg.ReservationTTL < time.Minute {
		errs = append(errs, errors.New("limits.reservation_ttl must be at least 1m"))
	}
	return cfg, errors.Join(errs...)
}

func main() {
	f, check, err := load(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
	cfg, err := relayConfig(f)
	if err != nil {
		log.Fatalf("configuration:\n%v", err)
	}
	if check {
		_ = yaml.NewEncoder(os.Stdout).Encode(f)
		return
	}
	_, closeLog, err := f.Log.Setup()
	if err != nil {
		log.Fatal(err)
	}
	defer closeLog()
	if len(cfg.Networks) == 0 {
		log.Print("WARNING: no access.networks: open relay, anyone may reserve; list the portal's key to admit only registered users")
	}
	for _, n := range cfg.Networks {
		if len(n.AllowPeers) == 0 {
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
	fmt.Printf("register it with:\n   portal relay add '%s' [name] [region]\n", registerAddr(r.Host.ID(), r.Host.Addrs()))
	if !hasIPv6(r.Host.Addrs()) {
		log.Print("note: no IPv6 address; IPv6-only users cannot reach this relay")
	}

	if f.Status.Listen != "" {
		srv := &http.Server{Addr: f.Status.Listen, ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"peer": keys.ID(), "addrs": r.Addrs(), "peers": len(r.Host.Network().Peers()), "stats": r.Stats(),
			})
		})}
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
