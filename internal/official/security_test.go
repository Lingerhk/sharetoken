package official

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tooMany is a 429 that says nothing of the plan: a burst, an overloaded
// model.
func tooMany(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "30")
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`)
}

// allowed429 is a 429 whose headers say the plan has room.
func allowed429(w http.ResponseWriter) {
	w.Header().Set("anthropic-ratelimit-unified-status", "allowed")
	w.Header().Set("anthropic-ratelimit-unified-5h-utilization", "0.4")
	w.Header().Set("anthropic-ratelimit-unified-5h-reset", "4102444800")
	tooMany(w)
}

// A request without the client's API key (any program on the machine can
// send one, with a spent plan of its own) neither holds the user's plan nor
// leaves any usage, and one login's spent plan does not hold another's.
func TestClaudeHoldIsPerLoginAndWantsKey(t *testing.T) {
	e := newEnv(t, "claude-opus-5-5")
	e.vendor.replies = []func(http.ResponseWriter){rejected}
	r := claudeReq("claude-opus-5-5", "Authorization", "Bearer someone-elses")
	r.Header.Del(HeaderKey)
	r = r.WithContext(WithoutKey(r.Context()))
	if w := serve(e.h, r); w.Code != http.StatusTooManyRequests || e.network.count() != 0 {
		t.Fatalf("no key: %d network=%d", w.Code, e.network.count())
	}
	if st := e.g.Status(); !st.Claude.HeldUntil.IsZero() || st.Claude.Usage != nil || st.Claude.Fallbacks != 0 || len(e.g.claude.logins) != 0 {
		t.Fatalf("a request without the key left state: %+v", st.Claude)
	}
	// The user's own request still goes to Anthropic.
	if w := serve(e.h, claudeReq("claude-opus-5-5")); w.Code != 200 || e.vendor.count() != 2 || e.network.count() != 0 {
		t.Fatalf("user's request: %d vendor=%d network=%d", w.Code, e.vendor.count(), e.network.count())
	}
	// Even with the same credentials, the keyless one is not told the hold.
	e.vendor.replies = []func(http.ResponseWriter){rejected}
	serve(e.h, claudeReq("claude-opus-5-5"))
	if e.network.count() != 1 || e.g.Status().Claude.HeldUntil.IsZero() {
		t.Fatalf("the user's spent plan did not fall back: network=%d", e.network.count())
	}
	r = claudeReq("claude-opus-5-5")
	r = r.WithContext(WithoutKey(r.Context()))
	before := e.vendor.count()
	if serve(e.h, r); e.vendor.count() != before+1 || e.network.count() != 1 {
		t.Errorf("keyless request while held: vendor=%d network=%d", e.vendor.count()-before, e.network.count())
	}
	// Another login, with the key (a second account of the user's): its
	// plan is its own.
	before = e.vendor.count()
	if serve(e.h, claudeReq("claude-opus-5-5", "Authorization", "Bearer second-account")); e.vendor.count() != before+1 || e.network.count() != 1 {
		t.Errorf("another login was held: vendor=%d network=%d", e.vendor.count()-before, e.network.count())
	}
	// The first is held still.
	if serve(e.h, claudeReq("claude-opus-5-5")); e.vendor.count() != before+1 || e.network.count() != 2 {
		t.Errorf("the held login: vendor=%d network=%d", e.vendor.count()-before, e.network.count())
	}
}

// Only a spent plan sends a conversation to the network: a 429 that does not
// say so is the tool's to see, and holds nothing.
func TestClaudePassing429IsNotAFallback(t *testing.T) {
	e := newEnv(t, "claude-opus-5-5")
	for i, reply := range []func(http.ResponseWriter){tooMany, allowed429} {
		e.vendor.replies = []func(http.ResponseWriter){reply}
		w := serve(e.h, claudeReq("claude-opus-5-5"))
		if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "slow down") || w.Header().Get("Retry-After") != "30" {
			t.Errorf("%d: %d %s", i, w.Code, w.Body)
		}
		if st := e.g.Status(); e.network.count() != 0 || !st.Claude.HeldUntil.IsZero() || st.Claude.Fallbacks != 0 {
			t.Errorf("%d: network=%d status %+v", i, e.network.count(), st.Claude)
		}
		// The next request goes to Anthropic again.
		before := e.vendor.count()
		if serve(e.h, claudeReq("claude-opus-5-5")); e.vendor.count() != before+1 {
			t.Errorf("%d: held after a passing 429", i)
		}
	}
	// A full window without the status says it too.
	e.vendor.replies = []func(http.ResponseWriter){func(w http.ResponseWriter) {
		w.Header().Set("anthropic-ratelimit-unified-7d-utilization", "1.0")
		w.Header().Set("anthropic-ratelimit-unified-7d-reset", "4102444800")
		tooMany(w)
	}}
	if w := serve(e.h, claudeReq("claude-opus-5-5")); w.Code != 200 || e.network.count() != 1 {
		t.Errorf("full window: %d network=%d", w.Code, e.network.count())
	}
}

// With no Claude model on the network a spent plan's 429 is what the tool
// gets: no other model takes a conversation over unasked.
func TestClaudeFallbackStaysInFamily(t *testing.T) {
	e := newEnv(t, "gpt-5.6-sol", "deepseek-v4")
	e.vendor.replies = []func(http.ResponseWriter){rejected}
	if w := serve(e.h, claudeReq("claude-opus-5-5")); w.Code != http.StatusTooManyRequests || e.network.count() != 0 {
		t.Fatalf("no Claude model: %d network=%d", w.Code, e.network.count())
	}
	// The model the user set is the user's choice.
	s := e.g.Settings()
	s.ClaudeFallbackModel = "deepseek-v4"
	_ = e.g.SetSettings(s)
	if w := serve(e.h, claudeReq("claude-opus-5-5")); w.Code != 200 || e.network.count() != 1 || bodyModel([]byte(e.network.bodies[0])) != "st-deepseek-v4" {
		t.Fatalf("set model: %d network=%d", w.Code, e.network.count())
	}
}

func codexAuth(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"tokens":{"access_token":"users-chatgpt-token","account_id":"users-account"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The unlock lends Codex's own ChatGPT login only to a request that showed
// the client's API key, from this machine, on a plain MCP path.
func TestUnlockLendsLoginOnlyWithKey(t *testing.T) {
	codexAuth(t)
	e := newEnv(t)
	h := http.HandlerFunc(e.g.ServeBackend)
	mcp := func(path, remote string, key bool, local net.Addr) *http.Request {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
		r.RemoteAddr = remote
		ctx := r.Context()
		if key {
			ctx = WithKey(ctx)
		}
		if local != nil {
			ctx = context.WithValue(ctx, http.LocalAddrContextKey, local)
		}
		return r.WithContext(ctx)
	}
	lo := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9787}
	last := func() *http.Request { return e.vendor.got[len(e.vendor.got)-1] }

	// The unlock off: nothing is lent, key or not.
	serve(h, mcp("/backend-api/ps/mcp", "127.0.0.1:5000", true, lo))
	if last().Header.Get("Authorization") != "" {
		t.Fatal("lent with the unlock off")
	}
	s := e.g.Settings()
	s.CodexUnlock = true
	_ = e.g.SetSettings(s)

	serve(h, mcp("/backend-api/ps/mcp/tools", "127.0.0.1:5000", true, lo))
	if got := last(); got.Header.Get("Authorization") != "Bearer users-chatgpt-token" || got.Header.Get("chatgpt-account-id") != "users-account" || got.URL.Path != "/ps/mcp/tools" {
		t.Fatalf("with the key: %s %v", got.URL, got.Header)
	}
	serve(h, mcp("/backend-api/ps/mcp", "[::1]:5000", true, &net.TCPAddr{IP: net.IPv6loopback, Port: 443}))
	if last().Header.Get("Authorization") == "" {
		t.Error("not lent over IPv6 loopback")
	}
	for name, r := range map[string]*http.Request{
		"no key":                 mcp("/backend-api/ps/mcp", "127.0.0.1:5000", false, lo),
		"no key, subpath":        mcp("/backend-api/ps/mcp/tools", "127.0.0.1:5000", false, lo),
		"from another machine":   mcp("/backend-api/ps/mcp", "192.168.1.7:5000", true, &net.TCPAddr{IP: net.ParseIP("192.168.1.2"), Port: 9787}),
		"to a LAN address":       mcp("/backend-api/ps/mcp", "127.0.0.1:5000", true, &net.TCPAddr{IP: net.ParseIP("192.168.1.2"), Port: 9787}),
		"not an MCP path":        mcp("/backend-api/wham/tasks", "127.0.0.1:5000", true, lo),
		"an MCP look-alike path": mcp("/backend-api/ps/mcpx", "127.0.0.1:5000", true, lo),
	} {
		before := e.vendor.count()
		serve(h, r)
		if e.vendor.count() != before+1 || last().Header.Get("Authorization") != "" || last().Header.Get("chatgpt-account-id") != "" {
			t.Errorf("%s: lent the login: %v", name, last().Header)
		}
	}
	// The caller's own login goes on as it came.
	r := mcp("/backend-api/ps/mcp", "127.0.0.1:5000", true, lo)
	r.Header.Set("Authorization", "Bearer callers-own")
	serve(h, r)
	if last().Header.Get("Authorization") != "Bearer callers-own" || last().Header.Get("chatgpt-account-id") != "" {
		t.Errorf("the caller's login: %v", last().Header)
	}
	// What the backend could read as another path is refused, key or not.
	for _, p := range []string{
		"/backend-api/ps/mcp/%252e%252e/%252e%252e/wham/usage", "/backend-api/ps/mcp/%25", "/backend-api/ps/mcp/a..b", "/backend-api/ps/mcp/..%5C..%5Cwham",
	} {
		for _, key := range []bool{true, false} {
			before := e.vendor.count()
			if w := serve(h, mcp(p, "127.0.0.1:5000", key, lo)); w.Code != http.StatusBadRequest || e.vendor.count() != before {
				t.Errorf("%s (key %t): %d, reached the backend %t", p, key, w.Code, e.vendor.count() != before)
			}
		}
	}
	// Any other path that is not plain goes on without the login.
	for _, p := range []string{"/backend-api/ps/mcp/x%3Fy", "/backend-api/ps/mcp/x;y", "/backend-api/ps/mcp/x%40y", "/backend-api/ps/mcp/x%5Cy"} {
		before := e.vendor.count()
		serve(h, mcp(p, "127.0.0.1:5000", true, lo))
		if e.vendor.count() != before+1 || last().Header.Get("Authorization") != "" {
			t.Errorf("%s: reached the backend %t, with %q", p, e.vendor.count() != before, last().Header.Get("Authorization"))
		}
	}
	// Dot segments are cleaned away before the path is judged.
	before := e.vendor.count()
	serve(h, mcp("/backend-api/ps/mcp/../../wham/usage", "127.0.0.1:5000", true, lo))
	if e.vendor.count() != before+1 || last().URL.Path != "/wham/usage" || last().Header.Get("Authorization") != "" {
		t.Errorf("cleaned path: %s %v", last().URL, last().Header)
	}
}

// What requests can vary (accounts, logins, queries) is kept in bounded
// tables, and requests without the client's API key leave nothing.
func TestGatewayStateIsBounded(t *testing.T) {
	e := newEnv(t, "gpt-5.6-sol")
	for i := range 3 * maxCodexAccounts {
		r := codexReq("gpt-6.2-astra", true)
		r.Header.Set("chatgpt-account-id", fmt.Sprintf("acct-%d", i))
		serve(e.h, r)
	}
	if n := len(e.g.codex); n == 0 || n > maxCodexAccounts {
		t.Errorf("%d ChatGPT accounts kept, at most %d", n, maxCodexAccounts)
	}
	if _, ok := e.g.codex[fmt.Sprintf("acct-%d", 3*maxCodexAccounts-1)]; !ok {
		t.Error("the newest account was not kept")
	}
	for i := range 3 * maxClaudeLogins {
		serve(e.h, claudeReq("claude-opus-5-5", "Authorization", fmt.Sprintf("Bearer token-%d", i)))
	}
	if n := len(e.g.claude.logins); n == 0 || n > maxClaudeLogins {
		t.Errorf("%d Claude logins kept, at most %d", n, maxClaudeLogins)
	}
	models := func(w http.ResponseWriter) { _, _ = io.WriteString(w, `{"models":[{"slug":"gpt-6.2-astra"}]}`) }
	list := func(query string, key bool) {
		e.vendor.mu.Lock()
		e.vendor.replies = []func(http.ResponseWriter){models}
		e.vendor.mu.Unlock()
		r := httptest.NewRequest(http.MethodGet, "/v1/models?client_version=1&"+query, nil)
		r.Header = codexReq("x", true).Header
		if !key {
			r = r.WithContext(WithoutKey(r.Context()))
		}
		if w := serve(e.h, r); !strings.Contains(w.Body.String(), "gpt-6.2-astra") {
			t.Fatalf("models: %s", w.Body)
		}
	}
	file := filepath.Join(e.g.home, officialModelsFile)
	for i := range 20 {
		list(fmt.Sprintf("x=%d", i), false)
	}
	if _, err := os.Stat(file); err == nil || len(e.g.official.lists) != 0 {
		t.Errorf("requests without the key left %d model lists", len(e.g.official.lists))
	}
	for i := range 3 * maxModelLists {
		list(fmt.Sprintf("x=%d", i), true)
	}
	list("x="+strings.Repeat("y", 2*maxModelListsKey), true)
	if n := len(e.g.official.lists); n == 0 || n > maxModelLists {
		t.Errorf("%d model lists kept, at most %d", n, maxModelLists)
	}
	if fi, err := os.Stat(file); err != nil || fi.Size() > maxModelLists*(maxModelListsKey+200) {
		t.Errorf("%s: %v %v", officialModelsFile, fi, err)
	}
	for k := range e.g.official.lists {
		if len(k) > maxModelListsKey {
			t.Errorf("a key of %d bytes was kept", len(k))
		}
	}
}

// PUT /api/v1/official takes JSON only as JSON, and does not turn the
// unlock on before its certificate is trusted (the unlock's own flow).
func TestSettingsRoute(t *testing.T) {
	e := newEnv(t)
	mux := http.NewServeMux()
	e.g.Routes(mux)
	put := func(ct, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPut, "/api/v1/official", strings.NewReader(body))
		if ct != "" {
			r.Header.Set("Content-Type", ct)
		}
		return serve(mux, r)
	}
	for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded"} {
		if w := put(ct, `{"claude_fallback_off":true}`); w.Code != http.StatusUnsupportedMediaType {
			t.Errorf("%q: %d", ct, w.Code)
		}
	}
	if e.g.Settings().ClaudeFallbackOff {
		t.Fatal("a form changed the settings")
	}
	if w := put("application/json", `{"claude_fallback_off":true,"codex_guard_percent":80}`); w.Code != 200 || !e.g.Settings().ClaudeFallbackOff || e.g.Settings().CodexGuardPercent != 80 {
		t.Fatalf("JSON: %d %s", w.Code, w.Body)
	}
	if w := put("application/json", `{"codex_unlock":true}`); w.Code != http.StatusBadRequest || e.g.Settings().CodexUnlock || !e.g.Settings().ClaudeFallbackOff {
		t.Fatalf("unlock without its certificate: %d %+v", w.Code, e.g.Settings())
	}
	// Once on (the unlock's flow), other settings change with it kept, and
	// it can be turned off here.
	s := e.g.Settings()
	s.CodexUnlock = true
	_ = e.g.SetSettings(s)
	if w := put("application/json", `{"codex_unlock":true,"codex_guard_percent":90}`); w.Code != 200 || !e.g.Settings().CodexUnlock || e.g.Settings().CodexGuardPercent != 90 {
		t.Fatalf("kept on: %d %+v", w.Code, e.g.Settings())
	}
	if w := put("application/json", `{}`); w.Code != 200 || e.g.Settings().CodexUnlock {
		t.Fatalf("off: %d %+v", w.Code, e.g.Settings())
	}
}

func TestEnvUpstreams(t *testing.T) {
	t.Setenv("SHARETOKEN_OFFICIAL_ANTHROPIC", "http://127.0.0.1:1/a")
	t.Setenv("SHARETOKEN_OFFICIAL_CHATGPT", "http://127.0.0.1:1/backend-api/codex/")
	if up := New(t.TempDir(), nil).Upstreams(); up != DefaultUpstreams {
		t.Fatalf("New read the environment: %+v", up)
	}
	if up := EnvUpstreams(); up.Anthropic != "http://127.0.0.1:1/a" || up.ChatGPTBackend != "http://127.0.0.1:1/backend-api" || up.OpenAI != DefaultUpstreams.OpenAI {
		t.Fatalf("EnvUpstreams: %+v", up)
	}
}
