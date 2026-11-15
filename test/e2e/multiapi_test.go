package e2e

// The three APIs end to end: buyers call the local endpoints the way Claude
// Code (Messages), Codex (Responses) and OpenAI clients (chat) do, and three
// sellers serve the model from a model server that speaks Anthropic Messages
// (an API key), the Codex backend's Responses (a ChatGPT login), or chat
// (a local server). Every client API meets every seller: same API passes
// through, the others are converted. The model servers are one scripted mock
// that answers like the real ones (thinking, tool calls, cache and reasoning
// usage).

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

	"github.com/gorilla/websocket"

	"sharetoken/internal/apicompat"
	"sharetoken/internal/buyer"
	"sharetoken/internal/coordinator"
	"sharetoken/internal/node"
	"sharetoken/internal/p2p"
	"sharetoken/internal/protocol"
	"sharetoken/internal/provider"
	"sharetoken/internal/receipt"
)

const finalAnswer = "The answer is 42."

// scripted is a model server for the three APIs. A request with tools and no
// tool result yet gets a tool call (after reasoning); one with a tool result
// gets the final answer; a request for a summary gets one.
type scripted struct {
	t    *testing.T
	mu   sync.Mutex
	seen map[string][]map[string]any // path -> request bodies
}

func newScripted(t *testing.T) (*scripted, *httptest.Server) {
	s := &scripted{t: t, seen: map[string][]map[string]any{}}
	srv := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(srv.Close)
	return s, srv
}

func (s *scripted) requests(path string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.seen[path]...)
}

func (s *scripted) serve(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "not JSON", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.seen[r.URL.Path] = append(s.seen[r.URL.Path], req)
	s.mu.Unlock()
	raw, _ := json.Marshal(req)
	text := string(raw)
	summary := strings.Contains(text, "summary of the conversation")
	_, hasTools := req["tools"]
	switch r.URL.Path {
	case "/anthropic/v1/messages":
		msgs, _ := req["messages"].([]any)
		last, _ := json.Marshal(msgs[len(msgs)-1])
		s.messages(w, hasTools && !summary && !strings.Contains(string(last), `"tool_result"`), summary)
	case "/codex/responses":
		input, _ := req["input"].([]any)
		last, _ := json.Marshal(input[len(input)-1])
		s.responses(w, hasTools && !summary && !strings.Contains(string(last), `_output"`), summary)
	case "/codex/responses/compact":
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_cmp","object":"response.compaction","created_at":1,"output":[{"id":"cmp_1","type":"compaction","encrypted_content":"OPAQUE-COMPACTION"}],"usage":{"input_tokens":300,"output_tokens":20,"output_tokens_details":{"reasoning_tokens":20}}}`)
	case "/chat/v1/chat/completions":
		msgs, _ := req["messages"].([]any)
		last, _ := msgs[len(msgs)-1].(map[string]any)
		s.chat(w, hasTools && !summary && last["role"] != "tool", summary)
	default:
		http.NotFound(w, r)
	}
}

func sse(w http.ResponseWriter, named bool, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	fl := w.(http.Flusher)
	for _, ev := range events {
		if named {
			var head struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal([]byte(ev), &head)
			fmt.Fprintf(w, "event: %s\n", head.Type)
		}
		fmt.Fprintf(w, "data: %s\n\n", ev)
		fl.Flush()
	}
}

func answerText(summary bool) string {
	if summary {
		return "<summary>The user asked for the answer; it is 42.</summary>"
	}
	return finalAnswer
}

func (s *scripted) messages(w http.ResponseWriter, tool, summary bool) {
	ev := []string{`{"type":"message_start","message":{"id":"msg_up","type":"message","role":"assistant","model":"claude-x","content":[],"stop_reason":null,"usage":{"input_tokens":100,"cache_read_input_tokens":400,"cache_creation_input_tokens":50,"output_tokens":1}}}`}
	if tool {
		ev = append(ev,
			`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"I should look it up."}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-anthropic-1"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_up1","name":"lookup","input":{}}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"q\":"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"answer\"}"}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":40}}`)
	} else {
		text, _ := json.Marshal(answerText(summary))
		ev = append(ev,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":`+string(text)+`}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":12}}`)
	}
	sse(w, true, append(ev, `{"type":"message_stop"}`)...)
}

func (s *scripted) responses(w http.ResponseWriter, tool, summary bool) {
	var items []string
	usage := `{"input_tokens":600,"input_tokens_details":{"cached_tokens":400},"output_tokens":12,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":612}`
	if tool {
		items = []string{
			`{"id":"rs_up1","type":"reasoning","summary":[],"encrypted_content":"ENC-REASONING-1"}`,
			`{"id":"fc_up1","type":"function_call","status":"completed","call_id":"call_up1","name":"lookup","arguments":"{\"q\":\"answer\"}"}`,
		}
		// Most of the output is reasoning the stream does not show.
		usage = `{"input_tokens":600,"input_tokens_details":{"cached_tokens":400},"output_tokens":900,"output_tokens_details":{"reasoning_tokens":880},"total_tokens":1500}`
	} else {
		text, _ := json.Marshal(answerText(summary))
		items = []string{`{"id":"msg_up1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":` + string(text) + `,"annotations":[]}]}`}
	}
	ev := []string{`{"type":"response.created","response":{"id":"resp_up1","object":"response","created_at":1,"status":"in_progress","model":"gpt-x","output":[]}}`}
	for i, it := range items {
		var item map[string]any
		_ = json.Unmarshal([]byte(it), &item)
		ev = append(ev, fmt.Sprintf(`{"type":"response.output_item.added","output_index":%d,"item":%s}`, i, it))
		switch item["type"] {
		case "function_call":
			args, _ := json.Marshal(item["arguments"])
			ev = append(ev, fmt.Sprintf(`{"type":"response.function_call_arguments.delta","output_index":%d,"item_id":"fc_up1","delta":%s}`, i, args))
		case "message":
			text, _ := json.Marshal(answerText(summary))
			ev = append(ev, fmt.Sprintf(`{"type":"response.output_text.delta","output_index":%d,"item_id":"msg_up1","content_index":0,"delta":%s}`, i, text))
		}
		ev = append(ev, fmt.Sprintf(`{"type":"response.output_item.done","output_index":%d,"item":%s}`, i, it))
	}
	ev = append(ev, `{"type":"response.completed","response":{"id":"resp_up1","object":"response","created_at":1,"status":"completed","model":"gpt-x","output":[`+strings.Join(items, ",")+`],"usage":`+usage+`}}`)
	for i := range ev {
		ev[i] = strings.Replace(ev[i], `{"type":`, fmt.Sprintf(`{"sequence_number":%d,"type":`, i), 1)
	}
	sse(w, true, ev...)
}

func (s *scripted) chat(w http.ResponseWriter, tool, summary bool) {
	var ev []string
	if tool {
		ev = []string{
			`{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_chat1","type":"function","function":{"name":"lookup","arguments":""}}]}}]}`,
			`{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":\"answer\"}"}}]}}]}`,
			`{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		}
	} else {
		text, _ := json.Marshal(answerText(summary))
		ev = []string{
			`{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":` + string(text) + `}}]}`,
			`{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		}
	}
	ev = append(ev, `{"id":"c1","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":500,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":200}}}`, "[DONE]")
	sse(w, false, ev...)
}

// apiMarket is a market with one seller per kind of model server, and a
// buyer's local endpoint.
type apiMarket struct {
	*market
	mock                    *scripted
	messagesNode, codexNode *p2p.Keys
	chatNode                *p2p.Keys
	buyerKeys               *p2p.Keys
	endpoint                string
	cl                      *buyer.Client
	nodeOf                  map[string]*p2p.Keys // native API -> seller
}

func newAPIMarket(t *testing.T, sellers ...string) *apiMarket {
	// No warm-up: a request no seller can take must say so at once.
	m := &apiMarket{market: newMarketWith(t, coordinator.Config{WarmUp: -1}), nodeOf: map[string]*p2p.Keys{}}
	mock, srv := newScripted(t)
	m.mock = mock
	if len(sellers) == 0 {
		sellers = protocol.AllAPIs
	}
	for _, api := range sellers {
		var up node.Upstream
		var err error
		price := uint64(priceIn)
		switch api {
		case protocol.APIMessages:
			e := &provider.Entry{ID: "anthropic", Name: "anthropic", Type: provider.KindAnthropic, Auth: provider.AuthAPIKey, APIKey: "sk-ant-test", BaseURL: srv.URL + "/anthropic"}
			up, err = e.Upstream(nil)
		case protocol.APIResponses:
			tok := &provider.Token{AccessToken: "at-1", AccountID: "acct-1"}
			e := &provider.Entry{ID: "codex", Name: "codex", Type: provider.KindOpenAI, Auth: provider.AuthOAuth, OAuth: tok, BaseURL: srv.URL + "/codex"}
			up, err = e.Upstream(provider.FixedToken(tok))
		case protocol.APIChat:
			up = &node.OpenAICompat{BaseURL: srv.URL + "/chat/v1"}
			price = priceIn / 2 // the cheapest: native sellers still come first
		}
		if err != nil {
			t.Fatal(err)
		}
		_, k := m.addNodeCfg(srv.URL, price, priceOut, nil, func(c *node.Config) {
			c.Upstream = up
			c.Models[0].UpstreamModel = api + "-upstream-model"
		})
		m.nodeOf[api] = k
	}
	m.messagesNode, m.codexNode, m.chatNode = m.nodeOf[protocol.APIMessages], m.nodeOf[protocol.APIResponses], m.nodeOf[protocol.APIChat]
	m.waitOffers(len(sellers))
	m.cl, _, m.buyerKeys = m.addBuyer(50_000_000)
	proxy := httptest.NewServer((&buyer.Proxy{Client: m.cl, BudgetMicros: 1_000_000}).Handler())
	t.Cleanup(proxy.Close)
	m.endpoint = proxy.URL
	return m
}

// reply is one answer of the local endpoint.
type reply struct {
	status   int
	body     map[string]any // the (aggregated) JSON answer
	header   http.Header
	payloads [][]byte
	trailer  string // the stream's verdict comment
}

var apiPath = map[string]string{protocol.APIChat: "/v1/chat/completions", protocol.APIMessages: "/v1/messages", protocol.APIResponses: "/v1/responses"}

// call posts a request in api and reads the answer; a stream is checked for
// well-formed events and aggregated like a client would.
func (m *apiMarket) call(t *testing.T, api, path string, req map[string]any, header map[string]string) reply {
	t.Helper()
	b, _ := json.Marshal(req)
	hr, _ := http.NewRequest(http.MethodPost, m.endpoint+path, bytes.NewReader(b))
	hr.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		hr.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(hr)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	r := reply{status: resp.StatusCode, header: resp.Header}
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		data, _ := io.ReadAll(resp.Body)
		_ = json.Unmarshal(data, &r.body)
		return r
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	var event string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, ": sharetoken"):
			r.trailer = line
		case strings.HasPrefix(line, "data: "):
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				continue
			}
			if want := apicompat.EventName(api, []byte(data)); want != event {
				t.Errorf("%s: event line %q for a %q payload", api, event, want)
			}
			event = ""
			r.payloads = append(r.payloads, []byte(data))
		}
	}
	agg, err := apicompat.Aggregate(api, r.payloads)
	if err != nil {
		t.Fatalf("%s stream does not aggregate: %v", api, err)
	}
	if err := json.Unmarshal(agg, &r.body); err != nil {
		t.Fatal(err)
	}
	return r
}

// verified reports whether the answer's receipt was checked and acked.
func (r reply) verified() bool {
	return r.header.Get("X-Sharetoken-Verified") == "true" || strings.Contains(r.trailer, "verified=true")
}

func (r reply) job() string {
	if j := r.header.Get("X-Sharetoken-Job"); j != "" {
		return j
	}
	for _, f := range strings.Fields(r.trailer) {
		if v, ok := strings.CutPrefix(f, "job="); ok {
			return v
		}
	}
	return ""
}

const lookupSchema = `{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`

// firstTurn is a question with a lookup tool, in api.
func firstTurn(api string, stream bool) map[string]any {
	var schema any
	_ = json.Unmarshal([]byte(lookupSchema), &schema)
	switch api {
	case protocol.APIMessages:
		return map[string]any{"model": model, "max_tokens": 4096, "stream": stream,
			"system":   []any{map[string]any{"type": "text", "text": "Use the tool.", "cache_control": map[string]any{"type": "ephemeral"}}},
			"thinking": map[string]any{"type": "enabled", "budget_tokens": 2048},
			"messages": []any{map[string]any{"role": "user", "content": "What is the answer?"}},
			"tools":    []any{map[string]any{"name": "lookup", "description": "Look it up", "input_schema": schema}}}
	case protocol.APIResponses:
		return map[string]any{"model": model, "stream": stream, "store": false, "instructions": "Use the tool.",
			"reasoning": map[string]any{"effort": "medium"}, "include": []any{"reasoning.encrypted_content"},
			"input": []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "What is the answer?"}}}},
			"tools": []any{map[string]any{"type": "function", "name": "lookup", "description": "Look it up", "parameters": schema}}}
	}
	return map[string]any{"model": model, "stream": stream,
		"messages": []any{map[string]any{"role": "system", "content": "Use the tool."}, map[string]any{"role": "user", "content": "What is the answer?"}},
		"tools":    []any{map[string]any{"type": "function", "function": map[string]any{"name": "lookup", "description": "Look it up", "parameters": schema}}}}
}

// toolCall finds the call in an answer, and nextTurn answers it.
func toolCall(t *testing.T, api string, body map[string]any) (id, name, args string) {
	t.Helper()
	switch api {
	case protocol.APIMessages:
		content, _ := body["content"].([]any)
		for _, c := range content {
			b := c.(map[string]any)
			if b["type"] == "tool_use" {
				in, _ := json.Marshal(b["input"])
				return b["id"].(string), b["name"].(string), string(in)
			}
		}
	case protocol.APIResponses:
		output, _ := body["output"].([]any)
		for _, o := range output {
			it := o.(map[string]any)
			if it["type"] == "function_call" {
				return it["call_id"].(string), it["name"].(string), it["arguments"].(string)
			}
		}
	default:
		choices, _ := body["choices"].([]any)
		if len(choices) > 0 {
			msg := choices[0].(map[string]any)["message"].(map[string]any)
			if calls, _ := msg["tool_calls"].([]any); len(calls) > 0 {
				c := calls[0].(map[string]any)
				f := c["function"].(map[string]any)
				return c["id"].(string), f["name"].(string), f["arguments"].(string)
			}
		}
	}
	t.Fatalf("%s answer has no tool call: %v", api, body)
	return "", "", ""
}

func nextTurn(api string, first, answer map[string]any, callID string) map[string]any {
	next := map[string]any{}
	for k, v := range first {
		next[k] = v
	}
	switch api {
	case protocol.APIMessages:
		next["messages"] = append(append([]any{}, first["messages"].([]any)...),
			map[string]any{"role": "assistant", "content": answer["content"]},
			map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": callID, "content": "42"}}})
	case protocol.APIResponses:
		next["input"] = append(append(append([]any{}, first["input"].([]any)...), answer["output"].([]any)...),
			map[string]any{"type": "function_call_output", "call_id": callID, "output": "42"})
	default:
		msg := answer["choices"].([]any)[0].(map[string]any)["message"]
		next["messages"] = append(append([]any{}, first["messages"].([]any)...), msg,
			map[string]any{"role": "tool", "tool_call_id": callID, "content": "42"})
	}
	return next
}

func finalText(api string, body map[string]any) string {
	var b strings.Builder
	switch api {
	case protocol.APIMessages:
		for _, c := range body["content"].([]any) {
			if blk := c.(map[string]any); blk["type"] == "text" {
				b.WriteString(blk["text"].(string))
			}
		}
	case protocol.APIResponses:
		for _, o := range body["output"].([]any) {
			if it := o.(map[string]any); it["type"] == "message" {
				for _, p := range it["content"].([]any) {
					b.WriteString(p.(map[string]any)["text"].(string))
				}
			}
		}
	default:
		if c, ok := body["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"].(string); ok {
			b.WriteString(c)
		}
	}
	return b.String()
}

// Every client API with every seller, streamed and not: a tool call, then
// the answer, each job verified, acked and settled at the ticket's prices.
func TestEveryAPIWithEverySeller(t *testing.T) {
	m := newAPIMarket(t)
	upstreamPath := map[string]string{protocol.APIMessages: "/anthropic/v1/messages", protocol.APIResponses: "/codex/responses", protocol.APIChat: "/chat/v1/chat/completions"}
	for _, api := range protocol.AllAPIs {
		for _, seller := range protocol.AllAPIs {
			for _, stream := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s client, %s seller, stream=%t", api, seller, stream), func(t *testing.T) {
					pin := map[string]string{buyer.HeaderNode: m.nodeOf[seller].ID()}
					before := len(m.mock.requests(upstreamPath[seller]))
					first := firstTurn(api, stream)
					r := m.call(t, api, apiPath[api], first, pin)
					if r.status != http.StatusOK || !r.verified() {
						t.Fatalf("first turn: %d %v %s", r.status, r.body, r.trailer)
					}
					id, name, args := toolCall(t, api, r.body)
					if name != "lookup" || !strings.Contains(args, `"answer"`) || id == "" {
						t.Fatalf("tool call %q %q %q", id, name, args)
					}
					r2 := m.call(t, api, apiPath[api], nextTurn(api, first, r.body, id), pin)
					if r2.status != http.StatusOK || !r2.verified() || finalText(api, r2.body) != finalAnswer {
						t.Fatalf("second turn: %d %v", r2.status, r2.body)
					}
					// The seller's model server was asked in its own API, twice.
					reqs := m.mock.requests(upstreamPath[seller])
					if len(reqs) != before+2 {
						t.Fatalf("%s upstream got %d requests, want 2", seller, len(reqs)-before)
					}
					up, _ := json.Marshal(reqs[len(reqs)-1])
					if !strings.Contains(string(up), `42`) || !strings.Contains(string(up), "lookup") {
						t.Fatalf("the tool result did not reach the upstream: %s", up)
					}
					for _, rr := range []reply{r, r2} {
						m.waitStatus(rr.job(), coordinator.StatusFinal)
					}
				})
			}
		}
	}
	if err := m.coord.Engine.CheckBooks(); err != nil {
		t.Fatal(err)
	}
}

// settled is the ticket and receipt of a finished job.
func (m *apiMarket) settled(t *testing.T, job string) (*protocol.Ticket, *protocol.Usage) {
	t.Helper()
	m.waitStatus(job, coordinator.StatusFinal)
	var tj, rj []byte
	if err := m.db.QueryRow(`SELECT ticket, receipt FROM jobs WHERE job_id = ?`, job).Scan(&tj, &rj); err != nil {
		t.Fatalf("job %s: %v", job, err)
	}
	var tk protocol.Ticket
	var sr receipt.SignedReceipt
	if json.Unmarshal(tj, &tk) != nil || json.Unmarshal(rj, &sr) != nil {
		t.Fatalf("job %s: unreadable ticket or receipt", job)
	}
	u := protocol.ReceiptUsage(&sr.Receipt)
	return &tk, &u
}

// Sellers whose model server speaks the request's API come first, even when
// a converting seller is cheaper; require_native takes only them, and fails
// when there is none.
func TestNativeSellersFirstAndRequireNative(t *testing.T) {
	m := newAPIMarket(t)
	for api, want := range map[string]*p2p.Keys{protocol.APIMessages: m.messagesNode, protocol.APIResponses: m.codexNode, protocol.APIChat: m.chatNode} {
		for i := range 3 {
			first := firstTurn(api, false)
			first["metadata"] = map[string]any{"user_id": fmt.Sprintf("user_x_session_%s-%d", api, i)} // a new conversation each time
			r := m.call(t, api, apiPath[api], first, nil)
			if r.status != http.StatusOK || r.header.Get("X-Sharetoken-Node") != want.ID() {
				t.Fatalf("%s request went to %s, want the native seller", api, r.header.Get("X-Sharetoken-Node"))
			}
			tk, _ := m.settled(t, r.job())
			if tk.API != api {
				t.Fatalf("ticket API %q, want %q", tk.API, api)
			}
		}
	}
	// The model list says which APIs some seller speaks natively.
	resp, err := http.Get(m.endpoint + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Data []struct {
			ID   string   `json:"id"`
			Type string   `json:"type"`
			APIs []string `json:"apis"`
		} `json:"data"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list.Data) != 1 || list.Data[0].ID != model || list.Data[0].Type != "model" || strings.Join(list.Data[0].APIs, ",") != "chat,messages,responses" {
		t.Fatalf("models: %+v", list)
	}

	// Without a Messages seller: a Messages request is converted, unless it
	// requires a native seller.
	m2 := newAPIMarket(t, protocol.APIResponses, protocol.APIChat)
	r := m2.call(t, protocol.APIMessages, "/v1/messages", firstTurn(protocol.APIMessages, false), nil)
	if r.status != http.StatusOK || !r.verified() {
		t.Fatalf("converted: %d %v", r.status, r.body)
	}
	r = m2.call(t, protocol.APIMessages, "/v1/messages", firstTurn(protocol.APIMessages, false), map[string]string{buyer.HeaderRequireNative: "true"})
	if r.status != http.StatusServiceUnavailable || r.body["type"] != "error" || !strings.Contains(fmt.Sprint(r.body["error"]), "natively") {
		t.Fatalf("require native without a native seller: %d %v", r.status, r.body)
	}
	// A Responses request may require it: the Codex seller speaks it.
	r = m2.call(t, protocol.APIResponses, "/v1/responses", firstTurn(protocol.APIResponses, true), map[string]string{buyer.HeaderRequireNative: "1"})
	if r.status != http.StatusOK || !strings.Contains(r.trailer, "node="+m2.codexNode.ID()) {
		t.Fatalf("require native: %d %s", r.status, r.trailer)
	}
}

// Prompt tokens read from and written to the cache are billed at the cache
// prices, and reasoning the response does not show is billed as the model
// server reported it, within the job's output limit.
func TestCacheAndHiddenReasoningAreBilled(t *testing.T) {
	m := newAPIMarket(t)
	// Messages, passed through: 100 input + 400 cache reads + 50 cache
	// writes, 40 output tokens, of which the thinking shows a few.
	r := m.call(t, protocol.APIMessages, "/v1/messages", firstTurn(protocol.APIMessages, false), map[string]string{buyer.HeaderNode: m.messagesNode.ID()})
	tk, u := m.settled(t, r.job())
	want := protocol.Usage{PromptTokens: 550, CompletionTokens: 40, CacheReadTokens: 400, CacheWriteTokens: 50}
	if *u != want {
		t.Fatalf("receipt usage %+v, want %+v", *u, want)
	}
	if tk.PriceCacheRead != priceIn/10 || tk.PriceCacheWrite != priceIn+priceIn/4 {
		t.Fatalf("ticket cache prices %d/%d", tk.PriceCacheRead, tk.PriceCacheWrite)
	}
	cost := protocol.TicketCost(tk, *u) // 100*1 + 400*0.1 + 50*1.25 + 40*2 = 282.5
	if cost != 283 || r.header.Get("X-Sharetoken-Cost-Micros") != "283" {
		t.Fatalf("cost %d (header %s), want 283", cost, r.header.Get("X-Sharetoken-Cost-Micros"))
	}
	// Responses from the Codex seller: 900 output tokens, 880 of them
	// reasoning the stream only shows as an encrypted item.
	r = m.call(t, protocol.APIResponses, "/v1/responses", firstTurn(protocol.APIResponses, true), map[string]string{buyer.HeaderNode: m.codexNode.ID()})
	_, u = m.settled(t, r.job())
	if u.PromptTokens != 600 || u.CacheReadTokens != 400 || u.CompletionTokens != 900 {
		t.Fatalf("hidden reasoning billed %+v", *u)
	}
	// The same through a conversion to Messages (Codex seller, Claude Code
	// buyer): the reasoning arrives as a thinking block.
	r = m.call(t, protocol.APIMessages, "/v1/messages", firstTurn(protocol.APIMessages, true), map[string]string{buyer.HeaderNode: m.codexNode.ID()})
	_, u = m.settled(t, r.job())
	if u.CompletionTokens != 900 || u.CacheReadTokens != 400 {
		t.Fatalf("converted hidden reasoning billed %+v", *u)
	}
	// A buyer's output limit bounds what hidden reasoning may cost.
	first := firstTurn(protocol.APIResponses, false)
	first["max_output_tokens"] = 300
	r = m.call(t, protocol.APIResponses, "/v1/responses", first, map[string]string{buyer.HeaderNode: m.codexNode.ID()})
	if r.status != http.StatusOK || !r.verified() {
		t.Fatalf("limited: %d %v", r.status, r.body)
	}
	if _, u = m.settled(t, r.job()); u.CompletionTokens != 300 {
		t.Fatalf("hidden reasoning beyond the limit billed %+v", *u)
	}
}

// Codex's /responses/compact: the Codex seller compacts natively; another
// seller is asked for a summary, which comes back as a compaction item and
// becomes the summary again for whichever seller gets the next request.
func TestCompact(t *testing.T) {
	m := newAPIMarket(t)
	conversation := firstTurn(protocol.APIResponses, false)
	delete(conversation, "stream")
	r := m.call(t, protocol.APIResponses, "/v1/responses/compact", conversation, map[string]string{buyer.HeaderNode: m.codexNode.ID()})
	out, _ := r.body["output"].([]any)
	if r.status != http.StatusOK || !r.verified() || len(out) != 1 || out[0].(map[string]any)["encrypted_content"] != "OPAQUE-COMPACTION" {
		t.Fatalf("native compact: %d %v", r.status, r.body)
	}
	if n := len(m.mock.requests("/codex/responses/compact")); n != 1 {
		t.Fatalf("compact endpoint asked %d times", n)
	}

	r = m.call(t, protocol.APIResponses, "/v1/responses/compact", conversation, map[string]string{buyer.HeaderNode: m.messagesNode.ID()})
	out, _ = r.body["output"].([]any)
	if r.status != http.StatusOK || !r.verified() || len(out) != 1 {
		t.Fatalf("emulated compact: %d %v", r.status, r.body)
	}
	item := out[0].(map[string]any)
	summary, ok := apicompat.DecodeCompaction(fmt.Sprint(item["encrypted_content"]))
	if item["type"] != "compaction" || !ok || summary != "The user asked for the answer; it is 42." {
		t.Fatalf("compaction item %v (%q)", item, summary)
	}
	_, u := m.settled(t, r.job())
	if u.CompletionTokens != 12 {
		t.Fatalf("summary billed %+v", *u)
	}

	// The next request carries the compaction item: the chat seller gets
	// the summary as a user message.
	next := firstTurn(protocol.APIResponses, false)
	next["input"] = append([]any{item}, next["input"].([]any)...)
	r = m.call(t, protocol.APIResponses, "/v1/responses", next, map[string]string{buyer.HeaderNode: m.chatNode.ID()})
	if r.status != http.StatusOK {
		t.Fatalf("after compaction: %d %v", r.status, r.body)
	}
	reqs := m.mock.requests("/chat/v1/chat/completions")
	sent, _ := json.Marshal(reqs[len(reqs)-1]["messages"])
	if !strings.Contains(string(sent), "conversation_summary") || !strings.Contains(string(sent), "it is 42") {
		t.Fatalf("summary not passed on: %s", sent)
	}
	// And the Codex seller too: it cannot read sharetoken's item either.
	r = m.call(t, protocol.APIResponses, "/v1/responses", next, map[string]string{buyer.HeaderNode: m.codexNode.ID()})
	reqs = m.mock.requests("/codex/responses")
	sent, _ = json.Marshal(reqs[len(reqs)-1]["input"])
	if r.status != http.StatusOK || !strings.Contains(string(sent), "conversation_summary") || strings.Contains(string(sent), "sharetoken-compaction") {
		t.Fatalf("summary to the Codex seller: %d %s", r.status, sent)
	}
}

// Codex over a WebSocket: a warm-up answered locally, then requests that
// send only what is new since the previous response.
func TestResponsesWebSocket(t *testing.T) {
	m := newAPIMarket(t)
	url := "ws" + strings.TrimPrefix(m.endpoint, "http") + "/v1/responses"
	conn, resp, err := websocket.DefaultDialer.Dial(url, http.Header{"session_id": {"ws-session"}, buyer.HeaderNode: {m.codexNode.ID()}})
	if err != nil {
		t.Fatalf("dial: %v %v", err, resp)
	}
	defer conn.Close()
	send := func(msg map[string]any) (events []map[string]any) {
		t.Helper()
		if err := conn.WriteJSON(msg); err != nil {
			t.Fatal(err)
		}
		for {
			var ev map[string]any
			if err := conn.ReadJSON(&ev); err != nil {
				t.Fatal(err)
			}
			events = append(events, ev)
			switch ev["type"] {
			case "response.completed", "response.incomplete", "error":
				return events
			}
		}
	}
	first := firstTurn(protocol.APIResponses, true)
	warm := map[string]any{"type": "response.create", "generate": false}
	for k, v := range first {
		warm[k] = v
	}
	evs := send(warm)
	if len(evs) != 2 || evs[1]["type"] != "response.completed" {
		t.Fatalf("warm-up: %v", evs)
	}
	before := len(m.mock.requests("/codex/responses"))
	warmID := evs[1]["response"].(map[string]any)["id"]
	// The first real request builds on the warm-up: nothing new but the
	// warm-up's input.
	req := map[string]any{"type": "response.create", "previous_response_id": warmID, "input": []any{}}
	for k, v := range first {
		if k != "input" {
			req[k] = v
		}
	}
	evs = send(req)
	last := evs[len(evs)-1]
	if last["type"] != "response.completed" {
		t.Fatalf("first turn: %v", last)
	}
	resp1 := last["response"].(map[string]any)
	var callID string
	for _, o := range resp1["output"].([]any) {
		if it := o.(map[string]any); it["type"] == "function_call" {
			callID = it["call_id"].(string)
		}
	}
	// The next one sends only the tool's output.
	req["previous_response_id"] = resp1["id"]
	req["input"] = []any{map[string]any{"type": "function_call_output", "call_id": callID, "output": "42"}}
	evs = send(req)
	last = evs[len(evs)-1]
	if last["type"] != "response.completed" {
		t.Fatalf("second turn: %v", last)
	}
	reqs := m.mock.requests("/codex/responses")
	if len(reqs) != before+2 {
		t.Fatalf("upstream requests: %d", len(reqs)-before)
	}
	input := reqs[len(reqs)-1]["input"].([]any)
	var types []string
	for _, it := range input {
		types = append(types, fmt.Sprint(it.(map[string]any)["type"]))
	}
	if strings.Join(types, ",") != "message,reasoning,function_call,function_call_output" {
		t.Fatalf("the node got %v, want the whole conversation", types)
	}
	// An unknown previous response is refused, and the connection stays.
	req["previous_response_id"] = "resp_unknown"
	evs = send(req)
	if e := evs[len(evs)-1]; e["type"] != "error" || !strings.Contains(fmt.Sprint(e["error"]), "previous_response_not_found") {
		t.Fatalf("unknown previous response: %v", e)
	}
	// A web page may not open the socket.
	if _, resp, err := websocket.DefaultDialer.Dial(url, http.Header{"Origin": {"https://evil.example"}}); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin socket: %v %v", err, resp)
	}
}

// count_tokens is answered locally; the Codex model manifest lists the
// models; errors come in each API's own shape.
func TestLocalAnswersAndErrors(t *testing.T) {
	m := newAPIMarket(t, protocol.APIChat)
	b, _ := json.Marshal(firstTurn(protocol.APIMessages, false))
	resp, err := http.Post(m.endpoint+"/v1/messages/count_tokens", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	var count map[string]uint64
	_ = json.NewDecoder(resp.Body).Decode(&count)
	resp.Body.Close()
	if count["input_tokens"] == 0 {
		t.Fatalf("count_tokens: %v", count)
	}
	resp, _ = http.Get(m.endpoint + "/v1/models?client_version=0.144.6")
	var manifest struct {
		Models []map[string]any `json:"models"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&manifest)
	resp.Body.Close()
	if len(manifest.Models) != 1 || manifest.Models[0]["slug"] != model {
		t.Fatalf("manifest: %v", manifest)
	}
	// An unknown model: a Messages error for Claude Code, an OpenAI one otherwise.
	bad := firstTurn(protocol.APIMessages, false)
	bad["model"] = "nope"
	r := m.call(t, protocol.APIMessages, "/v1/messages", bad, nil)
	if r.status != http.StatusServiceUnavailable || r.body["type"] != "error" {
		t.Fatalf("messages error: %d %v", r.status, r.body)
	}
	bad = firstTurn(protocol.APIResponses, false)
	bad["model"] = "nope"
	r = m.call(t, protocol.APIResponses, "/v1/responses", bad, nil)
	if e, _ := r.body["error"].(map[string]any); r.status != http.StatusServiceUnavailable || e["code"] != protocol.CodeNoOffers {
		t.Fatalf("responses error: %d %v", r.status, r.body)
	}
	bad["previous_response_id"] = "resp_1"
	bad["model"] = model
	if r = m.call(t, protocol.APIResponses, "/v1/responses", bad, nil); r.status != http.StatusBadRequest {
		t.Fatalf("previous_response_id over HTTP: %d %v", r.status, r.body)
	}
}

// Sellers with a ChatGPT (Codex) login and a Gemini CLI login, their model
// servers replayed (testdata/replay): every client API runs a tool loop on
// each, and the tool's output comes back in the answer.
func TestReplayedSubscriptionSellers(t *testing.T) {
	m := &apiMarket{market: newMarketWith(t, coordinator.Config{WarmUp: -1}), nodeOf: map[string]*p2p.Keys{}}
	rp, srv := newReplayer(t, "")
	codexTok := &provider.Token{AccessToken: "at-codex", AccountID: "acct-codex"}
	codex := &provider.Entry{ID: "codex", Name: "codex", Type: provider.KindOpenAI, Auth: provider.AuthOAuth, OAuth: codexTok, BaseURL: srv.URL + "/codex"}
	geminiTok := &provider.Token{AccessToken: "at-gemini", ProjectID: "project-1"}
	gemini := &provider.Entry{ID: "gemini", Name: "gemini", Type: provider.KindGemini, Auth: provider.AuthOAuth, OAuth: geminiTok, BaseURL: srv.URL + "/gemini"}
	sellers := map[string]*p2p.Keys{}
	for name, e := range map[string]*provider.Entry{"codex": codex, "gemini": gemini} {
		tok := codexTok
		if name == "gemini" {
			tok = geminiTok
		}
		up, err := e.Upstream(provider.FixedToken(tok))
		if err != nil {
			t.Fatal(err)
		}
		_, k := m.addNodeCfg(srv.URL, priceIn, priceOut, nil, func(c *node.Config) {
			c.Upstream = up
			c.Models[0].UpstreamModel = map[string]string{"codex": "gpt-5.5", "gemini": "gemini-2.5-pro"}[name]
		})
		sellers[name] = k
	}
	m.waitOffers(2)
	m.cl, _, m.buyerKeys = m.addBuyer(50_000_000)
	proxy := httptest.NewServer((&buyer.Proxy{Client: m.cl, BudgetMicros: 1_000_000}).Handler())
	defer proxy.Close()
	m.endpoint = proxy.URL
	paths := map[string]string{"codex": "/codex/responses", "gemini": "/gemini/v1internal:streamGenerateContent"}
	for _, api := range protocol.AllAPIs {
		for name, seller := range sellers {
			for _, stream := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s client, %s seller, stream=%t", api, name, stream), func(t *testing.T) {
					pin := map[string]string{buyer.HeaderNode: seller.ID()}
					first := firstTurn(api, stream)
					r := m.call(t, api, apiPath[api], first, pin)
					if r.status != http.StatusOK || !r.verified() {
						t.Fatalf("first turn: %d %v %s", r.status, r.body, r.trailer)
					}
					id, tool, args := toolCall(t, api, r.body)
					if tool != "lookup" || args != `{"q":"answer"}` {
						t.Fatalf("tool call %s %s %s", id, tool, args)
					}
					r2 := m.call(t, api, apiPath[api], nextTurn(api, first, r.body, id), pin)
					if r2.status != http.StatusOK || !r2.verified() || finalText(api, r2.body) != "The file says: 42" {
						t.Fatalf("second turn: %d %v", r2.status, r2.body)
					}
					// The reasoning the first answer only shows in part is billed
					// as reported (Gemini's thoughts, Codex's reasoning tokens).
					_, u := m.settled(t, r.job())
					want := map[string]uint64{"codex": 214, "gemini": 31 + 178}[name]
					if u.CompletionTokens != want || u.CacheReadTokens == 0 {
						t.Fatalf("first turn billed %+v, want %d completion tokens and cache reads", *u, want)
					}
					reqs := rp.requests(paths[name])
					last, _ := json.Marshal(reqs[len(reqs)-1])
					if !strings.Contains(string(last), "42") {
						t.Fatalf("the tool output did not reach the model server: %s", last)
					}
				})
			}
		}
	}
}
