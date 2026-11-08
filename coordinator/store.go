package coordinator

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"sharetoken/accounts"
	"sharetoken/internal/sqlite"
	"sharetoken/ledger"
	"sharetoken/protocol"
	"sharetoken/receipt"
)

// Migrate creates or upgrades every table of the shared database: ledger,
// accounts and coordinator state. The portal and the coordinator both call it,
// so either may start first.
func Migrate(db *sql.DB) error {
	mysql := sqlite.IsMySQL(db)
	schemas := []string{ledger.Schema, accounts.Schema, Schema}
	if mysql {
		schemas = []string{ledger.SchemaMySQL, accounts.SchemaMySQL, SchemaMySQL}
	}
	if err := sqlite.Migrate(db, schemas...); err != nil {
		return err
	}
	// v1 databases predate the account and token columns of jobs.
	for _, col := range []struct{ name, def string }{
		{"buyer_acct", "TEXT NOT NULL DEFAULT ''"},
		{"node_acct", "TEXT NOT NULL DEFAULT ''"},
		{"prompt_tokens", "INTEGER NOT NULL DEFAULT 0"},
		{"completion_tokens", "INTEGER NOT NULL DEFAULT 0"},
		{"started_at", "INTEGER NOT NULL DEFAULT 0"},
		{"ticket_expires", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := sqlite.AddColumn(db, "jobs", col.name, col.def); err != nil {
			return fmt.Errorf("coordinator: migrate jobs.%s: %w", col.name, err)
		}
	}
	// Columns added after the first v2 databases were created.
	for _, c := range []struct{ table, name, def string }{
		{"relays", "rtt_ms", "INTEGER NOT NULL DEFAULT 0"}, // the coordinator's measured round trip
		{"users", "mfa_secret", "BLOB NOT NULL DEFAULT x''"},
		{"users", "mfa_enrolled_at", "INTEGER NOT NULL DEFAULT 0"},
		{"users", "mfa_last_step", "INTEGER NOT NULL DEFAULT 0"},
		{"api_tokens", "mfa", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := sqlite.AddColumn(db, c.table, c.name, c.def); err != nil {
			return fmt.Errorf("coordinator: migrate %s.%s: %w", c.table, c.name, err)
		}
	}
	if mysql {
		return nil // SchemaMySQL declares the indexes inside CREATE TABLE
	}
	return sqlite.Migrate(db, jobIndexes)
}

const jobIndexes = `
CREATE INDEX IF NOT EXISTS jobs_by_buyer_acct ON jobs(buyer_acct, created_at);
CREATE INDEX IF NOT EXISTS jobs_by_node_acct  ON jobs(node_acct, created_at);
CREATE INDEX IF NOT EXISTS jobs_unstarted     ON jobs(status, started_at, ticket_expires);
-- IssueTicket counts a buyer's unused tickets on every request: without this
-- the query walks the account's whole job history under the write lock.
CREATE INDEX IF NOT EXISTS jobs_buyer_unstarted ON jobs(buyer_acct, status, started_at, created_at);
-- Covering indexes for the portal's per-account usage totals: without them the
-- totals read every (multi-kilobyte) job row of the account.
CREATE INDEX IF NOT EXISTS jobs_buy_usage  ON jobs(buyer_acct, status, prompt_tokens, completion_tokens, cost);
CREATE INDEX IF NOT EXISTS jobs_sell_usage ON jobs(node_acct, status, prompt_tokens, completion_tokens, cost, fee);
-- The history purge (DELETE … WHERE status IN (…) AND done_at < ?) must seek,
-- not read every finished job: it runs under the write lock.
CREATE INDEX IF NOT EXISTS jobs_status_done ON jobs(status, done_at);
-- The admin console lists all jobs (optionally one status) newest first:
-- without these the listing sorts the whole table on every page.
CREATE INDEX IF NOT EXISTS jobs_by_created        ON jobs(created_at);
CREATE INDEX IF NOT EXISTS jobs_by_status_created ON jobs(status, created_at);
-- Judged disputes are rare; the admin console lists them newest first.
CREATE INDEX IF NOT EXISTS jobs_judged ON jobs(created_at) WHERE dispute_outcome != '';
`

// Schema holds the coordinator's durable state next to the ledger tables.
// Offers are deliberately not stored: nodes re-announce them every few seconds.
const Schema = `
CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
) STRICT;
CREATE TABLE IF NOT EXISTS jobs (
	job_id          TEXT PRIMARY KEY,
	buyer           TEXT NOT NULL,
	node            TEXT NOT NULL,
	buyer_acct      TEXT NOT NULL DEFAULT '',
	node_acct       TEXT NOT NULL DEFAULT '',
	model           TEXT NOT NULL,
	status          TEXT NOT NULL,
	ticket          BLOB NOT NULL,
	receipt         BLOB,
	cost            INTEGER NOT NULL DEFAULT 0,
	fee             INTEGER NOT NULL DEFAULT 0,
	dispute_reason  TEXT NOT NULL DEFAULT '',
	dispute_outcome TEXT NOT NULL DEFAULT '',
	dispute_detail  TEXT NOT NULL DEFAULT '',
	created_at      INTEGER NOT NULL,
	release_at      INTEGER NOT NULL,
	finalize_at     INTEGER NOT NULL DEFAULT 0,
	done_at         INTEGER NOT NULL DEFAULT 0,
	prompt_tokens     INTEGER NOT NULL DEFAULT 0,
	completion_tokens INTEGER NOT NULL DEFAULT 0,
	started_at        INTEGER NOT NULL DEFAULT 0,
	ticket_expires    INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX IF NOT EXISTS jobs_status_release  ON jobs(status, release_at);
CREATE INDEX IF NOT EXISTS jobs_status_finalize ON jobs(status, finalize_at);
CREATE INDEX IF NOT EXISTS jobs_status_node     ON jobs(status, node);
CREATE INDEX IF NOT EXISTS jobs_buyer_status    ON jobs(buyer, status);
CREATE TABLE IF NOT EXISTS nodes (
	node              TEXT PRIMARY KEY,
	reputation        INTEGER NOT NULL,
	successes         INTEGER NOT NULL DEFAULT 0,
	failures          INTEGER NOT NULL DEFAULT 0,
	disputes_lost     INTEGER NOT NULL DEFAULT 0,
	audits_passed     INTEGER NOT NULL DEFAULT 0,
	audits_failed     INTEGER NOT NULL DEFAULT 0,
	audit_fail_streak INTEGER NOT NULL DEFAULT 0,
	suspended_until   INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE TABLE IF NOT EXISTS peers (
	peer         TEXT PRIMARY KEY,
	dev_credited INTEGER NOT NULL DEFAULT 0,
	auditor      INTEGER NOT NULL DEFAULT 0,
	created_at   INTEGER NOT NULL
) STRICT;
CREATE TABLE IF NOT EXISTS audits (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	at         INTEGER NOT NULL,
	job_id     TEXT NOT NULL,
	node       TEXT NOT NULL,
	model      TEXT NOT NULL,
	verdict    TEXT NOT NULL,
	similarity REAL NOT NULL,
	detail     TEXT NOT NULL
) STRICT;
CREATE INDEX IF NOT EXISTS audits_by_node ON audits(node, at);
`

// SchemaMySQL is Schema plus jobIndexes for MySQL 8 (see
// internal/sqlite/mysql.go). The partial index jobs_judged becomes a plain
// (dispute_outcome, created_at) index.
const SchemaMySQL = `
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
	if j.buyerAcct == "" { // a job written by v1
		j.buyerAcct = ledger.Account(j.ticket.Buyer)
	}
	if j.nodeAcct == "" {
		j.nodeAcct = ledger.Account(j.ticket.Node)
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

// upsert runs update and, when it matched no row, insert: a portable upsert
// (SQLite and MySQL spell ON CONFLICT differently). It is exact because
// writers are serialized (sqlite.Begin).
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
