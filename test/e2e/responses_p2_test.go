package e2e

// Responses clients beyond Codex's plain HTTP: previous_response_id over
// HTTP (the OpenAI Agents SDK), the standard event set for clients that are
// not Codex, and Codex's interrupt on its WebSocket. One seller, a ChatGPT
// login, in front of a mock backend that answers like the real one and adds
// its own events.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"sharetoken/internal/buyer"
	"sharetoken/internal/coordinator"
	"sharetoken/internal/node"
	"sharetoken/internal/provider"
)

// codexMock is a ChatGPT backend: a request whose last input item is not a
// tool's output gets reasoning and a call, one with the output gets the
// answer, with the backend's own events around them and a new item id in
// every event (as GitHub Copilot's /responses gives). A request saying SLOW
// streams on until the caller hangs up.
type codexMock struct {
	mu         sync.Mutex
	inputs     [][]string // each request's input item types
	turnStates []string   // each request's x-codex-turn-state
	hungUp     chan struct{}
}

func (c *codexMock) serve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Input []map[string]any `json:"input"`
	}
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &req)
	var types []string
	for _, it := range req.Input {
		types = append(types, fmt.Sprint(it["type"]))
	}
	c.mu.Lock()
	c.inputs = append(c.inputs, types)
	c.turnStates = append(c.turnStates, r.Header.Get("x-codex-turn-state"))
	n := len(c.inputs)
	c.mu.Unlock()
	w.Header().Set("x-codex-turn-state", fmt.Sprintf("ts-%d", n))
	created := `{"type":"response.created","response":{"id":"resp_up` + fmt.Sprint(n) + `","object":"response","status":"in_progress","model":"gpt-x","output":[]}}`
	if bytes.Contains(raw, []byte("SLOW")) {
		sse(w, true, created,
			`{"type":"response.output_item.added","output_index":0,"item":{"id":"msg_s","type":"message","role":"assistant","content":[]}}`,
			`{"type":"response.output_text.delta","output_index":0,"item_id":"msg_s","content_index":0,"delta":"Thinking about it"}`)
		// It goes on writing, as a model does, until the seller hangs up.
		for i := 0; i < 300; i++ {
			select {
			case <-r.Context().Done():
				close(c.hungUp)
				return
			case <-time.After(100 * time.Millisecond):
			}
			fmt.Fprintf(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"item_id\":\"msg_s\",\"content_index\":0,\"delta\":\".\"}\n\n")
			w.(http.Flusher).Flush()
		}
		return
	}
	extras := []string{`{"type":"codex.rate_limits","primary":{"used_percent":1}}`, `{"type":"keepalive"}`}
	var ev []string
	if last := types[len(types)-1]; last != "function_call_output" {
		rs := `{"id":"rs_1","type":"reasoning","summary":[],"encrypted_content":"ENC-1"}`
		fc := `{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"lookup","arguments":"{}"}`
		ev = append([]string{created}, extras...)
		ev = append(ev,
			`{"type":"response.output_item.added","output_index":0,"item":`+rs+`}`,
			`{"type":"response.output_item.done","output_index":0,"item":`+rs+`}`,
			`{"type":"response.output_item.added","output_index":1,"item":`+fc+`}`,
			`{"type":"response.output_item.done","output_index":1,"item":`+fc+`}`,
			`{"type":"response.completed","response":{"id":"resp_up`+fmt.Sprint(n)+`","object":"response","status":"completed","model":"gpt-x","output":[`+rs+`,`+fc+`],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`)
	} else {
		msg := func(id string) string {
			return `{"id":"` + id + `","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"42","annotations":[]}]}`
		}
		ev = append([]string{created}, extras...)
		ev = append(ev,
			`{"type":"response.output_item.added","output_index":0,"item":`+msg("msg_a")+`}`,
			`{"type":"response.output_text.delta","output_index":0,"item_id":"msg_b","content_index":0,"delta":"42"}`,
			`{"type":"response.metadata","metadata":{"x":"y"}}`,
			`{"type":"response.output_item.done","output_index":0,"item":`+msg("msg_c")+`}`,
			`{"type":"response.completed","response":{"id":"resp_up`+fmt.Sprint(n)+`","object":"response","status":"completed","model":"gpt-x","output":[`+msg("msg_d")+`],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`)
	}
	for i := range ev {
		ev[i] = strings.Replace(ev[i], `{"type":`, fmt.Sprintf(`{"sequence_number":%d,"type":`, i), 1)
	}
	sse(w, true, ev...)
}

func (c *codexMock) lastInput() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inputs[len(c.inputs)-1]
}

// newCodexMarket is a market with one ChatGPT seller in front of mock, and
// the buyer's local endpoint.
func newCodexMarket(t *testing.T, mock *codexMock) (*market, string) {
	m := newMarketWith(t, coordinator.Config{WarmUp: -1, DisputeWindow: time.Second})
	srv := httptest.NewServer(http.HandlerFunc(mock.serve))
	t.Cleanup(srv.Close)
	tok := &provider.Token{AccessToken: "at-1", AccountID: "acct-1"}
	e := &provider.Entry{ID: "codex", Name: "codex", Type: provider.KindOpenAI, Auth: provider.AuthOAuth, OAuth: tok, BaseURL: srv.URL + "/codex"}
	up, err := e.Upstream(provider.FixedToken(tok))
	if err != nil {
		t.Fatal(err)
	}
	m.addNodeCfg(srv.URL, priceIn, priceOut, nil, func(c *node.Config) {
		c.Upstream = up
		c.Models[0].UpstreamModel = "gpt-x"
	})
	m.waitOffers(1)
	cl, _, _ := m.addBuyer(50_000_000)
	proxy := httptest.NewServer((&buyer.Proxy{Client: cl, BudgetMicros: 1_000_000}).Handler())
	t.Cleanup(proxy.Close)
	return m, proxy.URL
}

// postResponses posts a Responses request as a client that is not Codex,
// and returns the status, the SSE events (type and data) or the JSON answer.
func postResponses(t *testing.T, endpoint, key string, req map[string]any) (int, []string, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(req)
	hr, _ := http.NewRequest(http.MethodPost, endpoint+"/v1/responses", bytes.NewReader(b))
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Authorization", "Bearer "+key)
	hr.Header.Set("User-Agent", "OpenAI/Python 2.1.0")
	resp, err := http.DefaultClient.Do(hr)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, nil, body
	}
	var lines []string
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	for sc.Scan() {
		if l := sc.Text(); l != "" {
			lines = append(lines, l)
		}
	}
	return resp.StatusCode, lines, nil
}

// lastResponse is the response of a stream's terminal event.
func lastResponse(t *testing.T, lines []string) map[string]any {
	t.Helper()
	for i := len(lines) - 1; i >= 0; i-- {
		if d, ok := strings.CutPrefix(lines[i], "data: "); ok {
			var ev map[string]any
			_ = json.Unmarshal([]byte(d), &ev)
			if ev["type"] == "response.completed" {
				return ev["response"].(map[string]any)
			}
		}
	}
	t.Fatalf("no response.completed in %v", lines)
	return nil
}

// A client sends a turn's new input only and names the response it follows:
// the seller gets the whole conversation, streamed or not, and a response
// the proxy does not keep (or another key's) is refused as OpenAI refuses it.
func TestResponsesPreviousResponseIDOverHTTP(t *testing.T) {
	mock := &codexMock{hungUp: make(chan struct{})}
	_, endpoint := newCodexMarket(t, mock)
	for _, stream := range []bool{false, true} {
		first := map[string]any{"model": model, "stream": stream,
			"input": []any{map[string]any{"type": "message", "role": "user", "content": "What is the answer?"}},
			"tools": []any{map[string]any{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object"}}}}
		status, lines, body := postResponses(t, endpoint, "k1", first)
		if status != http.StatusOK {
			t.Fatalf("stream=%v first turn: %d %v", stream, status, body)
		}
		if stream {
			body = lastResponse(t, lines)
		}
		id, _ := body["id"].(string)
		if id == "" {
			t.Fatalf("stream=%v: no response id in %v", stream, body)
		}
		next := map[string]any{"model": model, "stream": stream, "previous_response_id": id,
			"input": []any{map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "42"}}}
		if status, _, body := postResponses(t, endpoint, "k1", next); status != http.StatusOK {
			t.Fatalf("stream=%v second turn: %d %v", stream, status, body)
		}
		if got := strings.Join(mock.lastInput(), ","); got != "message,reasoning,function_call,function_call_output" {
			t.Fatalf("stream=%v: the seller got %s, want the whole conversation", stream, got)
		}
		status, _, body = postResponses(t, endpoint, "k2", next)
		if e, _ := body["error"].(map[string]any); status != http.StatusBadRequest || e["code"] != "previous_response_not_found" {
			t.Fatalf("stream=%v another key's response: %d %v", stream, status, body)
		}
	}
	next := map[string]any{"model": model, "previous_response_id": "resp_nope", "input": "hi"}
	if status, _, body := postResponses(t, endpoint, "k1", next); status != http.StatusBadRequest {
		t.Fatalf("unknown response: %d %v", status, body)
	}
}

// A client that is not Codex gets the standard events only, each item under
// one id; Codex gets the backend's events as they came.
func TestResponsesEventsForOtherClients(t *testing.T) {
	mock := &codexMock{hungUp: make(chan struct{})}
	_, endpoint := newCodexMarket(t, mock)
	req := map[string]any{"model": model, "stream": true, "store": false,
		"input": []any{map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "42"}}}
	_, lines, _ := postResponses(t, endpoint, "k", req)
	all := strings.Join(lines, "\n")
	for _, gone := range []string{"codex.rate_limits", "response.metadata", "event: keepalive", "msg_b", "msg_c", "msg_d"} {
		if strings.Contains(all, gone) {
			t.Errorf("%s reached a client that is not Codex:\n%s", gone, all)
		}
	}
	if !strings.Contains(all, `"item_id":"msg_a"`) || lastResponse(t, lines)["output"].([]any)[0].(map[string]any)["id"] != "msg_a" {
		t.Errorf("ids not made one:\n%s", all)
	}

	b, _ := json.Marshal(req)
	hr, _ := http.NewRequest(http.MethodPost, endpoint+"/v1/responses", bytes.NewReader(b))
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("originator", "codex_cli_rs")
	resp, err := http.DefaultClient.Do(hr)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	for _, kept := range []string{"codex.rate_limits", "response.metadata", "msg_b"} {
		if !strings.Contains(string(raw), kept) {
			t.Errorf("%s did not reach Codex:\n%s", kept, raw)
		}
	}
}

// Codex interrupts the response in flight on its WebSocket: the job stops at
// once (the backend sees the hang-up), Codex gets the interrupt accepted and
// the response ended as interrupted, and the socket goes on serving. A turn's
// second request carries the turn state the backend gave the first.
func TestResponsesWebSocketInterrupt(t *testing.T) {
	mock := &codexMock{hungUp: make(chan struct{})}
	m, endpoint := newCodexMarket(t, mock)
	url := "ws" + strings.TrimPrefix(endpoint, "http") + "/v1/responses"
	conn, resp, err := websocket.DefaultDialer.Dial(url, http.Header{"session_id": {"ws-1"}, "originator": {"codex_cli_rs"}})
	if err != nil {
		t.Fatalf("dial: %v %v", err, resp)
	}
	defer conn.Close()
	read := func(until ...string) (events []map[string]any) {
		t.Helper()
		_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
		for {
			var ev map[string]any
			if err := conn.ReadJSON(&ev); err != nil {
				t.Fatalf("read: %v (after %v)", err, events)
			}
			events = append(events, ev)
			for _, u := range until {
				if ev["type"] == u {
					return events
				}
			}
		}
	}
	slow := map[string]any{"type": "response.create", "model": model,
		"input": []any{map[string]any{"type": "message", "role": "user", "content": "SLOW please"}}}
	if err := conn.WriteJSON(slow); err != nil {
		t.Fatal(err)
	}
	evs := read("response.output_text.delta")
	id, _ := evs[0]["response"].(map[string]any)["id"].(string)
	if id == "" {
		t.Fatalf("no response id: %v", evs)
	}
	if err := conn.WriteJSON(map[string]any{"type": "response.interrupt", "response_id": id}); err != nil {
		t.Fatal(err)
	}
	evs = read("response.incomplete", "error")
	if len(evs) < 2 || evs[len(evs)-2]["type"] != "response.interrupt.accepted" {
		t.Fatalf("interrupt: %v", evs)
	}
	end := evs[len(evs)-1]["response"].(map[string]any)
	if end["id"] != id || end["incomplete_details"].(map[string]any)["reason"] != "interrupted" {
		t.Fatalf("end: %v", end)
	}
	select {
	case <-mock.hungUp:
	case <-time.After(5 * time.Second):
		t.Fatal("the backend's request went on after the interrupt")
	}
	// An interrupt with nothing in flight fails, and the socket stays.
	if err := conn.WriteJSON(map[string]any{"type": "response.interrupt", "response_id": id}); err != nil {
		t.Fatal(err)
	}
	if evs := read("response.interrupt.failed"); len(evs) != 1 {
		t.Fatalf("late interrupt: %v", evs)
	}
	// The same turn twice: the second request takes the first's turn state.
	turn := map[string]any{"type": "response.create", "model": model,
		"client_metadata": map[string]any{"turn_id": "turn-7"},
		"input":           []any{map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "42"}}}
	for i := 0; i < 2; i++ {
		if err := conn.WriteJSON(turn); err != nil {
			t.Fatal(err)
		}
		if evs := read("response.completed", "error"); evs[len(evs)-1]["type"] != "response.completed" {
			t.Fatalf("request %d: %v", i, evs[len(evs)-1])
		}
	}
	mock.mu.Lock()
	states := append([]string(nil), mock.turnStates...)
	mock.mu.Unlock()
	if n := len(states); n < 3 || states[n-2] != "" || states[n-1] != fmt.Sprintf("ts-%d", n-1) {
		t.Fatalf("turn states sent: %q", states)
	}
	if err := m.coord.Engine.CheckBooks(); err != nil {
		t.Fatal(err)
	}
}
