package apicompat

// The non-streaming Messages answer, built from a Messages event stream (one
// an Anthropic upstream sent, or one this package made).

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

type msgAggBlock struct {
	block map[string]any
	text  strings.Builder // text, thinking
	sig   strings.Builder
	input strings.Builder // tool_use partial_json
	cites []any
}

func msgAggregate(payloads [][]byte) ([]byte, error) {
	var (
		msg     map[string]any
		usage   = map[string]any{}
		blocks  = map[int]*msgAggBlock{}
		stop    any
		stopSeq any
		ended   bool // message_delta or message_stop came
	)
	for _, p := range payloads {
		var e struct {
			Type         string          `json:"type"`
			Index        int             `json:"index"`
			Message      json.RawMessage `json:"message"`
			ContentBlock json.RawMessage `json:"content_block"`
			Delta        json.RawMessage `json:"delta"`
			Usage        json.RawMessage `json:"usage"`
			Error        json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(p, &e); err != nil {
			return nil, fmt.Errorf("apicompat: messages event: %w", err)
		}
		switch e.Type {
		case "message_start":
			if err := json.Unmarshal(e.Message, &msg); err != nil || msg == nil {
				return nil, errors.New("apicompat: bad message_start")
			}
			if u, ok := msg["usage"].(map[string]any); ok {
				for k, v := range u {
					usage[k] = v
				}
			}
		case "content_block_start":
			var cb map[string]any
			if err := json.Unmarshal(e.ContentBlock, &cb); err != nil || cb == nil {
				return nil, errors.New("apicompat: bad content_block_start")
			}
			b := &msgAggBlock{block: cb}
			switch cb["type"] {
			case "text":
				s, _ := cb["text"].(string)
				b.text.WriteString(s)
			case "thinking":
				s, _ := cb["thinking"].(string)
				b.text.WriteString(s)
				s, _ = cb["signature"].(string)
				b.sig.WriteString(s)
			}
			blocks[e.Index] = b
		case "content_block_delta":
			b := blocks[e.Index]
			if b == nil {
				return nil, fmt.Errorf("apicompat: delta for content block %d before its start", e.Index)
			}
			var d struct {
				Type        string          `json:"type"`
				Text        string          `json:"text"`
				Thinking    string          `json:"thinking"`
				Signature   string          `json:"signature"`
				PartialJSON string          `json:"partial_json"`
				Citation    json.RawMessage `json:"citation"`
			}
			if err := json.Unmarshal(e.Delta, &d); err != nil {
				return nil, fmt.Errorf("apicompat: content_block_delta: %w", err)
			}
			switch d.Type {
			case "text_delta":
				b.text.WriteString(d.Text)
			case "thinking_delta":
				b.text.WriteString(d.Thinking)
			case "signature_delta":
				b.sig.WriteString(d.Signature)
			case "input_json_delta":
				b.input.WriteString(d.PartialJSON)
			case "citations_delta":
				var c any
				if json.Unmarshal(d.Citation, &c) == nil {
					b.cites = append(b.cites, c)
				}
			}
		case "message_stop":
			ended = true
		case "message_delta":
			ended = true
			var d struct {
				StopReason   any `json:"stop_reason"`
				StopSequence any `json:"stop_sequence"`
			}
			_ = json.Unmarshal(e.Delta, &d)
			if d.StopReason != nil {
				stop = d.StopReason
			}
			if d.StopSequence != nil {
				stopSeq = d.StopSequence
			}
			var u map[string]any
			if json.Unmarshal(e.Usage, &u) == nil {
				for k, v := range u {
					if v != nil {
						usage[k] = v
					}
				}
			}
		case "error":
			m := msgErrorMessage(e.Error)
			if m == "" {
				m = "error event"
			}
			return nil, errors.New("apicompat: upstream: " + m)
		}
	}
	if msg == nil {
		return nil, errors.New("apicompat: messages stream has no message_start")
	}
	if !ended {
		// Cut off before its end: half an answer is not the answer.
		return nil, errors.New("apicompat: messages stream ended before its message_stop")
	}

	idx := make([]int, 0, len(blocks))
	for i := range blocks {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	content := make([]any, 0, len(idx))
	for _, i := range idx {
		b := blocks[i]
		cb := b.block
		switch cb["type"] {
		case "text":
			cb["text"] = b.text.String()
			if len(b.cites) > 0 {
				cb["citations"] = b.cites
			}
		case "thinking":
			cb["thinking"] = b.text.String()
			cb["signature"] = b.sig.String()
		case "tool_use", "server_tool_use", "mcp_tool_use":
			if s := strings.TrimSpace(b.input.String()); s != "" {
				var in any
				if json.Unmarshal([]byte(s), &in) != nil {
					// Cut short (max_tokens): no input can be read.
					in = map[string]any{}
				}
				cb["input"] = in
			} else if cb["input"] == nil {
				cb["input"] = map[string]any{}
			}
		}
		content = append(content, cb)
	}
	for _, k := range []string{"input_tokens", "cache_creation_input_tokens", "cache_read_input_tokens", "output_tokens"} {
		if _, ok := usage[k]; !ok {
			usage[k] = 0
		}
	}
	out := map[string]any{
		"id":            msg["id"],
		"type":          "message",
		"role":          "assistant",
		"model":         msg["model"],
		"content":       content,
		"stop_reason":   stop,
		"stop_sequence": stopSeq,
		"usage":         usage,
	}
	if out["stop_reason"] == nil {
		out["stop_reason"] = msg["stop_reason"]
	}
	return json.Marshal(out)
}
