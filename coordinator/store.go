package coordinator

import (
	"database/sql"
	"encoding/json"
	"errors"

	"sharetoken/accounts"
	"sharetoken/internal/mysqldb"
	"sharetoken/ledger"
	"sharetoken/protocol"
	"sharetoken/receipt"
)

// Migrations is the schema of the shared database: ledger, accounts and
// coordinator state, as component "core".
var Migrations = []mysqldb.Migration{
	{Version: 1, Name: "initial schema", SQL: ledger.Schema + accounts.Schema + Schema},
	{Version: 2, Name: "date range listings", SQL: ledger.AtIndex + auditsAtIndex},
}

// Migrate brings the shared database up to date. The portal and the coordinator
// both call it, so either may start first.
func Migrate(db *sql.DB) error { return mysqldb.Migrate(db, "core", Migrations) }

// auditsAtIndex lets the audit listing walk a date range in time order.
const auditsAtIndex = `ALTER TABLE audits ADD KEY audits_by_at (at);`

// Schema holds the coordinator's durable state next to the ledger tables.
// Offers are deliberately not stored: nodes re-announce them every few seconds.
const Schema = `
CREATE TABLE IF NOT EXISTS meta (
	` + "`key`" + ` VARCHAR(191) PRIMARY KEY,
	value TEXT NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS jobs (
	job_id          VARCHAR(191) PRIMARY KEY,
	buyer           VARCHAR(191) NOT NULL,
	node            VARCHAR(191) NOT NULL,
	buyer_acct      VARCHAR(191) NOT NULL DEFAULT '',
	node_acct       VARCHAR(191) NOT NULL DEFAULT '',
	model           VARCHAR(255) NOT NULL,
	status          VARCHAR(32) NOT NULL,
	ticket          MEDIUMBLOB NOT NULL,
	receipt         MEDIUMBLOB,
	cost            BIGINT NOT NULL DEFAULT 0,
	fee             BIGINT NOT NULL DEFAULT 0,
	dispute_reason  TEXT NOT NULL DEFAULT (''),
	dispute_outcome VARCHAR(64) NOT NULL DEFAULT '',
	dispute_detail  TEXT NOT NULL DEFAULT (''),
	created_at      BIGINT NOT NULL,
	release_at      BIGINT NOT NULL,
	finalize_at     BIGINT NOT NULL DEFAULT 0,
	done_at         BIGINT NOT NULL DEFAULT 0,
	prompt_tokens     BIGINT NOT NULL DEFAULT 0,
	completion_tokens BIGINT NOT NULL DEFAULT 0,
	started_at        BIGINT NOT NULL DEFAULT 0,
	ticket_expires    BIGINT NOT NULL DEFAULT 0,
	KEY jobs_status_release (status, release_at),
	KEY jobs_status_finalize (status, finalize_at),
	KEY jobs_status_node (status, node),
	KEY jobs_buyer_status (buyer, status),
	KEY jobs_by_buyer_acct (buyer_acct, created_at),
	KEY jobs_by_node_acct (node_acct, created_at),
	KEY jobs_unstarted (status, started_at, ticket_expires),
	KEY jobs_buyer_unstarted (buyer_acct, status, started_at, created_at),
	KEY jobs_buy_usage (buyer_acct, status, prompt_tokens, completion_tokens, cost),
	KEY jobs_sell_usage (node_acct, status, prompt_tokens, completion_tokens, cost, fee),
	KEY jobs_status_done (status, done_at),
	KEY jobs_by_created (created_at),
	KEY jobs_by_status_created (status, created_at),
	KEY jobs_judged (dispute_outcome, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS nodes (
	node              VARCHAR(191) PRIMARY KEY,
	reputation        BIGINT NOT NULL,
	successes         BIGINT NOT NULL DEFAULT 0,
	failures          BIGINT NOT NULL DEFAULT 0,
	disputes_lost     BIGINT NOT NULL DEFAULT 0,
	audits_passed     BIGINT NOT NULL DEFAULT 0,
	audits_failed     BIGINT NOT NULL DEFAULT 0,
	audit_fail_streak BIGINT NOT NULL DEFAULT 0,
	suspended_until   BIGINT NOT NULL DEFAULT 0
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS peers (
	peer         VARCHAR(191) PRIMARY KEY,
	dev_credited BIGINT NOT NULL DEFAULT 0,
	auditor      BIGINT NOT NULL DEFAULT 0,
	created_at   BIGINT NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS audits (
	id         BIGINT PRIMARY KEY AUTO_INCREMENT,
	at         BIGINT NOT NULL,
	job_id     VARCHAR(191) NOT NULL,
	node       VARCHAR(191) NOT NULL,
	model      VARCHAR(255) NOT NULL,
	verdict    VARCHAR(32) NOT NULL,
	similarity DOUBLE NOT NULL,
	detail     TEXT NOT NULL,
	KEY audits_by_node (node, at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
`

// job is one row of the jobs table.
type job struct {
	ticket         protocol.Ticket
	buyerAcct      string // ledger account the buyer pays from
	nodeAcct       string // ledger account the node earns into
	startedAt      int64  // when the node reported the buyer connected; 0: not yet
	status         Status
	receipt        *receipt.SignedReceipt // the node's (or, after evidence, the judged) receipt
	cost           uint64
	fee            int64
	disputeReason  string // set while held: a pre-dispute applied at settlement
	disputeOutcome string // set once evidence was judged
	disputeDetail  string
	createdAt      int64
	releaseAt      int64
	finalizeAt     int64
	doneAt         int64
}

func loadJob(tx *sql.Tx, id string) (*job, error) {
	var (
		j            job
		ticket, rcpt []byte
		cost         int64
		status       string
	)
	err := tx.QueryRow(`SELECT status, ticket, receipt, cost, fee, dispute_reason, dispute_outcome, dispute_detail,
		created_at, release_at, finalize_at, done_at, buyer_acct, node_acct, started_at FROM jobs WHERE job_id = ?`, id).Scan(
		&status, &ticket, &rcpt, &cost, &j.fee, &j.disputeReason, &j.disputeOutcome, &j.disputeDetail,
		&j.createdAt, &j.releaseAt, &j.finalizeAt, &j.doneAt, &j.buyerAcct, &j.nodeAcct, &j.startedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	j.status, j.cost = Status(status), uint64(cost)
	if err := json.Unmarshal(ticket, &j.ticket); err != nil {
		return nil, err
	}
	if len(rcpt) > 0 {
		j.receipt = new(receipt.SignedReceipt)
		if err := json.Unmarshal(rcpt, j.receipt); err != nil {
			return nil, err
		}
	}
	return &j, nil
}

func insertJob(tx *sql.Tx, j *job) error {
	ticket, err := json.Marshal(&j.ticket)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO jobs (job_id, buyer, node, buyer_acct, node_acct, model, status, ticket, dispute_reason, created_at, release_at, ticket_expires)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.ticket.JobID, j.ticket.Buyer, j.ticket.Node, j.buyerAcct, j.nodeAcct, j.ticket.Model, string(j.status), ticket,
		j.disputeReason, j.createdAt, j.releaseAt, j.ticket.ExpiresAt)
	return err
}

// saveJob writes a job's mutable fields; the ticket never changes.
func saveJob(tx *sql.Tx, j *job) error {
	var (
		rcpt   []byte
		pt, ct uint64
	)
	if j.receipt != nil {
		var err error
		if rcpt, err = json.Marshal(j.receipt); err != nil {
			return err
		}
		pt, ct = j.receipt.Receipt.PromptTokens, j.receipt.Receipt.CompletionTokens
	}
	_, err := tx.Exec(`UPDATE jobs SET status = ?, receipt = ?, cost = ?, fee = ?, dispute_reason = ?, dispute_outcome = ?,
		dispute_detail = ?, finalize_at = ?, done_at = ?, prompt_tokens = ?, completion_tokens = ?, started_at = ? WHERE job_id = ?`,
		string(j.status), rcpt, int64(j.cost), j.fee, j.disputeReason, j.disputeOutcome, j.disputeDetail,
		j.finalizeAt, j.doneAt, int64(min(pt, 1<<62)), int64(min(ct, 1<<62)), j.startedAt, j.ticket.JobID)
	return err
}

// NodeStats is a node's track record, which drives matching.
type NodeStats struct {
	Node            string `json:"node"`
	Reputation      int64  `json:"reputation"`
	Successes       uint64 `json:"successes"`
	Failures        uint64 `json:"failures"`
	DisputesLost    uint64 `json:"disputes_lost"`
	AuditsPassed    uint64 `json:"audits_passed"`
	AuditsFailed    uint64 `json:"audits_failed"`
	AuditFailStreak uint64 `json:"audit_fail_streak"`
	SuspendedUntil  int64  `json:"suspended_until,omitempty"` // unix nanos
}

const statsColumns = `node, reputation, successes, failures, disputes_lost, audits_passed, audits_failed, audit_fail_streak, suspended_until`

func scanStats(row interface{ Scan(...any) error }) (*NodeStats, error) {
	var s NodeStats
	var succ, fail, lost, passed, failed, streak int64
	if err := row.Scan(&s.Node, &s.Reputation, &succ, &fail, &lost, &passed, &failed, &streak, &s.SuspendedUntil); err != nil {
		return nil, err
	}
	s.Successes, s.Failures, s.DisputesLost = uint64(succ), uint64(fail), uint64(lost)
	s.AuditsPassed, s.AuditsFailed, s.AuditFailStreak = uint64(passed), uint64(failed), uint64(streak)
	return &s, nil
}

func loadStats(tx *sql.Tx, node string) (*NodeStats, error) {
	s, err := scanStats(tx.QueryRow(`SELECT `+statsColumns+` FROM nodes WHERE node = ?`, node))
	if errors.Is(err, sql.ErrNoRows) {
		return &NodeStats{Node: node, Reputation: startReputation}, nil
	}
	return s, err
}

func saveStats(tx *sql.Tx, s *NodeStats) error {
	vals := []any{s.Reputation, int64(s.Successes), int64(s.Failures), int64(s.DisputesLost),
		int64(s.AuditsPassed), int64(s.AuditsFailed), int64(s.AuditFailStreak), s.SuspendedUntil}
	return upsert(tx, `UPDATE nodes SET reputation = ?, successes = ?, failures = ?, disputes_lost = ?,
		audits_passed = ?, audits_failed = ?, audit_fail_streak = ?, suspended_until = ? WHERE node = ?`, append(vals, s.Node),
		`INSERT INTO nodes (`+statsColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, append([]any{s.Node}, vals...))
}

// upsert runs update and, when it matched no row, insert. It is exact because
// writers are serialized (mysqldb.Begin).
func upsert(tx *sql.Tx, update string, uargs []any, insert string, iargs []any) error {
	res, err := tx.Exec(update, uargs...)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n > 0 {
		return err
	}
	_, err = tx.Exec(insert, iargs...)
	return err
}

// updateStats loads a node's stats, applies fn and saves them.
func updateStats(tx *sql.Tx, node string, fn func(s *NodeStats)) (*NodeStats, error) {
	s, err := loadStats(tx, node)
	if err != nil {
		return nil, err
	}
	fn(s)
	return s, saveStats(tx, s)
}

// busyNodes counts each node's jobs that are ticketed but not yet settled,
// except unstarted ones whose ticket expired before staleBefore (UnixNano):
// those are past their start grace and only kept, not released, because
// their node reconnected and a start report may still arrive; they must not
// keep its slots (a node that restarted lost them for good).
func busyNodes(tx *sql.Tx, staleBefore int64) (map[string]uint32, error) {
	rows, err := tx.Query(`SELECT node, COUNT(*) FROM jobs WHERE status = 'held'
		AND (started_at != 0 OR ticket_expires = 0 OR ticket_expires > ?) GROUP BY node`, staleBefore)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]uint32)
	for rows.Next() {
		var node string
		var n int64
		if err := rows.Scan(&node, &n); err != nil {
			return nil, err
		}
		out[node] = uint32(n)
	}
	return out, rows.Err()
}

func queryStrings(tx *sql.Tx, query string, args ...any) ([]string, error) {
	rows, err := tx.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
