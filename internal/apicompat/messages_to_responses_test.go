package apicompat

import (
	"reflect"
	"strings"
	"testing"
)

func TestMessagesToResponsesRequestBasics(t *testing.T) {
	body := msgReq(t, "gpt-5.5", []any{msgUser("hello")}, map[string]any{
		"temperature": 0.5, "top_p": 0.9, "top_k": 5, "stop_sequences": []string{"END"},
		"context_management": map[string]any{"edits": []any{}},
	})
	m, c := msgConvert(t, Responses, body, Options{Model: "claude-sonnet-5-5"})
	if c.Translator == nil {
		t.Fatal("no translator")
	}
	want := map[string]any{
		"model": "gpt-5.5", "stream": true, "store": false,
		"max_output_tokens": 4096.0,
		"include":           []any{"reasoning.encrypted_content"},
		"reasoning":         map[string]any{"effort": "medium", "summary": "auto"},
		"input": []any{map[string]any{"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": "hello"}}}},
	}
	for k, v := range want {
		if !reflect.DeepEqual(m[k], v) {
			t.Errorf("%s = %v, want %v", k, m[k], v)
		}
	}
	// Sampling parameters go nowhere for a reasoning model; top_k, stop
	// sequences and Anthropic-only fields have no Responses field.
	for _, k := range []string{"temperature", "top_p", "top_k", "stop", "stop_sequences", "context_management", "metadata", "max_tokens", "tools", "tool_choice", "parallel_tool_calls"} {
		if _, ok := m[k]; ok {
			t.Errorf("unexpected %s", k)
		}
	}
}

func TestMessagesToResponsesSampling(t *testing.T) {
	for _, tc := range []struct {
		model     string
		sampling  bool
		reasoning bool
	}{
		{"gpt-5.5", false, true},
		{"gpt-6-astra", false, true},
		{"o3", false, true},
		{"o4-mini", false, true},
		{"gpt-4.1", true, false},
		{"gpt-4o", true, false},
		{"grok-4", true, true},
		{"qwen3-coder", true, true},
	} {
		t.Run(tc.model, func(t *testing.T) {
			m, _ := msgConvert(t, Responses, msgReq(t, tc.model, []any{msgUser("x")}, map[string]any{"temperature": 0.2, "top_p": 0.3}), Options{})
			if _, ok := m["temperature"]; ok != tc.sampling {
				t.Errorf("temperature present = %v", ok)
			}
			if _, ok := m["top_p"]; ok != tc.sampling {
				t.Errorf("top_p present = %v", ok)
			}
			if _, ok := m["reasoning"]; ok != tc.reasoning {
				t.Errorf("reasoning present = %v", ok)
			}
			if _, ok := m["include"]; ok != tc.reasoning {
				t.Errorf("include present = %v", ok)
			}
		})
	}
}

func TestMessagesToResponsesMaxTokensFloor(t *testing.T) {
	for _, tc := range []struct{ in, want float64 }{{10, 16}, {16, 16}, {17, 17}, {128, 128}, {128000, 128000}} {
		m, _ := msgConvert(t, Responses, msgReq(t, "gpt-5.5", []any{msgUser("x")}, map[string]any{"max_tokens": tc.in}), Options{})
		if m["max_output_tokens"] != tc.want {
			t.Errorf("max_tokens %v → %v, want %v", tc.in, m["max_output_tokens"], tc.want)
		}
	}
}

func TestMessagesToResponsesEffort(t *testing.T) {
	for _, tc := range []struct {
		name    string
		extra   map[string]any
		effort  string
		summary bool
	}{
		{"default", nil, "medium", true},
		{"enabled without effort", map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": 10000}}, "medium", true},
		{"small budget", map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": 1024}}, "low", true},
		{"large budget", map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": 32000}}, "high", true},
		{"adaptive", map[string]any{"thinking": map[string]any{"type": "adaptive", "display": "omitted"}}, "medium", true},
		{"output effort", map[string]any{"output_config": map[string]any{"effort": "high"}}, "high", true},
		{"output effort low", map[string]any{"thinking": map[string]any{"type": "adaptive"}, "output_config": map[string]any{"effort": "low"}}, "low", true},
		{"max is xhigh", map[string]any{"output_config": map[string]any{"effort": "max"}}, "xhigh", true},
		{"effort beats budget", map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": 1024}, "output_config": map[string]any{"effort": "high"}}, "high", true},
		{"output config without effort", map[string]any{"output_config": map[string]any{}}, "medium", true},
		{"disabled", map[string]any{"thinking": map[string]any{"type": "disabled"}}, "none", false},
		{"disabled beats effort", map[string]any{"thinking": map[string]any{"type": "disabled"}, "output_config": map[string]any{"effort": "high"}}, "none", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := msgConvert(t, Responses, msgReq(t, "gpt-5.5", []any{msgUser("x")}, tc.extra), Options{})
			r := m["reasoning"].(map[string]any)
			if r["effort"] != tc.effort {
				t.Errorf("effort %v, want %v", r["effort"], tc.effort)
			}
			if _, ok := r["summary"]; ok != tc.summary {
				t.Errorf("summary present = %v", ok)
			}
			mc, _ := msgConvert(t, Chat, msgReq(t, "gpt-5.5", []any{msgUser("x")}, tc.extra), Options{})
			if mc["reasoning_effort"] != tc.effort {
				t.Errorf("chat reasoning_effort %v, want %v", mc["reasoning_effort"], tc.effort)
			}
		})
	}
}

func TestMessagesToResponsesSystem(t *testing.T) {
	for _, tc := range []struct {
		name   string
		system any
		want   []string
	}{
		{"string", "Be brief.", []string{"Be brief."}},
		{"empty string", "", nil},
		{"blocks", []any{
			map[string]any{"type": "text", "text": "x-anthropic-billing-header: cc_version=2.1.325.405;"},
			map[string]any{"type": "text", "text": "One.", "cache_control": map[string]any{"type": "ephemeral"}},
			map[string]any{"type": "text", "text": ""},
			map[string]any{"type": "text", "text": "Two."},
		}, []string{"One.", "Two."}},
		{"billing header only", "x-anthropic-billing-header: cc_version=1", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := msgConvert(t, Responses, msgReq(t, "gpt-5.5", []any{msgUser("hi")}, map[string]any{"system": tc.system}), Options{})
			items := msgItems(m)
			var got []string
			if items[0]["role"] == "developer" {
				for _, p := range items[0]["content"].([]any) {
					got = append(got, p.(map[string]any)["text"].(string))
				}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("developer texts %q, want %q", got, tc.want)
			}
			if strings.Contains(msgJSON(t, m), "cache_control") || strings.Contains(msgJSON(t, m), "billing-header") {
				t.Error("cache_control or billing header leaked")
			}
		})
	}
}

func TestMessagesToResponsesSystemMessageAmongTurns(t *testing.T) {
	body := msgReq(t, "gpt-5.5", []any{
		msgUser("first"),
		map[string]any{"role": "system", "content": "# Environment\ncwd: /x"},
		map[string]any{"role": "system", "content": []any{map[string]any{"type": "text", "text": "reminder"}}},
		msgUser("second"),
	}, nil)
	m, _ := msgConvert(t, Responses, body, Options{})
	var got []string
	for _, it := range msgItems(m) {
		got = append(got, it["role"].(string)+":"+it["content"].([]any)[0].(map[string]any)["text"].(string))
	}
	want := []string{"user:first", "developer:# Environment\ncwd: /x", "developer:reminder", "user:second"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("items %q, want %q", got, want)
	}
}

func TestMessagesToResponsesToolRound(t *testing.T) {
	body := msgReq(t, "gpt-5.5", []any{
		msgUser("weather?"),
		msgAssistant([]any{
			map[string]any{"type": "text", "text": "Checking."},
			map[string]any{"type": "tool_use", "id": "toolu_1", "name": "get_weather", "input": map[string]any{"city": "Tokyo"}},
			map[string]any{"type": "tool_use", "id": "toolu_2", "name": "get_time", "input": map[string]any{}},
		}),
		msgUser([]any{
			map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": "sunny"},
			map[string]any{"type": "tool_result", "tool_use_id": "toolu_2", "content": []any{
				map[string]any{"type": "text", "text": "a"}, map[string]any{"type": "text", "text": "b"}}, "is_error": true},
			map[string]any{"type": "text", "text": "thanks"},
		}),
	}, nil)
	m, _ := msgConvert(t, Responses, body, Options{})
	got := msgItems(m)
	want := []any{
		map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "weather?"}}},
		map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "Checking."}}},
		map[string]any{"type": "function_call", "call_id": "toolu_1", "name": "get_weather", "arguments": `{"city":"Tokyo"}`},
		map[string]any{"type": "function_call", "call_id": "toolu_2", "name": "get_time", "arguments": `{}`},
		map[string]any{"type": "function_call_output", "call_id": "toolu_1", "output": "sunny"},
		map[string]any{"type": "function_call_output", "call_id": "toolu_2", "output": "Error: a\n\nb"},
		map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "thanks"}}},
	}
	if msgJSON(t, got) != msgJSON(t, want) {
		t.Errorf("items\n%s\nwant\n%s", msgJSON(t, got), msgJSON(t, want))
	}
}

func TestMessagesToolPairingRepair(t *testing.T) {
	tu := func(id string) map[string]any {
		return map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": "ls"}}
	}
	tr := func(id string) map[string]any {
		return map[string]any{"type": "tool_result", "tool_use_id": id, "content": "ok"}
	}
	for _, tc := range []struct {
		name     string
		messages []any
		calls    []string // call ids of the calls kept
		outputs  []string // and of the outputs
	}{
		{"well formed", []any{msgUser("go"), msgAssistant([]any{tu("a")}), msgUser([]any{tr("a")})}, []string{"a"}, []string{"a"}},
		{"orphan result (trimmed history)", []any{msgUser([]any{tr("gone"), map[string]any{"type": "text", "text": "go on"}})}, nil, nil},
		{"parallel, one result missing", []any{msgUser("go"), msgAssistant([]any{tu("a"), tu("b")}), msgUser([]any{tr("a")})}, []string{"a"}, []string{"a"}},
		{"dangling call", []any{msgUser("go"), msgAssistant([]any{map[string]any{"type": "text", "text": "x"}, tu("a")}), msgUser("next")}, nil, nil},
		{"multi round", []any{msgUser("go"), msgAssistant([]any{tu("a")}), msgUser([]any{tr("a")}), msgAssistant([]any{tu("b")}), msgUser([]any{tr("b")})}, []string{"a", "b"}, []string{"a", "b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := msgReq(t, "gpt-5.5", tc.messages, nil)
			m, _ := msgConvert(t, Responses, body, Options{})
			var calls, outputs []string
			for _, it := range msgItems(m) {
				switch it["type"] {
				case "function_call":
					calls = append(calls, it["call_id"].(string))
				case "function_call_output":
					outputs = append(outputs, it["call_id"].(string))
				}
			}
			if !reflect.DeepEqual(calls, tc.calls) || !reflect.DeepEqual(outputs, tc.outputs) {
				t.Errorf("responses calls %v outputs %v, want %v %v", calls, outputs, tc.calls, tc.outputs)
			}
			mc, _ := msgConvert(t, Chat, body, Options{})
			calls, outputs = nil, nil
			for _, msg := range msgChatMsgs(mc) {
				for _, c := range asSlice(msg["tool_calls"]) {
					calls = append(calls, c.(map[string]any)["id"].(string))
				}
				if msg["role"] == "tool" {
					outputs = append(outputs, msg["tool_call_id"].(string))
				}
			}
			if !reflect.DeepEqual(calls, tc.calls) || !reflect.DeepEqual(outputs, tc.outputs) {
				t.Errorf("chat calls %v outputs %v, want %v %v", calls, outputs, tc.calls, tc.outputs)
			}
		})
	}
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func TestMessagesToResponsesMedia(t *testing.T) {
	img := func(src map[string]any) map[string]any { return map[string]any{"type": "image", "source": src} }
	for _, tc := range []struct {
		name  string
		block map[string]any
		want  map[string]any
	}{
		{"base64 image", img(map[string]any{"type": "base64", "media_type": "image/jpeg", "data": "AAA"}),
			map[string]any{"type": "input_image", "image_url": "data:image/jpeg;base64,AAA"}},
		{"image without media type", img(map[string]any{"type": "base64", "data": "AAA"}),
			map[string]any{"type": "input_image", "image_url": "data:image/png;base64,AAA"}},
		{"url image", img(map[string]any{"type": "url", "url": "https://x/y.png"}),
			map[string]any{"type": "input_image", "image_url": "https://x/y.png"}},
		{"pdf", map[string]any{"type": "document", "title": "spec.pdf", "source": map[string]any{"type": "base64", "media_type": "application/pdf", "data": "JVBE"}},
			map[string]any{"type": "input_file", "filename": "spec.pdf", "file_data": "data:application/pdf;base64,JVBE"}},
		{"pdf url", map[string]any{"type": "document", "source": map[string]any{"type": "url", "url": "https://x/a.pdf"}},
			map[string]any{"type": "input_file", "file_url": "https://x/a.pdf"}},
		{"text document", map[string]any{"type": "document", "title": "notes", "source": map[string]any{"type": "text", "media_type": "text/plain", "data": "body"}},
			map[string]any{"type": "input_text", "text": "notes\n\nbody"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := msgConvert(t, Responses, msgReq(t, "gpt-5.5", []any{msgUser([]any{tc.block, map[string]any{"type": "text", "text": "what is it?"}})}, nil), Options{})
			parts := msgItems(m)[0]["content"].([]any)
			if msgJSON(t, parts[0]) != msgJSON(t, tc.want) {
				t.Errorf("part %s, want %s", msgJSON(t, parts[0]), msgJSON(t, tc.want))
			}
		})
	}
}

func TestMessagesToResponsesToolResultImage(t *testing.T) {
	body := msgReq(t, "gpt-5.5", []any{
		msgUser("shot"),
		msgAssistant([]any{map[string]any{"type": "tool_use", "id": "toolu_s", "name": "Screenshot", "input": map[string]any{}}}),
		msgUser([]any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_s", "content": []any{
			map[string]any{"type": "text", "text": "here"},
			map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": "PNG"}},
		}}}),
		msgAssistant("ok"),
		msgUser([]any{map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": "ONLY"}}}),
	}, nil)
	m, _ := msgConvert(t, Responses, body, Options{})
	items := msgItems(m)
	if items[2]["type"] != "function_call_output" || items[2]["output"] != "here" {
		t.Fatalf("output item %v", items[2])
	}
	want := `{"content":[{"image_url":"data:image/png;base64,PNG","type":"input_image"}],"role":"user","type":"message"}`
	if msgJSON(t, items[3]) != want {
		t.Errorf("image of the tool result: %s", msgJSON(t, items[3]))
	}
	if items[4]["role"] != "assistant" || msgJSON(t, items[5]["content"]) != `[{"image_url":"data:image/png;base64,ONLY","type":"input_image"}]` {
		t.Errorf("image-only user message: %v", items[5])
	}
}

func TestMessagesToResponsesEmptyContent(t *testing.T) {
	body := msgReq(t, "gpt-5.5", []any{
		msgUser([]any{map[string]any{"type": "text", "text": ""}, map[string]any{"type": "text", "text": "real"}}),
		msgAssistant([]any{map[string]any{"type": "text", "text": ""}}),
		msgUser([]any{map[string]any{"type": "tool_result", "tool_use_id": "nobody", "content": ""}}),
		msgUser("again"),
	}, nil)
	m, _ := msgConvert(t, Responses, body, Options{})
	items := msgItems(m)
	if len(items) != 2 || msgJSON(t, items[0]["content"]) != `[{"text":"real","type":"input_text"}]` {
		t.Errorf("items %s", msgJSON(t, items))
	}
}

func TestMessagesToResponsesThinkingHistory(t *testing.T) {
	ours := msgSignaturePrefix + "gAAAAenc"
	body := msgReq(t, "gpt-5.5", []any{
		msgUser("q"),
		msgAssistant([]any{
			map[string]any{"type": "thinking", "thinking": "plan", "signature": ours},
			map[string]any{"type": "thinking", "thinking": "", "signature": msgSignaturePrefix + "hidden"},
			map[string]any{"type": "thinking", "thinking": "anthropic", "signature": "EqQBCkgIBRABGAIiQL"},
			map[string]any{"type": "thinking", "thinking": "unsigned", "signature": ""},
			map[string]any{"type": "redacted_thinking", "data": "EmwKAhgB"},
			map[string]any{"type": "text", "text": "answer"},
		}),
		msgUser("q2"),
	}, nil)
	m, _ := msgConvert(t, Responses, body, Options{})
	items := msgItems(m)
	want := []any{
		map[string]any{"type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "plan"}}, "encrypted_content": "gAAAAenc"},
		map[string]any{"type": "reasoning", "summary": []any{}, "encrypted_content": "hidden"},
		map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "answer"}}},
	}
	if msgJSON(t, items[1:4]) != msgJSON(t, want) {
		t.Errorf("assistant items\n%s\nwant\n%s", msgJSON(t, items[1:4]), msgJSON(t, want))
	}
	if strings.Contains(msgJSON(t, m), "EqQB") || strings.Contains(msgJSON(t, m), "unsigned") || strings.Contains(msgJSON(t, m), "EmwK") {
		t.Error("foreign or unsigned thinking was sent")
	}
}

func TestMessagesToResponsesTools(t *testing.T) {
	body := msgReq(t, "gpt-5.5", []any{msgUser("x")}, map[string]any{
		"tools": []any{
			map[string]any{"name": "Bash", "description": "run", "input_schema": map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}, "required": []any{"command"}}, "cache_control": map[string]any{"type": "ephemeral"}},
			map[string]any{"name": "NoProps", "input_schema": map[string]any{"type": "object"}},
			map[string]any{"name": "NoSchema"},
			map[string]any{"name": "Deferred", "input_schema": map[string]any{"type": "object", "properties": map[string]any{}}, "defer_loading": true},
			map[string]any{"type": "web_search_20250305", "name": "web_search", "max_uses": 5},
			map[string]any{"type": "bash_20250124", "name": "bash"},
		},
	})
	m, _ := msgConvert(t, Responses, body, Options{})
	want := `[{"description":"run","name":"Bash","parameters":{"properties":{"command":{"type":"string"}},"required":["command"],"type":"object"},"strict":false,"type":"function"},` +
		`{"description":"","name":"NoProps","parameters":{"properties":{},"type":"object"},"strict":false,"type":"function"},` +
		`{"description":"","name":"NoSchema","parameters":{"properties":{},"type":"object"},"strict":false,"type":"function"},` +
		`{"description":"","name":"Deferred","parameters":{"properties":{},"type":"object"},"strict":false,"type":"function"}]`
	if got := msgJSON(t, m["tools"]); got != want {
		t.Errorf("tools\n%s\nwant\n%s", got, want)
	}
	if m["parallel_tool_calls"] != true {
		t.Error("parallel_tool_calls")
	}
	if _, ok := m["tool_choice"]; ok {
		t.Error("tool_choice without one asked for")
	}
}

func TestMessagesToolChoice(t *testing.T) {
	tools := []any{map[string]any{"name": "Bash", "input_schema": map[string]any{"type": "object", "properties": map[string]any{}}}}
	for _, tc := range []struct {
		name       string
		choice     any
		responses  string // JSON, "" for none
		chat       string
		parallel   bool
		withoutAny bool // no tools in the request
	}{
		{"auto", map[string]any{"type": "auto"}, `"auto"`, `"auto"`, true, false},
		{"any", map[string]any{"type": "any"}, `"required"`, `"required"`, true, false},
		{"none", map[string]any{"type": "none"}, `"none"`, `"none"`, true, false},
		{"tool", map[string]any{"type": "tool", "name": "Bash"}, `{"name":"Bash","type":"function"}`, `{"function":{"name":"Bash"},"type":"function"}`, true, false},
		{"undeclared tool", map[string]any{"type": "tool", "name": "Nope"}, "", "", true, false},
		{"no parallel", map[string]any{"type": "auto", "disable_parallel_tool_use": true}, `"auto"`, `"auto"`, false, false},
		{"unknown", map[string]any{"type": "weird"}, "", "", true, false},
		{"no tools", map[string]any{"type": "any"}, "", "", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			extra := map[string]any{"tool_choice": tc.choice, "tools": tools}
			if tc.withoutAny {
				delete(extra, "tools")
			}
			for _, to := range []string{Responses, Chat} {
				m, _ := msgConvert(t, to, msgReq(t, "gpt-5.5", []any{msgUser("x")}, extra), Options{})
				want := tc.responses
				if to == Chat {
					want = tc.chat
				}
				got := ""
				if v, ok := m["tool_choice"]; ok {
					got = msgJSON(t, v)
				}
				if got != want {
					t.Errorf("%s tool_choice %s, want %s", to, got, want)
				}
				if !tc.withoutAny && m["parallel_tool_calls"] != tc.parallel {
					t.Errorf("%s parallel_tool_calls %v", to, m["parallel_tool_calls"])
				}
				if tc.withoutAny {
					if _, ok := m["parallel_tool_calls"]; ok {
						t.Errorf("%s parallel_tool_calls without tools", to)
					}
				}
			}
		})
	}
}

func TestMessagesPromptCacheKey(t *testing.T) {
	jsonID := `{"device_id":"dev1","account_uuid":"","session_id":"3bd63dd9-7681-4236-886b-f6c53f0e5919"}`
	legacy := "user_" + strings.Repeat("a", 64) + "_account__session_3bd63dd9-7681-4236-886b-f6c53f0e5919"
	cached := []any{map[string]any{"type": "text", "text": "sys", "cache_control": map[string]any{"type": "ephemeral"}}}
	key := func(extra map[string]any, msgs []any) string {
		m, _ := msgConvert(t, Responses, msgReq(t, "gpt-5.5", msgs, extra), Options{})
		s, _ := m["prompt_cache_key"].(string)
		return s
	}
	a := key(map[string]any{"metadata": map[string]any{"user_id": jsonID}}, []any{msgUser("one")})
	b := key(map[string]any{"metadata": map[string]any{"user_id": jsonID}}, []any{msgUser("two"), msgAssistant("x"), msgUser("three")})
	if a == "" || a != b || !strings.HasPrefix(a, "cc-") || strings.Contains(a, "3bd63dd9") {
		t.Errorf("session key %q / %q", a, b)
	}
	if l := key(map[string]any{"metadata": map[string]any{"user_id": legacy}}, []any{msgUser("one")}); l == "" || l == a {
		t.Errorf("legacy key %q", l)
	}
	c1 := key(map[string]any{"system": cached}, []any{msgUser("one")})
	c2 := key(map[string]any{"system": cached}, []any{msgUser("one"), msgAssistant("x"), msgUser("two")})
	if c1 == "" || c1 != c2 {
		t.Errorf("cache_control key %q / %q", c1, c2)
	}
	if k := key(map[string]any{"system": "plain"}, []any{msgUser("one")}); k != "" {
		t.Errorf("key without a session or cache marks: %q", k)
	}
	// Chat has no prompt_cache_key (strict chat upstreams reject unknown fields).
	m, _ := msgConvert(t, Chat, msgReq(t, "gpt-5.5", []any{msgUser("x")}, map[string]any{"metadata": map[string]any{"user_id": jsonID}}), Options{})
	if _, ok := m["prompt_cache_key"]; ok || strings.Contains(msgJSON(t, m), "dev1") {
		t.Error("chat body carries the session")
	}
}

func TestMessagesLongToolNames(t *testing.T) {
	long := "mcp__a_rather_long_server_name__and_an_even_longer_tool_name_for_testing"
	body := msgReq(t, "gpt-5.5", []any{
		msgUser("x"),
		msgAssistant([]any{map[string]any{"type": "tool_use", "id": "toolu_l", "name": long, "input": map[string]any{}}}),
		msgUser([]any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_l", "content": "ok"}}),
	}, map[string]any{
		"tools":       []any{map[string]any{"name": long, "input_schema": map[string]any{"type": "object", "properties": map[string]any{}}}},
		"tool_choice": map[string]any{"type": "tool", "name": long},
	})
	m, c := msgConvert(t, Responses, body, Options{})
	short := m["tools"].([]any)[0].(map[string]any)["name"].(string)
	if len(short) != 64 || !strings.HasPrefix(short, long[:55]) {
		t.Fatalf("short name %q", short)
	}
	if m["tool_choice"].(map[string]any)["name"] != short || msgItems(m)[1]["name"] != short {
		t.Error("tool_choice or history keeps the long name")
	}
	s := msgFeed(t, c.Translator,
		`{"type":"response.created","response":{"id":"resp_1","model":"gpt-5.5"}}`,
		msgJSON(t, map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": "function_call", "call_id": "call_1", "name": short}}),
		`{"type":"response.function_call_arguments.done","output_index":0,"arguments":"{}"}`,
		`{"type":"response.completed","response":{"status":"completed"}}`,
	)
	s.lifecycle(t)
	if got := s.blocks(t)[0]["name"]; got != long {
		t.Errorf("client tool name %v", got)
	}
	mc, cc := msgConvert(t, Chat, body, Options{})
	if mc["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["name"] != short {
		t.Error("chat tool name not shortened")
	}
	sc := msgFeed(t, cc.Translator,
		msgJSON(t, map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call_1", "function": map[string]any{"name": short, "arguments": "{}"}}}}}}}),
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	if got := sc.blocks(t)[0]["name"]; got != long {
		t.Errorf("chat client tool name %v", got)
	}
}

func TestMessagesBadRequest(t *testing.T) {
	for _, body := range []string{`{`, `{"messages":[]}`, `{"model":"m","messages":[{"role":"user","content":42}]}`, `{"model":"m","messages":[],"tool_choice":"auto","tools":[{"name":"a"}]}`} {
		for _, to := range []string{Responses, Chat} {
			if _, err := Convert(Messages, to, []byte(body), Options{}); err == nil {
				t.Errorf("%s accepted %s", to, body)
			}
		}
	}
	if !Supported(Messages, Responses) || !Supported(Messages, Chat) {
		t.Error("conversions not registered")
	}
	if got, ok := Target(Messages, []string{Chat, Responses}); !ok || got != Responses {
		t.Errorf("target %s", got)
	}
}
