package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"sharetoken/internal/node"
)

func TestAnthropicModelCapabilities(t *testing.T) {
	req := func(fields string) map[string]any {
		m, err := decodeObject([]byte("{" + fields + "}"))
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	for _, model := range []string{"claude-sonnet-5-5", "claude-haiku-5-5", "claude-fable-5-2", "claude-mythos-5-2"} {
		t.Run(model, func(t *testing.T) {
			for _, value := range []string{"0", "0.7", "1.5"} {
				if validateAnthropicRequest(req(`"temperature":`+value), model, "") == nil {
					t.Errorf("accepted temperature %v", value)
				}
				r := req(`"temperature":` + value)
				if err := validateAnthropicRequest(r, model, "default"); err != nil || r["temperature"] != nil {
					t.Errorf("default sampling: %v", err)
				}
			}
			if err := validateAnthropicRequest(req(`"temperature":1`), model, ""); err != nil {
				t.Fatal(err)
			}
			if err := validateAnthropicRequest(req(`"top_p":0.99`), model, ""); err != nil {
				t.Fatal(err)
			}
			if validateAnthropicRequest(req(`"temperature":1,"top_p":0.99`), model, "") == nil {
				t.Fatal("accepted both sampling fields")
			}
			err := validateAnthropicRequest(req(`"top_p":1`), model, "")
			if (err == nil) != (model == "claude-sonnet-5-5") {
				t.Fatalf("top_p=1: %v", err)
			}
		})
	}
	for _, model := range []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5", "claude-fable-5-2", "claude-mythos-5-2", "claude-sonnet-4-6", "other-5-5"} {
		err := validateAnthropicRequest(req(`"tool_choice":{"type":"any"}`), model, "default")
		allowed := model == "claude-haiku-5-5" || model == "claude-sonnet-4-6" || model == "other-5-5"
		if (err == nil) != allowed {
			t.Errorf("forced tool for %s: %v", model, err)
		}
	}
}

func TestClaudeFingerprintUTF16(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"", "49b"}, {"hi", "49b"}, {"abcdefghijklmnopqrstuvwxyz", "2e9"},
		{"这是一个用于验证中文指纹计算的测试消息，请保持一致。", "c7e"},
		{"abcd😀fghijklmnopqrstuvwxyz", "d96"}, {"abcd😀😀efghijklmnopqrstuvwxyz", "0e9"},
	} {
		if got := claudeFingerprint(tc.text, "2.1.283"); got != tc.want {
			t.Errorf("%q: %s != %s", tc.text, got, tc.want)
		}
	}
}

func parityEntry() *Entry {
	return &Entry{ID: "provider-id", Name: "claude", Type: KindAnthropic, Auth: AuthOAuth,
		OAuth:     &Token{AccessToken: "access", AccountUUID: "user-uuid", AccountID: "org-uuid"},
		Identity:  &AnthropicIdentity{DeviceID: strings.Repeat("a", 64), SessionKey: strings.Repeat("b", 64)},
		Anthropic: &AnthropicOptions{Profile: "claude-code"}}
}

func TestClaudeProfileIdentityAndRequest(t *testing.T) {
	e := parityEntry()
	ctx := node.WithRequestScope(context.Background(), "buyer-a")
	const body = `{"model":"claude-sonnet-4-6","max_tokens":37,"metadata":{"user_id":"untrusted-user"},"system":"Return JSON.","messages":[{"role":"user","content":"hello"}]}`
	build := func(ctx context.Context, model, body string) (map[string]any, http.Header) {
		req, err := decodeObject([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		req["model"] = model
		h, err := applyClaudeOAuthProfile(ctx, req, e, e.OAuth)
		if err != nil {
			t.Fatal(err)
		}
		return req, h
	}
	req, h := build(ctx, "claude-sonnet-4-6", body)
	if number(req["max_tokens"]) != 37 {
		t.Fatal("profile increased output budget")
	}
	if len(req["system"].([]any)) != 3 || len(req["tools"].([]any)) != 0 {
		t.Fatal("wrong profile blocks")
	}
	if h.Get("x-api-key") != "" || h.Get("Authorization") != "Bearer access" || len(strings.Split(h.Get("anthropic-beta"), ",")) != 9 {
		t.Fatal("wrong authentication/betas")
	}
	var uid claudeUserID
	if err := json.Unmarshal([]byte(req["metadata"].(map[string]any)["user_id"].(string)), &uid); err != nil {
		t.Fatal(err)
	}
	if uid.AccountUUID != "user-uuid" || uid.SessionID != h.Get("X-Claude-Code-Session-Id") {
		t.Fatalf("bad metadata: %+v", uid)
	}
	text := asJSON(t, req)
	if strings.Contains(text, "untrusted-user") || strings.Contains(text, "buyer-a") || strings.Contains(text, "org-uuid") {
		t.Fatal("incorrect identity forwarded")
	}
	wantAll(t, text, "[System Instructions]", "Return JSON.", "Understood.", "cc_version="+claudeCLIVersion)
	_, next := build(ctx, "claude-sonnet-4-6", body)
	if h.Get("X-Client-Request-Id") == next.Get("X-Client-Request-Id") || h.Get("X-Claude-Code-Session-Id") != next.Get("X-Claude-Code-Session-Id") {
		t.Fatal("request/session lifetime mismatch")
	}
	fable, _ := build(ctx, "claude-fable-5-2", body)
	if len(fable["system"].([]any)) != 2 {
		t.Fatal("Fable expansion was not omitted")
	}
	r2, _ := decodeObject([]byte(body))
	if claudeSession(ctx, e, r2) != uid.SessionID {
		t.Fatal("session changed")
	}
	if claudeSession(node.WithRequestScope(ctx, "buyer-b"), e, r2) == uid.SessionID {
		t.Fatal("buyers share session")
	}
	if claudeSession(node.WithRequestSession(ctx, "conversation-two"), e, r2) == uid.SessionID {
		t.Fatal("explicit session ignored")
	}
	u, err := anthropicMessagesURL("https://api.example.test/prefix?route=a", true)
	if err != nil || u != "https://api.example.test/prefix/v1/messages?beta=true&route=a" {
		t.Fatalf("URL: %s %v", u, err)
	}

	// Claude Code's own request keeps its system prompt: only the billing
	// line (the buyer's fingerprint) and the account change.
	cc := `{"model":"claude-sonnet-4-6","max_tokens":37,"metadata":{"user_id":"{\"device_id\":\"buyer-device\",\"session_id\":\"s\"}"},
		"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.325.405; cc_entrypoint=sdk-cli;"},{"type":"text","text":"You are a Claude agent, built on Anthropic's Claude Agent SDK.","cache_control":{"type":"ephemeral"}},{"type":"text","text":"rules"}],
		"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`
	req, h = build(ctx, "claude-sonnet-4-6", cc)
	sys := req["system"].([]any)
	if len(sys) != 3 || !strings.Contains(sys[0].(map[string]any)["text"].(string), "cc_version="+claudeCLIVersion+".") || sys[2].(map[string]any)["text"] != "rules" {
		t.Fatalf("Claude Code system changed: %s", asJSON(t, sys))
	}
	text = asJSON(t, req)
	if strings.Contains(text, "buyer-device") || strings.Contains(text, "[System Instructions]") || h.Get("X-Claude-Code-Session-Id") == "" {
		t.Fatalf("Claude Code request: %s", text)
	}
}
