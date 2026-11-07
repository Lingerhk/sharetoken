package p2p

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	lproto "github.com/libp2p/go-libp2p/core/protocol"
)

// A host that listens only on loopback is reached by another host through
// the relay it holds a reservation on; the connection is relayed, and the
// Reserver re-reserves after the relay connection drops.
func TestReserverMakesAHostReachableThroughARelay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	relay := newRelayHost(t)
	relayAddr := FullAddrs(relay)[0]

	target := newTestHost(t, HostConfig{Listen: []string{"/ip4/127.0.0.1/tcp/0"}})
	changes := make(chan struct{}, 8)
	r := NewReserver(target, func() { changes <- struct{}{} })
	if err := r.SetRelays([]string{relayAddr}); err != nil {
		t.Fatal(err)
	}
	go r.Run(ctx)
	select {
	case <-changes:
	case <-ctx.Done():
		t.Fatal("no reservation")
	}
	if r.Reserved() != 1 || len(r.CircuitAddrs()) == 0 {
		t.Fatalf("status %+v", r.Status())
	}
	// A reserved relay is pinged; the round trip shows up in the status.
	for deadline := time.Now().Add(15 * time.Second); ; {
		r.Kick()
		time.Sleep(100 * time.Millisecond)
		if st := r.Status(); len(st) == 1 && st[0].RTT > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no ping result: %+v", r.Status())
		}
	}
	target.SetStreamHandler("/test/echo", func(s network.Stream) { _, _ = s.Write([]byte("hi")); s.Close() })

	// The caller knows the target only by its circuit address.
	caller := newTestHost(t, HostConfig{Listen: []string{"/ip4/127.0.0.1/tcp/0"}})
	caller.Peerstore().AddAddrs(target.ID(), r.CircuitAddrs(), time.Minute)
	s, err := caller.NewStream(network.WithAllowLimitedConn(ctx, "test"), target.ID(), lproto.ID("/test/echo"))
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2)
	if _, err := s.Read(buf); err != nil || string(buf) != "hi" {
		t.Fatalf("read %q %v", buf, err)
	}
	if !IsRelayed(s.Conn()) {
		t.Fatalf("connection is not relayed: %s", s.Conn().RemoteMultiaddr())
	}

	// Drop the relay connection: the reservation is lost and renewed.
	_ = target.Network().ClosePeer(relay.ID())
	deadline := time.Now().Add(20 * time.Second)
	for {
		r.Kick()
		time.Sleep(200 * time.Millisecond)
		if target.Network().Connectedness(relay.ID()) == network.Connected && r.Reserved() == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("reservation not renewed: %+v", r.Status())
		}
	}
}

func TestMeasureRelaysSortsReachableFirst(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	relay := newRelayHost(t)
	dead := newRelayHost(t)
	deadAddr := FullAddrs(dead)[0]
	_ = dead.Close()
	h := newTestHost(t, HostConfig{Listen: []string{"/ip4/127.0.0.1/tcp/0"}})
	res := MeasureRelays(ctx, h, []string{deadAddr, FullAddrs(relay)[0], "garbage"}, 3)
	if len(res) != 3 || !res[0].OK() || res[0].ID != relay.ID().String() || res[0].RTT <= 0 {
		t.Fatalf("unexpected order: %+v", res)
	}
	if res[1].OK() || res[2].OK() {
		t.Fatalf("dead relays reported reachable: %+v", res)
	}
}
