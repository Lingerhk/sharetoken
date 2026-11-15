package apicompat

import (
	"reflect"
	"strings"
	"testing"
)

// Responses event builders.
const msgRCreated = `{"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5","status":"in_progress"}}`

func msgRCompleted(t *testing.T, status string, usage any, output any) string {
	t.Helper()
	r := map[string]any{"id": "resp_1", "status": status}
	if usage != nil {
		r["usage"] = usage
	}
	if output != nil {
		r["output"] = output
	}
	typ := "response.completed"
	if status == "incomplete" {
		typ = "response.incomplete"
	}
	return msgJSON(t, map[string]any{"type": typ, "response": r})
}

func msgRAdded(t *testing.T, i int, item map[string]any) string {
	return msgJSON(t, map[string]any{"type": "response.output_item.added", "output_index": i, "item": item})
}

func msgRDone(t *testing.T, i int, item map[string]any) string {
	return msgJSON(t, map[string]any{"type": "response.output_item.done", "output_index": i, "item": item})
}

func msgREv(t *testing.T, typ string, fields map[string]any) string {
	m := map[string]any{"type": typ}
	for k, v := range fields {
		m[k] = v
	}
	return msgJSON(t, m)
}

func msgRTranslator(t *testing.T) Translator {
	t.Helper()
	_, c := msgConvert(t, Responses, msgReq(t, "gpt-5.5", []any{msgUser("x")}, nil), Options{Model: "claude-sonnet-5-5"})
	return c.Translator
}

func TestMessagesFromResponsesText(t *testing.T) {
	s := msgFeed(t, msgRTranslator(t),
		msgRCreated,
		`{"type":"response.in_progress","response":{"id":"resp_1"}}`,
		msgRAdded(t, 0, map[string]any{"type": "message", "id": "msg_x", "role": "assistant", "status": "in_progress", "content": []any{}}),
		`{"type":"response.content_part.added","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`,
		`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"Hello"}`,
		`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":" world"}`,
		`{"type":"response.output_text.done","output_index":0,"content_index":0,"text":"Hello world"}`,
		`{"type":"response.content_part.done","output_index":0,"content_index":0,"part":{"type":"output_text","text":"Hello world"}}`,
		msgRDone(t, 0, map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "Hello world"}}}),
		`{"type":"response.some_future_event","foo":1}`,
		msgRCompleted(t, "completed", map[string]any{"input_tokens": 1200, "output_tokens": 30, "input_tokens_details": map[string]any{"cached_tokens": 1000}}, nil),
	)
	s.lifecycle(t)
	want := []string{"message_start", "content_block_start", "content_block_delta", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	if !reflect.DeepEqual(s.types(), want) {
		t.Errorf("events %v", s.types())
	}
	if s.text() != "Hello world" || s.stopReason() != "end_turn" {
		t.Errorf("text %q stop %q", s.text(), s.stopReason())
	}
	if u := s.usage(); !reflect.DeepEqual(u, map[string]int{"input_tokens": 200, "cache_read_input_tokens": 1000, "cache_creation_input_tokens": 0, "output_tokens": 30}) {
		t.Errorf("usage %v", u)
	}
	start := s.events[0]["message"].(map[string]any)
	if start["model"] != "claude-sonnet-5-5" || !strings.HasPrefix(start["id"].(string), "msg_") || start["stop_reason"] != nil {
		t.Errorf("message_start %v", start)
	}
	for _, k := range []string{"input_tokens", "cache_read_input_tokens", "cache_creation_input_tokens", "output_tokens"} {
		if _, ok := start["usage"].(map[string]any)[k]; !ok {
			t.Errorf("message_start usage lacks %s", k)
		}
	}
	agg := s.aggregate(t)
	if agg["stop_reason"] != "end_turn" || msgJSON(t, agg["content"]) != `[{"text":"Hello world","type":"text"}]` {
		t.Errorf("aggregate %v", agg)
	}
}

func TestMessagesFromResponsesUsage(t *testing.T) {
	for _, tc := range []struct {
		name  string
		usage any
		top   bool // as the event's own usage
		want  map[string]int
	}{
		{"plain", map[string]any{"input_tokens": 10, "output_tokens": 5}, false,
			map[string]int{"input_tokens": 10, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0, "output_tokens": 5}},
		{"cached", map[string]any{"input_tokens": 100, "output_tokens": 5, "input_tokens_details": map[string]any{"cached_tokens": 60}}, false,
			map[string]int{"input_tokens": 40, "cache_read_input_tokens": 60, "cache_creation_input_tokens": 0, "output_tokens": 5}},
		{"cache write nested", map[string]any{"input_tokens": 100, "output_tokens": 5, "input_tokens_details": map[string]any{"cached_tokens": 20, "cache_creation_tokens": 30}}, false,
			map[string]int{"input_tokens": 50, "cache_read_input_tokens": 20, "cache_creation_input_tokens": 30, "output_tokens": 5}},
		{"cache write tokens nested", map[string]any{"input_tokens": 100, "output_tokens": 5, "input_tokens_details": map[string]any{"cache_write_tokens": 30}}, false,
			map[string]int{"input_tokens": 70, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 30, "output_tokens": 5}},
		{"cache creation top level", map[string]any{"input_tokens": 100, "output_tokens": 5, "cache_creation_input_tokens": 25}, false,
			map[string]int{"input_tokens": 75, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 25, "output_tokens": 5}},
		{"cached beyond input clamps", map[string]any{"input_tokens": 10, "output_tokens": 1, "input_tokens_details": map[string]any{"cached_tokens": 50}}, false,
			map[string]int{"input_tokens": 0, "cache_read_input_tokens": 50, "cache_creation_input_tokens": 0, "output_tokens": 1}},
		{"chat spelling", map[string]any{"prompt_tokens": 100, "completion_tokens": 7, "prompt_tokens_details": map[string]any{"cached_tokens": 40}}, false,
			map[string]int{"input_tokens": 60, "cache_read_input_tokens": 40, "cache_creation_input_tokens": 0, "output_tokens": 7}},
		{"top level usage", map[string]any{"input_tokens": 9, "output_tokens": 3}, true,
			map[string]int{"input_tokens": 9, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0, "output_tokens": 3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			done := msgRCompleted(t, "completed", tc.usage, nil)
			if tc.top {
				done = msgJSON(t, map[string]any{"type": "response.done", "response": map[string]any{"status": "completed"}, "usage": tc.usage})
			}
			s := msgFeed(t, msgRTranslator(t), msgRCreated,
				`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"x"}`, done)
			s.lifecycle(t)
			if !reflect.DeepEqual(s.usage(), tc.want) {
				t.Errorf("usage %v, want %v", s.usage(), tc.want)
			}
		})
	}
}

func TestMessagesFromResponsesReasoning(t *testing.T) {
	s := msgFeed(t, msgRTranslator(t),
		msgRCreated,
		msgRAdded(t, 0, map[string]any{"type": "reasoning", "id": "rs_1", "summary": []any{}}),
		`{"type":"response.reasoning_summary_part.added","output_index":0,"summary_index":0,"part":{"type":"summary_text","text":""}}`,
		`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"Plan"}`,
		`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":" first."}`,
		`{"type":"response.reasoning_summary_text.done","output_index":0,"summary_index":0,"text":"Plan first."}`,
		`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":1,"delta":"Then act."}`,
		`{"type":"response.reasoning_summary_text.done","output_index":0,"summary_index":1,"text":"Then act."}`,
		msgRDone(t, 0, map[string]any{"type": "reasoning", "id": "rs_1", "encrypted_content": "gAAAAB-enc", "summary": []any{
			map[string]any{"type": "summary_text", "text": "Plan first."}, map[string]any{"type": "summary_text", "text": "Then act."}}}),
		`{"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"Done."}`,
		msgRCompleted(t, "completed", map[string]any{"input_tokens": 5, "output_tokens": 50, "output_tokens_details": map[string]any{"reasoning_tokens": 40}}, nil),
	)
	s.lifecycle(t)
	blocks := s.blocks(t)
	if len(blocks) != 2 {
		t.Fatalf("blocks %v", blocks)
	}
	if blocks[0]["type"] != "thinking" || blocks[0]["thinking"] != "Plan first.\n\nThen act." || blocks[0]["signature"] != msgSignaturePrefix+"gAAAAB-enc" {
		t.Errorf("thinking block %v", blocks[0])
	}
	if !IsConvertedThinkingSignature(blocks[0]["signature"].(string)) || IsConvertedThinkingSignature("EqQBCkgI") {
		t.Error("IsConvertedThinkingSignature")
	}
	if blocks[1]["text"] != "Done." {
		t.Errorf("text block %v", blocks[1])
	}
	// The signature is sent right before the thinking block stops.
	types := s.types()
	for i, e := range s.events {
		if e["type"] == "content_block_delta" && e["delta"].(map[string]any)["type"] == "signature_delta" && types[i+1] != "content_block_stop" {
			t.Error("signature_delta not right before the stop")
		}
	}

	// The next turn gives the reasoning back as it was.
	agg := s.aggregate(t)
	body := msgReq(t, "gpt-5.5", []any{msgUser("x"), msgAssistant(agg["content"]), msgUser("more")}, nil)
	m, _ := msgConvert(t, Responses, body, Options{})
	items := msgItems(m)
	want := `{"encrypted_content":"gAAAAB-enc","summary":[{"text":"Plan first.\n\nThen act.","type":"summary_text"}],"type":"reasoning"}`
	if msgJSON(t, items[1]) != want {
		t.Errorf("reasoning back: %s", msgJSON(t, items[1]))
	}
}

func TestMessagesFromResponsesHiddenReasoning(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events []string
	}{
		{"encrypted at done", []string{
			msgRAdded(t, 0, map[string]any{"type": "reasoning", "id": "rs_1", "summary": []any{}}),
			msgRDone(t, 0, map[string]any{"type": "reasoning", "id": "rs_1", "summary": []any{}, "encrypted_content": "ENC"}),
		}},
		{"encrypted at added", []string{
			msgRAdded(t, 0, map[string]any{"type": "reasoning", "id": "rs_1", "encrypted_content": "ENC"}),
		}},
		{"done only", []string{
			msgRDone(t, 0, map[string]any{"type": "reasoning", "id": "rs_1", "encrypted_content": "ENC"}),
		}},
		{"terminal only", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := append([]string{msgRCreated}, tc.events...)
			var output any
			if tc.events == nil {
				output = []any{map[string]any{"type": "reasoning", "encrypted_content": "ENC", "summary": []any{}}, map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "Hi"}}}}
			} else {
				events = append(events, `{"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"Hi"}`)
			}
			events = append(events, msgRCompleted(t, "completed", nil, output))
			s := msgFeed(t, msgRTranslator(t), events...)
			s.lifecycle(t)
			blocks := s.blocks(t)
			if len(blocks) != 2 || blocks[0]["type"] != "thinking" || blocks[0]["thinking"] != "" || blocks[0]["signature"] != msgSignaturePrefix+"ENC" || blocks[1]["text"] != "Hi" {
				t.Errorf("blocks %v", blocks)
			}
		})
	}
	// A reasoning item with nothing at all makes no block.
	s := msgFeed(t, msgRTranslator(t), msgRCreated,
		msgRAdded(t, 0, map[string]any{"type": "reasoning", "id": "rs_1"}),
		msgRDone(t, 0, map[string]any{"type": "reasoning", "id": "rs_1"}),
		`{"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"Hi"}`,
		msgRCompleted(t, "completed", nil, nil))
	s.lifecycle(t)
	if b := s.blocks(t); len(b) != 1 || b[0]["type"] != "text" {
		t.Errorf("empty reasoning made %v", b)
	}
}

func TestMessagesFromResponsesToolCalls(t *testing.T) {
	s := msgFeed(t, msgRTranslator(t),
		msgRCreated,
		`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"Let me look."}`,
		`{"type":"response.output_text.done","output_index":0,"content_index":0,"text":"Let me look."}`,
		msgRAdded(t, 1, map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_a", "name": "Bash", "arguments": ""}),
		`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"command\":"}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"\"ls\"}"}`,
		`{"type":"response.function_call_arguments.done","output_index":1,"arguments":"{\"command\":\"ls\"}"}`,
		msgRDone(t, 1, map[string]any{"type": "function_call", "id": "fc_1", "call_id": "call_a", "name": "Bash", "arguments": `{"command":"ls"}`}),
		msgRAdded(t, 2, map[string]any{"type": "function_call", "id": "fc_2", "call_id": "call_b", "name": "Glob"}),
		`{"type":"response.function_call_arguments.done","output_index":2,"arguments":"{\"pattern\":\"*.go\"}"}`,
		msgRDone(t, 2, map[string]any{"type": "function_call", "id": "fc_2", "call_id": "call_b", "name": "Glob", "arguments": `{"pattern":"*.go"}`}),
		msgRAdded(t, 3, map[string]any{"type": "function_call", "id": "fc_3", "call_id": "call_c", "name": "TodoRead"}),
		msgRDone(t, 3, map[string]any{"type": "function_call", "id": "fc_3", "call_id": "call_c", "name": "TodoRead", "arguments": ""}),
		msgRCompleted(t, "completed", map[string]any{"input_tokens": 10, "output_tokens": 10}, nil),
	)
	s.lifecycle(t)
	if s.stopReason() != "tool_use" {
		t.Errorf("stop %q", s.stopReason())
	}
	got := msgJSON(t, s.blocks(t))
	want := `[{"text":"Let me look.","type":"text"},` +
		`{"id":"call_a","input":{"command":"ls"},"name":"Bash","type":"tool_use"},` +
		`{"id":"call_b","input":{"pattern":"*.go"},"name":"Glob","type":"tool_use"},` +
		`{"id":"call_c","input":{},"name":"TodoRead","type":"tool_use"}]`
	if got != want {
		t.Errorf("blocks\n%s\nwant\n%s", got, want)
	}
	// Arguments streamed by deltas are not sent again by the done events.
	if n := strings.Count(s.deltas("input_json_delta", "partial_json"), `"ls"`); n != 1 {
		t.Errorf("arguments sent %d times", n)
	}

	// The next turn maps the ids back.
	body := msgReq(t, "gpt-5.5", []any{msgUser("x"), msgAssistant(s.aggregate(t)["content"]), msgUser([]any{
		map[string]any{"type": "tool_result", "tool_use_id": "call_a", "content": "a.go"},
		map[string]any{"type": "tool_result", "tool_use_id": "call_b", "content": "b.go"},
		map[string]any{"type": "tool_result", "tool_use_id": "call_c", "content": "[]"},
	})}, nil)
	m, _ := msgConvert(t, Responses, body, Options{})
	var ids []string
	for _, it := range msgItems(m) {
		if id, ok := it["call_id"].(string); ok {
			ids = append(ids, it["type"].(string)+":"+id)
		}
	}
	wantIDs := []string{"function_call:call_a", "function_call:call_b", "function_call:call_c", "function_call_output:call_a", "function_call_output:call_b", "function_call_output:call_c"}
	if !reflect.DeepEqual(ids, wantIDs) {
		t.Errorf("ids %v", ids)
	}
}

func TestMessagesFromResponsesPackedParallelCalls(t *testing.T) {
	events := []string{msgRCreated}
	for i, name := range []string{"a", "b", "c"} {
		events = append(events, msgRAdded(t, i, map[string]any{"type": "function_call", "call_id": "call_" + name, "name": "tool_" + name}))
	}
	for i, args := range []string{`{"a":1}`, `{"b":2}`, `{"c":3}`} {
		events = append(events, msgREv(t, "response.function_call_arguments.done", map[string]any{"output_index": i, "arguments": args}))
	}
	events = append(events, msgRCompleted(t, "completed", nil, nil))
	s := msgFeed(t, msgRTranslator(t), events...)
	s.lifecycle(t)
	got := msgJSON(t, s.blocks(t))
	want := `[{"id":"call_a","input":{"a":1},"name":"tool_a","type":"tool_use"},{"id":"call_b","input":{"b":2},"name":"tool_b","type":"tool_use"},{"id":"call_c","input":{"c":3},"name":"tool_c","type":"tool_use"}]`
	if got != want {
		t.Errorf("blocks %s", got)
	}
}

func TestMessagesFromResponsesInterleavedCalls(t *testing.T) {
	// Arguments of an earlier call arriving after a later call began still
	// reach it, and the block framing stays valid.
	s := msgFeed(t, msgRTranslator(t),
		msgRCreated,
		msgRAdded(t, 0, map[string]any{"type": "function_call", "call_id": "call_a", "name": "A"}),
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"x\":"}`,
		msgRAdded(t, 1, map[string]any{"type": "function_call", "call_id": "call_b", "name": "B"}),
		`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"y\":2}"}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"1}"}`,
		`{"type":"response.function_call_arguments.done","output_index":1,"arguments":"{\"y\":2}"}`,
		`{"type":"response.function_call_arguments.done","output_index":0,"arguments":"{\"x\":1}"}`,
		msgRCompleted(t, "completed", nil, nil),
	)
	s.lifecycle(t)
	if got := msgJSON(t, s.blocks(t)); got != `[{"id":"call_a","input":{"x":1},"name":"A","type":"tool_use"},{"id":"call_b","input":{"y":2},"name":"B","type":"tool_use"}]` {
		t.Errorf("blocks %s", got)
	}
}

func TestMessagesFromResponsesReadTool(t *testing.T) {
	for _, tc := range []struct {
		name, args, want string
	}{
		{"empty pages dropped", `{"file_path":"/tmp/a.go","pages":""}`, `{"file_path":"/tmp/a.go"}`},
		{"pages kept", `{"file_path":"/tmp/a.pdf","pages":"1-2"}`, `{"file_path":"/tmp/a.pdf","pages":"1-2"}`},
		{"no pages", `{"file_path":"/tmp/a.go","limit":20}`, `{"file_path":"/tmp/a.go","limit":20}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			half := len(tc.args) / 2
			s := msgFeed(t, msgRTranslator(t),
				msgRCreated,
				msgRAdded(t, 0, map[string]any{"type": "function_call", "call_id": "call_r", "name": "Read"}),
				msgREv(t, "response.function_call_arguments.delta", map[string]any{"output_index": 0, "delta": tc.args[:half]}),
				msgREv(t, "response.function_call_arguments.delta", map[string]any{"output_index": 0, "delta": tc.args[half:]}),
				msgREv(t, "response.function_call_arguments.done", map[string]any{"output_index": 0, "arguments": tc.args}),
				msgRCompleted(t, "completed", nil, nil),
			)
			s.lifecycle(t)
			if got := msgJSON(t, s.blocks(t)[0]["input"]); got != msgJSON(t, msgParse(t, tc.want)) {
				t.Errorf("input %s", got)
			}
		})
	}
	// Other tools keep empty strings.
	s := msgFeed(t, msgRTranslator(t), msgRCreated,
		msgRAdded(t, 0, map[string]any{"type": "function_call", "call_id": "call_w", "name": "Write"}),
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"pages\":\"\"}"}`,
		msgRCompleted(t, "completed", nil, nil))
	if got := msgJSON(t, s.blocks(t)[0]["input"]); got != `{"pages":""}` {
		t.Errorf("Write input %s", got)
	}
}

func TestMessagesFromResponsesTextRecovery(t *testing.T) {
	delta := func(o, c int, d string) string {
		return msgREv(t, "response.output_text.delta", map[string]any{"output_index": o, "content_index": c, "delta": d})
	}
	done := func(o, c int, text string) string {
		return msgREv(t, "response.output_text.done", map[string]any{"output_index": o, "content_index": c, "text": text})
	}
	message := func(texts ...string) map[string]any {
		var parts []any
		for _, x := range texts {
			parts = append(parts, map[string]any{"type": "output_text", "text": x})
		}
		return map[string]any{"type": "message", "role": "assistant", "content": parts}
	}
	completed := msgRCompleted(t, "completed", nil, nil)
	for _, tc := range []struct {
		name   string
		events []string
		want   string
		stop   string
	}{
		{"done only", []string{done(0, 0, "Hello world"), completed}, "Hello world", "end_turn"},
		{"content part done only", []string{`{"type":"response.content_part.done","output_index":0,"content_index":0,"part":{"type":"output_text","text":"part"}}`, completed}, "part", "end_turn"},
		{"item done only", []string{msgRDone(t, 0, message("from item")), completed}, "from item", "end_turn"},
		{"terminal only", []string{msgRCompleted(t, "completed", nil, []any{message("the answer")})}, "the answer", "end_turn"},
		{"terminal, every message", []string{msgRCompleted(t, "completed", nil, []any{
			map[string]any{"type": "reasoning"}, message("first"), message("second")})}, "firstsecond", "end_turn"},
		{"no duplicate of deltas", []string{delta(0, 0, "Hello"), delta(0, 0, " world"), done(0, 0, "Hello world"), msgRCompleted(t, "completed", nil, []any{message("Hello world")})}, "Hello world", "end_turn"},
		{"no duplicate of recovered done", []string{done(0, 0, "Hi"), msgRDone(t, 0, message("Hi")), msgRCompleted(t, "completed", nil, []any{message("Hi")})}, "Hi", "end_turn"},
		{"only the missing suffix", []string{delta(0, 0, "Hello"), done(0, 0, "Hello world"), completed}, "Hello world", "end_turn"},
		{"diverging done ignored", []string{delta(0, 0, "Hello"), done(0, 0, "Goodbye"), completed}, "Hello", "end_turn"},
		{"done under other content index", []string{delta(0, 0, "Hello world"), done(0, 1, "Hello world"), completed}, "Hello world", "end_turn"},
		{"done under other output index", []string{delta(0, 0, "Hello world"), done(1, 0, "Hello world"), completed}, "Hello world", "end_turn"},
		{"partial then unmatchable", []string{delta(0, 0, "Hello"), done(0, 1, "Hello world"), completed}, "Hello", "end_turn"},
		{"keys by both indexes", []string{delta(0, 0, "first"), done(0, 0, "first"), delta(0, 1, "second"), done(0, 1, "second-tail"), completed}, "firstsecond-tail", "end_turn"},
		{"terminal ignored once text streamed", []string{delta(0, 0, "streamed"), msgRCompleted(t, "completed", nil, []any{message("different")})}, "streamed", "end_turn"},
		{"tool-only turn stays text free", []string{
			msgRAdded(t, 0, map[string]any{"type": "function_call", "call_id": "call_1", "name": "lookup"}),
			msgREv(t, "response.function_call_arguments.done", map[string]any{"output_index": 0, "arguments": "{}", "text": "not text"}),
			msgRCompleted(t, "completed", nil, []any{map[string]any{"type": "function_call", "call_id": "call_1", "name": "lookup", "arguments": "{}"}})}, "", "tool_use"},
		{"done text while a tool block is open", []string{
			msgRAdded(t, 0, map[string]any{"type": "function_call", "call_id": "call_1", "name": "lookup"}),
			done(1, 0, "spoken after the call"), completed}, "spoken after the call", "tool_use"},
		{"terminal without message text", []string{msgRCompleted(t, "completed", nil, []any{message("")})}, "", "end_turn"},
		{"terminal refusal", []string{msgRCompleted(t, "completed", nil, []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "refusal", "refusal": "I can't."}}}})}, "I can't.", "refusal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := msgFeed(t, msgRTranslator(t), append([]string{msgRCreated}, tc.events...)...)
			s.lifecycle(t)
			if s.text() != tc.want || s.stopReason() != tc.stop {
				t.Errorf("text %q stop %q, want %q %q", s.text(), s.stopReason(), tc.want, tc.stop)
			}
		})
	}
}

func TestMessagesFromResponsesTerminalTools(t *testing.T) {
	// Tool calls only in the terminal response still reach the client.
	s := msgFeed(t, msgRTranslator(t), msgRCreated,
		msgRCompleted(t, "completed", nil, []any{
			map[string]any{"type": "function_call", "call_id": "call_z", "name": "Bash", "arguments": `{"command":"pwd"}`},
		}))
	s.lifecycle(t)
	if got := msgJSON(t, s.blocks(t)); got != `[{"id":"call_z","input":{"command":"pwd"},"name":"Bash","type":"tool_use"}]` || s.stopReason() != "tool_use" {
		t.Errorf("blocks %s stop %s", got, s.stopReason())
	}
}

func TestMessagesFromResponsesStopReasons(t *testing.T) {
	text := `{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"x"}`
	tool := msgRAdded(t, 1, map[string]any{"type": "function_call", "call_id": "c", "name": "T"})
	later := `{"type":"response.output_text.delta","output_index":2,"content_index":0,"delta":"after"}`
	incomplete := func(reason string) string {
		return msgJSON(t, map[string]any{"type": "response.incomplete", "response": map[string]any{"status": "incomplete", "incomplete_details": map[string]any{"reason": reason}}})
	}
	for _, tc := range []struct {
		name   string
		events []string
		want   string
	}{
		{"end_turn", []string{text, msgRCompleted(t, "completed", nil, nil)}, "end_turn"},
		{"tool_use", []string{text, tool, msgRCompleted(t, "completed", nil, nil)}, "tool_use"},
		{"tool_use survives later text", []string{tool, later, msgRCompleted(t, "completed", nil, nil)}, "tool_use"},
		{"max_tokens", []string{text, incomplete("max_output_tokens")}, "max_tokens"},
		{"max_tokens over tool", []string{tool, incomplete("max_output_tokens")}, "max_tokens"},
		{"content_filter", []string{text, incomplete("content_filter")}, "refusal"},
		{"incomplete other", []string{text, incomplete("other")}, "end_turn"},
		{"response.done", []string{text, `{"type":"response.done","response":{"status":"completed"}}`}, "end_turn"},
		{"refusal", []string{`{"type":"response.refusal.delta","output_index":0,"content_index":0,"delta":"No."}`, `{"type":"response.refusal.done","output_index":0,"content_index":0,"refusal":"No."}`, msgRCompleted(t, "completed", nil, nil)}, "refusal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := msgFeed(t, msgRTranslator(t), append([]string{msgRCreated}, tc.events...)...)
			s.lifecycle(t)
			if s.stopReason() != tc.want {
				t.Errorf("stop %q, want %q", s.stopReason(), tc.want)
			}
		})
	}
}

func TestMessagesFromResponsesErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events []string
		msg    string
	}{
		{"failed", []string{msgRCreated, `{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"boom"}}}`}, "boom"},
		{"failed after text", []string{msgRCreated, `{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"x"}`, `{"type":"response.failed","response":{"status":"failed"}}`}, "response failed"},
		{"error event", []string{`{"type":"error","code":"rate_limit_exceeded","message":"slow down"}`}, "slow down"},
		{"error event nested", []string{`{"type":"error","error":{"type":"invalid_request_error","message":"bad input"}}`}, "bad input"},
		{"nothing", nil, "without a response"},
		{"created only", []string{msgRCreated}, "without a response"},
		{"not JSON", []string{`nope`}, "responses event"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var emitted int
			tr := msgRTranslator(t)
			var err error
			for _, e := range tc.events {
				if err = tr.Event([]byte(e), func([]byte) error { emitted++; return nil }); err != nil {
					break
				}
			}
			if err == nil {
				err = tr.Close(func([]byte) error { emitted++; return nil })
			}
			if err == nil || !strings.Contains(err.Error(), tc.msg) {
				t.Errorf("error %v, want %q", err, tc.msg)
			}
			// Failing before any content, the client got nothing.
			if tc.name != "failed after text" && emitted != 0 {
				t.Errorf("%d events before the error", emitted)
			}
		})
	}
}

func TestMessagesFromResponsesCutShort(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events []string
		stop   string
		blocks string
	}{
		{"text", []string{msgRCreated, `{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"partial"}`}, "end_turn", `[{"text":"partial","type":"text"}]`},
		{"tool", []string{msgRCreated, msgRAdded(t, 0, map[string]any{"type": "function_call", "call_id": "c1", "name": "Bash"}),
			`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"command\":\"ls\"}"}`}, "tool_use", `[{"id":"c1","input":{"command":"ls"},"name":"Bash","type":"tool_use"}]`},
		{"done text only", []string{msgRCreated, `{"type":"response.output_text.done","output_index":0,"content_index":0,"text":"recovered"}`}, "end_turn", `[{"text":"recovered","type":"text"}]`},
		{"reasoning", []string{msgRCreated, msgRAdded(t, 0, map[string]any{"type": "reasoning"}), `{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"hmm"}`}, "end_turn", `[{"signature":"","thinking":"hmm","type":"thinking"}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := msgFeed(t, msgRTranslator(t), tc.events...)
			s.lifecycle(t)
			if s.stopReason() != tc.stop || msgJSON(t, s.blocks(t)) != tc.blocks {
				t.Errorf("stop %q blocks %s", s.stopReason(), msgJSON(t, s.blocks(t)))
			}
		})
	}
}

func TestMessagesFromResponsesAfterStop(t *testing.T) {
	tr := msgRTranslator(t)
	s := msgFeed(t, tr, msgRCreated, msgRCompleted(t, "completed", nil, []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "the answer"}}}}),
		`{"type":"response.output_text.done","output_index":7,"content_index":0,"text":"late text"}`)
	s.lifecycle(t)
	if s.text() != "the answer" {
		t.Errorf("text %q", s.text())
	}
}

func TestMessagesFromResponsesModel(t *testing.T) {
	// Without a client-facing name, the model the request named is used.
	_, c := msgConvert(t, Responses, msgReq(t, "gpt-5.5", []any{msgUser("x")}, nil), Options{})
	s := msgFeed(t, c.Translator, msgRCreated, `{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"x"}`, msgRCompleted(t, "completed", nil, nil))
	if m := s.events[0]["message"].(map[string]any)["model"]; m != "gpt-5.5" {
		t.Errorf("model %v", m)
	}
}

func TestMessagesFromResponsesReasoningWithoutDone(t *testing.T) {
	// A reasoning item that is never finished does not hold back the text
	// after it.
	tr := msgRTranslator(t)
	var got []string
	emit := func(b []byte) error { got = append(got, string(b)); return nil }
	for _, e := range []string{
		msgRCreated,
		msgRAdded(t, 0, map[string]any{"type": "reasoning", "id": "rs_1"}),
		msgRAdded(t, 1, map[string]any{"type": "message", "role": "assistant"}),
		`{"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"now"}`,
	} {
		if err := tr.Event([]byte(e), emit); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) == 0 || !strings.Contains(got[len(got)-1], `"text_delta"`) {
		t.Fatalf("text held back: %v", got)
	}
	// Its late done event still delivers the encrypted reasoning.
	s := msgFeed(t, tr,
		msgRDone(t, 0, map[string]any{"type": "reasoning", "id": "rs_1", "encrypted_content": "LATE"}),
		msgRCompleted(t, "completed", nil, nil))
	if s.err != nil {
		t.Fatal(s.err)
	}
	all := strings.Join(got, "") + strings.Join(func() []string {
		var r []string
		for _, b := range s.raw {
			r = append(r, string(b))
		}
		return r
	}(), "")
	if !strings.Contains(all, msgSignaturePrefix+"LATE") || !strings.HasSuffix(all, `{"type":"message_stop"}`) {
		t.Errorf("events %s", all)
	}
}
