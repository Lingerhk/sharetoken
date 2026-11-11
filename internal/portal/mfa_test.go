package portal

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sharetoken/internal/totp"
)

type challengeResp struct {
	ErrorBody
	Challenge string `json:"challenge"`
}

// Administrators must enroll a TOTP secret at their first login and present
// a code at every later one; tokens remember it, and admin endpoints accept
// nothing less.
func TestAdminLoginRequiresTwoFactor(t *testing.T) {
	db, err := openSharedDB(t)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Unix(1_800_000_000, 0)
	key := []byte(strings.Repeat("k", 32))
	s, err := New(Config{DB: db, MFAKey: key, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser("root", "rootrootroot", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser("alice", "correct horse", false); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	c := &web{t: t, url: srv.URL, hdr: map[string]string{"Origin": srv.URL}}
	api := &web{t: t, url: srv.URL, noWeb: true}

	// 1. A non-admin logs in with a password alone.
	resp := c.call("POST", "/api/v1/web/login", map[string]string{"username": "alice", "password": "correct horse"}, 200, nil)
	if sessionCookie(t, resp) == nil {
		t.Fatal("alice got no session")
	}

	// 2. The admin's password is right, but enrollment is demanded first.
	var ch challengeResp
	resp = c.call("POST", "/api/v1/web/login", map[string]string{"username": "root", "password": "rootrootroot"}, 401, &ch)
	if ch.Error.Code != "mfa_setup_required" || ch.Challenge == "" || len(resp.Cookies()) != 0 {
		t.Fatalf("admin login: %+v %v", ch, resp.Cookies())
	}
	// An enrollment challenge cannot be used as a code challenge.
	c.call("POST", "/api/v1/web/mfa", map[string]string{"challenge": ch.Challenge, "code": "000000"}, 401, nil)
	var setup MFASetup
	c.call("POST", "/api/v1/web/mfa/setup", map[string]string{"challenge": ch.Challenge}, 200, &setup)
	if !strings.HasPrefix(setup.QR, "data:image/png;base64,") || !strings.Contains(setup.URL, "secret="+setup.Secret) || !strings.Contains(setup.URL, defaultMFAIssuer+":root") {
		t.Fatalf("setup: %+v", setup)
	}
	var again MFASetup
	c.call("POST", "/api/v1/web/mfa/setup", map[string]string{"challenge": ch.Challenge}, 200, &again)
	if again.Secret != setup.Secret {
		t.Fatal("a second setup call must return the same pending secret")
	}
	secret, err := totp.Decode(setup.Secret)
	if err != nil {
		t.Fatal(err)
	}
	c.call("POST", "/api/v1/web/mfa/confirm", map[string]string{"challenge": ch.Challenge, "code": "123456"}, 401, nil)
	resp = c.call("POST", "/api/v1/web/mfa/confirm", map[string]string{"challenge": ch.Challenge, "code": totp.Code(secret, now)}, 200, nil)
	c.cookie = sessionCookie(t, resp).Value
	var me Me
	c.call("GET", "/api/v1/me", nil, 200, &me)
	if !me.User.MFAEnrolled() {
		t.Fatal("not marked enrolled")
	}
	c.call("GET", "/api/v1/admin/overview", nil, 200, nil)
	// The challenge is spent.
	c.call("POST", "/api/v1/web/mfa/confirm", map[string]string{"challenge": ch.Challenge, "code": totp.Code(secret, now)}, 401, nil)

	// 3. Tokens minted from a two-factor session carry the flag; plain ones do not.
	var nt struct {
		Token string `json:"token"`
		Info  struct {
			MFA bool `json:"mfa"`
		} `json:"info"`
	}
	c.call("POST", "/api/v1/tokens", map[string]any{"name": "ci"}, 201, &nt)
	if !nt.Info.MFA {
		t.Fatal("token from an mfa session must be mfa")
	}
	(&web{t: t, url: srv.URL, noWeb: true, hdr: map[string]string{"Authorization": "Bearer " + nt.Token}}).call("GET", "/api/v1/admin/overview", nil, 200, nil)
	plain, _, _ := s.Store().IssueToken(me.User.ID, "old", 0)
	var denied ErrorBody
	(&web{t: t, url: srv.URL, noWeb: true, hdr: map[string]string{"Authorization": "Bearer " + plain}}).call("GET", "/api/v1/admin/overview", nil, 403, &denied)
	if denied.Error.Code != "mfa_required" {
		t.Fatalf("plain admin token: %+v", denied)
	}

	// 4. Later logins: password, then a code; a code is good once.
	c.call("POST", "/api/v1/web/logout", nil, 200, nil)
	c.cookie = ""
	c.call("POST", "/api/v1/web/login", map[string]string{"username": "root", "password": "rootrootroot"}, 401, &ch)
	if ch.Error.Code != "mfa_required" || ch.Challenge == "" {
		t.Fatalf("second login: %+v", ch)
	}
	var bad ErrorBody
	c.call("POST", "/api/v1/web/mfa", map[string]string{"challenge": ch.Challenge, "code": totp.Code(secret, now)}, 401, &bad) // replay of the enrollment code
	if bad.Error.Code != "bad_code" {
		t.Fatalf("replay: %+v", bad)
	}
	now = now.Add(totp.Period)
	resp = c.call("POST", "/api/v1/web/mfa", map[string]string{"challenge": ch.Challenge, "code": totp.Code(secret, now)}, 200, nil)
	c.cookie = sessionCookie(t, resp).Value
	c.call("GET", "/api/v1/admin/overview", nil, 200, nil)

	// 5. API clients: one step with "totp", or the same challenge dance.
	api.call("POST", "/api/v1/login", map[string]string{"username": "root", "password": "rootrootroot"}, 401, &ch)
	if ch.Error.Code != "mfa_required" {
		t.Fatalf("api login: %+v", ch)
	}
	now = now.Add(totp.Period)
	var login AuthResponse
	api.call("POST", "/api/v1/mfa", map[string]string{"challenge": ch.Challenge, "code": totp.Code(secret, now)}, 200, &login)
	(&web{t: t, url: srv.URL, noWeb: true, hdr: map[string]string{"Authorization": "Bearer " + login.Token}}).call("GET", "/api/v1/admin/overview", nil, 200, nil)
	now = now.Add(totp.Period)
	api.call("POST", "/api/v1/login", map[string]string{"username": "root", "password": "rootrootroot", "totp": totp.Code(secret, now)}, 200, &login)
	(&web{t: t, url: srv.URL, noWeb: true, hdr: map[string]string{"Authorization": "Bearer " + login.Token}}).call("GET", "/api/v1/admin/overview", nil, 200, nil)
	api.call("POST", "/api/v1/login", map[string]string{"username": "root", "password": "rootrootroot", "totp": "000000"}, 401, &bad)
	if bad.Error.Code != "bad_code" {
		t.Fatalf("wrong totp: %+v", bad)
	}

	// 6. Guessing: a challenge dies after five wrong codes, and failures
	// count toward the login throttle.
	now = now.Add(failWindow + time.Second) // forget the failures above
	c.cookie = ""
	c.call("POST", "/api/v1/web/login", map[string]string{"username": "root", "password": "rootrootroot"}, 401, &ch)
	for i := 0; i < challengeTries; i++ {
		c.call("POST", "/api/v1/web/mfa", map[string]string{"challenge": ch.Challenge, "code": "000000"}, 401, &bad)
		if bad.Error.Code != "bad_code" {
			t.Fatalf("try %d: %+v", i, bad)
		}
	}
	c.call("POST", "/api/v1/web/mfa", map[string]string{"challenge": ch.Challenge, "code": totp.Code(secret, now)}, 401, &bad)
	if bad.Error.Code != "challenge_expired" {
		t.Fatalf("after %d wrong codes: %+v", challengeTries, bad)
	}
	for i := challengeTries; i < maxFails; i++ { // the remaining budget goes to bad passwords
		c.call("POST", "/api/v1/web/login", map[string]string{"username": "root", "password": "wrong"}, 401, nil)
	}
	c.call("POST", "/api/v1/web/login", map[string]string{"username": "root", "password": "rootrootroot"}, 429, nil)
	now = now.Add(failWindow + time.Second)

	// 7. Recovery: another admin resets the secret; every token dies and
	// the next login enrolls afresh.
	ops, err := s.CreateUser("ops", "opsopsopsops", true)
	if err != nil {
		t.Fatal(err)
	}
	opsSecret, _ := totp.NewSecret()
	sealed, err := s.seal(opsSecret)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Store().SetMFASecret(ops.ID, sealed); err != nil {
		t.Fatal(err)
	}
	var opsLogin AuthResponse
	api.call("POST", "/api/v1/login", map[string]string{"username": "ops", "password": "opsopsopsops", "totp": totp.Code(opsSecret, now)}, 200, &opsLogin)
	opsClient := &web{t: t, url: srv.URL, noWeb: true, hdr: map[string]string{"Authorization": "Bearer " + opsLogin.Token}}
	opsClient.call("GET", "/api/v1/admin/overview", nil, 200, nil)
	rootClient := &web{t: t, url: srv.URL, noWeb: true, hdr: map[string]string{"Authorization": "Bearer " + login.Token}}
	var reset struct {
		TokensRevoked int64 `json:"tokens_revoked"`
	}
	rootClient.call("POST", "/api/v1/admin/users/ops/mfa/reset", nil, 200, &reset)
	if reset.TokensRevoked != 1 {
		t.Fatalf("revoked %d", reset.TokensRevoked)
	}
	opsClient.call("GET", "/api/v1/me", nil, 401, nil)
	api.call("POST", "/api/v1/login", map[string]string{"username": "ops", "password": "opsopsopsops", "totp": totp.Code(opsSecret, now)}, 401, &ch)
	if ch.Error.Code != "mfa_setup_required" {
		t.Fatalf("after reset: %+v", ch)
	}
	// Secrets are sealed at rest: the database holds no usable secret.
	stored, _, err := s.Store().MFASecret(me.User.ID)
	if err != nil || strings.Contains(string(stored), string(secret)) || len(stored) <= len(secret) {
		t.Fatalf("secret not sealed: %x %v", stored, err)
	}
	// A portal without a sealing key cannot admit admins at all.
	bare, err := New(Config{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	bsrv := httptest.NewServer(bare.Handler())
	t.Cleanup(bsrv.Close)
	(&web{t: t, url: bsrv.URL, noWeb: true}).call("POST", "/api/v1/login", map[string]string{"username": "root", "password": "rootrootroot"}, 500, nil)
	(&web{t: t, url: bsrv.URL, noWeb: true}).call("POST", "/api/v1/login", map[string]string{"username": "alice", "password": "correct horse"}, 200, nil)
}
