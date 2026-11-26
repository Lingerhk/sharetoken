package relay

import (
	"net/netip"
	"sync"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	circuitproto "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/proto"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

// A circuit costs the relay memory and one of its destination's slots
// (MaxCircuits) for as long as its source keeps the stream open: libp2p's
// relay has no idle timeout, and a time or data limit would cut completions
// that stream for minutes. A source therefore holds only so many at a time:
// MaxCircuitsPerPeer one peer, and, peer IDs being free, MaxCircuitsPerIP all
// peers of one address together. A user holds one circuit to the coordinator
// and one to each node it is buying from; an office behind one address is at
// most HubAddrConns connections. Without these, one peer filled the 64
// streams libp2p let a relay open to the coordinator, and after that limit
// was raised could have filled max_circuits.
//
// The relay's coordinators are not counted: they are trusted with far more
// (a reservation without a pass).

// AllowConnect implements relayv2.ACLFilter; it runs on every request for a
// circuit. A circuit is the stream it was asked on, so the circuits a source
// holds are the hop streams it has open, this request's among them (and, for
// the moment it takes, a reservation being renewed).
func (a *admission) AllowConnect(src peer.ID, srcAddr ma.Multiaddr, _ peer.ID) bool {
	a.mu.Lock()
	h := a.h
	a.mu.Unlock()
	if h == nil || a.allow[src] != nil {
		return true
	}
	if n := hops(h.Network().ConnsToPeer(src)); n > a.perPeer {
		return false
	}
	key, ok := addrKey(srcAddr)
	return !ok || hops(a.sources.conns(key)) <= a.perAddr
}

// hops counts the streams on conns that ask the relay for something.
func hops(conns []network.Conn) int {
	n := 0
	for _, c := range conns {
		for _, s := range c.GetStreams() {
			if s.Stat().Direction == network.DirInbound && s.Protocol() == circuitproto.ProtoIDv2Hop {
				n++
			}
		}
	}
	return n
}

// addrKey is what the resource manager counts connections by (p2p.HubAddrConns):
// an IPv4 address, an IPv6 /56.
func addrKey(addr ma.Multiaddr) (netip.Prefix, bool) {
	ip, err := manet.ToIP(addr)
	if err != nil {
		return netip.Prefix{}, false
	}
	ap, ok := netip.AddrFromSlice(ip)
	if !ok {
		return netip.Prefix{}, false
	}
	ap = ap.Unmap()
	bits := 32
	if ap.Is6() {
		bits = 56
	}
	p, err := ap.Prefix(bits)
	return p, err == nil
}

// sources keeps the open connections by address, so that counting an
// address's circuits does not walk every connection of the relay.
type sources struct {
	mu     sync.Mutex
	byAddr map[netip.Prefix]map[network.Conn]struct{}
}

func (s *sources) watch(h host.Host) {
	h.Network().Notify(&network.NotifyBundle{
		ConnectedF: func(_ network.Network, c network.Conn) { s.add(c) },
		DisconnectedF: func(_ network.Network, c network.Conn) {
			key, ok := addrKey(c.RemoteMultiaddr())
			if !ok {
				return
			}
			s.mu.Lock()
			defer s.mu.Unlock()
			delete(s.byAddr[key], c)
			if len(s.byAddr[key]) == 0 {
				delete(s.byAddr, key)
			}
		},
	})
	// Those that arrived while the host was starting.
	for _, c := range h.Network().Conns() {
		s.add(c)
	}
}

func (s *sources) add(c network.Conn) {
	key, ok := addrKey(c.RemoteMultiaddr())
	if !ok || c.IsClosed() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.byAddr == nil {
		s.byAddr = map[netip.Prefix]map[network.Conn]struct{}{}
	}
	if s.byAddr[key] == nil {
		s.byAddr[key] = map[network.Conn]struct{}{}
	}
	s.byAddr[key][c] = struct{}{}
}

func (s *sources) conns(key netip.Prefix) []network.Conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]network.Conn, 0, len(s.byAddr[key]))
	for c := range s.byAddr[key] {
		out = append(out, c)
	}
	return out
}
