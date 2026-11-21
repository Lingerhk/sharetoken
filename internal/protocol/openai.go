package protocol

import "encoding/json"

// ToolCallDelta is one streamed fragment of a tool call. Fragments with the
// same Index belong to the same call: ID, Type and Name usually arrive once,
// Arguments in pieces.
type ToolCallDelta struct {
	Index     int
	ID        string
	Type      string
	Name      string
	Arguments string
}

// ToolCall is an assembled tool call in the OpenAI chat-completions shape.
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction is the function a ToolCall invokes.
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolCallAccumulator assembles streamed tool-call fragments in index order.
type ToolCallAccumulator struct {
	calls []ToolCall
	index map[int]int
}

// Add merges the fragments of one chunk.
func (a *ToolCallAccumulator) Add(ds []ToolCallDelta) {
	for _, d := range ds {
		if a.index == nil {
			a.index = map[int]int{}
		}
		i, ok := a.index[d.Index]
		if !ok {
			i = len(a.calls)
			a.index[d.Index] = i
			a.calls = append(a.calls, ToolCall{Type: "function"})
		}
		c := &a.calls[i]
		if d.ID != "" {
			c.ID = d.ID
		}
		if d.Type != "" {
			c.Type = d.Type
		}
		c.Function.Name += d.Name
		c.Function.Arguments += d.Arguments
	}
}

// Calls returns the assembled calls (nil when there were none).
func (a *ToolCallAccumulator) Calls() []ToolCall { return a.calls }

// ParseChunk extracts from a chat.completion.chunk deltas, finish reason and usage from one payload. Servers
// differ on the name of the reasoning field, so both common spellings are read.
func ParseChunk(payload []byte) (ChunkInfo, error) {
	var c struct {
		Choices []struct {
			Delta struct {
				AnthropicThinking []json.RawMessage `json:"anthropic_thinking"`
				Content           string            `json:"content"`
				ReasoningContent  string            `json:"reasoning_content"`
				Reasoning         string            `json:"reasoning"`
				ToolCalls         []struct {
					Index    int    `json:"index"`
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
		Usage *struct {
			PromptTokens        uint64 `json:"prompt_tokens"`
			CompletionTokens    uint64 `json:"completion_tokens"`
			PromptTokensDetails struct {
				CachedTokens        uint64 `json:"cached_tokens"`
				CacheCreationTokens uint64 `json:"cache_creation_tokens"`
			} `json:"prompt_tokens_details"`
			CompletionTokensDetails struct {
				ReasoningTokens uint64 `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
			// DeepSeek counts cache hits under names of its own.
			PromptCacheHitTokens uint64 `json:"prompt_cache_hit_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(payload, &c); err != nil {
		return ChunkInfo{}, err
	}
	var info ChunkInfo
	if u := c.Usage; u != nil {
		cr := u.PromptTokensDetails.CachedTokens
		if cr == 0 {
			cr = u.PromptCacheHitTokens
		}
		info.Usage = &Usage{PromptTokens: u.PromptTokens, CompletionTokens: u.CompletionTokens,
			CacheReadTokens: cr, CacheWriteTokens: u.PromptTokensDetails.CacheCreationTokens,
			ReasoningTokens: u.CompletionTokensDetails.ReasoningTokens}
		info.Hidden = u.CompletionTokensDetails.ReasoningTokens > 0
	}
	for _, ch := range c.Choices {
		info.AnthropicThinking = append(info.AnthropicThinking, ch.Delta.AnthropicThinking...)
		info.Hidden = info.Hidden || len(ch.Delta.AnthropicThinking) > 0
		info.Content += ch.Delta.Content
		info.Reasoning += ch.Delta.ReasoningContent + ch.Delta.Reasoning
		for _, tc := range ch.Delta.ToolCalls {
			info.ToolArgs += tc.Function.Arguments
			info.ToolCalls = append(info.ToolCalls, ToolCallDelta{Index: tc.Index, ID: tc.ID, Type: tc.Type, Name: tc.Function.Name, Arguments: tc.Function.Arguments})
		}
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			info.FinishReason = *ch.FinishReason
		}
	}
	return info, nil
}
