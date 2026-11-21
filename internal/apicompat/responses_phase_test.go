package apicompat

import (
	"encoding/json"
	"testing"
)

// A converted message followed by a call is commentary, and its done event
// says so before the call is added; the turn's last message is the final
// answer; an answer cut short names no final answer.
func TestResponsesPhaseStream(t *testing.T) {
	evs := respMustFeed(t, respMsgConv(t, respChatTools, Options{}).Translator,
		respMsgStart,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me look."}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"exec","input":{}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":\"ls\"}"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`,
		`{"type":"message_stop"}`,
	)
	var msgDone, callAdded int = -1, -1
	for i, e := range evs {
		switch {
		case e.typ() == "response.output_item.done" && e.item()["type"] == "message":
			msgDone = i
			if e.item()["phase"] != phaseCommentary {
				t.Fatalf("message done with phase %v", e.item()["phase"])
			}
		case e.typ() == "response.output_item.added" && e.item()["type"] == "function_call":
			callAdded = i
		}
	}
	if msgDone < 0 || callAdded < 0 || msgDone > callAdded {
		t.Fatalf("message done at %d, call added at %d: %v", msgDone, callAdded, respTypes(evs))
	}

	evs = respMustFeed(t, respChatConv(t, "", Options{}).Translator,
		`{"choices":[{"index":0,"delta":{"content":"Done."},"finish_reason":"stop"}]}`)
	if out := respOutput(evs); out[0]["phase"] != phaseFinal {
		t.Fatalf("final message: %v", out[0])
	}

	evs = respMustFeed(t, respChatConv(t, "", Options{}).Translator,
		`{"choices":[{"index":0,"delta":{"content":"Cut"},"finish_reason":"length"}]}`)
	if out := respOutput(evs); out[0]["phase"] != nil {
		t.Fatalf("cut message has a phase: %v", out[0])
	}
}

func TestResponsesPhaseOutput(t *testing.T) {
	for _, c := range []struct {
		out  string
		done bool
		want []any
	}{
		{`[{"type":"message","role":"assistant"},{"type":"function_call"}]`, true, []any{phaseCommentary, nil}},
		{`[{"type":"message","role":"assistant"},{"type":"reasoning"}]`, true, []any{phaseFinal, nil}},
		{`[{"type":"message","role":"assistant"},{"type":"message","role":"assistant"}]`, false, []any{phaseCommentary, nil}},
		{`[{"type":"message","role":"assistant","phase":"commentary"}]`, true, []any{phaseCommentary}},
		{`[{"type":"message","role":"user"}]`, true, []any{nil}},
	} {
		var out []any
		_ = json.Unmarshal([]byte(c.out), &out)
		phaseOutput(out, c.done)
		for i, w := range c.want {
			if got := out[i].(map[string]any)["phase"]; got != w {
				t.Errorf("%s done=%v: item %d phase %v, want %v", c.out, c.done, i, got, w)
			}
		}
	}
}

// The whole answer of a request that did not stream says its phases too, a
// phase the upstream wrote kept.
func TestResponsesPhaseAggregate(t *testing.T) {
	b, err := respAggregate([][]byte{
		[]byte(`{"type":"response.created","response":{"id":"r"}}`),
		[]byte(`{"type":"response.completed","response":{"id":"r","status":"completed","output":[
			{"type":"message","role":"assistant","content":[]},
			{"type":"function_call","call_id":"c","name":"f","arguments":"{}"},
			{"type":"message","role":"assistant","content":[]}]}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Output []map[string]any `json:"output"`
	}
	_ = json.Unmarshal(b, &res)
	if res.Output[0]["phase"] != phaseCommentary || res.Output[2]["phase"] != phaseFinal || res.Output[1]["phase"] != nil {
		t.Fatalf("phases: %s", b)
	}
}
