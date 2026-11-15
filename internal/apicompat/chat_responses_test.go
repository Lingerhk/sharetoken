package apicompat

import (
	"encoding/json"
	"strings"
	"testing"
)

func chatResponsesInput(t *testing.T, req map[string]any) []map[string]any {
	t.Helper()
	raw, _ := json.Marshal(req["input"])
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatalf("input: %v", err)
	}
	return items
}

// The provider's request test: everything a chat request carries reaches
// the Responses request.
func TestChatToResponsesFullRequest(t *testing.T) {
	var full map[string]any
	_ = json.Unmarshal([]byte(chatFullRequest), &full)
	full["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["name"] = "python" // reserved by the Codex backend
	full["messages"].([]any)[2].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["name"] = "python"
	req, _ := chatConvert(t, Responses, []byte(chatJSON(t, full)), "")
	got := chatJSON(t, req)
	chatWant(t, got,
		`"instructions":"Be brief."`, `"store":false`, `"stream":true`, `"include":["reasoning.encrypted_content"]`,
		`{"image_url":"data:image/png;base64,QUJD","type":"input_image"}`,
		`{"content":[{"text":"Let me look.","type":"output_text"}],"role":"assistant","type":"message"}`,
		`{"arguments":"{\"q\":\"cat\"}","call_id":"call_1","name":"python__st","type":"function_call"}`,
		`{"call_id":"call_1","output":"a cat","type":"function_call_output"}`,
		`"name":"python__st"`, `"parameters":{`, `"strict":false`, `"tool_choice":"required"`,
		`"max_output_tokens":900`, `"temperature":0.4`, `"top_p":0.9`, `"model":"m-1"`)
	chatWantNot(t, got, `"stop"`, "stream_options", `"max_tokens"`, `"role":"system"`)
}

func TestChatToResponsesRequest(t *testing.T) {
	for _, tc := range []struct {
		name, model, rest string
		want, not         []string
	}{
		{name: "plain user text stays a string", rest: `"messages":[{"role":"user","content":"Hello"}]`,
			want: []string{`"input":[{"content":"Hello","role":"user","type":"message"}]`, `"instructions":""`}},
		{name: "system messages are the instructions, developer keeps its role",
			rest: `"messages":[{"role":"system","content":"A"},{"role":"system","content":[{"type":"text","text":"B"}]},{"role":"developer","content":"D"},{"role":"user","content":"Hi"}]`,
			want: []string{`"instructions":"A\n\nB"`, `{"content":"D","role":"developer","type":"message"}`}},
		{name: "assistant reasoning and text, a reasoning-only turn",
			rest: `"messages":[{"role":"user","content":"Check"},{"role":"assistant","content":"I will check.","reasoning_content":"Need to inspect.","tool_calls":[{"id":"call_1","type":"function","function":{"name":"bash","arguments":""}}]},{"role":"tool","tool_call_id":"call_1","content":"/tmp"},{"role":"assistant","reasoning_content":"It returned /tmp."},{"role":"user","content":"Continue"}]`,
			want: []string{`"text":"<thinking>Need to inspect.</thinking>\nI will check."`, `"arguments":"{}"`,
				`"text":"<thinking>It returned /tmp.</thinking>"`}},
		{name: "assistant parts are joined, thinking parts tagged",
			rest: `"messages":[{"role":"user","content":"Hi"},{"role":"assistant","content":[{"type":"thinking","thinking":"plan"},{"type":"text","text":"A"},{"type":"text","text":"B"}]}]`,
			want: []string{`"text":"<thinking>plan</thinking>AB"`}},
		{name: "tool output is its text, images dropped; empty output",
			rest: `"messages":[{"role":"user","content":"Use it"},{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"x","arguments":"{}"}},{"id":"c2","type":"function","function":{"name":"x","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":[{"type":"text","text":"w: 100"},{"type":"image_url","image_url":{"url":"data:image/png;base64,QQ"}},{"type":"text","text":"; h: 200"}]},{"role":"tool","tool_call_id":"c2","content":""}]`,
			want: []string{`"output":"w: 100; h: 200"`, `{"call_id":"c2","output":"(empty)","type":"function_call_output"}`}},
		{name: "images with detail, empty data URLs skipped, files",
			rest: `"messages":[{"role":"user","content":[{"type":"text","text":"Look"},{"type":"image_url","image_url":{"url":"https://x/a.png","detail":"high"}},{"type":"image_url","image_url":{"url":"data:image/png;base64,"}},{"type":"image_url","image_url":{"url":"data:image/png;base64,   "}},{"type":"image_url","image_url":"https://x/b.png"},{"type":"file","file":{"filename":"a.pdf","file_data":"data:application/pdf;base64,JVBE"}},{"type":"file","file":{"file_id":"file-1"}},{"type":"file","file":{}},{"type":"input_audio","input_audio":{"data":"x","format":"wav"}}]}]`,
			want: []string{`{"detail":"high","image_url":"https://x/a.png","type":"input_image"}`, `{"image_url":"https://x/b.png","type":"input_image"}`,
				`{"file_data":"data:application/pdf;base64,JVBE","filename":"a.pdf","type":"input_file"}`, `{"file_id":"file-1","type":"input_file"}`},
			not: []string{`base64,"`, "input_audio"}},
		{name: "reasoning effort asks for summaries", model: "gpt-5.4", rest: `"reasoning_effort":"high","messages":[{"role":"user","content":"Hi"}]`,
			want: []string{`"reasoning":{"effort":"high","summary":"auto"}`}},
		{name: "json_object", rest: `"response_format":{"type":"json_object"},"messages":[{"role":"user","content":"Hi"}]`,
			want: []string{`"text":{"format":{"type":"json_object"}}`}},
		{name: "json_schema is flattened", rest: `"response_format":{"type":"json_schema","json_schema":{"name":"answer","description":"d","schema":{"properties":{"ok":{"type":"boolean"}},"type":"object"},"strict":true}},"messages":[{"role":"user","content":"Hi"}]`,
			want: []string{`"text":{"format":{"description":"d","name":"answer","schema":{"properties":{"ok":{"type":"boolean"}},"type":"object"},"strict":true,"type":"json_schema"}}`}},
		{name: "json_schema without a name gets one", rest: `"response_format":{"type":"json_schema","json_schema":{"schema":{"type":"object"}}},"messages":[{"role":"user","content":"Hi"}]`,
			want: []string{`"name":"answer"`}},
		{name: "text response_format adds nothing", rest: `"response_format":{"type":"text"},"messages":[{"role":"user","content":"Hi"}]`,
			not: []string{`"text":`}},
		{name: "a reasoning model takes no sampling settings", model: "gpt-5.2", rest: `"temperature":0.7,"top_p":0.7,"messages":[{"role":"user","content":"Hi"}]`,
			not: []string{"temperature", "top_p"}},
		{name: "nor does an o model", model: "o3-mini", rest: `"temperature":0.7,"messages":[{"role":"user","content":"Hi"}]`,
			not: []string{"temperature"}},
		{name: "other models do", model: "gpt-4o", rest: `"temperature":0.7,"top_p":0.5,"messages":[{"role":"user","content":"Hi"}]`,
			want: []string{`"temperature":0.7`, `"top_p":0.5`}},
		{name: "fields Responses lacks are dropped", rest: `"seed":1,"presence_penalty":1,"frequency_penalty":1,"stop":"X","user":"u","messages":[{"role":"user","content":"Hi"}]`,
			not: []string{"seed", "penalty", `"stop"`, `"user":"u"`}},
		{name: "session_id is the cache key", rest: `"session_id":"conv-1","messages":[{"role":"user","content":"Hi"}]`,
			want: []string{`"prompt_cache_key":"conv-1"`}},
		{name: "max_completion_tokens and a floor", rest: `"max_tokens":1,"messages":[{"role":"user","content":"Hi"}]`,
			want: []string{`"max_output_tokens":16`}},
		{name: "the smaller output limit wins", rest: `"max_tokens":500,"max_completion_tokens":300,"messages":[{"role":"user","content":"Hi"}]`,
			want: []string{`"max_output_tokens":300`}},
		{name: "tools: strict kept, built-ins kept, nameless and unknown dropped, parallel_tool_calls",
			rest: `"parallel_tool_calls":false,"messages":[{"role":"user","content":"Hi"}],"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object"},"strict":true}},{"type":"web_search"},{"type":"code_execution"},{"type":"unsupported_builtin"},{"type":"function"},{"type":"x_search","allowed_x_handles":["xai"],"from_date":"2026-08-01","enable_image_understanding":true}],"tool_choice":{"type":"x_search"}`,
			want: []string{`{"name":"read","parameters":{"properties":{},"type":"object"},"strict":true,"type":"function"}`, `{"type":"web_search"}`, `{"type":"code_execution"}`,
				`{"allowed_x_handles":["xai"],"enable_image_understanding":true,"from_date":"2026-08-01","type":"x_search"}`, `"tool_choice":{"type":"x_search"}`, `"parallel_tool_calls":false`},
			not: []string{"unsupported_builtin"}},
		{name: "no tools, no tool_choice", rest: `"tool_choice":"auto","parallel_tool_calls":true,"messages":[{"role":"user","content":"Hi"}]`,
			not: []string{"tool_choice", "parallel_tool_calls"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := tc.model
			if model == "" {
				model = "gpt-4o"
			}
			req, _ := chatConvert(t, Responses, chatBody(t, model, tc.rest), "")
			got := chatJSON(t, req)
			chatWant(t, got, tc.want...)
			chatWantNot(t, got, tc.not...)
		})
	}
}

func TestChatToResponsesToolChoice(t *testing.T) {
	for _, tc := range []struct{ choice, want string }{
		{`{"type":"function","function":{"name":"get_weather"}}`, `{"name":"get_weather","type":"function"}`},
		{`{"type":"function","name":"get_weather"}`, `{"name":"get_weather","type":"function"}`},
		{`{"type":"function","function":{"name":"python"}}`, `{"name":"python__st","type":"function"}`},
		{`"auto"`, `"auto"`}, {`"required"`, `"required"`}, {`"none"`, `"none"`},
		{`{"type":"allowed_tools","mode":"auto","tools":[{"type":"function","name":"get_weather"}]}`, `{"mode":"auto","tools":[{"name":"get_weather","type":"function"}],"type":"allowed_tools"}`},
		{`{"type":"function","function":{}}`, `{"function":{},"type":"function"}`},
	} {
		req, _ := chatConvert(t, Responses, chatBody(t, "gpt-4o", `"messages":[{"role":"user","content":"weather?"}],"tools":[{"type":"function","function":{"name":"get_weather"}},{"type":"function","function":{"name":"python"}}],"tool_choice":`+tc.choice), "")
		if got := chatJSON(t, req["tool_choice"]); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.choice, got, tc.want)
		}
	}
}

// Content the API would reject as null becomes an empty string.
func TestChatToResponsesEmptyContentIsNeverNull(t *testing.T) {
	for _, content := range []string{`null`, `[]`, `[{"type":"text","text":""}]`, `[{"type":"image_url","image_url":{"url":"data:image/png;base64,"}}]`} {
		req, _ := chatConvert(t, Responses, chatBody(t, "gpt-5.5", `"messages":[{"role":"user","content":`+content+`}]`), "")
		items := chatResponsesInput(t, req)
		if len(items) != 1 || items[0]["content"] != "" {
			t.Errorf("%s: %v", content, items)
		}
	}
}

// Legacy function calling has no call IDs: calls and results are paired by
// IDs given in message order, stable across turns, clear of the IDs in use.
func TestChatToResponsesLegacyFunctionCalls(t *testing.T) {
	history := `"messages":[{"role":"user","content":"Paris and Rome?"},
{"role":"assistant","content":"Checking Paris.","function_call":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}},
{"role":"function","name":"get_weather","content":"sunny"},
{"role":"assistant","function_call":{"name":"get_weather","arguments":"{\"city\":\"Rome\"}"}},
{"role":"function","name":"get_weather","content":"rainy"}]`
	req, _ := chatConvert(t, Responses, chatBody(t, "gpt-4o", history), "")
	items := chatResponsesInput(t, req)
	if len(items) != 6 {
		t.Fatalf("items %v", items)
	}
	c1, o1, c2, o2 := items[2], items[3], items[4], items[5]
	if c1["type"] != "function_call" || o1["type"] != "function_call_output" || c1["call_id"] == "" || c1["call_id"] != o1["call_id"] || o1["output"] != "sunny" ||
		c2["call_id"] != o2["call_id"] || c1["call_id"] == c2["call_id"] || o2["output"] != "rainy" || c2["arguments"] != `{"city":"Rome"}` {
		t.Fatalf("pairing %v", items)
	}
	again, _ := chatConvert(t, Responses, chatBody(t, "gpt-4o", history), "")
	if chatJSON(t, again["input"]) != chatJSON(t, req["input"]) {
		t.Fatal("the IDs change between turns")
	}

	req, _ = chatConvert(t, Responses, chatBody(t, "gpt-4o", `"messages":[{"role":"user","content":"hi"},
{"role":"assistant","function_call":{"name":"lookup","arguments":"{}"}},{"role":"function","name":"lookup","content":"legacy"},
{"role":"assistant","tool_calls":[{"id":"call_legacy_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_legacy_1","content":"tool"}]`), "")
	items = chatResponsesInput(t, req)
	if items[1]["call_id"] == "call_legacy_1" || items[1]["call_id"] != items[2]["call_id"] || items[3]["call_id"] != "call_legacy_1" {
		t.Fatalf("IDs in use: %v", items)
	}

	req, _ = chatConvert(t, Responses, chatBody(t, "gpt-4o", `"messages":[{"role":"user","content":"hi"},{"role":"function","name":"lookup","content":"orphan"}]`), "")
	if items = chatResponsesInput(t, req); items[1]["call_id"] != "lookup" {
		t.Fatalf("an unpaired result keeps the name: %v", items)
	}
}

func TestChatToResponsesRefusesBadRequests(t *testing.T) {
	for _, body := range []string{`{`, `{"model":"m","messages":[]}`, `{"model":"m","messages":[{"role":"system","content":"only"}]}`} {
		if _, err := Convert(Chat, Responses, []byte(body), Options{}); err == nil {
			t.Errorf("accepted %s", body)
		}
	}
}

func chatResponsesTranslator(t *testing.T, model string) Translator {
	t.Helper()
	_, tr := chatConvert(t, Responses, chatBody(t, "gpt-5.4", `"messages":[{"role":"user","content":"Hi"}]`), model)
	return tr
}

// The provider's stream test.
func TestChatFromResponsesStream(t *testing.T) {
	res := chatStream(t, chatResponsesTranslator(t, "client-model"),
		`{"type":"response.created","response":{"id":"resp_1"}}`,
		`{"type":"response.reasoning_summary_text.delta","delta":"thinking"}`,
		`{"type":"response.output_text.delta","delta":"Hel"}`,
		`{"type":"response.output_text.delta","delta":"lo"}`,
		`{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","call_id":"call_9","name":"python__st"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":2,"delta":"{\"code\":"}`,
		`{"type":"response.function_call_arguments.delta","output_index":2,"delta":"\"1\"}"}`,
		`{"type":"response.function_call_arguments.done","output_index":2,"arguments":"{\"code\":\"1\"}"}`,
		`{"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","call_id":"call_9","name":"python__st","arguments":"{\"code\":\"1\"}"}}`,
		`{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":31,"output_tokens":12}}}`)
	if res.err != nil || res.text != "Hello" || res.reasoning != "thinking" || res.finish != "tool_calls" || res.model != "client-model" {
		t.Fatalf("%+v", res)
	}
	if len(res.calls) != 1 || res.calls[0] != (chatTestCall{"call_9", "python", `{"code":"1"}`}) {
		t.Fatalf("calls %+v", res.calls)
	}
	if chatJSON(t, res.usage) != `{"completion_tokens":12,"prompt_tokens":31,"total_tokens":43}` {
		t.Fatalf("usage %v", res.usage)
	}
}

func TestChatFromResponsesRoundTrip(t *testing.T) {
	_, tr := chatConvert(t, Responses, chatBody(t, "gpt-4o", `"messages":[{"role":"user","content":"Hi"}]`), "")
	res := chatStream(t, tr,
		`{"type":"response.created","response":{"id":"resp_rt"}}`,
		`{"type":"response.in_progress","response":{"id":"resp_rt"}}`,
		`{"type":"response.output_text.delta","output_index":0,"delta":"Hello"}`,
		`{"type":"response.output_text.delta","output_index":0,"delta":", "}`,
		`{"type":"response.output_text.delta","output_index":0,"delta":"world"}`,
		`{"type":"response.output_text.delta","output_index":0,"delta":"!"}`,
		`{"type":"response.output_text.done","output_index":0,"text":"Hello, world!"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","content":[{"type":"output_text","text":"Hello, world!"}]}}`,
		`{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"Hello, world!"}]}],"usage":{"input_tokens":10,"output_tokens":4}}}`)
	if res.err != nil || len(res.chunks) != 6 || res.text != "Hello, world!" || res.finish != "stop" || res.model != "gpt-4o" {
		t.Fatalf("%d chunks %+v", len(res.chunks), res)
	}
}

func TestChatFromResponsesTerminalEvents(t *testing.T) {
	for _, tc := range []struct {
		name, event, finish, usage string
	}{
		{"completed", `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":13,"output_tokens":7}}}`, "stop",
			`{"completion_tokens":7,"prompt_tokens":13,"total_tokens":20}`},
		{"response.done", `{"type":"response.done","response":{"status":"completed","usage":{"input_tokens":13,"output_tokens":7}}}`, "stop",
			`{"completion_tokens":7,"prompt_tokens":13,"total_tokens":20}`},
		{"output limit", `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":13,"output_tokens":7}}}`, "length",
			`{"completion_tokens":7,"prompt_tokens":13,"total_tokens":20}`},
		{"response.done incomplete", `{"type":"response.done","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`, "length",
			`{"completion_tokens":0,"prompt_tokens":0,"total_tokens":0}`},
		{"content filter", `{"type":"response.completed","response":{"status":"incomplete","incomplete_details":{"reason":"content_filter"}}}`, "content_filter",
			`{"completion_tokens":0,"prompt_tokens":0,"total_tokens":0}`},
		{"usage at the top level", `{"type":"response.completed","response":{"status":"completed"},"usage":{"input_tokens":21,"output_tokens":9,"input_tokens_details":{"cached_tokens":4}}}`, "stop",
			`{"completion_tokens":9,"prompt_tokens":21,"prompt_tokens_details":{"cached_tokens":4},"total_tokens":30}`},
		{"every detail", `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":100,"output_tokens":50,"input_tokens_details":{"cached_tokens":60,"audio_tokens":4},"output_tokens_details":{"reasoning_tokens":30,"audio_tokens":2,"accepted_prediction_tokens":10,"rejected_prediction_tokens":3}}}}`, "stop",
			`{"completion_tokens":50,"completion_tokens_details":{"accepted_prediction_tokens":10,"audio_tokens":2,"reasoning_tokens":30,"rejected_prediction_tokens":3},"prompt_tokens":100,"prompt_tokens_details":{"audio_tokens":4,"cached_tokens":60},"total_tokens":150}`},
		{"zero reasoning tokens are left out", `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":10,"output_tokens":5,"output_tokens_details":{"reasoning_tokens":0}}}}`, "stop",
			`{"completion_tokens":5,"prompt_tokens":10,"total_tokens":15}`},
		{"cache writes", `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":1000,"output_tokens":50,"input_tokens_details":{"cached_tokens":100,"cache_write_tokens":200}}}}`, "stop",
			`{"completion_tokens":50,"prompt_tokens":1000,"prompt_tokens_details":{"cache_creation_tokens":200,"cached_tokens":100},"total_tokens":1050}`},
		{"the nested cache write count wins, even 0", `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":20,"output_tokens":2,"cache_creation_input_tokens":19,"input_tokens_details":{"cache_write_tokens":0}}}}`, "stop",
			`{"completion_tokens":2,"prompt_tokens":20,"total_tokens":22}`},
		{"the top-level cache write count", `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":20,"output_tokens":2,"cache_creation_input_tokens":19}}}`, "stop",
			`{"completion_tokens":2,"prompt_tokens":20,"prompt_tokens_details":{"cache_creation_tokens":19,"cached_tokens":0},"total_tokens":22}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := chatStream(t, chatResponsesTranslator(t, ""), `{"type":"response.output_text.delta","delta":"x"}`, tc.event)
			if res.err != nil || res.finish != tc.finish || chatJSON(t, res.usage) != tc.usage {
				t.Fatalf("finish %q usage %s err %v", res.finish, chatJSON(t, res.usage), res.err)
			}
		})
	}
}

func TestChatFromResponsesItems(t *testing.T) {
	for _, tc := range []struct {
		name      string
		events    []string
		text      string
		reasoning string
		refusal   string
		calls     []chatTestCall
	}{
		{name: "argument deltas carry no name", events: []string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_a","name":"exec"}}`,
			`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"cmd\":"}`,
			`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"\"ls\"}"}`},
			calls: []chatTestCall{{"call_a", "exec", `{"cmd":"ls"}`}}},
		{name: "arguments only on done", events: []string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_a","name":"exec"}}`,
			`{"type":"response.function_call_arguments.done","output_index":0,"arguments":"{\"cmd\":\"ls\"}"}`},
			calls: []chatTestCall{{"call_a", "exec", `{"cmd":"ls"}`}}},
		{name: "done completes what the deltas missed", events: []string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"call_a","name":"exec"}}`,
			`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"cmd\":"}`,
			`{"type":"response.function_call_arguments.done","output_index":0,"arguments":"{\"cmd\":\"ls\"}"}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","call_id":"call_a","name":"exec","arguments":"{\"cmd\":\"ls\"}"}}`},
			calls: []chatTestCall{{"call_a", "exec", `{"cmd":"ls"}`}}},
		{name: "a call only in output_item.done", events: []string{
			`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","call_id":"call_b","name":"python__st","arguments":"{}"}}`},
			calls: []chatTestCall{{"call_b", "python", `{}`}}},
		{name: "custom tool input", events: []string{
			`{"type":"response.output_item.added","output_index":1,"item":{"type":"custom_tool_call","call_id":"call_patch","name":"apply_patch"}}`,
			`{"type":"response.custom_tool_call_input.delta","output_index":1,"delta":"*** Begin Patch"}`,
			`{"type":"response.custom_tool_call_input.done","output_index":1,"input":"*** Begin Patch\n*** End Patch"}`},
			calls: []chatTestCall{{"call_patch", "apply_patch", "*** Begin Patch\n*** End Patch"}}},
		{name: "parallel calls keep their indexes", events: []string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"a","name":"f"}}`,
			`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"b","name":"g"}}`,
			`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"y\":2}"}`,
			`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"x\":1}"}`},
			calls: []chatTestCall{{"a", "f", `{"x":1}`}, {"b", "g", `{"y":2}`}}},
		{name: "raw reasoning text and summary parts", events: []string{
			`{"type":"response.reasoning_text.delta","output_index":0,"delta":"step"}`,
			`{"type":"response.reasoning_summary_part.added","output_index":1,"summary_index":0}`,
			`{"type":"response.reasoning_summary_text.delta","output_index":1,"summary_index":0,"delta":"one"}`,
			`{"type":"response.reasoning_summary_text.done","output_index":1,"summary_index":0,"text":"one"}`,
			`{"type":"response.reasoning_summary_part.added","output_index":1,"summary_index":1}`,
			`{"type":"response.reasoning_summary_text.delta","output_index":1,"summary_index":1,"delta":"two"}`,
			`{"type":"response.output_item.done","output_index":1,"item":{"type":"reasoning","summary":[{"type":"summary_text","text":"one"},{"type":"summary_text","text":"two"}]}}`},
			reasoning: "stepone\n\ntwo"},
		{name: "a refusal", events: []string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","role":"assistant"}}`,
			`{"type":"response.refusal.delta","output_index":0,"delta":"I can't "}`,
			`{"type":"response.refusal.delta","output_index":0,"delta":"help."}`,
			`{"type":"response.refusal.done","output_index":0,"refusal":"I can't help."}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","content":[{"type":"refusal","refusal":"I can't help."}]}}`},
			refusal: "I can't help."},
		{name: "text only in output_item.done", events: []string{
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","summary":[{"type":"summary_text","text":"why"}]}}`,
			`{"type":"response.output_item.done","output_index":1,"item":{"type":"message","content":[{"type":"output_text","text":"recovered"}]}}`},
			text: "recovered", reasoning: "why"},
		{name: "streamed text is not repeated", events: []string{
			`{"type":"response.output_text.delta","output_index":0,"delta":"once"}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","content":[{"type":"output_text","text":"once"}]}}`},
			text: "once"},
		{name: "encrypted reasoning has no text", events: []string{
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"gAAA"}}`,
			`{"type":"response.output_text.delta","output_index":1,"delta":"answer"}`},
			text: "answer"},
		{name: "built-in tool items are the upstream's", events: []string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"web_search_call","id":"ws_1"}}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"web_search_call","id":"ws_1","status":"completed"}}`,
			`{"type":"response.output_text.delta","output_index":1,"delta":"found"}`},
			text: "found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := chatStream(t, chatResponsesTranslator(t, ""), append(tc.events, `{"type":"response.completed","response":{"status":"completed"}}`)...)
			if res.err != nil || res.text != tc.text || res.reasoning != tc.reasoning || res.refusal != tc.refusal || chatJSON(t, res.calls) != chatJSON(t, tc.calls) {
				t.Fatalf("got text %q reasoning %q refusal %q calls %+v err %v", res.text, res.reasoning, res.refusal, res.calls, res.err)
			}
			want := "stop"
			if len(tc.calls) > 0 {
				want = "tool_calls"
			}
			if res.finish != want {
				t.Fatalf("finish %q", res.finish)
			}
		})
	}
}

// What the stream did not carry is taken from the final response's output.
func TestChatFromResponsesFinalOutput(t *testing.T) {
	res := chatStream(t, chatResponsesTranslator(t, ""),
		`{"type":"response.output_text.delta","output_index":0,"delta":"streamed"}`,
		`{"type":"response.completed","response":{"status":"completed","output":[
			{"type":"message","content":[{"type":"output_text","text":"streamed"}]},
			{"type":"function_call","call_id":"c","name":"f","arguments":"{\"a\":1}"}]}}`)
	if res.err != nil || res.text != "streamed" || len(res.calls) != 1 || res.calls[0].Args != `{"a":1}` || res.finish != "tool_calls" {
		t.Fatalf("%+v", res)
	}
}

func TestChatFromResponsesHiddenReasoningIsReported(t *testing.T) {
	res := chatStream(t, chatResponsesTranslator(t, ""),
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"gAAA"}}`,
		`{"type":"response.output_text.delta","output_index":1,"delta":"pong"}`,
		`{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":24,"output_tokens":33,"output_tokens_details":{"reasoning_tokens":32}}}}`)
	if res.reasoning != "" || res.text != "pong" || chatUsageNumber(t, res.usage, "completion_tokens_details", "reasoning_tokens") != 32 || chatUsageNumber(t, res.usage, "completion_tokens") != 33 {
		t.Fatalf("%+v", res)
	}
}

func TestChatFromResponsesErrors(t *testing.T) {
	for _, tc := range []struct{ name, event, want string }{
		{"response.failed", `{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"boom"}}}`, "server_error: boom"},
		{"error event", `{"type":"error","code":"rate_limit_exceeded","message":"slow down"}`, "rate_limit_exceeded: slow down"},
		{"nested error", `{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`, "invalid_request_error: bad"},
		{"failed status on completion", `{"type":"response.completed","response":{"status":"failed","error":{"code":"x","message":"y"}}}`, "x: y"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := chatResponsesTranslator(t, "")
			err := tr.Event([]byte(tc.event), func([]byte) error { return nil })
			var ue *chatUpstreamError
			if err == nil || !strings.Contains(err.Error(), tc.want) || !asChatUpstreamError(err, &ue) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func asChatUpstreamError(err error, target **chatUpstreamError) bool {
	e, ok := err.(*chatUpstreamError)
	*target = e
	return ok
}

func TestChatFromResponsesClose(t *testing.T) {
	// Cut short after text: finished as if it stopped, with estimated usage.
	res := chatStream(t, chatResponsesTranslator(t, ""), `{"type":"response.created"}`, `{"type":"response.output_text.delta","delta":"word word word word"}`)
	if res.err != nil || res.finish != "stop" || res.text != "word word word word" ||
		chatUsageNumber(t, res.usage, "completion_tokens") == 0 || chatUsageNumber(t, res.usage, "prompt_tokens") == 0 {
		t.Fatalf("%+v", res)
	}
	// Cut short after a call.
	res = chatStream(t, chatResponsesTranslator(t, ""),
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","call_id":"c","name":"f"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{}"}`)
	if res.err != nil || res.finish != "tool_calls" {
		t.Fatalf("%+v", res)
	}
	// Nothing came: an error.
	res = chatStream(t, chatResponsesTranslator(t, ""), `{"type":"response.created"}`, `not json`, `{"type":"response.something_new"}`)
	if res.err != errChatStreamEnded || len(res.chunks) != 0 {
		t.Fatalf("%+v", res)
	}
	// Finished: nothing more, and later events are ignored.
	tr := chatResponsesTranslator(t, "")
	res = chatStream(t, tr, `{"type":"response.output_text.delta","delta":"a"}`, `{"type":"response.completed","response":{"status":"completed"}}`,
		`{"type":"response.output_text.delta","delta":"late"}`)
	if res.text != "a" || len(res.chunks) != 3 {
		t.Fatalf("%+v", res)
	}
	if err := tr.Close(func([]byte) error { t.Fatal("emitted after the end"); return nil }); err != nil {
		t.Fatal(err)
	}
}
