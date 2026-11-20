package provider

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sharetoken/internal/node"
	"sharetoken/internal/protocol"
)

const turnBetas = "per-turn-control-2026-07-01,mid-conversation-output-config-2026-07-01"

// The betas that let a message carry its own output_config go upstream on
// every path, a rebuilt request's too: they describe the request's format.
func TestTurnConfigBetasAreKept(t *testing.T) {
	client := "interleaved-thinking-2025-05-14," + turnBetas
	for _, c := range []struct {
		name           string
		oauth, rebuilt bool
		want           string
	}{
		{"key", false, false, client},
		{"login, Claude Code's request", true, false, client},
		{"login, rebuilt", true, true, turnBetas},
	} {
		if got := strings.Join(buyerBetas(client, "claude-opus-5-5", c.oauth, c.rebuilt), ","); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

// On an API key, a request with a per-turn effort goes upstream with the
// buyer's betas and the message's output_config as it came.
func TestTurnConfigGoesWithItsBeta(t *testing.T) {
	var beta string
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		beta = r.Header.Get("anthropic-beta")
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		for _, ev := range messagesOK {
			fmt.Fprintf(w, "data: %s\n\n", ev)
		}
	}))
	t.Cleanup(srv.Close)
	up := &anthropicUpstream{base: srv.URL, header: http.Header{}}
	req := `{"model":"claude-opus-5-5","max_tokens":100,"stream":true,"messages":[{"role":"user","content":"hi"},{"role":"system","content":"","output_config":{"effort":"low"}},{"role":"user","content":"more"}]}`
	h := protocol.RequestHeader{"anthropic-beta": turnBetas}
	if err := up.Stream(context.Background(), &node.Request{API: protocol.APIMessages, Header: h, Body: []byte(req)}, discard); err != nil {
		t.Fatal(err)
	}
	if beta != turnBetas {
		t.Errorf("anthropic-beta %q", beta)
	}
	if !strings.Contains(body, `"output_config":{"effort":"low"}`) {
		t.Errorf("sent %s", body)
	}
}
