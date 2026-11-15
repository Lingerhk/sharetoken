package apicompat

import (
	"encoding/json"
	"fmt"
	"strings"
)

// chat → Responses. The request becomes a Responses request that streams,
// is never stored and asks for the encrypted reasoning; the events come back
// as chat chunks.

// chatMinOutputTokens is the least max_output_tokens the Responses API takes.
const chatMinOutputTokens = 16

func chatToResponses(body []byte, opt Options) (*Conversion, error) {
	r, err := chatParse(body)
	if err != nil {
		return nil, err
	}
	req, err := chatResponsesRequest(r)
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
	return &Conversion{Body: out, Translator: newChatFromResponses(model, chatPromptEstimate(r))}, nil
}

func chatResponsesRequest(r *chatRequest) (map[string]any, error) {
	var instructions []string
	var input []any
	ids := newChatLegacyIDs(r.Messages)
	for i := range r.Messages {
		m := &r.Messages[i]
		switch m.Role {
		case "system":
			// The Codex backend takes no system messages: they are the
			// instructions.
			if t := m.text(); t != "" {
				instructions = append(instructions, t)
			}
		case "developer":
			input = append(input, map[string]any{"type": "message", "role": "developer", "content": chatResponsesContent(m)})
		case "assistant":
			if t := m.assistantText(); t != "" {
				input = append(input, map[string]any{"type": "message", "role": "assistant",
					"content": []any{map[string]any{"type": "output_text", "text": t}}})
			}
			for _, tc := range m.toolCalls(ids) {
				args := tc.Function.Arguments
				if args == "" {
					args = "{}"
				}
				input = append(input, map[string]any{"type": "function_call", "call_id": tc.ID, "name": chatToolNameOut(tc.Function.Name), "arguments": args})
			}
		case "tool", "function":
			out := m.text()
			if out == "" {
				out = "(empty)"
			}
			input = append(input, map[string]any{"type": "function_call_output", "call_id": m.resultID(ids), "output": out})
		default: // user, and any other role as the user's
			input = append(input, map[string]any{"type": "message", "role": "user", "content": chatResponsesContent(m)})
		}
	}
	if len(input) == 0 {
		return nil, fmt.Errorf("the request has no user message")
	}
	req := map[string]any{
		"model":        r.Model,
		"instructions": strings.Join(instructions, "\n\n"),
		"input":        input,
		"stream":       true,
		"store":        false,
		"include":      []string{"reasoning.encrypted_content"},
	}
	if n := r.maxTokens(); n > 0 {
		req["max_output_tokens"] = max(n, chatMinOutputTokens)
	}
	// Reasoning models refuse sampling settings.
	if !chatReasoningModel(r.Model) {
		if r.Temperature != nil {
			req["temperature"] = *r.Temperature
		}
		if r.TopP != nil {
			req["top_p"] = *r.TopP
		}
	}
	if r.ReasoningEffort != "" {
		req["reasoning"] = map[string]any{"effort": r.ReasoningEffort, "summary": "auto"}
	}
	switch rf := r.responseFormat(); rf.kind {
	case "json_object":
		req["text"] = map[string]any{"format": map[string]any{"type": "json_object"}}
	case "json_schema":
		name := rf.name
		if name == "" {
			name = "answer"
		}
		format := map[string]any{"type": "json_schema", "name": name}
		if len(rf.schema) > 0 {
			format["schema"] = rf.schema
		} else {
			format["schema"] = json.RawMessage(`{"type":"object"}`)
		}
		if rf.description != "" {
			format["description"] = rf.description
		}
		if rf.strict != nil {
			format["strict"] = *rf.strict
		}
		req["text"] = map[string]any{"format": format}
	}
	if tools := chatResponsesTools(r.Tools); len(tools) > 0 {
		req["tools"] = tools
		mode, name, other := r.toolChoice()
		switch {
		case name != "":
			req["tool_choice"] = map[string]any{"type": "function", "name": chatToolNameOut(name)}
		case mode != "":
			req["tool_choice"] = mode
		case other != nil:
			req["tool_choice"] = other
		}
		if r.ParallelToolCalls != nil {
			req["parallel_tool_calls"] = *r.ParallelToolCalls
		}
	}
	if r.SessionID != "" {
		req["prompt_cache_key"] = r.SessionID
	}
	return req, nil
}

// chatResponsesContent is a user or developer message's content as
// Responses input: a string stays a string, parts become input parts. It is
// never null, which the API rejects.
func chatResponsesContent(m *chatMessage) any {
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return s
	}
	var out []any
	for _, p := range m.parts() {
		switch {
		case p.isText():
			if p.Text != "" {
				out = append(out, map[string]any{"type": "input_text", "text": p.Text})
			}
		case p.Type == "image_url":
			url, detail := p.image()
			if url == "" || chatEmptyDataURL(url) {
				continue
			}
			part := map[string]any{"type": "input_image", "image_url": url}
			if detail != "" {
				part["detail"] = detail
			}
			out = append(out, part)
		case p.Type == "file":
			if p.File == nil || (p.File.FileData == "" && p.File.FileID == "") {
				continue
			}
			part := map[string]any{"type": "input_file"}
			if p.File.FileData != "" {
				part["file_data"] = p.File.FileData
			}
			if p.File.FileID != "" {
				part["file_id"] = p.File.FileID
			}
			if p.File.Filename != "" {
				part["filename"] = p.File.Filename
			}
			out = append(out, part)
		}
	}
	if len(out) == 0 {
		return ""
	}
	return out
}

// chatResponsesTools converts function tools, and keeps the built-in tools
// the Responses API shares with chat (x_search with its settings, web_search,
// code_execution).
func chatResponsesTools(tools []chatTool) []any {
	var out []any
	for i := range tools {
		t := &tools[i]
		switch strings.ToLower(strings.TrimSpace(t.Type)) {
		case "x_search":
			var m map[string]any
			if json.Unmarshal(t.raw, &m) == nil {
				delete(m, "cache_control")
				m["type"] = "x_search"
				out = append(out, m)
			}
			continue
		case "web_search", "code_execution":
			out = append(out, map[string]any{"type": strings.ToLower(strings.TrimSpace(t.Type))})
			continue
		}
		if !t.isFunction() {
			continue
		}
		strict := false
		if t.Function.Strict != nil {
			strict = *t.Function.Strict
		}
		tool := map[string]any{"type": "function", "name": chatToolNameOut(t.Function.Name),
			"parameters": chatObjectSchema(t.Function.Parameters), "strict": strict}
		if t.Function.Description != "" {
			tool["description"] = t.Function.Description
		}
		out = append(out, tool)
	}
	return out
}

// chatObjectSchema makes a tool's parameters a schema the APIs take: an
// object schema that has properties.
func chatObjectSchema(schema json.RawMessage) json.RawMessage {
	const empty = `{"type":"object","properties":{}}`
	if len(schema) == 0 || string(schema) == "null" {
		return json.RawMessage(empty)
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(schema, &m) != nil || m == nil {
		return json.RawMessage(empty)
	}
	var typ string
	if raw, ok := m["type"]; ok && json.Unmarshal(raw, &typ) == nil && typ == "object" {
		if _, ok := m["properties"]; !ok {
			m["properties"] = json.RawMessage(`{}`)
			if out, err := json.Marshal(m); err == nil {
				return out
			}
		}
	}
	return schema
}

// chatReasoningModel reports an OpenAI reasoning model, which refuses
// temperature and top_p: the o series, and GPT 5 and later.
func chatReasoningModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	if len(m) >= 2 && m[0] == 'o' && m[1] >= '1' && m[1] <= '9' {
		return true
	}
	rest, ok := strings.CutPrefix(m, "gpt-")
	if !ok {
		return false
	}
	major, digits := 0, 0
	for _, c := range rest {
		if c < '0' || c > '9' {
			break
		}
		major = major*10 + int(c-'0')
		digits++
	}
	return digits > 0 && major >= 5
}

// chatFromResponses turns Responses events into chat chunks.
type chatFromResponses struct {
	out       *chatChunker
	promptEst uint64
	tools     map[int]*chatResponsesTool // by output_index
	text      map[int]bool               // message items whose text streamed
	reasoning map[int]bool               // reasoning items whose text streamed
	done      map[int]bool               // items finished with output_item.done
}

type chatResponsesTool struct {
	index int
	args  strings.Builder
}

func newChatFromResponses(model string, promptEst uint64) *chatFromResponses {
	return &chatFromResponses{out: newChatChunker(model), promptEst: promptEst,
		tools: map[int]*chatResponsesTool{}, text: map[int]bool{}, reasoning: map[int]bool{}, done: map[int]bool{}}
}

type chatResponsesItem struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Input     string `json:"input"`
	Content   []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
	Summary []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"summary"`
	EncryptedContent string `json:"encrypted_content"`
}

type chatResponsesUsage struct {
	InputTokens        uint64 `json:"input_tokens"`
	OutputTokens       uint64 `json:"output_tokens"`
	CacheCreation      uint64 `json:"cache_creation_input_tokens"`
	InputTokensDetails *struct {
		CachedTokens        uint64  `json:"cached_tokens"`
		CacheCreationTokens uint64  `json:"cache_creation_tokens"`
		CacheWriteTokens    *uint64 `json:"cache_write_tokens"`
		AudioTokens         uint64  `json:"audio_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails *struct {
		ReasoningTokens          uint64 `json:"reasoning_tokens"`
		AudioTokens              uint64 `json:"audio_tokens"`
		AcceptedPredictionTokens uint64 `json:"accepted_prediction_tokens"`
		RejectedPredictionTokens uint64 `json:"rejected_prediction_tokens"`
	} `json:"output_tokens_details"`
}

// usage maps Responses usage: input_tokens already counts cached input.
func (u *chatResponsesUsage) usage() chatUsage {
	out := chatUsage{Prompt: u.InputTokens, Completion: u.OutputTokens, CacheCreation: u.CacheCreation}
	if d := u.InputTokensDetails; d != nil {
		out.Cached, out.PromptAudio = d.CachedTokens, d.AudioTokens
		switch {
		case d.CacheCreationTokens > 0:
			out.CacheCreation = d.CacheCreationTokens
		case d.CacheWriteTokens != nil: // present, even as 0, it is the count
			out.CacheCreation = *d.CacheWriteTokens
		}
	}
	if d := u.OutputTokensDetails; d != nil {
		out.Reasoning, out.CompletionAudio = d.ReasoningTokens, d.AudioTokens
		out.Accepted, out.Rejected = d.AcceptedPredictionTokens, d.RejectedPredictionTokens
	}
	return out
}

func (s *chatFromResponses) Event(payload []byte, emit func([]byte) error) error {
	var ev struct {
		Type         string             `json:"type"`
		Delta        string             `json:"delta"`
		OutputIndex  int                `json:"output_index"`
		SummaryIndex int                `json:"summary_index"`
		Item         *chatResponsesItem `json:"item"`
		Arguments    string             `json:"arguments"`
		Input        string             `json:"input"`
		Response     *struct {
			Status            string              `json:"status"`
			Usage             *chatResponsesUsage `json:"usage"`
			IncompleteDetails *chatIncomplete     `json:"incomplete_details"`
			Error             *chatResponseError  `json:"error"`
			Output            []chatResponsesItem `json:"output"`
		} `json:"response"`
		Usage   *chatResponsesUsage `json:"usage"`
		Message string              `json:"message"`
		Code    string              `json:"code"`
		Error   *struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &ev); err != nil {
		return nil // not ours to judge: skip what cannot be read
	}
	s.out.emit = emit
	if s.out.finished && ev.Type != "error" && ev.Type != "response.failed" {
		return nil
	}
	switch ev.Type {
	case "response.output_text.delta":
		s.text[ev.OutputIndex] = true
		return s.out.content(ev.Delta)
	case "response.refusal.delta":
		s.text[ev.OutputIndex] = true
		return s.out.refusal(ev.Delta)
	case "response.reasoning_summary_part.added":
		if ev.SummaryIndex > 0 && s.reasoning[ev.OutputIndex] {
			return s.out.reasoning("\n\n")
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if ev.Delta != "" {
			s.reasoning[ev.OutputIndex] = true
		}
		return s.out.reasoning(ev.Delta)
	case "response.output_item.added":
		if it := ev.Item; it != nil && (it.Type == "function_call" || it.Type == "custom_tool_call") {
			_, err := s.toolStart(ev.OutputIndex, it)
			return err
		}
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		if t := s.tools[ev.OutputIndex]; t != nil && ev.Delta != "" {
			t.args.WriteString(ev.Delta)
			return s.out.toolArgs(t.index, ev.Delta)
		}
	case "response.function_call_arguments.done":
		return s.toolDone(ev.OutputIndex, ev.Arguments)
	case "response.custom_tool_call_input.done":
		return s.toolDone(ev.OutputIndex, ev.Input)
	case "response.output_item.done":
		if ev.Item != nil {
			s.done[ev.OutputIndex] = true
			return s.item(ev.OutputIndex, ev.Item)
		}
	case "response.completed", "response.done", "response.incomplete":
		u := chatUsage{}
		reason := "stop"
		if r := ev.Response; r != nil {
			// What the stream did not carry is in the final output.
			for i := range r.Output {
				if !s.done[i] {
					s.done[i] = true
					if err := s.item(i, &r.Output[i]); err != nil {
						return err
					}
				}
			}
			if r.Usage != nil {
				u = r.Usage.usage()
			}
			if r.Status == "incomplete" || ev.Type == "response.incomplete" {
				switch r.IncompleteDetails.reason() {
				case "max_output_tokens":
					reason = "length"
				case "content_filter":
					reason = "content_filter"
				}
			}
			if r.Status == "failed" {
				return s.failed(r.Error)
			}
		}
		if ev.Usage != nil {
			u = ev.Usage.usage()
		}
		return s.out.finish(reason, u)
	case "response.failed":
		if ev.Response != nil {
			return s.failed(ev.Response.Error)
		}
		return &chatUpstreamError{Kind: "response.failed", Message: "the response failed"}
	case "error":
		e := &chatUpstreamError{Kind: ev.Code, Message: ev.Message}
		if ev.Error != nil {
			e.Kind, e.Message = chatFirst(ev.Error.Code, ev.Error.Type, e.Kind), chatFirst(ev.Error.Message, e.Message)
		}
		return e
	}
	return nil
}

type chatIncomplete struct {
	Reason string `json:"reason"`
}

func (d *chatIncomplete) reason() string {
	if d == nil {
		return ""
	}
	return d.Reason
}

type chatResponseError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (s *chatFromResponses) failed(e *chatResponseError) error {
	if e == nil {
		return &chatUpstreamError{Kind: "response.failed", Message: "the response failed"}
	}
	return &chatUpstreamError{Kind: chatFirst(e.Code, "response.failed"), Message: e.Message}
}

func (s *chatFromResponses) toolStart(index int, it *chatResponsesItem) (*chatResponsesTool, error) {
	if t := s.tools[index]; t != nil {
		return t, nil
	}
	i, err := s.out.toolStart(it.CallID, chatToolNameIn(it.Name), "")
	t := &chatResponsesTool{index: i}
	s.tools[index] = t
	return t, err
}

// toolDone completes a tool call's arguments from the full text the
// upstream ends it with: what the deltas did not carry goes out.
func (s *chatFromResponses) toolDone(index int, full string) error {
	t := s.tools[index]
	if t == nil || full == "" {
		return nil
	}
	sent := t.args.String()
	if full == sent || !strings.HasPrefix(full, sent) {
		return nil
	}
	t.args.WriteString(full[len(sent):])
	return s.out.toolArgs(t.index, full[len(sent):])
}

// item handles a finished output item: whatever of it the stream did not
// carry goes out now.
func (s *chatFromResponses) item(index int, it *chatResponsesItem) error {
	switch it.Type {
	case "function_call", "custom_tool_call":
		_, err := s.toolStart(index, it)
		if err != nil {
			return err
		}
		full := it.Arguments
		if it.Type == "custom_tool_call" {
			full = it.Input
		}
		return s.toolDone(index, full)
	case "message":
		if s.text[index] {
			return nil
		}
		s.text[index] = true
		for _, p := range it.Content {
			var err error
			switch p.Type {
			case "output_text", "text":
				err = s.out.content(p.Text)
			case "refusal":
				err = s.out.refusal(p.Refusal)
			}
			if err != nil {
				return err
			}
		}
	case "reasoning":
		if s.reasoning[index] {
			return nil
		}
		var parts []string
		for _, p := range it.Summary {
			if p.Text != "" {
				parts = append(parts, p.Text)
			}
		}
		if len(parts) == 0 {
			for _, p := range it.Content {
				if p.Type == "reasoning_text" && p.Text != "" {
					parts = append(parts, p.Text)
				}
			}
		}
		if len(parts) == 0 {
			// Reasoning kept encrypted has no text: usage reports it as
			// reasoning_tokens.
			return nil
		}
		s.reasoning[index] = true
		return s.out.reasoning(strings.Join(parts, "\n\n"))
	}
	return nil
}

func (s *chatFromResponses) Close(emit func([]byte) error) error {
	s.out.emit = emit
	if s.out.finished {
		return nil
	}
	if !s.out.emitted {
		return errChatStreamEnded
	}
	// Cut short: closed as if it had stopped here, with estimated usage.
	return s.out.finish("stop", chatUsage{Prompt: s.promptEst, Completion: s.out.output})
}

func chatFirst(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
