package relay

import (
	"context"
	"crypto/ed25519"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	lproto "github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	pbv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/pb"
	circuitproto "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/proto"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/util"
	ma "github.com/multiformats/go-multiaddr"

	"sharetoken/internal/p2p"
)

const echoProto = lproto.ID("/test/echo/1.0.0")

// hub starts a coordinator-like host (a hub that lists its relay as a trunk),
// reserved on r, answering echoProto.
func hub(t *testing.T, ctx context.Context, r *Relay, keys *p2p.Keys) host.Host {
	t.Helper()
	relayID := r.Host.ID()
	h, err := p2p.NewHost(keys, p2p.HostConfig{Listen: []string{"/ip4/127.0.0.1/tcp/0"}, MaxConns: 1024,
		Trunk: func(p peer.ID) bool { return p == relayID }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	h.SetStreamHandler(echoProto, func(s network.Stream) {
		defer s.Close()
		_, _ = io.Copy(s, s)
	})
	if _, err := client.Reserve(ctx, h, peer.AddrInfo{ID: relayID, Addrs: r.Host.Addrs()}); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	return h
}

// reach connects from to dest through r and exchanges a byte, leaving the
// connection open.
func reach(ctx context.Context, from host.Host, r *Relay, dest peer.ID) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	addrs := p2p.CircuitAddrs(peer.AddrInfo{ID: r.Host.ID(), Addrs: r.Host.Addrs()})
	if err := from.Connect(ctx, peer.AddrInfo{ID: dest, Addrs: addrs}); err != nil {
		return err
	}
	s, err := from.NewStream(network.WithAllowLimitedConn(ctx, "test"), dest, echoProto)
	if err != nil {
		return err
	}
	defer s.Close()
	_ = s.SetDeadline(time.Now().Add(20 * time.Second))
	if _, err := s.Write([]byte{7}); err != nil {
		return err
	}
	var b [1]byte
	if _, err := io.ReadFull(s, b[:]); err != nil {
		return err
	}
	return nil
}

// Every online user holds one circuit to the coordinator through its relay.
// libp2p's limits let a relay open 64 streams to one peer, and a peer take
// 128 of them: user 65 of a relay got "resource limit exceeded" and could not
// reach the coordinator at all. Hundreds get through.
func TestRelayCarriesManyUsersToTheCoordinator(t *testing.T) {
	const users = 300
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	portalPub, _, _ := ed25519.GenerateKey(nil)
	coordKeys, _ := p2p.NewKeys()
	rk, _ := p2p.NewKeys()
	// All of them come from 127.0.0.1 here, which is not what the test is about.
	r, err := New(rk, Config{Listen: []string{"/ip4/127.0.0.1/tcp/0"}, MaxCircuitsPerIP: 2 * users, Networks: []Network{{Name: "prod",
		PortalKeys: []ed25519.PublicKey{portalPub}, AllowPeers: []peer.ID{coordKeys.PeerID}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	coord := hub(t, ctx, r, coordKeys)

	errs := make([]error, users)
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for i := range users {
		h, _ := newHost(t)
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			errs[i] = reach(ctx, h, r, coord.ID())
		}()
	}
	wg.Wait()
	failed := 0
	for i, err := range errs {
		if err != nil {
			failed++
			if failed <= 3 {
				t.Logf("user %d: %v", i, err)
			}
		}
	}
	if failed > 0 {
		t.Fatalf("%d of %d users did not reach the coordinator", failed, users)
	}
	if st := r.Stats(); st.Circuits < users {
		t.Fatalf("%d circuits open, want %d", st.Circuits, users)
	}
}

// hop asks the relay for a circuit to dest on a stream of its own, as a peer
// does that wants to hold slots rather than use them, and returns the stream
// and the relay's answer.
func hop(ctx context.Context, from host.Host, r *Relay, dest peer.ID) (network.Stream, pbv2.Status, error) {
	s, err := from.NewStream(ctx, r.Host.ID(), circuitproto.ProtoIDv2Hop)
	if err != nil {
		return nil, 0, err
	}
	_ = s.SetDeadline(time.Now().Add(20 * time.Second))
	msg := pbv2.HopMessage{Type: pbv2.HopMessage_CONNECT.Enum(), Peer: util.PeerInfoToPeerV2(peer.AddrInfo{ID: dest})}
	if err := util.NewDelimitedWriter(s).WriteMsg(&msg); err != nil {
		_ = s.Reset()
		return nil, 0, err
	}
	rd := util.NewDelimitedReader(s, 4096)
	defer rd.Close()
	msg.Reset()
	if err := rd.ReadMsg(&msg); err != nil {
		_ = s.Reset()
		return nil, 0, err
	}
	return s, msg.GetStatus(), nil
}

// One peer gets MaxCircuitsPerPeer circuits and no more, however it asks,
// and what it holds leaves the others' way to the coordinator open.
func TestRelayLimitsCircuitsOfOnePeer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	portalPub, _, _ := ed25519.GenerateKey(nil)
	coordKeys, _ := p2p.NewKeys()
	rk, _ := p2p.NewKeys()
	r, err := New(rk, Config{Listen: []string{"/ip4/127.0.0.1/tcp/0"}, MaxCircuits: 40, Networks: []Network{{Name: "prod",
		PortalKeys: []ed25519.PublicKey{portalPub}, AllowPeers: []peer.ID{coordKeys.PeerID}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.cfg.MaxCircuitsPerPeer != 32 || r.cfg.MaxCircuitsPerIP != 128 {
		t.Fatalf("defaults: %d circuits per peer, %d per address", r.cfg.MaxCircuitsPerPeer, r.cfg.MaxCircuitsPerIP)
	}
	coord := hub(t, ctx, r, coordKeys)

	greedy, _ := newHost(t)
	if err := greedy.Connect(ctx, peer.AddrInfo{ID: r.Host.ID(), Addrs: r.Host.Addrs()}); err != nil {
		t.Fatal(err)
	}
	held := 0
	for i := range 60 {
		s, st, err := hop(ctx, greedy, r, coord.ID())
		if err != nil {
			continue // the resource manager refused the stream itself
		}
		if st != pbv2.Status_OK {
			_ = s.Reset()
			continue
		}
		defer s.Reset()
		held++
		if i >= r.cfg.MaxCircuitsPerPeer {
			t.Fatalf("circuit %d of one peer was granted", i+1)
		}
	}
	if held != r.cfg.MaxCircuitsPerPeer {
		t.Fatalf("one peer holds %d circuits, want %d", held, r.cfg.MaxCircuitsPerPeer)
	}
	// 40 slots to the coordinator, 32 of them taken: the next users still
	// get theirs.
	for i := range 5 {
		h, _ := newHost(t)
		if err := reach(ctx, h, r, coord.ID()); err != nil {
			t.Fatalf("user %d after one peer took its fill: %v", i, err)
		}
	}
}

// Peer IDs cost nothing, so the peers of one address share a limit as well.
func TestRelayLimitsCircuitsOfOneAddress(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	portalPub, _, _ := ed25519.GenerateKey(nil)
	coordKeys, _ := p2p.NewKeys()
	rk, _ := p2p.NewKeys()
	r, err := New(rk, Config{Listen: []string{"/ip4/127.0.0.1/tcp/0"}, MaxCircuitsPerPeer: 4, MaxCircuitsPerIP: 10, Networks: []Network{{Name: "prod",
		PortalKeys: []ed25519.PublicKey{portalPub}, AllowPeers: []peer.ID{coordKeys.PeerID}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	coord := hub(t, ctx, r, coordKeys)

	held := 0
	for range 4 { // four peers of 127.0.0.1, each asking for its four
		h, _ := newHost(t)
		if err := h.Connect(ctx, peer.AddrInfo{ID: r.Host.ID(), Addrs: r.Host.Addrs()}); err != nil {
			t.Fatal(err)
		}
		for range 4 {
			s, st, err := hop(ctx, h, r, coord.ID())
			if err != nil {
				t.Fatal(err)
			}
			if st != pbv2.Status_OK {
				_ = s.Reset()
				continue
			}
			defer s.Reset()
			held++
		}
	}
	if held != 10 {
		t.Fatalf("one address holds %d circuits, want 10", held)
	}
	if st := r.Stats(); st.CircuitsDenied != 6 {
		t.Fatalf("stats: %+v", st)
	}
	// The coordinator opens circuits too (audits), and no limit of a source
	// counts it.
	for i := range 12 {
		s, st, err := hop(ctx, coord, r, coord.ID())
		if err != nil || st != pbv2.Status_OK {
			t.Fatalf("circuit %d of the coordinator: %v %v", i+1, st, err)
		}
		defer s.Reset()
	}
}

func TestAddrKey(t *testing.T) {
	for addr, want := range map[string]string{
		"/ip4/203.0.113.7/tcp/4001":                                      "203.0.113.7/32",
		"/ip4/203.0.113.7/udp/4001/quic-v1":                              "203.0.113.7/32",
		"/ip6/2001:db8:1:1ff::9/tcp/4001":                                "2001:db8:1:100::/56",
		"/ip6/::ffff:203.0.113.7/tcp/4001":                               "203.0.113.7/32",
		"/dns4/relay.example/tcp/4001":                                   "",
		"/p2p/" + "12D3KooWGzBSvBh8AqLSqsJHiHVnDVaVHJ1uYJhkvFCxkC6HjuTH": "",
	} {
		got, ok := addrKey(ma.StringCast(addr))
		if (want == "") == ok || (ok && got.String() != want) {
			t.Errorf("%s: %v %v, want %q", addr, got, ok, want)
		}
	}
}
