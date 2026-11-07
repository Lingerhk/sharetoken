package node

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"time"

	"sharetoken/protocol"
)

// HardMaxOutputTokens caps any single job regardless of budget.
const HardMaxOutputTokens = 32768

// forwardedFields is the subset of an OpenAI chat-completions request a node
// passes upstream. Everything else is dropped, so a buyer cannot switch the
// model, multiply work (n, best_of), or enable server features the node did
// not price. model, stream, stream_options and max_tokens are set by the node.
var forwardedFields = map[string]bool{
	"messages":          true,
	"temperature":       true,
	"top_p":             true,
	"stop":              true,
	"presence_penalty":  true,
	"frequency_penalty": true,
	"seed":              true,
	"response_format":   true,
	"tools":             true,
	"tool_choice":       true,
	"user":              true,
}

// UpstreamRequest is the exact body an honest node sends its model server for
// ticket t and the buyer's decrypted request plain, plus the prompt estimate
// it used. Output is capped against a padded prompt estimate, so chat-template
// overhead cannot push the real cost past the buyer's reservation. Auditors
// replay this body against a reference copy of the model.
func UpstreamRequest(t *protocol.Ticket, plain []byte, upstreamModel string) (body []byte, estPrompt uint64, err error) {
	estPrompt, err = protocol.EstimatePromptTokens(plain)
	if err != nil {
		return nil, 0, protocol.Errorf(protocol.CodeBadRequest, "request is not a chat-completions body")
	}
	budgetCap := protocol.MaxOutputTokens(t.ReservedMicros, protocol.PaddedPrompt(estPrompt), t.PriceInMicros, t.PriceOutMicros)
	body, err = prepareUpstreamBody(plain, upstreamModel, budgetCap)
	return body, estPrompt, err
}

// prepareUpstreamBody turns the buyer's decrypted request into the body sent
// to the model server. budgetCap is the output-token ceiling implied by the
// ticket's reservation; the buyer's own max_tokens can only lower it.
func prepareUpstreamBody(plain []byte, upstreamModel string, budgetCap uint64) ([]byte, error) {
	var in map[string]json.RawMessage
	if err := json.Unmarshal(plain, &in); err != nil {
		return nil, protocol.Errorf(protocol.CodeBadRequest, "request is not a JSON object")
	}
	if raw, ok := in["messages"]; !ok || len(raw) == 0 || string(raw) == "null" {
		return nil, protocol.Errorf(protocol.CodeBadRequest, "messages is required")
	}
	limit := min(budgetCap, HardMaxOutputTokens)
	for _, k := range []string{"max_tokens", "max_completion_tokens"} {
		var v uint64
		if raw, ok := in[k]; ok && json.Unmarshal(raw, &v) == nil && v > 0 {
			limit = min(limit, v)
		}
	}
	if limit == 0 {
		return nil, protocol.Errorf(protocol.CodeBudget, "reservation does not cover the prompt; raise the budget")
	}
	out := make(map[string]any, len(forwardedFields)+4)
	for k, v := range in {
		if forwardedFields[k] {
			out[k] = v
		}
	}
	out["model"] = upstreamModel
	out["stream"] = true
	out["stream_options"] = map[string]bool{"include_usage": true}
	out["max_tokens"] = limit
	return json.Marshal(out)
}

// identity is what a ticket must be bound to for this node to accept it.
type identity struct {
	self        string
	coordinator string
	boxPub      []byte
	receiptKey  ed25519.PublicKey
}

// checkTicket accepts a ticket only if the trusted coordinator signed it for
// this node, for the buyer on the other end of the authenticated stream, at a
// price at or above the node's current one, and it has not expired.
func checkTicket(t *protocol.Ticket, remote string, id identity, models map[string]ModelConfig, now time.Time) (ModelConfig, error) {
	bad := func(msg string) (ModelConfig, error) {
		return ModelConfig{}, protocol.Errorf(protocol.CodeBadTicket, "%s", msg)
	}
	if err := t.Verify(id.coordinator); err != nil {
		return bad(err.Error())
	}
	switch {
	case t.Node != id.self:
		return bad("ticket is for another node")
	case t.Buyer != remote:
		return bad("ticket was issued to a different buyer")
	case now.UnixNano() >= t.ExpiresAt:
		return bad("ticket expired")
	case !bytes.Equal(t.NodeBoxPub, id.boxPub) || !bytes.Equal(t.NodeReceiptKey, id.receiptKey):
		return bad("ticket keys do not match this node")
	}
	mc, ok := models[t.Model]
	if !ok {
		return bad("model not served by this node")
	}
	if t.PriceInMicros < mc.PriceInMicros || t.PriceOutMicros < mc.PriceOutMicros {
		return bad("ticket price is below this node's current price")
	}
	return mc, nil
}
