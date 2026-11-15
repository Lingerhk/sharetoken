package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sharetoken/internal/node"
	"sharetoken/internal/protocol"
)

func TestAnthropicCacheNormalization(t *testing.T) {
	req, err := decodeObject([]byte(`{"model":"claude-sonnet-4-6",
		"system":[{"type":"text","text":"rules","cache_control":{"type":"ephemeral","ttl":"5m"}}],
		"messages":[{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral","ttl":"1h"}}]}],
		"tools":[{"name":"lookup","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral"}}]}`))
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
}

// anthropicServer answers Messages requests with events and records what
// it was sent.
func anthropicServer(t *testing.T, events ...string) (*httptest.Server, chan *http.Request, chan map[string]any) {
	reqs, bodies := make(chan *http.Request, 4), make(chan map[string]any, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		reqs <- r
		bodies <- body
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range events {
			var head struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal([]byte(ev), &head)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", head.Type, ev)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, reqs, bodies
}

var messagesOK = []string{
	`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"usage":{"input_tokens":2000,"cache_read_input_tokens":1000,"output_tokens":1}}}`,
	`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pong"}}`,
	`{"type":"content_block_stop","index":0}`,
	`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
	`{"type":"message_stop"}`,
}

// An API-key Messages request goes out as it came: the buyer's betas and
// version kept, the output limit and thinking budget within the model's
// limit, the events passed on unchanged.
func TestAnthropicPassthrough(t *testing.T) {
	srv, reqs, bodies := anthropicServer(t, messagesOK...)
	e := &Entry{ID: "k", Name: "anthropic", Type: KindAnthropic, Auth: AuthAPIKey, APIKey: "sk-ant-test", BaseURL: srv.URL}
	up, err := e.Upstream(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := up.APIs(); len(got) != 1 || got[0] != protocol.APIMessages {
		t.Fatalf("APIs = %v", got)
	}
	body := `{"model":"claude-3-5-haiku-20241022","max_tokens":32768,"stream":true,"system":"be brief",
		"thinking":{"type":"enabled","budget_tokens":20000},
		"messages":[{"role":"user","content":"ping"},
			{"role":"assistant","content":[{"type":"thinking","thinking":"t","signature":"real-sig"},{"type":"thinking","thinking":"from chat","signature":""},
				{"type":"thinking","thinking":"converted","signature":"sharetoken-reasoning-v1:abc"},{"type":"text","text":"ok"}]},
			{"role":"user","content":"again"}]}`
	var got []string
	err = up.Stream(context.Background(), &node.Request{API: protocol.APIMessages, Header: protocol.RequestHeader{
		"anthropic-version": "2023-06-01", "anthropic-beta": "interleaved-thinking-2025-05-14"}, Body: []byte(body)}, func(p []byte) error {
		got = append(got, string(p))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "\n") != strings.Join(messagesOK, "\n") {
		t.Fatalf("events changed:\n%s", strings.Join(got, "\n"))
	}
	r, sent := <-reqs, <-bodies
	if r.Header.Get("x-api-key") != "sk-ant-test" || r.Header.Get("anthropic-beta") != "interleaved-thinking-2025-05-14" || r.Header.Get("anthropic-version") != "2023-06-01" {
		t.Fatalf("headers %v", r.Header)
	}
	if sent["max_tokens"].(float64) != 8192 || sent["thinking"].(map[string]any)["budget_tokens"].(float64) != 8191 || sent["system"] != "be brief" {
		t.Fatalf("body %v", sent)
	}
	// Thinking blocks no Anthropic model signed (converted ones, or empty
	// signatures from a chat history) are dropped; the model's own stay.
	text := asJSON(t, sent["messages"])
	if !strings.Contains(text, "real-sig") || strings.Contains(text, "from chat") || strings.Contains(text, "converted") {
		t.Fatalf("thinking history: %s", text)
	}

	// An error event fails the job as the server's; a stream without its
	// end too.
	srv2, _, _ := anthropicServer(t, messagesOK[0], `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`)
	e.BaseURL = srv2.URL
	up, _ = e.Upstream(nil)
	var se *node.StatusError
	if err := up.Stream(context.Background(), &node.Request{API: protocol.APIMessages, Body: []byte(body)}, func([]byte) error { return nil }); !errors.As(err, &se) || se.Status != 503 {
		t.Fatalf("error event: %v", err)
	}
	srv3, _, _ := anthropicServer(t, messagesOK[:3]...)
	e.BaseURL = srv3.URL
	up, _ = e.Upstream(nil)
	if err := up.Stream(context.Background(), &node.Request{API: protocol.APIMessages, Body: []byte(body)}, func([]byte) error { return nil }); err == nil {
		t.Fatal("a stream without message_stop was accepted")
	}
}

// A subscription login with the claude-code profile: Probe (a chat request,
// converted) reaches the Messages endpoint with the profile, and the seller
// pays for the instructions the profile adds.
func TestAnthropicProfileHTTPAndBilling(t *testing.T) {
	srv, reqs, bodies := anthropicServer(t, messagesOK...)
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
	if res.PromptTokens != 3000 || res.Text != "pong" {
		t.Fatalf("probe: %+v", res)
	}
	r, sent := <-reqs, <-bodies
	if r.URL.Query().Get("beta") != "true" || r.Header.Get("X-Client-Request-Id") == "" || r.Header.Get("Authorization") != "Bearer access" {
		t.Fatal("missing profile headers")
	}
	if sent["max_tokens"].(float64) != 512 {
		t.Fatal("budget changed")
	}
	// The cap the node bills by.
	b := &node.Billing{}
	ctx := node.WithBilling(context.Background(), b)
	body := `{"model":"claude-sonnet-4-6","max_tokens":100,"messages":[{"role":"user","content":"hello there"}]}`
	if err := up.Stream(ctx, &node.Request{API: protocol.APIMessages, Body: []byte(body)}, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	est, _ := protocol.EstimatePromptTokens(protocol.APIMessages, []byte(body))
	if b.PromptCap != est {
		t.Fatalf("prompt cap %d, want the estimate %d", b.PromptCap, est)
	}
}

// OpenAI's own API speaks Responses: a Responses request goes to /responses
// within the model's output limit, and the events come back as they are.
func TestOpenAIResponsesPassthrough(t *testing.T) {
	events := []string{
		`{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","status":"in_progress","output":[]}}`,
		`{"type":"response.output_text.delta","sequence_number":1,"delta":"hi"}`,
		`{"type":"response.completed","sequence_number":2,"response":{"id":"resp_1","status":"completed","usage":{"input_tokens":5,"output_tokens":1}}}`,
	}
	var path string
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&sent)
		for _, ev := range events {
			fmt.Fprintf(w, "data: %s\n\n", ev)
		}
	}))
	defer srv.Close()
	e := &Entry{ID: "o", Name: "openai", Type: KindOpenAI, Auth: AuthAPIKey, APIKey: "sk-test", BaseURL: srv.URL,
		Models: []node.ModelConfig{{ID: "gpt-x", UpstreamModel: "gpt-x", MaxOutputTokens: 1000}}}
	up, err := e.Upstream(nil)
	if err != nil {
		t.Fatal(err)
	}
	if apis := up.APIs(); strings.Join(apis, ",") != "chat,responses" {
		t.Fatalf("APIs = %v", apis)
	}
	var got []string
	body := `{"model":"gpt-x","stream":true,"max_output_tokens":5000,"input":"hi"}`
	if err := up.Stream(context.Background(), &node.Request{API: protocol.APIResponses, Body: []byte(body)}, func(p []byte) error {
		got = append(got, string(p))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if path != "/responses" || sent["max_output_tokens"].(float64) != 1000 || strings.Join(got, "\n") != strings.Join(events, "\n") {
		t.Fatalf("path %s body %v events %v", path, sent, got)
	}
}

// DeepSeek also serves the Messages API at /anthropic, with its key as
// x-api-key.
func TestDeepSeekMessages(t *testing.T) {
	srv, reqs, _ := anthropicServer(t, messagesOK...)
	e := &Entry{ID: "d", Name: "deepseek", Type: KindDeepSeek, Auth: AuthAPIKey, APIKey: "sk-ds", BaseURL: srv.URL}
	up, err := e.Upstream(nil)
	if err != nil {
		t.Fatal(err)
	}
	if apis := up.APIs(); strings.Join(apis, ",") != "chat,messages" {
		t.Fatalf("APIs = %v", apis)
	}
	body := `{"model":"deepseek-chat","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	if err := up.Stream(context.Background(), &node.Request{API: protocol.APIMessages, Body: []byte(body)}, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	r := <-reqs
	if r.URL.Path != "/anthropic/v1/messages" || r.Header.Get("x-api-key") != "sk-ds" || r.Header.Get("Authorization") != "" {
		t.Fatalf("path %s headers %v", r.URL.Path, r.Header)
	}
}

// The Codex backend: its rules applied to a Responses request, the stream
// cut where the buyer's reservation ends, the reserved tool name restored,
// and /responses/compact used natively.
func TestCodexPassthrough(t *testing.T) {
	var sent []map[string]any
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at-1" || r.Header.Get("chatgpt-account-id") != "acct-1" || r.Header.Get("originator") != "codex_cli_rs" ||
			!strings.HasPrefix(r.Header.Get("User-Agent"), "codex_cli_rs/"+r.Header.Get("version")) || r.Header.Get("session_id") != "sess-1" {
			http.Error(w, "bad headers", http.StatusNotFound)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		sent = append(sent, body)
		paths = append(paths, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/compact") {
			_, _ = io.WriteString(w, `{"id":"resp_c","object":"response.compaction","output":[{"id":"cmp_1","type":"compaction","encrypted_content":"OPAQUE"}],"usage":{"input_tokens":900,"output_tokens":40}}`)
			return
		}
		fl := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\",\"output\":[]}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"sequence_number\":1,\"output_index\":0,\"item\":{\"type\":\"function_call\",\"name\":\"python__st\",\"call_id\":\"c1\"}}\n\n")
		for i := range 2000 {
			if _, err := fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"sequence_number\":%d,\"delta\":\"word word word word \"}\n\n", i+2); err != nil {
				return
			}
			fl.Flush()
		}
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":5,\"output_tokens\":9999}}}\n\n")
	}))
	defer srv.Close()
	up := &codexUpstream{base: srv.URL, ts: FixedToken(&Token{AccessToken: "at-1", AccountID: "acct-1"})}
	body := `{"model":"gpt-5.4","stream":true,"max_output_tokens":50,"temperature":0.2,"store":false,
		"tools":[{"type":"function","name":"python","parameters":{"type":"object"}}],
		"input":[{"type":"message","role":"user","content":"go on"},{"type":"function_call","name":"python","call_id":"c0","arguments":"{}"},
			{"type":"compaction","encrypted_content":"` + "sharetoken-compaction-v1:" + `c3VtbWFyeQ=="}]}`
	var last string
	var meter protocol.UsageMeter
	var text strings.Builder
	err := up.Stream(context.Background(), &node.Request{API: protocol.APIResponses, Header: protocol.RequestHeader{"session_id": "sess-1"}, Body: []byte(body)}, func(p []byte) error {
		last = string(p)
		info, _ := protocol.ParseEvent(protocol.APIResponses, p)
		meter.Add(info)
		text.WriteString(info.Text())
		if strings.Contains(string(p), "python__st") {
			t.Errorf("reserved name leaked: %s", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	u := meter.Usage()
	if !strings.Contains(last, `"type":"response.incomplete"`) || !strings.Contains(last, `"max_output_tokens"`) || u == nil || u.CompletionTokens < 50 || u.CompletionTokens > 80 {
		t.Fatalf("last %s usage %+v", last, u)
	}
	req := asJSON(t, sent[0])
	wantAll(t, req, `"instructions":""`, `"store":false`, `"include":["reasoning.encrypted_content"]`, `"name":"python__st"`,
		`conversation_summary\u003e\nsummary\n`)
	for _, banned := range []string{"temperature", "max_output_tokens"} {
		if strings.Contains(req, banned) {
			t.Errorf("%s must not reach the Codex backend: %s", banned, req)
		}
	}
	// Compact.
	var ev string
	err = up.Compact(context.Background(), &node.Request{API: protocol.APIResponses, Op: protocol.OpCompact, Header: protocol.RequestHeader{"session_id": "sess-1"},
		Body: []byte(`{"model":"gpt-5.4","stream":true,"max_output_tokens":50,"input":[{"type":"message","role":"user","content":"hi"}]}`)}, func(p []byte) error {
		ev = string(p)
		return nil
	})
	if err != nil || paths[len(paths)-1] != "/responses/compact" || !strings.Contains(ev, `"type":"response.completed"`) || !strings.Contains(ev, `"encrypted_content":"OPAQUE"`) {
		t.Fatalf("compact: %v %v %s", err, paths, ev)
	}
	if _, ok := sent[len(sent)-1]["stream"]; ok {
		t.Error("compact must not ask for a stream")
	}
}

// Gemini wants each tool call's thought signature back: a call that lost it
// in a conversion gets Gemini's placeholder; a real one stays.
func TestGeminiToolCallsKeepASignature(t *testing.T) {
	in := `{"model":"g","messages":[{"role":"user","content":"hi"},
		{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"f","arguments":"{}"}},
			{"id":"b","type":"function","function":{"name":"f","arguments":"{}"},"extra_content":{"google":{"thought_signature":"real-sig"}}}]},
		{"role":"tool","tool_call_id":"a","content":"1"},{"role":"tool","tool_call_id":"b","content":"2"}]}`
	out := string(signToolCalls([]byte(in)))
	if strings.Count(out, `"thought_signature":"skip_thought_signature_validator"`) != 1 || !strings.Contains(out, `"thought_signature":"real-sig"`) {
		t.Fatalf("signed: %s", out)
	}
	plain := `{"model":"g","messages":[{"role":"user","content":"hi"}]}`
	if string(signToolCalls([]byte(plain))) != plain {
		t.Fatal("a request without tool calls changed")
	}
}

// A model that refuses the thinking form it was sent gets the other one.
func TestAnthropicThinkingFormIsSwapped(t *testing.T) {
	var sent []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		sent = append(sent, body)
		if th, _ := body["thinking"].(map[string]any); th["type"] == "enabled" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"\"thinking.type.enabled\" is not supported for this model. Use \"thinking.type.adaptive\" and \"output_config.effort\" to control thinking behavior."}}`)
			return
		}
		for _, ev := range messagesOK {
			fmt.Fprintf(w, "data: %s\n\n", ev)
		}
	}))
	defer srv.Close()
	e := &Entry{ID: "k", Name: "anthropic", Type: KindAnthropic, Auth: AuthAPIKey, APIKey: "k", BaseURL: srv.URL}
	up, _ := e.Upstream(nil)
	body := `{"model":"claude-haiku-5-5","max_tokens":4000,"thinking":{"type":"enabled","budget_tokens":4096},"messages":[{"role":"user","content":"hi"}]}`
	if err := up.Stream(context.Background(), &node.Request{API: protocol.APIMessages, Body: []byte(body)}, func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 || asJSON(t, sent[1]["thinking"]) != `{"type":"adaptive"}` || asJSON(t, sent[1]["output_config"]) != `{"effort":"medium"}` {
		t.Fatalf("sent %v", sent)
	}
	// The reverse, and no loop when the other form is refused too.
	out, ok := swapThinking([]byte(`{"max_tokens":3000,"thinking":{"type":"adaptive"},"output_config":{"effort":"high"}}`), `"thinking.type.adaptive" is not supported`)
	if !ok || string(out) != `{"max_tokens":3000,"thinking":{"budget_tokens":2999,"type":"enabled"}}` {
		t.Fatalf("to budget: %s %v", out, ok)
	}
	if _, ok := swapThinking([]byte(`{"thinking":{"type":"adaptive"}}`), `"thinking.type.enabled" is not supported`); ok {
		t.Fatal("swapped a form that was not refused")
	}
}

// A model that takes no system messages in the conversation gets them as
// user text, joined to the user turn.
func TestAnthropicSystemMessagesBecomeUserText(t *testing.T) {
	body := `{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]},{"role":"system","content":[{"type":"text","text":"be brief"}]},{"role":"assistant","content":"ok"}]}`
	out, ok := systemToUser([]byte(body), `role 'system' is not supported on this model`)
	want := `{"messages":[{"content":[{"text":"hi","type":"text"},{"text":"\u003csystem-reminder\u003e\nbe brief\n\u003c/system-reminder\u003e","type":"text"}],"role":"user"},{"content":"ok","role":"assistant"}]}`
	if !ok || string(out) != want {
		t.Fatalf("%s", out)
	}
	if _, ok := systemToUser([]byte(body), "something else"); ok {
		t.Fatal("changed on another refusal")
	}
}
