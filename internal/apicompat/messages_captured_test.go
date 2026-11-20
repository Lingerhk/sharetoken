package apicompat

import (
	"encoding/json"
	"strings"
	"testing"
)

// The requests Claude Code really sends, converted.

func msgCapturedTools(t *testing.T, body []byte) []string {
	t.Helper()
	var r struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range r.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func TestMessagesCapturedClaudeCodeToResponses(t *testing.T) {
	for _, name := range []string{"claude-code-2.1.325-turn1.json", "claude-code-2.1.325-turn2-tool-result.json"} {
		t.Run(name, func(t *testing.T) {
			body := msgCaptured(t, name)
			c, err := Convert(Messages, Responses, body, Options{Model: "claude-sonnet-5-5"})
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if err := json.Unmarshal(c.Body, &m); err != nil {
				t.Fatal(err)
			}
			raw := string(c.Body)
			for _, leak := range []string{"x-anthropic-billing-header", "cache_control", "context_management", "clear_thinking", "defer_loading", "device_id", "session_id", `"metadata"`, `"thinking"`, `"output_config"`, `"system"`} {
				if strings.Contains(raw, leak) {
					t.Errorf("converted body carries %s", leak)
				}
			}
			if m["max_output_tokens"] != 128000.0 || m["stream"] != true || m["store"] != false {
				t.Errorf("limits %v %v %v", m["max_output_tokens"], m["stream"], m["store"])
			}
			if msgJSON(t, m["reasoning"]) != `{"effort":"medium","summary":"auto"}` {
				t.Errorf("reasoning %v", m["reasoning"])
			}
			if k, _ := m["prompt_cache_key"].(string); !strings.HasPrefix(k, "cc-") {
				t.Errorf("prompt_cache_key %v", m["prompt_cache_key"])
			}
			tools := m["tools"].([]any)
			if want := msgCapturedTools(t, body); len(tools) != len(want) {
				t.Errorf("%d tools, want %d", len(tools), len(want))
			}
			for _, tool := range tools {
				tm := tool.(map[string]any)
				if tm["type"] != "function" || tm["parameters"].(map[string]any)["type"] != "object" {
					t.Errorf("tool %v", tm["name"])
				}
			}
			items := msgItems(m)
			if items[0]["role"] != "developer" {
				t.Fatalf("first item %v", items[0]["role"])
			}
			sys := msgJSON(t, items[0]["content"])
			if !strings.Contains(sys, "Claude Agent SDK") {
				t.Error("system prompt lost")
			}
			// The system message among the turns stays where it was, as a
			// developer message.
			var roles []string
			for _, it := range items {
				r, _ := it["role"].(string)
				roles = append(roles, it["type"].(string)+":"+r)
			}
			got := strings.Join(roles, " ")
			want := "message:developer message:user message:developer"
			if strings.Contains(name, "turn2") {
				want += " function_call: function_call_output:"
			}
			if got != want {
				t.Errorf("items %s, want %s", got, want)
			}
			if strings.Contains(name, "turn2") {
				fc, out := items[3], items[4]
				if fc["call_id"] != "toolu_cap1" || fc["name"] != "Read" || !strings.Contains(fc["arguments"].(string), "a.txt") {
					t.Errorf("function_call %v", fc)
				}
				if out["call_id"] != "toolu_cap1" || out["output"] != "1\thello\n2\t" {
					t.Errorf("function_call_output %v", out)
				}
				// The thinking block was signed by Anthropic: nothing an
				// OpenAI upstream can read.
				if strings.Contains(raw, "sigABC") || strings.Contains(raw, `"reasoning","summary"`) {
					t.Error("foreign thinking sent")
				}
			}
			// Same session, same key on every turn.
			c1, _ := Convert(Messages, Responses, msgCaptured(t, "claude-code-2.1.325-turn1.json"), Options{})
			var m1 map[string]any
			_ = json.Unmarshal(c1.Body, &m1)
			if m1["prompt_cache_key"] != m["prompt_cache_key"] {
				t.Error("prompt_cache_key changes between turns")
			}
		})
	}
}

func TestMessagesCapturedClaudeCodeToChat(t *testing.T) {
	for _, name := range []string{"claude-code-2.1.325-turn1.json", "claude-code-2.1.325-turn2-tool-result.json"} {
		t.Run(name, func(t *testing.T) {
			body := msgCaptured(t, name)
			c, err := Convert(Messages, Chat, body, Options{Model: "claude-sonnet-5-5"})
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if err := json.Unmarshal(c.Body, &m); err != nil {
				t.Fatal(err)
			}
			raw := string(c.Body)
			for _, leak := range []string{"x-anthropic-billing-header", "cache_control", "context_management", "defer_loading", "device_id", "prompt_cache_key"} {
				if strings.Contains(raw, leak) {
					t.Errorf("converted body carries %s", leak)
				}
			}
			if m["max_tokens"] != 128000.0 || m["reasoning_effort"] != "medium" || msgJSON(t, m["stream_options"]) != `{"include_usage":true}` {
				t.Errorf("body %v %v %v", m["max_tokens"], m["reasoning_effort"], m["stream_options"])
			}
			if len(m["tools"].([]any)) != len(msgCapturedTools(t, body)) {
				t.Error("tools lost")
			}
			var roles []string
			for _, msg := range msgChatMsgs(m) {
				roles = append(roles, msg["role"].(string))
			}
			want := "system user system"
			if strings.Contains(name, "turn2") {
				want += " assistant tool"
			}
			if got := strings.Join(roles, " "); got != want {
				t.Errorf("roles %s, want %s", got, want)
			}
			msgs := msgChatMsgs(m)
			if u, ok := msgs[1]["content"].(string); !ok || !strings.HasSuffix(u, "read a.txt and tell me what it says") {
				t.Errorf("user message %v", msgs[1]["content"])
			}
			if strings.Contains(name, "turn2") {
				a := msgs[3]
				call := a["tool_calls"].([]any)[0].(map[string]any)
				if call["id"] != "toolu_cap1" || call["function"].(map[string]any)["name"] != "Read" || a["reasoning_content"] != "Need to read the file." {
					t.Errorf("assistant %v", a)
				}
				if msgs[4]["tool_call_id"] != "toolu_cap1" || msgs[4]["content"] != "1\thello\n2\t" {
					t.Errorf("tool %v", msgs[4])
				}
			}
		})
	}
}

// A whole turn: Claude Code's request to a Responses upstream, the upstream's
// stream back, then the next request carrying the answer.
func TestMessagesCapturedRoundTrip(t *testing.T) {
	c, err := Convert(Messages, Responses, msgCaptured(t, "claude-code-2.1.325-turn1.json"), Options{Model: "claude-sonnet-5-5"})
	if err != nil {
		t.Fatal(err)
	}
	s := msgFeed(t, c.Translator,
		`{"type":"response.created","sequence_number":0,"response":{"id":"resp_9","object":"response","model":"gpt-5.5","status":"in_progress","output":[]}}`,
		`{"type":"response.in_progress","sequence_number":1,"response":{"id":"resp_9","status":"in_progress"}}`,
		`{"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}`,
		`{"type":"response.output_item.done","sequence_number":3,"output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"gAAAAABpZ-hidden"}}`,
		`{"type":"response.output_item.added","sequence_number":4,"output_index":1,"item":{"id":"fc_1","type":"function_call","status":"in_progress","arguments":"","call_id":"call_Read1","name":"Read"}}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":5,"item_id":"fc_1","output_index":1,"delta":"{\"file_path\":\"/w/a.txt\","}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":6,"item_id":"fc_1","output_index":1,"delta":"\"pages\":\"\"}"}`,
		`{"type":"response.function_call_arguments.done","sequence_number":7,"item_id":"fc_1","output_index":1,"arguments":"{\"file_path\":\"/w/a.txt\",\"pages\":\"\"}"}`,
		`{"type":"response.output_item.done","sequence_number":8,"output_index":1,"item":{"id":"fc_1","type":"function_call","status":"completed","arguments":"{\"file_path\":\"/w/a.txt\",\"pages\":\"\"}","call_id":"call_Read1","name":"Read"}}`,
		`{"type":"response.completed","sequence_number":9,"response":{"id":"resp_9","status":"completed","usage":{"input_tokens":20480,"input_tokens_details":{"cached_tokens":19456},"output_tokens":64,"output_tokens_details":{"reasoning_tokens":30},"total_tokens":20544}}}`,
	)
	s.lifecycle(t)
	agg := s.aggregate(t)
	wantContent := `[{"signature":"sharetoken-reasoning-v1:gAAAAABpZ-hidden","thinking":"","type":"thinking"},{"id":"call_Read1","input":{"file_path":"/w/a.txt"},"name":"Read","type":"tool_use"}]`
	if got := msgJSON(t, agg["content"]); got != wantContent || agg["stop_reason"] != "tool_use" {
		t.Errorf("answer %s stop %v", got, agg["stop_reason"])
	}
	if got := msgJSON(t, agg["usage"]); got != `{"cache_creation_input_tokens":0,"cache_read_input_tokens":19456,"input_tokens":1024,"output_tokens":64}` {
		t.Errorf("usage %s", got)
	}

	// Next turn, as Claude Code builds it.
	var req map[string]any
	_ = json.Unmarshal(msgCaptured(t, "claude-code-2.1.325-turn1.json"), &req)
	req["messages"] = append(req["messages"].([]any), msgAssistant(agg["content"]), msgUser([]any{
		map[string]any{"type": "tool_result", "tool_use_id": "call_Read1", "content": "1\thello"}}))
	m, _ := msgConvert(t, Responses, msgJSON(t, req), Options{})
	items := msgItems(m)
	tail := msgJSON(t, items[len(items)-3:])
	want := `[{"encrypted_content":"gAAAAABpZ-hidden","summary":[],"type":"reasoning"},` +
		`{"arguments":"{\"file_path\":\"/w/a.txt\"}","call_id":"call_Read1","name":"Read","type":"function_call"},` +
		`{"call_id":"call_Read1","output":"1\thello","type":"function_call_output"}]`
	if tail != want {
		t.Errorf("next turn\n%s\nwant\n%s", tail, want)
	}
}
