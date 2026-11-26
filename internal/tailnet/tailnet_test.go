package tailnet

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/tailscale/tailcat"
	"tailscale.com/envknob"
	"tailscale.com/net/netcheck"
	"tailscale.com/syncs"
	"tailscale.com/tailcfg"
	"tailscale.com/tstest/integration"
	"tailscale.com/types/key"
	"tailscale.com/types/logger"

	"sharetoken/internal/p2p"
)

// The engines under test must stay off the real network: without tailcat's
// release build tags they would ask the default gateway for a port mapping
// and probe for captive portals at every start (see tailcat's own TestMain).
func TestMain(m *testing.M) {
	envknob.Setenv("IN_TS_TEST", "true")
	netcheck.HookStartCaptivePortalDetection.SetForTest(func(ctx context.Context, c *netcheck.Client, dm *tailcfg.DERPMap, preferredDERP tailcfg.DERPRegionID, setCaptivePortal func(bool)) (done <-chan struct{}, stop func()) {
		return syncs.ClosedChan(), func() {}
	})
	os.Exit(m.Run())
}

const echoProto = "/tailnet-test/echo/1"

// newHost is a libp2p host with the tailnet transport and no other way in:
// it listens nowhere, so a connection to it proves the tunnel.
func newHost(t *testing.T, dm *tailcfg.DERPMap) (host.Host, *Transport) {
	t.Helper()
	return newHostWith(t, Config{Enabled: true, derpMap: dm})
}

func newHostWith(t *testing.T, cfg Config) (host.Host, *Transport) {
	t.Helper()
	tn, opt, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h, err := libp2p.New(libp2p.NoListenAddrs, p2p.Transports(), opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close(); _ = tn.Close() })
	h.SetStreamHandler(echoProto, func(s network.Stream) {
		defer s.Close()
		_, _ = io.Copy(s, s)
	})
	return h, tn
}

func derpMap(t *testing.T) *tailcfg.DERPMap {
	t.Helper()
	return integration.RunDERPAndSTUN(t, logger.Discard, "127.0.0.1")
}

func echo(t *testing.T, h host.Host, p peer.ID, msg string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := h.NewStream(ctx, p, echoProto)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer s.Close()
	// Written beside the reading: an echo of megabytes fills the windows
	// of a sender that does not read.
	go func() {
		_, _ = s.Write([]byte(msg))
		_ = s.CloseWrite()
	}()
	got, err := io.ReadAll(s)
	if err != nil || string(got) != msg {
		t.Fatalf("echo = %d bytes %.40q, %v; want %d bytes %.40q", len(got), got, err, len(msg), msg)
	}
}

func TestMultiaddr(t *testing.T) {
	ci := tailcat.ConnInfo{ServerPublic: tailcat.NodePublic{NodePublic: key.NewNode().Public()}, RegionID: 301}
	ci.ServerDiscoPublic = tailcat.DiscoPublicForNode(key.NewNode())
	a := ci.Addr()
	m, err := Multiaddr(a)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := FromMultiaddr(m); !ok || got != a {
		t.Fatalf("FromMultiaddr = %q, %v; want %q", got, ok, a)
	}
	// It survives the text and binary forms an offer and the peerstore use.
	back, err := ma.NewMultiaddr(m.String())
	if err != nil || !back.Equal(m) {
		t.Fatalf("text round trip: %v, %v", back, err)
	}
	bin, err := ma.NewMultiaddrBytes(m.Bytes())
	if err != nil || !bin.Equal(m) {
		t.Fatalf("binary round trip: %v, %v", bin, err)
	}
	for _, bad := range []string{"/tailcat/xx", "/tailcat/tc", "/tailcat/tc$$$", "/tailcat/" + "tc" + strings.Repeat("A", maxAddrLen)} {
		if _, err := ma.NewMultiaddr(bad); err == nil {
			t.Errorf("%s parsed", bad[:min(len(bad), 30)])
		}
	}
	other, _ := ma.NewMultiaddr("/ip4/100.64.0.1/tcp/4001")
	if Is(other) || !Is(m) {
		t.Fatal("Is is wrong")
	}
	if got := Addrs([]ma.Multiaddr{other, m}); len(got) != 1 || !got[0].Equal(m) {
		t.Fatalf("Addrs = %v", got)
	}
}

// A node serves over the tailnet and a buyer reaches it by that address
// alone: libp2p authenticates the peer inside the tunnel and streams work.
func TestServeAndDial(t *testing.T) {
	dm := derpMap(t)
	seller, stn := newHost(t, dm)
	buyer, btn := newHost(t, dm)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	addr, err := stn.Serve(ctx, seller)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if !Is(addr) {
		t.Fatalf("Serve gave %s", addr)
	}
	// The host keeps it out of the addresses it tells every peer; an offer
	// takes it from Addr.
	for _, a := range seller.Addrs() {
		if Is(a) {
			t.Fatalf("host addresses %v show the tailnet address", seller.Addrs())
		}
	}
	if got := stn.Addr(); got == nil || !got.Equal(addr) {
		t.Fatalf("Addr = %v", got)
	}
	again, err := stn.Serve(ctx, seller)
	if err != nil || !again.Equal(addr) {
		t.Fatalf("second Serve = %v, %v", again, err)
	}

	if !btn.Reach(ctx, buyer, seller.ID(), []ma.Multiaddr{addr}, 20*time.Second) {
		t.Fatalf("Reach: no direct connection; status %+v", btn.Status())
	}
	echo(t, buyer, seller.ID(), "over the tunnel")

	conns := buyer.Network().ConnsToPeer(seller.ID())
	if len(conns) != 1 {
		t.Fatalf("%d connections", len(conns))
	}
	if conns[0].Stat().Limited {
		t.Fatal("the tunnel connection counts as limited")
	}
	if path, ok := btn.PathOf(conns[0]); !ok || path == "" {
		t.Fatalf("PathOf = %q, %v", path, ok)
	}
	if got := conns[0].RemotePeer(); got != seller.ID() {
		t.Fatalf("remote peer %s", got)
	}

	st := btn.Status()
	if len(st.Tunnels) != 1 || st.Tunnels[0].Dir != "out" || st.Tunnels[0].Peer != seller.ID().String() || st.Tunnels[0].Conns != 1 {
		t.Fatalf("buyer status %+v", st)
	}
	// QUIC unless told otherwise, on both ends.
	if st.Proto != ProtoQUIC || st.Tunnels[0].Proto != ProtoQUIC || conns[0].ConnState().Transport != "quic-v1" {
		t.Fatalf("not QUIC: status %+v, connection %+v", st, conns[0].ConnState())
	}
	in := seller.Network().ConnsToPeer(buyer.ID())
	if len(in) != 1 || in[0].ConnState().Transport != "quic-v1" || !Is(in[0].LocalMultiaddr()) {
		t.Fatalf("seller's connections %v", in)
	}
	if ss := stn.Status(); !ss.Serving || ss.Addr != addr.String() || ss.Region == "" {
		t.Fatalf("seller status %+v", ss)
	}

	// Closing the connection frees the tunnel's reference; a new dial uses
	// the same tunnel.
	_ = conns[0].Close()
	deadline := time.Now().Add(5 * time.Second)
	for btn.Status().Tunnels[0].Conns != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("tunnel still referenced: %+v", btn.Status())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !btn.Reach(ctx, buyer, seller.ID(), []ma.Multiaddr{addr}, 20*time.Second) {
		t.Fatal("second Reach failed")
	}
	echo(t, buyer, seller.ID(), "again")
	if n := len(btn.Status().Tunnels); n != 1 {
		t.Fatalf("%d tunnels after redial", n)
	}

	// Reset ends it all: no address, no tunnel, no connection.
	stn.Reset(seller)
	btn.Reset(buyer)
	if stn.Addr() != nil || stn.Status().Serving {
		t.Fatal("still serving")
	}
	if n := len(btn.Status().Tunnels) + len(buyer.Network().ConnsToPeer(seller.ID())); n != 0 {
		t.Fatalf("%d tunnels and connections after Reset", n)
	}
	// Off, the address is not dialed at all.
	btn.SetEnabled(false)
	if btn.CanDial(addr) || btn.Reach(ctx, buyer, seller.ID(), []ma.Multiaddr{addr}, time.Second) {
		t.Fatal("dialed while off")
	}
	if _, err := stn.Serve(ctx, seller); err != nil {
		t.Fatalf("Serve again: %v", err)
	}
	stn.SetEnabled(false)
	stn.Reset(seller)
	if _, err := stn.Serve(ctx, seller); err == nil {
		t.Fatal("served while off")
	}
}

// The peer behind a tunnel is whoever libp2p proves it is: an address that
// leads to another node than the one asked for does not connect.
func TestWrongPeerRefused(t *testing.T) {
	dm := derpMap(t)
	seller, stn := newHost(t, dm)
	other, _ := newHost(t, dm)
	buyer, btn := newHost(t, dm)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	addr, err := stn.Serve(ctx, seller)
	if err != nil {
		t.Fatal(err)
	}
	if btn.Reach(ctx, buyer, other.ID(), []ma.Multiaddr{addr}, 20*time.Second) {
		t.Fatal("connected to a peer through another peer's address")
	}
	if n := len(buyer.Network().ConnsToPeer(other.ID())) + len(buyer.Network().ConnsToPeer(seller.ID())); n != 0 {
		t.Fatalf("%d connections left", n)
	}
	// The failure keeps the next attempt off for a while.
	t0 := time.Now()
	if btn.Reach(ctx, buyer, other.ID(), []ma.Multiaddr{addr}, 20*time.Second) || time.Since(t0) > time.Second {
		t.Fatal("a failed peer was dialed again at once")
	}
}

// A buyer follows a seller's address only to DERP hosts of its own map.
func TestForeignRegionRefused(t *testing.T) {
	dm := derpMap(t)
	maps := &derpMaps{fixed: dm}
	ctx := context.Background()

	mk := func(host string) tailcat.ConnInfo {
		ci := tailcat.ConnInfo{ServerPublic: tailcat.NodePublic{NodePublic: key.NewNode().Public()}}
		ci.ServerDiscoPublic = tailcat.DiscoPublicForNode(key.NewNode())
		ci.Region = []*tailcfg.DERPRegion{{RegionID: 1, Nodes: []*tailcfg.DERPNode{{Name: "x", RegionID: 1, HostName: host, IPv4: "10.1.2.3", DERPPort: 8443}}}}
		// Through the wire form, as a buyer gets it.
		back, err := tailcat.ParseAddr(ci.Addr())
		if err != nil {
			t.Fatal(err)
		}
		return back
	}
	evil := mk("internal.example")
	if err := maps.trusted(ctx, &evil); err != errUnknownRegion {
		t.Fatalf("foreign region: %v", err)
	}
	// A known host with the seller's own idea of its address and port: the
	// buyer's map wins.
	known := mk("127.0.0.1")
	if err := maps.trusted(ctx, &known); err != nil {
		t.Fatal(err)
	}
	n, want := known.Region[0].Nodes[0], dm.Regions[1].Nodes[0]
	if n.IPv4 != want.IPv4 || n.DERPPort != want.DERPPort {
		t.Fatalf("node %+v, want the map's %+v", n, want)
	}
	// By region number.
	short := tailcat.ConnInfo{ServerPublic: evil.ServerPublic, ServerDiscoPublic: evil.ServerDiscoPublic, RegionID: 1}
	if err := maps.trusted(ctx, &short); err != nil || len(short.Region) != 1 || short.RegionID != 0 {
		t.Fatalf("by number: %v, %+v", err, short)
	}
	missing := tailcat.ConnInfo{ServerPublic: evil.ServerPublic, ServerDiscoPublic: evil.ServerDiscoPublic, RegionID: 99}
	if err := maps.trusted(ctx, &missing); err != errUnknownRegion {
		t.Fatalf("missing region: %v", err)
	}

	// And through the transport: the dial fails before anything is sent.
	buyer, btn := newHost(t, dm)
	foreign := mk("internal.example")
	m, err := Multiaddr(foreign.Addr())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := btn.Dial(ctx, m, buyer.ID()); err == nil || !strings.Contains(err.Error(), "outside this client's DERP map") {
		t.Fatalf("Dial: %v", err)
	}
	if n := len(btn.Status().Tunnels); n != 0 {
		t.Fatalf("%d tunnels", n)
	}
}

// Idle connections and unused tunnels are closed.
func TestSweep(t *testing.T) {
	dm := derpMap(t)
	seller, stn := newHost(t, dm)
	buyer, btn := newHost(t, dm)
	btn.cfg.IdleClose = time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	addr, err := stn.Serve(ctx, seller)
	if err != nil {
		t.Fatal(err)
	}
	if !btn.Reach(ctx, buyer, seller.ID(), []ma.Multiaddr{addr}, 20*time.Second) {
		t.Fatal("Reach failed")
	}
	echo(t, buyer, seller.ID(), "x")
	idle := map[network.Conn]time.Time{}
	now := time.Now()
	waitStreams := time.Now().Add(5 * time.Second)
	for len(buyer.Network().ConnsToPeer(seller.ID())[0].GetStreams()) > 0 && time.Now().Before(waitStreams) {
		time.Sleep(20 * time.Millisecond)
	}
	btn.sweep(buyer, idle, now) // notes the connection as idle
	if len(buyer.Network().ConnsToPeer(seller.ID())) != 1 {
		t.Fatal("closed too early")
	}
	btn.sweep(buyer, idle, now.Add(30*time.Second))
	if len(buyer.Network().ConnsToPeer(seller.ID())) != 1 {
		t.Fatal("closed before the idle time")
	}
	// A stream that came and went between two looks counts as use: the
	// connection of a job a minute is not idle.
	var use *lastUse
	if c := buyer.Network().ConnsToPeer(seller.ID())[0]; !c.As(&use) || use.at.Load() == 0 {
		t.Fatalf("the connection does not note its use (%v)", use)
	}
	use.at.Store(now.Add(45 * time.Second).UnixNano())
	btn.sweep(buyer, idle, now.Add(61*time.Second))
	btn.sweep(buyer, idle, now.Add(104*time.Second))
	if len(buyer.Network().ConnsToPeer(seller.ID())) != 1 {
		t.Fatal("closed a minute after the first look, 16 seconds after its last stream")
	}
	btn.sweep(buyer, idle, now.Add(106*time.Second))
	deadline := time.Now().Add(5 * time.Second)
	for len(buyer.Network().ConnsToPeer(seller.ID())) != 0 || btn.Status().Tunnels[0].Conns != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("idle connection not closed: %+v", btn.Status())
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The tunnel lingers, then goes.
	btn.sweep(buyer, idle, time.Now().Add(tunnelLinger/2))
	if len(btn.Status().Tunnels) != 1 {
		t.Fatal("tunnel closed before its linger")
	}
	btn.sweep(buyer, idle, time.Now().Add(tunnelLinger+time.Second))
	if n := len(btn.Status().Tunnels); n != 0 {
		t.Fatalf("%d tunnels after the linger", n)
	}
}

func TestDERPMapSources(t *testing.T) {
	dir := t.TempDir()
	if _, err := newDERPMaps(filepath.Join(dir, "none.json"), ""); err == nil {
		t.Fatal("missing file accepted")
	}
	bad := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(bad, []byte(`{"Regions":{}}`), 0o600)
	if _, err := newDERPMaps(bad, ""); err == nil {
		t.Fatal("empty map accepted")
	}
	good := filepath.Join(dir, "good.json")
	data, _ := json.Marshal(&tailcfg.DERPMap{Regions: map[tailcfg.DERPRegionID]*tailcfg.DERPRegion{900: {RegionID: 900, RegionCode: "own", Nodes: []*tailcfg.DERPNode{{Name: "a", RegionID: 900, HostName: "derp.example"}}}}})
	_ = os.WriteFile(good, data, 0o600)
	d, err := newDERPMaps(good, dir)
	if err != nil {
		t.Fatal(err)
	}
	dm, err := d.get(context.Background())
	if err != nil || dm.Regions[900] == nil || d.source() != "file" {
		t.Fatalf("file map: %v, %v, %s", dm, err, d.source())
	}
	if d, _ := newDERPMaps("", ""); d.source() != tailcat.DefaultDERPMapURL {
		t.Fatalf("default source %s", d.source())
	}
	if d, _ := newDERPMaps("https://derp.example/map.json", ""); d.source() != "https://derp.example/map.json" {
		t.Fatalf("url source %s", d.source())
	}

	// The cache on disk answers for the URL it was stored under only.
	c := &fileCache{path: filepath.Join(dir, derpCacheFile)}
	if _, _, _, ok := c.Get("https://a"); ok {
		t.Fatal("empty cache answered")
	}
	if err := c.Put("https://a", data, "etag1"); err != nil {
		t.Fatal(err)
	}
	got, etag, at, ok := c.Get("https://a")
	if !ok || string(got) != string(data) || etag != "etag1" || time.Since(at) > time.Minute {
		t.Fatalf("cache: %s %s %v %v", got, etag, at, ok)
	}
	if _, _, _, ok := c.Get("https://b"); ok {
		t.Fatal("cache answered for another URL")
	}
	if err := c.Put("https://a", []byte("not json"), ""); err == nil {
		t.Fatal("garbage stored")
	}
}

// The client's build tags are the tailcat library's own, plus ts_omit_ssh:
// they are a copy (scripts/build/client-tags.txt), and it must follow the
// tailcat version in go.mod.
func TestClientBuildTagsFollowTailcat(t *testing.T) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/tailscale/tailcat").Output()
	if err != nil {
		t.Skipf("go list: %v", err)
	}
	theirs, err := os.ReadFile(filepath.Join(strings.TrimSpace(string(out)), "build-tags.txt"))
	if err != nil {
		t.Skipf("tailcat's tags: %v", err)
	}
	ours, err := os.ReadFile(filepath.Join("..", "..", "scripts", "build", "client-tags.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.TrimSpace(string(theirs)) + ",ts_omit_ssh"; strings.TrimSpace(string(ours)) != want {
		t.Fatal("scripts/build/client-tags.txt differs from tailcat's build-tags.txt: write it again (scripts/build/tags.sh says how)")
	}
}

// A machine on a Tailscale network of its own: its listeners are announced
// at that network's addresses, and a peer's such addresses are dialed only
// from a machine that is on one too.
func TestSystemTailscaleAddrs(t *testing.T) {
	mm := func(ss ...string) []ma.Multiaddr {
		var out []ma.Multiaddr
		for _, s := range ss {
			out = append(out, ma.StringCast(s))
		}
		return out
	}
	str := func(ms []ma.Multiaddr) string {
		var out []string
		for _, m := range ms {
			out = append(out, m.String())
		}
		return strings.Join(out, " ")
	}
	setIPs := func(ips ...string) {
		var addrs []netip.Addr
		for _, s := range ips {
			addrs = append(addrs, netip.MustParseAddr(s))
		}
		interfaceIPs = func() []netip.Addr { return addrs }
		systemMu.Lock()
		systemAt = time.Time{}
		systemMu.Unlock()
	}
	old := interfaceIPs
	defer func() {
		interfaceIPs = old
		systemMu.Lock()
		systemAt = time.Time{}
		systemMu.Unlock()
	}()

	listen := mm("/ip4/0.0.0.0/tcp/4001", "/ip4/0.0.0.0/udp/4001/quic-v1", "/ip6/::/tcp/4002", "/ip4/127.0.0.1/tcp/9")
	setIPs("192.168.1.5", "127.0.0.1", "100.101.102.103", "fd7a:115c:a1e0::1234", "fe80::1")
	want := "/ip4/100.101.102.103/tcp/4001 /ip4/100.101.102.103/udp/4001/quic-v1 /ip6/fd7a:115c:a1e0::1234/tcp/4002"
	if got := str(SystemAddrs(listen)); got != want {
		t.Fatalf("announced %q, want %q", got, want)
	}
	peer := mm("/ip4/100.64.0.9/tcp/1", "/ip4/10.0.0.9/tcp/1", "/ip4/8.8.8.8/tcp/1",
		"/ip4/100.64.0.7/tcp/1/p2p/12D3KooWPbcVN1DgJfooTyepsUg4kkNApbyBVJgTQ3fzBq9B8tEF/p2p-circuit", "/ip6/fd7a:115c:a1e0::9/udp/2/quic-v1", "/ip4/100.128.0.1/tcp/1")
	if got := str(systemPeerAddrs(peer)); got != "/ip4/100.64.0.9/tcp/1 /ip6/fd7a:115c:a1e0::9/udp/2/quic-v1" {
		t.Fatalf("dialed %q", got)
	}

	setIPs("192.168.1.5", "127.0.0.1")
	if got := SystemAddrs(listen); len(got) != 0 {
		t.Fatalf("announced %v without a Tailscale address", got)
	}
	if got := systemPeerAddrs(peer); len(got) != 0 {
		t.Fatalf("dialed %v without being on a Tailscale network", got)
	}
}

// A node announces many addresses a buyer cannot reach (its LAN, its
// container bridges): their dials wait for timeouts, each holding one of the
// connections libp2p allows per peer, and must not leave the tunnel without.
func TestTunnelAmongUnreachableAddrs(t *testing.T) {
	dm := derpMap(t)
	seller, stn := newHost(t, dm)
	// The buyer with a client's limits: a test host's own scale with the
	// machine's memory.
	btn, opt, err := New(Config{Enabled: true, derpMap: dm})
	if err != nil {
		t.Fatal(err)
	}
	rm, err := rcmgr.NewResourceManager(rcmgr.NewFixedLimiter(p2p.ClientLimits()))
	if err != nil {
		t.Fatal(err)
	}
	buyer, err := libp2p.New(libp2p.NoListenAddrs, libp2p.ResourceManager(rm), p2p.Transports(), opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = buyer.Close(); _ = btn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	addr, err := stn.Serve(ctx, seller)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	// TEST-NET addresses: packets to them vanish.
	var dead []ma.Multiaddr
	for i := 1; i <= 6; i++ {
		dead = append(dead, ma.StringCast(fmt.Sprintf("/ip4/192.0.2.%d/tcp/4001", i)),
			ma.StringCast(fmt.Sprintf("/ip4/192.0.2.%d/udp/4001/quic-v1", i)))
	}
	buyer.Peerstore().AddAddrs(seller.ID(), dead, time.Hour)
	if got := dialRanker(append(dead[:2:2], addr)); len(got) != 3 || !Is(got[0].Addr) || got[0].Delay != 0 {
		t.Fatalf("dial order %v: the tunnel is not first", got)
	}
	t0 := time.Now()
	if !btn.Reach(ctx, buyer, seller.ID(), []ma.Multiaddr{addr}, 20*time.Second) {
		t.Fatalf("Reach: no direct connection; status %+v", btn.Status())
	}
	// Dialed after them, the tunnel starts when their timeouts free a
	// connection (5 s), or never.
	if took := time.Since(t0); took > 4*time.Second {
		t.Fatalf("the tunnel took %v: it waited for the dead addresses", took)
	}
	echo(t, buyer, seller.ID(), "past the dead addresses")
}

// waitUnused waits until the transport's one tunnel has no connection left.
func waitUnused(t *testing.T, tn *Transport) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for tn.Status().Tunnels[0].Conns != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("tunnel still referenced: %+v", tn.Status())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// sink counts what a peer sends and answers with the count.
const sinkProto = "/tailnet-test/sink/1"

func sinkHandler(h host.Host) {
	h.SetStreamHandler(sinkProto, func(s network.Stream) {
		defer s.Close()
		n, _ := io.Copy(io.Discard, s)
		_, _ = fmt.Fprintf(s, "%d", n)
	})
}

func sendTo(t *testing.T, h host.Host, p peer.ID, size int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	s, err := h.NewStream(ctx, p, sinkProto)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	defer s.Close()
	if _, err := s.Write(make([]byte, size)); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = s.CloseWrite()
	got, err := io.ReadAll(s)
	if err != nil || string(got) != fmt.Sprint(size) {
		t.Fatalf("the node counted %q, %v; sent %d", got, err, size)
	}
}

// A buyer set to TCP connects as before the tunnel had QUIC, to the same
// node that serves QUIC to the others; changing the setting takes effect
// with the next connection.
func TestTCPThroughTheTunnel(t *testing.T) {
	dm := derpMap(t)
	seller, stn := newHost(t, dm)
	sinkHandler(seller)
	buyer, btn := newHostWith(t, Config{Enabled: true, Proto: ProtoTCP, derpMap: dm})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	addr, err := stn.Serve(ctx, seller)
	if err != nil {
		t.Fatal(err)
	}
	if !btn.Reach(ctx, buyer, seller.ID(), []ma.Multiaddr{addr}, 20*time.Second) {
		t.Fatalf("Reach: %+v", btn.Status())
	}
	sendTo(t, buyer, seller.ID(), 1<<20)
	conns := buyer.Network().ConnsToPeer(seller.ID())
	if st := btn.Status(); len(conns) != 1 || conns[0].ConnState().Transport == "quic-v1" || st.Proto != ProtoTCP || st.Tunnels[0].Proto != ProtoTCP {
		t.Fatalf("not TCP: %+v, %+v", conns, st)
	}
	var use *lastUse
	if !conns[0].As(&use) || use.at.Load() == 0 {
		t.Fatalf("the TCP connection does not note its use (%v)", use)
	}

	if err := btn.SetProto("sctp"); err == nil {
		t.Fatal("an unknown transport was taken")
	}
	if err := btn.SetProto(ProtoQUIC); err != nil {
		t.Fatal(err)
	}
	_ = conns[0].Close()
	waitUnused(t, btn)
	if !btn.Reach(ctx, buyer, seller.ID(), []ma.Multiaddr{addr}, 20*time.Second) {
		t.Fatalf("Reach after the change: %+v", btn.Status())
	}
	sendTo(t, buyer, seller.ID(), 1<<20)
	conns = buyer.Network().ConnsToPeer(seller.ID())
	if len(conns) != 1 || conns[0].ConnState().Transport != "quic-v1" || btn.Status().Tunnels[0].Proto != ProtoQUIC {
		t.Fatalf("not QUIC after the change: %+v, %+v", conns, btn.Status())
	}
}

// A node that serves the tunnel over TCP only (one from before the tunnel
// had QUIC) answers no QUIC handshake: the dial fails when the handshake
// times out, the buyer stays on the node's other paths, and a buyer set to
// TCP reaches it.
func TestQUICToANodeWithoutIt(t *testing.T) {
	dm := derpMap(t)
	seller, stn := newHostWith(t, Config{Enabled: true, derpMap: dm, tcpOnly: true})
	old := quicHandshake
	quicHandshake = 2 * time.Second
	t.Cleanup(func() { quicHandshake = old })
	buyer, btn := newHost(t, dm) // takes the short handshake timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	addr, err := stn.Serve(ctx, seller)
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	if btn.Reach(ctx, buyer, seller.ID(), []ma.Multiaddr{addr}, 20*time.Second) {
		t.Fatalf("connected over QUIC to a node without it: %+v", buyer.Network().ConnsToPeer(seller.ID()))
	}
	if d := time.Since(t0); d > 8*time.Second {
		t.Fatalf("the dial took %v to give up", d)
	}
	if n := len(buyer.Network().ConnsToPeer(seller.ID())); n != 0 {
		t.Fatalf("%d connections left", n)
	}

	tcpBuyer, ttn := newHostWith(t, Config{Enabled: true, Proto: ProtoTCP, derpMap: dm})
	if !ttn.Reach(ctx, tcpBuyer, seller.ID(), []ma.Multiaddr{addr}, 20*time.Second) {
		t.Fatalf("Reach over TCP: %+v", ttn.Status())
	}
	echo(t, tcpBuyer, seller.ID(), "over TCP")
}

// Streams over the tunnel's QUIC carry megabytes both ways, several at
// once, and a node takes more buyers than one address block may have
// connections (every tunnel address is in the same block).
func TestQUICStreamsAndManyBuyers(t *testing.T) {
	dm := derpMap(t)
	seller, stn := newHost(t, dm)
	sinkHandler(seller)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	addr, err := stn.Serve(ctx, seller)
	if err != nil {
		t.Fatal(err)
	}
	const buyers = 10
	var wg sync.WaitGroup
	for i := range buyers {
		buyer, btn := newHost(t, dm)
		wg.Go(func() {
			if !btn.Reach(ctx, buyer, seller.ID(), []ma.Multiaddr{addr}, 30*time.Second) {
				t.Errorf("buyer %d: no connection: %+v", i, btn.Status())
				return
			}
			if i > 1 {
				echo(t, buyer, seller.ID(), fmt.Sprint("buyer ", i))
				return
			}
			var inner sync.WaitGroup
			for range 3 {
				inner.Go(func() { sendTo(t, buyer, seller.ID(), 3<<20) })
			}
			big := strings.Repeat("0123456789abcdef", 1<<17) // 2 MB there and back
			echo(t, buyer, seller.ID(), big)
			inner.Wait()
		})
	}
	wg.Wait()
	if t.Failed() {
		return
	}
	in := 0
	for _, c := range seller.Network().Conns() {
		if c.ConnState().Transport == "quic-v1" && Is(c.LocalMultiaddr()) {
			in++
		}
	}
	if in != buyers {
		t.Fatalf("the node has %d QUIC connections from %d buyers", in, buyers)
	}
}

// Tests do not wait long for a direct path: the test network's comes at
// once or, now and then, not for seconds.
func init() { settleTime = time.Second }

// A QUIC connection starts when the tunnel is on its direct path, not while
// it still runs through the relay it started on.
func TestQUICWaitsForTheDirectPath(t *testing.T) {
	old := settleTime
	settleTime = 15 * time.Second
	t.Cleanup(func() { settleTime = old })
	dm := derpMap(t)
	seller, stn := newHost(t, dm)
	buyer, btn := newHost(t, dm)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	addr, err := stn.Serve(ctx, seller)
	if err != nil {
		t.Fatal(err)
	}
	if !btn.Reach(ctx, buyer, seller.ID(), []ma.Multiaddr{addr}, 25*time.Second) {
		t.Fatalf("Reach: %+v", btn.Status())
	}
	if st := btn.Status(); st.Tunnels[0].Proto != ProtoQUIC || st.Tunnels[0].Path != "direct" {
		t.Fatalf("the connection stands and the tunnel is not on its direct path: %+v", st.Tunnels[0])
	}
}

// The addresses an attempt dials outlive identify's sweep of a peer's
// temporary addresses, which runs when the last connection to the peer ends:
// an attempt that starts at that moment still finds them.
func TestReachAddrsOutliveTheSweep(t *testing.T) {
	dm := derpMap(t)
	seller, stn := newHost(t, dm)
	buyer, btn := newHost(t, dm)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	addr, err := stn.Serve(ctx, seller)
	if err != nil {
		t.Fatal(err)
	}
	if !btn.Reach(ctx, buyer, seller.ID(), []ma.Multiaddr{addr}, 20*time.Second) {
		t.Fatalf("Reach: %+v", btn.Status())
	}
	// What identify does when the last connection ends.
	buyer.Peerstore().UpdateAddrs(seller.ID(), peerstore.TempAddrTTL, 0)
	if got := Addrs(buyer.Peerstore().Addrs(seller.ID())); len(got) != 1 {
		t.Fatalf("the tunnel address was swept: %v", buyer.Peerstore().Addrs(seller.ID()))
	}

	// The node's tunnel restarts: the next attempt forgets the address
	// before.
	stn.Reset(seller)
	next, err := stn.Serve(ctx, seller)
	if err != nil || next.Equal(addr) {
		t.Fatalf("Serve after the reset: %v, %v", next, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for HasDirect(buyer, seller.ID()) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !btn.Reach(ctx, buyer, seller.ID(), []ma.Multiaddr{next}, 20*time.Second) {
		t.Fatalf("Reach at the new address: %+v", btn.Status())
	}
	if got := Addrs(buyer.Peerstore().Addrs(seller.ID())); len(got) != 1 || !got[0].Equal(next) {
		t.Fatalf("tunnel addresses after the restart: %v", got)
	}
}
