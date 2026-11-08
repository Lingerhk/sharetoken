// Package sqlite opens the shared database of the portal and the coordinator:
// SQLite (pure Go, no cgo) for a single machine, or MySQL 8 (a "mysql://" DSN)
// when they run on different machines. See mysql.go for the MySQL side.
//
// SQLite:
// One process writes at a time: the pool holds a single connection and every
// transaction starts with BEGIN IMMEDIATE, so a transaction never has to
// upgrade a read lock and cannot fail half-way with SQLITE_BUSY. Other
// processes (the coordinator's admin commands) may open the same file; they
// wait for the write lock instead of failing.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	sqlite3 "modernc.org/sqlite"
	sqlitelib "modernc.org/sqlite/lib"
)

// Memory is the path of a private in-memory database (tests, demos).
const Memory = ":memory:"

// Open opens or creates the database at path. File databases run in WAL mode
// with synchronous=FULL: a committed transaction survives power loss, which is
// the right trade for a ledger.
//
// A path starting with "mysql://" opens MySQL instead (see OpenMySQL). For
// tests, SHARETOKEN_TEST_MYSQL=mysql://… makes Open(Memory) return a fresh,
// private MySQL database, so the whole suite can run against MySQL.
func Open(path string) (*sql.DB, error) {
	if strings.HasPrefix(path, mysqlScheme) {
		return OpenMySQL(path)
	}
	if path == Memory {
		if dsn := os.Getenv("SHARETOKEN_TEST_MYSQL"); dsn != "" {
			return openTestMySQL(dsn)
		}
	}
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

// Migrate applies idempotent schema scripts in one transaction (on MySQL each
// DDL statement commits on its own; the scripts are idempotent).
func Migrate(db *sql.DB, scripts ...string) error {
	if IsMySQL(db) {
		return migrateMySQL(db, scripts...)
	}
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
// ADD COLUMN IF NOT EXISTS). def is written in SQLite terms; on MySQL its type
// is translated (see mysqlColumnDef).
func AddColumn(db *sql.DB, table, column, def string) error {
	if IsMySQL(db) {
		return addColumnMySQL(db, table, column, def)
	}
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
// On MySQL it reports a lock wait timeout or a deadlock.
func IsBusy(err error) bool {
	var se *sqlite3.Error
	return errors.As(err, &se) && se.Code()&0xff == sqlitelib.SQLITE_BUSY || isMySQLBusy(err)
}

// Begin starts a write transaction. On SQLite every transaction is already
// BEGIN IMMEDIATE (one writer at a time). On MySQL it also takes the database
// wide write lock (a row lock on st_write_lock), so writers of the portal and
// the coordinator are serialized exactly as on SQLite: the code reads a value
// and writes it back within a transaction (ledger balances, node statistics),
// which is only correct with a single writer.
func Begin(db *sql.DB) (*sql.Tx, error) { return BeginTx(context.Background(), db) }

// BeginTx is Begin with a context bounding the wait for the write lock.
func BeginTx(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil || !IsMySQL(db) {
		return tx, err
	}
	if err := lockWriter(ctx, tx); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}
