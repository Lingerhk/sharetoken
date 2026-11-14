package provider

import (
	"encoding/json"
	"fmt"
)

// The vendor signature is opaque: preserve it with the exact thinking text.
// It is history, not another reasoning delta, and must not be billed twice.
type anthropicThinkingBlock struct {
	Type      string `json:"type"`
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	Data      string `json:"data,omitempty"`
}

func anthropicThinkingHistory(history []json.RawMessage) ([]any, error) {
	var blocks []any
	for _, raw := range history {
		var b anthropicThinkingBlock
		if err := json.Unmarshal(raw, &b); err != nil {
			return nil, fmt.Errorf("invalid anthropic_thinking block")
		}
		switch b.Type {
		case "thinking":
			if b.Signature == "" {
				return nil, fmt.Errorf("anthropic_thinking requires the original signature")
			}
			blocks = append(blocks, map[string]any{"type": "thinking", "thinking": b.Thinking, "signature": b.Signature})
		case "redacted_thinking":
			if b.Data == "" {
				return nil, fmt.Errorf("redacted thinking requires its original data")
			}
			blocks = append(blocks, map[string]any{"type": "redacted_thinking", "data": b.Data})
		default:
			return nil, fmt.Errorf("unsupported anthropic_thinking block type")
		}
	}
	return blocks, nil
}
