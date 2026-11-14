package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sharetoken/internal/protocol"
)

func TestAnthropicCacheConversion(t *testing.T) {
	chat, _ := parseChat([]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"system","content":[{"type":"text","text":"rules","cache_control":{"type":"ephemeral","ttl":"5m"}}]},{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral","ttl":"1h"}}]}],"tools":[{"type":"function","cache_control":{"type":"ephemeral"},"function":{"name":"lookup","parameters":{"type":"object"}}}]}`))
	req, err := anthropicRequest(chat, 100)
	if err != nil {
		t.Fatal(err)
	}
	normalizeAnthropicCache(req)
	if got := strings.Count(asJSON(t, req), `"ttl":"1h"`); got != 3 {
		t.Fatalf("TTL order: %s", asJSON(t, req))
	}
	for i := 0; i < 5; i++ {
		req["tools"] = append(req["tools"].([]any), map[string]any{"name": fmt.Sprint(i), "cache_control": map[string]any{"type": "ephemeral"}})
	}
	normalizeAnthropicCache(req)
	if got := strings.Count(asJSON(t, req), `"cache_control"`); got != 4 {
		t.Fatalf("cache count=%d", got)
	}
	chat.Messages[0].Content = json.RawMessage(`[{"type":"text","text":"bad","cache_control":{"type":"ephemeral","ttl":"7d"}}]`)
	if _, err := anthropicRequest(chat, 100); err == nil {
		t.Fatal("accepted invalid cache TTL")
	}
}

func TestAnthropicProfileHTTPAndBilling(t *testing.T) {
	received := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("beta") != "true" || r.Header.Get("X-Client-Request-Id") == "" {
			t.Error("missing profile headers")
		}
		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)
		received <- request
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []string{
			`{"type":"message_start","message":{"usage":{"input_tokens":2000,"cache_read_input_tokens":1000}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"pong"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`{"type":"message_stop"}`,
		} {
			fmt.Fprintln(w, "data: "+event+"\n")
		}
	}))
	defer srv.Close()
	e := parityEntry()
	e.BaseURL = srv.URL
	up, err := e.Upstream(FixedToken(e.OAuth))
	if err != nil {
		t.Fatal(err)
	}
	res, err := Probe(context.Background(), up, "claude-sonnet-4-6", "ping")
	if err != nil {
		t.Fatal(err)
	}
	if res.PromptReported != 3000 || res.PromptTokens >= 100 || res.Text != "pong" {
		t.Fatalf("billing: %+v", res)
	}
	if err := protocol.UsagePlausible(res.PromptTokens, res.CompletionTokens, 10, 1); err != nil {
		t.Fatal(err)
	}
	if (<-received)["max_tokens"].(float64) != 512 {
		t.Fatal("budget changed")
	}
}

func TestAnthropicThinkingHistoryRoundTrip(t *testing.T) {
	var blocks []json.RawMessage
	var reasoning strings.Builder
	s := &anthropicStream{out: newChunks("model", func(data []byte) error {
		info, err := protocol.ParseChunk(data)
		blocks = append(blocks, info.AnthropicThinking...)
		reasoning.WriteString(info.Reasoning)
		return err
	}), tool: map[int]int{}}
	for _, event := range []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"plan"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"opaque-signature"}}`,
		`{"type":"content_block_stop","index":0}`,
	} {
		if err := s.event([]byte(event)); err != nil {
			t.Fatal(err)
		}
	}
	if reasoning.String() != "plan" || len(blocks) != 1 {
		t.Fatal("thinking was duplicated or lost")
	}
	chat := &chatRequest{Model: "claude-sonnet-5-5", Messages: []chatMessage{{Role: "user", Content: json.RawMessage(`"go"`)}, {Role: "assistant", AnthropicThinking: blocks, ToolCalls: []chatToolCall{{ID: "tool1", Type: "function"}}}, {Role: "tool", ToolCallID: "tool1", Content: json.RawMessage(`"done"`)}}}
	req, err := anthropicRequest(chat, 100)
	if err != nil {
		t.Fatal(err)
	}
	wantAll(t, asJSON(t, req), `"signature":"opaque-signature"`, `"thinking":"plan"`, `"tool_use_id":"tool1"`)
	if _, err := anthropicThinkingHistory([]json.RawMessage{json.RawMessage(`{"type":"thinking","thinking":"forged"}`)}); err == nil {
		t.Fatal("accepted unsigned thinking")
	}
}
