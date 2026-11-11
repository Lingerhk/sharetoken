package coordinator

import (
	"strings"
	"testing"
	"time"

	"sharetoken/internal/accounts"
	"sharetoken/internal/ledger"
	"sharetoken/internal/p2p"
	"sharetoken/internal/protocol"
)

// bind registers user (if new) and binds k to it, as the portal does.
func (f *fixture) bind(store *accounts.Store, user string, k *p2p.Keys) *accounts.User {
	f.t.Helper()
	u, err := store.UserByName(user)
	if err != nil {
		if u, err = store.CreateUser(user, "correct horse battery", false); err != nil {
			f.t.Fatal(err)
		}
	}
	ts := f.now.UnixNano()
	if _, err := store.BindDevice(u, k.ID(), "test", ts, accounts.SignBind(k.Signer, u.Username, k.ID(), ts)); err != nil {
		f.t.Fatal(err)
	}
	return u
}

func (f *fixture) store() *accounts.Store {
	return &accounts.Store{DB: f.eng.db, Now: func() time.Time { return f.now }}
}

func TestUnboundDevicesAreRefused(t *testing.T) {
	f := newStrictFixture(t, Config{})
	node, buyer := mustKeys(t), mustKeys(t)
	o := f.signedOffer(node, "m", priceIn, priceOut, 4)
	if err := f.eng.UpsertOffer(o); protocol.CodeOf(err) != protocol.CodeUnauthorized {
		t.Fatalf("offer from an unbound node: want unauthorized, got %v", err)
	}
	_, err := f.eng.IssueTicket(buyer.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 10})
	if protocol.CodeOf(err) != protocol.CodeUnauthorized {
		t.Fatalf("ticket for an unbound buyer: want unauthorized, got %v", err)
	}
	if _, err := f.eng.Balance(buyer.ID()); protocol.CodeOf(err) != protocol.CodeUnauthorized {
		t.Fatalf("balance of an unbound buyer: want unauthorized, got %v", err)
	}
}

// Money belongs to the user: earnings of one device can be spent from
// another, a user never buys from its own devices, and a disabled user's
// devices are refused.
func TestDevicesShareTheirUsersAccount(t *testing.T) {
	f := newStrictFixture(t, Config{FeeBps: 1000})
	st := f.store()
	aliceNode, aliceLaptop := mustKeys(t), mustKeys(t)
	bobNode, bob := mustKeys(t), mustKeys(t)
	alice := f.bind(st, "alice", aliceNode)
	f.bind(st, "alice", aliceLaptop)
	bobUser := f.bind(st, "bob", bob)
	f.bind(st, "bob", bobNode)

	f.offer(aliceNode, "m", priceIn, priceOut, 4)
	if err := f.eng.Deposit(bob.ID(), 1_000_000); err != nil {
		t.Fatal(err)
	}

	// Bob buys from Alice's node; Alice's account earns cost - 10%.
	tk := f.ticket(bob, 100_000)
	if tk.Node != aliceNode.ID() {
		t.Fatalf("matched %s", tk.Node)
	}
	sr := signReceipt(t, aliceNode, tk, 1000, 500) // 2000 micros
	if _, err := f.eng.Settle(aliceNode.ID(), protocol.SettleRequest{Ticket: *tk, Receipt: sr, Ack: ack(bob, &sr)}); err != nil {
		t.Fatal(err)
	}
	if got := f.account(ledger.UserAccount(alice.ID)); got != 1800 {
		t.Fatalf("alice earned %d, want 1800", got)
	}
	// Bob's other device sees the same balance.
	b, err := f.eng.Balance(bobNode.ID())
	if err != nil || b.AvailableMicros != 1_000_000-2000 {
		t.Fatalf("bob's second device: %+v %v", b, err)
	}

	// Alice's laptop spends what her node earned, but never buys from her
	// own node: with only her node online there is no match.
	_, err = f.eng.IssueTicket(aliceLaptop.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 1000})
	if protocol.CodeOf(err) != protocol.CodeNoOffers {
		t.Fatalf("alice matched her own node: %v", err)
	}
	f.offer(bobNode, "m", priceIn, priceOut, 4)
	tk2 := f.ticket(aliceLaptop, 1000)
	if tk2.Node != bobNode.ID() {
		t.Fatalf("alice matched %s, want bob's node", tk2.Node)
	}
	if got := f.account(ledger.UserAccount(alice.ID)); got != 800 {
		t.Fatalf("alice's balance after the hold: %d, want 800", got)
	}

	// History and usage, as the portal reads them.
	jobs, err := AccountJobs(f.eng.db, ledger.UserAccount(alice.ID), JobQuery{Limit: 10})
	if err != nil || len(jobs) != 2 {
		t.Fatalf("alice's jobs: %+v %v", jobs, err)
	}
	if jobs[0].Role == RoleSell { // the fixture's clock stands still, so they tie on time
		jobs[0], jobs[1] = jobs[1], jobs[0]
	}
	if jobs[0].Role != RoleBuy || jobs[1].Role != RoleSell || jobs[1].EarnedMicros != 1800 ||
		jobs[1].PromptTokens != 1000 || jobs[1].CompletionTokens != 500 {
		t.Fatalf("alice's jobs: %+v", jobs)
	}
	u, err := AccountUsage(f.eng.db, ledger.UserAccount(bobUser.ID))
	if err != nil || u.Bought.Jobs != 1 || u.Bought.Micros != 2000 || u.Bought.CompletionTokens != 500 || u.Sold.Pending != 1 {
		t.Fatalf("bob's usage: %+v %v", u, err)
	}

	// Disabling Bob locks out all of his devices.
	if err := st.SetDisabled(bobUser.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.eng.IssueTicket(bob.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 10}); protocol.CodeOf(err) != protocol.CodeUnauthorized {
		t.Fatalf("disabled user: want unauthorized, got %v", err)
	}
	if err := f.eng.UpsertOffer(f.signedOffer(bobNode, "m", priceIn, priceOut, 4)); protocol.CodeOf(err) != protocol.CodeUnauthorized {
		t.Fatalf("disabled user's node: want unauthorized, got %v", err)
	}
	f.booksBalance()
}

// A paired buyer buys only from the sellers it is paired with, and sees only
// their models; everyone else is matched as before.
func TestPairedBuyersBuyOnlyFromTheirSellers(t *testing.T) {
	f := newStrictFixture(t, Config{})
	st := f.store()
	aliceNode, carolNode, bob, dave := mustKeys(t), mustKeys(t), mustKeys(t), mustKeys(t)
	alice := f.bind(st, "alice", aliceNode)
	carol := f.bind(st, "carol", carolNode)
	bobUser := f.bind(st, "bob", bob)
	f.bind(st, "dave", dave)
	for _, k := range []*p2p.Keys{bob, dave} {
		if err := f.eng.Deposit(k.ID(), 1_000_000); err != nil {
			t.Fatal(err)
		}
	}
	// Carol is cheaper and the only one with model "x": an unpaired buyer gets her.
	f.offer(aliceNode, "m", priceIn, priceOut, 64)
	co := f.signedOffer(carolNode, "m", priceIn/2, priceOut/2, 64)
	co.Models = append(co.Models, protocol.ModelPrice{Model: "x", PriceInMicros: priceIn, PriceOutMicros: priceOut})
	co.Sign(carolNode.Signer)
	if err := f.eng.UpsertOffer(co); err != nil {
		t.Fatal(err)
	}
	req := func(model string) protocol.TicketRequest {
		return protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: model, BudgetMicros: 1000}
	}
	names := func(ms []protocol.ModelSummary) (out []string) {
		for _, m := range ms {
			out = append(out, m.Model)
		}
		return out
	}
	if tk := f.ticket(bob, 1000); tk.Node != carolNode.ID() {
		t.Fatalf("unpaired: matched %s, want the cheaper node", tk.Node)
	}

	code, err := st.CreatePairingCode(alice.ID, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.RedeemPairingCode(bobUser.ID, code.Code); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if tk := f.ticket(bob, 1000); tk.Node != aliceNode.ID() {
			t.Fatalf("paired with alice: matched %s", tk.Node)
		}
	}
	_, err = f.eng.IssueTicket(bob.ID(), req("x"))
	if protocol.CodeOf(err) != protocol.CodeNoOffers || !strings.Contains(err.Error(), "paired") {
		t.Fatalf("a model only another seller has: %v", err)
	}
	// Naming a node does not get around it.
	r := req("m")
	r.Node = carolNode.ID()
	if _, err := f.eng.IssueTicket(bob.ID(), r); protocol.CodeOf(err) != protocol.CodeNoOffers {
		t.Fatalf("pinned to an unpaired node: %v", err)
	}
	ms, err := f.eng.ModelsFor(bob.ID())
	if err != nil || len(ms) != 1 || ms[0].Model != "m" || ms[0].Nodes != 1 || ms[0].MinPriceIn != priceIn {
		t.Fatalf("bob's models: %+v %v", ms, err)
	}
	// Dave is not paired: nothing changes for him.
	if ms, _ := f.eng.ModelsFor(dave.ID()); len(ms) != 2 || ms[0].Nodes != 2 {
		t.Fatalf("dave's models: %v", names(ms))
	}
	if tk := f.ticket(dave, 1000); tk.Node != carolNode.ID() {
		t.Fatalf("dave matched %s", tk.Node)
	}
	// A device that belongs to no user (an auditor) is not restricted either.
	if ms, err := f.eng.ModelsFor(mustKeys(t).ID()); err != nil || len(ms) != 2 {
		t.Fatalf("a stranger's models: %v %v", names(ms), err)
	}

	// Pairing with Carol too widens the choice; leaving both lifts the limit.
	c2, _ := st.CreatePairingCode(carol.ID, "", 0, 0)
	if _, err := st.RedeemPairingCode(bobUser.ID, c2.Code); err != nil {
		t.Fatal(err)
	}
	if _, err := f.eng.IssueTicket(bob.ID(), req("x")); err != nil {
		t.Fatalf("after pairing with carol: %v", err)
	}
	if err := st.Unpair(bobUser.ID, carol.ID); err != nil {
		t.Fatal(err)
	}
	// The seller goes offline: the buyer waits for it rather than buying elsewhere.
	if err := f.eng.WithdrawOffer(aliceNode.ID(), f.now.UnixNano()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.eng.IssueTicket(bob.ID(), req("m")); protocol.CodeOf(err) != protocol.CodeNoOffers {
		t.Fatalf("paired seller offline: %v", err)
	}
	if err := st.Unpair(bobUser.ID, alice.ID); err != nil {
		t.Fatal(err)
	}
	if tk := f.ticket(bob, 1000); tk.Node != carolNode.ID() {
		t.Fatalf("after leaving: matched %s", tk.Node)
	}
}

// A private seller serves only the buyers paired with it; the platform's
// audits still reach it.
func TestPrivateSellersServeOnlyTheirBuyers(t *testing.T) {
	f := newStrictFixture(t, Config{})
	st := f.store()
	aliceNode, carolNode, bob, dave, auditor := mustKeys(t), mustKeys(t), mustKeys(t), mustKeys(t), mustKeys(t)
	alice := f.bind(st, "alice", aliceNode)
	f.bind(st, "carol", carolNode)
	bobUser := f.bind(st, "bob", bob)
	f.bind(st, "dave", dave)
	for _, k := range []*p2p.Keys{bob, dave} {
		if err := f.eng.Deposit(k.ID(), 1_000_000); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.eng.RegisterAuditor(auditor.ID(), 100_000); err != nil {
		t.Fatal(err)
	}
	// Alice is the cheaper one and the only seller of "x".
	ao := f.signedOffer(aliceNode, "m", priceIn/2, priceOut/2, 64)
	ao.Models = append(ao.Models, protocol.ModelPrice{Model: "x", PriceInMicros: priceIn, PriceOutMicros: priceOut})
	ao.Sign(aliceNode.Signer)
	if err := f.eng.UpsertOffer(ao); err != nil {
		t.Fatal(err)
	}
	f.offer(carolNode, "m", priceIn, priceOut, 64)
	req := func(model, node string) protocol.TicketRequest {
		return protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: model, BudgetMicros: 1000, Node: node}
	}
	if tk := f.ticket(dave, 1000); tk.Node != aliceNode.ID() {
		t.Fatalf("before private mode: matched %s", tk.Node)
	}

	if err := st.SetPrivateSeller(alice.ID, true); err != nil {
		t.Fatal(err)
	}
	// Dave is not paired with Alice: he is matched elsewhere and does not see her models.
	for i := 0; i < 3; i++ {
		if tk := f.ticket(dave, 1000); tk.Node != carolNode.ID() {
			t.Fatalf("unpaired buyer matched the private seller %s", tk.Node)
		}
	}
	if _, err := f.eng.IssueTicket(dave.ID(), req("x", "")); protocol.CodeOf(err) != protocol.CodeNoOffers {
		t.Fatalf("a model only the private seller has: %v", err)
	}
	if _, err := f.eng.IssueTicket(dave.ID(), req("m", aliceNode.ID())); protocol.CodeOf(err) != protocol.CodeNoOffers {
		t.Fatalf("pinned to the private seller: %v", err)
	}
	if ms, err := f.eng.ModelsFor(dave.ID()); err != nil || len(ms) != 1 || ms[0].Model != "m" || ms[0].Nodes != 1 || ms[0].MinPriceIn != priceIn {
		t.Fatalf("dave's models: %+v %v", ms, err)
	}
	// A peer that is nobody's device sees nothing of her either.
	if ms, _ := f.eng.ModelsFor(mustKeys(t).ID()); len(ms) != 1 {
		t.Fatalf("a stranger's models: %+v", ms)
	}
	// Alice sees her own offers, and audits reach her.
	if ms, _ := f.eng.ModelsFor(aliceNode.ID()); len(ms) != 2 {
		t.Fatalf("the private seller's own models: %+v", ms)
	}
	if ms, _ := f.eng.ModelsFor(auditor.ID()); len(ms) != 2 {
		t.Fatalf("the auditor's models: %+v", ms)
	}
	if _, err := f.eng.IssueTicket(auditor.ID(), req("x", "")); err != nil {
		t.Fatalf("an audit of the private seller: %v", err)
	}

	// Bob pairs with her and buys from her; that also limits him to her.
	code, err := st.CreatePairingCode(alice.ID, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.RedeemPairingCode(bobUser.ID, code.Code); err != nil {
		t.Fatal(err)
	}
	if _, err := f.eng.IssueTicket(bob.ID(), req("x", "")); err != nil {
		t.Fatalf("a paired buyer at the private seller: %v", err)
	}
	if ms, _ := f.eng.ModelsFor(bob.ID()); len(ms) != 2 || ms[0].Nodes != 1 {
		t.Fatalf("bob's models: %+v", ms)
	}

	// Private mode off: everyone is served again.
	if err := st.SetPrivateSeller(alice.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.eng.IssueTicket(dave.ID(), req("x", "")); err != nil {
		t.Fatalf("after private mode: %v", err)
	}
}
