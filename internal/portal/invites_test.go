package portal

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"sharetoken/internal/accounts"
)

func TestInviteLinks(t *testing.T) {
	clock := time.Now()
	s, a := newPortal(t, Config{OpenRegistration: true, Now: func() time.Time { return clock }})
	signUp := func(name string) (string, int64) {
		t.Helper()
		u, tok := testUser(t, s, name)
		return tok, u.ID
	}
	seller, sellerID := signUp("seller@example.com")
	buyer, _ := signUp("buyer@example.com")
	friend, friendID := signUp("friend@example.com")
	clock = clock.Add(2 * time.Hour) // none of them is a new account any more

	var code accounts.PairingCode
	a.call("POST", "/api/v1/pairing/codes", seller, map[string]any{"label": "team"}, 201, &code)
	if !accounts.ValidLinkToken(code.LinkToken) || code.Approval {
		t.Fatalf("code: %+v", code)
	}

	// The page of a link sees who it is from without signing in, masked.
	var p accounts.InvitePreview
	a.call("GET", "/api/v1/invite/p/"+code.LinkToken, "", nil, 200, &p)
	if p.Inviter != "se***@example.com" || p.Label != "team" || p.Kind != "p" || p.Approval || p.ExpiresAt != code.ExpiresAt {
		t.Fatalf("preview: %+v", p)
	}
	a.call("GET", "/api/v1/invite/s/"+code.LinkToken, "", nil, 404, nil)
	a.call("GET", "/api/v1/invite/p/"+strings.ReplaceAll(code.Code, "-", ""), "", nil, 404, nil)

	// A link pasted where a code goes works; one of the other kind is refused.
	link := "https://sharetoken.app/p#" + code.LinkToken
	var inv accounts.ShareInvite
	a.call("POST", "/api/v1/sharing/invites", friend, map[string]any{}, 201, &inv)
	a.call("POST", "/api/v1/pairing/redeem", buyer, map[string]string{"code": "https://sharetoken.app/s#" + inv.LinkToken}, 400, nil)
	a.call("POST", "/api/v1/sharing/join", buyer, map[string]string{"code": link}, 400, nil)
	var paired accounts.PairedUser
	a.call("POST", "/api/v1/pairing/redeem", buyer, map[string]string{"code": link}, 200, &paired)
	if paired.ID != sellerID || paired.Username != "se***@example.com" || paired.Pending {
		t.Fatalf("redeem a link: %+v", paired)
	}

	// /invite/accept takes either kind, by link or by bare token or code.
	var acc AcceptedInvite
	a.call("POST", "/api/v1/invite/accept", buyer, map[string]string{"link": "sharetoken://s/" + inv.LinkToken}, 200, &acc)
	if acc.Kind != "s" || acc.ID != friendID || acc.User != "fr***@example.com" || acc.Pending {
		t.Fatalf("accept a share link: %+v", acc)
	}
	a.call("POST", "/api/v1/sharing/invites", friend, map[string]any{}, 201, &inv)
	a.call("POST", "/api/v1/invite/accept", seller, map[string]string{"link": inv.LinkToken}, 200, &acc)
	if acc.Kind != "s" || acc.ID != friendID {
		t.Fatalf("accept a bare share token: %+v", acc)
	}
	a.call("POST", "/api/v1/sharing/invites", seller, map[string]any{}, 201, &inv)
	a.call("POST", "/api/v1/invite/accept", seller, map[string]string{"code": inv.Code}, 409, nil) // one's own
	a.call("POST", "/api/v1/invite/accept", friend, map[string]string{"code": strings.ToLower(inv.Code)}, 200, &acc)
	if acc.Kind != "s" || acc.ID != sellerID {
		t.Fatalf("accept a share code: %+v", acc)
	}
	a.call("POST", "/api/v1/invite/accept", friend, map[string]string{}, 404, nil)

	// With approval: the use files a request the owner approves.
	var ask accounts.PairingCode
	a.call("POST", "/api/v1/pairing/codes", friend, map[string]any{"approval": true, "max_uses": 1}, 201, &ask)
	a.call("GET", "/api/v1/invite/p/"+ask.LinkToken, "", nil, 200, &p)
	if !p.Approval {
		t.Fatalf("preview of an approval code: %+v", p)
	}
	a.call("POST", "/api/v1/invite/accept", buyer, map[string]string{"link": "https://x/p#" + ask.LinkToken}, 202, &acc)
	if !acc.Pending || acc.Kind != "p" || acc.ID != friendID {
		t.Fatalf("request: %+v", acc)
	}
	var v PairingView
	a.call("GET", "/api/v1/pairing", buyer, nil, 200, &v)
	if len(v.Waiting) != 1 || v.Waiting[0].Username != "fr***@example.com" || len(v.Sellers) != 1 {
		t.Fatalf("buyer's view: %+v", v)
	}
	a.call("GET", "/api/v1/pairing", friend, nil, 200, &v)
	if len(v.Requests) != 1 || v.Requests[0].Username != "bu***@example.com" || len(v.Buyers) != 0 {
		t.Fatalf("seller's view: %+v", v)
	}
	id := v.Requests[0].ID
	a.call("POST", fmt.Sprintf("/api/v1/pairing/requests/%d/approve", id), buyer, nil, 404, nil)
	a.call("POST", fmt.Sprintf("/api/v1/pairing/requests/%d/approve", id), friend, nil, 200, nil)
	a.call("GET", "/api/v1/pairing", friend, nil, 200, &v)
	if len(v.Requests) != 0 || len(v.Buyers) != 1 {
		t.Fatalf("after approval: %+v", v)
	}
	a.call("POST", fmt.Sprintf("/api/v1/pairing/requests/%d/approve", id), friend, nil, 404, nil)

	// Rejected and withdrawn requests.
	var ask2 accounts.ShareInvite
	a.call("POST", "/api/v1/sharing/invites", buyer, map[string]any{"approval": true}, 201, &ask2)
	var sp accounts.SharePartner
	other, _ := signUp("other@example.com")
	a.call("POST", "/api/v1/sharing/join", other, map[string]string{"code": ask2.Code}, 202, &sp)
	if !sp.Pending {
		t.Fatalf("share request: %+v", sp)
	}
	var sv SharingView
	a.call("GET", "/api/v1/sharing", other, nil, 200, &sv)
	if len(sv.Waiting) != 1 {
		t.Fatalf("waiting: %+v", sv.Waiting)
	}
	a.call("DELETE", fmt.Sprintf("/api/v1/sharing/waiting/%d", sv.Waiting[0].ID), buyer, nil, 404, nil)
	a.call("DELETE", fmt.Sprintf("/api/v1/sharing/waiting/%d", sv.Waiting[0].ID), other, nil, 200, nil)
	a.call("POST", "/api/v1/sharing/join", other, map[string]string{"code": ask2.Code}, 202, nil)
	a.call("GET", "/api/v1/sharing", buyer, nil, 200, &sv)
	if len(sv.Requests) != 1 || sv.Requests[0].Username != "ot***@example.com" {
		t.Fatalf("requests: %+v", sv.Requests)
	}
	a.call("DELETE", fmt.Sprintf("/api/v1/sharing/requests/%d", sv.Requests[0].ID), buyer, nil, 200, nil)
	a.call("GET", "/api/v1/sharing", other, nil, 200, &sv)
	if len(sv.Waiting) != 0 || len(sv.Partners) != 0 {
		t.Fatalf("after rejection: %+v", sv)
	}

	// A wrong link counts as a wrong code; the preview has its own budget.
	for i := 2; i < maxFails*4; i++ { // two wrong ones above
		a.call("GET", "/api/v1/invite/p/AAAAAAAAAAAA", "", nil, 404, nil)
	}
	a.call("GET", "/api/v1/invite/p/"+code.LinkToken, "", nil, 429, nil)
}

func TestCodeGuessingLimits(t *testing.T) {
	clock := time.Now()
	s, a := newPortal(t, Config{OpenRegistration: true, Now: func() time.Time { return clock }})
	oldTok, _ := func() (string, int64) { u, tok := testUser(t, s, "old@example.com"); return tok, u.ID }()
	clock = clock.Add(2 * time.Hour)
	newTok, _ := func() (string, int64) { u, tok := testUser(t, s, "new@example.com"); return tok, u.ID }()
	wrong := map[string]string{"code": "AAAAA-AAAAA"}

	// A new account gets fewer tries than an old one (the address has its own count).
	for range newAccountMaxFails {
		a.call("POST", "/api/v1/pairing/redeem", newTok, wrong, 404, nil)
	}
	a.call("POST", "/api/v1/pairing/redeem", newTok, wrong, 429, nil)
	clock = clock.Add(failWindow + time.Second)
	for range maxFails {
		a.call("POST", "/api/v1/invite/accept", oldTok, map[string]string{"code": "AAAAA-AAAAA", "kind": "p"}, 404, nil)
	}
	a.call("POST", "/api/v1/invite/accept", oldTok, map[string]any{"code": "AAAAA-AAAAA", "kind": "p"}, 429, nil)

	// Over a day an account has dailyMaxFails, however slowly it tries.
	clock = clock.Add(failWindow + time.Second)
	for range dailyMaxFails - maxFails {
		s.recordFailure("inv:day:u:1")
	}
	a.call("POST", "/api/v1/invite/accept", oldTok, map[string]string{"code": "AAAAA-AAAAA"}, 429, nil)
	clock = clock.Add(failDay)
	a.call("POST", "/api/v1/invite/accept", oldTok, map[string]string{"code": "AAAAA-AAAAA"}, 404, nil)

	// When wrong codes pour in from everyone, each account gets surgeMaxFails.
	clock = clock.Add(failWindow + time.Second)
	for range surgeFails {
		s.recordFailure(allFailsKey)
	}
	for range surgeMaxFails {
		a.call("POST", "/api/v1/sharing/join", oldTok, wrong, 404, nil)
	}
	a.call("POST", "/api/v1/sharing/join", oldTok, wrong, 429, nil)
	if s.surgeAlerted.IsZero() {
		t.Fatal("the administrators were not told")
	}
}
