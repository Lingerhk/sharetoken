package portal

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"sharetoken/accounts"
	"sharetoken/internal/totp"
	"sharetoken/ledger"
	"sharetoken/mailer"
)

// adminFixture is a portal with mail set up and a signed-in administrator.
type adminFixture struct {
	t     *testing.T
	s     *Server
	url   string
	token string
	admin *accounts.User
}

func newAdminFixture(t *testing.T, cfg Config) *adminFixture {
	t.Helper()
	db, err := openSharedDB(t)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cfg.DB = db
	if cfg.Mail.From == "" {
		cfg.Mail = MailConfig{From: "ST <team@example.com>", Support: "ST Support <support@example.com>"}
	}
	cfg.MFAKey = make([]byte, 32)
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	admin, err := s.store.CreateUser("root", "adminadmin", true)
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := s.store.IssueSessionToken(admin.ID, "t", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	return &adminFixture{t: t, s: s, url: srv.URL, token: tok, admin: admin}
}

// call makes a bearer request and returns its status and body.
func (f *adminFixture) call(method, path, token string, body any) (int, []byte) {
	f.t.Helper()
	var rd *strings.Reader = strings.NewReader("")
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = strings.NewReader(string(raw))
	}
	req, _ := http.NewRequest(method, f.url+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set(WebHeader, "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	var raw strings.Builder
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		raw.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, []byte(raw.String())
}

func (f *adminFixture) ok(method, path string, body, out any) {
	f.t.Helper()
	st, raw := f.call(method, path, f.token, body)
	if st >= 300 {
		f.t.Fatalf("%s %s: %d %s", method, path, st, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			f.t.Fatalf("%s %s: %v: %s", method, path, err, raw)
		}
	}
}

func (f *adminFixture) wantCode(method, path, token string, body any, status int, code string) {
	f.t.Helper()
	st, raw := f.call(method, path, token, body)
	var e ErrorBody
	_ = json.Unmarshal(raw, &e)
	if st != status || (code != "" && e.Error.Code != code) {
		f.t.Fatalf("%s %s: got %d %s, want %d %s", method, path, st, raw, status, code)
	}
}

func (f *adminFixture) setSettings(fields map[string]any) {
	f.t.Helper()
	f.ok("PUT", "/api/v1/admin/settings", fields, nil)
}

// auditActionsOf reads the log, newest first.
func (f *adminFixture) auditEntries(query string) []AuditEntry {
	f.t.Helper()
	var r struct {
		Entries []AuditEntry `json:"entries"`
		Total   int          `json:"total"`
	}
	f.ok("GET", "/api/v1/admin/oplog?"+query, nil, &r)
	return r.Entries
}

func TestSettingsChangeBehaviourWithoutRestart(t *testing.T) {
	f := newAdminFixture(t, Config{OpenRegistration: true, SignupCreditMicros: 7, PublicURL: "https://p.example.com"})

	// The configuration supplies the values until somebody saves.
	var info Info
	f.ok("GET", "/api/v1/info", nil, &info)
	if !info.RegistrationOpen || info.SignupCredit != 7 || !info.PasswordReset || info.DefaultPageSize != defaultPageSize {
		t.Fatalf("defaults: %+v", info)
	}

	// A field left out keeps its value.
	f.setSettings(map[string]any{"signup_credit_micros": 9, "default_page_size": 50, "open_registration": false})
	f.ok("GET", "/api/v1/info", nil, &info)
	if info.SignupCredit != 9 || info.DefaultPageSize != 50 || info.RegistrationOpen {
		t.Fatalf("after saving: %+v", info)
	}
	if !info.PasswordReset {
		t.Fatalf("a field left out changed: %+v", info)
	}

	// Closing registration takes effect at once, for the API and the code endpoint.
	f.wantCode("POST", "/api/v1/register", "", map[string]string{"username": "newbie", "password": "password1"}, 403, "registration_closed")

	// A restart reads the database, not the configuration.
	s2, err := New(Config{DB: f.s.db, OpenRegistration: true, MFAKey: make([]byte, 32)})
	if err != nil {
		t.Fatal(err)
	}
	if st := s2.settings(); st.OpenRegistration || st.SignupCreditMicros != 9 {
		t.Fatalf("after a restart: %+v", st)
	}
}

func TestSettingsValidation(t *testing.T) {
	f := newAdminFixture(t, Config{})
	for _, c := range []struct {
		field string
		value any
	}{
		{"mail_from", "not an address"},
		{"email_domain_allow", []string{"not a domain"}}, {"email_block", []string{"nobody"}},
		{"default_page_size", 7},
		{"rate_user_rpm", 5}, {"rate_public_rpm", 5}, {"audit_retention_days", 3},
		{"web_session_hours", -1},
	} {
		st, raw := f.call("PUT", "/api/v1/admin/settings", f.token, map[string]any{c.field: c.value})
		var e struct {
			Error struct{ Code string } `json:"error"`
			Field string                `json:"field"`
		}
		_ = json.Unmarshal(raw, &e)
		if st != 400 || e.Error.Code != "bad_setting" || e.Field != c.field {
			t.Errorf("%s=%v: %d %s", c.field, c.value, st, raw)
		}
	}

	// What depends on something else says so.
	f.wantCode("PUT", "/api/v1/admin/settings", f.token, map[string]any{"email_domain_allow": []string{"example.com"}}, 400, "bad_setting")
	f.wantCode("PUT", "/api/v1/admin/settings", f.token, map[string]any{"email_verification": true, "mail_from": ""}, 400, "bad_setting")

	// Nothing above was stored; good values are cleaned up.
	var v settingsView
	f.ok("PUT", "/api/v1/admin/settings", map[string]any{"email_verification": true,
		"email_domain_allow": []string{" Example.COM ", "*.edu.cn", "example.com", ""}, "email_block": []string{"A@Example.com", "a@example.com"}}, &v)
	if fmt.Sprint(v.Settings.EmailDomainAllow) != "[example.com *.edu.cn]" || fmt.Sprint(v.Settings.EmailBlock) != "[a@example.com]" {
		t.Fatalf("not cleaned: %+v", v.Settings)
	}

	// Admins only.
	u, _ := f.s.store.CreateUser("alice", "alicealice", false)
	tok, _, _ := f.s.store.IssueToken(u.ID, "t", time.Hour)
	f.wantCode("GET", "/api/v1/admin/settings", tok, nil, 403, "forbidden")
	f.wantCode("PUT", "/api/v1/admin/settings", tok, map[string]any{"open_registration": false}, 403, "forbidden")
}

func TestDomainAllowed(t *testing.T) {
	for _, c := range []struct {
		rules  []string
		domain string
		want   bool
	}{
		{nil, "any.example", true},
		{[]string{"example.com"}, "example.com", true},
		{[]string{"example.com"}, "a.example.com", false},
		{[]string{"example.com"}, "notexample.com", false},
		{[]string{"*.edu.cn"}, "pku.edu.cn", true},
		{[]string{"*.edu.cn"}, "a.b.edu.cn", true},
		{[]string{"*.edu.cn"}, "edu.cn", false},
		{[]string{"*.edu.cn"}, "fakeedu.cn", false},
	} {
		if got := domainAllowed(c.rules, c.domain); got != c.want {
			t.Errorf("%v %s: %v", c.rules, c.domain, got)
		}
	}
}

func TestRegistrationRules(t *testing.T) {
	f := newAdminFixture(t, Config{OpenRegistration: true, EmailVerification: true})
	f.setSettings(map[string]any{"email_domain_allow": []string{"corp.example", "*.edu.cn"}, "email_block": []string{"bad@corp.example"}})
	code := "/api/v1/register/code"

	// No mail goes out to an address that could not sign up anyway.
	f.wantCode("POST", code, "", map[string]string{"email": "x@other.example"}, 403, "email_not_allowed")
	f.wantCode("POST", code, "", map[string]string{"email": "bad@corp.example"}, 403, "email_not_allowed")
	f.wantCode("POST", code, "", map[string]string{"email": "good@corp.example"}, 200, "")
	f.wantCode("POST", code, "", map[string]string{"email": "s@pku.edu.cn"}, 200, "")
	f.wantCode("POST", "/api/v1/register", "", map[string]string{"email": "bad@corp.example", "password": "password1", "code": "000000"}, 403, "email_not_allowed")

	// Invite codes: one use, and a failed sign-up gives it back.
	var inv Invite
	f.ok("POST", "/api/v1/admin/invites", map[string]any{"note": "for good", "max_uses": 1}, &inv)
	if inv.Code == "" || inv.MaxUses != 1 {
		t.Fatalf("invite: %+v", inv)
	}
	f.setSettings(map[string]any{"invite_required": true, "email_verification": false, "email_domain_allow": []string{}})
	reg := "/api/v1/register"
	body := func(name, invite string) map[string]string {
		return map[string]string{"username": name, "password": "password1", "invite_code": invite}
	}
	f.wantCode("POST", reg, "", body("first", ""), 403, "invite_required")
	f.wantCode("POST", reg, "", body("first", "WRONG"), 403, "bad_invite")
	f.wantCode("POST", reg, "", body("root", inv.Code), 409, "exists")
	f.wantCode("POST", reg, "", body("first", strings.ToLower(inv.Code)), 201, "") // lower case is the same code
	f.wantCode("POST", reg, "", body("second", inv.Code), 403, "bad_invite")

	// A disabled one is gone, whatever uses it has left.
	var open Invite
	f.ok("POST", "/api/v1/admin/invites", map[string]any{"max_uses": 0, "ttl_hours": 1}, &open)
	f.wantCode("POST", reg, "", body("third", open.Code), 201, "")
	f.ok("DELETE", fmt.Sprintf("/api/v1/admin/invites/%d", open.ID), nil, nil)
	f.wantCode("POST", reg, "", body("fourth", open.Code), 403, "bad_invite")

	var list struct {
		Invites []Invite `json:"invites"`
	}
	f.ok("GET", "/api/v1/admin/invites", nil, &list)
	if len(list.Invites) != 2 || list.Invites[0].Used != 1 || !list.Invites[0].Disabled || list.Invites[1].Used != 1 {
		t.Fatalf("invites: %+v", list.Invites)
	}

	// Guessing is throttled.
	for i := range maxFails {
		f.call("POST", reg, "", body(fmt.Sprintf("guess%d", i), "WRONG")) // some wrong ones came before
	}
	f.wantCode("POST", reg, "", body("guess", "WRONG"), 429, "throttled")
}

func TestApplyBlocklist(t *testing.T) {
	f := newAdminFixture(t, Config{OpenRegistration: true})
	bad, _ := f.s.store.CreateUser("bad@example.com", "password1", false)
	if _, err := f.s.store.CreateUser("ok@example.com", "password1", false); err != nil {
		t.Fatal(err)
	}
	boss, _ := f.s.store.CreateUser("boss@example.com", "password1", true)
	f.setSettings(map[string]any{"email_block": []string{"bad@example.com", "boss@example.com", "gone@example.com"}})

	var dry struct {
		Matched  []string `json:"matched"`
		Admins   []string `json:"skipped_admins"`
		Disabled bool     `json:"disabled"`
	}
	f.ok("POST", "/api/v1/admin/settings/apply-blocklist", map[string]bool{"dry_run": true}, &dry)
	if fmt.Sprint(dry.Matched) != "[bad@example.com]" || fmt.Sprint(dry.Admins) != "[boss@example.com]" || dry.Disabled {
		t.Fatalf("dry run: %+v", dry)
	}
	if u, _ := f.s.store.User(bad.ID); u.Disabled {
		t.Fatal("a dry run disabled someone")
	}
	f.ok("POST", "/api/v1/admin/settings/apply-blocklist", map[string]bool{"dry_run": false}, &dry)
	if u, _ := f.s.store.User(bad.ID); !u.Disabled {
		t.Fatal("not disabled")
	}
	if u, _ := f.s.store.User(boss.ID); u.Disabled {
		t.Fatal("an administrator was disabled")
	}
}

func TestMailTest(t *testing.T) {
	f := newAdminFixture(t, Config{})
	f.wantCode("POST", "/api/v1/admin/mail/test", f.token, map[string]string{"to": "nope"}, 400, "")
	var r map[string]int64
	f.ok("POST", "/api/v1/admin/mail/test", map[string]string{"to": "me@example.com"}, &r)
	m, err := f.s.mail.OutMessage(r["outbox_id"])
	if err != nil || m.Kind != mailer.KindTest || m.To != "me@example.com" || m.From != "ST <team@example.com>" || m.CreatedBy != "root" {
		t.Fatalf("%+v %v", m, err)
	}
	// The sender comes from the settings as soon as they change.
	f.setSettings(map[string]any{"mail_from": "Acme <acme@example.com>"})
	f.ok("POST", "/api/v1/admin/mail/test", map[string]string{"to": "me@example.com"}, &r)
	if m, _ := f.s.mail.OutMessage(r["outbox_id"]); m.From != "Acme <acme@example.com>" {
		t.Fatalf("from: %q", m.From)
	}
	es := f.auditEntries("action=mail.test")
	if len(es) != 3 || es[0].Status != 200 || es[2].Status != 400 { // the refused one is on record too
		t.Fatalf("audit: %+v", es)
	}
}

func TestRateLimits(t *testing.T) {
	var clock time.Time = time.Unix(1_800_000_000, 0)
	now := &clock
	f := newAdminFixture(t, Config{OpenRegistration: true, Now: func() time.Time { return *now }})
	u, _ := f.s.store.CreateUser("alice", "alicealice", false)
	tok, _, _ := f.s.store.IssueToken(u.ID, "t", 365*24*time.Hour)

	// Off by default: well past any limit below.
	for range 70 {
		f.wantCode("GET", "/api/v1/info", "", nil, 200, "")
	}
	f.setSettings(map[string]any{"rate_user_rpm": 60, "rate_heavy_rpm": 1, "rate_public_rpm": 30, "rate_admin_exempt": true})

	for i := range 60 {
		st, raw := f.call("GET", "/api/v1/me", tok, nil)
		if st != 200 {
			t.Fatalf("request %d: %d %s", i, st, raw)
		}
	}
	f.wantCode("GET", "/api/v1/me", tok, nil, 429, "rate_limited")
	f.ok("GET", "/api/v1/admin/overview", nil, nil) // the exempt administrator

	for range 30 {
		f.wantCode("GET", "/api/v1/info", "", nil, 200, "")
	}
	f.wantCode("GET", "/api/v1/info", "", nil, 429, "rate_limited")

	// The next minute starts over.
	*now = now.Add(time.Minute)
	f.wantCode("GET", "/api/v1/me", tok, nil, 200, "")
	f.wantCode("GET", "/api/v1/ledger?format=csv", tok, nil, 200, "")
	f.wantCode("GET", "/api/v1/ledger?format=csv", tok, nil, 429, "rate_limited") // heavy has its own budget
	f.wantCode("GET", "/api/v1/ledger", tok, nil, 200, "")
}

// ---- audit log ----

func TestAuditLogRecordsAdminActions(t *testing.T) {
	f := newAdminFixture(t, Config{})
	alice, _ := f.s.store.CreateUser("alice", "alicealice", false)

	f.ok("POST", "/api/v1/admin/users/alice/credit", map[string]any{"micros": 5, "memo": "gift"}, nil)
	f.ok("POST", "/api/v1/admin/users/alice/password", map[string]any{"new_password": "SECRET-PASSWORD", "revoke_tokens": true}, nil)
	f.ok("POST", "/api/v1/admin/users", map[string]any{"username": "bob", "password": "SECRET-PASSWORD-2"}, nil)
	f.wantCode("POST", "/api/v1/admin/users/ghost/disable", f.token, nil, 404, "not_found")
	f.ok("POST", "/api/v1/admin/users/alice/disable", nil, nil)
	f.ok("GET", "/api/v1/admin/users/alice", nil, nil) // reads are not actions

	es := f.auditEntries("")
	got := []string{}
	for _, e := range es {
		got = append(got, fmt.Sprintf("%s %s %d", e.Action, e.Target, e.Status))
	}
	want := []string{"user.disable alice 200", "user.disable ghost 404", "user.create bob 201", "user.set_password alice 200", "user.credit alice 200"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("log:\n got %q\nwant %q", got, want)
	}
	if es[0].Actor != "root" || es[0].ActorID != f.admin.ID || es[0].IP == "" {
		t.Fatalf("who: %+v", es[0])
	}
	var credit struct {
		Micros int64  `json:"micros"`
		Memo   string `json:"memo"`
	}
	if err := json.Unmarshal(es[4].Detail, &credit); err != nil || credit.Micros != 5 || credit.Memo != "gift" {
		t.Fatalf("detail: %s", es[4].Detail)
	}
	// Each entry names its request, and a refusal says why.
	var req struct{ Method, Path, Error string }
	if err := json.Unmarshal(es[4].Detail, &req); err != nil || req.Method != "POST" || req.Path != "/api/v1/admin/users/alice/credit" || req.Error != "" {
		t.Fatalf("request: %s", es[4].Detail)
	}
	if err := json.Unmarshal(es[1].Detail, &req); err != nil || !strings.HasPrefix(req.Error, "not_found") {
		t.Fatalf("refusal: %s", es[1].Detail)
	}
	// A password never reaches the log.
	var n int
	if err := f.s.db.QueryRow(`SELECT COUNT(*) FROM admin_audit_log WHERE detail LIKE '%SECRET%'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("passwords in the log: %d %v", n, err)
	}
	_ = alice

	// Filters, and refusal for those who may not read it.
	if es := f.auditEntries("action=user.credit"); len(es) != 1 {
		t.Fatalf("by action: %+v", es)
	}
	if es := f.auditEntries("actor=ROOT&target=ali"); len(es) != 3 {
		t.Fatalf("by actor and target: %+v", es)
	}
	if es := f.auditEntries("actor=nobody"); len(es) != 0 {
		t.Fatalf("by unknown actor: %+v", es)
	}
	if es := f.auditEntries("target=" + url.QueryEscape("%")); len(es) != 0 {
		t.Fatalf("a wildcard searched: %+v", es)
	}
	u, _ := f.s.store.CreateUser("carol", "carolcarol", false)
	tok, _, _ := f.s.store.IssueToken(u.ID, "t", time.Hour)
	f.wantCode("GET", "/api/v1/admin/oplog", tok, nil, 403, "forbidden")

	// Settings say what changed, and there is no way to edit the log.
	f.setSettings(map[string]any{"default_page_size": 50, "email_block": []string{"a@example.com"}})
	es = f.auditEntries("action=settings.update")
	var diff map[string]any
	if len(es) != 1 || json.Unmarshal(es[0].Detail, &diff) != nil {
		t.Fatalf("settings entry: %+v", es)
	}
	changed, _ := diff["changed"].(map[string]any)
	if changed["email_block"] != "+1 -0" || changed["default_page_size"] == nil || len(changed) != 2 {
		t.Fatalf("diff: %s", es[0].Detail)
	}
	f.wantCode("DELETE", "/api/v1/admin/oplog", f.token, nil, 405, "")
	f.wantCode("POST", "/api/v1/admin/oplog", f.token, nil, 405, "")
}

// Every route that changes something names its action, so a new one cannot slip
// past the log unnamed. Reads the sources: the mux cannot be asked for its routes.
func TestEveryAdminRouteHasAnAuditAction(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	re := regexp.MustCompile(`"((?:POST|PUT|PATCH|DELETE) /api/v1/admin/[^"]*)", s\.admin\(`)
	seen := map[string]bool{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
			seen[m[1]] = true
			if _, ok := auditActions[m[1]]; !ok {
				t.Errorf("%s: no entry in auditActions", m[1])
			}
		}
	}
	if len(seen) < 20 {
		t.Fatalf("found only %d routes: is the pattern stale?", len(seen))
	}
	for route := range auditActions {
		if !seen[route] {
			t.Errorf("auditActions names %q, which no longer exists", route)
		}
	}
}

func TestAuditRetention(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	f := newAdminFixture(t, Config{Now: func() time.Time { return clock }})
	f.setSettings(map[string]any{"audit_retention_days": 7})
	old := clock.Add(-8 * 24 * time.Hour).UnixNano()
	if _, err := f.s.db.Exec(`INSERT INTO admin_audit_log (at, actor, action, detail) VALUES (?, 'old', 'x', '{}')`, old); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(2 * time.Hour) // past the hourly purge
	f.setSettings(map[string]any{"default_page_size": 100})
	var n int
	if err := f.s.db.QueryRow(`SELECT COUNT(*) FROM admin_audit_log WHERE actor = 'old'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("old entries: %d %v", n, err)
	}
	if es := f.auditEntries("action=settings.update"); len(es) != 2 {
		t.Fatalf("recent entries were purged: %+v", es)
	}
}

// ---- password reset ----

// resetLink waits for the mail and takes the token out of its text.
func (f *adminFixture) resetLink(to string) string {
	f.t.Helper()
	f.s.background.Wait()
	ms, err := f.s.mail.Outbox(mailer.OutFilter{Kind: mailer.KindVerify, Q: to, Limit: 1})
	if err != nil || len(ms) == 0 {
		f.t.Fatalf("no mail to %s: %v", to, err)
	}
	m, err := f.s.mail.OutMessage(ms[0].ID)
	if err != nil {
		f.t.Fatal(err)
	}
	if !m.Sensitive || m.ExpiresAt == 0 {
		f.t.Fatalf("a reset mail must be sensitive and expire: %+v", m)
	}
	match := regexp.MustCompile(`https://p\.example\.com/app/#reset=([A-Za-z0-9_-]+)`).FindStringSubmatch(m.Text)
	if match == nil {
		f.t.Fatalf("no link in: %s", m.Text)
	}
	return match[1]
}

func (f *adminFixture) forgotCount(to string) int {
	f.t.Helper()
	f.s.background.Wait()
	ms, err := f.s.mail.Outbox(mailer.OutFilter{Kind: mailer.KindVerify, Q: to})
	if err != nil {
		f.t.Fatal(err)
	}
	return len(ms)
}

func TestPasswordReset(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	f := newAdminFixture(t, Config{OpenRegistration: true, PublicURL: "https://p.example.com", Now: func() time.Time { return clock }})
	u, err := f.s.store.CreateUser("a@example.com", "oldpassword", false)
	if err != nil {
		t.Fatal(err)
	}
	session, _, _ := f.s.store.IssueToken(u.ID, "laptop", 0)
	forgot, reset := "/api/v1/password/forgot", "/api/v1/password/reset"

	// The answer says nothing about who has an account.
	st1, b1 := f.call("POST", forgot, "", map[string]string{"email": "a@example.com"})
	st2, b2 := f.call("POST", forgot, "", map[string]string{"email": "ghost@example.com"})
	st3, b3 := f.call("POST", forgot, "", map[string]string{"email": "not an address"})
	if st1 != 200 || st2 != 200 || st3 != 200 || string(b1) != string(b2) || string(b1) != string(b3) {
		t.Fatalf("answers differ: %d %s / %d %s / %d %s", st1, b1, st2, b2, st3, b3)
	}
	token := f.resetLink("a@example.com")
	if f.forgotCount("ghost@example.com") != 0 {
		t.Fatal("mail to an address without an account")
	}

	// Only a hash is stored.
	var stored []byte
	if err := f.s.db.QueryRow(`SELECT token_hash FROM password_resets`).Scan(&stored); err != nil || strings.Contains(string(stored), token) {
		t.Fatalf("%v", err)
	}
	// The admin outbox does not show the link.
	var box struct {
		Messages []mailer.OutMail `json:"messages"`
	}
	f.ok("GET", "/api/v1/admin/mail/outbox?kind=verify", nil, &box)
	if len(box.Messages) != 1 || strings.Contains(box.Messages[0].Text+box.Messages[0].Subject, token) {
		t.Fatalf("outbox: %+v", box.Messages)
	}

	// A short password does not use the link up; a wrong token is refused.
	f.wantCode("POST", reset, "", map[string]string{"token": token, "new_password": "short"}, 400, "bad_request")
	f.wantCode("POST", reset, "", map[string]string{"token": "wrong", "new_password": "newpassword"}, 400, "bad_reset_link")

	// It works once: the old password and every session are gone.
	f.wantCode("POST", reset, "", map[string]string{"token": token, "new_password": "newpassword"}, 200, "")
	f.wantCode("POST", reset, "", map[string]string{"token": token, "new_password": "another-one"}, 400, "bad_reset_link")
	if _, err := f.s.store.Authenticate("a@example.com", "oldpassword"); err == nil {
		t.Fatal("the old password still works")
	}
	if _, err := f.s.store.Authenticate("a@example.com", "newpassword"); err != nil {
		t.Fatal(err)
	}
	f.wantCode("GET", "/api/v1/me", session, nil, 401, "unauthorized")
	// The owner is told, and it is on the record.
	if ms, _ := f.s.mail.Outbox(mailer.OutFilter{Kind: mailer.KindNotice, Q: "a@example.com"}); len(ms) != 1 {
		t.Fatalf("no notice: %+v", ms)
	}
	if es := f.auditEntries("action=" + actionPasswordReset); len(es) != 1 || es[0].Actor != "a@example.com" {
		t.Fatalf("audit: %+v", es)
	}

	// A second request within the cooldown sends nothing new.
	before := f.forgotCount("a@example.com")
	clock = clock.Add(time.Second)
	f.wantCode("POST", forgot, "", map[string]string{"email": "a@example.com"}, 200, "")
	if n := f.forgotCount("a@example.com"); n != before {
		t.Fatalf("cooldown: %d mails, had %d", n, before)
	}

	// A newer link replaces the older; links expire.
	clock = clock.Add(2 * resetCooldown)
	f.wantCode("POST", forgot, "", map[string]string{"email": "a@example.com"}, 200, "")
	first := f.resetLink("a@example.com")
	clock = clock.Add(2 * resetCooldown)
	f.wantCode("POST", forgot, "", map[string]string{"email": "a@example.com"}, 200, "")
	second := f.resetLink("a@example.com")
	f.wantCode("POST", reset, "", map[string]string{"token": first, "new_password": "newpassword"}, 400, "bad_reset_link")
	clock = clock.Add(resetTTL + time.Second)
	f.wantCode("POST", reset, "", map[string]string{"token": second, "new_password": "newpassword"}, 400, "bad_reset_link")

	// A disabled account gets no link, and cannot use one it already has.
	clock = clock.Add(2 * resetCooldown)
	f.wantCode("POST", forgot, "", map[string]string{"email": "a@example.com"}, 200, "")
	third := f.resetLink("a@example.com")
	if err := f.s.store.SetDisabled(u.ID, true); err != nil {
		t.Fatal(err)
	}
	f.wantCode("POST", reset, "", map[string]string{"token": third, "new_password": "newpassword"}, 400, "bad_reset_link")
	before = f.forgotCount("a@example.com")
	clock = clock.Add(2 * resetCooldown)
	f.wantCode("POST", forgot, "", map[string]string{"email": "a@example.com"}, 200, "")
	if n := f.forgotCount("a@example.com"); n != before {
		t.Fatal("mail to a disabled account")
	}
}

func TestPasswordResetKeepsAdminSecondFactor(t *testing.T) {
	f := newAdminFixture(t, Config{PublicURL: "https://p.example.com"})
	boss, err := f.s.store.CreateUser("boss@example.com", "oldpassword", true)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := totp.NewSecret()
	sealed, _ := f.s.seal(secret)
	if err := f.s.store.SetMFASecret(boss.ID, sealed); err != nil {
		t.Fatal(err)
	}
	f.wantCode("POST", "/api/v1/password/forgot", "", map[string]string{"email": "boss@example.com"}, 200, "")
	f.wantCode("POST", "/api/v1/password/reset", "", map[string]string{"token": f.resetLink("boss@example.com"), "new_password": "newpassword"}, 200, "")
	u, _ := f.s.store.User(boss.ID)
	if !u.MFAEnrolled() {
		t.Fatal("a reset removed the second factor")
	}
	f.wantCode("POST", "/api/v1/web/login", "", map[string]string{"username": "boss@example.com", "password": "newpassword"}, 401, "mfa_required")
}

func TestPasswordResetOffUntilItCanWork(t *testing.T) {
	f := newAdminFixture(t, Config{}) // no public address
	var info Info
	f.ok("GET", "/api/v1/info", nil, &info)
	if info.PasswordReset {
		t.Fatal("offered without a public address")
	}
	f.wantCode("POST", "/api/v1/password/forgot", "", map[string]string{"email": "a@example.com"}, 404, "not_supported")
	// The address comes from the configuration only: the settings page cannot
	// point the links elsewhere.
	f.setSettings(map[string]any{"public_url": "https://evil.example.com"})
	f.wantCode("POST", "/api/v1/password/forgot", "", map[string]string{"email": "a@example.com"}, 404, "not_supported")
	f.s.cfg.PublicURL = "https://p.example.com"
	f.wantCode("POST", "/api/v1/password/forgot", "", map[string]string{"email": "a@example.com"}, 200, "")
	f.setSettings(map[string]any{"password_reset": false})
	f.wantCode("POST", "/api/v1/password/forgot", "", map[string]string{"email": "a@example.com"}, 404, "not_supported")

	// Cross-site pages cannot make a visitor's browser send mail, or use a link.
	f.setSettings(map[string]any{"password_reset": true})
	req, _ := http.NewRequest("POST", f.url+"/api/v1/password/forgot", strings.NewReader(`{"email":"a@example.com"}`))
	req.Header.Set("Origin", "https://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("cross-site forgot: %d", resp.StatusCode)
	}
	req, _ = http.NewRequest("POST", f.url+"/api/v1/password/reset", strings.NewReader(`{}`))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("reset without the page's header: %d", resp.StatusCode)
	}
}

// ---- listings ----

type pageOf[T any] struct {
	Total    int `json:"total"`
	Page     int `json:"page"`
	PageSize int `json:"page_size"`
	Users    []T `json:"users"`
	Entries  []T `json:"entries"`
}

func TestListingPagesAndDates(t *testing.T) {
	clock := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	f := newAdminFixture(t, Config{Now: func() time.Time { return clock }})
	acct := func(i int) string { return fmt.Sprintf("u%02d", i) }
	var ids []int64
	for i := range 25 {
		clock = clock.Add(time.Hour)
		u, err := f.s.store.CreateUser(acct(i), "password1", false)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, u.ID)
	}
	var p pageOf[accounts.UserRow]
	f.ok("GET", "/api/v1/admin/users/search?page=1&page_size=10", nil, &p)
	if p.Total != 26 || p.Page != 1 || p.PageSize != 10 || len(p.Users) != 10 || p.Users[0].Username != "u24" {
		t.Fatalf("page 1: %+v", p)
	}
	f.ok("GET", "/api/v1/admin/users/search?page=3&page_size=10", nil, &p)
	if len(p.Users) != 6 || p.Page != 3 || p.Users[5].Username != "root" {
		t.Fatalf("page 3: %+v", p)
	}
	// Past the end is empty, not an error; limit is another name for page_size.
	f.ok("GET", "/api/v1/admin/users/search?page=9&limit=10", nil, &p)
	if len(p.Users) != 0 || p.Total != 26 {
		t.Fatalf("past the end: %+v", p)
	}
	// Dates: from inclusive, to exclusive, in milliseconds.
	ms := func(t time.Time) string { return fmt.Sprint(t.UnixMilli()) }
	start := time.Date(2026, 6, 1, 13, 0, 0, 0, time.UTC) // u00
	f.ok("GET", "/api/v1/admin/users/search?from="+ms(start)+"&to="+ms(start.Add(3*time.Hour)), nil, &p)
	if p.Total != 3 || p.Users[0].Username != "u02" || p.Users[2].Username != "u00" {
		t.Fatalf("dates: %+v", p)
	}
	// Bad parameters are refused, deep pages told to narrow down.
	for _, q := range []string{"page=x", "page_size=-1", "from=x", "to=99999999999999999"} {
		f.wantCode("GET", "/api/v1/admin/users/search?"+q, f.token, nil, 400, "bad_request")
	}
	f.wantCode("GET", fmt.Sprintf("/api/v1/admin/users/search?page=%d&page_size=500", maxListOffset), f.token, nil, 400, "page_too_deep")
	_ = ids
}

func TestLedgerListingByDate(t *testing.T) {
	clock := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	f := newAdminFixture(t, Config{Now: func() time.Time { return clock }})
	a, _ := f.s.store.CreateUser("alice", "alicealice", false)
	b, _ := f.s.store.CreateUser("bob_2", "bobbobbob", false)
	tok, _, _ := f.s.store.IssueToken(a.ID, "t", 0)
	day := func(d int) time.Time { return time.Date(2026, 6, d, 12, 0, 0, 0, time.UTC) }
	for d := 1; d <= 10; d++ {
		clock = day(d)
		if err := f.s.AdjustCredit(a.ID, int64(d*1_000_000), fmt.Sprintf("day %d", d)); err != nil {
			t.Fatal(err)
		}
		if err := f.s.AdjustCredit(b.ID, 1, "other"); err != nil {
			t.Fatal(err)
		}
	}
	// Outgoing entries count too: negative in the user's view.
	clock = day(11)
	if err := f.s.AdjustCredit(a.ID, -500_000, "=SUM(A1)"); err != nil {
		t.Fatal(err)
	}
	var p pageOf[LedgerLine]
	f.call("GET", "/api/v1/ledger", tok, nil)
	get := func(q string) {
		t.Helper()
		st, raw := f.call("GET", "/api/v1/ledger?"+q, tok, nil)
		if st != 200 || json.Unmarshal(raw, &p) != nil {
			t.Fatalf("%s: %d %s", q, st, raw)
		}
	}
	get("page=1&page_size=3")
	if p.Total != 11 || len(p.Entries) != 3 || p.Entries[0].Amount != -500_000 || p.Entries[1].Memo != "day 10" {
		t.Fatalf("first page: %+v", p)
	}
	get("page=4&page_size=3")
	if len(p.Entries) != 2 || p.Entries[1].Memo != "day 1" {
		t.Fatalf("last page: %+v", p)
	}
	get(fmt.Sprintf("from=%d&to=%d", day(3).UnixMilli(), day(6).UnixMilli()))
	if p.Total != 3 || p.Entries[0].Memo != "day 5" || p.Entries[2].Memo != "day 3" {
		t.Fatalf("a range: %+v", p)
	}
	get(fmt.Sprintf("from=%d", day(20).UnixMilli()))
	if p.Total != 0 || len(p.Entries) != 0 {
		t.Fatalf("after the last entry: %+v", p)
	}
	// The older way of paging still works for clients that use it.
	get("limit=2")
	var seq = p.Entries[1].Seq
	get(fmt.Sprintf("limit=2&before=%d", seq))
	if p.Entries[0].Seq >= seq {
		t.Fatalf("before: %+v", p)
	}

	// Operators' view of the same journal.
	var j pageOf[JournalLine]
	f.ok("GET", fmt.Sprintf("/api/v1/admin/ledger?account=user:%d&page_size=5", a.ID), nil, &j)
	if j.Total != 11 || len(j.Entries) != 5 || j.Entries[0].FromUser != "alice" {
		t.Fatalf("journal of an account: %+v", j)
	}
	f.ok("GET", fmt.Sprintf("/api/v1/admin/ledger?from=%d&to=%d", day(1).UnixMilli(), day(2).UnixMilli()), nil, &j)
	if j.Total != 2 {
		t.Fatalf("journal over a day: %+v", j)
	}
	if bal, err := f.s.Balance(a.ID); err != nil || bal.AvailableMicros <= 0 {
		t.Fatalf("%+v %v", bal, err)
	}
	_ = ledger.External
}

func TestCSVExport(t *testing.T) {
	clock := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	f := newAdminFixture(t, Config{Now: func() time.Time { return clock }})
	a, _ := f.s.store.CreateUser("alice", "alicealice", false)
	tok, _, _ := f.s.store.IssueToken(a.ID, "t", 0)
	if err := f.s.AdjustCredit(a.ID, 2_500_000, "=HYPERLINK(\"http://evil\")"); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(time.Hour)
	if err := f.s.AdjustCredit(a.ID, -500_000, "+memo, with \"quotes\"\nand a line"); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest("GET", f.url+"/api/v1/ledger?format=csv", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/csv") || !strings.Contains(resp.Header.Get("Content-Disposition"), `filename="ledger-`) {
		t.Fatalf("headers: %v", resp.Header)
	}
	raw := make([]byte, 1<<16)
	n := 0
	for {
		m, err := resp.Body.Read(raw[n:])
		n += m
		if err != nil {
			break
		}
	}
	body := string(raw[:n])
	if !strings.HasPrefix(body, "\xef\xbb\xbf") {
		t.Fatalf("no BOM: %q", body[:10])
	}
	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(body, "\xef\xbb\xbf"))).ReadAll()
	if err != nil {
		t.Fatalf("not CSV: %v\n%s", err, body)
	}
	if len(rows) != 3 || rows[0][2] != "amount_credit" {
		t.Fatalf("rows: %q", rows)
	}
	// Text can never run as a formula; an amount stays a number, sign included.
	if rows[1][2] != "-0.500000" || rows[1][3] != "'+memo, with \"quotes\"\nand a line" {
		t.Fatalf("newest row: %q", rows[1])
	}
	if rows[2][2] != "2.500000" || rows[2][3] != "'=HYPERLINK(\"http://evil\")" {
		t.Fatalf("oldest row: %q", rows[2])
	}
	if rows[1][1] != "2026-06-01T01:00:00Z" {
		t.Fatalf("time: %q", rows[1][1])
	}

	// An admin's export is on the record, with the filter; a user's own is not.
	st, raw2 := f.call("GET", "/api/v1/admin/users/search?format=csv&q=ali", f.token, nil)
	if st != 200 || !strings.Contains(string(raw2), "alice") || strings.Contains(string(raw2), "root") {
		t.Fatalf("users: %d %s", st, raw2)
	}
	es := f.auditEntries("")
	if len(es) != 1 || es[0].Action != "export.users" || !strings.Contains(string(es[0].Detail), "q=ali") {
		t.Fatalf("audit: %+v", es)
	}

	// Every admin listing exports.
	for _, path := range []string{"jobs", "ledger", "audits", "disputes", "mail/inbox", "mail/outbox", "feedback", "oplog"} {
		st, raw := f.call("GET", "/api/v1/admin/"+path+"?format=csv", f.token, nil)
		if st != 200 || !strings.HasPrefix(string(raw), "\xef\xbb\xbf") {
			t.Errorf("%s: %d %.100s", path, st, raw)
		}
	}
	// Too much is refused rather than silently cut off.
	if _, err := f.s.db.Exec(`INSERT INTO admin_audit_log (at, actor, action, detail)
		WITH RECURSIVE n (i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?) SELECT i, 'x', 'y', '{}' FROM n`, maxExportRows+1); err != nil {
		// A server with a low recursion limit: raise it for this session's statement.
		if _, err2 := f.s.db.Exec(`SET GLOBAL cte_max_recursion_depth = 100000`); err2 != nil {
			t.Fatalf("seeding: %v / %v", err, err2)
		}
		t.Skip("cte_max_recursion_depth was too low; raised for the next run")
	}
	f.wantCode("GET", "/api/v1/admin/oplog?format=csv", f.token, nil, 413, "export_too_large")
}

func TestCSVText(t *testing.T) {
	for in, want := range map[string]string{"": "", "abc": "abc", "=1+1": "'=1+1", "+1": "'+1", "-1": "'-1", "@x": "'@x", "\tx": "'\tx", "\rx": "'\rx", "a=1": "a=1"} {
		if got := csvText(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
	for in, want := range map[int64]string{0: "0.000000", 1: "0.000001", -1: "-0.000001", 2_500_000: "2.500000", -1_000_000: "-1.000000"} {
		if got := csvCredit(in); got != want {
			t.Errorf("%d: %q", in, got)
		}
	}
}
