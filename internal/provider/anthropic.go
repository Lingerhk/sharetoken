package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"sharetoken/internal/node"
)

// Anthropic speaks the Messages API: the chat-completions request is
// rewritten into it, and its stream events back into OpenAI chunks.

const anthropicVersion = "2023-06-01"

// claudeCodeIdentity is the identity line used by both OAuth request profiles.
const claudeCodeIdentity = "You are Claude Code, Anthropic's official CLI for Claude."

type anthropicUpstream struct {
	base   string // without /v1
	limits outputLimits
	// API-key path: header carries the key. OAuth path: ts gives the bearer
	// token; oauth selects the configured OAuth request profile.
	header http.Header
	ts     TokenSource
	oauth  bool
	entry  *Entry
}

func (u *anthropicUpstream) Stream(ctx context.Context, body []byte, emit func([]byte) error) error {
	chat, err := parseChat(body)
	if err != nil {
		return &node.StatusError{Status: http.StatusBadRequest, Body: err.Error()}
	}
	responseModel := chat.Model
	if u.oauth {
		if model := claudeOAuthModelIDs[chat.Model]; model != "" {
			chat.Model = model
		}
	}
	sampling := ""
	if u.entry != nil && u.entry.Anthropic != nil {
		sampling = u.entry.Anthropic.Sampling
	}
	if err := validateAnthropicParameters(chat, sampling); err != nil {
		return &node.StatusError{Status: http.StatusBadRequest, Body: err.Error()}
	}
	profile := u.entry != nil && u.entry.claudeProfile()
	endpoint, err := anthropicMessagesURL(u.base, profile)
	if err != nil {
		return err
	}
	return u.limits.retry(func() error {
		send := func(tok *Token) error {
			req, err := anthropicRequest(chat, u.limits.of(responseModel))
			if err != nil {
				return &node.StatusError{Status: http.StatusBadRequest, Body: err.Error()}
			}
			h := u.header.Clone()
			if u.oauth {
				h = anthropicOAuthHeader(tok)
				if profile {
					h, err = applyClaudeOAuthProfile(ctx, req, chat, u.entry, tok)
					if err != nil {
						return err
					}
				} else {
					req["system"] = claudeCodeSystem(req["system"])
				}
			}
			if h == nil {
				h = http.Header{}
			}
			normalizeAnthropicCache(req)
			h.Set("anthropic-version", anthropicVersion)
			out, err := json.Marshal(req)
			if err != nil {
				return err
			}
			st := &anthropicStream{out: newChunks(responseModel, emit), tool: map[int]int{}}
			if err := postSSE(ctx, endpoint, h, out, st.event); err != nil {
				return err
			}
			if !st.done {
				return fmt.Errorf("the stream ended before the message did")
			}
			return nil
		}
		if u.ts != nil {
			return withToken(ctx, u.ts, send)
		}
		return send(nil)
	}, body)
}

// claudeOAuthModelIDs gives the dated ID of the short model names a
// subscription login does not take.
var claudeOAuthModelIDs = map[string]string{
	"claude-sonnet-4-5": "claude-sonnet-4-5-20250929",
	"claude-opus-4-5":   "claude-opus-4-5-20251101",
	"claude-haiku-4-5":  "claude-haiku-4-5-20251001",
}

// claudeCodeSystem puts the Claude Code identity line before whatever system
// blocks anthropicRequest built (which may be none).
func claudeCodeSystem(system any) []any {
	id := map[string]any{"type": "text", "text": claudeCodeIdentity}
	if blocks, ok := system.([]any); ok {
		return append([]any{id}, blocks...)
	}
	return []any{id}
}

// anthropicMaxOutput is the output limit of a Claude model, for those that
// refuse the node-wide one.
func anthropicMaxOutput(model string) uint64 {
	switch {
	case strings.Contains(model, "claude-3-haiku"), strings.Contains(model, "claude-3-opus"), strings.Contains(model, "claude-3-sonnet"):
		return 4096
	case strings.Contains(model, "claude-3-5"):
		return 8192
	}
	return 32000 // Opus 4 and 4.1; later models take more, and the node asks for no more than 32768
}

// anthropicRequest turns a chat-completions request into a Messages request.
func anthropicRequest(chat *chatRequest, limit uint64) (map[string]any, error) {
	if limit == 0 {
		limit = anthropicMaxOutput(chat.Model)
	}
	maxTokens := chat.MaxTokens
	if maxTokens == 0 || maxTokens > limit {
		maxTokens = limit
	}
	req := map[string]any{"model": chat.Model, "max_tokens": maxTokens, "stream": true}

	var system []any
	var msgs []map[string]any
	// Consecutive messages of one role become one: tool results, which
	// OpenAI sends as a message each, are blocks of a single user turn.
	add := func(role string, blocks ...any) {
		if len(blocks) == 0 {
			return
		}
		if n := len(msgs); n > 0 && msgs[n-1]["role"] == role {
			msgs[n-1]["content"] = append(msgs[n-1]["content"].([]any), blocks...)
			return
		}
		msgs = append(msgs, map[string]any{"role": role, "content": blocks})
	}
	for i := range chat.Messages {
		m := &chat.Messages[i]
		for _, p := range m.parts() {
			if err := p.CacheControl.valid(); err != nil {
				return nil, err
			}
		}
		switch m.Role {
		case "system", "developer":
			for _, p := range m.parts() {
				if (p.Type == "" || p.Type == "text") && p.Text != "" {
					block := map[string]any{"type": "text", "text": p.Text}
					if p.CacheControl != nil {
						block["cache_control"] = p.CacheControl.value()
					}
					system = append(system, block)
				}
			}
		case "user":
			add("user", anthropicBlocks(m)...)
		case "assistant":
			blocks, err := anthropicThinkingHistory(m.AnthropicThinking)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, anthropicBlocks(m)...)
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": tc.ID, "name": tc.Function.Name, "input": rawObject(tc.Function.Arguments)})
			}
			add("assistant", blocks...)
		case "tool":
			content := anthropicBlocks(m)
			if len(content) == 0 {
				content = []any{map[string]any{"type": "text", "text": ""}}
			}
			add("user", map[string]any{"type": "tool_result", "tool_use_id": m.ToolCallID, "content": content})
		default:
			return nil, fmt.Errorf("unsupported message role %q", m.Role)
		}
	}
	if len(msgs) == 0 {
		return nil, fmt.Errorf("the request has no user message")
	}
	req["messages"] = msgs

	wantJSON, _, schema := chat.jsonSchema()
	if wantJSON {
		hint := "Answer with a single JSON object and nothing else."
		if len(schema) > 0 {
			hint = "Answer with a single JSON object that matches this JSON schema, and nothing else:\n" + string(schema)
		}
		system = append(system, map[string]any{"type": "text", "text": hint})
	}
	if len(system) > 0 {
		req["system"] = system
	}

	// Current Claude models refuse temperature together with top_p.
	switch {
	case chat.Temperature != nil:
		req["temperature"] = min(*chat.Temperature, 1) // OpenAI's range is 0..2, Anthropic's 0..1
	case chat.TopP != nil:
		req["top_p"] = *chat.TopP
	}
	if s := chat.stops(); len(s) > 0 {
		req["stop_sequences"] = s
	}
	if chat.User != "" {
		req["metadata"] = map[string]any{"user_id": chat.User}
	}
	mode, name := chat.toolChoice()
	if len(chat.Tools) > 0 && mode != "none" {
		var tools []any
		for _, t := range chat.Tools {
			if t.Type != "" && t.Type != "function" {
				continue
			}
			schema := t.Function.Parameters
			if len(schema) == 0 || string(schema) == "null" {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			tool := map[string]any{"name": t.Function.Name, "input_schema": schema}
			if err := t.CacheControl.valid(); err != nil {
				return nil, err
			}
			if t.CacheControl != nil {
				tool["cache_control"] = t.CacheControl.value()
			}
			if t.Function.Description != "" {
				tool["description"] = t.Function.Description
			}
			tools = append(tools, tool)
		}
		if len(tools) > 0 {
			req["tools"] = tools
			switch {
			case name != "":
				req["tool_choice"] = map[string]any{"type": "tool", "name": name}
			case mode == "required":
				req["tool_choice"] = map[string]any{"type": "any"}
			case mode == "auto":
				req["tool_choice"] = map[string]any{"type": "auto"}
			}
		}
	}
	return req, nil
}

// anthropicBlocks turns a message's text and images into content blocks.
func anthropicBlocks(m *chatMessage) []any {
	var out []any
	for _, p := range m.parts() {
		before := len(out)
		switch p.Type {
		case "text", "":
			if p.Text != "" {
				out = append(out, map[string]any{"type": "text", "text": p.Text})
			}
		case "image_url":
			if mt, data, ok := dataURL(p.ImageURL.URL); ok {
				out = append(out, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": mt, "data": data}})
			} else if p.ImageURL.URL != "" {
				out = append(out, map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": p.ImageURL.URL}})
			}
		}
		if p.CacheControl != nil && len(out) > before {
			out[len(out)-1].(map[string]any)["cache_control"] = p.CacheControl.value()
		}
	}
	return out
}

// anthropicStream turns Messages stream events into chunks.
type anthropicStream struct {
	out      *chunks
	tool     map[int]int // content block index → tool call index
	prompt   uint64
	output   uint64
	reason   string
	done     bool
	thinking map[int]*anthropicThinkingBlock
}

func (s *anthropicStream) event(data []byte) error {
	var ev struct {
		Type    string `json:"type"`
		Index   int    `json:"index"`
		Message struct {
			Usage anthropicUsage `json:"usage"`
		} `json:"message"`
		ContentBlock struct {
			Type      string `json:"type"`
			ID        string `json:"id"`
			Name      string `json:"name"`
			Text      string `json:"text"`
			Thinking  string `json:"thinking"`
			Signature string `json:"signature"`
			Data      string `json:"data"`
		} `json:"content_block"`
		Delta struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			Thinking    string `json:"thinking"`
			Signature   string `json:"signature"`
			PartialJSON string `json:"partial_json"`
			StopReason  string `json:"stop_reason"`
		} `json:"delta"`
		Usage anthropicUsage `json:"usage"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &ev); err != nil {
		return nil // not ours to judge: skip what cannot be read
	}
	switch ev.Type {
	case "message_start":
		s.prompt = ev.Message.Usage.prompt()
		s.output = ev.Message.Usage.OutputTokens
	case "content_block_start":
		switch ev.ContentBlock.Type {
		case "thinking", "redacted_thinking":
			if s.thinking == nil {
				s.thinking = map[int]*anthropicThinkingBlock{}
			}
			s.thinking[ev.Index] = &anthropicThinkingBlock{Type: ev.ContentBlock.Type, Thinking: ev.ContentBlock.Thinking, Signature: ev.ContentBlock.Signature, Data: ev.ContentBlock.Data}
			return s.out.reasoning(ev.ContentBlock.Thinking)
		case "tool_use":
			i, err := s.out.toolStart(ev.ContentBlock.ID, ev.ContentBlock.Name, "")
			s.tool[ev.Index] = i
			return err
		case "text":
			return s.out.text(ev.ContentBlock.Text)
		}
	case "content_block_delta":
		switch ev.Delta.Type {
		case "text_delta":
			return s.out.text(ev.Delta.Text)
		case "thinking_delta":
			if block := s.thinking[ev.Index]; block != nil {
				block.Thinking += ev.Delta.Thinking
			}
			return s.out.reasoning(ev.Delta.Thinking)
		case "signature_delta":
			if block := s.thinking[ev.Index]; block != nil {
				block.Signature += ev.Delta.Signature
			}
		case "input_json_delta":
			if i, ok := s.tool[ev.Index]; ok {
				return s.out.toolArgs(i, ev.Delta.PartialJSON)
			}
		}
	case "content_block_stop":
		if block := s.thinking[ev.Index]; block != nil {
			delete(s.thinking, ev.Index)
			if block.Signature != "" || block.Type == "redacted_thinking" {
				return s.out.send(map[string]any{"anthropic_thinking": []any{block}}, "", nil)
			}
		}
	case "message_delta":
		if ev.Delta.StopReason != "" {
			s.reason = ev.Delta.StopReason
		}
		if ev.Usage.OutputTokens > 0 {
			s.output = ev.Usage.OutputTokens
		}
		if p := ev.Usage.prompt(); p > 0 {
			s.prompt = p
		}
	case "message_stop":
		s.done = true
		reason := "stop"
		switch s.reason {
		case "max_tokens":
			reason = "length"
		case "tool_use":
			reason = "tool_calls"
		case "refusal":
			reason = "content_filter"
		}
		return s.out.finish(reason, s.prompt, s.output)
	case "error":
		status := http.StatusBadGateway
		if ev.Error.Type == "overloaded_error" {
			status = http.StatusServiceUnavailable
		}
		return &node.StatusError{Status: status, Body: ev.Error.Type + ": " + ev.Error.Message}
	}
	return nil
}

type anthropicUsage struct {
	InputTokens         uint64 `json:"input_tokens"`
	OutputTokens        uint64 `json:"output_tokens"`
	CacheCreationTokens uint64 `json:"cache_creation_input_tokens"`
	CacheReadTokens     uint64 `json:"cache_read_input_tokens"`
}

// prompt is everything the request sent in: cached parts are counted apart
// by Anthropic and belong to the prompt all the same.
func (u anthropicUsage) prompt() uint64 {
	return u.InputTokens + u.CacheCreationTokens + u.CacheReadTokens
}

func anthropicModels(ctx context.Context, base string, header http.Header) ([]string, error) {
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	header.Set("anthropic-version", anthropicVersion)
	if err := getJSON(ctx, base+"/v1/models?limit=1000", header, &out); err != nil {
		return nil, err
	}
	ids := []string{}
	for _, m := range out.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}
