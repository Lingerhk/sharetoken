package apicompat

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// chatChunker writes an answer as chat.completion.chunk payloads, as OpenAI
// streams them with stream_options.include_usage: deltas, a chunk with the
// finish reason, then one with no choices and the usage.
type chatChunker struct {
	id       string
	model    string
	created  int64
	emit     func([]byte) error
	role     bool   // the role went out (on the first delta)
	emitted  bool   // something the client can use went out
	tools    int    // tool calls started so far
	finished bool   // the finish and usage chunks went out
	output   uint64 // estimated tokens of what went out
}

func newChatChunker(model string) *chatChunker {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return &chatChunker{id: "chatcmpl-" + hex.EncodeToString(b), model: model, created: time.Now().Unix()}
}

func (c *chatChunker) send(delta map[string]any, finish string, usage map[string]any) error {
	out := map[string]any{"id": c.id, "object": "chat.completion.chunk", "created": c.created, "model": c.model}
	if delta != nil || finish != "" {
		if delta == nil {
			delta = map[string]any{}
		}
		if !c.role {
			delta["role"], c.role = "assistant", true
		}
		choice := map[string]any{"index": 0, "delta": delta, "finish_reason": nil}
		if finish != "" {
			choice["finish_reason"] = finish
		}
		out["choices"] = []any{choice}
	} else {
		out["choices"] = []any{}
	}
	if usage != nil {
		out["usage"] = usage
	}
	data, err := json.Marshal(out)
	if err != nil {
		return err
	}
	return c.emit(data)
}

func (c *chatChunker) delta(field, s string) error {
	if s == "" {
		return nil
	}
	c.emitted = true
	c.output += chatEstimateTokens(s)
	return c.send(map[string]any{field: s}, "", nil)
}

func (c *chatChunker) content(s string) error   { return c.delta("content", s) }
func (c *chatChunker) reasoning(s string) error { return c.delta("reasoning_content", s) }
func (c *chatChunker) refusal(s string) error   { return c.delta("refusal", s) }

// toolStart opens a tool call and returns its index for toolArgs.
func (c *chatChunker) toolStart(id, name, args string) (int, error) {
	i := c.tools
	c.tools++
	c.emitted = true
	c.output += chatEstimateTokens(name + args)
	return i, c.send(map[string]any{"tool_calls": []any{map[string]any{
		"index": i, "id": id, "type": "function", "function": map[string]any{"name": name, "arguments": args},
	}}}, "", nil)
}

// toolArgs adds to a tool call's arguments. The name is not repeated: a
// client that concatenates names would garble it.
func (c *chatChunker) toolArgs(index int, args string) error {
	if args == "" {
		return nil
	}
	c.output += chatEstimateTokens(args)
	return c.send(map[string]any{"tool_calls": []any{map[string]any{
		"index": index, "function": map[string]any{"arguments": args},
	}}}, "", nil)
}

// thinking passes on a signed or redacted Anthropic thinking block, which
// the client sends back as anthropic_thinking on the assistant message. It
// is history, not another reasoning delta: its text went out already.
func (c *chatChunker) thinking(block chatThinkingBlock) error {
	c.emitted = true
	return c.send(map[string]any{"anthropic_thinking": []any{block}}, "", nil)
}

// finish closes the answer: the finish reason, then the usage.
func (c *chatChunker) finish(reason string, u chatUsage) error {
	if c.finished {
		return nil
	}
	c.finished = true
	if reason == "" {
		reason = "stop"
	}
	if c.tools > 0 && reason == "stop" {
		reason = "tool_calls"
	}
	if err := c.send(nil, reason, nil); err != nil {
		return err
	}
	return c.send(nil, "", u.chat())
}

// chatUsage is the token accounting of an answer. Prompt counts all input,
// cached and cache writes included.
type chatUsage struct {
	Prompt, Completion    uint64
	Cached, CacheCreation uint64
	PromptAudio           uint64
	Reasoning             uint64
	CompletionAudio       uint64
	Accepted, Rejected    uint64
}

func (u chatUsage) chat() map[string]any {
	m := map[string]any{"prompt_tokens": u.Prompt, "completion_tokens": u.Completion, "total_tokens": u.Prompt + u.Completion}
	pd := map[string]any{}
	if u.Cached > 0 {
		pd["cached_tokens"] = u.Cached
	}
	if u.CacheCreation > 0 {
		pd["cache_creation_tokens"] = u.CacheCreation
	}
	if u.PromptAudio > 0 {
		pd["audio_tokens"] = u.PromptAudio
	}
	if len(pd) > 0 {
		if _, ok := pd["cached_tokens"]; !ok {
			pd["cached_tokens"] = 0
		}
		m["prompt_tokens_details"] = pd
	}
	cd := map[string]any{}
	if u.Reasoning > 0 {
		cd["reasoning_tokens"] = u.Reasoning
	}
	if u.CompletionAudio > 0 {
		cd["audio_tokens"] = u.CompletionAudio
	}
	if u.Accepted > 0 {
		cd["accepted_prediction_tokens"] = u.Accepted
	}
	if u.Rejected > 0 {
		cd["rejected_prediction_tokens"] = u.Rejected
	}
	if len(cd) > 0 {
		m["completion_tokens_details"] = cd
	}
	return m
}

// chatThinkingBlock is an Anthropic thinking block as anthropic_thinking
// carries it: the signature (or a redacted block's data) is opaque and goes
// back with the exact thinking text.
type chatThinkingBlock struct {
	Type      string `json:"type"`
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	Data      string `json:"data,omitempty"`
}

// chatUpstreamError is an error event the upstream streamed.
type chatUpstreamError struct {
	Kind    string // the upstream's error type or code, e.g. overloaded_error
	Message string
}

func (e *chatUpstreamError) Error() string {
	if e.Kind == "" {
		return fmt.Sprintf("upstream error: %s", e.Message)
	}
	return fmt.Sprintf("upstream error: %s: %s", e.Kind, e.Message)
}

// errChatStreamEnded fails a stream that ended before it produced anything.
var errChatStreamEnded = fmt.Errorf("the stream ended before the response did")
