package protocol_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"sharetoken/internal/attest"
	"sharetoken/internal/protocol"
)

// What a node of 1.0.4 or 1.0.5 announces stays valid: the addresses of its
// interfaces, of its relays and of its tunnel, and model IDs as sellers
// write them.
func TestOfferLimitsAdmitWhatClientsSend(t *testing.T) {
	const relay = "/p2p/12D3KooWGzBkQZ2tTWbN3cJmJd5Jq3xgVN8f3mXKXdc1Q2mY8UqB/p2p-circuit"
	for _, a := range []string{
		"/ip4/192.168.1.7/tcp/40123",
		"/ip4/203.0.113.9/udp/40123/quic-v1",
		"/ip6/2001:db8::7/tcp/40123",
		"/ip6/2001:db8::7/udp/40123/quic-v1",
		"/dns4/relay-us.example.org/tcp/4001" + relay,
		"/ip4/203.0.113.1/udp/4001/quic-v1" + relay,
		"/ip6/2001:db8::1/tcp/4001" + relay,
		"/tailcat/tc" + strings.Repeat("Ab0-_", 60),
	} {
		if err := protocol.CheckNodeAddr(a); err != nil {
			t.Errorf("%s: %v", a, err)
		}
	}
	for _, id := range []string{"m", "gpt-5.5", "qwen2.5:7b", "claude-sonnet-4-5-20250929", "meta-llama/Llama-3-70B-Instruct", "my model (local)", "通义千问-7b"} {
		if err := protocol.CheckModelID(id); err != nil {
			t.Errorf("%q: %v", id, err)
		}
	}
	// An offer at every count limit verifies.
	k, now := mustKeys(t), time.Now()
	o := signedOffer(k, now)
	o.Addrs, o.Models = nil, nil
	for i := range protocol.MaxOfferAddrs {
		o.Addrs = append(o.Addrs, fmt.Sprintf("/ip4/10.0.%d.1/tcp/4001", i))
	}
	for i := range protocol.MaxOfferModels {
		o.Models = append(o.Models, protocol.ModelPrice{Model: fmt.Sprintf("m-%d", i), PriceInMicros: protocol.MaxPriceMicros, PriceOutMicros: 1})
	}
	o.MaxConcurrency = protocol.MaxOfferConcurrency
	o.Sign(k.Signer)
	if err := o.Verify(now, 5*time.Minute); err != nil {
		t.Fatalf("an offer at the limits: %v", err)
	}
}

// An offer beyond a limit is refused although its signature is good: by the
// coordinator when announced, and by a buyer handed it with a ticket.
func TestOfferLimits(t *testing.T) {
	now := time.Now()
	addrs := func(a ...string) func(o *protocol.Offer) { return func(o *protocol.Offer) { o.Addrs = a } }
	model := func(id string) func(o *protocol.Offer) { return func(o *protocol.Offer) { o.Models[0].Model = id } }
	cases := map[string]func(o *protocol.Offer){
		"too many models": func(o *protocol.Offer) {
			for i := range protocol.MaxOfferModels {
				o.Models = append(o.Models, protocol.ModelPrice{Model: fmt.Sprintf("m-%d", i)})
			}
		},
		"long model id":       model(strings.Repeat("m", protocol.MaxModelIDBytes+1)),
		"control character":   model("gpt\n5"),
		"bidi override":       model("gpt‮5"),
		"invalid UTF-8":       model("gpt\xff"),
		"padded model id":     model(" gpt-5"),
		"price out of range":  func(o *protocol.Offer) { o.Models[0].PriceOutMicros = protocol.MaxPriceMicros + 1 },
		"cache price":         func(o *protocol.Offer) { o.Models[0].PriceCacheWrite = protocol.MaxPriceMicros + 1 },
		"too many APIs":       func(o *protocol.Offer) { o.Models[0].APIs = slices.Repeat([]string{protocol.APIChat}, 9) },
		"concurrency":         func(o *protocol.Offer) { o.MaxConcurrency = protocol.MaxOfferConcurrency + 1 },
		"long client version": func(o *protocol.Offer) { o.ClientVersion = strings.Repeat("1", 65) },
		"too many addresses": func(o *protocol.Offer) {
			for i := range protocol.MaxOfferAddrs {
				o.Addrs = append(o.Addrs, fmt.Sprintf("/ip4/10.0.%d.1/tcp/4001", i))
			}
		},
		"long address":      addrs("/dns4/" + strings.Repeat("a", 250) + "/tcp/1"),
		"not an address":    addrs("203.0.113.9:4001"),
		"webrtc-direct":     addrs("/ip4/203.0.113.9/udp/4001/webrtc-direct"),
		"webtransport":      addrs("/ip4/203.0.113.9/udp/4001/quic-v1/webtransport"),
		"websocket":         addrs("/ip4/203.0.113.9/tcp/443/ws"),
		"secure websocket":  addrs("/dns4/evil.example/tcp/443/tls/ws"),
		"bare udp":          addrs("/ip4/203.0.113.9/udp/4001"),
		"unix socket":       addrs("/unix/tmp/sock"),
		"circuit in a row":  addrs("/ip4/203.0.113.9/tcp/1/p2p/12D3KooWGzBkQZ2tTWbN3cJmJd5Jq3xgVN8f3mXKXdc1Q2mY8UqB/p2p-circuit/p2p-circuit"),
		"long tunnel":       addrs("/tailcat/tc" + strings.Repeat("a", 1100)),
		"tunnel characters": addrs("/tailcat/tc/ip4/203.0.113.9/udp/4001/webrtc-direct"),
		"large quote": func(o *protocol.Offer) {
			o.Quote = &attest.Quote{Backend: "tdx", Evidence: make([]byte, protocol.MaxQuoteBytes)}
		},
	}
	for name, mutate := range cases {
		k := mustKeys(t)
		o := signedOffer(k, now)
		mutate(o)
		o.Sign(k.Signer)
		if err := o.Verify(now, 5*time.Minute); err == nil {
			t.Errorf("%s: the offer verified", name)
		}
	}
}
