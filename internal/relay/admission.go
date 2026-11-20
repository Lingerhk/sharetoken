package relay

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	lproto "github.com/libp2p/go-libp2p/core/protocol"
	ma "github.com/multiformats/go-multiaddr"

	"sharetoken/internal/protocol"
	"sharetoken/internal/relaypass"
	"sharetoken/internal/version"
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
	// AllowAccounts, when not empty, makes the relay private: only devices of
	// these accounts of the network ("user:3") may reserve. The portal signs
	// the account into every pass, so a new device of an owner is admitted
	// without touching the relay, and a disabled user's passes run out.
	AllowAccounts []string
}

// Private reports whether only listed accounts may reserve.
func (n Network) Private() bool { return len(n.AllowAccounts) > 0 }

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
		for _, acct := range n.AllowAccounts {
			if acct == "" || len(acct) > 64 || strings.ContainsAny(acct, " \t\r\n") {
				errs = append(errs, fmt.Errorf("network %q: allow_accounts: %q is not an account (the portal shows yours, e.g. user:3)", n.Name, acct))
			}
		}
	}
	return errors.Join(errs...)
}

// tenant is a Network at run time.
type tenant struct {
	Network
	accepted atomic.Int64
	denied   atomic.Int64
	refused  atomic.Int64 // valid passes of accounts a private relay does not list

	keyMu sync.RWMutex
	added []ed25519.PublicKey // portal keys learned after start (AddPortalKey)
}

func (t *tenant) keys() []ed25519.PublicKey {
	t.keyMu.RLock()
	defer t.keyMu.RUnlock()
	return append(slices.Clone(t.PortalKeys), t.added...)
}

func (t *tenant) admits(account string) bool {
	return !t.Private() || slices.Contains(t.AllowAccounts, account)
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
	client  relaypass.Client // signed by the portal, for statistics only
	expires time.Time
}

// holder is a granted reservation.
type holder struct {
	tenant  *tenant
	account string // "" for an allow-listed peer, which no limit counts
	client  relaypass.Client
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
	if !ok || !now.Before(e.expires) || !e.tenant.admits(e.account) {
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
	a.reserved[p] = holder{tenant: e.tenant, account: e.account, client: e.client, until: now.Add(a.ttl)}
	return true
}

// AllowConnect implements relayv2.ACLFilter: connecting is not gated (see above).
func (a *admission) AllowConnect(peer.ID, ma.Multiaddr, peer.ID) bool { return true }

// verify finds the network whose portal signed p.
func (a *admission) verify(p *relaypass.Pass, remote string, now time.Time) (*tenant, error) {
	err := relaypass.ErrBadSignature
	for _, t := range a.tenants {
		if err = p.Verify(t.keys(), remote, now); err == nil {
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
	env, err := protocol.RecvStream(s, protocol.MaxSmallFrame)
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
	if !t.admits(p.Account) {
		// Said plainly, so that the client can tell its user why sharing
		// through this relay does not work.
		t.refused.Add(1)
		_ = protocol.SendError(s, protocol.Errorf(protocol.CodeForbidden, "this relay is private and does not list account %s", p.Account))
		return
	}
	a.mu.Lock()
	a.passes[remote] = admitted{tenant: t, account: p.Account, client: p.Client.Clean(), expires: p.Expires()}
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
	// Coordinators counts the allow-listed peers holding a live reservation. At
	// zero, this network's users cannot reach their coordinator through this relay.
	Coordinators         int `json:"coordinators"`
	CoordinatorsExpected int `json:"coordinators_expected"` // allow_peers configured
	MaxReservations      int `json:"max_reservations"`      // 0: only the relay's global limit
	// Private relays admit only AllowedAccounts accounts; PassesRefused
	// counts the valid passes of others.
	Private         bool  `json:"private"`
	AllowedAccounts int   `json:"allowed_accounts,omitempty"`
	PassesRefused   int64 `json:"passes_refused"`
	// Clients counts the devices holding a reservation by what their pass
	// says of their client (signed by the portal, as the client reported it).
	Clients []ClientCount `json:"clients,omitempty"`
	// ClientMismatches counts those whose libp2p identify agent says
	// otherwise: another release or platform than their pass, or none.
	ClientMismatches int `json:"client_mismatches"`
}

// ClientCount is how many devices run one client build.
type ClientCount struct {
	Version string `json:"version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	Devices int    `json:"devices"`
}

// maxClientKinds bounds the distinct builds listed per network (and of
// connected agents); the rest are counted under version "other".
const maxClientKinds = 64

// agentOf reads what a connected peer announced in libp2p's identify exchange.
func agentOf(h host.Host, p peer.ID) (version.Agent, bool) {
	v, err := h.Peerstore().Get(p, "AgentVersion")
	if err != nil {
		return version.Agent{}, false
	}
	s, _ := v.(string)
	return version.ParseAgent(s)
}

// countClient adds one device of build c to counts, keyed by build.
func countClient(counts map[relaypass.Client]int, c relaypass.Client) {
	if _, ok := counts[c]; !ok && len(counts) >= maxClientKinds {
		c = relaypass.Client{Version: "other"}
	}
	counts[c]++
}

func clientCounts(counts map[relaypass.Client]int) []ClientCount {
	out := make([]ClientCount, 0, len(counts))
	for c, n := range counts {
		out = append(out, ClientCount{Version: c.Version, OS: c.OS, Arch: c.Arch, Devices: n})
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Devices != b.Devices {
			return a.Devices > b.Devices
		}
		if a.Version != b.Version {
			return a.Version < b.Version
		}
		if a.OS != b.OS {
			return a.OS < b.OS
		}
		return a.Arch < b.Arch
	})
	return out
}

func (a *admission) networkStats() []NetworkStats {
	if a.open() {
		return nil
	}
	now := a.now()
	held, coordinators, mismatches := map[*tenant]int{}, map[*tenant]int{}, map[*tenant]int{}
	clients := map[*tenant]map[relaypass.Client]int{}
	a.mu.Lock()
	for p, h := range a.reserved {
		if now.Before(h.until) && a.h != nil && a.h.Network().Connectedness(p) == network.Connected {
			held[h.tenant]++
			if h.account == "" {
				coordinators[h.tenant]++
				continue
			}
			if clients[h.tenant] == nil {
				clients[h.tenant] = map[relaypass.Client]int{}
			}
			countClient(clients[h.tenant], h.client)
			if ag, ok := agentOf(a.h, p); !ok || ag.Program != "client" || ag.Version != h.client.Version || ag.OS != h.client.OS || ag.Arch != h.client.Arch {
				mismatches[h.tenant]++
			}
		}
	}
	a.mu.Unlock()
	out := make([]NetworkStats, 0, len(a.tenants))
	for _, t := range a.tenants {
		out = append(out, NetworkStats{Name: t.Name, Reservations: held[t], PassesAccepted: t.accepted.Load(), ReservationsDenied: t.denied.Load(),
			Coordinators: coordinators[t], CoordinatorsExpected: len(t.AllowPeers), MaxReservations: t.MaxReservations,
			Private: t.Private(), AllowedAccounts: len(t.AllowAccounts), PassesRefused: t.refused.Load(),
			Clients: clientCounts(clients[t]), ClientMismatches: mismatches[t]})
	}
	return out
}

const maxTrackedPasses = 100_000

// maxAddedKeys bounds the portal keys a network learns after start: a
// rotation adds one.
const maxAddedKeys = 4

// addPortalKey makes network name accept passes signed by key as well. It
// reports whether the key is new. A key another network uses is refused: a
// pass must belong to exactly one network.
func (a *admission) addPortalKey(name string, key ed25519.PublicKey) (bool, error) {
	var target *tenant
	for _, t := range a.tenants {
		known := slices.ContainsFunc(t.keys(), func(k ed25519.PublicKey) bool { return k.Equal(key) })
		switch {
		case t.Name == name && known:
			return false, nil
		case known:
			return false, fmt.Errorf("the key belongs to network %q", t.Name)
		case t.Name == name:
			target = t
		}
	}
	if target == nil {
		return false, fmt.Errorf("no network %q", name)
	}
	target.keyMu.Lock()
	defer target.keyMu.Unlock()
	if len(target.added) >= maxAddedKeys {
		target.added = target.added[1:]
	}
	target.added = append(target.added, key)
	return true, nil
}

func (a *admission) register(h host.Host) {
	a.mu.Lock()
	a.h = h
	a.mu.Unlock()
	h.SetStreamHandler(lproto.ID(relaypass.Protocol), a.handlePass)
}
