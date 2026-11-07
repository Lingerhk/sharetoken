package relay

import (
	"crypto/ed25519"
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

// admission decides who may reserve. With no portal keys it admits everyone
// (an open relay). Otherwise a peer must be on the allow list (the
// coordinator) or have presented a valid pass, and one user account may hold
// reservations from at most maxPerAccount devices at once.
//
// Only reservations are gated. Connecting through the relay to a peer that
// holds a reservation is not: every such peer is a registered user or the
// coordinator, and each authenticates whoever reaches it (the coordinator
// refuses unbound devices, nodes accept only tickets).
type admission struct {
	keys          []ed25519.PublicKey
	allow         map[peer.ID]bool
	maxPerAccount int
	h             host.Host
	now           func() time.Time

	ttl time.Duration // reservation lifetime

	mu       sync.Mutex
	passes   map[peer.ID]admitted
	reserved map[peer.ID]time.Time // granted reservations and their expiry

	accepted, rejected atomic.Int64
}

type admitted struct {
	account string
	expires time.Time
}

func (a *admission) open() bool { return len(a.keys) == 0 }

// AllowReserve implements relayv2.ACLFilter; it runs on every reservation
// and renewal.
func (a *admission) AllowReserve(p peer.ID, _ ma.Multiaddr) bool {
	if a.open() || a.allow[p] {
		return true
	}
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.h == nil {
		return false // not started yet
	}
	e, ok := a.passes[p]
	if !ok || !now.Before(e.expires) {
		return false
	}
	// Count the account's other devices that hold a live reservation (the
	// relay drops a reservation when its peer disconnects).
	n := 0
	for other, until := range a.reserved {
		if other == p {
			continue
		}
		if !now.Before(until) || a.h.Network().Connectedness(other) != network.Connected {
			delete(a.reserved, other)
			continue
		}
		if a.passes[other].account == e.account {
			n++
		}
	}
	if n >= a.maxPerAccount {
		return false
	}
	a.reserved[p] = now.Add(a.ttl)
	return true
}

// AllowConnect implements relayv2.ACLFilter: connecting is not gated (see above).
func (a *admission) AllowConnect(peer.ID, ma.Multiaddr, peer.ID) bool { return true }

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
	if err := p.Verify(a.keys, remote.String(), now); err != nil {
		a.rejected.Add(1)
		_ = protocol.SendError(s, protocol.Errorf(protocol.CodeUnauthorized, "%v", err))
		return
	}
	a.mu.Lock()
	a.passes[remote] = admitted{account: p.Account, expires: p.Expires()}
	if len(a.passes) > 4*maxTrackedPasses/5 {
		for id, e := range a.passes {
			if !now.Before(e.expires) {
				delete(a.passes, id)
			}
		}
	}
	a.mu.Unlock()
	a.accepted.Add(1)
	_ = protocol.Send(s, relaypass.TypeOK, struct{}{})
}

const maxTrackedPasses = 100_000

func (a *admission) register(h host.Host) {
	a.mu.Lock()
	a.h = h
	a.mu.Unlock()
	h.SetStreamHandler(lproto.ID(relaypass.Protocol), a.handlePass)
}
