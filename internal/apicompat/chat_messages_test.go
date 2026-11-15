package apicompat

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func chatMessagesReq(t *testing.T, body []byte) map[string]any {
	t.Helper()
	r, err := chatParse(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := chatMessagesRequest(r)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

// The provider's request test.
func TestChatToMessagesFullRequest(t *testing.T) {
	req, _ := chatConvert(t, Messages, []byte(chatFullRequest), "")
	got := chatJSON(t, req)
	chatWant(t, got,
		`"system":[{"text":"Be brief.","type":"text"}]`, `"stream":true`,
		`"max_tokens":900`, `"temperature":0.4`, `"stop_sequences":["END"]`,
		`{"source":{"data":"QUJD","media_type":"image/png","type":"base64"},"type":"image"}`,
		`{"id":"call_1","input":{"q":"cat"},"name":"lookup","type":"tool_use"}`,
		// The tool result and the next user message are one user turn.
		`{"content":[{"content":[{"text":"a cat","type":"text"}],"tool_use_id":"call_1","type":"tool_result"},{"text":"And now?","type":"text"}],"role":"user"}`,
		`"tool_choice":{"type":"any"}`, `"input_schema":{`)
	chatWantNot(t, got, "top_p", "stream_options")
	// A model with a small output limit gets no more than it takes.
	var full map[string]any
	_ = json.Unmarshal([]byte(chatFullRequest), &full)
	full["model"], full["max_tokens"] = "claude-3-5-haiku-20241022", 32768
	if req, _ = chatConvert(t, Messages, []byte(chatJSON(t, full)), ""); req["max_tokens"] != float64(8192) {
		t.Errorf("max_tokens = %v", req["max_tokens"])
	}
	delete(full, "max_tokens")
	if req, _ = chatConvert(t, Messages, []byte(chatJSON(t, full)), ""); req["max_tokens"] != float64(8192) {
		t.Errorf("no max_tokens: %v", req["max_tokens"])
	}
}

// The provider's cache test: breakpoints on parts and tools, TTL order, at
// most four, invalid TTLs refused.
func TestChatToMessagesCache(t *testing.T) {
	body := chatBody(t, "claude-sonnet-4-6", `"messages":[{"role":"system","content":[{"type":"text","text":"rules","cache_control":{"type":"ephemeral","ttl":"5m"}}]},{"role":"user","content":[{"type":"text","text":"hello","cache_control":{"type":"ephemeral","ttl":"1h"}}]}],"tools":[{"type":"function","cache_control":{"type":"ephemeral"},"function":{"name":"lookup","parameters":{"type":"object"}}}]`)
	req := chatMessagesReq(t, body)
	if got := strings.Count(chatJSON(t, req), `"ttl":"1h"`); got != 3 {
		t.Fatalf("TTL order: %s", chatJSON(t, req))
	}
	for i := 0; i < 5; i++ {
		req["tools"] = append(req["tools"].([]any), map[string]any{"name": fmt.Sprint(i), "cache_control": map[string]any{"type": "ephemeral"}})
	}
	chatNormalizeCache(req)
	if got := strings.Count(chatJSON(t, req), `"cache_control"`); got != 4 {
		t.Fatalf("cache count=%d", got)
	}
	for _, bad := range []string{
		`"messages":[{"role":"user","content":[{"type":"text","text":"bad","cache_control":{"type":"ephemeral","ttl":"7d"}}]}]`,
		`"messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","cache_control":{"type":"persistent"},"function":{"name":"f"}}]`,
	} {
		if _, err := Convert(Chat, Messages, chatBody(t, "claude-sonnet-4-6", bad), Options{}); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	// Breakpoints on images, tool results and thinking blocks.
	req = chatMessagesReq(t, chatBody(t, "claude-sonnet-4-6", `"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://x/a.png"},"cache_control":{"type":"ephemeral"}}]},
{"role":"assistant","anthropic_thinking":[{"type":"thinking","thinking":"t","signature":"s"}],"tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{}"}}]},
{"role":"tool","tool_call_id":"c","content":[{"type":"text","text":"r","cache_control":{"type":"ephemeral","ttl":"1h"}}]}]`))
	got := chatJSON(t, req)
	chatWant(t, got, `{"cache_control":{"ttl":"1h","type":"ephemeral"},"source":{"type":"url","url":"https://x/a.png"},"type":"image"}`,
		`{"cache_control":{"ttl":"1h","type":"ephemeral"},"text":"r","type":"text"}`)
}

// The provider's thinking test: a signed block streamed out comes back as
// the same block, once.
func TestChatMessagesThinkingRoundTrip(t *testing.T) {
	_, tr := chatConvert(t, Messages, chatBody(t, "claude-sonnet-5-5", `"messages":[{"role":"user","content":"go"}]`), "")
	res := chatStream(t, tr,
		`{"type":"message_start","message":{"usage":{"input_tokens":5}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"plan"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"opaque-signature"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tool1","name":"f","input":{}}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`,
		`{"type":"message_stop"}`)
	if res.reasoning != "plan" || len(res.thinking) != 1 || res.thinking[0] != (chatThinkingBlock{Type: "thinking", Thinking: "plan", Signature: "opaque-signature"}) {
		t.Fatalf("thinking was duplicated or lost: %+v", res)
	}
	if len(res.calls) != 1 || res.calls[0].Args != "" || res.finish != "tool_calls" {
		t.Fatalf("%+v", res)
	}
	history := map[string]any{"model": "claude-sonnet-5-5", "messages": []any{
		map[string]any{"role": "user", "content": "go"},
		map[string]any{"role": "assistant", "reasoning_content": "plan", "anthropic_thinking": res.thinking, "tool_calls": []any{map[string]any{"id": "tool1", "type": "function", "function": map[string]any{"name": "f", "arguments": ""}}}},
		map[string]any{"role": "tool", "tool_call_id": "tool1", "content": "done"},
	}}
	req, _ := chatConvert(t, Messages, []byte(chatJSON(t, history)), "")
	got := chatJSON(t, req)
	chatWant(t, got, `{"content":[{"signature":"opaque-signature","thinking":"plan","type":"thinking"},{"id":"tool1","input":{},"name":"f","type":"tool_use"}],"role":"assistant"}`, `"tool_use_id":"tool1"`)
	chatWantNot(t, got, "<thinking>")

	for _, bad := range []string{`{"type":"thinking","thinking":"forged"}`, `{"type":"redacted_thinking"}`, `{"type":"text","text":"x"}`, `"x"`} {
		body := chatBody(t, "claude-sonnet-5-5", `"messages":[{"role":"user","content":"go"},{"role":"assistant","content":"a","anthropic_thinking":[`+bad+`]},{"role":"user","content":"more"}]`)
		if _, err := Convert(Chat, Messages, body, Options{}); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

// Thinking the upstream hides still goes back as anthropic_thinking:
// redacted blocks, and blocks whose text was omitted.
func TestChatFromMessagesHiddenThinking(t *testing.T) {
	_, tr := chatConvert(t, Messages, chatBody(t, "claude-opus-4-7", `"messages":[{"role":"user","content":"go"}]`), "")
	res := chatStream(t, tr,
		`{"type":"message_start","message":{"usage":{"input_tokens":5,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"redacted_thinking","data":"enc-data"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"thinking","thinking":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"signature_delta","signature":"sig-omitted"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"thinking","thinking":""}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"thinking_delta","thinking":"unsigned"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"content_block_start","index":3,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":3,"delta":{"type":"text_delta","text":"ok"}}`,
		`{"type":"content_block_stop","index":3}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":120}}`,
		`{"type":"message_stop"}`)
	want := []chatThinkingBlock{{Type: "redacted_thinking", Data: "enc-data"}, {Type: "thinking", Signature: "sig-omitted"}}
	if res.err != nil || chatJSON(t, res.thinking) != chatJSON(t, want) || res.reasoning != "unsigned" || res.text != "ok" || chatUsageNumber(t, res.usage, "completion_tokens") != 120 {
		t.Fatalf("%+v", res)
	}
	if !strings.Contains(res.chunks[0], `"anthropic_thinking":[{"type":"redacted_thinking","data":"enc-data"}]`) {
		t.Fatalf("chunk shape: %s", res.chunks[0])
	}
	// And it goes back as it came.
	history := map[string]any{"model": "claude-opus-4-7", "messages": []any{
		map[string]any{"role": "user", "content": "go"},
		map[string]any{"role": "assistant", "content": "ok", "anthropic_thinking": res.thinking},
		map[string]any{"role": "user", "content": "more"},
	}}
	req, _ := chatConvert(t, Messages, []byte(chatJSON(t, history)), "")
	chatWant(t, chatJSON(t, req), `{"content":[{"data":"enc-data","type":"redacted_thinking"},{"signature":"sig-omitted","thinking":"","type":"thinking"},{"text":"ok","type":"text"}],"role":"assistant"}`)
}

// The provider's stream test, and its billing test.
func TestChatFromMessagesStream(t *testing.T) {
	_, tr := chatConvert(t, Messages, chatBody(t, "claude-sonnet-4-6", `"messages":[{"role":"user","content":"go"}]`), "client-model")
	res := chatStream(t, tr,
		`{"type":"message_start","message":{"usage":{"input_tokens":10,"cache_read_input_tokens":5,"cache_creation_input_tokens":2,"output_tokens":1}}}`,
		`{"type":"ping"}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hi "}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"there"}}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"lookup"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"q\":"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"cat\"}"}}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":42}}`,
		`{"type":"message_stop"}`)
	if res.err != nil || res.text != "Hi there" || res.reasoning != "hmm" || res.finish != "tool_calls" || res.model != "client-model" {
		t.Fatalf("%+v", res)
	}
	if len(res.calls) != 1 || res.calls[0] != (chatTestCall{"toolu_1", "lookup", `{"q":"cat"}`}) {
		t.Fatalf("calls %+v", res.calls)
	}
	if chatJSON(t, res.usage) != `{"completion_tokens":42,"prompt_tokens":17,"prompt_tokens_details":{"cache_creation_tokens":2,"cached_tokens":5},"total_tokens":59}` {
		t.Fatalf("usage %s", chatJSON(t, res.usage))
	}

	_, tr = chatConvert(t, Messages, chatBody(t, "claude-sonnet-4-6", `"messages":[{"role":"user","content":"ping"}]`), "")
	res = chatStream(t, tr,
		`{"type":"message_start","message":{"usage":{"input_tokens":2000,"cache_read_input_tokens":1000}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"pong"}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
		`{"type":"message_stop"}`)
	if res.text != "pong" || chatUsageNumber(t, res.usage, "prompt_tokens") != 3000 || chatUsageNumber(t, res.usage, "prompt_tokens_details", "cached_tokens") != 1000 {
		t.Fatalf("billing: %+v", res)
	}
}

func TestChatFromMessagesEvents(t *testing.T) {
	start := `{"type":"message_start","message":{"usage":{"input_tokens":3}}}`
	for _, tc := range []struct {
		name   string
		events []string
		text   string
		calls  []chatTestCall
		finish string
		prompt uint64
	}{
		{name: "input given on the block start", events: []string{
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t1","name":"read","input":{"path":"a"}}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`}, calls: []chatTestCall{{"t1", "read", `{"path":"a"}`}}, finish: "tool_calls", prompt: 3},
		{name: "streamed input wins over the start's", events: []string{
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t1","name":"read","input":{"path":"a"}}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"b\"}"}}`,
			`{"type":"content_block_stop","index":0}`}, calls: []chatTestCall{{"t1", "read", `{"path":"b"}`}}, finish: "tool_calls", prompt: 3},
		{name: "server tools are the upstream's", events: []string{
			`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{}}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"x\"}"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[]}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":2,"delta":{"type":"citations_delta","citation":{}}}`,
			`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"found"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`}, text: "found", finish: "stop", prompt: 3},
		{name: "output limit", events: []string{`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"a"}}`, `{"type":"message_delta","delta":{"stop_reason":"max_tokens"}}`}, text: "a", finish: "length", prompt: 3},
		{name: "context window", events: []string{`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"a"}}`, `{"type":"message_delta","delta":{"stop_reason":"model_context_window_exceeded"}}`}, text: "a", finish: "length", prompt: 3},
		{name: "refusal", events: []string{`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"a"}}`, `{"type":"message_delta","delta":{"stop_reason":"refusal"}}`}, text: "a", finish: "content_filter", prompt: 3},
		{name: "stop sequence", events: []string{`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"a"}}`, `{"type":"message_delta","delta":{"stop_reason":"stop_sequence","stop_sequence":"END"}}`}, text: "a", finish: "stop", prompt: 3},
		{name: "pause turn", events: []string{`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"a"}}`, `{"type":"message_delta","delta":{"stop_reason":"pause_turn"}}`}, text: "a", finish: "stop", prompt: 3},
		{name: "input usage on message_delta", events: []string{`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"a"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":50,"cache_read_input_tokens":7,"output_tokens":2}}`}, text: "a", finish: "stop", prompt: 57},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, tr := chatConvert(t, Messages, chatBody(t, "claude-sonnet-4-6", `"messages":[{"role":"user","content":"go"}]`), "")
			res := chatStream(t, tr, append(append([]string{start}, tc.events...), `{"type":"message_stop"}`)...)
			if res.err != nil || res.text != tc.text || res.finish != tc.finish || chatJSON(t, res.calls) != chatJSON(t, tc.calls) || chatUsageNumber(t, res.usage, "prompt_tokens") != tc.prompt {
				t.Fatalf("%+v", res)
			}
		})
	}
}

func TestChatFromMessagesErrorsAndClose(t *testing.T) {
	_, tr := chatConvert(t, Messages, chatBody(t, "claude-sonnet-4-6", `"messages":[{"role":"user","content":"go"}]`), "")
	err := tr.Event([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`), func([]byte) error { return nil })
	var ue *chatUpstreamError
	if !asChatUpstreamError(err, &ue) || ue.Kind != "overloaded_error" || ue.Message != "busy" {
		t.Fatalf("error event: %v", err)
	}

	// Cut short mid-answer: the open thinking block still goes out, the
	// prompt is what message_start said, the output at least what was sent.
	_, tr = chatConvert(t, Messages, chatBody(t, "claude-sonnet-4-6", `"messages":[{"role":"user","content":"go"}]`), "")
	res := chatStream(t, tr,
		`{"type":"message_start","message":{"usage":{"input_tokens":40,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"a"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"s"}}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":"some words of text"}}`)
	if res.err != nil || res.finish != "stop" || len(res.thinking) != 1 || chatUsageNumber(t, res.usage, "prompt_tokens") != 40 || chatUsageNumber(t, res.usage, "completion_tokens") < 2 {
		t.Fatalf("%+v", res)
	}
	// Cut short before message_start reported anything.
	_, tr = chatConvert(t, Messages, chatBody(t, "claude-sonnet-4-6", `"messages":[{"role":"user","content":"a long enough question"}]`), "")
	res = chatStream(t, tr, `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t","name":"f"}}`)
	if res.err != nil || res.finish != "tool_calls" || chatUsageNumber(t, res.usage, "prompt_tokens") == 0 {
		t.Fatalf("%+v", res)
	}
	// Nothing usable came.
	_, tr = chatConvert(t, Messages, chatBody(t, "claude-sonnet-4-6", `"messages":[{"role":"user","content":"go"}]`), "")
	if res = chatStream(t, tr, `{"type":"message_start","message":{}}`, `{"type":"ping"}`, `garbage`, `{"type":"something_new"}`); res.err != errChatStreamEnded || len(res.chunks) != 0 {
		t.Fatalf("%+v", res)
	}
}

func TestChatToMessagesRequest(t *testing.T) {
	user := `{"role":"user","content":"Hi"}`
	for _, tc := range []struct {
		name, model, rest string
		want, not         []string
	}{
		{name: "json_object hint", rest: `"response_format":{"type":"json_object"},"messages":[` + user + `]`,
			want: []string{`"system":[{"text":"Answer with a single JSON object and nothing else.","type":"text"}]`}},
		{name: "json_schema hint", rest: `"response_format":{"type":"json_schema","json_schema":{"name":"a","schema":{"type":"object"}}},"messages":[{"role":"system","content":"S"},` + user + `]`,
			want: []string{`"system":[{"text":"S","type":"text"},{"text":"Answer with a single JSON object that matches this JSON schema, and nothing else:\n{\"type\":\"object\"}","type":"text"}]`}},
		{name: "temperature is clamped", rest: `"temperature":1.5,"messages":[` + user + `]`, want: []string{`"temperature":1`}},
		{name: "temperature wins over top_p", rest: `"temperature":0.2,"top_p":0.5,"messages":[` + user + `]`, want: []string{`"temperature":0.2`}, not: []string{"top_p"}},
		{name: "top_p alone", rest: `"top_p":0.5,"messages":[` + user + `]`, want: []string{`"top_p":0.5`}},
		{name: "user and stop", rest: `"user":"u-1","stop":"END","messages":[` + user + `]`, want: []string{`"metadata":{"user_id":"u-1"}`, `"stop_sequences":["END"]`}},
		{name: "fields Messages lacks are dropped", rest: `"seed":3,"presence_penalty":1,"frequency_penalty":1,"session_id":"s","messages":[` + user + `]`,
			not: []string{"seed", "penalty", "session"}},
		{name: "system and developer are the system", rest: `"messages":[{"role":"system","content":[{"type":"text","text":"A","cache_control":{"type":"ephemeral"}}]},{"role":"developer","content":"B"},` + user + `]`,
			want: []string{`"system":[{"cache_control":{"type":"ephemeral"},"text":"A","type":"text"},{"text":"B","type":"text"}]`}},
		{name: "tool_choice auto", rest: `"tool_choice":"auto","messages":[` + user + `],"tools":[{"type":"function","function":{"name":"f"}}]`,
			want: []string{`"tool_choice":{"type":"auto"}`, `"tools":[{"input_schema":{"properties":{},"type":"object"},"name":"f"}]`}},
		{name: "tool_choice none keeps the tools", rest: `"tool_choice":"none","messages":[` + user + `],"tools":[{"type":"function","function":{"name":"f"}}]`,
			want: []string{`"tool_choice":{"type":"none"}`, `"tools":[`}},
		{name: "a named tool", rest: `"tool_choice":{"type":"function","function":{"name":"f"}},"messages":[` + user + `],"tools":[{"type":"function","function":{"name":"f","description":"does f"}}]`,
			want: []string{`"tool_choice":{"name":"f","type":"tool"}`, `"description":"does f"`}},
		{name: "no parallel tool calls", rest: `"parallel_tool_calls":false,"messages":[` + user + `],"tools":[{"type":"function","function":{"name":"f"}}]`,
			want: []string{`"tool_choice":{"disable_parallel_tool_use":true,"type":"auto"}`}},
		{name: "no tools, no tool_choice", rest: `"tool_choice":"required","messages":[` + user + `]`, not: []string{"tool_choice", "tools"}},
		{name: "web search", rest: `"messages":[` + user + `],"tools":[{"type":"web_search"},{"type":"code_interpreter"}]`,
			want: []string{`"tools":[{"name":"web_search","type":"web_search_20250305"}]`}},
		{name: "a root union becomes one object", rest: `"messages":[` + user + `],"tools":[{"type":"function","function":{"name":"f","parameters":{"anyOf":[{"type":"object","properties":{"a":{"type":"string"},"c":{"type":"string"}},"required":["a","c"]},{"type":"object","properties":{"b":{"type":"string"},"c":{"type":"integer"}},"required":["b","c"]}]}}}]`,
			want: []string{`"input_schema":{"properties":{"a":{"type":"string"},"b":{"type":"string"},"c":{"anyOf":[{"type":"string"},{"type":"integer"}]}},"required":["c"],"type":"object"}`}},
		{name: "allOf requires every branch's", rest: `"messages":[` + user + `],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object","allOf":[{"properties":{"a":{}},"required":["a"]},{"type":"object","properties":{"b":{}},"required":["b"]}]}}}]`,
			want: []string{`"input_schema":{"properties":{"a":{},"b":{}},"required":["a","b"],"type":"object"}`}},
		{name: "a non-object schema becomes an empty object", rest: `"messages":[` + user + `],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"string"}}}]`,
			want: []string{`"input_schema":{"properties":{},"type":"object"}`}},
		{name: "images and documents", rest: `"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://x/a.png"}},{"type":"image_url","image_url":{"url":"data:image/png;base64,"}},{"type":"file","file":{"filename":"a.pdf","file_data":"data:application/pdf;base64,JVBE"}},{"type":"file","file":{"file_data":"data:text/plain;base64,aGVsbG8="}},{"type":"file","file":{"file_id":"file-1"}},{"type":"input_audio","input_audio":{"data":"x"}},{"type":"text","text":"  "}]}]`,
			want: []string{`{"source":{"type":"url","url":"https://x/a.png"},"type":"image"}`,
				`{"source":{"data":"JVBE","media_type":"application/pdf","type":"base64"},"title":"a.pdf","type":"document"}`,
				`{"source":{"data":"hello","media_type":"text/plain","type":"text"},"type":"document"}`},
			not: []string{"file-1", "input_audio", `"text":"  "`, `base64,"`}},
		{name: "parallel tool results are one user turn, first in it", rest: `"messages":[` + user + `,{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"f","arguments":"{}"}},{"id":"b","type":"function","function":{"name":"f","arguments":"not json"}}]},{"role":"user","content":"meanwhile"},{"role":"tool","tool_call_id":"a","content":"A"},{"role":"tool","tool_call_id":"b","content":""}]`,
			want: []string{`{"content":[{"content":[{"text":"A","type":"text"}],"tool_use_id":"a","type":"tool_result"},{"content":[{"text":"(empty)","type":"text"}],"tool_use_id":"b","type":"tool_result"},{"text":"meanwhile","type":"text"}],"role":"user"}`,
				`{"id":"b","input":{},"name":"f","type":"tool_use"}`}},
		{name: "unanswered calls and orphan results are dropped", rest: `"messages":[` + user + `,{"role":"assistant","content":"calling","tool_calls":[{"id":"a","type":"function","function":{"name":"f","arguments":"{}"}},{"id":"gone","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"a","content":"A"},{"role":"tool","tool_call_id":"orphan","content":"O"}]`,
			want: []string{`{"content":[{"text":"calling","type":"text"},{"id":"a","input":{},"name":"f","type":"tool_use"}],"role":"assistant"}`},
			not:  []string{"gone", "orphan"}},
		{name: "a result far from its call is moved next to it", rest: `"messages":[` + user + `,{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"user","content":"wait"},{"role":"assistant","content":"waiting"},{"role":"tool","tool_call_id":"a","content":"A"}]`,
			want: []string{`"messages":[{"content":[{"text":"Hi","type":"text"}],"role":"user"},{"content":[{"id":"a","input":{},"name":"f","type":"tool_use"}],"role":"assistant"},{"content":[{"content":[{"text":"A","type":"text"}],"tool_use_id":"a","type":"tool_result"},{"text":"wait","type":"text"}],"role":"user"},{"content":[{"text":"waiting","type":"text"}],"role":"assistant"}]`}},
		{name: "call IDs Anthropic refuses are mended alike", rest: `"messages":[` + user + `,{"role":"assistant","tool_calls":[{"id":"functions.f:0","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"functions.f:0","content":"A"}]`,
			want: []string{`"id":"functions_f_0"`, `"tool_use_id":"functions_f_0"`}},
		{name: "legacy function calls", rest: `"messages":[` + user + `,{"role":"assistant","function_call":{"name":"f","arguments":"{\"x\":1}"}},{"role":"function","name":"f","content":"r"}]`,
			want: []string{`{"id":"call_legacy_1","input":{"x":1},"name":"f","type":"tool_use"}`, `"tool_use_id":"call_legacy_1"`}},
		{name: "reasoning without signed blocks is tagged text", rest: `"messages":[` + user + `,{"role":"assistant","reasoning_content":"why","content":"answer"},{"role":"user","content":"more"}]`,
			want: []string{`{"content":[{"text":"<thinking>why</thinking>","type":"text"},{"text":"answer","type":"text"}],"role":"assistant"}`}},
		{name: "consecutive turns of a role are one", rest: `"messages":[` + user + `,{"role":"user","content":"again"}]`,
			want: []string{`"messages":[{"content":[{"text":"Hi","type":"text"},{"text":"again","type":"text"}],"role":"user"}]`}},
		{name: "a prefill does not end in whitespace", rest: `"messages":[` + user + `,{"role":"assistant","content":"Sure: \n"}]`,
			want: []string{`{"content":[{"text":"Sure:","type":"text"}],"role":"assistant"}`}},
		{name: "effort as a thinking budget", model: "claude-sonnet-4-6", rest: `"reasoning_effort":"high","temperature":0.3,"max_tokens":20000,"messages":[` + user + `]`,
			want: []string{`"thinking":{"budget_tokens":10240,"type":"enabled"}`}, not: []string{"temperature", "output_config"}},
		{name: "a budget below max_tokens", model: "claude-sonnet-4-6", rest: `"reasoning_effort":"xhigh","max_tokens":4000,"messages":[` + user + `]`,
			want: []string{`"thinking":{"budget_tokens":3999,"type":"enabled"}`}},
		{name: "no room to think", model: "claude-sonnet-4-6", rest: `"reasoning_effort":"low","max_tokens":1024,"messages":[` + user + `]`, not: []string{"thinking"}},
		{name: "no thinking with a forced tool", model: "claude-sonnet-4-6", rest: `"reasoning_effort":"high","tool_choice":"required","messages":[` + user + `],"tools":[{"type":"function","function":{"name":"f"}}]`, not: []string{"thinking"}},
		{name: "effort none", model: "claude-sonnet-4-6", rest: `"reasoning_effort":"none","messages":[` + user + `]`, not: []string{"thinking"}},
		{name: "5.5 thinks adaptively", model: "claude-opus-5-5", rest: `"reasoning_effort":"high","messages":[` + user + `]`,
			want: []string{`"thinking":{"type":"adaptive"}`, `"output_config":{"effort":"high"}`}},
		{name: "Sonnet 5.5 at no effort", model: "claude-sonnet-5-5", rest: `"reasoning_effort":"none","messages":[` + user + `]`,
			want: []string{`"thinking":{"type":"between_tools"}`, `"output_config":{"effort":"low"}`}},
		{name: "Haiku 5.5 at no effort", model: "claude-haiku-5-5", rest: `"reasoning_effort":"none","messages":[` + user + `]`,
			want: []string{`"thinking":{"type":"disabled"}`, `"output_config":{"effort":"low"}`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := tc.model
			if model == "" {
				model = "claude-sonnet-4-6"
			}
			req, _ := chatConvert(t, Messages, chatBody(t, model, tc.rest), "")
			got := chatJSON(t, req)
			chatWant(t, got, tc.want...)
			chatWantNot(t, got, tc.not...)
		})
	}
}

func TestChatToMessagesRefusesBadRequests(t *testing.T) {
	for _, body := range []string{`[]`, `{"model":"m","messages":[]}`, `{"model":"m","messages":[{"role":"system","content":"only"}]}`,
		`{"model":"m","messages":[{"role":"user","content":""}]}`} {
		if _, err := Convert(Chat, Messages, []byte(body), Options{}); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
}

func TestChatConversionsAreRegistered(t *testing.T) {
	if !Supported(Chat, Responses) || !Supported(Chat, Messages) {
		t.Fatal("chat conversions missing")
	}
	if got, ok := Target(Chat, []string{Messages, Responses}); !ok || got != Responses {
		t.Fatalf("target %s", got)
	}
	if got, ok := Target(Chat, []string{Messages}); !ok || got != Messages {
		t.Fatalf("target %s", got)
	}
}
