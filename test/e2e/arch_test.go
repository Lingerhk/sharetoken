package e2e

import (
	"cmp"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/control"
	lnet "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	libp2pproto "github.com/libp2p/go-libp2p/core/protocol"
	circuit "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	ma "github.com/multiformats/go-multiaddr"

	"sharetoken/internal/client"
	"sharetoken/internal/coordinator"
	"sharetoken/internal/mysqldb"
	"sharetoken/internal/node"
	"sharetoken/internal/p2p"
	"sharetoken/internal/portal"
	"sharetoken/internal/protocol"
	"sharetoken/internal/relay"
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
	// healthEvery is the clients' health-check interval (default 200ms).
	healthEvery time.Duration
}

func newNetwork(t *testing.T, relays int, relayOpts ...func(*relay.Config)) *network {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	n := &network{t: t, ctx: ctx}
	dbPath, err := mysqldb.TestDSN()
	if err != nil {
		t.Fatal(err)
	}

	// Portal first: it creates the schema; the coordinator joins the database.
	pdb, err := mysqldb.Open(dbPath)
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
		rc := relay.Config{Listen: []string{"/ip4/127.0.0.1/tcp/0", "/ip6/::1/tcp/0"}, // dual stack
			Networks: []relay.Network{{Name: "test", PortalKeys: []ed25519.PublicKey{passPub}, AllowPeers: []peer.ID{ck.PeerID}}}}
		for _, o := range relayOpts {
			o(&rc)
		}
		r, err := relay.New(k, rc)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = r.Close() })
		n.relays = append(n.relays, r)
		if _, err := n.portal.Store().AddRelay(p2p.RelayAddr(peer.AddrInfo{ID: r.Host.ID(), Addrs: r.Host.Addrs()}), "relay-"+string(rune('a'+i)), "local"); err != nil {
			t.Fatal(err)
		}
	}

	cdb, err := mysqldb.Open(dbPath)
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
	fam := ma.P_IP4
	if strings.HasPrefix(listen, "/ip6/") {
		fam = ma.P_IP6
	}
	return n.userWith(name, share, listen, onlyFamily{fam}, nil)
}

// userWith starts a user whose host dials through gater; edit, if not nil,
// adjusts its configuration first.
func (n *network) userWith(name string, share *client.ShareConfig, listen string, gater connmgr.ConnectionGater, edit func(*client.Config)) *client.Agent {
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
	if edit != nil {
		edit(&cfg)
	}
	if err := client.SaveConfig(home, cfg); err != nil {
		n.t.Fatal(err)
	}
	a := client.NewAgent(home, cfg)
	a.HealthEvery, a.MeasureEvery = cmp.Or(n.healthEvery, 200*time.Millisecond), time.Hour
	a.HostOptions = []libp2p.Option{libp2p.ConnectionGater(gater)}
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
	getJSON(n.t, "http://"+a.Addr()+"/api/v1/status", &st)
	var me portal.Me
	getJSON(n.t, "http://"+a.Addr()+"/api/v1/account", &me)
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
// Relays known by host name (/dns4/..., as registered in production) work
// like IP addresses: the coordinator reserves on them and users trade.
func TestRelaysAnnouncedByHostName(t *testing.T) {
	n := newNetwork(t, 2, func(c *relay.Config) {
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := l.Addr().(*net.TCPAddr).Port
		_ = l.Close()
		c.Listen = []string{fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", port)}
		c.Announce = []string{fmt.Sprintf("/dns4/localhost/tcp/%d", port)}
	})
	for _, r := range n.relays {
		if a := r.Host.Addrs(); len(a) != 1 || !strings.HasPrefix(a[0].String(), "/dns4/localhost/") {
			t.Fatalf("relay addresses %v", a)
		}
	}
	alice := n.user("alice", shareMock(mockUpstream(t, "mock-7b", false).URL, priceIn, priceOut))
	bob := n.user("bob", nil)
	if !waitUntil(20*time.Second, func() bool { return len(n.coord.Engine.Offers()) == 1 }) {
		st, _ := json.MarshalIndent(alice.Status(), "", " ")
		t.Fatalf("no offer from alice; status %s", st)
	}
	content, hdr := chat(t, bob)
	if content != "Hello, world!" || hdr.Get("X-Sharetoken-Node") != alice.PeerID() {
		t.Fatalf("bob got %q from %s", content, hdr.Get("X-Sharetoken-Node"))
	}
	if st := bob.Status(); !st.Coordinator.Connected || !strings.Contains(st.Coordinator.Via, "/dns4/localhost/") {
		t.Fatalf("bob reaches the coordinator via %q", st.Coordinator.Via)
	}
}

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
		protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: model, BudgetMicros: 10}, protocol.TypeTicket, &tr)
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
	// A host name of the same family (a relay inside a circuit address is
	// not resolved before the gater sees it) counts as that family.
	dns := map[int]int{ma.P_IP4: ma.P_DNS4, ma.P_IP6: ma.P_DNS6}[g.proto]
	for _, p := range []int{g.proto, dns, ma.P_DNS} {
		if _, err := a.ValueForProtocol(p); err == nil {
			return true
		}
	}
	return false
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

// blackhole is an IPv4-only gater that refuses circuit dials to one peer
// through the relays in its set: those relays stay up and answer pings, but
// the peer cannot be reached through them (a relay on a congested link, or,
// with every relay in the set, the peer being down).
type blackhole struct {
	onlyFamily
	target peer.ID
	mu     sync.Mutex
	relays map[peer.ID]bool
}

func (g *blackhole) set(relays ...peer.ID) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.relays = map[peer.ID]bool{}
	for _, r := range relays {
		g.relays[r] = true
	}
}

func (g *blackhole) InterceptAddrDial(p peer.ID, a ma.Multiaddr) bool {
	if p == g.target {
		g.mu.Lock()
		defer g.mu.Unlock()
		for r := range g.relays {
			if strings.Contains(a.String(), "/p2p/"+r.String()+"/p2p-circuit") {
				return false
			}
		}
	}
	return g.onlyFamily.InterceptAddrDial(p, a)
}

// delayProxy forwards TCP connections to target, holding each chunk for
// delay in both directions: a relay on a slower path.
func delayProxy(t *testing.T, target string, delay time.Duration) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	pipe := func(dst, src net.Conn) {
		defer dst.Close()
		buf := make([]byte, 32<<10)
		for {
			k, err := src.Read(buf)
			if k > 0 {
				time.Sleep(delay)
				if _, werr := dst.Write(buf[:k]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			u, err := net.Dial("tcp", target)
			if err != nil {
				_ = c.Close()
				continue
			}
			go pipe(u, c)
			go pipe(c, u)
		}
	}()
	return l.Addr().String()
}

// A relay that answers pings fastest but cannot carry coordinator traffic is
// passed over once it has failed, instead of being picked again and again by
// RTT (seen in a distributed test: one relay taken over after another died), and a later
// measurement does not move the client back onto it.
func TestRelayThatFailsTheCoordinatorIsAvoided(t *testing.T) {
	n := newNetwork(t, 2)
	alice := n.user("alice", shareMock(mockUpstream(t, "mock-7b", false).URL, priceIn, priceOut))
	fast, slow := n.relays[0].Host, n.relays[1].Host
	var slowTCP string
	for _, a := range slow.Addrs() {
		if v, err := a.ValueForProtocol(ma.P_TCP); err == nil && strings.HasPrefix(a.String(), "/ip4/") {
			ip, _ := a.ValueForProtocol(ma.P_IP4)
			slowTCP = net.JoinHostPort(ip, v)
		}
	}
	host, port, _ := net.SplitHostPort(delayProxy(t, slowTCP, 25*time.Millisecond))
	slowAddr := p2p.RelayAddr(peer.AddrInfo{ID: slow.ID(), Addrs: []ma.Multiaddr{ma.StringCast("/ip4/" + host + "/tcp/" + port)}})
	fastAddr := p2p.RelayAddr(peer.AddrInfo{ID: fast.ID(), Addrs: fast.Addrs()})

	g := &blackhole{onlyFamily: onlyFamily{ma.P_IP4}, target: n.coordH}
	bob := n.userWith("bob", nil, "/ip4/127.0.0.1/tcp/0", g, func(c *client.Config) { c.Relays = []string{fastAddr, slowAddr} })
	waitFor(t, 20*time.Second, "alice's offer", func() bool { return len(n.coord.Engine.Offers()) == 1 })
	if via := bob.Status().Coordinator.Via; via != fastAddr {
		t.Fatalf("bob should start on the fast relay, is on %s", via)
	}
	chat(t, bob)

	// The coordinator is down (unreachable through every relay): no relay is
	// to blame, so bob stays on the fastest one instead of wandering onto
	// slower ones and avoiding them all.
	g.set(fast.ID(), slow.ID())
	_ = bob.Host().Network().ClosePeer(n.coordH)
	waitFor(t, 10*time.Second, "bob to notice the coordinator is gone", func() bool { return !bob.Status().Coordinator.Connected })
	for range 15 {
		bob.Kick()
		time.Sleep(200 * time.Millisecond)
		if via := bob.Status().Coordinator.Via; via != fastAddr {
			t.Fatalf("coordinator down: bob left the fastest relay for %s", via)
		}
	}
	g.set()
	waitFor(t, 30*time.Second, "bob back on the fast relay", func() bool {
		st := bob.Status()
		return st.Coordinator.Via == fastAddr && st.Coordinator.Connected
	})
	chat(t, bob)

	// The fast relay stops carrying coordinator traffic but keeps answering
	// pings, while the coordinator is reachable through the slow one.
	g.set(fast.ID())
	_ = bob.Host().Network().ClosePeer(n.coordH)
	waitFor(t, 30*time.Second, "bob to move to the slow relay", func() bool {
		st := bob.Status()
		return st.Coordinator.Via == slowAddr && st.Coordinator.Connected
	})
	for i := range 10 {
		res := bob.Remeasure(n.ctx)
		if len(res) != 2 || res[0].Addr != fastAddr || !res[0].OK() || !res[1].OK() || res[1].RTT < 20*time.Millisecond {
			t.Fatalf("measurement %d: want the bad relay fastest and both up: %+v", i, res)
		}
		if via := bob.Status().Coordinator.Via; via != slowAddr {
			t.Fatalf("measurement %d: bob moved back to the relay that cannot reach the coordinator", i)
		}
	}
	if content, hdr := chat(t, bob); content != "Hello, world!" || hdr.Get("X-Sharetoken-Node") != alice.PeerID() {
		t.Fatalf("after avoiding the relay bob got %q from %s", content, hdr.Get("X-Sharetoken-Node"))
	}
}

// When the relay carrying a buyer's coordinator traffic dies, the failed
// call itself triggers the failover (no periodic health check runs here),
// and the request that hit the dead path is retried and succeeds instead of
// failing.
func TestFailedCoordinatorCallTriggersFailover(t *testing.T) {
	n := newNetwork(t, 2)
	n.healthEvery = time.Hour // only kicks drive failover
	alice := n.user("alice", shareMock(mockUpstream(t, "mock-7b", false).URL, priceIn, priceOut))
	bob := n.user("bob", nil)
	waitFor(t, 20*time.Second, "alice's offer", func() bool { return len(n.coord.Engine.Offers()) == 1 })
	chat(t, bob)

	via := bob.Status().Coordinator.Via
	for _, r := range n.relays {
		if ai, _ := p2p.ParseRelay(via); ai.ID == r.Host.ID() {
			_ = r.Close()
		}
	}
	start := time.Now()
	content, hdr := chat(t, bob) // fails the test on any error
	if content != "Hello, world!" || hdr.Get("X-Sharetoken-Node") != alice.PeerID() {
		t.Fatalf("after the relay died bob got %q from %s", content, hdr.Get("X-Sharetoken-Node"))
	}
	if d := time.Since(start); d > 15*time.Second {
		t.Fatalf("failover took %s", d)
	}
	if st := bob.Status(); st.Coordinator.Via == via || !st.Coordinator.Connected {
		t.Fatalf("bob still on the dead relay: %+v", st.Coordinator)
	}
}

// A request that does not say its version is refused, and the coordinator's
// replies keep their protocol code.
func TestRequestsMustCarryAVersion(t *testing.T) {
	m := newMarket(t)
	h, _ := newHost(t)
	if err := h.Connect(m.ctx, m.info); err != nil {
		t.Fatal(err)
	}
	st, err := h.NewStream(lnet.WithAllowLimitedConn(m.ctx, "test"), m.info.ID, libp2pproto.ID(protocol.CtrlProtocol))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := protocol.Send(st, protocol.TypeBalance, nil); err != nil {
		t.Fatal(err)
	}
	env, err := protocol.Recv(st)
	if err != nil {
		t.Fatal(err)
	}
	var b protocol.BalanceResponse
	if err := env.Decode(protocol.TypeBalance, &b); protocol.CodeOf(err) != protocol.CodeBadRequest {
		t.Fatalf("got %v", err)
	}
}
