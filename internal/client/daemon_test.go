package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sharetoken/internal/accounts"
	"sharetoken/internal/mysqldb"
	"sharetoken/internal/portal"
	"sharetoken/internal/secfile"
	"sharetoken/internal/totp"
	"sharetoken/internal/version"
)

// No test may reach the production portal: the built-in address is somewhere
// nothing listens unless a test points it at its own portal.
func TestMain(m *testing.M) {
	os.Setenv("SHARETOKEN_PORTAL", "https://127.0.0.1:1")
	os.Exit(m.Run())
}

// daemonEnv is a portal (with no coordinator, so agents cannot join the network
// unless a test arranges it) and helpers to run daemons against it.
type daemonEnv struct {
	t      *testing.T
	portal *portal.Server
	db     *sql.DB
	srv    *httptest.Server
	url    string
	logins atomic.Int64 // requests the portal saw on its sign-in endpoints (an address or a code)
}

func newDaemonEnv(t *testing.T, cfg portal.Config, useTLS bool) *daemonEnv {
	t.Helper()
	db, err := mysqldb.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg.DB = db
	if cfg.Mail.From == "" {
		cfg.Mail.From = "ShareToken <team@example.test>" // sign-in codes are mail
	}
	p, err := portal.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	e := &daemonEnv{t: t, portal: p, db: db}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/auth/") || r.URL.Path == "/api/v1/mfa" {
			e.logins.Add(1)
		}
		p.Handler().ServeHTTP(w, r)
	})
	e.srv = httptest.NewUnstartedServer(h)
	if useTLS {
		cert, err := portal.SelfSignedCert(t.TempDir(), []string{"127.0.0.1"})
		if err != nil {
			t.Fatal(err)
		}
		e.srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
		e.srv.StartTLS()
	} else {
		e.srv.Start()
	}
	t.Cleanup(e.srv.Close)
	e.url = e.srv.URL
	return e
}

func openPortal() portal.Config {
	return portal.Config{OpenRegistration: true, SignupCreditMicros: 5_000_000}
}

type testDaemon struct {
	*Daemon
	t             *testing.T
	base          string
	closeOnce     func()
	portalForTest string
}

func (e *daemonEnv) daemonIn(home string) *testDaemon {
	e.t.Helper()
	d := NewDaemon(home)
	d.HTTP = "127.0.0.1:0"
	d.RetryMin, d.RetryMax = 50*time.Millisecond, 200*time.Millisecond
	nat := false
	d.NAT, d.Listen = &nat, []string{"/ip4/127.0.0.1/tcp/0"}
	if err := d.Start(); err != nil {
		e.t.Fatal(err)
	}
	var once sync.Once
	td := &testDaemon{Daemon: d, t: e.t, base: "http://" + d.Addr()}
	e.t.Cleanup(func() { once.Do(d.Close) })
	return td
}

func (e *daemonEnv) daemon() *testDaemon { return e.daemonIn(e.t.TempDir()) }

type apiErrBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// call sends a request and returns the status and raw reply.
func (d *testDaemon) call(method, path string, body any, hdr ...string) (int, []byte) {
	d.t.Helper()
	var rd io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, d.base+path, rd)
	if err != nil {
		d.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if strings.HasPrefix(path, "/v1/") {
		req.Header.Set("Authorization", "Bearer "+d.APIKey()) // as a tool set up with the API key sends it
	} else {
		req.Header.Set("Authorization", "Bearer "+d.Key()) // as the desktop shell and the CLI send it
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		switch {
		case hdr[i] == "Host":
			req.Host = hdr[i+1]
		case hdr[i] == "Authorization" && hdr[i+1] == "":
			req.Header.Del("Authorization")
		default:
			req.Header.Set(hdr[i], hdr[i+1])
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		d.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

// want asserts the status and, when not empty, the error code of a request.
func (d *testDaemon) want(status int, code, method, path string, body any, hdr ...string) []byte {
	d.t.Helper()
	got, data := d.call(method, path, body, hdr...)
	if got != status {
		d.t.Fatalf("%s %s: status %d, want %d: %s", method, path, got, status, data)
	}
	if code != "" {
		var eb apiErrBody
		if err := json.Unmarshal(data, &eb); err != nil || eb.Error.Code != code {
			d.t.Fatalf("%s %s: error %s, want code %s", method, path, data, code)
		}
	}
	return data
}

func (d *testDaemon) session() Session {
	d.t.Helper()
	var s Session
	if err := json.Unmarshal(d.want(200, "", "GET", "/api/v1/session", nil), &s); err != nil {
		d.t.Fatal(err)
	}
	return s
}

func (d *testDaemon) waitState(want ...string) Session {
	d.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		s := d.session()
		for _, w := range want {
			if s.State == w {
				return s
			}
		}
		if time.Now().After(deadline) {
			d.t.Fatalf("session stays %+v, want %v", s, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// code reads the last sign-in code mailed to email, as its recipient would
// (nothing delivers mail here: the outbox keeps it), and moves the request out
// of the per-address cooldown and hourly limits, so a test can sign in again.
func (e *daemonEnv) code(email string) string {
	e.t.Helper()
	var subject string
	if err := e.db.QueryRow(`SELECT subject FROM mail_outbox WHERE to_addr = ? ORDER BY id DESC LIMIT 1`, accounts.NormalizeEmail(email)).Scan(&subject); err != nil {
		e.t.Fatalf("no code mailed to %s: %v", email, err)
	}
	if _, err := e.db.Exec(`UPDATE mail_codes SET created_at = created_at - 7200`); err != nil {
		e.t.Fatal(err)
	}
	return subject[strings.LastIndex(subject, " ")+1:]
}

// requestCode has the daemon ask for a code and returns the portal's answer.
func (d *testDaemon) requestCode(portalURL, email string) CodeSent {
	d.t.Helper()
	var sent CodeSent
	if err := json.Unmarshal(d.want(200, "", "POST", "/api/v1/auth/code", map[string]string{"portal": portalURL, "email": email}), &sent); err != nil {
		d.t.Fatal(err)
	}
	return sent
}

// signIn runs the whole sign-in through the daemon: a code by mail, redeemed.
func (d *testDaemon) signIn(e *daemonEnv, email string) Session {
	d.t.Helper()
	d.requestCode(e.url, email)
	var s Session
	data := d.want(200, "", "POST", "/api/v1/auth/verify", map[string]string{"portal": e.url, "email": email, "code": e.code(email), "device": "test-box"})
	if err := json.Unmarshal(data, &s); err != nil {
		d.t.Fatal(err)
	}
	return s
}

func (d *testDaemon) savedConfig() Config {
	d.t.Helper()
	c, err := LoadConfig(d.home)
	if err != nil {
		d.t.Fatal(err)
	}
	return c
}

// portalAPI calls the portal directly with a bearer token (the account's own
// settings, which the desktop app leaves to the web portal).
func (e *daemonEnv) portalAPI(method, path, token string, in, out any) int {
	e.t.Helper()
	data, _ := json.Marshal(in)
	req, _ := http.NewRequest(method, e.url+path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			e.t.Fatalf("%s: %s", path, body)
		}
	}
	return resp.StatusCode
}

// adminToken makes an administrator and a token that admin endpoints accept.
func (e *daemonEnv) adminToken() string {
	e.t.Helper()
	u, err := e.portal.CreateUser("ops@example.test", true)
	if err != nil {
		e.t.Fatal(err)
	}
	tok, _, err := e.portal.Store().IssueSessionToken(u.ID, "test", 0, true)
	if err != nil {
		e.t.Fatal(err)
	}
	return tok
}

// Logged out, the daemon runs and says so; nothing needing a login works.
func TestDaemonLoggedOut(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	d := e.daemon()
	s := d.session()
	if s.State != StateLoggedOut || s.LoggedIn || s.User != "" || !strings.HasSuffix(s.API, "/v1") {
		t.Fatalf("session: %+v", s)
	}
	d.want(401, "not_logged_in", "GET", "/api/v1/account", nil)
	d.want(401, "not_logged_in", "GET", "/api/v1/jobs", nil)
	d.want(401, "not_logged_in", "GET", "/api/v1/ledger", nil)
	d.want(401, "not_logged_in", "GET", "/api/v1/status", nil)
	d.want(401, "not_logged_in", "GET", "/api/v1/models", nil)
	d.want(401, "not_logged_in", "POST", "/v1/chat/completions", map[string]string{})
	d.want(401, "not_logged_in", "POST", "/api/v1/share/start", nil)
	d.want(200, "", "POST", "/api/v1/logout", nil) // nothing to do, not an error
}

// The whole sign-up: an address and the code mailed to it in, a bound device
// and a usable session out, with the account readable although no network can
// be joined.
func TestDaemonSignUp(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	d := e.daemon()
	if sent := d.requestCode(e.url, "  Alice.Smith+st@Example.COM "); !sent.NewAccount || sent.CooldownSeconds <= 0 {
		t.Fatalf("code: %+v", sent)
	}
	var s Session
	if err := json.Unmarshal(d.want(200, "", "POST", "/api/v1/auth/verify", map[string]string{"portal": e.url, "email": "  Alice.Smith+st@Example.COM ",
		"code": e.code("alice.smith+st@example.com"), "device": "test-box"}), &s); err != nil {
		t.Fatal(err)
	}
	if !s.LoggedIn || s.User != "alice.smith+st@example.com" || s.Device != "test-box" || s.Peer == "" {
		t.Fatalf("session: %+v", s)
	}
	cfg := d.savedConfig()
	if cfg.User != s.User || !strings.HasPrefix(cfg.Token, "st_") || cfg.Portal != e.url {
		t.Fatalf("config: %+v", cfg)
	}
	if err := secfile.CheckPrivate(filepath.Join(d.home, ConfigFile)); err != nil {
		t.Fatal(err)
	}
	// No coordinator on this portal: the agent keeps retrying, the account works.
	s = d.waitState(StateRetrying)
	if !s.LoggedIn || s.Error == "" {
		t.Fatalf("session: %+v", s)
	}
	d.want(503, "not_connected", "GET", "/api/v1/status", nil)
	var me portal.Me
	if err := json.Unmarshal(d.want(200, "", "GET", "/api/v1/account", nil), &me); err != nil {
		t.Fatal(err)
	}
	if me.User.Username != s.User || me.Balance.AvailableMicros != 5_000_000 || len(me.Devices) != 1 || me.Devices[0].Peer != s.Peer || me.Devices[0].Name != "test-box" {
		t.Fatalf("me: %+v", me)
	}
	d.want(200, "", "GET", "/api/v1/jobs?limit=5", nil)
	d.want(200, "", "GET", "/api/v1/ledger", nil)

	// The token never leaves the daemon.
	for _, path := range []string{"/api/v1/session", "/api/v1/account"} {
		if _, data := d.call("GET", path, nil); bytes.Contains(data, []byte(cfg.Token)) || bytes.Contains(data, []byte(`"token"`)) {
			t.Fatalf("%s leaks the token: %s", path, data)
		}
	}
	// A second sign-in without logging out is refused.
	d.want(409, "already_logged_in", "POST", "/api/v1/auth/verify", map[string]string{"portal": e.url, "email": s.User, "code": "123456"})
}

// Every way a sign-in can be refused comes back with a code the app handles,
// and none leaves a login behind.
func TestDaemonSignInErrors(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	d := e.daemon()
	try := func(status int, code, path, portalURL, email, mailCode string) {
		t.Helper()
		d.want(status, code, "POST", path, map[string]string{"portal": portalURL, "email": email, "code": mailCode})
		if s := d.session(); s.LoggedIn {
			t.Fatalf("logged in after a failed sign-in: %+v", s)
		}
		if c := d.savedConfig(); c.Token != "" {
			t.Fatal("token saved after a failed sign-in")
		}
	}
	for _, bad := range []string{"plainname", "a@b", "a b@example.com", "@example.com", "a@", "a@@example.com", "a..b@example.com", ".a@example.com", "a@-x.com", strings.Repeat("a", 200) + "@example.com", "<a@example.com>", "Alice <a@example.com>", "a@example.com\nbcc:x@y.z"} {
		try(400, "bad_email", "/api/v1/auth/code", e.url, bad, "")
	}
	try(400, "bad_request", "/api/v1/auth/code", e.url, "", "")
	try(400, "bad_request", "/api/v1/auth/verify", e.url, "nocode@example.com", "")
	t.Setenv("SHARETOKEN_PORTAL", "ftp://not-a-portal.example.com")
	try(400, "bad_portal", "/api/v1/auth/code", "", "a@example.com", "") // empty: the built-in address, here a bad one
	t.Setenv("SHARETOKEN_PORTAL", "https://127.0.0.1:1")
	try(400, "bad_portal", "/api/v1/auth/code", "ftp://x.example.com", "a@example.com", "")
	try(400, "bad_portal", "/api/v1/auth/code", "https://user:pw@x.example.com", "a@example.com", "")
	try(400, "insecure_portal", "/api/v1/auth/code", "http://203.0.113.7:8092", "a@example.com", "")
	try(502, "portal_unreachable", "/api/v1/auth/code", "https://127.0.0.1:1", "a@example.com", "")
	try(502, "portal_unreachable", "/api/v1/auth/verify", "https://127.0.0.1:1", "a@example.com", "123456")

	// A wrong code, and one for another address.
	d.requestCode(e.url, "w@example.com")
	right := e.code("w@example.com")
	try(401, "bad_code", "/api/v1/auth/verify", e.url, "w@example.com", wrongCode(right))
	try(401, "bad_code", "/api/v1/auth/verify", e.url, "other@example.com", right)
	// Asking again within a minute is refused (the test helper skips this wait).
	d.requestCode(e.url, "cool@example.com")
	try(429, "code_cooldown", "/api/v1/auth/code", e.url, "cool@example.com", "")
	d.want(400, "bad_request", "POST", "/api/v1/auth/code", json.RawMessage(`{"portal":`+"3"+`}`))
}

// With registration closed, a new address is refused before any mail goes
// out, and an account an administrator made signs in as usual.
func TestDaemonRegistrationClosed(t *testing.T) {
	e := newDaemonEnv(t, portal.Config{}, false)
	d := e.daemon()
	var p PortalProbe
	if err := json.Unmarshal(d.want(200, "", "POST", "/api/v1/portal/probe", map[string]string{"portal": e.url}), &p); err != nil {
		t.Fatal(err)
	}
	if p.RegistrationOpen {
		t.Fatal("probe says registration is open")
	}
	d.want(403, "registration_closed", "POST", "/api/v1/auth/code", map[string]string{"portal": e.url, "email": "a@example.com"})
	if _, err := e.portal.CreateUser("bob@example.com", false); err != nil {
		t.Fatal(err)
	}
	if sent := d.requestCode(e.url, "bob@example.com"); sent.NewAccount {
		t.Fatalf("an existing account is new: %+v", sent)
	}
	d.want(200, "", "POST", "/api/v1/auth/verify", map[string]string{"portal": e.url, "email": "bob@example.com", "code": e.code("bob@example.com")})
	if s := d.session(); s.User != "bob@example.com" || !s.LoggedIn {
		t.Fatalf("session: %+v", s)
	}
}

// Signing in again: the same account (any case), the same device.
func TestDaemonSignInExisting(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	d := e.daemon()
	d.signIn(e, "alice@example.com")
	d.want(200, "", "POST", "/api/v1/logout", nil)
	if sent := d.requestCode(e.url, "ALICE@example.com"); sent.NewAccount {
		t.Fatalf("an existing account is new: %+v", sent)
	}
	d.want(200, "", "POST", "/api/v1/auth/verify", map[string]string{"portal": e.url, "email": "ALICE@example.com", "code": e.code("alice@example.com")})
	var me portal.Me
	if err := json.Unmarshal(d.want(200, "", "GET", "/api/v1/account", nil), &me); err != nil {
		t.Fatal(err)
	}
	if me.User.Username != "alice@example.com" || len(me.Devices) != 1 || me.Balance.AvailableMicros != 5_000_000 {
		t.Fatalf("me: %+v", me)
	}
}

func TestDaemonCodeGuessesThrottled(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	d := e.daemon()
	var last int
	for range 12 {
		last, _ = d.call("POST", "/api/v1/auth/verify", map[string]string{"portal": e.url, "email": "victim@example.com", "code": "000000"})
	}
	if last != 429 {
		t.Fatalf("last status %d, want 429", last)
	}
	d.want(429, "throttled", "POST", "/api/v1/auth/verify", map[string]string{"portal": e.url, "email": "victim@example.com", "code": "000000"})
}

// On a portal that asks new accounts for an invite code, the code request says
// so before any mail goes out, and the invite is used up by the sign-up.
func TestDaemonInviteRequired(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	admin := e.adminToken()
	if st := e.portalAPI("PUT", "/api/v1/admin/settings", admin, map[string]bool{"invite_required": true}, nil); st != 200 {
		t.Fatalf("settings: %d", st)
	}
	var inv portal.Invite
	if st := e.portalAPI("POST", "/api/v1/admin/invites", admin, map[string]int{"max_uses": 1}, &inv); st != 201 {
		t.Fatalf("invite: %d", st)
	}
	d := e.daemon()
	var p PortalProbe
	if err := json.Unmarshal(d.want(200, "", "POST", "/api/v1/portal/probe", map[string]string{"portal": e.url}), &p); err != nil || !p.InviteRequired {
		t.Fatalf("probe: %+v %v", p, err)
	}
	d.want(403, "invite_required", "POST", "/api/v1/auth/code", map[string]string{"portal": e.url, "email": "new@example.com"})
	d.want(403, "bad_invite", "POST", "/api/v1/auth/code", map[string]string{"portal": e.url, "email": "new@example.com", "invite_code": "NOPE"})
	d.want(200, "", "POST", "/api/v1/auth/code", map[string]string{"portal": e.url, "email": "new@example.com", "invite_code": inv.Code})
	d.want(200, "", "POST", "/api/v1/auth/verify", map[string]string{"portal": e.url, "email": "new@example.com", "code": e.code("new@example.com"), "invite_code": inv.Code})
	if s := d.session(); !s.LoggedIn || s.User != "new@example.com" {
		t.Fatalf("session: %+v", s)
	}
	// Used up: the next new address is refused.
	other := e.daemon()
	other.want(403, "bad_invite", "POST", "/api/v1/auth/code", map[string]string{"portal": e.url, "email": "late@example.com", "invite_code": inv.Code})
}

// An administrator's sign-in asks for the second factor without logging in,
// and the daemon keeps the challenge for POST /api/v1/auth/mfa.
func TestDaemonAdminTwoFactor(t *testing.T) {
	now := time.Now()
	e := newDaemonEnv(t, portal.Config{MFAKey: []byte(strings.Repeat("k", 32))}, false)
	if _, err := e.portal.CreateUser("root@example.com", true); err != nil {
		t.Fatal(err)
	}
	d := e.daemon()
	d.want(401, "challenge_expired", "POST", "/api/v1/auth/mfa", map[string]string{"code": "123456"})
	d.requestCode(e.url, "root@example.com")
	d.want(403, "mfa_setup_required", "POST", "/api/v1/auth/verify", map[string]string{"portal": e.url, "email": "root@example.com", "code": e.code("root@example.com")})

	secret := enrollMFA(t, e, "root@example.com")
	d.requestCode(e.url, "root@example.com")
	d.want(401, "mfa_required", "POST", "/api/v1/auth/verify", map[string]string{"portal": e.url, "email": "root@example.com", "code": e.code("root@example.com")})
	if d.session().LoggedIn {
		t.Fatal("logged in without the second factor")
	}
	d.want(401, "bad_code", "POST", "/api/v1/auth/mfa", map[string]string{"code": wrongCode(totp.Code(secret, now))})
	d.want(200, "", "POST", "/api/v1/auth/mfa", map[string]string{"code": totp.Code(secret, time.Now())})
	if s := d.session(); !s.LoggedIn || s.User != "root@example.com" {
		t.Fatalf("not logged in with the right code: %+v", s)
	}
	d.want(200, "", "GET", "/api/v1/account", nil)
	// The challenge is used up.
	d.want(409, "already_logged_in", "POST", "/api/v1/auth/mfa", map[string]string{"code": "123456"})
}

// A user who turned two-factor authentication on in the account settings is
// asked for it at the next sign-in.
func TestDaemonUserTwoFactor(t *testing.T) {
	e := newDaemonEnv(t, portal.Config{OpenRegistration: true, MFAKey: []byte(strings.Repeat("k", 32))}, false)
	d := e.daemon()
	d.signIn(e, "alice@example.com")
	tok := d.savedConfig().Token
	var setup struct {
		portal.MFASetup
		Challenge string `json:"challenge"`
	}
	if st := e.portalAPI("POST", "/api/v1/account/mfa/setup", tok, struct{}{}, &setup); st != 200 {
		t.Fatalf("setup: %d", st)
	}
	secret, err := totp.Decode(setup.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if st := e.portalAPI("POST", "/api/v1/account/mfa/enable", tok, map[string]string{"challenge": setup.Challenge, "code": totp.Code(secret, time.Now())}, nil); st != 200 {
		t.Fatalf("enable: %d", st)
	}
	nextStep()
	d.want(200, "", "POST", "/api/v1/logout", nil)
	d.requestCode(e.url, "alice@example.com")
	d.want(401, "mfa_required", "POST", "/api/v1/auth/verify", map[string]string{"portal": e.url, "email": "alice@example.com", "code": e.code("alice@example.com")})
	d.want(200, "", "POST", "/api/v1/auth/mfa", map[string]string{"code": totp.Code(secret, time.Now())})
	if s := d.session(); !s.LoggedIn || s.User != "alice@example.com" {
		t.Fatalf("session: %+v", s)
	}
}

func wrongCode(right string) string {
	if right == "000000" {
		return "000001"
	}
	return "000000"
}

// nextStep waits for the next TOTP step: a code once used is burnt.
func nextStep() {
	time.Sleep(time.Until(time.Unix((time.Now().Unix()/30+1)*30, 0)) + 50*time.Millisecond)
}

// enrollMFA runs an administrator's first sign-in on the web pages (a code by
// mail, then a challenge, a secret and a first TOTP code), which the desktop
// app leaves to the portal's web page.
func enrollMFA(t *testing.T, e *daemonEnv, user string) []byte {
	t.Helper()
	post := func(path string, in, out any) {
		t.Helper()
		data, _ := json.Marshal(in)
		req, _ := http.NewRequest("POST", e.url+path, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", e.url)
		req.Header.Set("X-Sharetoken-Web", "1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if out != nil {
			if err := json.Unmarshal(body, out); err != nil {
				t.Fatalf("%s: %s", path, body)
			}
		}
	}
	post("/api/v1/auth/code", map[string]string{"email": user}, nil)
	var ch struct {
		Challenge string `json:"challenge"`
	}
	post("/api/v1/web/auth/verify", map[string]string{"email": user, "code": e.code(user)}, &ch)
	var setup portal.MFASetup
	post("/api/v1/web/mfa/setup", map[string]string{"challenge": ch.Challenge}, &setup)
	secret, err := totp.Decode(setup.Secret)
	if err != nil {
		t.Fatalf("setup %+v: %v", setup, err)
	}
	post("/api/v1/web/mfa/confirm", map[string]string{"challenge": ch.Challenge, "code": totp.Code(secret, time.Now())}, nil)
	nextStep()
	return secret
}

// A self-signed portal: no address or code goes out until the user confirmed
// its fingerprint, the confirmation is kept, and a replaced certificate is caught.
func TestDaemonSelfSignedPortal(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), true)
	home := t.TempDir()
	d := e.daemonIn(home)
	var p PortalProbe
	if err := json.Unmarshal(d.want(200, "", "POST", "/api/v1/portal/probe", map[string]string{"portal": e.url + "/"}), &p); err != nil {
		t.Fatal(err)
	}
	if p.Trusted || !p.PinRequired || p.Known || p.PinChanged || !p.HTTPS || !strings.HasPrefix(p.Fingerprint, "sha256:") || p.Portal != e.url || !p.RegistrationOpen || p.SignupCredit != 5_000_000 {
		t.Fatalf("probe: %+v", p)
	}
	before := e.logins.Load()
	req := map[string]string{"portal": e.url, "email": "a@example.com"}
	d.want(400, "pin_required", "POST", "/api/v1/auth/code", req)
	req["pin"] = "sha256:" + strings.Repeat("0", 64)
	d.want(502, "pin_mismatch", "POST", "/api/v1/auth/code", req)
	req["code"] = "123456"
	d.want(502, "pin_mismatch", "POST", "/api/v1/auth/verify", req)
	if n := e.logins.Load(); n != before {
		t.Fatalf("the address was sent to an unconfirmed portal (%d requests)", n-before)
	}
	req["pin"] = strings.ToUpper(p.Fingerprint[:6]) + p.Fingerprint[6:] // any case
	d.want(200, "", "POST", "/api/v1/auth/code", req)
	req["code"] = e.code("a@example.com")
	d.want(200, "", "POST", "/api/v1/auth/verify", req)
	if c := d.savedConfig(); !strings.EqualFold(c.PortalPin, p.Fingerprint) {
		t.Fatalf("pin not saved: %+v", c)
	}
	d.want(200, "", "GET", "/api/v1/account", nil)
	d.want(200, "", "POST", "/api/v1/logout", nil)

	// The saved pin carries over: known, no confirmation asked.
	if err := json.Unmarshal(d.want(200, "", "POST", "/api/v1/portal/probe", map[string]string{"portal": e.url}), &p); err != nil {
		t.Fatal(err)
	}
	if !p.Known || p.PinRequired || p.PinChanged {
		t.Fatalf("second probe: %+v", p)
	}
	d.signIn(e, "a@example.com")
	d.Close()

	// The pin survives a restart of the daemon, and the session with it.
	d2 := e.daemonIn(home)
	if s := d2.session(); !s.LoggedIn || s.User != "a@example.com" {
		t.Fatalf("after restart: %+v", s)
	}
	d2.want(200, "", "GET", "/api/v1/account", nil)
	d2.want(200, "", "POST", "/api/v1/logout", nil)

	// Someone else answers at that address now.
	c := d2.savedConfig()
	c.PortalPin = "sha256:" + strings.Repeat("a", 64)
	if err := SaveConfig(home, c); err != nil {
		t.Fatal(err)
	}
	d2.Close()
	d3 := e.daemonIn(home)
	if err := json.Unmarshal(d3.want(200, "", "POST", "/api/v1/portal/probe", map[string]string{"portal": e.url}), &p); err != nil {
		t.Fatal(err)
	}
	if !p.PinChanged || !p.PinRequired || p.Known {
		t.Fatalf("probe after a certificate change: %+v", p)
	}
	before = e.logins.Load()
	d3.want(502, "pin_mismatch", "POST", "/api/v1/auth/code", map[string]string{"portal": e.url, "email": "a@example.com"})
	if e.logins.Load() != before {
		t.Fatal("the address was sent despite the changed certificate")
	}
}

func TestDaemonProbeErrors(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	d := e.daemon()
	d.want(502, "portal_unreachable", "POST", "/api/v1/portal/probe", map[string]string{"portal": " "}) // blank: the built-in address (TestMain: nothing listens there)
	d.want(400, "insecure_portal", "POST", "/api/v1/portal/probe", map[string]string{"portal": "http://203.0.113.7"})
	d.want(502, "portal_unreachable", "POST", "/api/v1/portal/probe", map[string]string{"portal": "https://127.0.0.1:1"})
	d.want(502, "portal_unreachable", "POST", "/api/v1/portal/probe", map[string]string{"portal": "http://127.0.0.1:1"})
	notPortal := httptest.NewServer(http.NotFoundHandler())
	defer notPortal.Close()
	d.want(502, "not_a_portal", "POST", "/api/v1/portal/probe", map[string]string{"portal": notPortal.URL})
	// -allow-http lets a test network through: the address is tried, not refused.
	d.AllowHTTP = true
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	var ae *apiError
	if _, err := d.probe(ctx, "http://192.0.2.1:1"); !errors.As(err, &ae) || ae.Code != "portal_unreachable" {
		t.Errorf("with AllowHTTP: %v", err)
	}
}

func TestNormalizePortalURL(t *testing.T) {
	for in, want := range map[string]string{
		"portal.example.com":             "https://portal.example.com",
		" https://portal.example.com/ ":  "https://portal.example.com",
		"http://127.0.0.1:8092//":        "http://127.0.0.1:8092",
		"https://[::1]:8092":             "https://[::1]:8092",
		"https://example.com/st/portal/": "https://example.com/st/portal",
	} {
		if got, err := NormalizePortalURL(in); err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "   ", "https://", "file:///etc/passwd", "https://u:p@x", "https://x?y=1", "https://x#f", "javascript://x"} {
		if got, err := NormalizePortalURL(in); err == nil {
			t.Errorf("%q accepted as %q", in, got)
		}
	}
}

// Logging out stops using the token everywhere, and keeps the other settings.
func TestDaemonLogout(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	d := e.daemon()
	s := d.signIn(e, "alice@example.com")
	token := d.savedConfig().Token
	if err := func() error { c := d.savedConfig(); c.Buy.BudgetMicros = 123; return SaveConfig(d.home, c) }(); err != nil {
		t.Fatal(err)
	}
	d.want(200, "", "POST", "/api/v1/logout", nil)
	after := d.session()
	if after.LoggedIn || after.State != StateLoggedOut || after.User != s.User || after.Portal != PortalAddr() || after.Peer != "" {
		t.Fatalf("session: %+v", after)
	}
	c := d.savedConfig()
	if c.Token != "" || c.User != s.User || c.Buy.BudgetMicros != 123 {
		t.Fatalf("config: %+v", c)
	}
	if _, err := NewPortalClient(e.url, token, "").Me(context.Background()); !IsUnauthorized(err) {
		t.Fatalf("the token still works at the portal: %v", err)
	}
	d.want(401, "not_logged_in", "GET", "/api/v1/account", nil)
	d.want(200, "", "POST", "/api/v1/logout", nil)
}

// With the portal gone, logging out still works locally.
func TestDaemonLogoutPortalDown(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	d := e.daemon()
	d.signIn(e, "alice@example.com")
	e.srv.Close()
	d.want(502, "portal_unreachable", "GET", "/api/v1/account", nil)
	if s := d.session(); !s.LoggedIn {
		t.Fatalf("an unreachable portal must not log the user out: %+v", s)
	}
	d.want(200, "", "POST", "/api/v1/logout", nil)
	if d.savedConfig().Token != "" {
		t.Fatal("token kept")
	}
}

// A token revoked elsewhere ends the session at the next portal call and on
// the next start, and the user can sign in again.
func TestDaemonTokenRevoked(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	home := t.TempDir()
	d := e.daemonIn(home)
	s := d.signIn(e, "alice@example.com")
	revokeAll := func() {
		t.Helper()
		u, err := e.portal.Store().UserByName(s.User)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.portal.Store().RevokeAllTokens(u.ID); err != nil {
			t.Fatal(err)
		}
	}
	revokeAll()
	d.want(401, "unauthorized", "GET", "/api/v1/account", nil)
	got := d.waitState(StateExpired)
	if got.LoggedIn || got.Error == "" || got.User != s.User {
		t.Fatalf("session: %+v", got)
	}
	if d.savedConfig().Token != "" {
		t.Fatal("rejected token kept")
	}
	d.signIn(e, s.User)
	if st := d.session(); !st.LoggedIn || st.Error != "" {
		t.Fatalf("session after new login: %+v", st)
	}
	d.want(200, "", "GET", "/api/v1/account", nil)

	// A reply about the old token, arriving late, must not end the new login.
	d.expire("st_old", "late")
	if !d.session().LoggedIn {
		t.Fatal("a stale rejection ended the current login")
	}

	revokeAll()
	d.Close()
	d2 := e.daemonIn(home)
	if got := d2.session(); got.State == StateRunning {
		t.Fatalf("session: %+v", got)
	}
	d2.want(401, "unauthorized", "GET", "/api/v1/account", nil)
	d2.waitState(StateExpired)
}

// A disabled user is treated like a revoked login.
func TestDaemonUserDisabled(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	d := e.daemon()
	s := d.signIn(e, "alice@example.com")
	u, _ := e.portal.Store().UserByName(s.User)
	if err := e.portal.Store().SetDisabled(u.ID, true); err != nil {
		t.Fatal(err)
	}
	d.call("GET", "/api/v1/account", nil)
	d.waitState(StateExpired)
	d.want(403, "account_disabled", "POST", "/api/v1/auth/code", map[string]string{"portal": e.url, "email": s.User})
}

// Someone else signs in on this machine: their account gets its own device key.
func TestDaemonSwitchAccounts(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	d := e.daemon()
	alice := d.signIn(e, "alice@example.com")
	d.want(200, "", "POST", "/api/v1/logout", nil)
	bob := d.signIn(e, "bob@example.com")
	if bob.Peer == "" || bob.Peer == alice.Peer {
		t.Fatalf("peers: %s %s", alice.Peer, bob.Peer)
	}
	retired, _ := filepath.Glob(filepath.Join(d.home, KeyFile+".retired-*"))
	if len(retired) != 1 {
		t.Fatalf("retired keys: %v", retired)
	}
	if err := secfile.CheckPrivate(filepath.Join(d.home, KeyFile)); err != nil {
		t.Fatal(err)
	}
	devices := func(name string) []accounts.Device {
		u, _ := e.portal.Store().UserByName(name)
		ds, err := e.portal.Store().Devices(u.ID)
		if err != nil {
			t.Fatal(err)
		}
		return ds
	}
	if ds := devices("bob@example.com"); len(ds) != 1 || ds[0].Peer != bob.Peer {
		t.Fatalf("bob's devices: %+v", ds)
	}
	if ds := devices("alice@example.com"); len(ds) != 1 || ds[0].Peer != alice.Peer {
		t.Fatalf("alice's devices: %+v", ds)
	}
	// And back: alice can sign in here again.
	d.want(200, "", "POST", "/api/v1/logout", nil)
	d.signIn(e, "alice@example.com")
	d.want(200, "", "GET", "/api/v1/account", nil)
}

// A failed binding leaves nobody logged in and no token behind.
func TestDaemonBindFailureRevokesToken(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	var bindFails atomic.Bool
	bindFails.Store(true)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/devices" && r.Method == "POST" && bindFails.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		e.portal.Handler().ServeHTTP(w, r)
	}))
	defer front.Close()
	d := e.daemon()
	d.want(200, "", "POST", "/api/v1/auth/code", map[string]string{"portal": front.URL, "email": "a@example.com"})
	d.want(500, "bind_failed", "POST", "/api/v1/auth/verify", map[string]string{"portal": front.URL, "email": "a@example.com", "code": e.code("a@example.com")})
	if s := d.session(); s.LoggedIn {
		t.Fatalf("session: %+v", s)
	}
	if d.savedConfig().Token != "" {
		t.Fatal("token saved")
	}
	u, err := e.portal.Store().UserByName("a@example.com")
	if err != nil {
		t.Fatal(err)
	}
	ts, err := e.portal.Store().Tokens(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 0 {
		t.Fatalf("tokens left active: %+v", ts)
	}
	// The account exists; signing in again finishes the job.
	bindFails.Store(false)
	d.want(200, "", "POST", "/api/v1/auth/code", map[string]string{"portal": front.URL, "email": "a@example.com"})
	d.want(200, "", "POST", "/api/v1/auth/verify", map[string]string{"portal": front.URL, "email": "a@example.com", "code": e.code("a@example.com")})
}

// Web pages get nothing from this API; the desktop shell and CLI tools do.
func TestDaemonRequestGuards(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	d := e.daemon()
	check := func(want int, method, path string, body any, hdr ...string) {
		t.Helper()
		if got, data := d.call(method, path, body, hdr...); got != want {
			t.Errorf("%s %s %v: %d, want %d: %s", method, path, hdr, got, want, data)
		}
	}
	login := map[string]string{"portal": e.url, "email": "x@example.com", "code": "000000"}
	check(403, "POST", "/api/v1/auth/verify", login, "Origin", "https://evil.example.com")
	check(403, "POST", "/api/v1/logout", nil, "Origin", "https://evil.example.com")
	check(403, "POST", "/api/v1/auth/verify", login, "Origin", "null")
	check(403, "GET", "/api/v1/session", nil, "Origin", "http://evil.localhost.example.com")
	check(401, "POST", "/api/v1/auth/verify", login, "Origin", d.base)
	check(401, "POST", "/api/v1/auth/verify", login) // CLI tools and the desktop shell send no Origin
	for _, o := range []string{"http://localhost:3000", "http://127.0.0.1:3000", "chrome-extension://abc", "wails://wails"} {
		check(403, "POST", "/api/v1/auth/verify", login, "Origin", o)
		check(403, "POST", "/api/v1/logout", nil, "Origin", o)
		check(403, "GET", "/api/v1/account", nil, "Origin", o)
		// The OpenAI endpoint stays open to local tools, as on `sharetoken up`.
		check(401, "POST", "/v1/chat/completions", map[string]string{}, "Origin", o)
	}

	// DNS rebinding: a same-origin request, by the browser's reckoning.
	check(403, "GET", "/api/v1/session", nil, "Host", "evil.example.com")
	check(403, "GET", "/api/v1/session", nil, "Host", "evil.example.com:9787")
	check(403, "GET", "/", nil, "Host", "127.0.0.1.evil.example.com")
	check(200, "GET", "/api/v1/session", nil, "Host", "localhost:9787")
	check(200, "GET", "/api/v1/session", nil, "Host", "[::1]:9787")

	// What a cross-site form could send.
	req, _ := http.NewRequest("POST", d.base+"/api/v1/auth/verify", strings.NewReader(`{"portal":"x"}`))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Authorization", "Bearer "+d.Key())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain: %d", resp.StatusCode)
	}
	// Oversized and malformed bodies.
	big := strings.NewReader(`{"portal":"` + strings.Repeat("x", 1<<20) + `"}`)
	req, _ = http.NewRequest("POST", d.base+"/api/v1/auth/verify", big)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+d.Key())
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("oversized body: %d", resp.StatusCode)
	}
	// The wrong method on a sign-in route does nothing (it falls through to the
	// agent's routes, and there is no agent).
	check(401, "GET", "/api/v1/auth/verify", nil)
	check(401, "PUT", "/api/v1/session", map[string]string{})
}

// Loopback is open to every program and user of the machine: the management
// API wants the key from run.json, and the hello proves the daemon holds it.
func TestDaemonLocalKey(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	d := e.daemon()
	addr, key := LocalAPI(d.home, Config{})
	if addr != d.Addr() || key != d.Key() || len(key) != 2*localKeyBytes {
		t.Fatalf("run.json: %s %q", addr, key)
	}
	if info, err := os.Stat(filepath.Join(d.home, RunFile)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("run.json mode: %v %v", info, err)
	}
	noKey := []string{"Authorization", ""}
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/v1/session"}, {"GET", "/api/v1/status"}, {"GET", "/api/v1/account"}, {"POST", "/api/v1/logout"},
		{"GET", "/api/v1/providers"}, {"POST", "/api/v1/auth/code"}, {"POST", "/api/v1/hello"},
		{"GET", "/api/v1/../api/v1/session"}, {"GET", "/ui/../api/v1/session"}, {"GET", "/v1/../api/v1/session"}, {"GET", "/nothing"},
	} {
		if code, data := d.call(c.method, c.path, nil, noKey...); code != 401 || !strings.Contains(string(data), "local_auth") {
			t.Errorf("%s %s without the key: %d %s", c.method, c.path, code, data)
		}
	}
	d.want(401, "local_auth", "GET", "/api/v1/session", nil, "Authorization", "Bearer "+strings.Repeat("0", 64))
	d.want(401, "local_auth", "GET", "/api/v1/session", nil, "Authorization", key)
	d.want(200, "", "GET", "/api/v1/session", nil)
	// Open without it: the page, its files and the hello. The OpenAI endpoint
	// wants the API key instead (TestDaemonAPIKey), not this one.
	for _, p := range []string{"/", "/ui/app.js"} {
		d.want(200, "", "GET", p, nil, noKey...)
	}
	d.want(401, "invalid_api_key", "GET", "/v1/models", nil, "Authorization", "Bearer "+key)
	nonce := strings.Repeat("ab", 32)
	var h Hello
	if code, data := d.call("GET", "/api/v1/hello?nonce="+nonce, nil, noKey...); code != 200 || json.Unmarshal(data, &h) != nil {
		t.Fatalf("hello: %d %s", code, data)
	}
	if h.Env != Env || h.Version != version.Version || h.Proof != LocalProof(key, d.Addr(), nonce) || h.Proof == LocalProof(key, "127.0.0.1:1", nonce) {
		t.Fatalf("hello: %+v", h)
	}
	for _, n := range []string{"", "abc", strings.Repeat("zz", 32), strings.Repeat("ab", 65)} {
		d.want(400, "bad_request", "GET", "/api/v1/hello?nonce="+n, nil, noKey...)
	}
	// A new start, a new key.
	d.Close()
	d2 := e.daemonIn(d.home)
	if d2.Key() == key {
		t.Fatal("the key did not change")
	}
}

// The OpenAI endpoint wants the API key from apikey.json unless the user
// turned it off; a change to the file reaches the running daemon.
func TestDaemonAPIKey(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	d := e.daemon()
	path := filepath.Join(d.home, APIKeyFile)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("apikey.json: %v %v", info, err)
	}
	s, err := LoadAPIKey(d.home)
	if err != nil || !s.Required || !strings.HasPrefix(s.Key, "sk-st-") || s.Key != d.APIKey() {
		t.Fatalf("new home: %+v %v", s, err)
	}
	noKey := []string{"Authorization", ""}
	d.want(401, "invalid_api_key", "GET", "/v1/models", nil, noKey...)
	d.want(401, "invalid_api_key", "GET", "/v1/models", nil, "Authorization", "Bearer sk-st-"+strings.Repeat("0", 48))
	d.want(401, "invalid_api_key", "GET", "/v1/models", nil, "Authorization", "Bearer "+d.Key()) // the management API's key is not this one
	d.want(401, "not_logged_in", "GET", "/v1/models", nil)                                      // with the key, on to the agent
	d.want(401, "not_logged_in", "GET", "/v1/models", nil, "Authorization", "", "X-Api-Key", s.Key)

	// The management API shows and changes it, with its own key only.
	var v apiKeyView
	if code, data := d.call("GET", "/api/v1/apikey", nil); code != 200 || json.Unmarshal(data, &v) != nil || v.Key != s.Key || !v.Required {
		t.Fatalf("GET: %d %s", code, data)
	}
	d.want(401, "local_auth", "GET", "/api/v1/apikey", nil, noKey...)
	d.want(400, "bad_request", "PUT", "/api/v1/apikey", map[string]string{})
	d.want(200, "", "PUT", "/api/v1/apikey", map[string]bool{"required": false})
	d.want(401, "not_logged_in", "GET", "/v1/models", nil, noKey...)
	d.want(403, "", "GET", "/v1/models", nil, "Authorization", "", "Origin", "https://evil.example.com") // web pages stay out all the same

	// `sharetoken apikey on` writes the file; the daemon reads it again.
	if _, err := SetAPIKeyRequired(d.home, true); err != nil {
		t.Fatal(err)
	}
	d.want(401, "invalid_api_key", "GET", "/v1/models", nil, noKey...)

	// A new key: the old one stops working.
	var r apiKeyView
	if code, data := d.call("POST", "/api/v1/apikey/rotate", nil); code != 200 || json.Unmarshal(data, &r) != nil || r.Key == s.Key || !validAPIKey(r.Key) {
		t.Fatalf("rotate: %d %s", code, data)
	}
	d.want(401, "invalid_api_key", "GET", "/v1/models", nil, "Authorization", "Bearer "+s.Key)
	d.want(401, "not_logged_in", "GET", "/v1/models", nil, "Authorization", "Bearer "+r.Key)

	// A damaged file lets nothing in, and says what to do.
	if err := os.WriteFile(path, []byte(`{"key":"nope","required":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, data := d.call("GET", "/v1/models", nil, noKey...); !strings.Contains(string(data), "damaged") {
		t.Fatalf("damaged: %s", data)
	}
	// The key outlives the daemon.
	os.Remove(path)
	d.Close()
	d2 := e.daemonIn(d.home)
	k2 := d2.APIKey()
	d2.Close()
	d3 := e.daemonIn(d.home)
	if k2 == "" || d3.APIKey() != k2 {
		t.Fatal("the key changed with a restart")
	}
}

// The API listens on loopback only.
func TestDaemonRefusesNonLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", ":0", "192.0.2.1:0", "example.com:80", "nonsense"} {
		d := NewDaemon(t.TempDir())
		d.HTTP = addr
		if err := d.Start(); err == nil {
			d.Close()
			t.Errorf("%s accepted", addr)
		}
	}
}

func TestDaemonPortInUse(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	d := e.daemon()
	other := NewDaemon(t.TempDir())
	other.HTTP = d.Addr()
	if err := other.Start(); err == nil {
		other.Close()
		t.Fatal("second daemon on the same port")
	}
}

// The pages come with a CSP that allows no inline code and nothing from
// elsewhere, and hold no inline code that would need it.
func TestDaemonServesApp(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	d := e.daemon()
	for path, ct := range map[string]string{"/": "text/html", "/ui/app.js": "javascript", "/ui/app.css": "text/css", "/ui/i18n.js": "javascript", "/ui/i18n.css": "text/css", "/ui/i18n/client/en.json": "application/json"} {
		resp, err := http.Get(d.base + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), ct) || len(body) == 0 {
			t.Errorf("%s: %d %s", path, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		csp := resp.Header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") || strings.Contains(csp, "unsafe") {
			t.Errorf("%s: CSP %q", path, csp)
		}
		if path == "/" && (bytes.Contains(body, []byte("<script>")) || bytes.Contains(body, []byte("onclick")) || bytes.Contains(body, []byte("http"))) {
			t.Errorf("index.html has inline code or remote references: %s", body)
		}
		if path == "/ui/app.js" && (bytes.Contains(body, []byte("innerHTML")) || bytes.Contains(body, []byte("eval("))) {
			t.Error("app.js must build the DOM with textContent only")
		}
	}
	d.want(404, "", "GET", "/ui/../daemon.go", nil)
	d.want(404, "", "GET", "/ui/nothing.js", nil)
	d.want(404, "", "GET", "/ui/i18n/client/nope.json", nil)
	d.want(400, "", "GET", "/ui/i18n/client/..%2f..%2fapp.js", nil) // no way out of the dictionaries
}

// Sign-in codes and tokens stay out of the logs.
func TestDaemonLogsNoSecrets(t *testing.T) {
	var buf safeBuffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(old)
	e := newDaemonEnv(t, openPortal(), false)
	d := e.daemon()
	d.signIn(e, "alice@example.com")
	token := d.savedConfig().Token
	d.waitState(StateRetrying)
	d.want(200, "", "POST", "/api/v1/logout", nil)
	d.requestCode(e.url, "alice@example.com")
	code := e.code("alice@example.com")
	d.want(401, "bad_code", "POST", "/api/v1/auth/verify", map[string]string{"portal": e.url, "email": "alice@example.com", "code": "x" + code})
	d.want(200, "", "POST", "/api/v1/auth/verify", map[string]string{"portal": e.url, "email": "alice@example.com", "code": code})
	d.Close()
	logs := buf.String()
	if !strings.Contains(logs, "signed in") {
		t.Fatalf("no logs captured: %s", logs)
	}
	for _, secret := range []string{code, token, d.Key()} {
		if strings.Contains(logs, secret) {
			t.Errorf("log contains %q:\n%s", secret, logs)
		}
	}
}

type safeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// Double-clicks and racing windows: exactly one sign-in wins, and racing
// logouts and reads leave a consistent state (run with -race).
func TestDaemonConcurrentSignIn(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	d := e.daemon()
	var wg sync.WaitGroup
	var ok, conflict atomic.Int64
	d.requestCode(e.url, "race@example.com")
	code := e.code("race@example.com")
	for range 6 {
		wg.Go(func() {
			status, data := d.call("POST", "/api/v1/auth/verify", map[string]string{"portal": e.url, "email": "race@example.com", "code": code})
			switch status {
			case 200:
				ok.Add(1)
			case 409:
				conflict.Add(1)
			default:
				t.Errorf("status %d: %s", status, data)
			}
		})
	}
	wg.Wait()
	if ok.Load() != 1 || conflict.Load() != 5 {
		t.Fatalf("ok %d conflict %d", ok.Load(), conflict.Load())
	}
	for i := range 4 {
		wg.Go(func() {
			for range 5 {
				d.call("GET", "/api/v1/session", nil)
				d.call("GET", "/api/v1/account", nil)
				d.call("GET", "/api/v1/status", nil)
				if i == 0 {
					d.call("POST", "/api/v1/logout", nil)
					d.call("POST", "/api/v1/auth/code", map[string]string{"portal": e.url, "email": "race@example.com"})
					d.call("POST", "/api/v1/auth/verify", map[string]string{"portal": e.url, "email": "race@example.com", "code": e.code("race@example.com")})
				}
			}
		})
	}
	wg.Wait()
	s := d.session()
	if s.LoggedIn != (d.savedConfig().Token != "") {
		t.Fatalf("session %+v disagrees with the saved config", s)
	}
}

// The sign-in page never asks for the portal: an empty address means the
// built-in one (the build type's, or $SHARETOKEN_PORTAL on a test network), and
// the session reports it for a logged-out app.
func TestDaemonBuiltInPortal(t *testing.T) {
	t.Setenv("SHARETOKEN_PORTAL", "")
	if PortalAddr() != "https://dev.sharetoken.app" {
		t.Fatalf("PortalAddr() = %q", PortalAddr())
	}
	e := newDaemonEnv(t, openPortal(), false)
	t.Setenv("SHARETOKEN_PORTAL", e.url)
	d := e.daemon()
	if s := d.session(); s.LoggedIn || s.Portal != e.url {
		t.Fatalf("logged-out session: %+v", s)
	}
	var p PortalProbe
	if err := json.Unmarshal(d.want(200, "", "POST", "/api/v1/portal/probe", map[string]string{}), &p); err != nil || p.Portal != e.url {
		t.Fatalf("probe without an address: %+v %v", p, err)
	}
	d.want(200, "", "POST", "/api/v1/auth/code", map[string]string{"email": "builtin@example.com"})
	d.want(200, "", "POST", "/api/v1/auth/verify", map[string]string{"email": "builtin@example.com", "code": e.code("builtin@example.com"), "device": "test-box"})
	if c := d.savedConfig(); c.Portal != e.url {
		t.Fatalf("saved portal = %q, want the built-in %q", c.Portal, e.url)
	}
}

// The probe tells the app what the sign-in page needs: where to write for
// help, and that this portal signs in with mailed codes.
func TestDaemonProbeSupport(t *testing.T) {
	cfg := openPortal()
	cfg.Mail = portal.MailConfig{From: "ShareToken <team@sharetoken.test>", Support: "Support <support@sharetoken.test>"}
	e := newDaemonEnv(t, cfg, false)
	d := e.daemon()
	var p PortalProbe
	if err := json.Unmarshal(d.want(200, "", "POST", "/api/v1/portal/probe", map[string]string{"portal": e.url}), &p); err != nil {
		t.Fatal(err)
	}
	if p.SupportEmail != "support@sharetoken.test" || !p.RegistrationOpen || p.InviteRequired {
		t.Fatalf("probe: %+v", p)
	}
}

// The daemon forwards feedback with the login it holds, and it, not the page,
// says where the message comes from.
func TestDaemonFeedback(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	d := e.daemon()
	body := map[string]string{"category": "bug", "message": "it broke", "lang": "de", "source": "portal", "app_version": "forged"}
	d.want(401, "not_logged_in", "POST", "/api/v1/feedback", body)
	d.signIn(e, "fb@example.com")
	d.want(201, "", "POST", "/api/v1/feedback", body)
	d.want(400, "bad_request", "POST", "/api/v1/feedback", map[string]string{"category": "bug", "message": "  "})

	var list struct {
		Feedback []accounts.Feedback `json:"feedback"`
	}
	if err := json.Unmarshal(d.want(200, "", "GET", "/api/v1/feedback/mine", nil), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Feedback) != 1 {
		t.Fatalf("feedback: %+v", list.Feedback)
	}
	f := list.Feedback[0]
	if f.Message != "it broke" || f.Lang != "de" || f.Source != "client" || f.AppVersion != version.Version || f.OS != runtime.GOOS+"/"+runtime.GOARCH {
		t.Fatalf("feedback: %+v", f)
	}
}

// The settings page changes the budget and require_native: the running
// endpoint takes them at once and config.json keeps them.
func TestDaemonBuySettings(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	d := e.daemon()
	d.want(401, "not_logged_in", "GET", "/api/v1/buy", nil)
	d.signIn(e, "buyer@example.com")
	var v BuySettingsView
	if err := json.Unmarshal(d.want(200, "", "GET", "/api/v1/buy", nil), &v); err != nil || v.BudgetMicros != DefaultBudgetMicros || v.RequireNative {
		t.Fatalf("defaults: %+v %v", v, err)
	}
	if err := json.Unmarshal(d.want(200, "", "PUT", "/api/v1/buy", BuySettingsView{BudgetMicros: 5_000_000, RequireNative: true}), &v); err != nil || v.BudgetMicros != 5_000_000 || !v.RequireNative {
		t.Fatalf("set: %+v %v", v, err)
	}
	c, err := LoadConfig(d.home)
	if err != nil || c.Buy.BudgetMicros != 5_000_000 || !c.Buy.RequireNative {
		t.Fatalf("saved: %+v %v", c.Buy, err)
	}
	d.want(400, "bad_request", "PUT", "/api/v1/buy", BuySettingsView{BudgetMicros: 5})
	d.want(400, "bad_request", "PUT", "/api/v1/buy", map[string]any{"budget_micros": "lots"})

	// Carrying the platform's spot checks is on unless the user turns it
	// off; a change that does not mention it (an older page) keeps it.
	if v.SpotChecks == nil || !*v.SpotChecks || !c.Buy.CarriesSpotChecks() {
		t.Fatalf("spot checks by default: %+v", v)
	}
	off := false
	if err := json.Unmarshal(d.want(200, "", "PUT", "/api/v1/buy", BuySettingsView{BudgetMicros: 5_000_000, SpotChecks: &off}), &v); err != nil || v.SpotChecks == nil || *v.SpotChecks {
		t.Fatalf("turning spot checks off: %+v %v", v, err)
	}
	if err := json.Unmarshal(d.want(200, "", "PUT", "/api/v1/buy", map[string]any{"budget_micros": 4_000_000}), &v); err != nil || v.SpotChecks == nil || *v.SpotChecks || v.BudgetMicros != 4_000_000 {
		t.Fatalf("a change without spot_checks: %+v %v", v, err)
	}
	if c, err := LoadConfig(d.home); err != nil || c.Buy.CarriesSpotChecks() {
		t.Fatalf("saved: %+v %v", c.Buy, err)
	}
}
