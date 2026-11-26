package p2p

import (
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	ma "github.com/multiformats/go-multiaddr"
)

// A host runs TCP, QUIC and the circuit relay transport, and nothing else:
// libp2p's WebSocket, WebTransport and WebRTC transports would parse what any
// peer sends, and dial such an address when a seller's offer named one.
func TestHostTransports(t *testing.T) {
	const relay = "/p2p/12D3KooWGzBkQZ2tTWbN3cJmJd5Jq3xgVN8f3mXKXdc1Q2mY8UqB/p2p-circuit"
	for name, cfg := range map[string]HostConfig{
		"user":      {},
		"dial-only": {DialOnly: true},
		"hub":       {Listen: []string{"/ip4/127.0.0.1/tcp/0"}, MaxConns: 64},
	} {
		h := newTestHost(t, cfg)
		sw := h.Network().(*swarm.Swarm)
		for addr, want := range map[string]bool{
			"/ip4/203.0.113.9/tcp/4001":                              true,
			"/ip4/203.0.113.9/udp/4001/quic-v1":                      true,
			"/ip6/2001:db8::9/udp/4001/quic-v1":                      true,
			"/ip4/203.0.113.9/tcp/4001" + relay:                      true,
			"/ip4/203.0.113.9/udp/4001/webrtc-direct":                false,
			"/ip4/203.0.113.9/udp/4001/quic-v1/webtransport":         false,
			"/ip4/203.0.113.9/tcp/443/ws":                            false,
			"/ip4/203.0.113.9/tcp/443/tls/ws":                        false,
			"/dns4/example.org/tcp/443/wss":                          false,
			"/ip4/203.0.113.9/udp/4001/quic-v1/webtransport" + relay: true, // dialed as a circuit; the relay's own address is then refused
		} {
			if got := sw.TransportForDialing(ma.StringCast(addr)) != nil; got != want {
				t.Errorf("%s host: a transport for %s: %v, want %v", name, addr, got, want)
			}
		}
		// Listening on libp2p's own default addresses would have named
		// WebTransport and WebRTC; ours name TCP and QUIC.
		for _, a := range h.Network().ListenAddresses() {
			if s := a.String(); !strings.HasSuffix(s, "/quic-v1") && !strings.Contains(s, "/tcp/") && s != "/p2p-circuit" {
				t.Errorf("%s host listens on %s", name, s)
			}
		}
		if name == "user" && len(h.Network().ListenAddresses()) < 2 {
			t.Errorf("a host with no listen addresses given listens on %v, want TCP and QUIC", h.Network().ListenAddresses())
		}
	}
}
