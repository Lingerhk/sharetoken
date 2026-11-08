package coordinator

import (
	"database/sql"
	"sync"
	"testing"

	"sharetoken/internal/sqlite"
)

// dropColumns turns a current database into one created before these columns
// existed, keeping the rows.
func dropColumns(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		`INSERT INTO relays (addr) VALUES ('/dns4/old.example/tcp/4001')`,
		`ALTER TABLE relays DROP COLUMN rtt_ms`,
		`ALTER TABLE users DROP COLUMN mfa_secret`,
		`ALTER TABLE api_tokens DROP COLUMN mfa`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

// An existing database gets columns added since it was created, on SQLite and
// on MySQL (SHARETOKEN_TEST_MYSQL), where CREATE TABLE IF NOT EXISTS alone
// leaves it as it was. Its rows stay, and migrating again changes nothing.
func TestMigrateAddsMissingColumns(t *testing.T) {
	db, err := sqlite.Open(sqlite.Memory)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dropColumns(t, db)
	for range 2 {
		if err := Migrate(db); err != nil {
			t.Fatal(err)
		}
	}
	var rtt int64
	if err := db.QueryRow(`SELECT rtt_ms FROM relays WHERE addr = '/dns4/old.example/tcp/4001'`).Scan(&rtt); err != nil {
		t.Fatal(err)
	}
	if rtt != 0 {
		t.Fatalf("rtt_ms = %d", rtt)
	}
	for _, s := range []string{`SELECT mfa_secret FROM users`, `SELECT mfa FROM api_tokens`} {
		rows, err := db.Query(s)
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		rows.Close()
	}
}

// The portal and the coordinator migrate on start and may start together: both
// can see a column missing, and the one that adds it second must not fail.
func TestMigrateConcurrentlyOnMySQL(t *testing.T) {
	db, err := sqlite.Open(sqlite.Memory)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if !sqlite.IsMySQL(db) {
		t.Skip("set SHARETOKEN_TEST_MYSQL=mysql://user@host/ to run")
	}
	dropColumns(t, db)
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Go(func() { errs[i] = Migrate(db) })
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE relays SET rtt_ms = 1`); err != nil {
		t.Fatal(err)
	}
}
