package accounts

import (
	"errors"
	"testing"
	"time"
)

func TestSharePartners(t *testing.T) {
	s := testStore(t)
	now := time.Unix(1_800_000_000, 0)
	s.Now = func() time.Time { return now }
	user := func(name string) *User {
		t.Helper()
		u, err := s.CreateUser(name, false)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	alice, bob, carol := user("alice"), user("bob"), user("carol")

	inv, err := s.CreateShareInvite(alice.ID, 0)
	if err != nil || len(inv.Code) != 16 || inv.ExpiresAt != now.Add(DefaultShareTTL).UnixNano() {
		t.Fatalf("invite: %+v %v", inv, err)
	}
	if _, err := s.CreateShareInvite(alice.ID, MaxShareTTL+time.Hour); !errors.Is(err, ErrInvalid) {
		t.Fatalf("too long: %v", err)
	}
	if _, err := s.JoinShare(alice.ID, inv.Code); !errors.Is(err, ErrOwnCode) {
		t.Fatalf("own invite: %v", err)
	}
	if _, err := s.JoinShare(bob.ID, "AAAAAAAAAAAAAAAA"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong code: %v", err)
	}
	p, err := s.JoinShare(bob.ID, FormatPairingCode(inv.Code))
	if err != nil || p.ID != alice.ID || p.GroupID == 0 {
		t.Fatalf("join: %+v %v", p, err)
	}
	// An invite admits one person.
	if _, err := s.JoinShare(carol.ID, inv.Code); !errors.Is(err, ErrNotFound) {
		t.Fatalf("used invite: %v", err)
	}
	for _, c := range []struct {
		u, want *User
	}{{alice, bob}, {bob, alice}} {
		ps, err := s.SharePartners(c.u.ID)
		if err != nil || len(ps) != 1 || ps[0].ID != c.want.ID || ps[0].GroupID != p.GroupID {
			t.Fatalf("%s's partners: %+v %v", c.u.Username, ps, err)
		}
	}
	if invs, _ := s.ShareInvites(alice.ID); len(invs) != 0 {
		t.Fatalf("open invites after use: %+v", invs)
	}
	// Joining someone again changes nothing and leaves the invite open.
	inv2, _ := s.CreateShareInvite(bob.ID, 0)
	if p2, err := s.JoinShare(alice.ID, inv2.Code); err != nil || p2.GroupID != p.GroupID {
		t.Fatalf("join again: %+v %v", p2, err)
	}
	if invs, _ := s.ShareInvites(bob.ID); len(invs) != 1 {
		t.Fatalf("bob's invites: %+v", invs)
	}
	if err := s.RevokeShareInvite(bob.ID, inv2.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeShareInvite(bob.ID, inv2.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoke twice: %v", err)
	}

	// Settings: defaults, validation, and what the coordinator reads.
	if st, err := s.ShareSettings(alice.ID); err != nil || st != DefaultShareSettings {
		t.Fatalf("defaults: %+v %v", st, err)
	}
	for _, bad := range []ShareSettings{{SharePct: 101}, {SharePct: -1}, {DailyLimitTokens: -1}} {
		if err := s.SetShareSettings(alice.ID, bad); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
	want := ShareSettings{OnlyPartners: true, DailyLimitTokens: 5000, SharePct: 40}
	if err := s.SetShareSettings(alice.ID, want); err != nil {
		t.Fatal(err)
	}
	if err := s.SetShareSettings(alice.ID, want); err != nil { // again: an update
		t.Fatal(err)
	}
	own, grants, err := PartnerGrants(s.DB, bob.ID)
	if err != nil || own != DefaultShareSettings || len(grants) != 1 || grants[0].UserID != alice.ID ||
		grants[0].DailyLimitTokens != 5000 || grants[0].SharePct != 40 {
		t.Fatalf("bob's grants: %+v %+v %v", own, grants, err)
	}
	// A disabled partner gives nothing.
	if err := s.SetDisabled(alice.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, grants, _ := PartnerGrants(s.DB, bob.ID); len(grants) != 0 {
		t.Fatalf("disabled partner: %+v", grants)
	}
	if err := s.SetDisabled(alice.ID, false); err != nil {
		t.Fatal(err)
	}

	// Administrators see the groups with full names.
	if n, err := s.CountShareGroups(ShareGroupQuery{User: "BOB"}); err != nil || n != 1 {
		t.Fatalf("count: %d %v", n, err)
	}
	gs, err := s.ListShareGroups(ShareGroupQuery{Limit: 10})
	if err != nil || len(gs) != 1 || len(gs[0].Members) != 2 || gs[0].Members[0] != "alice" {
		t.Fatalf("groups: %+v %v", gs, err)
	}

	// Leaving dissolves a group of two.
	if err := s.LeaveSharePartner(alice.ID, carol.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("leave a stranger: %v", err)
	}
	if err := s.LeaveSharePartner(alice.ID, bob.ID); err != nil {
		t.Fatal(err)
	}
	if ps, _ := s.SharePartners(bob.ID); len(ps) != 0 {
		t.Fatalf("bob's partners after: %+v", ps)
	}
	if n, _ := s.CountShareGroups(ShareGroupQuery{}); n != 0 {
		t.Fatalf("groups after: %d", n)
	}
}

func TestSharePartnerLimit(t *testing.T) {
	s := testStore(t)
	users := make([]*User, MaxSharePartners+2)
	for i := range users {
		u, err := s.CreateUser("user"+string(rune('a'+i)), false)
		if err != nil {
			t.Fatal(err)
		}
		users[i] = u
	}
	hub := users[0]
	for _, u := range users[1 : MaxSharePartners+1] {
		inv, err := s.CreateShareInvite(hub.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.JoinShare(u.ID, inv.Code); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.CreateShareInvite(hub.ID, 0); !errors.Is(err, ErrLimit) {
		t.Fatalf("invite past the limit: %v", err)
	}
	// Nor can the hub join someone else's invite.
	inv, err := s.CreateShareInvite(users[len(users)-1].ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.JoinShare(hub.ID, inv.Code); !errors.Is(err, ErrLimit) {
		t.Fatalf("join past the limit: %v", err)
	}
}
