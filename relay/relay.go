// Package relay is sharetoken's relay role: a public libp2p host that only
// forwards traffic (circuit relay v2). It keeps no state about users, money or
// jobs, and it cannot read what it forwards: libp2p's Noise handshake runs end
// to end between the two peers of a relayed connection.
//
// Every user reaches the coordinator through a relay, and users that share a
// model hold a reservation on one so buyers can reach them behind NAT. The
// network runs several relays; each user measures them and uses the one with
// the best round-trip time (p2p.MeasureRelays).
//
// The coordinator holds a reservation on every relay and receives many
// circuits through each, so the per-peer circuit limit and the reservation
// limits are far above libp2p's defaults (16 circuits per peer, 128
// reservations, 8 per IP), which suit hole-punch signalling only.
package relay

import (
	"fmt"
	"sort"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	pbv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/pb"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	ma "github.com/multiformats/go-multiaddr"

	"sharetoken/p2p"
)

// Config sizes a relay. Zero values get defaults.
type Config struct {
	Listen               []string
	MaxReservations      int           // default 4096
	MaxReservationsPerIP int           // default 64 (carrier-grade NAT puts many users behind one IP)
	MaxCircuits          int           // open circuits per destination peer; default 4096 (the coordinator)
	BufferSize           int           // per-circuit buffer; default 16 KiB
	ReservationTTL       time.Duration // default 1h
	MaxConns             int           // connection-manager high watermark; default 8192
	// Announce replaces the addresses the relay advertises, e.g. the public
	// address of a cloud VM whose interface only has a private IP.
	Announce []string

	// Networks are the deployments this relay serves. Empty: an open relay,
	// anyone may reserve.
	Networks []Network
	Now      func() time.Time // tests
}

func (c *Config) setDefaults() {
	if c.MaxReservations == 0 {
		c.MaxReservations = 4096
	}
	if c.MaxReservationsPerIP == 0 {
		c.MaxReservationsPerIP = 64
	}
	if c.MaxCircuits == 0 {
		c.MaxCircuits = 4096
	}
	if c.BufferSize == 0 {
		c.BufferSize = 16 << 10
	}
	if c.ReservationTTL == 0 {
		c.ReservationTTL = time.Hour
	}
	if c.MaxConns == 0 {
		c.MaxConns = 8192
	}
	if c.Now == nil {
		c.Now = time.Now
	}
}

// Stats counts what a relay has done since it started.
type Stats struct {
	Reservations        int64 `json:"reservations"`         // currently held
	ReservationsGranted int64 `json:"reservations_granted"` // including renewals
	ReservationsDenied  int64 `json:"reservations_denied"`
	Circuits            int64 `json:"circuits"` // currently open
	CircuitsOpened      int64 `json:"circuits_opened"`
	CircuitsDenied      int64 `json:"circuits_denied"`
	BytesRelayed        int64 `json:"bytes_relayed"`
	PassesAccepted      int64 `json:"passes_accepted"`
	PassesRejected      int64 `json:"passes_rejected"`
	// ServiceEnabled is whether the circuit relay service is running. The host
	// can be up with the service off, and then nobody can reserve.
	ServiceEnabled bool `json:"service_enabled"`
	// Networks splits what can be attributed. Circuits and bytes cannot: the
	// relay does not know which network a buyer belongs to.
	Networks []NetworkStats `json:"networks,omitempty"`
	// Agents counts the connected peers by what they announced in libp2p's
	// identify exchange: every sharetoken program, buyers that hold no
	// reservation included. Peers say what they like here; it is unsigned.
	Agents []AgentCount `json:"agents,omitempty"`
}

// AgentCount is how many connected peers announced one build.
type AgentCount struct {
	Program string `json:"program"` // client, coordinator, relay; "other" for anything not sharetoken
	Version string `json:"version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	Peers   int    `json:"peers"`
}

// agentCounts groups the connected peers by their identify agent.
func (r *Relay) agentCounts() []AgentCount {
	counts := map[AgentCount]int{}
	for _, p := range r.Host.Network().Peers() {
		k := AgentCount{Program: "other"}
		if ag, ok := agentOf(r.Host, p); ok {
			k = AgentCount{Program: ag.Program, Version: ag.Version, OS: ag.OS, Arch: ag.Arch}
		}
		if _, seen := counts[k]; !seen && len(counts) >= maxClientKinds {
			k = AgentCount{Program: "other"}
		}
		counts[k]++
	}
	out := make([]AgentCount, 0, len(counts))
	for k, n := range counts {
		k.Peers = n
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Peers != b.Peers {
			return a.Peers > b.Peers
		}
		return a.Program+"/"+a.Version+"/"+a.OS+"/"+a.Arch < b.Program+"/"+b.Version+"/"+b.OS+"/"+b.Arch
	})
	return out
}

// Relay is a running relay.
type Relay struct {
	Host  host.Host
	stats stats
	adm   *admission
	cfg   Config
}

// New starts a relay host for keys.
func New(keys *p2p.Keys, cfg Config) (*Relay, error) {
	cfg.setDefaults()
	if err := validateNetworks(cfg.Networks); err != nil {
		return nil, fmt.Errorf("relay: %w", err)
	}
	adm := newAdmission(cfg)
	r := &Relay{adm: adm, cfg: cfg}
	rc := relayv2.Resources{
		Limit:                  nil, // completions stream for minutes; no per-circuit time or data cap
		ReservationTTL:         cfg.ReservationTTL,
		MaxReservations:        cfg.MaxReservations,
		MaxCircuits:            cfg.MaxCircuits,
		BufferSize:             cfg.BufferSize,
		MaxReservationsPerPeer: 1,
		MaxReservationsPerIP:   cfg.MaxReservationsPerIP,
		MaxReservationsPerASN:  cfg.MaxReservations,
	}
	opts := []libp2p.Option{}
	if len(cfg.Announce) > 0 {
		var announce []ma.Multiaddr
		for _, a := range cfg.Announce {
			m, err := ma.NewMultiaddr(a)
			if err != nil {
				return nil, fmt.Errorf("relay: announce address %q: %w", a, err)
			}
			announce = append(announce, m)
		}
		opts = append(opts, libp2p.AddrsFactory(func([]ma.Multiaddr) []ma.Multiaddr { return announce }))
	}
	h, err := p2p.NewHost(keys, p2p.HostConfig{
		Program:  "relay",
		Listen:   cfg.Listen,
		MaxConns: cfg.MaxConns,
		Options: append(opts,
			// A relay is public by definition; the relay service only runs
			// once the host believes it is publicly reachable.
			libp2p.ForceReachabilityPublic(),
			// AutoNAT lets users learn whether they are reachable directly.
			libp2p.EnableNATService(),
			libp2p.EnableRelayService(relayv2.WithResources(rc), relayv2.WithMetricsTracer(&r.stats), relayv2.WithACL(adm)),
		),
	})
	if err != nil {
		return nil, err
	}
	r.Host = h
	adm.register(h)
	return r, nil
}

// ValidateNetworks reports a set of networks New would refuse.
func ValidateNetworks(nets []Network) error { return validateNetworks(nets) }

// Open reports whether the relay admits anyone (no networks configured).
func (r *Relay) Open() bool { return r.adm.open() }

// Close stops the relay.
func (r *Relay) Close() error { return r.Host.Close() }

// Addrs returns the relay's full multiaddrs (…/p2p/<id>), the form registered
// with the portal.
func (r *Relay) Addrs() []string { return p2p.FullAddrs(r.Host) }

// Stats returns the relay's counters.
func (r *Relay) Stats() Stats {
	s := r.stats.snapshot()
	s.PassesAccepted, s.PassesRejected = r.adm.accepted.Load(), r.adm.rejected.Load()
	s.Networks = r.adm.networkStats()
	s.Agents = r.agentCounts()
	return s
}

// stats implements relayv2.MetricsTracer with plain counters.
type stats struct {
	reservations, granted, rDenied, circuits, opened, cDenied, bytes atomic.Int64
	enabled                                                          atomic.Bool
}

func (s *stats) snapshot() Stats {
	return Stats{
		Reservations: s.reservations.Load(), ReservationsGranted: s.granted.Load(), ReservationsDenied: s.rDenied.Load(),
		Circuits: s.circuits.Load(), CircuitsOpened: s.opened.Load(), CircuitsDenied: s.cDenied.Load(), BytesRelayed: s.bytes.Load(),
		ServiceEnabled: s.enabled.Load(),
	}
}

func (s *stats) RelayStatus(enabled bool)       { s.enabled.Store(enabled) }
func (s *stats) ConnectionOpened()              { s.circuits.Add(1); s.opened.Add(1) }
func (s *stats) ConnectionClosed(time.Duration) { s.circuits.Add(-1) }
func (s *stats) BytesTransferred(n int)         { s.bytes.Add(int64(n)) }
func (s *stats) ReservationClosed(n int)        { s.reservations.Add(-int64(n)) }
func (s *stats) ConnectionRequestHandled(st pbv2.Status) {
	if st != pbv2.Status_OK {
		s.cDenied.Add(1)
	}
}
func (s *stats) ReservationAllowed(renewal bool) {
	s.granted.Add(1)
	if !renewal {
		s.reservations.Add(1)
	}
}
func (s *stats) ReservationRequestHandled(st pbv2.Status) {
	if st != pbv2.Status_OK {
		s.rDenied.Add(1)
	}
}
