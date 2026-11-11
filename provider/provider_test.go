package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"sharetoken/node"
	"sharetoken/protocol"
)

// A chat request with everything the converters have to carry over.
const fullChat = `{"model":"m-1","stream":true,"max_tokens":900,"temperature":0.4,"top_p":0.9,"stop":["END"],
"messages":[
 {"role":"system","content":"Be brief."},
 {"role":"user","content":[{"type":"text","text":"What is in the picture?"},{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}}]},
 {"role":"assistant","content":"Let me look.","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"cat\"}"}}]},
 {"role":"tool","tool_call_id":"call_1","content":"a cat"},
 {"role":"user","content":"And now?"}],
"tools":[{"type":"function","function":{"name":"lookup","description":"Look something up","parameters":{"type":"object","properties":{"q":{"type":["string","null"],"minLength":1}},"required":["q","gone"],"additionalProperties":false}}}],
"tool_choice":"required"}`

func mustChat(t *testing.T) *chatRequest {
	t.Helper()
	c, err := parseChat([]byte(fullChat))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func asJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func wantAll(t *testing.T, got string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(got, p) {
			t.Errorf("missing %s in\n%s", p, got)
		}
	}
}

func TestAnthropicRequest(t *testing.T) {
	req, err := anthropicRequest(mustChat(t), 0)
	if err != nil {
		t.Fatal(err)
	}
	got := asJSON(t, req)
	wantAll(t, got,
		`"system":[{"text":"Be brief.","type":"text"}]`,
		`"max_tokens":900`, `"temperature":0.4`, `"stop_sequences":["END"]`,
		`{"source":{"data":"QUJD","media_type":"image/png","type":"base64"},"type":"image"}`,
		`{"id":"call_1","input":{"q":"cat"},"name":"lookup","type":"tool_use"}`,
		// The tool result and the next user message are one user turn.
		`{"content":[{"content":[{"text":"a cat","type":"text"}],"tool_use_id":"call_1","type":"tool_result"},{"text":"And now?","type":"text"}],"role":"user"}`,
		`"tool_choice":{"type":"any"}`, `"input_schema":{`)
	if strings.Contains(got, "top_p") {
		t.Error("temperature and top_p must not go out together")
	}
	// A model with a small output limit gets no more than it takes.
	c := mustChat(t)
	c.Model, c.MaxTokens = "claude-3-5-haiku-20241022", 32768
	if req, _ = anthropicRequest(c, 0); req["max_tokens"] != uint64(8192) {
		t.Errorf("max_tokens = %v", req["max_tokens"])
	}
}

// collect runs a stream parser over SSE payloads and returns what a buyer reads.
func collect(t *testing.T, feed func(emit func([]byte) error) error) (text, reasoning, finish string, calls []protocol.ToolCall, usage *protocol.Usage) {
	t.Helper()
	var acc protocol.ToolCallAccumulator
	err := feed(func(p []byte) error {
		info, err := protocol.ParseChunk(p)
		if err != nil {
			t.Fatalf("not a chunk: %s", p)
		}
		text += info.Content
		reasoning += info.Reasoning
		acc.Add(info.ToolCalls)
		if info.FinishReason != "" {
			finish = info.FinishReason
		}
		if info.Usage != nil {
			usage = info.Usage
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return text, reasoning, finish, acc.Calls(), usage
}

func TestAnthropicStream(t *testing.T) {
	events := []string{
		`{"type":"message_start","message":{"usage":{"input_tokens":10,"cache_read_input_tokens":5,"cache_creation_input_tokens":2,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hi "}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"there"}}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"lookup"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"q\":"}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"cat\"}"}}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":42}}`,
		`{"type":"message_stop"}`,
	}
	text, reasoning, finish, calls, usage := collect(t, func(emit func([]byte) error) error {
		st := &anthropicStream{out: newChunks("m", emit), tool: map[int]int{}}
		for _, e := range events {
			if err := st.event([]byte(e)); err != nil {
				return err
			}
		}
		return nil
	})
	if text != "Hi there" || reasoning != "hmm" || finish != "tool_calls" {
		t.Fatalf("text %q reasoning %q finish %q", text, reasoning, finish)
	}
	if len(calls) != 1 || calls[0].ID != "toolu_1" || calls[0].Function.Name != "lookup" || calls[0].Function.Arguments != `{"q":"cat"}` {
		t.Fatalf("calls %+v", calls)
	}
	if usage == nil || usage.PromptTokens != 17 || usage.CompletionTokens != 42 {
		t.Fatalf("usage %+v", usage)
	}
	// An error in the stream fails the job as the server's.
	st := &anthropicStream{out: newChunks("m", func([]byte) error { return nil }), tool: map[int]int{}}
	var se *node.StatusError
	if err := st.event([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`)); !errors.As(err, &se) || se.Status != 503 {
		t.Fatalf("error event: %v", err)
	}
}

func TestResponsesRequestAndStream(t *testing.T) {
	c := mustChat(t)
	c.Tools[0].Function.Name = "python" // reserved by the backend
	c.Messages[2].ToolCalls[0].Function.Name = "python"
	req, err := responsesRequest(c)
	if err != nil {
		t.Fatal(err)
	}
	got := asJSON(t, req)
	wantAll(t, got,
		`"instructions":"Be brief."`, `"store":false`, `"stream":true`, `"include":["reasoning.encrypted_content"]`,
		`{"image_url":"data:image/png;base64,QUJD","type":"input_image"}`,
		`{"content":[{"text":"Let me look.","type":"output_text"}],"role":"assistant","type":"message"}`,
		`{"arguments":"{\"q\":\"cat\"}","call_id":"call_1","name":"python__st","type":"function_call"}`,
		`{"call_id":"call_1","output":"a cat","type":"function_call_output"}`,
		`"name":"python__st","parameters":`, `"tool_choice":"required"`)
	for _, banned := range []string{"temperature", "top_p", "max_tokens", "max_output_tokens", `"stop"`} {
		if strings.Contains(got, banned) {
			t.Errorf("%s must not reach the Codex backend: %s", banned, got)
		}
	}

	events := []string{
		`{"type":"response.created","response":{"id":"resp_1"}}`,
		`{"type":"response.reasoning_summary_text.delta","delta":"thinking"}`,
		`{"type":"response.output_text.delta","delta":"Hel"}`,
		`{"type":"response.output_text.delta","delta":"lo"}`,
		`{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","call_id":"call_9","name":"python__st"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":2,"delta":"{\"code\":"}`,
		`{"type":"response.function_call_arguments.delta","output_index":2,"delta":"\"1\"}"}`,
		`{"type":"response.completed","response":{"usage":{"input_tokens":31,"output_tokens":12}}}`,
	}
	text, reasoning, finish, calls, usage := collect(t, func(emit func([]byte) error) error {
		st := &responsesStream{out: newChunks("m", emit), tool: map[int]int{}}
		for _, e := range events {
			if err := st.event([]byte(e)); err != nil {
				return err
			}
		}
		return nil
	})
	if text != "Hello" || reasoning != "thinking" || finish != "tool_calls" || usage == nil || usage.PromptTokens != 31 || usage.CompletionTokens != 12 {
		t.Fatalf("text %q reasoning %q finish %q usage %+v", text, reasoning, finish, usage)
	}
	if len(calls) != 1 || calls[0].Function.Name != "python" || calls[0].Function.Arguments != `{"code":"1"}` {
		t.Fatalf("calls %+v", calls)
	}
}

// The Codex backend takes no output limit: the stream is cut where the
// buyer's reservation ends.
func TestCodexStreamIsCutAtTheOutputLimit(t *testing.T) {
	var sent int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at-1" || r.Header.Get("chatgpt-account-id") != "acct-1" || r.Header.Get("originator") != "codex_cli_rs" ||
			!strings.HasPrefix(r.Header.Get("User-Agent"), "codex_cli_rs/"+r.Header.Get("version")) || r.Header.Get("session_id") == "" {
			http.Error(w, "bad headers", http.StatusNotFound)
			return
		}
		fl := w.(http.Flusher)
		for range 2000 {
			if _, err := fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"word word word word \"}\n\n"); err != nil {
				return
			}
			sent++
			fl.Flush()
		}
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":5,\"output_tokens\":9999}}}\n\n")
	}))
	defer srv.Close()
	up := &codexUpstream{base: srv.URL, ts: FixedToken(&Token{AccessToken: "at-1", AccountID: "acct-1"})}
	body := `{"model":"gpt-5.4","stream":true,"max_tokens":50,"messages":[{"role":"user","content":"go on"}]}`
	text, _, finish, _, usage := collect(t, func(emit func([]byte) error) error { return up.Stream(context.Background(), []byte(body), emit) })
	if finish != "length" || usage == nil || usage.CompletionTokens < 50 || usage.CompletionTokens > 80 || len(text) > 2000 {
		t.Fatalf("finish %q usage %+v, %d bytes of text", finish, usage, len(text))
	}
}

func TestGeminiRequestAndStream(t *testing.T) {
	req, err := geminiRequest(mustChat(t), geminiOptions{validated: true, signFunctionCalls: true, maxOutput: 500})
	if err != nil {
		t.Fatal(err)
	}
	antigravitySystem(req)
	got := asJSON(t, req)
	wantAll(t, got,
		`{"inlineData":{"data":"QUJD","mimeType":"image/png"}}`,
		`{"functionCall":{"args":{"q":"cat"},"id":"call_1","name":"lookup"},"thoughtSignature":"skip_thought_signature_validator"}`,
		// The tool result is named after the call it answers, and shares the user turn that follows.
		`{"parts":[{"functionResponse":{"id":"call_1","name":"lookup","response":{"result":"a cat"}}},{"text":"And now?"}],"role":"user"}`,
		`"maxOutputTokens":500`, `"stopSequences":["END"]`, `"temperature":0.4`, `"topP":0.9`,
		`"functionCallingConfig":{"mode":"ANY"}`,
		// The schema is reduced to what the backend takes.
		`"parameters":{"properties":{"q":{"description":"(nullable)","type":"string"}},"required":["q"],"type":"object"}`,
		`You are Antigravity`, `{"text":"Be brief."},{"text":"\n--- [SYSTEM_PROMPT_END] ---"}`)
	if strings.Contains(got, "additionalProperties") || strings.Contains(got, "minLength") {
		t.Errorf("schema not cleaned: %s", got)
	}

	events := []string{
		`{"response":{"candidates":[{"content":{"parts":[{"text":"pondering","thought":true}]}}]}}`,
		`{"response":{"candidates":[{"content":{"parts":[{"text":"Hi"}]}}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":1}}}`,
		`{"response":{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{"q":"dog"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":9,"thoughtsTokenCount":4}}}`,
	}
	text, reasoning, finish, calls, usage := collect(t, func(emit func([]byte) error) error {
		st := &geminiStream{out: newChunks("m", emit)}
		for _, e := range events {
			if err := st.event([]byte(e)); err != nil {
				return err
			}
		}
		return st.end()
	})
	if text != "Hi" || reasoning != "pondering" || finish != "tool_calls" || usage == nil || usage.PromptTokens != 7 || usage.CompletionTokens != 13 {
		t.Fatalf("text %q reasoning %q finish %q usage %+v", text, reasoning, finish, usage)
	}
	if len(calls) != 1 || calls[0].Function.Name != "lookup" || calls[0].Function.Arguments != `{"q":"dog"}` || calls[0].ID == "" {
		t.Fatalf("calls %+v", calls)
	}
	st := &geminiStream{out: newChunks("m", func([]byte) error { return nil })}
	var se *node.StatusError
	if err := st.event([]byte(`{"error":{"code":429,"message":"quota"}}`)); !errors.As(err, &se) || se.Status != 429 {
		t.Fatalf("error answer: %v", err)
	}
}

func TestLimitOutput(t *testing.T) {
	body := []byte(`{"model":"gpt-x","max_tokens":32768,"messages":[]}`)
	out, err := limitOutput(body, &outputLimits{set: map[string]uint64{"gpt-x": 4096}}, "max_completion_tokens")
	if err != nil || !strings.Contains(string(out), `"max_completion_tokens":4096`) || strings.Contains(string(out), `"max_tokens"`) {
		t.Fatalf("%s %v", out, err)
	}
	if out, _ = limitOutput(body, &outputLimits{}, ""); string(out) != string(body) {
		t.Fatalf("untouched body changed: %s", out)
	}
}

// A model that takes less output than the node asks for says so when it
// refuses: the request is sent again within its limit, and later ones start there.
func TestOutputLimitIsLearntFromTheRefusal(t *testing.T) {
	for name, refusal := range map[string]string{
		"openai":    `{"error":{"message":"max_tokens is too large: 32768. This model supports at most 16384 completion tokens, whereas you provided 32768.","param":"max_tokens"}}`,
		"deepseek":  `{"error":{"message":"Invalid max_tokens value, the valid range of max_tokens is [1, 8192]"}}`,
		"anthropic": `{"type":"error","error":{"message":"max_tokens: 32768 > 8192, which is the maximum allowed number of output tokens for claude-x"}}`,
	} {
		var asked []float64
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			n, _ := req["max_tokens"].(float64)
			asked = append(asked, n)
			if n > 16384 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, refusal)
				return
			}
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
		}))
		up := &openAIUpstream{base: srv.URL, header: http.Header{}}
		body := []byte(`{"model":"m","max_tokens":32768,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
		for range 2 {
			if text, _, _, _, _ := collect(t, func(emit func([]byte) error) error { return up.Stream(context.Background(), body, emit) }); text != "ok" {
				t.Fatalf("%s: text %q", name, text)
			}
		}
		srv.Close()
		if len(asked) != 3 || asked[0] != 32768 || asked[1] > 16384 || asked[2] != asked[1] {
			t.Fatalf("%s: asked for %v", name, asked)
		}
	}
	// Other refusals are the buyer's to hear, once.
	calls := 0
	l := &outputLimits{}
	err := l.retry(func() error {
		calls++
		return &node.StatusError{Status: 400, Body: "messages: too many images, at most 20"}
	}, []byte(`{"model":"m","max_tokens":100}`))
	if err == nil || calls != 1 {
		t.Fatalf("unrelated refusal: %v, %d calls", err, calls)
	}
}

// Tokens a model spent out of the buyer's sight are not claimed: the buyer
// would reject the receipt, and the job with it.
func TestHiddenReasoningIsNotBilled(t *testing.T) {
	up := &billable{up: streamOf(
		`{"choices":[{"delta":{"content":"pong"}}]}`,
		`{"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":74,"total_tokens":85}}`)}
	res, err := Probe(context.Background(), up, "m", "")
	if err != nil || res.CompletionTokens != 2*1+64 || res.Reported != 74 || res.PromptTokens != 11 {
		t.Fatalf("%+v, %v", res, err)
	}
	if protocol.UsagePlausible(res.PromptTokens, res.CompletionTokens, 100, protocol.EstimateTokens(res.Text)) != nil {
		t.Fatal("the claim would still be rejected")
	}
	// A count the buyer can check goes through as it is.
	up = &billable{up: streamOf(`{"choices":[{"delta":{"content":"pong"}}]}`, `{"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":3}}`)}
	if res, err = Probe(context.Background(), up, "m", ""); err != nil || res.CompletionTokens != 3 || res.Reported != 0 {
		t.Fatalf("%+v, %v", res, err)
	}
}

type streamOf2 []string

func streamOf(payloads ...string) node.Upstream { return streamOf2(payloads) }

func (s streamOf2) Stream(ctx context.Context, body []byte, emit func([]byte) error) error {
	for _, p := range s {
		if err := emit([]byte(p)); err != nil {
			return err
		}
	}
	return nil
}

func TestStore(t *testing.T) {
	home := t.TempDir()
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-good-0123456789" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"models/m-1"},{"id":"m-2"}]}`)
	}))
	defer srv.Close()

	if _, err := AddWithKey(ctx, home, AddRequest{Type: "custom", Name: "Mine", APIKey: "sk-bad-0123456789", BaseURL: srv.URL}); !errors.Is(err, ErrKeyRefused) {
		t.Fatalf("bad key: %v", err)
	}
	if _, err := os.Stat(Path(home)); err == nil {
		t.Fatal("a refused key was saved")
	}
	added, err := AddWithKey(ctx, home, AddRequest{Type: "custom", Name: "Mine", APIKey: "sk-good-0123456789", BaseURL: srv.URL + "/"})
	if err != nil || added.Provider.Name != "mine" || !added.Provider.Enabled || strings.Join(added.Check.Models, ",") != "m-1,m-2" || added.Provider.Key != "sk-…6789" {
		t.Fatalf("add: %+v, %v", added, err)
	}
	if strings.Contains(asJSON(t, added), "sk-good") {
		t.Fatal("the answer holds the key")
	}
	if fi, err := os.Stat(Path(home)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("providers file: %v %v", fi, err)
	}
	for _, bad := range []AddRequest{
		{Type: "custom", APIKey: "k"},                                // no address
		{Type: "custom", BaseURL: "http://api.example.com/v1"},       // a key in the clear
		{Type: "antigravity", APIKey: "k"},                           // login only
		{Type: "nope", APIKey: "k"},                                  // unknown kind
		{Type: "deepseek", Name: "mine", APIKey: "k", NoCheck: true}, // the name is another kind's
		{Type: "deepseek", Name: "Bad Name", APIKey: "k", NoCheck: true},
	} {
		if _, err := AddWithKey(ctx, home, bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}

	m := node.ModelConfig{ID: "net-1", UpstreamModel: "m-1", PriceInMicros: 1, PriceOutMicros: 2}
	if _, err := SetModel(home, "mine", m, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := AddWithKey(ctx, home, AddRequest{Type: "deepseek", APIKey: "k", NoCheck: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := SetModel(home, "deepseek", m, nil); !errors.Is(err, ErrModelTaken) {
		t.Fatalf("same ID from two providers: %v", err)
	}
	if _, err := SetModel(home, "deepseek", node.ModelConfig{ID: "loc", UpstreamModel: "x"}, []string{"loc"}); !errors.Is(err, ErrModelTaken) {
		t.Fatalf("ID of a local model: %v", err)
	}
	// A new key keeps the models and the state.
	if _, err := SetEnabled(home, "mine", false); err != nil {
		t.Fatal(err)
	}
	added, err = AddWithKey(ctx, home, AddRequest{Type: "custom", Name: "mine", APIKey: "sk-good-0123456789", BaseURL: srv.URL})
	if err != nil || len(added.Provider.Models) != 1 || added.Provider.Enabled {
		t.Fatalf("replacing the key: %+v, %v", added, err)
	}
	if from, err := RemoveModel(home, "net-1"); err != nil || from != "mine" {
		t.Fatalf("remove model: %q %v", from, err)
	}
	if err := Remove(home, "mine"); err != nil {
		t.Fatal(err)
	}
	if err := Remove(home, "mine"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removing twice: %v", err)
	}
}

func TestParseCallback(t *testing.T) {
	for in, want := range map[string][2]string{
		"abc123":   {"abc123", ""},
		" abc#st ": {"abc", "st"},
		"http://localhost:1455/auth/callback?code=c1&scope=openid&state=s1": {"c1", "s1"},
		"?code=c2&state=s2": {"c2", "s2"},
		"http://127.0.0.1:56121/callback?state=s3&code=c3#": {"c3", "s3"},
		"4/0AbCd-EfG_hij": {"4/0AbCd-EfG_hij", ""},
	} {
		if code, state := parseCallback(in); code != want[0] || state != want[1] {
			t.Errorf("parseCallback(%q) = %q, %q; want %q, %q", in, code, state, want[0], want[1])
		}
	}
}

func TestLoginAddresses(t *testing.T) {
	for kind, wants := range map[string][]string{
		KindOpenAI:      {"https://auth.openai.com/oauth/authorize?", "client_id=app_EMoamEEZ73f0CkXaXp7hrann", "redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fauth%2Fcallback", "codex_cli_simplified_flow=true", "code_challenge_method=S256"},
		KindGemini:      {"https://accounts.google.com/o/oauth2/v2/auth?", "redirect_uri=https%3A%2F%2Fcodeassist.google.com%2Fauthcode", "access_type=offline", "prompt=consent"},
		KindAntigravity: {"redirect_uri=http%3A%2F%2Flocalhost%3A8085%2Fcallback", "client_id=1071006060591-"},
		KindGrok:        {"https://auth.x.ai/oauth2/authorize?", "redirect_uri=http%3A%2F%2F127.0.0.1%3A56121%2Fcallback", "nonce=", "grok-cli%3Aaccess"},
	} {
		l, err := StartLogin(kind, "")
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		wantAll(t, l.URL, append(wants, "state="+l.state, "code_challenge="+challengeS256(l.verifier))...)
		if l.Hint == "" {
			t.Errorf("%s: no hint", kind)
		}
		// A code from another attempt is refused before anything is sent.
		if _, _, err := FinishLogin(context.Background(), l.ID, "http://localhost/cb?code=x&state=other"); err == nil || !strings.Contains(err.Error(), "another login") {
			t.Errorf("%s: foreign state: %v", kind, err)
		}
	}
	if _, err := StartLogin(KindDeepSeek, ""); err == nil {
		t.Error("deepseek has no login")
	}
	if _, err := StartLogin(KindAnthropic, ""); !errors.Is(err, ErrOAuthUnavailable) {
		t.Errorf("anthropic login: %v", err)
	}
}

// A token about to expire is renewed and written back; what the renewal does
// not send again is kept.
func TestTokensRenewAndSave(t *testing.T) {
	home := t.TempDir()
	renewals := 0
	kinds = append(kinds, Kind{ID: "testauth", Name: "Test", OAuth: true, OAuthURL: "https://api.example.com"})
	oauthFlows["testauth"] = &oauthFlow{refresh: func(ctx context.Context, old *Token) (*Token, error) {
		renewals++
		if old.RefreshToken != "rt-1" {
			return nil, errors.New("wrong refresh token")
		}
		return &Token{AccessToken: fmt.Sprintf("at-%d", renewals+1), ExpiresAt: time.Now().Add(time.Hour)}, nil
	}}
	t.Cleanup(func() { kinds = kinds[:len(kinds)-1]; delete(oauthFlows, "testauth") })

	e := &Entry{Name: "acct", Type: "testauth", Auth: AuthOAuth, Enabled: true,
		OAuth: &Token{AccessToken: "at-1", RefreshToken: "rt-1", ExpiresAt: time.Now().Add(time.Minute), Email: "a@example.com", ProjectID: "proj"}}
	if err := Update(home, func(f *File) error { return f.Put(e) }); err != nil {
		t.Fatal(err)
	}
	ts := (&Tokens{Home: home}).For("acct")
	tok, err := ts.Token(context.Background(), false)
	if err != nil || tok.AccessToken != "at-2" || tok.RefreshToken != "rt-1" || tok.Email != "a@example.com" || tok.ProjectID != "proj" {
		t.Fatalf("renewed: %+v, %v", tok, err)
	}
	if tok, err = ts.Token(context.Background(), false); err != nil || tok.AccessToken != "at-2" || renewals != 1 {
		t.Fatalf("a fresh token was renewed again: %+v, %v, %d renewals", tok, err, renewals)
	}
	f, _ := Load(home)
	if got := f.Get("acct").OAuth; got.AccessToken != "at-2" || got.RefreshToken != "rt-1" {
		t.Fatalf("saved: %+v", got)
	}
	// The server refused the token: renew it whatever its expiry says.
	calls := 0
	err = withToken(context.Background(), ts, func(tok *Token) error {
		if calls++; tok.AccessToken == "at-2" {
			return &node.StatusError{Status: http.StatusUnauthorized}
		}
		return nil
	})
	if err != nil || calls != 2 || renewals != 2 {
		t.Fatalf("retry after 401: %v, %d calls, %d renewals", err, calls, renewals)
	}
}
