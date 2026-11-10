package coordinator

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"sharetoken/internal/mysqldb"
	"sharetoken/ledger"
	"sharetoken/protocol"
)

// Querier is satisfied by *sql.DB and *sql.Tx.
type Querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// The portal reads job history straight from the shared database with these
// functions; they never take the engine's lock.

// JobInfo is one job as an account holder sees it.
type JobInfo struct {
	JobID            string `json:"job_id"`
	Role             string `json:"role"` // "buy" or "sell"
	Model            string `json:"model"`
	Status           Status `json:"status"`
	Buyer            string `json:"buyer"` // device peer IDs
	Node             string `json:"node"`
	PromptTokens     uint64 `json:"prompt_tokens"`
	CompletionTokens uint64 `json:"completion_tokens"`
	CostMicros       uint64 `json:"cost_micros"` // what the buyer pays (0 when refunded or released)
	FeeMicros        uint64 `json:"fee_micros"`  // platform fee out of the cost
	EarnedMicros     uint64 `json:"earned_micros,omitempty"`
	DisputeOutcome   string `json:"dispute_outcome,omitempty"`
	CreatedAt        int64  `json:"created_at"`
	DoneAt           int64  `json:"done_at,omitempty"`
}

// Job roles.
const (
	RoleBuy  = "buy"
	RoleSell = "sell"
)

// JobQuery narrows and pages a job listing, newest first. From and To bound
// created_at in unix nanos: From inclusive, To exclusive, 0 unbounded.
type JobQuery struct {
	Status string // "" for any (operator listings)
	Acct   string // "" for any; matches either party (operator listings)
	Role   string // account listings: "buy", "sell", or "" for both
	From   int64
	To     int64
	Offset int
	Limit  int
}

func (f JobQuery) to() int64 {
	if f.To <= 0 {
		return 1<<63 - 1
	}
	return f.To
}

const accountJobCols = `job_id, buyer_acct, model, status, buyer, node, prompt_tokens, completion_tokens, cost, fee,
	dispute_outcome, created_at, done_at`

// accountJobsSQL walks the index of each side in created_at order and stops
// after the page: an OR of the two columns would sort the account's whole
// history. Both sides read offset+limit rows at most.
func accountJobsSQL(acct string, f JobQuery) (string, []any, error) {
	side := func(col string, n int, tail string) (string, []any) {
		return `SELECT ` + accountJobCols + ` FROM jobs FORCE INDEX (jobs_by_` + col + `) WHERE ` + col + ` = ? AND created_at >= ? AND created_at < ?
			ORDER BY created_at DESC, job_id DESC ` + tail, []any{acct, f.From, f.to(), n}
	}
	switch f.Role {
	case RoleBuy, RoleSell:
		col := "buyer_acct"
		if f.Role == RoleSell {
			col = "node_acct"
		}
		q, args := side(col, f.Limit, `LIMIT ? OFFSET ?`)
		return q, append(args, f.Offset), nil
	case "":
		n := f.Offset + f.Limit
		buy, ba := side("buyer_acct", n, `LIMIT ?`)
		sell, sa := side("node_acct", n, `LIMIT ?`)
		return `SELECT * FROM (` + buy + `) AS b UNION ALL SELECT * FROM (` + sell + `) AS s ORDER BY created_at DESC, job_id DESC LIMIT ? OFFSET ?`,
			append(append(ba, sa...), f.Limit, f.Offset), nil
	}
	return "", nil, fmt.Errorf("unknown role %q", f.Role)
}

// CountAccountJobs counts what AccountJobs pages through.
func CountAccountJobs(db *sql.DB, acct string, f JobQuery) (int64, error) {
	var total int64
	for _, side := range []struct{ role, col string }{{RoleBuy, "buyer_acct"}, {RoleSell, "node_acct"}} {
		if f.Role != "" && f.Role != side.role {
			continue
		}
		var n int64
		err := db.QueryRow(`SELECT COUNT(*) FROM jobs FORCE INDEX (jobs_by_`+side.col+`) WHERE `+side.col+` = ? AND created_at >= ? AND created_at < ?`,
			acct, f.From, f.to()).Scan(&n)
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// AccountJobs lists an account's jobs, newest first. f.Role selects jobs it
// bought ("buy"), sold ("sell"), or both ("").
func AccountJobs(db *sql.DB, acct string, f JobQuery) ([]JobInfo, error) {
	query, args, err := accountJobsSQL(acct, f)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []JobInfo{}
	for rows.Next() {
		var (
			j             JobInfo
			buyerAcct     string
			pt, ct, c, fe int64
		)
		if err := rows.Scan(&j.JobID, &buyerAcct, &j.Model, &j.Status, &j.Buyer, &j.Node, &pt, &ct, &c, &fe,
			&j.DisputeOutcome, &j.CreatedAt, &j.DoneAt); err != nil {
			return nil, err
		}
		j.PromptTokens, j.CompletionTokens, j.CostMicros, j.FeeMicros = uint64(pt), uint64(ct), uint64(c), uint64(fe)
		if j.Status == StatusRefunded || j.Status == StatusReleased {
			j.CostMicros, j.FeeMicros = 0, 0 // nothing was charged
		}
		j.Role = RoleSell
		if buyerAcct == acct {
			j.Role = RoleBuy
		}
		if j.Role == RoleSell && j.Status == StatusFinal {
			j.EarnedMicros = j.CostMicros - j.FeeMicros
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// UsageSide totals one side of an account's trading.
type UsageSide struct {
	Jobs             uint64 `json:"jobs"`
	PromptTokens     uint64 `json:"prompt_tokens"`
	CompletionTokens uint64 `json:"completion_tokens"`
	Micros           uint64 `json:"micros"` // spent (buy) or earned net of fees (sell), final jobs only
	Pending          uint64 `json:"pending_jobs"`
}

// Usage totals an account's token trading over the retained jobs.
type Usage struct {
	Bought UsageSide `json:"bought"`
	Sold   UsageSide `json:"sold"`
}

// AccountUsage totals the tokens an account bought and sold. Jobs older than
// the coordinator's retention window (30 days by default) are not counted;
// the ledger keeps the money history forever.
func AccountUsage(db *sql.DB, acct string) (Usage, error) {
	var u Usage
	for _, side := range []struct {
		col  string
		out  *UsageSide
		earn bool
	}{{"buyer_acct", &u.Bought, false}, {"node_acct", &u.Sold, true}} {
		var jobs, pt, ct, money, pending sql.NullInt64
		err := db.QueryRow(accountUsageSQL(side.col, side.earn), acct).Scan(&jobs, &pt, &ct, &money, &pending)
		if err != nil {
			return u, err
		}
		*side.out = UsageSide{Jobs: uint64(jobs.Int64), PromptTokens: uint64(pt.Int64), CompletionTokens: uint64(ct.Int64),
			Micros: uint64(money.Int64), Pending: uint64(pending.Int64)}
	}
	return u, nil
}

// accountUsageSQL is answered from the index alone, which holds every column
// it reads; the optimizer would otherwise read each of the account's rows.
func accountUsageSQL(col string, earn bool) string {
	index := "jobs_buy_usage"
	if earn {
		index = "jobs_sell_usage"
	}
	return `SELECT
		SUM(CASE WHEN status = 'final' THEN 1 ELSE 0 END),
		SUM(CASE WHEN status = 'final' THEN prompt_tokens ELSE 0 END),
		SUM(CASE WHEN status = 'final' THEN completion_tokens ELSE 0 END),
		SUM(CASE WHEN status = 'final' THEN cost - ` + feeTerm(earn) + ` ELSE 0 END),
		SUM(CASE WHEN status IN ('held', 'pending', 'disputed') THEN 1 ELSE 0 END)
		FROM jobs FORCE INDEX (` + index + `) WHERE ` + col + ` = ?`
}

func feeTerm(earn bool) string {
	if earn {
		return "fee"
	}
	return "0"
}

// CoordinatorID returns the peer ID of the coordinator that owns the
// database, or "" before it first started.
func CoordinatorID(db *sql.DB) (string, error) {
	var id string
	err := db.QueryRow("SELECT value FROM meta WHERE `key` = 'coordinator'").Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return id, err
}

// ---- operator views, shared by the coordinator's commands and the portal ----

// AdminJob is one job as operators see it: both parties and their accounts.
type AdminJob struct {
	JobID            string `json:"job_id"`
	Model            string `json:"model"`
	Status           Status `json:"status"`
	Buyer            string `json:"buyer"`
	Node             string `json:"node"`
	BuyerAcct        string `json:"buyer_acct"`
	NodeAcct         string `json:"node_acct"`
	PromptTokens     uint64 `json:"prompt_tokens"`
	CompletionTokens uint64 `json:"completion_tokens"`
	CostMicros       uint64 `json:"cost_micros"`
	FeeMicros        uint64 `json:"fee_micros"`
	DisputeReason    string `json:"dispute_reason,omitempty"`
	DisputeOutcome   string `json:"dispute_outcome,omitempty"`
	DisputeDetail    string `json:"dispute_detail,omitempty"`
	CreatedAt        int64  `json:"created_at"`
	StartedAt        int64  `json:"started_at,omitempty"`
	DoneAt           int64  `json:"done_at,omitempty"`
}

// QueryJobs lists jobs newest first, narrowed by f.
func QueryJobs(q Querier, f JobQuery) ([]AdminJob, error) {
	query, args := queryJobsSQL(f)
	rows, err := q.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminJob{}
	for rows.Next() {
		var j AdminJob
		var pt, ct, c, fe int64
		if err := rows.Scan(&j.JobID, &j.Model, &j.Status, &j.Buyer, &j.Node, &j.BuyerAcct, &j.NodeAcct, &pt, &ct, &c, &fe,
			&j.DisputeReason, &j.DisputeOutcome, &j.DisputeDetail, &j.CreatedAt, &j.StartedAt, &j.DoneAt); err != nil {
			return nil, err
		}
		j.PromptTokens, j.CompletionTokens, j.CostMicros, j.FeeMicros = uint64(pt), uint64(ct), uint64(c), uint64(fe)
		if j.Status == StatusRefunded || j.Status == StatusReleased {
			j.CostMicros, j.FeeMicros = 0, 0
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// queryJobsSQL builds the admin listing. Every branch walks one index in
// created_at order and stops after the page; none of them may sort the
// table (TestQueryJobsUsesIndexes checks the plans).
func queryJobsSQL(f JobQuery) (string, []any) {
	const cols = `job_id, model, status, buyer, node, buyer_acct, node_acct, prompt_tokens, completion_tokens,
		cost, fee, dispute_reason, dispute_outcome, dispute_detail, created_at, started_at, done_at`
	var statusWhere string
	var statusArgs []any
	if f.Status != "" {
		statusWhere, statusArgs = ` AND status = ?`, []any{f.Status}
	}
	if f.Acct == "" {
		return `SELECT ` + cols + ` FROM jobs WHERE created_at >= ? AND created_at < ?` + statusWhere + ` ORDER BY created_at DESC, job_id DESC LIMIT ? OFFSET ?`,
			append(append([]any{f.From, f.to()}, statusArgs...), f.Limit, f.Offset)
	}
	// Two index-ordered scans of at most offset+limit rows each, merged: an OR
	// of the two account columns would sort the account's whole history. The
	// index is forced because the optimizer otherwise walks all recent jobs
	// looking for an account whose history is old.
	n := f.Offset + f.Limit
	side := func(col string) (string, []any) {
		return `SELECT ` + cols + ` FROM jobs FORCE INDEX (jobs_by_` + col + `) WHERE ` + col + ` = ? AND created_at >= ? AND created_at < ?` + statusWhere + ` ORDER BY created_at DESC, job_id DESC LIMIT ?`,
			append(append([]any{f.Acct, f.From, f.to()}, statusArgs...), n)
	}
	buy, buyArgs := side("buyer_acct")
	sell, sellArgs := side("node_acct")
	// UNION ALL is exact: IssueTicket never lets an account buy from itself,
	// so no job matches both sides.
	return `SELECT * FROM (` + buy + `) AS b UNION ALL SELECT * FROM (` + sell + `) AS s ORDER BY created_at DESC, job_id DESC LIMIT ? OFFSET ?`,
		append(append(buyArgs, sellArgs...), f.Limit, f.Offset)
}

// CountJobs counts what QueryJobs pages through.
func CountJobs(q Querier, f JobQuery) (int64, error) {
	var statusWhere string
	var statusArgs []any
	if f.Status != "" {
		statusWhere, statusArgs = ` AND status = ?`, []any{f.Status}
	}
	count := func(query string, args ...any) (int64, error) {
		var n int64
		err := q.QueryRow(query, args...).Scan(&n)
		return n, err
	}
	if f.Acct == "" {
		return count(`SELECT COUNT(*) FROM jobs WHERE created_at >= ? AND created_at < ?`+statusWhere, append([]any{f.From, f.to()}, statusArgs...)...)
	}
	var total int64
	for _, col := range []string{"buyer_acct", "node_acct"} {
		n, err := count(`SELECT COUNT(*) FROM jobs FORCE INDEX (jobs_by_`+col+`) WHERE `+col+` = ? AND created_at >= ? AND created_at < ?`+statusWhere,
			append([]any{f.Acct, f.From, f.to()}, statusArgs...)...)
		if err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// JobCounts counts jobs by status.
func JobCounts(q Querier) (map[Status]int64, error) {
	rows, err := q.Query(`SELECT status, COUNT(*) FROM jobs GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[Status]int64{}
	for rows.Next() {
		var st Status
		var n int64
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}

// QueryNodes lists every node with a record, best reputation first.
func QueryNodes(q Querier) ([]NodeStats, error) {
	rows, err := q.Query(`SELECT ` + statsColumns + ` FROM nodes ORDER BY reputation DESC, node`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NodeStats{}
	for rows.Next() {
		s, err := scanStats(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// RangeQuery pages a listing over a date range in unix nanos: From inclusive,
// To exclusive, 0 unbounded.
type RangeQuery struct {
	From, To      int64
	Offset, Limit int
}

func (f RangeQuery) to() int64 {
	if f.To <= 0 {
		return 1<<63 - 1
	}
	return f.To
}

// QueryAudits lists the most recent audit results.
func QueryAudits(q Querier, limit int) ([]AuditRecord, error) {
	return QueryAuditsPage(q, RangeQuery{Limit: limit})
}

// CountAudits counts what QueryAuditsPage pages through.
func CountAudits(q Querier, f RangeQuery) (int64, error) {
	var n int64
	err := q.QueryRow(`SELECT COUNT(*) FROM audits WHERE at >= ? AND at < ?`, f.From, f.to()).Scan(&n)
	return n, err
}

// QueryAuditsPage lists audit results over a date range, newest first.
func QueryAuditsPage(q Querier, f RangeQuery) ([]AuditRecord, error) {
	rows, err := q.Query(`SELECT job_id, node, model, verdict, similarity, detail, at FROM audits WHERE at >= ? AND at < ? ORDER BY at DESC, id DESC LIMIT ? OFFSET ?`,
		f.From, f.to(), f.Limit, f.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditRecord{}
	for rows.Next() {
		var r AuditRecord
		if err := rows.Scan(&r.JobID, &r.Node, &r.Model, &r.Verdict, &r.Similarity, &r.Detail, &r.At); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Open disputes come off the (status, created_at) index, judged ones off
// jobs_judged; an OR of the two would scan the table.
const disputesCols = `job_id, buyer, node, buyer_acct, node_acct, model, status, cost, dispute_reason, dispute_outcome, dispute_detail,
	created_at, done_at`
const disputesSQL = `SELECT ` + disputesCols + ` FROM (
		SELECT * FROM (SELECT ` + disputesCols + ` FROM jobs WHERE status = 'disputed' AND created_at >= ? AND created_at < ? ORDER BY created_at DESC LIMIT ?) AS o
		UNION SELECT * FROM (SELECT ` + disputesCols + ` FROM jobs WHERE dispute_outcome != '' AND created_at >= ? AND created_at < ? ORDER BY created_at DESC LIMIT ?) AS j) AS d
	ORDER BY status = 'disputed' DESC, created_at DESC LIMIT ? OFFSET ?`

func disputesArgs(f RangeQuery) []any {
	n := f.Offset + f.Limit
	return []any{f.From, f.to(), n, f.From, f.to(), n, f.Limit, f.Offset}
}

// QueryDisputes lists jobs awaiting arbitration first, then judged ones, newest first.
func QueryDisputes(q Querier, limit int) ([]DisputeInfo, error) {
	return QueryDisputesPage(q, RangeQuery{Limit: limit})
}

// CountDisputes counts what QueryDisputesPage pages through.
func CountDisputes(q Querier, f RangeQuery) (int64, error) {
	var n int64
	err := q.QueryRow(`SELECT COUNT(*) FROM (
		SELECT job_id FROM jobs WHERE status = 'disputed' AND created_at >= ? AND created_at < ?
		UNION SELECT job_id FROM jobs WHERE dispute_outcome != '' AND created_at >= ? AND created_at < ?) AS d`,
		f.From, f.to(), f.From, f.to()).Scan(&n)
	return n, err
}

// QueryDisputesPage is QueryDisputes over a date range, one page at a time.
func QueryDisputesPage(q Querier, f RangeQuery) ([]DisputeInfo, error) {
	rows, err := q.Query(disputesSQL, disputesArgs(f)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DisputeInfo{}
	for rows.Next() {
		var d DisputeInfo
		var cost int64
		if err := rows.Scan(&d.JobID, &d.Buyer, &d.Node, &d.BuyerAcct, &d.NodeAcct, &d.Model, &d.Status, &cost, &d.Reason, &d.Outcome, &d.Detail,
			&d.CreatedAt, &d.DoneAt); err != nil {
			return nil, err
		}
		d.Cost = uint64(cost)
		out = append(out, d)
	}
	return out, rows.Err()
}

// ErrNotDisputed is returned when a job awaiting no arbitration is resolved.
var ErrNotDisputed = errors.New("coordinator: job is not disputed")

// ResolveDispute settles a disputed job from another process (the portal):
// payNode captures the hold for the node, otherwise the buyer is refunded and
// the node charged a lost dispute. It uses the fee rate the coordinator
// recorded in the database and is atomic with the coordinator's own writes.
func ResolveDispute(db *sql.DB, jobID string, payNode bool, now time.Time) error {
	tx, err := mysqldb.Begin(db)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	j, err := loadJob(tx, jobID)
	if err != nil {
		return err
	}
	if j == nil {
		return protocol.Errorf(protocol.CodeNotFound, "unknown job")
	}
	if j.status != StatusDisputed {
		return fmt.Errorf("%w: job is %s", ErrNotDisputed, j.status)
	}
	led := ledger.On(tx, now)
	if payNode {
		var raw string
		if err := tx.QueryRow("SELECT value FROM meta WHERE `key` = 'fee_bps'").Scan(&raw); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		feeBps, _ := strconv.ParseInt(raw, 10, 64)
		if err := finalizeJob(tx, led, j, now, feeBps); err != nil {
			return err
		}
	} else if err := refundJob(tx, led, j, now); err != nil {
		return err
	}
	return tx.Commit()
}
