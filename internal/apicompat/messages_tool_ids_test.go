package apicompat

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSaltedCallIDsRoundTrip(t *testing.T) {
	for _, id := range []string{"call_0", "fc_0", "x", "调用_1"} {
		a, b := saltCallID(id), saltCallID(id)
		if a == b || !anthropicToolName.MatchString(a) {
			t.Fatalf("salted %q: %q %q", id, a, b)
		}
		if got, ok := unsaltCallID(a); !ok || got != id {
			t.Fatalf("unsalted %q: %q %v", a, got, ok)
		}
	}
	for _, id := range []string{"call_0", "toolu_01abc", saltedIDMark, saltedIDMark + "!!", saltedIDMark + "Y2FsbF8w"} {
		if _, ok := unsaltCallID(id); ok {
			t.Fatalf("%q read as salted", id)
		}
	}
}

// A chat upstream that numbers its calls by their place (call_0 on every
// turn) answers a Messages client with an id the client already holds: it
// goes out salted, on the stream and in the aggregated answer, and comes
// back to the upstream as the upstream's own id, on the call and its result.
func TestRepeatedUpstreamCallIDsAreSaltedForMessagesClients(t *testing.T) {
	history := []any{
		msgUser("list files"),
		msgAssistant([]any{map[string]any{"type": "tool_use", "id": "call_0", "name": "ls", "input": map[string]any{}}}),
		msgUser([]any{map[string]any{"type": "tool_result", "tool_use_id": "call_0", "content": "a.txt"}}),
	}
	tools := map[string]any{"tools": []any{map[string]any{"name": "ls", "input_schema": map[string]any{"type": "object"}}}}
	_, c := msgConvert(t, Chat, msgReq(t, "deepseek-chat", history, tools), Options{})
	s := msgFeed(t, c.Translator,
		msgToolDelta(t, 0, "call_0", "ls", `{"path":"."}`),
		msgChunk(t, map[string]any{}, "tool_calls"), msgChatUsage)
	s.lifecycle(t)
	salted := ""
	for _, e := range s.events {
		if e["type"] == "content_block_start" {
			salted, _ = e["content_block"].(map[string]any)["id"].(string)
		}
	}
	if raw, ok := unsaltCallID(salted); !ok || raw != "call_0" {
		t.Fatalf("the repeated id went out as %q", salted)
	}
	if got := s.blocks(t)[0]["id"]; got != salted {
		t.Fatalf("aggregated id %v, streamed %q", got, salted)
	}

	// The next turn sends the salted id back: the upstream gets its own.
	next := append(history,
		msgAssistant([]any{map[string]any{"type": "tool_use", "id": salted, "name": "ls", "input": map[string]any{"path": "."}}}),
		msgUser([]any{map[string]any{"type": "tool_result", "tool_use_id": salted, "content": "b.txt"}}))
	req, _ := msgConvert(t, Chat, msgReq(t, "deepseek-chat", next, tools), Options{})
	body := msgJSON(t, req)
	if strings.Contains(body, saltedIDMark) || strings.Count(body, `"call_0"`) != 4 {
		t.Fatalf("the upstream did not get its ids back: %s", body)
	}

	// A new id goes as the upstream gave it.
	_, c = msgConvert(t, Chat, msgReq(t, "deepseek-chat", next, tools), Options{})
	s = msgFeed(t, c.Translator, msgToolDelta(t, 0, "call_9", "ls", `{}`), msgChunk(t, map[string]any{}, "tool_calls"), msgChatUsage)
	if got := s.blocks(t)[0]["id"]; got != "call_9" {
		t.Fatalf("a new id changed: %v", got)
	}
}

// The same for a Responses upstream (fc_0 numbering).
func TestRepeatedResponsesCallIDsAreSalted(t *testing.T) {
	history := []any{
		msgUser("go"),
		msgAssistant([]any{map[string]any{"type": "tool_use", "id": "fc_0", "name": "ls", "input": map[string]any{}}}),
		msgUser([]any{map[string]any{"type": "tool_result", "tool_use_id": "fc_0", "content": "ok"}}),
	}
	req, c := msgConvert(t, Responses, msgReq(t, "gpt-5", history, nil), Options{})
	if !strings.Contains(msgJSON(t, req), `"call_id":"fc_0"`) {
		t.Fatalf("history: %s", msgJSON(t, req))
	}
	s := msgFeed(t, c.Translator,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"x0","call_id":"fc_0","name":"ls","arguments":""}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"x0","call_id":"fc_0","name":"ls","arguments":"{}"}}`,
		`{"type":"response.completed","response":{"status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`)
	id, _ := s.blocks(t)[0]["id"].(string)
	if raw, ok := unsaltCallID(id); !ok || raw != "fc_0" {
		t.Fatalf("Responses call id went out as %q", id)
	}
}

// A conversion to Messages of a history with tool calls and no tools
// declares a stub per tool called, and lets the model call none.
func TestToolHistoryWithoutToolsGetsStubs(t *testing.T) {
	chat := chatBody(t, "claude-sonnet-5", `"messages":[{"role":"user","content":"hi"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{}"}},{"id":"c2","type":"function","function":{"name":"read_file","arguments":"{\"a\":1}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"x"},{"role":"tool","tool_call_id":"c2","content":"y"},{"role":"user","content":"thanks"}],"tools":[]`)
	req, _ := chatConvert(t, Messages, chat, "")
	got := chatJSON(t, req)
	chatWant(t, got, `"tool_choice":{"type":"none"}`, `"name":"read_file"`, stubToolDescription)
	if n := len(req["tools"].([]any)); n != 1 {
		t.Fatalf("%d stubs: %s", n, got)
	}

	resp := `{"model":"claude-sonnet-5","stream":true,"input":[{"type":"message","role":"user","content":"hi"},
		{"type":"function_call","call_id":"c1","name":"shell","arguments":"{}"},
		{"type":"function_call_output","call_id":"c1","output":"ok"},{"type":"message","role":"user","content":"next"}]}`
	c, err := Convert(Responses, Messages, []byte(resp), Options{})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.Unmarshal(c.Body, &out)
	got = chatJSON(t, out)
	chatWant(t, got, `"tool_choice":{"type":"none"}`, `"name":"shell"`)

	// Declared tools stay as the client gave them.
	declared := chatBody(t, "claude-sonnet-5", `"messages":[{"role":"user","content":"hi"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"old","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"x"}],"tools":[{"type":"function","function":{"name":"now","parameters":{"type":"object"}}}]`)
	req, _ = chatConvert(t, Messages, declared, "")
	chatWantNot(t, chatJSON(t, req), stubToolDescription, `"type":"none"`)
}
