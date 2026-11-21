package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	"tailscale.com/envknob"
	"tailscale.com/net/netcheck"
	"tailscale.com/syncs"
	"tailscale.com/tailcfg"
	"tailscale.com/tstest/integration"
	"tailscale.com/types/logger"

	"sharetoken/internal/client"
	"sharetoken/internal/tailnet"
)

// The tailnet's engines must stay off the real network in tests: built
// without tailcat's release tags they ask the default gateway for a port
// mapping and probe for captive portals whenever they start.
func TestMain(m *testing.M) {
	envknob.Setenv("IN_TS_TEST", "true")
	netcheck.HookStartCaptivePortalDetection.SetForTest(func(ctx context.Context, c *netcheck.Client, dm *tailcfg.DERPMap, preferredDERP tailcfg.DERPRegionID, setCaptivePortal func(bool)) (done <-chan struct{}, stop func()) {
		return syncs.ClosedChan(), func() {}
	})
	os.Exit(m.Run())
}

// behindNAT lets a client reach one peer through circuit relays and tunnels
// only, as if neither could dial the other's addresses.
type behindNAT struct {
	onlyFamily
	target peer.ID
}

func (g behindNAT) InterceptAddrDial(p peer.ID, a ma.Multiaddr) bool {
	if tailnet.Is(a) {
		return true
	}
	if _, err := a.ValueForProtocol(ma.P_CIRCUIT); p == g.target && err != nil {
		return false
	}
	return g.onlyFamily.InterceptAddrDial(p, a)
}

// derpMapFile runs a DERP relay and a STUN server on loopback and writes
// their DERP map to a file, as a network that runs its own relays hands it
// to its users.
func derpMapFile(t *testing.T) string {
	t.Helper()
	dm := integration.RunDERPAndSTUN(t, logger.Discard, "127.0.0.1")
	data, err := json.Marshal(dm)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "derpmap.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func tailnetAddrs(addrs []string) int {
	c := 0
	for _, a := range addrs {
		if strings.HasPrefix(a, "/"+tailnet.ProtocolName+"/") {
			c++
		}
	}
	return c
}

func timing(h http.Header) map[string]string {
	out := map[string]string{}
	for _, f := range strings.Fields(h.Get("X-Sharetoken-Timing")) {
		if k, v, ok := strings.Cut(f, "="); ok {
			out[k] = v
		}
	}
	return out
}

func tailnetStatus(t *testing.T, a *client.Agent) client.TailnetStatus {
	t.Helper()
	var st client.TailnetStatus
	getJSON(t, a, "/api/v1/tailnet", &st)
	return st
}

// A seller and its buyers with the tailnet on: the offer carries the
// seller's tunnel address, jobs run through the tunnel as direct
// connections, a buyer without the tailnet is served through the relay as
// before, and turning the tailnet off takes everyone back to the relay.
func TestJobsOverTheTailnet(t *testing.T) {
	n := newNetwork(t, 1)
	derp := derpMapFile(t)
	on := func(wait int) func(*client.Config) {
		return func(c *client.Config) {
			c.Tailnet = client.TailnetConfig{Enabled: true, DERPMap: derp, WaitMillis: wait}
		}
	}

	alice := n.userWith("alice", shareMock(mockUpstream(t, "mock-7b", false).URL, priceIn, priceOut), "/ip4/127.0.0.1/tcp/0", onlyFamily{ma.P_IP4}, on(0))
	aid, _ := peer.Decode(alice.PeerID())
	waitFor(t, 30*time.Second, "alice's offer with her tunnel address", func() bool {
		addrs := offerAddrs(n, alice.PeerID())
		return tailnetAddrs(addrs) == 1 && circuitsThrough(addrs, n.relays[0].Host.ID()) > 0
	})
	if st := tailnetStatus(t, alice); !st.Enabled || !st.Serving || st.Region == "" || st.DERPMap != "file" {
		t.Fatalf("alice's tailnet: %+v", st)
	}
	// The address is in the offer and nowhere else: not among the addresses
	// alice's host tells every peer it meets.
	for _, a := range alice.Host().Addrs() {
		if tailnet.Is(a) {
			t.Fatalf("alice's host announces %s", a)
		}
	}

	// Bob waits for the tunnel: his first job already runs through it.
	bob := n.userWith("bob", nil, "/ip4/127.0.0.1/tcp/0", behindNAT{onlyFamily{ma.P_IP4}, aid}, on(10_000))
	content, hdr := chat(t, bob)
	if content != "Hello, world!" || hdr.Get("X-Sharetoken-Node") != alice.PeerID() || hdr.Get("X-Sharetoken-Verified") != "true" {
		t.Fatalf("bob got %q from %s", content, hdr.Get("X-Sharetoken-Node"))
	}
	if tm := timing(hdr); tm["relayed"] != "false" || tm["tailnet"] == "" {
		t.Fatalf("bob's first job: %s", hdr.Get("X-Sharetoken-Timing"))
	}
	direct := 0
	for _, c := range bob.Host().Network().ConnsToPeer(aid) {
		if tailnet.Is(c.RemoteMultiaddr()) && !c.Stat().Limited {
			direct++
		}
	}
	if direct != 1 {
		t.Fatalf("bob's connections to alice: %v", bob.Host().Network().ConnsToPeer(aid))
	}
	if st := tailnetStatus(t, bob); len(st.Tunnels) != 1 || st.Tunnels[0].Dir != "out" || st.Tunnels[0].Peer != alice.PeerID() {
		t.Fatalf("bob's tunnels: %+v", st.Tunnels)
	}
	waitFor(t, 10*time.Second, "bob's tunnel in alice's status", func() bool {
		for _, tun := range tailnetStatus(t, alice).Tunnels {
			if tun.Dir == "in" && tun.RxBytes > 0 {
				return true
			}
		}
		return false
	})
	var bst client.Status
	getJSON(t, bob, "/api/v1/status", &bst)
	if bst.Tailnet == nil || len(bst.Tailnet.Tunnels) != 1 {
		t.Fatalf("bob's status: %+v", bst.Tailnet)
	}

	// Dave does not wait: his first job goes through the relay while the
	// tunnel comes up beside it, and the next one finds the tunnel.
	dave := n.userWith("dave", nil, "/ip4/127.0.0.1/tcp/0", behindNAT{onlyFamily{ma.P_IP4}, aid}, on(0))
	if _, hdr := chat(t, dave); timing(hdr)["relayed"] != "true" {
		// The tunnel may win on a fast machine; then it must say so.
		if timing(hdr)["tailnet"] == "" {
			t.Fatalf("dave's first job: %s", hdr.Get("X-Sharetoken-Timing"))
		}
	}
	waitFor(t, 30*time.Second, "dave's direct connection", func() bool { return tailnet.HasDirect(dave.Host(), aid) })
	if _, hdr := chat(t, dave); timing(hdr)["relayed"] != "false" || timing(hdr)["tailnet"] == "" {
		t.Fatalf("dave's second job: %s", hdr.Get("X-Sharetoken-Timing"))
	}

	// Carol has no tailnet: the address means nothing to her and she is
	// served through the relay.
	carol := n.userWith("carol", nil, "/ip4/127.0.0.1/tcp/0", behindNAT{onlyFamily{ma.P_IP4}, aid}, nil)
	if content, hdr := chat(t, carol); content != "Hello, world!" || timing(hdr)["relayed"] != "true" || timing(hdr)["tailnet"] != "" {
		t.Fatalf("carol: %q, %s", content, hdr.Get("X-Sharetoken-Timing"))
	}
	if st := tailnetStatus(t, carol); st.Enabled || len(st.Tunnels) != 0 {
		t.Fatalf("carol's tailnet: %+v", st)
	}
	var cst client.Status
	getJSON(t, carol, "/api/v1/status", &cst)
	if cst.Tailnet != nil {
		t.Fatalf("carol's status shows a tailnet: %+v", cst.Tailnet)
	}

	// Carol turns it on while running: the next job brings the tunnel up.
	putTailnet(t, carol, `{"enabled":true,"derp_map":"`+derp+`","wait_ms":10000}`, http.StatusOK)
	if _, hdr := chat(t, carol); timing(hdr)["tailnet"] == "" || timing(hdr)["relayed"] != "false" {
		// Her relayed connection from before still stands and serves this
		// job; the tunnel is up for the one after.
		waitFor(t, 30*time.Second, "carol's direct connection", func() bool { return tailnet.HasDirect(carol.Host(), aid) })
		if _, hdr := chat(t, carol); timing(hdr)["tailnet"] == "" || timing(hdr)["relayed"] != "false" {
			t.Fatalf("carol with the tailnet on: %s", hdr.Get("X-Sharetoken-Timing"))
		}
	}

	// Alice turns it off: the address leaves her offer, the tunnels end, and
	// the buyers are back on the relay.
	putTailnet(t, alice, `{"enabled":false}`, http.StatusOK)
	waitFor(t, 20*time.Second, "alice's offer without the tunnel address", func() bool {
		return tailnetAddrs(offerAddrs(n, alice.PeerID())) == 0
	})
	if st := tailnetStatus(t, alice); st.Enabled || st.Serving || len(st.Tunnels) != 0 {
		t.Fatalf("alice's tailnet after off: %+v", st)
	}
	waitFor(t, 20*time.Second, "bob off the tunnel", func() bool { return !tailnet.HasDirect(bob.Host(), aid) })
	if content, hdr := chat(t, bob); content != "Hello, world!" || timing(hdr)["relayed"] != "true" || timing(hdr)["tailnet"] != "" {
		t.Fatalf("bob after alice turned it off: %q, %s", content, hdr.Get("X-Sharetoken-Timing"))
	}

	// And on again.
	putTailnet(t, alice, `{"enabled":true}`, http.StatusOK)
	waitFor(t, 30*time.Second, "alice's offer with a tunnel address again", func() bool {
		return tailnetAddrs(offerAddrs(n, alice.PeerID())) == 1
	})
	waitFor(t, 90*time.Second, "bob through the tunnel again", func() bool {
		_, hdr := chat(t, bob)
		return timing(hdr)["tailnet"] != ""
	})

	// What the settings refuse.
	putTailnet(t, bob, `{"wait_ms":-1}`, http.StatusBadRequest)
	putTailnet(t, bob, `{"wait_ms":999999}`, http.StatusBadRequest)
	putTailnet(t, bob, `{"derp_map":"/no/such/file.json"}`, http.StatusBadRequest)
	if st := tailnetStatus(t, bob); !st.Enabled || st.WaitMillis != 10_000 {
		t.Fatalf("bob's settings changed by refused requests: %+v", st)
	}
}

// A seller whose DERP relays the buyer does not know: the buyer does not
// follow the address, and the job runs through the relay.
func TestTailnetAddressOutsideTheBuyersMap(t *testing.T) {
	n := newNetwork(t, 1)
	sellerMap, buyerMap := derpMapFile(t), filepath.Join(t.TempDir(), "other.json")
	other := &tailcfg.DERPMap{Regions: map[tailcfg.DERPRegionID]*tailcfg.DERPRegion{7: {RegionID: 7, RegionCode: "else",
		Nodes: []*tailcfg.DERPNode{{Name: "e", RegionID: 7, HostName: "derp.invalid", IPv4: "127.0.0.2", DERPPort: 1}}}}}
	data, _ := json.Marshal(other)
	if err := os.WriteFile(buyerMap, data, 0o600); err != nil {
		t.Fatal(err)
	}
	alice := n.userWith("alice", shareMock(mockUpstream(t, "mock-7b", false).URL, priceIn, priceOut), "/ip4/127.0.0.1/tcp/0", onlyFamily{ma.P_IP4},
		func(c *client.Config) { c.Tailnet = client.TailnetConfig{Enabled: true, DERPMap: sellerMap} })
	aid, _ := peer.Decode(alice.PeerID())
	waitFor(t, 30*time.Second, "alice's offer with her tunnel address", func() bool {
		return tailnetAddrs(offerAddrs(n, alice.PeerID())) == 1
	})
	bob := n.userWith("bob", nil, "/ip4/127.0.0.1/tcp/0", behindNAT{onlyFamily{ma.P_IP4}, aid},
		func(c *client.Config) {
			c.Tailnet = client.TailnetConfig{Enabled: true, DERPMap: buyerMap, WaitMillis: 3000}
		})
	t0 := time.Now()
	content, hdr := chat(t, bob)
	if content != "Hello, world!" || timing(hdr)["relayed"] != "true" || timing(hdr)["tailnet"] != "" {
		t.Fatalf("bob: %q, %s", content, hdr.Get("X-Sharetoken-Timing"))
	}
	// The refusal is immediate: the job did not sit out the wait.
	if d := time.Since(t0); d > 2*time.Second {
		t.Fatalf("the job took %v", d)
	}
	if st := tailnetStatus(t, bob); len(st.Tunnels) != 0 {
		t.Fatalf("bob has tunnels: %+v", st.Tunnels)
	}
}

func putTailnet(t *testing.T, a *client.Agent, body string, want int) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, "http://"+a.Addr()+"/api/v1/tailnet", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+a.Key())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != want {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		t.Fatalf("PUT /api/v1/tailnet %s: %d %s, want %d", body, resp.StatusCode, buf.String(), want)
	}
}
