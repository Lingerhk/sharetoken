package coordinator

import (
	"testing"
	"time"

	"sharetoken/internal/p2p"
	"sharetoken/internal/protocol"
)

// An account has a bounded number of nodes with a live offer: offers stay in
// the coordinator's memory, and devices are cheap to bind.
func TestOffersPerAccountAreCapped(t *testing.T) {
	f := newStrictFixture(t, Config{MaxOffersPerAccount: 2})
	st := f.store()
	a, b, c, other := mustKeys(t), mustKeys(t), mustKeys(t), mustKeys(t)
	for _, k := range []*p2p.Keys{a, b, c} {
		f.bind(st, "alice", k)
	}
	f.bind(st, "bob", other)
	f.offer(a, "m", priceIn, priceOut, 2)
	f.offer(b, "m", priceIn, priceOut, 2)
	if err := f.eng.UpsertOffer(f.signedOffer(c, "m", priceIn, priceOut, 2)); protocol.CodeOf(err) != protocol.CodeBusy {
		t.Fatalf("a third node of one account: want busy, got %v", err)
	}
	// The nodes that are in keep announcing, and other accounts are not held back.
	f.now = f.now.Add(15 * time.Second)
	f.offer(a, "m", priceIn, priceOut, 2)
	f.offer(b, "m", priceIn, priceOut, 2)
	f.offer(other, "m", priceIn, priceOut, 2)
	if n := len(f.eng.Offers()); n != 3 {
		t.Fatalf("%d offers, want 3", n)
	}
	// A node that left makes room.
	if err := f.eng.WithdrawOffer(a.ID(), f.now.UnixNano()); err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Second)
	f.offer(c, "m", priceIn, priceOut, 2)
}

// The defaults leave room for what a person runs.
func TestOffersPerAccountDefault(t *testing.T) {
	f := newStrictFixture(t, Config{})
	st := f.store()
	for range 16 {
		k := mustKeys(t)
		f.bind(st, "alice", k)
		f.offer(k, "m", priceIn, priceOut, 2)
	}
	k := mustKeys(t)
	f.bind(st, "alice", k)
	if err := f.eng.UpsertOffer(f.signedOffer(k, "m", priceIn, priceOut, 2)); protocol.CodeOf(err) != protocol.CodeBusy {
		t.Fatalf("a 17th node of one account: want busy, got %v", err)
	}
}

// An announce frame has an offer's size limit, the requests that carry next
// to nothing a small one, and a frame that hides its type the small one too.
func TestCtrlFrameLimits(t *testing.T) {
	for typ, want := range map[string]int{
		protocol.TypeAnnounce:      protocol.MaxOfferFrame,
		protocol.TypeWithdraw:      protocol.MaxSmallFrame,
		protocol.TypeModels:        protocol.MaxSmallFrame,
		protocol.TypeBalance:       protocol.MaxSmallFrame,
		protocol.TypeErrandPoll:    protocol.MaxSmallFrame,
		"":                         protocol.MaxSmallFrame,
		protocol.TypeSettle:        protocol.MaxFrameSize,
		protocol.TypeEvidence:      protocol.MaxFrameSize,
		protocol.TypeTicketRequest: protocol.MaxFrameSize,
		protocol.TypeErrandReport:  protocol.MaxFrameSize,
	} {
		if got := ctrlFrameLimit(typ); got != want {
			t.Errorf("%q: limit %d, want %d", typ, got, want)
		}
	}
}
