package p2p

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	ma "github.com/multiformats/go-multiaddr"
)

func newTestHost(t *testing.T, cfg HostConfig) host.Host {
	t.Helper()
	k, err := NewKeys()
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewHost(k, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

// newRelayHost starts a relay like package relay does (which imports p2p).
func newRelayHost(t *testing.T) host.Host {
	return newTestHost(t, HostConfig{Listen: []string{"/ip4/127.0.0.1/tcp/0"}, Options: []libp2p.Option{
		libp2p.ForceReachabilityPublic(), libp2p.EnableRelayService(relayv2.WithInfiniteLimits()),
	}})
}

// A dial-only host (the auditor's) must reach a node that is known only by
// a /p2p-circuit address, as every node behind NAT is.
func TestDialOnlyHostDialsRelayedNode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	local := []string{"/ip4/127.0.0.1/tcp/0"}
	relay := newRelayHost(t)
	node := newTestHost(t, HostConfig{Listen: local})
	relayInfo := peer.AddrInfo{ID: relay.ID(), Addrs: relay.Addrs()}
	if err := node.Connect(ctx, relayInfo); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Reserve(ctx, node, relayInfo); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	circuit, err := ma.NewMultiaddr(relay.Addrs()[0].String() + "/p2p/" + relay.ID().String() + "/p2p-circuit")
	if err != nil {
		t.Fatal(err)
	}

	auditor := newTestHost(t, HostConfig{DialOnly: true})
	if len(auditor.Addrs()) != 0 {
		t.Fatalf("dial-only host listens on %v", auditor.Addrs())
	}
	if err := auditor.Connect(ctx, peer.AddrInfo{ID: node.ID(), Addrs: []ma.Multiaddr{circuit}}); err != nil {
		t.Fatalf("dial-only host could not dial the relayed node: %v", err)
	}
}
