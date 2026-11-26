package coordinator

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"sharetoken/internal/mysqldb"
	"sharetoken/internal/p2p"
)

// The coordinator's host limits its relays as trunks (every user's stream
// arrives through one), and a peer's limit is fixed when it first connects:
// the host must know a relay before the coordinator dials it.
func TestRelaysAreTrunksBeforeTheyAreDialed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, err := mysqldb.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	local := []string{"/ip4/127.0.0.1/tcp/0"}
	rh, err := p2p.NewHost(mustKeys(t), p2p.HostConfig{Listen: local})
	if err != nil {
		t.Fatal(err)
	}
	defer rh.Close()
	relay := p2p.RelayAddr(peer.AddrInfo{ID: rh.ID(), Addrs: rh.Addrs()})

	var relays p2p.PeerSet
	ck := mustKeys(t)
	ch, err := p2p.NewHost(ck, p2p.HostConfig{Listen: local, MaxConns: 64, Trunk: relays.Has})
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	// What the host was told when the relay connected.
	known := make(chan bool, 1)
	ch.Network().Notify(&network.NotifyBundle{ConnectedF: func(_ network.Network, c network.Conn) {
		if c.RemotePeer() == rh.ID() {
			select {
			case known <- relays.Has(rh.ID()):
			default:
			}
		}
	}})
	svc, err := NewService(ctx, ch, ck, Config{DB: db, Relays: []string{relay}, RelayPeers: relays.Set})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	select {
	case ok := <-known:
		if !ok {
			t.Fatal("the relay was dialed before the host knew it for one")
		}
	case <-ctx.Done():
		t.Fatal("the coordinator did not dial its relay")
	}
	if relays.Has(ch.ID()) {
		t.Fatal("a peer that is no relay counts as one")
	}
}
