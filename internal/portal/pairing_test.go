package portal

import (
	"fmt"
	"strings"
	"testing"

	"sharetoken/internal/accounts"
)

func TestPairingAPI(t *testing.T) {
	s, a := newPortal(t, Config{OpenRegistration: true})
	signUp := func(name string) (string, int64) {
		t.Helper()
		u, tok := testUser(t, s, name)
		return tok, u.ID
	}
	seller, sellerID := signUp("seller@example.com")
	b1, b1ID := signUp("buyer-one")
	b2, _ := signUp("buyer2@example.com")

	a.call("GET", "/api/v1/pairing", "", nil, 401, nil)
	var v PairingView
	a.call("GET", "/api/v1/pairing", seller, nil, 200, &v)
	if v.Restricted || v.Sellers == nil || v.Buyers == nil || v.Codes == nil || len(v.Codes) != 0 {
		t.Fatalf("empty view: %+v", v)
	}

	var code accounts.PairingCode
	a.call("POST", "/api/v1/pairing/codes", seller, map[string]any{"label": "team", "ttl_hours": 2, "max_uses": 0}, 201, &code)
	if len(code.Code) != 19 || strings.Count(code.Code, "-") != 3 || code.ExpiresAt-code.CreatedAt != int64(2*3600e9) {
		t.Fatalf("code: %+v", code)
	}
	a.call("POST", "/api/v1/pairing/codes", seller, map[string]any{"ttl_hours": -1}, 400, nil)
	a.call("POST", "/api/v1/pairing/codes", seller, map[string]any{"ttl_hours": 100000}, 400, nil)
	a.call("POST", "/api/v1/pairing/codes", seller, map[string]any{"max_uses": -2}, 400, nil)

	// The seller cannot use its own code; a buyer can, however it types it.
	a.call("POST", "/api/v1/pairing/redeem", seller, map[string]string{"code": code.Code}, 409, nil)
	var got accounts.PairedUser
	a.call("POST", "/api/v1/pairing/redeem", b1, map[string]string{"code": " " + strings.ToLower(code.Code) + " "}, 200, &got)
	if got.ID != sellerID || got.Username != "se***@example.com" {
		t.Fatalf("redeemed: %+v", got)
	}
	a.call("POST", "/api/v1/pairing/redeem", b2, map[string]string{"code": strings.ReplaceAll(code.Code, "-", "")}, 200, nil)

	a.call("GET", "/api/v1/pairing", b1, nil, 200, &v)
	if !v.Restricted || len(v.Sellers) != 1 || v.Sellers[0].ID != sellerID || v.Sellers[0].Username != "se***@example.com" || len(v.Buyers) != 0 || len(v.Codes) != 0 {
		t.Fatalf("buyer's view: %+v", v)
	}
	a.call("GET", "/api/v1/pairing", seller, nil, 200, &v)
	if v.Restricted || len(v.Buyers) != 2 || len(v.Codes) != 1 || v.Codes[0].Used != 2 || v.Codes[0].Code != code.Code {
		t.Fatalf("seller's view: %+v", v)
	}
	names := map[string]bool{}
	for _, b := range v.Buyers {
		names[b.Username] = true
		if b.Label != "team" {
			t.Errorf("buyer %+v: no code label", b)
		}
	}
	if !names["bu***"] || !names["bu***@example.com"] {
		t.Fatalf("buyers as the seller sees them: %v", names)
	}

	// Each side ends only its own pairings.
	a.call("DELETE", fmt.Sprintf("/api/v1/pairing/buyers/%d", sellerID), b1, nil, 404, nil)
	a.call("DELETE", "/api/v1/pairing/buyers/x", seller, nil, 400, nil)
	a.call("DELETE", fmt.Sprintf("/api/v1/pairing/buyers/%d", b1ID), seller, nil, 200, nil)
	a.call("GET", "/api/v1/pairing", b1, nil, 200, &v)
	if v.Restricted || len(v.Sellers) != 0 {
		t.Fatalf("removed buyer's view: %+v", v)
	}
	a.call("DELETE", fmt.Sprintf("/api/v1/pairing/sellers/%d", sellerID), b2, nil, 200, nil)
	a.call("DELETE", fmt.Sprintf("/api/v1/pairing/sellers/%d", sellerID), b2, nil, 404, nil)

	// A revoked code admits nobody, and only its seller revokes it.
	a.call("DELETE", fmt.Sprintf("/api/v1/pairing/codes/%d", code.ID), b1, nil, 404, nil)
	a.call("DELETE", fmt.Sprintf("/api/v1/pairing/codes/%d", code.ID), seller, nil, 200, nil)
	a.call("POST", "/api/v1/pairing/redeem", b1, map[string]string{"code": code.Code}, 404, nil)

	// Guessing pairing codes is throttled like guessing sign-in codes.
	for i := 1; i < maxFails; i++ {
		a.call("POST", "/api/v1/pairing/redeem", b1, map[string]string{"code": "AAAA-AAAA-AAAA-AAAA"}, 404, nil)
	}
	a.call("POST", "/api/v1/pairing/redeem", b1, map[string]string{"code": "AAAA-AAAA-AAAA-AAAA"}, 429, nil)
}

func TestPairingPrivateLimitAndAdminList(t *testing.T) {
	s, a := newPortal(t, Config{OpenRegistration: true})
	signUp := func(name string) (string, int64) {
		t.Helper()
		u, tok := testUser(t, s, name)
		return tok, u.ID
	}
	buyer, _ := signUp("buyer@example.com")
	var v PairingView
	var sellers []string
	for i := 0; i <= accounts.MaxPairedSellers; i++ {
		tok, _ := signUp(fmt.Sprintf("seller%d@example.com", i))
		sellers = append(sellers, tok)
		var code accounts.PairingCode
		a.call("POST", "/api/v1/pairing/codes", tok, map[string]any{"label": fmt.Sprintf("code %d", i)}, 201, &code)
		want := 200
		if i == accounts.MaxPairedSellers {
			want = 409 // one seller too many
		}
		a.call("POST", "/api/v1/pairing/redeem", buyer, map[string]string{"code": code.Code}, want, nil)
	}
	a.call("GET", "/api/v1/pairing", buyer, nil, 200, &v)
	if len(v.Sellers) != accounts.MaxPairedSellers || v.MaxSellers != 3 || v.Private {
		t.Fatalf("buyer's view: %+v", v)
	}
	// A code lasts 30 days at most.
	a.call("POST", "/api/v1/pairing/codes", sellers[0], map[string]any{"ttl_hours": 720}, 201, nil)
	a.call("POST", "/api/v1/pairing/codes", sellers[0], map[string]any{"ttl_hours": 721}, 400, nil)

	// Private mode is the seller's own switch.
	a.call("PUT", "/api/v1/pairing/private", "", map[string]bool{"enabled": true}, 401, nil)
	a.call("PUT", "/api/v1/pairing/private", sellers[1], map[string]bool{"enabled": true}, 200, nil)
	a.call("GET", "/api/v1/pairing", sellers[1], nil, 200, &v)
	if !v.Private || len(v.Buyers) != 1 {
		t.Fatalf("private seller's view: %+v", v)
	}
	a.call("GET", "/api/v1/pairing", sellers[0], nil, 200, &v)
	if v.Private {
		t.Fatalf("another seller became private: %+v", v)
	}

	// Administrators see every pairing with full names; nobody else does.
	a.call("GET", "/api/v1/admin/pairings", buyer, nil, 403, nil)
	root, err := s.store.CreateUser("root", true)
	if err != nil {
		t.Fatal(err)
	}
	admin, _, err := s.store.IssueSessionToken(root.ID, "test", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Pairings []accounts.Pairing `json:"pairings"`
		Total    int64              `json:"total"`
	}
	a.call("GET", "/api/v1/admin/pairings", admin, nil, 200, &list)
	if list.Total != 3 || len(list.Pairings) != 3 || list.Pairings[0].Buyer != "buyer@example.com" || list.Pairings[0].Seller != "seller2@example.com" || list.Pairings[0].Label != "code 2" {
		t.Fatalf("admin list: %+v", list)
	}
	private := 0
	for _, p := range list.Pairings {
		if p.SellerPrivate {
			private++
			if p.Seller != "seller1@example.com" {
				t.Errorf("wrong seller marked private: %+v", p)
			}
		}
	}
	if private != 1 {
		t.Fatalf("private sellers in the list: %d", private)
	}
	a.call("GET", "/api/v1/admin/pairings?user=SELLER1&page_size=10", admin, nil, 200, &list)
	if list.Total != 1 || list.Pairings[0].Seller != "seller1@example.com" {
		t.Fatalf("search: %+v", list)
	}
	a.call("PUT", "/api/v1/pairing/private", sellers[1], map[string]bool{"enabled": false}, 200, nil)
	a.call("GET", "/api/v1/pairing", sellers[1], nil, 200, &v)
	if v.Private {
		t.Fatal("private mode did not turn off")
	}
}

func TestMaskName(t *testing.T) {
	for in, want := range map[string]string{
		"alice@example.com": "al***@example.com", "ab@example.com": "a***@example.com", "a@b.co": "a***@b.co",
		"bob": "bo***", "al": "a***", "": "***",
	} {
		if got := maskName(in); got != want {
			t.Errorf("maskName(%q) = %q, want %q", in, got, want)
		}
	}
}
