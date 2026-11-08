package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

// MySQL support.
//
// DSN: mysql://user[:password]@host[:port]/database[?param=value…]. Without a
// password in the URL the SHARETOKEN_DB_PASSWORD environment variable is used,
// which keeps it out of the process list. Extra query parameters are passed to
// the server as session variables (go-sql-driver/mysql semantics).
//
// Every session runs READ COMMITTED with CLIENT_FOUND_ROWS (RowsAffected
// counts matched rows, as SQLite does), utf8mb4 with binary collation (string
// comparison is exact, as in SQLite), and a 10 s lock wait (like SQLite's
// busy timeout). Write transactions take a database-wide lock (Begin), so the
// application's single-writer assumptions hold unchanged across processes.
//
// Schemas are separate MySQL scripts next to the SQLite ones (SchemaMySQL in
// ledger, accounts and coordinator); the DML is portable.

const mysqlScheme = "mysql://"

// IsMySQL reports whether db is a MySQL database.
func IsMySQL(db *sql.DB) bool {
	_, ok := db.Driver().(*mysql.MySQLDriver)
	return ok
}

// MySQLConfig parses a mysql:// DSN.
func MySQLConfig(dsn string) (*mysql.Config, error) {
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme != "mysql" || u.Host == "" {
		return nil, fmt.Errorf("sqlite: bad MySQL DSN (want mysql://user[:password]@host[:port]/database)")
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

// OpenMySQL opens a mysql:// DSN. The database must exist.
func OpenMySQL(dsn string) (*sql.DB, error) { return openMySQLWith(dsn, Options{}) }

func openMySQLWith(dsn string, o Options) (*sql.DB, error) {
	cfg, err := MySQLConfig(dsn)
	if err != nil {
		return nil, err
	}
	if cfg.DBName == "" {
		return nil, errors.New("sqlite: MySQL DSN names no database")
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
	conn, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, err
	}
	return openMySQLConnector(conn, cfg, o)
}

func openMySQLConfig(cfg *mysql.Config) (*sql.DB, error) {
	conn, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, err
	}
	return openMySQLConnector(conn, cfg, Options{})
}

func openMySQLConnector(conn driver.Connector, cfg *mysql.Config, o Options) (*sql.DB, error) {
	o.fill()
	db := sql.OpenDB(conn)
	db.SetMaxOpenConns(o.MaxOpenConns)
	db.SetMaxIdleConns(o.MaxIdleConns)
	db.SetConnMaxLifetime(o.ConnMaxLifetime)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sqlite: open MySQL %s/%s: %w", cfg.Addr, cfg.DBName, err)
	}
	return db, nil
}

// openTestMySQL creates a private database sttest_<random> on the server of
// dsn and opens it (SHARETOKEN_TEST_MYSQL). Remove leftovers with
// DROP DATABASE sttest_…; they are small.
func openTestMySQL(dsn string) (*sql.DB, error) {
	cfg, err := MySQLConfig(dsn)
	if err != nil {
		return nil, err
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	now := time.Now().Unix()
	name := fmt.Sprintf("%s%d_%s", testDBPrefix, now, hex.EncodeToString(b))
	admin := cfg.Clone()
	admin.DBName = ""
	adb, err := openMySQLConfig(admin)
	if err != nil {
		return nil, err
	}
	defer adb.Close()
	// Callers only Close the handle, so test databases are never dropped by
	// their test; sweep those of earlier runs instead (an hour is longer
	// than any test run).
	if err := dropTestDatabases(adb, now-3600); err != nil {
		return nil, err
	}
	if _, err := adb.Exec("CREATE DATABASE " + name + " CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"); err != nil {
		return nil, err
	}
	cfg.DBName = name
	return openMySQLConfig(cfg)
}

// lockWriter takes the database-wide write lock for the rest of tx.
func lockWriter(ctx context.Context, tx *sql.Tx) error {
	var one int
	if err := tx.QueryRowContext(ctx, `SELECT id FROM st_write_lock WHERE id = 1 FOR UPDATE`).Scan(&one); err != nil {
		return fmt.Errorf("sqlite: write lock: %w", err)
	}
	return nil
}

const writeLockSchema = `CREATE TABLE IF NOT EXISTS st_write_lock (id INT PRIMARY KEY) ENGINE=InnoDB`

var mysqlComment = regexp.MustCompile(`(?m)--.*$`)

// migrateMySQL runs each statement of the scripts. Index creation is made
// idempotent by ignoring "duplicate key name" (MySQL has no CREATE INDEX IF
// NOT EXISTS).
func migrateMySQL(db *sql.DB, scripts ...string) error {
	if _, err := db.Exec(writeLockSchema); err != nil {
		return fmt.Errorf("sqlite: migrate: %w", err)
	}
	// Insert the lock row once. Check with a plain (non-locking) read first:
	// INSERT IGNORE would wait for the row lock whenever a writer holds it,
	// and every admin command migrates.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM st_write_lock`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if _, err := db.Exec(`INSERT IGNORE INTO st_write_lock (id) VALUES (1)`); err != nil {
			return err
		}
	}
	for _, script := range scripts {
		for _, stmt := range strings.Split(mysqlComment.ReplaceAllString(script, ""), ";") {
			if stmt = strings.TrimSpace(stmt); stmt == "" {
				continue
			}
			if _, err := db.Exec(stmt); err != nil && !mysqlErrIs(err, 1061) {
				return fmt.Errorf("sqlite: migrate: %w\n%s", err, stmt)
			}
		}
	}
	return nil
}

func addColumnMySQL(db *sql.DB, table, column, def string) error {
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = ?`, table, column).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err := db.Exec(fmt.Sprintf("ALTER TABLE `%s` ADD COLUMN `%s` %s", table, column, mysqlColumnDef(def)))
	if mysqlErrIs(err, 1060) { // duplicate column: another process added it since the check
		return nil
	}
	return err
}

// mysqlColumnDef translates a SQLite column definition used with AddColumn:
// INTEGER → BIGINT, REAL → DOUBLE, TEXT → VARCHAR(255), BLOB → VARBINARY(255)
// (a hex-literal empty default becomes a plain empty string default).
func mysqlColumnDef(def string) string {
	typ, rest, _ := strings.Cut(strings.TrimSpace(def), " ")
	switch strings.ToUpper(typ) {
	case "INTEGER":
		typ = "BIGINT"
	case "REAL":
		typ = "DOUBLE"
	case "TEXT":
		typ = "VARCHAR(255)"
	case "BLOB":
		typ = "VARBINARY(255)"
		rest = strings.ReplaceAll(rest, "x''", "''")
	}
	return strings.TrimSpace(typ + " " + rest)
}

func mysqlErrIs(err error, code uint16) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && me.Number == code
}

// isMySQLBusy: lock wait timeout (1205) or deadlock (1213).
func isMySQLBusy(err error) bool { return mysqlErrIs(err, 1205) || mysqlErrIs(err, 1213) }

const testDBPrefix = "sttest_"

// dropTestDatabases drops the test databases created before the Unix time
// before, and those named in the older format without a time.
func dropTestDatabases(adb *sql.DB, before int64) error {
	rows, err := adb.Query(`SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME LIKE 'sttest!_%' ESCAPE '!'`)
	if err != nil {
		return err
	}
	var old []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		if testDBStale(name, before) {
			old = append(old, name)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, name := range old {
		if _, err := adb.Exec("DROP DATABASE IF EXISTS `" + name + "`"); err != nil {
			return err
		}
	}
	return nil
}

// testDBStale reports whether the test database name was created before the
// Unix time before. Names without a time (the older sttest_<hex>) are stale.
func testDBStale(name string, before int64) bool {
	rest, ok := strings.CutPrefix(name, testDBPrefix)
	if !ok {
		return false
	}
	ts, _, ok := strings.Cut(rest, "_")
	if !ok {
		return true
	}
	t, err := strconv.ParseInt(ts, 10, 64)
	return err != nil || t < before
}
