package apicompat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// chatAggregate builds the chat.completion a non-streaming client expects
// from the chat.completion.chunk payloads of a stream: the content,
// reasoning, refusal, the tool calls assembled by index, the
// anthropic_thinking blocks, the finish reason and the usage.
func chatAggregate(payloads [][]byte) ([]byte, error) {
	type call struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	var (
		id, model, finish, fingerprint, tier string
		created                              int64
		content, reasoning, refusal          strings.Builder
		calls                                []*call
		byIndex                              = map[int]*call{}
		thinking                             []json.RawMessage
		usage                                json.RawMessage
		chunks                               int
	)
	for _, p := range payloads {
		p = bytes.TrimSpace(p)
		if len(p) == 0 || string(p) == "[DONE]" {
			continue
		}
		var c struct {
			ID                string          `json:"id"`
			Model             string          `json:"model"`
			Created           int64           `json:"created"`
			SystemFingerprint string          `json:"system_fingerprint"`
			ServiceTier       string          `json:"service_tier"`
			Usage             json.RawMessage `json:"usage"`
			Choices           []struct {
				Index int `json:"index"`
				Delta struct {
					Content           *string           `json:"content"`
					ReasoningContent  string            `json:"reasoning_content"`
					Reasoning         string            `json:"reasoning"`
					Refusal           string            `json:"refusal"`
					AnthropicThinking []json.RawMessage `json:"anthropic_thinking"`
					ToolCalls         []struct {
						Index    *int   `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(p, &c); err != nil {
			return nil, fmt.Errorf("not a chat.completion.chunk: %w", err)
		}
		chunks++
		id, model = chatFirst(id, c.ID), chatFirst(model, c.Model)
		fingerprint, tier = chatFirst(c.SystemFingerprint, fingerprint), chatFirst(c.ServiceTier, tier)
		if created == 0 {
			created = c.Created
		}
		if len(c.Usage) > 0 && string(c.Usage) != "null" {
			usage = c.Usage
		}
		for _, ch := range c.Choices {
			if ch.Index != 0 {
				continue // one choice is all a converted stream has
			}
			d := ch.Delta
			if d.Content != nil {
				content.WriteString(*d.Content)
			}
			reasoning.WriteString(d.ReasoningContent + d.Reasoning)
			refusal.WriteString(d.Refusal)
			thinking = append(thinking, d.AnthropicThinking...)
			for j, tc := range d.ToolCalls {
				i := j
				if tc.Index != nil {
					i = *tc.Index
				}
				cl := byIndex[i]
				// Some model servers give every parallel call the same
				// index: another ID there is another call.
				if cl != nil && tc.ID != "" && cl.ID != "" && tc.ID != cl.ID {
					cl = nil
				}
				if cl == nil {
					cl = &call{Type: "function"}
					byIndex[i] = cl
					calls = append(calls, cl)
				}
				if tc.ID != "" {
					cl.ID = tc.ID
				}
				if tc.Type != "" {
					cl.Type = tc.Type
				}
				cl.Function.Name += tc.Function.Name
				cl.Function.Arguments += tc.Function.Arguments
			}
			if ch.FinishReason != nil && *ch.FinishReason != "" {
				finish = *ch.FinishReason
			}
		}
	}
	if chunks == 0 {
		return nil, fmt.Errorf("the stream carried no chunks")
	}
	if finish == "" {
		finish = "stop"
	}
	if len(calls) > 0 && finish == "stop" {
		finish = "tool_calls"
	}
	msg := map[string]any{"role": "assistant", "content": content.String()}
	for _, cl := range calls {
		if strings.TrimSpace(cl.Function.Arguments) == "" {
			cl.Function.Arguments = "{}" // clients parse the arguments as JSON
		}
	}
	if len(calls) > 0 {
		msg["tool_calls"] = calls
		if content.Len() == 0 {
			msg["content"] = nil
		}
	}
	if refusal.Len() > 0 {
		msg["refusal"] = refusal.String()
		if content.Len() == 0 {
			msg["content"] = nil
		}
	}
	if reasoning.Len() > 0 {
		msg["reasoning_content"] = reasoning.String()
	}
	if len(thinking) > 0 {
		msg["anthropic_thinking"] = thinking
	}
	if id == "" {
		id = newChatChunker("").id
	}
	out := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish, "logprobs": nil}},
	}
	if usage != nil {
		out["usage"] = usage
	}
	if fingerprint != "" {
		out["system_fingerprint"] = fingerprint
	}
	if tier != "" {
		out["service_tier"] = tier
	}
	return json.Marshal(out)
}
