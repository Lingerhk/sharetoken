// Package ledger is the coordinator's double-entry credit ledger, stored in
// SQLite.
//
// Every movement is a balanced transfer between two accounts and is journaled,
// so the sum of all balances is always zero: deposits come from the External
// account, which goes negative by exactly the credits in circulation. Jobs use
// holds:
//
//	Hold:    acct:buyer -> hold:job        (reservation when a ticket is issued)
//	Capture: hold:job   -> acct:node       (cost - fee)
//	         hold:job   -> platform:fees   (fee)
//	         hold:job   -> acct:buyer      (unused remainder)
//	Release: hold:job   -> acct:buyer      (expired or refunded job)
//
// A peer has ONE account whether it buys or sells, so credits earned by serving
// can be spent on buying: the "share now, borrow later" loop. Credits are a
// closed-loop unit here; cash deposit/withdrawal rails are out of scope.
//
// Ledger operations run inside a transaction the caller owns (Tx), so a money
// movement commits or rolls back together with the job state change that
// caused it. Balances are materialized for fast reads; Reconcile replays the
// journal and proves they still match it.
package ledger

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
)

// Well-known accounts.
const (
	External     = "external"
	PlatformFees = "platform:fees"
)

// Account is the spend/earn account of a peer that is not bound to a user:
// audit identities, and every peer when the coordinator runs with open
// accounts (tests, local demos).
func Account(peerID string) string { return "acct:" + peerID }

// UserAccount is a registered user's single spend/earn account. All of the
// user's devices buy from and sell into it.
func UserAccount(userID int64) string { return fmt.Sprintf("user:%d", userID) }

func holdAccount(jobID string) string { return "hold:" + jobID }

// Errors.
var (
	ErrInvalidAmount     = errors.New("ledger: amount must be positive")
	ErrInsufficientFunds = errors.New("ledger: insufficient funds")
	ErrHoldExists        = errors.New("ledger: hold already exists for job")
	ErrNoHold            = errors.New("ledger: no open hold for job")
	ErrExceedsHold       = errors.New("ledger: capture exceeds held amount")
	ErrOverflow          = errors.New("ledger: balance overflow")
)

// Schema creates the ledger tables. It is idempotent.
const Schema = `
CREATE TABLE IF NOT EXISTS ledger_accounts (
	account TEXT PRIMARY KEY,
	balance INTEGER NOT NULL
) STRICT;
CREATE TABLE IF NOT EXISTS ledger_holds (
	job_id TEXT PRIMARY KEY,
	owner  TEXT NOT NULL,
	amount INTEGER NOT NULL CHECK (amount > 0),
	open   INTEGER NOT NULL CHECK (open IN (0, 1))
) STRICT;
CREATE INDEX IF NOT EXISTS ledger_holds_open_by_owner ON ledger_holds(owner) WHERE open = 1;
CREATE TABLE IF NOT EXISTS ledger_entries (
	seq     INTEGER PRIMARY KEY AUTOINCREMENT,
	at      INTEGER NOT NULL,
	job_id  TEXT NOT NULL,
	debit   TEXT NOT NULL,
	credit  TEXT NOT NULL,
	amount  INTEGER NOT NULL CHECK (amount > 0),
	memo    TEXT NOT NULL
) STRICT;
CREATE INDEX IF NOT EXISTS ledger_entries_by_debit  ON ledger_entries(debit, seq);
CREATE INDEX IF NOT EXISTS ledger_entries_by_credit ON ledger_entries(credit, seq);
`

// SchemaMySQL is Schema for MySQL 8 (see internal/sqlite/mysql.go).
const SchemaMySQL = `
CREATE TABLE IF NOT EXISTS ledger_accounts (
	account VARCHAR(191) PRIMARY KEY,
	balance BIGINT NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS ledger_holds (
	job_id VARCHAR(191) PRIMARY KEY,
	owner  VARCHAR(191) NOT NULL,
	amount BIGINT NOT NULL CHECK (amount > 0),
	open   BIGINT NOT NULL CHECK (open IN (0, 1)),
	KEY ledger_holds_open_by_owner (owner, open)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS ledger_entries (
	seq     BIGINT PRIMARY KEY AUTO_INCREMENT,
	at      BIGINT NOT NULL,
	job_id  VARCHAR(191) NOT NULL,
	debit   VARCHAR(191) NOT NULL,
	credit  VARCHAR(191) NOT NULL,
	amount  BIGINT NOT NULL CHECK (amount > 0),
	memo    TEXT NOT NULL,
	KEY ledger_entries_by_debit (debit, seq),
	KEY ledger_entries_by_credit (credit, seq)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
`

// Entry is one journal line: Amount moved from Debit to Credit.
type Entry struct {
	Seq    int64  `json:"seq"`
	At     int64  `json:"at"` // unix nanos
	JobID  string `json:"job_id,omitempty"`
	Debit  string `json:"from"`
	Credit string `json:"to"`
	Amount int64  `json:"amount"`
	Memo   string `json:"memo"`
}

// Tx is the ledger seen through one SQL transaction.
type Tx struct {
	tx *sql.Tx
	at int64
}

// On binds the ledger to tx; now timestamps the journal entries it writes.
func On(tx *sql.Tx, now time.Time) *Tx { return &Tx{tx: tx, at: now.UnixNano()} }

// Balance is an account's available (unheld) balance.
func (l *Tx) Balance(account string) (int64, error) {
	var b int64
	err := l.tx.QueryRow(`SELECT balance FROM ledger_accounts WHERE account = ?`, account).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return b, err
}

// Held is the total currently on hold for an owner account.
func (l *Tx) Held(owner string) (int64, error) {
	var s sql.NullInt64
	err := l.tx.QueryRow(`SELECT SUM(amount) FROM ledger_holds WHERE owner = ? AND open = 1`, owner).Scan(&s)
	return s.Int64, err
}

// transfer moves amount (>= 0) between two accounts and journals it. Callers
// check business rules (sufficient funds); transfer only refuses overflow.
func (l *Tx) transfer(jobID, from, to string, amount int64, memo string) error {
	if amount < 0 {
		return ErrInvalidAmount
	}
	if amount == 0 {
		return nil
	}
	if from == to {
		return fmt.Errorf("ledger: transfer from %s to itself", from)
	}
	fb, err := l.Balance(from)
	if err != nil {
		return err
	}
	tb, err := l.Balance(to)
	if err != nil {
		return err
	}
	if fb < math.MinInt64+amount || tb > math.MaxInt64-amount {
		return ErrOverflow
	}
	if err := l.setBalance(from, fb-amount); err != nil {
		return err
	}
	if err := l.setBalance(to, tb+amount); err != nil {
		return err
	}
	_, err = l.tx.Exec(`INSERT INTO ledger_entries (at, job_id, debit, credit, amount, memo) VALUES (?, ?, ?, ?, ?, ?)`,
		l.at, jobID, from, to, amount, memo)
	return err
}

func (l *Tx) setBalance(account string, balance int64) error {
	// UPDATE then INSERT instead of an upsert (portable to MySQL); safe
	// because writers are serialized (sqlite.Begin).
	res, err := l.tx.Exec(`UPDATE ledger_accounts SET balance = ? WHERE account = ?`, balance, account)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n > 0 {
		return err
	}
	_, err = l.tx.Exec(`INSERT INTO ledger_accounts (account, balance) VALUES (?, ?)`, account, balance)
	return err
}

// Deposit credits account from External (operator top-ups, dev credit).
func (l *Tx) Deposit(account string, amount int64, memo string) error {
	if amount <= 0 {
		return ErrInvalidAmount
	}
	return l.transfer("", External, account, amount, memo)
}

// Transfer moves available funds between two accounts.
func (l *Tx) Transfer(from, to string, amount int64, memo string) error {
	if amount <= 0 {
		return ErrInvalidAmount
	}
	b, err := l.Balance(from)
	if err != nil {
		return err
	}
	if b < amount {
		return ErrInsufficientFunds
	}
	return l.transfer("", from, to, amount, memo)
}

// Fund pays an account out of PlatformFees, which may go negative: that is
// the operator subsidizing platform costs (audits) beyond the fees collected.
func (l *Tx) Fund(account string, amount int64, memo string) error {
	if amount <= 0 {
		return ErrInvalidAmount
	}
	return l.transfer("", PlatformFees, account, amount, memo)
}

type hold struct {
	owner  string
	amount int64
	open   bool
}

func (l *Tx) hold(jobID string) (*hold, error) {
	var h hold
	err := l.tx.QueryRow(`SELECT owner, amount, open FROM ledger_holds WHERE job_id = ?`, jobID).Scan(&h.owner, &h.amount, &h.open)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &h, err
}

// Hold reserves amount from owner for jobID.
func (l *Tx) Hold(jobID, owner string, amount int64) error {
	if amount <= 0 {
		return ErrInvalidAmount
	}
	h, err := l.hold(jobID)
	if err != nil {
		return err
	}
	if h != nil {
		return ErrHoldExists
	}
	b, err := l.Balance(owner)
	if err != nil {
		return err
	}
	if b < amount {
		return ErrInsufficientFunds
	}
	if err := l.transfer(jobID, owner, holdAccount(jobID), amount, "hold"); err != nil {
		return err
	}
	_, err = l.tx.Exec(`INSERT INTO ledger_holds (job_id, owner, amount, open) VALUES (?, ?, ?, 1)`, jobID, owner, amount)
	return err
}

// Capture closes jobID's hold: cost goes to payee minus a fee of feeBps basis
// points to PlatformFees, and the unused remainder returns to the owner.
func (l *Tx) Capture(jobID, payee string, cost, feeBps int64) (fee int64, err error) {
	if cost < 0 || feeBps < 0 || feeBps > 10_000 {
		return 0, ErrInvalidAmount
	}
	h, err := l.hold(jobID)
	if err != nil {
		return 0, err
	}
	if h == nil || !h.open {
		return 0, ErrNoHold
	}
	if cost > h.amount {
		return 0, ErrExceedsHold
	}
	// Split the product so cost*feeBps cannot overflow.
	fee = cost/10_000*feeBps + cost%10_000*feeBps/10_000
	src := holdAccount(jobID)
	for _, t := range []struct {
		to     string
		amount int64
		memo   string
	}{
		{payee, cost - fee, "payout"},
		{PlatformFees, fee, "fee"},
		{h.owner, h.amount - cost, "refund unused"},
	} {
		if err := l.transfer(jobID, src, t.to, t.amount, t.memo); err != nil {
			return 0, err
		}
	}
	return fee, l.closeHold(jobID)
}

// Release returns jobID's entire hold to its owner.
func (l *Tx) Release(jobID, memo string) error {
	h, err := l.hold(jobID)
	if err != nil {
		return err
	}
	if h == nil || !h.open {
		return ErrNoHold
	}
	if err := l.transfer(jobID, holdAccount(jobID), h.owner, h.amount, memo); err != nil {
		return err
	}
	return l.closeHold(jobID)
}

// closeHold marks the hold closed and drops its now-empty account row; the
// journal keeps the history.
func (l *Tx) closeHold(jobID string) error {
	if _, err := l.tx.Exec(`UPDATE ledger_holds SET open = 0 WHERE job_id = ?`, jobID); err != nil {
		return err
	}
	_, err := l.tx.Exec(`DELETE FROM ledger_accounts WHERE account = ? AND balance = 0`, holdAccount(jobID))
	return err
}

// Journal returns up to limit entries after seq, oldest first.
func (l *Tx) Journal(afterSeq int64, limit int) ([]Entry, error) {
	rows, err := l.tx.Query(`SELECT seq, at, job_id, debit, credit, amount, memo FROM ledger_entries
		WHERE seq > ? ORDER BY seq LIMIT ?`, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.Seq, &e.At, &e.JobID, &e.Debit, &e.Credit, &e.Amount, &e.Memo); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AccountEntries returns up to limit journal entries that move money into or
// out of account, or of its job holds (owner = account), newest first,
// starting below beforeSeq (0: from the newest).
func (l *Tx) AccountEntries(account string, beforeSeq int64, limit int) ([]Entry, error) {
	if beforeSeq <= 0 {
		beforeSeq = math.MaxInt64
	}
	// Two index-ordered scans of at most limit rows each, merged: an OR of
	// the two columns would sort the account's whole history. UNION ALL is
	// exact because transfer refuses debit = credit.
	const side = `SELECT seq, at, job_id, debit, credit, amount, memo FROM ledger_entries
		WHERE %s = ? AND seq < ? ORDER BY seq DESC LIMIT ?`
	rows, err := l.tx.Query(`SELECT * FROM (`+fmt.Sprintf(side, "debit")+`) AS d UNION ALL SELECT * FROM (`+fmt.Sprintf(side, "credit")+`) AS c
		ORDER BY seq DESC LIMIT ?`, account, beforeSeq, limit, account, beforeSeq, limit, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.Seq, &e.At, &e.JobID, &e.Debit, &e.Credit, &e.Amount, &e.Memo); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Sum is the sum of every balance. It is zero whenever the books balance.
func (l *Tx) Sum() (int64, error) {
	var s sql.NullInt64
	err := l.tx.QueryRow(`SELECT SUM(balance) FROM ledger_accounts`).Scan(&s)
	return s.Int64, err
}

// Reconcile replays the journal and checks that it reproduces every
// materialized balance, that the books sum to zero, and that each hold account
// holds exactly its open hold. Run it at startup: a mismatch means the
// database was edited outside the ledger.
func (l *Tx) Reconcile() error {
	// Grouping each column before the union, and two NOT EXISTS instead of an
	// OR of the two columns, let both SQLite and MySQL use the (debit, …) and
	// (credit, …) indexes; the OR form scanned the journal once per account
	// on MySQL (4.7 s at 400k entries).
	rows, err := l.tx.Query(`
		WITH flows AS (
			SELECT credit AS account, SUM(amount) AS delta FROM ledger_entries GROUP BY credit
			UNION ALL
			SELECT debit, -SUM(amount) FROM ledger_entries GROUP BY debit
		),
		replayed AS (SELECT account, SUM(delta) AS balance FROM flows GROUP BY account)
		SELECT r.account, r.balance, COALESCE(a.balance, 0) FROM replayed r
			LEFT JOIN ledger_accounts a ON a.account = r.account
			WHERE r.balance != COALESCE(a.balance, 0)
		UNION ALL
		SELECT a.account, 0, a.balance FROM ledger_accounts a
			WHERE a.balance != 0 AND NOT EXISTS (
				SELECT 1 FROM ledger_entries e WHERE e.debit = a.account) AND NOT EXISTS (
				SELECT 1 FROM ledger_entries e WHERE e.credit = a.account)
		LIMIT 1`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		var acct string
		var replayed, stored int64
		if err := rows.Scan(&acct, &replayed, &stored); err != nil {
			return err
		}
		return fmt.Errorf("ledger: %s holds %d but its journal replays to %d", acct, stored, replayed)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if sum, err := l.Sum(); err != nil {
		return err
	} else if sum != 0 {
		return fmt.Errorf("ledger: balances sum to %d, not 0", sum)
	}
	var job string
	var amount, balance int64
	err = l.tx.QueryRow(`SELECT h.job_id, h.amount * h.open, COALESCE(a.balance, 0) FROM ledger_holds h
		LEFT JOIN ledger_accounts a ON a.account = CONCAT('hold:', h.job_id)
		WHERE h.amount * h.open != COALESCE(a.balance, 0) LIMIT 1`).Scan(&job, &amount, &balance)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil
	case err != nil:
		return err
	default:
		return fmt.Errorf("ledger: hold for job %s should contain %d but contains %d", job, amount, balance)
	}
}
