package provider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"sharetoken/internal/node"
)

func TestUsageFromClaudeHeaders(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	h := http.Header{}
	h.Set("anthropic-ratelimit-unified-5h-utilization", "0.34")
	h.Set("anthropic-ratelimit-unified-5h-reset", "1800003600")
	h.Set("anthropic-ratelimit-unified-7d-utilization", "1.0")
	h.Set("anthropic-ratelimit-unified-7d-reset", "1800300000")
	h.Set("anthropic-ratelimit-unified-status", "allowed")
	l, ok := usageFromHeaders(h, now)
	if !ok || len(l.Windows) != 2 {
		t.Fatalf("got %+v %v", l, ok)
	}
	if w := l.Windows[0]; w.Name != "5h" || w.Used != 34 || !w.ResetsAt.Equal(time.Unix(1800003600, 0)) || w.Span() != 5*time.Hour {
		t.Fatalf("5h window %+v", w)
	}
	if w := l.Windows[1]; w.Name != "7d" || w.Used != 100 || w.Span() != 7*24*time.Hour {
		t.Fatalf("7d window %+v", w)
	}
	if l.Rejected || l.Overage {
		t.Fatalf("flags %+v", l)
	}
	h.Set("anthropic-ratelimit-unified-status", "rejected")
	h.Set("anthropic-ratelimit-unified-representative-claim", "overage")
	if l, _ = usageFromHeaders(h, now); !l.Rejected || !l.Overage {
		t.Fatalf("flags %+v", l)
	}
}

func TestUsageFromCodexHeaders(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	h := http.Header{}
	h.Set("x-codex-primary-used-percent", "12.5")
	h.Set("x-codex-primary-window-minutes", "300")
	h.Set("x-codex-primary-reset-after-seconds", "600")
	h.Set("x-codex-secondary-used-percent", "40")
	h.Set("x-codex-secondary-window-minutes", "10080")
	h.Set("x-codex-secondary-reset-at", "1800500000")
	l, ok := usageFromHeaders(h, now)
	if !ok || len(l.Windows) != 2 {
		t.Fatalf("got %+v %v", l, ok)
	}
	if w := l.Windows[0]; w.Name != "5h" || w.Used != 12.5 || !w.ResetsAt.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("primary %+v", w)
	}
	if w := l.Windows[1]; w.Name != "7d" || !w.ResetsAt.Equal(time.Unix(1800500000, 0)) {
		t.Fatalf("secondary %+v", w)
	}
	h.Set("x-codex-active-limit", "imagegen_premium")
	if _, ok := usageFromHeaders(h, now); ok {
		t.Fatal("an image allowance's window was taken for the account's")
	}
	if _, ok := usageFromHeaders(http.Header{}, now); ok {
		t.Fatal("no headers told windows")
	}
}

func TestNormalizeTimes(t *testing.T) {
	got, err := NormalizeTimes([]string{"11:05", "6:00", " 06:00 ", ""})
	if err != nil || strings.Join(got, ",") != "06:00,11:05" {
		t.Fatalf("got %v %v", got, err)
	}
	for _, bad := range []string{"24:00", "6", "06:0", "6:60", "x:00"} {
		if _, err := NormalizeTimes([]string{bad}); err == nil {
			t.Fatalf("%q was taken", bad)
		}
	}
	if _, err := NormalizeTimes([]string{"01:00", "02:00", "03:00", "04:00", "05:00", "06:00", "07:00"}); err == nil {
		t.Fatal("seven times were taken")
	}
}

func TestSetWarmupOnlyForSubscriptions(t *testing.T) {
	home := t.TempDir()
	if err := Update(home, func(f *File) error {
		if err := f.Put(&Entry{Name: "max", Type: KindAnthropic, Auth: AuthOAuth, Enabled: true, OAuth: &Token{AccessToken: "a", RefreshToken: "r"}}); err != nil {
			return err
		}
		return f.Put(&Entry{Name: "ds", Type: KindDeepSeek, Auth: AuthAPIKey, Enabled: true, APIKey: "sk-0123456789abcdef"})
	}); err != nil {
		t.Fatal(err)
	}
	v, err := SetWarmup(home, "max", &WarmOptions{Times: []string{"6:00"}, OnReset: "week"})
	if err != nil || !v.WarmCapable || v.Warmup == nil || v.Warmup.Times[0] != "06:00" {
		t.Fatalf("got %+v %v", v, err)
	}
	if _, err := SetWarmup(home, "ds", &WarmOptions{Times: []string{"06:00"}}); err == nil {
		t.Fatal("an API key provider took a warm-up")
	}
	if _, err := SetWarmup(home, "max", &WarmOptions{OnReset: "sometimes"}); err == nil {
		t.Fatal("a bad on_reset was taken")
	}
	// a new login of the same name keeps the warm-up
	if err := Update(home, func(f *File) error {
		return f.Put(&Entry{Name: "max", Type: KindAnthropic, Auth: AuthOAuth, Enabled: true, OAuth: &Token{AccessToken: "b", RefreshToken: "r2"}})
	}); err != nil {
		t.Fatal(err)
	}
	f, _ := Load(home)
	if e := f.Get("max"); e.Warmup == nil || e.Warmup.OnReset != "week" {
		t.Fatalf("warm-up lost on a new login: %+v", e.Warmup)
	}
	if v, err := SetWarmup(home, "max", &WarmOptions{}); err != nil || v.Warmup != nil {
		t.Fatalf("an empty warm-up did not turn it off: %+v %v", v, err)
	}
}

// A warm-up goes through the provider's own upstream: the Codex backend
// with the account's headers, a cheap model, and the windows its answer
// tells are kept, with the request counted as answered.
func TestWarmCodexThroughUpstream(t *testing.T) {
	var got map[string]any
	var account string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		account = r.Header.Get("chatgpt-account-id")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-codex-primary-used-percent", "1")
		w.Header().Set("x-codex-primary-window-minutes", "300")
		w.Header().Set("x-codex-primary-reset-after-seconds", "17999")
		io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n")
		io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\n")
	}))
	defer srv.Close()
	home := t.TempDir()
	if err := Update(home, func(f *File) error {
		return f.Put(&Entry{Name: "plus", Type: KindOpenAI, Auth: AuthOAuth, Enabled: true, BaseURL: srv.URL,
			OAuth: &Token{AccessToken: "a", RefreshToken: "r", AccountID: "acct-1", ExpiresAt: time.Now().Add(time.Hour)}})
	}); err != nil {
		t.Fatal(err)
	}
	r, err := Warm(context.Background(), home, "plus")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Model, "mini") || got["model"] != r.Model || account != "acct-1" {
		t.Fatalf("model %q, request %v, account %q", r.Model, got["model"], account)
	}
	f, _ := Load(home)
	id := f.Get("plus").ID
	l, ok := UsageOf(id)
	if !ok || len(l.Windows) != 1 || l.Windows[0].Used != 1 {
		t.Fatalf("windows not kept: %+v %v", l, ok)
	}
	if time.Since(LastSuccess(id)) > time.Minute {
		t.Fatal("the answered warm-up was not counted")
	}
	if _, err := Warm(context.Background(), home, "nope"); err == nil {
		t.Fatal("an unknown provider was warmed")
	}
}

func TestWarmModels(t *testing.T) {
	e := &Entry{Type: KindAnthropic, Auth: AuthOAuth, Warmup: &WarmOptions{Model: "claude-x"}}
	e.Models = append(e.Models, node.ModelConfig{ID: "opus", UpstreamModel: "claude-opus-5-5"})
	ms := warmModels(e)
	if ms[0] != "claude-x" || ms[1] != "claude-haiku-4-5-20251001" || ms[len(ms)-1] != "claude-opus-5-5" {
		t.Fatalf("got %v", ms)
	}
}

// A Claude login's warm-up asks Haiku through the Messages API with the
// subscription's bearer token, and keeps the unified rate-limit windows.
func TestWarmClaudeThroughUpstream(t *testing.T) {
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("anthropic-ratelimit-unified-5h-utilization", "0.02")
		w.Header().Set("anthropic-ratelimit-unified-5h-reset", strconv.FormatInt(time.Now().Add(5*time.Hour).Unix(), 10))
		w.Header().Set("anthropic-ratelimit-unified-status", "allowed")
		for _, ev := range []string{
			`{"type":"message_start","message":{"id":"m","model":"claude-haiku-4-5-20251001","usage":{"input_tokens":8,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
			`{"type":"message_stop"}`,
		} {
			io.WriteString(w, "data: "+ev+"\n\n")
		}
	}))
	defer srv.Close()
	home := t.TempDir()
	if err := Update(home, func(f *File) error {
		return f.Put(&Entry{Name: "max", Type: KindAnthropic, Auth: AuthOAuth, Enabled: true, BaseURL: srv.URL,
			OAuth: &Token{AccessToken: "acc", RefreshToken: "r", ExpiresAt: time.Now().Add(time.Hour)}})
	}); err != nil {
		t.Fatal(err)
	}
	r, err := Warm(context.Background(), home, "max")
	if err != nil {
		t.Fatal(err)
	}
	if r.Model != "claude-haiku-4-5-20251001" || got["model"] != r.Model || auth != "Bearer acc" {
		t.Fatalf("model %q, request %v, auth %q", r.Model, got["model"], auth)
	}
	if mt, _ := got["max_tokens"].(float64); mt <= 0 || mt > 64 {
		t.Fatalf("max_tokens %v", got["max_tokens"])
	}
	f, _ := Load(home)
	l, ok := UsageOf(f.Get("max").ID)
	if !ok || len(l.Windows) != 1 || l.Windows[0].Used != 2 {
		t.Fatalf("windows not kept: %+v %v", l, ok)
	}
}
