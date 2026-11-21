package apicompat

import (
	"bytes"
	"encoding/json"
)

// Lenient readers for what chat model servers send where OpenAI has a string.
// A chunk these cannot read either is skipped by the translators, not the end
// of the answer.

// looseArgs is a tool call's arguments: a JSON string, or (Ollama and some
// other servers) the arguments object itself.
type looseArgs string

func (a *looseArgs) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch {
	case len(b) == 0 || string(b) == "null":
		*a = ""
	case b[0] == '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*a = looseArgs(s)
	default:
		*a = looseArgs(b)
	}
	return nil
}

// looseContent reads a message's (or a delta's) content: a string, or the
// parts some servers stream instead (Mistral's reasoning models send
// [{"type":"thinking","thinking":[{"type":"text","text":"..."}]}] and text
// parts). It returns the text and the thinking apart.
func looseContent(raw json.RawMessage) (text, thinking string) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return "", ""
	}
	type part struct {
		Type     string          `json:"type"`
		Text     string          `json:"text"`
		Thinking json.RawMessage `json:"thinking"`
	}
	var parts []part
	switch raw[0] {
	case '"':
		_ = json.Unmarshal(raw, &text)
		return text, ""
	case '{':
		var one part
		if json.Unmarshal(raw, &one) != nil {
			return "", ""
		}
		parts = []part{one}
	default:
		if json.Unmarshal(raw, &parts) != nil {
			return "", ""
		}
	}
	for _, p := range parts {
		switch p.Type {
		case "thinking", "reasoning":
			t, _ := looseContent(p.Thinking)
			if t == "" {
				t = p.Text
			}
			thinking += t
		case "", "text", "output_text":
			text += p.Text
		}
	}
	return text, thinking
}

// firstPositive is the first count above zero.
func firstPositive(counts ...*int) int {
	for _, c := range counts {
		if c != nil && *c > 0 {
			return *c
		}
	}
	return 0
}
