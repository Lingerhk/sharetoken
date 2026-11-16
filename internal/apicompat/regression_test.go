package apicompat

import (
	"encoding/json"
	"strings"
	"testing"
)

// Regression tests for defects a review of the conversions found: each test
// names the behaviour that was wrong.

func regConvert(t *testing.T, from, to, body string) *Conversion {
	t.Helper()
	c, err := Convert(from, to, []byte(body), Options{Model: "m"})
	if err != nil {
		t.Fatalf("convert %s to %s: %v", from, to, err)
	}
	return c
}

// regStream runs events through a translator and closes it.
func regStream(t *testing.T, tr Translator, events ...string) (out [][]byte, err error) {
	t.Helper()
	emit := func(b []byte) error { out = append(out, append([]byte(nil), b...)); return nil }
	for _, e := range events {
		if err := tr.Event([]byte(e), emit); err != nil {
			return out, err
		}
	}
	return out, tr.Close(emit)
}

func regJoin(out [][]byte) string {
	var b strings.Builder
	for _, p := range out {
		b.Write(p)
		b.WriteByte('\n')
	}
	return b.String()
}

func regWant(t *testing.T, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("missing %s in\n%s", w, got)
		}
	}
}

func regNot(t *testing.T, got string, not ...string) {
	t.Helper()
	for _, n := range not {
		if strings.Contains(got, n) {
			t.Errorf("unexpected %s in\n%s", n, got)
		}
	}
}

// A chat request's built-in tools would run on the upstream's side: only
// function tools cross, and a tool_choice naming another tool is auto.
func TestChatConversionsKeepFunctionToolsOnly(t *testing.T) {
	body := `{"model":"x","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"web_search"},{"type":"code_execution"},{"type":"x_search","from_date":"2026-01-01"},{"type":"function","function":{"name":"f"}}],"tool_choice":{"type":"web_search"}}`
	got := string(regConvert(t, Chat, Messages, body).Body)
	regWant(t, got, `"tools":[{"input_schema":{"type":"object","properties":{}},"name":"f"}]`)
	regNot(t, got, "web_search", "code_execution", "x_search", "tool_choice")
	got = string(regConvert(t, Chat, Responses, body).Body)
	regWant(t, got, `"tool_choice":"auto"`, `"tools":[{"name":"f","parameters":{"type":"object","properties":{}},"strict":false,"type":"function"}]`)
	regNot(t, got, "web_search", "code_execution", "x_search")
}

// Some model servers give every parallel tool call index 0 and tell them
// apart by ID: they are separate calls, not one with both arguments.
func TestParallelCallsSharingAnIndex(t *testing.T) {
	events := []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"id_a","type":"function","function":{"name":"fa","arguments":"{\"a\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"id_b","type":"function","function":{"name":"fb","arguments":"{\"b\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"2}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`,
		`[DONE]`,
	}
	out, err := regStream(t, regConvert(t, Messages, Chat, `{"model":"x","max_tokens":500,"messages":[{"role":"user","content":"hi"}]}`).Translator, events...)
	if err != nil {
		t.Fatal(err)
	}
	agg, err := Aggregate(Messages, out)
	if err != nil {
		t.Fatal(err)
	}
	regWant(t, string(agg), `{"id":"id_a","input":{"a":1},"name":"fa","type":"tool_use"},{"id":"id_b","input":{"b":2},"name":"fb","type":"tool_use"}`)

	out, err = regStream(t, regConvert(t, Responses, Chat, `{"model":"x","input":"hi"}`).Translator, events...)
	if err != nil {
		t.Fatal(err)
	}
	last := string(out[len(out)-1])
	regWant(t, last, `"arguments":"{\"a\":1}","call_id":"id_a"`, `"arguments":"{\"b\":2}","call_id":"id_b"`)

	var raw [][]byte
	for _, e := range events {
		raw = append(raw, []byte(e))
	}
	agg, err = Aggregate(Chat, raw)
	if err != nil {
		t.Fatal(err)
	}
	regWant(t, string(agg), `{"id":"id_a","type":"function","function":{"name":"fa","arguments":"{\"a\":1}"}},{"id":"id_b","type":"function","function":{"name":"fb","arguments":"{\"b\":2}"}}`)
}

// A history that uses a tool call ID again (a model server numbering the
// calls of every answer from zero) keeps every call with its own result.
func TestRepeatedCallIDsPairInOrder(t *testing.T) {
	chat := `{"model":"x","messages":[{"role":"user","content":"hi"},
	 {"role":"assistant","tool_calls":[{"id":"call_0","type":"function","function":{"name":"f","arguments":"{\"n\":1}"}}]},
	 {"role":"tool","tool_call_id":"call_0","content":"r1"},
	 {"role":"assistant","tool_calls":[{"id":"call_0","type":"function","function":{"name":"f","arguments":"{\"n\":2}"}}]},
	 {"role":"tool","tool_call_id":"call_0","content":"r2"}],"tools":[{"type":"function","function":{"name":"f"}}]}`
	got := string(regConvert(t, Chat, Messages, chat).Body)
	regWant(t, got,
		`{"content":[{"id":"call_0","input":{"n":1},"name":"f","type":"tool_use"}],"role":"assistant"},{"content":[{"content":[{"text":"r1","type":"text"}],"tool_use_id":"call_0","type":"tool_result"}],"role":"user"}`,
		`{"content":[{"id":"call_0_2","input":{"n":2},"name":"f","type":"tool_use"}],"role":"assistant"},{"content":[{"content":[{"text":"r2","type":"text"}],"tool_use_id":"call_0_2","type":"tool_result"}],"role":"user"}`)
	got = string(regConvert(t, Chat, Responses, chat).Body)
	regWant(t, got, `{"arguments":"{\"n\":1}","call_id":"call_0","name":"f","type":"function_call"},{"call_id":"call_0","output":"r1","type":"function_call_output"},{"arguments":"{\"n\":2}","call_id":"call_0","name":"f","type":"function_call"},{"call_id":"call_0","output":"r2","type":"function_call_output"}`)

	resp := `{"model":"x","input":[{"type":"message","role":"user","content":"hi"},
	 {"type":"function_call","call_id":"call_0","name":"f","arguments":"{\"n\":1}"},
	 {"type":"function_call_output","call_id":"call_0","output":"r1"},
	 {"type":"function_call","call_id":"call_0","name":"f","arguments":"{\"n\":2}"},
	 {"type":"function_call_output","call_id":"call_0","output":"r2"}],"tools":[{"type":"function","name":"f"}]}`
	got = string(regConvert(t, Responses, Messages, resp).Body)
	regWant(t, got,
		`{"id":"call_0","input":{"n":1},"name":"f","type":"tool_use"}`, `"content":"r1","tool_use_id":"call_0","type":"tool_result"`,
		`{"id":"call_0_2","input":{"n":2},"name":"f","type":"tool_use"}`, `"content":"r2","tool_use_id":"call_0_2","type":"tool_result"`)
	got = string(regConvert(t, Responses, Chat, resp).Body)
	regWant(t, got,
		`"tool_calls":[{"id":"call_0","type":"function","function":{"name":"f","arguments":"{\"n\":1}"}}]},{"role":"tool","content":"r1","tool_call_id":"call_0"}`,
		`"tool_calls":[{"id":"call_0_2","type":"function","function":{"name":"f","arguments":"{\"n\":2}"}}]},{"role":"tool","content":"r2","tool_call_id":"call_0_2"}`)

	msgs := `{"model":"x","max_tokens":500,"messages":[{"role":"user","content":"hi"},
	 {"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"f","input":{"n":1}}]},
	 {"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"r1"}]},
	 {"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"f","input":{"n":2}}]},
	 {"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"r2"}]},
	 {"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"f","input":{"n":3}}]}]}`
	got = string(regConvert(t, Messages, Chat, msgs).Body)
	regWant(t, got,
		`"arguments":"{\"n\":1}"}}]},{"role":"tool","content":"r1","tool_call_id":"t1"}`,
		`"arguments":"{\"n\":2}"}}]},{"role":"tool","content":"r2","tool_call_id":"t1"}`)
	regNot(t, got, `{\"n\":3}`) // the third call has no result
	got = string(regConvert(t, Messages, Responses, msgs).Body)
	regWant(t, got, `"call_id":"t1","output":"r1"`, `"call_id":"t1","output":"r2"`)
	regNot(t, got, `{\"n\":3}`)
}

// A tool without input streams no arguments from a Messages upstream; a chat
// client parses the arguments as JSON, so they are "{}", not "".
func TestChatToolCallWithoutArguments(t *testing.T) {
	c := regConvert(t, Chat, Messages, `{"model":"x","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"now"}}]}`)
	out, err := regStream(t, c.Translator,
		`{"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"now","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`,
		`{"type":"message_stop"}`)
	if err != nil {
		t.Fatal(err)
	}
	regWant(t, regJoin(out), `"tool_calls":[{"function":{"arguments":"{}"},"index":0}]`)
	agg, err := Aggregate(Chat, out)
	if err != nil {
		t.Fatal(err)
	}
	regWant(t, string(agg), `"function":{"name":"now","arguments":"{}"}`)

	// A chat stream passed through as it is: the aggregate repairs it too.
	agg, err = Aggregate(Chat, [][]byte{[]byte(`{"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"now","arguments":""}}]},"finish_reason":"tool_calls"}]}`)})
	if err != nil {
		t.Fatal(err)
	}
	regWant(t, string(agg), `"function":{"name":"now","arguments":"{}"}`)
}

// A JSON schema for the answer crosses to the field the upstream API has for
// it: output_config.format, text.format, response_format.
func TestStructuredOutputAcrossAPIs(t *testing.T) {
	const schema = `{"type":"object","properties":{"a":{"type":"string"}},"required":["a"],"additionalProperties":false}`
	msgs := `{"model":"gpt-5","max_tokens":500,"messages":[{"role":"user","content":"hi"}],"output_config":{"effort":"low","format":{"type":"json_schema","schema":` + schema + `}}}`
	var m map[string]any
	if err := json.Unmarshal(regConvert(t, Messages, Responses, msgs).Body, &m); err != nil {
		t.Fatal(err)
	}
	format, _ := json.Marshal(m["text"])
	regWant(t, string(format), `"type":"json_schema"`, `"name":"answer"`, `"schema":{"additionalProperties":false,"properties":{"a":{"type":"string"}},"required":["a"],"type":"object"}`)
	m = nil
	if err := json.Unmarshal(regConvert(t, Messages, Chat, msgs).Body, &m); err != nil {
		t.Fatal(err)
	}
	format, _ = json.Marshal(m["response_format"])
	regWant(t, string(format), `{"json_schema":{"name":"answer","schema":{"additionalProperties":false,`, `"type":"json_schema"}`)

	got := string(regConvert(t, Responses, Messages, `{"model":"claude-x","max_output_tokens":500,"input":"hi","reasoning":{"effort":"low"},"text":{"format":{"type":"json_schema","name":"a","strict":true,"schema":`+schema+`}}}`).Body)
	regWant(t, got, `"output_config":{"effort":"low","format":{"schema":`+schema+`,"type":"json_schema"}}`)
	got = string(regConvert(t, Responses, Messages, `{"model":"claude-x","max_output_tokens":500,"input":"hi","text":{"format":{"type":"text"}}}`).Body)
	regNot(t, got, "output_config")

	// chat to Messages, with the effort an adaptive model keeps beside it.
	got = string(regConvert(t, Chat, Messages, `{"model":"claude-sonnet-5-5","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}],"response_format":{"type":"json_schema","json_schema":{"name":"a","schema":{"type":"object"}}}}`).Body)
	regWant(t, got, `"output_config":{"effort":"high","format":{"schema":{"type":"object"},"type":"json_schema"}}`)
	regNot(t, got, "Answer with")
}

// Anthropic refuses a stop sequence of whitespace only.
func TestChatToMessagesDropsBlankStops(t *testing.T) {
	got := string(regConvert(t, Chat, Messages, `{"model":"claude-x","messages":[{"role":"user","content":"hi"}],"stop":["\n","END"," \t"]}`).Body)
	regWant(t, got, `"stop_sequences":["END"]`)
	got = string(regConvert(t, Chat, Messages, `{"model":"claude-x","messages":[{"role":"user","content":"hi"}],"stop":"\n"}`).Body)
	regNot(t, got, "stop_sequences")
}

// The node computed the output limit from the buyer's reservation: a
// conversion raises it to the upstream API's own minimum at most.
func TestOutputLimitIsNotRaised(t *testing.T) {
	msgs := `{"model":"gpt-5","max_tokens":50,"messages":[{"role":"user","content":"hi"}]}`
	regWant(t, string(regConvert(t, Messages, Responses, msgs).Body), `"max_output_tokens":50,`)
	regWant(t, string(regConvert(t, Messages, Chat, msgs).Body), `"max_completion_tokens":50,`)
	regWant(t, string(regConvert(t, Messages, Chat, `{"model":"deepseek-chat","max_tokens":50,"messages":[{"role":"user","content":"hi"}]}`).Body), `"max_tokens":50,`)
	regWant(t, string(regConvert(t, Messages, Responses, `{"model":"gpt-5","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`).Body), `"max_output_tokens":16,`)
}

// chat to Responses repairs the tool call pairing as the other request
// conversions do, and an image a tool returned reaches the model.
func TestChatToResponsesPairsToolCalls(t *testing.T) {
	got := string(regConvert(t, Chat, Responses, `{"model":"gpt-5","messages":[{"role":"user","content":"hi"},
	 {"role":"assistant","content":"looking","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}},{"id":"c2","type":"function","function":{"name":"f","arguments":"{}"}},{"id":"c3","type":"function","function":{"name":"g","arguments":"{}"}}]},
	 {"role":"user","content":"wait"},
	 {"role":"tool","tool_call_id":"c2","content":"two"},
	 {"role":"tool","tool_call_id":"c1","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]},
	 {"role":"tool","tool_call_id":"orphan","content":"x"}]}`).Body)
	var m struct {
		Input []map[string]any `json:"input"`
	}
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, it := range m.Input {
		s, _ := it["type"].(string)
		if id, _ := it["call_id"].(string); id != "" {
			s += ":" + id
		} else if role, _ := it["role"].(string); role != "" {
			s += ":" + role
		}
		order = append(order, s)
	}
	// c3 has no output and the orphan no call; the user's "wait" and the
	// image come after the outputs.
	if want := "message:user message:assistant function_call:c1 function_call:c2 function_call_output:c1 function_call_output:c2 message:user message:user"; strings.Join(order, " ") != want {
		t.Fatalf("got  %s\nwant %s\n%s", strings.Join(order, " "), want, got)
	}
	regWant(t, got, `{"call_id":"c1","output":"`+respMediaMarker+`","type":"function_call_output"}`,
		`{"content":[{"image_url":"data:image/png;base64,AAAA","type":"input_image"}],"role":"user","type":"message"},{"content":"wait","role":"user","type":"message"}`)
	regNot(t, got, "orphan", "(empty)", "c3")
}

// A tool call the output limit or a content filter cut off is not a call to
// run: a Responses client gets it as incomplete, a Messages client never gets
// stop_reason tool_use for it, nor a call it had not seen yet.
func TestCutOffToolCalls(t *testing.T) {
	c := regConvert(t, Responses, Messages, `{"model":"claude-x","max_output_tokens":500,"input":"hi","tools":[{"type":"function","name":"shell","parameters":{"type":"object"}},{"type":"custom","name":"apply_patch"}]}`)
	out, err := regStream(t, c.Translator,
		`{"type":"message_start","message":{"id":"m","model":"claude","usage":{"input_tokens":10}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"shell","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":\"ls\"}"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_2","name":"shell","input":{}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":\"ls"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":30}}`,
		`{"type":"message_stop"}`)
	if err != nil {
		t.Fatal(err)
	}
	got := regJoin(out)
	regWant(t, got, `"call_id":"toolu_1","id":"`, `"status":"incomplete","type":"function_call"},"output_index":1,`, `"type":"response.incomplete"`)
	if n := strings.Count(got, `"type":"response.function_call_arguments.done"`); n != 1 {
		t.Errorf("arguments.done for %d calls, want only the complete one\n%s", n, got)
	}
	var done struct {
		Response struct {
			Output []struct{ CallID, Status string }
		}
	}
	if err := json.Unmarshal(out[len(out)-1], &done); err != nil {
		t.Fatal(err)
	}
	if o := done.Response.Output; len(o) != 2 || o[0].Status != "completed" || o[1].Status != "incomplete" {
		t.Errorf("output %+v", o)
	}

	// Messages client, chat upstream: a content filter ends the answer in
	// the middle of the first call, a second one has not started.
	tr := regConvert(t, Messages, Chat, `{"model":"x","max_tokens":500,"messages":[{"role":"user","content":"hi"}]}`).Translator
	out, err = regStream(t, tr,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c0","function":{"name":"a","arguments":"{\"x\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"c1","function":{"name":"b","arguments":"{\"y\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
	if err != nil {
		t.Fatal(err)
	}
	got = regJoin(out)
	regWant(t, got, `"stop_reason":"refusal"`, `"type":"message_stop"`)
	regNot(t, got, `"stop_reason":"tool_use"`, `"id":"c1"`)

	// The output limit, Responses upstream: the same.
	tr = regConvert(t, Messages, Responses, `{"model":"gpt-5","max_tokens":500,"messages":[{"role":"user","content":"hi"}]}`).Translator
	out, err = regStream(t, tr,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs"}}`,
		`{"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"plan"}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"call_1","name":"f","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"a\":"}`,
		`{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"usage":{"input_tokens":5,"output_tokens":7}}}`)
	if err != nil {
		t.Fatal(err)
	}
	got = regJoin(out)
	regWant(t, got, `"stop_reason":"max_tokens"`, `"thinking":"plan"`)
	regNot(t, got, `call_1`, `tool_use`)

	// A complete call is still a call when the answer ends on its own.
	tr = regConvert(t, Messages, Chat, `{"model":"x","max_tokens":500,"messages":[{"role":"user","content":"hi"}]}`).Translator
	out, err = regStream(t, tr,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c0","function":{"name":"a","arguments":"{}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
	if err != nil {
		t.Fatal(err)
	}
	regWant(t, regJoin(out), `"stop_reason":"tool_use"`, `"id":"c0"`)
}

// With thinking on, Anthropic wants the assistant turn of a tool loop to
// start with a thinking block: a chat client that sent none back gets the
// turn without thinking instead of a refused request.
func TestChatToMessagesThinkingInToolLoop(t *testing.T) {
	const head = `{"model":"claude-sonnet-4-6","max_tokens":8000,"reasoning_effort":"high","tools":[{"type":"function","function":{"name":"f"}}],"messages":[{"role":"user","content":"hi"},`
	const call = `"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"r1"}`
	regNot(t, string(regConvert(t, Chat, Messages, head+`{"role":"assistant","content":null,`+call+`]}`).Body), `"thinking":{`)
	regWant(t, string(regConvert(t, Chat, Messages, head+`{"role":"assistant","content":null,"anthropic_thinking":[{"type":"thinking","thinking":"t","signature":"s"}],`+call+`]}`).Body),
		`"thinking":{"budget_tokens":4000,"type":"enabled"}`)
	// A new user turn after the loop: thinking again.
	regWant(t, string(regConvert(t, Chat, Messages, head+`{"role":"assistant","content":null,`+call+`,{"role":"assistant","content":"done"},{"role":"user","content":"more"}]}`).Body),
		`"thinking":{"budget_tokens":4000,"type":"enabled"}`)
}

// A tool_result outside a user turn is read by no converter: it does not
// pair, so its call is left out too (no call without a result, no message
// without a role).
func TestMessagesToolResultOutsideUserTurn(t *testing.T) {
	body := `{"model":"x","max_tokens":500,"messages":[{"role":"user","content":"hi"},
	 {"role":"assistant","content":[{"type":"text","text":"ok"},{"type":"tool_use","id":"t1","name":"f","input":{}}]},
	 {"role":"system","content":[{"type":"tool_result","tool_use_id":"t1","content":"r"}]},
	 {"role":"user","content":"go"}]}`
	got := string(regConvert(t, Messages, Chat, body).Body)
	regWant(t, got, `{"role":"user","content":"hi"},{"role":"assistant","content":"ok"},{"role":"user","content":"go"}`)
	regNot(t, got, `"role":""`, "tool_calls")
	got = string(regConvert(t, Messages, Responses, body).Body)
	regNot(t, got, "function_call")

	// msgChatOrder alone: a call whose reply is missing goes, with a message
	// that held nothing else.
	out := msgChatOrder([]msgChatMsg{{Role: "user", Content: "hi"}, {Role: "assistant", ToolCalls: []msgChatCall{{ID: "gone", Type: "function"}}}, {Role: "user", Content: "go"}})
	if len(out) != 2 || out[0].Role != "user" || out[1].Role != "user" {
		t.Errorf("%+v", out)
	}
}

// Half an answer is not the answer: a failed Responses stream and a Messages
// stream cut off before its end do not aggregate.
func TestAggregateFailsOnFailedOrCutStreams(t *testing.T) {
	if b, err := Aggregate(Responses, [][]byte{[]byte(`{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"boom"},"output":[]}}`)}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("failed response: %s, %v", b, err)
	}
	start := []byte(`{"type":"message_start","message":{"id":"m","model":"c","usage":{"input_tokens":1}}}`)
	block := []byte(`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"par"}}`)
	if b, err := Aggregate(Messages, [][]byte{start, block}); err == nil {
		t.Errorf("cut stream: %s", b)
	}
	b, err := Aggregate(Messages, [][]byte{start, block, []byte(`{"type":"content_block_stop","index":0}`),
		[]byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`), []byte(`{"type":"message_stop"}`)})
	if err != nil || !strings.Contains(string(b), `"text":"par"`) {
		t.Errorf("whole stream: %s, %v", b, err)
	}
}

// A chat stream that brings only a role and [DONE] is an empty stream for a
// Responses client too: an error and no event, not an empty response.
func TestResponsesFromChatEmptyStream(t *testing.T) {
	tr := regConvert(t, Responses, Chat, `{"model":"x","input":"hi"}`).Translator
	out, err := regStream(t, tr, `{"choices":[{"index":0,"delta":{"role":"assistant"}}]}`, `[DONE]`)
	if err == nil || len(out) != 0 {
		t.Fatalf("err %v, events:\n%s", err, regJoin(out))
	}
	// An answer that is empty but ended is still an answer.
	tr = regConvert(t, Responses, Chat, `{"model":"x","input":"hi"}`).Translator
	out, err = regStream(t, tr, `{"choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":"stop"}]}`, `[DONE]`)
	if err != nil || !strings.Contains(regJoin(out), `"type":"response.completed"`) || !strings.HasPrefix(string(out[0]), `{"response":`) {
		t.Fatalf("err %v, events:\n%s", err, regJoin(out))
	}
}
