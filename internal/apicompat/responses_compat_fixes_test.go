package apicompat

import (
	"encoding/json"
	"strings"
	"testing"
)

// The Responses API checks a json_schema strictly unless told not to; a chat
// client that said nothing meant chat's default, which is not strict.
func TestChatToResponsesSchemaStrictDefault(t *testing.T) {
	for _, tc := range []struct{ strict, want string }{
		{"", `"strict":false`},
		{`,"strict":false`, `"strict":false`},
		{`,"strict":true`, `"strict":true`},
	} {
		body := `{"model":"m","messages":[{"role":"user","content":"Hi"}],"response_format":{"type":"json_schema","json_schema":{"name":"a","schema":{"type":"object"}` + tc.strict + `}}}`
		req, _ := chatConvert(t, Responses, []byte(body), "")
		got := chatJSON(t, req["text"])
		if !strings.Contains(got, tc.want) {
			t.Errorf("strict %q: %s", tc.strict, got)
		}
	}
}

// An Anthropic output schema goes as a schema the Responses API does not
// check strictly: Anthropic needs neither all properties required nor
// additionalProperties:false.
func TestMessagesToResponsesSchemaNotStrict(t *testing.T) {
	body := `{"model":"gpt-5.4","max_tokens":100,"messages":[{"role":"user","content":"Hi"}],
		"output_config":{"format":{"type":"json_schema","schema":{"type":"object","properties":{"a":{"type":"string"}}}}}}`
	c, err := Convert(Messages, Responses, []byte(body), Options{})
	if err != nil {
		t.Fatal(err)
	}
	var req struct {
		Text struct {
			Format map[string]any `json:"format"`
		} `json:"text"`
	}
	if err := json.Unmarshal(c.Body, &req); err != nil {
		t.Fatal(err)
	}
	if req.Text.Format == nil {
		t.Fatalf("no format: %s", c.Body)
	}
	if strict, ok := req.Text.Format["strict"]; !ok || strict != false {
		t.Fatalf("format %v", req.Text.Format)
	}
}

func TestExternalContext(t *testing.T) {
	page := `{"page_id":"p1","title":"T"}`
	for _, tc := range []struct {
		name                       string
		typ, ns, callID, tool, out string
		want                       string // "" for not external context
	}{
		{"browser info", "function_call_output", "agent", "", "browser_info", "Chrome 1", "<external_browser_info>Chrome 1</external_browser_info>"},
		{"goal", "function_call_output", "agent", "", "goal_objective", "ship", "<external_goal_objective>ship</external_goal_objective>"},
		{"untrusted", "function_call_output", "agent", "", "untrusted_context", "x", "<external_untrusted_context>x</external_untrusted_context>"},
		{"open page", "function_call_output", "agent", "", "codex_apps_open_page", page, "<external_codex_apps_open_page>\n" + page + "\n</external_codex_apps_open_page>"},
		{"page part", "function_call_output", "agent", "", "codex_apps_selected_page_part_1_of_2", "Companion Page snapshot part 1 of 2:\n{\"page_id\":\"p", "<external_codex_apps_selected_page_part_1_of_2>\nCompanion Page snapshot part 1 of 2:\n{\"page_id\":\"p\n</external_codex_apps_selected_page_part_1_of_2>"},
		{"single part must be a snapshot", "function_call_output", "agent", "", "codex_apps_selected_page_part_1_of_1", "Companion Page snapshot part 1 of 1:\nnot json", ""},
		{"part header mismatch", "function_call_output", "agent", "", "codex_apps_selected_page_part_2_of_2", "Companion Page snapshot part 1 of 2:\nx", ""},
		{"part past total", "function_call_output", "agent", "", "codex_apps_selected_page_part_3_of_2", "Companion Page snapshot part 3 of 2:\nx", ""},
		{"page without page_id", "function_call_output", "agent", "", "codex_apps_open_page", `{"title":"T"}`, ""},
		{"with a call id", "function_call_output", "agent", "call_1", "browser_info", "x", ""},
		{"other namespace", "function_call_output", "", "", "browser_info", "x", ""},
		{"unknown name", "function_call_output", "agent", "", "other", "x", ""},
		{"empty", "function_call_output", "agent", "", "browser_info", "  ", ""},
		{"not an output", "function_call", "agent", "", "browser_info", "x", ""},
	} {
		got, ok := ExternalContext(tc.typ, tc.ns, tc.callID, tc.tool, tc.out)
		if ok != (tc.want != "") || got != tc.want {
			t.Errorf("%s: got %q, %v", tc.name, got, ok)
		}
	}
}

// Codex's external context reaches a converted request as a user message
// instead of being dropped as an output without a call.
func TestResponsesExternalContextBecomesUserMessage(t *testing.T) {
	items := respItemsOf(t, `{"model":"m","input":[
		{"type":"message","role":"user","content":"Hi"},
		{"type":"function_call_output","namespace":"agent","name":"goal_objective","output":"finish the report"},
		{"type":"function_call_output","namespace":"agent","name":"nothing_known","output":"dropped"}]}`)
	got := respDescribe(items)
	if want := "user:Hi | user:<external_goal_objective>finish the report</external_goal_objective>"; got != want {
		t.Fatalf("items %s", got)
	}
}
