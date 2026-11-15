package apicompat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// chat → Messages. The request becomes an Anthropic Messages request that
// streams; the events come back as chat chunks. Signed thinking blocks travel
// through the chat client as anthropic_thinking: on the stream at the end of
// each block, back on the assistant message of a later request.

func chatToMessages(body []byte, opt Options) (*Conversion, error) {
	r, err := chatParse(body)
	if err != nil {
		return nil, err
	}
	req, err := chatMessagesRequest(r)
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	model := opt.Model
	if model == "" {
		model = r.Model
	}
	return &Conversion{Body: out, Translator: &chatFromMessages{out: newChatChunker(model), promptEst: chatPromptEstimate(r), blocks: map[int]*chatMessagesBlock{}}}, nil
}

// chatAnthropicMaxOutput is the output limit of a Claude model, for those
// that refuse more.
func chatAnthropicMaxOutput(model string) uint64 {
	switch {
	case strings.Contains(model, "claude-3-haiku"), strings.Contains(model, "claude-3-opus"), strings.Contains(model, "claude-3-sonnet"):
		return 4096
	case strings.Contains(model, "claude-3-5"):
		return 8192
	}
	return 32000 // Opus 4 and 4.1; later models take more, and a node asks for no more than 32768
}

func chatMessagesRequest(r *chatRequest) (map[string]any, error) {
	limit := chatAnthropicMaxOutput(r.Model)
	maxTokens := r.maxTokens()
	if maxTokens == 0 || maxTokens > limit {
		maxTokens = limit
	}
	req := map[string]any{"model": r.Model, "max_tokens": maxTokens, "stream": true}

	var system []any
	var msgs []map[string]any
	add := func(role string, blocks ...any) {
		if len(blocks) > 0 {
			msgs = append(msgs, map[string]any{"role": role, "content": blocks})
		}
	}
	ids := newChatLegacyIDs(r.Messages)
	for i := range r.Messages {
		m := &r.Messages[i]
		for _, p := range m.parts() {
			if err := p.CacheControl.valid(); err != nil {
				return nil, err
			}
		}
		switch m.Role {
		case "system", "developer":
			for _, p := range m.parts() {
				if p.isText() && p.Text != "" {
					block := map[string]any{"type": "text", "text": p.Text}
					if p.CacheControl != nil {
						block["cache_control"] = p.CacheControl.value()
					}
					system = append(system, block)
				}
			}
		case "assistant":
			blocks, err := chatThinkingHistory(m.AnthropicThinking)
			if err != nil {
				return nil, err
			}
			if len(blocks) > 0 {
				// The signed blocks carry the reasoning: it is not repeated as text.
				blocks = append(blocks, chatAnthropicBlocks(m, false)...)
			} else {
				blocks = chatAnthropicBlocks(m, true)
			}
			for _, tc := range m.toolCalls(ids) {
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": chatToolUseID(tc.ID), "name": tc.Function.Name, "input": chatObjectJSON(tc.Function.Arguments)})
			}
			add("assistant", blocks...)
		case "tool", "function":
			result := map[string]any{"type": "tool_result", "tool_use_id": chatToolUseID(m.resultID(ids))}
			content := chatAnthropicBlocks(m, false)
			if len(content) == 0 {
				content = []any{map[string]any{"type": "text", "text": "(empty)"}}
			}
			result["content"] = content
			add("user", result)
		default: // user, and any other role as the user's
			add("user", chatAnthropicBlocks(m, false)...)
		}
	}
	msgs = chatPairTools(msgs)
	if len(msgs) == 0 {
		return nil, fmt.Errorf("the request has no user message")
	}
	// A final assistant turn is a prefill, which may not end in whitespace.
	if last := msgs[len(msgs)-1]; last["role"] == "assistant" {
		blocks := last["content"].([]any)
		if b, ok := blocks[len(blocks)-1].(map[string]any); ok && b["type"] == "text" {
			b["text"] = strings.TrimRight(b["text"].(string), " \t\r\n")
		}
	}
	req["messages"] = msgs

	switch rf := r.responseFormat(); rf.kind {
	case "json_object":
		system = append(system, map[string]any{"type": "text", "text": "Answer with a single JSON object and nothing else."})
	case "json_schema":
		hint := "Answer with a single JSON object and nothing else."
		if len(rf.schema) > 0 {
			hint = "Answer with a single JSON object that matches this JSON schema, and nothing else:\n" + string(rf.schema)
		}
		system = append(system, map[string]any{"type": "text", "text": hint})
	}
	if len(system) > 0 {
		req["system"] = system
	}

	// Current Claude models refuse temperature together with top_p.
	switch {
	case r.Temperature != nil:
		req["temperature"] = min(*r.Temperature, 1) // OpenAI's range is 0..2, Anthropic's 0..1
	case r.TopP != nil:
		req["top_p"] = *r.TopP
	}
	if s := r.stops(); len(s) > 0 {
		req["stop_sequences"] = s
	}
	if r.User != "" {
		req["metadata"] = map[string]any{"user_id": r.User}
	}

	forced := false
	tools, err := chatAnthropicTools(r.Tools)
	if err != nil {
		return nil, err
	}
	if len(tools) > 0 {
		req["tools"] = tools
		mode, name, _ := r.toolChoice()
		var choice map[string]any
		switch {
		case name != "":
			choice, forced = map[string]any{"type": "tool", "name": name}, true
		case mode == "required":
			choice, forced = map[string]any{"type": "any"}, true
		case mode == "none":
			// The tools stay: a history with tool_use blocks needs them.
			choice = map[string]any{"type": "none"}
		case mode == "auto":
			choice = map[string]any{"type": "auto"}
		}
		if r.ParallelToolCalls != nil && !*r.ParallelToolCalls && (choice == nil || choice["type"] != "none") {
			if choice == nil {
				choice = map[string]any{"type": "auto"}
			}
			choice["disable_parallel_tool_use"] = true
		}
		if choice != nil {
			req["tool_choice"] = choice
		}
	}
	chatAnthropicThinking(req, r, maxTokens, forced)
	chatNormalizeCache(req)
	return req, nil
}

// AdaptiveThinking reports whether a Claude model thinks adaptively (thinking
// {"type":"adaptive"} with output_config.effort) and refuses a thinking
// budget: the 5.5 generation. A model server that refuses either form says
// so, and the Anthropic adapter switches to the other.
func AdaptiveThinking(model string) bool {
	model = strings.ToLower(model)
	return strings.HasPrefix(model, "claude-") && strings.Contains(model, "-5-5")
}

// chatAnthropicThinking maps reasoning_effort to extended thinking. The 5.5
// models think adaptively at an effort; earlier models take a budget, which
// must be at least 1024 tokens and below max_tokens, and refuse sampling
// settings and a forced tool with it.
func chatAnthropicThinking(req map[string]any, r *chatRequest, maxTokens uint64, forced bool) {
	effort := strings.ToLower(strings.TrimSpace(r.ReasoningEffort))
	if effort == "" {
		return
	}
	model := strings.ToLower(r.Model)
	if AdaptiveThinking(model) {
		switch effort {
		case "none":
			switch {
			case strings.Contains(model, "sonnet"):
				req["thinking"] = map[string]any{"type": "between_tools"}
			case strings.Contains(model, "haiku"):
				req["thinking"] = map[string]any{"type": "disabled"}
			default:
				req["thinking"] = map[string]any{"type": "adaptive"}
			}
			effort = "low"
		case "minimal":
			req["thinking"], effort = map[string]any{"type": "adaptive"}, "low"
		case "low", "medium", "high", "xhigh", "max":
			req["thinking"] = map[string]any{"type": "adaptive"}
		default:
			req["thinking"], effort = map[string]any{"type": "adaptive"}, "medium"
		}
		req["output_config"] = map[string]any{"effort": effort}
		return
	}
	budget := map[string]uint64{"low": 1024, "medium": 4096, "high": 10240, "xhigh": 32768, "max": 32768}[effort]
	if budget == 0 || forced || maxTokens <= 1024 {
		return // none, minimal, unknown; or no room to think
	}
	budget = min(budget, maxTokens-1)
	req["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
	delete(req, "temperature")
	if p, ok := req["top_p"].(float64); ok && p < 0.95 {
		delete(req, "top_p")
	}
}

// chatThinkingHistory turns anthropic_thinking blocks back into the signed
// blocks they were: a block without its signature (or data) is refused, as
// the upstream would.
func chatThinkingHistory(history []json.RawMessage) ([]any, error) {
	var blocks []any
	for _, raw := range history {
		var b chatThinkingBlock
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

// chatAnthropicBlocks turns a message's content into content blocks: text,
// images and documents. withReasoning marks an assistant message's reasoning
// and thinking parts with <thinking> tags in its text.
func chatAnthropicBlocks(m *chatMessage, withReasoning bool) []any {
	var out []any
	if m.Role == "assistant" && withReasoning {
		if r := m.reasoning(); r != "" {
			out = append(out, map[string]any{"type": "text", "text": "<thinking>" + r + "</thinking>"})
		}
	}
	for _, p := range m.parts() {
		before := len(out)
		switch {
		case p.isText() || p.Type == "output_text":
			if strings.TrimSpace(p.Text) != "" {
				out = append(out, map[string]any{"type": "text", "text": p.Text})
			}
		case p.Type == "thinking" || p.Type == "reasoning":
			t := chatFirst(p.Thinking, p.Text)
			if withReasoning && t != "" {
				out = append(out, map[string]any{"type": "text", "text": "<thinking>" + t + "</thinking>"})
			}
		case p.Type == "image_url":
			url, _ := p.image()
			if mt, data, ok := chatDataURL(url); ok {
				if strings.TrimSpace(data) != "" {
					out = append(out, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": mt, "data": data}})
				}
			} else if url != "" {
				out = append(out, map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": url}})
			}
		case p.Type == "file":
			if p.File == nil {
				continue
			}
			if mt, data, ok := chatDataURL(p.File.FileData); ok && strings.TrimSpace(data) != "" {
				doc := map[string]any{"type": "document", "source": map[string]any{"type": "base64", "media_type": mt, "data": data}}
				if mt == "text/plain" {
					if text, err := chatBase64Text(data); err == nil {
						doc["source"] = map[string]any{"type": "text", "media_type": mt, "data": text}
					}
				}
				if p.File.Filename != "" {
					doc["title"] = p.File.Filename
				}
				out = append(out, doc)
			}
		}
		if p.CacheControl != nil && len(out) > before {
			out[len(out)-1].(map[string]any)["cache_control"] = p.CacheControl.value()
		}
	}
	return out
}

var chatToolUseIDBad = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// chatToolUseID makes a call ID one Anthropic takes (letters, digits, _ and
// -), the same for a call and its result.
func chatToolUseID(id string) string {
	if id == "" {
		return "toolu_empty"
	}
	return chatToolUseIDBad.ReplaceAllString(id, "_")
}

// chatPairTools makes the turns what Anthropic requires: every tool_use is
// answered by a tool_result in the very next user turn (results are moved
// there from wherever the history had them; calls without a result and
// results without a call are dropped), the roles alternate (consecutive
// turns of a role become one: tool results, which chat sends as a message
// each, are blocks of a single user turn), and a user turn starts with its
// tool results.
func chatPairTools(msgs []map[string]any) []map[string]any {
	results := map[string]any{}
	for _, m := range msgs {
		if m["role"] != "user" {
			continue
		}
		for _, b := range m["content"].([]any) {
			if bm, ok := b.(map[string]any); ok && bm["type"] == "tool_result" {
				if _, dup := results[bm["tool_use_id"].(string)]; !dup {
					results[bm["tool_use_id"].(string)] = b
				}
			}
		}
	}
	var out []map[string]any
	add := func(role any, blocks []any) {
		if len(blocks) == 0 {
			return
		}
		if n := len(out); n > 0 && out[n-1]["role"] == role {
			out[n-1]["content"] = append(out[n-1]["content"].([]any), blocks...)
			return
		}
		out = append(out, map[string]any{"role": role, "content": blocks})
	}
	for _, m := range msgs {
		var kept, answers []any
		for _, b := range m["content"].([]any) {
			bm, _ := b.(map[string]any)
			switch {
			case bm != nil && bm["type"] == "tool_use":
				id := bm["id"].(string)
				if r, ok := results[id]; ok && r != nil {
					kept, answers = append(kept, b), append(answers, r)
					results[id] = nil // each result answers one call
				}
				continue
			case bm != nil && bm["type"] == "tool_result":
				continue // it goes after its call
			}
			kept = append(kept, b)
		}
		add(m["role"], kept)
		add("user", answers)
	}
	for _, m := range out {
		if m["role"] == "user" {
			slices.SortStableFunc(m["content"].([]any), func(a, b any) int { return chatResultRank(a) - chatResultRank(b) })
		}
	}
	return out
}

func chatResultRank(b any) int {
	if bm, ok := b.(map[string]any); ok && bm["type"] == "tool_result" {
		return 0
	}
	return 1
}

// chatAnthropicTools converts function tools (their schema made an object
// schema Anthropic takes) and web_search.
func chatAnthropicTools(tools []chatTool) ([]any, error) {
	var out []any
	for i := range tools {
		t := &tools[i]
		if err := t.CacheControl.valid(); err != nil {
			return nil, err
		}
		var tool map[string]any
		switch {
		case strings.EqualFold(strings.TrimSpace(t.Type), "web_search"):
			tool = map[string]any{"type": "web_search_20250305", "name": "web_search"}
		case t.isFunction():
			tool = map[string]any{"name": t.Function.Name, "input_schema": chatAnthropicSchema(t.Function.Parameters)}
			if t.Function.Description != "" {
				tool["description"] = t.Function.Description
			}
		default:
			continue
		}
		if t.CacheControl != nil {
			tool["cache_control"] = t.CacheControl.value()
		}
		out = append(out, tool)
	}
	return out, nil
}

// chatAnthropicSchema makes a tool's parameters an input_schema Anthropic
// takes: an object at the root, with properties, and no oneOf, anyOf or
// allOf there. A root union becomes one object: the properties of all
// branches (one a branch defines differently becomes an anyOf of both), and
// as required what every branch of a oneOf or anyOf requires, and what any
// branch of an allOf does.
func chatAnthropicSchema(schema json.RawMessage) json.RawMessage {
	const empty = `{"type":"object","properties":{}}`
	var m map[string]json.RawMessage
	if len(schema) == 0 || json.Unmarshal(schema, &m) != nil || m == nil {
		return json.RawMessage(empty)
	}
	chatFlattenUnions(m)
	if raw, ok := m["type"]; !ok || string(raw) == "null" {
		m["type"] = json.RawMessage(`"object"`)
	} else {
		var typ string
		if json.Unmarshal(raw, &typ) != nil || typ != "object" {
			return json.RawMessage(empty)
		}
	}
	if _, ok := m["properties"]; !ok {
		m["properties"] = json.RawMessage(`{}`)
	}
	out, err := json.Marshal(m)
	if err != nil {
		return json.RawMessage(empty)
	}
	return out
}

func chatFlattenUnions(schema map[string]json.RawMessage) {
	props := map[string]json.RawMessage{}
	if raw, ok := schema["properties"]; ok {
		_ = json.Unmarshal(raw, &props)
		if props == nil {
			props = map[string]json.RawMessage{}
		}
	}
	required := chatRequired(schema)
	found := false
	for _, kw := range []string{"oneOf", "anyOf", "allOf"} {
		raw, ok := schema[kw]
		if !ok {
			continue
		}
		found = true
		delete(schema, kw)
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil || len(items) == 0 {
			continue
		}
		var common []string
		for i, item := range items {
			var branch map[string]json.RawMessage
			if json.Unmarshal(item, &branch) != nil || branch == nil {
				branch = nil
			} else {
				chatFlattenUnions(branch)
			}
			object := false
			if branch != nil {
				if t, ok := branch["type"]; ok {
					var typ string
					object = json.Unmarshal(t, &typ) == nil && typ == "object"
				} else {
					_, object = branch["properties"]
				}
			}
			var req []string
			if object {
				var bp map[string]json.RawMessage
				if json.Unmarshal(branch["properties"], &bp) == nil {
					for name, p := range bp {
						if have, ok := props[name]; ok && !chatSameJSON(have, p) {
							both, _ := json.Marshal(map[string][]json.RawMessage{"anyOf": {have, p}})
							props[name] = both
						} else if !ok {
							props[name] = p
						}
					}
				}
				req = chatRequired(branch)
			}
			if kw == "allOf" {
				required = chatUnion(required, req)
				continue
			}
			if i == 0 {
				common = req
			} else {
				common = slices.DeleteFunc(common, func(n string) bool { return !slices.Contains(req, n) })
			}
		}
		if kw != "allOf" {
			required = chatUnion(required, common)
		}
	}
	if !found {
		return
	}
	if len(props) > 0 {
		schema["properties"], _ = json.Marshal(props)
	}
	if len(required) > 0 {
		schema["required"], _ = json.Marshal(required)
	}
}

func chatRequired(schema map[string]json.RawMessage) []string {
	var req []string
	if raw, ok := schema["required"]; ok {
		_ = json.Unmarshal(raw, &req)
	}
	return req
}

func chatUnion(a, b []string) []string {
	out := slices.Clone(a)
	for _, n := range b {
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

func chatSameJSON(a, b json.RawMessage) bool {
	var x, y bytes.Buffer
	return json.Compact(&x, a) == nil && json.Compact(&y, b) == nil && x.String() == y.String()
}

// chatNormalizeCache keeps the request's cache breakpoints within what
// Anthropic takes: at most four (removed first from the tools' end, then the
// messages' start, then the system's end), never on thinking blocks, and no
// 1h breakpoint after a 5m one in processing order (tools, system,
// messages). Run it again after adding blocks with breakpoints.
func chatNormalizeCache(req map[string]any) {
	var tools, system, messages []map[string]any
	collect := func(raw []any, dst *[]map[string]any) {
		for _, item := range raw {
			b, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if b["type"] == "thinking" || b["type"] == "redacted_thinking" {
				delete(b, "cache_control")
				continue
			}
			if _, ok := b["cache_control"].(map[string]any); ok {
				*dst = append(*dst, b)
			}
			if inner, ok := b["content"].([]any); ok && b["type"] == "tool_result" {
				for _, c := range inner {
					if cb, ok := c.(map[string]any); ok {
						if _, ok := cb["cache_control"].(map[string]any); ok {
							*dst = append(*dst, cb)
						}
					}
				}
			}
		}
	}
	if raw, ok := req["tools"].([]any); ok {
		collect(raw, &tools)
	}
	if raw, ok := req["system"].([]any); ok {
		collect(raw, &system)
	}
	if raw, ok := req["messages"].([]map[string]any); ok {
		for _, msg := range raw {
			if blocks, ok := msg["content"].([]any); ok {
				collect(blocks, &messages)
			}
		}
	}
	remaining := len(tools) + len(system) + len(messages) - 4
	remove := func(b map[string]any) {
		if remaining > 0 {
			delete(b, "cache_control")
			remaining--
		}
	}
	for i := len(tools) - 1; i >= 0; i-- {
		remove(tools[i])
	}
	for _, b := range messages {
		remove(b)
	}
	for i := len(system) - 1; i >= 0; i-- {
		remove(system[i])
	}
	ordered := append(append(append([]map[string]any{}, tools...), system...), messages...)
	longer := false
	for i := len(ordered) - 1; i >= 0; i-- {
		cc, ok := ordered[i]["cache_control"].(map[string]any)
		if !ok {
			continue
		}
		if cc["ttl"] == "1h" {
			longer = true
		}
		if longer {
			cc["ttl"] = "1h"
		}
	}
}

// chatFromMessages turns Messages events into chat chunks.
type chatFromMessages struct {
	out       *chatChunker
	promptEst uint64
	blocks    map[int]*chatMessagesBlock // open content blocks by index
	usage     chatMessagesUsage
	started   bool // message_start came
	stop      string
}

type chatMessagesBlock struct {
	kind     string // text, thinking, tool, other
	tool     int
	seed     string // a tool_use's input given on its start
	streamed bool   // input_json_delta came
	thinking chatThinkingBlock
}

type chatMessagesUsage struct {
	InputTokens   uint64 `json:"input_tokens"`
	OutputTokens  uint64 `json:"output_tokens"`
	CacheCreation uint64 `json:"cache_creation_input_tokens"`
	CacheRead     uint64 `json:"cache_read_input_tokens"`
}

// merge takes the counts an event reports: Anthropic sends input counts on
// message_start and, from some upstreams, again on message_delta.
func (u *chatMessagesUsage) merge(v *chatMessagesUsage) {
	if v == nil {
		return
	}
	if v.InputTokens > 0 {
		u.InputTokens = v.InputTokens
	}
	if v.OutputTokens > 0 {
		u.OutputTokens = v.OutputTokens
	}
	if v.CacheCreation > 0 {
		u.CacheCreation = v.CacheCreation
	}
	if v.CacheRead > 0 {
		u.CacheRead = v.CacheRead
	}
}

// chat is the usage as chat reports it: the prompt counts cached input and
// cache writes, which Anthropic counts apart.
func (u *chatMessagesUsage) chat() chatUsage {
	return chatUsage{Prompt: u.InputTokens + u.CacheRead + u.CacheCreation, Completion: u.OutputTokens,
		Cached: u.CacheRead, CacheCreation: u.CacheCreation}
}

func (s *chatFromMessages) Event(payload []byte, emit func([]byte) error) error {
	var ev struct {
		Type    string `json:"type"`
		Index   int    `json:"index"`
		Message *struct {
			Usage *chatMessagesUsage `json:"usage"`
		} `json:"message"`
		ContentBlock *struct {
			Type      string          `json:"type"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Text      string          `json:"text"`
			Thinking  string          `json:"thinking"`
			Signature string          `json:"signature"`
			Data      string          `json:"data"`
			Input     json.RawMessage `json:"input"`
		} `json:"content_block"`
		Delta *struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			Thinking    string `json:"thinking"`
			Signature   string `json:"signature"`
			PartialJSON string `json:"partial_json"`
			StopReason  string `json:"stop_reason"`
		} `json:"delta"`
		Usage *chatMessagesUsage `json:"usage"`
		Error *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &ev); err != nil {
		return nil // not ours to judge: skip what cannot be read
	}
	s.out.emit = emit
	if s.out.finished && ev.Type != "error" {
		return nil
	}
	switch ev.Type {
	case "message_start":
		s.started = true
		if ev.Message != nil {
			s.usage.merge(ev.Message.Usage)
		}
	case "content_block_start":
		cb := ev.ContentBlock
		if cb == nil {
			return nil
		}
		switch cb.Type {
		case "text":
			s.blocks[ev.Index] = &chatMessagesBlock{kind: "text"}
			return s.out.content(cb.Text)
		case "thinking", "redacted_thinking":
			s.blocks[ev.Index] = &chatMessagesBlock{kind: "thinking",
				thinking: chatThinkingBlock{Type: cb.Type, Thinking: cb.Thinking, Signature: cb.Signature, Data: cb.Data}}
			return s.out.reasoning(cb.Thinking)
		case "tool_use":
			i, err := s.out.toolStart(cb.ID, cb.Name, "")
			b := &chatMessagesBlock{kind: "tool", tool: i}
			// Some relays give the whole input here and stream none: kept
			// until the block ends, unless deltas come.
			if in := strings.TrimSpace(string(cb.Input)); in != "" && in != "{}" && in != "null" {
				b.seed = in
			}
			s.blocks[ev.Index] = b
			return err
		default: // server tools and their results run upstream: not the client's
			s.blocks[ev.Index] = &chatMessagesBlock{kind: "other"}
		}
	case "content_block_delta":
		b, d := s.blocks[ev.Index], ev.Delta
		if d == nil {
			return nil
		}
		switch d.Type {
		case "text_delta":
			return s.out.content(d.Text)
		case "thinking_delta":
			if b != nil && b.kind == "thinking" {
				b.thinking.Thinking += d.Thinking
			}
			return s.out.reasoning(d.Thinking)
		case "signature_delta":
			if b != nil && b.kind == "thinking" {
				b.thinking.Signature += d.Signature
			}
		case "input_json_delta":
			if b != nil && b.kind == "tool" {
				b.streamed = true
				return s.out.toolArgs(b.tool, d.PartialJSON)
			}
		}
	case "content_block_stop":
		b := s.blocks[ev.Index]
		delete(s.blocks, ev.Index)
		if b == nil {
			return nil
		}
		return s.closeBlock(b)
	case "message_delta":
		if ev.Delta != nil && ev.Delta.StopReason != "" {
			s.stop = ev.Delta.StopReason
		}
		s.usage.merge(ev.Usage)
	case "message_stop":
		if err := s.closeAll(); err != nil {
			return err
		}
		return s.out.finish(chatStopReason(s.stop), s.usage.chat())
	case "error":
		e := &chatUpstreamError{Kind: "error", Message: "the upstream failed"}
		if ev.Error != nil {
			e.Kind, e.Message = chatFirst(ev.Error.Type, e.Kind), ev.Error.Message
		}
		return e
	}
	return nil
}

// closeBlock finishes a content block: a signed or redacted thinking block
// goes out whole as anthropic_thinking (also when its text was omitted, so
// the reasoning is not lost to the next turn), a tool_use without streamed
// input gets the input its start gave.
func (s *chatFromMessages) closeBlock(b *chatMessagesBlock) error {
	switch b.kind {
	case "thinking":
		if b.thinking.Signature != "" || (b.thinking.Type == "redacted_thinking" && b.thinking.Data != "") {
			return s.out.thinking(b.thinking)
		}
	case "tool":
		if !b.streamed && b.seed != "" {
			b.streamed = true
			return s.out.toolArgs(b.tool, b.seed)
		}
	}
	return nil
}

func (s *chatFromMessages) closeAll() error {
	idx := make([]int, 0, len(s.blocks))
	for i := range s.blocks {
		idx = append(idx, i)
	}
	slices.Sort(idx)
	for _, i := range idx {
		b := s.blocks[i]
		delete(s.blocks, i)
		if err := s.closeBlock(b); err != nil {
			return err
		}
	}
	return nil
}

// chatStopReason maps an Anthropic stop_reason to a chat finish_reason.
func chatStopReason(stop string) string {
	switch stop {
	case "max_tokens", "model_context_window_exceeded":
		return "length"
	case "tool_use":
		return "tool_calls"
	case "refusal":
		return "content_filter"
	}
	return "stop" // end_turn, stop_sequence, pause_turn
}

func (s *chatFromMessages) Close(emit func([]byte) error) error {
	s.out.emit = emit
	if s.out.finished {
		return nil
	}
	if err := s.closeAll(); err != nil {
		return err
	}
	if !s.out.emitted {
		return errChatStreamEnded
	}
	// Cut short: closed as if it had stopped here. The prompt is what
	// message_start reported, else an estimate; the output what was sent.
	u := s.usage.chat()
	if u.Prompt == 0 {
		u.Prompt = s.promptEst
	}
	u.Completion = max(u.Completion, s.out.output)
	return s.out.finish(chatStopReason(s.stop), u)
}
