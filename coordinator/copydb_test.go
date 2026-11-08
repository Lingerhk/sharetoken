package coordinator

import (
	"path/filepath"
	"testing"

	"sharetoken/accounts"
	"sharetoken/internal/sqlite"
	"sharetoken/protocol"
)

// A SQLite database copies table by table into an empty one (MySQL when
// SHARETOKEN_TEST_MYSQL is set) with balances, jobs and users intact; a
// second copy into the now non-empty destination is refused.
func TestCopyDatabase(t *testing.T) {
	src, err := sqlite.Open(filepath.Join(t.TempDir(), "src.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	f := newFixture(t, Config{DB: src})
	node, buyer := mustKeys(t), mustKeys(t)
	f.offer(node, "m", 1000, 2000, 2)
	f.deposit(buyer, 5_000_000)
	tk := f.ticket(buyer, 1_000_000)
	sr := signReceipt(t, node, tk, 10, 20)
	if _, err := f.eng.Settle(node.ID(), protocol.SettleRequest{Ticket: *tk, Receipt: sr, Ack: ack(buyer, &sr)}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store().CreateUser("alice", "correct horse", false); err != nil {
		t.Fatal(err)
	}
	f.eng.Close()

	dst, err := sqlite.Open(sqlite.Memory)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	copied := map[string]int64{}
	if err := CopyDatabase(src, dst, func(tb string, n int64) { copied[tb] = n }); err != nil {
		t.Fatal(err)
	}
	if copied["jobs"] != 1 || copied["users"] != 1 || copied["ledger_entries"] == 0 {
		t.Fatalf("copied %v", copied)
	}
	for _, tb := range copyTables {
		var a, b int64
		_ = src.QueryRow(`SELECT COUNT(*) FROM ` + tb).Scan(&a)
		_ = dst.QueryRow(`SELECT COUNT(*) FROM ` + tb).Scan(&b)
		if a != b {
			t.Errorf("%s: %d rows in source, %d in copy", tb, a, b)
		}
	}
	eng, err := NewEngine(f.eng.self, nil, Config{DB: dst})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if err := eng.CheckBooks(); err != nil {
		t.Fatal(err)
	}
	st := &accounts.Store{DB: dst}
	if _, err := st.Authenticate("alice", "correct horse"); err != nil {
		t.Fatalf("login on the copy: %v", err)
	}
	// A new user on the copy gets a fresh id (AUTO_INCREMENT moved past the copied rows).
	if u, err := st.CreateUser("bob", "correct horse", false); err != nil || u.ID <= 1 {
		t.Fatalf("new user on the copy: %+v %v", u, err)
	}
	if err := CopyDatabase(src, dst, nil); err == nil {
		t.Fatal("copy into a non-empty database succeeded")
	}
}
