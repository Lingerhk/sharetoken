package apicompat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// respCaptured loads a captured client request body, as the node hands it to
// a converter: hosted tools, store and client_metadata stay (the converter
// must cope), and max_output_tokens is set.
func respCaptured(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "captured", name))
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(capture.Body), &body); err != nil {
		t.Fatal(err)
	}
	body["max_output_tokens"] = 32000
	b, _ := json.Marshal(body)
	return b
}

var respCapturedCodex = []string{
	"codex-0.144.6-turn1.json",
	"codex-0.144.6-compaction-turn.json",
	"codex-0.144.6-after-compaction.json",
}

func TestResponsesCapturedCodexToMessages(t *testing.T) {
	for _, name := range respCapturedCodex {
		t.Run(name, func(t *testing.T) {
			body := respCaptured(t, name)
			c, err := Convert(Responses, Messages, body, Options{Model: "gpt-5.5"})
			if err != nil {
				t.Fatal(err)
			}
			m := respBody(t, c)
			msgs := respMsgs(t, m)
			respCheckMessagesSendable(t, msgs)

			var req map[string]any
			_ = json.Unmarshal(body, &req)
			if m["model"] != req["model"] || respNum(m, "max_tokens") != 32000 {
				t.Errorf("model/max_tokens: %v %v", m["model"], m["max_tokens"])
			}
			sys := m["system"].([]any)
			if len(sys) != 2 || sys[0].(map[string]any)["text"] != req["instructions"] ||
				!strings.HasPrefix(sys[1].(map[string]any)["text"].(string), "<permissions instructions>") {
				t.Errorf("system: instructions, then the developer message")
			}
			if msgs[0]["role"] != "user" || !strings.Contains(respJSON(t, msgs[0]), "<environment_context>") {
				t.Errorf("first message: %v", msgs[0])
			}
			// medium effort turns thinking on, unless a tool loop without a thinking block is open
			if req["reasoning"].(map[string]any)["effort"] != "medium" || m["output_config"].(map[string]any)["effort"] != "medium" {
				t.Errorf("effort")
			}
			s := string(c.Body)
			for _, leak := range []string{"client_metadata", "prompt_cache_key", "web_search", "x-codex", `"ENC1"`, `"store"`} {
				if strings.Contains(s, leak) {
					t.Errorf("%s reached the upstream request", leak)
				}
			}
			tools, _ := m["tools"].([]any)
			names := map[string]map[string]any{}
			for _, x := range tools {
				names[x.(map[string]any)["name"].(string)] = x.(map[string]any)
			}
			if list, _ := req["tools"].([]any); len(list) > 0 {
				patch := names["apply_patch"]
				if patch == nil || !respJSONEqual(json.RawMessage(respJSON(t, patch["input_schema"])), json.RawMessage(respCustomInputSchema)) ||
					!strings.Contains(patch["description"].(string), "begin_patch") {
					t.Errorf("apply_patch must be a function with an input string: %v", patch)
				}
				if names["tool_search"] == nil || names["exec_command"] == nil {
					t.Errorf("tools: %v", tools)
				}
			}
			if name == "codex-0.144.6-compaction-turn.json" {
				last := respBlocks(msgs[len(msgs)-1])
				if !strings.Contains(last[len(last)-1]["text"].(string), "CONTEXT CHECKPOINT COMPACTION") {
					t.Errorf("the compaction request is the last user text: %v", last)
				}
				if !strings.Contains(s, `"tool_use_id":"call_cap1"`) || m["thinking"] != nil {
					t.Errorf("the tool round is kept; thinking stays off (no thinking block to continue the loop)")
				}
			}
			if name == "codex-0.144.6-after-compaction.json" &&
				!strings.Contains(respJSON(t, msgs[len(msgs)-1]), "Another language model started") {
				t.Errorf("the handoff summary is kept")
			}
		})
	}
}

func TestResponsesCapturedCodexToChat(t *testing.T) {
	for _, name := range respCapturedCodex {
		t.Run(name, func(t *testing.T) {
			body := respCaptured(t, name)
			c, err := Convert(Responses, Chat, body, Options{Model: "gpt-5.5"})
			if err != nil {
				t.Fatal(err)
			}
			m := respBody(t, c)
			msgs := respChatMsgs(t, m)
			respCheckChatSendable(t, msgs)
			var req map[string]any
			_ = json.Unmarshal(body, &req)
			if msgs[0]["role"] != "system" || !strings.HasPrefix(msgs[0]["content"].(string), req["instructions"].(string)) ||
				!strings.Contains(msgs[0]["content"].(string), "<permissions instructions>") {
				t.Errorf("system message: instructions and the developer message")
			}
			if m["reasoning_effort"] != "medium" || respNum(m, "max_tokens") != 32000 {
				t.Errorf("top level: %v %v", m["reasoning_effort"], m["max_tokens"])
			}
			s := string(c.Body)
			for _, leak := range []string{"client_metadata", "web_search", "x-codex", `"store"`} {
				if strings.Contains(s, leak) {
					t.Errorf("%s reached the upstream request", leak)
				}
			}
			if list, _ := req["tools"].([]any); len(list) > 0 {
				found := false
				for _, x := range m["tools"].([]any) {
					fn := x.(map[string]any)["function"].(map[string]any)
					if fn["name"] == "apply_patch" {
						found = true
						if !respJSONEqual(json.RawMessage(respJSON(t, fn["parameters"])), json.RawMessage(respCustomInputSchema)) {
							t.Errorf("apply_patch parameters %v", fn["parameters"])
						}
					}
				}
				if !found || m["parallel_tool_calls"] != req["parallel_tool_calls"] {
					t.Errorf("apply_patch missing or parallel_tool_calls lost")
				}
			}
			if name == "codex-0.144.6-compaction-turn.json" {
				var call map[string]any
				for _, msg := range msgs {
					if msg["tool_calls"] != nil {
						call = msg
					}
				}
				if call == nil || call["reasoning_content"] != "Read it." {
					t.Errorf("the reasoning summary goes back with its tool call: %v", call)
				}
			}
		})
	}
}

// A full Codex turn through a Messages upstream: the model calls apply_patch
// and the client gets a custom_tool_call with the patch as input.
func TestResponsesCapturedCodexApplyPatchRoundTrip(t *testing.T) {
	body := respCaptured(t, "codex-0.144.6-turn1.json")
	patch := "*** Begin Patch\n*** Add File: hello.txt\n+hello\n*** End Patch\n"
	args, _ := json.Marshal(map[string]string{"input": patch})
	argsJSON, _ := json.Marshal(string(args))

	c, err := Convert(Responses, Messages, body, Options{Model: "gpt-5.5"})
	if err != nil {
		t.Fatal(err)
	}
	evs := respMustFeed(t, c.Translator,
		`{"type":"message_start","message":{"id":"msg_1","model":"claude","content":[],"usage":{"input_tokens":9000,"cache_read_input_tokens":12000,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Create the file."}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig=="}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_01","name":"apply_patch","input":{}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":`+string(argsJSON)+`}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":80}}`,
		`{"type":"message_stop"}`,
	)
	out := respOutput(evs)
	if len(out) != 2 || out[1]["type"] != "custom_tool_call" || out[1]["name"] != "apply_patch" || out[1]["input"] != patch || out[1]["call_id"] != "toolu_01" {
		t.Fatalf("output: %v", out)
	}
	u := respUsageOf(evs)
	if respNum(u, "input_tokens") != 21000 || respNum(u, "input_tokens_details", "cached_tokens") != 12000 || respNum(u, "output_tokens") != 80 {
		t.Errorf("usage %v", u)
	}

	// Codex's next request carries the call and its output back; the upstream
	// sees its own tool_use, signed thinking first.
	var req map[string]any
	_ = json.Unmarshal(body, &req)
	input := req["input"].([]any)
	input = append(input, out[0], out[1], map[string]any{"type": "custom_tool_call_output", "call_id": "toolu_01", "output": "Success. Updated the following files:\nA hello.txt\n"})
	req["input"] = input
	next, _ := json.Marshal(req)
	c2, err := Convert(Responses, Messages, next, Options{})
	if err != nil {
		t.Fatal(err)
	}
	m := respBody(t, c2)
	msgs := respMsgs(t, m)
	respCheckMessagesSendable(t, msgs)
	asst := respBlocks(msgs[len(msgs)-2])
	if asst[0]["type"] != "thinking" || asst[0]["signature"] != "sig==" || asst[1]["name"] != "apply_patch" ||
		respJSON(t, asst[1]["input"]) != string(args) {
		t.Errorf("assistant turn: %v", asst)
	}
	if m["thinking"] == nil {
		t.Errorf("thinking stays on")
	}
}

// The same through a chat upstream, with tool_search.
func TestResponsesCapturedCodexChatToolSearchRoundTrip(t *testing.T) {
	body := respCaptured(t, "codex-0.144.6-after-compaction.json")
	c, err := Convert(Responses, Chat, body, Options{})
	if err != nil {
		t.Fatal(err)
	}
	evs := respMustFeed(t, c.Translator,
		`{"id":"x","model":"glm","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_s","function":{"name":"tool_search","arguments":"{\"query\":\"cmux\"}"}}]}}]}`,
		`{"id":"x","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_p","function":{"name":"apply_patch","arguments":"{\"input\":\"*** Begin Patch\\n*** End Patch\\n\"}"}}]},"finish_reason":"tool_calls"}]}`,
		`{"id":"x","choices":[],"usage":{"prompt_tokens":20000,"completion_tokens":40,"prompt_tokens_details":{"cached_tokens":19000}}}`,
		`[DONE]`,
	)
	out := respOutput(evs)
	if len(out) != 2 || out[0]["type"] != "tool_search_call" || out[0]["execution"] != "client" ||
		respJSON(t, out[0]["arguments"]) != `{"query":"cmux"}` || out[1]["type"] != "custom_tool_call" || out[1]["input"] != "*** Begin Patch\n*** End Patch\n" {
		t.Fatalf("output %v", out)
	}
	if respTerminal(evs)["model"] != "gpt-5.5" {
		t.Errorf("the model defaults to the request's")
	}
}
