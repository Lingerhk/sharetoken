package accounts

import (
	"errors"
	"testing"
	"time"
)

func TestPairing(t *testing.T) {
	s := testStore(t)
	now := time.Unix(1_800_000_000, 0)
	s.Now = func() time.Time { return now }
	user := func(name string) *User {
		t.Helper()
		u, err := s.CreateUser(name, "correct horse", false)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	seller, other, b1, b2 := user("seller"), user("other"), user("buyer1"), user("buyer2")

	code, err := s.CreatePairingCode(seller.ID, " team ", 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(code.Code) != 16 || code.Label != "team" || code.ExpiresAt != now.Add(DefaultPairingTTL).UnixNano() {
		t.Fatalf("code: %+v", code)
	}

	// A code is read the way people pass it on: grouped, lower case, with spaces.
	spoken := " " + FormatPairingCode(code.Code)[:9] + " " + FormatPairingCode(code.Code)[10:] + "\n"
	if NormalizePairingCode(spoken) != code.Code {
		t.Fatalf("normalize %q: %q", spoken, NormalizePairingCode(spoken))
	}
	if _, err := s.RedeemPairingCode(seller.ID, code.Code); !errors.Is(err, ErrOwnCode) {
		t.Fatalf("own code: %v", err)
	}
	if _, err := s.RedeemPairingCode(b1.ID, "AAAAAAAAAAAAAAAA"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong code: %v", err)
	}
	if _, err := s.RedeemPairingCode(b1.ID, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty code: %v", err)
	}
	p, err := s.RedeemPairingCode(b1.ID, spoken)
	if err != nil || p.ID != seller.ID || p.Username != "seller" {
		t.Fatalf("redeem: %+v %v", p, err)
	}
	// Again: nothing changes and no use is taken.
	if _, err := s.RedeemPairingCode(b1.ID, code.Code); err != nil {
		t.Fatal(err)
	}
	if cs, _ := s.PairingCodes(seller.ID); len(cs) != 1 || cs[0].Used != 1 {
		t.Fatalf("codes after one buyer: %+v", cs)
	}
	// One code, many buyers, up to its limit.
	if _, err := s.RedeemPairingCode(b2.ID, code.Code); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemPairingCode(other.ID, code.Code); !errors.Is(err, ErrNotFound) {
		t.Fatalf("used-up code: %v", err)
	}
	if cs, _ := s.PairingCodes(seller.ID); len(cs) != 0 {
		t.Fatalf("a used-up code is still listed: %+v", cs)
	}

	sellers, buyers, err := s.Pairings(seller.ID)
	if err != nil || len(sellers) != 0 || len(buyers) != 2 || buyers[0].Label != "team" {
		t.Fatalf("seller's view: %+v %+v %v", sellers, buyers, err)
	}
	sellers, buyers, _ = s.Pairings(b1.ID)
	if len(sellers) != 1 || sellers[0].ID != seller.ID || len(buyers) != 0 {
		t.Fatalf("buyer's view: %+v %+v", sellers, buyers)
	}
	if ids, err := PairedSellers(s.DB, b1.ID); err != nil || len(ids) != 1 || ids[0] != seller.ID {
		t.Fatalf("paired sellers: %v %v", ids, err)
	}
	if ids, _ := PairedSellers(s.DB, other.ID); len(ids) != 0 {
		t.Fatalf("an unpaired user has sellers: %v", ids)
	}

	// A buyer may pair with several sellers.
	c2, err := s.CreatePairingCode(other.ID, "", time.Hour, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemPairingCode(b1.ID, c2.Code); err != nil {
		t.Fatal(err)
	}
	if ids, _ := PairedSellers(s.DB, b1.ID); len(ids) != 2 {
		t.Fatalf("paired sellers: %v", ids)
	}

	// Either side ends a pairing; only its own.
	if err := s.Unpair(b2.ID, other.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unpair what is not paired: %v", err)
	}
	if err := s.Unpair(b1.ID, seller.ID); err != nil {
		t.Fatal(err)
	}
	if ids, _ := PairedSellers(s.DB, b1.ID); len(ids) != 1 || ids[0] != other.ID {
		t.Fatalf("after leaving: %v", ids)
	}

	// Revoked and expired codes admit nobody; the pairings made with them stay.
	if err := s.RevokePairingCode(seller.ID, c2.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoking another seller's code: %v", err)
	}
	if err := s.RevokePairingCode(other.ID, c2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemPairingCode(b2.ID, c2.Code); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked code: %v", err)
	}
	if ids, _ := PairedSellers(s.DB, b1.ID); len(ids) != 1 {
		t.Fatalf("revoking a code ended a pairing: %v", ids)
	}
	c3, _ := s.CreatePairingCode(seller.ID, "", time.Hour, 0)
	now = now.Add(time.Hour + time.Second)
	if _, err := s.RedeemPairingCode(b1.ID, c3.Code); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired code: %v", err)
	}

	// A disabled seller's code admits nobody.
	c4, _ := s.CreatePairingCode(seller.ID, "", time.Hour, 0)
	if err := s.SetDisabled(seller.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemPairingCode(b1.ID, c4.Code); !errors.Is(err, ErrNotFound) {
		t.Fatalf("code of a disabled seller: %v", err)
	}

	for _, bad := range []struct {
		ttl  time.Duration
		uses int64
	}{{time.Second, 0}, {MaxPairingTTL + time.Hour, 0}, {time.Hour, -1}, {time.Hour, MaxPairedBuyers + 1}} {
		if _, err := s.CreatePairingCode(other.ID, "", bad.ttl, bad.uses); !errors.Is(err, ErrInvalid) {
			t.Errorf("ttl %s uses %d: %v", bad.ttl, bad.uses, err)
		}
	}
	for i := 0; i < MaxPairingCodes; i++ {
		if _, err := s.CreatePairingCode(other.ID, "", time.Hour, 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.CreatePairingCode(other.ID, "", time.Hour, 0); !errors.Is(err, ErrLimit) {
		t.Fatalf("code limit: %v", err)
	}
}

func TestPairingLimitPrivateAndListing(t *testing.T) {
	s := testStore(t)
	now := time.Unix(1_800_000_000, 0)
	s.Now = func() time.Time { return now }
	user := func(name string) *User {
		t.Helper()
		u, err := s.CreateUser(name, "correct horse", false)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	buyer := user("buyer@example.com")
	var sellers []*User
	for i, name := range []string{"sel1", "sel2", "sel3", "sel4_x"} {
		u := user(name)
		sellers = append(sellers, u)
		c, err := s.CreatePairingCode(u.ID, "for "+name, time.Hour, 0)
		if err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Minute)
		_, err = s.RedeemPairingCode(buyer.ID, c.Code)
		if i < MaxPairedSellers && err != nil {
			t.Fatalf("seller %d: %v", i, err)
		}
		if i >= MaxPairedSellers && !errors.Is(err, ErrLimit) {
			t.Fatalf("a buyer paired with more than %d sellers: %v", MaxPairedSellers, err)
		}
	}
	// Leaving one makes room for another.
	if err := s.Unpair(buyer.ID, sellers[0].ID); err != nil {
		t.Fatal(err)
	}
	c, _ := s.CreatePairingCode(sellers[3].ID, "late", time.Hour, 0)
	if _, err := s.RedeemPairingCode(buyer.ID, c.Code); err != nil {
		t.Fatalf("after leaving one: %v", err)
	}

	// Private mode: off at first, on and off again, and setting it twice is fine.
	if on, err := s.PrivateSeller(sellers[1].ID); err != nil || on {
		t.Fatalf("private at first: %v %v", on, err)
	}
	for range 2 {
		if err := s.SetPrivateSeller(sellers[1].ID, true); err != nil {
			t.Fatal(err)
		}
	}
	if on, _ := s.PrivateSeller(sellers[1].ID); !on {
		t.Fatal("private mode did not turn on")
	}
	if ids, err := PrivateSellers(s.DB); err != nil || len(ids) != 1 || ids[0] != sellers[1].ID {
		t.Fatalf("private sellers: %v %v", ids, err)
	}

	// The administrator's listing: newest first, full names, searchable by either side.
	all, err := s.ListPairings(PairingQuery{Limit: 10})
	if err != nil || len(all) != 3 {
		t.Fatalf("listing: %+v %v", all, err)
	}
	if all[0].Seller != "sel4_x" || all[0].Buyer != "buyer@example.com" || all[0].Label != "late" || all[0].SellerPrivate {
		t.Fatalf("newest pairing: %+v", all[0])
	}
	if !all[1].SellerPrivate && !all[2].SellerPrivate {
		t.Fatalf("the private seller is not marked: %+v", all)
	}
	for q, want := range map[string]int64{"": 3, "BUYER@": 3, "SEL4_": 1, "sel_": 0, "nobody": 0} {
		if n, err := s.CountPairings(PairingQuery{User: q}); err != nil || n != want {
			t.Errorf("search %q: %d %v, want %d", q, n, err, want)
		}
	}
	if page, _ := s.ListPairings(PairingQuery{Limit: 1, Offset: 2}); len(page) != 1 || page[0].Seller != "sel2" {
		t.Fatalf("paging: %+v", page)
	}
	if n, _ := s.CountPairings(PairingQuery{From: all[0].Since}); n != 1 {
		t.Fatalf("from: %d", n)
	}

	if err := s.SetPrivateSeller(sellers[1].ID, false); err != nil {
		t.Fatal(err)
	}
	if ids, _ := PrivateSellers(s.DB); len(ids) != 0 {
		t.Fatalf("private sellers after turning off: %v", ids)
	}
}
