package apicompat

// responses → messages: a Responses request (Codex) for an upstream that
// speaks Anthropic Messages, and the upstream's Messages events back into
// Responses events.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// respThinkingPrefix marks a reasoning item's encrypted_content that carries
// an Anthropic thinking block (its text and signature, or a redacted block's
// data), so the next request can give the block back to the upstream as it
// requires for extended thinking with tools.
const respThinkingPrefix = "sharetoken-thinking-v1:"

type respThinkingBlock struct {
	Type      string `json:"type"`
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	Data      string `json:"data,omitempty"`
}

func respEncodeThinking(b respThinkingBlock) string {
	raw, _ := json.Marshal(b)
	return respThinkingPrefix + base64.StdEncoding.EncodeToString(raw)
}

func respDecodeThinking(enc string) (respThinkingBlock, bool) {
	rest, ok := strings.CutPrefix(enc, respThinkingPrefix)
	if !ok {
		return respThinkingBlock{}, false
	}
	raw, err := base64.StdEncoding.DecodeString(rest)
	if err != nil {
		return respThinkingBlock{}, false
	}
	var b respThinkingBlock
	if json.Unmarshal(raw, &b) != nil {
		return respThinkingBlock{}, false
	}
	switch {
	case b.Type == "thinking" && b.Signature != "":
	case b.Type == "redacted_thinking" && b.Data != "":
	default:
		return respThinkingBlock{}, false
	}
	return b, true
}

// respDefaultMaxTokens is used when a request carries no output limit;
// Messages requires one.
const respDefaultMaxTokens = 8192

type respMsgMessage struct {
	Role    string           `json:"role"`
	Content []map[string]any `json:"content"`
}

func respToMessages(body []byte, opt Options) (*Conversion, error) {
	r, err := respParseRequest(body)
	if err != nil {
		return nil, err
	}

	// system: instructions, then the system/developer messages that open the
	// conversation; later ones stay in place as user turns.
	var system []map[string]any
	if strings.TrimSpace(r.instructions) != "" {
		system = append(system, map[string]any{"type": "text", "text": r.instructions})
	}
	items := r.items
	for len(items) > 0 && items[0].kind == respItemMessage && items[0].role == "system" {
		if t := respJoinText(items[0].parts); strings.TrimSpace(t) != "" {
			system = append(system, map[string]any{"type": "text", "text": t})
		}
		items = items[1:]
	}

	var msgs []respMsgMessage
	add := func(role string, blocks ...map[string]any) {
		if len(blocks) == 0 {
			return
		}
		if n := len(msgs); n > 0 && msgs[n-1].Role == role {
			msgs[n-1].Content = append(msgs[n-1].Content, blocks...)
			return
		}
		msgs = append(msgs, respMsgMessage{Role: role, Content: blocks})
	}
	for _, it := range items {
		switch it.kind {
		case respItemMessage:
			if it.role == "assistant" {
				var blocks []map[string]any
				for _, p := range it.parts {
					if p.kind == "text" && strings.TrimSpace(p.text) != "" {
						blocks = append(blocks, map[string]any{"type": "text", "text": p.text})
					}
				}
				add("assistant", blocks...)
				continue
			}
			add("user", respMessagesBlocks(it.parts)...)
		case respItemReasoning:
			// Only a thinking block this bridge carried can go back; reasoning
			// another backend encrypted, or a bare summary, cannot.
			if b, ok := respDecodeThinking(it.encrypted); ok {
				block := map[string]any{"type": b.Type}
				if b.Type == "thinking" {
					block["thinking"] = b.Thinking
					block["signature"] = b.Signature
				} else {
					block["data"] = b.Data
				}
				add("assistant", block)
			}
		case respItemCall:
			var input any
			if json.Unmarshal([]byte(it.args), &input) != nil {
				input = map[string]any{}
			}
			if _, ok := input.(map[string]any); !ok {
				input = map[string]any{}
			}
			add("assistant", map[string]any{"type": "tool_use", "id": respToolUseID(it.callID), "name": it.name, "input": input})
		case respItemOutput:
			block := map[string]any{"type": "tool_result", "tool_use_id": respToolUseID(it.callID)}
			if it.outMedia {
				// A tool result holds images itself: content parts in order, or
				// (media nested in JSON) the text with a placeholder per image,
				// then the images.
				var content []map[string]any
				if !respHasTextPart(it.outParts) && strings.TrimSpace(it.outText) != "" && it.outText != respMediaMarker {
					content = append(content, map[string]any{"type": "text",
						"text": strings.ReplaceAll(it.outText, respMediaMarker, "[image]")})
				}
				for _, p := range it.outParts {
					switch p.kind {
					case "text":
						if strings.TrimSpace(p.text) != "" {
							content = append(content, map[string]any{"type": "text", "text": p.text})
						}
					case "image":
						if src := respMessagesSource(p.url); src != nil {
							content = append(content, map[string]any{"type": "image", "source": src})
						}
					}
				}
				if len(content) == 0 {
					content = append(content, map[string]any{"type": "text", "text": "(empty)"})
				}
				block["content"] = content
			} else {
				text := it.outText
				if strings.TrimSpace(text) == "" {
					text = "(empty)"
				}
				block["content"] = text
			}
			add("user", block)
		}
	}
	if len(msgs) == 0 {
		return nil, errors.New("apicompat: the request has no message to send")
	}

	out := map[string]any{"model": r.model, "messages": msgs, "stream": true}
	maxTokens := r.maxOutput
	if maxTokens <= 0 {
		maxTokens = respDefaultMaxTokens
	}
	out["max_tokens"] = maxTokens

	// tools
	var tools []map[string]any
	for _, d := range r.tools.decls {
		t := map[string]any{"name": d.name, "input_schema": d.parameters}
		if d.description != "" {
			t["description"] = d.description
		}
		tools = append(tools, t)
	}
	choice, forced := respMessagesToolChoice(r.toolChoice, r.tools)
	if len(tools) > 0 {
		out["tools"] = tools
		if choice != nil && r.parallel != nil && !*r.parallel && choice["type"] != "none" {
			choice["disable_parallel_tool_use"] = true
		}
		if choice == nil && r.parallel != nil && !*r.parallel {
			choice = map[string]any{"type": "auto", "disable_parallel_tool_use": true}
		}
		if choice != nil {
			out["tool_choice"] = choice
		}
	}

	// reasoning effort → output_config.effort, and extended thinking with a
	// budget for the efforts above low.
	thinking := false
	effort := r.effort
	switch effort {
	case "", "none", "minimal":
		effort = ""
	case "xhigh", "max":
		effort = "max"
	}
	if effort != "" {
		out["output_config"] = map[string]any{"effort": effort}
		budget := map[string]int{"medium": 4096, "high": 10240, "max": 32768}[effort]
		if budget >= maxTokens {
			budget = maxTokens / 2
		}
		// Thinking cannot be forced alongside a forced tool choice, and needs
		// the last tool-using assistant turn to start with a thinking block.
		switch {
		case AdaptiveThinking(r.model) && !forced:
			// These models think adaptively, at the effort; they refuse a budget.
			out["thinking"] = map[string]any{"type": "adaptive"}
			thinking = true
		case budget >= 1024 && !forced && respTrailingToolTurnHasThinking(msgs):
			out["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
			thinking = true
		}
	}

	// Structured output: text.format's schema is output_config.format's.
	if schema := respFormatSchema(r.textFormat); schema != nil {
		oc, ok := out["output_config"].(map[string]any)
		if !ok {
			oc = map[string]any{}
			out["output_config"] = oc
		}
		oc["format"] = map[string]any{"type": "json_schema", "schema": schema}
	}

	if !thinking {
		if t := r.temperature; t != nil {
			v := *t
			if v > 1 {
				v = 1
			}
			out["temperature"] = v
		} else if r.topP != nil {
			out["top_p"] = *r.topP
		}
	}

	// Prompt caching: Responses caches by itself, Messages only at marked
	// breakpoints. Mark the tools and system (the stable prefix) and the
	// newest content, so each turn reads what the last one wrote.
	if len(tools) > 0 {
		tools[len(tools)-1]["cache_control"] = map[string]any{"type": "ephemeral"}
	}
	if len(system) > 0 {
		system[len(system)-1]["cache_control"] = map[string]any{"type": "ephemeral"}
		out["system"] = system
	}
	respMarkLastBlock(msgs)

	b, err := respMarshal(out)
	if err != nil {
		return nil, err
	}
	model := opt.Model
	if model == "" {
		model = r.model
	}
	return &Conversion{Body: b, Translator: &respFromMessages{s: newRespStream(model, r.tools), blocks: map[int]*respMsgBlock{}}}, nil
}

// respFormatSchema is the JSON schema text.format asks the answer to follow,
// nil when it asks for none (text, or json_object, which Messages has no
// field for).
func respFormatSchema(raw json.RawMessage) json.RawMessage {
	var f struct {
		Type   string          `json:"type"`
		Schema json.RawMessage `json:"schema"`
	}
	if json.Unmarshal(raw, &f) != nil || f.Type != "json_schema" || len(f.Schema) == 0 || string(f.Schema) == "null" {
		return nil
	}
	return f.Schema
}

func respHasTextPart(parts []respPart) bool {
	for _, p := range parts {
		if p.kind == "text" {
			return true
		}
	}
	return false
}

func respJoinText(parts []respPart) string {
	var texts []string
	for _, p := range parts {
		if p.kind == "text" {
			texts = append(texts, p.text)
		}
	}
	return strings.Join(texts, "\n\n")
}

// respMessagesBlocks turns user content into Messages blocks.
func respMessagesBlocks(parts []respPart) []map[string]any {
	var blocks []map[string]any
	for _, p := range parts {
		switch p.kind {
		case "text":
			if strings.TrimSpace(p.text) != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": p.text})
			}
		case "image":
			if src := respMessagesSource(p.url); src != nil {
				blocks = append(blocks, map[string]any{"type": "image", "source": src})
			}
		case "file":
			var src map[string]any
			if mt, data, ok := respDataURL(p.fileData); ok {
				switch {
				case mt == "application/pdf":
					src = map[string]any{"type": "base64", "media_type": mt, "data": data}
				case strings.HasPrefix(mt, "text/"):
					if raw, err := base64.StdEncoding.DecodeString(data); err == nil {
						src = map[string]any{"type": "text", "media_type": "text/plain", "data": string(raw)}
					}
				}
			} else if strings.HasPrefix(p.fileURL, "http://") || strings.HasPrefix(p.fileURL, "https://") {
				src = map[string]any{"type": "url", "url": p.fileURL}
			}
			if src != nil {
				doc := map[string]any{"type": "document", "source": src}
				if p.fileName != "" {
					doc["title"] = p.fileName
				}
				blocks = append(blocks, doc)
			}
		}
	}
	return blocks
}

// respMessagesSource is the image source for a data: or http(s) URL.
func respMessagesSource(url string) map[string]any {
	if mt, data, ok := respDataURL(url); ok {
		return map[string]any{"type": "base64", "media_type": mt, "data": data}
	}
	if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
		return map[string]any{"type": "url", "url": url}
	}
	return nil
}

// respToolUseID makes a call id valid as a Messages tool_use id.
func respToolUseID(id string) string {
	var b strings.Builder
	for _, r := range id {
		if respNameRune(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "toolu_empty"
	}
	return b.String()
}

// respMessagesToolChoice maps tool_choice; forced reports a choice that
// makes the model call a tool.
func respMessagesToolChoice(raw json.RawMessage, tools *respTools) (map[string]any, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, false
	}
	if s := respString(raw); s != "" {
		switch s {
		case "auto":
			return map[string]any{"type": "auto"}, false
		case "required":
			return map[string]any{"type": "any"}, true
		case "none":
			return map[string]any{"type": "none"}, false
		}
		return nil, false
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
		return nil, false
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
			return map[string]any{"type": "any"}, true
		}
		return map[string]any{"type": "auto"}, false
	default:
		return nil, false // a hosted tool, or a namespace group
	}
	name, ok := tools.toUpstream[target]
	if !ok {
		return nil, false
	}
	return map[string]any{"type": "tool", "name": name}, true
}

// respTrailingToolTurnHasThinking reports whether thinking can be enabled:
// when the conversation ends in a tool loop, the assistant turn that called
// the tools must start with a thinking block.
func respTrailingToolTurnHasThinking(msgs []respMsgMessage) bool {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role == "user" {
			hasResult := false
			for _, b := range m.Content {
				hasResult = hasResult || b["type"] == "tool_result"
			}
			if !hasResult {
				return true // a fresh user turn: no tool loop to continue
			}
			continue
		}
		if len(m.Content) == 0 {
			return true
		}
		hasTool := false
		for _, b := range m.Content {
			hasTool = hasTool || b["type"] == "tool_use"
		}
		if !hasTool {
			return true
		}
		t := m.Content[0]["type"]
		return t == "thinking" || t == "redacted_thinking"
	}
	return true
}

// respMarkLastBlock sets a cache breakpoint on the newest block that takes one.
func respMarkLastBlock(msgs []respMsgMessage) {
	for i := len(msgs) - 1; i >= 0; i-- {
		c := msgs[i].Content
		for j := len(c) - 1; j >= 0; j-- {
			switch c[j]["type"] {
			case "text", "image", "document", "tool_use", "tool_result":
				c[j]["cache_control"] = map[string]any{"type": "ephemeral"}
				return
			}
		}
	}
}

// --- the stream ---

type respMsgBlock struct {
	kind      string // text, thinking, redacted_thinking, tool_use, or "" (ignored)
	call      *respCallState
	seed      string // tool input given whole on content_block_start
	gotDelta  bool
	thinking  strings.Builder
	signature strings.Builder
	data      string
}

type respFromMessages struct {
	s          *respStream
	blocks     map[int]*respMsgBlock
	stopReason string
	stopped    bool

	input, cacheRead, cacheWrite, output int
}

type respMsgEvent struct {
	Type    string `json:"type"`
	Index   *int   `json:"index"`
	Message *struct {
		ID      string            `json:"id"`
		Model   string            `json:"model"`
		Content []json.RawMessage `json:"content"`
		Usage   *respMsgUsage     `json:"usage"`
	} `json:"message"`
	ContentBlock json.RawMessage `json:"content_block"`
	Delta        *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *respMsgUsage `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type respMsgUsage struct {
	InputTokens              *int `json:"input_tokens"`
	OutputTokens             *int `json:"output_tokens"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
}

type respMsgContentBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Signature string          `json:"signature"`
	Data      string          `json:"data"`
}

func (t *respFromMessages) usage(u *respMsgUsage) {
	if u == nil {
		return
	}
	if u.InputTokens != nil && *u.InputTokens > 0 {
		t.input = *u.InputTokens
	}
	if u.CacheReadInputTokens != nil && *u.CacheReadInputTokens > 0 {
		t.cacheRead = *u.CacheReadInputTokens
	}
	if u.CacheCreationInputTokens != nil && *u.CacheCreationInputTokens > 0 {
		t.cacheWrite = *u.CacheCreationInputTokens
	}
	if u.OutputTokens != nil && *u.OutputTokens > 0 {
		t.output = *u.OutputTokens
	}
}

func (t *respFromMessages) Event(payload []byte, emit func([]byte) error) error {
	t.s.emit = emit
	var ev respMsgEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return fmt.Errorf("apicompat: parse messages event: %w", err)
	}
	if t.s.finished && ev.Type != "error" {
		return nil
	}
	switch ev.Type {
	case "error":
		if ev.Error != nil {
			return fmt.Errorf("apicompat: upstream error: %s: %s", ev.Error.Type, ev.Error.Message)
		}
		return fmt.Errorf("apicompat: upstream error: %s", payload)
	case "message_start":
		if ev.Message != nil {
			t.usage(ev.Message.Usage)
			if t.s.model == "" {
				t.s.model = ev.Message.Model
			}
		}
		if err := t.s.start(); err != nil {
			return err
		}
		// Some relays put the content on message_start instead of streaming it.
		if ev.Message != nil {
			for _, raw := range ev.Message.Content {
				if err := t.wholeBlock(raw); err != nil {
					return err
				}
			}
		}
	case "content_block_start":
		idx := 0
		if ev.Index != nil {
			idx = *ev.Index
		}
		return t.blockStart(idx, ev.ContentBlock)
	case "content_block_delta":
		if ev.Delta == nil {
			return nil
		}
		b := t.block(ev.Index)
		if b == nil {
			return nil
		}
		return t.blockDelta(b, ev.Delta.Type, ev.Delta.Text, ev.Delta.Thinking, ev.Delta.Signature, ev.Delta.PartialJSON)
	case "content_block_stop":
		idx := 0
		if ev.Index != nil {
			idx = *ev.Index
		}
		b := t.blocks[idx]
		if b == nil {
			return nil
		}
		delete(t.blocks, idx)
		return t.blockStop(b)
	case "message_delta":
		t.usage(ev.Usage)
		if ev.Delta != nil && ev.Delta.StopReason != "" {
			t.stopReason = ev.Delta.StopReason
		}
	case "message_stop":
		t.stopped = true
		return t.finish()
	}
	return nil // ping and unknown events
}

func (t *respFromMessages) block(index *int) *respMsgBlock {
	idx := 0
	if index != nil {
		idx = *index
	}
	return t.blocks[idx]
}

func (t *respFromMessages) blockStart(idx int, raw json.RawMessage) error {
	var cb respMsgContentBlock
	if json.Unmarshal(raw, &cb) != nil {
		return nil
	}
	if err := t.s.start(); err != nil {
		return err
	}
	b := &respMsgBlock{kind: cb.Type}
	switch cb.Type {
	case "text":
		if err := t.s.closeReasoning(""); err != nil {
			return err
		}
		if err := t.s.openTextPart(); err != nil {
			return err
		}
		if err := t.s.textDelta(cb.Text); err != nil {
			return err
		}
	case "thinking", "redacted_thinking":
		if err := t.s.closeMessage(); err != nil {
			return err
		}
		if err := t.s.closeReasoning(""); err != nil {
			return err
		}
		if err := t.s.openReasoning(); err != nil {
			return err
		}
		b.data = cb.Data
		b.signature.WriteString(cb.Signature)
		b.thinking.WriteString(cb.Thinking)
		if err := t.s.reasoningDelta(cb.Thinking); err != nil {
			return err
		}
	case "tool_use":
		if err := t.s.closeMessage(); err != nil {
			return err
		}
		if err := t.s.closeReasoning(""); err != nil {
			return err
		}
		c, err := t.s.openCall(cb.ID, cb.Name)
		if err != nil {
			return err
		}
		b.call = c
		if s := strings.TrimSpace(string(cb.Input)); s != "" && s != "{}" && s != "null" {
			b.seed = s
		}
	default:
		b.kind = "" // server tools and others have no Responses counterpart here
	}
	t.blocks[idx] = b
	return nil
}

func (t *respFromMessages) blockDelta(b *respMsgBlock, typ, text, thinking, signature, partial string) error {
	switch typ {
	case "text_delta":
		if b.kind == "text" {
			return t.s.textDelta(text)
		}
	case "thinking_delta":
		if b.kind == "thinking" {
			b.thinking.WriteString(thinking)
			return t.s.reasoningDelta(thinking)
		}
	case "signature_delta":
		b.signature.WriteString(signature)
	case "input_json_delta":
		if b.kind == "tool_use" && partial != "" {
			b.gotDelta = true // deltas win over a seed from content_block_start
			return t.s.callArgs(b.call, partial)
		}
	}
	return nil
}

func (t *respFromMessages) blockStop(b *respMsgBlock) error {
	switch b.kind {
	case "text":
		return t.s.closeTextPart()
	case "thinking", "redacted_thinking":
		enc := ""
		if b.kind == "thinking" && b.signature.Len() > 0 {
			enc = respEncodeThinking(respThinkingBlock{Type: "thinking", Thinking: b.thinking.String(), Signature: b.signature.String()})
		} else if b.kind == "redacted_thinking" && b.data != "" {
			enc = respEncodeThinking(respThinkingBlock{Type: "redacted_thinking", Data: b.data})
		}
		return t.s.closeReasoning(enc)
	case "tool_use":
		if !b.gotDelta && b.seed != "" {
			if err := t.s.callArgs(b.call, b.seed); err != nil {
				return err
			}
		}
		if args := b.call.args.String(); args != "" && !json.Valid([]byte(args)) {
			// Cut off, most likely: the stop reason, which comes after the
			// block's end, says whether it is, and the call ends then.
			return nil
		}
		return t.s.closeCall(b.call)
	}
	return nil
}

// wholeBlock handles a complete content block (from message_start).
func (t *respFromMessages) wholeBlock(raw json.RawMessage) error {
	var cb respMsgContentBlock
	if json.Unmarshal(raw, &cb) != nil {
		return nil
	}
	const idx = -1
	if err := t.blockStart(idx, raw); err != nil {
		return err
	}
	b := t.blocks[idx]
	delete(t.blocks, idx)
	if b == nil {
		return nil
	}
	if b.kind == "text" && cb.Text == "" {
		return t.s.closeTextPart()
	}
	return t.blockStop(b)
}

func (t *respFromMessages) finish() error {
	// close blocks the upstream left open
	for idx, b := range t.blocks {
		delete(t.blocks, idx)
		if err := t.blockStop(b); err != nil {
			return err
		}
	}
	reason := ""
	switch t.stopReason {
	case "max_tokens", "model_context_window_exceeded":
		reason = "max_output_tokens"
	case "refusal":
		reason = "content_filter"
	}
	return t.s.finish(reason, respUsage{
		input:      t.input + t.cacheRead + t.cacheWrite,
		cached:     t.cacheRead,
		cacheWrite: t.cacheWrite,
		output:     t.output,
	})
}

func (t *respFromMessages) Close(emit func([]byte) error) error {
	t.s.emit = emit
	if t.s.finished {
		return nil
	}
	if !t.s.produced() && t.stopReason == "" {
		return errors.New("apicompat: the upstream stream ended without a response")
	}
	return t.finish()
}
