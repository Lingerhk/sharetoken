package portal

import (
	"bytes"
	"crypto/ed25519"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"sharetoken/accounts"
	"sharetoken/internal/mysqldb"
	"sharetoken/p2p"
	"sharetoken/relaypass"
)

type api struct {
	t   *testing.T
	url string
	hdr map[string]string // sent with every request
}

func (a *api) call(method, path, token string, body any, wantStatus int, out any) {
	a.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rd = bytes.NewReader(data)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, a.url+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range a.hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer resp.Body.Close()
	var raw json.RawMessage
	_ = json.NewDecoder(resp.Body).Decode(&raw)
	if resp.StatusCode != wantStatus {
		a.t.Fatalf("%s %s: status %d, want %d: %s", method, path, resp.StatusCode, wantStatus, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			a.t.Fatalf("%s %s: %v: %s", method, path, err, raw)
		}
	}
}

// openSharedDB opens a fresh shared database.
func openSharedDB(t *testing.T) (*sql.DB, error) { return mysqldb.OpenTest() }

func newPortal(t *testing.T, cfg Config) (*Server, *api) {
	t.Helper()
	db, err := openSharedDB(t)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg.DB = db
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return s, &api{t: t, url: srv.URL}
}

func TestPortalAccountLifecycle(t *testing.T) {
	s, a := newPortal(t, Config{OpenRegistration: true, SignupCreditMicros: 5_000_000})

	var reg AuthResponse
	a.call("POST", "/api/v1/register", "", map[string]string{"username": "Alice", "password": "correct horse"}, 201, &reg)
	if reg.User.Username != "alice" || reg.Token == "" {
		t.Fatalf("register: %+v", reg)
	}
	a.call("POST", "/api/v1/register", "", map[string]string{"username": "alice", "password": "correct horse"}, 409, nil)
	a.call("POST", "/api/v1/register", "", map[string]string{"username": "bob", "password": "short"}, 400, nil)

	var me Me
	a.call("GET", "/api/v1/me", reg.Token, nil, 200, &me)
	if me.Balance.AvailableMicros != 5_000_000 || me.Balance.Account != "user:1" {
		t.Fatalf("sign-up credit: %+v", me.Balance)
	}
	a.call("GET", "/api/v1/me", "", nil, 401, nil)
	a.call("GET", "/api/v1/me", "st_bogus", nil, 401, nil)

	// Login, and throttling after repeated failures.
	var login AuthResponse
	a.call("POST", "/api/v1/login", "", map[string]string{"username": "alice", "password": "correct horse"}, 200, &login)
	for i := 0; i < maxFails; i++ {
		a.call("POST", "/api/v1/login", "", map[string]string{"username": "alice", "password": "wrong"}, 401, nil)
	}
	a.call("POST", "/api/v1/login", "", map[string]string{"username": "alice", "password": "correct horse"}, 429, nil)

	// API tokens: create, list, revoke.
	var nt struct {
		Token string         `json:"token"`
		Info  accounts.Token `json:"info"`
	}
	a.call("POST", "/api/v1/tokens", login.Token, map[string]any{"name": "ci", "ttl_hours": 1}, 201, &nt)
	var tl struct{ Tokens []accounts.Token }
	a.call("GET", "/api/v1/tokens", nt.Token, nil, 200, &tl)
	if len(tl.Tokens) != 3 {
		t.Fatalf("want 3 tokens (register, login, ci), got %+v", tl.Tokens)
	}
	a.call("DELETE", fmt.Sprintf("/api/v1/tokens/%d", nt.Info.ID), login.Token, nil, 200, nil)
	a.call("GET", "/api/v1/me", nt.Token, nil, 401, nil)
	a.call("POST", "/api/v1/logout", login.Token, nil, 200, nil)
	a.call("GET", "/api/v1/me", login.Token, nil, 401, nil)

	// Device binding needs the device's signature over the username.
	k, _ := p2p.NewKeys()
	ts := time.Now().UnixNano()
	good := BindRequest{Peer: k.ID(), Name: "laptop", UnixNano: ts, Sig: accounts.SignBind(k.Signer, "alice", k.ID(), ts)}
	forged := good
	forged.Sig = accounts.SignBind(k.Signer, "mallory", k.ID(), ts)
	a.call("POST", "/api/v1/devices", reg.Token, forged, 400, nil)
	stale := good
	stale.UnixNano = ts - int64(time.Hour)
	stale.Sig = accounts.SignBind(k.Signer, "alice", k.ID(), stale.UnixNano)
	a.call("POST", "/api/v1/devices", reg.Token, stale, 400, nil)
	a.call("POST", "/api/v1/devices", reg.Token, good, 200, nil)
	a.call("POST", "/api/v1/devices", reg.Token, good, 200, nil) // idempotent

	// Someone else cannot take the device over, even with its signature.
	var bob AuthResponse
	a.call("POST", "/api/v1/register", "", map[string]string{"username": "bob", "password": "hunter2hunter2"}, 201, &bob)
	steal := BindRequest{Peer: k.ID(), Name: "x", UnixNano: ts, Sig: accounts.SignBind(k.Signer, "bob", k.ID(), ts)}
	a.call("POST", "/api/v1/devices", bob.Token, steal, 409, nil)

	// Admin: only admins; credit grants and deductions land in the ledger.
	a.call("GET", "/api/v1/admin/overview", reg.Token, nil, 403, nil)
	admin, err := s.CreateUser("root", "rootrootroot", true)
	if err != nil {
		t.Fatal(err)
	}
	// Admin endpoints need a token from a two-factor login; a plain one is refused.
	plainTok, _, _ := s.Store().IssueToken(admin.ID, "plain", 0)
	a.call("GET", "/api/v1/admin/overview", plainTok, nil, 403, nil)
	rootTok, _, _ := s.Store().IssueSessionToken(admin.ID, "test", 0, true)
	var b Balance
	a.call("POST", "/api/v1/admin/users/alice/credit", rootTok, map[string]any{"micros": 1_000_000, "memo": "top-up"}, 200, &b)
	if b.AvailableMicros != 6_000_000 {
		t.Fatalf("after grant: %+v", b)
	}
	a.call("POST", "/api/v1/admin/users/alice/credit", rootTok, map[string]any{"micros": -7_000_000}, 409, nil)
	a.call("POST", "/api/v1/admin/users/alice/credit", rootTok, map[string]any{"micros": -500_000, "memo": "correction"}, 200, &b)
	var led struct {
		Entries []LedgerLine `json:"entries"`
	}
	a.call("GET", "/api/v1/ledger", reg.Token, nil, 200, &led)
	if len(led.Entries) != 3 || led.Entries[0].Amount != -500_000 || led.Entries[1].Amount != 1_000_000 || led.Entries[2].Memo != "sign-up credit" {
		t.Fatalf("ledger: %+v", led.Entries)
	}

	// Disabling a user locks its tokens out.
	a.call("POST", "/api/v1/admin/users/bob/disable", rootTok, nil, 200, nil)
	a.call("GET", "/api/v1/me", bob.Token, nil, 401, nil)
	a.call("POST", "/api/v1/admin/users/root/disable", rootTok, nil, 400, nil)

	// Relays: registry, and the public network endpoint.
	a.call("GET", "/api/v1/network", "", nil, 503, nil) // no coordinator has started on this database
	a.call("POST", "/api/v1/admin/relays", rootTok, map[string]string{"addr": "/ip4/1.2.3.4/tcp/1"}, 400, nil)
	a.call("POST", "/api/v1/admin/relays", rootTok, map[string]string{"addr": "/ip4/1.2.3.4/tcp/1/p2p/" + k.ID(), "name": "r1"}, 201, nil)
	if _, err := s.db.Exec("INSERT INTO meta (`key`, value) VALUES ('coordinator', ?)", k.ID()); err != nil {
		t.Fatal(err)
	}
	var n NetworkInfo
	a.call("GET", "/api/v1/network", "", nil, 200, &n)
	if n.Coordinator != k.ID() || len(n.Relays) != 1 || n.Relays[0].OK {
		t.Fatalf("network: %+v", n)
	}

	var o Overview
	a.call("GET", "/api/v1/admin/overview", rootTok, nil, 200, &o)
	if !o.BooksBalance || o.Users != 3 || o.Circulating != 5_000_000*3+1_000_000-500_000 {
		t.Fatalf("overview: %+v", o)
	}
}

func TestRegistrationCanBeClosed(t *testing.T) {
	_, a := newPortal(t, Config{})
	a.call("POST", "/api/v1/register", "", map[string]string{"username": "alice", "password": "correct horse"}, 403, nil)
}

func TestRelayPassEndpoint(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(nil)
	s, a := newPortal(t, Config{OpenRegistration: true, PassKey: key, PassTTL: time.Hour})
	var alice, bob AuthResponse
	a.call("POST", "/api/v1/register", "", map[string]string{"username": "alice", "password": "correct horse"}, 201, &alice)
	a.call("POST", "/api/v1/register", "", map[string]string{"username": "bob", "password": "correct horse"}, 201, &bob)
	k, _ := p2p.NewKeys()
	a.call("POST", "/api/v1/relay-pass", alice.Token, map[string]string{"peer": k.ID()}, 403, nil) // not bound yet
	ts := time.Now().UnixNano()
	a.call("POST", "/api/v1/devices", alice.Token, BindRequest{Peer: k.ID(), UnixNano: ts, Sig: accounts.SignBind(k.Signer, "alice", k.ID(), ts)}, 200, nil)
	var p relaypass.Pass
	a.call("POST", "/api/v1/relay-pass", alice.Token, map[string]string{"peer": k.ID()}, 200, &p)
	if err := p.Verify([]ed25519.PublicKey{key.Public().(ed25519.PublicKey)}, k.ID(), time.Now()); err != nil || p.Account != "user:1" || p.Client != (relaypass.Client{}) {
		t.Fatalf("pass %+v: %v", p, err)
	}
	// The client's build goes into the pass as it reported it, if well formed.
	a.hdr = map[string]string{ClientVersionHeader: "1.0.0", ClientPlatformHeader: "windows/arm64"}
	a.call("POST", "/api/v1/relay-pass", alice.Token, map[string]string{"peer": k.ID()}, 200, &p)
	if want := (relaypass.Client{Version: "1.0.0", OS: "windows", Arch: "arm64"}); p.Client != want || p.Verify([]ed25519.PublicKey{key.Public().(ed25519.PublicKey)}, k.ID(), time.Now()) != nil {
		t.Fatalf("pass client %+v", p.Client)
	}
	a.hdr = map[string]string{ClientVersionHeader: "1.0.0 <b>", ClientPlatformHeader: "Windows"}
	a.call("POST", "/api/v1/relay-pass", alice.Token, map[string]string{"peer": k.ID()}, 200, &p)
	if p.Client != (relaypass.Client{}) {
		t.Fatalf("malformed client info signed: %+v", p.Client)
	}
	a.hdr = nil
	a.call("POST", "/api/v1/relay-pass", bob.Token, map[string]string{"peer": k.ID()}, 403, nil) // alice's device
	_ = s

	_, b := newPortal(t, Config{OpenRegistration: true})
	var u AuthResponse
	b.call("POST", "/api/v1/register", "", map[string]string{"username": "carol", "password": "correct horse"}, 201, &u)
	b.call("POST", "/api/v1/relay-pass", u.Token, map[string]string{"peer": k.ID()}, 404, nil) // passes disabled
}

// A relay is registered once per peer ID; its IPv4 and IPv6 addresses go in
// one entry, normalised.
func TestDualStackRelayRegistration(t *testing.T) {
	s, _ := newPortal(t, Config{})
	id := "12D3KooWHNbGXKRnGB1G5wRSPnPZFGfaJqnMhz4xgE1wH1fvHFkc"
	v4, v6 := "/ip4/203.0.113.7/tcp/8091/p2p/"+id, "/ip6/2001:db8::7/tcp/8091/p2p/"+id
	r, err := s.Store().AddRelay(" "+v4+" , "+v6+" ", "home", "cn")
	if err != nil || r.Addr != v4+","+v6 {
		t.Fatalf("add: %+v %v", r, err)
	}
	if _, err := s.Store().AddRelay(v6, "again", ""); !errors.Is(err, accounts.ErrExists) {
		t.Fatalf("same relay by another address: %v", err)
	}
	if _, err := s.Store().AddRelay("/ip4/1.2.3.4/tcp/1", "", ""); !errors.Is(err, accounts.ErrInvalid) {
		t.Fatalf("no peer ID: %v", err)
	}
	ni, err := s.Network()
	if err != nil || len(ni.Relays) != 1 || ni.Relays[0].Addr != v4+","+v6 {
		t.Fatalf("network: %+v %v", ni, err)
	}
}
