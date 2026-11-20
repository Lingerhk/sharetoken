package apicompat

import (
	"strings"
	"testing"
)

func respChatMsgs(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, m := range body["messages"].([]any) {
		out = append(out, m.(map[string]any))
	}
	return out
}

// respCheckChatSendable checks the chat tool-call invariant: an assistant
// message with tool_calls is followed directly by one tool message per call,
// in order; no tool message elsewhere; system only first.
func respCheckChatSendable(t *testing.T, msgs []map[string]any) {
	t.Helper()
	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		if m["role"] == "system" && i != 0 {
			t.Fatalf("system message at %d", i)
		}
		if m["role"] == "tool" {
			t.Fatalf("tool message %d does not follow its call: %v", i, msgs)
		}
		calls, _ := m["tool_calls"].([]any)
		for _, c := range calls {
			i++
			id := c.(map[string]any)["id"]
			if i >= len(msgs) || msgs[i]["role"] != "tool" || msgs[i]["tool_call_id"] != id {
				t.Fatalf("call %v is not answered next: %v", id, msgs)
			}
		}
	}
}

func respToChatMsgs(t *testing.T, input, extra string) (map[string]any, []map[string]any) {
	t.Helper()
	c := respConvert(t, Chat, `{"model":"deepseek-x","stream":true,"max_output_tokens":2000,"input":`+input+extra+`}`, Options{})
	m := respBody(t, c)
	msgs := respChatMsgs(t, m)
	respCheckChatSendable(t, msgs)
	return m, msgs
}

func TestResponsesToChatRequestBasics(t *testing.T) {
	m, msgs := respToChatMsgs(t, `[
		{"type":"message","role":"developer","content":[{"type":"input_text","text":"dev"}]},
		{"type":"message","role":"system","content":"sys"},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"a"},{"type":"input_text","text":"b"}]},
		{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]},
		{"type":"message","role":"developer","content":"switched"},
		{"type":"message","content":"no role"}
	]`, `,"instructions":"inst","temperature":0.7,"top_p":0.9,"reasoning":{"effort":"high","summary":"auto"},"text":{"verbosity":"low","format":{"type":"json_schema","name":"out","schema":{"type":"object"},"strict":true}},"prompt_cache_key":"k","client_metadata":{},"include":["x"],"store":false`)
	want := []string{
		`{"content":"inst\n\ndev\n\nsys","role":"system"}`,
		`{"content":"a\n\nb","role":"user"}`,
		`{"content":"answer","role":"assistant"}`,
		`{"content":"switched","role":"user"}`,
		`{"content":"no role","role":"user"}`,
	}
	if len(msgs) != len(want) {
		t.Fatalf("messages: %v", msgs)
	}
	for i := range want {
		if got := respJSON(t, msgs[i]); got != want[i] {
			t.Errorf("message %d: %s, want %s", i, got, want[i])
		}
	}
	if m["model"] != "deepseek-x" || m["stream"] != true || respNum(m, "max_tokens") != 2000 || m["temperature"] != 0.7 ||
		m["top_p"] != 0.9 || m["reasoning_effort"] != "high" || respJSON(t, m["stream_options"]) != `{"include_usage":true}` {
		t.Errorf("top level: %v", m)
	}
	if respJSON(t, m["response_format"]) != `{"json_schema":{"name":"out","schema":{"type":"object"},"strict":true},"type":"json_schema"}` {
		t.Errorf("response_format: %v", m["response_format"])
	}
	for _, k := range []string{"prompt_cache_key", "client_metadata", "include", "store", "input", "instructions", "max_output_tokens", "text", "reasoning", "tools", "tool_choice", "parallel_tool_calls"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s leaked into the chat request", k)
		}
	}
	m, _ = respToChatMsgs(t, `"x"`, `,"text":{"format":{"type":"json_object"}}`)
	if respJSON(t, m["response_format"]) != `{"type":"json_object"}` {
		t.Errorf("json_object: %v", m["response_format"])
	}
	m, _ = respToChatMsgs(t, `"x"`, `,"text":{"format":{"type":"text"}}`)
	if m["response_format"] != nil {
		t.Errorf("text format is the default")
	}
	if _, err := Convert(Responses, Chat, []byte(`{"model":"m","input":[],"instructions":"only"}`), Options{}); err == nil {
		t.Errorf("a request with only instructions must fail")
	}
}

func TestResponsesToChatContentParts(t *testing.T) {
	_, msgs := respToChatMsgs(t, `[{"type":"message","role":"user","content":[
		{"type":"input_text","text":"see"},
		{"type":"input_image","image_url":"data:image/png;base64,AAA","detail":"high"},
		{"type":"input_image","image_url":{"url":"https://x/y.png"}},
		{"type":"input_image","image_url":"data:image/png;base64,"},
		{"type":"input_file","filename":"a.pdf","file_data":"data:application/pdf;base64,JVB="},
		{"type":"input_file","file_id":"file_9"}
	]}]`, "")
	want := `[{"text":"see","type":"text"},{"image_url":{"detail":"high","url":"data:image/png;base64,AAA"},"type":"image_url"},{"image_url":{"url":"https://x/y.png"},"type":"image_url"},{"file":{"file_data":"data:application/pdf;base64,JVB=","filename":"a.pdf"},"type":"file"},{"file":{"file_id":"file_9"},"type":"file"}]`
	if got := respJSON(t, msgs[0]["content"]); got != want {
		t.Errorf("content:\n%s\n%s", got, want)
	}
}

func TestResponsesToChatToolHistory(t *testing.T) {
	_, msgs := respToChatMsgs(t, `[
		{"type":"message","role":"user","content":"go"},
		{"type":"reasoning","summary":[{"type":"summary_text","text":"plan A"}],"encrypted_content":"gAAAA"},
		{"type":"message","role":"assistant","content":"working"},
		{"type":"function_call","call_id":"c1","name":"exec","arguments":"{\"cmd\":\"ls\"}"},
		{"type":"function_call","call_id":"c2","namespace":"gh","name":"list","arguments":""},
		{"type":"message","role":"developer","content":"Approved"},
		{"type":"function_call_output","call_id":"c2","output":"[]"},
		{"type":"function_call_output","call_id":"c1","output":{"stdout":"a"}},
		{"type":"custom_tool_call","call_id":"p1","name":"apply_patch","input":"*** Begin Patch"},
		{"type":"custom_tool_call_output","call_id":"p1","output":"Done"},
		{"type":"tool_search_call","call_id":"s1","execution":"client","arguments":{"query":"q"}},
		{"type":"tool_search_output","call_id":"s1","execution":"client","status":"completed","tools":[]},
		{"type":"function_call","call_id":"dangling","name":"exec","arguments":"{}"},
		{"type":"function_call_output","call_id":"orphan","output":"x"},
		{"type":"message","role":"user","content":"next"}
	]`, `,"tools":[{"type":"custom","name":"apply_patch"},{"type":"tool_search"},{"type":"namespace","name":"gh","tools":[{"type":"function","name":"list"}]}]`)
	want := []string{
		`{"content":"go","role":"user"}`,
		`{"content":"working","reasoning_content":"plan A","role":"assistant","tool_calls":[{"function":{"arguments":"{\"cmd\":\"ls\"}","name":"exec"},"id":"c1","type":"function"},{"function":{"arguments":"{}","name":"gh__list"},"id":"c2","type":"function"}]}`,
		`{"content":"{\"stdout\":\"a\"}","role":"tool","tool_call_id":"c1"}`,
		`{"content":"[]","role":"tool","tool_call_id":"c2"}`,
		`{"content":"Approved","role":"user"}`,
		`{"content":null,"reasoning_content":"plan A","role":"assistant","tool_calls":[{"function":{"arguments":"{\"input\":\"*** Begin Patch\"}","name":"apply_patch"},"id":"p1","type":"function"}]}`,
		`{"content":"Done","role":"tool","tool_call_id":"p1"}`,
		`{"content":null,"reasoning_content":"plan A","role":"assistant","tool_calls":[{"function":{"arguments":"{\"query\":\"q\"}","name":"tool_search"},"id":"s1","type":"function"}]}`,
		`{"content":"[]","role":"tool","tool_call_id":"s1"}`,
		`{"content":"next","role":"user"}`,
	}
	if len(msgs) != len(want) {
		t.Fatalf("messages:\n%s", respJSON(t, msgs))
	}
	for i := range want {
		b, _ := respMarshal(msgs[i])
		if string(b) != want[i] {
			t.Errorf("message %d:\n got %s\nwant %s", i, b, want[i])
		}
	}
}

func TestResponsesToChatThinkingEnvelopeReasoning(t *testing.T) {
	enc := respEncodeThinking(respThinkingBlock{Type: "thinking", Thinking: "from claude", Signature: "S"})
	_, msgs := respToChatMsgs(t, `[
		{"type":"message","role":"user","content":"q"},
		{"type":"reasoning","summary":[],"encrypted_content":"`+enc+`"},
		{"type":"message","role":"assistant","content":"a"}
	]`, "")
	if msgs[1]["reasoning_content"] != "from claude" {
		t.Errorf("reasoning from a thinking envelope: %v", msgs[1])
	}
}

func TestResponsesToChatToolOutputMedia(t *testing.T) {
	_, msgs := respToChatMsgs(t, `[
		{"type":"message","role":"user","content":"look"},
		{"type":"function_call","call_id":"v1","name":"view_image","arguments":"{}"},
		{"type":"function_call","call_id":"v2","name":"view_image","arguments":"{}"},
		{"type":"function_call_output","call_id":"v1","output":[{"type":"input_text","text":"loaded"},{"type":"input_image","image_url":"data:image/png;base64,QQ=="}]},
		{"type":"function_call_output","call_id":"v2","output":"data:image/png;base64,Qg=="},
		{"type":"message","role":"user","content":"what is it"}
	]`, "")
	if len(msgs) != 6 {
		t.Fatalf("%s", respJSON(t, msgs))
	}
	if msgs[2]["content"] != "loaded\n"+respMediaMarker || msgs[3]["content"] != respMediaMarker {
		t.Errorf("tool contents: %v %v", msgs[2]["content"], msgs[3]["content"])
	}
	want := `[{"text":"[Tool output media for call v1]","type":"text"},{"image_url":{"url":"data:image/png;base64,QQ=="},"type":"image_url"},{"text":"[Tool output media for call v2]","type":"text"},{"image_url":{"url":"data:image/png;base64,Qg=="},"type":"image_url"}]`
	if msgs[4]["role"] != "user" || respJSON(t, msgs[4]["content"]) != want {
		t.Errorf("media message: %s", respJSON(t, msgs[4]))
	}
	// a media-free output keeps its bytes
	_, msgs = respToChatMsgs(t, `[{"type":"message","role":"user","content":"x"},
		{"type":"function_call","call_id":"c","name":"f","arguments":"{}"},
		{"type":"function_call_output","call_id":"c","output":"{\"b\": 1,  \"a\": 2}"}]`, "")
	if msgs[2]["content"] != `{"b": 1,  "a": 2}` {
		t.Errorf("output bytes changed: %v", msgs[2]["content"])
	}
}

func TestResponsesToChatTools(t *testing.T) {
	tools := `,"tools":[
		{"type":"function","name":"exec","description":"d","parameters":{"type":"object","properties":{}},"strict":false},
		{"type":"custom","name":"apply_patch","description":"freeform"},
		{"type":"namespace","name":"mcp","tools":[{"type":"function","name":"read"}]},
		{"type":"tool_search","execution":"client"},
		{"type":"web_search"}
	]`
	m, _ := respToChatMsgs(t, `"x"`, tools+`,"parallel_tool_calls":true`)
	list := m["tools"].([]any)
	var names []string
	for _, x := range list {
		fn := x.(map[string]any)["function"].(map[string]any)
		names = append(names, fn["name"].(string))
		if x.(map[string]any)["type"] != "function" || fn["parameters"].(map[string]any)["type"] != "object" {
			t.Errorf("tool %v", x)
		}
	}
	if strings.Join(names, ",") != "exec,apply_patch,mcp__read,tool_search" || m["parallel_tool_calls"] != true {
		t.Errorf("tools %v, parallel %v", names, m["parallel_tool_calls"])
	}
	if list[0].(map[string]any)["function"].(map[string]any)["strict"] != false {
		t.Errorf("strict kept")
	}
	for choice, want := range map[string]string{
		`"auto"`:                                 `"auto"`,
		`"required"`:                             `"required"`,
		`"none"`:                                 `"none"`,
		`{"type":"function","name":"exec"}`:      `{"function":{"name":"exec"},"type":"function"}`,
		`{"type":"custom","name":"apply_patch"}`: `{"function":{"name":"apply_patch"},"type":"function"}`,
		`{"type":"tool_search"}`:                 `{"function":{"name":"tool_search"},"type":"function"}`,
		`{"type":"web_search"}`:                  ``,
		`{"type":"function","name":"gone"}`:      ``,
		`{"type":"allowed_tools","mode":"required","tools":[]}`: `"required"`,
	} {
		m, _ := respToChatMsgs(t, `"x"`, tools+`,"tool_choice":`+choice)
		got := ""
		if m["tool_choice"] != nil {
			got = respJSON(t, m["tool_choice"])
		}
		if got != want {
			t.Errorf("tool_choice %s → %s, want %s", choice, got, want)
		}
	}
	// only hosted tools: no tools and no tool_choice
	m, _ = respToChatMsgs(t, `"x"`, `,"tools":[{"type":"web_search"}],"tool_choice":"auto","parallel_tool_calls":false`)
	if m["tools"] != nil || m["tool_choice"] != nil || m["parallel_tool_calls"] != nil {
		t.Errorf("hosted-only tools: %v", m)
	}
}

func TestResponsesToChatCompaction(t *testing.T) {
	_, msgs := respToChatMsgs(t, `[
		{"type":"compaction_summary","encrypted_content":"`+EncodeCompaction("summary text")+`"},
		{"type":"compaction","encrypted_content":"opaque"},
		{"type":"message","role":"user","content":"go on"}
	]`, "")
	if len(msgs) != 2 || msgs[0]["content"] != SummaryMessage("summary text") || msgs[1]["content"] != "go on" {
		t.Errorf("%s", respJSON(t, msgs))
	}
}

// --- stream ---

const respChatTools = `,"tools":[{"type":"function","name":"exec"},{"type":"custom","name":"apply_patch"},
	{"type":"namespace","name":"functions","tools":[{"type":"function","name":"wait"}]},{"type":"tool_search","execution":"client"},{"type":"local_shell"}]`

func respChatConv(t *testing.T, tools string, opt Options) *Conversion {
	t.Helper()
	return respConvert(t, Chat, `{"model":"deepseek-x","input":"q","stream":true`+tools+`}`, opt)
}

func TestResponsesFromChatText(t *testing.T) {
	evs := respMustFeed(t, respChatConv(t, "", Options{Model: "client"}).Translator,
		`{"id":"c1","object":"chat.completion.chunk","model":"deepseek-x","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{"reasoning_content":"think "},"finish_reason":null}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{"reasoning_content":"more"},"finish_reason":null}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{"content":"Hel"},"finish_reason":null}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":null}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"id":"c1","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":30,"total_tokens":130,"prompt_tokens_details":{"cached_tokens":60},"completion_tokens_details":{"reasoning_tokens":12}}}`,
		`[DONE]`,
	)
	out := respOutput(evs)
	if len(out) != 2 || out[0]["type"] != "reasoning" || respJSON(t, out[0]["summary"]) != `[{"text":"think more","type":"summary_text"}]` ||
		out[1]["content"].([]any)[0].(map[string]any)["text"] != "Hello" {
		t.Fatalf("output %v", out)
	}
	if respTerminal(evs)["model"] != "client" {
		t.Errorf("model")
	}
	u := respUsageOf(evs)
	if respNum(u, "input_tokens") != 100 || respNum(u, "input_tokens_details", "cached_tokens") != 60 ||
		respNum(u, "output_tokens") != 30 || respNum(u, "output_tokens_details", "reasoning_tokens") != 12 || respNum(u, "total_tokens") != 130 {
		t.Errorf("usage %v", u)
	}
}

func TestResponsesFromChatUsageVariants(t *testing.T) {
	tests := []struct {
		usage                                        string
		input, cached, write, output, reasoning, tot int
	}{
		{`{"prompt_tokens":10,"completion_tokens":5}`, 10, 0, 0, 5, 0, 15},
		{`{"prompt_tokens":10,"completion_tokens":5,"prompt_cache_hit_tokens":7,"prompt_cache_miss_tokens":3}`, 10, 7, 0, 5, 0, 15},
		{`{"prompt_tokens":10,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":2,"cache_write_tokens":4}}`, 10, 2, 4, 5, 0, 15},
		{`{"prompt_tokens":10,"completion_tokens":5,"total_tokens":16,"prompt_tokens_details":{"cache_creation_tokens":3}}`, 10, 0, 3, 5, 0, 16},
	}
	for _, tt := range tests {
		evs := respMustFeed(t, respChatConv(t, "", Options{}).Translator,
			`{"choices":[{"index":0,"delta":{"content":"x"},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":`+tt.usage+`}`)
		u := respUsageOf(evs)
		if respNum(u, "input_tokens") != tt.input || respNum(u, "input_tokens_details", "cached_tokens") != tt.cached ||
			max(respNum(u, "input_tokens_details", "cache_creation_tokens"), 0) != tt.write || respNum(u, "output_tokens") != tt.output ||
			respNum(u, "total_tokens") != tt.tot {
			t.Errorf("%s → %v", tt.usage, u)
		}
	}
}

func TestResponsesFromChatToolCalls(t *testing.T) {
	evs := respMustFeed(t, respChatConv(t, respChatTools, Options{}).Translator,
		`{"choices":[{"index":0,"delta":{"role":"assistant","content":"Let me run it."}}]}`,
		// two parallel calls, interleaved
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"exec","arguments":""}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"functions__wait","arguments":"{\"ms\""}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"cmd\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"ls\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":":5}"}}]}}]}`,
		// a custom tool, a tool search, a local shell call
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":2,"id":"call_c","function":{"name":"apply_patch","arguments":"{\"input\":\"*** Begin"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":2,"function":{"arguments":" Patch\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":3,"id":"call_d","function":{"name":"tool_search","arguments":"{\"query\":\"gh\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":4,"id":"call_e","function":{"name":"local_shell","arguments":"{\"command\":[\"pwd\"]}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2}}`,
	)
	out := respOutput(evs)
	want := []string{
		`{"content":[{"annotations":[],"logprobs":[],"text":"Let me run it.","type":"output_text"}],"phase":"commentary","role":"assistant","status":"completed","type":"message"}`,
		`{"arguments":"{\"cmd\":\"ls\"}","call_id":"call_a","name":"exec","status":"completed","type":"function_call"}`,
		`{"arguments":"{\"ms\":5}","call_id":"call_b","name":"wait","namespace":"functions","status":"completed","type":"function_call"}`,
		`{"call_id":"call_c","input":"*** Begin Patch","name":"apply_patch","status":"completed","type":"custom_tool_call"}`,
		`{"arguments":{"query":"gh"},"call_id":"call_d","execution":"client","status":"completed","type":"tool_search_call"}`,
		`{"action":{"command":["pwd"],"type":"exec"},"call_id":"call_e","status":"completed","type":"local_shell_call"}`,
	}
	if len(out) != len(want) {
		t.Fatalf("output %v", out)
	}
	for i := range want {
		delete(out[i], "id")
		if b, _ := respMarshal(out[i]); string(b) != want[i] {
			t.Errorf("item %d:\n got %s\nwant %s", i, b, want[i])
		}
	}
	byItem := map[string]string{}
	for _, e := range respFind(evs, "response.function_call_arguments.delta") {
		byItem[e["item_id"].(string)] += e["delta"].(string)
	}
	for _, e := range respFind(evs, "response.function_call_arguments.done") {
		if byItem[e["item_id"].(string)] != e["arguments"] {
			t.Errorf("done %v vs deltas %q", e["arguments"], byItem[e["item_id"].(string)])
		}
	}
	if len(respFind(evs, "response.function_call_arguments.done")) != 2 {
		t.Errorf("argument events only for plain functions")
	}
	// the namespace call is announced with its client identity
	for _, e := range respFind(evs, "response.output_item.added") {
		if e.item()["call_id"] == "call_b" && (e.item()["name"] != "wait" || e.item()["namespace"] != "functions") {
			t.Errorf("namespace announce %v", e.item())
		}
	}
}

func TestResponsesFromChatToolCallQuirks(t *testing.T) {
	t.Run("arguments in the first chunk are not doubled", func(t *testing.T) {
		evs := respMustFeed(t, respChatConv(t, respChatTools, Options{}).Translator,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"exec","arguments":"{\"a\":1}"}}]},"finish_reason":"tool_calls"}]}`)
		if respOutput(evs)[0]["arguments"] != `{"a":1}` {
			t.Errorf("%v", respOutput(evs))
		}
	})
	t.Run("custom name arrives late", func(t *testing.T) {
		evs := respMustFeed(t, respChatConv(t, respChatTools, Options{}).Translator,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"arguments":"{\"inp"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"apply_patch","arguments":"ut\": \"dir\"}"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
		added := respFind(evs, "response.output_item.added")
		if len(added) != 1 || added[0].item()["type"] != "custom_tool_call" || respOutput(evs)[0]["input"] != "dir" {
			t.Errorf("%v", respOutput(evs))
		}
		if len(respFind(evs, "response.function_call_arguments.delta")) != 0 {
			t.Errorf("no function events for a custom call")
		}
	})
	t.Run("function name arrives late: early arguments are replayed", func(t *testing.T) {
		evs := respMustFeed(t, respChatConv(t, respChatTools, Options{}).Translator,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c9","function":{"arguments":"{\"cell"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"exec","arguments":"_id\": 3}"}}]}}]}`)
		d := ""
		for _, e := range respFind(evs, "response.function_call_arguments.delta") {
			d += e["delta"].(string)
		}
		if d != `{"cell_id": 3}` || respOutput(evs)[0]["arguments"] != `{"cell_id": 3}` {
			t.Errorf("deltas %q, output %v", d, respOutput(evs))
		}
	})
	t.Run("name never arrives", func(t *testing.T) {
		evs := respMustFeed(t, respChatConv(t, respChatTools, Options{}).Translator,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c","function":{"arguments":"{}"}}]}}]}`)
		if respOutput(evs)[0]["type"] != "function_call" {
			t.Errorf("%v", respOutput(evs))
		}
	})
	t.Run("calls without index", func(t *testing.T) {
		evs := respMustFeed(t, respChatConv(t, respChatTools, Options{}).Translator,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"id":"x1","function":{"name":"exec","arguments":"{\"a\":"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"function":{"arguments":"1}"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"id":"x2","function":{"name":"exec","arguments":"{}"}}]}}]}`)
		out := respOutput(evs)
		if len(out) != 2 || out[0]["arguments"] != `{"a":1}` || out[1]["call_id"] != "x2" {
			t.Errorf("%v", out)
		}
	})
	t.Run("missing call id is generated", func(t *testing.T) {
		evs := respMustFeed(t, respChatConv(t, "", Options{}).Translator,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"f","arguments":"{}"}}]}}]}`)
		if id, _ := respOutput(evs)[0]["call_id"].(string); !strings.HasPrefix(id, "call_") {
			t.Errorf("call id %q", id)
		}
	})
	t.Run("namespace-style alias of a custom tool", func(t *testing.T) {
		evs := respMustFeed(t, respChatConv(t, respChatTools, Options{}).Translator,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"functions__apply_patch","arguments":"{\"input\":\"p\"}"}}]}}]}`)
		if o := respOutput(evs)[0]; o["type"] != "custom_tool_call" || o["name"] != "apply_patch" || o["input"] != "p" {
			t.Errorf("%v", o)
		}
	})
}

func TestResponsesFromChatEndings(t *testing.T) {
	t.Run("length", func(t *testing.T) {
		evs := respMustFeed(t, respChatConv(t, "", Options{}).Translator,
			`{"choices":[{"index":0,"delta":{"content":"cut"},"finish_reason":"length"}]}`)
		if evs[len(evs)-1].typ() != "response.incomplete" ||
			respTerminal(evs)["incomplete_details"].(map[string]any)["reason"] != "max_output_tokens" {
			t.Errorf("%v", respTerminal(evs))
		}
	})
	t.Run("content_filter", func(t *testing.T) {
		evs := respMustFeed(t, respChatConv(t, "", Options{}).Translator,
			`{"choices":[{"index":0,"delta":{"content":"x"},"finish_reason":"content_filter"}]}`)
		if respTerminal(evs)["incomplete_details"].(map[string]any)["reason"] != "content_filter" {
			t.Errorf("%v", respTerminal(evs))
		}
	})
	t.Run("error chunk", func(t *testing.T) {
		_, _, err := respFeed(t, respChatConv(t, "", Options{}).Translator,
			`{"choices":[{"index":0,"delta":{"content":"x"}}]}`,
			`{"error":{"message":"rate limited","type":"rate_limit_error"}}`)
		if err == nil || !strings.Contains(err.Error(), "rate limited") {
			t.Errorf("err %v", err)
		}
	})
	t.Run("nothing produced", func(t *testing.T) {
		if _, _, err := respFeed(t, respChatConv(t, "", Options{}).Translator); err == nil {
			t.Errorf("want error")
		}
		if _, _, err := respFeed(t, respChatConv(t, "", Options{}).Translator, `{"choices":[]}`); err == nil {
			t.Errorf("want error for an empty chunk only")
		}
	})
	t.Run("ended without finish_reason", func(t *testing.T) {
		evs := respMustFeed(t, respChatConv(t, "", Options{}).Translator, `{"choices":[{"index":0,"delta":{"content":"partial"}}]}`)
		if evs[len(evs)-1].typ() != "response.completed" {
			t.Errorf("%v", respTypes(evs))
		}
	})
	t.Run("chunks after [DONE] are ignored", func(t *testing.T) {
		evs := respMustFeed(t, respChatConv(t, "", Options{}).Translator,
			`{"choices":[{"index":0,"delta":{"content":"a"},"finish_reason":"stop"}]}`, `[DONE]`,
			`{"choices":[{"index":0,"delta":{"content":"late"}}]}`)
		if strings.Contains(respJSON(t, evs), "late") {
			t.Errorf("late chunk emitted")
		}
	})
	t.Run("whole message in the final chunk", func(t *testing.T) {
		evs := respMustFeed(t, respChatConv(t, "", Options{}).Translator,
			`{"choices":[{"index":0,"delta":{},"message":{"role":"assistant","content":"only here"},"finish_reason":"stop"}]}`)
		if respOutput(evs)[0]["content"].([]any)[0].(map[string]any)["text"] != "only here" {
			t.Errorf("%v", respOutput(evs))
		}
		evs = respMustFeed(t, respChatConv(t, "", Options{}).Translator,
			`{"choices":[{"index":0,"delta":{"content":"streamed"}}]}`,
			`{"choices":[{"index":0,"message":{"content":"streamed"},"finish_reason":"stop"}]}`)
		if respOutput(evs)[0]["content"].([]any)[0].(map[string]any)["text"] != "streamed" {
			t.Errorf("text repeated in the final message is not doubled: %v", respOutput(evs))
		}
	})
	t.Run("refusal", func(t *testing.T) {
		evs := respMustFeed(t, respChatConv(t, "", Options{}).Translator,
			`{"choices":[{"index":0,"delta":{"refusal":"I can't help"},"finish_reason":"stop"}]}`)
		if respOutput(evs)[0]["content"].([]any)[0].(map[string]any)["text"] != "I can't help" {
			t.Errorf("%v", respOutput(evs))
		}
	})
}

func TestResponsesFromChatReasoning(t *testing.T) {
	t.Run("reasoning only becomes the answer", func(t *testing.T) {
		evs := respMustFeed(t, respChatConv(t, "", Options{}).Translator,
			`{"choices":[{"index":0,"delta":{"reasoning_content":"the answer is 4"},"finish_reason":"stop"}]}`)
		out := respOutput(evs)
		if len(out) != 2 || out[0]["type"] != "reasoning" || out[1]["content"].([]any)[0].(map[string]any)["text"] != "the answer is 4" {
			t.Errorf("%v", out)
		}
	})
	t.Run("blank reasoning only does not", func(t *testing.T) {
		evs := respMustFeed(t, respChatConv(t, "", Options{}).Translator,
			`{"choices":[{"index":0,"delta":{"reasoning_content":"  "},"finish_reason":"stop"}]}`)
		for _, o := range respOutput(evs) {
			if o["type"] == "message" {
				t.Errorf("%v", o)
			}
		}
	})
	t.Run("reasoning then tool call does not", func(t *testing.T) {
		evs := respMustFeed(t, respChatConv(t, "", Options{}).Translator,
			`{"choices":[{"index":0,"delta":{"reasoning":"plan"}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"f","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
		out := respOutput(evs)
		if len(out) != 2 || out[0]["type"] != "reasoning" || out[1]["type"] != "function_call" {
			t.Errorf("%v", out)
		}
	})
	t.Run("hidden reasoning still yields an item", func(t *testing.T) {
		evs := respMustFeed(t, respChatConv(t, "", Options{}).Translator,
			`{"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":50,"completion_tokens_details":{"reasoning_tokens":40}}}`)
		out := respOutput(evs)
		if len(out) != 2 || out[1]["type"] != "reasoning" || respJSON(t, out[1]["summary"]) != `[]` {
			t.Errorf("%v", out)
		}
	})
	t.Run("reasoning after text opens a new item", func(t *testing.T) {
		evs := respMustFeed(t, respChatConv(t, "", Options{}).Translator,
			`{"choices":[{"index":0,"delta":{"reasoning_content":"a"}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"b"}}]}`,
			`{"choices":[{"index":0,"delta":{"reasoning_content":"c"}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"d"}}]}`)
		var kinds []string
		for _, o := range respOutput(evs) {
			kinds = append(kinds, o["type"].(string))
		}
		if strings.Join(kinds, ",") != "reasoning,message,reasoning" {
			t.Errorf("%v", kinds)
		}
	})
}
