package coordinator

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"sharetoken/internal/mysqldb"
	"sharetoken/internal/p2p"
	"sharetoken/internal/protocol"
)

// The two tables that peers fill by asking are bounded.
func TestPeerFilledTablesAreBounded(t *testing.T) {
	f := newFixture(t, Config{})
	for i := range maxWithdrawn + 10 {
		_ = f.eng.WithdrawOffer(fmt.Sprintf("node-%d", i), 1)
	}
	if n := len(f.eng.withdrawn); n != maxWithdrawn {
		t.Fatalf("%d withdrawals remembered, want %d", n, maxWithdrawn)
	}
	// One that is known is still updated.
	_ = f.eng.WithdrawOffer("node-0", 7)
	if f.eng.withdrawn["node-0"] != 7 {
		t.Fatal("a known node's withdrawal was not updated")
	}

	now := time.Unix(1_800_000_000, 0)
	b := newErrands()
	b.now = func() time.Time { return now }
	for i := range maxErrandDevices + 10 {
		b.poll(fmt.Sprintf("device-%d", i))
	}
	if n := len(b.seen); n != maxErrandDevices {
		t.Fatalf("%d devices on the board, want %d", n, maxErrandDevices)
	}
	// Devices that stopped asking make room for those that ask now.
	now = now.Add(b.Window() + time.Second)
	b.poll("late")
	if _, ok := b.seen["late"]; !ok || len(b.seen) != 1 {
		t.Fatalf("after everyone left: %d devices, the new one there: %v", len(b.seen), ok)
	}
}

// A peer that is no user's device gets nothing from the control plane: not
// the model list, not a place on the errand board, not a withdrawal noted.
// And an announce frame larger than an offer can be is not read.
func TestCtrlRefusesUnboundPeers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, err := mysqldb.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	local := p2p.HostConfig{Listen: []string{"/ip4/127.0.0.1/tcp/0"}}
	ck := mustKeys(t)
	ch, err := p2p.NewHost(ck, local)
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()
	svc, err := NewService(ctx, ch, ck, Config{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	f := &fixture{t: t, now: time.Now(), eng: svc.Engine}

	stranger, device := mustKeys(t), mustKeys(t)
	f.bind(f.store(), "alice", device)
	call := func(k *p2p.Keys, typ string, req any, respType string, resp any) error {
		h, err := p2p.NewHost(k, local)
		if err != nil {
			t.Fatal(err)
		}
		defer h.Close()
		if err := h.Connect(ctx, peer.AddrInfo{ID: ch.ID(), Addrs: ch.Addrs()}); err != nil {
			t.Fatal(err)
		}
		return p2p.Call(ctx, h, ch.ID(), protocol.CtrlProtocol, typ, req, respType, resp)
	}
	for typ, c := range map[string]struct {
		req      any
		respType string
	}{
		protocol.TypeModels:     {nil, protocol.TypeModels},
		protocol.TypeErrandPoll: {protocol.ErrandPoll{}, protocol.TypeErrand},
		protocol.TypeWithdraw:   {protocol.WithdrawRequest{IssuedAt: 1}, protocol.TypeOK},
	} {
		if err := call(stranger, typ, c.req, c.respType, nil); protocol.CodeOf(err) != protocol.CodeUnauthorized {
			t.Errorf("%s from an unbound peer: want unauthorized, got %v", typ, err)
		}
		if err := call(device, typ, c.req, c.respType, nil); err != nil {
			t.Errorf("%s from a user's device: %v", typ, err)
		}
	}
	if len(svc.Errands.seen) != 1 || len(svc.Engine.withdrawn) != 1 {
		t.Fatalf("the board knows %d devices and the engine %d withdrawals, want the bound device's only", len(svc.Errands.seen), len(svc.Engine.withdrawn))
	}

	// An offer padded past the frame limit of an announce: never read, so
	// the answer is a reset stream, not a refusal of the offer.
	o := f.signedOffer(device, "m", priceIn, priceOut, 2)
	o.ClientVersion = strings.Repeat("9", protocol.MaxOfferFrame)
	if err := call(device, protocol.TypeAnnounce, o, protocol.TypeOK, nil); err == nil || protocol.CodeOf(err) != "" {
		t.Fatalf("an announce frame over the limit: want the stream reset, got %v", err)
	}
	// The same device's real offer goes through.
	o = f.signedOffer(device, "m", priceIn, priceOut, 2)
	if err := call(device, protocol.TypeAnnounce, o, protocol.TypeOK, nil); err != nil {
		t.Fatalf("an offer: %v", err)
	}
}
