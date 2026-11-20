package buyer

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"sharetoken/internal/protocol"
)

// deltas is text as the events of each API carry it, a piece at a time.
func deltas(api string, reasoning bool, pieces []string) [][]byte {
	var out [][]byte
	for _, p := range pieces {
		var v any
		switch api {
		case protocol.APIChat:
			key := "content"
			if reasoning {
				key = "reasoning_content"
			}
			v = map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{key: p}}}}
		case protocol.APIMessages:
			d := map[string]any{"type": "text_delta", "text": p}
			if reasoning {
				d = map[string]any{"type": "thinking_delta", "thinking": p}
			}
			v = map[string]any{"type": "content_block_delta", "index": 0, "delta": d}
		case protocol.APIResponses:
			typ := "response.output_text.delta"
			if reasoning {
				typ = "response.reasoning_summary_text.delta"
			}
			v = map[string]any{"type": typ, "output_index": 0, "delta": p}
		}
		b, _ := json.Marshal(v)
		out = append(out, b)
	}
	return out
}

func feedAll(g *loopGuard, events [][]byte) (loopTrip, int, bool) {
	for i, ev := range events {
		if t, ok := g.feed(ev); ok {
			return t, i, true
		}
	}
	return loopTrip{}, len(events), false
}

// The reported loop: a few short sentences over and over, a word changing
// between rounds.
func loopingPieces(rounds int) []string {
	words := []string{"Now.", "Writing.", "Go.", "Let me write.", "OK.", "Producing.", "Here.", "Final.", "Produce."}
	var out []string
	for i := range rounds {
		out = append(out, words[i%len(words)]+" ")
		if i%7 == 0 {
			out = append(out, "\n")
		}
	}
	return out
}

func TestLoopGuardTripsOnLoops(t *testing.T) {
	for _, api := range protocol.AllAPIs {
		for _, reasoning := range []bool{true, false} {
			g := &loopGuard{}
			trip, at, ok := feedAll(g, deltas(api, reasoning, loopingPieces(2000)))
			if !ok {
				t.Fatalf("%s reasoning=%v: the loop was not seen", api, reasoning)
			}
			if trip.reasoning != reasoning || at > 400 || len(trip.units) > loopDistinct || !strings.Contains(trip.String(), "loop") {
				t.Errorf("%s reasoning=%v: trip %+v at %d", api, reasoning, trip, at)
			}
		}
	}
	// A loop with no break in it.
	g := &loopGuard{}
	if trip, _, ok := feedAll(g, deltas(protocol.APIChat, false, []string{strings.Repeat("mcp ", 3000)})); !ok || !trip.runOn {
		t.Fatalf("run-on loop: %+v %v", trip, ok)
	}
}

func TestLoopGuardLetsWritingBy(t *testing.T) {
	var prose, code, table []string
	for i := range 1500 {
		prose = append(prose, fmt.Sprintf("Step %d looks at the file number %c%c and its owner. ", i, 'a'+i%26, 'a'+i/26%26))
		code = append(code, fmt.Sprintf("\tx%d := compute(y%d, %d)\n}\n", i, i, i*7))
		table = append(table, fmt.Sprintf("| row %d | %d | %x |\n", i, i*i, i*31))
	}
	for name, pieces := range map[string][]string{"prose": prose, "code": code, "table": table, "braces": strings.Split(strings.Repeat("}\n", 2000), "\n")} {
		g := &loopGuard{}
		if trip, _, ok := feedAll(g, deltas(protocol.APIResponses, false, pieces)); ok {
			t.Errorf("%s was taken for a loop: %+v", name, trip)
		}
	}
	// Repeated lines among many different ones are no loop either.
	var mixed []string
	for i := range 1000 {
		mixed = append(mixed, "Running the tests again.\n", fmt.Sprintf("Test case %c%c passed.\n", 'a'+i%26, 'a'+i/26%26))
	}
	g := &loopGuard{}
	if trip, _, ok := feedAll(g, deltas(protocol.APIMessages, true, mixed)); ok {
		t.Errorf("mixed lines were taken for a loop: %+v", trip)
	}
}

func TestEventWordsIgnoresOtherEvents(t *testing.T) {
	for _, p := range []string{
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"a\":"}}`,
		`{"type":"response.function_call_arguments.delta","delta":"{\"path\":"}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"x"}}]}}]}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
		`not json delta`,
	} {
		if th, tx := eventWords([]byte(p)); th != "" || tx != "" {
			t.Errorf("%s: got %q %q", p, th, tx)
		}
	}
}

func TestStreamCloserEndsEachAPIAsALengthStop(t *testing.T) {
	ev := func(s string) []byte { return []byte(s) }
	t.Run("chat", func(t *testing.T) {
		c := streamCloser{api: protocol.APIChat}
		c.observe(ev(`{"id":"c1","object":"chat.completion.chunk","created":5,"model":"m","choices":[{"index":0,"delta":{"content":"x"}}]}`))
		out := c.closing()
		var v struct {
			ID      string `json:"id"`
			Model   string `json:"model"`
			Choices []struct {
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
		}
		if len(out) != 1 || json.Unmarshal(out[0], &v) != nil || v.ID != "c1" || v.Model != "m" || v.Choices[0].FinishReason != "length" {
			t.Fatalf("got %s", out)
		}
	})
	t.Run("messages", func(t *testing.T) {
		c := streamCloser{api: protocol.APIMessages}
		for _, s := range []string{
			`{"type":"message_start","message":{"id":"msg_1"}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"text"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"x"}}`,
		} {
			c.observe(ev(s))
		}
		out := c.closing()
		want := []string{"content_block_stop", "message_delta", "message_stop"}
		if len(out) != len(want) {
			t.Fatalf("got %s", out)
		}
		for i, w := range want {
			if eventTypeOf(out[i]) != w {
				t.Fatalf("event %d: %s, want %s", i, out[i], w)
			}
		}
		if !strings.Contains(string(out[0]), `"index":1`) || !strings.Contains(string(out[1]), `"stop_reason":"max_tokens"`) {
			t.Fatalf("got %s", out)
		}
	})
	t.Run("responses", func(t *testing.T) {
		c := streamCloser{api: protocol.APIResponses}
		for _, s := range []string{
			`{"type":"response.created","response":{"id":"resp_1","object":"response","model":"m","status":"in_progress"}}`,
			`{"type":"response.output_item.added","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}`,
			`{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"thinking"}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[{"type":"summary_text","text":"thinking"}]}}`,
			`{"type":"response.output_item.added","output_index":1,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}`,
			`{"type":"response.output_text.delta","output_index":1,"delta":"Now. "}`,
			`{"type":"response.output_text.delta","output_index":1,"delta":"Go."}`,
			`{"type":"response.output_item.added","output_index":2,"item":{"id":"fc_1","type":"function_call","name":"shell"}}`,
		} {
			c.observe(ev(s))
		}
		out := c.closing()
		if len(out) != 2 || eventTypeOf(out[0]) != "response.output_item.done" || eventTypeOf(out[1]) != "response.incomplete" {
			t.Fatalf("got %s", out)
		}
		var done struct {
			Response struct {
				ID                string `json:"id"`
				Status            string `json:"status"`
				IncompleteDetails struct {
					Reason string `json:"reason"`
				} `json:"incomplete_details"`
				Output []struct {
					Type    string `json:"type"`
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"output"`
			} `json:"response"`
		}
		if err := json.Unmarshal(out[1], &done); err != nil {
			t.Fatal(err)
		}
		r := done.Response
		if r.ID != "resp_1" || r.Status != "incomplete" || r.IncompleteDetails.Reason != "max_output_tokens" || len(r.Output) != 2 ||
			r.Output[1].Type != "message" || r.Output[1].Content[0].Text != "Now. Go." {
			t.Fatalf("got %s", out[1])
		}
	})
}

func TestStreamGuardCutsAfterDelivering(t *testing.T) {
	g := newStreamGuard(protocol.APIChat, false)
	var got int
	deliver := g.wrap(func([]byte) error { got++; return nil })
	var err error
	events := deltas(protocol.APIChat, false, loopingPieces(2000))
	for _, ev := range events {
		if err = deliver(ev); err != nil {
			break
		}
	}
	if !g.cut(err) || got == 0 || got == len(events) {
		t.Fatalf("cut=%v after %d of %d events (err %v)", g.cut(err), got, len(events), err)
	}
	var none *streamGuard
	if none.cut(err) {
		t.Fatal("a guard that is off cuts nothing")
	}
}

func eventTypeOf(p []byte) string {
	var ev struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(p, &ev)
	return ev.Type
}

// Codex takes response.incomplete for a broken stream and asks again, so a
// cut answer reaches it as response.completed with the response marked
// incomplete.
func TestCutAnswerEndsCompletedForCodex(t *testing.T) {
	c := streamCloser{api: protocol.APIResponses, codex: true}
	c.observe([]byte(`{"type":"response.created","response":{"id":"resp_1","object":"response","status":"in_progress"}}`))
	c.observe([]byte(`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_1","type":"message","role":"assistant","content":[]}}`))
	c.observe([]byte(`{"type":"response.output_text.delta","output_index":0,"delta":"again"}`))
	out := c.closing()
	last := out[len(out)-1]
	if eventTypeOf(last) != "response.completed" || !strings.Contains(string(last), `"status":"incomplete"`) || !strings.Contains(string(last), `"reason":"max_output_tokens"`) {
		t.Fatalf("got %s", out)
	}
}
