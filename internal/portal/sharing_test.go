package portal

import (
	"fmt"
	"strings"
	"testing"

	"sharetoken/internal/accounts"
)

func TestSharingAPI(t *testing.T) {
	s, a := newPortal(t, Config{OpenRegistration: true})
	signUp := func(name string) (string, int64) {
		t.Helper()
		u, tok := testUser(t, s, name)
		return tok, u.ID
	}
	alice, aliceID := signUp("alice@example.com")
	bob, bobID := signUp("bob@example.com")
	carol, _ := signUp("carol@example.com")

	a.call("GET", "/api/v1/sharing", "", nil, 401, nil)
	var v SharingView
	a.call("GET", "/api/v1/sharing", alice, nil, 200, &v)
	if v.Partners == nil || v.Invites == nil || len(v.Partners) != 0 || v.Settings != accounts.DefaultShareSettings ||
		v.WindowHours != 24 || v.MaxPartners != accounts.MaxSharePartners {
		t.Fatalf("empty view: %+v", v)
	}

	var inv accounts.ShareInvite
	a.call("POST", "/api/v1/sharing/invites", alice, map[string]any{"ttl_hours": 2}, 201, &inv)
	if len(inv.Code) != 7 || len(inv.LinkToken) != 8 || strings.Count(inv.Code, "-") != 1 || inv.ExpiresAt-inv.CreatedAt != int64(2*3600e9) {
		t.Fatalf("invite: %+v", inv)
	}
	a.call("POST", "/api/v1/sharing/invites", alice, map[string]any{"ttl_hours": 169}, 400, nil)
	a.call("POST", "/api/v1/sharing/join", alice, map[string]string{"code": inv.Code}, 409, nil)
	var p accounts.SharePartner
	a.call("POST", "/api/v1/sharing/join", bob, map[string]string{"code": strings.ToLower(inv.Code)}, 200, &p)
	if p.ID != aliceID || p.Username != "al***@example.com" {
		t.Fatalf("joined: %+v", p)
	}
	a.call("POST", "/api/v1/sharing/join", carol, map[string]string{"code": inv.Code}, 404, nil)

	a.call("GET", "/api/v1/sharing", alice, nil, 200, &v)
	if len(v.Partners) != 1 || v.Partners[0].ID != bobID || v.Partners[0].Username != "bo***@example.com" || len(v.Invites) != 0 {
		t.Fatalf("alice's view: %+v", v)
	}

	// Settings are each user's own.
	a.call("PUT", "/api/v1/sharing/settings", bob, map[string]any{"share_pct": 120}, 400, nil)
	want := accounts.ShareSettings{OnlyPartners: true, DailyLimitTokens: 100000, SharePct: 50}
	a.call("PUT", "/api/v1/sharing/settings", bob, want, 200, nil)
	a.call("GET", "/api/v1/sharing", bob, nil, 200, &v)
	if v.Settings != want {
		t.Fatalf("bob's settings: %+v", v.Settings)
	}
	a.call("GET", "/api/v1/sharing", alice, nil, 200, &v)
	if v.Settings != accounts.DefaultShareSettings {
		t.Fatalf("alice's settings changed: %+v", v.Settings)
	}

	// Administrators list the groups.
	a.call("GET", "/api/v1/admin/share-groups", alice, nil, 403, nil)
	root, err := s.store.CreateUser("root", true)
	if err != nil {
		t.Fatal(err)
	}
	admin, _, err := s.store.IssueSessionToken(root.ID, "test", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Groups []accounts.ShareGroup `json:"groups"`
		Total  int64                 `json:"total"`
	}
	a.call("GET", "/api/v1/admin/share-groups?user=bob", admin, nil, 200, &list)
	if list.Total != 1 || len(list.Groups) != 1 || strings.Join(list.Groups[0].Members, ",") != "alice@example.com,bob@example.com" {
		t.Fatalf("admin list: %+v", list)
	}

	// Either side ends it.
	a.call("DELETE", fmt.Sprintf("/api/v1/sharing/partners/%d", aliceID), carol, nil, 404, nil)
	a.call("DELETE", fmt.Sprintf("/api/v1/sharing/partners/%d", aliceID), bob, nil, 200, nil)
	a.call("GET", "/api/v1/sharing", alice, nil, 200, &v)
	if len(v.Partners) != 0 {
		t.Fatalf("after parting: %+v", v)
	}

	// Revoking an invite, and guessing codes is throttled.
	a.call("POST", "/api/v1/sharing/invites", alice, map[string]any{}, 201, &inv)
	a.call("DELETE", fmt.Sprintf("/api/v1/sharing/invites/%d", inv.ID), bob, nil, 404, nil)
	a.call("DELETE", fmt.Sprintf("/api/v1/sharing/invites/%d", inv.ID), alice, nil, 200, nil)
	a.call("POST", "/api/v1/sharing/join", bob, map[string]string{"code": inv.Code}, 404, nil)
	for i := 1; i < newAccountMaxFails; i++ { // bob's account is new: it gets fewer tries (invites.go)
		a.call("POST", "/api/v1/sharing/join", bob, map[string]string{"code": "AAAA-AAAA-AAAA-AAAA"}, 404, nil)
	}
	a.call("POST", "/api/v1/sharing/join", bob, map[string]string{"code": "AAAA-AAAA-AAAA-AAAA"}, 429, nil)
}
