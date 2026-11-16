package e2e

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"

	"sharetoken/internal/client"
	"sharetoken/internal/p2p"
	"sharetoken/internal/relay"
)

// circuitOnly refuses direct dials to one peer, as if it sat behind NAT.
type circuitOnly struct {
	onlyFamily
	target peer.ID
}

func (g circuitOnly) InterceptAddrDial(p peer.ID, a ma.Multiaddr) bool {
	if _, err := a.ValueForProtocol(ma.P_CIRCUIT); p == g.target && err != nil {
		return false
	}
	return g.onlyFamily.InterceptAddrDial(p, a)
}

// privateRelay starts a relay that serves only accounts, with no coordinator.
func (n *network) privateRelay(accounts ...string) (*relay.Relay, string) {
	n.t.Helper()
	k, _ := p2p.NewKeys()
	r, err := relay.New(k, relay.Config{Listen: []string{"/ip4/127.0.0.1/tcp/0"}, Networks: []relay.Network{{Name: "mine",
		PortalKeys: []ed25519.PublicKey{n.passKey.Public().(ed25519.PublicKey)}, AllowAccounts: accounts}}})
	if err != nil {
		n.t.Fatal(err)
	}
	n.t.Cleanup(func() { _ = r.Close() })
	return r, p2p.RelayAddr(peer.AddrInfo{ID: r.Host.ID(), Addrs: r.Host.Addrs()})
}

func offerAddrs(n *network, node string) []string {
	for _, o := range n.coord.Engine.Offers() {
		if o.Node == node {
			return o.Addrs
		}
	}
	return nil
}

func circuitsThrough(addrs []string, relay peer.ID) int {
	c := 0
	for _, a := range addrs {
		if strings.Contains(a, "/p2p/"+relay.String()+"/p2p-circuit") {
			c++
		}
	}
	return c
}

// A user's own relay: their node is reached through it and no longer through
// the network's relays, a buyer's job runs over it, and neither the
// coordinator nor the node's traffic with the coordinator ever touches it.
// Another user is refused a reservation there and falls back to the
// network's relays; so does the owner when the private relay dies.
func TestSharingThroughAPrivateRelay(t *testing.T) {
	n := newNetwork(t, 1)
	official := n.relays[0].Host.ID()
	n.privateGrace = 2 * time.Second
	// The first user after root.
	priv, privAddr := n.privateRelay("user:2")
	withRelay := func(c *client.Config) { c.PrivateRelays = []string{privAddr} }

	alice := n.userWith("alice", shareMock(mockUpstream(t, "mock-7b", false).URL, priceIn, priceOut), "/ip4/127.0.0.1/tcp/0", onlyFamily{ma.P_IP4}, withRelay)
	if acct := n.me(alice).Balance.Account; acct != "user:2" {
		t.Fatalf("alice's account is %s", acct)
	}
	waitFor(t, 20*time.Second, "alice's offer through her relay only", func() bool {
		addrs := offerAddrs(n, alice.PeerID())
		return circuitsThrough(addrs, priv.Host.ID()) > 0 && circuitsThrough(addrs, official) == 0
	})
	st := alice.Status()
	var view *client.RelayView
	for i, r := range st.Relays {
		if r.Private {
			view = &st.Relays[i]
		} else if r.Reserved {
			t.Fatalf("alice still reserves on %s", r.Addr)
		}
	}
	if view == nil || !view.Reserved || view.Current || view.Addr != privAddr || view.RTTMillis <= 0 {
		t.Fatalf("the private relay in alice's status: %+v", st.Relays)
	}
	if via, _ := p2p.ParseRelay(st.Coordinator.Via); via.ID != official {
		t.Fatalf("alice reaches the coordinator through %s", st.Coordinator.Via)
	}

	// Bob cannot dial alice directly: his job runs over her relay.
	aid, _ := peer.Decode(alice.PeerID())
	bob := n.userWith("bob", nil, "/ip4/127.0.0.1/tcp/0", circuitOnly{onlyFamily{ma.P_IP4}, aid}, nil)
	if content, hdr := chat(t, bob); content != "Hello, world!" || hdr.Get("X-Sharetoken-Node") != alice.PeerID() || hdr.Get("X-Sharetoken-Verified") != "true" {
		t.Fatalf("bob got %q from %s", content, hdr.Get("X-Sharetoken-Node"))
	}
	conns := bob.Host().Network().ConnsToPeer(aid)
	if len(conns) == 0 {
		t.Fatal("bob has no connection to alice")
	}
	for _, c := range conns {
		if !strings.Contains(c.RemoteMultiaddr().String(), "/p2p/"+priv.Host.ID().String()+"/p2p-circuit") {
			t.Fatalf("bob reached alice at %s", c.RemoteMultiaddr())
		}
	}
	if ps := priv.Stats(); ps.BytesRelayed == 0 || ps.Networks[0].Reservations != 1 {
		t.Fatalf("the private relay's stats: %+v", ps)
	}

	// The coordinator was never there, and nobody talked to it through there.
	if c := priv.Host.Network().ConnsToPeer(n.coordH); len(c) != 0 {
		t.Fatalf("the coordinator connected to the private relay: %v", c)
	}
	for _, a := range []*client.Agent{alice, bob} {
		for _, c := range a.Host().Network().ConnsToPeer(n.coordH) {
			if !strings.Contains(c.RemoteMultiaddr().String(), "/p2p/"+official.String()+"/p2p-circuit") {
				t.Fatalf("coordinator traffic at %s", c.RemoteMultiaddr())
			}
		}
	}

	// Carol lists the same relay, but it is not hers: she is told, and her
	// node is offered through the network's relay after the grace period.
	carol := n.userWith("carol", shareMock(mockUpstream(t, "mock-7b", false).URL, priceIn, priceOut), "/ip4/127.0.0.1/tcp/0", onlyFamily{ma.P_IP4}, withRelay)
	waitFor(t, 20*time.Second, "carol's offer through the network's relay", func() bool {
		addrs := offerAddrs(n, carol.PeerID())
		return circuitsThrough(addrs, official) > 0 && circuitsThrough(addrs, priv.Host.ID()) == 0
	})
	warned := false
	for _, w := range carol.Status().Warnings {
		warned = warned || strings.Contains(w, "does not list your account")
	}
	if !warned || priv.Stats().Networks[0].PassesRefused == 0 {
		t.Fatalf("carol was not told: %v, relay %+v", carol.Status().Warnings, priv.Stats().Networks[0])
	}

	// The private relay dies: alice's node moves to the network's relay.
	_ = priv.Close()
	waitFor(t, 30*time.Second, "alice's offer through the network's relay", func() bool {
		return circuitsThrough(offerAddrs(n, alice.PeerID()), official) > 0
	})
}

// The local API adds and removes private relays while the node runs, and
// refuses what is not a private relay.
func TestPrivateRelaysAreManagedThroughTheLocalAPI(t *testing.T) {
	n := newNetwork(t, 1)
	official := n.relays[0]
	priv, privAddr := n.privateRelay("user:2")
	n.privateGrace = time.Second
	alice := n.user("alice", shareMock(mockUpstream(t, "mock-7b", false).URL, priceIn, priceOut))
	waitFor(t, 20*time.Second, "alice's offer through the network's relay", func() bool {
		return circuitsThrough(offerAddrs(n, alice.PeerID()), official.Host.ID()) > 0
	})
	call := func(method, addr string) (int, []client.RelayView) {
		body, _ := json.Marshal(map[string]string{"addr": addr})
		req, _ := http.NewRequest(method, "http://"+alice.Addr()+"/api/v1/relays/private", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+alice.Key())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var rs []client.RelayView
		if resp.StatusCode == 200 {
			_ = json.NewDecoder(resp.Body).Decode(&rs)
		}
		return resp.StatusCode, rs
	}
	for name, addr := range map[string]string{
		"garbage":            "not an address",
		"the network's own":  official.Addrs()[0],
		"an address without": "/ip4/127.0.0.1/tcp/4001",
	} {
		if code, _ := call("POST", addr); code != 400 {
			t.Fatalf("adding %s: %d", name, code)
		}
	}
	if code, rs := call("POST", privAddr); code != 200 || len(rs) != 2 || !rs[1].Private {
		t.Fatalf("adding the private relay: %d %+v", code, rs)
	}
	waitFor(t, 20*time.Second, "alice's offer through her relay only", func() bool {
		addrs := offerAddrs(n, alice.PeerID())
		return circuitsThrough(addrs, priv.Host.ID()) > 0 && circuitsThrough(addrs, official.Host.ID()) == 0
	})
	if c, err := client.LoadConfig(alice.Home()); err != nil || len(c.PrivateRelays) != 1 {
		t.Fatalf("saved: %+v %v", c.PrivateRelays, err)
	}
	// Removed by peer ID: back to the network's relay.
	if code, _ := call("DELETE", "12D3KooWunknown"); code != 400 {
		t.Fatalf("removing an unknown relay: %d", code)
	}
	if code, rs := call("DELETE", priv.Host.ID().String()); code != 200 || len(rs) != 1 {
		t.Fatalf("removing the private relay: %d %+v", code, rs)
	}
	waitFor(t, 20*time.Second, "alice's offer through the network's relay again", func() bool {
		addrs := offerAddrs(n, alice.PeerID())
		return circuitsThrough(addrs, official.Host.ID()) > 0 && circuitsThrough(addrs, priv.Host.ID()) == 0
	})
	if c, _ := client.LoadConfig(alice.Home()); len(c.PrivateRelays) != 0 {
		t.Fatalf("still saved: %+v", c.PrivateRelays)
	}
}
