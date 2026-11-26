package accounts

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
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
	if err := mysqldb.Migrate(db, "accounts", []mysqldb.Migration{{Version: 1, Name: "accounts", SQL: ledger.Schema + Schema + PairingSchema + NoPasswords + SharingSchema + InviteLinkSchema + ShareInviteUsesSchema + InviteCooldownSchema + MailboxSchema}}); err != nil {
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

func TestMailboxKey(t *testing.T) {
	for in, want := range map[string]string{
		"Alice@Example.com":          "alice@example.com",
		"alice+promo@example.com":    "alice@example.com",
		"alice+a+b@example.com":      "alice@example.com",
		"a.lice@example.com":         "a.lice@example.com", // dots mean something elsewhere
		"a.li.ce@gmail.com":          "alice@gmail.com",
		"A.Lice+x@GoogleMail.com":    "alice@gmail.com",
		"alice@googlemail.com":       "alice@gmail.com",
		"alice@gmail.com.example.cn": "alice@gmail.com.example.cn",
		"root":                       "root",
		" bob+1@mail.example.org ":   "bob@mail.example.org",
	} {
		if got := MailboxKey(in); got != want {
			t.Errorf("MailboxKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// One mailbox has one first account, however its address is spelled and
// however many sign up at once; rows from before the column get the same key
// from SQL as from Go.
func TestCreateUserOncePerMailbox(t *testing.T) {
	s := testStore(t)
	names := []string{"carol@gmail.com", "c.arol@gmail.com", "carol+1@gmail.com", "ca.rol+x@googlemail.com", "carol+2@gmail.com", "c.a.r.o.l@gmail.com"}
	var wg sync.WaitGroup
	var firsts atomic.Int64
	for _, n := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, first, err := s.CreateUserOnce(n, false)
			if err != nil {
				t.Error(err)
			}
			if first {
				firsts.Add(1)
			}
		}()
	}
	wg.Wait()
	if firsts.Load() != 1 {
		t.Fatalf("%d first accounts of one mailbox, want 1", firsts.Load())
	}
	if _, first, err := s.CreateUserOnce("carol@example.com", false); err != nil || !first {
		t.Fatalf("another mailbox: first %v, %v", first, err)
	}
	us, err := s.UsersByMailbox("Carol+any@gmail.com")
	if err != nil || len(us) != len(names) {
		t.Fatalf("%d accounts of the mailbox, want %d: %v", len(us), len(names), err)
	}

	// Rows an older binary wrote have no key until the next sign-up or lookup.
	for _, n := range append(names, "carol@example.com", "root", "dave+tag@example.org", "x.y+z@sub.example.org") {
		if _, err := s.DB.Exec(`INSERT INTO users (username, created_at) VALUES (?, 1) ON DUPLICATE KEY UPDATE mailbox = ''`, n); err != nil {
			t.Fatal(err)
		}
	}
	if _, first, err := s.CreateUserOnce("dave@example.org", false); err != nil || first {
		t.Fatalf("the mailbox of an unkeyed row: first %v, %v", first, err)
	}
	rows, err := s.DB.Query(`SELECT username, mailbox FROM users`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, key string
		if err := rows.Scan(&name, &key); err != nil {
			t.Fatal(err)
		}
		if key != MailboxKey(name) {
			t.Errorf("mailbox of %q is %q in SQL, %q in Go", name, key, MailboxKey(name))
		}
	}
	// A renamed account moves to its new mailbox.
	if _, err := s.RenameUser(us[0].ID, "erin+1@example.org"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.UsersByMailbox("erin@example.org"); len(got) != 1 || got[0].ID != us[0].ID {
		t.Fatalf("after a rename: %+v", got)
	}
}
