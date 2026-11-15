package apicompat

import (
	"reflect"
	"strings"
	"testing"
)

func TestMessagesToChatRequestBasics(t *testing.T) {
	for _, tc := range []struct {
		model    string
		limitKey string
		sampling bool
	}{
		{"deepseek-chat", "max_tokens", true},
		{"glm-5.2", "max_tokens", true},
		{"gpt-4o", "max_tokens", true},
		{"gpt-5.5", "max_completion_tokens", false},
		{"o3-mini", "max_completion_tokens", false},
	} {
		t.Run(tc.model, func(t *testing.T) {
			body := msgReq(t, tc.model, []any{msgUser("hello")}, map[string]any{
				"system": "Be brief.", "temperature": 0.5, "top_p": 0.9, "top_k": 3,
				"stop_sequences": []string{"A", "B", "C", "D", "E"},
			})
			m, _ := msgConvert(t, Chat, body, Options{})
			if m["model"] != tc.model || m["stream"] != true || msgJSON(t, m["stream_options"]) != `{"include_usage":true}` {
				t.Errorf("body %v", m)
			}
			if m[tc.limitKey] != 4096.0 {
				t.Errorf("%s = %v", tc.limitKey, m[tc.limitKey])
			}
			if _, ok := m["temperature"]; ok != tc.sampling {
				t.Errorf("temperature present %v", ok)
			}
			if msgJSON(t, m["stop"]) != `["A","B","C","D"]` {
				t.Errorf("stop %v", m["stop"])
			}
			want := `[{"content":"Be brief.","role":"system"},{"content":"hello","role":"user"}]`
			if got := msgJSON(t, m["messages"]); got != want {
				t.Errorf("messages %s", got)
			}
			for _, k := range []string{"top_k", "system", "metadata", "thinking", "store", "include"} {
				if _, ok := m[k]; ok {
					t.Errorf("unexpected %s", k)
				}
			}
		})
	}
	m, _ := msgConvert(t, Chat, msgReq(t, "gpt-4o", []any{msgUser("x")}, nil), Options{})
	if _, ok := m["reasoning_effort"]; ok {
		t.Error("reasoning_effort for a model without reasoning")
	}
	m, _ = msgConvert(t, Chat, msgReq(t, "m", []any{msgUser("x")}, map[string]any{"max_tokens": 1}), Options{})
	if m["max_tokens"] != 128.0 {
		t.Errorf("floor %v", m["max_tokens"])
	}
}

func TestMessagesToChatMessages(t *testing.T) {
	body := msgReq(t, "deepseek-chat", []any{
		msgUser([]any{map[string]any{"type": "text", "text": "a"}, map[string]any{"type": "text", "text": ""}, map[string]any{"type": "text", "text": "b"}}),
		msgUser("c"),
		map[string]any{"role": "system", "content": "env"},
		msgAssistant([]any{
			map[string]any{"type": "thinking", "thinking": "need ls", "signature": "x"},
			map[string]any{"type": "redacted_thinking", "data": "zz"},
			map[string]any{"type": "text", "text": "Listing."},
			map[string]any{"type": "tool_use", "id": "toolu_1", "name": "Bash", "input": map[string]any{"command": "ls"}},
		}),
		map[string]any{"role": "system", "content": "a reminder between the call and its result"},
		msgUser([]any{
			map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": []any{
				map[string]any{"type": "text", "text": "a.go"},
				map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": "PNG"}}}},
			map[string]any{"type": "text", "text": "and?"},
		}),
		msgAssistant([]any{map[string]any{"type": "thinking", "thinking": "plain answer, no tools"}, map[string]any{"type": "text", "text": "Done."}}),
		msgAssistant("More."),
		msgUser([]any{
			map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": "https://x/i.png"}},
			map[string]any{"type": "document", "title": "a.pdf", "source": map[string]any{"type": "base64", "media_type": "application/pdf", "data": "JVBE"}},
		}),
	}, nil)
	m, _ := msgConvert(t, Chat, body, Options{})
	want := []any{
		map[string]any{"role": "user", "content": "a\n\nb\n\nc"},
		map[string]any{"role": "system", "content": "env"},
		map[string]any{"role": "assistant", "content": "Listing.", "reasoning_content": "need ls", "tool_calls": []any{
			map[string]any{"id": "toolu_1", "type": "function", "function": map[string]any{"name": "Bash", "arguments": `{"command":"ls"}`}}}},
		map[string]any{"role": "tool", "tool_call_id": "toolu_1", "content": "a.go"},
		map[string]any{"role": "system", "content": "a reminder between the call and its result"},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "and?"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,PNG"}}}},
		map[string]any{"role": "assistant", "content": "Done.\n\nMore."},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://x/i.png"}},
			map[string]any{"type": "file", "file": map[string]any{"filename": "a.pdf", "file_data": "data:application/pdf;base64,JVBE"}}}},
	}
	if got := msgJSON(t, m["messages"]); got != msgJSON(t, want) {
		t.Errorf("messages\n%s\nwant\n%s", got, msgJSON(t, want))
	}
}

func TestMessagesToChatTools(t *testing.T) {
	body := msgReq(t, "deepseek-chat", []any{msgUser("x")}, map[string]any{
		"tools": []any{
			map[string]any{"name": "Bash", "description": "run", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}}},
			map[string]any{"name": "Empty"},
			map[string]any{"type": "web_search_20250305", "name": "web_search"},
		},
	})
	m, _ := msgConvert(t, Chat, body, Options{})
	want := `[{"function":{"description":"run","name":"Bash","parameters":{"properties":{"command":{"type":"string"}},"type":"object"},"strict":false},"type":"function"},` +
		`{"function":{"description":"","name":"Empty","parameters":{"properties":{},"type":"object"},"strict":false},"type":"function"}]`
	if got := msgJSON(t, m["tools"]); got != want {
		t.Errorf("tools %s", got)
	}
	// Only server tools: no tools at all.
	m, _ = msgConvert(t, Chat, msgReq(t, "deepseek-chat", []any{msgUser("x")}, map[string]any{
		"tools": []any{map[string]any{"type": "web_search_20250305", "name": "web_search"}}, "tool_choice": map[string]any{"type": "auto"}}), Options{})
	for _, k := range []string{"tools", "tool_choice", "parallel_tool_calls"} {
		if _, ok := m[k]; ok {
			t.Errorf("unexpected %s", k)
		}
	}
}

func msgCTranslator(t *testing.T) Translator {
	t.Helper()
	_, c := msgConvert(t, Chat, msgReq(t, "deepseek-chat", []any{msgUser("x")}, nil), Options{Model: "claude-sonnet-5-5"})
	return c.Translator
}

// msgChunk is a chat chunk with one choice's delta (and a finish reason).
func msgChunk(t *testing.T, delta map[string]any, finish string) string {
	t.Helper()
	ch := map[string]any{"index": 0, "delta": delta}
	if finish != "" {
		ch["finish_reason"] = finish
	}
	return msgJSON(t, map[string]any{"id": "chatcmpl-1", "object": "chat.completion.chunk", "model": "deepseek-chat", "choices": []any{ch}})
}

func msgToolDelta(t *testing.T, index any, id, name, args string) string {
	tc := map[string]any{"function": map[string]any{}}
	if index != nil {
		tc["index"] = index
	}
	if id != "" {
		tc["id"] = id
		tc["type"] = "function"
	}
	if name != "" {
		tc["function"].(map[string]any)["name"] = name
	}
	if args != "" {
		tc["function"].(map[string]any)["arguments"] = args
	}
	return msgChunk(t, map[string]any{"tool_calls": []any{tc}}, "")
}

const msgChatUsage = `{"id":"chatcmpl-1","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"prompt_tokens_details":{"cached_tokens":64}}}`

func TestMessagesFromChatText(t *testing.T) {
	s := msgFeed(t, msgCTranslator(t),
		msgChunk(t, map[string]any{"role": "assistant", "content": ""}, ""),
		msgChunk(t, map[string]any{"content": "Hello"}, ""),
		msgChunk(t, map[string]any{"content": " world"}, ""),
		msgChunk(t, map[string]any{}, "stop"),
		msgChatUsage,
		"[DONE]",
	)
	s.lifecycle(t)
	if s.text() != "Hello world" || s.stopReason() != "end_turn" {
		t.Errorf("text %q stop %q", s.text(), s.stopReason())
	}
	if !reflect.DeepEqual(s.usage(), map[string]int{"input_tokens": 36, "cache_read_input_tokens": 64, "cache_creation_input_tokens": 0, "output_tokens": 20}) {
		t.Errorf("usage %v", s.usage())
	}
	if m := s.events[0]["message"].(map[string]any)["model"]; m != "claude-sonnet-5-5" {
		t.Errorf("model %v", m)
	}
	if n := len(s.types()); s.types()[n-1] != "message_stop" || strings.Count(strings.Join(s.types(), ","), "message_start") != 1 {
		t.Errorf("events %v", s.types())
	}
}

func TestMessagesFromChatUsage(t *testing.T) {
	for _, tc := range []struct {
		name  string
		usage string
		want  map[string]int
	}{
		{"cache write", `{"prompt_tokens":100,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":10,"cache_creation_tokens":40}}`,
			map[string]int{"input_tokens": 50, "cache_read_input_tokens": 10, "cache_creation_input_tokens": 40, "output_tokens": 5}},
		{"cache write spelling", `{"prompt_tokens":100,"completion_tokens":5,"prompt_tokens_details":{"cache_write_tokens":40}}`,
			map[string]int{"input_tokens": 60, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 40, "output_tokens": 5}},
		{"none", `{"prompt_tokens":7,"completion_tokens":5}`,
			map[string]int{"input_tokens": 7, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0, "output_tokens": 5}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Usage on the finishing chunk itself ends the message there.
			fin := msgJSON(t, map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}}, "usage": msgParse(t, tc.usage)})
			s := msgFeed(t, msgCTranslator(t), msgChunk(t, map[string]any{"content": "x"}, ""), fin)
			s.lifecycle(t)
			if !reflect.DeepEqual(s.usage(), tc.want) {
				t.Errorf("usage %v", s.usage())
			}
		})
	}
}

func TestMessagesFromChatReasoning(t *testing.T) {
	for _, field := range []string{"reasoning_content", "reasoning"} {
		t.Run(field, func(t *testing.T) {
			s := msgFeed(t, msgCTranslator(t),
				msgChunk(t, map[string]any{field: "Let me"}, ""),
				msgChunk(t, map[string]any{field: " think."}, ""),
				msgChunk(t, map[string]any{"content": "Answer."}, ""),
				msgChunk(t, map[string]any{}, "stop"),
			)
			s.lifecycle(t)
			if got := msgJSON(t, s.blocks(t)); got != `[{"signature":"","thinking":"Let me think.","type":"thinking"},{"text":"Answer.","type":"text"}]` {
				t.Errorf("blocks %s", got)
			}
		})
	}
	// reasoning_content wins over reasoning.
	s := msgFeed(t, msgCTranslator(t), msgChunk(t, map[string]any{"reasoning_content": "A", "reasoning": "B"}, "stop"))
	if s.thinking() != "A" {
		t.Errorf("thinking %q", s.thinking())
	}
	// Reasoning goes back on the tool turn it led to.
	s = msgFeed(t, msgCTranslator(t),
		msgChunk(t, map[string]any{"reasoning_content": "need a tool"}, ""),
		msgToolDelta(t, 0, "call_1", "Bash", `{"command":"ls"}`),
		msgChunk(t, map[string]any{}, "tool_calls"))
	body := msgReq(t, "deepseek-reasoner", []any{msgUser("x"), msgAssistant(s.aggregate(t)["content"]),
		msgUser([]any{map[string]any{"type": "tool_result", "tool_use_id": "call_1", "content": "ok"}})}, nil)
	m, _ := msgConvert(t, Chat, body, Options{})
	if a := msgChatMsgs(m)[1]; a["reasoning_content"] != "need a tool" || a["tool_calls"] == nil {
		t.Errorf("assistant %v", a)
	}
}

func TestMessagesFromChatToolCalls(t *testing.T) {
	for _, tc := range []struct {
		name   string
		chunks []string
		want   string
		stop   string
	}{
		{"streamed arguments", []string{
			msgChunk(t, map[string]any{"content": "Checking."}, ""),
			msgToolDelta(t, 0, "call_1", "get_weather", ""),
			msgToolDelta(t, 0, "", "", `{"city":`),
			msgToolDelta(t, 0, "", "", `"Tokyo"}`),
			msgChunk(t, map[string]any{}, "tool_calls"),
		}, `[{"text":"Checking.","type":"text"},{"id":"call_1","input":{"city":"Tokyo"},"name":"get_weather","type":"tool_use"}]`, "tool_use"},
		{"parallel", []string{
			msgToolDelta(t, 0, "call_1", "get_weather", `{"city":"Tokyo"}`),
			msgToolDelta(t, 1, "call_2", "get_time", `{}`),
			msgChunk(t, map[string]any{}, "tool_calls"),
		}, `[{"id":"call_1","input":{"city":"Tokyo"},"name":"get_weather","type":"tool_use"},{"id":"call_2","input":{},"name":"get_time","type":"tool_use"}]`, "tool_use"},
		{"interleaved", []string{
			msgToolDelta(t, 0, "call_1", "A", `{"x":`),
			msgToolDelta(t, 1, "call_2", "B", `{"y":`),
			msgToolDelta(t, 0, "", "", `1}`),
			msgToolDelta(t, 1, "", "", `2}`),
			msgChunk(t, map[string]any{}, "tool_calls"),
		}, `[{"id":"call_1","input":{"x":1},"name":"A","type":"tool_use"},{"id":"call_2","input":{"y":2},"name":"B","type":"tool_use"}]`, "tool_use"},
		{"name arrives late", []string{
			msgToolDelta(t, 0, "call_1", "", ""),
			msgToolDelta(t, 0, "", "", `{"q":`),
			msgToolDelta(t, 0, "", "lookup", ""),
			msgToolDelta(t, 0, "", "", `1}`),
			msgChunk(t, map[string]any{}, "tool_calls"),
		}, `[{"id":"call_1","input":{"q":1},"name":"lookup","type":"tool_use"}]`, "tool_use"},
		{"name never arrives", []string{
			msgToolDelta(t, 0, "call_1", "", `{"q":1}`),
			msgChunk(t, map[string]any{}, "tool_calls"),
		}, `[{"id":"call_1","input":{"q":1},"name":"","type":"tool_use"}]`, "tool_use"},
		{"empty arguments", []string{
			msgToolDelta(t, 0, "call_1", "TodoRead", ""),
			msgChunk(t, map[string]any{}, "tool_calls"),
		}, `[{"id":"call_1","input":{},"name":"TodoRead","type":"tool_use"}]`, "tool_use"},
		{"no index, by id", []string{
			msgToolDelta(t, nil, "call_1", "A", `{"a":1}`),
			msgToolDelta(t, nil, "call_2", "B", `{"b":2}`),
			msgChunk(t, map[string]any{}, "stop"),
		}, `[{"id":"call_1","input":{"a":1},"name":"A","type":"tool_use"},{"id":"call_2","input":{"b":2},"name":"B","type":"tool_use"}]`, "tool_use"},
		{"missing id", []string{
			msgToolDelta(t, 0, "", "A", `{}`),
			msgChunk(t, map[string]any{}, "tool_calls"),
		}, ``, "tool_use"},
		{"Read sanitized", []string{
			msgToolDelta(t, 0, "call_r", "Read", `{"file_path":"/a",`),
			msgToolDelta(t, 0, "", "", `"pages":""}`),
			msgChunk(t, map[string]any{}, "tool_calls"),
		}, `[{"id":"call_r","input":{"file_path":"/a"},"name":"Read","type":"tool_use"}]`, "tool_use"},
		{"length", []string{
			msgChunk(t, map[string]any{"content": "cut"}, "length"),
		}, `[{"text":"cut","type":"text"}]`, "max_tokens"},
		{"content filter", []string{
			msgChunk(t, map[string]any{"content": "no"}, "content_filter"),
		}, `[{"text":"no","type":"text"}]`, "refusal"},
		{"refusal delta", []string{
			msgChunk(t, map[string]any{"refusal": "I can't help."}, "stop"),
		}, `[{"text":"I can't help.","type":"text"}]`, "refusal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := msgFeed(t, msgCTranslator(t), append(tc.chunks, msgChatUsage, "[DONE]")...)
			s.lifecycle(t)
			if s.stopReason() != tc.stop {
				t.Errorf("stop %q", s.stopReason())
			}
			if tc.want == "" {
				b := s.blocks(t)
				if len(b) != 1 || !strings.HasPrefix(b[0]["id"].(string), "call_") {
					t.Errorf("blocks %v", b)
				}
				return
			}
			if got := msgJSON(t, s.blocks(t)); got != tc.want {
				t.Errorf("blocks\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

func TestMessagesFromChatEnd(t *testing.T) {
	// Without [DONE] or a usage chunk the stream ends at Close.
	s := msgFeed(t, msgCTranslator(t), msgChunk(t, map[string]any{"content": "x"}, "stop"))
	s.lifecycle(t)
	// Cut short: no finish reason.
	s = msgFeed(t, msgCTranslator(t), msgToolDelta(t, 0, "call_1", "Bash", `{"command":"ls"}`))
	s.lifecycle(t)
	if s.stopReason() != "tool_use" {
		t.Errorf("stop %q", s.stopReason())
	}
	// Nothing after the message ended.
	s = msgFeed(t, msgCTranslator(t), msgChunk(t, map[string]any{"content": "x"}, "stop"), msgChatUsage, msgChunk(t, map[string]any{"content": "late"}, ""))
	s.lifecycle(t)
	if s.text() != "x" {
		t.Errorf("text %q", s.text())
	}
	// Other choices are not ours.
	s = msgFeed(t, msgCTranslator(t), `{"choices":[{"index":1,"delta":{"content":"other"}},{"index":0,"delta":{"content":"mine"},"finish_reason":"stop"}]}`)
	if s.text() != "mine" {
		t.Errorf("text %q", s.text())
	}
}

func TestMessagesFromChatErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		chunks []string
		msg    string
	}{
		{"error chunk", []string{`{"error":{"message":"overloaded","type":"server_error"}}`}, "overloaded"},
		{"error after text", []string{msgChunk(t, map[string]any{"content": "x"}, ""), `{"error":{"message":"cut"}}`}, "cut"},
		{"nothing", nil, "without a response"},
		{"done only", []string{"[DONE]"}, "without a response"},
		{"role only", []string{msgChunk(t, map[string]any{"role": "assistant"}, "")}, "without a response"},
		{"not JSON", []string{"{"}, "chat chunk"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := msgFeed(t, msgCTranslator(t), tc.chunks...)
			if s.err == nil || !strings.Contains(s.err.Error(), tc.msg) {
				t.Errorf("error %v", s.err)
			}
			if tc.name != "error after text" && len(s.events) != 0 {
				t.Errorf("events before the error: %v", s.types())
			}
		})
	}
}
