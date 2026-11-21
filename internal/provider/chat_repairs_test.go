package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"sharetoken/internal/node"
	"sharetoken/internal/protocol"
)

// A 429 that is about one request fails that job alone: the provider stays
// ready for the next. A 429 about the account still rests it.
func TestRequestLimitedDoesNotRestTheProvider(t *testing.T) {
	for _, c := range []struct {
		body   string
		status int // what the job fails with; 429 rests the provider
	}{
		{`{"error":{"message":"Request too large for gpt-4o in organization org-x on tokens per min (TPM): Limit 30000, Requested 45000. The input or output tokens must be reduced in order to run successfully.","type":"tokens","code":"rate_limit_exceeded"}}`, 413},
		{`{"error":{"message":"Rate limit reached for gpt-4o on tokens per min (TPM): Limit 30000, Requested 50000."}}`, 413},
		{`{"error":{"message":"This request's input exceeds the TPM limit of your plan"}}`, 413},
		{`{"error":{"message":"This model's maximum context length is 128000 tokens. However, your messages resulted in 130000 tokens."}}`, 400},
		{`{"error":{"message":"Rate limit reached for gpt-4o on tokens per min (TPM): Limit 30000, Used 28000, Requested 5000. Please try again in 4s."}}`, 429},
		{`{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached"}}`, 429},
		{`{"error":{"message":"You exceeded your current quota, please check your plan and billing details."}}`, 429},
		{`{"error":{"message":"Your requests exceeded the token rate limit of your current pricing tier."}}`, 429},
		{`Too many tokens, please wait before trying again; the context is full`, 429},
	} {
		t.Run(c.body[:min(40, len(c.body))], func(t *testing.T) {
			fastRetries(t)
			srv, sent := scriptedServer(t, scripted{Status: 429, Body: c.body}, scripted{Status: 200, Body: chatOK})
			e := &Entry{ID: t.Name(), Name: "relay", Type: KindCustom, Auth: AuthAPIKey, APIKey: "sk", BaseURL: srv.URL}
			defer recordProviderStatus(e.ID, nil)
			up, err := e.Upstream(nil)
			if err != nil {
				t.Fatal(err)
			}
			req := &node.Request{API: protocol.APIChat, Body: []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`)}
			var se *node.StatusError
			if err := up.Stream(context.Background(), req, discard); !errors.As(err, &se) || se.Status != c.status {
				t.Fatalf("first job: %v, want %d", err, c.status)
			}
			if len(*sent) != 1 {
				t.Fatalf("%d requests for the refused job", len(*sent))
			}
			err = up.Stream(context.Background(), req, discard)
			if c.status == 429 {
				if err == nil || len(*sent) != 1 {
					t.Fatalf("an account's limit did not rest the provider: %v", err)
				}
				return
			}
			if err != nil || len(*sent) != 2 {
				t.Fatalf("the next job: %v, %d requests", err, len(*sent))
			}
		})
	}
}

func refusal400(msg string) scripted {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg, "type": "invalid_request_error"}})
	return scripted{Status: 400, Body: string(body)}
}

// A strict OpenAI-compatible server's refusal of a request's shape is
// repaired once, and the repair is made before the first try from then on,
// for that model only.
func TestStrictServerShapesAreRepairedAndRemembered(t *testing.T) {
	const tools = `"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"},"strict":false}}]`
	for _, c := range []struct {
		name    string
		refusal string
		body    string
		ok      func(sent map[string]any) bool
	}{
		{"strict", "Unsupported Chat Completions API field: tools[0].function.strict",
			`{"model":"m","stream":true,` + tools + `,"messages":[{"role":"user","content":"hi"}]}`,
			func(s map[string]any) bool { return !strings.Contains(asJSONQuiet(s), "strict") }},
		{"strict pydantic", `{"detail":[{"type":"extra_forbidden","loc":["body","tools",0,"function","strict"],"msg":"Extra inputs are not permitted"}]}`,
			`{"model":"m","stream":true,` + tools + `,"messages":[{"role":"user","content":"hi"}]}`,
			func(s map[string]any) bool { return !strings.Contains(asJSONQuiet(s), "strict") }},
		{"text parts", "messages.0.content: Input should be a valid string",
			`{"model":"m","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}]}`,
			func(s map[string]any) bool { return strings.Contains(asJSONQuiet(s), `"content":"a\nb"`) }},
		{"token field", "Unsupported parameter: 'max_tokens' is not supported with this model. Use 'max_completion_tokens' instead.",
			`{"model":"m","stream":true,"max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`,
			func(s map[string]any) bool { return s["max_tokens"] == nil && s["max_completion_tokens"] == 100.0 }},
		{"token fields together", "max_tokens and max_completion_tokens are mutually exclusive",
			`{"model":"m","stream":true,"max_tokens":100,"max_completion_tokens":200,"messages":[{"role":"user","content":"hi"}]}`,
			func(s map[string]any) bool { return s["max_completion_tokens"] == nil && s["max_tokens"] == 100.0 }},
		{"prefill", "This model does not support assistant message prefill. The conversation must end with a user message.",
			`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"Sure,"}]}`,
			func(s map[string]any) bool {
				return strings.HasSuffix(asJSONQuiet(s["messages"]), `{"content":"Continue.","role":"user"}]`)
			}},
		{"reasoning back", "The reasoning_content in the thinking mode must be passed back to the API.",
			`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c1","content":"7"}]}`,
			func(s map[string]any) bool { return strings.Contains(asJSONQuiet(s), `"reasoning_content":" "`) }},
		{"reasoning missing", "Missing required parameter: 'messages[1].reasoning_content'",
			`{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"x"},{"role":"user","content":"again"}]}`,
			func(s map[string]any) bool { return strings.Contains(asJSONQuiet(s), `"reasoning_content":" "`) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			refused := refusal400(c.refusal)
			if strings.HasPrefix(c.refusal, "{") { // the server's own shape
				refused = scripted{Status: 400, Body: c.refusal}
			}
			srv, sent := scriptedServer(t, refused, scripted{Status: 200, Body: chatOK})
			up := (&openAIUpstream{base: srv.URL, header: http.Header{}}).chatQuirks(KindCustom)
			req := &node.Request{API: protocol.APIChat, Body: []byte(c.body)}
			for range 2 {
				if err := up.Stream(context.Background(), req, discard); err != nil {
					t.Fatal(err)
				}
			}
			if len(*sent) != 3 {
				t.Fatalf("%d requests, want 2 for the first job and 1 for the second", len(*sent))
			}
			if c.ok((*sent)[0]) {
				t.Fatalf("the first try was already repaired: %s", asJSON(t, (*sent)[0]))
			}
			for _, i := range []int{1, 2} {
				if !c.ok((*sent)[i]) {
					t.Fatalf("request %d not repaired: %s", i, asJSON(t, (*sent)[i]))
				}
			}
			// Another model of the provider goes as it came.
			other := strings.Replace(c.body, `"model":"m"`, `"model":"n"`, 1)
			if err := up.Stream(context.Background(), &node.Request{API: protocol.APIChat, Body: []byte(other)}, discard); err != nil || c.ok((*sent)[3]) {
				t.Fatalf("another model: %v %s", err, asJSON(t, (*sent)[3]))
			}
		})
	}
}

func asJSONQuiet(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// A refusal that does not fit the request (nothing to repair) is the
// buyer's to hear, after one try.
func TestShapeRefusalWithNothingToRepair(t *testing.T) {
	srv, sent := scriptedServer(t, refusal400("This model does not support assistant message prefill."))
	up := &openAIUpstream{base: srv.URL, header: http.Header{}}
	body := `{"model":"m","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	var se *node.StatusError
	if err := up.Stream(context.Background(), &node.Request{API: protocol.APIChat, Body: []byte(body)}, discard); !errors.As(err, &se) || se.Status != 400 || len(*sent) != 1 {
		t.Fatalf("%v, %d requests", err, len(*sent))
	}
}

// A tool call kept without an id gets one made from its place and content,
// the same on every request, and the tool message answering it the same.
func TestChatCallIDsAreMadeForCallsWithout(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"},
		{"role":"assistant","tool_calls":[{"id":"","type":"function","function":{"name":"f","arguments":"{}"}},{"type":"function","function":{"name":"g","arguments":"{\"a\":1}"}}]},
		{"role":"tool","tool_call_id":"","content":"1"},{"role":"tool","content":"2"},
		{"role":"assistant","tool_calls":[{"id":"call_kept","type":"function","function":{"name":"f","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"call_kept","content":"3"}]}`)
	out := chatCallIDs(body)
	if string(chatCallIDs(body)) != string(out) {
		t.Fatal("the ids differ from one request to the next")
	}
	var req struct {
		Messages []struct {
			ToolCalls []struct {
				ID string `json:"id"`
			} `json:"tool_calls"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatal(err)
	}
	m := req.Messages
	a, b := m[1].ToolCalls[0].ID, m[1].ToolCalls[1].ID
	if !strings.HasPrefix(a, "call_") || a == b || m[2].ToolCallID != a || m[3].ToolCallID != b {
		t.Fatalf("ids: %s", out)
	}
	if m[4].ToolCalls[0].ID != "call_kept" || m[5].ToolCallID != "call_kept" {
		t.Fatalf("a call with an id changed: %s", out)
	}
	whole := []byte(`{"model":"m","messages":[{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]}]}`)
	if string(chatCallIDs(whole)) != string(whole) {
		t.Fatal("a request whose calls have ids changed")
	}
}

// Model servers are dialled with keep-alive probes that find a dead
// connection in about 30 seconds, through every transport.
func TestProviderDialerKeepsAlive(t *testing.T) {
	d := providerDialer()
	if k := d.KeepAliveConfig; !k.Enable || k.Idle != 15*time.Second || k.Interval != 5*time.Second || k.Count != 3 {
		t.Fatalf("keep-alive: %+v", k)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		if c, err := ln.Accept(); err == nil {
			_ = c.Close()
		}
	}()
	c, err := d.DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	checkUserTimeout(t, c)
	_ = c.Close()
	for _, o := range []TransportOptions{{}, {ProxyMode: "direct"}, {TLSProfile: "chrome"}} {
		if makeProviderHTTPClient(o) == nil {
			t.Fatalf("no client for %+v", o)
		}
	}
	if tr, ok := makeProviderHTTPClient(TransportOptions{}).Transport.(*http.Transport); !ok || tr.DialContext == nil || tr.ResponseHeaderTimeout != headerTimeout {
		t.Fatal("the native transport lost its dialer or header timeout")
	}
}
