package buyer

import (
	ma "github.com/multiformats/go-multiaddr"

	"sharetoken/internal/protocol"
)

// dialAddrs are those of a ticket's node addresses this client dials: TCP
// and QUIC, direct or through a relay, and tailnet tunnels
// (protocol.CheckNodeAddr). The addresses are the seller's own words, signed
// in its offer. A coordinator of this release refuses an offer with any
// other; the buyer does not rely on that, and dials nothing a host here has
// no use for.
func dialAddrs(addrs []string) []ma.Multiaddr {
	out := make([]ma.Multiaddr, 0, min(len(addrs), protocol.MaxOfferAddrs))
	for _, a := range addrs {
		if len(out) == protocol.MaxOfferAddrs {
			break
		}
		if protocol.CheckNodeAddr(a) != nil {
			continue
		}
		if m, err := ma.NewMultiaddr(a); err == nil {
			out = append(out, m)
		}
	}
	return out
}
