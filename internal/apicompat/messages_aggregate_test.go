package apicompat

import (
	"encoding/json"
	"strings"
	"testing"
)

func msgPayloads(events ...string) [][]byte {
	var out [][]byte
	for _, e := range events {
		out = append(out, []byte(e))
	}
	return out
}

func TestAggregateMessages(t *testing.T) {
	// As an Anthropic upstream streams it.
	b, err := Aggregate(Messages, msgPayloads(
		`{"type":"message_start","message":{"id":"msg_01","type":"message","role":"assistant","model":"claude-sonnet-5-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":472,"cache_creation_input_tokens":100,"cache_read_input_tokens":2000,"output_tokens":1,"service_tier":"standard"}}}`,
		`{"type":"ping"}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me "}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"check."}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"EqQBCkgI"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"EmwKAhgB"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"Reading "}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"citations_delta","citation":{"type":"char_location","cited_text":"x"}}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"it."}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}}`,
		`{"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":""}}`,
		`{"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"{\"file_path\":"}}`,
		`{"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"\"/a.txt\"}"}}`,
		`{"type":"content_block_stop","index":3}`,
		`{"type":"content_block_start","index":4,"content_block":{"type":"tool_use","id":"toolu_2","name":"TodoRead","input":{}}}`,
		`{"type":"content_block_stop","index":4}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":87}}`,
		`{"type":"message_stop"}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"content":[` +
		`{"signature":"EqQBCkgI","thinking":"Let me check.","type":"thinking"},` +
		`{"data":"EmwKAhgB","type":"redacted_thinking"},` +
		`{"citations":[{"cited_text":"x","type":"char_location"}],"text":"Reading it.","type":"text"},` +
		`{"id":"toolu_1","input":{"file_path":"/a.txt"},"name":"Read","type":"tool_use"},` +
		`{"id":"toolu_2","input":{},"name":"TodoRead","type":"tool_use"}],` +
		`"id":"msg_01","model":"claude-sonnet-5-5","role":"assistant","stop_reason":"tool_use","stop_sequence":null,"type":"message",` +
		`"usage":{"cache_creation_input_tokens":100,"cache_read_input_tokens":2000,"input_tokens":472,"output_tokens":87,"service_tier":"standard"}}`
	if string(b) != want {
		t.Errorf("got\n%s\nwant\n%s", b, want)
	}
}

func TestAggregateMessagesEdges(t *testing.T) {
	start := `{"type":"message_start","message":{"id":"msg_x","model":"m","usage":{"input_tokens":3,"output_tokens":0}}}`
	for _, tc := range []struct {
		name   string
		events []string
		want   string // a substring of the answer, or of the error
		err    bool
	}{
		{"empty message", []string{start, `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":0}}`, `{"type":"message_stop"}`},
			`"content":[],"id":"msg_x","model":"m","role":"assistant","stop_reason":"end_turn","stop_sequence":null,"type":"message","usage":{"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"input_tokens":3,"output_tokens":0}`, false},
		{"stop sequence", []string{start, `{"type":"message_delta","delta":{"stop_reason":"stop_sequence","stop_sequence":"END"},"usage":{"output_tokens":4}}`},
			`"stop_reason":"stop_sequence","stop_sequence":"END"`, false},
		{"usage in message_delta wins", []string{start, `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":10,"cache_read_input_tokens":5,"output_tokens":4}}`},
			`"usage":{"cache_creation_input_tokens":0,"cache_read_input_tokens":5,"input_tokens":10,"output_tokens":4}`, false},
		{"tool input cut short", []string{start, `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t","name":"W","input":{}}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"content\":\"ab"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":4}}`}, `"input":{}`, false},
		{"no message_start", []string{`{"type":"message_stop"}`}, "no message_start", true},
		{"delta before start", []string{start, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"x"}}`}, "before its start", true},
		{"error event", []string{start, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`}, "Overloaded", true},
		{"bad JSON", []string{"{"}, "messages event", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := Aggregate(Messages, msgPayloads(tc.events...))
			if tc.err {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Errorf("error %v", err)
				}
				return
			}
			if err != nil || !strings.Contains(string(b), tc.want) {
				t.Errorf("got %s (%v), want %s", b, err, tc.want)
			}
			if !json.Valid(b) {
				t.Error("invalid JSON")
			}
		})
	}
}

// A stream this package made aggregates to the answer it carries.
func TestAggregateMessagesConverted(t *testing.T) {
	s := msgFeed(t, msgRTranslator(t),
		msgRCreated,
		msgRAdded(t, 0, map[string]any{"type": "reasoning"}),
		msgRDone(t, 0, map[string]any{"type": "reasoning", "encrypted_content": "E", "summary": []any{map[string]any{"type": "summary_text", "text": "s"}}}),
		`{"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"hi"}`,
		msgRAdded(t, 2, map[string]any{"type": "function_call", "call_id": "call_1", "name": "Bash"}),
		`{"type":"response.function_call_arguments.delta","output_index":2,"delta":"{\"command\":\"ls\"}"}`,
		msgRCompleted(t, "completed", map[string]any{"input_tokens": 50, "output_tokens": 9, "input_tokens_details": map[string]any{"cached_tokens": 20}}, nil),
	)
	agg := s.aggregate(t)
	want := `{"content":[{"signature":"sharetoken-reasoning-v1:E","thinking":"s","type":"thinking"},{"text":"hi","type":"text"},{"id":"call_1","input":{"command":"ls"},"name":"Bash","type":"tool_use"}],` +
		`"id":"` + agg["id"].(string) + `","model":"claude-sonnet-5-5","role":"assistant","stop_reason":"tool_use","stop_sequence":null,"type":"message",` +
		`"usage":{"cache_creation_input_tokens":0,"cache_read_input_tokens":20,"input_tokens":30,"output_tokens":9}}`
	if got := msgJSON(t, agg); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}
