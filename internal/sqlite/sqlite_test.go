package sqlite

import (
	"database/sql"
	"os"
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

// On MySQL, write transactions of different processes (here: two pools) are
// serialized by Begin, so a read-modify-write never loses an update.
func TestMySQLWritersAreSerialized(t *testing.T) {
	if os.Getenv("SHARETOKEN_TEST_MYSQL") == "" {
		t.Skip("set SHARETOKEN_TEST_MYSQL=mysql://user@host/ to run")
	}
	a, err := Open(Memory)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := Migrate(a, `CREATE TABLE counter (id INT PRIMARY KEY, n BIGINT NOT NULL); INSERT INTO counter VALUES (1, 0)`); err != nil {
		t.Fatal(err)
	}
	var dbName string
	if err := a.QueryRow(`SELECT DATABASE()`).Scan(&dbName); err != nil {
		t.Fatal(err)
	}
	cfg, err := MySQLConfig(os.Getenv("SHARETOKEN_TEST_MYSQL"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.DBName = dbName
	b, err := openMySQLConfig(cfg)
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

func TestMySQLColumnDef(t *testing.T) {
	for in, want := range map[string]string{
		"INTEGER NOT NULL DEFAULT 0": "BIGINT NOT NULL DEFAULT 0",
		"TEXT NOT NULL DEFAULT ''":   "VARCHAR(255) NOT NULL DEFAULT ''",
		"BLOB NOT NULL DEFAULT x''":  "VARBINARY(255) NOT NULL DEFAULT ''",
		"REAL NOT NULL DEFAULT 0":    "DOUBLE NOT NULL DEFAULT 0",
	} {
		if got := mysqlColumnDef(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}

func TestTestDBStale(t *testing.T) {
	for name, want := range map[string]bool{
		"sttest_1794100000_ab12cd": true,  // before
		"sttest_1794200000_ab12cd": false, // after
		"sttest_ab12cd34ef56":      true,  // older format, no time
		"sharetoken":               false,
		"sttest_x_ab":              true,
	} {
		if got := testDBStale(name, 1794150000); got != want {
			t.Errorf("%s: got %v, want %v", name, got, want)
		}
	}
}
