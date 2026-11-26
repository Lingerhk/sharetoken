package portal

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
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
	// Asking again right away would mail the owner again: it waits.
	waits := func(path string, body map[string]string) {
		t.Helper()
		var e ErrorBody
		if a.call("POST", path, other, body, 429, &e); e.Error.Code != "request_cooldown" {
			t.Fatalf("%s: %+v", path, e)
		}
	}
	waits("/api/v1/sharing/join", map[string]string{"code": ask2.Code})
	waits("/api/v1/invite/accept", map[string]string{"link": ask2.LinkToken})
	clock = clock.Add(accounts.WithdrawCooldown)
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
	clock = clock.Add(accounts.WithdrawCooldown) // a rejection waits longer
	waits("/api/v1/sharing/join", map[string]string{"code": ask2.Code})

	// A wrong link in the preview counts against the address.
	clock = clock.Add(guessDay + time.Second)
	for range ipMaxFails {
		a.call("GET", "/api/v1/invite/p/AAAAAAAA", "", nil, 404, nil)
	}
	a.call("GET", "/api/v1/invite/p/"+code.LinkToken, "", nil, 429, nil)
}

func TestCodeGuessingLimits(t *testing.T) {
	clock := time.Now()
	s, a := newPortal(t, Config{OpenRegistration: true, Now: func() time.Time { return clock }})
	user := func(name string) (string, int64) { u, tok := testUser(t, s, name); return tok, u.ID }
	oldTok, oldID := user("old@example.com")
	old2, _ := user("old2@example.com")
	old3, _ := user("old3@example.com")
	clock = clock.Add(guessDay + time.Hour)
	newTok, _ := user("new@example.com")
	wrong := map[string]string{"code": "AAA-AAA"}
	var e ErrorBody

	// A new account gets newAccountMaxFails for its first day.
	for range newAccountMaxFails {
		a.call("POST", "/api/v1/pairing/redeem", newTok, wrong, 404, nil)
	}
	a.call("POST", "/api/v1/pairing/redeem", newTok, wrong, 429, nil)
	clock = clock.Add(guessWindow + time.Second)
	a.call("POST", "/api/v1/pairing/redeem", newTok, wrong, 429, nil)

	// An account gets userMaxFails per guessWindow ...
	for range userMaxFails {
		a.call("POST", "/api/v1/invite/accept", oldTok, map[string]string{"code": "AAA-AAA", "kind": "p"}, 404, nil)
	}
	a.call("POST", "/api/v1/invite/accept", oldTok, map[string]any{"code": "AAA-AAA", "kind": "p"}, 429, &e)
	if e.Error.Code != "throttled" {
		t.Fatalf("refused with %q", e.Error.Code)
	}
	// ... and userDayMaxFails per day, however slowly it tries.
	clock = clock.Add(guessWindow + time.Second)
	for range userDayMaxFails - userMaxFails - 1 {
		s.recordFailure(fmt.Sprintf("inv:ud:%d", oldID))
	}
	a.call("POST", "/api/v1/invite/accept", oldTok, map[string]string{"code": "AAA-AAA"}, 404, nil)
	a.call("POST", "/api/v1/invite/accept", oldTok, map[string]string{"code": "AAA-AAA"}, 429, nil)
	clock = clock.Add(guessDay)
	a.call("POST", "/api/v1/invite/accept", oldTok, map[string]string{"code": "AAA-AAA"}, 404, nil)

	// An address gets ipMaxFails per guessWindow, whichever accounts try.
	clock = clock.Add(guessDay)
	for range userMaxFails {
		a.call("POST", "/api/v1/sharing/join", old2, wrong, 404, nil)
	}
	for range ipMaxFails - userMaxFails {
		a.call("POST", "/api/v1/sharing/join", old3, wrong, 404, nil)
	}
	a.call("POST", "/api/v1/sharing/join", oldTok, wrong, 429, nil)

	// codeSurgeFails wrong codes from everyone in an hour stop codes for
	// everyone; links go on.
	clock = clock.Add(guessDay)
	for range codeSurgeFails - 1 {
		s.recordFailure(codeFailsKey)
	}
	a.call("POST", "/api/v1/sharing/join", oldTok, wrong, 404, nil)
	a.call("POST", "/api/v1/sharing/join", old2, wrong, 429, &e)
	if e.Error.Code != "codes_paused" || !s.paused(&s.codesPaused) {
		t.Fatalf("codes not paused: %q", e.Error.Code)
	}
	a.call("POST", "/api/v1/invite/accept", old2, map[string]string{"link": "https://x.example/p#AAAAAAAA"}, 404, nil)
	clock = clock.Add(surgePause + time.Second)
	a.call("POST", "/api/v1/sharing/join", old2, wrong, 404, nil)

	// linkSurgeFails wrong links stop the public preview; accepting goes on.
	for range linkSurgeFails - 1 {
		s.recordFailure(linkFailsKey)
	}
	a.call("GET", "/api/v1/invite/s/AAAAAAAA", "", nil, 404, nil)
	a.call("GET", "/api/v1/invite/s/AAAAAAAA", "", nil, 429, &e)
	if e.Error.Code != "links_paused" {
		t.Fatalf("previews not paused: %q", e.Error.Code)
	}
	a.call("POST", "/api/v1/invite/accept", old3, map[string]string{"link": "https://x.example/s#AAAAAAAA"}, 404, nil)
}

func TestSurgeAlertPushover(t *testing.T) {
	got := make(chan url.Values, 2)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		got <- r.PostForm
	}))
	defer api.Close()
	creds := filepath.Join(t.TempDir(), "pushover.env")
	os.WriteFile(creds, []byte("# alerts\nPUSHOVER_TOKEN=\"tok\"\nPUSHOVER_USER=usr\n"), 0o600)
	clock := time.Unix(1_800_000_000, 0)
	s := &Server{cfg: Config{Now: func() time.Time { return clock }, PushoverFile: creds}, log: slog.New(slog.DiscardHandler), pushoverURL: api.URL}
	for range codeSurgeFails {
		s.recordFailure(codeFailsKey)
	}
	s.guessFailed(false)
	s.recordFailure(codeFailsKey)
	s.guessFailed(false) // during the pause: no second alert
	s.background.Wait()
	select {
	case f := <-got:
		if f.Get("token") != "tok" || f.Get("user") != "usr" || f.Get("priority") != "1" || !strings.Contains(f.Get("message"), "wrong pairing or invite codes") {
			t.Fatalf("alert %v", f)
		}
	default:
		t.Fatal("no alert sent")
	}
	if len(got) != 0 {
		t.Fatal("alerted twice in one pause")
	}
	clock = clock.Add(surgePause)
	s.cfg.PushoverFile = filepath.Join(t.TempDir(), "missing")
	s.recordFailure(codeFailsKey)
	s.guessFailed(false) // a new pause, but no account: only logged
	s.background.Wait()
	if len(got) != 0 || !s.paused(&s.codesPaused) {
		t.Fatal("alert sent without an account, or no new pause")
	}
}
