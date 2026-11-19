package p2p

import (
	"context"
	"testing"
	"time"

	"fmt"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	lproto "github.com/libp2p/go-libp2p/core/protocol"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	circuitproto "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/proto"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	"github.com/libp2p/go-libp2p/p2p/protocol/identify"
	"github.com/libp2p/go-libp2p/p2p/protocol/ping"
	ma "github.com/multiformats/go-multiaddr"
)

// A hub's system limits fit MaxConns connections, and the defaults it keeps
// per stream still bound what one stream may reserve.
func TestHubLimitsFitMaxConnsAndKeepStreamLimits(t *testing.T) {
	l := HubLimits(8192)
	sys := l.ToPartialLimitConfig().System
	if sys.Conns != 8192 || sys.ConnsInbound != 8192 || sys.FD != 8192 {
		t.Fatalf("system conns %+v, want 8192", sys)
	}
	if sys.StreamsInbound < 4*8192 {
		t.Fatalf("system inbound streams %d, want at least %d", sys.StreamsInbound, 4*8192)
	}
	if sys.Memory == rcmgr.Unlimited64 || sys.Memory == 0 {
		t.Fatalf("system memory must stay bounded, got %v", sys.Memory)
	}
	pc := l.ToPartialLimitConfig()
	for name, got := range map[string]rcmgr.ResourceLimits{
		"relay service": pc.Service[relayv2.ServiceName],
		"hop protocol":  pc.Protocol[circuitproto.ProtoIDv2Hop],
		"stop protocol": pc.Protocol[circuitproto.ProtoIDv2Stop],
		"ping service":  pc.Service[ping.ServiceName],
		"ping protocol": pc.Protocol[ping.ID],
		"identify":      pc.Service[identify.ServiceName],
	} {
		if got.StreamsInbound < 8192 || got.Streams < 2*8192 {
			t.Fatalf("%s streams %+v: every relayed connection is one long-lived stream", name, got)
		}
	}
	for name, l := range map[string]rcmgr.ConcreteLimitConfig{"hub": l, "client": ClientLimits()} {
		pp := l.ToPartialLimitConfig().ProtocolPeer[ping.ID]
		if pp.StreamsInbound < 4 || pp.StreamsOutbound < 4 {
			t.Fatalf("%s: one peer's concurrent pings %+v: a relay measurement overlapping the link monitor must not be refused", name, pp)
		}
	}
	st := l.ToPartialLimitConfig().Stream
	if st.Memory == rcmgr.Unlimited64 || int64(st.Memory) < 8<<20 {
		t.Fatalf("stream memory %v: a frame of MaxFrameSize must fit and more must not", st.Memory)
	}
}

// On a hub, a stream cannot reserve more memory than the stream limit: that is
// what protocol.RecvStream relies on to refuse a flood of large frames.
func TestHubRefusesOversizedStreamReservation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	hub := newTestHost(t, HostConfig{Listen: []string{"/ip4/127.0.0.1/tcp/0"}, MaxConns: 64})
	client := newTestHost(t, HostConfig{Listen: []string{"/ip4/127.0.0.1/tcp/0"}})

	const proto = "/test/reserve/1.0.0"
	result := make(chan error, 1)
	hub.SetStreamHandler(lproto.ID(proto), func(s network.Stream) {
		defer s.Close()
		if err := s.Scope().ReserveMemory(1<<20, network.ReservationPriorityLow); err != nil {
			result <- err
			return
		}
		s.Scope().ReleaseMemory(1 << 20)
		result <- s.Scope().ReserveMemory(64<<20, network.ReservationPriorityLow)
	})
	if err := client.Connect(ctx, peer.AddrInfo{ID: hub.ID(), Addrs: hub.Addrs()}); err != nil {
		t.Fatal(err)
	}
	s, err := client.NewStream(ctx, hub.ID(), lproto.ID(proto))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// Protocol negotiation is lazy: the handler runs once the client has sent something.
	if _, err := s.Write([]byte{0}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("a 64 MiB reservation on one stream was granted")
		}
	case <-ctx.Done():
		t.Fatal("no answer from the hub")
	}
}

// A node's offer lists every address it listens on, those of its own
// network among them, and from elsewhere a dial to one of them hangs until
// its timeout while holding one of the connections the peer may have.
// libp2p dials a relay address a moment after the direct ones: with its
// default of eight connections a peer, a node with a handful of interfaces
// had none left by then, the relay dial was refused, and the job waited for
// the timeouts (or for a tunnel).
func TestDeadAddressesLeaveRoomForLaterOnes(t *testing.T) {
	seller, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	defer seller.Close()
	late := seller.Addrs()[0]
	rm, err := rcmgr.NewResourceManager(rcmgr.NewFixedLimiter(ClientLimits()))
	if err != nil {
		t.Fatal(err)
	}
	// Every address at once, the one that answers a quarter of a second
	// later: where libp2p puts a relay.
	ranker := func(addrs []ma.Multiaddr) []network.AddrDelay {
		out := make([]network.AddrDelay, 0, len(addrs))
		for _, a := range addrs {
			d := time.Duration(0)
			if a.Equal(late) {
				d = 250 * time.Millisecond
			}
			out = append(out, network.AddrDelay{Addr: a, Delay: d})
		}
		return out
	}
	buyer, err := libp2p.New(libp2p.NoListenAddrs, libp2p.ResourceManager(rm), libp2p.SwarmOpts(swarm.WithDialRanker(ranker)))
	if err != nil {
		t.Fatal(err)
	}
	defer buyer.Close()
	// TEST-NET addresses: packets to them vanish.
	addrs := []ma.Multiaddr{late}
	for i := 1; i <= 16; i++ {
		addrs = append(addrs, ma.StringCast(fmt.Sprintf("/ip4/192.0.2.%d/tcp/4001", i)))
	}
	buyer.Peerstore().AddAddrs(seller.ID(), addrs, time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	t0 := time.Now()
	if err := buyer.Connect(ctx, peer.AddrInfo{ID: seller.ID()}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if took := time.Since(t0); took > 3*time.Second {
		t.Fatalf("connected after %v: the address had to wait for the dead ones", took)
	}
}
