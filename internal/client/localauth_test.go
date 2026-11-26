package client

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sharetoken/internal/official"
)

// The API key in the path (/k/<key>/…) reaches /v1 and nothing else: not the
// management API, whatever the path is made to look like, and not when the
// endpoint wants no key (`apikey off`), when every path key matches.
func TestPathKeyReachesOnlyV1(t *testing.T) {
	e := newDaemonEnv(t, openPortal(), false)
	d := e.daemon()
	noKey := []string{"Authorization", ""}
	k := "/k/" + d.APIKey()
	outside := []struct{ method, path string }{
		{"GET", "/api/v1/session"}, {"GET", "/api/v1/apikey"}, {"POST", "/api/v1/logout"}, {"POST", "/api/v1/apikey/rotate"},
		{"GET", "/api/v1/official"}, {"GET", "/api/v1/warmup"}, {"GET", "/api/v1/hello"},
		{"GET", "/v1/../api/v1/session"}, {"GET", "/v1/%2e%2e/api/v1/session"}, {"GET", "/v1/..%2fapi/v1/session"},
		{"GET", "/v1/%2E%2E/api/v1/session"}, {"GET", "/v1/./../api/v1/session"}, {"GET", "//api/v1/session"},
		{"GET", "/v1x/models"}, {"GET", "/ui/app.js"}, {"GET", "/"}, {"GET", ""}, {"GET", "/k/" + d.APIKey() + "/v1/models"},
	}
	check := func(prefix string) {
		t.Helper()
		for _, c := range outside {
			if code, data := d.call(c.method, prefix+c.path, nil, noKey...); code != 404 || !strings.Contains(string(data), "not_found") {
				t.Errorf("%s %s%s: %d %s, want 404", c.method, prefix, c.path, code, data)
			}
			// Nor with the local key next to it: the path key is for /v1.
			if code, _ := d.call(c.method, prefix+c.path, nil); code != 404 {
				t.Errorf("%s %s%s with the local key: %d, want 404", c.method, prefix, c.path, code)
			}
		}
	}
	check(k)
	d.want(401, "not_logged_in", "GET", k+"/v1/models", nil, noKey...) // on to the agent
	d.want(401, "not_logged_in", "POST", k+"/v1/chat/completions", map[string]string{}, noKey...)
	d.want(401, "invalid_api_key", "GET", "/k/sk-st-"+strings.Repeat("0", 48)+"/v1/models", nil, noKey...)
	// Encoded twice, the dots are a name under /v1 like any other.
	d.want(401, "not_logged_in", "GET", k+"/v1/%252e%252e/api/v1/session", nil, noKey...)
	// A path that only cleans to the prefix is judged as cleaned.
	d.want(404, "not_found", "GET", "/x/../k/"+d.APIKey()+"/api/v1/session", nil, noKey...)

	// Web pages: the origin checks do not depend on the prefix.
	for _, o := range []string{"http://localhost:3000", "chrome-extension://abc"} {
		d.want(404, "not_found", "GET", k+"/api/v1/session", nil, "Authorization", "", "Origin", o)
		if code, data := d.call("GET", "//api/v1/session", nil, "Origin", o); code != 403 {
			t.Errorf("//api/v1/session from %s: %d %s, want 403", o, code, data)
		}
		if code, data := d.call("GET", "/v1/../api/v1/session", nil, "Origin", o); code != 403 {
			t.Errorf("/v1/../api/v1/session from %s: %d %s, want 403", o, code, data)
		}
	}
	d.want(403, "", "GET", k+"/api/v1/session", nil, "Authorization", "", "Origin", "https://evil.example.com")

	// `apikey off`: any key matches on /v1, and still nothing else is there.
	d.want(200, "", "PUT", "/api/v1/apikey", map[string]bool{"required": false})
	check("/k/x")
	check(k)
	d.want(401, "not_logged_in", "GET", "/k/x/v1/models", nil, noKey...)
	d.want(200, "", "PUT", "/api/v1/apikey", map[string]bool{"required": true}) // the settings above were not touched
}

// Under the path key the ChatGPT backend passthrough is marked as the
// user's own tool's (official.HasKey) only for the key itself, also when
// /v1 wants none.
func TestPathKeyMarksBackendRequests(t *testing.T) {
	home := t.TempDir()
	gate, err := newAPIKeyGate(home)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := LoadAPIKey(home)
	var path string
	var marked, reached bool
	h := requireLocalKey("local", agentOpen, gate, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, marked, reached = r.URL.Path, official.HasKey(r.Context()), true
	}))
	get := func(p string) int {
		path, marked, reached = "", false, false
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		return w.Code
	}
	for _, required := range []bool{true, false} {
		if _, err := SetAPIKeyRequired(home, required); err != nil {
			t.Fatal(err)
		}
		if get("/k/" + s.Key + "/backend-api/ps/mcp"); !marked || path != "/backend-api/ps/mcp" {
			t.Errorf("required=%t: the key in the path: marked %t, path %q", required, marked, path)
		}
		for _, p := range []string{"/backend-api/ps/mcp", "/k/x/backend-api/ps/mcp", "/k/sk-st-" + strings.Repeat("0", 48) + "/backend-api/ps/mcp"} {
			if get(p); !reached || marked {
				t.Errorf("required=%t: %s: reached %t, marked %t", required, p, reached, marked)
			}
		}
		// /v1 requests are never marked: the mark is the backend's.
		if get("/k/" + s.Key + "/v1/models"); marked {
			t.Errorf("required=%t: a /v1 request was marked", required)
		}
		if code := get("/k/" + s.Key + "/backend-api/../api/v1/status"); code != 404 || reached {
			t.Errorf("required=%t: out of the backend: %d, reached %t", required, code, reached)
		}
	}
}

// A prod build cannot be sent to other vendors by its environment.
func TestProdIgnoresOfficialOverride(t *testing.T) {
	defer func(e string) { Env = e }(Env)
	t.Setenv("SHARETOKEN_OFFICIAL_ANTHROPIC", "http://127.0.0.1:1/anthropic")
	t.Setenv("SHARETOKEN_OFFICIAL_CHATGPT", "http://127.0.0.1:1/backend-api/codex")
	up := newGateway(t.TempDir(), nil).Upstreams()
	if up.Anthropic != "http://127.0.0.1:1/anthropic" || up.ChatGPT != "http://127.0.0.1:1/backend-api/codex" || up.ChatGPTBackend != "http://127.0.0.1:1/backend-api" {
		t.Fatalf("dev: %+v", up)
	}
	Env = EnvProd
	if up := newGateway(t.TempDir(), nil).Upstreams(); up != official.DefaultUpstreams {
		t.Fatalf("prod: %+v", up)
	}
	if up := NewDaemon(t.TempDir()).official.Upstreams(); up != official.DefaultUpstreams {
		t.Fatalf("prod daemon: %+v", up)
	}
}

// The routes that read their bodies themselves take JSON only as JSON: a web
// form can post JSON-looking text, but not that content type.
func TestLocalRoutesWantJSONContentType(t *testing.T) {
	home := t.TempDir()
	cfg := DefaultConfig()
	if err := SaveConfig(home, cfg); err != nil {
		t.Fatal(err)
	}
	a := NewAgent(home, cfg)
	a.portal = NewPortalClient("http://127.0.0.1:1", "", "")
	mux := http.NewServeMux()
	a.providerRoutes(mux)
	a.privateRelayRoutes(mux)
	a.pairingRoutes(mux)
	a.sharingRoutes(mux)
	official.New(home, nil).Routes(mux)
	call := func(method, path, ct, body string) int {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/v1/providers", `{"type":"anthropic","name":"x","api_key":"k"}`},
		{"PATCH", "/api/v1/providers/x", `{"transport":{"proxy_mode":"direct"}}`},
		{"POST", "/api/v1/relays/private", `{"addr":"x"}`},
		{"DELETE", "/api/v1/relays/private", `{"addr":"x"}`},
		{"POST", "/api/v1/pairing/redeem", `{"code":"x"}`},
		{"PUT", "/api/v1/official", `{"claude_fallback_off":true}`},
	} {
		for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x"} {
			if code := call(c.method, c.path, ct, c.body); code != http.StatusUnsupportedMediaType && code != http.StatusBadRequest {
				t.Errorf("%s %s as %q: %d", c.method, c.path, ct, code)
			}
		}
	}
	if s, _ := official.LoadSettings(home); s.ClaudeFallbackOff {
		t.Error("a form changed the settings")
	}
	if code := call("PUT", "/api/v1/official", "application/json; charset=utf-8", `{"claude_fallback_off":true}`); code != 200 {
		t.Errorf("JSON: %d", code)
	}
	// The portal's IDs are numbers: nothing else goes on to it as a path.
	for _, p := range []string{"/api/v1/pairing/codes/1%2F..%2F..%2Fsharing%2Fsettings", "/api/v1/pairing/sellers/x", "/api/v1/sharing/partners/..%2f1", "/api/v1/pairing/requests/1%3Fx"} {
		if code := call("DELETE", p, "", ""); code != http.StatusNotFound {
			t.Errorf("DELETE %s: %d, want 404", p, code)
		}
	}
	if code := call("DELETE", "/api/v1/pairing/codes/12", "", ""); code != http.StatusBadGateway { // on to the portal, which is not there
		t.Errorf("a numeric ID: %d", code)
	}
	// Approving sends no body, and so needs no content type.
	if code := call("POST", "/api/v1/pairing/requests/12/approve", "", ""); code != http.StatusBadGateway {
		t.Errorf("approve without a body: %d", code)
	}
}
