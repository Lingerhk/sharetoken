package coordinator

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"sharetoken/internal/sqlite"
	"sharetoken/protocol"
)

// explain returns the query plan of a statement as one string.
func explain(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	if sqlite.IsMySQL(db) {
		t.Skip("asserts SQLite query plans")
	}
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatal(err)
		}
		out = append(out, detail)
	}
	return strings.Join(out, "\n")
}

// The admin job listing must page through the table by index in every
// filter combination: with 75k jobs a sort over the whole table (or over
// an account's whole history) made each page take hundreds of ms.
func TestQueryJobsUsesIndexes(t *testing.T) {
	f := newFixture(t, Config{DisputeWindow: time.Hour})
	node, buyer := mustKeys(t), mustKeys(t)
	f.offer(node, "m", priceIn, priceOut, 4)
	f.deposit(buyer, 1_000_000)
	f.deposit(node, 1_000_000)
	var ids []string
	for i := 0; i < 3; i++ {
		tk := f.ticket(buyer, 100_000)
		if _, err := f.eng.Settle(node.ID(), protocol.SettleRequest{Ticket: *tk, Receipt: signReceipt(t, node, tk, 10, 5)}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, tk.JobID)
		f.now = f.now.Add(time.Second)
	}
	var acct string
	if err := f.eng.db.QueryRow(`SELECT node_acct FROM jobs WHERE job_id = ?`, ids[0]).Scan(&acct); err != nil {
		t.Fatal(err)
	}
	// The node also buys once (the matcher skips its own offer), so its
	// account appears on both sides of the listing.
	other := mustKeys(t)
	f.offer(other, "m", priceIn, priceOut, 4)
	bought := f.ticket(node, 100_000)
	if _, err := f.eng.Settle(other.ID(), protocol.SettleRequest{Ticket: *bought, Receipt: signReceipt(t, other, bought, 10, 5)}); err != nil {
		t.Fatal(err)
	}

	all, err := QueryJobs(f.eng.db, "", "", 0, 10)
	if err != nil || len(all) != 4 || all[0].JobID != bought.JobID || all[3].JobID != ids[0] {
		t.Fatalf("all jobs: %+v %v", all, err)
	}
	pending, err := QueryJobs(f.eng.db, string(StatusPending), "", 0, 10)
	if err != nil || len(pending) != 4 {
		t.Fatalf("pending: %d %v", len(pending), err)
	}
	if none, err := QueryJobs(f.eng.db, string(StatusFinal), "", 0, 10); err != nil || len(none) != 0 {
		t.Fatalf("final: %+v %v", none, err)
	}
	mine, err := QueryJobs(f.eng.db, "", acct, 0, 10)
	if err != nil || len(mine) != 4 || mine[0].JobID != bought.JobID || mine[0].BuyerAcct != acct || mine[1].NodeAcct != acct {
		t.Fatalf("node account's jobs, bought and served, merged newest first: %+v %v", mine, err)
	}
	page, err := QueryJobs(f.eng.db, string(StatusPending), acct, mine[0].CreatedAt, 2)
	if err != nil || len(page) != 2 || page[0].JobID != ids[2] || page[1].JobID != ids[1] {
		t.Fatalf("second page: %+v %v", page, err)
	}

	// Every variant must walk a fitting index in created_at order: the only
	// sort allowed is the merge of the two account sides, each already cut
	// to limit rows.
	cases := map[string]struct {
		status, acct string
		index        string
	}{
		"all":            {"", "", "jobs_by_created"},
		"status":         {string(StatusPending), "", "jobs_by_status_created"},
		"account":        {"", acct, "_acct"},
		"account+status": {string(StatusPending), acct, "_acct"},
	}
	for name, c := range cases {
		query, args := queryJobsSQL(c.status, c.acct, 1<<62, 10)
		plan := explain(t, f.eng.db, query, args...)
		sorts := strings.Count(plan, "TEMP B-TREE")
		if c.acct == "" && sorts > 0 || c.acct != "" && sorts > 2 || strings.Contains(plan, "SCAN jobs") {
			t.Errorf("%s: plan sorts or scans the table:\n%s", name, plan)
		}
		for _, line := range strings.Split(plan, "\n") {
			if strings.Contains(line, "SEARCH jobs") && !strings.Contains(line, c.index) {
				t.Errorf("%s: wrong index: %s", name, line)
			}
		}
	}
}

// The disputes listing and the journal must not scan their tables either.
func TestDisputesAndJournalUseIndexes(t *testing.T) {
	f := newFixture(t, Config{DisputeWindow: time.Hour})
	plan := explain(t, f.eng.db, `SELECT job_id FROM (
			SELECT * FROM (SELECT job_id, status, dispute_outcome, created_at FROM jobs WHERE status = 'disputed' ORDER BY created_at DESC LIMIT ?)
			UNION SELECT * FROM (SELECT job_id, status, dispute_outcome, created_at FROM jobs WHERE dispute_outcome != '' ORDER BY created_at DESC LIMIT ?))
		ORDER BY status = 'disputed' DESC, created_at DESC LIMIT ?`, 10, 10, 10)
	// "SCAN jobs USING INDEX jobs_judged" walks the (tiny) partial index in
	// order; only a scan of the table itself is wrong.
	if strings.Contains(plan, "SCAN jobs\n") || strings.HasSuffix(plan, "SCAN jobs") || !strings.Contains(plan, "jobs_judged") || !strings.Contains(plan, "jobs_by_status_created") {
		t.Errorf("disputes plan:\n%s", plan)
	}
	for _, col := range []string{"debit", "credit"} {
		plan := explain(t, f.eng.db, `SELECT seq FROM ledger_entries WHERE `+col+` = ? AND seq < ? ORDER BY seq DESC LIMIT ?`, "a", 1<<62, 10)
		if !strings.Contains(plan, "ledger_entries_by_"+col) || strings.Contains(plan, "TEMP B-TREE") {
			t.Errorf("journal by %s:\n%s", col, plan)
		}
	}
}
