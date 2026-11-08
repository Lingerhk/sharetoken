package mysqldb

import (
	"database/sql"
	"errors"
	"sync"
	"testing"
)

func open(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dsn, err := TestDSN()
	if err != nil {
		t.Fatal(err)
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, dsn
}

func TestParseDSN(t *testing.T) {
	cfg, err := ParseDSN("mysql://u:p@db.example/st?wait_timeout=5")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.User != "u" || cfg.Passwd != "p" || cfg.Addr != "db.example:3306" || cfg.DBName != "st" || cfg.Params["wait_timeout"] != "5" || !cfg.ClientFoundRows {
		t.Fatalf("%+v", cfg)
	}
	if _, err := ParseDSN("st.db"); err == nil {
		t.Fatal("accepted a DSN that is not mysql://")
	}
}

var counterTable = []Migration{{Version: 1, Name: "counter", SQL: `
	-- one row; a comment; with a semicolon
	CREATE TABLE IF NOT EXISTS counter (id INT PRIMARY KEY, n BIGINT NOT NULL);
	INSERT IGNORE INTO counter VALUES (1, 0)`}}

// Migrations apply in order, once, and a later binary's new ones are picked up.
func TestMigrateApplies(t *testing.T) {
	db, _ := open(t)
	for range 2 {
		if err := Migrate(db, "test", counterTable); err != nil {
			t.Fatal(err)
		}
	}
	next := append(append([]Migration{}, counterTable...), Migration{Version: 2, Name: "add label", SQL: `ALTER TABLE counter ADD COLUMN label VARCHAR(16) NOT NULL DEFAULT 'x'`})
	if err := Migrate(db, "test", next); err != nil {
		t.Fatal(err)
	}
	var n int
	var label string
	if err := db.QueryRow(`SELECT n, label FROM counter WHERE id = 1`).Scan(&n, &label); err != nil || label != "x" {
		t.Fatalf("%d %q %v", n, label, err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE component = 'test'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("recorded %d, %v", count, err)
	}
	// Another component numbers its own migrations.
	if err := Migrate(db, "other", []Migration{{Version: 1, Name: "t", SQL: `CREATE TABLE other_t (id INT PRIMARY KEY)`}}); err != nil {
		t.Fatal(err)
	}
}

// A migration interrupted after some of its statements runs again from its start.
func TestMigrateResumes(t *testing.T) {
	db, _ := open(t)
	if err := Migrate(db, "test", counterTable); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`ALTER TABLE counter ADD COLUMN label VARCHAR(16) NOT NULL DEFAULT 'x'`); err != nil {
		t.Fatal(err)
	}
	ms := append(append([]Migration{}, counterTable...), Migration{Version: 2, Name: "two steps", SQL: `
		ALTER TABLE counter ADD COLUMN label VARCHAR(16) NOT NULL DEFAULT 'x';
		CREATE INDEX counter_label ON counter(label)`})
	if err := Migrate(db, "test", ms); err != nil {
		t.Fatal(err)
	}
}

// Processes starting together all succeed and apply each migration once.
func TestMigrateConcurrently(t *testing.T) {
	db, _ := open(t)
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Go(func() { errs[i] = Migrate(db, "test", counterTable) })
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

// Additive migrations leave older binaries running; a breaking one stops them.
func TestOlderBinaries(t *testing.T) {
	db, _ := open(t)
	old := counterTable
	additive := append(append([]Migration{}, old...), Migration{Version: 2, Name: "additive", SQL: `ALTER TABLE counter ADD COLUMN a INT NOT NULL DEFAULT 0`})
	if err := Migrate(db, "test", additive); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db, "test", old); err != nil {
		t.Fatalf("an additive migration stopped an older binary: %v", err)
	}
	breaking := append(append([]Migration{}, additive...), Migration{Version: 3, Name: "breaking", MinReader: 3, SQL: `ALTER TABLE counter DROP COLUMN a`})
	if err := Migrate(db, "test", breaking); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db, "test", old); !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("older binary after a breaking migration: %v", err)
	}
	if err := CheckSchema(db, "test", 3); err != nil {
		t.Fatal(err)
	}
}

// Write transactions of different processes (here: two pools) are serialized
// by Begin, so a read-modify-write never loses an update.
func TestWritersAreSerialized(t *testing.T) {
	a, dsn := open(t)
	if err := Migrate(a, "test", counterTable); err != nil {
		t.Fatal(err)
	}
	b, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	const workers, rounds = 8, 25
	errs := make(chan error, 2*workers)
	for i := 0; i < 2*workers; i++ {
		db := a
		if i%2 == 1 {
			db = b
		}
		go func() {
			for r := 0; r < rounds; r++ {
				tx, err := Begin(db)
				if err != nil {
					errs <- err
					return
				}
				var n int64
				if err := tx.QueryRow(`SELECT n FROM counter WHERE id = 1`).Scan(&n); err != nil {
					errs <- err
					return
				}
				if _, err := tx.Exec(`UPDATE counter SET n = ? WHERE id = 1`, n+1); err != nil {
					errs <- err
					return
				}
				if err := tx.Commit(); err != nil {
					errs <- err
					return
				}
			}
			errs <- nil
		}()
	}
	for i := 0; i < 2*workers; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	var n int64
	if err := a.QueryRow(`SELECT n FROM counter WHERE id = 1`).Scan(&n); err != nil || n != 2*workers*rounds {
		t.Fatalf("counter = %d, %v; want %d", n, err, 2*workers*rounds)
	}
}

func TestIsBusy(t *testing.T) {
	a, dsn := open(t)
	if err := Migrate(a, "test", counterTable); err != nil {
		t.Fatal(err)
	}
	b, err := OpenWith(dsn, Options{LockWaitTimeout: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	tx, err := Begin(a)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := Begin(b); !IsBusy(err) {
		t.Fatalf("want a lock wait timeout, got %v", err)
	}
	if IsBusy(nil) || IsBusy(sql.ErrNoRows) {
		t.Fatal("IsBusy must be false for other errors")
	}
}

func TestTestDBStale(t *testing.T) {
	for name, want := range map[string]bool{
		"sttest_1794100000_ab12cd": true,  // before
		"sttest_1794200000_ab12cd": false, // after
		"sharetoken":               false,
		"sttest_x_ab":              true,
	} {
		if got := testDBStale(name, 1794150000); got != want {
			t.Errorf("%s: got %v, want %v", name, got, want)
		}
	}
}
