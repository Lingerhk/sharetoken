package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"sharetoken/internal/node"
	"sharetoken/internal/protocol"
)

const (
	callA   = `{"type":"tool_use","id":"toolu_a","name":"Read","input":{"path":"x"}}`
	callB   = `{"type":"tool_use","id":"toolu_b","name":"Read","input":{"path":"y"}}`
	answerA = `{"type":"tool_result","tool_use_id":"toolu_a","content":"A"}`
	answerB = `{"type":"tool_result","tool_use_id":"toolu_b","content":"B"}`
	missA   = `{"type":"tool_result","tool_use_id":"toolu_a","content":"[The result of this tool call is unavailable: the turn was interrupted.]","is_error":true}`
	missB   = `{"type":"tool_result","tool_use_id":"toolu_b","content":"[The result of this tool call is unavailable: the turn was interrupted.]","is_error":true}`
)

func TestPairedToolBlocks(t *testing.T) {
	asst := func(blocks ...string) string {
		return `{"role":"assistant","content":[` + strings.Join(blocks, ",") + `]}`
	}
	user := func(blocks ...string) string {
		return `{"role":"user","content":[` + strings.Join(blocks, ",") + `]}`
	}
	text := func(s string) string { return `{"type":"text","text":"` + s + `"}` }
	req := func(msgs ...string) string {
		return `{"model":"claude-x", "max_tokens" : 10,"messages": [` + strings.Join(msgs, ", ") + `] ,"stream":true}`
	}
	cases := []struct {
		name     string
		in, want string // want "" for the input as it came
	}{
		{"no tools", req(`{"role":"user","content":"hi"}`), ""},
		{"paired", req(user(text("go")), asst(text("ok"), callA, callB), user(answerB, answerA, text("next"))), ""},
		{"system message between", req(user(text("go")), asst(callA), `{"role":"system","content":"","output_config":{"effort":"low"}}`, user(answerA)), ""},
		{"one of two parallel calls unanswered",
			req(user(text("go")), asst(callA, callB), user(answerA, text("next"))),
			`{"model":"claude-x", "max_tokens" : 10,"messages": [` + user(text("go")) + `,` + asst(callA, callB) + `,` + user(answerA, missB, text("next")) + `] ,"stream":true}`},
		{"interrupted: the user wrote instead",
			req(asst(callA), `{"role":"user","content":"stop"}`),
			`{"model":"claude-x", "max_tokens" : 10,"messages": [` + asst(callA) + `,{"role":"user","content":[` + missA + `,` + text("stop") + `]}] ,"stream":true}`},
		{"two assistant turns in a row",
			req(asst(callA), asst(text("again"))),
			`{"model":"claude-x", "max_tokens" : 10,"messages": [` + asst(callA) + `,` + user(missA) + `,` + asst(text("again")) + `] ,"stream":true}`},
		{"the history ends at a call",
			req(user(text("go")), asst(callA)),
			`{"model":"claude-x", "max_tokens" : 10,"messages": [` + user(text("go")) + `,` + asst(callA) + `,` + user(missA) + `] ,"stream":true}`},
		{"a result for no call becomes its text",
			req(user(text("go")), asst(text("plain")), user(answerA, text("more"))),
			`{"model":"claude-x", "max_tokens" : 10,"messages": [` + user(text("go")) + `,` + asst(text("plain")) + `,` + user(text("A"), text("more")) + `] ,"stream":true}`},
		{"a second answer is left out, a late one moved first",
			req(asst(callA), user(text("note"), answerA, answerA)),
			`{"model":"claude-x", "max_tokens" : 10,"messages": [` + asst(callA) + `,` + user(answerA, text("note")) + `] ,"stream":true}`},
	}
	for _, c := range cases {
		got := string(pairedToolBlocks([]byte(c.in)))
		want := c.want
		if want == "" {
			want = c.in
		}
		if got != want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, want)
		}
		if !json.Valid([]byte(got)) {
			t.Errorf("%s: not JSON", c.name)
		}
	}
}

// The adapter sends a history Claude Code left unpaired with its missing
// result, without a refusal first.
func TestAnthropicSendsPairedToolBlocks(t *testing.T) {
	srv, sent := scriptedServer(t, scripted{Status: 200, Body: strings.Join(messagesOK, "\n")})
	up := &anthropicUpstream{base: srv.URL, header: http.Header{}}
	body := `{"model":"claude-sonnet-4-6","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"go"},` +
		`{"role":"assistant","content":[` + callA + `,` + callB + `]},{"role":"user","content":[` + answerA + `]}]}`
	if err := up.Stream(context.Background(), &node.Request{API: protocol.APIMessages, Body: []byte(body)}, discard); err != nil {
		t.Fatal(err)
	}
	if len(*sent) != 1 {
		t.Fatalf("%d requests", len(*sent))
	}
	msgs := list((*sent)[0]["messages"])
	last := asJSON(t, msgs[len(msgs)-1])
	if !strings.Contains(last, `"tool_use_id":"toolu_a"`) || !strings.Contains(last, `"tool_use_id":"toolu_b"`) || !strings.Contains(last, `"is_error":true`) {
		t.Fatalf("last message: %s", last)
	}
}
