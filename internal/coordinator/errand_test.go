package coordinator

import (
	"errors"
	"slices"
	"testing"
	"time"

	"sharetoken/internal/ledger"
	"sharetoken/internal/p2p"
	"sharetoken/internal/protocol"
)

func errandReq(node string) protocol.TicketRequest {
	return protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", Node: node, BudgetMicros: 100_000, MaxTokens: 64, PromptTokens: 50}
}

// An errand ticket names the carrier as the buyer and the platform as the
// payer: the carrier needs no credit and none of it is held; the node is
// paid out of the platform's fees, and what the job did not use goes back.
func TestErrandTicketIsPaidByThePlatform(t *testing.T) {
	f := newStrictFixture(t, Config{FeeBps: 1000})
	st := f.store()
	node, carrier := mustKeys(t), mustKeys(t)
	seller := f.bind(st, "alice", node)
	buyer := f.bind(st, "bob", carrier)
	f.offer(node, "m", priceIn, priceOut, 4)

	tr, err := f.eng.IssueErrandTicket(carrier.ID(), errandReq(node.ID()))
	if err != nil {
		t.Fatal(err)
	}
	tk := &tr.Ticket
	if tk.Buyer != carrier.ID() || tk.Node != node.ID() || tk.Shared || tk.ReservedMicros == 0 {
		t.Fatalf("ticket: %+v", tk)
	}
	if b, _ := f.eng.Balance(carrier.ID()); b.HeldMicros != 0 || b.AvailableMicros != 0 {
		t.Fatalf("the carrier's balance: %+v", b)
	}
	// The carrier cannot dispute it, nor send evidence for it.
	if err := f.eng.Dispute(carrier.ID(), protocol.DisputeRequest{JobID: tk.JobID, Reason: "x"}); protocol.CodeOf(err) != protocol.CodeForbidden {
		t.Fatalf("dispute of an errand: %v", err)
	}
	sr := signReceipt(t, node, tk, 100, 20)
	resp, err := f.eng.Settle(node.ID(), protocol.SettleRequest{Ticket: *tk, Receipt: sr, Ack: ack(carrier, &sr)})
	if err != nil || resp.Status != string(StatusFinal) || resp.CostMicros == 0 {
		t.Fatalf("settle: %+v %v", resp, err)
	}
	if got := f.account(ledger.UserAccount(seller.ID)); got != int64(resp.CostMicros-resp.FeeMicros) {
		t.Fatalf("the seller got %d of %d (fee %d)", got, resp.CostMicros, resp.FeeMicros)
	}
	if got := f.account(ledger.UserAccount(buyer.ID)); got != 0 {
		t.Fatalf("the carrier's account moved: %d", got)
	}
	f.now = f.now.Add(2 * time.Minute)
	f.eng.Tick(f.now)
	if got := f.account(string(AuditAccount)); got != 0 {
		t.Fatalf("audit funds left: %d", got)
	}
	if err := f.eng.CheckBooks(); err != nil {
		t.Fatal(err)
	}
	// The carried job is none of the carrier's usage.
	if u, err := AccountUsage(f.eng.db, ledger.UserAccount(buyer.ID)); err != nil || u.Bought.Jobs != 0 {
		t.Fatalf("the carrier's usage: %+v %v", u, err)
	}
}

// Who may carry a question to a node: a device the market could match to it
// for money. Not the seller's own devices, not its share partners, not a
// stranger to a private seller, not a buyer paired elsewhere, not a peer
// without an account; and buyers with purchases come before the rest.
func TestErrandCarriers(t *testing.T) {
	f := newStrictFixture(t, Config{})
	st := f.store()
	node, own, friend, paired, plain, stranger, other := mustKeys(t), mustKeys(t), mustKeys(t), mustKeys(t), mustKeys(t), mustKeys(t), mustKeys(t)
	alice := f.bind(st, "alice", node)
	f.bind(st, "alice", own)
	friendUser := f.bind(st, "frank", friend)
	pairedUser := f.bind(st, "pat", paired)
	f.bind(st, "bob", plain)
	otherSeller := f.bind(st, "carol", other)
	f.offer(node, "m", priceIn, priceOut, 4)
	f.offer(other, "m", priceIn, priceOut, 4)
	f.partners(st, alice, friendUser)
	code, err := st.CreatePairingCode(otherSeller.ID, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.RedeemPairingCode(pairedUser.ID, code.Code); err != nil {
		t.Fatal(err)
	}

	all := []string{own.ID(), friend.ID(), paired.ID(), plain.ID(), stranger.ID(), node.ID()}
	got, err := f.eng.ErrandCarriers(node.ID(), all)
	if err != nil || !slices.Equal(got, []string{plain.ID()}) {
		t.Fatalf("carriers: %v %v, want only bob's device", got, err)
	}
	for _, k := range []*p2p.Keys{own, friend, paired, stranger, node} {
		if _, err := f.eng.IssueErrandTicket(k.ID(), errandReq(node.ID())); err == nil {
			t.Fatalf("an errand ticket for a device that may not carry (%s)", k.ID())
		}
	}
	// A private seller is audited through the buyers paired with it.
	if err := st.SetPrivateSeller(alice.ID, true); err != nil {
		t.Fatal(err)
	}
	if got, err := f.eng.ErrandCarriers(node.ID(), all); err != nil || len(got) != 0 {
		t.Fatalf("carriers of a private seller: %v %v", got, err)
	}
	// An unknown or expired offer has no carriers.
	if got, err := f.eng.ErrandCarriers(stranger.ID(), all); err != nil || len(got) != 0 {
		t.Fatalf("carriers of no node: %v %v", got, err)
	}
}

// The board: a job waits for its carrier's next poll and is made then; only
// that device can hand it back; devices that stop asking drop out.
func TestErrandBoard(t *testing.T) {
	b := newErrands()
	now := time.Unix(1_800_000_000, 0)
	b.now = func() time.Time { return now }
	b.SetPollAfter(time.Minute)

	if e := b.poll("a"); e.Job != nil || e.PollAfter != 60 {
		t.Fatalf("an idle poll: %+v", e)
	}
	b.poll("b")
	if got := b.Carriers(); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("carriers: %v", got)
	}
	made := 0
	job := func() (*protocol.ErrandJob, error) {
		made++
		return &protocol.ErrandJob{Ticket: protocol.Ticket{JobID: "j1"}}, nil
	}
	res, withdraw, err := b.Post("a", job)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.Post("a", job); err == nil {
		t.Fatal("two errands for one device")
	}
	if got := b.Carriers(); !slices.Equal(got, []string{"b"}) || made != 0 {
		t.Fatalf("carriers while a waits: %v (made %d)", got, made)
	}
	if e := b.poll("b"); e.Job != nil {
		t.Fatal("b got a's job")
	}
	e := b.poll("a")
	if e.Job == nil || e.Job.Ticket.JobID != "j1" || made != 1 {
		t.Fatalf("a's poll: %+v (made %d)", e, made)
	}
	if b.carried("b", "j1") || !b.carried("a", "j1") || b.deliver("b", "j1", ErrandResult{}) {
		t.Fatal("another device can hand the job back")
	}
	if got := b.Carriers(); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("carriers while a carries: %v", got)
	}
	if !b.deliver("a", "j1", ErrandResult{Via: "r"}) || b.deliver("a", "j1", ErrandResult{}) {
		t.Fatal("a job is handed back once")
	}
	if r := <-res; r.Via != "r" {
		t.Fatalf("result: %+v", r)
	}
	withdraw()

	// A job that cannot be made tells whoever posted it.
	res, withdraw, _ = b.Post("b", func() (*protocol.ErrandJob, error) { return nil, errors.New("no funds") })
	if e := b.poll("b"); e.Job != nil {
		t.Fatal("a job that was not made")
	}
	if r := <-res; r.Error == "" {
		t.Fatalf("result: %+v", r)
	}
	withdraw()

	// Withdrawn before the poll: the device gets nothing.
	_, withdraw, _ = b.Post("a", job)
	withdraw()
	if e := b.poll("a"); e.Job != nil || made != 1 {
		t.Fatal("a withdrawn job was handed out")
	}

	b.Bench("a", time.Hour)
	if got := b.Carriers(); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("carriers with a benched: %v", got)
	}
	now = now.Add(b.Window() + time.Second)
	if got := b.Carriers(); len(got) != 0 {
		t.Fatalf("devices that stopped asking: %v", got)
	}
	now = now.Add(time.Hour)
	b.poll("a")
	if got := b.Carriers(); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("after the bench: %v", got)
	}
}
