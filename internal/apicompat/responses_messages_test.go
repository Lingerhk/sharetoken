package apicompat

import (
	"encoding/json"
	"strings"
	"testing"
)

// respMsgs returns the messages of a converted Messages request.
func respMsgs(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, m := range body["messages"].([]any) {
		out = append(out, m.(map[string]any))
	}
	return out
}

func respBlocks(m map[string]any) []map[string]any {
	var out []map[string]any
	for _, b := range m["content"].([]any) {
		out = append(out, b.(map[string]any))
	}
	return out
}

// respCheckMessagesSendable checks what Anthropic requires of messages: roles
// alternate, no empty content or blank text, every tool_result answers a
// tool_use of the message before, every tool_use is answered in the next.
func respCheckMessagesSendable(t *testing.T, msgs []map[string]any) {
	t.Helper()
	for i, m := range msgs {
		if i > 0 && msgs[i-1]["role"] == m["role"] {
			t.Fatalf("messages %d and %d are both %v", i-1, i, m["role"])
		}
		blocks := respBlocks(m)
		if len(blocks) == 0 {
			t.Fatalf("message %d is empty", i)
		}
		for _, b := range blocks {
			switch b["type"] {
			case "text":
				if strings.TrimSpace(b["text"].(string)) == "" {
					t.Fatalf("message %d has a blank text block", i)
				}
			case "tool_result":
				if i == 0 || !respHasBlock(msgs[i-1], "tool_use", "id", b["tool_use_id"]) {
					t.Fatalf("tool_result %v has no tool_use before it", b["tool_use_id"])
				}
			case "tool_use":
				if i+1 >= len(msgs) || !respHasBlock(msgs[i+1], "tool_result", "tool_use_id", b["id"]) {
					t.Fatalf("tool_use %v is not answered next", b["id"])
				}
			case "image", "document", "thinking", "redacted_thinking":
			default:
				t.Fatalf("message %d has a block Anthropic does not take: %v", i, b["type"])
			}
		}
	}
}

func respHasBlock(m map[string]any, typ, key string, val any) bool {
	for _, b := range respBlocks(m) {
		if b["type"] == typ && b[key] == val {
			return true
		}
	}
	return false
}

func respToMsgs(t *testing.T, input string, extra string) (map[string]any, []map[string]any) {
	t.Helper()
	body := `{"model":"claude-x","stream":true,"max_output_tokens":4000,"input":` + input + extra + `}`
	c := respConvert(t, Messages, body, Options{})
	m := respBody(t, c)
	msgs := respMsgs(t, m)
	respCheckMessagesSendable(t, msgs)
	return m, msgs
}

func TestResponsesToMessagesRequestBasics(t *testing.T) {
	m, msgs := respToMsgs(t, `[
		{"type":"message","role":"developer","content":[{"type":"input_text","text":"dev rules"}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},
		{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi there"}]},
		{"type":"message","role":"developer","content":"model switched"},
		{"type":"message","role":"user","content":"next"}
	]`, `,"instructions":"be brief","temperature":0.5,"prompt_cache_key":"k","client_metadata":{"a":"b"},"include":["reasoning.encrypted_content"],"store":false`)
	if m["model"] != "claude-x" || m["stream"] != true || respNum(m, "max_tokens") != 4000 || m["temperature"] != 0.5 {
		t.Errorf("top level: %v", m)
	}
	for _, k := range []string{"prompt_cache_key", "client_metadata", "include", "store", "input", "instructions", "max_output_tokens"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s leaked into the Messages request", k)
		}
	}
	sys := m["system"].([]any)
	if len(sys) != 2 || sys[0].(map[string]any)["text"] != "be brief" || sys[1].(map[string]any)["text"] != "dev rules" {
		t.Errorf("system: %v", sys)
	}
	if sys[1].(map[string]any)["cache_control"] == nil {
		t.Errorf("the system prompt should end in a cache breakpoint")
	}
	if len(msgs) != 3 {
		t.Fatalf("messages: %v", msgs)
	}
	last := respBlocks(msgs[2])
	if last[0]["text"] != "model switched" || last[1]["text"] != "next" {
		t.Errorf("a later developer message stays in place as user text: %v", last)
	}
	if last[1]["cache_control"] == nil {
		t.Errorf("the newest block should carry a cache breakpoint")
	}
}

func TestResponsesToMessagesStringInputAndDefaults(t *testing.T) {
	c := respConvert(t, Messages, `{"model":"m","input":"just text","stream":true}`, Options{})
	m := respBody(t, c)
	msgs := respMsgs(t, m)
	if len(msgs) != 1 || msgs[0]["role"] != "user" || respBlocks(msgs[0])[0]["text"] != "just text" {
		t.Fatalf("messages: %v", msgs)
	}
	if respNum(m, "max_tokens") != respDefaultMaxTokens {
		t.Errorf("max_tokens defaults to %d: %v", respDefaultMaxTokens, m["max_tokens"])
	}
	if _, err := Convert(Responses, Messages, []byte(`{"model":"m","input":[]}`), Options{}); err == nil {
		t.Errorf("a request with nothing to send must fail")
	}
	if _, err := Convert(Responses, Messages, []byte(`{"model":`), Options{}); err == nil {
		t.Errorf("bad JSON must fail")
	}
}

func TestResponsesToMessagesContentParts(t *testing.T) {
	_, msgs := respToMsgs(t, `[
		{"type":"message","role":"user","content":[
			{"type":"input_text","text":"look"},
			{"type":"input_image","image_url":"data:image/png;base64,iVBOR"},
			{"type":"input_image","image_url":"https://example.com/a.png"},
			{"type":"input_image","file_id":"file_1"},
			{"type":"input_file","filename":"a.pdf","file_data":"data:application/pdf;base64,JVBERi0="},
			{"type":"input_file","file_id":"file_2"},
			{"type":"reasoning_text","text":"drop me"}
		]},
		{"type":"message","role":"user","content":[{"type":"input_file","file_id":"only_id"}]},
		{"type":"message","role":"assistant","content":[{"type":"refusal","refusal":"I can't"}]},
		{"type":"message","role":"user","content":"ok"}
	]`, "")
	b := respBlocks(msgs[0])
	if len(b) != 4 {
		t.Fatalf("user blocks: %v", b)
	}
	if b[1]["type"] != "image" || respJSON(t, b[1]["source"]) != `{"data":"iVBOR","media_type":"image/png","type":"base64"}` {
		t.Errorf("data image: %v", b[1])
	}
	if respJSON(t, b[2]["source"]) != `{"type":"url","url":"https://example.com/a.png"}` {
		t.Errorf("url image: %v", b[2])
	}
	if b[3]["type"] != "document" || b[3]["title"] != "a.pdf" || b[3]["source"].(map[string]any)["media_type"] != "application/pdf" {
		t.Errorf("pdf document: %v", b[3])
	}
	if strings.Contains(respJSON(t, msgs), "drop me") {
		t.Errorf("unknown parts must be left out")
	}
	if len(msgs) != 3 || respBlocks(msgs[1])[0]["text"] != "I can't" {
		t.Errorf("a message with only unusable parts is dropped, a refusal kept as text: %v", msgs)
	}
}

// Ported from sub2api's tool pairing and Claude Code chain tests.
func TestResponsesToMessagesToolPairing(t *testing.T) {
	tests := []struct {
		name  string
		input string
		check func(t *testing.T, msgs []map[string]any)
	}{
		{"developer message between call and output", `[
			{"type":"message","role":"user","content":"do it"},
			{"type":"function_call","call_id":"call_A","name":"exec","arguments":"{}"},
			{"type":"message","role":"developer","content":"Approved command prefix saved"},
			{"type":"function_call_output","call_id":"call_A","output":"ok"}
		]`, func(t *testing.T, msgs []map[string]any) {
			if len(msgs) != 3 || !respHasBlock(msgs[2], "tool_result", "tool_use_id", "call_A") ||
				respBlocks(msgs[2])[1]["text"] != "Approved command prefix saved" {
				t.Errorf("%v", msgs)
			}
		}},
		{"parallel calls share one assistant message", `[
			{"type":"message","role":"user","content":"q"},
			{"type":"function_call","call_id":"c0","name":"exec","arguments":"{}"},
			{"type":"function_call","call_id":"c1","name":"exec","arguments":"{}"},
			{"type":"function_call_output","call_id":"c1","output":"b"},
			{"type":"function_call_output","call_id":"c0","output":"a"}
		]`, func(t *testing.T, msgs []map[string]any) {
			if len(msgs) != 3 || len(respBlocks(msgs[1])) != 2 {
				t.Fatalf("%v", msgs)
			}
			r := respBlocks(msgs[2])
			if r[0]["tool_use_id"] != "c0" || r[1]["tool_use_id"] != "c1" {
				t.Errorf("results follow call order: %v", r)
			}
		}},
		{"unanswered parallel call dropped", `[
			{"type":"message","role":"user","content":"q"},
			{"type":"function_call","call_id":"A","name":"exec","arguments":"{}"},
			{"type":"function_call","call_id":"B","name":"exec","arguments":"{}"},
			{"type":"function_call_output","call_id":"A","output":"oa"}
		]`, func(t *testing.T, msgs []map[string]any) {
			if strings.Contains(respJSON(t, msgs), `"B"`) {
				t.Errorf("%v", msgs)
			}
		}},
		{"orphan output dropped", `[
			{"type":"message","role":"user","content":"q"},
			{"type":"function_call_output","call_id":"ghost","output":"orphan"},
			{"type":"message","role":"assistant","content":"done"}
		]`, func(t *testing.T, msgs []map[string]any) {
			if strings.Contains(respJSON(t, msgs), "ghost") || len(msgs) != 2 {
				t.Errorf("%v", msgs)
			}
		}},
		{"dangling call dropped", `[
			{"type":"message","role":"user","content":"q"},
			{"type":"function_call","call_id":"A","name":"exec","arguments":"{}"}
		]`, func(t *testing.T, msgs []map[string]any) {
			if len(msgs) != 1 {
				t.Errorf("%v", msgs)
			}
		}},
		{"sequential rounds stay separate", `[
			{"type":"message","role":"user","content":"A then B"},
			{"type":"message","role":"assistant","content":"running A"},
			{"type":"function_call","call_id":"a","name":"exec","arguments":"{\"cmd\":\"A\"}"},
			{"type":"function_call_output","call_id":"a","output":"A ok"},
			{"type":"function_call","call_id":"b","name":"exec","arguments":"{\"cmd\":\"B\"}"},
			{"type":"function_call_output","call_id":"b","output":""},
			{"type":"message","role":"assistant","content":"all done"}
		]`, func(t *testing.T, msgs []map[string]any) {
			if len(msgs) != 6 {
				t.Fatalf("%v", msgs)
			}
			first := respBlocks(msgs[1])
			if first[0]["text"] != "running A" || first[1]["type"] != "tool_use" ||
				respJSON(t, first[1]["input"]) != `{"cmd":"A"}` {
				t.Errorf("%v", first)
			}
			if respBlocks(msgs[4])[0]["content"] != "(empty)" {
				t.Errorf("an empty output is sent as (empty): %v", msgs[4])
			}
		}},
		{"truncated call and its output dropped", `[
			{"type":"message","role":"user","content":"q"},
			{"type":"function_call","call_id":"bad","name":"exec","arguments":"{\"cmd\":"},
			{"type":"function_call_output","call_id":"bad","output":"x"},
			{"type":"message","role":"user","content":"again"}
		]`, func(t *testing.T, msgs []map[string]any) {
			if strings.Contains(respJSON(t, msgs), "bad") {
				t.Errorf("%v", msgs)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, msgs := respToMsgs(t, tt.input, "")
			tt.check(t, msgs)
		})
	}
}

func TestResponsesToMessagesToolOutputs(t *testing.T) {
	_, msgs := respToMsgs(t, `[
		{"type":"message","role":"user","content":"look"},
		{"type":"function_call","call_id":"v1","name":"view_image","arguments":"{}"},
		{"type":"function_call","call_id":"v2","name":"view_image","arguments":"{}"},
		{"type":"function_call","call_id":"v3","name":"mcp","arguments":"{}"},
		{"type":"function_call_output","call_id":"v1","output":[{"type":"input_text","text":"image loaded"},{"type":"input_image","image_url":"data:image/png;base64,YQ=="}]},
		{"type":"function_call_output","call_id":"v2","output":"data:image/jpeg;base64,Zm9v"},
		{"type":"function_call_output","call_id":"v3","output":"{\"content\":[{\"type\":\"image\",\"data\":\"QUJD\",\"mimeType\":\"image/gif\"}]}"}
	]`, "")
	r := respBlocks(msgs[2])
	c1 := r[0]["content"].([]any)
	if len(c1) != 2 || c1[0].(map[string]any)["text"] != "image loaded" ||
		c1[1].(map[string]any)["source"].(map[string]any)["data"] != "YQ==" {
		t.Errorf("parts output: %v", c1)
	}
	c2 := r[1]["content"].([]any)
	if len(c2) != 1 || c2[0].(map[string]any)["source"].(map[string]any)["media_type"] != "image/jpeg" {
		t.Errorf("data URL output: %v", c2)
	}
	c3 := r[2]["content"].([]any)
	if len(c3) != 2 || !strings.Contains(c3[0].(map[string]any)["text"].(string), "[image]") ||
		c3[1].(map[string]any)["source"].(map[string]any)["media_type"] != "image/gif" {
		t.Errorf("MCP JSON output: %v", c3)
	}
}

func TestResponsesToMessagesToolsAndChoice(t *testing.T) {
	tools := `,"tools":[
		{"type":"function","name":"exec","description":"run","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}},"strict":false},
		{"type":"custom","name":"apply_patch","description":"Patch files. This is a FREEFORM tool.","format":{"type":"grammar","syntax":"lark","definition":"start: \"*** Begin Patch\""}},
		{"type":"web_search","external_web_access":true},
		{"type":"namespace","name":"mcp__github","description":"GitHub","tools":[{"type":"function","name":"get_issue","parameters":{"type":"object"}}]},
		{"type":"tool_search","execution":"client","description":"x"},
		{"type":"local_shell"}
	]`
	tests := []struct {
		choice string
		want   string
	}{
		{`"auto"`, `{"type":"auto"}`},
		{`"required"`, `{"type":"any"}`},
		{`"none"`, `{"type":"none"}`},
		{`{"type":"function","name":"exec"}`, `{"name":"exec","type":"tool"}`},
		{`{"type":"function","function":{"name":"exec"}}`, `{"name":"exec","type":"tool"}`},
		{`{"type":"custom","name":"apply_patch"}`, `{"name":"apply_patch","type":"tool"}`},
		{`{"type":"tool_search"}`, `{"name":"tool_search","type":"tool"}`},
		{`{"type":"web_search"}`, ``},
		{`{"type":"function","name":"missing"}`, ``},
	}
	for _, tt := range tests {
		m, _ := respToMsgs(t, `"hi"`, tools+`,"tool_choice":`+tt.choice)
		got := ""
		if m["tool_choice"] != nil {
			got = respJSON(t, m["tool_choice"])
		}
		if got != tt.want {
			t.Errorf("tool_choice %s → %s, want %s", tt.choice, got, tt.want)
		}
	}
	m, _ := respToMsgs(t, `"hi"`, tools+`,"parallel_tool_calls":false`)
	if respJSON(t, m["tool_choice"]) != `{"disable_parallel_tool_use":true,"type":"auto"}` {
		t.Errorf("parallel_tool_calls false: %v", m["tool_choice"])
	}
	list := m["tools"].([]any)
	var names []string
	for _, x := range list {
		tool := x.(map[string]any)
		names = append(names, tool["name"].(string))
		if tool["input_schema"].(map[string]any)["type"] != "object" {
			t.Errorf("tool %v without an object schema", tool["name"])
		}
	}
	if strings.Join(names, ",") != "exec,apply_patch,mcp__github__get_issue,tool_search,local_shell" {
		t.Fatalf("tools: %v", names)
	}
	patch := list[1].(map[string]any)
	if respJSON(t, patch["input_schema"].(map[string]any)["required"]) != `["input"]` ||
		!strings.Contains(patch["description"].(string), `start: "*** Begin Patch"`) {
		t.Errorf("custom tool: %v", patch)
	}
	if list[4].(map[string]any)["cache_control"] == nil {
		t.Errorf("the last tool should carry a cache breakpoint")
	}
}

func TestResponsesToMessagesThinking(t *testing.T) {
	tests := []struct {
		name, extra string
		thinking    string // budget, or "" for none
		effort      string
	}{
		{"no reasoning", ``, "", ""},
		{"minimal", `,"reasoning":{"effort":"minimal"}`, "", ""},
		{"low", `,"reasoning":{"effort":"low"}`, "", "low"},
		{"medium", `,"reasoning":{"effort":"medium","summary":"auto"}`, "4096", "medium"},
		{"high", `,"reasoning":{"effort":"high"}`, "10240", "high"},
		{"xhigh", `,"reasoning":{"effort":"xhigh"}`, "32768", "max"},
		{"forced tool disables thinking", `,"reasoning":{"effort":"high"},"tools":[{"type":"function","name":"f"}],"tool_choice":"required"`, "", "high"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"model":"m","input":"q","max_output_tokens":64000,"temperature":0.2` + tt.extra + `}`
			m := respBody(t, respConvert(t, Messages, body, Options{}))
			got := ""
			if th, ok := m["thinking"].(map[string]any); ok {
				got = respJSON(t, th["budget_tokens"])
				if th["type"] != "enabled" {
					t.Errorf("thinking %v", th)
				}
				if _, ok := m["temperature"]; ok {
					t.Errorf("temperature must go with thinking on")
				}
			} else if m["temperature"] != 0.2 {
				t.Errorf("temperature kept without thinking: %v", m["temperature"])
			}
			if got != tt.thinking {
				t.Errorf("thinking budget %q, want %q", got, tt.thinking)
			}
			e := ""
			if oc, ok := m["output_config"].(map[string]any); ok {
				e, _ = oc["effort"].(string)
			}
			if e != tt.effort {
				t.Errorf("effort %q, want %q", e, tt.effort)
			}
		})
	}
	// the budget stays below max_tokens
	m := respBody(t, respConvert(t, Messages, `{"model":"m","input":"q","max_output_tokens":5000,"reasoning":{"effort":"high"}}`, Options{}))
	if respNum(m, "thinking", "budget_tokens") != 2500 {
		t.Errorf("budget: %v", m["thinking"])
	}
}

func TestResponsesToMessagesThinkingRoundTrip(t *testing.T) {
	sig := respEncodeThinking(respThinkingBlock{Type: "thinking", Thinking: "let me see", Signature: "SIG"})
	red := respEncodeThinking(respThinkingBlock{Type: "redacted_thinking", Data: "REDACTED"})
	input := `[
		{"type":"message","role":"user","content":"q"},
		{"type":"reasoning","summary":[{"type":"summary_text","text":"let me see"}],"encrypted_content":"` + sig + `"},
		{"type":"reasoning","summary":[],"encrypted_content":"` + red + `"},
		{"type":"reasoning","summary":[{"type":"summary_text","text":"openai"}],"encrypted_content":"gAAAAopaque"},
		{"type":"function_call","call_id":"c1","name":"exec","arguments":"{}"},
		{"type":"function_call_output","call_id":"c1","output":"ok"}
	]`
	m, msgs := respToMsgs(t, input, `,"reasoning":{"effort":"medium"}`)
	b := respBlocks(msgs[1])
	if len(b) != 3 || b[0]["type"] != "thinking" || b[0]["thinking"] != "let me see" || b[0]["signature"] != "SIG" ||
		b[1]["type"] != "redacted_thinking" || b[1]["data"] != "REDACTED" || b[2]["type"] != "tool_use" {
		t.Fatalf("assistant turn: %v", b)
	}
	if strings.Contains(string(respJSON(t, m)), "gAAAA") || strings.Contains(respJSON(t, m), `"openai"`) {
		t.Errorf("foreign reasoning must not reach the upstream")
	}
	if m["thinking"] == nil {
		t.Errorf("thinking stays on: the tool turn starts with a thinking block")
	}

	// The same tool loop without a thinking block cannot have thinking on.
	m, _ = respToMsgs(t, `[
		{"type":"message","role":"user","content":"q"},
		{"type":"reasoning","summary":[{"type":"summary_text","text":"x"}],"encrypted_content":"gAAAA"},
		{"type":"function_call","call_id":"c1","name":"exec","arguments":"{}"},
		{"type":"function_call_output","call_id":"c1","output":"ok"}
	]`, `,"reasoning":{"effort":"medium"}`)
	if m["thinking"] != nil {
		t.Errorf("thinking must be off: %v", m["thinking"])
	}
	// A new user turn after the loop: thinking is fine.
	m, _ = respToMsgs(t, `[
		{"type":"message","role":"user","content":"q"},
		{"type":"function_call","call_id":"c1","name":"exec","arguments":"{}"},
		{"type":"function_call_output","call_id":"c1","output":"ok"},
		{"type":"message","role":"assistant","content":"done"},
		{"type":"message","role":"user","content":"next"}
	]`, `,"reasoning":{"effort":"medium"}`)
	if m["thinking"] == nil {
		t.Errorf("thinking should be on")
	}
}

func TestResponsesToMessagesClientToolHistory(t *testing.T) {
	_, msgs := respToMsgs(t, `[
		{"type":"message","role":"user","content":"go"},
		{"type":"custom_tool_call","id":"ctc_1","call_id":"p1","name":"apply_patch","input":"*** Begin Patch\n*** End Patch\n"},
		{"type":"custom_tool_call_output","call_id":"p1","output":"Done!"},
		{"type":"tool_search_call","call_id":"s1","execution":"client","arguments":{"query":"github"}},
		{"type":"tool_search_output","call_id":"s1","status":"completed","execution":"client","tools":[{"type":"namespace","name":"gh","tools":[{"type":"function","name":"list","parameters":{"type":"object"}}]}]},
		{"type":"function_call","call_id":"n1","namespace":"gh","name":"list","arguments":"{}"},
		{"type":"function_call_output","call_id":"n1","output":"[]"},
		{"type":"local_shell_call","call_id":"l1","status":"completed","action":{"type":"exec","command":["ls","-la"],"working_directory":"/tmp"}},
		{"type":"local_shell_call_output","call_id":"l1","output":"total 0"}
	]`, `,"tools":[{"type":"custom","name":"apply_patch"},{"type":"tool_search","execution":"client"},{"type":"local_shell"}]`)
	var uses []map[string]any
	for _, m := range msgs {
		for _, b := range respBlocks(m) {
			if b["type"] == "tool_use" {
				uses = append(uses, b)
			}
		}
	}
	want := []string{
		`{"id":"p1","input":{"input":"*** Begin Patch\n*** End Patch\n"},"name":"apply_patch","type":"tool_use"}`,
		`{"id":"s1","input":{"query":"github"},"name":"tool_search","type":"tool_use"}`,
		`{"id":"n1","input":{},"name":"gh__list","type":"tool_use"}`,
		`{"id":"l1","input":{"command":["ls","-la"],"workdir":"/tmp"},"name":"local_shell","type":"tool_use"}`,
	}
	if len(uses) != len(want) {
		t.Fatalf("tool uses: %v", uses)
	}
	for i := range want {
		b, _ := respMarshal(uses[i])
		if string(b) != want[i] {
			t.Errorf("tool_use %d:\n got %s\nwant %s", i, b, want[i])
		}
	}
	if !strings.Contains(respJSON(t, msgs), `"tool_use_id":"s1"`) {
		t.Errorf("the tool_search output answers its call")
	}
}

func TestResponsesToMessagesCompaction(t *testing.T) {
	enc := EncodeCompaction("we fixed the bug in main.go")
	_, msgs := respToMsgs(t, `[
		{"type":"compaction","encrypted_content":"`+enc+`"},
		{"type":"compaction_summary","encrypted_content":"gAAAAAopaque-from-openai"},
		{"type":"message","role":"user","content":"continue"}
	]`, "")
	b := respBlocks(msgs[0])
	if len(msgs) != 1 || len(b) != 2 || b[0]["text"] != SummaryMessage("we fixed the bug in main.go") || b[1]["text"] != "continue" {
		t.Fatalf("compaction: %v", msgs)
	}
	if strings.Contains(respJSON(t, msgs), "opaque") {
		t.Errorf("an opaque compaction item must be dropped")
	}
}

func TestResponsesToMessagesUnknownItems(t *testing.T) {
	_, msgs := respToMsgs(t, `[
		{"type":"message","role":"user","content":"hi"},
		{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"x"}},
		{"type":"some_future_item","content":[{"type":"input_text","text":"keep me"},{"type":"reasoning_text","text":"drop me"}]},
		{"type":"item_reference","id":"msg_1"},
		{"type":"additional_tools","tools":[{"type":"function","name":"extra"}]}
	]`, "")
	s := respJSON(t, msgs)
	if !strings.Contains(s, "keep me") || strings.Contains(s, "drop me") || strings.Contains(s, "ws_1") {
		t.Errorf("%s", s)
	}
}

// --- stream ---

func respMsgConv(t *testing.T, tools string, opt Options) *Conversion {
	t.Helper()
	return respConvert(t, Messages, `{"model":"claude-x","input":"q","max_output_tokens":1000,"stream":true`+tools+`}`, opt)
}

const respMsgStart = `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-x","content":[],"stop_reason":null,"usage":{"input_tokens":100,"cache_read_input_tokens":40,"cache_creation_input_tokens":10,"output_tokens":1}}}`

func TestResponsesFromMessagesText(t *testing.T) {
	evs := respMustFeed(t, respMsgConv(t, "", Options{Model: "client-model"}).Translator,
		respMsgStart,
		`{"type":"ping"}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"second"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":20}}`,
		`{"type":"message_stop"}`,
	)
	want := []string{"response.created", "response.in_progress", "response.output_item.added",
		"response.content_part.added", "response.output_text.delta", "response.output_text.delta",
		"response.output_text.done", "response.content_part.done",
		"response.content_part.added", "response.output_text.delta", "response.output_text.done", "response.content_part.done",
		"response.output_item.done", "response.completed"}
	if strings.Join(respTypes(evs), ",") != strings.Join(want, ",") {
		t.Fatalf("events:\n%v", respTypes(evs))
	}
	parts := respFind(evs, "response.content_part.added")
	if respNum(parts[0], "content_index") != 0 || respNum(parts[1], "content_index") != 1 {
		t.Errorf("each text block is its own content part")
	}
	if respFind(evs, "response.output_text.done")[0]["text"] != "Hello" {
		t.Errorf("done carries the full text")
	}
	r := respTerminal(evs)
	if r["model"] != "client-model" || r["status"] != "completed" {
		t.Errorf("response: %v", r)
	}
	out := respOutput(evs)
	if respJSON(t, out[0]["content"]) != `[{"annotations":[],"logprobs":[],"text":"Hello","type":"output_text"},{"annotations":[],"logprobs":[],"text":"second","type":"output_text"}]` {
		t.Errorf("output: %v", out)
	}
	u := respUsageOf(evs)
	if respNum(u, "input_tokens") != 150 || respNum(u, "input_tokens_details", "cached_tokens") != 40 ||
		respNum(u, "input_tokens_details", "cache_creation_tokens") != 10 || respNum(u, "cache_creation_input_tokens") != 10 ||
		respNum(u, "output_tokens") != 20 || respNum(u, "total_tokens") != 170 {
		t.Errorf("usage: %v", u)
	}
}

func TestResponsesFromMessagesThinking(t *testing.T) {
	evs := respMustFeed(t, respMsgConv(t, "", Options{}).Translator,
		respMsgStart,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me "}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"think."}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"SIG1"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"ENCRYPTED"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"signature_delta","signature":"SIG2"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"content_block_start","index":3,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":3,"delta":{"type":"text_delta","text":"Answer"}}`,
		`{"type":"content_block_stop","index":3}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":50}}`,
		`{"type":"message_stop"}`,
	)
	out := respOutput(evs)
	if len(out) != 4 || out[0]["type"] != "reasoning" || out[1]["type"] != "reasoning" || out[2]["type"] != "reasoning" || out[3]["type"] != "message" {
		t.Fatalf("output: %v", out)
	}
	if respJSON(t, out[0]["summary"]) != `[{"text":"Let me think.","type":"summary_text"}]` {
		t.Errorf("summary: %v", out[0]["summary"])
	}
	for i, want := range []respThinkingBlock{
		{Type: "thinking", Thinking: "Let me think.", Signature: "SIG1"},
		{Type: "redacted_thinking", Data: "ENCRYPTED"},
		{Type: "thinking", Signature: "SIG2"},
	} {
		got, ok := respDecodeThinking(out[i]["encrypted_content"].(string))
		if !ok || got != want {
			t.Errorf("reasoning %d carries %+v, want %+v", i, got, want)
		}
	}
	// hidden reasoning: an item with an empty summary and no summary events
	if respJSON(t, out[1]["summary"]) != `[]` || respJSON(t, out[2]["summary"]) != `[]` {
		t.Errorf("redacted/empty thinking summaries: %v %v", out[1], out[2])
	}
	if n := len(respFind(evs, "response.reasoning_summary_part.added")); n != 1 {
		t.Errorf("summary parts: %d", n)
	}
	if d := respFind(evs, "response.reasoning_summary_text.done"); len(d) != 1 || d[0]["text"] != "Let me think." {
		t.Errorf("summary done: %v", d)
	}

	// The reasoning item goes back as the thinking block on the next turn.
	next := `{"model":"m","input":[{"type":"message","role":"user","content":"q"},` +
		respJSON(t, out[0]) + `,{"type":"function_call","call_id":"c","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"c","output":"x"}],"reasoning":{"effort":"high"},"max_output_tokens":30000}`
	m := respBody(t, respConvert(t, Messages, next, Options{}))
	first := respBlocks(respMsgs(t, m)[1])[0]
	if first["type"] != "thinking" || first["signature"] != "SIG1" || first["thinking"] != "Let me think." {
		t.Errorf("restored: %v", first)
	}
}

func TestResponsesFromMessagesInterleaved(t *testing.T) {
	// text → thinking → text → tool_use: every item closed, each its own index
	evs := respMustFeed(t, respMsgConv(t, "", Options{}).Translator,
		respMsgStart,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"a"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"thinking","thinking":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"thinking_delta","thinking":"r"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"b"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"toolu_1","name":"exec","input":{}}}`,
		`{"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
		`{"type":"content_block_stop","index":3}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`,
		`{"type":"message_stop"}`,
	)
	var kinds []string
	for _, o := range respOutput(evs) {
		kinds = append(kinds, o["type"].(string))
	}
	if strings.Join(kinds, ",") != "message,reasoning,message,function_call" {
		t.Errorf("output: %v", kinds)
	}
}

func TestResponsesFromMessagesToolUse(t *testing.T) {
	tools := `,"tools":[{"type":"function","name":"exec"},{"type":"custom","name":"apply_patch","format":{"type":"grammar"}},
		{"type":"namespace","name":"mcp__gh","tools":[{"type":"function","name":"get_issue"}]},{"type":"tool_search","execution":"client"},{"type":"local_shell"}]`
	evs := respMustFeed(t, respMsgConv(t, tools, Options{}).Translator,
		respMsgStart,
		// a plain function, arguments in pieces
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"exec","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"ls\"}"}}`,
		`{"type":"content_block_stop","index":0}`,
		// a custom tool
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_2","name":"apply_patch","input":{}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"input\": \"*** Begin Patch\\n*** End"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":" Patch\\n\"}"}}`,
		`{"type":"content_block_stop","index":1}`,
		// a namespace child, arguments whole on content_block_start
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_3","name":"mcp__gh__get_issue","input":{"n":7}}}`,
		`{"type":"content_block_stop","index":2}`,
		// tool_search
		`{"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"toolu_4","name":"tool_search","input":{}}}`,
		`{"type":"content_block_delta","index":3,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"github\"}"}}`,
		`{"type":"content_block_stop","index":3}`,
		// local_shell
		`{"type":"content_block_start","index":4,"content_block":{"type":"tool_use","id":"toolu_5","name":"local_shell","input":{}}}`,
		`{"type":"content_block_delta","index":4,"delta":{"type":"input_json_delta","partial_json":"{\"command\":[\"ls\"],\"workdir\":\"/w\",\"timeout_ms\":500}"}}`,
		`{"type":"content_block_stop","index":4}`,
		// a function with no arguments
		`{"type":"content_block_start","index":5,"content_block":{"type":"tool_use","id":"toolu_6","name":"exec","input":{}}}`,
		`{"type":"content_block_stop","index":5}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":30}}`,
		`{"type":"message_stop"}`,
	)
	out := respOutput(evs)
	want := []string{
		`{"arguments":"{\"cmd\":\"ls\"}","call_id":"toolu_1","name":"exec","status":"completed","type":"function_call"}`,
		`{"call_id":"toolu_2","input":"*** Begin Patch\n*** End Patch\n","name":"apply_patch","status":"completed","type":"custom_tool_call"}`,
		`{"arguments":"{\"n\":7}","call_id":"toolu_3","name":"get_issue","namespace":"mcp__gh","status":"completed","type":"function_call"}`,
		`{"arguments":{"query":"github"},"call_id":"toolu_4","execution":"client","status":"completed","type":"tool_search_call"}`,
		`{"action":{"command":["ls"],"timeout_ms":500,"type":"exec","working_directory":"/w"},"call_id":"toolu_5","status":"completed","type":"local_shell_call"}`,
		`{"arguments":"{}","call_id":"toolu_6","name":"exec","status":"completed","type":"function_call"}`,
	}
	prefixes := []string{"fc_", "ctc_", "fc_", "tsc_", "lsh_", "fc_"}
	if len(out) != len(want) {
		t.Fatalf("output: %v", out)
	}
	for i := range want {
		id := out[i]["id"].(string)
		if !strings.HasPrefix(id, prefixes[i]) {
			t.Errorf("item %d id %s, want prefix %s", i, id, prefixes[i])
		}
		delete(out[i], "id")
		if got, _ := respMarshal(out[i]); string(got) != want[i] {
			t.Errorf("item %d:\n got %s\nwant %s", i, got, want[i])
		}
	}
	// the arguments of plain functions stream, and done repeats their sum
	byItem := map[string]string{}
	for _, e := range respFind(evs, "response.function_call_arguments.delta") {
		byItem[e["item_id"].(string)] += e["delta"].(string)
	}
	for _, e := range respFind(evs, "response.function_call_arguments.done") {
		if byItem[e["item_id"].(string)] != e["arguments"] {
			t.Errorf("done %v does not match the deltas %q", e["arguments"], byItem[e["item_id"].(string)])
		}
	}
	if len(respFind(evs, "response.function_call_arguments.done")) != 3 {
		t.Errorf("only plain function calls get argument events")
	}
	cd := respFind(evs, "response.custom_tool_call_input.done")
	if len(cd) != 1 || cd[0]["input"] != "*** Begin Patch\n*** End Patch\n" ||
		respFind(evs, "response.custom_tool_call_input.delta")[0]["delta"] != "*** Begin Patch\n*** End Patch\n" {
		t.Errorf("custom input events: %v", cd)
	}
	for _, e := range respFind(evs, "response.output_item.added") {
		if e.item()["type"] == "custom_tool_call" && e.item()["name"] != "apply_patch" {
			t.Errorf("custom call announced as %v", e.item())
		}
	}
}

func TestResponsesFromMessagesStopReasons(t *testing.T) {
	for stop, want := range map[string]string{
		"end_turn": "", "tool_use": "", "stop_sequence": "", "pause_turn": "",
		"max_tokens": "max_output_tokens", "refusal": "content_filter", "model_context_window_exceeded": "max_output_tokens",
	} {
		evs := respMustFeed(t, respMsgConv(t, "", Options{}).Translator,
			respMsgStart,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"x"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"`+stop+`"},"usage":{"output_tokens":1}}`,
			`{"type":"message_stop"}`,
		)
		r := respTerminal(evs)
		got := ""
		if d, ok := r["incomplete_details"].(map[string]any); ok {
			got = d["reason"].(string)
		}
		if got != want {
			t.Errorf("%s → %q, want %q", stop, got, want)
		}
		if (want == "") != (evs[len(evs)-1].typ() == "response.completed") {
			t.Errorf("%s: terminal %s", stop, evs[len(evs)-1].typ())
		}
	}
}

func TestResponsesFromMessagesEndings(t *testing.T) {
	t.Run("error event", func(t *testing.T) {
		_, _, err := respFeed(t, respMsgConv(t, "", Options{}).Translator,
			respMsgStart, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
		if err == nil || !strings.Contains(err.Error(), "Overloaded") {
			t.Errorf("err %v", err)
		}
	})
	t.Run("nothing produced", func(t *testing.T) {
		_, _, err := respFeed(t, respMsgConv(t, "", Options{}).Translator)
		if err == nil {
			t.Errorf("want error")
		}
		_, _, err = respFeed(t, respMsgConv(t, "", Options{}).Translator, respMsgStart)
		if err == nil {
			t.Errorf("want error after a bare message_start")
		}
	})
	t.Run("cut short mid tool call", func(t *testing.T) {
		evs := respMustFeed(t, respMsgConv(t, "", Options{}).Translator,
			respMsgStart,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"x"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t","name":"f","input":{}}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"a\":1}"}}`,
		)
		if evs[len(evs)-1].typ() != "response.completed" || len(respOutput(evs)) != 2 {
			t.Errorf("%v", respTypes(evs))
		}
	})
	t.Run("empty answer", func(t *testing.T) {
		evs := respMustFeed(t, respMsgConv(t, "", Options{}).Translator,
			respMsgStart, `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":0}}`, `{"type":"message_stop"}`)
		if len(respOutput(evs)) != 0 {
			t.Errorf("%v", respOutput(evs))
		}
	})
	t.Run("events after message_stop are ignored", func(t *testing.T) {
		evs := respMustFeed(t, respMsgConv(t, "", Options{}).Translator,
			respMsgStart, `{"type":"message_stop"}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"late"}}`)
		if strings.Contains(respJSON(t, evs), "late") {
			t.Errorf("late event emitted")
		}
	})
	t.Run("content on message_start", func(t *testing.T) {
		evs := respMustFeed(t, respMsgConv(t, `,"tools":[{"type":"function","name":"f"}]`, Options{}).Translator,
			`{"type":"message_start","message":{"id":"m","model":"x","content":[{"type":"text","text":"whole"},{"type":"tool_use","id":"t1","name":"f","input":{"k":1}}],"usage":{"input_tokens":3}}}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":4}}`,
			`{"type":"message_stop"}`)
		out := respOutput(evs)
		if len(out) != 2 || out[0]["content"].([]any)[0].(map[string]any)["text"] != "whole" || out[1]["arguments"] != `{"k":1}` {
			t.Errorf("%v", out)
		}
		if respTerminal(evs)["model"] != "claude-x" {
			t.Errorf("the model defaults to the request's: %v", respTerminal(evs)["model"])
		}
	})
	t.Run("bad payload", func(t *testing.T) {
		_, _, err := respFeed(t, respMsgConv(t, "", Options{}).Translator, `{not json`)
		if err == nil {
			t.Errorf("want error")
		}
	})
}

func TestResponsesFromMessagesUnknownBlocksIgnored(t *testing.T) {
	evs := respMustFeed(t, respMsgConv(t, "", Options{}).Translator,
		respMsgStart,
		`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"q\":1}"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[]}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"citations_delta","citation":{}}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"ok"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"some_new_event"}`,
		`{"type":"message_stop"}`,
	)
	if out := respOutput(evs); len(out) != 1 || out[0]["type"] != "message" {
		t.Errorf("%v", out)
	}
}

func TestResponsesThinkingEnvelope(t *testing.T) {
	for _, b := range []respThinkingBlock{
		{Type: "thinking", Thinking: "t", Signature: "s"},
		{Type: "redacted_thinking", Data: "d"},
	} {
		got, ok := respDecodeThinking(respEncodeThinking(b))
		if !ok || got != b {
			t.Errorf("round trip %+v → %+v", b, got)
		}
	}
	for _, bad := range []string{"", "gAAAA", respThinkingPrefix + "!!", respEncodeThinking(respThinkingBlock{Type: "thinking", Thinking: "no sig"})} {
		if _, ok := respDecodeThinking(bad); ok {
			t.Errorf("%q decoded", bad)
		}
	}
}

// The 5.5 models think adaptively and refuse a thinking budget.
func TestResponsesEffortToAdaptiveThinking(t *testing.T) {
	for model, want := range map[string]string{"claude-haiku-5-5": `{"type":"adaptive"}`, "claude-sonnet-4-6": `"enabled"`} {
		conv, err := Convert(Responses, Messages, []byte(`{"model":"`+model+`","stream":true,"max_output_tokens":20000,"input":"hi","reasoning":{"effort":"high"}}`), Options{})
		if err != nil {
			t.Fatal(err)
		}
		var req map[string]any
		_ = json.Unmarshal(conv.Body, &req)
		th, _ := json.Marshal(req["thinking"])
		oc, _ := json.Marshal(req["output_config"])
		if !strings.Contains(string(th), want) || string(oc) != `{"effort":"high"}` {
			t.Errorf("%s: thinking %s output_config %s", model, th, oc)
		}
	}
}
