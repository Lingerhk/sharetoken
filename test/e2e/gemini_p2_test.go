package e2e

// Gemini sellers (a Gemini CLI login) end to end: Gemini 3's thought
// signature on a tool call reaches the buyer inside the call's id, comes
// back with the next turn from every client API, and goes to the model
// server on the call where it belongs; a seller that did not serve the
// earlier turn sends the placeholder instead. The tool call's args come as
// JSON text in pieces, which the seller joins into one call.

import (
	"encoding/base64"
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
	"sharetoken/internal/p2p"
	"sharetoken/internal/protocol"
	"sharetoken/internal/provider"
)

// codeAssistMock answers streamGenerateContent: a tool call (signed, its
// args in two pieces) to a request without a function response, a text
// answer to one with.
type codeAssistMock struct {
	mu   sync.Mutex
	seen []map[string]any
}

var geminiSignature = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("gemini thought state ", 100)))

func (m *codeAssistMock) serve(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !strings.HasSuffix(r.URL.Path, ":streamGenerateContent") {
		http.Error(w, "unexpected", http.StatusBadRequest)
		return
	}
	m.mu.Lock()
	m.seen = append(m.seen, req)
	m.mu.Unlock()
	raw, _ := json.Marshal(req)
	w.Header().Set("Content-Type", "text/event-stream")
	events := []string{
		`{"response":{"candidates":[{"content":{"parts":[{"text":"looking it up","thought":true},{"functionCall":{"name":"lookup","args":"{\"q\":","id":"fc-7"},"thoughtSignature":"` + geminiSignature + `"}]}}]}}`,
		`{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":"\"answer\"}","id":"fc-7"}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":5,"thoughtsTokenCount":3}}}`,
	}
	if strings.Contains(string(raw), `"functionResponse"`) {
		events = []string{`{"response":{"candidates":[{"content":{"parts":[{"text":"` + finalAnswer + `"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":40,"candidatesTokenCount":6}}}`}
	}
	for _, ev := range events {
		_, _ = io.WriteString(w, "data: "+ev+"\n\n")
	}
}

func (m *codeAssistMock) last(t *testing.T) string {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	b, _ := json.Marshal(m.seen[len(m.seen)-1])
	return string(b)
}

func TestGeminiThoughtSignatureEveryAPI(t *testing.T) {
	m := &apiMarket{market: newMarketWith(t, coordinator.Config{WarmUp: -1}), nodeOf: map[string]*p2p.Keys{}}
	mock := &codeAssistMock{}
	srv := httptest.NewServer(http.HandlerFunc(mock.serve))
	t.Cleanup(srv.Close)
	sellers := map[string]*p2p.Keys{}
	for _, name := range []string{"gemini-a", "gemini-b"} {
		tok := &provider.Token{AccessToken: "at-" + name, ProjectID: "project-" + name}
		e := &provider.Entry{ID: name, Name: name, Type: provider.KindGemini, Auth: provider.AuthOAuth, OAuth: tok, BaseURL: srv.URL + "/" + name}
		up, err := e.Upstream(provider.FixedToken(tok))
		if err != nil {
			t.Fatal(err)
		}
		_, k := m.addNodeCfg(srv.URL, priceIn, priceOut, nil, func(c *node.Config) {
			c.Upstream = up
			c.Models[0].UpstreamModel = "gemini-3.5-flash"
		})
		sellers[name] = k
	}
	m.waitOffers(2)
	m.cl, _, m.buyerKeys = m.addBuyer(50_000_000)
	proxy := httptest.NewServer((&buyer.Proxy{Client: m.cl, BudgetMicros: 1_000_000}).Handler())
	t.Cleanup(proxy.Close)
	m.endpoint = proxy.URL

	signedCall := `{"functionCall":{"args":{"q":"answer"},"id":"fc-7","name":"lookup"},"thoughtSignature":"` + geminiSignature + `"}`
	placeholder := `{"functionCall":{"args":{"q":"answer"},"id":"fc-7","name":"lookup"},"thoughtSignature":"skip_thought_signature_validator"}`
	for _, api := range protocol.AllAPIs {
		for _, stream := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s client, stream=%t", api, stream), func(t *testing.T) {
				onA := map[string]string{buyer.HeaderNode: sellers["gemini-a"].ID()}
				first := firstTurn(api, stream)
				r := m.call(t, api, apiPath[api], first, onA)
				if r.status != http.StatusOK || !r.verified() {
					t.Fatalf("first turn: %d %v %s", r.status, r.body, r.trailer)
				}
				id, tool, args := toolCall(t, api, r.body)
				if tool != "lookup" || args != `{"q":"answer"}` || !strings.HasPrefix(id, "fc-7__tsk16__") {
					t.Fatalf("tool call %s %s %s", id, tool, args)
				}
				next := nextTurn(api, first, r.body, id)

				// The same seller gives the model its signature back.
				r2 := m.call(t, api, apiPath[api], next, onA)
				if r2.status != http.StatusOK || finalText(api, r2.body) != finalAnswer {
					t.Fatalf("second turn: %d %v", r2.status, r2.body)
				}
				got := mock.last(t)
				if !strings.Contains(got, signedCall) || !strings.Contains(got, `"functionResponse":{"id":"fc-7"`) || strings.Contains(got, "__ts") {
					t.Fatalf("second turn sent %s", got)
				}

				// Another seller has no such signature: the placeholder.
				r3 := m.call(t, api, apiPath[api], next, map[string]string{buyer.HeaderNode: sellers["gemini-b"].ID()})
				if r3.status != http.StatusOK || finalText(api, r3.body) != finalAnswer {
					t.Fatalf("on another seller: %d %v", r3.status, r3.body)
				}
				got = mock.last(t)
				if !strings.Contains(got, placeholder) || strings.Contains(got, geminiSignature) || strings.Contains(got, "__ts") {
					t.Fatalf("another seller sent %s", got)
				}
			})
		}
	}
}
