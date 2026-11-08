package mysqldb

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// TestServerEnv names the test server: mysql://user@host:port/. scripts/test.sh
// starts one in Docker and sets it.
const TestServerEnv = "SHARETOKEN_TEST_MYSQL"

const testDBPrefix = "sttest_"

// TestDSN creates a private empty database on the test server and returns its
// DSN. Tests only close their handles, so each call sweeps databases of earlier
// runs (an hour is longer than any test run).
func TestDSN() (string, error) {
	server := os.Getenv(TestServerEnv)
	if server == "" {
		return "", errors.New("mysqldb: tests need a MySQL server: run scripts/test.sh, or set " + TestServerEnv + "=mysql://user@host:port/")
	}
	cfg, err := ParseDSN(server)
	if err != nil {
		return "", err
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	now := time.Now().Unix()
	name := fmt.Sprintf("%s%d_%s", testDBPrefix, now, hex.EncodeToString(b))
	cfg.DBName = ""
	adb, err := openConfig(cfg, Options{MaxOpenConns: 2, MaxIdleConns: 1})
	if err != nil {
		return "", err
	}
	defer adb.Close()
	if err := dropTestDatabases(adb, now-3600); err != nil {
		return "", err
	}
	if _, err := adb.Exec("CREATE DATABASE " + name + " CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"); err != nil {
		return "", err
	}
	u, err := url.Parse(server)
	if err != nil {
		return "", err
	}
	u.Path = "/" + name
	return u.String(), nil
}

// OpenTest opens a private empty database on the test server.
func OpenTest() (*sql.DB, error) {
	dsn, err := TestDSN()
	if err != nil {
		return nil, err
	}
	return Open(dsn)
}

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
// Unix time before.
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
