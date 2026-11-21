package apicompat

// The Responses events a converted stream sends the client, built the same
// way for a Messages and a chat upstream: response.created and
// response.in_progress, one output item at a time (or several, for parallel
// chat tool calls) with its full added/delta/done lifecycle, and a terminal
// response.completed or response.incomplete carrying the whole response
// with its output and usage. Every event has an increasing sequence_number,
// and every field Codex reads is present even when empty.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

func init() {
	register(Responses, Messages, respToMessages)
	register(Responses, Chat, respToChat)
	registerAggregator(Responses, respAggregate)
}

// respUsage is the usage a stream reports, in Responses terms: input counts
// every input token, cached ones included.
type respUsage struct {
	input, cached, cacheWrite, output, reasoning, total int
}

func (u respUsage) wire() map[string]any {
	total := u.total
	if total == 0 {
		total = u.input + u.output
	}
	in := map[string]any{"cached_tokens": u.cached}
	m := map[string]any{
		"input_tokens":          u.input,
		"input_tokens_details":  in,
		"output_tokens":         u.output,
		"output_tokens_details": map[string]any{"reasoning_tokens": u.reasoning},
		"total_tokens":          total,
	}
	if u.cacheWrite > 0 {
		// Cache writes: sharetoken's field, and the top-level alias sub2api uses.
		in["cache_creation_tokens"] = u.cacheWrite
		m["cache_creation_input_tokens"] = u.cacheWrite
	}
	return m
}

// respStream emits the client's events.
type respStream struct {
	emit    func([]byte) error
	tools   *respTools
	id      string
	model   string
	created int64
	seq     int

	started, finished bool
	cut               bool // the answer ends incomplete: an open call's arguments may be cut off
	nextIndex         int
	done              map[int]map[string]any // finished items by output_index

	msg       *respMsgState
	reasoning *respReasoningState
	calls     []*respCallState // open calls

	held []map[string]any // events waiting on a message's phase (responses_phase.go)
}

type respMsgState struct {
	index    int
	id       string
	parts    []string
	cur      strings.Builder
	partOpen bool
}

type respReasoningState struct {
	index     int
	id        string
	text      strings.Builder
	partOpen  bool
	encrypted string
}

type respCallState struct {
	index     int
	id        string
	callID    string
	name      string // as the upstream called it
	client    respClientTool
	args      strings.Builder
	announced bool
}

func newRespStream(model string, tools *respTools) *respStream {
	if tools == nil {
		tools = &respTools{byUpstream: map[string]respClientTool{}, toUpstream: map[respClientTool]string{}}
	}
	return &respStream{
		tools: tools, id: respNewID("resp_"), model: model, created: time.Now().Unix(),
		done: map[int]map[string]any{},
	}
}

func respNewID(prefix string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func (s *respStream) send(ev map[string]any) error {
	for _, ev := range s.phaseEvent(ev) {
		ev["sequence_number"] = s.seq
		s.seq++
		b, err := respMarshal(ev)
		if err != nil {
			return err
		}
		if err := s.emit(b); err != nil {
			return err
		}
	}
	return nil
}

// produced reports whether the client got any output item.
func (s *respStream) produced() bool {
	return len(s.done) > 0 || s.msg != nil || s.reasoning != nil || len(s.calls) > 0
}

func (s *respStream) response(status string) map[string]any {
	return map[string]any{
		"id": s.id, "object": "response", "created_at": s.created, "status": status,
		"model": s.model, "output": []any{}, "usage": nil, "error": nil, "incomplete_details": nil,
		"parallel_tool_calls": true, "tool_choice": "auto", "tools": []any{},
	}
}

func (s *respStream) start() error {
	if s.started {
		return nil
	}
	s.started = true
	if err := s.send(map[string]any{"type": "response.created", "response": s.response("in_progress")}); err != nil {
		return err
	}
	return s.send(map[string]any{"type": "response.in_progress", "response": s.response("in_progress")})
}

func (s *respStream) alloc() int {
	i := s.nextIndex
	s.nextIndex++
	return i
}

func respOutputText(text string) map[string]any {
	return map[string]any{"type": "output_text", "text": text, "annotations": []any{}, "logprobs": []any{}}
}

// --- message items ---

func (s *respStream) openMessage() error {
	if s.msg != nil {
		return nil
	}
	if err := s.start(); err != nil {
		return err
	}
	s.msg = &respMsgState{index: s.alloc(), id: respNewID("msg_")}
	return s.send(map[string]any{"type": "response.output_item.added", "output_index": s.msg.index,
		"item": map[string]any{"type": "message", "id": s.msg.id, "status": "in_progress", "role": "assistant", "content": []any{}}})
}

func (s *respStream) openTextPart() error {
	if err := s.openMessage(); err != nil {
		return err
	}
	if s.msg.partOpen {
		return nil
	}
	s.msg.partOpen = true
	s.msg.cur.Reset()
	return s.send(map[string]any{"type": "response.content_part.added", "item_id": s.msg.id,
		"output_index": s.msg.index, "content_index": len(s.msg.parts), "part": respOutputText("")})
}

func (s *respStream) textDelta(text string) error {
	if text == "" {
		return nil
	}
	if err := s.openTextPart(); err != nil {
		return err
	}
	s.msg.cur.WriteString(text)
	return s.send(map[string]any{"type": "response.output_text.delta", "item_id": s.msg.id,
		"output_index": s.msg.index, "content_index": len(s.msg.parts), "delta": text, "logprobs": []any{}})
}

func (s *respStream) closeTextPart() error {
	if s.msg == nil || !s.msg.partOpen {
		return nil
	}
	text := s.msg.cur.String()
	ci := len(s.msg.parts)
	s.msg.partOpen = false
	s.msg.parts = append(s.msg.parts, text)
	if err := s.send(map[string]any{"type": "response.output_text.done", "item_id": s.msg.id,
		"output_index": s.msg.index, "content_index": ci, "text": text, "logprobs": []any{}}); err != nil {
		return err
	}
	return s.send(map[string]any{"type": "response.content_part.done", "item_id": s.msg.id,
		"output_index": s.msg.index, "content_index": ci, "part": respOutputText(text)})
}

func (s *respStream) closeMessage() error {
	if s.msg == nil {
		return nil
	}
	if err := s.closeTextPart(); err != nil {
		return err
	}
	content := make([]any, 0, len(s.msg.parts))
	for _, p := range s.msg.parts {
		content = append(content, respOutputText(p))
	}
	item := map[string]any{"type": "message", "id": s.msg.id, "status": "completed", "role": "assistant", "content": content}
	idx := s.msg.index
	s.msg = nil
	s.done[idx] = item
	return s.send(map[string]any{"type": "response.output_item.done", "output_index": idx, "item": item})
}

// --- reasoning items ---

func (s *respStream) openReasoning() error {
	if s.reasoning != nil {
		return nil
	}
	if err := s.start(); err != nil {
		return err
	}
	s.reasoning = &respReasoningState{index: s.alloc(), id: respNewID("rs_")}
	return s.send(map[string]any{"type": "response.output_item.added", "output_index": s.reasoning.index,
		"item": map[string]any{"type": "reasoning", "id": s.reasoning.id, "summary": []any{}}})
}

func (s *respStream) reasoningDelta(text string) error {
	if text == "" {
		return nil
	}
	if err := s.openReasoning(); err != nil {
		return err
	}
	r := s.reasoning
	if !r.partOpen {
		r.partOpen = true
		if err := s.send(map[string]any{"type": "response.reasoning_summary_part.added", "item_id": r.id,
			"output_index": r.index, "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}}); err != nil {
			return err
		}
	}
	r.text.WriteString(text)
	return s.send(map[string]any{"type": "response.reasoning_summary_text.delta", "item_id": r.id,
		"output_index": r.index, "summary_index": 0, "delta": text})
}

// closeReasoning finishes the open reasoning item; encrypted, when set,
// becomes its encrypted_content.
func (s *respStream) closeReasoning(encrypted string) error {
	r := s.reasoning
	if r == nil {
		return nil
	}
	text := r.text.String()
	if r.partOpen {
		if err := s.send(map[string]any{"type": "response.reasoning_summary_text.done", "item_id": r.id,
			"output_index": r.index, "summary_index": 0, "text": text}); err != nil {
			return err
		}
		if err := s.send(map[string]any{"type": "response.reasoning_summary_part.done", "item_id": r.id,
			"output_index": r.index, "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": text}}); err != nil {
			return err
		}
	}
	summary := []any{}
	if text != "" {
		summary = append(summary, map[string]any{"type": "summary_text", "text": text})
	}
	item := map[string]any{"type": "reasoning", "id": r.id, "summary": summary}
	if encrypted == "" {
		encrypted = r.encrypted
	}
	if encrypted != "" {
		item["encrypted_content"] = encrypted
	}
	s.reasoning = nil
	s.done[r.index] = item
	return s.send(map[string]any{"type": "response.output_item.done", "output_index": r.index, "item": item})
}

// --- tool calls ---

func (s *respStream) openCall(callID, name string) (*respCallState, error) {
	if err := s.start(); err != nil {
		return nil, err
	}
	if callID == "" {
		callID = respNewID("call_")
	}
	c := &respCallState{index: s.alloc(), callID: callID, name: name}
	s.calls = append(s.calls, c)
	if name != "" {
		return c, s.announce(c)
	}
	return c, nil // announced once the name is known (or when it closes)
}

func (s *respStream) setCallName(c *respCallState, name string) error {
	if name == "" || c.announced {
		return nil
	}
	c.name = name
	return s.announce(c)
}

func (s *respStream) announce(c *respCallState) error {
	if c.announced {
		return nil
	}
	c.announced = true
	c.client = s.tools.resolve(c.name)
	var item map[string]any
	switch c.client.kind {
	case respKindCustom:
		c.id = respNewID("ctc_")
		item = map[string]any{"type": "custom_tool_call", "id": c.id, "status": "in_progress",
			"call_id": c.callID, "name": c.client.name, "input": ""}
	case respKindToolSearch:
		c.id = respNewID("tsc_")
		item = map[string]any{"type": "tool_search_call", "id": c.id, "status": "in_progress",
			"call_id": c.callID, "execution": "client", "arguments": map[string]any{}}
	case respKindLocalShell:
		c.id = respNewID("lsh_")
		item = map[string]any{"type": "local_shell_call", "id": c.id, "status": "in_progress",
			"call_id": c.callID, "action": map[string]any{"type": "exec", "command": []any{}}}
	default:
		c.id = respNewID("fc_")
		item = map[string]any{"type": "function_call", "id": c.id, "status": "in_progress",
			"call_id": c.callID, "name": c.client.name, "arguments": ""}
		if c.client.kind == respKindNamespace {
			item["namespace"] = c.client.namespace
		}
	}
	if err := s.send(map[string]any{"type": "response.output_item.added", "output_index": c.index, "item": item}); err != nil {
		return err
	}
	if c.args.Len() > 0 && respStreamsArgs(c) {
		return s.sendArgsDelta(c, c.args.String())
	}
	return nil
}

// respStreamsArgs: only a plain function call streams its arguments; the
// other kinds need them whole, so they are sent when the call is done.
func respStreamsArgs(c *respCallState) bool {
	return c.client.kind == respKindFunction || c.client.kind == respKindNamespace
}

func (s *respStream) sendArgsDelta(c *respCallState, delta string) error {
	return s.send(map[string]any{"type": "response.function_call_arguments.delta", "item_id": c.id,
		"output_index": c.index, "delta": delta})
}

func (s *respStream) callArgs(c *respCallState, delta string) error {
	if delta == "" {
		return nil
	}
	c.args.WriteString(delta)
	if c.announced && respStreamsArgs(c) {
		return s.sendArgsDelta(c, delta)
	}
	return nil
}

func (s *respStream) closeCall(c *respCallState) error {
	for i, open := range s.calls {
		if open == c {
			s.calls = append(s.calls[:i], s.calls[i+1:]...)
			break
		}
	}
	if err := s.announce(c); err != nil {
		return err
	}
	args := c.args.String()
	// A call the output limit (or a content filter) cut off has half its
	// arguments: it is handed over as incomplete, not as a call to run.
	status := "completed"
	if s.cut && !json.Valid([]byte(args)) {
		status = "incomplete"
	}
	var item map[string]any
	switch c.client.kind {
	case respKindCustom:
		input := respCustomInput(args)
		if status == "incomplete" {
			input = ""
		}
		if input != "" {
			if err := s.send(map[string]any{"type": "response.custom_tool_call_input.delta", "item_id": c.id,
				"output_index": c.index, "delta": input}); err != nil {
				return err
			}
		}
		if err := s.send(map[string]any{"type": "response.custom_tool_call_input.done", "item_id": c.id,
			"output_index": c.index, "input": input}); err != nil {
			return err
		}
		item = map[string]any{"type": "custom_tool_call", "id": c.id, "status": status,
			"call_id": c.callID, "name": c.client.name, "input": input}
	case respKindToolSearch:
		item = map[string]any{"type": "tool_search_call", "id": c.id, "status": status,
			"call_id": c.callID, "execution": "client", "arguments": respArgumentsObject(args)}
	case respKindLocalShell:
		item = map[string]any{"type": "local_shell_call", "id": c.id, "status": status,
			"call_id": c.callID, "action": respLocalShellAction(args)}
	default:
		if status == "completed" {
			if strings.TrimSpace(args) == "" {
				args = "{}"
				if err := s.sendArgsDelta(c, args); err != nil {
					return err
				}
			}
			if err := s.send(map[string]any{"type": "response.function_call_arguments.done", "item_id": c.id,
				"output_index": c.index, "arguments": args}); err != nil {
				return err
			}
		}
		item = map[string]any{"type": "function_call", "id": c.id, "status": status,
			"call_id": c.callID, "name": c.client.name, "arguments": args}
		if c.client.kind == respKindNamespace {
			item["namespace"] = c.client.namespace
		}
	}
	s.done[c.index] = item
	return s.send(map[string]any{"type": "response.output_item.done", "output_index": c.index, "item": item})
}

// closeAll finishes every open item, in output order.
func (s *respStream) closeAll() error {
	type closer struct {
		index int
		fn    func() error
	}
	var cs []closer
	if s.reasoning != nil {
		cs = append(cs, closer{s.reasoning.index, func() error { return s.closeReasoning("") }})
	}
	if s.msg != nil {
		cs = append(cs, closer{s.msg.index, s.closeMessage})
	}
	for _, c := range append([]*respCallState(nil), s.calls...) {
		c := c
		cs = append(cs, closer{c.index, func() error { return s.closeCall(c) }})
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].index < cs[j].index })
	for _, c := range cs {
		if err := c.fn(); err != nil {
			return err
		}
	}
	return nil
}

// finish closes what is open and sends the terminal event: reason "" for
// response.completed, else response.incomplete with that reason.
func (s *respStream) finish(reason string, usage respUsage) error {
	if s.finished {
		return nil
	}
	if err := s.start(); err != nil {
		return err
	}
	s.cut = reason != ""
	if err := s.closeAll(); err != nil {
		return err
	}
	s.finished = true
	idx := make([]int, 0, len(s.done))
	for i := range s.done {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	output := make([]any, 0, len(idx))
	for _, i := range idx {
		output = append(output, s.done[i])
	}
	typ, status := "response.completed", "completed"
	resp := s.response(status)
	if reason != "" {
		typ, status = "response.incomplete", "incomplete"
		resp["status"] = status
		resp["incomplete_details"] = map[string]any{"reason": reason}
	}
	resp["output"] = output
	resp["usage"] = usage.wire()
	return s.send(map[string]any{"type": typ, "response": resp})
}

// respAggregate builds the non-streaming Responses answer: the response of
// the terminal event, its output rebuilt from the done items when empty.
func respAggregate(payloads [][]byte) ([]byte, error) {
	type doneItem struct {
		index int
		item  json.RawMessage
	}
	var items []doneItem
	var terminal map[string]json.RawMessage
	for _, p := range payloads {
		var ev struct {
			Type        string          `json:"type"`
			OutputIndex int             `json:"output_index"`
			Item        json.RawMessage `json:"item"`
			Response    json.RawMessage `json:"response"`
			Message     string          `json:"message"`
			Error       json.RawMessage `json:"error"`
		}
		if json.Unmarshal(p, &ev) != nil {
			continue
		}
		switch ev.Type {
		case "response.output_item.done":
			if len(ev.Item) > 0 {
				items = append(items, doneItem{ev.OutputIndex, ev.Item})
			}
		case "response.completed", "response.incomplete", "response.done":
			var r map[string]json.RawMessage
			if json.Unmarshal(ev.Response, &r) == nil && r != nil {
				terminal = r
			}
		case "response.failed":
			// A failed response is an error, as it is on a stream.
			var r struct {
				Error json.RawMessage `json:"error"`
			}
			_ = json.Unmarshal(ev.Response, &r)
			msg := msgErrorMessage(r.Error)
			if msg == "" {
				msg = "response failed"
			}
			return nil, fmt.Errorf("apicompat: responses stream error: %s", msg)
		case "error":
			msg := ev.Message
			if msg == "" {
				msg = string(ev.Error)
			}
			return nil, fmt.Errorf("apicompat: responses stream error: %s", msg)
		}
	}
	if terminal == nil {
		return nil, errors.New("apicompat: responses stream has no terminal event")
	}
	var out []json.RawMessage
	_ = json.Unmarshal(terminal["output"], &out)
	if len(out) == 0 {
		sort.SliceStable(items, func(i, j int) bool { return items[i].index < items[j].index })
		list := make([]json.RawMessage, 0, len(items))
		for _, it := range items {
			list = append(list, it.item)
		}
		b, err := json.Marshal(list)
		if err != nil {
			return nil, err
		}
		terminal["output"] = b
	}
	// Its messages say their phase, as a stream's do.
	var status string
	_ = json.Unmarshal(terminal["status"], &status)
	if b, ok := phaseRawOutput(terminal["output"], status == "completed" || status == ""); ok {
		terminal["output"] = b
	}
	return respMarshal(terminal)
}
