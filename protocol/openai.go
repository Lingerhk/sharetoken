package protocol

import "encoding/json"

// Usage is the token accounting an OpenAI-compatible server reports in the
// final streamed chunk when stream_options.include_usage is set.
type Usage struct {
	PromptTokens     uint64 `json:"prompt_tokens"`
	CompletionTokens uint64 `json:"completion_tokens"`
}

// ChunkInfo is what the node and the buyer each extract from one streamed
// chat.completion.chunk payload.
type ChunkInfo struct {
	Content      string
	Reasoning    string
	ToolArgs     string
	FinishReason string
	Usage        *Usage
}

// Text is everything in the chunk that consumed completion tokens.
func (c ChunkInfo) Text() string { return c.Content + c.Reasoning + c.ToolArgs }

// ParseChunk extracts deltas, finish reason and usage from one payload. Servers
// differ on the name of the reasoning field, so both common spellings are read.
func ParseChunk(payload []byte) (ChunkInfo, error) {
	var c struct {
		Choices []struct {
			Delta struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				Reasoning        string `json:"reasoning"`
				ToolCalls        []struct {
					Function struct {
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
		Usage *Usage `json:"usage"`
	}
	if err := json.Unmarshal(payload, &c); err != nil {
		return ChunkInfo{}, err
	}
	info := ChunkInfo{Usage: c.Usage}
	for _, ch := range c.Choices {
		info.Content += ch.Delta.Content
		info.Reasoning += ch.Delta.ReasoningContent + ch.Delta.Reasoning
		for _, tc := range ch.Delta.ToolCalls {
			info.ToolArgs += tc.Function.Arguments
		}
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			info.FinishReason = *ch.FinishReason
		}
	}
	return info, nil
}
