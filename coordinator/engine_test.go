package coordinator

import (
	"crypto/ed25519"
	"encoding/hex"
	"path/filepath"
	"testing"
	"time"

	"sharetoken/attest"
	"sharetoken/evidence"
	"sharetoken/internal/sqlite"
	"sharetoken/ledger"
	"sharetoken/p2p"
	"sharetoken/protocol"
	"sharetoken/receipt"
)

// 1 micro per input token, 2 per output token.
const (
	priceIn  = 1_000_000
	priceOut = 2_000_000
)

type fixture struct {
	t   *testing.T
	now time.Time
	eng *Engine
}

func newFixture(t *testing.T, cfg Config) *fixture {
	f := &fixture{t: t, now: time.Unix(1_800_000_000, 0)}
	coord := mustKeys(t)
	cfg.Now = func() time.Time { return f.now }
	eng, err := NewEngine(coord.ID(), coord.Signer, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Close)
	f.eng = eng
	return f
}

func mustKeys(t *testing.T) *p2p.Keys {
	t.Helper()
	k, err := p2p.NewKeys()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func (f *fixture) offer(k *p2p.Keys, model string, pin, pout uint64, maxConc uint32) {
	f.t.Helper()
	o := &protocol.Offer{
		Node:           k.ID(),
		Addrs:          []string{"/ip4/127.0.0.1/tcp/1"},
		BoxPub:         k.Box.Public(),
		ReceiptKey:     k.ReceiptKey(),
		Models:         []protocol.ModelPrice{{Model: model, PriceInMicros: pin, PriceOutMicros: pout}},
		MaxConcurrency: maxConc,
		IssuedAt:       f.now.UnixNano(),
		ExpiresAt:      f.now.Add(time.Minute).UnixNano(),
	}
	o.Sign(k.Signer)
	if err := f.eng.UpsertOffer(o); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) deposit(k *p2p.Keys, micros uint64) {
	f.t.Helper()
	if err := f.eng.Deposit(k.ID(), micros); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) ticket(buyer *p2p.Keys, budget uint64) *protocol.Ticket {
	f.t.Helper()
	tr, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{Model: "m", BudgetMicros: budget})
	if err != nil {
		f.t.Fatal(err)
	}
	return &tr.Ticket
}

func signReceipt(t *testing.T, node *p2p.Keys, tk *protocol.Ticket, pt, ct uint64) receipt.SignedReceipt {
	t.Helper()
	sr, err := receipt.Sign(node.Signer, receipt.Receipt{
		JobID: tk.JobID, ModelID: tk.Model, NodeID: node.ReceiptKey(),
		RequestHash: receipt.Hash([]byte("req")), ResponseHash: receipt.Hash([]byte("resp")),
		PromptTokens: pt, CompletionTokens: ct, UnixNano: 1, Nonce: []byte("0123456789abcdef"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return *sr
}

func ack(buyer *p2p.Keys, sr *receipt.SignedReceipt) []byte {
	return ed25519.Sign(buyer.Signer, protocol.AckBytes(&sr.Receipt))
}

func (f *fixture) balance(k *p2p.Keys) int64 { return f.account(ledger.Account(k.ID())) }

func (f *fixture) account(acct string) int64 {
	f.t.Helper()
	b, err := f.eng.AccountBalance(acct)
	if err != nil {
		f.t.Fatal(err)
	}
	return b
}

func (f *fixture) held(k *p2p.Keys) int64 {
	f.t.Helper()
	b, err := f.eng.Balance(k.ID())
	if err != nil {
		f.t.Fatal(err)
	}
	return b.HeldMicros
}

func (f *fixture) stats(k *p2p.Keys) *NodeStats {
	f.t.Helper()
	st, err := f.eng.NodeStats(k.ID())
	if err != nil {
		f.t.Fatal(err)
	}
	return st
}

func (f *fixture) booksBalance() {
	f.t.Helper()
	if err := f.eng.CheckBooks(); err != nil {
		f.t.Fatalf("books do not balance: %v", err)
	}
}

func TestAckedSettlementIsFinalAndIdempotent(t *testing.T) {
	f := newFixture(t, Config{FeeBps: 500})
	node, buyer := mustKeys(t), mustKeys(t)
	f.offer(node, "m", priceIn, priceOut, 4)
	f.deposit(buyer, 1_000_000)

	tk := f.ticket(buyer, 100_000)
	if err := tk.Verify(f.eng.self); err != nil {
		t.Fatalf("issued ticket does not verify: %v", err)
	}
	sr := signReceipt(t, node, tk, 1000, 500) // 1000*1 + 500*2 = 2000 micros
	req := protocol.SettleRequest{Ticket: *tk, Receipt: sr, Ack: ack(buyer, &sr)}
	resp, err := f.eng.Settle(node.ID(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != string(StatusFinal) || resp.CostMicros != 2000 || resp.FeeMicros != 100 {
		t.Fatalf("unexpected settlement %+v", resp)
	}
	if again, err := f.eng.Settle(node.ID(), req); err != nil || again.Status != string(StatusFinal) {
		t.Fatalf("re-settlement must be idempotent: %+v %v", again, err)
	}
	if got := f.balance(node); got != 1900 {
		t.Fatalf("node earned %d, want 1900", got)
	}
	if got := f.balance(buyer); got != 1_000_000-2000 {
		t.Fatalf("buyer has %d, want %d", got, 1_000_000-2000)
	}
	if got := f.account(ledger.PlatformFees); got != 100 {
		t.Fatalf("platform fee %d, want 100", got)
	}
	f.booksBalance()
}

func TestUnackedSettlementWaitsOutDisputeWindow(t *testing.T) {
	f := newFixture(t, Config{DisputeWindow: time.Hour})
	node, buyer := mustKeys(t), mustKeys(t)
	f.offer(node, "m", priceIn, priceOut, 4)
	f.deposit(buyer, 1_000_000)
	tk := f.ticket(buyer, 100_000)

	resp, err := f.eng.Settle(node.ID(), protocol.SettleRequest{Ticket: *tk, Receipt: signReceipt(t, node, tk, 10, 5)})
	if err != nil || resp.Status != string(StatusPending) {
		t.Fatalf("want pending, got %+v %v", resp, err)
	}
	if held := f.held(buyer); held != 100_000 {
		t.Fatalf("funds must stay on hold while pending, held=%d", held)
	}
	f.eng.Tick(f.now.Add(59 * time.Minute))
	if st, _ := f.eng.JobStatus(tk.JobID); st != StatusPending {
		t.Fatalf("finalized before the window closed: %s", st)
	}
	f.eng.Tick(f.now.Add(61 * time.Minute))
	if st, _ := f.eng.JobStatus(tk.JobID); st != StatusFinal {
		t.Fatalf("want final after the window, got %s", st)
	}
	if got := f.balance(node); got != 20 {
		t.Fatalf("node earned %d, want 20", got)
	}
	f.booksBalance()
}

func TestLateAckFinalizesPendingJob(t *testing.T) {
	f := newFixture(t, Config{})
	node, buyer := mustKeys(t), mustKeys(t)
	f.offer(node, "m", priceIn, priceOut, 4)
	f.deposit(buyer, 1_000_000)
	tk := f.ticket(buyer, 100_000)
	sr := signReceipt(t, node, tk, 10, 5)
	_, _ = f.eng.Settle(node.ID(), protocol.SettleRequest{Ticket: *tk, Receipt: sr})

	// An ack for a DIFFERENT receipt must not finalize the job.
	other := signReceipt(t, node, tk, 999, 999)
	if resp, _ := f.eng.Settle(node.ID(), protocol.SettleRequest{Ticket: *tk, Receipt: other, Ack: ack(buyer, &other)}); resp.Status != string(StatusPending) {
		t.Fatalf("ack for another receipt changed the job: %+v", resp)
	}
	resp, err := f.eng.Settle(buyer.ID(), protocol.SettleRequest{Ticket: *tk, Receipt: sr, Ack: ack(buyer, &sr)})
	if err != nil || resp.Status != string(StatusFinal) {
		t.Fatalf("late ack should finalize: %+v %v", resp, err)
	}
}

func TestDisputeFreezesPayoutAndRefundPenalizesNode(t *testing.T) {
	f := newFixture(t, Config{DisputeWindow: time.Hour})
	node, buyer := mustKeys(t), mustKeys(t)
	f.offer(node, "m", priceIn, priceOut, 4)
	f.deposit(buyer, 1_000_000)
	tk := f.ticket(buyer, 100_000)
	_, _ = f.eng.Settle(node.ID(), protocol.SettleRequest{Ticket: *tk, Receipt: signReceipt(t, node, tk, 10, 5)})

	if err := f.eng.Dispute(node.ID(), protocol.DisputeRequest{JobID: tk.JobID}); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatalf("only the buyer may dispute, got %v", err)
	}
	if err := f.eng.Dispute(buyer.ID(), protocol.DisputeRequest{JobID: tk.JobID, Reason: "inflated usage"}); err != nil {
		t.Fatal(err)
	}
	f.eng.Tick(f.now.Add(2 * time.Hour))
	if st, _ := f.eng.JobStatus(tk.JobID); st != StatusDisputed {
		t.Fatalf("a disputed job must not auto-finalize, got %s", st)
	}
	if err := f.eng.ResolveDispute(tk.JobID, false); err != nil {
		t.Fatal(err)
	}
	if got := f.balance(buyer); got != 1_000_000 {
		t.Fatalf("buyer refund: have %d, want 1000000", got)
	}
	if rep := f.stats(node).Reputation; rep != startReputation-repPerLostDispute {
		t.Fatalf("node reputation %d, want %d", rep, startReputation-repPerLostDispute)
	}
	if st, _ := f.eng.JobStatus(tk.JobID); st != StatusRefunded {
		t.Fatalf("want refunded, got %s", st)
	}
	if _, err := f.eng.Settle(node.ID(), protocol.SettleRequest{Ticket: *tk, Receipt: signReceipt(t, node, tk, 10, 5)}); protocol.CodeOf(err) != protocol.CodeConflict {
		t.Fatalf("settling a refunded job: want conflict, got %v", err)
	}
	f.booksBalance()
}

func TestDisputeFiledBeforeSettlementApplies(t *testing.T) {
	f := newFixture(t, Config{})
	node, buyer := mustKeys(t), mustKeys(t)
	f.offer(node, "m", priceIn, priceOut, 4)
	f.deposit(buyer, 1_000_000)
	tk := f.ticket(buyer, 100_000)
	if err := f.eng.Dispute(buyer.ID(), protocol.DisputeRequest{JobID: tk.JobID, Reason: "no output"}); err != nil {
		t.Fatal(err)
	}
	resp, err := f.eng.Settle(node.ID(), protocol.SettleRequest{Ticket: *tk, Receipt: signReceipt(t, node, tk, 10, 5)})
	if err != nil || resp.Status != string(StatusDisputed) {
		t.Fatalf("want disputed, got %+v %v", resp, err)
	}
}

func TestUnsettledHoldIsReleased(t *testing.T) {
	f := newFixture(t, Config{TicketTTL: time.Minute, SettleGrace: time.Minute})
	node, buyer := mustKeys(t), mustKeys(t)
	f.offer(node, "m", priceIn, priceOut, 4)
	f.deposit(buyer, 1_000_000)
	tk := f.ticket(buyer, 100_000)
	f.eng.Tick(f.now.Add(3 * time.Minute))
	if st, _ := f.eng.JobStatus(tk.JobID); st != StatusReleased {
		t.Fatalf("want released, got %s", st)
	}
	if got := f.balance(buyer); got != 1_000_000 {
		t.Fatalf("hold not returned: %d", got)
	}
	if _, err := f.eng.Settle(node.ID(), protocol.SettleRequest{Ticket: *tk, Receipt: signReceipt(t, node, tk, 1, 1)}); protocol.CodeOf(err) != protocol.CodeConflict {
		t.Fatalf("settling an expired ticket: want conflict, got %v", err)
	}
	f.booksBalance()
}

func TestSettleRejectsForgeries(t *testing.T) {
	f := newFixture(t, Config{})
	node, buyer, mallory := mustKeys(t), mustKeys(t), mustKeys(t)
	f.offer(node, "m", priceIn, priceOut, 4)
	f.deposit(buyer, 1_000_000)
	tk := f.ticket(buyer, 100_000)
	good := signReceipt(t, node, tk, 10, 5)

	inflated := *tk
	inflated.ReservedMicros = 999_999
	otherJob := *tk
	otherJob.JobID = "not-this-job"
	wrongModel := *tk
	wrongModel.Model = "other-model"

	cases := map[string]struct {
		submitter string
		req       protocol.SettleRequest
		code      string
	}{
		"receipt signed by another key": {node.ID(), protocol.SettleRequest{Ticket: *tk, Receipt: signReceipt(t, mallory, tk, 10, 5)}, protocol.CodeBadRequest},
		"receipt for another job":       {node.ID(), protocol.SettleRequest{Ticket: *tk, Receipt: signReceipt(t, node, &otherJob, 10, 5)}, protocol.CodeBadRequest},
		"receipt for another model":     {node.ID(), protocol.SettleRequest{Ticket: *tk, Receipt: signReceipt(t, node, &wrongModel, 10, 5)}, protocol.CodeBadRequest},
		"ticket altered after issuance": {node.ID(), protocol.SettleRequest{Ticket: inflated, Receipt: good}, protocol.CodeBadTicket},
		"ack forged by the node":        {node.ID(), protocol.SettleRequest{Ticket: *tk, Receipt: good, Ack: ack(node, &good)}, protocol.CodeBadRequest},
		"settled by an unrelated peer":  {mallory.ID(), protocol.SettleRequest{Ticket: *tk, Receipt: good}, protocol.CodeForbidden},
	}
	for name, c := range cases {
		if _, err := f.eng.Settle(c.submitter, c.req); protocol.CodeOf(err) != c.code {
			t.Errorf("%s: want %s, got %v", name, c.code, err)
		}
	}
	if st, _ := f.eng.JobStatus(tk.JobID); st != StatusHeld {
		t.Fatalf("a rejected settlement changed the job: %s", st)
	}
}

func TestCostIsCappedAtReservation(t *testing.T) {
	f := newFixture(t, Config{})
	node, buyer := mustKeys(t), mustKeys(t)
	f.offer(node, "m", priceIn, priceOut, 4)
	f.deposit(buyer, 1_000_000)
	tk := f.ticket(buyer, 1_000)
	sr := signReceipt(t, node, tk, 1_000_000_000, 1_000_000_000)
	resp, err := f.eng.Settle(node.ID(), protocol.SettleRequest{Ticket: *tk, Receipt: sr, Ack: ack(buyer, &sr)})
	if err != nil || resp.CostMicros != 1_000 {
		t.Fatalf("cost must cap at the reservation: %+v %v", resp, err)
	}
	f.booksBalance()
}

func TestMatchingPrefersCheaperAndHonorsLimits(t *testing.T) {
	f := newFixture(t, Config{})
	cheap, pricey, buyer := mustKeys(t), mustKeys(t), mustKeys(t)
	f.offer(cheap, "m", priceIn, priceOut, 4)
	f.offer(pricey, "m", 3*priceIn, 3*priceOut, 4)
	f.deposit(buyer, 10_000_000)

	tr, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{Model: "m", BudgetMicros: 1000})
	if err != nil || tr.Ticket.Node != cheap.ID() {
		t.Fatalf("want the cheaper node, got %v %v", tr, err)
	}
	tr, err = f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{Model: "m", BudgetMicros: 1000, Exclude: []string{cheap.ID()}})
	if err != nil || tr.Ticket.Node != pricey.ID() {
		t.Fatalf("exclusion ignored: %v %v", tr, err)
	}
	_, err = f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{Model: "m", BudgetMicros: 1000, MaxPriceOut: priceOut, Exclude: []string{cheap.ID()}})
	if protocol.CodeOf(err) != protocol.CodeNoOffers {
		t.Fatalf("price ceiling ignored: %v", err)
	}
}

func TestRejectsSelfDealingAndOverdraft(t *testing.T) {
	f := newFixture(t, Config{})
	node := mustKeys(t)
	f.offer(node, "m", priceIn, priceOut, 4)
	f.deposit(node, 1_000_000)
	if _, err := f.eng.IssueTicket(node.ID(), protocol.TicketRequest{Model: "m", BudgetMicros: 10}); protocol.CodeOf(err) != protocol.CodeNoOffers {
		t.Fatalf("a node must not be matched with itself: %v", err)
	}
	broke := mustKeys(t)
	if _, err := f.eng.IssueTicket(broke.ID(), protocol.TicketRequest{Model: "m", BudgetMicros: 10}); protocol.CodeOf(err) != protocol.CodeInsufficientFunds {
		t.Fatalf("want insufficient_funds, got %v", err)
	}
}

func TestConcurrencyLimit(t *testing.T) {
	f := newFixture(t, Config{})
	node, buyer := mustKeys(t), mustKeys(t)
	f.offer(node, "m", priceIn, priceOut, 1)
	f.deposit(buyer, 1_000_000)
	f.ticket(buyer, 100)
	if _, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{Model: "m", BudgetMicros: 100}); protocol.CodeOf(err) != protocol.CodeNoOffers {
		t.Fatalf("node at capacity must not be matched: %v", err)
	}
}

func TestAnyModelIsAcceptedAndStaleOffersCannotReplaceNewer(t *testing.T) {
	f := newFixture(t, Config{})
	node, other, buyer := mustKeys(t), mustKeys(t), mustKeys(t)
	f.deposit(buyer, 1_000_000)
	// No allow-list: any open-weight model ID a node offers can be matched.
	f.offer(other, "any-open-model-v9", 5, 5, 4)
	if _, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{Model: "any-open-model-v9", BudgetMicros: 10}); err != nil {
		t.Fatalf("model without allow-list: %v", err)
	}

	f.offer(node, "m", 5, 5, 4)
	f.now = f.now.Add(-time.Second) // an older offer arrives late
	f.offer(node, "m", 1, 1, 4)
	f.now = f.now.Add(time.Second)
	for _, o := range f.eng.Offers() {
		if p, ok := o.Price("m"); ok && p.PriceInMicros != 5 {
			t.Fatalf("a stale offer replaced a newer one: %+v", p)
		}
	}
}

// The user's original idea: share capacity now, spend the credits later.
func TestMutualAidEarnThenSpend(t *testing.T) {
	f := newFixture(t, Config{})
	alice, bob, carol := mustKeys(t), mustKeys(t), mustKeys(t)
	f.offer(alice, "m", priceIn, priceOut, 4)
	f.deposit(bob, 1_000_000)

	// Bob buys from Alice.
	tk := f.ticket(bob, 100_000)
	sr := signReceipt(t, alice, tk, 10_000, 10_000) // 30_000 micros
	if _, err := f.eng.Settle(alice.ID(), protocol.SettleRequest{Ticket: *tk, Receipt: sr, Ack: ack(bob, &sr)}); err != nil {
		t.Fatal(err)
	}
	// Alice never deposited, yet she can now buy from Carol with what she earned.
	f.offer(carol, "m", priceIn, priceOut, 4)
	if _, err := f.eng.IssueTicket(alice.ID(), protocol.TicketRequest{Model: "m", BudgetMicros: 30_000, Exclude: []string{alice.ID()}}); err != nil {
		t.Fatalf("earned credits should be spendable: %v", err)
	}
	f.booksBalance()
}

func TestStateSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "coordinator.db")
	coord, node, buyer := mustKeys(t), mustKeys(t), mustKeys(t)
	now := time.Unix(1_800_000_000, 0)
	open := func() *Engine {
		db, err := sqlite.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		eng, err := NewEngine(coord.ID(), coord.Signer, Config{DB: db, Now: func() time.Time { return now }})
		if err != nil {
			t.Fatal(err)
		}
		return eng
	}
	f := &fixture{t: t, now: now, eng: open()}
	f.offer(node, "m", priceIn, priceOut, 4)
	f.deposit(buyer, 1_000_000)
	tk := f.ticket(buyer, 100_000)

	// Restart between ticket and settlement: the hold and the job survive.
	f.eng = open()
	if held := f.held(buyer); held != 100_000 {
		t.Fatalf("hold lost across restart: %d", held)
	}
	sr := signReceipt(t, node, tk, 1000, 500)
	resp, err := f.eng.Settle(node.ID(), protocol.SettleRequest{Ticket: *tk, Receipt: sr, Ack: ack(buyer, &sr)})
	if err != nil || resp.Status != string(StatusFinal) || resp.CostMicros != 2000 {
		t.Fatalf("settling after restart: %+v %v", resp, err)
	}
	if got := f.balance(node); got != 2000 {
		t.Fatalf("node earned %d, want 2000", got)
	}
	f.booksBalance()

	// The database belongs to this coordinator key.
	db, _ := sqlite.Open(path)
	defer db.Close()
	other := mustKeys(t)
	if _, err := NewEngine(other.ID(), other.Signer, Config{DB: db}); err == nil {
		t.Fatal("another coordinator identity opened this database")
	}
}

func TestDevCreditIsGrantedOncePerPeer(t *testing.T) {
	f := newFixture(t, Config{DevCreditMicros: 1_000})
	node, buyer := mustKeys(t), mustKeys(t)
	f.offer(node, "m", priceIn, priceOut, 4)
	f.ticket(buyer, 100)
	f.ticket(buyer, 100)
	if got, held := f.balance(buyer), f.held(buyer); got+held != 1_000 {
		t.Fatalf("dev credit granted more than once: available %d + held %d", got, held)
	}
}

func TestEvidenceVerdictsMoveMoney(t *testing.T) {
	f := newFixture(t, Config{})
	node, buyer := mustKeys(t), mustKeys(t)
	f.offer(node, "m", priceIn, priceOut, 8)
	f.deposit(buyer, 1_000_000)

	// Upheld while pending: the buyer is refunded, the node loses reputation.
	tk := f.ticket(buyer, 100_000)
	sr := signReceipt(t, node, tk, 10, 5)
	_, _ = f.eng.Settle(node.ID(), protocol.SettleRequest{Ticket: *tk, Receipt: sr})
	if _, _, err := f.eng.EvidenceTarget(node.ID(), tk.JobID); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatalf("only the buyer may submit evidence: %v", err)
	}
	v, err := f.eng.ApplyVerdict(buyer.ID(), tk.JobID, "inflated", &sr, evidence.Verdict{Outcome: evidence.Upheld, Detail: "billed too much"})
	if err != nil || v.Status != string(StatusRefunded) {
		t.Fatalf("upheld: %+v %v", v, err)
	}
	if f.balance(buyer) != 1_000_000 || f.stats(node).DisputesLost != 1 {
		t.Fatalf("upheld verdict did not refund the buyer / charge the node")
	}
	if _, err := f.eng.ApplyVerdict(buyer.ID(), tk.JobID, "again", &sr, evidence.Verdict{Outcome: evidence.Dismissed}); protocol.CodeOf(err) != protocol.CodeConflict {
		t.Fatalf("a judged job cannot be judged again: %v", err)
	}

	// Dismissed before the node even settled: the node is paid from the
	// receipt the buyer presented, and its later settlement is idempotent.
	tk2 := f.ticket(buyer, 100_000)
	sr2 := signReceipt(t, node, tk2, 10, 5)
	if v, err := f.eng.ApplyVerdict(buyer.ID(), tk2.JobID, "", &sr2, evidence.Verdict{Outcome: evidence.Dismissed}); err != nil || v.Status != string(StatusFinal) {
		t.Fatalf("dismissed: %+v %v", v, err)
	}
	if resp, err := f.eng.Settle(node.ID(), protocol.SettleRequest{Ticket: *tk2, Receipt: sr2}); err != nil || resp.Status != string(StatusFinal) || resp.CostMicros != 20 {
		t.Fatalf("settling a dismissed job: %+v %v", resp, err)
	}

	// Unverifiable: the job waits for arbitration and does not auto-finalize.
	tk3 := f.ticket(buyer, 100_000)
	sr3 := signReceipt(t, node, tk3, 10, 5)
	if v, _ := f.eng.ApplyVerdict(buyer.ID(), tk3.JobID, "", &sr3, evidence.Verdict{Outcome: evidence.Unverifiable}); v.Status != string(StatusDisputed) {
		t.Fatalf("unverifiable: %+v", v)
	}
	f.eng.Tick(f.now.Add(48 * time.Hour))
	if st, _ := f.eng.JobStatus(tk3.JobID); st != StatusDisputed {
		t.Fatalf("an unverifiable dispute must wait for arbitration, got %s", st)
	}
	if ds, _ := f.eng.Disputes(10); len(ds) != 3 || ds[0].JobID != tk3.JobID {
		t.Fatalf("dispute listing: %+v", ds)
	}
	f.booksBalance()
}

func TestNodeThatSettlesADifferentReceiptLosesTheDispute(t *testing.T) {
	f := newFixture(t, Config{})
	node, buyer := mustKeys(t), mustKeys(t)
	f.offer(node, "m", priceIn, priceOut, 4)
	f.deposit(buyer, 1_000_000)
	tk := f.ticket(buyer, 100_000)
	presented := signReceipt(t, node, tk, 10, 5)
	_, _ = f.eng.Settle(node.ID(), protocol.SettleRequest{Ticket: *tk, Receipt: signReceipt(t, node, tk, 10, 500)})
	v, err := f.eng.ApplyVerdict(buyer.ID(), tk.JobID, "", &presented, evidence.Verdict{Outcome: evidence.Dismissed})
	if err != nil || v.Outcome != string(evidence.Upheld) {
		t.Fatalf("two receipts for one job must be upheld: %+v %v", v, err)
	}
}

func TestFailedAuditsSuspendANode(t *testing.T) {
	f := newFixture(t, Config{SuspendFor: time.Hour})
	node, buyer := mustKeys(t), mustKeys(t)
	f.offer(node, "m", priceIn, priceOut, 4)
	f.deposit(buyer, 1_000_000)

	st, _ := f.eng.RecordAudit(AuditRecord{JobID: "a1", Node: node.ID(), Model: "m", Verdict: AuditMatch, Similarity: 1})
	if st.Reputation != startReputation+repPerAuditPass || st.AuditsPassed != 1 {
		t.Fatalf("after a passed audit: %+v", st)
	}
	_, _ = f.eng.RecordAudit(AuditRecord{JobID: "a2", Node: node.ID(), Model: "m", Verdict: AuditMismatch})
	f.ticket(buyer, 100) // one failure does not suspend: nondeterminism happens
	st, _ = f.eng.RecordAudit(AuditRecord{JobID: "a3", Node: node.ID(), Model: "m", Verdict: AuditMismatch})
	if st.SuspendedUntil == 0 {
		t.Fatalf("two consecutive failed audits must suspend: %+v", st)
	}
	if _, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{Model: "m", BudgetMicros: 100}); protocol.CodeOf(err) != protocol.CodeNoOffers {
		t.Fatalf("a suspended node must not be matched: %v", err)
	}
	f.now = f.now.Add(2 * time.Hour)
	f.offer(node, "m", priceIn, priceOut, 4)
	if _, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{Model: "m", BudgetMicros: 100}); err != nil {
		t.Fatalf("suspension must end: %v", err)
	}
	if as, _ := f.eng.Audits(10); len(as) != 3 || as[0].JobID != "a3" {
		t.Fatalf("audit listing: %+v", as)
	}
}

func TestAuditorIsFundedByThePlatformAndLeftoversReturn(t *testing.T) {
	f := newFixture(t, Config{DevCreditMicros: 5_000_000}) // dev credit must not reach audit identities
	node, auditor := mustKeys(t), mustKeys(t)
	f.offer(node, "m", priceIn, priceOut, 4)
	if err := f.eng.RegisterAuditor(auditor.ID(), 1_000); err != nil {
		t.Fatal(err)
	}
	tk := f.ticket(auditor, 500)
	sr := signReceipt(t, node, tk, 10, 5) // 20 micros
	if _, err := f.eng.Settle(node.ID(), protocol.SettleRequest{Ticket: *tk, Receipt: sr, Ack: ack(auditor, &sr)}); err != nil {
		t.Fatal(err)
	}
	f.eng.Tick(f.now.Add(2 * time.Minute))
	if got := f.balance(auditor); got != 0 {
		t.Fatalf("auditor kept %d", got)
	}
	if got := f.account(ledger.PlatformFees); got != -20 {
		t.Fatalf("platform = %d, want -20: it paid for the audit job", got)
	}
	f.booksBalance()
}

func TestRequireTEEAndPinnedNodes(t *testing.T) {
	measurement := []byte("audited-image")
	att, root := attest.NewMockAttestor(measurement)
	f := newFixture(t, Config{Attestation: &Attestation{
		Verifier: attest.Multi{attest.MockBackend: attest.MockVerifier{RootPub: root}},
		Policy:   &attest.Policy{Measurements: map[string]string{hex.EncodeToString(measurement): "node v1"}},
	}})
	tee, plain, buyer := mustKeys(t), mustKeys(t), mustKeys(t)
	f.deposit(buyer, 1_000_000)

	q, _ := att.Attest(attest.ReportData(tee.ReceiptKey(), tee.Box.Public()))
	o := &protocol.Offer{
		Node: tee.ID(), BoxPub: tee.Box.Public(), ReceiptKey: tee.ReceiptKey(),
		Models:   []protocol.ModelPrice{{Model: "m", PriceInMicros: 9 * priceIn, PriceOutMicros: 9 * priceOut}},
		IssuedAt: f.now.UnixNano(), ExpiresAt: f.now.Add(time.Minute).UnixNano(), Quote: q,
	}
	o.Sign(tee.Signer)
	if err := f.eng.UpsertOffer(o); err != nil {
		t.Fatal(err)
	}
	f.offer(plain, "m", priceIn, priceOut, 4) // much cheaper, not attested

	for range 3 {
		tr, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{Model: "m", BudgetMicros: 10, RequireTEE: true})
		if err != nil || tr.Ticket.Node != tee.ID() {
			t.Fatalf("require_tee must match only the attested node: %v %v", tr, err)
		}
	}
	tr, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{Model: "m", BudgetMicros: 10, Node: plain.ID()})
	if err != nil || tr.Ticket.Node != plain.ID() {
		t.Fatalf("pinned node: %v %v", tr, err)
	}
	if ms := f.eng.Models(); len(ms) != 1 || ms[0].Nodes != 2 || ms[0].AttestedNodes != 1 {
		t.Fatalf("model summary: %+v", ms)
	}

	// A quote bound to other keys is not accepted as attestation.
	stolen := &protocol.Offer{
		Node: plain.ID(), BoxPub: plain.Box.Public(), ReceiptKey: plain.ReceiptKey(),
		Models:   []protocol.ModelPrice{{Model: "m", PriceInMicros: 1, PriceOutMicros: 1}},
		IssuedAt: f.now.Add(time.Second).UnixNano(), ExpiresAt: f.now.Add(time.Minute).UnixNano(), Quote: q,
	}
	stolen.Sign(plain.Signer)
	_ = f.eng.UpsertOffer(stolen)
	for _, lo := range f.eng.LiveOffers() {
		if lo.Node == plain.ID() && lo.AttestedImage != "" {
			t.Fatal("a node reusing another node's quote was treated as attested")
		}
	}
}
