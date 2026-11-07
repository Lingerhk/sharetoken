package p2p

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/control"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	"github.com/libp2p/go-libp2p/p2p/protocol/ping"
	ma "github.com/multiformats/go-multiaddr"
)

func TestParseDualStackRelay(t *testing.T) {
	id := "12D3KooWHNbGXKRnGB1G5wRSPnPZFGfaJqnMhz4xgE1wH1fvHFkc"
	other := "12D3KooWSony3boFHAksPJFJoMx54N7EDbPirXpvJVeUmBGf2ozP"
	v4, v6 := "/ip4/203.0.113.7/tcp/8091/p2p/"+id, "/ip6/2001:db8::7/udp/8091/quic-v1/p2p/"+id
	ai, err := ParseRelay(v4 + ", " + v6)
	if err != nil || ai.ID.String() != id || len(ai.Addrs) != 2 {
		t.Fatalf("dual stack: %v %v", ai, err)
	}
	if got := RelayAddr(ai); got != v4+","+v6 {
		t.Fatalf("round trip: %s", got)
	}
	if ai, err := ParseRelay(v4); err != nil || len(ai.Addrs) != 1 {
		t.Fatalf("single: %v %v", ai, err)
	}
	if _, err := ParseRelay(v4 + ",/ip6/::1/tcp/1/p2p/" + other); err == nil || !strings.Contains(err.Error(), "different peers") {
		t.Fatalf("mixed peers: %v", err)
	}
	if _, err := ParseRelay(" , "); err == nil {
		t.Fatal("empty accepted")
	}
}

// onlyFamily lets a host dial only one IP family, like a machine that has
// only IPv4 or only IPv6 connectivity.
type onlyFamily struct{ proto int }

func (g onlyFamily) InterceptPeerDial(peer.ID) bool { return true }
func (g onlyFamily) InterceptAddrDial(_ peer.ID, a ma.Multiaddr) bool {
	_, err := a.ValueForProtocol(g.proto)
	return err == nil
}
func (g onlyFamily) InterceptAccept(network.ConnMultiaddrs) bool { return true }
func (g onlyFamily) InterceptSecured(network.Direction, peer.ID, network.ConnMultiaddrs) bool {
	return true
}
func (g onlyFamily) InterceptUpgraded(network.Conn) (bool, control.DisconnectReason) { return true, 0 }

// A dual-stack relay bridges an IPv4-only buyer and an IPv6-only seller.
func TestDualStackRelayBridgesIPv4AndIPv6(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	relay := newTestHost(t, HostConfig{Listen: []string{"/ip4/127.0.0.1/tcp/0", "/ip6/::1/tcp/0", "/ip6/::1/udp/0/quic-v1"},
		Options: []libp2p.Option{libp2p.ForceReachabilityPublic(), libp2p.EnableRelayService(relayv2.WithInfiniteLimits())}})
	ri, err := ParseRelay(RelayAddr(peer.AddrInfo{ID: relay.ID(), Addrs: relay.Addrs()}))
	if err != nil {
		t.Fatal(err)
	}
	seller := newTestHost(t, HostConfig{Listen: []string{"/ip6/::1/tcp/0"},
		Options: []libp2p.Option{libp2p.ConnectionGater(onlyFamily{ma.P_IP6})}})
	buyer := newTestHost(t, HostConfig{Listen: []string{"/ip4/127.0.0.1/tcp/0"},
		Options: []libp2p.Option{libp2p.ConnectionGater(onlyFamily{ma.P_IP4})}})

	if _, err := client.Reserve(ctx, seller, ri); err != nil {
		t.Fatalf("IPv6-only seller reserving: %v", err)
	}
	if c := seller.Network().ConnsToPeer(relay.ID()); len(c) == 0 || !isIP6(c[0].RemoteMultiaddr()) {
		t.Fatalf("seller should reach the relay over IPv6: %v", c)
	}

	// The buyer knows the seller's own (IPv6) address and the circuit
	// address; only the circuit works for it.
	buyer.Peerstore().AddAddrs(seller.ID(), append(seller.Addrs(), CircuitAddrs(ri)...), time.Minute)
	if err := buyer.Connect(ctx, peer.AddrInfo{ID: seller.ID()}); err != nil {
		t.Fatalf("IPv4-only buyer reaching the IPv6-only seller: %v", err)
	}
	conns := buyer.Network().ConnsToPeer(seller.ID())
	if len(conns) == 0 || !IsRelayed(conns[0]) {
		t.Fatalf("want a relayed connection, got %v", conns)
	}
	if c := buyer.Network().ConnsToPeer(relay.ID()); len(c) == 0 || isIP6(c[0].RemoteMultiaddr()) {
		t.Fatalf("buyer should reach the relay over IPv4: %v", c)
	}
	res := <-ping.Ping(network.WithAllowLimitedConn(ctx, "test"), buyer, seller.ID())
	if res.Error != nil {
		t.Fatalf("traffic across the bridge: %v", res.Error)
	}
}

func isIP6(a ma.Multiaddr) bool {
	_, err := a.ValueForProtocol(ma.P_IP6)
	return err == nil
}
