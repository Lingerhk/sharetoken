package e2e

// A Claude Code style Messages client against a seller whose provider speaks
// only chat completions (an OpenAI-compatible key), through the buyer's
// local endpoint and a real node: tool call ids an upstream numbers by
// their place, and a rate limit that is about one request alone.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"sharetoken/internal/buyer"
	"sharetoken/internal/coordinator"
	"sharetoken/internal/node"
	"sharetoken/internal/provider"
)

// positional is a chat model server that calls the tool on the first two
// turns, numbering the call by its place (call_0 each time, as some relays
// do), and answers on the third; a request that says HUGE-REQUEST is turned
// away with OpenAI's 429 for a request larger than the account's tokens per
// minute.
type positional struct {
	mu   sync.Mutex
	seen []map[string]any
}

func (p *positional) serve(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "not JSON", http.StatusBadRequest)
		return
	}
	p.mu.Lock()
	p.seen = append(p.seen, req)
	p.mu.Unlock()
	raw, _ := json.Marshal(req)
	if strings.Contains(string(raw), "HUGE-REQUEST") {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "20")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"Request too large for chat-upstream-model in organization org-test on tokens per min (TPM): Limit 30000, Requested 45000. The input or output tokens must be reduced in order to run successfully.","type":"tokens","param":null,"code":"rate_limit_exceeded"}}`)
		return
	}
	calls := 0
	msgs, _ := req["messages"].([]any)
	for _, m := range msgs {
		if mm, _ := m.(map[string]any); mm["role"] == "assistant" && mm["tool_calls"] != nil {
			calls++
		}
	}
	var ev []string
	if calls < 2 {
		ev = []string{
			`{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_0","type":"function","function":{"name":"lookup","arguments":""}}]}}]}`,
			fmt.Sprintf(`{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":\"step %d\"}"}}]}}]}`, calls),
			`{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		}
	} else {
		ev = []string{
			`{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"` + finalAnswer + `"}}]}`,
			`{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		}
	}
	ev = append(ev, `{"id":"c1","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":500,"completion_tokens":20}}`, "[DONE]")
	sse(w, false, ev...)
}

func (p *positional) requests() []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]map[string]any(nil), p.seen...)
}

// newChatKeyMarket is a market with one seller whose provider is an
// OpenAI-compatible key at the mock, and the buyer's local endpoint.
func newChatKeyMarket(t *testing.T) (*apiMarket, *positional) {
	mock := &positional{}
	srv := httptest.NewServer(http.HandlerFunc(mock.serve))
	t.Cleanup(srv.Close)
	m := &apiMarket{market: newMarketWith(t, coordinator.Config{WarmUp: -1})}
	e := &provider.Entry{ID: t.Name(), Name: "relay", Type: provider.KindCustom, Auth: provider.AuthAPIKey, APIKey: "sk-relay", BaseURL: srv.URL + "/v1"}
	up, err := e.Upstream(nil)
	if err != nil {
		t.Fatal(err)
	}
	m.addNodeCfg(srv.URL, priceIn, priceOut, nil, func(c *node.Config) {
		c.Upstream = up
		c.Models[0].UpstreamModel = "chat-upstream-model"
	})
	m.waitOffers(1)
	m.cl, _, m.buyerKeys = m.addBuyer(50_000_000)
	proxy := httptest.NewServer((&buyer.Proxy{Client: m.cl, BudgetMicros: 1_000_000}).Handler())
	t.Cleanup(proxy.Close)
	m.endpoint = proxy.URL
	return m, mock
}

// The upstream calls the tool as call_0 on two turns running: Claude Code
// would drop the second block as one it has seen ("[Tool use interrupted]").
// The client sees two ids, and the upstream gets its own back each time.
func TestPositionalToolIDsReachClaudeCodeDistinct(t *testing.T) {
	for _, stream := range []bool{true, false} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			m, mock := newChatKeyMarket(t)
			req := firstTurn("messages", stream)
			delete(req, "thinking")
			var ids []string
			for turn := range 3 {
				r := m.call(t, "messages", "/v1/messages", req, nil)
				if r.status != http.StatusOK {
					t.Fatalf("turn %d: %d %v", turn, r.status, r.body)
				}
				if turn == 2 {
					if got := finalText("messages", r.body); got != finalAnswer {
						t.Fatalf("final answer %q", got)
					}
					break
				}
				id, _, _ := toolCall(t, "messages", r.body)
				ids = append(ids, id)
				req = nextTurn("messages", req, r.body, id)
			}
			if ids[0] != "call_0" || ids[1] == ids[0] || ids[1] == "" {
				t.Fatalf("the client saw ids %q", ids)
			}
			last := mock.requests()[2]
			body, _ := json.Marshal(last["messages"])
			if strings.Contains(string(body), "toolu_st_") || strings.Count(string(body), `"call_0"`) != 4 {
				t.Fatalf("the upstream did not get its own ids back: %s", body)
			}
		})
	}
}

// A 429 that says the request alone is larger than the account's tokens per
// minute fails that job, and the seller serves the next request at once.
func TestRequestTooLargeForTheMinuteDoesNotRestTheSeller(t *testing.T) {
	m, mock := newChatKeyMarket(t)
	huge := firstTurn("messages", false)
	huge["messages"] = []any{map[string]any{"role": "user", "content": "HUGE-REQUEST"}}
	r := m.call(t, "messages", "/v1/messages", huge, nil)
	if r.status == http.StatusOK || r.status == http.StatusTooManyRequests {
		t.Fatalf("the huge request: %d %v", r.status, r.body)
	}
	if msg := fmt.Sprint(r.body); !strings.Contains(msg, "Request too large") {
		t.Fatalf("the refusal did not reach the client: %v", r.body)
	}
	n := len(mock.requests())
	r = m.call(t, "messages", "/v1/messages", firstTurn("messages", false), nil)
	if r.status != http.StatusOK {
		t.Fatalf("the next request: %d %v", r.status, r.body)
	}
	if len(mock.requests()) != n+1 {
		t.Fatalf("the seller did not ask its model server: %d requests after %d", len(mock.requests()), n)
	}
}
