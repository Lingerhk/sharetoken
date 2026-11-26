package coordinator

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"testing"

	"sharetoken/internal/ledger"
)

// The plans below are asked of MySQL on tables shaped like production's: the
// optimizer picks by row counts, so an empty table proves nothing.
const (
	planRows  = 30_000
	planPage  = 50
	planEpoch = int64(1_800_000_000_000_000_000) // row i was created at planEpoch + i

	planBig   = "acct-big"   // a fifth of all rows, spread over time
	planOld   = "acct-old"   // thousands of rows, all among the oldest
	planSmall = "acct-small" // a few rows

	// Rows a page may read: its own, the merge of two sides, and some slack
	// for what the plan skips. Walking the whole table is hundreds of times this.
	planPageBudget = 20 * planPage
)

func planAcct(r *rand.Rand, side string, i int) string {
	n := r.Intn(100)
	switch {
	case n < 20:
		return planBig
	case i < planRows/5 && n < 80:
		return planOld
	case i%2500 == 1:
		return planSmall
	}
	return fmt.Sprintf("acct-%s-%d", side, r.Intn(500))
}

func planStatus(r *rand.Rand) string {
	switch n := r.Intn(200); {
	case n < 170:
		return "final"
	case n < 180:
		return "released"
	case n < 186:
		return "refunded"
	case n < 196:
		return "pending"
	case n < 199:
		return "held"
	}
	return "disputed"
}

func seedPlanTables(t *testing.T, db *sql.DB) {
	t.Helper()
	r := rand.New(rand.NewSource(1))
	insert := func(head string, width int, row func(i int) []any) {
		place := "(" + strings.TrimSuffix(strings.Repeat("?,", width), ",") + ")"
		for start := 0; start < planRows; start += 1000 {
			var args []any
			var marks []string
			for i := start; i < start+1000; i++ {
				args = append(args, row(i)...)
				marks = append(marks, place)
			}
			if _, err := db.Exec(head+strings.Join(marks, ","), args...); err != nil {
				t.Fatal(err)
			}
		}
	}
	insert(`INSERT INTO jobs (job_id, buyer, node, buyer_acct, node_acct, model, status, ticket, cost, fee, dispute_outcome,
		created_at, release_at, finalize_at, done_at, started_at, ticket_expires) VALUES `, 17, func(i int) []any {
		at, outcome, started := planEpoch+int64(i), "", planEpoch+int64(i)
		if r.Intn(400) == 0 {
			outcome = "pay_node"
		}
		if r.Intn(7) == 0 {
			started = 0
		}
		return []any{fmt.Sprintf("job-%d", i), fmt.Sprintf("dev-b-%d", r.Intn(300)), fmt.Sprintf("dev-n-%d", r.Intn(40)),
			planAcct(r, "b", i), planAcct(r, "n", i), "m", planStatus(r), []byte{}, 10, 1, outcome,
			at, at + 1000, at + 2000, at + 3000, started, at + 500}
	})
	insert(`INSERT INTO ledger_entries (at, job_id, debit, credit, amount, memo) VALUES `, 6, func(i int) []any {
		return []any{planEpoch + int64(i), fmt.Sprintf("job-%d", i), planAcct(r, "d", i), planAcct(r, "c", i) + "-c", 10, ""}
	})
	for _, tb := range []string{"jobs", "ledger_entries"} {
		if _, err := db.Exec(`ANALYZE TABLE ` + tb); err != nil {
			t.Fatal(err)
		}
	}
}

// planFixture seeds one database for all of a test's questions.
func planFixture(t *testing.T) *sql.DB {
	t.Helper()
	db := newFixture(t, Config{}).eng.db
	seedPlanTables(t, db)
	return db
}

// queryPlan is what EXPLAIN FORMAT=JSON says about one statement.
type queryPlan struct {
	tables    []planTable // the real tables read, in plan order
	filesorts int
	raw       string
}

type planTable struct {
	Name, Access, Key string
	Covering          bool
}

func explainPlan(t *testing.T, db *sql.DB, query string, args ...any) queryPlan {
	t.Helper()
	var raw string
	if err := db.QueryRow("EXPLAIN FORMAT=JSON "+query, args...).Scan(&raw); err != nil {
		t.Fatalf("%v\n%s", err, query)
	}
	var doc any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	p := queryPlan{raw: raw}
	p.walk(doc)
	return p
}

func (p *queryPlan) walk(v any) {
	switch n := v.(type) {
	case []any:
		for _, e := range n {
			p.walk(e)
		}
	case map[string]any:
		if s, _ := n["using_filesort"].(bool); s {
			p.filesorts++
		}
		if tb, ok := n["table"].(map[string]any); ok {
			name, _ := tb["table_name"].(string)
			if name == "jobs" || name == "ledger_entries" {
				pt := planTable{Name: name}
				pt.Access, _ = tb["access_type"].(string)
				pt.Key, _ = tb["key"].(string)
				pt.Covering, _ = tb["using_index"].(bool)
				p.tables = append(p.tables, pt)
			}
		}
		for _, e := range n {
			p.walk(e)
		}
	}
}

// keys reports the index of each table read, failing on a whole-table scan.
func (p queryPlan) keys(t *testing.T, name string) string {
	t.Helper()
	var keys []string
	for _, tb := range p.tables {
		if tb.Access == "ALL" || tb.Key == "" {
			t.Errorf("%s: scans all of %s:\n%s", name, tb.Name, p.raw)
		}
		keys = append(keys, tb.Key)
	}
	return strings.Join(keys, ",")
}

// rowsRead runs a query to its end and returns how many rows the server read
// for it: what a plan really cost, where EXPLAIN only estimates.
func rowsRead(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	counter := func() int {
		rows, err := conn.QueryContext(ctx, `SHOW SESSION STATUS LIKE 'Handler_read%'`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		total := 0
		for rows.Next() {
			var name, value string
			if err := rows.Scan(&name, &value); err != nil {
				t.Fatal(err)
			}
			n, err := strconv.Atoi(value)
			if err != nil {
				t.Fatal(err)
			}
			total += n
		}
		return total
	}
	base := counter()
	idle := counter() - base // what asking for the counter itself reads
	base = counter()
	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	return counter() - base - idle
}

// The job listing pages by index in every filter: a page must cost about a
// page, whichever account it is for and however its history is spread over
// time. The only sort allowed is the merge of the two account sides, each
// already cut to the page size.
func TestPlansJobListing(t *testing.T) {
	db := planFixture(t)
	const both = "jobs_by_buyer_acct,jobs_by_node_acct"
	before := planEpoch + planRows + 1
	for _, c := range []struct {
		name, status, acct, keys string
		sorts, budget            int
	}{
		{"all", "", "", "jobs_by_created", 0, planPageBudget},
		{"by status", "pending", "", "jobs_by_status_created", 0, planPageBudget},
		{"big account", "", planBig, both, 1, planPageBudget},
		{"old account", "", planOld, both, 1, planPageBudget},
		{"small account", "", planSmall, both, 1, planPageBudget},
		{"big account, common status", "final", planBig, both, 1, planPageBudget},
		{"old account, common status", "final", planOld, both, 1, planPageBudget},
		{"small account, common status", "final", planSmall, both, 1, planPageBudget},
		// A rare status walks the account's own history, never anybody else's.
		{"old account, rare status", "disputed", planOld, both, 1, planRows / 2},
	} {
		query, args := queryJobsSQL(JobQuery{Status: c.status, Acct: c.acct, To: before, Limit: planPage})
		p := explainPlan(t, db, query, args...)
		if keys := p.keys(t, c.name); keys != c.keys || p.filesorts != c.sorts {
			t.Errorf("%s: keys %s with %d sorts, want %s with %d", c.name, keys, p.filesorts, c.keys, c.sorts)
		}
		if n := rowsRead(t, db, query, args...); n > c.budget {
			t.Errorf("%s: a page read %d rows, budget %d (keys %s)", c.name, n, c.budget, p.keys(t, c.name))
		}
	}
}

func TestPlansDisputes(t *testing.T) {
	db := planFixture(t)
	args := disputesArgs(RangeQuery{Limit: planPage})
	p := explainPlan(t, db, disputesSQL, args...)
	if keys := p.keys(t, "disputes"); keys != "jobs_by_status_created,jobs_judged" {
		t.Errorf("disputes: keys %s:\n%s", keys, p.raw)
	}
	if n := rowsRead(t, db, disputesSQL, args...); n > planPageBudget {
		t.Errorf("disputes: a page read %d rows, budget %d", n, planPageBudget)
	}
}

func TestPlansAccountJournal(t *testing.T) {
	db := planFixture(t)
	for _, acct := range []string{planBig, planOld, planSmall} {
		args := ledger.AccountEntriesArgs(acct, 0, 1<<62, planPage, 0)
		p := explainPlan(t, db, ledger.AccountEntriesSQL, args...)
		if keys := p.keys(t, acct); keys != "ledger_entries_by_debit,ledger_entries_by_credit" {
			t.Errorf("%s: keys %s:\n%s", acct, keys, p.raw)
		}
		if n := rowsRead(t, db, ledger.AccountEntriesSQL, args...); n > planPageBudget {
			t.Errorf("%s: a page read %d rows, budget %d", acct, n, planPageBudget)
		}
	}
}

// What runs on every tick and on every ticket request. The texts of the first
// few are copies of those in engine.go and store.go.
func TestPlansHotQueries(t *testing.T) {
	db := planFixture(t)
	old := planEpoch + planRows/100 // a percent of the jobs is past any cutoff
	for _, c := range []struct {
		name, query string
		args        []any
		key         string
		covering    bool
	}{
		{"unused tickets", `SELECT buyer, node, created_at, dispute_reason != '' FROM jobs
			WHERE buyer_acct = ? AND status = 'held' AND started_at = 0 AND created_at < ?`, []any{planBig, old}, "jobs_buyer_unstarted", false},
		{"idle tickets", `SELECT node, COUNT(*) FROM jobs WHERE buyer_acct = ? AND status = 'held' AND started_at = 0 GROUP BY node`,
			[]any{planBig}, "jobs_buyer_unstarted", false},
		{"busy nodes", `SELECT node, COUNT(*) FROM jobs WHERE status = 'held'
			AND (started_at != 0 OR ticket_expires = 0 OR ticket_expires > ?) GROUP BY node`, []any{old}, "jobs_status_node", false},
		{"held past release", `SELECT job_id FROM jobs WHERE status = 'held' AND release_at <= ?`, []any{old}, "jobs_status_release", true},
		{"unstarted past grace", `SELECT job_id FROM jobs WHERE status = 'held' AND started_at = 0
			AND ticket_expires > 0 AND ticket_expires <= ?`, []any{old}, "jobs_unstarted", true},
		{"pending past dispute window", `SELECT job_id FROM jobs WHERE status = 'pending' AND finalize_at <= ?`, []any{old}, "jobs_status_finalize", true},
		{"purge", `DELETE FROM jobs WHERE status IN ('final', 'released', 'refunded') AND done_at < ?`, []any{old}, "jobs_status_done", false},
		{"bought usage", accountUsageSQL("buyer_acct", false), []any{planBig}, "jobs_buy_usage", true},
		{"sold usage", accountUsageSQL("node_acct", true), []any{planBig}, "jobs_sell_usage", true},
	} {
		p := explainPlan(t, db, c.query, c.args...)
		if keys := p.keys(t, c.name); keys != c.key {
			t.Errorf("%s: keys %s, want %s:\n%s", c.name, keys, c.key, p.raw)
		} else if c.covering && !p.tables[0].Covering {
			t.Errorf("%s: reads the rows, the index should cover it:\n%s", c.name, p.raw)
		}
	}
}

// A page deep into an account's jobs reads what precedes it on each side and no
// more; a date range keeps to the same indexes.
func TestPlansPagedAccountJobs(t *testing.T) {
	db := planFixture(t)
	const both = "jobs_by_buyer_acct,jobs_by_node_acct"
	for _, acct := range []string{planBig, planOld, planSmall} {
		query, args, err := accountJobsSQL(acct, JobQuery{Limit: planPage})
		if err != nil {
			t.Fatal(err)
		}
		if p := explainPlan(t, db, query, args...); p.keys(t, acct) != both {
			t.Errorf("%s: keys %s:\n%s", acct, p.keys(t, acct), p.raw)
		}
		if n := rowsRead(t, db, query, args...); n > planPageBudget {
			t.Errorf("%s: a page read %d rows, budget %d", acct, n, planPageBudget)
		}
		query, args, err = accountJobsSQL(acct, JobQuery{Limit: planPage, Offset: 5 * planPage, From: planEpoch, To: planEpoch + planRows})
		if err != nil {
			t.Fatal(err)
		}
		if p := explainPlan(t, db, query, args...); p.keys(t, acct) != both {
			t.Errorf("%s, deep: keys %s:\n%s", acct, p.keys(t, acct), p.raw)
		}
		if n := rowsRead(t, db, query, args...); n > 6*planPageBudget {
			t.Errorf("%s, deep: a page read %d rows, budget %d", acct, n, 6*planPageBudget)
		}
		// Counting reads the account's index entries, never the table's other rows.
		var total int64
		if total, err = CountAccountJobs(db, acct, JobQuery{}); err != nil || total == 0 {
			t.Errorf("%s: count %d %v", acct, total, err)
		}
		for _, role := range []string{RoleBuy, RoleSell} {
			if q, a, err := accountJobsSQL(acct, JobQuery{Role: role, Limit: planPage}); err != nil {
				t.Error(err)
			} else if n := rowsRead(t, db, q, a...); n > planPageBudget {
				t.Errorf("%s as %s: a page read %d rows, budget %d", acct, role, n, planPageBudget)
			}
		}
	}
}

// Turning a date range into seq bounds costs a look at one index entry per bound,
// not a walk over the journal.
func TestPlansJournalDateRange(t *testing.T) {
	db := planFixture(t)
	const first = `SELECT seq FROM ledger_entries FORCE INDEX (ledger_entries_by_at) WHERE at >= ? ORDER BY at, seq LIMIT 1`
	if n := rowsRead(t, db, first, int64(0)); n > 50 { // the counters also tick for the statement's own bookkeeping

		t.Errorf("finding the first entry of a date read %d rows", n)
	}
	lo, hi, err := ledger.SeqRange(db, 1, 1<<62)
	if err != nil || lo <= 0 || lo > hi {
		t.Fatalf("seq range %d %d %v", lo, hi, err)
	}
	if n := rowsRead(t, db, `SELECT seq, at FROM ledger_entries WHERE seq >= ? AND seq < ? ORDER BY seq DESC LIMIT ? OFFSET ?`, lo, hi, planPage, planPage); n > 2*planPageBudget {
		t.Errorf("a page of the whole journal read %d rows", n)
	}
}
