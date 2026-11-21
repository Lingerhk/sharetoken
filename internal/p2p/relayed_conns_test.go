package p2p

import (
	"crypto/rand"
	"fmt"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"
	ma "github.com/multiformats/go-multiaddr"
)

// Connections through one relay all carry the relay's IP address. They are
// not counted against it, on a hub (the coordinator) and on a user host (a
// seller behind NAT) alike, and they leave that address's own budget alone.
// The system limit still bounds them.
func TestRelayedConnsNotCountedAgainstRelayIP(t *testing.T) {
	sk, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	relay, err := peer.IDFromPrivateKey(sk)
	if err != nil {
		t.Fatal(err)
	}
	const relayIP = "203.0.113.7"
	circuit := ma.StringCast(fmt.Sprintf("/ip4/%s/udp/4001/quic-v1/p2p/%s/p2p-circuit", relayIP, relay))
	newRM := func(limits rcmgr.ConcreteLimitConfig) network.ResourceManager {
		rm, err := rcmgr.NewResourceManager(rcmgr.NewFixedLimiter(limits))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { rm.Close() })
		return relayedUncounted{rm}
	}
	open := func(rm network.ResourceManager, dir network.Direction, addr ma.Multiaddr, n int) ([]network.ConnManagementScope, error) {
		var scopes []network.ConnManagementScope
		for range n {
			s, err := rm.OpenConnection(dir, true, addr)
			if err != nil {
				return scopes, err
			}
			scopes = append(scopes, s)
		}
		return scopes, nil
	}

	for _, tc := range []struct {
		name   string
		limits rcmgr.ConcreteLimitConfig
		n      int
	}{
		// Well past the per-address limits (32 on a hub, 8 on a user
		// host), within the transient scope these unattached scopes stay in.
		{"hub", HubLimits(4096), 400},
		{"user", ClientLimits(), 40},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rm := newRM(tc.limits)
			in, err := open(rm, network.DirInbound, circuit, tc.n)
			if err != nil {
				t.Fatalf("inbound relayed connection %d refused: %v", len(in)+1, err)
			}
			if out, err := open(rm, network.DirOutbound, circuit, 10); err != nil {
				t.Fatalf("outbound relayed connection %d refused: %v", len(out)+1, err)
			}
			direct := ma.StringCast("/ip4/" + relayIP + "/udp/4001/quic-v1")
			got, _ := open(rm, network.DirInbound, direct, 9)
			if len(got) != 8 {
				t.Fatalf("direct connections from the relay's address: %d, want libp2p's 8", len(got))
			}
			for _, s := range in {
				s.Done()
			}
		})
	}

	// The system scope still bounds relayed connections.
	rm := newRM(HubLimits(64))
	if got, err := open(rm, network.DirInbound, circuit, 65); err == nil || len(got) != 64 {
		t.Fatalf("a hub of 64 took %d relayed connections (err %v), want 64", len(got), err)
	}
}

func TestUncountedEndpoint(t *testing.T) {
	sk, _, _ := crypto.GenerateEd25519Key(rand.Reader)
	relay, _ := peer.IDFromPrivateKey(sk)
	for in, want := range map[string]string{
		"/ip4/203.0.113.7/tcp/4001/p2p/" + relay.String() + "/p2p-circuit":                          "/ip6/::1/p2p/" + relay.String() + "/p2p-circuit",
		"/dns4/relay.example/tcp/4001/p2p/" + relay.String() + "/p2p-circuit/p2p/" + relay.String(): "/ip6/::1/p2p/" + relay.String() + "/p2p-circuit/p2p/" + relay.String(),
		"/ip4/203.0.113.7/tcp/4001": "/ip4/203.0.113.7/tcp/4001",
	} {
		if got := uncountedEndpoint(ma.StringCast(in)).String(); got != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
}
