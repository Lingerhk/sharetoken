package accounts

import (
	"errors"
	"strings"
	"testing"

	"sharetoken/internal/ledger"
	"sharetoken/internal/mysqldb"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	db, err := mysqldb.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := mysqldb.Migrate(db, "accounts", []mysqldb.Migration{{Version: 1, Name: "accounts", SQL: ledger.Schema + Schema + PairingSchema + NoPasswords + SharingSchema + InviteLinkSchema + ShareInviteUsesSchema + InviteCooldownSchema}}); err != nil {
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
	u, err := s.CreateUser(" Alice@Example.COM ", false)
	if err != nil {
		t.Fatal(err)
	}
	if u.Username != "alice@example.com" {
		t.Fatalf("stored as %q", u.Username)
	}
	if _, err := s.CreateUser("ALICE@example.com", false); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate: %v", err)
	}
	if got, err := s.UserByName("Alice@example.com "); err != nil || got.ID != u.ID {
		t.Fatalf("lookup: %+v %v", got, err)
	}
}

// A rename keeps the account (its id) and takes only a free email address.
func TestRenameUser(t *testing.T) {
	s := testStore(t)
	root, err := s.CreateUser("root", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser("bob@example.com", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RenameUser(root.ID, "not-an-email"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("plain name: %v", err)
	}
	if _, err := s.RenameUser(root.ID, "BOB@example.com"); !errors.Is(err, ErrExists) {
		t.Fatalf("taken: %v", err)
	}
	if _, err := s.RenameUser(9999, "x@example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing user: %v", err)
	}
	u, err := s.RenameUser(root.ID, " Ops@Example.com ")
	if err != nil || u.ID != root.ID || u.Username != "ops@example.com" || !u.Admin {
		t.Fatalf("rename: %+v %v", u, err)
	}
	if u, err := s.RenameUser(root.ID, "ops@example.com"); err != nil || u.Username != "ops@example.com" {
		t.Fatalf("same name again: %+v %v", u, err)
	}
}
