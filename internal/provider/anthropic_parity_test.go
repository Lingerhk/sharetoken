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
	for _, model := range []string{"claude-sonnet-5-5", "claude-haiku-5-5", "claude-fable-5-2", "claude-mythos-5-2"} {
		t.Run(model, func(t *testing.T) {
			for _, value := range []float64{0, .7, 1.5} {
				c := &chatRequest{Model: model, Temperature: &value}
				if validateAnthropicParameters(c, "") == nil {
					t.Errorf("accepted temperature %v", value)
				}
				if err := validateAnthropicParameters(c, "default"); err != nil || c.Temperature != nil {
					t.Errorf("default sampling: %v", err)
				}
			}
			one, p := 1.0, .99
			if err := validateAnthropicParameters(&chatRequest{Model: model, Temperature: &one}, ""); err != nil {
				t.Fatal(err)
			}
			if err := validateAnthropicParameters(&chatRequest{Model: model, TopP: &p}, ""); err != nil {
				t.Fatal(err)
			}
			if validateAnthropicParameters(&chatRequest{Model: model, Temperature: &one, TopP: &p}, "") == nil {
				t.Fatal("accepted both sampling fields")
			}
			p = 1
			err := validateAnthropicParameters(&chatRequest{Model: model, TopP: &p}, "")
			if (err == nil) != (model == "claude-sonnet-5-5") {
				t.Fatalf("top_p=1: %v", err)
			}
		})
	}
	for _, model := range []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5", "claude-fable-5-2", "claude-mythos-5-2", "claude-sonnet-4-6", "other-5-5"} {
		c := &chatRequest{Model: model, ToolChoice: json.RawMessage(`"required"`)}
		err := validateAnthropicParameters(c, "default")
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
	chat, _ := parseChat([]byte(`{"model":"claude-sonnet-4-6","max_tokens":37,"user":"untrusted-user","messages":[{"role":"system","content":"Return JSON."},{"role":"user","content":"hello"}]}`))
	build := func(model string) (map[string]any, http.Header) {
		c := *chat
		c.Model = model
		req, err := anthropicRequest(&c, 37)
		if err != nil {
			t.Fatal(err)
		}
		h, err := applyClaudeOAuthProfile(ctx, req, &c, e, e.OAuth)
		if err != nil {
			t.Fatal(err)
		}
		return req, h
	}
	req, h := build(chat.Model)
	if req["max_tokens"] != uint64(37) {
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
	_, next := build(chat.Model)
	if h.Get("X-Client-Request-Id") == next.Get("X-Client-Request-Id") || h.Get("X-Claude-Code-Session-Id") != next.Get("X-Claude-Code-Session-Id") {
		t.Fatal("request/session lifetime mismatch")
	}
	fable, _ := build("claude-fable-5-2")
	if len(fable["system"].([]any)) != 2 {
		t.Fatal("Fable expansion was not omitted")
	}
	chat.Messages = append(chat.Messages, chatMessage{Role: "user", Content: json.RawMessage(`"next"`)})
	if claudeSession(ctx, e, chat) != uid.SessionID {
		t.Fatal("session changed after append")
	}
	if claudeSession(node.WithRequestScope(ctx, "buyer-b"), e, chat) == uid.SessionID {
		t.Fatal("buyers share session")
	}
	chat.SessionID = "conversation-two"
	if claudeSession(ctx, e, chat) == uid.SessionID {
		t.Fatal("explicit session ignored")
	}
	u, err := anthropicMessagesURL("https://api.example.test/prefix?route=a", true)
	if err != nil || u != "https://api.example.test/prefix/v1/messages?beta=true&route=a" {
		t.Fatalf("URL: %s %v", u, err)
	}
}
