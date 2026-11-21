package e2e

// The Messages API passed through to an API-key seller: a Claude Code
// history whose tool exchange does not pair (parallel calls stopped before
// both results came) reaches the model server mended, without a refusal.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"sharetoken/internal/buyer"
	"sharetoken/internal/protocol"
)

func TestUnpairedToolHistoryReachesTheUpstreamPaired(t *testing.T) {
	m := newAPIMarket(t, protocol.APIMessages)
	req := firstTurn(protocol.APIMessages, true)
	req["messages"] = append(req["messages"].([]any),
		map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "text", "text": "Looking both up."},
			map[string]any{"type": "tool_use", "id": "toolu_one", "name": "lookup", "input": map[string]any{"q": "answer"}},
			map[string]any{"type": "tool_use", "id": "toolu_two", "name": "lookup", "input": map[string]any{"q": "question"}},
		}},
		// The user stopped the second call: only the first was answered.
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "toolu_one", "content": "42"},
			map[string]any{"type": "text", "text": "[Request interrupted by user for tool use]"},
		}})
	pin := map[string]string{buyer.HeaderNode: m.nodeOf[protocol.APIMessages].ID()}
	r := m.call(t, protocol.APIMessages, "/v1/messages", req, pin)
	if r.status != http.StatusOK || !r.verified() || finalText(protocol.APIMessages, r.body) != finalAnswer {
		t.Fatalf("%d %v %s", r.status, r.body, r.trailer)
	}
	reqs := m.mock.requests("/anthropic/v1/messages")
	if len(reqs) != 1 {
		t.Fatalf("the model server was asked %d times", len(reqs))
	}
	msgs := reqs[0]["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)["content"].([]any)
	var ids []string
	for _, b := range last {
		blk := b.(map[string]any)
		if blk["type"] == "tool_result" {
			ids = append(ids, blk["tool_use_id"].(string))
		}
	}
	got, _ := json.Marshal(last)
	if strings.Join(ids, ",") != "toolu_one,toolu_two" || !strings.Contains(string(got), `"is_error":true`) ||
		last[len(last)-1].(map[string]any)["type"] != "text" {
		t.Fatalf("the last user message reached the model server as %s", got)
	}
}
