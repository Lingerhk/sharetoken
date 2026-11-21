// Package mysqldb opens the MySQL 8.4 database shared by the portal, the
// coordinator and the mail server, and applies its numbered schema migrations.
//
// DSN: mysql://user[:password]@host[:port]/database[?param=value…]. Without a
// password in the URL, Options.Password and then $SHARETOKEN_DB_PASSWORD are
// used, which keeps it out of the process list. Extra query parameters are
// passed to the server as session variables.
//
// Every session runs READ COMMITTED with CLIENT_FOUND_ROWS (RowsAffected counts
// matched rows), utf8mb4 with binary collation (string comparison is exact) and
// a 10 s lock wait. Write transactions take a database-wide lock (Begin), so
// the application's read-then-write code stays correct across processes.
package mysqldb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Options tunes the connection pool. Zero values take the defaults.
type Options struct {
	Password        string        // used when the DSN has none (before $SHARETOKEN_DB_PASSWORD)
	MaxOpenConns    int           // default 32
	MaxIdleConns    int           // default 8
	ConnMaxLifetime time.Duration // default 30m
	ConnectTimeout  time.Duration // default 10s
	LockWaitTimeout time.Duration // innodb_lock_wait_timeout, default 10s
}

func (o *Options) fill() {
	if o.MaxOpenConns <= 0 {
		o.MaxOpenConns = 32
	}
	if o.MaxIdleConns <= 0 {
		o.MaxIdleConns = 8
	}
	if o.ConnMaxLifetime <= 0 {
		o.ConnMaxLifetime = 30 * time.Minute
	}
}

// ParseDSN parses a mysql:// DSN.
func ParseDSN(dsn string) (*mysql.Config, error) {
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme != "mysql" || u.Host == "" {
		return nil, errors.New("mysqldb: bad DSN (want mysql://user[:password]@host[:port]/database)")
	}
	cfg := mysql.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = u.Host
	if u.Port() == "" {
		cfg.Addr = u.Host + ":3306"
	}
	cfg.User = u.User.Username()
	if pw, ok := u.User.Password(); ok {
		cfg.Passwd = pw
	} else {
		cfg.Passwd = os.Getenv("SHARETOKEN_DB_PASSWORD")
	}
	cfg.DBName = strings.TrimPrefix(u.Path, "/")
	cfg.Collation = "utf8mb4_bin"
	cfg.ClientFoundRows = true
	cfg.Timeout = 10 * time.Second
	cfg.Params = map[string]string{
		"transaction_isolation":    "'READ-COMMITTED'",
		"innodb_lock_wait_timeout": "10",
	}
	for k, v := range u.Query() {
		cfg.Params[k] = v[0]
	}
	return cfg, nil
}

// Open opens a mysql:// DSN. The database must exist.
func Open(dsn string) (*sql.DB, error) { return OpenWith(dsn, Options{}) }

// OpenWith is Open with connection options.
func OpenWith(dsn string, o Options) (*sql.DB, error) {
	cfg, err := ParseDSN(dsn)
	if err != nil {
		return nil, err
	}
	if cfg.DBName == "" {
		return nil, errors.New("mysqldb: DSN names no database")
	}
	u, _ := url.Parse(dsn)
	if _, inURL := u.User.Password(); !inURL && o.Password != "" {
		cfg.Passwd = o.Password
	}
	if o.ConnectTimeout > 0 {
		cfg.Timeout = o.ConnectTimeout
	}
	if o.LockWaitTimeout > 0 && u.Query().Get("innodb_lock_wait_timeout") == "" {
		cfg.Params["innodb_lock_wait_timeout"] = strconv.Itoa(max(1, int(o.LockWaitTimeout/time.Second)))
	}
	return openConfig(cfg, o)
}

func openConfig(cfg *mysql.Config, o Options) (*sql.DB, error) {
	conn, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, err
	}
	o.fill()
	db := sql.OpenDB(conn)
	db.SetMaxOpenConns(o.MaxOpenConns)
	db.SetMaxIdleConns(o.MaxIdleConns)
	db.SetConnMaxLifetime(o.ConnMaxLifetime)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("mysqldb: open %s/%s: %w", cfg.Addr, cfg.DBName, err)
	}
	return db, nil
}

// Begin starts a write transaction holding the database-wide write lock (a row
// lock on st_write_lock), which serializes the writers of the portal and the
// coordinator: the code reads a value and writes it back within a transaction
// (ledger balances, node statistics), which is only correct with a single writer.
func Begin(db *sql.DB) (*sql.Tx, error) { return BeginTx(context.Background(), db) }

// BeginTx is Begin with a context bounding the wait for the write lock.
func BeginTx(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	var one int
	if err := tx.QueryRowContext(ctx, `SELECT id FROM st_write_lock WHERE id = 1 FOR UPDATE`).Scan(&one); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("mysqldb: write lock: %w", err)
	}
	return tx, nil
}

// IsBusy reports a lock wait timeout or a deadlock: the database is fine and
// the caller may retry.
func IsBusy(err error) bool { return errIs(err, 1205) || errIs(err, 1213) }

func errIs(err error, code uint16) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == code
}
