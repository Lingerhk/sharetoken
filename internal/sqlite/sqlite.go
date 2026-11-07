// Package sqlite opens the coordinator's SQLite database (pure Go, no cgo).
//
// One process writes at a time: the pool holds a single connection and every
// transaction starts with BEGIN IMMEDIATE, so a transaction never has to
// upgrade a read lock and cannot fail half-way with SQLITE_BUSY. Other
// processes (the coordinator's admin commands) may open the same file; they
// wait for the write lock instead of failing.
package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	sqlite3 "modernc.org/sqlite"
	sqlitelib "modernc.org/sqlite/lib"
)

// Memory is the path of a private in-memory database (tests, demos).
const Memory = ":memory:"

// Open opens or creates the database at path. File databases run in WAL mode
// with synchronous=FULL: a committed transaction survives power loss, which is
// the right trade for a ledger.
func Open(path string) (*sql.DB, error) {
	params := "_pragma=foreign_keys(1)&_pragma=busy_timeout(10000)&_txlock=immediate"
	dsn := "file::memory:?" + params
	if path != Memory && path != "" {
		if strings.ContainsAny(path, "?#") {
			return nil, fmt.Errorf("sqlite: unsupported character in database path %q", path)
		}
		dsn = "file:" + strings.ReplaceAll(path, "%", "%25") + "?" + params +
			"&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// A single long-lived connection: it serializes writers, and an in-memory
	// database lives exactly as long as its connection.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: open %s: %w", path, err)
	}
	return db, nil
}

// Migrate applies idempotent schema scripts in one transaction.
func Migrate(db *sql.DB, scripts ...string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, s := range scripts {
		if _, err := tx.Exec(s); err != nil {
			return fmt.Errorf("sqlite: migrate: %w", err)
		}
	}
	return tx.Commit()
}

// AddColumn adds a column to table unless it already exists (SQLite has no
// ADD COLUMN IF NOT EXISTS).
func AddColumn(db *sql.DB, table, column, def string) error {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	_, err = db.Exec(fmt.Sprintf(`ALTER TABLE %q ADD COLUMN %q %s`, table, column, def))
	return err
}

// IsBusy reports whether err is SQLITE_BUSY: another process held the write
// lock for the whole busy timeout. The database is fine; the caller may retry.
func IsBusy(err error) bool {
	var se *sqlite3.Error
	return errors.As(err, &se) && se.Code()&0xff == sqlitelib.SQLITE_BUSY
}
