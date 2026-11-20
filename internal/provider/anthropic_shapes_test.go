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

func refusalBody(t *testing.T, msg string) string {
	b, err := json.Marshal(map[string]any{"type": "error", "error": map[string]any{"type": "invalid_request_error", "message": msg}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Claude Code 2.1.326 asks claude-sonnet-5-5 for display "updates", which it
// refuses: the request goes again at once without it, and the model's later
// requests go without asking; "summarized" still goes, and another model is
// sent what it was asked.
func TestRefusedThinkingDisplayIsLearnt(t *testing.T) {
	refused := refusalBody(t, "thinking.adaptive.display: Input should be 'summarized', 'omitted'")
	ok := scripted{Status: 200, Body: strings.Join(messagesOK, "\n")}
	srv, sent := scriptedServer(t, scripted{Status: 400, Body: refused}, ok, ok, ok, ok)
	up := &anthropicUpstream{base: srv.URL, header: http.Header{}}
	ask := func(model, display string) {
		t.Helper()
		body := `{"model":"` + model + `","max_tokens":100,"stream":true,"thinking":{"type":"adaptive","display":"` + display + `"},"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hi"}]}`
		if err := up.Stream(context.Background(), &node.Request{API: protocol.APIMessages, Body: []byte(body)}, discard); err != nil {
			t.Fatal(err)
		}
	}
	display := func(i int) any {
		th, _ := (*sent)[i]["thinking"].(map[string]any)
		return th["display"]
	}
	ask("claude-sonnet-5-5", "updates")
	if len(*sent) != 2 || display(0) != "updates" || display(1) != nil {
		t.Fatalf("first request: %s", asJSON(t, *sent))
	}
	ask("claude-sonnet-5-5", "updates")
	ask("claude-sonnet-5-5", "summarized")
	ask("claude-opus-5-5", "updates")
	if len(*sent) != 5 || display(2) != nil || display(3) != "summarized" || display(4) != "updates" {
		t.Fatalf("later requests: %s", asJSON(t, (*sent)[2:]))
	}
}

// An endpoint that takes no display at all, or no per-turn effort, is sent
// neither once it refused them.
func TestRefusedDisplayAndTurnEffortAreLeftOut(t *testing.T) {
	ok := scripted{Status: 200, Body: strings.Join(messagesOK, "\n")}
	srv, sent := scriptedServer(t,
		scripted{Status: 400, Body: refusalBody(t, "thinking.adaptive.display: Extra inputs are not permitted")},
		scripted{Status: 400, Body: refusalBody(t, "messages.1.output_config: Extra inputs are not permitted")},
		ok)
	up := &anthropicUpstream{base: srv.URL, header: http.Header{}}
	body := `{"model":"kimi-k3","max_tokens":100,"stream":true,"thinking":{"type":"adaptive","display":"summarized"},
		"messages":[{"role":"user","content":"hi"},{"role":"system","content":"","output_config":{"effort":"low"}},{"role":"user","content":"more"}]}`
	if err := up.Stream(context.Background(), &node.Request{API: protocol.APIMessages, Body: []byte(body)}, discard); err != nil {
		t.Fatal(err)
	}
	if len(*sent) != 3 {
		t.Fatalf("%d requests", len(*sent))
	}
	last := asJSON(t, (*sent)[2])
	if strings.Contains(last, "display") || strings.Contains(last, "output_config") || strings.Contains(last, `"role":"system"`) || !strings.Contains(last, `"more"`) {
		t.Fatalf("last request: %s", last)
	}
}

// Thinking turned off goes in the form each Claude takes, before any
// refusal; a name the rules do not know is answered by the refusal that
// asks for between_tools.
func TestThinkingOffAsTheModelTakesIt(t *testing.T) {
	ok := scripted{Status: 200, Body: strings.Join(messagesOK, "\n")}
	cases := []struct {
		model, thinking, effort string
		want                    string // thinking sent, "" for none
		wantEffort              string
	}{
		{"claude-sonnet-5-5", `{"type":"disabled"}`, "", `{"type":"between_tools"}`, ""},
		{"claude-sonnet-5-5", `{"type":"disabled","display":"updates"}`, "max", `{"type":"between_tools"}`, "high"},
		{"claude-opus-5-5", `{"type":"disabled"}`, "", "", "low"},
		{"claude-fable-5-2", `{"type":"between_tools"}`, "medium", "", "medium"},
		{"claude-haiku-5-5", `{"type":"disabled"}`, "xhigh", `{"type":"disabled"}`, "high"},
		{"claude-haiku-5-5", `{"type":"disabled"}`, "high", `{"type":"disabled"}`, "high"},
		{"claude-sonnet-4-6", `{"type":"between_tools"}`, "", `{"type":"disabled"}`, ""},
		{"claude-opus-4-8", `{"type":"disabled"}`, "", `{"type":"disabled"}`, ""},
	}
	for _, c := range cases {
		srv, sent := scriptedServer(t, ok)
		up := &anthropicUpstream{base: srv.URL, header: http.Header{}}
		oc := ""
		if c.effort != "" {
			oc = `"output_config":{"effort":"` + c.effort + `"},`
		}
		body := `{"model":"` + c.model + `","max_tokens":64,"stream":true,` + oc + `"thinking":` + c.thinking + `,"messages":[{"role":"user","content":"hi"}]}`
		if err := up.Stream(context.Background(), &node.Request{API: protocol.APIMessages, Body: []byte(body)}, discard); err != nil {
			t.Fatalf("%s: %v", c.model, err)
		}
		got := (*sent)[0]
		th := ""
		if got["thinking"] != nil {
			th = asJSON(t, got["thinking"])
		}
		effort := ""
		if oc, _ := got["output_config"].(map[string]any); oc != nil {
			effort, _ = oc["effort"].(string)
		}
		if th != c.want || effort != c.wantEffort {
			t.Errorf("%s %s effort %q: sent thinking %s effort %q", c.model, c.thinking, c.effort, th, effort)
		}
		if c.want == "" && number(got["max_tokens"]) != 64+2048 {
			t.Errorf("%s: max_tokens %v", c.model, got["max_tokens"])
		}
	}
	// A relay's name for Sonnet 5.5 that the rules cannot read.
	srv, sent := scriptedServer(t, scripted{Status: 400, Body: refusalBody(t, `To turn thinking off on this model, send "thinking": {"type": "between_tools"} instead of {"type": "disabled"}.`)}, ok)
	up := &anthropicUpstream{base: srv.URL, header: http.Header{}}
	body := `{"model":"relay-s55","max_tokens":64,"stream":true,"thinking":{"type":"disabled"},"messages":[{"role":"user","content":"hi"}]}`
	if err := up.Stream(context.Background(), &node.Request{API: protocol.APIMessages, Body: []byte(body)}, discard); err != nil {
		t.Fatal(err)
	}
	if len(*sent) != 2 || asJSON(t, (*sent)[1]["thinking"]) != `{"type":"between_tools"}` {
		t.Fatalf("sent %s", asJSON(t, *sent))
	}
}
