package coordinator

import (
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
