package ledger

import (
	"database/sql"
	"errors"
	"math"
	"testing"
	"time"

	"sharetoken/internal/mysqldb"
)

func newDSN(t *testing.T) string {
	t.Helper()
	dsn, err := mysqldb.TestDSN()
	if err != nil {
		t.Fatal(err)
	}
	return dsn
}

func openDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := mysqldb.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := mysqldb.Migrate(db, "ledger", []mysqldb.Migration{{Version: 1, Name: "ledger", SQL: Schema}}); err != nil {
		t.Fatal(err)
	}
	return db
}

// update runs fn in one transaction and commits only if it succeeds.
func update(t *testing.T, db *sql.DB, fn func(l *Tx) error) error {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := fn(On(tx, time.Unix(1_800_000_000, 0))); err != nil {
		return err
	}
	return tx.Commit()
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func balance(t *testing.T, db *sql.DB, account string) (b int64) {
	t.Helper()
	must(t, update(t, db, func(l *Tx) (err error) { b, err = l.Balance(account); return }))
	return b
}

func checkBooks(t *testing.T, db *sql.DB) {
	t.Helper()
	must(t, update(t, db, func(l *Tx) error { return l.Reconcile() }))
}

func TestHoldCaptureSplitsCostFeeAndRefund(t *testing.T) {
	db := openDB(t, newDSN(t))
	buyer, node := Account("buyer"), Account("node")
	must(t, update(t, db, func(l *Tx) error {
		if err := l.Deposit(buyer, 1_000, "deposit"); err != nil {
			return err
		}
		return l.Hold("job1", buyer, 400)
	}))
	if got := balance(t, db, buyer); got != 600 {
		t.Fatalf("available after hold = %d, want 600", got)
	}
	var fee int64
	must(t, update(t, db, func(l *Tx) error {
		held, err := l.Held(buyer)
		if err != nil || held != 400 {
			t.Fatalf("held = %d (%v), want 400", held, err)
		}
		fee, err = l.Capture("job1", node, 250, 800) // 8% fee
		return err
	}))
	if fee != 20 {
		t.Fatalf("fee = %d, want 20", fee)
	}
	for acct, want := range map[string]int64{node: 230, PlatformFees: 20, buyer: 750} {
		if got := balance(t, db, acct); got != want {
			t.Errorf("%s = %d, want %d", acct, got, want)
		}
	}
	checkBooks(t, db)
}

func TestEarnThenSpend(t *testing.T) {
	// The mutual-aid loop: a peer earns by serving, then spends those credits.
	db := openDB(t, newDSN(t))
	alice, bob := Account("alice"), Account("bob")
	must(t, update(t, db, func(l *Tx) error {
		_ = l.Deposit(alice, 100, "deposit")
		_ = l.Hold("j1", alice, 100)
		_, err := l.Capture("j1", bob, 100, 0)
		return err
	}))
	if err := update(t, db, func(l *Tx) error { return l.Hold("j2", bob, 100) }); err != nil {
		t.Fatalf("credits earned by serving should be spendable: %v", err)
	}
	checkBooks(t, db)
}

func TestFailedOperationRollsBackTheWholeTransaction(t *testing.T) {
	db := openDB(t, newDSN(t))
	err := update(t, db, func(l *Tx) error {
		if err := l.Deposit(Account("b"), 10, "deposit"); err != nil {
			return err
		}
		return l.Hold("j", Account("b"), 11) // fails: only 10 available
	})
	if !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("want ErrInsufficientFunds, got %v", err)
	}
	if got := balance(t, db, Account("b")); got != 0 {
		t.Fatalf("the deposit in the failed transaction must not persist, balance %d", got)
	}
	checkBooks(t, db)
}

func TestCaptureCannotExceedHold(t *testing.T) {
	db := openDB(t, newDSN(t))
	err := update(t, db, func(l *Tx) error {
		_ = l.Deposit(Account("b"), 100, "deposit")
		_ = l.Hold("j", Account("b"), 50)
		_, err := l.Capture("j", Account("n"), 51, 0)
		return err
	})
	if !errors.Is(err, ErrExceedsHold) {
		t.Fatalf("want ErrExceedsHold, got %v", err)
	}
}

func TestHoldClosesOnlyOnce(t *testing.T) {
	db := openDB(t, newDSN(t))
	must(t, update(t, db, func(l *Tx) error {
		_ = l.Deposit(Account("b"), 100, "deposit")
		_ = l.Hold("j", Account("b"), 50)
		_, err := l.Capture("j", Account("n"), 10, 0)
		return err
	}))
	cases := map[string]func(l *Tx) error{
		"second capture":       func(l *Tx) error { _, err := l.Capture("j", Account("n"), 10, 0); return err },
		"release after close":  func(l *Tx) error { return l.Release("j", "release") },
		"reusing a job id":     func(l *Tx) error { return l.Hold("j", Account("b"), 1) },
		"release unknown hold": func(l *Tx) error { return l.Release("nope", "release") },
	}
	want := map[string]error{"second capture": ErrNoHold, "release after close": ErrNoHold, "reusing a job id": ErrHoldExists, "release unknown hold": ErrNoHold}
	for name, fn := range cases {
		if err := update(t, db, fn); !errors.Is(err, want[name]) {
			t.Errorf("%s: want %v, got %v", name, want[name], err)
		}
	}
	checkBooks(t, db)
}

func TestReleaseRefundsEverything(t *testing.T) {
	db := openDB(t, newDSN(t))
	must(t, update(t, db, func(l *Tx) error {
		_ = l.Deposit(Account("b"), 100, "deposit")
		_ = l.Hold("j", Account("b"), 70)
		return l.Release("j", "release")
	}))
	if got := balance(t, db, Account("b")); got != 100 {
		t.Fatalf("balance after release = %d, want 100", got)
	}
	checkBooks(t, db)
}

func TestFundMayOverdrawPlatformAndTransferMayNot(t *testing.T) {
	db := openDB(t, newDSN(t))
	must(t, update(t, db, func(l *Tx) error { return l.Fund(Account("auditor"), 500, "audit funding") }))
	if got := balance(t, db, PlatformFees); got != -500 {
		t.Fatalf("platform = %d, want -500 (operator subsidy)", got)
	}
	err := update(t, db, func(l *Tx) error { return l.Transfer(Account("auditor"), PlatformFees, 501, "sweep") })
	if !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("overdrawing a peer account: want ErrInsufficientFunds, got %v", err)
	}
	must(t, update(t, db, func(l *Tx) error { return l.Transfer(Account("auditor"), PlatformFees, 500, "sweep") }))
	checkBooks(t, db)
}

func TestFeeMathDoesNotOverflow(t *testing.T) {
	db := openDB(t, newDSN(t))
	big := int64(1) << 61
	var fee int64
	must(t, update(t, db, func(l *Tx) (err error) {
		_ = l.Deposit(Account("b"), big, "deposit")
		_ = l.Hold("j", Account("b"), big)
		fee, err = l.Capture("j", Account("n"), big, 10_000)
		return err
	}))
	if fee != big {
		t.Fatalf("100%% fee of %d = %d", big, fee)
	}
	if err := update(t, db, func(l *Tx) error { return l.Deposit(PlatformFees, math.MaxInt64-10, "deposit") }); !errors.Is(err, ErrOverflow) {
		t.Fatalf("want ErrOverflow, got %v", err)
	}
}

func TestBooksSurviveRestartAndReconcileCatchesTampering(t *testing.T) {
	path := newDSN(t)
	db := openDB(t, path)
	must(t, update(t, db, func(l *Tx) error {
		_ = l.Deposit(Account("b"), 1_000, "deposit")
		_ = l.Hold("j1", Account("b"), 300)
		_, err := l.Capture("j1", Account("n"), 120, 500)
		if err != nil {
			return err
		}
		return l.Hold("j2", Account("b"), 200) // still open across the restart
	}))
	_ = db.Close()

	db = openDB(t, path)
	checkBooks(t, db)
	if got := balance(t, db, Account("b")); got != 1_000-120-200 {
		t.Fatalf("buyer after restart = %d", got)
	}
	var journal []Entry
	must(t, update(t, db, func(l *Tx) (err error) { journal, err = l.Journal(0, 100); return }))
	// deposit, hold j1, payout, fee, refund unused, hold j2
	if len(journal) != 6 || journal[2].Memo != "payout" || journal[2].Amount != 114 || journal[3].Amount != 6 {
		t.Fatalf("unexpected journal after restart: %+v", journal)
	}

	// Editing a balance behind the ledger's back is detected.
	if _, err := db.Exec(`UPDATE ledger_accounts SET balance = balance + 1 WHERE account = ?`, Account("n")); err != nil {
		t.Fatal(err)
	}
	if err := update(t, db, func(l *Tx) error { return l.Reconcile() }); err == nil {
		t.Fatal("reconcile accepted a tampered balance")
	}
	if _, err := db.Exec(`UPDATE ledger_accounts SET balance = balance - 1 WHERE account = ?`, Account("n")); err != nil {
		t.Fatal(err)
	}
	checkBooks(t, db)

	// So are balances that no journal entry explains, even when they cancel out.
	if _, err := db.Exec(`INSERT INTO ledger_accounts (account, balance) VALUES ('ghost:a', 5), ('ghost:b', -5)`); err != nil {
		t.Fatal(err)
	}
	if err := update(t, db, func(l *Tx) error { return l.Reconcile() }); err == nil {
		t.Fatal("reconcile accepted balances without journal entries")
	}
}
