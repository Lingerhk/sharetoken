package tokens

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sharetoken/internal/protocol"
)

// fakeServer reports prompt_tokens = 7 (template) + 1 per word of message
// content, like a real server would through its tokenizer and chat template.
func fakeServer(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model     string `json:"model"`
			MaxTokens int    `json:"max_tokens"`
			Messages  []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.MaxTokens != 1 || req.Model != "ref-model" {
			t.Errorf("unexpected counting request: %+v %v", req, err)
		}
		n := 7
		for _, m := range req.Messages {
			n += len(strings.Fields(m.Content))
		}
		fmt.Fprintf(w, `{"choices":[{"message":{"content":"x"}}],"usage":{"prompt_tokens":%d,"completion_tokens":1}}`, n)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestReferenceCountsByDifferenceAndFallsBack(t *testing.T) {
	srv := fakeServer(t)
	c := &Reference{Servers: map[string]Server{"m": {BaseURL: srv.URL + "/v1", Model: "ref-model"}}}
	ctx := context.Background()

	n, err := c.CountText(ctx, "m", "one two three four five")
	if err != nil || n != 5 {
		t.Fatalf("CountText = %d, %v; want 5 (template overhead cancels)", n, err)
	}
	p, err := c.CountPrompt(ctx, "m", protocol.APIChat, []byte(`{"messages":[{"role":"user","content":"a b c"}],"n":9}`))
	if err != nil || p != 10 {
		t.Fatalf("CountPrompt = %d, %v; want 10", p, err)
	}
	if !c.Exact("m", protocol.APIChat) || c.Exact("unknown", protocol.APIChat) || c.Exact("m", protocol.APIMessages) {
		t.Fatal("Exact must be true only for models with a reference server")
	}
	// No server for this model: the heuristic answers.
	if n, err := c.CountText(ctx, "unknown", "abcdefgh"); err != nil || n != 2 {
		t.Fatalf("fallback CountText = %d, %v; want 2", n, err)
	}
}

func TestAllowanceIsTightOnlyForExactCounts(t *testing.T) {
	p, c := Allowance(true, 100, 200)
	if p != 121 || c != 226 {
		t.Fatalf("exact allowance = %d/%d", p, c)
	}
	p, c = Allowance(false, 100, 200)
	if p != 812 || c != 464 {
		t.Fatalf("heuristic allowance = %d/%d", p, c)
	}
}
