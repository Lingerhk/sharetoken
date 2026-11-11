package portal

import (
	"fmt"
	"strings"
	"testing"

	"sharetoken/internal/accounts"
)

func TestPairingAPI(t *testing.T) {
	_, a := newPortal(t, Config{OpenRegistration: true})
	signUp := func(name string) (string, int64) {
		t.Helper()
		var reg AuthResponse
		a.call("POST", "/api/v1/register", "", map[string]string{"username": name, "password": "correct horse"}, 201, &reg)
		return reg.Token, reg.User.ID
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

	// Guessing codes is throttled like guessing passwords.
	for i := 1; i < maxFails; i++ {
		a.call("POST", "/api/v1/pairing/redeem", b1, map[string]string{"code": "AAAA-AAAA-AAAA-AAAA"}, 404, nil)
	}
	a.call("POST", "/api/v1/pairing/redeem", b1, map[string]string{"code": "AAAA-AAAA-AAAA-AAAA"}, 429, nil)
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
