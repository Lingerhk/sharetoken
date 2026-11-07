package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestIsBusy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	holder, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	tx, err := holder.Begin() // BEGIN IMMEDIATE: holds the write lock
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	// A second process with a short busy timeout.
	other, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(50)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	_, err = other.Exec(`CREATE TABLE IF NOT EXISTS x (a INTEGER)`)
	if err == nil || !IsBusy(err) {
		t.Fatalf("want SQLITE_BUSY, got %v", err)
	}
	if IsBusy(nil) || IsBusy(sql.ErrNoRows) {
		t.Fatal("IsBusy must be false for other errors")
	}
}
