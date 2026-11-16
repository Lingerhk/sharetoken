package node

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"sharetoken/internal/p2p"
	"sharetoken/internal/protocol"
)

func prepared(t *testing.T, api string, in []byte, limit uint64) map[string]any {
	t.Helper()
	out, err := prepareBody(api, in, "upstream-model", limit)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func TestPrepareChatEnforcesNodePolicy(t *testing.T) {
	got := prepared(t, protocol.APIChat, []byte(`{"model":"gpt-something","n":8,"best_of":4,"stream":false,"max_tokens":500,
		"temperature":0.2,"messages":[{"role":"user","content":"hi"}],"logprobs":true,"session_id":"local-session","parallel_tool_calls":false}`), 300)
	if got["model"] != "upstream-model" {
		t.Errorf("model not overridden: %v", got["model"])
	}
	for _, k := range []string{"n", "best_of", "logprobs", "session_id"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s must be dropped", k)
		}
	}
	if got["stream"] != true {
		t.Error("stream must be forced on")
	}
	if so, _ := got["stream_options"].(map[string]any); so["include_usage"] != true {
		t.Error("usage reporting must be requested")
	}
	if got["max_tokens"].(float64) != 300 {
		t.Errorf("max_tokens = %v, want the limit 300", got["max_tokens"])
	}
	if got["temperature"].(float64) != 0.2 || got["parallel_tool_calls"] != false {
		t.Error("allowed parameters must pass through")
	}
	if _, err := prepareBody(protocol.APIChat, []byte(`{"prompt":"x"}`), "m", 10); protocol.CodeOf(err) != protocol.CodeBadRequest {
		t.Errorf("missing messages: want bad_request, got %v", err)
	}
}

func TestPrepareMessagesDropsServerTools(t *testing.T) {
	got := prepared(t, protocol.APIMessages, []byte(`{"model":"claude-x","max_tokens":128000,"stream":false,
		"system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral"}}],
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"name":"Read","input_schema":{"type":"object"}},{"type":"web_search_20250305","name":"web_search"},
			{"type":"code_execution_20250825","name":"code_execution"},{"type":"bash_20250124","name":"bash"}],
		"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"context_management":{"edits":[]},
		"metadata":{"user_id":"u"},"container":"c","mcp_servers":[{"url":"x"}],"service_tier":"priority"}`), 5000)
	for _, k := range []string{"container", "mcp_servers", "service_tier"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s must be dropped", k)
		}
	}
	for _, k := range []string{"system", "thinking", "output_config", "context_management", "metadata"} {
		if _, ok := got[k]; !ok {
			t.Errorf("%s must pass", k)
		}
	}
	tools := got["tools"].([]any)
	if len(tools) != 2 || tools[0].(map[string]any)["name"] != "Read" || tools[1].(map[string]any)["name"] != "bash" {
		t.Errorf("server tools must go, client tools stay: %v", tools)
	}
	if got["max_tokens"].(float64) != 5000 || got["stream"] != true || got["model"] != "upstream-model" {
		t.Errorf("node fields not set: %v %v %v", got["max_tokens"], got["stream"], got["model"])
	}
	// Only server tools: tools and tool_choice both go.
	got = prepared(t, protocol.APIMessages, []byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"web_fetch_20250910","name":"f"}],"tool_choice":{"type":"auto"}}`), 10)
	if _, ok := got["tools"]; ok {
		t.Error("tools must be dropped")
	}
	if _, ok := got["tool_choice"]; ok {
		t.Error("tool_choice must be dropped with the tools")
	}
}

func TestPrepareResponsesPolicy(t *testing.T) {
	got := prepared(t, protocol.APIResponses, []byte(`{"model":"gpt-5.5","input":[{"type":"message","role":"user","content":"hi"}],
		"instructions":"i","store":true,"background":true,"service_tier":"priority","client_metadata":{"a":"b"},
		"include":["reasoning.encrypted_content","file_search_call.results"],"prompt_cache_key":"k","max_output_tokens":99999,
		"tools":[{"type":"function","name":"exec_command"},{"type":"custom","name":"apply_patch"},{"type":"web_search"},
			{"type":"tool_search","execution":"client"},{"type":"tool_search","execution":"server"},{"type":"image_generation"}],
		"tool_choice":{"type":"web_search"}}`), 777)
	if got["store"] != false {
		t.Error("store must be forced off")
	}
	for _, k := range []string{"background", "service_tier", "client_metadata"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s must be dropped", k)
		}
	}
	if inc := got["include"].([]any); len(inc) != 1 || inc[0] != "reasoning.encrypted_content" {
		t.Errorf("include = %v", inc)
	}
	var kept []string
	for _, tl := range got["tools"].([]any) {
		m := tl.(map[string]any)
		kept = append(kept, m["type"].(string))
	}
	if strings.Join(kept, ",") != "function,custom,tool_search" {
		t.Errorf("tools kept = %v", kept)
	}
	if got["tool_choice"] != "auto" {
		t.Errorf("a choice of a dropped tool must become auto, got %v", got["tool_choice"])
	}
	if got["max_output_tokens"].(float64) != 777 || got["prompt_cache_key"] != "k" {
		t.Errorf("max_output_tokens = %v", got["max_output_tokens"])
	}
	for _, f := range []string{`"previous_response_id":"resp_1"`, `"conversation":"conv_1"`} {
		if _, err := prepareBody(protocol.APIResponses, []byte(`{"input":"x",`+f+`}`), "m", 10); protocol.CodeOf(err) != protocol.CodeBadRequest {
			t.Errorf("%s: want bad_request, got %v", f, err)
		}
	}
}

func TestUpstreamRequestLimitsAndHeaders(t *testing.T) {
	tk := &protocol.Ticket{API: protocol.APIMessages, PriceInMicros: 1_000_000, PriceOutMicros: 1_000_000, ReservedMicros: 1_000_000}
	h := protocol.RequestHeader{"anthropic-version": "2023-06-01", "anthropic-beta": "interleaved-thinking-2025-05-14,files-api-2025-04-14, context-1m-2025-08-07,mcp-client-2025-04-04", "authorization": "Bearer x"}
	p, err := UpstreamRequest(tk, h, []byte(`{"model":"c","max_tokens":200,"messages":[{"role":"user","content":"hello"}]}`), "up")
	if err != nil {
		t.Fatal(err)
	}
	if p.Limit != 200 || p.API != protocol.APIMessages {
		t.Errorf("limit %d api %s", p.Limit, p.API)
	}
	if p.Header["anthropic-beta"] != "interleaved-thinking-2025-05-14,context-1m-2025-08-07" || p.Header["authorization"] != "" || p.Header["anthropic-version"] != "2023-06-01" {
		t.Errorf("headers = %v", p.Header)
	}
	// No limit asked: the hard cap of the API.
	p, _ = UpstreamRequest(tk, nil, []byte(`{"model":"c","messages":[{"role":"user","content":"hello"}]}`), "up")
	if p.Limit != protocol.HardMaxOutput(protocol.APIMessages) {
		t.Errorf("limit %d, want the hard cap", p.Limit)
	}
	tk.API = ""
	p, _ = UpstreamRequest(tk, nil, []byte(`{"model":"c","messages":[{"role":"user","content":"hello"}]}`), "up")
	if p.Limit != HardMaxOutputTokens || p.API != protocol.APIChat {
		t.Errorf("chat limit %d api %q", p.Limit, p.API)
	}
	tk.ReservedMicros = 0
	if _, err := UpstreamRequest(tk, nil, []byte(`{"messages":[{"role":"user","content":"x"}]}`), "m"); protocol.CodeOf(err) != protocol.CodeBudget {
		t.Errorf("zero budget: want budget error, got %v", err)
	}
	tk.ReservedMicros, tk.API = 100, protocol.APIChat
	if _, err := UpstreamRequest(tk, protocol.RequestHeader{protocol.HeaderOp: protocol.OpCompact}, []byte(`{"messages":[{"role":"user","content":"x"}]}`), "m"); protocol.CodeOf(err) != protocol.CodeBadRequest {
		t.Errorf("compact on chat: want bad_request, got %v", err)
	}
}

func TestRequestSession(t *testing.T) {
	cases := []struct {
		api  string
		h    protocol.RequestHeader
		body string
		want string
	}{
		{protocol.APIChat, nil, `{"session_id":"s1"}`, "s1"},
		{protocol.APIMessages, nil, `{"metadata":{"user_id":"{\"device_id\":\"d\",\"session_id\":\"cc-1\"}"}}`, "cc-1"},
		{protocol.APIMessages, nil, `{"metadata":{"user_id":"user_abc_account__session_old-2"}}`, "old-2"},
		{protocol.APIMessages, protocol.RequestHeader{"x-claude-code-session-id": "hdr"}, `{}`, "hdr"},
		{protocol.APIResponses, protocol.RequestHeader{"session_id": "h"}, `{"prompt_cache_key":"pck"}`, "pck"},
		{protocol.APIResponses, protocol.RequestHeader{"session_id": "h"}, `{}`, "h"},
	}
	for _, c := range cases {
		got, err := requestSession(c.api, c.h, []byte(c.body))
		if err != nil || got != c.want {
			t.Errorf("%s %s: %q, %v; want %q", c.api, c.body, got, err, c.want)
		}
	}
}

func TestCheckTicket(t *testing.T) {
	keys := func() *p2p.Keys {
		k, err := p2p.NewKeys()
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	coord, self, buyer, mallory := keys(), keys(), keys(), keys()
	now := time.Now()
	id := identity{self: self.ID(), coordinator: coord.ID(), boxPub: self.Box.Public(), receiptKey: self.ReceiptKey()}
	models := map[string]ModelConfig{"m": {ID: "m", UpstreamModel: "u", PriceInMicros: 10, PriceOutMicros: 20}}

	base := func() protocol.Ticket {
		return protocol.Ticket{InferVersion: protocol.InferVersion,
			JobID: "j", Model: "m", Node: self.ID(), NodeBoxPub: self.Box.Public(), NodeReceiptKey: self.ReceiptKey(),
			Buyer: buyer.ID(), PriceInMicros: 10, PriceOutMicros: 20, PriceCacheRead: 1, PriceCacheWrite: 10, ReservedMicros: 100,
			ExpiresAt: now.Add(time.Minute).UnixNano(), Coordinator: coord.ID(),
		}
	}
	signed := func(mut func(*protocol.Ticket), signer *p2p.Keys) *protocol.Ticket {
		tk := base()
		if mut != nil {
			mut(&tk)
		}
		tk.Sign(signer.Signer)
		return &tk
	}

	if _, err := checkTicket(signed(nil, coord), buyer.ID(), id, models, now); err != nil {
		t.Fatalf("valid ticket rejected: %v", err)
	}
	cases := map[string]struct {
		tk     *protocol.Ticket
		remote string
	}{
		"presented by someone else":   {signed(nil, coord), mallory.ID()},
		"signed by an untrusted peer": {signed(func(tk *protocol.Ticket) { tk.Coordinator = mallory.ID() }, mallory), buyer.ID()},
		"for another node":            {signed(func(tk *protocol.Ticket) { tk.Node = mallory.ID() }, coord), buyer.ID()},
		"expired":                     {signed(func(tk *protocol.Ticket) { tk.ExpiresAt = now.Add(-time.Second).UnixNano() }, coord), buyer.ID()},
		"price below current offer":   {signed(func(tk *protocol.Ticket) { tk.PriceOutMicros = 1 }, coord), buyer.ID()},
		"cache price below the offer": {signed(func(tk *protocol.Ticket) { tk.PriceCacheWrite = 9 }, coord), buyer.ID()},
		"unknown API":                 {signed(func(tk *protocol.Ticket) { tk.API = "completions" }, coord), buyer.ID()},
		"foreign box key":             {signed(func(tk *protocol.Ticket) { tk.NodeBoxPub = mallory.Box.Public() }, coord), buyer.ID()},
		"model not served":            {signed(func(tk *protocol.Ticket) { tk.Model = "other" }, coord), buyer.ID()},
	}
	for name, c := range cases {
		if _, err := checkTicket(c.tk, c.remote, id, models, now); protocol.CodeOf(err) != protocol.CodeBadTicket {
			t.Errorf("%s: want bad_ticket, got %v", name, err)
		}
	}
}

// Tools that would run on the seller's account (a chat server's hosted
// tools) are dropped from chat requests; function tools stay.
func TestPrepareChatDropsHostedTools(t *testing.T) {
	got := prepared(t, protocol.APIChat, []byte(`{"messages":[{"role":"user","content":"hi"}],
		"tools":[{"type":"web_search"},{"type":"function","function":{"name":"f","parameters":{}}},{"type":"code_execution"}],
		"tool_choice":{"type":"web_search"}}`), 100)
	tools, _ := got["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["type"] != "function" {
		t.Fatalf("tools = %v, want the function tool only", got["tools"])
	}
	if got["tool_choice"] != "auto" {
		t.Errorf("tool_choice = %v, want auto", got["tool_choice"])
	}
	got = prepared(t, protocol.APIChat, []byte(`{"messages":[{"role":"user","content":"hi"}],
		"tools":[{"type":"function","function":{"name":"f","parameters":{}}}],"tool_choice":{"type":"function","function":{"name":"f"}}}`), 100)
	if tc, _ := got["tool_choice"].(map[string]any); tc["type"] != "function" {
		t.Errorf("a function tool_choice must stay: %v", got["tool_choice"])
	}
	got = prepared(t, protocol.APIChat, []byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"x_search"}],"tool_choice":"required"}`), 100)
	if _, ok := got["tools"]; ok {
		t.Errorf("tools = %v, want none", got["tools"])
	}
	if _, ok := got["tool_choice"]; ok {
		t.Errorf("tool_choice without tools: %v", got["tool_choice"])
	}
}

// Header values the buyer chose reach the model server only as plain tokens:
// a control character would fail the seller's request, at the seller's cost.
func TestUpstreamHeaderValuesArePlainTokens(t *testing.T) {
	h := upstreamHeader(protocol.APIMessages, protocol.RequestHeader{
		"anthropic-version": "2023-06-01\r\nx-evil: 1",
		"anthropic-beta":    "interleaved-thinking-2025-05-14,context-1m-2025\x00-08-07,context-1m-2025-08-07 x",
	})
	if _, ok := h["anthropic-version"]; ok || h["anthropic-beta"] != "interleaved-thinking-2025-05-14" {
		t.Errorf("headers = %q", h)
	}
	h = upstreamHeader(protocol.APIResponses, protocol.RequestHeader{"session_id": "abc\ndef"})
	if len(h) != 0 {
		t.Errorf("headers = %q", h)
	}
	h = upstreamHeader(protocol.APIResponses, protocol.RequestHeader{"session_id": "0199a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"})
	if h["session_id"] == "" {
		t.Error("a UUID session id must pass")
	}
}

// A usage report of no tokens at all (a converted stream has to carry a
// usage object even when the model server sent none) is no report: the node
// bills its own estimate, as when there is none.
func TestBillTreatsAnEmptyUsageReportAsNone(t *testing.T) {
	prep := &Prepared{Request: Request{Limit: 1000}, EstPrompt: 40}
	text := strings.Repeat("word ", 50)
	var none, zero, real protocol.UsageMeter
	zero.Add(protocol.ChunkInfo{Usage: &protocol.Usage{}})
	real.Add(protocol.ChunkInfo{Usage: &protocol.Usage{PromptTokens: 42, CompletionTokens: 55}})
	want := bill(prep, &none, text, &Billing{})
	if want.PromptTokens != 40 || want.CompletionTokens == 0 {
		t.Fatalf("estimate: %+v", want)
	}
	if got := bill(prep, &zero, text, &Billing{}); got != want {
		t.Errorf("an empty report billed %+v, want the estimate %+v", got, want)
	}
	if got := bill(prep, &real, text, &Billing{}); got.PromptTokens != 42 || got.CompletionTokens != 55 {
		t.Errorf("a real report billed %+v", got)
	}
}
