package portal

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"sharetoken/internal/accounts"
	"sharetoken/internal/coordinator"
	"sharetoken/internal/ledger"
	"sharetoken/internal/mysqldb"
	"sharetoken/internal/p2p"
	"sharetoken/internal/protocol"
)

// web is a browser-like client: it keeps the session cookie and sends the
// header the portal's pages send.
type web struct {
	t      *testing.T
	url    string
	cookie string
	hdr    map[string]string // extra headers (Origin, Sec-Fetch-Site…)
	noWeb  bool              // omit X-Sharetoken-Web
}

func (c *web) do(method, path string, body any) (*http.Response, json.RawMessage) {
	c.t.Helper()
	var rd io.Reader = bytes.NewReader(nil)
	if body != nil {
		data, _ := json.Marshal(body)
		rd = bytes.NewReader(data)
	}
	req, _ := http.NewRequest(method, c.url+path, rd)
	if !c.noWeb {
		req.Header.Set(WebHeader, "1")
	}
	if c.cookie != "" {
		req.Header.Set("Cookie", SessionCookie+"="+c.cookie)
	}
	for k, v := range c.hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, raw
}

func (c *web) call(method, path string, body any, want int, out any) *http.Response {
	c.t.Helper()
	resp, raw := c.do(method, path, body)
	if resp.StatusCode != want {
		c.t.Fatalf("%s %s: status %d, want %d: %s", method, path, resp.StatusCode, want, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			c.t.Fatalf("%s %s: %v: %s", method, path, err, raw)
		}
	}
	return resp
}

func sessionCookie(t *testing.T, resp *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == SessionCookie {
			return c
		}
	}
	t.Fatalf("no %s cookie in %v", SessionCookie, resp.Header)
	return nil
}

func TestWebSessionsAndCSRF(t *testing.T) {
	clock := time.Now()
	s, a := newPortal(t, Config{OpenRegistration: true, SignupCreditMicros: 1_000_000, WebSessionTTL: 2 * time.Hour, Now: func() time.Time { return clock }})
	c := &web{t: t, url: a.url}
	const alice = "alice@example.com"

	// Login CSRF: the sign-in endpoints refuse requests without the page header.
	code := sendCode(t, s, c, alice)
	(&web{t: t, url: a.url, noWeb: true}).call("POST", "/api/v1/web/auth/verify", map[string]string{"email": alice, "code": code}, 403, nil)

	var reg struct{ User accounts.User }
	resp := c.call("POST", "/api/v1/web/auth/verify", map[string]string{"email": alice, "code": code}, 201, &reg)
	ck := sessionCookie(t, resp)
	if !ck.HttpOnly || ck.SameSite != http.SameSiteStrictMode || ck.Path != "/" || ck.MaxAge != 7200 || ck.Value == "" {
		t.Fatalf("session cookie attributes: %+v", ck)
	}
	if ck.Secure {
		t.Fatal("cookie must not be Secure on plain HTTP (it would never be sent back)")
	}
	if strings.Contains(string(mustJSON(reg)), ck.Value) {
		t.Fatal("the session secret must not appear in the response body")
	}
	c.cookie = ck.Value

	var me Me
	c.call("GET", "/api/v1/me", nil, 200, &me)
	if me.User.Username != alice || me.Balance.AvailableMicros != 1_000_000 {
		t.Fatalf("me: %+v", me)
	}

	// The cookie alone is never enough: a cross-site request lacks the header.
	cross := &web{t: t, url: a.url, cookie: ck.Value, noWeb: true}
	cross.call("GET", "/api/v1/me", nil, 403, nil)
	cross.call("POST", "/api/v1/tokens", map[string]any{"name": "x"}, 403, nil)
	// A foreign Origin or Sec-Fetch-Site is refused even with the header.
	for _, hdr := range []map[string]string{
		{"Origin": "https://evil.example"},
		{"Origin": "http://evil.example"},
		{"Sec-Fetch-Site": "cross-site"},
		{"Origin": "null"},
	} {
		(&web{t: t, url: a.url, cookie: ck.Value, hdr: hdr}).call("GET", "/api/v1/me", nil, 403, nil)
	}
	// The portal's own origin passes.
	(&web{t: t, url: a.url, cookie: ck.Value, hdr: map[string]string{"Origin": a.url, "Sec-Fetch-Site": "same-origin"}}).call("GET", "/api/v1/me", nil, 200, nil)
	// A bearer token never falls back to the cookie and is unaffected by the header.
	(&web{t: t, url: a.url, noWeb: true, hdr: map[string]string{"Authorization": "Bearer st_bogus"}}).call("GET", "/api/v1/me", nil, 401, nil)

	// The session shows up as a token and is marked current.
	var tl struct {
		Tokens  []accounts.Token
		Current int64
	}
	c.call("GET", "/api/v1/tokens", nil, 200, &tl)
	if len(tl.Tokens) != 1 || tl.Tokens[0].Name != webTokenName || tl.Tokens[0].ID != tl.Current || tl.Tokens[0].ExpiresAt == 0 {
		t.Fatalf("tokens: %+v current %d", tl.Tokens, tl.Current)
	}

	// Logout revokes the session and clears the cookie.
	resp = c.call("POST", "/api/v1/web/logout", nil, 200, nil)
	if ck := sessionCookie(t, resp); ck.Value != "" || ck.MaxAge != -1 {
		t.Fatalf("logout cookie: %+v", ck)
	}
	c.call("GET", "/api/v1/me", nil, 401, nil)

	// Sign in again: the same account.
	clock = clock.Add(2 * time.Minute)
	c.call("POST", "/api/v1/web/auth/verify", map[string]string{"email": alice, "code": otherCode(code)}, 401, nil)
	resp = webSignIn(t, s, c, alice)
	if resp.StatusCode != 200 {
		t.Fatalf("second sign-in: %d", resp.StatusCode)
	}
	c.call("GET", "/api/v1/me", nil, 200, nil)

	// Device rename is the owner's.
	k, _ := p2p.NewKeys()
	ts := time.Now().UnixNano()
	c.call("POST", "/api/v1/devices", BindRequest{Peer: k.ID(), Name: "old", UnixNano: ts, Sig: accounts.SignBind(k.Signer, alice, k.ID(), ts)}, 200, nil)
	c.call("PATCH", "/api/v1/devices/"+k.ID(), map[string]string{"name": "new"}, 200, nil)
	c.call("PATCH", "/api/v1/devices/"+k.ID(), map[string]string{"name": ""}, 400, nil)
	c.call("PATCH", "/api/v1/devices/12D3KooWunknown", map[string]string{"name": "x"}, 404, nil)
	var dl struct{ Devices []accounts.Device }
	c.call("GET", "/api/v1/devices", nil, 200, &dl)
	if len(dl.Devices) != 1 || dl.Devices[0].Name != "new" {
		t.Fatalf("devices: %+v", dl.Devices)
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func TestSecureCookieOverTLS(t *testing.T) {
	db, err := openSharedDB(t)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err := New(Config{DB: db, OpenRegistration: true, Mail: testMail})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(s.Handler())
	t.Cleanup(srv.Close)
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/auth/code", strings.NewReader(`{"email":"alice@example.com"}`))
	if resp, err := srv.Client().Do(req); err != nil || resp.StatusCode != 200 {
		t.Fatalf("code: %v %v", resp, err)
	}
	req, _ = http.NewRequest("POST", srv.URL+"/api/v1/web/auth/verify", strings.NewReader(`{"email":"alice@example.com","code":"`+mailedCode(t, s, "alice@example.com")+`"}`))
	req.Header.Set(WebHeader, "1")
	req.Header.Set("Origin", srv.URL)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if ck := sessionCookie(t, resp); !ck.Secure {
		t.Fatalf("cookie over TLS must be Secure: %+v", ck)
	}
	if hsts := resp.Header.Get("Strict-Transport-Security"); hsts == "" {
		t.Fatal("no HSTS header over TLS")
	}
	// A page that is served over TLS but claims a plain-http origin is cross-site.
	req, _ = http.NewRequest("GET", srv.URL+"/api/v1/me", nil)
	req.Header.Set(WebHeader, "1")
	req.Header.Set("Origin", strings.Replace(srv.URL, "https://", "http://", 1))
	req.AddCookie(sessionCookie(t, resp))
	resp2, _ := srv.Client().Do(req)
	resp2.Body.Close()
	if resp2.StatusCode != 403 {
		t.Fatalf("http origin against https portal: status %d", resp2.StatusCode)
	}
}

func TestPagesAndSecurityHeaders(t *testing.T) {
	_, a := newPortal(t, Config{})
	get := func(path string) (*http.Response, string) {
		t.Helper()
		resp, err := http.Get(a.url + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	for _, tc := range []struct {
		path, ctype, contains string
		status                int
	}{
		{"/", "text/html", "site.js", 200},
		{"/app/", "text/html", "user.js", 200},
		{"/login", "text/html", "user.js", 200}, // the client follows the redirect to /app/
		{"/changelog/", "text/html", "changelog.js", 200},
		{"/changelog", "text/html", "changelog.js", 200}, // redirects to /changelog/
		{"/static/changelog.js", "javascript", "THEME.toggle", 200},
		{"/static/changelog.html", "", "", 404},
		{"/static/site.css", "text/css", ":root", 200},
		{"/static/site.js", "javascript", "/api/v1/info", 200},
		{"/static/favicon.svg", "image/svg", "<svg", 200},
		{"/admin/", "text/html", "admin.js", 200},
		{"/static/app.css", "text/css", ":root", 200},
		{"/static/common.js", "javascript", "X-Sharetoken-Web", 200},
		{"/static/admin.js", "javascript", "/api/v1/admin/", 200},
		{"/static/index.html", "", "", 404},
		{"/static/nope.js", "", "", 404},
		{"/static/i18n.js", "javascript", "const I18N", 200},
		{"/static/i18n.css", "text/css", ".lang-menu", 200},
		{"/static/theme.js", "javascript", "const THEME", 200},
		{"/static/icons.js", "javascript", "const ICONS", 200},
		{"/static/i18n/site/en.json", "application/json", "ShareToken", 200},
		{"/static/i18n/app/en.json", "application/json", "Log in", 200},
		{"/static/i18n/app/nope.json", "", "", 404},
		{"/static/i18n/nope/en.json", "", "", 404},
		{"/static/i18n/site/..%2f..%2findex.html", "", "", 400}, // no way out of the dictionaries
		{"/static/", "", "", 404},
		{"/nope", "", "", 404},
	} {
		resp, body := get(tc.path)
		if resp.StatusCode != tc.status {
			t.Fatalf("%s: status %d, want %d", tc.path, resp.StatusCode, tc.status)
		}
		if tc.ctype != "" && !strings.Contains(resp.Header.Get("Content-Type"), tc.ctype) {
			t.Fatalf("%s: content-type %q", tc.path, resp.Header.Get("Content-Type"))
		}
		if tc.contains != "" && !strings.Contains(body, tc.contains) {
			t.Fatalf("%s: body lacks %q", tc.path, tc.contains)
		}
		for h, want := range map[string]string{
			"Content-Security-Policy": "script-src 'self'",
			"X-Content-Type-Options":  "nosniff",
			"X-Frame-Options":         "DENY",
			"Referrer-Policy":         "no-referrer",
		} {
			if got := resp.Header.Get(h); !strings.Contains(got, want) {
				t.Fatalf("%s: header %s = %q, want %q", tc.path, h, got, want)
			}
		}
		if strings.Contains(body, "<script>") || strings.Contains(body, "onclick=") || strings.Contains(body, "style=") {
			t.Fatalf("%s: inline code would violate the CSP", tc.path)
		}
	}
	// /admin redirects to /admin/ so relative paths resolve.
	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, _ := cl.Get(a.url + "/admin")
	resp.Body.Close()
	if resp.StatusCode != 301 || resp.Header.Get("Location") != "/admin/" {
		t.Fatalf("/admin: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	for _, path := range []string{"/app", "/login"} {
		resp, _ := cl.Get(a.url + path)
		resp.Body.Close()
		if resp.StatusCode != 302 || resp.Header.Get("Location") != "/app/" {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	// The pages never embed data: the HTML is static and the API is JSON only.
	resp, body := get("/api/v1/info")
	if resp.StatusCode != 200 || !strings.Contains(body, `"registration_open":false`) {
		t.Fatalf("info: %d %s", resp.StatusCode, body)
	}
}

func TestRegistrationIsRateLimited(t *testing.T) {
	clock := time.Now()
	s, a := newPortal(t, Config{OpenRegistration: true, Now: func() time.Time { return clock }})
	for i := 0; i < maxFails; i++ {
		clientSignIn(t, s, a.url, fmt.Sprintf("user%d@example.com", i))
	}
	c := &web{t: t, url: a.url, noWeb: true}
	code := sendCode(t, s, c, "onemore@example.com")
	c.wantErr("POST", "/api/v1/auth/verify", map[string]string{"email": "onemore@example.com", "code": code}, 429, "throttled")
	// Signing in to an account is not affected by sign-up throttling.
	clock = clock.Add(2 * time.Minute)
	if res := clientSignIn(t, s, a.url, "user0@example.com"); res.Created {
		t.Fatalf("%+v", res)
	}
}

func TestAdminConsoleEndpoints(t *testing.T) {
	s, a := newPortal(t, Config{OpenRegistration: true, SignupCreditMicros: 2_000_000})
	root, err := s.store.CreateUser("root", true)
	if err != nil {
		t.Fatal(err)
	}
	rootTok, _, _ := s.Store().IssueSessionToken(root.ID, "cli", 0, true)
	admin := &web{t: t, url: a.url, noWeb: true, hdr: map[string]string{"Authorization": "Bearer " + rootTok}}

	aliceU, aliceTok := testUser(t, s, "alice")
	testUser(t, s, "bob_2")
	user := &web{t: t, url: a.url, noWeb: true, hdr: map[string]string{"Authorization": "Bearer " + aliceTok}}

	// Everything under /admin is admin-only.
	for _, ep := range []struct{ m, p string }{
		{"GET", "/api/v1/admin/users/search"}, {"POST", "/api/v1/admin/users/alice/admin"},
		{"GET", "/api/v1/admin/users/alice/tokens"}, {"POST", "/api/v1/admin/users/alice/tokens/revoke"}, {"GET", "/api/v1/admin/jobs"},
		{"GET", "/api/v1/admin/nodes"}, {"GET", "/api/v1/admin/audits"}, {"GET", "/api/v1/admin/audit-calibrations"}, {"GET", "/api/v1/admin/disputes"}, {"GET", "/api/v1/admin/ledger"},
		{"GET", "/api/v1/admin/accounts"}, {"PATCH", "/api/v1/admin/relays/1"}, {"POST", "/api/v1/admin/disputes/x/resolve"},
	} {
		user.call(ep.m, ep.p, map[string]any{}, 403, nil)
	}

	// Search with totals, balances and wildcard escaping.
	var sr struct {
		Users []accounts.UserRow
		Total int64
	}
	admin.call("GET", "/api/v1/admin/users/search?q=_2", nil, 200, &sr)
	if sr.Total != 1 || len(sr.Users) != 1 || sr.Users[0].Username != "bob_2" || sr.Users[0].AvailableMicros != 2_000_000 || sr.Users[0].Tokens != 1 {
		t.Fatalf("search _2: %+v", sr)
	}
	admin.call("GET", "/api/v1/admin/users/search?q=%25", nil, 200, &sr) // a literal % matches nothing
	if sr.Total != 0 {
		t.Fatalf("search %%: %+v", sr)
	}
	admin.call("GET", "/api/v1/admin/users/search?limit=2", nil, 200, &sr)
	if sr.Total != 3 || len(sr.Users) != 2 || sr.Users[0].Username != "bob_2" {
		t.Fatalf("search all: %+v", sr)
	}

	// Admin rights: grant, and never revoke your own.
	admin.call("POST", "/api/v1/admin/users/alice/admin", map[string]bool{"is_admin": true}, 200, nil)
	admin.call("POST", "/api/v1/admin/users/root/admin", map[string]bool{"is_admin": false}, 400, nil)
	// A promotion does not upgrade the sessions she already has: admin
	// access needs a fresh login with two-factor authentication.
	var denied ErrorBody
	user.call("GET", "/api/v1/admin/overview", nil, 403, &denied)
	if denied.Error.Code != "mfa_required" {
		t.Fatalf("promoted user's old session: %+v", denied)
	}
	admin.call("POST", "/api/v1/admin/users/alice/admin", map[string]bool{"is_admin": false}, 200, nil)
	user.call("GET", "/api/v1/admin/overview", nil, 403, &denied)
	if denied.Error.Code != "forbidden" {
		t.Fatalf("demoted user: %+v", denied)
	}

	// Tokens of a user: list, revoke one, revoke all.
	var tl struct{ Tokens []accounts.Token }
	admin.call("GET", "/api/v1/admin/users/alice/tokens", nil, 200, &tl)
	if len(tl.Tokens) != 1 {
		t.Fatalf("alice tokens: %+v", tl.Tokens)
	}
	admin.call("DELETE", fmt.Sprintf("/api/v1/admin/users/bob_2/tokens/%d", tl.Tokens[0].ID), nil, 404, nil) // not bob's
	admin.call("DELETE", fmt.Sprintf("/api/v1/admin/users/alice/tokens/%d", tl.Tokens[0].ID), nil, 200, nil)
	user.call("GET", "/api/v1/me", nil, 401, nil)
	newTok := func() string {
		tok, _, err := s.Store().IssueSessionToken(aliceU.ID, "login", 0, false)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	user.hdr["Authorization"] = "Bearer " + newTok()
	var ra struct {
		TokensRevoked int64 `json:"tokens_revoked"`
	}
	admin.call("POST", "/api/v1/admin/users/alice/tokens/revoke", nil, 200, &ra)
	if ra.TokensRevoked != 1 {
		t.Fatalf("revoke all: %d", ra.TokensRevoked)
	}
	user.call("GET", "/api/v1/me", nil, 401, nil)
	user.hdr["Authorization"] = "Bearer " + newTok()

	// Devices: admin unbinds; detail lists tokens too.
	k, _ := p2p.NewKeys()
	ts := time.Now().UnixNano()
	user.call("POST", "/api/v1/devices", BindRequest{Peer: k.ID(), Name: "box", UnixNano: ts, Sig: accounts.SignBind(k.Signer, "alice", k.ID(), ts)}, 200, nil)
	var detail struct {
		Me
		Tokens []accounts.Token
	}
	admin.call("GET", "/api/v1/admin/users/alice", nil, 200, &detail)
	if len(detail.Devices) != 1 || len(detail.Tokens) != 1 {
		t.Fatalf("detail: %+v", detail)
	}
	admin.call("DELETE", "/api/v1/admin/users/bob_2/devices/"+k.ID(), nil, 404, nil)
	admin.call("DELETE", "/api/v1/admin/users/alice/devices/"+k.ID(), nil, 200, nil)
	admin.call("GET", "/api/v1/admin/users/alice/ledger", nil, 200, &struct{}{})
	admin.call("GET", "/api/v1/admin/users/alice/jobs?role=buy", nil, 200, &struct{}{})
	admin.call("GET", "/api/v1/admin/users/nobody/ledger", nil, 404, nil)

	// Relays: edit name/region; bad lengths are refused.
	var rl accounts.Relay
	admin.call("POST", "/api/v1/admin/relays", map[string]string{"addr": "/ip4/1.2.3.4/tcp/1/p2p/" + k.ID(), "name": "r1"}, 201, &rl)
	admin.call("PATCH", fmt.Sprintf("/api/v1/admin/relays/%d", rl.ID), map[string]string{"name": "edge", "region": "sg"}, 200, nil)
	admin.call("PATCH", fmt.Sprintf("/api/v1/admin/relays/%d", rl.ID), map[string]string{"name": strings.Repeat("x", 65)}, 400, nil)
	admin.call("PATCH", "/api/v1/admin/relays/999", map[string]string{"name": "x"}, 404, nil)
	var rls struct{ Relays []accounts.Relay }
	admin.call("GET", "/api/v1/admin/relays", nil, 200, &rls)
	if len(rls.Relays) != 1 || rls.Relays[0].Name != "edge" || rls.Relays[0].Region != "sg" {
		t.Fatalf("relays: %+v", rls.Relays)
	}

	// Read-only coordinator views are empty but well-formed; filters are validated.
	admin.call("GET", "/api/v1/admin/jobs?status=bogus", nil, 400, nil)
	admin.call("GET", "/api/v1/admin/jobs?user=nobody", nil, 404, nil)
	var jl struct{ Jobs []coordinator.AdminJob }
	admin.call("GET", "/api/v1/admin/jobs?status=final&user=alice", nil, 200, &jl)
	admin.call("GET", "/api/v1/admin/nodes", nil, 200, &struct{}{})
	admin.call("GET", "/api/v1/admin/audits", nil, 200, &struct{}{})
	var cal struct {
		Calibrations []coordinator.AuditCalibration
	}
	admin.call("GET", "/api/v1/admin/audit-calibrations", nil, 200, &cal)
	if cal.Calibrations == nil {
		t.Fatal("calibrations: want an empty list, not null")
	}
	admin.call("GET", "/api/v1/admin/disputes", nil, 200, &struct{}{})

	// Ledger journal with user names, and account balances.
	var jr struct{ Entries []JournalLine }
	admin.call("GET", "/api/v1/admin/ledger?limit=2", nil, 200, &jr)
	if len(jr.Entries) != 2 || jr.Entries[0].ToUser != "bob_2" || jr.Entries[0].Debit != ledger.External || jr.Entries[1].ToUser != "alice" {
		t.Fatalf("journal: %+v", jr.Entries)
	}
	admin.call("GET", "/api/v1/admin/ledger?account=user:2&before="+fmt.Sprint(jr.Entries[0].Seq+1), nil, 200, &jr)
	if len(jr.Entries) != 1 || jr.Entries[0].Credit != "user:2" {
		t.Fatalf("journal filtered: %+v", jr.Entries)
	}
	var ar struct{ Accounts []AccountRow }
	admin.call("GET", "/api/v1/admin/accounts", nil, 200, &ar)
	if len(ar.Accounts) != 3 || ar.Accounts[0].Account != ledger.External || ar.Accounts[0].Balance != -4_000_000 || ar.Accounts[1].User != "alice" {
		t.Fatalf("accounts: %+v", ar.Accounts)
	}

	var o Overview
	admin.call("GET", "/api/v1/admin/overview", nil, 200, &o)
	if o.Users != 3 || o.Relays != 1 || o.RelaysEnabled != 1 || o.RelaysOK != 0 || o.Jobs == nil || !o.BooksBalance {
		t.Fatalf("overview: %+v", o)
	}
}

// A disputed job is resolved from the portal with the same effect as the
// coordinator's own ResolveDispute: refund returns the hold to the buyer and
// charges the node; pay captures it minus the recorded fee.
func TestResolveDisputeFromPortal(t *testing.T) {
	s, a := newPortal(t, Config{})
	root, _ := s.store.CreateUser("root", true)
	rootTok, _, _ := s.Store().IssueSessionToken(root.ID, "cli", 0, true)
	admin := &web{t: t, url: a.url, noWeb: true, hdr: map[string]string{"Authorization": "Bearer " + rootTok}}

	buyer, node := "12D3KooWbuyer", "12D3KooWnode"
	seed := func(jobID string) {
		t.Helper()
		tk, _ := json.Marshal(protocol.Ticket{JobID: jobID, Buyer: buyer, Node: node, Model: "m"})
		if err := s.ledgerTx(func(tx *sql.Tx, led *ledger.Tx) error {
			if err := led.Deposit("user:1", 1000, "seed"); err != nil {
				return err
			}
			if err := led.Hold(jobID, "user:1", 1000); err != nil {
				return err
			}
			_, err := tx.Exec(`INSERT INTO jobs (job_id, buyer, node, buyer_acct, node_acct, model, status, ticket, cost, dispute_reason, created_at, release_at)
				VALUES (?, ?, ?, 'user:1', 'user:2', 'm', 'disputed', ?, 1000, 'inflated', ?, 0)`, jobID, buyer, node, tk, time.Now().UnixNano())
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec("INSERT INTO meta (`key`, value) VALUES ('fee_bps', '1000')"); err != nil {
		t.Fatal(err)
	}
	seed("job-refund")
	seed("job-pay")

	var dl struct{ Disputes []coordinator.DisputeInfo }
	admin.call("GET", "/api/v1/admin/disputes", nil, 200, &dl)
	if len(dl.Disputes) != 2 || dl.Disputes[0].Status != coordinator.StatusDisputed || dl.Disputes[0].BuyerAcct != "user:1" {
		t.Fatalf("disputes: %+v", dl.Disputes)
	}
	admin.call("POST", "/api/v1/admin/disputes/job-refund/resolve", map[string]string{"decision": "maybe"}, 400, nil)
	admin.call("POST", "/api/v1/admin/disputes/nope/resolve", map[string]string{"decision": "pay"}, 404, nil)
	admin.call("POST", "/api/v1/admin/disputes/job-refund/resolve", map[string]string{"decision": "refund"}, 200, nil)
	admin.call("POST", "/api/v1/admin/disputes/job-refund/resolve", map[string]string{"decision": "refund"}, 409, nil)
	admin.call("POST", "/api/v1/admin/disputes/job-pay/resolve", map[string]string{"decision": "pay"}, 200, nil)

	b1, _ := s.Balance(1)
	b2, _ := s.Balance(2)
	if b1.AvailableMicros != 1000 || b1.HeldMicros != 0 || b2.AvailableMicros != 900 {
		t.Fatalf("after resolution: buyer %+v node %+v", b1, b2)
	}
	var jl struct{ Jobs []coordinator.AdminJob }
	admin.call("GET", "/api/v1/admin/jobs", nil, 200, &jl)
	st := map[string]coordinator.Status{}
	for _, j := range jl.Jobs {
		st[j.JobID] = j.Status
	}
	if st["job-refund"] != coordinator.StatusRefunded || st["job-pay"] != coordinator.StatusFinal {
		t.Fatalf("jobs: %+v", st)
	}
	var nl struct {
		Nodes []struct {
			coordinator.NodeStats
			User string
		}
	}
	admin.call("GET", "/api/v1/admin/nodes", nil, 200, &nl)
	if len(nl.Nodes) != 1 || nl.Nodes[0].DisputesLost != 1 || nl.Nodes[0].Successes != 1 {
		t.Fatalf("nodes: %+v", nl.Nodes)
	}
	var o Overview
	admin.call("GET", "/api/v1/admin/overview", nil, 200, &o)
	if !o.BooksBalance || o.PlatformFees != 100 || o.FeeBps != 1000 || o.Jobs["final"] != 1 || o.Jobs["refunded"] != 1 {
		t.Fatalf("overview: %+v", o)
	}
}

// The coordinator holds the database's write lock most of the time under
// load. Reads must not wait for it, and a write that does time out must come
// back as 503, not 500.
func TestReadsDoNotNeedWriteLock(t *testing.T) {
	dsn, err := mysqldb.TestDSN()
	if err != nil {
		t.Fatal(err)
	}
	// The portal's own connection, with a short lock wait so the test is quick.
	db, err := mysqldb.OpenWith(dsn, mysqldb.Options{LockWaitTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err := New(Config{DB: db, OpenRegistration: true, SignupCreditMicros: 5_000_000, Mail: testMail})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	c := &web{t: t, url: srv.URL, hdr: map[string]string{"Origin": srv.URL}}
	webSignIn(t, s, c, "alice@example.com")
	c.call("POST", "/api/v1/tokens", map[string]any{"name": "first"}, 201, nil)

	// Another process (the coordinator) takes and keeps the write lock.
	holder, err := mysqldb.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	lock, err := mysqldb.Begin(holder)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()

	start := time.Now()
	var me struct {
		Balance struct {
			AvailableMicros int64 `json:"available_micros"`
		} `json:"balance"`
	}
	c.call("GET", "/api/v1/me", nil, 200, &me)
	for _, p := range []string{"/api/v1/ledger", "/api/v1/jobs", "/api/v1/tokens", "/api/v1/devices", "/api/v1/info"} {
		c.call("GET", p, nil, 200, nil)
	}
	if me.Balance.AvailableMicros != 5_000_000 {
		t.Fatalf("balance %d", me.Balance.AvailableMicros)
	}
	// 7 reads + at most one bounded last_used_at touch: well under the sum of busy timeouts.
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Fatalf("reads waited for the write lock: %v", d)
	}
	resp := c.call("POST", "/api/v1/tokens", map[string]any{"name": "second"}, 503, nil)
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("503 without Retry-After")
	}
	if err := lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	c.call("POST", "/api/v1/tokens", map[string]any{"name": "second"}, 201, nil)
}

// The network page shows the coordinator's latency to each relay and lists
// working relays fastest first.
func TestNetworkShowsRelayLatency(t *testing.T) {
	db, err := openSharedDB(t)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err := New(Config{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO meta (`key`, value) VALUES ('coordinator', 'coord')"); err != nil {
		t.Fatal(err)
	}
	seed := func(addr string, ok bool, rtt int64) {
		if _, err := db.Exec(`INSERT INTO relays (addr, name) VALUES (?, ?)`, addr, addr); err != nil {
			t.Fatal(err)
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := accounts.SetRelayStatus(tx, addr, ok, "", rtt, "", time.Now()); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	seed("slow", true, 180)
	seed("down", false, 0)
	seed("fast", true, 12)
	seed("unmeasured", true, 0)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	c := &web{t: t, url: srv.URL}
	var n struct {
		Relays []struct {
			Addr string `json:"addr"`
			OK   bool   `json:"ok"`
			RTT  int64  `json:"rtt_ms"`
		} `json:"relays"`
	}
	c.call("GET", "/api/v1/network", nil, 200, &n)
	var order []string
	for _, r := range n.Relays {
		order = append(order, r.Addr)
	}
	if strings.Join(order, ",") != "fast,slow,unmeasured,down" || n.Relays[0].RTT != 12 || n.Relays[2].RTT != 0 {
		t.Fatalf("relays: %+v", n.Relays)
	}
	var rls struct {
		Relays []accounts.Relay `json:"relays"`
	}
	if err := json.Unmarshal(mustJSON(map[string]any{"relays": mustRelays(t, s)}), &rls); err != nil || rls.Relays[0].RTTMillis == 0 && rls.Relays[2].RTTMillis == 0 {
		t.Fatalf("admin relay rows carry rtt_ms: %+v %v", rls.Relays, err)
	}
}

func mustRelays(t *testing.T, s *Server) []accounts.Relay {
	t.Helper()
	rs, err := s.store.Relays(false)
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

// Account names are email addresses; the admin console's URLs carry them,
// '@' and '+' included, and admins create accounts by address only.
func TestAdminURLsWithEmail(t *testing.T) {
	s, a := newPortal(t, Config{OpenRegistration: true, SignupCreditMicros: 7})
	res := clientSignIn(t, s, a.url, " Alice+x@Example.com ")
	if res.User.Username != "alice+x@example.com" {
		t.Fatalf("%+v", res)
	}
	var m Me
	a.call("GET", "/api/v1/me", res.Token, nil, 200, &m)
	if m.Balance.AvailableMicros != 7 {
		t.Fatalf("me: %+v", m)
	}
	root, err := s.store.CreateUser("root", true)
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := s.Store().IssueSessionToken(root.ID, "t", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	admin := &web{t: t, url: a.url, noWeb: true, hdr: map[string]string{"Authorization": "Bearer " + tok}}
	admin.call("GET", "/api/v1/admin/users/"+url.PathEscape("alice+x@example.com"), nil, 200, nil)
	admin.call("POST", "/api/v1/admin/users/"+url.PathEscape("alice+x@example.com")+"/disable", nil, 200, nil)
	a.call("GET", "/api/v1/me", res.Token, nil, 401, nil)
	admin.wantErr("POST", "/api/v1/admin/users", map[string]any{"username": "plainname"}, 400, "bad_request")
	var made accounts.User
	admin.call("POST", "/api/v1/admin/users", map[string]any{"username": "Made@Example.com"}, 201, &made)
	admin.call("GET", "/api/v1/admin/users/"+url.PathEscape("made@example.com"), nil, 200, nil)
	// The new account signs in with a mailed code.
	if r := clientSignIn(t, s, a.url, "made@example.com"); r.Created || r.User.ID != made.ID {
		t.Fatalf("%+v", r)
	}
}

func TestClientIPTrustsRealIPOnlyFromLoopback(t *testing.T) {
	mk := func(remote, real string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if real != "" {
			r.Header.Set("X-Real-IP", real)
		}
		return r
	}
	if got := clientIP(mk("127.0.0.1:5555", "203.0.113.9"), nil); got != "203.0.113.9" {
		t.Errorf("via local proxy: %q", got)
	}
	if got := clientIP(mk("[::1]:5555", "203.0.113.9"), nil); got != "203.0.113.9" {
		t.Errorf("via local proxy v6: %q", got)
	}
	if got := clientIP(mk("198.51.100.7:5555", "203.0.113.9"), nil); got != "198.51.100.7" {
		t.Errorf("remote peer must not spoof: %q", got)
	}
	proxy := []net.IP{net.ParseIP("10.0.0.5")}
	if got := clientIP(mk("10.0.0.5:5555", "203.0.113.9"), proxy); got != "203.0.113.9" {
		t.Fatalf("trusted proxy: %s", got)
	}
	if got := clientIP(mk("10.0.0.6:5555", "203.0.113.9"), proxy); got != "10.0.0.6" {
		t.Fatalf("another machine on the LAN was believed: %s", got)
	}
	if got := clientIP(mk("127.0.0.1:5555", "not-an-ip"), nil); got != "127.0.0.1" {
		t.Errorf("garbage header: %q", got)
	}
}
