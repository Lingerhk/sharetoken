package apicompat

// Messages client, Responses upstream: the request into a Responses request,
// the Responses events back into Messages events.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

func init() {
	register(Messages, Responses, msgToResponses)
	register(Messages, Chat, msgToChat)
	registerAggregator(Messages, msgAggregate)
}

// msgMinOutputTokens is the least max_output_tokens the Responses API takes.
// A limit is raised to it and no further: the node computed the request's
// from the buyer's reservation.
const msgMinOutputTokens = 16

func msgToResponses(body []byte, opt Options) (*Conversion, error) {
	r, err := msgParseRequest(body)
	if err != nil {
		return nil, err
	}
	names := msgNewToolNames(r.Tools, r.Messages)
	input, err := msgResponsesInput(r, names)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"model":  r.Model,
		"input":  input,
		"stream": true,
		"store":  false,
	}
	if !msgNoReasoning(r.Model) {
		effort := msgEffort(r)
		reasoning := map[string]any{"effort": effort}
		if effort != "none" {
			reasoning["summary"] = "auto"
		}
		out["reasoning"] = reasoning
		out["include"] = []string{"reasoning.encrypted_content"}
	}
	if r.MaxTokens > 0 {
		out["max_output_tokens"] = max(r.MaxTokens, msgMinOutputTokens)
	}
	// The Responses API has no stop parameter: the request's stop_sequences
	// are not sent, and the answer does not end at them.
	if schema := msgOutputSchema(r); schema != nil {
		// strict:false: the Responses API is strict by default, which wants
		// every property in required and additionalProperties:false; an
		// Anthropic output schema needs neither, and would be refused.
		out["text"] = map[string]any{"format": map[string]any{"type": "json_schema", "name": msgSchemaName, "schema": schema, "strict": false}}
	}
	if !msgReasoningOnly(r.Model) {
		if r.Temperature != nil {
			out["temperature"] = *r.Temperature
		}
		if r.TopP != nil {
			out["top_p"] = *r.TopP
		}
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
			"type":        "function",
			"name":        name,
			"description": t.Description,
			"parameters":  msgToolParameters(t.InputSchema),
			"strict":      false,
		})
	}
	if len(tools) > 0 {
		out["tools"] = tools
		choice, parallel, err := msgToolChoice(r.ToolChoice, names, declared, false)
		if err != nil {
			return nil, err
		}
		if choice != nil {
			out["tool_choice"] = choice
		}
		out["parallel_tool_calls"] = parallel
	}
	if key := msgPromptCacheKey(r); key != "" {
		out["prompt_cache_key"] = key
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	model := opt.Model
	if model == "" {
		model = r.Model
	}
	t := newMsgFromResponses(model, names)
	t.w.held = r.held
	return &Conversion{Body: b, Translator: t}, nil
}

// msgToolChoice maps tool_choice: auto → auto, any → required, none → none,
// tool → that function (only a declared one: upstreams reject others). The
// second result is parallel_tool_calls.
func msgToolChoice(raw json.RawMessage, names *msgToolNames, declared map[string]bool, chat bool) (any, bool, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, true, nil
	}
	var tc struct {
		Type                   string `json:"type"`
		Name                   string `json:"name"`
		DisableParallelToolUse bool   `json:"disable_parallel_tool_use"`
	}
	if err := json.Unmarshal(raw, &tc); err != nil {
		return nil, true, fmt.Errorf("apicompat: tool_choice: %w", err)
	}
	parallel := !tc.DisableParallelToolUse
	switch tc.Type {
	case "auto":
		return "auto", parallel, nil
	case "any":
		return "required", parallel, nil
	case "none":
		return "none", parallel, nil
	case "tool":
		name := names.toUpstream(tc.Name)
		if !declared[name] {
			return nil, parallel, nil
		}
		if chat {
			return map[string]any{"type": "function", "function": map[string]string{"name": name}}, parallel, nil
		}
		return map[string]any{"type": "function", "name": name}, parallel, nil
	}
	return nil, parallel, nil
}

func msgInputText(text string) map[string]any {
	return map[string]any{"type": "input_text", "text": text}
}

// msgResponsesMedia is an image or file document as a Responses input part.
func msgResponsesMedia(b msgContent) (map[string]any, bool) {
	switch b.Type {
	case "image":
		if u := msgImageURL(b); u != "" {
			return map[string]any{"type": "input_image", "image_url": u}, true
		}
	case "document":
		if t, ok := msgDocumentText(b); ok {
			return msgInputText(t), t != ""
		}
		if b.Source == nil {
			return nil, false
		}
		if b.Source.Type == "url" && b.Source.URL != "" {
			return map[string]any{"type": "input_file", "file_url": b.Source.URL}, true
		}
		if u := msgDataURI(b.Source, "application/pdf"); u != "" {
			return map[string]any{"type": "input_file", "filename": msgDocumentName(b), "file_data": u}, true
		}
	}
	return nil, false
}

func msgResponsesInput(r *msgRequest, names *msgToolNames) ([]any, error) {
	var items []any
	developer := func(texts []string) {
		var parts []any
		for _, t := range texts {
			if t != "" {
				parts = append(parts, msgInputText(t))
			}
		}
		if len(parts) > 0 {
			items = append(items, map[string]any{"type": "message", "role": "developer", "content": parts})
		}
	}
	sys, err := msgSystemTexts(r.System)
	if err != nil {
		return nil, err
	}
	developer(sys)

	uses, results := msgPairs(r.Messages)
	for _, m := range r.Messages {
		blocks, err := msgBlocks(m.Content)
		if err != nil {
			return nil, err
		}
		switch m.Role {
		case "system":
			// Newer Claude Code puts system messages among the turns.
			var texts []string
			for _, b := range blocks {
				if b.Type == "text" {
					texts = append(texts, b.Text)
				}
			}
			developer(texts)
		case "assistant":
			var text []string
			flush := func() {
				if len(text) > 0 {
					items = append(items, map[string]any{
						"type": "message", "role": "assistant",
						"content": []any{map[string]any{"type": "output_text", "text": strings.Join(text, "\n\n")}},
					})
					text = nil
				}
			}
			for _, b := range blocks {
				switch b.Type {
				case "text":
					if b.Text != "" {
						text = append(text, b.Text)
					}
				case "thinking":
					// Only reasoning this package got from a Responses
					// upstream goes back; other signatures mean nothing to it.
					enc, ok := strings.CutPrefix(b.Signature, msgSignaturePrefix)
					if !ok || enc == "" {
						continue
					}
					flush()
					summary := []any{}
					if b.Thinking != "" {
						summary = append(summary, map[string]any{"type": "summary_text", "text": b.Thinking})
					}
					items = append(items, map[string]any{"type": "reasoning", "summary": summary, "encrypted_content": enc})
				case "tool_use":
					if !msgTake(uses, b.ID) {
						continue
					}
					flush()
					items = append(items, map[string]any{
						"type": "function_call", "call_id": b.ID,
						"name": names.toUpstream(b.Name), "arguments": msgToolArgs(b.Input),
					})
				}
			}
			flush()
		default: // user
			var parts, media []any
			for _, b := range blocks {
				if b.Type != "tool_result" || !msgTake(results, b.ToolUseID) {
					continue
				}
				text, extra := msgToolResult(b)
				items = append(items, map[string]any{"type": "function_call_output", "call_id": b.ToolUseID, "output": text})
				for _, c := range extra {
					if p, ok := msgResponsesMedia(c); ok {
						media = append(media, p)
					}
				}
			}
			for _, b := range blocks {
				switch b.Type {
				case "text":
					if b.Text != "" {
						parts = append(parts, msgInputText(b.Text))
					}
				case "image", "document":
					if p, ok := msgResponsesMedia(b); ok {
						parts = append(parts, p)
					}
				case "search_result":
					if t, ok := msgSearchResultText(b); ok {
						parts = append(parts, msgInputText(t))
					}
				}
			}
			parts = append(parts, media...)
			if len(parts) > 0 {
				items = append(items, map[string]any{"type": "message", "role": "user", "content": parts})
			}
		}
	}
	if items == nil {
		items = []any{}
	}
	return items, nil
}

// msgFromResponses translates a Responses stream into Messages events.
type msgFromResponses struct {
	w     *msgWriter
	names *msgToolNames
	items map[int]*msgRespItem // by output_index

	text     map[string]*strings.Builder // text delivered, by part key
	textSent bool                        // any text reached the client
	refusal  bool
	tools    map[string]bool // call_ids seen
	thought  bool            // a reasoning item was seen
	usage    msgUsage
	content  bool // the upstream produced something
}

type msgRespItem struct {
	kind    string // function_call, reasoning, message
	callID  string
	name    string
	args    strings.Builder // arguments that came before the item did
	argsIn  bool            // argument deltas arrived
	summary int             // last summary_index
	said    bool            // reasoning text was added
	closed  bool            // its block was finished
}

func newMsgFromResponses(model string, names *msgToolNames) *msgFromResponses {
	return &msgFromResponses{
		w:     newMsgWriter(model),
		names: names,
		items: map[int]*msgRespItem{},
		text:  map[string]*strings.Builder{},
		tools: map[string]bool{},
	}
}

// msgRespEvent is the part of a Responses event the translation reads.
type msgRespEvent struct {
	Type         string          `json:"type"`
	OutputIndex  int             `json:"output_index"`
	ContentIndex int             `json:"content_index"`
	SummaryIndex int             `json:"summary_index"`
	Delta        json.RawMessage `json:"delta"`
	Text         string          `json:"text"`
	Refusal      string          `json:"refusal"`
	Arguments    string          `json:"arguments"`
	Item         *msgRespOutput  `json:"item"`
	Part         *msgRespPart    `json:"part"`
	Response     *msgRespBody    `json:"response"`
	Usage        json.RawMessage `json:"usage"`
	// error event
	Code    json.RawMessage `json:"code"`
	Message string          `json:"message"`
	Error   json.RawMessage `json:"error"`
}

type msgRespBody struct {
	ID                string           `json:"id"`
	Model             string           `json:"model"`
	Status            string           `json:"status"`
	Output            []msgRespOutput  `json:"output"`
	Usage             json.RawMessage  `json:"usage"`
	Error             json.RawMessage  `json:"error"`
	IncompleteDetails *json.RawMessage `json:"incomplete_details"`
}

type msgRespOutput struct {
	Type             string        `json:"type"`
	ID               string        `json:"id"`
	CallID           string        `json:"call_id"`
	Name             string        `json:"name"`
	Arguments        string        `json:"arguments"`
	Content          []msgRespPart `json:"content"`
	Summary          []msgRespPart `json:"summary"`
	EncryptedContent string        `json:"encrypted_content"`
}

type msgRespPart struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Refusal string `json:"refusal"`
}

func msgDeltaString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

func (t *msgFromResponses) Event(payload []byte, emit func([]byte) error) error {
	if t.w.stopped {
		return nil
	}
	var e msgRespEvent
	if err := json.Unmarshal(payload, &e); err != nil {
		return nil // skipped, as in msgFromChat
	}
	if err := t.event(&e); err != nil {
		return err
	}
	return t.w.flush(emit)
}

func (t *msgFromResponses) itemKey(i int) string { return "o" + strconv.Itoa(i) }

func (t *msgFromResponses) partKey(o, c int) string {
	return "t" + strconv.Itoa(o) + "." + strconv.Itoa(c)
}

func (t *msgFromResponses) item(i int) *msgRespItem {
	it := t.items[i]
	if it == nil {
		it = &msgRespItem{}
		t.items[i] = it
	}
	return it
}

func (t *msgFromResponses) event(e *msgRespEvent) error {
	switch e.Type {
	case "response.created", "response.in_progress", "response.queued":
		// message_start waits for the first content (msgWriter.start): a
		// stream that fails before any leaves the client untouched.
		if e.Response != nil && t.w.model == "" {
			t.w.model = e.Response.Model
		}

	case "response.output_item.added":
		if e.Item != nil {
			t.itemAdded(e.OutputIndex, e.Item)
		}

	case "response.output_text.delta", "response.refusal.delta":
		t.addText(e.OutputIndex, e.ContentIndex, msgDeltaString(e.Delta), e.Type == "response.refusal.delta")

	case "response.output_text.done", "response.refusal.done":
		text, refusal := e.Text, e.Type == "response.refusal.done"
		if refusal {
			text = e.Refusal
		}
		t.recoverText(e.OutputIndex, e.ContentIndex, text, refusal)

	case "response.content_part.done":
		if e.Part != nil {
			switch e.Part.Type {
			case "output_text":
				t.recoverText(e.OutputIndex, e.ContentIndex, e.Part.Text, false)
			case "refusal":
				t.recoverText(e.OutputIndex, e.ContentIndex, e.Part.Refusal, true)
			}
		}

	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		t.addThinking(e.OutputIndex, e.SummaryIndex, msgDeltaString(e.Delta))

	case "response.reasoning_summary_text.done", "response.reasoning_text.done":
		// Text only on the done event: keep it unless deltas already gave it.
		if it := t.item(e.OutputIndex); !it.said && !it.closed {
			t.addThinking(e.OutputIndex, e.SummaryIndex, e.Text)
		}

	case "response.function_call_arguments.delta":
		t.addArgs(e.OutputIndex, msgDeltaString(e.Delta))

	case "response.function_call_arguments.done":
		it := t.item(e.OutputIndex)
		if it.closed {
			break
		}
		if !it.argsIn {
			t.addArgs(e.OutputIndex, e.Arguments)
		}
		if b := t.w.block(t.itemKey(e.OutputIndex)); b != nil {
			it.closed = true
			t.w.finish(b)
		}

	case "response.output_item.done":
		if e.Item != nil {
			t.itemDone(e.OutputIndex, e.Item)
		}

	case "response.completed", "response.done", "response.incomplete":
		return t.terminal(e)

	case "response.failed":
		msg := "response failed"
		if e.Response != nil {
			if m := msgErrorMessage(e.Response.Error); m != "" {
				msg = m
			}
		}
		return errors.New("apicompat: upstream: " + msg)

	case "error":
		msg := e.Message
		if m := msgErrorMessage(e.Error); m != "" {
			msg = m
		}
		if msg == "" {
			msg = "error event"
		}
		return errors.New("apicompat: upstream: " + msg)
	}
	return nil
}

// msgErrorMessage reads an error object's message (or a string error).
func msgErrorMessage(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var e struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
		Type    string `json:"type"`
	}
	if json.Unmarshal(raw, &e) != nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Type != "" {
		return e.Type
	}
	if e.Code != nil {
		return fmt.Sprint(e.Code)
	}
	return string(raw)
}

func (t *msgFromResponses) openTool(i int, callID, name string) *msgBlock {
	key := t.itemKey(i)
	if b := t.w.block(key); b != nil {
		return b
	}
	b := t.w.open(key, msgKindToolUse)
	if callID == "" {
		callID = "call_" + msgRandomHex(12)
	}
	b.id = callID
	b.name = t.names.toClient(name)
	b.hold = b.name == "Read"
	t.tools[callID] = true
	t.content = true
	return b
}

func (t *msgFromResponses) openThinking(i int) *msgBlock {
	t.thought = true
	t.content = true
	return t.w.open(t.itemKey(i), msgKindThinking)
}

func (t *msgFromResponses) itemAdded(i int, item *msgRespOutput) {
	// Items come one after another: a reasoning item still empty when the
	// next one starts would hold everything behind it, so it ends. Should
	// its done event bring something after all, that gets a block of its own.
	for j, other := range t.items {
		if b := t.w.block(t.itemKey(j)); j != i && other.kind == "reasoning" && b != nil && b.all.Len() == 0 && b.sig == "" {
			t.w.finish(b)
		}
	}
	it := t.item(i)
	it.kind = item.Type
	switch item.Type {
	case "function_call":
		it.callID, it.name = item.CallID, item.Name
		b := t.openTool(i, item.CallID, item.Name)
		if pending := it.args.String(); pending != "" {
			it.args.Reset()
			t.w.add(b, pending)
		}
	case "reasoning":
		b := t.openThinking(i)
		if item.EncryptedContent != "" {
			b.sig = msgSignaturePrefix + item.EncryptedContent
		}
	}
}

func (t *msgFromResponses) addArgs(i int, s string) {
	if s == "" {
		return
	}
	it := t.item(i)
	it.argsIn = true
	if b := t.w.block(t.itemKey(i)); b != nil && b.kind == msgKindToolUse {
		t.w.add(b, s)
		return
	}
	// No item announced the call yet: keep the arguments for when one does.
	it.args.WriteString(s)
}

func (t *msgFromResponses) addThinking(i, summary int, s string) {
	if s == "" {
		return
	}
	it := t.item(i)
	b := t.w.block(t.itemKey(i))
	if b == nil || b.kind != msgKindThinking {
		b = t.openThinking(i)
	}
	if summary != it.summary && b.all.Len() > 0 {
		// Summary parts are paragraphs.
		t.w.add(b, "\n\n")
	}
	it.summary = summary
	it.said = true
	t.w.add(b, s)
}

func (t *msgFromResponses) addText(o, c int, s string, refusal bool) {
	if s == "" {
		return
	}
	key := t.partKey(o, c)
	b := t.w.block(key)
	if b == nil {
		b = t.w.open(key, msgKindText)
	}
	sent := t.text[key]
	if sent == nil {
		sent = &strings.Builder{}
		t.text[key] = sent
	}
	sent.WriteString(s)
	t.textSent = true
	t.content = true
	if refusal {
		t.refusal = true
	}
	t.w.add(b, s)
}

// recoverText takes a part's whole text from a done event: what the deltas
// did not deliver is added, and the part is finished. Text that is not a
// continuation of what was sent cannot be taken back, so it is left alone, as
// is a payload that matches no part when other text was sent (it may be text
// the client already has, under other indexes).
func (t *msgFromResponses) recoverText(o, c int, text string, refusal bool) {
	key := t.partKey(o, c)
	sent, known := t.text[key]
	switch {
	case !known && t.textSent:
	case !known:
		t.addText(o, c, text, refusal)
	default:
		if have := sent.String(); len(text) > len(have) && strings.HasPrefix(text, have) {
			t.addText(o, c, text[len(have):], refusal)
		}
	}
	t.w.finish(t.w.block(key))
}

func (t *msgFromResponses) itemDone(i int, item *msgRespOutput) {
	it := t.item(i)
	if it.closed {
		return
	}
	switch item.Type {
	case "function_call":
		it.closed = true
		b := t.w.block(t.itemKey(i))
		if b == nil {
			b = t.openTool(i, item.CallID, item.Name)
			if pending := it.args.String(); pending != "" {
				it.args.Reset()
				t.w.add(b, pending)
			}
		}
		if !it.argsIn {
			it.argsIn = true
			t.w.add(b, item.Arguments)
		}
		t.w.finish(b)
	case "reasoning":
		it.closed = true
		b := t.w.block(t.itemKey(i))
		if b == nil {
			b = t.openThinking(i)
		}
		if item.EncryptedContent != "" {
			b.sig = msgSignaturePrefix + item.EncryptedContent
		}
		if !it.said {
			t.addThinking(i, 0, msgReasoningText(item))
		}
		t.w.finish(b)
	case "message":
		for c, p := range item.Content {
			switch p.Type {
			case "output_text":
				t.recoverText(i, c, p.Text, false)
			case "refusal":
				t.recoverText(i, c, p.Refusal, true)
			}
		}
		// Parts the done item does not list are finished too.
		prefix := "t" + strconv.Itoa(i) + "."
		for _, b := range append([]*msgBlock(nil), t.w.queue...) {
			if strings.HasPrefix(b.key, prefix) {
				t.w.finish(b)
			}
		}
	}
}

// msgReasoningText is a reasoning item's summary, else its reasoning text.
func msgReasoningText(item *msgRespOutput) string {
	var parts []string
	for _, p := range item.Summary {
		if p.Text != "" {
			parts = append(parts, p.Text)
		}
	}
	if len(parts) == 0 {
		for _, p := range item.Content {
			if p.Text != "" {
				parts = append(parts, p.Text)
			}
		}
	}
	return strings.Join(parts, "\n\n")
}

func (t *msgFromResponses) terminal(e *msgRespEvent) error {
	if u, ok := msgUsageFromOpenAI(e.Usage); ok {
		t.usage = u
	}
	status, incomplete := "", ""
	if r := e.Response; r != nil {
		status = r.Status
		if u, ok := msgUsageFromOpenAI(r.Usage); ok {
			t.usage = u
		}
		if r.IncompleteDetails != nil {
			var d struct {
				Reason string `json:"reason"`
			}
			_ = json.Unmarshal(*r.IncompleteDetails, &d)
			incomplete = d.Reason
		}
		if incomplete == "max_output_tokens" || incomplete == "content_filter" {
			// Cut off: a tool call the client has not seen yet with half its
			// input is not sent.
			t.w.dropCut()
		}
		t.recoverOutput(r.Output)
	}
	if e.Type == "response.incomplete" && status == "" {
		status = "incomplete"
	}
	reason := "end_turn"
	switch {
	case status == "incomplete" && incomplete == "max_output_tokens":
		reason = "max_tokens"
	case status == "incomplete" && incomplete == "content_filter":
		reason = "refusal"
	case t.w.toolUse:
		reason = "tool_use"
	case t.refusal:
		reason = "refusal"
	}
	t.w.stop(reason, t.usage)
	return nil
}

// recoverOutput takes from the terminal response's output what the stream
// never delivered: text when no text was sent, tool calls not seen, reasoning
// when none was seen. The terminal output and the streamed items share no
// reliable identity besides call_id, so text is only taken when none was sent
// at all: repeating an answer is worse than a partial one.
func (t *msgFromResponses) recoverOutput(output []msgRespOutput) {
	textSent, thought := t.textSent, t.thought
	base := 1 << 20 // keys apart from the streamed ones
	for i := range output {
		item := &output[i]
		switch item.Type {
		case "message":
			if textSent {
				continue
			}
			for c, p := range item.Content {
				switch p.Type {
				case "output_text":
					t.addText(base+i, c, p.Text, false)
				case "refusal":
					t.addText(base+i, c, p.Refusal, true)
				}
			}
		case "function_call":
			if item.CallID == "" || t.tools[item.CallID] {
				continue
			}
			b := t.openTool(base+i, item.CallID, item.Name)
			t.w.add(b, item.Arguments)
		case "reasoning":
			if thought {
				continue
			}
			text := msgReasoningText(item)
			if text == "" && item.EncryptedContent == "" {
				continue
			}
			b := t.openThinking(base + i)
			if item.EncryptedContent != "" {
				b.sig = msgSignaturePrefix + item.EncryptedContent
			}
			t.w.add(b, text)
		}
		t.w.finishAll()
	}
}

func (t *msgFromResponses) Close(emit func([]byte) error) error {
	if t.w.stopped {
		return nil
	}
	if !t.content {
		return errors.New("apicompat: upstream stream ended without a response")
	}
	reason := "end_turn"
	if t.w.toolUse {
		reason = "tool_use"
	}
	t.w.stop(reason, t.usage)
	return t.w.flush(emit)
}
