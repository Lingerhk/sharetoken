package coordinator

import (
	"testing"
	"time"

	"sharetoken/protocol"
)

// The portal resolves disputes in its own process through ResolveDispute;
// it must see the same jobs and settle them exactly like the engine.
func TestPortalSideDisputeResolution(t *testing.T) {
	f := newFixture(t, Config{DisputeWindow: time.Hour, FeeBps: 1000})
	if err := f.eng.RecordFeeRate(); err != nil { // the serving coordinator does this
		t.Fatal(err)
	}
	node, buyer := mustKeys(t), mustKeys(t)
	f.offer(node, "m", priceIn, priceOut, 4)
	f.deposit(buyer, 1_000_000)
	dispute := func() *protocol.Ticket {
		tk := f.ticket(buyer, 100_000)
		if _, err := f.eng.Settle(node.ID(), protocol.SettleRequest{Ticket: *tk, Receipt: signReceipt(t, node, tk, 10, 5)}); err != nil {
			t.Fatal(err)
		}
		if err := f.eng.Dispute(buyer.ID(), protocol.DisputeRequest{JobID: tk.JobID, Reason: "inflated usage"}); err != nil {
			t.Fatal(err)
		}
		return tk
	}
	tk1, tk2 := dispute(), dispute()

	ds, err := QueryDisputes(f.eng.db, 10)
	if err != nil || len(ds) != 2 || ds[0].Status != StatusDisputed || ds[0].Reason != "inflated usage" {
		t.Fatalf("disputes: %+v %v", ds, err)
	}
	if err := ResolveDispute(f.eng.db, "nope", true, f.now); protocol.CodeOf(err) != protocol.CodeNotFound {
		t.Fatalf("unknown job: %v", err)
	}
	if err := ResolveDispute(f.eng.db, tk1.JobID, false, f.now); err != nil {
		t.Fatal(err)
	}
	if err := ResolveDispute(f.eng.db, tk1.JobID, false, f.now); err == nil {
		t.Fatal("resolving twice must fail")
	}
	if err := ResolveDispute(f.eng.db, tk2.JobID, true, f.now); err != nil {
		t.Fatal(err)
	}
	cost := uint64(10)*priceIn/1_000_000 + uint64(5)*priceOut/1_000_000
	if st, _ := f.eng.JobStatus(tk1.JobID); st != StatusRefunded {
		t.Fatalf("refunded job is %s", st)
	}
	if st, _ := f.eng.JobStatus(tk2.JobID); st != StatusFinal {
		t.Fatalf("paid job is %s", st)
	}
	if got := f.balance(buyer); got != 1_000_000-int64(cost) { // tk1's hold came back, tk2's was captured
		t.Fatalf("buyer balance %d, want %d", got, 1_000_000-int64(cost))
	}
	fee := int64(cost) * 1000 / 10_000
	if got := f.balance(node); got != int64(cost)-fee {
		t.Fatalf("node earned %d, want %d (fee %d from meta)", got, int64(cost)-fee, fee)
	}
	st := f.stats(node)
	if st.DisputesLost != 1 || st.Successes != 1 {
		t.Fatalf("node stats: %+v", st)
	}
	// An admin command opens the engine without a fee and must not reset the rate.
	if _, err := NewEngine(f.eng.self, f.eng.priv, Config{DB: f.eng.db, OpenAccounts: true}); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := f.eng.db.QueryRow("SELECT value FROM meta WHERE `key` = 'fee_bps'").Scan(&raw); err != nil || raw != "1000" {
		t.Fatalf("fee_bps after an admin engine: %q %v", raw, err)
	}
	f.booksBalance()
	jobs, err := QueryJobs(f.eng.db, "", "", 0, 10)
	if err != nil || len(jobs) != 2 {
		t.Fatalf("jobs: %+v %v", jobs, err)
	}
	counts, _ := JobCounts(f.eng.db)
	if counts[StatusFinal] != 1 || counts[StatusRefunded] != 1 {
		t.Fatalf("counts: %+v", counts)
	}
}
