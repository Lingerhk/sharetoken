package coordinator

import (
	"testing"
	"time"

	"sharetoken/internal/protocol"
)

// An audit job is paid by the platform, so the seller's price is capped: a
// node priced above three times the median of its model's sellers gets no
// audit ticket, carried or direct, while buyers may still pay what it asks.
func TestAuditPriceIsCapped(t *testing.T) {
	f := newStrictFixture(t, Config{})
	st := f.store()
	fair, fair2, greedy, carrier := mustKeys(t), mustKeys(t), mustKeys(t), mustKeys(t)
	f.bind(st, "alice", fair)
	f.bind(st, "anna", fair2)
	f.bind(st, "mallory", greedy)
	f.bind(st, "bob", carrier)
	f.offer(fair, "m", priceIn, priceOut, 4)
	f.offer(fair2, "m", priceIn, 2*priceOut, 4)
	f.offer(greedy, "m", 1000*priceIn, 1000*priceOut, 4)

	if !f.eng.AuditPriceOK(fair.ID(), "m") || !f.eng.AuditPriceOK(fair2.ID(), "m") {
		t.Fatal("nodes at the market's price are not audited")
	}
	if f.eng.AuditPriceOK(greedy.ID(), "m") {
		t.Fatal("a node at a thousand times the market's price is audited")
	}
	if f.eng.AuditPriceOK(greedy.ID(), "other") || f.eng.AuditPriceOK(carrier.ID(), "m") {
		t.Fatal("an audit of a model or a node that is not offered")
	}
	if _, err := f.eng.IssueErrandTicket(carrier.ID(), errandReq(greedy.ID())); protocol.CodeOf(err) != protocol.CodeNoOffers {
		t.Fatalf("a carried audit of the overpriced node: want no_offers, got %v", err)
	}
	if _, err := f.eng.IssueErrandTicket(carrier.ID(), errandReq(fair.ID())); err != nil {
		t.Fatalf("a carried audit of a fair node: %v", err)
	}
	// An audit identity asking directly is held to the same cap.
	auditor := mustKeys(t)
	if err := f.eng.RegisterAuditor(auditor.ID(), 1_000_000); err != nil {
		t.Fatal(err)
	}
	if _, err := f.eng.IssueTicket(auditor.ID(), errandReq(greedy.ID())); protocol.CodeOf(err) != protocol.CodeNoOffers {
		t.Fatalf("a direct audit of the overpriced node: want no_offers, got %v", err)
	}
	if _, err := f.eng.IssueTicket(auditor.ID(), errandReq(fair2.ID())); err != nil {
		t.Fatalf("a direct audit of a fair node: %v", err)
	}
	// A cache price is a price too.
	sneaky := mustKeys(t)
	f.bind(st, "sam", sneaky)
	o := f.signedOffer(sneaky, "m", priceIn, priceOut, 4)
	o.Models[0].PriceCacheWrite = 1000 * priceIn
	o.Sign(sneaky.Signer)
	if err := f.eng.UpsertOffer(o); err != nil {
		t.Fatal(err)
	}
	if f.eng.AuditPriceOK(sneaky.ID(), "m") {
		t.Fatal("a node with a cache write price a thousand times the market's is audited")
	}
	// A buyer with credit and no price limit of its own may pay the price.
	if err := f.eng.Deposit(carrier.ID(), 1_000_000_000); err != nil {
		t.Fatal(err)
	}
	req := errandReq(greedy.ID())
	if tr, err := f.eng.IssueTicket(carrier.ID(), req); err != nil || tr.Ticket.PriceInMicros != 1000*priceIn {
		t.Fatalf("a buyer's own purchase from the overpriced node: %v", err)
	}
	f.booksBalance()
}

// What audit jobs reserve out of the platform's fees in a day is bounded,
// whatever the prices: a seller alone with a model sets its median itself.
func TestAuditDailyBudget(t *testing.T) {
	f := newStrictFixture(t, Config{AuditDailyMicros: 1000})
	st := f.store()
	node, carrier := mustKeys(t), mustKeys(t)
	f.bind(st, "alice", node)
	f.bind(st, "bob", carrier)
	f.offer(node, "m", priceIn, priceOut, 100)
	req := errandReq(node.ID()) // 50 prompt and 64 output tokens: a hold of some hundred micros
	var issued, hold uint64
	for ; issued < 20; issued++ {
		tr, err := f.eng.IssueErrandTicket(carrier.ID(), req)
		if protocol.CodeOf(err) == protocol.CodeBusy {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		hold = tr.Ticket.ReservedMicros
	}
	if hold == 0 || hold > 500 || issued != 1000/hold {
		t.Fatalf("%d errand tickets of %d micros within a budget of 1000", issued, hold)
	}
	if used, budget, err := f.eng.AuditBudgetUsed(); err != nil || used != issued*hold || budget != 1000 {
		t.Fatalf("budget used %d of %d (%v)", used, budget, err)
	}
	// Audit identities draw on the same budget.
	auditor := mustKeys(t)
	if err := f.eng.RegisterAuditor(auditor.ID(), 1_000_000); err != nil {
		t.Fatal(err)
	}
	if _, err := f.eng.IssueTicket(auditor.ID(), req); protocol.CodeOf(err) != protocol.CodeBusy {
		t.Fatalf("a direct audit past the day's budget: want busy, got %v", err)
	}
	// A refused ticket moved nothing, and buyers are not concerned.
	if err := f.eng.Deposit(carrier.ID(), 1_000_000); err != nil {
		t.Fatal(err)
	}
	if _, err := f.eng.IssueTicket(carrier.ID(), req); err != nil {
		t.Fatalf("a buyer's ticket: %v", err)
	}
	// The next day (UTC) has a budget of its own.
	f.now = f.now.Add(24 * time.Hour)
	f.offer(node, "m", priceIn, priceOut, 100)
	if _, err := f.eng.IssueErrandTicket(carrier.ID(), req); err != nil {
		t.Fatalf("the next day: %v", err)
	}
	if used, _, _ := f.eng.AuditBudgetUsed(); used != hold {
		t.Fatalf("the next day's use: %d", used)
	}
	f.booksBalance()
}
