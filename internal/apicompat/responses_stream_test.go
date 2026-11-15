package apicompat

import (
	"encoding/json"
	"strings"
	"testing"
)

// respEv is one emitted client event, decoded.
type respEv map[string]any

func (e respEv) typ() string { s, _ := e["type"].(string); return s }

func (e respEv) item() map[string]any { m, _ := e["item"].(map[string]any); return m }

func (e respEv) response() map[string]any { m, _ := e["response"].(map[string]any); return m }

// respConvert converts a Responses request and fails the test on error.
func respConvert(t *testing.T, to string, body string, opt Options) *Conversion {
	t.Helper()
	c, err := Convert(Responses, to, []byte(body), opt)
	if err != nil {
		t.Fatalf("Convert(responses → %s): %v", to, err)
	}
	return c
}

// respBody decodes a converted request body.
func respBody(t *testing.T, c *Conversion) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(c.Body, &m); err != nil {
		t.Fatalf("converted body is not JSON: %v\n%s", err, c.Body)
	}
	return m
}

// respFeed runs upstream payloads through a translator, then Close, and
// returns the client events and the first error.
func respFeed(t *testing.T, tr Translator, payloads ...string) ([]respEv, [][]byte, error) {
	t.Helper()
	var evs []respEv
	var raw [][]byte
	emit := func(b []byte) error {
		var m respEv
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("emitted payload is not JSON: %v\n%s", err, b)
		}
		raw = append(raw, append([]byte(nil), b...))
		evs = append(evs, m)
		return nil
	}
	for _, p := range payloads {
		if err := tr.Event([]byte(p), emit); err != nil {
			return evs, raw, err
		}
	}
	return evs, raw, tr.Close(emit)
}

// respMustFeed is respFeed that must succeed and produce a well-formed stream.
func respMustFeed(t *testing.T, tr Translator, payloads ...string) []respEv {
	t.Helper()
	evs, _, err := respFeed(t, tr, payloads...)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	respCheckStream(t, evs)
	return evs
}

// respCheckStream checks the invariants of every client stream: sequence
// numbers 0,1,2,...; created first and one terminal event last; each item
// added once and done once, with matching ids and types; the terminal output
// is the done items in output_index order.
func respCheckStream(t *testing.T, evs []respEv) {
	t.Helper()
	if len(evs) < 3 {
		t.Fatalf("stream too short: %v", respTypes(evs))
	}
	for i, e := range evs {
		if n, ok := e["sequence_number"].(float64); !ok || int(n) != i {
			t.Fatalf("event %d (%s) has sequence_number %v", i, e.typ(), e["sequence_number"])
		}
	}
	if evs[0].typ() != "response.created" || evs[1].typ() != "response.in_progress" {
		t.Fatalf("stream must start with created, in_progress: %v", respTypes(evs))
	}
	last := evs[len(evs)-1]
	if lt := last.typ(); lt != "response.completed" && lt != "response.incomplete" {
		t.Fatalf("stream must end with a terminal event: %v", respTypes(evs))
	}
	added := map[string]map[string]any{}
	doneByIndex := map[int]map[string]any{}
	for _, e := range evs[:len(evs)-1] {
		switch e.typ() {
		case "response.completed", "response.incomplete", "response.failed":
			t.Fatalf("terminal event before the end: %v", respTypes(evs))
		case "response.output_item.added":
			it := e.item()
			id, _ := it["id"].(string)
			if id == "" || added[id] != nil {
				t.Fatalf("output_item.added with empty or repeated id: %v", it)
			}
			added[id] = it
			if _, ok := e["output_index"].(float64); !ok {
				t.Fatalf("output_item.added without output_index")
			}
		case "response.output_item.done":
			it := e.item()
			id, _ := it["id"].(string)
			a := added[id]
			if a == nil {
				t.Fatalf("output_item.done for an item never added: %v", it)
			}
			if a["type"] != it["type"] {
				t.Fatalf("item %s added as %v, done as %v", id, a["type"], it["type"])
			}
			delete(added, id)
			doneByIndex[int(e["output_index"].(float64))] = it
		}
	}
	if len(added) > 0 {
		t.Fatalf("items added but never done: %v", added)
	}
	output, _ := last.response()["output"].([]any)
	if len(output) != len(doneByIndex) {
		t.Fatalf("terminal output has %d items, %d were done", len(output), len(doneByIndex))
	}
	for i, o := range output {
		want, ok := doneByIndex[i]
		if !ok {
			t.Fatalf("no done item at output_index %d", i)
		}
		wb, _ := json.Marshal(want)
		ob, _ := json.Marshal(o)
		if string(wb) != string(ob) {
			t.Fatalf("terminal output[%d] differs from its done item:\n%s\n%s", i, ob, wb)
		}
	}
}

func respTypes(evs []respEv) []string {
	var out []string
	for _, e := range evs {
		out = append(out, e.typ())
	}
	return out
}

func respFind(evs []respEv, typ string) []respEv {
	var out []respEv
	for _, e := range evs {
		if e.typ() == typ {
			out = append(out, e)
		}
	}
	return out
}

func respTerminal(evs []respEv) map[string]any { return evs[len(evs)-1].response() }

func respOutput(evs []respEv) []map[string]any {
	var out []map[string]any
	for _, o := range respTerminal(evs)["output"].([]any) {
		out = append(out, o.(map[string]any))
	}
	return out
}

func respUsageOf(evs []respEv) map[string]any {
	u, _ := respTerminal(evs)["usage"].(map[string]any)
	return u
}

func respNum(m map[string]any, path ...string) int {
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return -1
		}
		cur = mm[p]
	}
	f, ok := cur.(float64)
	if !ok {
		return -1
	}
	return int(f)
}

func respJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := respMarshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The wire shapes strict clients (Codex) require, field by field.
func TestResponsesStreamWireShapes(t *testing.T) {
	s := newRespStream("m", nil)
	var evs []respEv
	s.emit = func(b []byte) error {
		var m respEv
		_ = json.Unmarshal(b, &m)
		evs = append(evs, m)
		return nil
	}
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.reasoningDelta("think"))
	must(s.closeReasoning(""))
	must(s.textDelta("hi"))
	must(s.closeMessage())
	c, err := s.openCall("call_1", "exec")
	must(err)
	must(s.callArgs(c, `{"a":1}`))
	must(s.finish("", respUsage{input: 3, output: 2}))
	respCheckStream(t, evs)

	has := func(e respEv, keys ...string) {
		t.Helper()
		for _, k := range keys {
			if _, ok := e[k]; !ok {
				t.Errorf("%s lacks %q: %v", e.typ(), k, e)
			}
		}
	}
	for _, e := range evs {
		switch e.typ() {
		case "response.output_text.delta":
			has(e, "item_id", "output_index", "content_index", "delta", "logprobs")
		case "response.output_text.done":
			has(e, "item_id", "output_index", "content_index", "text")
		case "response.content_part.added", "response.content_part.done":
			has(e, "item_id", "output_index", "content_index", "part")
			p := e["part"].(map[string]any)
			for _, k := range []string{"type", "text", "annotations", "logprobs"} {
				if _, ok := p[k]; !ok {
					t.Errorf("output_text part lacks %q", k)
				}
			}
		case "response.reasoning_summary_part.added", "response.reasoning_summary_part.done",
			"response.reasoning_summary_text.delta", "response.reasoning_summary_text.done":
			has(e, "item_id", "output_index", "summary_index")
		case "response.function_call_arguments.delta":
			has(e, "item_id", "output_index", "delta")
		case "response.function_call_arguments.done":
			has(e, "item_id", "output_index", "arguments")
		case "response.output_item.added", "response.output_item.done":
			it := e.item()
			switch it["type"] {
			case "message":
				if _, ok := it["content"].([]any); !ok {
					t.Errorf("message item without content array: %v", it)
				}
				has(respEv(it), "role", "status")
			case "reasoning":
				if _, ok := it["summary"].([]any); !ok {
					t.Errorf("reasoning item without summary array: %v", it)
				}
			case "function_call":
				has(respEv(it), "call_id", "name", "arguments", "status")
				if e.typ() == "response.output_item.added" && it["arguments"] != "" {
					t.Errorf("function_call added with arguments %v", it["arguments"])
				}
			}
		case "response.created", "response.completed":
			r := e.response()
			has(respEv(r), "id", "object", "created_at", "status", "model", "output")
			if !strings.HasPrefix(r["id"].(string), "resp_") || r["object"] != "response" || r["model"] != "m" {
				t.Errorf("response object: %v", r)
			}
		}
	}
	u := respUsageOf(evs)
	if respNum(u, "input_tokens") != 3 || respNum(u, "output_tokens") != 2 || respNum(u, "total_tokens") != 5 ||
		respNum(u, "input_tokens_details", "cached_tokens") != 0 || respNum(u, "output_tokens_details", "reasoning_tokens") != 0 {
		t.Errorf("usage: %v", u)
	}
	if _, ok := u["cache_creation_input_tokens"]; ok {
		t.Errorf("no cache write was reported: %v", u)
	}
}

func TestResponsesStreamIncompleteAndUsage(t *testing.T) {
	s := newRespStream("m", nil)
	var last []byte
	s.emit = func(b []byte) error { last = b; return nil }
	if err := s.textDelta("x"); err != nil {
		t.Fatal(err)
	}
	if err := s.finish("max_output_tokens", respUsage{input: 10, cached: 4, cacheWrite: 3, output: 5, reasoning: 2}); err != nil {
		t.Fatal(err)
	}
	var ev respEv
	_ = json.Unmarshal(last, &ev)
	r := ev.response()
	if ev.typ() != "response.incomplete" || r["status"] != "incomplete" ||
		r["incomplete_details"].(map[string]any)["reason"] != "max_output_tokens" {
		t.Fatalf("terminal: %s", last)
	}
	u := r["usage"].(map[string]any)
	if respNum(u, "input_tokens_details", "cache_creation_tokens") != 3 || respNum(u, "cache_creation_input_tokens") != 3 ||
		respNum(u, "input_tokens_details", "cached_tokens") != 4 || respNum(u, "output_tokens_details", "reasoning_tokens") != 2 ||
		respNum(u, "total_tokens") != 15 {
		t.Fatalf("usage: %v", u)
	}
	// nothing more after the terminal event
	if err := s.finish("", respUsage{}); err != nil {
		t.Fatal(err)
	}
}

func TestAggregateResponses(t *testing.T) {
	item0 := `{"type":"reasoning","id":"rs_1","summary":[]}`
	item1 := `{"type":"message","id":"msg_1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"hi","annotations":[],"logprobs":[]}]}`
	tests := []struct {
		name     string
		payloads []string
		want     string // the expected output ids, comma separated
		status   string
		err      bool
	}{
		{"completed carries output", []string{
			`{"type":"response.created","response":{"id":"resp_1","output":[]}}`,
			`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[` + item1 + `],"usage":{"input_tokens":1}}}`,
		}, "msg_1", "completed", false},
		{"empty terminal output is rebuilt in index order", []string{
			`{"type":"response.output_item.done","output_index":1,"item":` + item1 + `}`,
			`{"type":"response.output_item.done","output_index":0,"item":` + item0 + `}`,
			`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}`,
		}, "rs_1,msg_1", "completed", false},
		{"incomplete", []string{
			`{"type":"response.output_item.done","output_index":0,"item":` + item1 + `}`,
			`{"type":"response.incomplete","response":{"id":"resp_1","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`,
		}, "msg_1", "incomplete", false},
		{"failed", []string{
			`{"type":"response.failed","response":{"id":"resp_1","status":"failed","error":{"code":"x","message":"y"}}}`,
		}, "", "failed", false},
		{"no terminal", []string{`{"type":"response.created","response":{}}`}, "", "", true},
		{"error event", []string{`{"type":"error","message":"boom"}`}, "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ps [][]byte
			for _, p := range tt.payloads {
				ps = append(ps, []byte(p))
			}
			b, err := Aggregate(Responses, ps)
			if tt.err {
				if err == nil {
					t.Fatalf("want error, got %s", b)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var r map[string]any
			if err := json.Unmarshal(b, &r); err != nil {
				t.Fatal(err)
			}
			if r["status"] != tt.status {
				t.Errorf("status %v", r["status"])
			}
			var ids []string
			out, _ := r["output"].([]any)
			for _, o := range out {
				ids = append(ids, o.(map[string]any)["id"].(string))
			}
			if strings.Join(ids, ",") != tt.want {
				t.Errorf("output ids %v, want %s", ids, tt.want)
			}
		})
	}
}

// The aggregator reads exactly what a translator emits.
func TestAggregateResponsesFromTranslator(t *testing.T) {
	c := respConvert(t, Messages, `{"model":"claude","input":"hi","max_output_tokens":100,"stream":true}`, Options{Model: "client-model"})
	_, raw, err := respFeed(t, c.Translator,
		`{"type":"message_start","message":{"id":"msg_1","model":"claude","usage":{"input_tokens":5,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7}}`,
		`{"type":"message_stop"}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Aggregate(Responses, raw)
	if err != nil {
		t.Fatal(err)
	}
	var r map[string]any
	_ = json.Unmarshal(b, &r)
	out := r["output"].([]any)
	if r["model"] != "client-model" || len(out) != 1 ||
		out[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"] != "Hello" {
		t.Fatalf("aggregate: %s", b)
	}
}
