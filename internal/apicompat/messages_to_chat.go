package apicompat

// Messages client, chat upstream: the request into a chat completions
// request, the chat chunks back into Messages events.

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
)

// msgChatMsg is a chat message being built.
type msgChatMsg struct {
	Role             string        `json:"role"`
	Content          any           `json:"content,omitempty"` // string or []any parts
	ToolCalls        []msgChatCall `json:"tool_calls,omitempty"`
	ToolCallID       string        `json:"tool_call_id,omitempty"`
	ReasoningContent string        `json:"reasoning_content,omitempty"`
}

type msgChatCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func msgToChat(body []byte, opt Options) (*Conversion, error) {
	r, err := msgParseRequest(body)
	if err != nil {
		return nil, err
	}
	names := msgNewToolNames(r.Tools, r.Messages)
	msgs, err := msgChatMessages(r, names)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"model":          r.Model,
		"messages":       msgs,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}
	if r.MaxTokens > 0 {
		// OpenAI's reasoning models only take max_completion_tokens; other
		// chat upstreams know max_tokens. The limit is the request's (the node
		// computed it from the buyer's reservation): it is not raised.
		if msgReasoningOnly(r.Model) {
			out["max_completion_tokens"] = r.MaxTokens
		} else {
			out["max_tokens"] = r.MaxTokens
		}
	}
	if !msgReasoningOnly(r.Model) {
		if r.Temperature != nil {
			out["temperature"] = *r.Temperature
		}
		if r.TopP != nil {
			out["top_p"] = *r.TopP
		}
	}
	if !msgNoReasoning(r.Model) {
		out["reasoning_effort"] = msgEffort(r)
	}
	if len(r.StopSequences) > 0 {
		stop := r.StopSequences
		if len(stop) > 4 { // OpenAI's limit
			stop = stop[:4]
		}
		out["stop"] = stop
	}
	if schema := msgOutputSchema(r); schema != nil {
		out["response_format"] = map[string]any{"type": "json_schema",
			"json_schema": map[string]any{"name": msgSchemaName, "schema": schema}}
	}
	var tools []any
	declared := map[string]bool{}
	for _, t := range r.Tools {
		if !msgIsClientTool(t) {
			continue
		}
		name := names.toUpstream(t.Name)
		declared[name] = true
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        name,
				"description": t.Description,
				"parameters":  msgToolParameters(t.InputSchema),
				"strict":      false,
			},
		})
	}
	if len(tools) > 0 {
		out["tools"] = tools
		choice, parallel, err := msgToolChoice(r.ToolChoice, names, declared, true)
		if err != nil {
			return nil, err
		}
		if choice != nil {
			out["tool_choice"] = choice
		}
		out["parallel_tool_calls"] = parallel
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	model := opt.Model
	if model == "" {
		model = r.Model
	}
	t := newMsgFromChat(model, names)
	t.w.held = r.held
	return &Conversion{Body: b, Translator: t}, nil
}

func msgChatText(text string) map[string]any { return map[string]any{"type": "text", "text": text} }

// msgChatMedia is an image or document as a chat content part.
func msgChatMedia(b msgContent) (map[string]any, bool) {
	switch b.Type {
	case "image":
		if u := msgImageURL(b); u != "" {
			return map[string]any{"type": "image_url", "image_url": map[string]any{"url": u}}, true
		}
	case "document":
		if t, ok := msgDocumentText(b); ok {
			return msgChatText(t), t != ""
		}
		if b.Source == nil {
			return nil, false
		}
		if b.Source.Type == "url" && b.Source.URL != "" {
			// Chat takes no file URLs.
			return msgChatText("Document: " + b.Source.URL), true
		}
		if u := msgDataURI(b.Source, "application/pdf"); u != "" {
			return map[string]any{"type": "file", "file": map[string]any{"filename": msgDocumentName(b), "file_data": u}}, true
		}
	}
	return nil, false
}

// msgChatContent is user content: a string when it is only text (strict chat
// upstreams reject part arrays), else the parts.
func msgChatContent(parts []any) any {
	var texts []string
	for _, p := range parts {
		m, _ := p.(map[string]any)
		if m["type"] != "text" {
			return parts
		}
		texts = append(texts, m["text"].(string))
	}
	return strings.Join(texts, "\n\n")
}

func msgChatMessages(r *msgRequest, names *msgToolNames) ([]msgChatMsg, error) {
	var out []msgChatMsg
	sys, err := msgSystemTexts(r.System)
	if err != nil {
		return nil, err
	}
	if len(sys) > 0 {
		out = append(out, msgChatMsg{Role: "system", Content: strings.Join(sys, "\n\n")})
	}
	uses, results := msgPairs(r.Messages)
	for _, m := range r.Messages {
		blocks, err := msgBlocks(m.Content)
		if err != nil {
			return nil, err
		}
		switch m.Role {
		case "system":
			var texts []string
			for _, b := range blocks {
				if b.Type == "text" && b.Text != "" {
					texts = append(texts, b.Text)
				}
			}
			if len(texts) > 0 {
				out = append(out, msgChatMsg{Role: "system", Content: strings.Join(texts, "\n\n")})
			}
		case "assistant":
			msg := msgChatMsg{Role: "assistant"}
			var texts, thinking []string
			for _, b := range blocks {
				switch b.Type {
				case "text":
					if b.Text != "" {
						texts = append(texts, b.Text)
					}
				case "thinking":
					if b.Thinking != "" {
						thinking = append(thinking, b.Thinking)
					}
				case "tool_use":
					if !msgTake(uses, b.ID) {
						continue
					}
					var c msgChatCall
					c.ID, c.Type = b.ID, "function"
					c.Function.Name = names.toUpstream(b.Name)
					c.Function.Arguments = msgToolArgs(b.Input)
					msg.ToolCalls = append(msg.ToolCalls, c)
				}
			}
			if len(texts) > 0 {
				msg.Content = strings.Join(texts, "\n\n")
			}
			// Reasoning goes back with the tool calls it led to (DeepSeek's
			// thinking mode requires it there), never on a plain answer.
			if len(msg.ToolCalls) > 0 {
				msg.ReasoningContent = strings.Join(thinking, "\n")
			}
			if msg.Content != nil || len(msg.ToolCalls) > 0 {
				out = append(out, msg)
			}
		default: // user
			var parts, media []any
			for _, b := range blocks {
				if b.Type != "tool_result" || !msgTake(results, b.ToolUseID) {
					continue
				}
				text, extra := msgToolResult(b)
				out = append(out, msgChatMsg{Role: "tool", ToolCallID: b.ToolUseID, Content: text})
				for _, c := range extra {
					if p, ok := msgChatMedia(c); ok {
						media = append(media, p)
					}
				}
			}
			for _, b := range blocks {
				switch b.Type {
				case "text":
					if b.Text != "" {
						parts = append(parts, msgChatText(b.Text))
					}
				case "image", "document":
					if p, ok := msgChatMedia(b); ok {
						parts = append(parts, p)
					}
				case "search_result":
					if t, ok := msgSearchResultText(b); ok {
						parts = append(parts, msgChatText(t))
					}
				}
			}
			parts = append(parts, media...)
			if len(parts) > 0 {
				out = append(out, msgChatMsg{Role: "user", Content: msgChatContent(parts)})
			}
		}
	}
	return msgChatMerge(msgChatOrder(out)), nil
}

// msgChatOrder puts each tool reply right after the assistant message that
// called it: chat upstreams reject any other message in between (a system
// message among the turns can land there). The replies of a call ID used
// again answer its calls in order of appearance; a call without a reply is
// left out, as its reply would be.
func msgChatOrder(msgs []msgChatMsg) []msgChatMsg {
	replies := map[string][]msgChatMsg{}
	for _, m := range msgs {
		if m.Role == "tool" {
			replies[m.ToolCallID] = append(replies[m.ToolCallID], m)
		}
	}
	out := make([]msgChatMsg, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "tool" {
			continue
		}
		var calls []msgChatCall
		var answers []msgChatMsg
		for _, c := range m.ToolCalls {
			q := replies[c.ID]
			if len(q) == 0 {
				continue
			}
			replies[c.ID] = q[1:]
			calls, answers = append(calls, c), append(answers, q[0])
		}
		if len(m.ToolCalls) > 0 && len(calls) == 0 {
			m.ReasoningContent = ""
			if m.Content == nil {
				continue
			}
		}
		m.ToolCalls = calls
		out = append(out, m)
		out = append(out, answers...)
	}
	return out
}

// msgChatMerge joins adjacent user messages, and an assistant message into
// the one before it when that one called no tool.
func msgChatMerge(msgs []msgChatMsg) []msgChatMsg {
	var out []msgChatMsg
	for _, m := range msgs {
		if n := len(out); n > 0 {
			prev := &out[n-1]
			if m.Role == prev.Role && (m.Role == "user" || (m.Role == "assistant" && len(prev.ToolCalls) == 0)) {
				prev.Content = msgChatJoin(prev.Content, m.Content)
				prev.ToolCalls = m.ToolCalls
				prev.ReasoningContent = m.ReasoningContent
				continue
			}
		}
		out = append(out, m)
	}
	return out
}

func msgChatJoin(a, b any) any {
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}
	as, aok := a.(string)
	bs, bok := b.(string)
	if aok && bok {
		return as + "\n\n" + bs
	}
	parts := func(v any) []any {
		if s, ok := v.(string); ok {
			return []any{msgChatText(s)}
		}
		return v.([]any)
	}
	return append(parts(a), parts(b)...)
}

// msgFromChat translates chat chunks into Messages events.
type msgFromChat struct {
	w     *msgWriter
	names *msgToolNames

	seq      int
	thinking *msgBlock
	text     *msgBlock
	tools    map[int]*msgChatTool
	byID     map[string]int
	slots    map[int]int // the upstream's tool call index: the call it means now
	last     int         // index of the last tool call

	finish    string
	usage     msgUsage
	haveUsage bool
	content   bool
	refusal   bool
}

type msgChatTool struct {
	block *msgBlock
	id    string // the upstream's call ID, empty until it gave one
	args  strings.Builder
}

func newMsgFromChat(model string, names *msgToolNames) *msgFromChat {
	return &msgFromChat{w: newMsgWriter(model), names: names, tools: map[int]*msgChatTool{}, byID: map[string]int{}, slots: map[int]int{}}
}

// slot is the call a fragment with the upstream's index belongs to. Some
// model servers give every parallel call the same index and tell them apart
// by ID only: another ID at a known index is another call.
func (t *msgFromChat) slot(index int, id string) int {
	s, known := t.slots[index]
	if known {
		if tool := t.tools[s]; tool == nil || id == "" || tool.id == "" || tool.id == id {
			return s
		}
	}
	s = index
	for t.tools[s] != nil {
		s++
	}
	t.slots[index] = s
	return s
}

type msgChatChunk struct {
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content          json.RawMessage `json:"content"`
			ReasoningContent *string         `json:"reasoning_content"`
			Reasoning        json.RawMessage `json:"reasoning"`
			Refusal          *string         `json:"refusal"`
			ToolCalls        []struct {
				Index    *int   `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string    `json:"name"`
					Arguments looseArgs `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage json.RawMessage `json:"usage"`
	Error json.RawMessage `json:"error"`
}

func (t *msgFromChat) Event(payload []byte, emit func([]byte) error) error {
	if t.w.stopped {
		return nil
	}
	payload = bytes.TrimSpace(payload)
	if string(payload) == "[DONE]" {
		if t.content || t.finish != "" {
			t.end()
		}
		return t.w.flush(emit)
	}
	var c msgChatChunk
	if err := json.Unmarshal(payload, &c); err != nil {
		// A chunk that cannot be read is skipped (as sub2api does): one odd
		// chunk does not take the rest of the answer with it.
		return nil
	}
	if m := msgErrorMessage(c.Error); m != "" {
		return errors.New("apicompat: upstream: " + m)
	}
	if u, ok := msgUsageFromOpenAI(c.Usage); ok {
		t.usage, t.haveUsage = u, true
	}
	for _, ch := range c.Choices {
		if ch.Index != 0 {
			continue // one answer is asked for
		}
		d := ch.Delta
		text, reasoning := looseContent(d.Content)
		if d.ReasoningContent != nil && *d.ReasoningContent != "" {
			reasoning = *d.ReasoningContent
		} else if r := msgDeltaString(d.Reasoning); r != "" {
			reasoning = r
		}
		if reasoning != "" {
			if t.thinking == nil {
				t.thinking = t.w.open("r"+t.nextSeq(), msgKindThinking)
			}
			t.content = true
			t.w.add(t.thinking, reasoning)
		}
		refusal := ""
		if d.Refusal != nil {
			refusal = *d.Refusal
		}
		for i, s := range []string{text, refusal} {
			if s == "" {
				continue
			}
			t.endThinking()
			if t.text == nil {
				t.text = t.w.open("x"+t.nextSeq(), msgKindText)
			}
			if i == 1 {
				t.refusal = true
			}
			t.content = true
			t.w.add(t.text, s)
		}
		for i := range d.ToolCalls {
			tc := &d.ToolCalls[i]
			// Without an index a call is known by its id; a fragment with
			// neither continues the last call.
			idx := t.last
			switch {
			case tc.Index != nil:
				idx = t.slot(*tc.Index, tc.ID)
			case tc.ID != "":
				if i, ok := t.byID[tc.ID]; ok {
					idx = i
				} else {
					idx = len(t.tools)
					for t.tools[idx] != nil {
						idx++
					}
				}
			}
			t.last = idx
			t.toolCall(idx, tc.ID, tc.Function.Name, string(tc.Function.Arguments))
		}
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			t.finish = *ch.FinishReason
			if t.cut() {
				// A tool call the client has not seen yet with half its
				// input is not sent.
				t.w.dropCut()
			}
			t.w.finishAll()
			t.thinking, t.text = nil, nil
		}
	}
	if t.finish != "" && t.haveUsage {
		t.end()
	}
	return t.w.flush(emit)
}

func (t *msgFromChat) nextSeq() string {
	t.seq++
	return strconv.Itoa(t.seq)
}

func (t *msgFromChat) endThinking() {
	if t.thinking != nil {
		t.w.finish(t.thinking)
		t.thinking = nil
	}
}

func (t *msgFromChat) toolCall(idx int, id, name, args string) {
	tool := t.tools[idx]
	if tool == nil {
		// A new call ends the thinking and the text before it, and the calls
		// before it whose arguments are complete.
		t.endThinking()
		if t.text != nil {
			t.w.finish(t.text)
			t.text = nil
		}
		for _, o := range t.tools {
			if json.Valid([]byte(o.args.String())) {
				t.w.finish(o.block)
			}
		}
		tool = &msgChatTool{id: id}
		if id == "" {
			id = "call_" + msgRandomHex(12)
		}
		b := t.w.open("c"+t.nextSeq(), msgKindToolUse)
		b.id, b.named = id, false
		tool.block = b
		t.tools[idx] = tool
		t.byID[id] = idx
		t.content = true
	}
	b := tool.block
	if id != "" && tool.id == "" {
		// The ID came after the call's first fragment.
		tool.id = id
		t.byID[id] = idx
		if !b.live {
			b.id = id
		}
	}
	if name != "" && !b.named {
		b.name = t.names.toClient(name)
		b.named = true
		b.hold = b.name == "Read"
	}
	tool.args.WriteString(args)
	t.w.add(b, args)
	t.w.pump()
}

func (t *msgFromChat) end() {
	reason := "end_turn"
	switch t.finish {
	case "length":
		reason = "max_tokens"
	case "content_filter":
		reason = "refusal"
	}
	// An answer that was cut off does not end in tool_use: its last call may
	// have half its input.
	if !t.cut() {
		if t.w.toolUse {
			reason = "tool_use"
		} else if t.refusal {
			reason = "refusal"
		}
	}
	t.w.stop(reason, t.usage)
}

// cut reports an answer the output limit or a content filter ended.
func (t *msgFromChat) cut() bool { return t.finish == "length" || t.finish == "content_filter" }

func (t *msgFromChat) Close(emit func([]byte) error) error {
	if t.w.stopped {
		return nil
	}
	if !t.content && t.finish == "" {
		return errors.New("apicompat: upstream stream ended without a response")
	}
	t.end()
	return t.w.flush(emit)
}
