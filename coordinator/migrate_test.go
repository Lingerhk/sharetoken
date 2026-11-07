package coordinator

import (
	"path/filepath"
	"testing"

	"sharetoken/internal/sqlite"
)

// A v1 database (jobs without account and token columns) upgrades in place,
// and its old jobs pay the peer accounts they were ticketed to.
func TestMigrateUpgradesAV1Database(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "v1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v1 := `CREATE TABLE jobs (
		job_id TEXT PRIMARY KEY, buyer TEXT NOT NULL, node TEXT NOT NULL, model TEXT NOT NULL, status TEXT NOT NULL,
		ticket BLOB NOT NULL, receipt BLOB, cost INTEGER NOT NULL DEFAULT 0, fee INTEGER NOT NULL DEFAULT 0,
		dispute_reason TEXT NOT NULL DEFAULT '', dispute_outcome TEXT NOT NULL DEFAULT '', dispute_detail TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL, release_at INTEGER NOT NULL, finalize_at INTEGER NOT NULL DEFAULT 0, done_at INTEGER NOT NULL DEFAULT 0
	) STRICT;
	INSERT INTO jobs (job_id, buyer, node, model, status, ticket, created_at, release_at)
		VALUES ('j1', 'B', 'N', 'm', 'held', CAST('{"job_id":"j1","buyer":"B","node":"N"}' AS BLOB), 1, 2);`
	if err := sqlite.Migrate(db, v1); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // idempotent
		if err := Migrate(db); err != nil {
			t.Fatal(err)
		}
	}
	tx, _ := db.Begin()
	defer tx.Rollback()
	j, err := loadJob(tx, "j1")
	if err != nil || j.buyerAcct != "acct:B" || j.nodeAcct != "acct:N" {
		t.Fatalf("migrated job: %+v %v", j, err)
	}
}
