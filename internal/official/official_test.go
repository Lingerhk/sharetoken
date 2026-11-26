package official

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"sharetoken/internal/protocol"
)

// fake records what reached it and answers with the next reply.
type fake struct {
	mu      sync.Mutex
	got     []*http.Request
	bodies  []string
	replies []func(w http.ResponseWriter)
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.got = append(f.got, r)
	f.bodies = append(f.bodies, string(b))
	var reply func(http.ResponseWriter)
	if len(f.replies) > 0 {
		reply, f.replies = f.replies[0], f.replies[1:]
	}
	f.mu.Unlock()
	if reply == nil {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":"vendor"}`)
		return
	}
	reply(w)
}

func (f *fake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

type env struct {
	g       *Gateway
	h       http.Handler
	vendor  *fake
	network *fake
}

func newEnv(t *testing.T, models ...string) *env {
	t.Helper()
	e := &env{vendor: &fake{}, network: &fake{}}
	srv := httptest.NewServer(e.vendor)
	t.Cleanup(srv.Close)
	e.g = New(t.TempDir(), nil)
	e.g.SetUpstreams(Upstreams{Anthropic: srv.URL, ChatGPT: srv.URL + "/codex", ChatGPTBackend: srv.URL, OpenAI: srv.URL + "/oa"})
	ms := func(context.Context) ([]protocol.ModelSummary, error) {
		var out []protocol.ModelSummary
		for _, m := range models {
			out = append(out, protocol.ModelSummary{Model: m, APIs: []string{protocol.APIMessages}})
		}
		return out, nil
	}
	e.h = e.g.Wrap(e.network, ms)
	return e
}

func claudeReq(model string, extra ...string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/messages?beta=true", strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`))
	r.Header.Set("Authorization", "Bearer oauth-token")
	r.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20")
	r.Header.Set(HeaderKey, "sk-st-key")
	for i := 0; i+1 < len(extra); i += 2 {
		r.Header.Set(extra[i], extra[i+1])
	}
	return r
}

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestClassify(t *testing.T) {
	h := func(kv ...string) http.Header {
		out := http.Header{}
		for i := 0; i < len(kv); i += 2 {
			out.Set(kv[i], kv[i+1])
		}
		return out
	}
	for _, c := range []struct {
		h    http.Header
		want Cred
	}{
		{h("Authorization", "Bearer sk-st-abc"), CredNone},
		{h(), CredNone},
		{h("Authorization", "Bearer tok", "anthropic-beta", "x,oauth-2025-04-20"), CredClaudeOAuth},
		{h("x-api-key", "sk-ant-api03-x"), CredAnthropicKey},
		{h("Authorization", "Bearer eyJ", "chatgpt-account-id", "acc"), CredChatGPT},
		{h("Authorization", "Bearer sk-proj-1"), CredOpenAIKey},
		{h("Authorization", "Bearer tok"), CredNone},
	} {
		if got := Classify(c.h); got != c.want {
			t.Errorf("Classify(%v) = %v, want %v", c.h, got, c.want)
		}
	}
}

func TestClaudeRouting(t *testing.T) {
	e := newEnv(t, "gpt-5.6-sol")
	// An official model goes to the vendor with the user's credentials.
	w := serve(e.h, claudeReq("claude-opus-5-5"))
	if w.Code != 200 || e.vendor.count() != 1 || e.network.count() != 0 {
		t.Fatalf("official: %d vendor=%d network=%d", w.Code, e.vendor.count(), e.network.count())
	}
	got := e.vendor.got[0]
	if got.URL.Path != "/v1/messages" || got.URL.RawQuery != "beta=true" || got.Header.Get("Authorization") != "Bearer oauth-token" ||
		!strings.Contains(got.Header.Get("anthropic-beta"), "oauth-") || got.Header.Get(HeaderKey) != "" {
		t.Errorf("vendor got %s %v", got.URL, got.Header)
	}
	// A network model goes to the network, without the vendor's credentials.
	serve(e.h, claudeReq("st-gpt-5.6-sol"))
	if e.network.count() != 1 {
		t.Fatal("st- model did not reach the network")
	}
	n := e.network.got[0]
	if n.Header.Get("Authorization") != "" || n.Header.Get(HeaderKey) != "" {
		t.Errorf("network got credentials: %v", n.Header)
	}
	// Without the client's key, network models are refused.
	r := claudeReq("st-gpt-5.6-sol")
	r = r.WithContext(WithoutKey(r.Context()))
	if w := serve(e.h, r); w.Code != http.StatusUnauthorized || e.network.count() != 1 {
		t.Errorf("no key: %d network=%d", w.Code, e.network.count())
	}
}

func rejected(w http.ResponseWriter) {
	w.Header().Set("anthropic-ratelimit-unified-status", "rejected")
	w.Header().Set("anthropic-ratelimit-unified-5h-utilization", "1.0")
	w.Header().Set("anthropic-ratelimit-unified-5h-reset", "4102444800")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"limit"}}`)
}

func TestClaudeFallback(t *testing.T) {
	e := newEnv(t, "gpt-5.6-sol", "claude-sonnet-5-5", "claude-opus-5-5-x")
	e.vendor.replies = []func(http.ResponseWriter){rejected}
	w := serve(e.h, claudeReq("claude-opus-5-5"))
	if w.Code != 200 || e.network.count() != 1 {
		t.Fatalf("fallback: %d network=%d %s", w.Code, e.network.count(), w.Body)
	}
	if m := bodyModel([]byte(e.network.bodies[0])); m != "st-claude-opus-5-5-x" {
		t.Errorf("fallback model %q", m)
	}
	// While held, the next request goes to the network directly.
	serve(e.h, claudeReq("claude-sonnet-5-5"))
	if e.vendor.count() != 1 || e.network.count() != 2 {
		t.Errorf("held: vendor=%d network=%d", e.vendor.count(), e.network.count())
	}
	st := e.g.Status()
	if st.Claude.Fallbacks != 2 || st.Claude.HeldUntil.IsZero() || st.Claude.Usage == nil {
		t.Errorf("status %+v", st.Claude)
	}
	// With the fallback off the vendor's 429 is what the tool gets.
	s := e.g.Settings()
	s.ClaudeFallbackOff = true
	if err := e.g.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	e.vendor.replies = []func(http.ResponseWriter){rejected}
	if w := serve(e.h, claudeReq("claude-opus-5-5")); w.Code != http.StatusTooManyRequests || e.network.count() != 2 {
		t.Errorf("fallback off: %d network=%d", w.Code, e.network.count())
	}
}

func TestPickFallback(t *testing.T) {
	ms := []protocol.ModelSummary{{Model: "gpt-5.6-sol", APIs: []string{"responses"}}, {Model: "deepseek-v4", APIs: []string{"messages"}}, {Model: "claude-sonnet-5-5"}}
	for _, c := range []struct{ asked, set, want string }{
		{"claude-sonnet-5-5", "", "claude-sonnet-5-5"},
		{"claude-opus-5-5", "", "claude-sonnet-5-5"},
		{"claude-haiku-5-5", "", "claude-sonnet-5-5"},
		{"claude-opus-5-5", "st-gpt-5.6-sol", "gpt-5.6-sol"},
		{"claude-opus-5-5", "missing", "claude-sonnet-5-5"},
	} {
		if got := pickFallback(c.asked, ms, c.set); got != c.want {
			t.Errorf("pickFallback(%q, %q) = %q, want %q", c.asked, c.set, got, c.want)
		}
	}
	// No Claude model on the network: none takes over, not even one that
	// speaks Messages.
	if got := pickFallback("claude-opus-5-5", ms[:2], ""); got != "" {
		t.Errorf("no claude: %q, want none", got)
	}
	both := []protocol.ModelSummary{{Model: "claude-haiku-5-5"}, {Model: "claude-sonnet-5-5"}}
	if got := pickFallback("claude-opus-5-5", both, ""); got != "claude-sonnet-5-5" {
		t.Errorf("opus took %q, want the stronger", got)
	}
	if got := pickFallback("claude-haiku-4-5", both, ""); got != "claude-haiku-5-5" {
		t.Errorf("haiku took %q", got)
	}
}

func TestThinkingRetry(t *testing.T) {
	e := newEnv(t)
	e.vendor.replies = []func(http.ResponseWriter){func(w http.ResponseWriter) {
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"Invalid signature in thinking block"}}`)
	}}
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(
		`{"model":"claude-opus-5-5","thinking":{"type":"enabled"},"messages":[{"role":"user","content":"a"},{"role":"assistant","content":[{"type":"thinking","thinking":"x","signature":"bad"},{"type":"text","text":"b"}]},{"role":"user","content":"c"}]}`))
	r.Header = claudeReq("x").Header
	if w := serve(e.h, r); w.Code != 200 || e.vendor.count() != 2 {
		t.Fatalf("retry: %d vendor=%d", w.Code, e.vendor.count())
	}
	if strings.Contains(e.vendor.bodies[1], "signature") || !strings.Contains(e.vendor.bodies[1], `"thinking":{"type":"enabled"}`) {
		t.Errorf("retried body %s", e.vendor.bodies[1])
	}
}

func codexReq(model string, stream bool) *http.Request {
	b, _ := json.Marshal(map[string]any{"model": model, "stream": stream, "input": []any{
		map[string]any{"type": "message", "role": "user", "content": "hi"},
		map[string]any{"type": "reasoning", "encrypted_content": "zzz"},
	}, "client_metadata": map[string]any{"x-codex-turn-metadata": `{"turn_id":"t1"}`}})
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(b)))
	r.Header.Set("Authorization", "Bearer eyJchatgpt")
	r.Header.Set("chatgpt-account-id", "acct-1")
	r.Header.Set(HeaderKey, "sk-st-key")
	return r
}

func usageHeaders(used string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("x-codex-primary-used-percent", used)
		w.Header().Set("x-codex-primary-window-minutes", "300")
		w.Header().Set("x-codex-primary-reset-at", "4102444800")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {}\n\n")
	}
}

func wham(used string) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"rate_limit":{"allowed":true,"primary_window":{"used_percent":`+used+`,"limit_window_seconds":18000,"reset_at":4102444800}}}`)
	}
}

func TestCodexRoutingAndGuard(t *testing.T) {
	e := newEnv(t, "gpt-5.6-sol")
	// The first request asks for the plan's usage, then each answer's
	// headers keep it.
	e.vendor.replies = []func(http.ResponseWriter){wham("10"), usageHeaders("50"), usageHeaders("96")}
	for range 2 {
		if w := serve(e.h, codexReq("gpt-6.2-astra", true)); w.Code != 200 {
			t.Fatalf("official: %d", w.Code)
		}
	}
	if e.vendor.got[0].URL.Path != "/wham/usage" || e.vendor.got[1].URL.Path != "/codex/responses" || e.vendor.got[1].Header.Get("chatgpt-account-id") != "acct-1" {
		t.Errorf("vendor got %s %s %v", e.vendor.got[0].URL, e.vendor.got[1].URL, e.vendor.got[1].Header)
	}
	// 96% used: the next one is held (no fresh usage: the backend is the fake,
	// which answers wham/usage with nothing useful).
	w := serve(e.h, codexReq("gpt-6.2-astra", true))
	if !strings.Contains(w.Body.String(), "response.completed") || !strings.Contains(w.Body.String(), "st-") {
		t.Fatalf("not held: %s", w.Body)
	}
	// A network model still goes.
	serve(e.h, codexReq("st-gpt-5.6-sol", true))
	if e.network.count() != 1 {
		t.Error("network model held too")
	}
	// The unlock turns the guard off.
	s := e.g.Settings()
	s.CodexUnlock = true
	_ = e.g.SetSettings(s)
	before := e.vendor.count()
	serve(e.h, codexReq("gpt-6.2-astra", true))
	if e.vendor.count() == before {
		t.Error("unlock did not turn the guard off")
	}
}

func TestCodexEncryptedRetry(t *testing.T) {
	e := newEnv(t)
	e.vendor.replies = []func(http.ResponseWriter){wham("1"), func(w http.ResponseWriter) {
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"error":{"code":"invalid_encrypted_content","message":"Encrypted content could not be decrypted"}}`)
	}}
	if w := serve(e.h, codexReq("gpt-6.2-astra", true)); w.Code != 200 || e.vendor.count() != 3 {
		t.Fatalf("retry: %d vendor=%d", w.Code, e.vendor.count())
	}
	if !strings.Contains(e.vendor.bodies[1], "encrypted_content") || strings.Contains(e.vendor.bodies[2], "encrypted_content") {
		t.Errorf("retried with reasoning: %s", e.vendor.bodies[2])
	}
}

func TestCodexModelsMerged(t *testing.T) {
	e := newEnv(t, "gpt-5.6-sol")
	e.vendor.replies = []func(http.ResponseWriter){func(w http.ResponseWriter) {
		_, _ = io.WriteString(w, `{"models":[{"slug":"gpt-6.2-astra","priority":1}]}`)
	}}
	r := httptest.NewRequest(http.MethodGet, "/v1/models?client_version=0.169.0", nil)
	r.Header = codexReq("x", true).Header
	w := serve(e.h, r)
	var out struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || len(out.Models) != 2 {
		t.Fatalf("models: %s", w.Body)
	}
	if out.Models[0]["slug"] != "gpt-6.2-astra" || out.Models[1]["slug"] != "st-gpt-5.6-sol" {
		t.Errorf("order: %v", out.Models)
	}
	if e.vendor.got[0].URL.Path != "/codex/models" {
		t.Errorf("asked %s", e.vendor.got[0].URL)
	}
	// The vendor down: the last list.
	e.vendor.replies = []func(http.ResponseWriter){func(w http.ResponseWriter) { w.WriteHeader(502) }}
	w = serve(e.h, r)
	if !strings.Contains(w.Body.String(), "gpt-6.2-astra") {
		t.Errorf("cached list missing: %s", w.Body)
	}
}

func TestUnlockUsage(t *testing.T) {
	u := map[string]any{"rate_limit": map[string]any{"allowed": false, "limit_reached": true}, "rate_limit_reached_type": "primary"}
	if !unlockUsage(u) || u["rate_limit"].(map[string]any)["allowed"] != true || u["rate_limit_reached_type"] != nil {
		t.Errorf("unlock: %v", u)
	}
	open := map[string]any{"rate_limit": map[string]any{"allowed": true}}
	if unlockUsage(open) {
		t.Error("changed an open answer")
	}
	doc := map[string]any{"accounts": []any{map[string]any{"workspace_backend_origin": "NO_CONSTRAINT"}}}
	if !pointAccountsAt(doc, "https://localhost") {
		t.Error("accounts check unchanged")
	}
}

func TestBackendUnlock(t *testing.T) {
	e := newEnv(t)
	e.vendor.replies = []func(http.ResponseWriter){func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"rate_limit":{"allowed":false,"limit_reached":true}}`)
	}, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"rate_limit":{"allowed":false,"limit_reached":true}}`)
	}}
	r := httptest.NewRequest(http.MethodGet, "/backend-api/wham/usage", nil)
	if w := serve(http.HandlerFunc(e.g.ServeBackend), r); !strings.Contains(w.Body.String(), `"allowed":false`) {
		t.Errorf("rewritten with the unlock off: %s", w.Body)
	}
	s := e.g.Settings()
	s.CodexUnlock = true
	_ = e.g.SetSettings(s)
	r = httptest.NewRequest(http.MethodGet, "/backend-api/wham/usage", nil)
	if w := serve(http.HandlerFunc(e.g.ServeBackend), r); !strings.Contains(w.Body.String(), `"allowed":true`) {
		t.Errorf("not rewritten: %s", w.Body)
	}
	if e.vendor.got[1].URL.Path != "/wham/usage" {
		t.Errorf("backend path %s", e.vendor.got[1].URL.Path)
	}
}
