package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sharetoken/internal/node"
	"sharetoken/internal/protocol"
)

// sseServer answers every request with the events, as an SSE stream.
func sseServer(t *testing.T, events ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range events {
			fmt.Fprintf(w, "data: %s\n\n", ev)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func discard([]byte) error { return nil }

// A refusal that only quotes the buyer's text teaches no limit, however much
// the text looks like one; neither does a number no model has for a limit;
// and a limit that was learnt is asked for again after a day.
func TestOutputLimitIsNotLearntFromQuotedText(t *testing.T) {
	var sent []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		sent = append(sent, req)
		if effort, _ := req["reasoning_effort"].(string); effort != "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":{"message":"Invalid value: '%s'. Supported values are: 'low', 'medium', and 'high'."}}`, effort)
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	up := &openAIUpstream{base: srv.URL, header: http.Header{}}
	for _, quoted := range []string{
		"max_ at most 1",
		"max_tokens is too large: 4096. This model supports at most 2000 completion tokens",
		"max_tokens: 4096 > 1500, which is the maximum allowed number of output tokens",
	} {
		body, _ := json.Marshal(map[string]any{"model": "m", "max_tokens": 4096, "messages": []any{}, "reasoning_effort": quoted})
		if err := up.Stream(context.Background(), &node.Request{API: protocol.APIChat, Body: body}, discard); err == nil {
			t.Fatalf("%q: the refusal was lost", quoted)
		}
		if got := up.limits.of("m"); got != 0 {
			t.Fatalf("%q: learnt the limit %d from the buyer's own text", quoted, got)
		}
	}
	sent = nil
	if err := up.Stream(context.Background(), &node.Request{API: protocol.APIChat, Body: []byte(`{"model":"m","max_tokens":4096,"messages":[]}`)}, discard); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || sent[0]["max_tokens"].(float64) != 4096 {
		t.Fatalf("the next buyer's request: %v", sent)
	}

	// Too small to be a model's limit, or not below what was asked for.
	for _, refusal := range []string{
		"the valid range of max_tokens is [1, 512]",
		"the valid range of max_tokens is [1, 8192]",
	} {
		l, calls := &outputLimits{}, 0
		_ = l.retry(func() error {
			calls++
			return &node.StatusError{Status: 400, Body: refusal}
		}, []byte(`{"model":"m","max_tokens":4096}`))
		if calls != 1 || l.of("m") != 0 {
			t.Fatalf("%q: %d calls, limit %d", refusal, calls, l.of("m"))
		}
	}

	// A learnt limit lasts a day.
	l := &outputLimits{set: map[string]uint64{"m": 32768}}
	l.learn("m", 8192)
	if l.of("m") != 8192 {
		t.Fatalf("learnt limit %d", l.of("m"))
	}
	now := timeNow
	defer func() { timeNow = now }()
	timeNow = func() time.Time { return now().Add(learnedLimitTTL + time.Minute) }
	if l.of("m") != 32768 {
		t.Fatalf("limit after a day %d, want the configured one", l.of("m"))
	}
}

// A stream that stops before its terminal event was cut on the way: an
// error, not an answer the seller bills.
func TestStreamsWithoutTheirEndFail(t *testing.T) {
	stream := func(api, body string, events ...string) error {
		srv := sseServer(t, events...)
		up := &openAIUpstream{base: srv.URL, header: http.Header{}, responses: true}
		return up.Stream(context.Background(), &node.Request{API: api, Body: []byte(body)}, discard)
	}
	const (
		created = `{"type":"response.created","response":{"id":"r1"}}`
		delta   = `{"type":"response.output_text.delta","delta":"hel"}`
		chunk   = `{"choices":[{"index":0,"delta":{"content":"hel"},"finish_reason":null}]}`
	)
	resp := `{"model":"m","input":"x","max_output_tokens":100}`
	if err := stream(protocol.APIResponses, resp, created, delta); err == nil {
		t.Error("a Responses stream without its terminal event was accepted")
	}
	for _, end := range []string{"response.completed", "response.incomplete", "response.done"} {
		if err := stream(protocol.APIResponses, resp, created, delta, `{"type":"`+end+`","response":{"id":"r1"}}`); err != nil {
			t.Errorf("%s: %v", end, err)
		}
	}
	chat := `{"model":"m","messages":[],"max_tokens":100}`
	if err := stream(protocol.APIChat, chat, chunk); err == nil {
		t.Error("a chat stream without finish_reason or [DONE] was accepted")
	}
	if err := stream(protocol.APIChat, chat, chunk, `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`); err != nil {
		t.Errorf("finish_reason: %v", err)
	}
	if err := stream(protocol.APIChat, chat, chunk, "[DONE]"); err != nil {
		t.Errorf("[DONE]: %v", err)
	}
}

// The Codex backend's stream ending before its terminal event is an error
// too: no response.completed is made up for it.
func TestCodexStreamEndingEarlyFails(t *testing.T) {
	srv := sseServer(t,
		`{"type":"response.created","sequence_number":0,"response":{"id":"r1","status":"in_progress"}}`,
		`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"message","id":"msg_1"}}`,
		`{"type":"response.output_text.delta","sequence_number":2,"delta":"partial"}`)
	var got []string
	err := streamCodex(context.Background(), srv.URL, http.Header{}, []byte(`{"model":"m","input":[],"max_output_tokens":50}`), func(p []byte) error {
		got = append(got, string(p))
		return nil
	})
	if err == nil || len(got) != 3 {
		t.Fatalf("err %v, events %v", err, got)
	}
}

// The cut at the job's output limit ends the stream as the API ends an
// answer that reached max_output_tokens: the open item closed and in the
// response's output with status incomplete, then response.incomplete.
func TestCodexCutClosesTheOpenItem(t *testing.T) {
	long, _ := json.Marshal(strings.Repeat("word ", 300))
	cut := func(events ...string) (types []string, all []map[string]any) {
		t.Helper()
		srv := sseServer(t, append(events, `{"type":"response.completed","response":{"id":"r1"}}`)...)
		body := `{"model":"m","max_output_tokens":50,"tools":[{"type":"function","name":"python"}],"input":[]}`
		err := streamCodex(context.Background(), srv.URL, http.Header{}, []byte(body), func(p []byte) error {
			if strings.Contains(string(p), reservedToolAlias) {
				t.Errorf("reserved name leaked: %s", p)
			}
			var ev map[string]any
			if err := json.Unmarshal(p, &ev); err != nil {
				t.Fatalf("not JSON: %s", p)
			}
			types, all = append(types, ev["type"].(string)), append(all, ev)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		for i := 1; i < len(all); i++ {
			if all[i]["sequence_number"].(float64) <= all[i-1]["sequence_number"].(float64) {
				t.Fatalf("sequence numbers not rising at %d: %v", i, types)
			}
		}
		return types, all
	}
	const created = `{"type":"response.created","sequence_number":0,"response":{"id":"r1","status":"in_progress","output":[]}}`

	// Mid-message, after a reasoning item that was finished.
	types, all := cut(created,
		`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"reasoning","id":"rs_1"}}`,
		`{"type":"response.output_item.done","sequence_number":2,"output_index":0,"item":{"type":"reasoning","id":"rs_1","encrypted_content":"E"}}`,
		`{"type":"response.output_item.added","sequence_number":3,"output_index":1,"item":{"type":"message","id":"msg_1","role":"assistant","status":"in_progress","content":[]}}`,
		`{"type":"response.content_part.added","sequence_number":4,"output_index":1,"content_index":0,"item_id":"msg_1","part":{"type":"output_text","text":""}}`,
		`{"type":"response.output_text.delta","sequence_number":5,"output_index":1,"content_index":0,"item_id":"msg_1","delta":`+string(long)+`}`,
		`{"type":"response.output_text.delta","sequence_number":6,"output_index":1,"content_index":0,"item_id":"msg_1","delta":"never sent"}`)
	want := "response.created,response.output_item.added,response.output_item.done,response.output_item.added,response.content_part.added," +
		"response.output_text.delta,response.output_text.done,response.content_part.done,response.output_item.done,response.incomplete"
	if strings.Join(types, ",") != want {
		t.Fatalf("events %v", types)
	}
	n := len(all)
	text := strings.Repeat("word ", 300)
	if all[n-4]["text"] != text || all[n-4]["item_id"] != "msg_1" || all[n-4]["output_index"].(float64) != 1 {
		t.Fatalf("output_text.done: %v", all[n-4])
	}
	item := all[n-2]["item"].(map[string]any)
	if item["status"] != "incomplete" || item["content"].([]any)[0].(map[string]any)["text"] != text {
		t.Fatalf("item done: %v", item)
	}
	resp := all[n-1]["response"].(map[string]any)
	out := resp["output"].([]any)
	if resp["status"] != "incomplete" || resp["incomplete_details"].(map[string]any)["reason"] != "max_output_tokens" ||
		len(out) != 2 || out[1].(map[string]any)["status"] != "incomplete" {
		t.Fatalf("response: %v", resp)
	}
	if got := uint64(resp["usage"].(map[string]any)["output_tokens"].(float64)); got != protocol.EstimateTokens(text) {
		t.Fatalf("output tokens %d, want %d", got, protocol.EstimateTokens(text))
	}

	// Mid-function-call: its arguments so far, under the buyer's tool name.
	types, all = cut(created,
		`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"c1","name":"python__st","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":2,"output_index":0,"item_id":"fc_1","delta":`+string(long)+`}`)
	if strings.Join(types, ",") != "response.created,response.output_item.added,response.function_call_arguments.delta,"+
		"response.function_call_arguments.done,response.output_item.done,response.incomplete" {
		t.Fatalf("events %v", types)
	}
	item = all[len(all)-2]["item"].(map[string]any)
	if item["name"] != "python" || item["arguments"] != text || item["status"] != "incomplete" {
		t.Fatalf("function call: %v", item)
	}

	// Cut while reasoning: no item to give, and the output is a list still.
	_, all = cut(created,
		`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"reasoning","id":"rs_1"}}`,
		`{"type":"response.reasoning_summary_text.delta","sequence_number":2,"output_index":0,"item_id":"rs_1","delta":`+string(long)+`}`)
	raw, _ := json.Marshal(all[len(all)-1]["response"].(map[string]any)["output"])
	if all[len(all)-1]["type"] != "response.incomplete" || string(raw) != "[]" {
		t.Fatalf("output %s in %v", raw, all[len(all)-1])
	}

	// The running count is the estimate of the whole text.
	var c tokenCounter
	var whole strings.Builder
	for _, piece := range []string{"hello ", "世界", "", "こんにちは wor", "ld 한국어", "x"} {
		c.add(piece)
		whole.WriteString(piece)
		if c.tokens() != protocol.EstimateTokens(whole.String()) {
			t.Fatalf("count %d after %q, want %d", c.tokens(), whole.String(), protocol.EstimateTokens(whole.String()))
		}
	}
}

// What another seller's account encrypted is taken out when the backend
// refuses it, for a compact request as for a normal one: first the reasoning
// items, then the backend's own compaction items. A compaction item
// sharetoken made is readable and stays.
func TestCodexRetriesWithoutForeignEncryptedItems(t *testing.T) {
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sent = append(sent, string(b))
		if strings.Contains(string(b), "FOREIGN") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"The encrypted content could not be verified.","code":"invalid_encrypted_content"}}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/compact") {
			_, _ = io.WriteString(w, `{"output":[{"type":"compaction","encrypted_content":"OPAQUE"}]}`)
			return
		}
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\"}}\n\n")
	}))
	defer srv.Close()
	up := &codexUpstream{base: srv.URL, ts: FixedToken(&Token{AccessToken: "a"})}
	user := `{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}`
	reasoning := `{"model":"m","input":[{"type":"reasoning","encrypted_content":"FOREIGN-R"},` + user + `]}`

	if err := up.Compact(context.Background(), &node.Request{API: protocol.APIResponses, Op: protocol.OpCompact, Body: []byte(reasoning)}, discard); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if len(sent) != 2 || strings.Contains(sent[1], `"reasoning"`) || !strings.Contains(sent[1], "input_text") {
		t.Fatalf("compact requests: %v", sent)
	}

	sent = nil
	both := `{"model":"m","input":[{"type":"compaction","encrypted_content":"FOREIGN-C"},{"type":"reasoning","encrypted_content":"own"},` + user + `]}`
	if err := up.Stream(context.Background(), &node.Request{API: protocol.APIResponses, Body: []byte(both)}, discard); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(sent) != 3 || !strings.Contains(sent[1], "FOREIGN-C") || strings.Contains(sent[1], `"reasoning"`) || strings.Contains(sent[2], `"compaction"`) {
		t.Fatalf("stream requests: %v", sent)
	}

	own := `{"input":[{"type":"compaction","encrypted_content":"sharetoken-compaction-v1:c3VtbWFyeQ=="},{"type":"compaction_summary","encrypted_content":"gAAA"},` + user + `]}`
	stripped, ok := withoutForeignCompaction([]byte(own))
	if !ok || !strings.Contains(string(stripped), "sharetoken-compaction-v1:") || strings.Contains(string(stripped), "gAAA") {
		t.Fatalf("stripped %s", stripped)
	}
	if _, ok := withoutForeignCompaction([]byte(`{"input":[` + user + `]}`)); ok {
		t.Fatal("nothing to strip, yet changed")
	}
}

// Without its thinking a request cannot ask to clear thinking either.
func TestWithoutThinkingDropsEditsThatNeedIt(t *testing.T) {
	const messages = `"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"x","signature":"s"},{"type":"text","text":"a"}]}]`
	out, ok := withoutThinking([]byte(`{"thinking":{"type":"enabled","budget_tokens":2000},
		"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"},{"type":"clear_tool_uses_20250919"}]},` + messages + `}`))
	if !ok || strings.Contains(string(out), "clear_thinking") || strings.Contains(string(out), `"thinking"`) || !strings.Contains(string(out), "clear_tool_uses_20250919") {
		t.Fatalf("%v %s", ok, out)
	}
	out, _ = withoutThinking([]byte(`{"context_management":{"edits":[{"type":"clear_thinking_20251015"}]},` + messages + `}`))
	if strings.Contains(string(out), "context_management") {
		t.Fatalf("empty context_management left: %s", out)
	}
}

// DeepSeek's Messages endpoint keeps the limit it learnt: only the first
// request that asks too much is sent twice.
func TestDeepSeekMessagesKeepTheLearntLimit(t *testing.T) {
	var asked []float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		n, _ := req["max_tokens"].(float64)
		asked = append(asked, n)
		if n > 8192 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"Invalid max_tokens value, the valid range of max_tokens is [1, 8192]"}}`)
			return
		}
		for _, ev := range messagesOK {
			fmt.Fprintf(w, "data: %s\n\n", ev)
		}
	}))
	defer srv.Close()
	e := &Entry{ID: "d2", Name: "deepseek", Type: KindDeepSeek, Auth: AuthAPIKey, APIKey: "sk-ds", BaseURL: srv.URL}
	up, err := e.Upstream(nil)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		body := `{"model":"deepseek-chat","max_tokens":32000,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
		if err := up.Stream(context.Background(), &node.Request{API: protocol.APIMessages, Body: []byte(body)}, discard); err != nil {
			t.Fatal(err)
		}
	}
	if len(asked) != 4 || asked[0] != 32000 || asked[1] != 8192 || asked[3] != 8192 {
		t.Fatalf("asked for %v", asked)
	}
}

// The Codex backend takes its input as a list: a string becomes one user message.
func TestCodexStringInputBecomesAMessage(t *testing.T) {
	req, _, _, err := codexRequest([]byte(`{"model":"m","input":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := asJSON(t, req["input"]); got != `[{"content":[{"text":"hello","type":"input_text"}],"role":"user","type":"message"}]` {
		t.Fatalf("input %s", got)
	}
}
