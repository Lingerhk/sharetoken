package portal

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sharetoken/internal/accounts"
	"sharetoken/internal/mailer"
	"sharetoken/internal/totp"
)

type challengeResp struct {
	ErrorBody
	Challenge string `json:"challenge"`
}

// mfaPortal is a portal on a clock the test moves. Each sign-in mails a code,
// and the mailer allows one a minute and five an hour per address: tick moves
// the clock past both.
type mfaPortal struct {
	t   *testing.T
	s   *Server
	url string
	now time.Time
}

func newMFAPortal(t *testing.T) *mfaPortal {
	t.Helper()
	p := &mfaPortal{t: t, now: time.Unix(1_800_000_000, 0)}
	db, err := openSharedDB(t)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	p.s, err = New(Config{DB: db, OpenRegistration: true, MFAKey: []byte(strings.Repeat("k", 32)), Mail: testMail, Now: func() time.Time { return p.now }})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(p.s.Handler())
	t.Cleanup(srv.Close)
	p.url = srv.URL
	return p
}

func (p *mfaPortal) tick() { p.now = p.now.Add(13 * time.Minute) }

// code mails a fresh sign-in code for email through c.
func (p *mfaPortal) code(c *web, email string) string {
	p.t.Helper()
	p.tick()
	return sendCode(p.t, p.s, c, email)
}

func (p *mfaPortal) client(token string) *web {
	return &web{t: p.t, url: p.url, noWeb: true, hdr: map[string]string{"Authorization": "Bearer " + token}}
}

// enroll gives u a sealed secret directly.
func (p *mfaPortal) enroll(u *accounts.User) []byte {
	p.t.Helper()
	secret, _ := totp.NewSecret()
	sealed, err := p.s.seal(secret)
	if err != nil {
		p.t.Fatal(err)
	}
	if err := p.s.Store().SetMFASecret(u.ID, sealed); err != nil {
		p.t.Fatal(err)
	}
	return secret
}

// Administrators must enroll a TOTP secret at their first sign-in and present
// a code at every later one; tokens remember it, and admin endpoints accept
// nothing less.
func TestAdminSignInRequiresTwoFactor(t *testing.T) {
	p := newMFAPortal(t)
	s := p.s
	if _, err := s.CreateUser("root@example.com", true); err != nil {
		t.Fatal(err)
	}
	c := &web{t: t, url: p.url, hdr: map[string]string{"Origin": p.url}}
	api := &web{t: t, url: p.url, noWeb: true}
	const root = "root@example.com"

	// 1. Anyone else signs in with the mailed code alone.
	resp := c.call("POST", "/api/v1/web/auth/verify", map[string]string{"email": "alice@example.com", "code": p.code(c, "alice@example.com")}, 201, nil)
	if sessionCookie(t, resp) == nil {
		t.Fatal("alice got no session")
	}

	// 2. The admin's code is right, but enrollment is demanded first.
	var ch challengeResp
	resp = c.call("POST", "/api/v1/web/auth/verify", map[string]string{"email": root, "code": p.code(c, root)}, 401, &ch)
	if ch.Error.Code != "mfa_setup_required" || ch.Challenge == "" || len(resp.Cookies()) != 0 {
		t.Fatalf("admin sign-in: %+v %v", ch, resp.Cookies())
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
	c.call("POST", "/api/v1/web/mfa/confirm", map[string]string{"challenge": ch.Challenge, "code": otherCode(totp.Code(secret, p.now))}, 401, nil)
	resp = c.call("POST", "/api/v1/web/mfa/confirm", map[string]string{"challenge": ch.Challenge, "code": totp.Code(secret, p.now)}, 200, nil)
	c.cookie = sessionCookie(t, resp).Value
	var me Me
	c.call("GET", "/api/v1/me", nil, 200, &me)
	if !me.User.MFAEnrolled() {
		t.Fatal("not marked enrolled")
	}
	c.call("GET", "/api/v1/admin/overview", nil, 200, nil)
	// The challenge is spent.
	c.call("POST", "/api/v1/web/mfa/confirm", map[string]string{"challenge": ch.Challenge, "code": totp.Code(secret, p.now)}, 401, nil)
	// Admins keep it.
	c.wantErr("POST", "/api/v1/account/mfa/disable", map[string]string{"code": totp.Code(secret, p.now)}, 403, "mfa_required")

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
	p.client(nt.Token).call("GET", "/api/v1/admin/overview", nil, 200, nil)
	plain, _, _ := s.Store().IssueToken(me.User.ID, "old", 0)
	p.client(plain).wantErr("GET", "/api/v1/admin/overview", nil, 403, "mfa_required")

	// 4. Later sign-ins: the mailed code, then an authenticator code; each is good once.
	c.call("POST", "/api/v1/web/logout", nil, 200, nil)
	c.cookie = ""
	enrollCode := totp.Code(secret, p.now)
	c.call("POST", "/api/v1/web/auth/verify", map[string]string{"email": root, "code": p.code(c, root)}, 401, &ch)
	if ch.Error.Code != "mfa_required" || ch.Challenge == "" {
		t.Fatalf("second sign-in: %+v", ch)
	}
	if code := totp.Code(secret, p.now); code != enrollCode { // the clock moved on: replay the burnt step
		c.call("POST", "/api/v1/web/mfa", map[string]string{"challenge": ch.Challenge, "code": enrollCode}, 401, nil)
	}
	resp = c.call("POST", "/api/v1/web/mfa", map[string]string{"challenge": ch.Challenge, "code": totp.Code(secret, p.now)}, 200, nil)
	c.cookie = sessionCookie(t, resp).Value
	c.call("GET", "/api/v1/admin/overview", nil, 200, nil)
	var audits struct {
		Entries []AuditEntry `json:"entries"`
	}
	c.call("GET", "/api/v1/admin/oplog?action="+actionAdminLogin, nil, 200, &audits)
	if len(audits.Entries) != 2 {
		t.Fatalf("admin sign-ins audited: %+v", audits.Entries)
	}

	// 5. Clients: the same challenge, or one step with "totp".
	api.call("POST", "/api/v1/auth/verify", map[string]string{"email": root, "code": p.code(api, root)}, 401, &ch)
	if ch.Error.Code != "mfa_required" {
		t.Fatalf("client sign-in: %+v", ch)
	}
	var login AuthResponse
	api.call("POST", "/api/v1/mfa", map[string]string{"challenge": ch.Challenge, "code": totp.Code(secret, p.now)}, 200, &login)
	p.client(login.Token).call("GET", "/api/v1/admin/overview", nil, 200, nil)
	code := p.code(api, root)
	api.call("POST", "/api/v1/auth/verify", map[string]string{"email": root, "code": code, "totp": totp.Code(secret, p.now)}, 200, &login)
	p.client(login.Token).call("GET", "/api/v1/admin/overview", nil, 200, nil)
	code = p.code(api, root)
	api.wantErr("POST", "/api/v1/auth/verify", map[string]string{"email": root, "code": code, "totp": otherCode(totp.Code(secret, p.now))}, 401, "bad_code")
	// The mailed code was used up: a second try needs a new one.
	api.wantErr("POST", "/api/v1/auth/verify", map[string]string{"email": root, "code": code, "totp": totp.Code(secret, p.now)}, 401, "bad_code")

	// 6. Guessing: a challenge dies after five wrong codes, and failures
	// count toward the throttle.
	p.now = p.now.Add(time.Hour) // forget the failures and the mailed codes above
	c.cookie = ""
	c.call("POST", "/api/v1/web/auth/verify", map[string]string{"email": root, "code": p.code(c, root)}, 401, &ch)
	wrong := otherCode(totp.Code(secret, p.now))
	for i := 0; i < challengeTries; i++ {
		c.wantErr("POST", "/api/v1/web/mfa", map[string]string{"challenge": ch.Challenge, "code": wrong}, 401, "bad_code")
	}
	c.wantErr("POST", "/api/v1/web/mfa", map[string]string{"challenge": ch.Challenge, "code": totp.Code(secret, p.now)}, 401, "challenge_expired")
	p.now = p.now.Add(61 * time.Second) // a new mailed code, within the failure window
	c.call("POST", "/api/v1/web/auth/verify", map[string]string{"email": root, "code": sendCode(t, s, c, root)}, 401, &ch)
	for i := challengeTries; i < maxFails; i++ {
		c.wantErr("POST", "/api/v1/web/mfa", map[string]string{"challenge": ch.Challenge, "code": wrong}, 401, "bad_code")
	}
	c.wantErr("POST", "/api/v1/web/mfa", map[string]string{"challenge": ch.Challenge, "code": totp.Code(secret, p.now)}, 429, "throttled")
	p.now = p.now.Add(failWindow + time.Second)

	// 7. Recovery: another admin resets the secret; every token dies and
	// the next sign-in enrolls afresh.
	ops, err := s.CreateUser("ops@example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	opsSecret := p.enroll(ops)
	var opsLogin AuthResponse
	code = p.code(api, ops.Username)
	api.call("POST", "/api/v1/auth/verify", map[string]string{"email": ops.Username, "code": code, "totp": totp.Code(opsSecret, p.now)}, 200, &opsLogin)
	opsClient := p.client(opsLogin.Token)
	opsClient.call("GET", "/api/v1/admin/overview", nil, 200, nil)
	var reset struct {
		TokensRevoked int64 `json:"tokens_revoked"`
	}
	p.client(login.Token).call("POST", "/api/v1/admin/users/ops@example.com/mfa/reset", nil, 200, &reset)
	if reset.TokensRevoked != 1 {
		t.Fatalf("revoked %d", reset.TokensRevoked)
	}
	opsClient.call("GET", "/api/v1/me", nil, 401, nil)
	api.call("POST", "/api/v1/auth/verify", map[string]string{"email": ops.Username, "code": p.code(api, ops.Username)}, 401, &ch)
	if ch.Error.Code != "mfa_setup_required" {
		t.Fatalf("after reset: %+v", ch)
	}
	// Secrets are sealed at rest: the database holds no usable secret.
	stored, _, err := s.Store().MFASecret(me.User.ID)
	if err != nil || strings.Contains(string(stored), string(secret)) || len(stored) <= len(secret) {
		t.Fatalf("secret not sealed: %x %v", stored, err)
	}
	// A portal without a sealing key cannot admit admins at all.
	bare, err := New(Config{DB: s.db, Mail: testMail, Now: func() time.Time { return p.now }})
	if err != nil {
		t.Fatal(err)
	}
	bsrv := httptest.NewServer(bare.Handler())
	t.Cleanup(bsrv.Close)
	b := &web{t: t, url: bsrv.URL, noWeb: true}
	p.tick()
	b.call("POST", "/api/v1/auth/verify", map[string]string{"email": root, "code": sendCode(t, bare, b, root)}, 500, nil)
	b.call("POST", "/api/v1/auth/verify", map[string]string{"email": "alice@example.com", "code": sendCode(t, bare, b, "alice@example.com")}, 200, nil)
}

// Anyone may turn two-factor authentication on in their settings; signing in
// then asks for it, and turning it off takes a current code.
func TestUserTwoFactor(t *testing.T) {
	p := newMFAPortal(t)
	s := p.s
	api := &web{t: t, url: p.url, noWeb: true}
	const email = "ann@example.com"
	var first AuthResponse
	api.call("POST", "/api/v1/auth/verify", map[string]string{"email": email, "code": p.code(api, email)}, 201, &first)
	ann := p.client(first.Token)
	bob := p.client(clientSignIn(t, s, p.url, "bob@example.com").Token)

	// Turning it off when it is not on, enabling without a setup.
	ann.wantErr("POST", "/api/v1/account/mfa/disable", map[string]string{"code": "000000"}, 409, "mfa_not_enrolled")
	ann.wantErr("POST", "/api/v1/account/mfa/enable", map[string]string{"challenge": "nope", "code": "000000"}, 400, "challenge_expired")
	api.call("POST", "/api/v1/account/mfa/setup", nil, 401, nil) // needs a sign-in
	var setup struct {
		MFASetup
		Challenge string `json:"challenge"`
	}
	ann.call("POST", "/api/v1/account/mfa/setup", nil, 200, &setup)
	if setup.Challenge == "" || !strings.Contains(setup.URL, defaultMFAIssuer+":ann") || !strings.HasPrefix(setup.QR, "data:image/png;base64,") {
		t.Fatalf("%+v", setup)
	}
	secret, err := totp.Decode(setup.Secret)
	if err != nil {
		t.Fatal(err)
	}
	// Someone else's setup is not theirs to finish.
	bob.wantErr("POST", "/api/v1/account/mfa/enable", map[string]string{"challenge": setup.Challenge, "code": totp.Code(secret, p.now)}, 400, "challenge_expired")
	ann.wantErr("POST", "/api/v1/account/mfa/enable", map[string]string{"challenge": setup.Challenge, "code": otherCode(totp.Code(secret, p.now))}, 400, "bad_code")
	var on struct {
		User accounts.User `json:"user"`
	}
	ann.call("POST", "/api/v1/account/mfa/enable", map[string]string{"challenge": setup.Challenge, "code": totp.Code(secret, p.now)}, 200, &on)
	if !on.User.MFAEnrolled() {
		t.Fatalf("%+v", on)
	}
	ann.wantErr("POST", "/api/v1/account/mfa/setup", nil, 409, "mfa_enrolled")
	notices := func() int {
		ms, _ := s.mail.Outbox(mailer.OutFilter{Kind: mailer.KindNotice, Q: email})
		return len(ms)
	}
	if notices() != 1 {
		t.Fatal("no notice that two-factor authentication was turned on")
	}
	// The token from before stays valid.
	ann.call("GET", "/api/v1/me", nil, 200, nil)

	// Signing in now takes the second factor, on the web and in clients.
	var ch challengeResp
	api.call("POST", "/api/v1/auth/verify", map[string]string{"email": email, "code": p.code(api, email)}, 401, &ch)
	if ch.Error.Code != "mfa_required" {
		t.Fatalf("%+v", ch)
	}
	var res AuthResponse
	api.call("POST", "/api/v1/mfa", map[string]string{"challenge": ch.Challenge, "code": totp.Code(secret, p.now)}, 200, &res)
	c := &web{t: t, url: p.url}
	c.call("POST", "/api/v1/web/auth/verify", map[string]string{"email": email, "code": p.code(c, email)}, 401, &ch)
	resp := c.call("POST", "/api/v1/web/mfa", map[string]string{"challenge": ch.Challenge, "code": totp.Code(secret, p.now)}, 200, nil)
	c.cookie = sessionCookie(t, resp).Value
	// A user's two-factor session opens no admin endpoint.
	c.wantErr("GET", "/api/v1/admin/overview", nil, 403, "forbidden")

	// Off again, with a current code only.
	p.client(res.Token).wantErr("POST", "/api/v1/account/mfa/disable", map[string]string{"code": otherCode(totp.Code(secret, p.now))}, 400, "bad_code")
	p.tick()
	on.User = accounts.User{} // mfa_enrolled_at is left out when zero
	c.call("POST", "/api/v1/account/mfa/disable", map[string]string{"code": totp.Code(secret, p.now)}, 200, &on)
	if on.User.MFAEnrolled() || notices() != 2 {
		t.Fatalf("%+v notices %d", on, notices())
	}
	api.call("POST", "/api/v1/auth/verify", map[string]string{"email": email, "code": p.code(api, email)}, 200, &res)
}
