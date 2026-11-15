package apicompat

// responses → chat: a Responses request (Codex) for an upstream that speaks
// OpenAI chat completions, and the upstream's chunks back into Responses
// events.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type respChatMessage struct {
	Role             string            `json:"role"`
	Content          any               `json:"content"`
	ReasoningContent string            `json:"reasoning_content,omitempty"`
	ToolCalls        []respChatToolUse `json:"tool_calls,omitempty"`
	ToolCallID       string            `json:"tool_call_id,omitempty"`
}

type respChatToolUse struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func respToChat(body []byte, opt Options) (*Conversion, error) {
	r, err := respParseRequest(body)
	if err != nil {
		return nil, err
	}

	var msgs []respChatMessage
	// One system message: instructions and the system/developer messages that
	// open the conversation. Strict upstreams accept system content only at
	// the start, so later ones become user messages.
	var system []string
	if strings.TrimSpace(r.instructions) != "" {
		system = append(system, r.instructions)
	}
	items := r.items
	for len(items) > 0 && items[0].kind == respItemMessage && items[0].role == "system" {
		if t := respJoinText(items[0].parts); strings.TrimSpace(t) != "" {
			system = append(system, t)
		}
		items = items[1:]
	}
	if len(system) > 0 {
		msgs = append(msgs, respChatMessage{Role: "system", Content: strings.Join(system, "\n\n")})
	}

	// Reasoning goes back as reasoning_content on the assistant message it
	// led to (DeepSeek's thinking mode requires it). A turn's reasoning also
	// covers its later tool calls, until the next user turn.
	pending, turn := "", ""
	var media []respChatMedia // tool output images, sent after the outputs
	flushMedia := func() {
		if len(media) == 0 {
			return
		}
		var parts []map[string]any
		last := ""
		for _, m := range media {
			if m.callID != last {
				parts = append(parts, map[string]any{"type": "text", "text": fmt.Sprintf("[Tool output media for call %s]", m.callID)})
				last = m.callID
			}
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": m.url}})
		}
		msgs = append(msgs, respChatMessage{Role: "user", Content: parts})
		media = nil
	}
	assistant := func() *respChatMessage {
		if n := len(msgs); n > 0 && msgs[n-1].Role == "assistant" {
			if msgs[n-1].ReasoningContent == "" {
				msgs[n-1].ReasoningContent = pending
			}
			pending = ""
			return &msgs[n-1]
		}
		msgs = append(msgs, respChatMessage{Role: "assistant"})
		m := &msgs[len(msgs)-1]
		if pending != "" {
			m.ReasoningContent = pending
		} else {
			m.ReasoningContent = turn
		}
		pending = ""
		return m
	}
	for _, it := range items {
		if it.kind != respItemOutput {
			flushMedia()
		}
		switch it.kind {
		case respItemMessage:
			if it.role == "assistant" {
				text := respJoinText(it.parts)
				m := assistant()
				if s, _ := m.Content.(string); s != "" {
					text = s + "\n\n" + text
				}
				m.Content = text
				continue
			}
			if it.role == "user" {
				pending, turn = "", "" // a user turn ends the thinking span; a developer notice does not
			}
			msgs = append(msgs, respChatMessage{Role: "user", Content: respChatContent(it.parts)})
		case respItemReasoning:
			text := it.summary
			if text == "" {
				if b, ok := respDecodeThinking(it.encrypted); ok {
					text = b.Thinking
				}
			}
			if text != "" {
				pending, turn = text, text
			}
		case respItemCall:
			m := assistant()
			var tc respChatToolUse
			tc.ID, tc.Type = it.callID, "function"
			tc.Function.Name, tc.Function.Arguments = it.name, it.args
			m.ToolCalls = append(m.ToolCalls, tc)
		case respItemOutput:
			msgs = append(msgs, respChatMessage{Role: "tool", ToolCallID: it.callID, Content: it.outText})
			for _, p := range it.outParts {
				if p.kind == "image" {
					media = append(media, respChatMedia{callID: it.callID, url: p.url})
				}
			}
		}
	}
	flushMedia()
	for i := range msgs {
		// An assistant message without text has null content when it calls tools.
		if msgs[i].Role == "assistant" && msgs[i].Content == nil {
			if len(msgs[i].ToolCalls) == 0 {
				msgs[i].Content = ""
			}
		}
	}
	if len(msgs) == 0 || len(msgs) == 1 && msgs[0].Role == "system" {
		return nil, errors.New("apicompat: the request has no message to send")
	}

	out := map[string]any{
		"model": r.model, "messages": msgs, "stream": true,
		"stream_options": map[string]any{"include_usage": true},
	}
	if r.maxOutput > 0 {
		out["max_tokens"] = r.maxOutput
	}
	if r.temperature != nil {
		out["temperature"] = *r.temperature
	}
	if r.topP != nil {
		out["top_p"] = *r.topP
	}
	switch r.effort {
	case "":
	default:
		out["reasoning_effort"] = r.effort
	}
	if rf := respChatResponseFormat(r.textFormat); rf != nil {
		out["response_format"] = rf
	}
	var tools []map[string]any
	for _, d := range r.tools.decls {
		fn := map[string]any{"name": d.name, "parameters": d.parameters}
		if d.description != "" {
			fn["description"] = d.description
		}
		if d.strict != nil {
			fn["strict"] = *d.strict
		}
		tools = append(tools, map[string]any{"type": "function", "function": fn})
	}
	if len(tools) > 0 {
		out["tools"] = tools
		if c := respChatToolChoice(r.toolChoice, r.tools); c != nil {
			out["tool_choice"] = c
		}
		if r.parallel != nil {
			out["parallel_tool_calls"] = *r.parallel
		}
	}

	b, err := respMarshal(out)
	if err != nil {
		return nil, err
	}
	model := opt.Model
	if model == "" {
		model = r.model
	}
	return &Conversion{Body: b, Translator: &respFromChat{s: newRespStream(model, r.tools), calls: map[int]*respCallState{}}}, nil
}

type respChatMedia struct{ callID, url string }

// respChatContent is user content: a string when it is only text, else parts.
func respChatContent(parts []respPart) any {
	onlyText := true
	for _, p := range parts {
		onlyText = onlyText && p.kind == "text"
	}
	if onlyText {
		return respJoinText(parts)
	}
	var out []map[string]any
	for _, p := range parts {
		switch p.kind {
		case "text":
			out = append(out, map[string]any{"type": "text", "text": p.text})
		case "image":
			img := map[string]any{"url": p.url}
			if p.detail != "" {
				img["detail"] = p.detail
			}
			out = append(out, map[string]any{"type": "image_url", "image_url": img})
		case "file":
			f := map[string]any{}
			if p.fileData != "" {
				f["file_data"] = p.fileData
			}
			if p.fileName != "" {
				f["filename"] = p.fileName
			}
			if p.fileID != "" {
				f["file_id"] = p.fileID
			}
			if len(f) > 0 && (p.fileData != "" || p.fileID != "") {
				out = append(out, map[string]any{"type": "file", "file": f})
			}
		}
	}
	if len(out) == 0 {
		return ""
	}
	return out
}

// respChatResponseFormat maps text.format to response_format.
func respChatResponseFormat(raw json.RawMessage) any {
	var f map[string]json.RawMessage
	if json.Unmarshal(raw, &f) != nil || f == nil {
		return nil
	}
	switch respString(f["type"]) {
	case "json_object":
		return map[string]any{"type": "json_object"}
	case "json_schema":
		schema := map[string]json.RawMessage{}
		for k, v := range f {
			if k != "type" {
				schema[k] = v
			}
		}
		return map[string]any{"type": "json_schema", "json_schema": schema}
	}
	return nil // text is the default
}

// respChatToolChoice maps tool_choice; a choice naming a tool the upstream
// was not given is dropped, since chat upstreams reject it.
func respChatToolChoice(raw json.RawMessage, tools *respTools) any {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if s := respString(raw); s != "" {
		switch s {
		case "auto", "none", "required":
			return s
		}
		return nil
	}
	var c struct {
		Type     string `json:"type"`
		Name     string `json:"name"`
		Mode     string `json:"mode"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return nil
	}
	var target respClientTool
	switch c.Type {
	case "function":
		name := c.Name
		if name == "" {
			name = c.Function.Name
		}
		target = respClientTool{kind: respKindFunction, name: name}
	case "custom":
		target = respClientTool{kind: respKindCustom, name: c.Name}
	case "tool_search":
		target = respClientTool{kind: respKindToolSearch, name: respToolSearchName}
	case "local_shell":
		target = respClientTool{kind: respKindLocalShell, name: respLocalShellName}
	case "allowed_tools":
		if c.Mode == "required" {
			return "required"
		}
		return "auto"
	default:
		return nil
	}
	name, ok := tools.toUpstream[target]
	if !ok {
		return nil
	}
	return map[string]any{"type": "function", "function": map[string]any{"name": name}}
}

// --- the stream ---

type respFromChat struct {
	s         *respStream
	calls     map[int]*respCallState
	order     []int
	finish    string
	usage     respUsage
	gotUsage  bool
	reasoning strings.Builder
	text      bool
	reasoned  bool // a reasoning item was sent
}

type respChatChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int             `json:"index"`
		Delta        *respChatDelta  `json:"delta"`
		Message      *respChatDelta  `json:"message"`
		FinishReason json.RawMessage `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		TotalTokens         int `json:"total_tokens"`
		PromptCacheHit      int `json:"prompt_cache_hit_tokens"`
		PromptTokensDetails *struct {
			CachedTokens        int `json:"cached_tokens"`
			CacheCreationTokens int `json:"cache_creation_tokens"`
			CacheWriteTokens    int `json:"cache_write_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails *struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
	Error json.RawMessage `json:"error"`
}

type respChatDelta struct {
	Content          *string `json:"content"`
	ReasoningContent *string `json:"reasoning_content"`
	Reasoning        any     `json:"reasoning"`
	Refusal          *string `json:"refusal"`
	ToolCalls        []struct {
		Index    *int   `json:"index"`
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

func (d *respChatDelta) reasoningText() string {
	if d.ReasoningContent != nil && *d.ReasoningContent != "" {
		return *d.ReasoningContent
	}
	if s, ok := d.Reasoning.(string); ok {
		return s
	}
	return ""
}

func (t *respFromChat) Event(payload []byte, emit func([]byte) error) error {
	t.s.emit = emit
	if strings.TrimSpace(string(payload)) == "[DONE]" {
		if t.s.started && !t.s.finished {
			return t.end()
		}
		return nil
	}
	var ch respChatChunk
	if err := json.Unmarshal(payload, &ch); err != nil {
		return fmt.Errorf("apicompat: parse chat chunk: %w", err)
	}
	if e := strings.TrimSpace(string(ch.Error)); e != "" && e != "null" {
		var detail struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		}
		if json.Unmarshal(ch.Error, &detail) == nil && detail.Message != "" {
			return fmt.Errorf("apicompat: upstream error: %s: %s", detail.Type, detail.Message)
		}
		return fmt.Errorf("apicompat: upstream error: %s", e)
	}
	if t.s.finished {
		return nil
	}
	if t.s.model == "" && ch.Model != "" {
		t.s.model = ch.Model
	}
	if u := ch.Usage; u != nil {
		t.gotUsage = true
		t.usage = respUsage{input: u.PromptTokens, output: u.CompletionTokens, total: u.TotalTokens, cached: u.PromptCacheHit}
		if d := u.PromptTokensDetails; d != nil {
			if d.CachedTokens > 0 {
				t.usage.cached = d.CachedTokens
			}
			t.usage.cacheWrite = max(d.CacheCreationTokens, d.CacheWriteTokens)
		}
		if d := u.CompletionTokensDetails; d != nil {
			t.usage.reasoning = d.ReasoningTokens
		}
	}
	if err := t.s.start(); err != nil {
		return err
	}
	for _, c := range ch.Choices {
		if c.Index != 0 {
			continue
		}
		if c.Delta != nil {
			if err := t.delta(c.Delta, false); err != nil {
				return err
			}
		}
		if c.Message != nil {
			// a final chunk carrying the whole message: use what was not streamed
			if err := t.delta(c.Message, true); err != nil {
				return err
			}
		}
		if f := respString(c.FinishReason); f != "" {
			t.finish = f
		}
	}
	return nil
}

func (t *respFromChat) delta(d *respChatDelta, whole bool) error {
	if r := d.reasoningText(); r != "" && !(whole && t.reasoning.Len() > 0) {
		t.reasoning.WriteString(r)
		t.reasoned = true
		if err := t.s.reasoningDelta(r); err != nil {
			return err
		}
	}
	text := ""
	if d.Content != nil {
		text = *d.Content
	}
	if d.Refusal != nil {
		text += *d.Refusal
	}
	if text != "" && !(whole && t.text) {
		if err := t.s.closeReasoning(""); err != nil {
			return err
		}
		t.text = true
		if err := t.s.textDelta(text); err != nil {
			return err
		}
	}
	if whole && len(t.order) > 0 {
		return nil
	}
	for _, tc := range d.ToolCalls {
		idx := -1
		if tc.Index != nil {
			idx = *tc.Index
		} else {
			// No index: a new id is a new call, no id continues the last one.
			for _, i := range t.order {
				if tc.ID != "" && t.calls[i].callID == tc.ID {
					idx = i
				}
			}
			if idx < 0 {
				if tc.ID == "" && len(t.order) > 0 {
					idx = t.order[len(t.order)-1]
				} else {
					idx = len(t.order)
					for t.calls[idx] != nil {
						idx++
					}
				}
			}
		}
		c := t.calls[idx]
		if c == nil {
			if err := t.s.closeReasoning(""); err != nil {
				return err
			}
			var err error
			c, err = t.s.openCall(tc.ID, tc.Function.Name)
			if err != nil {
				return err
			}
			t.calls[idx] = c
			t.order = append(t.order, idx)
		} else {
			if tc.ID != "" && !c.announced {
				c.callID = tc.ID
			}
			if err := t.s.setCallName(c, tc.Function.Name); err != nil {
				return err
			}
		}
		if err := t.s.callArgs(c, tc.Function.Arguments); err != nil {
			return err
		}
	}
	return nil
}

// end finishes the response: a chat stream's end is its terminal event,
// since the usage comes after the finish reason.
func (t *respFromChat) end() error {
	// A reasoning-only answer (no text, no tool call) shows its reasoning
	// as the answer, as sub2api does, rather than an empty reply.
	if !t.text && len(t.order) == 0 && strings.TrimSpace(t.reasoning.String()) != "" {
		if err := t.s.closeReasoning(""); err != nil {
			return err
		}
		if err := t.s.textDelta(t.reasoning.String()); err != nil {
			return err
		}
	}
	// Reasoning the upstream did not show still yields a reasoning item.
	if !t.reasoned && t.usage.reasoning > 0 {
		if err := t.s.openReasoning(); err != nil {
			return err
		}
		if err := t.s.closeReasoning(""); err != nil {
			return err
		}
	}
	reason := ""
	switch t.finish {
	case "length":
		reason = "max_output_tokens"
	case "content_filter":
		reason = "content_filter"
	}
	return t.s.finish(reason, t.usage)
}

func (t *respFromChat) Close(emit func([]byte) error) error {
	t.s.emit = emit
	if t.s.finished {
		return nil
	}
	if !t.s.produced() && t.finish == "" && !t.gotUsage {
		return errors.New("apicompat: the upstream stream ended without a response")
	}
	return t.end()
}
