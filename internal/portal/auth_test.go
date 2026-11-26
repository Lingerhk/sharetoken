package portal

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sharetoken/internal/accounts"
	"sharetoken/internal/mailer"
)

// ---- helpers: signing in through the real API ----

// sendCode asks for a sign-in code for email and reads it from the outbox.
func sendCode(t *testing.T, s *Server, c *web, email string) string {
	t.Helper()
	c.call("POST", "/api/v1/auth/code", map[string]string{"email": email}, 200, nil)
	return mailedCode(t, s, email)
}

// clientSignIn signs email in as a client (no second factor) and returns the
// response: a new address gets an account.
func clientSignIn(t *testing.T, s *Server, url, email string) AuthResponse {
	t.Helper()
	c := &web{t: t, url: url, noWeb: true}
	code := sendCode(t, s, c, email)
	resp, raw := c.do("POST", "/api/v1/auth/verify", map[string]string{"email": email, "code": code, "token_name": "test"})
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("sign-in of %s: %d %s", email, resp.StatusCode, raw)
	}
	var res AuthResponse
	if err := json.Unmarshal(raw, &res); err != nil || res.Token == "" {
		t.Fatalf("sign-in of %s: %v %s", email, err, raw)
	}
	return res
}

// webSignIn signs c in with a session cookie (no second factor) and returns
// the response.
func webSignIn(t *testing.T, s *Server, c *web, email string) *http.Response {
	t.Helper()
	code := sendCode(t, s, c, email)
	resp, raw := c.do("POST", "/api/v1/web/auth/verify", map[string]string{"email": email, "code": code})
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("web sign-in of %s: %d %s", email, resp.StatusCode, raw)
	}
	c.cookie = sessionCookie(t, resp).Value
	return resp
}

// testUser makes an account directly in the store (plain names allowed),
// grants the configured sign-up credit as a first sign-in would, and returns
// a token.
func testUser(t *testing.T, s *Server, name string) (*accounts.User, string) {
	t.Helper()
	u, err := s.store.CreateUser(name, false)
	if err != nil {
		t.Fatal(err)
	}
	if credit := s.settings().SignupCreditMicros; credit > 0 {
		if err := s.AdjustCredit(u.ID, int64(credit), "sign-up credit"); err != nil {
			t.Fatal(err)
		}
	}
	tok, _, err := s.store.IssueSessionToken(u.ID, "login", 0, false)
	if err != nil {
		t.Fatal(err)
	}
	return u, tok
}

func errCode(t *testing.T, raw []byte) string {
	t.Helper()
	var e ErrorBody
	_ = json.Unmarshal(raw, &e)
	return e.Error.Code
}

// wantErr sends a request and checks the status and error code.
func (c *web) wantErr(method, path string, body any, status int, code string) {
	c.t.Helper()
	resp, raw := c.do(method, path, body)
	if resp.StatusCode != status || errCode(c.t, raw) != code {
		c.t.Fatalf("%s %s: %d %s, want %d %s", method, path, resp.StatusCode, raw, status, code)
	}
}

func otherCode(code string) string {
	if code == "000000" {
		return "000001"
	}
	return "000000"
}

// ---- tests ----

// One form for both: a new address gets an account when its code is
// redeemed, an existing one signs in.
func TestSignInCreatesOrOpensAccount(t *testing.T) {
	clock := time.Now()
	s, a := newPortal(t, Config{OpenRegistration: true, SignupCreditMicros: 1000, Now: func() time.Time { return clock }})
	c := &web{t: t, url: a.url, noWeb: true}
	var info Info
	a.call("GET", "/api/v1/info", "", nil, 200, &info)
	if !info.CodeSignIn || info.SupportEmail != "support@sharetoken.test" || !info.RegistrationOpen {
		t.Fatalf("%+v", info)
	}

	var sent map[string]any
	c.call("POST", "/api/v1/auth/code", map[string]string{"email": " Alice@Example.com "}, 200, &sent)
	if sent["new_account"] != true || sent["cooldown_seconds"] == nil || sent["expires_in_seconds"] == nil || sent["nonce"] == "" || sent["nonce"] == nil {
		t.Fatalf("%+v", sent)
	}
	c.wantErr("POST", "/api/v1/auth/code", map[string]string{"email": "alice@example.com"}, 429, "code_cooldown")
	c.wantErr("POST", "/api/v1/auth/code", map[string]string{"email": "not-an-email"}, 400, "bad_email")
	code := mailedCode(t, s, "alice@example.com")
	if len(code) != 6 {
		t.Fatalf("code %q", code)
	}
	if ms, _ := s.mail.Outbox(mailer.OutFilter{Q: "alice@example.com", Limit: 1}); len(ms) != 1 || strings.Contains(ms[0].Subject, code) {
		t.Fatalf("%+v", ms)
	}
	// A code is for one address, and there is no way around it.
	c.wantErr("POST", "/api/v1/auth/verify", map[string]string{"email": "bob@example.com", "code": code}, 401, "bad_code")
	c.wantErr("POST", "/api/v1/auth/verify", map[string]string{"email": "alice@example.com"}, 401, "bad_code")

	var res AuthResponse
	resp, raw := c.do("POST", "/api/v1/auth/verify", map[string]string{"email": "ALICE@example.com", "code": " " + code + " ", "token_name": "laptop"})
	if resp.StatusCode != 201 || json.Unmarshal(raw, &res) != nil || !res.Created || res.User.Username != "alice@example.com" || !strings.HasPrefix(res.Token, "st_") {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	if bal, _ := s.Balance(res.User.ID); bal.AvailableMicros != 1000 {
		t.Fatalf("sign-up credit: %+v", bal)
	}
	c.wantErr("POST", "/api/v1/auth/verify", map[string]string{"email": "alice@example.com", "code": code}, 401, "bad_code") // used
	var toks struct{ Tokens []accounts.Token }
	a.call("GET", "/api/v1/tokens", res.Token, nil, 200, &toks)
	if len(toks.Tokens) != 1 || toks.Tokens[0].Name != "laptop" {
		t.Fatalf("%+v", toks)
	}

	// The same address again: the same account, no second credit.
	clock = clock.Add(2 * time.Minute) // past the cooldown
	sent = nil
	c.call("POST", "/api/v1/auth/code", map[string]string{"email": "alice@example.com"}, 200, &sent)
	if sent["new_account"] != false {
		t.Fatalf("%+v", sent)
	}
	code = mailedCode(t, s, "alice@example.com")
	var again AuthResponse
	a.call("POST", "/api/v1/auth/verify", "", map[string]string{"email": "alice@example.com", "code": code}, 200, &again)
	if again.Created || again.User.ID != res.User.ID || again.Token == res.Token {
		t.Fatalf("%+v", again)
	}
	if bal, _ := s.Balance(res.User.ID); bal.AvailableMicros != 1000 {
		t.Fatalf("credit twice: %+v", bal)
	}

	// The longest address that fits the column works; one longer does not.
	longest := strings.Repeat("a", accounts.MaxEmailLen-len("@example.com")) + "@example.com"
	if r := clientSignIn(t, s, a.url, longest); !r.Created {
		t.Fatalf("%+v", r)
	}
	c.wantErr("POST", "/api/v1/auth/code", map[string]string{"email": "l" + longest}, 400, "bad_email")
}

// The portal's pages get a session cookie through the same codes.
func TestWebSignIn(t *testing.T) {
	s, a := newPortal(t, Config{OpenRegistration: true})
	c := &web{t: t, url: a.url}
	code := sendCode(t, s, c, "web@example.com")
	// Login CSRF: without the pages' header the web endpoint refuses.
	(&web{t: t, url: a.url, noWeb: true}).wantErr("POST", "/api/v1/web/auth/verify", map[string]string{"email": "web@example.com", "code": code}, 403, "cross_site")
	var out struct {
		User accounts.User `json:"user"`
	}
	resp := c.call("POST", "/api/v1/web/auth/verify", map[string]string{"email": "Web@Example.com", "code": code}, 201, &out) // 201: a new account
	if out.User.Username != "web@example.com" {
		t.Fatalf("%+v", out)
	}
	c.cookie = sessionCookie(t, resp).Value
	c.call("GET", "/api/v1/me", nil, 200, nil)
	// Clients cannot use the web endpoint's cookie flow, nor the pages the client one.
	(&web{t: t, url: a.url, noWeb: true, hdr: map[string]string{"Origin": "https://evil.example"}}).wantErr("POST", "/api/v1/auth/code", map[string]string{"email": "x@example.com"}, 403, "cross_site")
}

// Lifetimes: 60 days for a client's token, 15 days for a browser session,
// unless the settings say otherwise.
func TestSignInLifetimes(t *testing.T) {
	s, a := newPortal(t, Config{OpenRegistration: true})
	res := clientSignIn(t, s, a.url, "life@example.com")
	var toks struct{ Tokens []accounts.Token }
	a.call("GET", "/api/v1/tokens", res.Token, nil, 200, &toks)
	got := time.Duration(toks.Tokens[0].ExpiresAt - toks.Tokens[0].CreatedAt)
	if got != 60*24*time.Hour {
		t.Fatalf("client token lives %v", got)
	}
	c := &web{t: t, url: a.url}
	resp := webSignIn(t, s, c, "web-life@example.com")
	if ck := sessionCookie(t, resp); ck.MaxAge != 15*24*3600 {
		t.Fatalf("session cookie: %+v", ck)
	}
	var info Info
	a.call("GET", "/api/v1/info", "", nil, 200, &info)
	if info.SessionHours != 15*24 {
		t.Fatalf("%+v", info)
	}

	st := s.settings()
	st.WebSessionHours, st.LoginTokenHours = 2, 3
	if err := s.saveSettings(st, "test"); err != nil {
		t.Fatal(err)
	}
	res = clientSignIn(t, s, a.url, "life2@example.com")
	a.call("GET", "/api/v1/tokens", res.Token, nil, 200, &toks)
	if got := time.Duration(toks.Tokens[0].ExpiresAt - toks.Tokens[0].CreatedAt); got != 3*time.Hour {
		t.Fatalf("client token lives %v", got)
	}
	resp = webSignIn(t, s, &web{t: t, url: a.url}, "web-life2@example.com")
	if ck := sessionCookie(t, resp); ck.MaxAge != 2*3600 {
		t.Fatalf("session cookie: %+v", ck)
	}
}

// Closed registration keeps new addresses out, not existing accounts.
func TestSignInRegistrationClosed(t *testing.T) {
	s, a := newPortal(t, Config{})
	if _, err := s.CreateUser("old@example.com", false); err != nil {
		t.Fatal(err)
	}
	c := &web{t: t, url: a.url, noWeb: true}
	c.wantErr("POST", "/api/v1/auth/code", map[string]string{"email": "new@example.com"}, 403, "registration_closed")
	if ms, _ := s.mail.Outbox(mailer.OutFilter{Q: "new@example.com"}); len(ms) != 0 {
		t.Fatalf("mail went out: %+v", ms)
	}
	res := clientSignIn(t, s, a.url, "old@example.com")
	if res.Created || res.User.Username != "old@example.com" {
		t.Fatalf("%+v", res)
	}
	// Probing many new addresses from one IP is throttled.
	for i := 1; i < maxFails; i++ {
		c.wantErr("POST", "/api/v1/auth/code", map[string]string{"email": "new@example.com"}, 403, "registration_closed")
	}
	c.wantErr("POST", "/api/v1/auth/code", map[string]string{"email": "new@example.com"}, 429, "throttled")
}

// Invite codes: asked for before any mail goes out, used up only by the
// account they create.
func TestSignInInvites(t *testing.T) {
	s, a := newPortal(t, Config{OpenRegistration: true})
	st := s.settings()
	st.InviteRequired = true
	if err := s.saveSettings(st, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO invite_codes (code, max_uses, created_at) VALUES ('ONCE', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser("member@example.com", false); err != nil {
		t.Fatal(err)
	}
	c := &web{t: t, url: a.url, noWeb: true}
	c.wantErr("POST", "/api/v1/auth/code", map[string]string{"email": "new@example.com"}, 403, "invite_required")
	c.wantErr("POST", "/api/v1/auth/code", map[string]string{"email": "new@example.com", "invite_code": "WRONG"}, 403, "bad_invite")
	// Members need none.
	if res := clientSignIn(t, s, a.url, "member@example.com"); res.Created {
		t.Fatalf("%+v", res)
	}
	c.call("POST", "/api/v1/auth/code", map[string]string{"email": "new@example.com", "invite_code": "once"}, 200, nil)
	code := mailedCode(t, s, "new@example.com")
	c.wantErr("POST", "/api/v1/auth/verify", map[string]string{"email": "new@example.com", "code": code}, 403, "invite_required")
	var used int
	_ = s.db.QueryRow(`SELECT used FROM invite_codes WHERE code = 'ONCE'`).Scan(&used)
	if used != 0 {
		t.Fatal("a refused sign-in used the invite up")
	}
	var res AuthResponse
	a.call("POST", "/api/v1/auth/verify", "", map[string]string{"email": "new@example.com", "code": code, "invite_code": "ONCE"}, 201, &res)
	_ = s.db.QueryRow(`SELECT used FROM invite_codes WHERE code = 'ONCE'`).Scan(&used)
	if used != 1 || !res.Created {
		t.Fatalf("used %d %+v", used, res)
	}
	// Signing in again needs no invite; the used-up one opens no other account.
	c.wantErr("POST", "/api/v1/auth/code", map[string]string{"email": "third@example.com", "invite_code": "ONCE"}, 403, "bad_invite")
}

// The blocklist and the domain rules apply to new accounts.
func TestSignInEmailRules(t *testing.T) {
	s, a := newPortal(t, Config{OpenRegistration: true})
	if _, err := s.CreateUser("old@other.org", false); err != nil {
		t.Fatal(err)
	}
	st := s.settings()
	st.EmailDomainAllow, st.EmailBlock = []string{"example.com", "*.edu.cn"}, []string{"bad@example.com"}
	if err := s.saveSettings(st, "test"); err != nil {
		t.Fatal(err)
	}
	c := &web{t: t, url: a.url, noWeb: true}
	c.wantErr("POST", "/api/v1/auth/code", map[string]string{"email": "bad@example.com"}, 403, "email_not_allowed")
	c.wantErr("POST", "/api/v1/auth/code", map[string]string{"email": "x@other.org"}, 403, "email_not_allowed")
	// A blocked mailbox does not come back under another spelling.
	c.wantErr("POST", "/api/v1/auth/code", map[string]string{"email": "bad+1@example.com"}, 403, "email_not_allowed")
	c.wantErr("POST", "/api/v1/auth/code", map[string]string{"email": "Bad+again@Example.com"}, 403, "email_not_allowed")
	st.EmailDomainAllow, st.EmailBlock = nil, []string{"b.a.d+x@gmail.com"}
	if err := s.saveSettings(st, "test"); err != nil {
		t.Fatal(err)
	}
	c.wantErr("POST", "/api/v1/auth/code", map[string]string{"email": "ba.d@googlemail.com"}, 403, "email_not_allowed")
	c.call("POST", "/api/v1/auth/code", map[string]string{"email": "ba.d@example.com"}, 200, nil)
	st.EmailDomainAllow, st.EmailBlock = []string{"example.com", "*.edu.cn"}, nil
	if err := s.saveSettings(st, "test"); err != nil {
		t.Fatal(err)
	}
	c.call("POST", "/api/v1/auth/code", map[string]string{"email": "x@cs.pku.edu.cn"}, 200, nil)
	// An existing account outside the rules still signs in.
	clientSignIn(t, s, a.url, "old@other.org")
}

// The sign-up credit goes to the first account of a mailbox: a "+tag" or a
// Gmail-dot spelling of it signs up as an account of its own, without credit.
func TestSignupCreditOncePerMailbox(t *testing.T) {
	clock := time.Now()
	s, a := newPortal(t, Config{OpenRegistration: true, SignupCreditMicros: 1000, Now: func() time.Time { return clock }})
	credit := func(email string) int64 {
		t.Helper()
		res := clientSignIn(t, s, a.url, email)
		if res.User.Username != accounts.NormalizeEmail(email) {
			t.Fatalf("%s signed in as %s", email, res.User.Username)
		}
		bal, err := s.Balance(res.User.ID)
		if err != nil {
			t.Fatal(err)
		}
		clock = clock.Add(2 * time.Minute)
		return int64(bal.AvailableMicros)
	}
	for email, want := range map[string]int64{"ann@example.com": 1000, "bob@gmail.com": 1000} {
		if got := credit(email); got != want {
			t.Fatalf("%s got %d, want %d", email, got, want)
		}
	}
	for _, alias := range []string{"ann+1@example.com", "ann+2@example.com", "b.ob@gmail.com", "bob+x@googlemail.com"} {
		if got := credit(alias); got != 0 {
			t.Fatalf("%s, a second account of its mailbox, got %d", alias, got)
		}
	}
	// Dots are part of the name anywhere but Gmail.
	if got := credit("a.nn@example.com"); got != 1000 {
		t.Fatalf("a.nn@example.com got %d, want 1000", got)
	}
	// Signing in again changes nothing.
	if got := credit("ann@example.com"); got != 1000 {
		t.Fatalf("ann@example.com has %d after signing in again", got)
	}
}

// A disabled account is answered like a working one when a code is asked for
// (nobody learns from the answer that it exists and is disabled); its mail
// says so and carries no code, and nothing signs it in.
func TestSignInDisabledAccount(t *testing.T) {
	clock := time.Now()
	s, a := newPortal(t, Config{OpenRegistration: true, Now: func() time.Time { return clock }})
	res := clientSignIn(t, s, a.url, "gone@example.com")
	other := clientSignIn(t, s, a.url, "here@example.com")
	if err := s.store.SetDisabled(res.User.ID, true); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(2 * time.Minute)
	c := &web{t: t, url: a.url, noWeb: true}
	var gone, here map[string]any
	c.call("POST", "/api/v1/auth/code", map[string]string{"email": "gone@example.com"}, 200, &gone)
	c.call("POST", "/api/v1/auth/code", map[string]string{"email": other.User.Username}, 200, &here)
	nonce, _ := gone["nonce"].(string)
	delete(gone, "nonce")
	delete(here, "nonce")
	if nonce == "" || fmt.Sprint(gone) != fmt.Sprint(here) {
		t.Fatalf("a disabled account answers %v, a working one %v", gone, here)
	}
	c.wantErr("POST", "/api/v1/auth/code", map[string]string{"email": "gone@example.com"}, 429, "code_cooldown")
	ms, _ := s.mail.Outbox(mailer.OutFilter{Q: "gone@example.com", Limit: 1})
	if len(ms) != 1 || ms[0].Kind != mailer.KindNotice || !strings.Contains(ms[0].Subject, "disabled") {
		t.Fatalf("mail to a disabled account: %+v", ms)
	}
	if m, _ := s.mail.OutMessage(ms[0].ID); mailedCodeRE.MatchString(m.Text) {
		t.Fatalf("a code went to a disabled account: %q", m.Text)
	}
	c.wantErr("POST", "/api/v1/auth/verify", map[string]string{"email": "gone@example.com", "code": "000000"}, 401, "bad_code")
	a.call("GET", "/api/v1/me", res.Token, nil, 401, nil)
	if _, _, err := s.IssueSignInCode("gone@example.com"); !errors.Is(err, accounts.ErrDisabled) {
		t.Fatalf("an operator's code for a disabled account: %v", err)
	}
}

// from is a client at ip, as the portal sees it behind its proxy.
func from(t *testing.T, url, ip string) *web {
	return &web{t: t, url: url, noWeb: true, hdr: map[string]string{"X-Real-IP": ip}}
}

// A code belongs to the request that asked for it. Someone else who knows
// only the address can neither use up its tries, nor get its owner
// throttled, nor replace it with a code of their own.
func TestSignInCodeBoundToRequest(t *testing.T) {
	clock := time.Now()
	s, a := newPortal(t, Config{OpenRegistration: true, Now: func() time.Time { return clock }})
	const email = "victim@example.com"
	victim, attacker := from(t, a.url, "198.51.100.7"), from(t, a.url, "203.0.113.9")
	var sent struct {
		Nonce string `json:"nonce"`
	}
	victim.call("POST", "/api/v1/auth/code", map[string]string{"email": email}, 200, &sent)
	code := mailedCode(t, s, email)
	if sent.Nonce == "" {
		t.Fatal("no nonce with the code")
	}

	// Guesses without the nonce, and with a made-up one: until the attacker's
	// own address is throttled.
	for i := range 3 * maxFails {
		body := map[string]string{"email": email, "code": otherCode(code), "nonce": ""}
		if i%2 == 1 {
			body["nonce"] = "AAAAAAAAAAAAAAAAAAAAAA"
		}
		want, ecode := 401, "bad_code"
		if i >= maxFails {
			want, ecode = 429, "throttled"
		}
		attacker.wantErr("POST", "/api/v1/auth/verify", body, want, ecode)
	}
	// Even the right code is worth nothing without the nonce.
	from(t, a.url, "203.0.113.10").wantErr("POST", "/api/v1/auth/verify", map[string]string{"email": email, "code": code, "nonce": ""}, 401, "bad_code")
	// The attacker asks for a code for the address and burns it.
	clock = clock.Add(mailer.CodeCooldown + time.Second)
	third := from(t, a.url, "203.0.113.11")
	var theirs struct {
		Nonce string `json:"nonce"`
	}
	third.call("POST", "/api/v1/auth/code", map[string]string{"email": email}, 200, &theirs)
	for range 6 {
		third.wantErr("POST", "/api/v1/auth/verify", map[string]string{"email": email, "code": otherCode(mailedCode(t, s, email)), "nonce": theirs.Nonce}, 401, "bad_code")
	}

	// None of it reached the first code: it still signs its owner in.
	var res AuthResponse
	victim.call("POST", "/api/v1/auth/verify", map[string]string{"email": email, "code": code, "nonce": sent.Nonce}, 201, &res)
	if res.Token == "" || res.User.Username != email {
		t.Fatalf("%+v", res)
	}
}

// Wrong codes sent together cannot all be tested: each takes its place under
// the throttle before it is checked.
func TestSignInGuessesHoldInParallel(t *testing.T) {
	s, a := newPortal(t, Config{OpenRegistration: true})
	const email = "a@example.com"
	c := &web{t: t, url: a.url, noWeb: true}
	code := sendCode(t, s, c, email)
	var wg sync.WaitGroup
	var tested atomic.Int64
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, raw := c.do("POST", "/api/v1/auth/verify", map[string]string{"email": email, "code": otherCode(code)})
			switch {
			case resp.StatusCode == 401:
				tested.Add(1)
			case resp.StatusCode != 429:
				t.Errorf("%d %s", resp.StatusCode, raw)
			}
		}()
	}
	wg.Wait()
	if n := tested.Load(); n != maxFails {
		t.Fatalf("%d wrong codes were tested in parallel, want %d", n, maxFails)
	}
}

// An IPv6 network counts as one address: its hosts share the hourly codes.
func TestCodeQuotaCountsIPv6Networks(t *testing.T) {
	_, a := newPortal(t, Config{OpenRegistration: true})
	for i := range mailer.MaxCodesPerIP {
		from(t, a.url, fmt.Sprintf("2001:db8:1:2::%x", i+1)).call("POST", "/api/v1/auth/code", map[string]string{"email": fmt.Sprintf("u%d@example.com", i)}, 200, nil)
	}
	from(t, a.url, "2001:db8:1:2:ffff::1").wantErr("POST", "/api/v1/auth/code", map[string]string{"email": "more@example.com"}, 429, "throttled")
	from(t, a.url, "2001:db8:1:3::1").call("POST", "/api/v1/auth/code", map[string]string{"email": "more@example.com"}, 200, nil)
}

// When the codes for addresses without an account are used up, people with an
// account still get theirs, and the administrators are told once.
func TestCodeBudgetForNewAddresses(t *testing.T) {
	alerts := make(chan url.Values, 4)
	push := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		alerts <- r.PostForm
	}))
	defer push.Close()
	creds := filepath.Join(t.TempDir(), "pushover.env")
	os.WriteFile(creds, []byte("PUSHOVER_TOKEN=tok\nPUSHOVER_USER=usr\n"), 0o600)
	s, a := newPortal(t, Config{OpenRegistration: true, PushoverFile: creds})
	s.pushoverURL = push.URL
	if _, err := s.store.CreateUser("member@example.com", false); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	for i := range mailer.MaxNewCodesPerHour {
		if _, err := s.db.Exec(`INSERT INTO mail_codes (purpose, email, ip, salt, code_hash, known, expires_at, created_at) VALUES ('login', ?, ?, 'x', 'x', 0, ?, ?)`,
			fmt.Sprintf("made-up-%d@example.com", i), fmt.Sprintf("198.51.%d.%d", i/250, i%250), now+600, now); err != nil {
			t.Fatal(err)
		}
	}
	c := &web{t: t, url: a.url, noWeb: true}
	c.wantErr("POST", "/api/v1/auth/code", map[string]string{"email": "newcomer@example.com"}, 429, "throttled")
	c.wantErr("POST", "/api/v1/auth/code", map[string]string{"email": "newcomer2@example.com"}, 429, "throttled")
	c.call("POST", "/api/v1/auth/code", map[string]string{"email": "member@example.com"}, 200, nil)
	s.background.Wait()
	if len(alerts) != 1 {
		t.Fatalf("%d alerts, want one", len(alerts))
	}
	if f := <-alerts; !strings.Contains(f.Get("message"), "without an account") {
		t.Fatalf("alert %v", f)
	}
}

func TestSignInWrongCodesThrottled(t *testing.T) {
	s, a := newPortal(t, Config{OpenRegistration: true})
	c := &web{t: t, url: a.url, noWeb: true}
	code := sendCode(t, s, c, "a@example.com")
	wrong := otherCode(code)
	// The mailer gives a code five tries; the throttle counts on.
	for i := 0; i < maxFails; i++ {
		c.wantErr("POST", "/api/v1/auth/verify", map[string]string{"email": "a@example.com", "code": wrong}, 401, "bad_code")
	}
	c.wantErr("POST", "/api/v1/auth/verify", map[string]string{"email": "a@example.com", "code": code}, 429, "throttled")
}

func TestSignInNeedsMail(t *testing.T) {
	s, a := newPortal(t, Config{OpenRegistration: true})
	st := s.settings()
	st.MailFrom = ""
	if err := s.saveSettings(st, "test"); err != nil {
		t.Fatal(err)
	}
	(&web{t: t, url: a.url, noWeb: true}).wantErr("POST", "/api/v1/auth/code", map[string]string{"email": "a@example.com"}, 503, "mail_unavailable")
}

// The old password endpoints are gone.
func TestNoPasswordEndpoints(t *testing.T) {
	_, a := newPortal(t, Config{OpenRegistration: true})
	for _, p := range []string{"/api/v1/register", "/api/v1/register/code", "/api/v1/login", "/api/v1/web/login", "/api/v1/web/register",
		"/api/v1/password", "/api/v1/password/forgot", "/api/v1/password/reset", "/api/v1/admin/users/x/password"} {
		resp, err := http.Post(a.url+p, "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s: %d", p, resp.StatusCode)
		}
	}
}

// A fresh database keeps no password anywhere.
func TestSchemaHasNoPasswords(t *testing.T) {
	s, _ := newPortal(t, Config{})
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'users' AND column_name = 'pass_hash'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("users.pass_hash: %d %v", n, err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'password_resets'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("password_resets: %d %v", n, err)
	}
	// Binaries that still write passwords refuse such a database.
	var need int
	if err := s.db.QueryRow(`SELECT min_reader FROM schema_migrations WHERE component = 'core' AND version = 5`).Scan(&need); err != nil || need != 5 {
		t.Fatalf("min reader %d %v", need, err)
	}
}

// An operator's code (portal login-code) signs in like a mailed one, on the
// pages and in clients, once; nothing is mailed, and it does not use up the
// holder's own requests.
func TestOperatorSignInCode(t *testing.T) {
	s, a := newPortal(t, Config{})
	if _, _, err := s.IssueSignInCode("nobody@example.com"); !errors.Is(err, accounts.ErrNotFound) {
		t.Fatalf("unknown address: %v", err)
	}
	u, _ := testUser(t, s, "ann@example.com")
	code, exp, err := s.IssueSignInCode(" Ann@Example.com ")
	if err != nil || len(code) != 12 || exp.Before(time.Now().Add(9*time.Minute)) {
		t.Fatalf("code %q until %v: %v", code, exp, err)
	}
	if ms, _ := s.mail.Outbox(mailer.OutFilter{Q: "ann@example.com", Limit: 5}); len(ms) != 0 {
		t.Fatalf("an operator's code was mailed: %+v", ms)
	}
	c := &web{t: t, url: a.url, noWeb: true}
	var res AuthResponse
	// No nonce: nobody asked the portal for this code.
	resp, raw := c.do("POST", "/api/v1/auth/verify", map[string]string{"email": "ann@example.com", "code": code, "nonce": ""})
	if resp.StatusCode != 200 || json.Unmarshal(raw, &res) != nil || res.User.ID != u.ID || res.Created {
		t.Fatalf("client sign-in: %d %s", resp.StatusCode, raw)
	}
	c.wantErr("POST", "/api/v1/auth/verify", map[string]string{"email": "ann@example.com", "code": code}, 401, "bad_code")
	// The holder can still ask for a mailed code at once.
	sendCode(t, s, c, "ann@example.com")
	// The pages take one too, typed into the form that just asked for a mailed code.
	code, _, err = s.IssueSignInCode("ann@example.com")
	if err != nil {
		t.Fatal(err)
	}
	w := &web{t: t, url: a.url}
	if resp, raw := w.do("POST", "/api/v1/web/auth/verify", map[string]string{"email": "ann@example.com", "code": code}); resp.StatusCode != 200 {
		t.Fatalf("web sign-in: %d %s", resp.StatusCode, raw)
	}
	if err := s.store.SetDisabled(u.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.IssueSignInCode("ann@example.com"); !errors.Is(err, accounts.ErrDisabled) {
		t.Fatalf("disabled account: %v", err)
	}
}
