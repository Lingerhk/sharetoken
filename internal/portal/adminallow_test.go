package portal

import (
	"context"
	"errors"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"sharetoken/internal/totp"
)

func TestParseAdminAllow(t *testing.T) {
	good := []string{"203.0.113.7", "2001:db8::1", "198.51.100.0/24", "2001:db8::/48", "sgvpn.example.com", "Admin.Example.com."}
	if err := ParseAdminAllow(good); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "localhost", "203.0.113.7/33", "exa mple.com", "-bad.example.com", "a..b", "http://x.example.com"} {
		if err := ParseAdminAllow([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// fakeLookup answers host names from a table the test changes.
type fakeLookup struct {
	mu    sync.Mutex
	addrs map[string][]string
	fail  bool
}

func (f *fakeLookup) set(host string, addrs ...string) {
	f.mu.Lock()
	f.addrs[host] = addrs
	f.mu.Unlock()
}

func (f *fakeLookup) lookup(_ context.Context, host string) ([]netip.Addr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return nil, errors.New("SERVFAIL")
	}
	var out []netip.Addr
	for _, a := range f.addrs[host] {
		out = append(out, netip.MustParseAddr(a))
	}
	return out, nil
}

func TestAdminAllowMatching(t *testing.T) {
	fl := &fakeLookup{addrs: map[string][]string{}}
	fl.set("home.example.com", "192.0.2.10", "2001:db8:1::4b0")
	a, err := newAdminAllow(AdminAllowConfig{From: []string{"203.0.113.7", "198.51.100.0/24", "home.example.com"}, Lookup: fl.lookup})
	if err != nil {
		t.Fatal(err)
	}
	if a.allows("192.0.2.10") {
		t.Fatal("a host name counts only once resolved")
	}
	if errs := a.resolve(context.Background()); len(errs) != 0 {
		t.Fatal(errs)
	}
	for ip, want := range map[string]bool{
		"203.0.113.7": true, "203.0.113.8": false, "198.51.100.200": true, "198.51.101.1": false,
		"192.0.2.10": true, "::ffff:192.0.2.10": true, "2001:db8:1::4b0": true, "2001:db8:1::4b1": false,
		"127.0.0.1": false, "not-an-ip": false, "": false,
	} {
		if got := a.allows(ip); got != want {
			t.Errorf("%s: %v, want %v", ip, got, want)
		}
	}
	// A failed lookup keeps the previous addresses.
	fl.fail = true
	if errs := a.resolve(context.Background()); len(errs) != 1 {
		t.Fatalf("errors: %v", errs)
	}
	if !a.allows("192.0.2.10") {
		t.Fatal("lost the last good addresses on a failed lookup")
	}
	// The address moves: the old one is out, the new one in.
	fl.fail = false
	fl.set("home.example.com", "192.0.2.20")
	a.resolve(context.Background())
	if a.allows("192.0.2.10") || !a.allows("192.0.2.20") {
		t.Fatal("did not follow the new address")
	}
	// No list: no limit.
	if a, err := newAdminAllow(AdminAllowConfig{}); a != nil || err != nil {
		t.Fatal(a, err)
	}
	if _, err := newAdminAllow(AdminAllowConfig{From: []string{"not a host"}}); err == nil {
		t.Fatal("bad entry accepted")
	}
}

// allowPortal is an mfaPortal whose admin side admits 203.0.113.7,
// 198.51.100.0/24 and whatever home.example.com resolves to (192.0.2.10).
func allowPortal(t *testing.T) *mfaPortal {
	t.Helper()
	fl := &fakeLookup{addrs: map[string][]string{"home.example.com": {"192.0.2.10"}}}
	p := &mfaPortal{t: t, now: time.Unix(1_800_000_000, 0)}
	db, err := openSharedDB(t)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	p.s, err = New(Config{DB: db, OpenRegistration: true, MFAKey: []byte(strings.Repeat("k", 32)), Mail: testMail, Now: func() time.Time { return p.now },
		AdminAllow: AdminAllowConfig{From: []string{"203.0.113.7", "198.51.100.0/24", "home.example.com"}, Lookup: fl.lookup}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(p.s.Handler())
	t.Cleanup(srv.Close)
	p.url = srv.URL
	return p
}

// from is a client whose requests come through the local proxy from ip.
func (p *mfaPortal) from(ip string, hdr map[string]string) *web {
	h := map[string]string{"X-Real-IP": ip, "Origin": p.url}
	for k, v := range hdr {
		h[k] = v
	}
	return &web{t: p.t, url: p.url, hdr: h}
}

func TestAdminSideOnlyFromAllowedSources(t *testing.T) {
	p := allowPortal(t)
	root, err := p.s.CreateUser("root@example.com", true)
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := p.s.Store().IssueSessionToken(root.ID, "t", 0, true)
	if err != nil {
		t.Fatal(err)
	}
	bearer := map[string]string{"Authorization": "Bearer " + tok}
	for _, ip := range []string{"203.0.113.7", "198.51.100.42", "192.0.2.10"} {
		c := p.from(ip, bearer)
		c.noWeb = true
		c.call("GET", "/api/v1/admin/overview", nil, 200, nil)
		c.call("GET", "/admin/", nil, 200, nil)
	}
	// From elsewhere the admin side does not exist, with or without a token.
	for _, ip := range []string{"192.0.2.11", "127.0.0.1", "203.0.113.8"} {
		for _, c := range []*web{p.from(ip, bearer), p.from(ip, nil)} {
			c.noWeb = true
			for _, path := range []string{"/admin", "/admin/", "/api/v1/admin/overview", "/api/v1/admin/users", "/api/v1/admin/no-such-thing", "/api/v1/admin"} {
				resp, raw := c.do("GET", path, nil)
				if resp.StatusCode != 404 || string(raw) != "404 page not found\n" {
					t.Errorf("%s GET %s: %d %q", ip, path, resp.StatusCode, raw)
				}
			}
			if resp, _ := c.do("POST", "/api/v1/admin/users", map[string]string{"username": "x@example.com"}); resp.StatusCode != 404 {
				t.Errorf("%s POST: %d", ip, resp.StatusCode)
			}
		}
		// The rest of the portal is open as before.
		c := p.from(ip, bearer)
		c.noWeb = true
		c.call("GET", "/api/v1/me", nil, 200, nil)
		c.call("GET", "/app/", nil, 200, nil)
		c.call("GET", "/healthz", nil, 200, nil)
	}
}

func TestAdminSignInOnlyFromAllowedSources(t *testing.T) {
	p := allowPortal(t)
	const root, fresh = "root@example.com", "fresh@example.com"
	u, err := p.s.CreateUser(root, true)
	if err != nil {
		t.Fatal(err)
	}
	secret := p.enroll(u)
	if _, err := p.s.CreateUser(fresh, true); err != nil {
		t.Fatal(err)
	}
	in, out := p.from("203.0.113.7", nil), p.from("192.0.2.99", nil)

	// Someone who is not an administrator signs in from anywhere.
	webSignIn(t, p.s, out, "alice@example.com")

	// An administrator's right code from outside goes no further, on the web or a client.
	out.wantErr("POST", "/api/v1/web/auth/verify", map[string]string{"email": root, "code": p.code(out, root)}, 403, "forbidden")
	api := &web{t: t, url: p.url, noWeb: true, hdr: map[string]string{"X-Real-IP": "192.0.2.99"}}
	api.wantErr("POST", "/api/v1/auth/verify", map[string]string{"email": root, "code": p.code(api, root),
		"totp": totp.Code(secret, p.now)}, 403, "forbidden")

	// A challenge won inside cannot be finished outside.
	var ch challengeResp
	in.call("POST", "/api/v1/web/auth/verify", map[string]string{"email": root, "code": p.code(in, root)}, 401, &ch)
	if ch.Error.Code != "mfa_required" {
		t.Fatalf("%+v", ch)
	}
	out.wantErr("POST", "/api/v1/web/mfa", map[string]string{"challenge": ch.Challenge, "code": totp.Code(secret, p.now)}, 403, "forbidden")
	resp := in.call("POST", "/api/v1/web/mfa", map[string]string{"challenge": ch.Challenge, "code": totp.Code(secret, p.now)}, 200, nil)
	in.cookie = sessionCookie(t, resp).Value
	in.call("GET", "/api/v1/admin/overview", nil, 200, nil)
	// The session itself is useless outside.
	out.cookie = in.cookie
	if resp, _ := out.do("GET", "/api/v1/admin/overview", nil); resp.StatusCode != 404 {
		t.Fatalf("admin session used outside: %d", resp.StatusCode)
	}
	out.cookie = ""

	// Enrollment: neither the QR code nor the confirmation outside.
	in2 := p.from("198.51.100.3", nil)
	in2.call("POST", "/api/v1/web/auth/verify", map[string]string{"email": fresh, "code": p.code(in2, fresh)}, 401, &ch)
	if ch.Error.Code != "mfa_setup_required" {
		t.Fatalf("%+v", ch)
	}
	out.wantErr("POST", "/api/v1/web/mfa/setup", map[string]string{"challenge": ch.Challenge}, 403, "forbidden")
	var setup MFASetup
	in2.call("POST", "/api/v1/web/mfa/setup", map[string]string{"challenge": ch.Challenge}, 200, &setup)
	s2, _ := totp.Decode(setup.Secret)
	out.wantErr("POST", "/api/v1/web/mfa/confirm", map[string]string{"challenge": ch.Challenge, "code": totp.Code(s2, p.now)}, 403, "forbidden")
	in2.call("POST", "/api/v1/web/mfa/confirm", map[string]string{"challenge": ch.Challenge, "code": totp.Code(s2, p.now)}, 200, nil)
}
