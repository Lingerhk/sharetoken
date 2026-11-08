package client

import (
	"bytes"
	"context"
	"crypto/tls"
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

	"sharetoken/accounts"
	"sharetoken/internal/secfile"
	"sharetoken/internal/mysqldb"
	"sharetoken/internal/totp"
	"sharetoken/portal"
)

const testPassword = "correct horse battery"

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
	srv    *httptest.Server
	url    string
	logins atomic.Int64 // requests the portal saw on its sign-in endpoints
}

func newDaemonEnv(t *testing.T, cfg portal.Config, useTLS bool) *daemonEnv {
	t.Helper()
	db, err := mysqldb.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg.DB = db
	p, err := portal.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	e := &daemonEnv{t: t, portal: p}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/login" || r.URL.Path == "/api/v1/register" {
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
	for i := 0; i+1 < len(hdr); i += 2 {
		if hdr[i] == "Host" {
			req.Host = hdr[i+1]
		} else {
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
func (d *testDaemon) want(status int, code, method, path string, body any) []byte {
	d.t.Helper()
	got, data := d.call(method, path, body)
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
	if err := json.Unmarshal(d.want(200, "", "GET", "/api/session", nil), &s); err != nil {
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

func (d *testDaemon) register(portalURL, email string) Session {
	d.t.Helper()
	var s Session
	data := d.want(201, "", "POST", "/api/register", map[string]string{"portal": portalURL, "email": email, "password": testPassword, "device": "test-box"})
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

// Logged out, the daemon runs and says so; nothing needing a login works.
func TestDaemonLoggedOut(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	d := e.daemon()
	s := d.session()
	if s.State != StateLoggedOut || s.LoggedIn || s.User != "" || !strings.HasSuffix(s.API, "/v1") {
		t.Fatalf("session: %+v", s)
	}
	d.want(401, "not_logged_in", "GET", "/api/account", nil)
	d.want(401, "not_logged_in", "GET", "/api/jobs", nil)
	d.want(401, "not_logged_in", "GET", "/api/ledger", nil)
	d.want(401, "not_logged_in", "GET", "/api/status", nil)
	d.want(401, "not_logged_in", "GET", "/api/models", nil)
	d.want(401, "not_logged_in", "POST", "/v1/chat/completions", map[string]string{})
	d.want(401, "not_logged_in", "POST", "/api/share/start", nil)
	d.want(200, "", "POST", "/api/logout", nil) // nothing to do, not an error
}

// The whole sign-up: email and password in, a bound device and a usable
// session out, with the account readable although no network can be joined.
func TestDaemonRegisterWithEmail(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	d := e.daemon()
	s := d.register(e.url, "  Alice.Smith+st@Example.COM ")
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
	d.want(503, "not_connected", "GET", "/api/status", nil)
	var me portal.Me
	if err := json.Unmarshal(d.want(200, "", "GET", "/api/account", nil), &me); err != nil {
		t.Fatal(err)
	}
	if me.User.Username != s.User || me.Balance.AvailableMicros != 5_000_000 || len(me.Devices) != 1 || me.Devices[0].Peer != s.Peer || me.Devices[0].Name != "test-box" {
		t.Fatalf("me: %+v", me)
	}
	d.want(200, "", "GET", "/api/jobs?limit=5", nil)
	d.want(200, "", "GET", "/api/ledger", nil)

	// The token never leaves the daemon.
	for _, path := range []string{"/api/session", "/api/account"} {
		if _, data := d.call("GET", path, nil); bytes.Contains(data, []byte(cfg.Token)) || bytes.Contains(data, []byte(`"token"`)) {
			t.Fatalf("%s leaks the token: %s", path, data)
		}
	}
	// A second login without logging out is refused.
	d.want(409, "already_logged_in", "POST", "/api/login", map[string]string{"portal": e.url, "account": s.User, "password": testPassword})
}

// Every way a sign-up can be refused comes back with a code the app handles.
func TestDaemonRegisterErrors(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	d := e.daemon()
	reg := func(status int, code, portalURL, email, password string) {
		t.Helper()
		d.want(status, code, "POST", "/api/register", map[string]string{"portal": portalURL, "email": email, "password": password})
		if s := d.session(); s.LoggedIn {
			t.Fatalf("logged in after a failed sign-up: %+v", s)
		}
		if c := d.savedConfig(); c.Token != "" {
			t.Fatal("token saved after a failed sign-up")
		}
	}
	for _, bad := range []string{"plainname", "a@b", "a b@example.com", "@example.com", "a@", "a@@example.com", "a..b@example.com", ".a@example.com", "a@-x.com", strings.Repeat("a", 200) + "@example.com", "<a@example.com>", "Alice <a@example.com>", "a@example.com\nbcc:x@y.z"} {
		reg(400, "bad_request", e.url, bad, testPassword)
	}
	reg(400, "bad_request", e.url, "", testPassword)
	reg(400, "bad_request", e.url, "short@example.com", "1234567")
	reg(400, "bad_request", e.url, "nopass@example.com", "")
	t.Setenv("SHARETOKEN_PORTAL", "ftp://not-a-portal.example.com")
	reg(400, "bad_portal", "", "a@example.com", testPassword) // empty: the built-in address, here a bad one
	t.Setenv("SHARETOKEN_PORTAL", "https://127.0.0.1:1")
	reg(400, "bad_portal", "ftp://x.example.com", "a@example.com", testPassword)
	reg(400, "bad_portal", "https://user:pw@x.example.com", "a@example.com", testPassword)
	reg(400, "insecure_portal", "http://203.0.113.7:8092", "a@example.com", testPassword)
	reg(502, "portal_unreachable", "https://127.0.0.1:1", "a@example.com", testPassword)

	// The address is taken, in any capitalization.
	other := e.daemon()
	other.register(e.url, "taken@example.com")
	reg(409, "exists", e.url, "Taken@Example.com", testPassword)
	d.want(400, "bad_request", "POST", "/api/register", json.RawMessage(`{"portal":`+"3"+`}`))
}

func TestDaemonRegistrationClosed(t *testing.T) {
	e := newDaemonEnv(t, portal.Config{}, false)
	d := e.daemon()
	var p PortalProbe
	if err := json.Unmarshal(d.want(200, "", "POST", "/api/portal/probe", map[string]string{"portal": e.url}), &p); err != nil {
		t.Fatal(err)
	}
	if p.RegistrationOpen {
		t.Fatal("probe says registration is open")
	}
	d.want(403, "registration_closed", "POST", "/api/register", map[string]string{"portal": e.url, "email": "a@example.com", "password": testPassword})
	// An account an administrator made still logs in, by username.
	if _, err := e.portal.CreateUser("bob", testPassword, false); err != nil {
		t.Fatal(err)
	}
	d.want(200, "", "POST", "/api/login", map[string]string{"portal": e.url, "account": "bob", "password": testPassword})
	if s := d.session(); s.User != "bob" || !s.LoggedIn {
		t.Fatalf("session: %+v", s)
	}
}

func TestDaemonLogin(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	d := e.daemon()
	d.register(e.url, "alice@example.com")
	d.want(200, "", "POST", "/api/logout", nil)
	d.want(401, "bad_credentials", "POST", "/api/login", map[string]string{"portal": e.url, "account": "alice@example.com", "password": "wrong password"})
	d.want(401, "bad_credentials", "POST", "/api/login", map[string]string{"portal": e.url, "account": "nobody@example.com", "password": testPassword})
	d.want(400, "bad_request", "POST", "/api/login", map[string]string{"portal": e.url, "account": "  ", "password": testPassword})
	// Case does not matter, and the same device stays bound.
	d.want(200, "", "POST", "/api/login", map[string]string{"portal": e.url, "account": "ALICE@example.com", "password": testPassword})
	var me portal.Me
	if err := json.Unmarshal(d.want(200, "", "GET", "/api/account", nil), &me); err != nil {
		t.Fatal(err)
	}
	if me.User.Username != "alice@example.com" || len(me.Devices) != 1 {
		t.Fatalf("me: %+v", me)
	}
}

func TestDaemonLoginThrottled(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	d := e.daemon()
	var last int
	for range 12 {
		last, _ = d.call("POST", "/api/login", map[string]string{"portal": e.url, "account": "victim@example.com", "password": "wrong password"})
	}
	if last != 429 {
		t.Fatalf("last status %d, want 429", last)
	}
	d.want(429, "throttled", "POST", "/api/login", map[string]string{"portal": e.url, "account": "victim@example.com", "password": "wrong password"})
}

// An administrator's login asks for the second factor without logging in.
func TestDaemonAdminTwoFactor(t *testing.T) {
	now := time.Now()
	e := newDaemonEnv(t, portal.Config{MFAKey: []byte(strings.Repeat("k", 32))}, false)
	if _, err := e.portal.CreateUser("root", testPassword, true); err != nil {
		t.Fatal(err)
	}
	d := e.daemon()
	login := map[string]string{"portal": e.url, "account": "root", "password": testPassword}
	d.want(401, "mfa_setup_required", "POST", "/api/login", login)

	secret := enrollAdmin(t, e, "root")
	d.want(401, "mfa_required", "POST", "/api/login", login)
	if d.session().LoggedIn {
		t.Fatal("logged in without the code")
	}
	login["totp"] = wrongCode(totp.Code(secret, now))
	d.want(401, "bad_code", "POST", "/api/login", login)
	login["totp"] = totp.Code(secret, time.Now())
	d.want(200, "", "POST", "/api/login", login)
	if !d.session().LoggedIn {
		t.Fatal("not logged in with the right code")
	}
}

func wrongCode(right string) string {
	if right == "000000" {
		return "000001"
	}
	return "000000"
}

// enrollAdmin runs the web enrollment (a challenge, a secret, a code), which
// logging in on the desktop leaves to the portal's web page.
func enrollAdmin(t *testing.T, e *daemonEnv, user string) []byte {
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
	var ch struct {
		Challenge string `json:"challenge"`
	}
	post("/api/v1/web/login", map[string]string{"username": user, "password": testPassword}, &ch)
	var setup portal.MFASetup
	post("/api/v1/web/mfa/setup", map[string]string{"challenge": ch.Challenge}, &setup)
	secret, err := totp.Decode(setup.Secret)
	if err != nil {
		t.Fatalf("setup %+v: %v", setup, err)
	}
	post("/api/v1/web/mfa/confirm", map[string]string{"challenge": ch.Challenge, "code": totp.Code(secret, time.Now())}, nil)
	// The code just used is burnt: wait for the next step so the login can use one.
	time.Sleep(time.Until(time.Unix((time.Now().Unix()/30+1)*30, 0)) + 50*time.Millisecond)
	return secret
}

// A self-signed portal: no password goes out until the user confirmed its
// fingerprint, the confirmation is kept, and a replaced certificate is caught.
func TestDaemonSelfSignedPortal(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), true)
	home := t.TempDir()
	d := e.daemonIn(home)
	var p PortalProbe
	if err := json.Unmarshal(d.want(200, "", "POST", "/api/portal/probe", map[string]string{"portal": e.url + "/"}), &p); err != nil {
		t.Fatal(err)
	}
	if p.Trusted || !p.PinRequired || p.Known || p.PinChanged || !p.HTTPS || !strings.HasPrefix(p.Fingerprint, "sha256:") || p.Portal != e.url || !p.RegistrationOpen || p.SignupCredit != 5_000_000 {
		t.Fatalf("probe: %+v", p)
	}
	before := e.logins.Load()
	reg := map[string]string{"portal": e.url, "email": "a@example.com", "password": testPassword}
	d.want(400, "pin_required", "POST", "/api/register", reg)
	reg["pin"] = "sha256:" + strings.Repeat("0", 64)
	d.want(502, "pin_mismatch", "POST", "/api/register", reg)
	if n := e.logins.Load(); n != before {
		t.Fatalf("the password was sent to an unconfirmed portal (%d requests)", n-before)
	}
	reg["pin"] = strings.ToUpper(p.Fingerprint[:6]) + p.Fingerprint[6:] // any case
	d.want(201, "", "POST", "/api/register", reg)
	if c := d.savedConfig(); !strings.EqualFold(c.PortalPin, p.Fingerprint) {
		t.Fatalf("pin not saved: %+v", c)
	}
	d.want(200, "", "GET", "/api/account", nil)
	d.want(200, "", "POST", "/api/logout", nil)

	// The saved pin carries over: known, no confirmation asked.
	if err := json.Unmarshal(d.want(200, "", "POST", "/api/portal/probe", map[string]string{"portal": e.url}), &p); err != nil {
		t.Fatal(err)
	}
	if !p.Known || p.PinRequired || p.PinChanged {
		t.Fatalf("second probe: %+v", p)
	}
	d.want(200, "", "POST", "/api/login", map[string]string{"portal": e.url, "account": "a@example.com", "password": testPassword})
	d.Close()

	// The pin survives a restart of the daemon, and the session with it.
	d2 := e.daemonIn(home)
	if s := d2.session(); !s.LoggedIn || s.User != "a@example.com" {
		t.Fatalf("after restart: %+v", s)
	}
	d2.want(200, "", "GET", "/api/account", nil)
	d2.want(200, "", "POST", "/api/logout", nil)

	// Someone else answers at that address now.
	c := d2.savedConfig()
	c.PortalPin = "sha256:" + strings.Repeat("a", 64)
	if err := SaveConfig(home, c); err != nil {
		t.Fatal(err)
	}
	d2.Close()
	d3 := e.daemonIn(home)
	if err := json.Unmarshal(d3.want(200, "", "POST", "/api/portal/probe", map[string]string{"portal": e.url}), &p); err != nil {
		t.Fatal(err)
	}
	if !p.PinChanged || !p.PinRequired || p.Known {
		t.Fatalf("probe after a certificate change: %+v", p)
	}
	before = e.logins.Load()
	d3.want(502, "pin_mismatch", "POST", "/api/login", map[string]string{"portal": e.url, "account": "a@example.com", "password": testPassword})
	if e.logins.Load() != before {
		t.Fatal("the password was sent despite the changed certificate")
	}
}

func TestDaemonProbeErrors(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	d := e.daemon()
	d.want(502, "portal_unreachable", "POST", "/api/portal/probe", map[string]string{"portal": " "}) // blank: the built-in address (TestMain: nothing listens there)
	d.want(400, "insecure_portal", "POST", "/api/portal/probe", map[string]string{"portal": "http://203.0.113.7"})
	d.want(502, "portal_unreachable", "POST", "/api/portal/probe", map[string]string{"portal": "https://127.0.0.1:1"})
	d.want(502, "portal_unreachable", "POST", "/api/portal/probe", map[string]string{"portal": "http://127.0.0.1:1"})
	notPortal := httptest.NewServer(http.NotFoundHandler())
	defer notPortal.Close()
	d.want(502, "not_a_portal", "POST", "/api/portal/probe", map[string]string{"portal": notPortal.URL})
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
	s := d.register(e.url, "alice@example.com")
	token := d.savedConfig().Token
	if err := func() error { c := d.savedConfig(); c.Buy.BudgetMicros = 123; return SaveConfig(d.home, c) }(); err != nil {
		t.Fatal(err)
	}
	d.want(200, "", "POST", "/api/logout", nil)
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
	d.want(401, "not_logged_in", "GET", "/api/account", nil)
	d.want(200, "", "POST", "/api/logout", nil)
}

// With the portal gone, logging out still works locally.
func TestDaemonLogoutPortalDown(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	d := e.daemon()
	d.register(e.url, "alice@example.com")
	e.srv.Close()
	d.want(502, "portal_unreachable", "GET", "/api/account", nil)
	if s := d.session(); !s.LoggedIn {
		t.Fatalf("an unreachable portal must not log the user out: %+v", s)
	}
	d.want(200, "", "POST", "/api/logout", nil)
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
	s := d.register(e.url, "alice@example.com")
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
	d.want(401, "unauthorized", "GET", "/api/account", nil)
	got := d.waitState(StateExpired)
	if got.LoggedIn || got.Error == "" || got.User != s.User {
		t.Fatalf("session: %+v", got)
	}
	if d.savedConfig().Token != "" {
		t.Fatal("rejected token kept")
	}
	d.want(200, "", "POST", "/api/login", map[string]string{"portal": e.url, "account": s.User, "password": testPassword})
	if st := d.session(); !st.LoggedIn || st.Error != "" {
		t.Fatalf("session after new login: %+v", st)
	}
	d.want(200, "", "GET", "/api/account", nil)

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
	d2.want(401, "unauthorized", "GET", "/api/account", nil)
	d2.waitState(StateExpired)
}

// A disabled user is treated like a revoked login.
func TestDaemonUserDisabled(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	d := e.daemon()
	s := d.register(e.url, "alice@example.com")
	u, _ := e.portal.Store().UserByName(s.User)
	if err := e.portal.Store().SetDisabled(u.ID, true); err != nil {
		t.Fatal(err)
	}
	d.call("GET", "/api/account", nil)
	d.waitState(StateExpired)
	d.want(401, "bad_credentials", "POST", "/api/login", map[string]string{"portal": e.url, "account": s.User, "password": testPassword})
}

// Someone else signs in on this machine: their account gets its own device key.
func TestDaemonSwitchAccounts(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	d := e.daemon()
	alice := d.register(e.url, "alice@example.com")
	d.want(200, "", "POST", "/api/logout", nil)
	bob := d.register(e.url, "bob@example.com")
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
	d.want(200, "", "POST", "/api/logout", nil)
	d.want(200, "", "POST", "/api/login", map[string]string{"portal": e.url, "account": "alice@example.com", "password": testPassword})
	d.want(200, "", "GET", "/api/account", nil)
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
	d.want(500, "bind_failed", "POST", "/api/register", map[string]string{"portal": front.URL, "email": "a@example.com", "password": testPassword})
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
	// The account exists; logging in finishes the job.
	bindFails.Store(false)
	d.want(200, "", "POST", "/api/login", map[string]string{"portal": front.URL, "account": "a@example.com", "password": testPassword})
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
	login := map[string]string{"portal": e.url, "account": "x@example.com", "password": "wrong password"}
	check(403, "POST", "/api/login", login, "Origin", "https://evil.example.com")
	check(403, "POST", "/api/logout", nil, "Origin", "https://evil.example.com")
	check(403, "POST", "/api/login", login, "Origin", "null")
	check(403, "GET", "/api/session", nil, "Origin", "http://evil.localhost.example.com")
	check(401, "POST", "/api/login", login, "Origin", d.base)
	check(401, "POST", "/api/login", login) // CLI tools and the desktop shell send no Origin
	for _, o := range []string{"http://localhost:3000", "http://127.0.0.1:3000", "chrome-extension://abc", "wails://wails"} {
		check(403, "POST", "/api/login", login, "Origin", o)
		check(403, "POST", "/api/logout", nil, "Origin", o)
		check(403, "GET", "/api/account", nil, "Origin", o)
		// The OpenAI endpoint stays open to local tools, as on `sharetoken up`.
		check(401, "POST", "/v1/chat/completions", map[string]string{}, "Origin", o)
	}

	// DNS rebinding: a same-origin request, by the browser's reckoning.
	check(403, "GET", "/api/session", nil, "Host", "evil.example.com")
	check(403, "GET", "/api/session", nil, "Host", "evil.example.com:9787")
	check(403, "GET", "/", nil, "Host", "127.0.0.1.evil.example.com")
	check(200, "GET", "/api/session", nil, "Host", "localhost:9787")
	check(200, "GET", "/api/session", nil, "Host", "[::1]:9787")

	// What a cross-site form could send.
	req, _ := http.NewRequest("POST", d.base+"/api/login", strings.NewReader(`{"portal":"x"}`))
	req.Header.Set("Content-Type", "text/plain")
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
	resp, err = http.Post(d.base+"/api/login", "application/json", big)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("oversized body: %d", resp.StatusCode)
	}
	// The wrong method on a sign-in route does nothing (it falls through to the
	// agent's routes, and there is no agent).
	check(401, "GET", "/api/login", nil)
	check(401, "PUT", "/api/session", map[string]string{})
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
	for path, ct := range map[string]string{"/": "text/html", "/ui/app.js": "javascript", "/ui/app.css": "text/css", "/ui/i18n.js": "javascript", "/ui/i18n/client/en.json": "application/json"} {
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

// Passwords and tokens stay out of the logs.
func TestDaemonLogsNoSecrets(t *testing.T) {
	var buf safeBuffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(old)
	e := newDaemonEnv(t, openPortal(), false)
	d := e.daemon()
	d.register(e.url, "alice@example.com")
	token := d.savedConfig().Token
	d.waitState(StateRetrying)
	d.want(200, "", "POST", "/api/logout", nil)
	d.want(401, "bad_credentials", "POST", "/api/login", map[string]string{"portal": e.url, "account": "alice@example.com", "password": "another-secret-pw"})
	d.Close()
	logs := buf.String()
	if !strings.Contains(logs, "signed in") {
		t.Fatalf("no logs captured: %s", logs)
	}
	for _, secret := range []string{testPassword, "another-secret-pw", token} {
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
	for range 6 {
		wg.Go(func() {
			status, data := d.call("POST", "/api/register", map[string]string{"portal": e.url, "email": "race@example.com", "password": testPassword})
			switch status {
			case 201:
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
				d.call("GET", "/api/session", nil)
				d.call("GET", "/api/account", nil)
				d.call("GET", "/api/status", nil)
				if i == 0 {
					d.call("POST", "/api/logout", nil)
					d.call("POST", "/api/login", map[string]string{"portal": e.url, "account": "race@example.com", "password": testPassword})
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
	if err := json.Unmarshal(d.want(200, "", "POST", "/api/portal/probe", map[string]string{}), &p); err != nil || p.Portal != e.url {
		t.Fatalf("probe without an address: %+v %v", p, err)
	}
	d.want(201, "", "POST", "/api/register", map[string]string{"email": "builtin@example.com", "password": testPassword, "device": "test-box"})
	if c := d.savedConfig(); c.Portal != e.url {
		t.Fatalf("saved portal = %q, want the built-in %q", c.Portal, e.url)
	}
}

// A portal that verifies addresses: the code comes from the outbox here, from
// the user's mailbox in real life.
func TestDaemonRegisterWithMailedCode(t *testing.T) {
	cfg := openPortal()
	cfg.EmailVerification = true
	cfg.Mail = portal.MailConfig{From: "ShareToken <team@sharetoken.test>", Support: "Support <support@sharetoken.test>"}
	e := newDaemonEnv(t, cfg, false)
	d := e.daemon()
	var p PortalProbe
	if err := json.Unmarshal(d.want(200, "", "POST", "/api/portal/probe", map[string]string{"portal": e.url}), &p); err != nil {
		t.Fatal(err)
	}
	if !p.EmailVerification || p.SupportEmail != "support@sharetoken.test" {
		t.Fatalf("probe: %+v", p)
	}
	const email = "code@example.com"
	reg := func(status int, code, mailCode string) {
		t.Helper()
		d.want(status, code, "POST", "/api/register", map[string]string{"portal": e.url, "email": email, "password": testPassword, "code": mailCode})
	}
	reg(400, "bad_code", "")
	d.want(400, "bad_request", "POST", "/api/register/code", map[string]string{"portal": e.url, "email": ""})
	var sent CodeSent
	if err := json.Unmarshal(d.want(200, "", "POST", "/api/register/code", map[string]string{"portal": e.url, "email": email}), &sent); err != nil {
		t.Fatal(err)
	}
	if sent.CooldownSeconds <= 0 {
		t.Fatalf("%+v", sent)
	}
	d.want(429, "code_cooldown", "POST", "/api/register/code", map[string]string{"portal": e.url, "email": email})
	var subject string
	if err := e.portal.Store().DB.QueryRow(`SELECT subject FROM mail_outbox WHERE to_addr = ?`, email).Scan(&subject); err != nil {
		t.Fatal(err)
	}
	code := subject[strings.LastIndex(subject, ": ")+len(": "):]
	wrong := "000000"
	if code == wrong {
		wrong = "000001"
	}
	reg(400, "bad_code", wrong)
	if d.session().LoggedIn {
		t.Fatal("logged in with a wrong code")
	}
	reg(201, "", code)
	if s := d.session(); !s.LoggedIn || s.User != email {
		t.Fatalf("session: %+v", s)
	}
}

// The daemon forwards feedback with the login it holds, and it, not the page,
// says where the message comes from.
func TestDaemonFeedback(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	d := e.daemon()
	body := map[string]string{"category": "bug", "message": "it broke", "lang": "de", "source": "portal", "app_version": "forged"}
	d.want(401, "not_logged_in", "POST", "/api/feedback", body)
	d.register(e.url, "fb@example.com")
	d.want(201, "", "POST", "/api/feedback", body)
	d.want(400, "bad_request", "POST", "/api/feedback", map[string]string{"category": "bug", "message": "  "})

	var list struct {
		Feedback []accounts.Feedback `json:"feedback"`
	}
	if err := json.Unmarshal(d.want(200, "", "GET", "/api/feedback/mine", nil), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Feedback) != 1 {
		t.Fatalf("feedback: %+v", list.Feedback)
	}
	f := list.Feedback[0]
	if f.Message != "it broke" || f.Lang != "de" || f.Source != "client" || f.AppVersion != Version || f.OS != runtime.GOOS+"/"+runtime.GOARCH {
		t.Fatalf("feedback: %+v", f)
	}
}
