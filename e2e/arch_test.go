package e2e

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/control"
	lnet "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	circuit "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	ma "github.com/multiformats/go-multiaddr"

	"sharetoken/client"
	"sharetoken/coordinator"
	"sharetoken/internal/sqlite"
	"sharetoken/node"
	"sharetoken/p2p"
	"sharetoken/portal"
	"sharetoken/protocol"
	"sharetoken/relay"
)

// network is the full v2 topology on localhost: relays, a coordinator that
// listens nowhere (reachable only through relays), a portal on the same
// database file through its own connection (as a separate process would),
// and user clients.
type network struct {
	t       *testing.T
	ctx     context.Context
	relays  []*relay.Relay
	coord   *coordinator.Service
	coordH  peer.ID
	portal  *portal.Server
	url     string // portal base URL
	admin   string // admin token
	passKey ed25519.PrivateKey
}

func newNetwork(t *testing.T, relays int) *network {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	n := &network{t: t, ctx: ctx}
	dbPath := filepath.Join(t.TempDir(), "sharetoken.db")

	// Portal first: it creates the schema; the coordinator joins the file.
	pdb, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pdb.Close() })
	passPub, passKey, _ := ed25519.GenerateKey(nil)
	n.passKey = passKey
	n.portal, err = portal.New(portal.Config{DB: pdb, OpenRegistration: true, SignupCreditMicros: 5_000_000, PassKey: passKey})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(n.portal.Handler())
	t.Cleanup(srv.Close)
	n.url = srv.URL
	root, err := n.portal.CreateUser("root", "rootrootroot", true)
	if err != nil {
		t.Fatal(err)
	}
	n.admin, _, _ = n.portal.Store().IssueSessionToken(root.ID, "test", 0, true) // admins need a two-factor login

	// Relays admit only pass holders, and the coordinator by its peer ID.
	ck, _ := p2p.NewKeys()
	for i := 0; i < relays; i++ {
		k, _ := p2p.NewKeys()
		r, err := relay.New(k, relay.Config{Listen: []string{"/ip4/127.0.0.1/tcp/0", "/ip6/::1/tcp/0"}, // dual stack
			PortalKeys: []ed25519.PublicKey{passPub}, AllowPeers: []peer.ID{ck.PeerID}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = r.Close() })
		n.relays = append(n.relays, r)
		if _, err := n.portal.Store().AddRelay(p2p.RelayAddr(peer.AddrInfo{ID: r.Host.ID(), Addrs: r.Host.Addrs()}), "relay-"+string(rune('a'+i)), "local"); err != nil {
			t.Fatal(err)
		}
	}

	cdb, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cdb.Close() })
	ch, err := p2p.NewHost(ck, p2p.HostConfig{DialOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ch.Close() })
	n.coord, err = coordinator.NewService(ctx, ch, ck, coordinator.Config{DB: cdb, FeeBps: feeBps, RelaySync: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(n.coord.Close)
	n.coordH = ch.ID()
	if len(ch.Addrs()) != 0 {
		t.Fatalf("the coordinator must not listen, has %v", ch.Addrs())
	}
	// Wait until the portal reports the coordinator reachable through every relay.
	waitFor(t, 20*time.Second, "coordinator reservations on every relay", func() bool {
		ni, err := n.portal.Network()
		if err != nil || ni.Coordinator != ch.ID().String() || len(ni.Relays) != relays {
			return false
		}
		for _, r := range ni.Relays {
			if !r.OK {
				return false
			}
		}
		return true
	})
	return n
}

// user registers a user through the portal and starts its client.
func (n *network) user(name string, share *client.ShareConfig) *client.Agent {
	n.t.Helper()
	return n.userOn(name, share, "/ip4/127.0.0.1/tcp/0")
}

// userOn starts a user whose machine has only listen's IP family: it listens
// there and can dial nothing else.
func (n *network) userOn(name string, share *client.ShareConfig, listen string) *client.Agent {
	n.t.Helper()
	home := n.t.TempDir()
	pc := client.NewPortalClient(n.url, "", "")
	res, err := pc.Register(n.ctx, name, "password-"+name, "test")
	if err != nil {
		n.t.Fatal(err)
	}
	cfg := client.DefaultConfig()
	cfg.Portal, cfg.User, cfg.Token, cfg.DeviceName = n.url, res.User.Username, res.Token, name+"-laptop"
	cfg.HTTP = "127.0.0.1:0"
	cfg.Listen = []string{listen}
	cfg.NAT = false
	cfg.Buy.BudgetMicros = 1_000_000
	if share != nil {
		cfg.Share = *share
	}
	if err := client.SaveConfig(home, cfg); err != nil {
		n.t.Fatal(err)
	}
	a := client.NewAgent(home, cfg)
	a.HealthEvery, a.MeasureEvery = 200*time.Millisecond, time.Hour
	fam := ma.P_IP4
	if strings.HasPrefix(listen, "/ip6/") {
		fam = ma.P_IP6
	}
	a.HostOptions = []libp2p.Option{libp2p.ConnectionGater(onlyFamily{fam})}
	if err := a.Start(n.ctx); err != nil {
		n.t.Fatalf("starting %s: %v", name, err)
	}
	n.t.Cleanup(a.Close)
	return a
}

func shareMock(upstream string, pin, pout uint64) *client.ShareConfig {
	return &client.ShareConfig{
		Enabled: true, Upstream: upstream + "/v1", Concurrency: 2, Relays: 2,
		Models: []node.ModelConfig{{ID: model, UpstreamModel: "mock-7b", PriceInMicros: pin, PriceOutMicros: pout}},
	}
}

// chat posts a chat completion to a client's local OpenAI endpoint.
func chat(t *testing.T, a *client.Agent) (string, http.Header) {
	t.Helper()
	resp, err := http.Post("http://"+a.Addr()+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("chat: %d %s", resp.StatusCode, data)
	}
	var out struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &out); err != nil || len(out.Choices) != 1 {
		t.Fatalf("chat body: %s", data)
	}
	return out.Choices[0].Message.Content, resp.Header
}

func (n *network) me(a *client.Agent) *portal.Me {
	n.t.Helper()
	var st client.Status
	getJSON(n.t, "http://"+a.Addr()+"/api/status", &st)
	var me portal.Me
	getJSON(n.t, "http://"+a.Addr()+"/api/account", &me)
	return &me
}

func getJSON(t *testing.T, url string, out any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s: %d %s", url, resp.StatusCode, data)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatal(err)
	}
}

// Two users each share a model and buy from each other through relays; the
// coordinator is only ever reached through a relay; money moves between the
// users' accounts as the portal reports it; a dead relay is routed around.
func TestUsersTradeThroughRelays(t *testing.T) {
	n := newNetwork(t, 2)
	alice := n.user("alice", shareMock(mockUpstream(t, "mock-7b", false).URL, priceIn, priceOut))
	bob := n.user("bob", shareMock(mockUpstream(t, "mock-7b", false).URL, priceIn, priceOut))
	waitFor(t, 20*time.Second, "both offers", func() bool { return len(n.coord.Engine.Offers()) == 2 })

	// Bob buys: his own node is excluded, so Alice serves him.
	content, hdr := chat(t, bob)
	if content != "Hello, world!" || hdr.Get("X-Sharetoken-Verified") != "true" || hdr.Get("X-Sharetoken-Node") != alice.PeerID() {
		t.Fatalf("bob got %q from %s (verified %s)", content, hdr.Get("X-Sharetoken-Node"), hdr.Get("X-Sharetoken-Verified"))
	}
	// Alice buys: Bob serves her.
	_, hdr = chat(t, alice)
	if hdr.Get("X-Sharetoken-Node") != bob.PeerID() {
		t.Fatalf("alice was served by %s, want bob's node", hdr.Get("X-Sharetoken-Node"))
	}

	// Every connection to the coordinator runs through a relay.
	for _, a := range []*client.Agent{alice, bob} {
		conns := a.Host().Network().ConnsToPeer(n.coordH)
		if len(conns) == 0 {
			t.Fatal("no connection to the coordinator")
		}
		for _, c := range conns {
			if !p2p.IsRelayed(c) {
				t.Fatalf("direct connection to the coordinator: %s", c.RemoteMultiaddr())
			}
		}
		st := a.Status()
		if !st.Coordinator.Connected || !st.Coordinator.Relayed || st.Coordinator.Via == "" || len(st.Relays) != 2 {
			t.Fatalf("status: %+v", st.Coordinator)
		}
	}
	// The live link to the relay in use is pinged at every health check.
	waitFor(t, 10*time.Second, "live link quality", func() bool {
		q := bob.Status().Coordinator.Link
		return q != nil && q.Samples >= 3 && q.Relay == bob.Status().Coordinator.Via && q.AvgMillis > 0 && q.LossPct == 0
	})

	// Each paid 20 micros for the other's job and earned 19 (8% fee rounds to 1).
	waitFor(t, 10*time.Second, "settlements", func() bool {
		me := n.me(alice)
		return me.Usage.Sold.Jobs == 1 && me.Usage.Bought.Jobs == 1
	})
	for name, a := range map[string]*client.Agent{"alice": alice, "bob": bob} {
		me := n.me(a)
		if me.Balance.AvailableMicros != 5_000_000-20+19 || me.Usage.Bought.CompletionTokens != 5 || me.Usage.Sold.Micros != 19 {
			t.Fatalf("%s: balance %+v usage %+v", name, me.Balance, me.Usage)
		}
		if len(me.Devices) != 1 || me.Devices[0].Peer != a.PeerID() {
			t.Fatalf("%s devices: %+v", name, me.Devices)
		}
	}
	o, err := n.portal.Overview()
	if err != nil || !o.BooksBalance || o.PlatformFees != 2 {
		t.Fatalf("overview: %+v %v", o, err)
	}

	// The relay carrying Bob's coordinator traffic dies: Bob switches to the
	// other one, Alice is still reachable through her second reservation.
	via := bob.Status().Coordinator.Via
	for _, r := range n.relays {
		if ai, _ := p2p.ParseRelay(via); ai.ID == r.Host.ID() {
			_ = r.Close()
		}
	}
	waitFor(t, 30*time.Second, "bob to switch relays", func() bool {
		st := bob.Status()
		return st.Coordinator.Via != via && st.Coordinator.Connected
	})
	waitFor(t, 10*time.Second, "the live link to follow the new relay", func() bool {
		st := bob.Status()
		q := st.Coordinator.Link
		return q != nil && q.Relay == st.Coordinator.Via && q.AvgMillis > 0
	})
	waitFor(t, 30*time.Second, "alice's offer through the surviving relay", func() bool {
		for _, o := range n.coord.Engine.Offers() {
			if o.Node == alice.PeerID() && time.Unix(0, o.IssuedAt).After(time.Now().Add(-3*time.Second)) {
				return true
			}
		}
		return false
	})
	if content, hdr := chat(t, bob); content != "Hello, world!" || hdr.Get("X-Sharetoken-Node") != alice.PeerID() {
		t.Fatalf("after failover bob got %q from %s", content, hdr.Get("X-Sharetoken-Node"))
	}
}

// A peer that is not bound to a user reaches the coordinator through a relay
// but is refused; a disabled user's client is refused too.
func TestUnboundAndDisabledUsersAreRefused(t *testing.T) {
	n := newNetwork(t, 1)
	k, _ := p2p.NewKeys()
	h, err := p2p.NewHost(k, p2p.HostConfig{Listen: []string{"/ip4/127.0.0.1/tcp/0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	ri, _ := p2p.ParseRelay(n.relays[0].Addrs()[0])
	h.Peerstore().AddAddrs(n.coordH, p2p.CircuitAddrs(ri), time.Minute)
	var tr protocol.TicketResponse
	err = p2p.Call(n.ctx, h, n.coordH, protocol.CtrlProtocol, protocol.TypeTicketRequest,
		protocol.TicketRequest{Model: model, BudgetMicros: 10}, protocol.TypeTicket, &tr)
	if protocol.CodeOf(err) != protocol.CodeUnauthorized {
		t.Fatalf("unbound peer: want unauthorized, got %v", err)
	}

	alice := n.user("alice", shareMock(mockUpstream(t, "mock-7b", false).URL, priceIn, priceOut))
	bob := n.user("bob", nil)
	waitFor(t, 20*time.Second, "alice's offer", func() bool { return len(n.coord.Engine.Offers()) == 1 })
	chat(t, bob)

	pc := client.NewPortalClient(n.url, n.admin, "")
	if err := pc.Do(n.ctx, "POST", "/api/v1/admin/users/bob/disable", nil, nil); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post("http://"+bob.Addr()+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode == 200 || !strings.Contains(string(data), protocol.CodeUnauthorized) {
		t.Fatalf("disabled user: %d %s", resp.StatusCode, data)
	}
	_ = alice
}

// Relays reserve only for registered users' devices: a sharer gets its pass
// from the portal automatically; a peer with no account is refused; a
// disabled user gets no new pass.
func TestRelaysAdmitOnlyRegisteredDevices(t *testing.T) {
	n := newNetwork(t, 1)
	alice := n.user("alice", shareMock(mockUpstream(t, "mock-7b", false).URL, priceIn, priceOut))
	waitFor(t, 20*time.Second, "alice's reservation", func() bool {
		st := alice.Status()
		return len(st.Share.Reservations) == 1 && st.Share.Reservations[0].Reserved
	})
	if st := n.relays[0].Stats(); st.PassesAccepted == 0 {
		t.Fatalf("alice reserved without presenting a pass? %+v", st)
	}

	ri, _ := p2p.ParseRelay(n.relays[0].Addrs()[0])
	k, _ := p2p.NewKeys()
	h, err := p2p.NewHost(k, p2p.HostConfig{Listen: []string{"/ip4/127.0.0.1/tcp/0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if _, err := circuit.Reserve(n.ctx, h, ri); err == nil {
		t.Fatal("a peer with no account reserved on the relay")
	}
	// The portal signs passes only for the caller's own bound devices.
	pc := client.NewPortalClient(n.url, n.admin, "")
	if _, err := pc.RelayPass(n.ctx, k.ID()); err == nil {
		t.Fatal("portal issued a pass for a device that is not the caller's")
	}

	// A disabled user gets no new pass.
	if err := pc.Do(n.ctx, "POST", "/api/v1/admin/users/alice/disable", nil, nil); err != nil {
		t.Fatal(err)
	}
	u, _ := n.portal.Store().UserByName("alice")
	tok, _, _ := n.portal.Store().IssueToken(u.ID, "x", 0)
	if _, err := client.NewPortalClient(n.url, tok, "").RelayPass(n.ctx, alice.PeerID()); err == nil {
		t.Fatal("a disabled user got a relay pass")
	}
}

// The local API refuses cross-origin requests from web pages.
func TestLocalAPIRefusesWebPages(t *testing.T) {
	n := newNetwork(t, 1)
	bob := n.user("bob", nil)
	req, _ := http.NewRequest("POST", "http://"+bob.Addr()+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Origin", "https://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin request: %d", resp.StatusCode)
	}
}

// onlyFamily lets a host dial only one IP family, like a machine with only
// IPv4 or only IPv6 connectivity.
type onlyFamily struct{ proto int }

func (g onlyFamily) InterceptPeerDial(peer.ID) bool { return true }
func (g onlyFamily) InterceptAddrDial(_ peer.ID, a ma.Multiaddr) bool {
	_, err := a.ValueForProtocol(g.proto)
	return err == nil
}
func (g onlyFamily) InterceptAccept(lnet.ConnMultiaddrs) bool { return true }
func (g onlyFamily) InterceptSecured(lnet.Direction, peer.ID, lnet.ConnMultiaddrs) bool {
	return true
}
func (g onlyFamily) InterceptUpgraded(lnet.Conn) (bool, control.DisconnectReason) { return true, 0 }

func relayConnFamilies(a *client.Agent, relays []*relay.Relay) map[int]int {
	out := map[int]int{}
	for _, r := range relays {
		for _, c := range a.Host().Network().ConnsToPeer(r.Host.ID()) {
			if _, err := c.RemoteMultiaddr().ValueForProtocol(ma.P_IP6); err == nil {
				out[ma.P_IP6]++
			} else {
				out[ma.P_IP4]++
			}
		}
	}
	return out
}

// An IPv6-only user sells to an IPv4-only user and buys from it, through
// dual-stack relays; both reach the coordinator.
func TestIPv4AndIPv6OnlyUsersTrade(t *testing.T) {
	n := newNetwork(t, 2)
	six := n.userOn("six", shareMock(mockUpstream(t, "mock-7b", false).URL, priceIn, priceOut), "/ip6/::1/tcp/0")
	four := n.userOn("four", shareMock(mockUpstream(t, "mock-7b", false).URL, priceIn, priceOut), "/ip4/127.0.0.1/tcp/0")
	waitFor(t, 20*time.Second, "both offers", func() bool { return len(n.coord.Engine.Offers()) == 2 })

	if content, hdr := chat(t, four); content != "Hello, world!" || hdr.Get("X-Sharetoken-Node") != six.PeerID() {
		t.Fatalf("IPv4 buyer got %q from %s, want the IPv6 seller", content, hdr.Get("X-Sharetoken-Node"))
	}
	if content, hdr := chat(t, six); content != "Hello, world!" || hdr.Get("X-Sharetoken-Node") != four.PeerID() {
		t.Fatalf("IPv6 buyer got %q from %s, want the IPv4 seller", content, hdr.Get("X-Sharetoken-Node"))
	}
	// Each reached the relays only over its own family, and the other only
	// through a relay.
	if f := relayConnFamilies(six, n.relays); f[ma.P_IP4] != 0 || f[ma.P_IP6] == 0 {
		t.Fatalf("IPv6 user's relay connections: %v", f)
	}
	if f := relayConnFamilies(four, n.relays); f[ma.P_IP6] != 0 || f[ma.P_IP4] == 0 {
		t.Fatalf("IPv4 user's relay connections: %v", f)
	}
	for _, c := range four.Host().Network().ConnsToPeer(six.Host().ID()) {
		if !p2p.IsRelayed(c) {
			t.Fatalf("direct connection across families: %s", c.RemoteMultiaddr())
		}
	}
	for _, a := range []*client.Agent{six, four} {
		waitFor(t, 10*time.Second, "coordinator connected and the link pinged", func() bool {
			st := a.Status()
			return st.Coordinator.Connected && st.Coordinator.Link != nil && st.Coordinator.Link.LossPct == 0
		})
	}
}
