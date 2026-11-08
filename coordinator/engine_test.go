package coordinator

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"
	"time"

	"sharetoken/attest"
	"sharetoken/evidence"
	"sharetoken/internal/mysqldb"
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

// newFixture runs the engine with open accounts: most tests use bare peers;
// accounts_test.go covers devices bound to users.
func newFixture(t *testing.T, cfg Config) *fixture {
	cfg.OpenAccounts = true
	return newStrictFixture(t, cfg)
}

func newStrictFixture(t *testing.T, cfg Config) *fixture {
	f := &fixture{t: t, now: time.Unix(1_800_000_000, 0)}
	coord := mustKeys(t)
	cfg.Now = func() time.Time { return f.now }
	if cfg.DB == nil {
		db, err := mysqldb.OpenTest()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		cfg.DB = db
	}
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
	if err := f.eng.UpsertOffer(f.signedOffer(k, model, pin, pout, maxConc)); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) signedOffer(k *p2p.Keys, model string, pin, pout uint64, maxConc uint32) *protocol.Offer {
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
	return o
}

func (f *fixture) deposit(k *p2p.Keys, micros uint64) {
	f.t.Helper()
	if err := f.eng.Deposit(k.ID(), micros); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) ticket(buyer *p2p.Keys, budget uint64) *protocol.Ticket {
	f.t.Helper()
	tr, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: budget})
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
	if err := f.eng.MarkStarted(node.ID(), *tk); err != nil { // started, then never settled
		t.Fatal(err)
	}
	f.eng.Tick(f.now.Add(90 * time.Second)) // ticket expired, but the job is running
	if st, _ := f.eng.JobStatus(tk.JobID); st != StatusHeld {
		t.Fatalf("a started job must stay held until its settle deadline, got %s", st)
	}
	f.eng.Tick(f.now.Add(3 * time.Minute))
	if st, _ := f.eng.JobStatus(tk.JobID); st != StatusReleased {
		t.Fatalf("want released, got %s", st)
	}
	if got := f.balance(buyer); got != 1_000_000 {
		t.Fatalf("hold not returned: %d", got)
	}
	if f.stats(node).Failures != 1 {
		t.Fatal("a started job left unsettled counts against the node")
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

	tr, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 1000})
	if err != nil || tr.Ticket.Node != cheap.ID() {
		t.Fatalf("want the cheaper node, got %v %v", tr, err)
	}
	tr, err = f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 1000, Exclude: []string{cheap.ID()}})
	if err != nil || tr.Ticket.Node != pricey.ID() {
		t.Fatalf("exclusion ignored: %v %v", tr, err)
	}
	_, err = f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 1000, MaxPriceOut: priceOut, Exclude: []string{cheap.ID()}})
	if protocol.CodeOf(err) != protocol.CodeNoOffers {
		t.Fatalf("price ceiling ignored: %v", err)
	}
}

func TestRejectsSelfDealingAndOverdraft(t *testing.T) {
	f := newFixture(t, Config{})
	node := mustKeys(t)
	f.offer(node, "m", priceIn, priceOut, 4)
	f.deposit(node, 1_000_000)
	if _, err := f.eng.IssueTicket(node.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 10}); protocol.CodeOf(err) != protocol.CodeNoOffers {
		t.Fatalf("a node must not be matched with itself: %v", err)
	}
	broke := mustKeys(t)
	if _, err := f.eng.IssueTicket(broke.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 10}); protocol.CodeOf(err) != protocol.CodeInsufficientFunds {
		t.Fatalf("want insufficient_funds, got %v", err)
	}
}

func TestConcurrencyLimit(t *testing.T) {
	f := newFixture(t, Config{})
	node, buyer := mustKeys(t), mustKeys(t)
	f.offer(node, "m", priceIn, priceOut, 1)
	f.deposit(buyer, 1_000_000)
	f.ticket(buyer, 100)
	if _, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 100}); protocol.CodeOf(err) != protocol.CodeCapacity {
		t.Fatalf("node at capacity must not be matched: %v", err)
	}
}

func TestAnyModelIsAcceptedAndStaleOffersCannotReplaceNewer(t *testing.T) {
	f := newFixture(t, Config{})
	node, other, buyer := mustKeys(t), mustKeys(t), mustKeys(t)
	f.deposit(buyer, 1_000_000)
	// No allow-list: any open-weight model ID a node offers can be matched.
	f.offer(other, "any-open-model-v9", 5, 5, 4)
	if _, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "any-open-model-v9", BudgetMicros: 10}); err != nil {
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
	if _, err := f.eng.IssueTicket(alice.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 30_000, Exclude: []string{alice.ID()}}); err != nil {
		t.Fatalf("earned credits should be spendable: %v", err)
	}
	f.booksBalance()
}

func TestStateSurvivesRestart(t *testing.T) {
	path, err := mysqldb.TestDSN()
	if err != nil {
		t.Fatal(err)
	}
	coord, node, buyer := mustKeys(t), mustKeys(t), mustKeys(t)
	now := time.Unix(1_800_000_000, 0)
	open := func() *Engine {
		db, err := mysqldb.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		eng, err := NewEngine(coord.ID(), coord.Signer, Config{DB: db, OpenAccounts: true, Now: func() time.Time { return now }})
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
	db, _ := mysqldb.Open(path)
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
	if _, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 100}); protocol.CodeOf(err) != protocol.CodeNoOffers {
		t.Fatalf("a suspended node must not be matched: %v", err)
	}
	f.now = f.now.Add(2 * time.Hour)
	f.offer(node, "m", priceIn, priceOut, 4)
	if _, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 100}); err != nil {
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
		tr, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 10, RequireTEE: true})
		if err != nil || tr.Ticket.Node != tee.ID() {
			t.Fatalf("require_tee must match only the attested node: %v %v", tr, err)
		}
	}
	tr, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 10, Node: plain.ID()})
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

func TestAbandonReleasesTheHoldAtOnce(t *testing.T) {
	f := newFixture(t, Config{})
	node, buyer := mustKeys(t), mustKeys(t)
	f.offer(node, "m", priceIn, priceOut, 1)
	f.deposit(buyer, 1_000_000)
	tk := f.ticket(buyer, 100_000)

	// Concurrency 1: the held job blocks the node until it is abandoned.
	if _, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 10}); protocol.CodeOf(err) != protocol.CodeCapacity {
		t.Fatalf("busy node matched: %v", err)
	}
	if err := f.eng.Abandon(buyer.ID(), protocol.AbandonRequest{Ticket: *tk}); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatalf("buyer abandoning: want forbidden, got %v", err)
	}
	if err := f.eng.Abandon(node.ID(), protocol.AbandonRequest{Ticket: *tk, Reason: "model server error"}); err != nil {
		t.Fatal(err)
	}
	if err := f.eng.Abandon(node.ID(), protocol.AbandonRequest{Ticket: *tk}); err != nil {
		t.Fatalf("abandon must be idempotent: %v", err)
	}
	if f.held(buyer) != 0 || f.balance(buyer) != 1_000_000 || f.stats(node).Failures != 1 {
		t.Fatalf("after abandon: held %d balance %d failures %d", f.held(buyer), f.balance(buyer), f.stats(node).Failures)
	}
	// The slot is free again; a settled job cannot be abandoned.
	tk2 := f.ticket(buyer, 100_000)
	sr := signReceipt(t, node, tk2, 10, 10)
	if _, err := f.eng.Settle(node.ID(), protocol.SettleRequest{Ticket: *tk2, Receipt: sr}); err != nil {
		t.Fatal(err)
	}
	if err := f.eng.Abandon(node.ID(), protocol.AbandonRequest{Ticket: *tk2}); protocol.CodeOf(err) != protocol.CodeConflict {
		t.Fatalf("abandoning a settled job: want conflict, got %v", err)
	}
	f.booksBalance()
}

// A job the buyer's own request made impossible (the model server rejected it
// as invalid) is released without counting against the node: otherwise any
// buyer could drain honest nodes' reputation with malformed requests.
func TestNoFaultAbandonKeepsReputation(t *testing.T) {
	f := newFixture(t, Config{})
	node, buyer := mustKeys(t), mustKeys(t)
	f.offer(node, "m", priceIn, priceOut, 1)
	f.deposit(buyer, 1_000_000)
	before := f.stats(node)
	for i := 0; i < 5; i++ {
		tk := f.ticket(buyer, 100_000)
		if err := f.eng.Abandon(node.ID(), protocol.AbandonRequest{Ticket: *tk, Reason: "request rejected", NoFault: true}); err != nil {
			t.Fatal(err)
		}
	}
	after := f.stats(node)
	if after.Failures != before.Failures || after.Reputation != before.Reputation {
		t.Fatalf("no-fault abandons penalized the node: %+v -> %+v", before, after)
	}
	if f.held(buyer) != 0 || f.balance(buyer) != 1_000_000 {
		t.Fatalf("held %d balance %d", f.held(buyer), f.balance(buyer))
	}
	f.booksBalance()
}

// A request that caps its output holds only its worst-case cost, so a balance
// supports many requests in flight; uncapped requests hold the whole budget.
func TestCappedRequestsHoldOnlyTheirWorstCase(t *testing.T) {
	f := newFixture(t, Config{})
	node, buyer := mustKeys(t), mustKeys(t)
	f.offer(node, "m", priceIn, priceOut, 100)
	f.deposit(buyer, 1_000_000)
	tr, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 200_000, PromptTokens: 20, MaxTokens: 32})
	if err != nil {
		t.Fatal(err)
	}
	want := protocol.HoldFor(20, 32, priceIn, priceOut)
	if tr.Ticket.ReservedMicros != want || f.held(buyer) != int64(want) || want >= 200_000 {
		t.Fatalf("capped request reserved %d (held %d), want %d", tr.Ticket.ReservedMicros, f.held(buyer), want)
	}
	tr2, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 200_000})
	if err != nil || tr2.Ticket.ReservedMicros != 200_000 {
		t.Fatalf("uncapped request reserved %d: %v", tr2.Ticket.ReservedMicros, err)
	}
	// The budget stays the ceiling.
	tr3, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 5, PromptTokens: 20, MaxTokens: 1 << 30})
	if err != nil || tr3.Ticket.ReservedMicros != 5 {
		t.Fatalf("hold above the budget: %d %v", tr3.Ticket.ReservedMicros, err)
	}
}

// A buyer that takes tickets and never connects cannot keep a node busy: the
// hold is released soon after the ticket expires, without penalizing the node,
// and the buyer cannot pile up unused tickets.
func TestTicketsNeverStartedAreReleasedEarly(t *testing.T) {
	f := newFixture(t, Config{TicketTTL: time.Minute, StartGrace: 30 * time.Second, MaxStaleTickets: 2, StaleAfter: 10 * time.Second})
	node, buyer := mustKeys(t), mustKeys(t)
	f.offer(node, "m", priceIn, priceOut, 2)
	f.deposit(buyer, 1_000_000)
	a := f.ticket(buyer, 1000)
	b := f.ticket(buyer, 1000)
	// Concurrency 2: both slots are taken.
	if _, err := f.eng.IssueTicket(mustKeys(t).ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 1}); protocol.CodeOf(err) != protocol.CodeCapacity {
		t.Fatalf("full node matched: %v", err)
	}
	// Two stale unused tickets: the buyer gets no third one.
	f.now = f.now.Add(20 * time.Second)
	f.offer(node, "m", priceIn, priceOut, 10)
	if _, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 1000}); protocol.CodeOf(err) != protocol.CodeBusy {
		t.Fatalf("third ticket with two unused: want busy, got %v", err)
	}
	if err := f.eng.MarkStarted(buyer.ID(), *a); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatalf("buyer reporting a start: want forbidden, got %v", err)
	}
	if err := f.eng.MarkStarted(node.ID(), *a); err != nil {
		t.Fatal(err)
	}
	// Ticket TTL (1m) + grace (30s): the unstarted one is released, the started one is not.
	f.now = f.now.Add(71 * time.Second)
	f.eng.Tick(f.now)
	if st, _ := f.eng.JobStatus(b.JobID); st != StatusReleased {
		t.Fatalf("unstarted ticket: %s", st)
	}
	if st, _ := f.eng.JobStatus(a.JobID); st != StatusHeld {
		t.Fatalf("started ticket: %s", st)
	}
	if f.held(buyer) != 1000 || f.stats(node).Failures != 0 {
		t.Fatalf("held %d, node failures %d", f.held(buyer), f.stats(node).Failures)
	}
	// A late start report for the released job tells the node to stop.
	if err := f.eng.MarkStarted(node.ID(), *b); protocol.CodeOf(err) != protocol.CodeConflict {
		t.Fatalf("late start: want conflict, got %v", err)
	}
	f.booksBalance()
}

// A node that is not connected to the coordinator (crashed, or its relay just
// died) is not matched: buyers could not reach it, and it could not settle.
func TestOfflineNodesAreNotMatched(t *testing.T) {
	online := map[string]bool{}
	f := newFixture(t, Config{NodeOnline: func(n string) bool { return online[n] }})
	a, b, buyer := mustKeys(t), mustKeys(t), mustKeys(t)
	f.offer(a, "m", priceIn, priceOut, 4)
	f.offer(b, "m", priceIn/2, priceOut/2, 4) // cheaper, but offline
	f.deposit(buyer, 1_000_000)
	if _, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 100}); protocol.CodeOf(err) != protocol.CodeNoOffers {
		t.Fatalf("all nodes offline: want no_offers, got %v", err)
	}
	online[a.ID()] = true
	tr, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 100})
	if err != nil || tr.Ticket.Node != a.ID() {
		t.Fatalf("want the online node, got %v %v", tr, err)
	}
}

// Tickets a buyer disputed because it could not reach the node do not lock it
// out like unused (hoarded) tickets do, but they are still capped.
func TestFailedTicketsDoNotLockOutTheBuyer(t *testing.T) {
	f := newFixture(t, Config{MaxStaleTickets: 2, StaleAfter: 10 * time.Second})
	node, buyer := mustKeys(t), mustKeys(t)
	f.offer(node, "m", priceIn, priceOut, 100)
	f.deposit(buyer, 1_000_000)
	issue := func() error {
		_, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 10})
		return err
	}
	for i := 0; i < 8; i++ { // 4x the limit, each reported as failed
		tr, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 10})
		if err != nil {
			t.Fatalf("ticket %d: %v", i, err)
		}
		if err := f.eng.Dispute(buyer.ID(), protocol.DisputeRequest{JobID: tr.Ticket.JobID, Reason: "dial failed"}); err != nil {
			t.Fatal(err)
		}
		f.now = f.now.Add(11 * time.Second)
		f.offer(node, "m", priceIn, priceOut, 100) // keep the offer fresh
	}
	if err := issue(); protocol.CodeOf(err) != protocol.CodeBusy {
		t.Fatalf("after 8 failed tickets (limit 4x2): want busy, got %v", err)
	}
}

// Tickets whose node is offline (it cannot report starts while its relay is
// down) do not count as unused against the buyer.
func TestTicketsOfOfflineNodesAreNotStale(t *testing.T) {
	online := map[string]bool{}
	f := newFixture(t, Config{MaxStaleTickets: 2, StaleAfter: 10 * time.Second, NodeOnline: func(n string) bool { return online[n] }})
	node, buyer := mustKeys(t), mustKeys(t)
	online[node.ID()] = true
	f.offer(node, "m", priceIn, priceOut, 100)
	f.deposit(buyer, 1_000_000)
	for i := 0; i < 2; i++ {
		f.ticket(buyer, 10)
	}
	f.now = f.now.Add(11 * time.Second)
	f.offer(node, "m", priceIn, priceOut, 100)
	if _, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 10}); protocol.CodeOf(err) != protocol.CodeBusy {
		t.Fatalf("two unused tickets of an online node: want busy, got %v", err)
	}
	// The node drops off (its relay died): its tickets prove nothing about the buyer.
	online[node.ID()] = false
	other := mustKeys(t)
	online[other.ID()] = true
	f.offer(other, "m", priceIn, priceOut, 100)
	if _, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 10}); err != nil {
		t.Fatalf("tickets of an offline node locked the buyer out: %v", err)
	}
}

// Right after a (re)start offers are still being re-announced: a first ask
// for a model nobody offers yet is told to retry (capacity), not no_offers.
func TestWarmUpAsksBuyersToRetry(t *testing.T) {
	f := newFixture(t, Config{WarmUp: 30 * time.Second})
	buyer := mustKeys(t)
	f.deposit(buyer, 1_000_000)
	req := protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 10}
	if _, err := f.eng.IssueTicket(buyer.ID(), req); protocol.CodeOf(err) != protocol.CodeCapacity {
		t.Fatalf("during warm-up: want capacity, got %v", err)
	}
	req.Exclude = []string{"some-node"}
	if _, err := f.eng.IssueTicket(buyer.ID(), req); protocol.CodeOf(err) != protocol.CodeNoOffers {
		t.Fatalf("after failovers: want no_offers, got %v", err)
	}
	f.now = f.now.Add(31 * time.Second)
	if _, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 10}); protocol.CodeOf(err) != protocol.CodeNoOffers {
		t.Fatalf("after warm-up: want no_offers, got %v", err)
	}
}

// A ticket whose reply may have been lost (the buyer's device reconnected
// after it was issued) does not count as unused against the buyer.
func TestTicketsBeforeAReconnectAreNotStale(t *testing.T) {
	since := map[string]time.Time{}
	f := newFixture(t, Config{MaxStaleTickets: 2, StaleAfter: 10 * time.Second,
		ConnectedSince: func(p string) time.Time { return since[p] }})
	node, buyer := mustKeys(t), mustKeys(t)
	since[buyer.ID()] = f.now.Add(-time.Hour)
	f.offer(node, "m", priceIn, priceOut, 100)
	f.deposit(buyer, 1_000_000)
	f.ticket(buyer, 10)
	f.ticket(buyer, 10)
	f.now = f.now.Add(11 * time.Second)
	f.offer(node, "m", priceIn, priceOut, 100)
	if _, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 10}); protocol.CodeOf(err) != protocol.CodeBusy {
		t.Fatalf("connected throughout: want busy, got %v", err)
	}
	since[buyer.ID()] = f.now.Add(-time.Second) // the device just reconnected
	if _, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 10}); err != nil {
		t.Fatalf("tickets issued before the reconnect locked the buyer out: %v", err)
	}
}

// A ticket whose reply could not be sent is released at once: the buyer's
// hold comes back and the node's slot is free. A started job is left alone.
func TestUndeliveredTicketIsReleased(t *testing.T) {
	f := newFixture(t, Config{})
	node, buyer := mustKeys(t), mustKeys(t)
	f.offer(node, "m", priceIn, priceOut, 1)
	f.deposit(buyer, 1_000_000)
	tk := f.ticket(buyer, 100_000)
	if err := f.eng.ReleaseUndelivered(tk.JobID); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.eng.JobStatus(tk.JobID); st != StatusReleased || f.held(buyer) != 0 || f.balance(buyer) != 1_000_000 {
		t.Fatalf("status %s held %d balance %d", st, f.held(buyer), f.balance(buyer))
	}
	if s := f.stats(node); s.Failures != 0 {
		t.Fatalf("node penalized: %+v", s)
	}
	tk2 := f.ticket(buyer, 100_000) // the slot is free again
	if err := f.eng.MarkStarted(node.ID(), *tk2); err != nil {
		t.Fatal(err)
	}
	if err := f.eng.ReleaseUndelivered(tk2.JobID); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.eng.JobStatus(tk2.JobID); st != StatusHeld {
		t.Fatalf("a started job must not be released, got %s", st)
	}
	f.booksBalance()
}

// Finished jobs are purged after JobRetention, at most once a minute.
func TestFinishedJobsArePurgedAfterRetention(t *testing.T) {
	f := newFixture(t, Config{JobRetention: time.Hour})
	node, buyer := mustKeys(t), mustKeys(t)
	f.offer(node, "m", priceIn, priceOut, 4)
	f.deposit(buyer, 1_000_000)
	a := f.ticket(buyer, 1000)
	if err := f.eng.Abandon(node.ID(), protocol.AbandonRequest{Ticket: *a, NoFault: true}); err != nil {
		t.Fatal(err)
	}
	f.eng.Tick(f.now) // first tick purges (nothing old yet)
	b := f.ticket(buyer, 1000)
	if err := f.eng.Abandon(node.ID(), protocol.AbandonRequest{Ticket: *b, NoFault: true}); err != nil {
		t.Fatal(err)
	}
	later := f.now.Add(2 * time.Hour)
	f.eng.Tick(later)
	for _, id := range []string{a.JobID, b.JobID} {
		if _, ok := f.eng.JobStatus(id); ok {
			t.Fatalf("job %s not purged after retention", id)
		}
	}
	f.booksBalance() // the journal is kept
}

// A ticket for a node that reconnected after it was issued (it lost or
// switched its relay, so its start report may be late) does not count as
// unused against the buyer.
func TestTicketsBeforeANodeReconnectAreNotStale(t *testing.T) {
	since := map[string]time.Time{}
	f := newFixture(t, Config{MaxStaleTickets: 2, StaleAfter: 10 * time.Second,
		ConnectedSince: func(p string) time.Time { return since[p] }})
	node, buyer := mustKeys(t), mustKeys(t)
	since[buyer.ID()] = f.now.Add(-time.Hour)
	since[node.ID()] = f.now.Add(-time.Hour)
	f.offer(node, "m", priceIn, priceOut, 100)
	f.deposit(buyer, 1_000_000)
	f.ticket(buyer, 10)
	f.ticket(buyer, 10)
	f.now = f.now.Add(11 * time.Second)
	f.offer(node, "m", priceIn, priceOut, 100)
	if _, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 10}); protocol.CodeOf(err) != protocol.CodeBusy {
		t.Fatalf("both connected throughout: want busy, got %v", err)
	}
	since[node.ID()] = f.now.Add(-time.Second) // the node just reconnected through another relay
	if _, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 10}); err != nil {
		t.Fatalf("tickets issued before the node's reconnect locked the buyer out: %v", err)
	}
}

// A job whose node reconnected after the ticket was issued (its start report
// may still be on its way) is not released early as never started; it is
// once ReconnectGrace has passed since the reconnect.
func TestUnstartedJobOfAReconnectedNodeIsNotReleasedEarly(t *testing.T) {
	since := map[string]time.Time{}
	f := newFixture(t, Config{TicketTTL: time.Minute, StartGrace: 30 * time.Second, ReconnectGrace: 5 * time.Minute,
		ConnectedSince: func(p string) time.Time { return since[p] }})
	node, other, buyer, buyer2 := mustKeys(t), mustKeys(t), mustKeys(t), mustKeys(t)
	f.deposit(buyer2, 1_000_000)
	since[node.ID()], since[other.ID()] = f.now.Add(-time.Hour), f.now.Add(-time.Hour)
	f.offer(node, "m", priceIn, priceOut, 1)
	f.deposit(buyer, 1_000_000)
	reconnected := f.ticket(buyer, 1000)
	f.offer(node, "m", priceIn, priceOut, 0) // stop offering: the next ticket goes to other
	f.offer(other, "m", priceIn, priceOut, 1)
	steady := f.ticket(buyer, 1000)
	if steady.Node != other.ID() {
		t.Fatalf("second ticket went to %s", steady.Node)
	}
	since[node.ID()] = f.now.Add(20 * time.Second) // node switched relays after the ticket

	f.now = f.now.Add(91 * time.Second)
	f.eng.Tick(f.now)
	if st, _ := f.eng.JobStatus(steady.JobID); st != StatusReleased {
		t.Fatalf("node connected throughout: want released, got %s", st)
	}
	if st, _ := f.eng.JobStatus(reconnected.JobID); st != StatusHeld {
		t.Fatalf("node reconnected: want held, got %s", st)
	}
	// The kept job no longer takes the node's only slot (a node that
	// restarted lost it for good).
	f.offer(node, "m", priceIn, priceOut, 1)
	r, err := f.eng.IssueTicket(buyer2.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 1000, Node: node.ID()})
	if err != nil {
		t.Fatalf("slot still taken by the kept job: %v", err)
	}
	if err := f.eng.Abandon(node.ID(), protocol.AbandonRequest{Ticket: r.Ticket, Reason: "test", NoFault: true}); err != nil {
		t.Fatal(err)
	}
	// Its late start report is still accepted, and settlement proceeds as usual.
	if err := f.eng.MarkStarted(node.ID(), *reconnected); err != nil {
		t.Fatalf("late start report: %v", err)
	}
	sr := signReceipt(t, node, reconnected, 100, 50)
	if resp, err := f.eng.Settle(node.ID(), protocol.SettleRequest{Ticket: *reconnected, Receipt: sr, Ack: ack(buyer, &sr)}); err != nil || resp.Status != string(StatusFinal) {
		t.Fatalf("settle: %+v %v", resp, err)
	}

	// Without a start report the job is released once the grace runs out.
	unreported := mustKeys(t)
	since[unreported.ID()] = f.now.Add(-time.Hour)
	f.offer(unreported, "m", priceIn, priceOut, 1)
	resp, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 1000, Node: unreported.ID()})
	if err != nil {
		t.Fatal(err)
	}
	tk := &resp.Ticket
	since[unreported.ID()] = f.now.Add(10 * time.Second)
	f.now = f.now.Add(4 * time.Minute)
	f.eng.Tick(f.now)
	if st, _ := f.eng.JobStatus(tk.JobID); st != StatusHeld {
		t.Fatalf("within the reconnect grace: want held, got %s", st)
	}
	f.now = f.now.Add(2 * time.Minute) // 5m10s after the reconnect
	f.eng.Tick(f.now)
	if st, _ := f.eng.JobStatus(tk.JobID); st != StatusReleased || f.stats(unreported).Failures != 0 {
		t.Fatalf("after the reconnect grace: %s, failures %d", st, f.stats(unreported).Failures)
	}
	f.booksBalance()
}

// A job released early as never started is still paid when its settlement
// arrives before the settlement deadline with the buyer's acknowledgement
// (the start report was lost): the buyer is charged the receipt's cost and
// the node paid. Without an ack, past the deadline, or when the buyer can no
// longer pay, the settlement is refused.
func TestLateSettlementOfAnEarlyReleasedJob(t *testing.T) {
	f := newFixture(t, Config{FeeBps: 500, TicketTTL: time.Minute, StartGrace: 30 * time.Second, SettleGrace: 15 * time.Minute})
	node, buyer := mustKeys(t), mustKeys(t)
	f.offer(node, "m", priceIn, priceOut, 4)
	f.deposit(buyer, 10_000)
	tk := f.ticket(buyer, 5_000)
	noAck := f.ticket(buyer, 1_000)
	tooLate := f.ticket(buyer, 1_000)
	f.now = f.now.Add(91 * time.Second)
	f.eng.Tick(f.now)
	for _, x := range []*protocol.Ticket{tk, noAck, tooLate} {
		if st, _ := f.eng.JobStatus(x.JobID); st != StatusReleased {
			t.Fatalf("want released early, got %s", st)
		}
	}
	if f.balance(buyer) != 10_000 || f.held(buyer) != 0 {
		t.Fatalf("after release: balance %d held %d", f.balance(buyer), f.held(buyer))
	}

	// A plain receipt, signed by the node alone, proves nothing.
	sr := signReceipt(t, node, noAck, 100, 50)
	if _, err := f.eng.Settle(node.ID(), protocol.SettleRequest{Ticket: *noAck, Receipt: sr}); protocol.CodeOf(err) != protocol.CodeConflict {
		t.Fatalf("no ack: want conflict, got %v", err)
	}
	// An ack the node forged is rejected.
	sr = signReceipt(t, node, tk, 1000, 500) // 1000*1 + 500*2 = 2000 micros
	if _, err := f.eng.Settle(node.ID(), protocol.SettleRequest{Ticket: *tk, Receipt: sr, Ack: ack(node, &sr)}); protocol.CodeOf(err) != protocol.CodeBadRequest {
		t.Fatalf("forged ack: want bad request, got %v", err)
	}
	// The buyer's ack: paid as a normal settlement, and idempotent.
	req := protocol.SettleRequest{Ticket: *tk, Receipt: sr, Ack: ack(buyer, &sr)}
	resp, err := f.eng.Settle(node.ID(), req)
	if err != nil || resp.Status != string(StatusFinal) || resp.CostMicros != 2000 || resp.FeeMicros != 100 {
		t.Fatalf("late settle: %+v %v", resp, err)
	}
	if again, err := f.eng.Settle(node.ID(), req); err != nil || again.Status != string(StatusFinal) {
		t.Fatalf("repeat: %+v %v", again, err)
	}
	if f.balance(buyer) != 8_000 || f.balance(node) != 1_900 || f.stats(node).Successes != 1 {
		t.Fatalf("buyer %d node %d stats %+v", f.balance(buyer), f.balance(node), f.stats(node))
	}
	if st, _ := f.eng.JobStatus(tk.JobID); st != StatusFinal {
		t.Fatalf("job status %s", st)
	}

	// Past the settlement deadline (ticket + SettleGrace) it is refused.
	f.now = f.now.Add(15 * time.Minute)
	sr = signReceipt(t, node, tooLate, 100, 50)
	if _, err := f.eng.Settle(node.ID(), protocol.SettleRequest{Ticket: *tooLate, Receipt: sr, Ack: ack(buyer, &sr)}); protocol.CodeOf(err) != protocol.CodeConflict {
		t.Fatalf("past the deadline: want conflict, got %v", err)
	}
	f.booksBalance()
}

// A late settlement the buyer can no longer cover is refused and changes
// nothing.
func TestLateSettlementTheBuyerCannotPay(t *testing.T) {
	f := newFixture(t, Config{TicketTTL: time.Minute, StartGrace: 30 * time.Second})
	node, buyer := mustKeys(t), mustKeys(t)
	f.offer(node, "m", priceIn, priceOut, 4)
	f.deposit(buyer, 3_000)
	tk := f.ticket(buyer, 3_000)
	f.now = f.now.Add(91 * time.Second)
	f.eng.Tick(f.now)
	// The released funds are spent on another job meanwhile.
	f.offer(node, "m", priceIn, priceOut, 4)
	spend := f.ticket(buyer, 3_000)
	sr := signReceipt(t, node, spend, 1000, 1000) // 3000
	if _, err := f.eng.Settle(node.ID(), protocol.SettleRequest{Ticket: *spend, Receipt: sr, Ack: ack(buyer, &sr)}); err != nil {
		t.Fatal(err)
	}
	sr = signReceipt(t, node, tk, 1000, 500)
	if _, err := f.eng.Settle(node.ID(), protocol.SettleRequest{Ticket: *tk, Receipt: sr, Ack: ack(buyer, &sr)}); protocol.CodeOf(err) != protocol.CodeConflict {
		t.Fatalf("unpayable: want conflict, got %v", err)
	}
	if st, _ := f.eng.JobStatus(tk.JobID); st != StatusReleased || f.balance(buyer) != 0 {
		t.Fatalf("status %s buyer %d", st, f.balance(buyer))
	}
	f.booksBalance()
}

// The default hoarding limits: 5 unused tickets, counted after 15 s.
func TestStaleTicketDefaults(t *testing.T) {
	f := newFixture(t, Config{})
	node, buyer := mustKeys(t), mustKeys(t)
	f.offer(node, "m", priceIn, priceOut, 100)
	f.deposit(buyer, 1_000_000)
	for range 5 {
		f.ticket(buyer, 10)
	}
	f.now = f.now.Add(14 * time.Second)
	f.offer(node, "m", priceIn, priceOut, 100)
	if _, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 10}); err != nil {
		t.Fatalf("5 tickets 14 s old are not stale yet: %v", err)
	}
	f.now = f.now.Add(2 * time.Second) // the first five are now 16 s old
	if _, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 10}); protocol.CodeOf(err) != protocol.CodeBusy {
		t.Fatalf("5 stale tickets: want busy, got %v", err)
	}
}

// Four stale tickets are below the default limit.
func TestFourStaleTicketsAreAllowedByDefault(t *testing.T) {
	f := newFixture(t, Config{})
	node, buyer := mustKeys(t), mustKeys(t)
	f.offer(node, "m", priceIn, priceOut, 100)
	f.deposit(buyer, 1_000_000)
	for range 4 {
		f.ticket(buyer, 10)
	}
	f.now = f.now.Add(16 * time.Second)
	f.offer(node, "m", priceIn, priceOut, 100)
	if _, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 10}); err != nil {
		t.Fatalf("4 stale tickets: %v", err)
	}
}
