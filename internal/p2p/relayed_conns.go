package p2p

import (
	"github.com/libp2p/go-libp2p/core/network"
	ma "github.com/multiformats/go-multiaddr"
)

// relayedUncounted is the resource manager of a host, except that a
// connection through a relay is not counted against the relay's IP address.
//
// libp2p limits connections per IP address (8 by default) and their arrival
// rate, and takes the IP from the first component of the remote address. A
// relayed connection's address starts with the relay's own address
// (/ip4/<relay>/.../p2p/<relay>/p2p-circuit), so every connection through one
// relay counted as coming from that relay: the coordinator, reachable only
// through relays, took at most that many users per relay address, and a
// seller behind NAT that many buyers per relay. Where the connection
// physically comes from is the relay's business; the relay limits circuits
// itself (per peer, per reservation, max_circuits).
//
// A relayed connection is therefore opened under the loopback address,
// which libp2p exempts from both the per-address limit and the rate limit
// (hubs keep that exemption in their own limiter too). Every other limit still
// applies: the system and transient scopes, the peer scope once the peer is
// known, and the connection manager. Direct connections are untouched.
type relayedUncounted struct {
	network.ResourceManager
}

// relayedEndpoint stands for every relayed connection: loopback, then the
// relay's peer ID and /p2p-circuit, so errors and logs still name the relay.
var relayedEndpoint = ma.StringCast("/ip6/::1")

func (r relayedUncounted) OpenConnection(dir network.Direction, usefd bool, endpoint ma.Multiaddr) (network.ConnManagementScope, error) {
	return r.ResourceManager.OpenConnection(dir, usefd, uncountedEndpoint(endpoint))
}

// uncountedEndpoint is the address a connection is opened under: a relayed
// address rewritten to start at loopback, any other address as it is.
func uncountedEndpoint(endpoint ma.Multiaddr) ma.Multiaddr {
	for i, c := range endpoint {
		if c.Protocol().Code != ma.P_CIRCUIT {
			continue
		}
		start := i // from the relay's /p2p component, when there is one
		if i > 0 && endpoint[i-1].Protocol().Code == ma.P_P2P {
			start = i - 1
		}
		return ma.Join(relayedEndpoint, endpoint[start:])
	}
	return endpoint
}
