package coordinator

import (
	"database/sql"
	"encoding/json"
	"errors"

	"sharetoken/protocol"
	"sharetoken/receipt"
)

// schema holds the coordinator's durable state next to the ledger tables.
// Offers are deliberately not stored: nodes re-gossip them every few seconds.
const schema = `
CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
) STRICT;
CREATE TABLE IF NOT EXISTS jobs (
	job_id          TEXT PRIMARY KEY,
	buyer           TEXT NOT NULL,
	node            TEXT NOT NULL,
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
	done_at         INTEGER NOT NULL DEFAULT 0
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

// job is one row of the jobs table.
type job struct {
	ticket         protocol.Ticket
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
		created_at, release_at, finalize_at, done_at FROM jobs WHERE job_id = ?`, id).Scan(
		&status, &ticket, &rcpt, &cost, &j.fee, &j.disputeReason, &j.disputeOutcome, &j.disputeDetail,
		&j.createdAt, &j.releaseAt, &j.finalizeAt, &j.doneAt)
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
	_, err = tx.Exec(`INSERT INTO jobs (job_id, buyer, node, model, status, ticket, dispute_reason, created_at, release_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.ticket.JobID, j.ticket.Buyer, j.ticket.Node, j.ticket.Model, string(j.status), ticket, j.disputeReason, j.createdAt, j.releaseAt)
	return err
}

// saveJob writes a job's mutable fields; the ticket never changes.
func saveJob(tx *sql.Tx, j *job) error {
	var rcpt []byte
	if j.receipt != nil {
		var err error
		if rcpt, err = json.Marshal(j.receipt); err != nil {
			return err
		}
	}
	_, err := tx.Exec(`UPDATE jobs SET status = ?, receipt = ?, cost = ?, fee = ?, dispute_reason = ?, dispute_outcome = ?,
		dispute_detail = ?, finalize_at = ?, done_at = ? WHERE job_id = ?`,
		string(j.status), rcpt, int64(j.cost), j.fee, j.disputeReason, j.disputeOutcome, j.disputeDetail,
		j.finalizeAt, j.doneAt, j.ticket.JobID)
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
	_, err := tx.Exec(`INSERT INTO nodes (`+statsColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (node) DO UPDATE SET reputation = excluded.reputation, successes = excluded.successes,
		failures = excluded.failures, disputes_lost = excluded.disputes_lost, audits_passed = excluded.audits_passed,
		audits_failed = excluded.audits_failed, audit_fail_streak = excluded.audit_fail_streak,
		suspended_until = excluded.suspended_until`,
		s.Node, s.Reputation, int64(s.Successes), int64(s.Failures), int64(s.DisputesLost),
		int64(s.AuditsPassed), int64(s.AuditsFailed), int64(s.AuditFailStreak), s.SuspendedUntil)
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

// busyNodes counts each node's jobs that are ticketed but not yet settled.
func busyNodes(tx *sql.Tx) (map[string]uint32, error) {
	rows, err := tx.Query(`SELECT node, COUNT(*) FROM jobs WHERE status = 'held' GROUP BY node`)
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
