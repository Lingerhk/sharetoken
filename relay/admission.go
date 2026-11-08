package relay

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	lproto "github.com/libp2p/go-libp2p/core/protocol"
	ma "github.com/multiformats/go-multiaddr"

	"sharetoken/protocol"
	"sharetoken/relaypass"
)

// Network is one sharetoken deployment served by this relay (production, dev):
// its own portal, coordinator and users. One relay can serve several. They
// share its bandwidth and its global limits, but their accounts are separate
// namespaces, and a network with a reservation cap cannot crowd out the others.
//
// Sharing a relay does not join the networks. Traffic stays end-to-end
// encrypted, nodes accept only tickets from their own coordinator, and a
// coordinator refuses devices that are not bound to one of its users.
type Network struct {
	Name string
	// PortalKeys are the portal public keys whose passes admit a device to
	// this network (several during a key rotation).
	PortalKeys []ed25519.PublicKey
	// AllowPeers may reserve without a pass: the network's coordinator.
	AllowPeers []peer.ID
	// MaxDevicesPerAccount caps how many devices of one user account hold
	// reservations at once (default 4).
	MaxDevicesPerAccount int
	// MaxReservations caps the reservations of all this network's devices
	// together; AllowPeers do not count. 0: only the relay's global limit.
	MaxReservations int
}

// validateNetworks checks that the networks can be told apart: a pass or a peer must
// belong to exactly one.
func validateNetworks(nets []Network) error {
	var errs []error
	names := map[string]bool{}
	keys := map[string]string{}
	peers := map[peer.ID]string{}
	for i, n := range nets {
		if n.Name == "" {
			errs = append(errs, fmt.Errorf("network %d has no name", i))
		} else if names[n.Name] {
			errs = append(errs, fmt.Errorf("network %q is listed twice", n.Name))
		}
		names[n.Name] = true
		if len(n.PortalKeys) == 0 {
			errs = append(errs, fmt.Errorf("network %q has no portal key", n.Name))
		}
		for _, k := range n.PortalKeys {
			if other, dup := keys[string(k)]; dup {
				errs = append(errs, fmt.Errorf("networks %q and %q share a portal key", other, n.Name))
			}
			keys[string(k)] = n.Name
		}
		for _, p := range n.AllowPeers {
			if other, dup := peers[p]; dup {
				errs = append(errs, fmt.Errorf("networks %q and %q share the allowed peer %s", other, n.Name, p))
			}
			peers[p] = n.Name
		}
		if n.MaxDevicesPerAccount < 0 {
			errs = append(errs, fmt.Errorf("network %q: negative device limit", n.Name))
		}
		if n.MaxReservations < 0 {
			errs = append(errs, fmt.Errorf("network %q: negative reservation limit", n.Name))
		}
	}
	return errors.Join(errs...)
}

// tenant is a Network at run time.
type tenant struct {
	Network
	accepted atomic.Int64
	denied   atomic.Int64
}

// admission decides who may reserve. With no networks it admits everyone (an
// open relay). Otherwise a peer must be on a network's allow list (its
// coordinator) or have presented a valid pass of that network; device limits
// count within the network, so "user:3" of one network is not "user:3" of another.
//
// Only reservations are gated. Connecting through the relay to a peer that
// holds a reservation is not: every such peer is a registered user or a
// coordinator, and each authenticates whoever reaches it (a coordinator
// refuses unbound devices, nodes accept only tickets). The relay could not
// tell a buyer's network anyway: buyers hold no reservation and show no pass.
type admission struct {
	tenants []*tenant
	allow   map[peer.ID]*tenant
	h       host.Host
	now     func() time.Time

	ttl time.Duration // reservation lifetime

	mu       sync.Mutex
	passes   map[peer.ID]admitted
	reserved map[peer.ID]holder // granted reservations

	accepted, rejected atomic.Int64
}

type admitted struct {
	tenant  *tenant
	account string
	expires time.Time
}

// holder is a granted reservation.
type holder struct {
	tenant  *tenant
	account string // "" for an allow-listed peer, which no limit counts
	until   time.Time
}

func newAdmission(cfg Config) *admission {
	a := &admission{allow: map[peer.ID]*tenant{}, now: cfg.Now, ttl: cfg.ReservationTTL,
		passes: map[peer.ID]admitted{}, reserved: map[peer.ID]holder{}}
	for _, n := range cfg.Networks {
		if n.MaxDevicesPerAccount == 0 {
			n.MaxDevicesPerAccount = 4
		}
		t := &tenant{Network: n}
		a.tenants = append(a.tenants, t)
		for _, p := range n.AllowPeers {
			a.allow[p] = t
		}
	}
	return a
}

func (a *admission) open() bool { return len(a.tenants) == 0 }

// AllowReserve implements relayv2.ACLFilter; it runs on every reservation
// and renewal.
func (a *admission) AllowReserve(p peer.ID, _ ma.Multiaddr) bool {
	if a.open() {
		return true
	}
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if t, ok := a.allow[p]; ok {
		a.reserved[p] = holder{tenant: t, until: now.Add(a.ttl)}
		return true
	}
	if a.h == nil {
		return false // not started yet
	}
	e, ok := a.passes[p]
	if !ok || !now.Before(e.expires) {
		return false
	}
	// Count the other devices that hold a live reservation (the relay drops a
	// reservation when its peer disconnects).
	devices, total := 0, 0
	for other, h := range a.reserved {
		if other == p {
			continue
		}
		if !now.Before(h.until) || a.h.Network().Connectedness(other) != network.Connected {
			delete(a.reserved, other)
			continue
		}
		if h.tenant != e.tenant || h.account == "" {
			continue
		}
		total++
		if h.account == e.account {
			devices++
		}
	}
	if devices >= e.tenant.MaxDevicesPerAccount || (e.tenant.MaxReservations > 0 && total >= e.tenant.MaxReservations) {
		e.tenant.denied.Add(1)
		return false
	}
	a.reserved[p] = holder{tenant: e.tenant, account: e.account, until: now.Add(a.ttl)}
	return true
}

// AllowConnect implements relayv2.ACLFilter: connecting is not gated (see above).
func (a *admission) AllowConnect(peer.ID, ma.Multiaddr, peer.ID) bool { return true }

// verify finds the network whose portal signed p.
func (a *admission) verify(p *relaypass.Pass, remote string, now time.Time) (*tenant, error) {
	err := relaypass.ErrBadSignature
	for _, t := range a.tenants {
		if err = p.Verify(t.PortalKeys, remote, now); err == nil {
			return t, nil
		}
		if !errors.Is(err, relaypass.ErrBadSignature) {
			return nil, err // malformed, expired or another peer's: the same under any key
		}
	}
	return nil, err
}

// handlePass receives a device's pass and admits it until the pass expires.
func (a *admission) handlePass(s network.Stream) {
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(10 * time.Second))
	env, err := protocol.Recv(s)
	if err != nil {
		_ = s.Reset()
		return
	}
	var p relaypass.Pass
	if err := env.Decode(relaypass.TypePass, &p); err != nil {
		_ = protocol.SendError(s, protocol.Errorf(protocol.CodeBadRequest, "malformed pass"))
		return
	}
	remote := s.Conn().RemotePeer()
	now := a.now()
	t, err := a.verify(&p, remote.String(), now)
	if err != nil {
		a.rejected.Add(1)
		_ = protocol.SendError(s, protocol.Errorf(protocol.CodeUnauthorized, "%v", err))
		return
	}
	a.mu.Lock()
	a.passes[remote] = admitted{tenant: t, account: p.Account, expires: p.Expires()}
	if len(a.passes) > 4*maxTrackedPasses/5 {
		for id, e := range a.passes {
			if !now.Before(e.expires) {
				delete(a.passes, id)
			}
		}
	}
	a.mu.Unlock()
	a.accepted.Add(1)
	t.accepted.Add(1)
	_ = protocol.Send(s, relaypass.TypeOK, struct{}{})
}

// NetworkStats is one network's share of a relay.
type NetworkStats struct {
	Name               string `json:"name"`
	Reservations       int    `json:"reservations"` // held now, its coordinator's included
	PassesAccepted     int64  `json:"passes_accepted"`
	ReservationsDenied int64  `json:"reservations_denied"` // over this network's limits
}

func (a *admission) networkStats() []NetworkStats {
	if a.open() {
		return nil
	}
	now := a.now()
	held := map[*tenant]int{}
	a.mu.Lock()
	for p, h := range a.reserved {
		if now.Before(h.until) && a.h != nil && a.h.Network().Connectedness(p) == network.Connected {
			held[h.tenant]++
		}
	}
	a.mu.Unlock()
	out := make([]NetworkStats, 0, len(a.tenants))
	for _, t := range a.tenants {
		out = append(out, NetworkStats{Name: t.Name, Reservations: held[t], PassesAccepted: t.accepted.Load(), ReservationsDenied: t.denied.Load()})
	}
	return out
}

const maxTrackedPasses = 100_000

func (a *admission) register(h host.Host) {
	a.mu.Lock()
	a.h = h
	a.mu.Unlock()
	h.SetStreamHandler(lproto.ID(relaypass.Protocol), a.handlePass)
}
