package buyer

import (
	"fmt"
	"testing"

	"sharetoken/internal/protocol"
)

// A buyer dials only TCP and QUIC addresses of a ticket, direct or relayed:
// an address of a transport no host here runs (which a seller can write into
// its offer) is dropped, and so is everything past the address limit.
func TestDialAddrsKeepsOnlyOurTransports(t *testing.T) {
	const relay = "/ip4/203.0.113.1/tcp/4001/p2p/12D3KooWGzBkQZ2tTWbN3cJmJd5Jq3xgVN8f3mXKXdc1Q2mY8UqB/p2p-circuit"
	got := dialAddrs([]string{
		"/ip4/203.0.113.9/udp/4001/webrtc-direct",
		"/ip4/203.0.113.9/tcp/4001",
		"/ip4/203.0.113.9/udp/4001/quic-v1/webtransport",
		"/ip4/203.0.113.9/udp/4001/quic-v1",
		"/dns4/evil.example/tcp/443/tls/ws",
		relay,
		"not an address",
	})
	want := []string{"/ip4/203.0.113.9/tcp/4001", "/ip4/203.0.113.9/udp/4001/quic-v1", relay}
	if len(got) != len(want) {
		t.Fatalf("dial addresses %v, want %v", got, want)
	}
	for i, m := range got {
		if m.String() != want[i] {
			t.Fatalf("dial addresses %v, want %v", got, want)
		}
	}
	var many []string
	for i := range 4 * protocol.MaxOfferAddrs {
		many = append(many, fmt.Sprintf("/ip4/10.%d.%d.1/tcp/4001", i/256, i%256))
	}
	if got := dialAddrs(many); len(got) != protocol.MaxOfferAddrs {
		t.Fatalf("%d addresses kept, want %d", len(got), protocol.MaxOfferAddrs)
	}
}
