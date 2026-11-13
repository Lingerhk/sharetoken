package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sharetoken/internal/client"
	"sharetoken/internal/provider"
)

// mockAnthropic answers like Anthropic's Messages API, behind an API key.
func mockAnthropic(t *testing.T, key string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != key || r.Header.Get("anthropic-version") == "" {
			http.Error(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`, http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"claude-test-1"}]}`)
			return
		}
		var req struct {
			Model     string `json:"model"`
			MaxTokens int    `json:"max_tokens"`
			Stream    bool   `json:"stream"`
			Messages  []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if r.URL.Path != "/v1/messages" || req.Model != "claude-test-1" || !req.Stream || req.MaxTokens <= 0 || len(req.Messages) == 0 {
			t.Errorf("anthropic got %s %+v", r.URL.Path, req)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range []string{
			`{"type":"message_start","message":{"usage":{"input_tokens":8,"cache_read_input_tokens":4,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Bonjour"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" from Claude"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":6}}`,
			`{"type":"message_stop"}`,
		} {
			fmt.Fprintf(w, "event: x\ndata: %s\n\n", ev)
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// keyedOpenAI is an OpenAI-compatible API behind a bearer key.
func keyedOpenAI(t *testing.T, key string) *httptest.Server {
	model := mockUpstream(t, "mock-7b", false)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key {
			http.Error(w, `{"error":{"message":"Authentication Fails"}}`, http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/v1/models" {
			_, _ = io.WriteString(w, `{"data":[{"id":"mock-7b"}]}`)
			return
		}
		model.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func localAPI(t *testing.T, a *client.Agent, method, path string, in, out any) (int, string) {
	t.Helper()
	var rd io.Reader
	if in != nil {
		data, _ := json.Marshal(in)
		rd = bytes.NewReader(data)
	}
	req, _ := http.NewRequest(method, "http://"+a.Addr()+path, rd)
	req.Header.Set("Authorization", "Bearer "+a.Key())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil && resp.StatusCode == 200 {
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("%s %s: %v in %s", method, path, err, data)
		}
	}
	return resp.StatusCode, string(data)
}

func chatModel(t *testing.T, a *client.Agent, model string) (int, string, http.Header) {
	t.Helper()
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}]}`, model)
	resp, err := postV1("http://"+a.Addr()+"/v1/chat/completions", a.APIKey(), body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var out struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
	}
	if resp.StatusCode != 200 || json.Unmarshal(data, &out) != nil || len(out.Choices) != 1 {
		return resp.StatusCode, string(data), resp.Header
	}
	return 200, out.Choices[0].Message.Content, resp.Header
}

// A user sells the models of two hosted APIs at once, set up through the
// local API: one speaks OpenAI chat completions, the other Anthropic's
// Messages API, and buyers get the same OpenAI answers from both.
func TestProvidersSellThroughTheirOwnAPIs(t *testing.T) {
	const keyA, keyB = "sk-openai-0123456789abcd", "sk-ant-0123456789abcdef"
	n := newNetwork(t, 2)
	oa, an := keyedOpenAI(t, keyA), mockAnthropic(t, keyB)
	seller := n.user("seller", &client.ShareConfig{Enabled: true, Concurrency: 2, Relays: 2})
	buyer := n.user("buyer", nil)

	// Sharing is on with nothing to share yet: said, not fatal.
	if st := seller.Status(); st.Share.Running || len(st.Warnings) == 0 {
		t.Fatalf("before any provider: running %v, warnings %v", st.Share.Running, st.Warnings)
	}

	// A wrong key is refused and nothing is saved.
	if code, out := localAPI(t, seller, "POST", "/api/v1/providers", provider.AddRequest{Type: "anthropic", Name: "claude", APIKey: "sk-wrong-0123456789", BaseURL: an.URL}, nil); code != 401 {
		t.Fatalf("wrong key: %d %s", code, out)
	}
	var added provider.Added
	if code, out := localAPI(t, seller, "POST", "/api/v1/providers", provider.AddRequest{Type: "anthropic", Name: "claude", APIKey: keyB, BaseURL: an.URL}, &added); code != 200 ||
		strings.Contains(out, keyB) || len(added.Check.Models) != 1 || added.Check.Models[0] != "claude-test-1" {
		t.Fatalf("add anthropic: %d %s", code, out)
	}
	if code, out := localAPI(t, seller, "POST", "/api/v1/providers", provider.AddRequest{Type: "custom", Name: "oa", APIKey: keyA, BaseURL: oa.URL + "/v1"}, nil); code != 200 {
		t.Fatalf("add custom: %d %s", code, out)
	}
	sell := func(name, id, upstream string) (int, string) {
		return localAPI(t, seller, "PUT", "/api/v1/providers/"+name+"/models", map[string]any{"id": id, "upstream_model": upstream, "price_in": priceIn, "price_out": priceOut}, nil)
	}
	if code, out := sell("claude", "claude-net", "claude-test-1"); code != 200 {
		t.Fatalf("sell claude: %d %s", code, out)
	}
	if code, out := sell("oa", "mock-net", "mock-7b"); code != 200 {
		t.Fatalf("sell mock: %d %s", code, out)
	}
	if code, out := sell("oa", "claude-net", "mock-7b"); code != 409 {
		t.Fatalf("one ID from two providers: %d %s", code, out)
	}
	waitFor(t, 20*time.Second, "the offer with both models", func() bool {
		offers := n.coord.Engine.Offers()
		return len(offers) == 1 && len(offers[0].Models) == 2
	})

	if code, text, hdr := chatModel(t, buyer, "claude-net"); code != 200 || text != "Bonjour from Claude" || hdr.Get("X-Sharetoken-Node") != seller.PeerID() || hdr.Get("X-Sharetoken-Verified") != "true" {
		t.Fatalf("claude-net: %d %q", code, text)
	}
	if code, text, _ := chatModel(t, buyer, "mock-net"); code != 200 || text != "Hello, world!" {
		t.Fatalf("mock-net: %d %q", code, text)
	}
	// Tokens are billed as each API counted them: Anthropic's cached prompt
	// tokens are prompt tokens.
	waitFor(t, 10*time.Second, "settlements", func() bool { return n.me(seller).Usage.Sold.Jobs == 2 })
	if u := n.me(seller).Usage.Sold; u.PromptTokens != 12+10 || u.CompletionTokens != 6+5 {
		t.Fatalf("sold usage: %+v", u)
	}

	// Nothing the local API returns holds a key.
	for _, path := range []string{"/api/v1/status", "/api/v1/share", "/api/v1/providers"} {
		code, out := localAPI(t, seller, "GET", path, nil, nil)
		if code != 200 || strings.Contains(out, keyA) || strings.Contains(out, keyB) || !strings.Contains(out, `"claude"`) {
			t.Fatalf("%s: %d %s", path, code, out)
		}
	}
	// Web pages, local ones too, stay out of the providers API, even one that
	// had the key; and nothing gets in without it.
	req, _ := http.NewRequest("GET", "http://"+seller.Addr()+"/api/v1/providers", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Authorization", "Bearer "+seller.Key())
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 403 {
		t.Fatalf("a local web page: %v %v", resp, err)
	}
	if code, out := localAPI(t, seller, "GET", "/api/v1/providers", nil, nil); code != 200 {
		t.Fatalf("with the key: %d %s", code, out)
	}
	req, _ = http.NewRequest("GET", "http://"+seller.Addr()+"/api/v1/providers", nil)
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 401 {
		t.Fatalf("without the key: %v %v", resp, err)
	}

	var models struct{ Models []string }
	if code, out := localAPI(t, seller, "GET", "/api/v1/providers/claude/models", nil, &models); code != 200 || len(models.Models) != 1 {
		t.Fatalf("models: %d %s", code, out)
	}
	var probe struct {
		Result provider.ProbeResult `json:"result"`
	}
	if code, out := localAPI(t, seller, "POST", "/api/v1/providers/claude/test", map[string]string{"model": "claude-net"}, &probe); code != 200 || probe.Result.Text != "Bonjour from Claude" || probe.Result.PromptTokens != 12 {
		t.Fatalf("test: %d %s", code, out)
	}

	// Turning a provider off withdraws its models; the other's stay.
	on := false
	if code, out := localAPI(t, seller, "PATCH", "/api/v1/providers/claude", map[string]*bool{"enabled": &on}, nil); code != 200 {
		t.Fatalf("off: %d %s", code, out)
	}
	waitFor(t, 20*time.Second, "one model left", func() bool {
		offers := n.coord.Engine.Offers()
		return len(offers) == 1 && len(offers[0].Models) == 1 && offers[0].Models[0].Model == "mock-net"
	})
	if code, _, _ := chatModel(t, buyer, "claude-net"); code == 200 {
		t.Fatal("a model of a provider that is off was served")
	}
	if code, out := localAPI(t, seller, "DELETE", "/api/v1/providers/oa", nil, nil); code != 200 {
		t.Fatalf("delete: %d %s", code, out)
	}
	waitFor(t, 20*time.Second, "no offer left", func() bool { return len(n.coord.Engine.Offers()) == 0 })
}

// Credentials a provider refuses fail the job and tell the seller which
// provider it was.
func TestRefusedProviderKeyWarnsTheSeller(t *testing.T) {
	n := newNetwork(t, 2)
	oa := keyedOpenAI(t, "sk-right-0123456789")
	seller := n.user("seller", &client.ShareConfig{Enabled: true, Concurrency: 2, Relays: 2})
	buyer := n.user("buyer", nil)
	if code, out := localAPI(t, seller, "POST", "/api/v1/providers", provider.AddRequest{Type: "custom", Name: "oa", APIKey: "sk-wrong-0123456789", BaseURL: oa.URL + "/v1", NoCheck: true}, nil); code != 200 {
		t.Fatalf("add: %d %s", code, out)
	}
	if code, out := localAPI(t, seller, "PUT", "/api/v1/providers/oa/models", map[string]any{"id": "mock-net", "upstream_model": "mock-7b", "price_in": priceIn, "price_out": priceOut}, nil); code != 200 {
		t.Fatalf("sell: %d %s", code, out)
	}
	waitFor(t, 20*time.Second, "the offer", func() bool { return len(n.coord.Engine.Offers()) == 1 })
	if code, out, _ := chatModel(t, buyer, "mock-net"); code == 200 || strings.Contains(out, "sk-") {
		t.Fatalf("buyer got %d %s", code, out)
	}
	warned := func() bool {
		for _, w := range seller.Status().Warnings {
			if strings.Contains(w, "oa refused its credentials") {
				return true
			}
		}
		return false
	}
	waitFor(t, 5*time.Second, "the warning", warned)
	// The right key clears it.
	if code, out := localAPI(t, seller, "POST", "/api/v1/providers", provider.AddRequest{Type: "custom", Name: "oa", APIKey: "sk-right-0123456789", BaseURL: oa.URL + "/v1"}, nil); code != 200 {
		t.Fatalf("new key: %d %s", code, out)
	}
	if warned() {
		t.Fatal("the warning outlived the key")
	}
	waitFor(t, 20*time.Second, "the offer again", func() bool { return len(n.coord.Engine.Offers()) == 1 })
	if code, text, _ := chatModel(t, buyer, "mock-net"); code != 200 || text != "Hello, world!" {
		t.Fatalf("with the right key: %d %q", code, text)
	}
}
