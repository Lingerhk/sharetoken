package provider

import (
	"context"
	"encoding/json"
	"errors"
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

// scripted answers each request with the next answer: an SSE stream of the
// events (separated by a newline) for 200, the text as the body otherwise.
// The last answer repeats. It returns the bodies it was sent.
type scripted struct {
	Status int
	Body   string
	Header map[string]string
}

func scriptedServer(t *testing.T, answers ...scripted) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var sent []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		a := answers[min(len(sent), len(answers)-1)]
		sent = append(sent, body)
		for k, v := range a.Header {
			w.Header().Set(k, v)
		}
		if a.Status != http.StatusOK {
			w.WriteHeader(a.Status)
			_, _ = io.WriteString(w, a.Body)
			return
		}
		for _, ev := range strings.Split(a.Body, "\n") {
			fmt.Fprintf(w, "data: %s\n\n", ev)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &sent
}

const chatOK = `{"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`

func fastRetries(t *testing.T) {
	t.Helper()
	old := transientBackoff
	transientBackoff = time.Millisecond
	t.Cleanup(func() { transientBackoff = old })
}

// The buyer's betas reach the seller's account by the account and the model:
// the long context only where the model has it, nothing Claude Code does not
// send on a subscription login, and only format betas on a rebuilt request.
func TestBuyerBetasByAccount(t *testing.T) {
	const client = "interleaved-thinking-2025-05-14, context-1m-2025-08-07,output-128k-2025-02-19,computer-use-2025-01-24,mid-conversation-tool-changes-2026-07-01"
	for _, c := range []struct {
		name           string
		model          string
		oauth, rebuilt bool
		want           string
	}{
		{"key", "claude-opus-4-8", false, false, "interleaved-thinking-2025-05-14,output-128k-2025-02-19,computer-use-2025-01-24,mid-conversation-tool-changes-2026-07-01"},
		{"key, long context model", "claude-sonnet-5-5", false, false, "interleaved-thinking-2025-05-14,context-1m-2025-08-07,output-128k-2025-02-19,computer-use-2025-01-24,mid-conversation-tool-changes-2026-07-01"},
		{"login, Claude Code's request", "claude-sonnet-4-6", true, false, "interleaved-thinking-2025-05-14,mid-conversation-tool-changes-2026-07-01"},
		{"login, long context model", "claude-sonnet-5", true, false, "interleaved-thinking-2025-05-14,context-1m-2025-08-07,mid-conversation-tool-changes-2026-07-01"},
		{"login, rebuilt", "claude-sonnet-5", true, true, "mid-conversation-tool-changes-2026-07-01"},
		{"not Sonnet 5", "claude-sonnet-50", false, false, "interleaved-thinking-2025-05-14,output-128k-2025-02-19,computer-use-2025-01-24,mid-conversation-tool-changes-2026-07-01"},
	} {
		if got := strings.Join(buyerBetas(client, c.model, c.oauth, c.rebuilt), ","); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestToolsetDropsFineGrainedStreaming(t *testing.T) {
	req := map[string]any{"tools": []any{map[string]any{"type": "computer_toolset_20260801"}}}
	h := http.Header{"Anthropic-Beta": {"claude-code-20250219,fine-grained-tool-streaming-2025-05-14"}}
	dropToolsetStreamingBeta(h, req, "claude-opus-5-5")
	if !strings.Contains(h.Get("anthropic-beta"), "fine-grained") {
		t.Fatal("dropped for a model that takes both")
	}
	dropToolsetStreamingBeta(h, req, "claude-sonnet-5-5")
	if h.Get("anthropic-beta") != "claude-code-20250219" {
		t.Fatalf("betas %q", h.Get("anthropic-beta"))
	}
}

// On the wire: a subscription login's rebuilt request carries the profile's
// betas and none of the buyer's that would change what the account is billed.
func TestProfileRequestKeepsItsOwnBetas(t *testing.T) {
	srv, reqs, _ := anthropicServer(t, messagesOK...)
	e := parityEntry()
	e.BaseURL = srv.URL
	up, err := e.Upstream(FixedToken(e.OAuth))
	if err != nil {
		t.Fatal(err)
	}
	header := protocol.RequestHeader{"anthropic-beta": "context-1m-2025-08-07,output-128k-2025-02-19,computer-use-2025-01-24,interleaved-thinking-2025-05-14"}
	body := `{"model":"claude-sonnet-4-6","max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`
	if err := up.Stream(context.Background(), &node.Request{API: protocol.APIMessages, Header: header, Body: []byte(body)}, discard); err != nil {
		t.Fatal(err)
	}
	if got := (<-reqs).Header.Get("anthropic-beta"); got != strings.Join(claudeProfileBetas, ",") {
		t.Fatalf("betas %s", got)
	}
}

// A 429 without Retry-After still tells when the limit ends, each provider
// in its own way; one that does not leaves the default hold.
func TestLimitResetIsReadFromTheRefusal(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	for name, c := range map[string]struct {
		header map[string]string
		body   string
		want   time.Duration
	}{
		"anthropic window": {header: map[string]string{"anthropic-ratelimit-unified-reset": "1800003600"}, want: time.Hour},
		"anthropic past":   {header: map[string]string{"anthropic-ratelimit-unified-reset": "1700000000"}},
		"codex headers": {header: map[string]string{"x-codex-primary-used-percent": "100", "x-codex-primary-reset-after-seconds": "900",
			"x-codex-secondary-used-percent": "40", "x-codex-secondary-reset-after-seconds": "90000"}, want: 15 * time.Minute},
		"codex resets_at":  {body: `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","resets_at":1800007200,"resets_in_seconds":7200}}`, want: 2 * time.Hour},
		"codex resets_in":  {body: `{"error":{"type":"usage_limit_reached","resets_in_seconds":133107}}`, want: 133107 * time.Second},
		"other error type": {body: `{"error":{"type":"invalid_request_error","resets_in_seconds":60}}`},
		"gemini quota":     {body: `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","metadata":{"quotaResetDelay":"1h2m3s"}}]}}`, want: time.Hour + 2*time.Minute + 3*time.Second},
		"code assist list": {body: `[{"error":{"code":429,"details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"39s"}]}}]`, want: 39 * time.Second},
		"gemini message":   {body: `{"error":{"code":429,"message":"You exceeded your current quota. Please retry in 12.5s."}}`, want: 12500 * time.Millisecond},
		"a year":           {body: `{"error":{"type":"usage_limit_reached","resets_in_seconds":31536000}}`, want: maxCooldown},
		"nothing":          {body: `{"error":{"message":"slow down"}}`},
	} {
		h := http.Header{}
		for k, v := range c.header {
			h.Set(k, v)
		}
		got := limitReset(h, c.body, now)
		if c.want == 0 && !got.IsZero() || c.want != 0 && got.Sub(now) != c.want {
			t.Errorf("%s: reset in %v, want %v", name, got.Sub(now), c.want)
		}
	}
	// On the wire: the provider is held until then, not for the default minute.
	resp := &http.Response{StatusCode: 429, Header: http.Header{}}
	se := responseStatus(resp, `{"error":{"type":"usage_limit_reached","resets_in_seconds":7200}}`)
	owner := t.Name()
	defer recordProviderStatus(owner, nil)
	recordProviderStatus(owner, se)
	if left := time.Until(providerStatus(owner).RetryAt); left < 119*time.Minute || left > 121*time.Minute {
		t.Fatalf("held for %v", left)
	}
}

// A model server that fails before answering is asked again; one that
// refused the request, or failed after part of the answer went on, is not.
func TestTransientFailuresAreRetried(t *testing.T) {
	fastRetries(t)
	run := func(answers ...scripted) (int, error) {
		t.Helper()
		srv, sent := scriptedServer(t, answers...)
		owner := fmt.Sprint(t.Name(), len(answers), answers[0].Status, answers[0].Body)
		t.Cleanup(func() { recordProviderStatus(owner, nil) })
		up := &outboundUpstream{up: &openAIUpstream{base: srv.URL, header: http.Header{}}, client: http.DefaultClient, owner: owner}
		err := up.Stream(context.Background(), &node.Request{API: protocol.APIChat, Body: []byte(`{"model":"m","messages":[]}`)}, discard)
		return len(*sent), err
	}
	ok := scripted{Status: 200, Body: chatOK}
	for _, status := range []int{500, 502, 503, 504, 529} {
		if n, err := run(scripted{Status: status, Body: "overloaded"}, scripted{Status: status, Body: "overloaded"}, ok); err != nil || n != 3 {
			t.Errorf("HTTP %d: %d requests, %v", status, n, err)
		}
	}
	var se *node.StatusError
	if n, err := run(scripted{Status: 529, Body: "overloaded"}); !errors.As(err, &se) || se.Status != 529 || n != 1+transientRetries {
		t.Errorf("a server that stays overloaded: %d requests, %v", n, err)
	}
	// A rate limit that ends at once is waited out; a longer one holds the provider.
	if n, err := run(scripted{Status: 429, Body: "slow down", Header: map[string]string{"Retry-After": "0"}}, ok); err != nil || n != 2 {
		t.Errorf("short rate limit: %d requests, %v", n, err)
	}
	if n, err := run(scripted{Status: 429, Body: "slow down", Header: map[string]string{"Retry-After": "600"}}, ok); err == nil || n != 1 {
		t.Errorf("long rate limit: %d requests, %v", n, err)
	}
	for _, status := range []int{400, 401, 403, 404} {
		if n, err := run(scripted{Status: status, Body: "no"}, ok); err == nil || n != 1 {
			t.Errorf("HTTP %d: %d requests, %v", status, n, err)
		}
	}
	// An error after the first chunk: the buyer has part of the answer.
	if n, err := run(scripted{Status: 200, Body: `{"choices":[{"index":0,"delta":{"content":"par"}}]}` + "\n" + `{"error":{"message":"overloaded"}}`}, ok); err == nil || n != 1 {
		t.Errorf("failure mid-answer: %d requests, %v", n, err)
	}
	// The same error as the first event came before any answer.
	if n, err := run(scripted{Status: 200, Body: `{"error":{"message":"overloaded"}}`}, ok); err != nil || n != 2 {
		t.Errorf("failure before the answer: %d requests, %v", n, err)
	}
	// A connection that breaks before the answer.
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits++; hits == 1 {
			conn, _, _ := w.(http.Hijacker).Hijack()
			_ = conn.Close()
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", chatOK)
	}))
	defer srv.Close()
	up := &outboundUpstream{up: &openAIUpstream{base: srv.URL, header: http.Header{}}, client: http.DefaultClient}
	if err := up.Stream(context.Background(), &node.Request{API: protocol.APIChat, Body: []byte(`{"model":"m","messages":[]}`)}, discard); err != nil || hits != 2 {
		t.Errorf("broken connection: %d requests, %v", hits, err)
	}
}

// Refused credentials hold the provider, so the jobs that follow are turned
// away instead of failing one by one; a login that cannot be renewed counts
// as refused.
func TestRefusedCredentialsHoldTheProvider(t *testing.T) {
	owner := t.Name()
	defer recordProviderStatus(owner, nil)
	recordProviderStatus(owner, &node.StatusError{Status: 401})
	u := &outboundUpstream{owner: owner}
	if left := time.Until(u.ReadyAt()); left < refusedCooldown-time.Minute || left > refusedCooldown {
		t.Fatalf("held for %v", left)
	}
	if err := u.Stream(context.Background(), nil, nil); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("hold: %v", err)
	}
	recordProviderStatus(owner, nil)
	recordProviderStatus(owner, &node.StatusError{Status: 403})
	if !u.ReadyAt().IsZero() {
		t.Fatal("a 403 held the provider")
	}

	home := t.TempDir()
	var refusal error = tokenError(400, "invalid_grant", "refresh token already used")
	kinds = append(kinds, Kind{ID: "testauth", Name: "Test", OAuth: true, OAuthURL: "https://api.example.com"})
	oauthFlows["testauth"] = &oauthFlow{refresh: func(context.Context, *Token) (*Token, error) { return nil, refusal }}
	t.Cleanup(func() { kinds = kinds[:len(kinds)-1]; delete(oauthFlows, "testauth") })
	put := func(expires time.Time) {
		e := &Entry{Name: "acct", Type: "testauth", Auth: AuthOAuth, Enabled: true, OAuth: &Token{AccessToken: "at", RefreshToken: "rt", ExpiresAt: expires}}
		if err := Update(home, func(f *File) error { return f.Put(e) }); err != nil {
			t.Fatal(err)
		}
	}
	ts := (&Tokens{Home: home}).For("acct")
	// Still good for a minute: used as it is.
	put(time.Now().Add(time.Minute))
	if tok, err := ts.Token(context.Background(), false); err != nil || tok.AccessToken != "at" {
		t.Fatalf("a token that is still good: %+v, %v", tok, err)
	}
	put(time.Now().Add(-time.Minute))
	var se *node.StatusError
	if _, err := ts.Token(context.Background(), false); !errors.As(err, &se) || se.Status != 401 || !strings.Contains(se.Body, "invalid_grant") {
		t.Fatalf("a spent login: %v", err)
	}
	// The login server could not be reached: nothing says the login is gone.
	refusal = errors.New("dial tcp: timeout")
	if _, err := ts.Token(context.Background(), false); err == nil || errors.As(err, &se) {
		t.Fatalf("an unreachable login server: %v", err)
	}
}

const thinkingHistory = `{"model":"claude-opus-4-8","max_tokens":4000,"stream":true,"thinking":{"type":"enabled","budget_tokens":2000},
	"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},
	"messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"text","text":""}]},
		{"role":"assistant","content":[{"type":"text","text":""},{"type":"tool_use","id":"t1","name":"f","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":""},{"type":"text","text":"7"}]}]}]}`

// A history without the thinking the model expects (it moved here from a
// model that does not think) is refused; the request goes again without
// thinking. Empty text blocks never go out.
func TestThinkingRefusalsGoWithoutThinking(t *testing.T) {
	for _, refusal := range []string{
		"messages.1.content.0.type: Expected `thinking` or `redacted_thinking`, but found `tool_use`. When `thinking` is enabled, a final `assistant` message must start with a thinking block",
		"messages.1.content.0: `thinking` or `redacted_thinking` blocks in the latest assistant message cannot be modified",
		"messages.1.content.0: Invalid `signature` in `thinking` block",
	} {
		body, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]any{"type": "invalid_request_error", "message": refusal}})
		srv, sent := scriptedServer(t, scripted{Status: 400, Body: string(body)}, scripted{Status: 200, Body: strings.Join(messagesOK, "\n")})
		up := &anthropicUpstream{base: srv.URL, header: http.Header{}}
		if err := up.Stream(context.Background(), &node.Request{API: protocol.APIMessages, Body: []byte(thinkingHistory)}, discard); err != nil {
			t.Fatalf("%s: %v", refusal, err)
		}
		if len(*sent) != 2 || (*sent)[0]["thinking"] == nil || (*sent)[1]["thinking"] != nil || (*sent)[1]["context_management"] != nil {
			t.Fatalf("%s: sent %s", refusal, asJSON(t, *sent))
		}
		if first := asJSON(t, (*sent)[0]); strings.Contains(first, `"text":""`) || !strings.Contains(first, `"content":[{"text":"7","type":"text"}]`) {
			t.Fatalf("empty text blocks went out: %s", first)
		}
	}
	// Another refusal is the buyer's to hear, once.
	srv, sent := scriptedServer(t, scripted{Status: 400, Body: `{"type":"error","error":{"message":"messages: at least one message is required"}}`})
	up := &anthropicUpstream{base: srv.URL, header: http.Header{}}
	if err := up.Stream(context.Background(), &node.Request{API: protocol.APIMessages, Body: []byte(thinkingHistory)}, discard); err == nil || len(*sent) != 1 {
		t.Fatalf("unrelated refusal: %v, %d requests", err, len(*sent))
	}
}

// DeepSeek wants the reasoning of the history back in both of its APIs: the
// thinking blocks it did not sign stay in a Messages request, an assistant
// message that lost its reasoning_content gets a blank one in a chat
// request, and "developer" messages go as "system".
func TestDeepSeekGetsItsReasoningBack(t *testing.T) {
	srv, sent := scriptedServer(t, scripted{Status: 200, Body: strings.Join(messagesOK, "\n")}, scripted{Status: 200, Body: chatOK})
	e := &Entry{ID: t.Name(), Name: "deepseek", Type: KindDeepSeek, Auth: AuthAPIKey, APIKey: "sk-ds", BaseURL: srv.URL}
	up, err := e.Upstream(nil)
	if err != nil {
		t.Fatal(err)
	}
	messages := `{"model":"deepseek-v4","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"},
		{"role":"assistant","content":[{"type":"thinking","thinking":"hm","signature":""},{"type":"tool_use","id":"t1","name":"f","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"7"}]}]}`
	if err := up.Stream(context.Background(), &node.Request{API: protocol.APIMessages, Body: []byte(messages)}, discard); err != nil {
		t.Fatal(err)
	}
	if got := asJSON(t, (*sent)[0]); !strings.Contains(got, `"thinking":"hm"`) {
		t.Fatalf("thinking was dropped: %s", got)
	}
	chat := `{"model":"deepseek-v4","max_tokens":100,"stream":true,"temperature":0.25,"messages":[{"role":"developer","content":"be brief"},{"role":"user","content":"hi"},
		{"role":"assistant","content":null,"tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"c1","content":"7"},
		{"role":"assistant","content":"seven","reasoning_content":"it said 7"},{"role":"user","content":"sure?"}]}`
	if err := up.Stream(context.Background(), &node.Request{API: protocol.APIChat, Body: []byte(chat)}, discard); err != nil {
		t.Fatal(err)
	}
	got := asJSON(t, (*sent)[1])
	wantAll(t, got, `{"content":"be brief","role":"system"}`, `"reasoning_content":" "`, `"reasoning_content":"it said 7"`, `"temperature":0.25`)
	if strings.Count(got, "reasoning_content") != 2 || strings.Contains(got, "developer") {
		t.Fatalf("chat request: %s", got)
	}
	// Other chat servers get the request as it is; a custom one at a strict
	// host gets the role only.
	plain := strictChat([]byte(chat), false, false)
	if string(plain) != chat {
		t.Fatal("the request changed for a server that needs nothing")
	}
	custom := (&openAIUpstream{base: "https://api.moonshot.cn/v1"}).chatQuirks(KindCustom)
	if other := (&openAIUpstream{base: "https://api.example.com/v1"}).chatQuirks(KindCustom); !custom.systemRole || custom.reasoningBack || other.systemRole {
		t.Fatalf("quirks by host: %+v %+v", custom, other)
	}
}

// A model that refuses a sampling setting gets the request again without
// it, and later requests without asking; what an answer depends on is never
// dropped.
func TestRefusedSamplingSettingsAreDropped(t *testing.T) {
	refuse := func(msg, param string) scripted {
		body, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg, "type": "invalid_request_error", "param": param, "code": "unsupported_value"}})
		return scripted{Status: 400, Body: string(body)}
	}
	srv, sent := scriptedServer(t,
		refuse("Unsupported value: 'temperature' does not support 0.2 with this model. Only the default (1) value is supported.", "temperature"),
		refuse("Unsupported parameter: 'top_p' is not supported with this model.", "top_p"),
		scripted{Status: 200, Body: chatOK})
	up := &openAIUpstream{base: srv.URL, header: http.Header{}, tokensField: "max_completion_tokens", responses: true}
	chat := []byte(`{"model":"gpt-x","max_tokens":100,"stream":true,"temperature":0.2,"top_p":0.9,"stop":["END"],"messages":[{"role":"user","content":"hi"}]}`)
	for range 2 {
		if err := up.Stream(context.Background(), &node.Request{API: protocol.APIChat, Body: chat}, discard); err != nil {
			t.Fatal(err)
		}
	}
	if len(*sent) != 4 {
		t.Fatalf("%d requests, want 3 for the first job and 1 for the second", len(*sent))
	}
	for i, has := range []string{"temperature top_p", "top_p", "", ""} {
		body := (*sent)[i]
		if (body["temperature"] != nil) != strings.Contains(has, "temperature") || (body["top_p"] != nil) != strings.Contains(has, "top_p") || body["stop"] == nil || body["max_completion_tokens"] == nil {
			t.Fatalf("request %d: %s", i, asJSON(t, body))
		}
	}
	// Another model of the provider is asked with its settings.
	other := []byte(`{"model":"gpt-y","max_tokens":100,"stream":true,"temperature":0.2,"messages":[]}`)
	if err := up.Stream(context.Background(), &node.Request{API: protocol.APIChat, Body: other}, discard); err != nil || (*sent)[4]["temperature"] == nil {
		t.Fatalf("another model: %v %s", err, asJSON(t, (*sent)[4]))
	}

	// A Responses request (Claude Code's, converted, carries a temperature).
	srv, sent = scriptedServer(t, refuse("Unsupported parameter: 'temperature' is not supported with this model.", "temperature"),
		scripted{Status: 200, Body: `{"type":"response.completed","response":{"id":"r1"}}`})
	up = &openAIUpstream{base: srv.URL, header: http.Header{}, responses: true}
	resp := []byte(`{"model":"gpt-x","max_output_tokens":100,"stream":true,"temperature":1,"input":"hi"}`)
	if err := up.Stream(context.Background(), &node.Request{API: protocol.APIResponses, Body: resp}, discard); err != nil || len(*sent) != 2 || (*sent)[1]["temperature"] != nil {
		t.Fatalf("responses: %v %s", err, asJSON(t, *sent))
	}

	// A refused stop sequence is the buyer's to hear: without it the answer
	// would run on.
	srv, sent = scriptedServer(t, refuse("Unsupported parameter: 'stop' is not supported with this model.", "stop"))
	up = &openAIUpstream{base: srv.URL, header: http.Header{}}
	var se *node.StatusError
	if err := up.Stream(context.Background(), &node.Request{API: protocol.APIChat, Body: chat}, discard); !errors.As(err, &se) || se.Status != 400 || len(*sent) != 1 {
		t.Fatalf("stop: %v, %d requests", err, len(*sent))
	}
	// A refusal that only quotes the buyer's text teaches nothing.
	srv, sent = scriptedServer(t, refuse("Invalid message: \"Unsupported parameter: 'temperature'\" is not valid JSON", "messages"))
	up = &openAIUpstream{base: srv.URL, header: http.Header{}}
	if err := up.Stream(context.Background(), &node.Request{API: protocol.APIChat, Body: chat}, discard); err == nil || len(*sent) != 2 || len(up.refused.byModel) != 0 {
		t.Fatalf("quoted text: %v, %d requests, learnt %v", err, len(*sent), up.refused.byModel)
	}
}

// The Codex backend stores nothing and takes no system messages: items go
// without the IDs an earlier answer gave them, references to stored items
// are left out, and system messages go as developer ones.
func TestCodexInputGoesAsCodexSendsIt(t *testing.T) {
	req, _, _, err := codexRequest([]byte(`{"model":"gpt-5.4","reasoning":{"effort":"minimal","summary":"auto"},"input":[
		{"type":"message","role":"system","content":"be brief"},
		{"type":"item_reference","id":"msg_0"},
		{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"OPAQUE"},
		{"type":"function_call","id":"fc_1","call_id":"call_1","name":"f","arguments":"{}"},
		{"type":"function_call_output","call_id":"call_1","output":"7"},
		{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"seven"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	got := asJSON(t, req)
	wantAll(t, got, `{"content":"be brief","role":"developer","type":"message"}`, `"encrypted_content":"OPAQUE"`, `"call_id":"call_1"`, `"effort":"none"`, `"summary":"auto"`)
	for _, banned := range []string{`"id"`, "item_reference", `"system"`, "minimal"} {
		if strings.Contains(got, banned) {
			t.Errorf("%s reached the Codex backend: %s", banned, got)
		}
	}
	if n := len(req["input"].([]any)); n != 5 {
		t.Fatalf("%d input items", n)
	}
}

// A Code Assist stream is whole when it gave a finishReason. A refused
// prompt has none and is an answer all the same; a tool call the backend
// could not read is a failure when nothing else came.
func TestGeminiStreamEnds(t *testing.T) {
	end := func(events ...string) (finish string, err error) {
		st := &geminiStream{out: newChunks("m", func(p []byte) error {
			if info, perr := protocol.ParseChunk(p); perr == nil && info.FinishReason != "" {
				finish = info.FinishReason
			}
			return nil
		})}
		for _, e := range events {
			if err := st.event([]byte(e)); err != nil {
				return "", err
			}
		}
		return finish, st.end()
	}
	const text = `{"response":{"candidates":[{"content":{"parts":[{"text":"Hi"}]}}]}}`
	if _, err := end(text); err == nil {
		t.Error("a stream without finishReason was accepted")
	}
	if finish, err := end(text, `{"response":{"candidates":[{"content":{"parts":[{"text":"!"}]},"finishReason":"STOP"}]}}`); err != nil || finish != "stop" {
		t.Errorf("STOP: %q %v", finish, err)
	}
	if finish, err := end(`{"response":{"promptFeedback":{"blockReason":"PROHIBITED_CONTENT"},"usageMetadata":{"promptTokenCount":9}}}`); err != nil || finish != "content_filter" {
		t.Errorf("refused prompt: %q %v", finish, err)
	}
	var se *node.StatusError
	if _, err := end(`{"response":{"candidates":[{"content":{},"finishReason":"MALFORMED_FUNCTION_CALL"}]}}`); !errors.As(err, &se) || se.Status != 502 {
		t.Errorf("unreadable tool call: %v", err)
	}
	if finish, err := end(text, `{"response":{"candidates":[{"content":{},"finishReason":"MALFORMED_FUNCTION_CALL"}]}}`); err != nil || finish != "stop" {
		t.Errorf("unreadable tool call after text: %q %v", finish, err)
	}
	// A rate limit inside the stream says when it ends.
	_, err := end(`{"error":{"code":429,"message":"quota","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"39s"}]}}`)
	if !errors.As(err, &se) || se.Status != 429 || time.Until(se.RetryAt) < 30*time.Second {
		t.Errorf("rate limit in the stream: %+v", err)
	}
}

// A Gemini CLI login signs the function calls of the history like an
// Antigravity one: Gemini 3 refuses a call without a thought signature.
func TestGeminiCLISignsFunctionCalls(t *testing.T) {
	srv, sent := scriptedServer(t, scripted{Status: 200, Body: `{"response":{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}}`})
	e := &Entry{ID: t.Name(), Name: "gemini", Type: KindGemini, Auth: AuthOAuth, BaseURL: srv.URL}
	up := &codeAssistUpstream{entry: e, ts: FixedToken(&Token{AccessToken: "at", ProjectID: "proj"})}
	if err := up.Stream(context.Background(), &node.Request{API: protocol.APIChat, Body: []byte(fullChat)}, discard); err != nil {
		t.Fatal(err)
	}
	if got := asJSON(t, (*sent)[0]); !strings.Contains(got, `"thoughtSignature":"skip_thought_signature_validator"`) || strings.Contains(got, "VALIDATED") {
		t.Fatalf("request: %s", got)
	}
}

func TestRefusedEffortAndSchemaAreRepaired(t *testing.T) {
	refuse := func(msg string) scripted {
		body, _ := json.Marshal(map[string]any{"error": map[string]any{"message": msg, "type": "invalid_request_error"}})
		return scripted{Status: 400, Body: string(body)}
	}
	none := "Unsupported value: 'none' is not supported with the 'gpt-5-mini' model. Supported values are: 'minimal', 'low', 'medium', and 'high'."
	done := scripted{Status: 200, Body: `{"type":"response.completed","response":{"id":"r1"}}`}
	srv, sent := scriptedServer(t, refuse(none), done, done)
	up := &openAIUpstream{base: srv.URL, header: http.Header{}, responses: true}
	resp := []byte(`{"model":"gpt-5-mini","max_output_tokens":100,"stream":true,"reasoning":{"effort":"none","summary":"auto"},"input":"hi"}`)
	for range 2 {
		if err := up.Stream(context.Background(), &node.Request{API: protocol.APIResponses, Body: resp}, discard); err != nil {
			t.Fatal(err)
		}
	}
	if len(*sent) != 3 {
		t.Fatalf("%d requests, want 2 for the first job and 1 for the second", len(*sent))
	}
	for i, want := range []string{"none", "minimal", "minimal"} {
		r, _ := (*sent)[i]["reasoning"].(map[string]any)
		if r["effort"] != want || r["summary"] != "auto" {
			t.Fatalf("request %d: %s", i, asJSON(t, (*sent)[i]))
		}
	}
	for want, have := range map[string][]string{"none": {"none", "low", "medium", "high"}, "high": {"low", "medium", "high"}} {
		from := map[string]string{"none": "minimal", "high": "xhigh"}[want]
		if got := nearestEffort(from, have); got != want {
			t.Fatalf("nearest to %s among %v: %q", from, have, got)
		}
	}

	// A model that does not reason, in both APIs' words.
	srv, sent = scriptedServer(t, refuse("Unrecognized request argument supplied: reasoning_effort"), scripted{Status: 200, Body: chatOK})
	up = &openAIUpstream{base: srv.URL, header: http.Header{}, tokensField: "max_tokens", responses: true}
	if err := up.Stream(context.Background(), &node.Request{API: protocol.APIChat, Body: []byte(`{"model":"gpt-4o-mini","max_tokens":10,"stream":true,"reasoning_effort":"low","messages":[]}`)}, discard); err != nil || len(*sent) != 2 || (*sent)[1]["reasoning_effort"] != nil {
		t.Fatalf("chat without reasoning: %v %s", err, asJSON(t, *sent))
	}
	srv, sent = scriptedServer(t, refuse("Unsupported parameter: 'reasoning.effort' is not supported with this model."), done)
	up = &openAIUpstream{base: srv.URL, header: http.Header{}, responses: true}
	plain := []byte(`{"model":"gpt-4o-mini","max_output_tokens":100,"stream":true,"reasoning":{"effort":"low"},"include":["reasoning.encrypted_content","file_search_call.results"],"input":"hi"}`)
	if err := up.Stream(context.Background(), &node.Request{API: protocol.APIResponses, Body: plain}, discard); err != nil || len(*sent) != 2 || (*sent)[1]["reasoning"] != nil || asJSON(t, (*sent)[1]["include"]) != `["file_search_call.results"]` {
		t.Fatalf("responses without reasoning: %v %s", err, asJSON(t, *sent))
	}

	// Gemini names no setting.
	srv, sent = scriptedServer(t, scripted{Status: 400, Body: `[{"error":{"code":400,"message":"Request contains an invalid argument.","status":"INVALID_ARGUMENT"}}]`}, scripted{Status: 200, Body: chatOK})
	up = &openAIUpstream{base: srv.URL, header: http.Header{}, tokensField: "max_tokens"}
	if err := up.Stream(context.Background(), &node.Request{API: protocol.APIChat, Body: []byte(`{"model":"gemini-x","max_tokens":10,"stream":true,"reasoning_effort":"none","messages":[]}`)}, discard); err != nil || len(*sent) != 2 || (*sent)[1]["reasoning_effort"] != "minimal" {
		t.Fatalf("gemini: %v %s", err, asJSON(t, *sent))
	}

	// A chat API without structured output: JSON mode, the schema in a system
	// message; remembered for the model.
	srv, sent = scriptedServer(t, refuse("This response_format type is unavailable now"), scripted{Status: 200, Body: chatOK}, scripted{Status: 200, Body: chatOK})
	up = &openAIUpstream{base: srv.URL, header: http.Header{}, tokensField: "max_tokens"}
	chat := []byte(`{"model":"deepseek-x","max_tokens":100,"stream":true,"response_format":{"type":"json_schema","json_schema":{"name":"p","schema":{"type":"object","properties":{"age":{"type":"integer"}}}}},"messages":[{"role":"user","content":"hi"}]}`)
	for range 2 {
		if err := up.Stream(context.Background(), &node.Request{API: protocol.APIChat, Body: chat}, discard); err != nil {
			t.Fatal(err)
		}
	}
	if len(*sent) != 3 {
		t.Fatalf("%d requests, want 3", len(*sent))
	}
	for _, i := range []int{1, 2} {
		body := (*sent)[i]
		format, _ := body["response_format"].(map[string]any)
		msgs, _ := body["messages"].([]any)
		first, _ := msgs[0].(map[string]any)
		text, _ := first["content"].(string)
		if format["type"] != "json_object" || len(format) != 1 || len(msgs) != 2 || first["role"] != "system" || !strings.Contains(text, `"age"`) || !strings.Contains(text, "JSON") {
			t.Fatalf("request %d: %s", i, asJSON(t, body))
		}
	}
	// JSON mode itself refused is the buyer's to hear.
	srv, sent = scriptedServer(t, refuse("This response_format type is unavailable now"), refuse("This response_format type is unavailable now"))
	up = &openAIUpstream{base: srv.URL, header: http.Header{}, tokensField: "max_tokens"}
	var se *node.StatusError
	if err := up.Stream(context.Background(), &node.Request{API: protocol.APIChat, Body: chat}, discard); !errors.As(err, &se) || se.Status != 400 || len(*sent) != 2 {
		t.Fatalf("refused twice: %v after %d requests", err, len(*sent))
	}
}

func TestToolCallsEndWithTheirFinishReason(t *testing.T) {
	stream := `{"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]}}]}` + "\n" +
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
	srv, _ := scriptedServer(t, scripted{Status: 200, Body: stream}, scripted{Status: 200, Body: chatOK})
	up := &openAIUpstream{base: srv.URL, header: http.Header{}, tokensField: "max_tokens"}
	chat := []byte(`{"model":"m","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	for _, want := range []string{"tool_calls", "stop"} {
		var got string
		err := up.Stream(context.Background(), &node.Request{API: protocol.APIChat, Body: chat}, func(p []byte) error {
			if info, err := protocol.ParseChunk(p); err == nil && info.FinishReason != "" {
				got = info.FinishReason
			}
			return nil
		})
		if err != nil || got != want {
			t.Fatalf("finish reason %q (%v), want %q", got, err, want)
		}
	}
}
