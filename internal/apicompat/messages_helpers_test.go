package apicompat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// msgT is a test's view of a converted stream.
type msgT struct {
	events []map[string]any
	raw    [][]byte
	err    error // from Event or Close
}

// msgFeed runs payloads through a translator, then closes it (unless an
// event failed).
func msgFeed(t *testing.T, tr Translator, payloads ...string) msgT {
	t.Helper()
	var out msgT
	emit := func(b []byte) error {
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("emitted invalid JSON %s: %v", b, err)
		}
		out.events = append(out.events, m)
		out.raw = append(out.raw, b)
		return nil
	}
	for _, p := range payloads {
		if err := tr.Event([]byte(p), emit); err != nil {
			out.err = err
			return out
		}
	}
	out.err = tr.Close(emit)
	return out
}

func (s msgT) types() []string {
	var out []string
	for _, e := range s.events {
		out = append(out, e["type"].(string))
	}
	return out
}

func (s msgT) deltas(kind, field string) string {
	var b strings.Builder
	for _, e := range s.events {
		if e["type"] != "content_block_delta" {
			continue
		}
		d := e["delta"].(map[string]any)
		if d["type"] == kind {
			b.WriteString(d[field].(string))
		}
	}
	return b.String()
}

func (s msgT) text() string     { return s.deltas("text_delta", "text") }
func (s msgT) thinking() string { return s.deltas("thinking_delta", "thinking") }

func (s msgT) last(typ string) map[string]any {
	for i := len(s.events) - 1; i >= 0; i-- {
		if s.events[i]["type"] == typ {
			return s.events[i]
		}
	}
	return nil
}

func (s msgT) stopReason() string {
	md := s.last("message_delta")
	if md == nil {
		return ""
	}
	r, _ := md["delta"].(map[string]any)["stop_reason"].(string)
	return r
}

func (s msgT) usage() map[string]int {
	md := s.last("message_delta")
	if md == nil {
		return nil
	}
	out := map[string]int{}
	for k, v := range md["usage"].(map[string]any) {
		out[k] = int(v.(float64))
	}
	return out
}

// aggregate is the non-streaming answer for the stream, as a map.
func (s msgT) aggregate(t *testing.T) map[string]any {
	t.Helper()
	b, err := Aggregate(Messages, s.raw)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// blocks is the aggregated content.
func (s msgT) blocks(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, c := range s.aggregate(t)["content"].([]any) {
		out = append(out, c.(map[string]any))
	}
	return out
}

// lifecycle checks the framing a strict Messages client relies on:
// message_start first, blocks numbered from 0 one at a time, every delta
// inside its open block, every block closed, message_delta then message_stop
// last.
func (s msgT) lifecycle(t *testing.T) {
	t.Helper()
	if s.err != nil {
		t.Fatalf("stream failed: %v", s.err)
	}
	types := s.types()
	if len(types) < 3 || types[0] != "message_start" || types[len(types)-1] != "message_stop" || types[len(types)-2] != "message_delta" {
		t.Fatalf("framing: %v", types)
	}
	open, next := false, 0
	for i, e := range s.events {
		switch e["type"] {
		case "message_start":
			if i != 0 {
				t.Fatalf("message_start at %d", i)
			}
		case "content_block_start":
			if open || int(e["index"].(float64)) != next {
				t.Fatalf("event %d: block start index %v (next %d, open %v): %v", i, e["index"], next, open, types)
			}
			open = true
		case "content_block_delta":
			if !open || int(e["index"].(float64)) != next {
				t.Fatalf("event %d: delta outside its block: %v", i, e)
			}
		case "content_block_stop":
			if !open || int(e["index"].(float64)) != next {
				t.Fatalf("event %d: stop outside its block", i)
			}
			open = false
			next++
		case "message_delta", "message_stop":
			if open {
				t.Fatalf("event %d: %v with a block open", i, e["type"])
			}
		}
	}
}

// msgJSON is a value as a JSON string (for compact payloads in tests).
func msgJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// msgConvert converts a Messages request and returns the body as a map.
func msgConvert(t *testing.T, to, body string, opt Options) (map[string]any, *Conversion) {
	t.Helper()
	c, err := Convert(Messages, to, []byte(body), opt)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(c.Body, &m); err != nil {
		t.Fatal(err)
	}
	return m, c
}

// msgItems is a converted Responses request's input.
func msgItems(m map[string]any) []map[string]any {
	var out []map[string]any
	for _, it := range m["input"].([]any) {
		out = append(out, it.(map[string]any))
	}
	return out
}

// msgChatMsgs is a converted chat request's messages.
func msgChatMsgs(m map[string]any) []map[string]any {
	var out []map[string]any
	for _, it := range m["messages"].([]any) {
		out = append(out, it.(map[string]any))
	}
	return out
}

// msgCaptured reads a captured client request's body.
func msgCaptured(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "captured", name))
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return []byte(c.Body)
}

// msgReq builds a Messages request body around messages (and extra fields).
func msgReq(t *testing.T, model string, messages any, extra map[string]any) string {
	t.Helper()
	m := map[string]any{"model": model, "max_tokens": 4096, "stream": true, "messages": messages}
	for k, v := range extra {
		m[k] = v
	}
	return msgJSON(t, m)
}

func msgUser(content any) map[string]any { return map[string]any{"role": "user", "content": content} }
func msgAssistant(content any) map[string]any {
	return map[string]any{"role": "assistant", "content": content}
}

func msgParse(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("bad JSON %s: %v", s, err)
	}
	return v
}
