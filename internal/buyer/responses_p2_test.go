package buyer

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sharetoken/internal/protocol"
)

// A client that is not Codex gets the standard events only, each item under
// the id it was added with; a heartbeat becomes a comment.
func TestRespTidy(t *testing.T) {
	var tidy respTidy
	cases := []struct {
		in   string
		want string
		what tidyResult
	}{
		{`{"type":"response.created","response":{"id":"r"}}`, "same", tidyKeep},
		{`{"type":"codex.rate_limits","primary":{}}`, "", tidyDrop},
		{`{"type":"response.metadata","metadata":{}}`, "", tidyDrop},
		{`{"type":"keepalive"}`, "", tidyComment},
		{`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_a","type":"message"}}`, "same", tidyKeep},
		{`{"type":"response.output_text.delta","output_index":0,"item_id":"msg_b","delta":"x"}`,
			`{"delta":"x","item_id":"msg_a","output_index":0,"type":"response.output_text.delta"}`, tidyKeep},
		{`{"type":"response.output_text.delta","output_index":0,"item_id":"msg_a","delta":"y"}`, "same", tidyKeep},
		{`{"type":"response.output_item.done","output_index":0,"item":{"id":"msg_c","type":"message"}}`,
			`{"item":{"id":"msg_a","type":"message"},"output_index":0,"type":"response.output_item.done"}`, tidyKeep},
		{`{"type":"response.completed","response":{"output":[{"id":"msg_d","type":"message"}]}}`,
			`{"response":{"output":[{"id":"msg_a","type":"message"}]},"type":"response.completed"}`, tidyKeep},
		{`{"type":"response.done","response":{"status":"incomplete","output":[]}}`,
			`{"response":{"output":[],"status":"incomplete"},"type":"response.incomplete"}`, tidyKeep},
	}
	for _, c := range cases {
		got, what := tidy.event([]byte(c.in))
		want := c.want
		if want == "same" {
			want = c.in
		}
		if what != c.what || what == tidyKeep && string(got) != want {
			t.Errorf("%s:\n got %s (%d)\nwant %s (%d)", c.in, got, what, want, c.what)
		}
	}
}

// Through the SSE writer: Codex gets every event, other clients the tidy ones.
func TestSSEWriterTidiesForOthers(t *testing.T) {
	for _, codex := range []bool{true, false} {
		rec := httptest.NewRecorder()
		s := &sseWriter{w: rec, api: protocol.APIResponses, codex: codex}
		if !codex {
			s.tidy = &respTidy{}
		}
		_ = s.event([]byte(`{"type":"response.created","response":{"id":"r"}}`))
		_ = s.event([]byte(`{"type":"codex.rate_limits"}`))
		_ = s.event([]byte(`{"type":"keepalive"}`))
		body := rec.Body.String()
		if strings.Contains(body, "codex.rate_limits") == !codex || strings.Contains(body, "\n\n: keepalive") == codex {
			t.Fatalf("codex=%v: %q", codex, body)
		}
	}
}

func TestPrevStore(t *testing.T) {
	var s prevStore
	item := json.RawMessage(`{"type":"message","role":"user","content":"hi"}`)
	s.put("a", &wsTurn{id: "r1", items: []json.RawMessage{item}})
	if s.get("a", "r1") == nil {
		t.Fatal("not kept")
	}
	if s.get("b", "r1") != nil {
		t.Fatal("another scope read it")
	}
	for i := 0; i < prevMost+5; i++ {
		s.put("a", &wsTurn{id: fmt.Sprint("x", i), items: []json.RawMessage{item}})
	}
	if s.ll.Len() > prevMost || s.get("a", "r1") != nil {
		t.Fatalf("%d kept, the oldest still there", s.ll.Len())
	}
	big := json.RawMessage(`"` + strings.Repeat("x", prevMostSize/2) + `"`)
	s.put("a", &wsTurn{id: "big1", items: []json.RawMessage{big}})
	s.put("a", &wsTurn{id: "big2", items: []json.RawMessage{big}})
	if s.size > prevMostSize || s.get("a", "big1") != nil || s.get("a", "big2") == nil {
		t.Fatalf("size %d", s.size)
	}
	e := s.m[prevKey{"a", "big2"}].Value.(*prevEntry)
	e.at = time.Now().Add(-prevTTL - time.Minute)
	if s.get("a", "big2") != nil {
		t.Fatal("kept past prevTTL")
	}
}

func TestPrevExpandAndWatch(t *testing.T) {
	prior := &wsTurn{id: "r1", items: []json.RawMessage{json.RawMessage(`{"a":1}`), json.RawMessage(`{"b":2}`)}}
	body, input, err := prevExpand([]byte(`{"model":"m","previous_response_id":"r1","input":"next"}`), prior)
	if err != nil || len(input) != 3 || strings.Contains(string(body), "previous_response_id") || !strings.Contains(string(body), `{"a":1}`) {
		t.Fatalf("%s %d %v", body, len(input), err)
	}
	var w turnWatch
	w.see([]byte(`{"type":"response.created","response":{"id":"r2"}}`))
	w.see([]byte(`{"type":"response.output_item.done","item":{"x":1}}`))
	w.see([]byte(`{"type":"response.completed","response":{"id":"r2","output":[]}}`))
	if turn := w.turn(input); turn.id != "r2" || len(turn.items) != 4 {
		t.Fatalf("turn %+v", turn)
	}
	if turn := answerTurn([]byte(`{"id":"r3","output":[{"y":1}]}`), input); turn.id != "r3" || len(turn.items) != 4 {
		t.Fatalf("answer turn %+v", turn)
	}
	if !storeOff([]byte(`{"store":false}`)) || storeOff([]byte(`{}`)) || storeOff([]byte(`{"store":true}`)) {
		t.Fatal("storeOff")
	}
}

func TestPrevScope(t *testing.T) {
	a := httptest.NewRequest("POST", "/v1/responses", nil)
	a.Header.Set("Authorization", "Bearer k1")
	b := httptest.NewRequest("POST", "/v1/responses", nil)
	b.Header.Set("Authorization", "Bearer k2")
	if prevScope(a) == prevScope(b) {
		t.Fatal("two keys share a scope")
	}
}

// An unknown previous_response_id is refused as OpenAI refuses it, before
// any job.
func TestResponsesPrevNotFound(t *testing.T) {
	p := &Proxy{}
	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/responses",
		strings.NewReader(`{"model":"m","input":"hi","previous_response_id":"resp_nope"}`)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"code":"previous_response_not_found"`) ||
		!strings.Contains(rec.Body.String(), `"param":"previous_response_id"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestCompactFails(t *testing.T) {
	var f compactFails
	key := compactKey([]byte(`{"model":"m","prompt_cache_key":"c"}`))
	if key == "" || compactKey([]byte(`{"model":"m"}`)) != "" {
		t.Fatal("compactKey")
	}
	fail := protocol.Errorf("upstream_error", "the stream ended")
	for i := 0; i < compactFailsMost-1; i++ {
		f.failed(key, fail)
	}
	if _, held := f.held(key, "m"); held {
		t.Fatal("held too soon")
	}
	f.failed(key, protocol.Errorf(protocol.CodeInsufficientFunds, "no credits"))
	if _, held := f.held(key, "m"); held {
		t.Fatal("an account failure counted")
	}
	f.failed(key, fail)
	msg, held := f.held(key, "m")
	if !held || !strings.Contains(msg, "3 times") {
		t.Fatalf("not held: %q", msg)
	}
	f.done(key)
	if _, held := f.held(key, "m"); held {
		t.Fatal("held after a summary came")
	}
	// Failures too far apart do not add up.
	for i := 0; i < compactFailsMost; i++ {
		f.failed(key, errors.New("x"))
		c := f.m[key]
		c.last = c.last.Add(-compactHeldFor)
		f.m[key] = c
	}
	if _, held := f.held(key, "m"); held {
		t.Fatal("held for old failures")
	}
}

func TestCodexTurnID(t *testing.T) {
	h := http.Header{}
	for _, c := range []struct {
		body, header, want string
	}{
		{`{"client_metadata":{"turn_id":"t1"}}`, "", "t1"},
		{`{"client_metadata":{"x-codex-turn-metadata":"{\"turn_id\":\"t2\"}"}}`, "", "t2"},
		{`{"input":"x"}`, `{"turn_id":"t3","thread_id":"s"}`, "t3"},
		{`{"input":"x"}`, "", ""},
	} {
		h.Set("x-codex-turn-metadata", c.header)
		if got := codexTurnID(h, []byte(c.body)); got != c.want {
			t.Errorf("%s / %s: %q", c.body, c.header, got)
		}
	}
}
