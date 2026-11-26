package portal

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"sharetoken/internal/accounts"
	"sharetoken/internal/p2p"
	"sharetoken/internal/version"
)

// A newer client may send fields this portal does not know yet: they are
// ignored, and in particular grant nothing. Malformed JSON is still refused.
func TestRequestsIgnoreUnknownFields(t *testing.T) {
	s, a := newPortal(t, Config{OpenRegistration: true})

	a.call("POST", "/api/v1/auth/code", "", map[string]any{"email": "alice@example.com", "client_feature": "from a newer client"}, 200, nil)
	var reg AuthResponse
	a.call("POST", "/api/v1/auth/verify", "", map[string]any{
		"email": "alice@example.com", "code": mailedCode(t, s, "alice@example.com"),
		"client_feature": "from a newer client", "is_admin": true,
	}, 201, &reg)
	if reg.User.Username != "alice@example.com" || reg.User.Admin {
		t.Fatalf("sign-in: %+v", reg.User)
	}

	resp, err := http.Post(a.url+"/api/v1/auth/verify", "application/json", strings.NewReader(`{"email":`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed JSON: status %d", resp.StatusCode)
	}
}

// GET /api/v1/version answers without a login and says what this portal is.
func TestVersionEndpoint(t *testing.T) {
	s, a := newPortal(t, Config{})
	if err := accounts.SaveVersionPolicy(s.db, accounts.VersionPolicy{MinClient: "1.0.0", LatestClient: "1.2.0", DownloadURL: "/d#get"}); err != nil {
		t.Fatal(err)
	}
	var v VersionInfo
	a.call("GET", "/api/v1/version", "", nil, 200, &v)
	if v.API != version.API || v.Server.Version != version.Version || v.Client.Min != "1.0.0" || v.Client.Latest != "1.2.0" ||
		v.Client.DownloadURL != "/d#get" || v.Schema["core"] == 0 || v.Schema["mail"] == 0 || v.Protocols.Ctrl == "" {
		t.Fatalf("%+v", v)
	}
	resp, err := http.Get(a.url + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get(ServerVersionHeader) != version.Version {
		t.Fatalf("responses carry %q", resp.Header.Get(ServerVersionHeader))
	}
}

func clientPost(t *testing.T, a *api, path, ver string, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest("POST", a.url+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if ver != "" {
		req.Header.Set(ClientVersionHeader, ver)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var eb ErrorBody
	_ = json.NewDecoder(resp.Body).Decode(&eb)
	return resp.StatusCode, eb.Error.Code
}

// The policy stops clients from starting new business, and nothing else.
func TestVersionPolicyGate(t *testing.T) {
	s, a := newPortal(t, Config{OpenRegistration: true})
	// A wrong code: whether the gate let the request through shows as 401.
	const signIn = `{"email":"alice@example.com","code":"x"}`
	if code, _ := clientPost(t, a, "/api/v1/auth/verify", "0.9.0", signIn); code != 401 {
		t.Fatalf("without a policy every client is served: %d", code)
	}
	var login AuthResponse
	_, login.Token = testUser(t, s, "alice@example.com")
	if err := accounts.SaveVersionPolicy(s.db, accounts.VersionPolicy{MinClient: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		ver  string
		want int
	}{
		{"0.9.0", 426}, {"", 426}, {"1.0.0-dev.abc", 426}, {"dev", 426}, {"1.0.0", 401}, {"1.1.0", 401},
	} {
		code, ecode := clientPost(t, a, "/api/v1/auth/verify", c.ver, signIn)
		if code != c.want || (code == 426 && ecode != "upgrade_required") {
			t.Errorf("sign-in as %q: %d %s, want %d", c.ver, code, ecode, c.want)
		}
	}
	// What the client already has keeps working: it must still settle and dispute.
	a.call("GET", "/api/v1/me", login.Token, nil, 200, nil)
	a.call("GET", "/api/v1/version", "", nil, 200, nil)
	a.call("POST", "/api/v1/relay-pass", login.Token, map[string]string{"peer": "x"}, 404, nil)
	// A policy for a development network lets its builds through.
	if err := accounts.SaveVersionPolicy(s.db, accounts.VersionPolicy{MinClient: "1.0.0", AllowDev: true}); err != nil {
		t.Fatal(err)
	}
	if code, _ := clientPost(t, a, "/api/v1/auth/verify", "1.0.0-dev.abc", signIn); code != 401 {
		t.Errorf("dev build on a dev network: %d", code)
	}
	if code, _ := clientPost(t, a, "/api/v1/auth/verify", "0.9.0-dev.abc", signIn); code != 426 {
		t.Errorf("old dev build: %d", code)
	}
	if err := accounts.SaveVersionPolicy(s.db, accounts.VersionPolicy{MinClient: "bad"}); err == nil {
		t.Error("accepted a policy with a bad version")
	}
	if err := accounts.SaveVersionPolicy(s.db, accounts.VersionPolicy{MinClient: "2.0.0", LatestClient: "1.0.0"}); err == nil {
		t.Error("accepted min > latest")
	}
}

// A client's version is recorded where the operator can see it.
func TestDeviceVersions(t *testing.T) {
	s, a := newPortal(t, Config{OpenRegistration: true})
	var reg AuthResponse
	_, reg.Token = testUser(t, s, "alice")
	k, _ := p2p.NewKeys()
	ts := time.Now().UnixNano()
	body, _ := json.Marshal(BindRequest{Peer: k.ID(), Name: "laptop", UnixNano: ts, Sig: accounts.SignBind(k.Signer, "alice", k.ID(), ts)})
	req, _ := http.NewRequest("POST", a.url+"/api/v1/devices", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+reg.Token)
	req.Header.Set(ClientVersionHeader, "1.2.3")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("bind: %d", resp.StatusCode)
	}
	rep, err := s.Versions()
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Devices) != 1 || rep.Devices[0].Version != "1.2.3" || rep.Devices[0].Devices != 1 {
		t.Fatalf("%+v", rep.Devices)
	}
	devs, err := s.store.Devices(1)
	if err != nil || len(devs) != 1 || devs[0].ClientVersion != "1.2.3" {
		t.Fatalf("%+v %v", devs, err)
	}
}
