package accounts

import (
	"errors"
	"strings"
	"testing"

	"sharetoken/internal/mysqldb"
	"sharetoken/ledger"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	db, err := mysqldb.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := mysqldb.Migrate(db, "accounts", []mysqldb.Migration{{Version: 1, Name: "accounts", SQL: ledger.Schema + Schema}}); err != nil {
		t.Fatal(err)
	}
	return &Store{DB: db}
}

func TestCheckEmail(t *testing.T) {
	for _, ok := range []string{"a@b.co", "alice.smith+tag@example.com", "a_b-c%d@sub.example.co.uk", "x@xn--fsqu00a.xn--0zwm56d", strings.Repeat("a", 170) + "@example.com"} {
		if err := CheckEmail(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
		if err := CheckUsername(ok); err != nil {
			t.Errorf("as an account name, %q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "a", "a@b", "@b.co", "a@", "a@@b.co", "a b@c.co", "a@b c.co", ".a@b.co", "a.@b.co", "a..b@c.co", "a@-b.co", "a@b-.co", "a@b..co", "A@b.co", "a@b.co\n", "<a@b.co>", "用户@example.com", "a@例子.com", strings.Repeat("a", 180) + "@example.com"} {
		err := CheckEmail(bad)
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%q accepted: %v", bad, err)
		}
		if strings.Contains(bad, "@") && CheckUsername(bad) == nil {
			t.Errorf("%q accepted as an account name", bad)
		}
	}
	// Plain usernames keep their rules.
	for _, ok := range []string{"alice", "a.b-c_d", "abc"} {
		if err := CheckUsername(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"ab", "Alice", "-abc", "a b c", strings.Repeat("a", 33)} {
		if CheckUsername(bad) == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestEmailAccounts(t *testing.T) {
	s := testStore(t)
	u, err := s.CreateUser(" Alice@Example.COM ", "correct horse", false)
	if err != nil {
		t.Fatal(err)
	}
	if u.Username != "alice@example.com" {
		t.Fatalf("stored as %q", u.Username)
	}
	if _, err := s.CreateUser("ALICE@example.com", "correct horse", false); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate: %v", err)
	}
	if got, err := s.Authenticate("Alice@example.com ", "correct horse"); err != nil || got.ID != u.ID {
		t.Fatalf("authenticate: %+v %v", got, err)
	}
	if _, err := s.Authenticate("alice@example.com", "wrong"); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("wrong password: %v", err)
	}
}
