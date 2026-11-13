package coordinator

import (
	"testing"
	"time"

	"sharetoken/internal/accounts"
	"sharetoken/internal/ledger"
	"sharetoken/internal/protocol"
)

// partners makes a and b share partners, as the portal does.
func (f *fixture) partners(st *accounts.Store, a, b *accounts.User) {
	f.t.Helper()
	inv, err := st.CreateShareInvite(a.ID, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := st.JoinShare(b.ID, inv.Code); err != nil {
		f.t.Fatal(err)
	}
}

// Partners use each other's nodes first and for free: no hold, no charge,
// no fee, even with an empty balance and a cheaper node on the market.
func TestSharePartnersUseEachOthersNodesForFree(t *testing.T) {
	f := newStrictFixture(t, Config{FeeBps: 1000})
	st := f.store()
	aliceNode, bobNode, carolNode, bob := mustKeys(t), mustKeys(t), mustKeys(t), mustKeys(t)
	alice := f.bind(st, "alice", aliceNode)
	bobUser := f.bind(st, "bob", bob)
	f.bind(st, "bob", bobNode)
	f.bind(st, "carol", carolNode)
	f.offer(aliceNode, "m", priceIn, priceOut, 4)
	f.offer(carolNode, "m", priceIn/2, priceOut/2, 4)

	if _, err := f.eng.IssueTicket(bob.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 1000}); protocol.CodeOf(err) != protocol.CodeInsufficientFunds {
		t.Fatalf("no partners and no credits: %v", err)
	}
	f.partners(st, alice, bobUser)

	ms, err := f.eng.ModelsFor(bob.ID())
	if err != nil || len(ms) != 1 || ms[0].Nodes != 2 || ms[0].SharedNodes != 1 {
		t.Fatalf("bob's models: %+v %v", ms, err)
	}
	tk := f.ticket(bob, 100_000)
	if tk.Node != aliceNode.ID() || !tk.Shared || tk.ReservedMicros == 0 {
		t.Fatalf("bob got %+v, want a shared ticket for alice's node", tk)
	}
	sr := signReceipt(t, aliceNode, tk, 1000, 500)
	resp, err := f.eng.Settle(aliceNode.ID(), protocol.SettleRequest{Ticket: *tk, Receipt: sr, Ack: ack(bob, &sr)})
	if err != nil || resp.Status != string(StatusFinal) || resp.CostMicros != 0 || resp.FeeMicros != 0 {
		t.Fatalf("settle: %+v %v", resp, err)
	}
	for who, acct := range map[string]string{"alice": ledger.UserAccount(alice.ID), "bob": ledger.UserAccount(bobUser.ID), "fees": ledger.PlatformFees} {
		if got := f.account(acct); got != 0 {
			t.Fatalf("%s's balance moved: %d", who, got)
		}
	}
	if err := f.eng.CheckBooks(); err != nil {
		t.Fatal(err)
	}
	// Free jobs earn no reputation: two accounts of one person could farm it.
	if st := f.stats(aliceNode); st.Successes != 0 || st.Reputation != startReputation {
		t.Fatalf("alice's node record moved: %+v", st)
	}
	jobs, err := AccountJobs(f.eng.db, ledger.UserAccount(bobUser.ID), JobQuery{Limit: 10})
	if err != nil || len(jobs) != 1 || !jobs[0].Shared || jobs[0].CostMicros != 0 || jobs[0].CompletionTokens != 500 {
		t.Fatalf("bob's jobs: %+v %v", jobs, err)
	}
	if n, err := SharedTokensServed(f.eng.db, ledger.UserAccount(alice.ID), 0); err != nil || n != 1500 {
		t.Fatalf("alice served %d %v", n, err)
	}
	flows, err := SharedFlows(f.eng.db, ledger.UserAccount(bobUser.ID), 0)
	if err != nil || flows[ledger.UserAccount(alice.ID)] != (ShareFlow{Taken: 1500, Jobs: 1}) {
		t.Fatalf("bob's flows: %+v %v", flows, err)
	}

	// It goes both ways: alice's laptop runs on bob's node.
	aliceLaptop := mustKeys(t)
	f.bind(st, "alice", aliceLaptop)
	f.offer(bobNode, "m", priceIn, priceOut, 4)
	if tk := f.ticket(aliceLaptop, 1000); tk.Node != bobNode.ID() || !tk.Shared {
		t.Fatalf("alice got %+v", tk)
	}

	// A shared ticket nobody uses is released without touching the ledger.
	tk2 := f.ticket(bob, 1000)
	f.now = f.now.Add(time.Hour)
	f.eng.Tick(f.now)
	if s, _ := f.eng.JobStatus(tk2.JobID); s != StatusReleased {
		t.Fatalf("unused shared ticket: %s", s)
	}
	if err := f.eng.CheckBooks(); err != nil {
		t.Fatal(err)
	}

	// Parting ends it: bob is back on the market and has to pay.
	f.offer(aliceNode, "m", priceIn, priceOut, 4)
	f.offer(carolNode, "m", priceIn/2, priceOut/2, 4)
	if err := st.LeaveSharePartner(bobUser.ID, alice.ID); err != nil {
		t.Fatal(err)
	}
	if ps, _ := st.SharePartners(alice.ID); len(ps) != 0 {
		t.Fatalf("alice still has partners: %+v", ps)
	}
	f.deposit(bob, 1_000_000)
	if tk := f.ticket(bob, 1000); tk.Shared || tk.Node != carolNode.ID() {
		t.Fatalf("after parting: %+v", tk)
	}
}

// What a partner gives is bounded by its daily token limit and its part of
// each node's slots; past them the buyer goes to the market, or waits when it
// uses partners only.
func TestSharePartnerLimits(t *testing.T) {
	f := newStrictFixture(t, Config{})
	st := f.store()
	aliceNode, carolNode, bob := mustKeys(t), mustKeys(t), mustKeys(t)
	alice := f.bind(st, "alice", aliceNode)
	bobUser := f.bind(st, "bob", bob)
	f.bind(st, "carol", carolNode)
	f.partners(st, bobUser, alice)
	f.offer(aliceNode, "m", priceIn, priceOut, 4)
	f.offer(carolNode, "m", priceIn, priceOut, 4)
	f.deposit(bob, 1_000_000)

	// Half of alice's 4 slots: the third concurrent job goes to the market.
	if err := st.SetShareSettings(alice.ID, accounts.ShareSettings{SharePct: 50}); err != nil {
		t.Fatal(err)
	}
	var shared []*protocol.Ticket
	for i := 0; i < 2; i++ {
		tk := f.ticket(bob, 100_000)
		if !tk.Shared || tk.Node != aliceNode.ID() {
			t.Fatalf("job %d: %+v", i, tk)
		}
		shared = append(shared, tk)
	}
	// (The market may still pick alice's node, at her price.)
	if tk := f.ticket(bob, 1000); tk.Shared || tk.ReservedMicros == 0 {
		t.Fatalf("over alice's share: %+v", tk)
	}
	// Partners only: bob waits instead.
	if err := st.SetShareSettings(bobUser.ID, accounts.ShareSettings{OnlyPartners: true, SharePct: 100}); err != nil {
		t.Fatal(err)
	}
	_, err := f.eng.IssueTicket(bob.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 1000})
	if protocol.CodeOf(err) != protocol.CodeCapacity {
		t.Fatalf("partners only, partner busy: %v", err)
	}
	if ms, _ := f.eng.ModelsFor(bob.ID()); len(ms) != 1 || ms[0].Nodes != 1 || ms[0].SharedNodes != 1 {
		t.Fatalf("partners only: models %+v", ms)
	}
	if _, err := f.eng.IssueTicket(bob.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "x", BudgetMicros: 1000}); protocol.CodeOf(err) != protocol.CodeNoOffers {
		t.Fatalf("partners only, model nobody shares: %v", err)
	}

	// The daily limit: once alice served 1000 tokens, she gives no more for 24 hours.
	for _, tk := range shared {
		sr := signReceipt(t, aliceNode, tk, 400, 200)
		if _, err := f.eng.Settle(aliceNode.ID(), protocol.SettleRequest{Ticket: *tk, Receipt: sr, Ack: ack(bob, &sr)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetShareSettings(alice.ID, accounts.ShareSettings{SharePct: 100, DailyLimitTokens: 1000}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.eng.IssueTicket(bob.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: "m", BudgetMicros: 1000}); protocol.CodeOf(err) != protocol.CodeCapacity {
		t.Fatalf("partners only, limit used up: %v", err)
	}
	if err := st.SetShareSettings(bobUser.ID, accounts.DefaultShareSettings); err != nil {
		t.Fatal(err)
	}
	if tk := f.ticket(bob, 1000); tk.Shared {
		t.Fatalf("limit used up: %+v", tk)
	}
	f.now = f.now.Add(accounts.ShareWindow + time.Minute)
	f.eng.Tick(f.now)
	f.offer(aliceNode, "m", priceIn, priceOut, 4)
	f.offer(carolNode, "m", priceIn, priceOut, 4)
	if tk := f.ticket(bob, 1000); !tk.Shared {
		t.Fatalf("a day later: %+v", tk)
	}

	// Sharing paused (0%): no partner jobs at all.
	if err := st.SetShareSettings(alice.ID, accounts.ShareSettings{SharePct: 0}); err != nil {
		t.Fatal(err)
	}
	if tk := f.ticket(bob, 1000); tk.Shared {
		t.Fatalf("paused: %+v", tk)
	}
	if ms, _ := f.eng.ModelsFor(bob.ID()); len(ms) != 1 || ms[0].SharedNodes != 0 {
		t.Fatalf("paused: models %+v", ms)
	}
}

func TestPartnerSlots(t *testing.T) {
	for _, c := range []struct {
		conc uint32
		pct  int64
		want uint32
	}{{4, 100, 4}, {4, 50, 2}, {4, 10, 1}, {2, 0, 0}, {3, 60, 1}, {10, 75, 7}} {
		if got := accounts.PartnerSlots(c.conc, c.pct); got != c.want {
			t.Errorf("PartnerSlots(%d, %d) = %d, want %d", c.conc, c.pct, got, c.want)
		}
	}
}
