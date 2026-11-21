package apicompat

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// The chat client side: a request in OpenAI chat completions, converted for an
// upstream that speaks Responses (chat_responses.go) or Messages
// (chat_messages.go), and the upstream's events turned back into
// chat.completion.chunk payloads (chat_chunks.go).

func init() {
	register(Chat, Responses, chatToResponses)
	register(Chat, Messages, chatToMessages)
	registerAggregator(Chat, chatAggregate)
}

// chatRequest is the chat-completions request a node hands to a converter:
// the fields it forwards from the buyer and those it sets itself.
type chatRequest struct {
	Model               string          `json:"model"`
	Messages            []chatMessage   `json:"messages"`
	MaxTokens           uint64          `json:"max_tokens"`
	MaxCompletionTokens uint64          `json:"max_completion_tokens"`
	Temperature         *float64        `json:"temperature"`
	TopP                *float64        `json:"top_p"`
	Stop                json.RawMessage `json:"stop"`
	ResponseFormat      json.RawMessage `json:"response_format"`
	Tools               []chatTool      `json:"tools"`
	ToolChoice          json.RawMessage `json:"tool_choice"`
	ParallelToolCalls   *bool           `json:"parallel_tool_calls"`
	User                string          `json:"user"`
	ReasoningEffort     string          `json:"reasoning_effort"`
	SessionID           string          `json:"session_id"`
}

type chatMessage struct {
	Role              string            `json:"role"`
	Content           json.RawMessage   `json:"content"` // a string, or parts
	Name              string            `json:"name"`
	ToolCalls         []chatToolCall    `json:"tool_calls"`
	ToolCallID        string            `json:"tool_call_id"`
	FunctionCall      *chatFunctionCall `json:"function_call"` // legacy function calling
	ReasoningContent  string            `json:"reasoning_content"`
	Reasoning         string            `json:"reasoning"` // the other common spelling
	AnthropicThinking []json.RawMessage `json:"anthropic_thinking"`
}

type chatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatFunctionCall `json:"function"`
}

type chatFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// chatTool is one entry of tools: a function, or a built-in tool kept raw.
type chatTool struct {
	Type         string            `json:"type"`
	CacheControl *chatCacheControl `json:"cache_control"`
	Function     *struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
		Strict      *bool           `json:"strict"`
	} `json:"function"`
	raw json.RawMessage
}

func (t *chatTool) UnmarshalJSON(b []byte) error {
	type plain chatTool
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*t = chatTool(p)
	t.raw = append(json.RawMessage(nil), b...)
	return nil
}

// isFunction reports whether the tool is a function tool with a name.
func (t *chatTool) isFunction() bool {
	return (t.Type == "" || t.Type == "function") && t.Function != nil && t.Function.Name != ""
}

// chatPart is one part of a message's content.
type chatPart struct {
	Type         string            `json:"type"`
	Text         string            `json:"text"`
	Thinking     string            `json:"thinking"` // assistant parts of type thinking
	Refusal      string            `json:"refusal"`
	CacheControl *chatCacheControl `json:"cache_control"`
	ImageURL     json.RawMessage   `json:"image_url"` // {"url","detail"} or a bare string
	File         *struct {
		FileData string `json:"file_data"`
		FileID   string `json:"file_id"`
		Filename string `json:"filename"`
	} `json:"file"`
}

// image returns an image_url part's URL and detail.
func (p *chatPart) image() (url, detail string) {
	if len(p.ImageURL) == 0 {
		return "", ""
	}
	if json.Unmarshal(p.ImageURL, &url) == nil {
		return url, ""
	}
	var obj struct {
		URL    string `json:"url"`
		Detail string `json:"detail"`
	}
	_ = json.Unmarshal(p.ImageURL, &obj)
	return obj.URL, obj.Detail
}

// isText reports a text part (a part without a type is text).
func (p *chatPart) isText() bool { return p.Type == "" || p.Type == "text" || p.Type == "input_text" }

type chatCacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

func (c *chatCacheControl) valid() error {
	if c == nil {
		return nil
	}
	if c.Type != "ephemeral" || (c.TTL != "" && c.TTL != "5m" && c.TTL != "1h") {
		return fmt.Errorf("cache_control must be ephemeral with ttl 5m or 1h")
	}
	return nil
}

func (c *chatCacheControl) value() map[string]any {
	m := map[string]any{"type": c.Type}
	if c.TTL != "" {
		m["ttl"] = c.TTL
	}
	return m
}

func chatParse(body []byte) (*chatRequest, error) {
	var r chatRequest
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("not a chat-completions request: %w", err)
	}
	if len(r.Messages) == 0 {
		return nil, fmt.Errorf("the request has no messages")
	}
	return &r, nil
}

// maxTokens is the output limit the request asks for (0: none).
func (r *chatRequest) maxTokens() uint64 {
	switch {
	case r.MaxTokens == 0:
		return r.MaxCompletionTokens
	case r.MaxCompletionTokens == 0:
		return r.MaxTokens
	}
	return min(r.MaxTokens, r.MaxCompletionTokens)
}

// parts returns a message's content as parts: a plain string is one text part.
func (m *chatMessage) parts() []chatPart {
	if len(m.Content) == 0 || string(m.Content) == "null" {
		return nil
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		if s == "" {
			return nil
		}
		return []chatPart{{Type: "text", Text: s}}
	}
	var ps []chatPart
	if json.Unmarshal(m.Content, &ps) == nil {
		return ps
	}
	var one chatPart // a single part object
	if json.Unmarshal(m.Content, &one) == nil && one.Type != "" {
		return []chatPart{one}
	}
	return nil
}

// text is the text of a message's content, without its other parts.
func (m *chatMessage) text() string {
	var b strings.Builder
	for _, p := range m.parts() {
		if p.isText() {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// reasoning is the reasoning an assistant message carries back.
func (m *chatMessage) reasoning() string {
	if m.ReasoningContent != "" {
		return m.ReasoningContent
	}
	return m.Reasoning
}

// assistantText is the text of an assistant message as plain text: its
// reasoning and any thinking parts are kept, marked with <thinking> tags, so
// an upstream that cannot take them as reasoning still sees them apart from
// the answer.
func (m *chatMessage) assistantText() string {
	var b strings.Builder
	if r := m.reasoning(); r != "" {
		b.WriteString("<thinking>" + r + "</thinking>")
	}
	var body strings.Builder
	for _, p := range m.parts() {
		switch p.Type {
		case "thinking", "reasoning":
			t := p.Thinking
			if t == "" {
				t = p.Text
			}
			if t != "" {
				body.WriteString("<thinking>" + t + "</thinking>")
			}
		case "", "text", "output_text":
			body.WriteString(p.Text)
		}
	}
	if body.Len() > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(body.String())
	}
	return b.String()
}

// toolCalls returns an assistant message's tool calls, a legacy
// function_call included (given an ID by ids).
func (m *chatMessage) toolCalls(ids *chatLegacyIDs) []chatToolCall {
	if len(m.ToolCalls) > 0 || m.FunctionCall == nil {
		return m.ToolCalls
	}
	return []chatToolCall{{ID: ids.assign(m.FunctionCall.Name), Type: "function", Function: *m.FunctionCall}}
}

// resultID is the call a tool or legacy function message answers.
func (m *chatMessage) resultID(ids *chatLegacyIDs) string {
	if m.ToolCallID != "" {
		return m.ToolCallID
	}
	if m.Role == "function" {
		if id := ids.claim(m.Name); id != "" {
			return id
		}
		return m.Name
	}
	return ""
}

// chatLegacyIDs pairs legacy function calls with their results. Legacy
// function calling has no call IDs (the result names the function), while
// Responses and Messages pair a call and its result by ID. IDs follow message
// order, so a replayed history converts to the same request (and keeps its
// cached prefix), and skip IDs the conversation already uses.
type chatLegacyIDs struct {
	used    map[string]bool
	pending map[string][]string
	next    int
}

func newChatLegacyIDs(msgs []chatMessage) *chatLegacyIDs {
	ids := &chatLegacyIDs{used: map[string]bool{}, pending: map[string][]string{}}
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			ids.used[tc.ID] = true
		}
		if m.ToolCallID != "" {
			ids.used[m.ToolCallID] = true
		}
	}
	return ids
}

func (ids *chatLegacyIDs) assign(name string) string {
	for {
		ids.next++
		id := fmt.Sprintf("call_legacy_%d", ids.next)
		if !ids.used[id] {
			ids.used[id] = true
			ids.pending[name] = append(ids.pending[name], id)
			return id
		}
	}
}

func (ids *chatLegacyIDs) claim(name string) string {
	q := ids.pending[name]
	if len(q) == 0 {
		return ""
	}
	ids.pending[name] = q[1:]
	return q[0]
}

// stops returns the stop sequences: the field is a string or a list.
func (r *chatRequest) stops() []string {
	if len(r.Stop) == 0 || string(r.Stop) == "null" {
		return nil
	}
	var one string
	if json.Unmarshal(r.Stop, &one) == nil {
		if one == "" {
			return nil
		}
		return []string{one}
	}
	var many []string
	_ = json.Unmarshal(r.Stop, &many)
	return many
}

// toolChoice reads tool_choice: mode is "auto", "none" or "required" (or
// another string), or name is the one function to call. other is an object
// that names no function (a built-in tool, allowed_tools), kept raw.
func (r *chatRequest) toolChoice() (mode, name string, other json.RawMessage) {
	if len(r.ToolChoice) == 0 || string(r.ToolChoice) == "null" {
		return "", "", nil
	}
	if json.Unmarshal(r.ToolChoice, &mode) == nil {
		return mode, "", nil
	}
	var obj struct {
		Type     string `json:"type"`
		Name     string `json:"name"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if json.Unmarshal(r.ToolChoice, &obj) != nil {
		return "", "", nil
	}
	if obj.Type == "function" || obj.Type == "" {
		if n := strings.TrimSpace(obj.Function.Name); n != "" {
			return "", n, nil
		}
		if n := strings.TrimSpace(obj.Name); n != "" {
			return "", n, nil
		}
	}
	return "", "", r.ToolChoice
}

// chatResponseFormat is response_format read: kind is "json_object" or
// "json_schema" (or "" for text and anything unknown).
type chatResponseFormat struct {
	kind        string
	name        string
	description string
	schema      json.RawMessage
	strict      *bool
}

func (r *chatRequest) responseFormat() chatResponseFormat {
	var rf struct {
		Type       string `json:"type"`
		JSONSchema struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Schema      json.RawMessage `json:"schema"`
			Strict      *bool           `json:"strict"`
		} `json:"json_schema"`
	}
	if len(r.ResponseFormat) == 0 || json.Unmarshal(r.ResponseFormat, &rf) != nil {
		return chatResponseFormat{}
	}
	switch rf.Type {
	case "json_object":
		return chatResponseFormat{kind: rf.Type}
	case "json_schema":
		s := rf.JSONSchema.Schema
		if string(s) == "null" {
			s = nil
		}
		return chatResponseFormat{kind: rf.Type, name: rf.JSONSchema.Name, description: rf.JSONSchema.Description, schema: s, strict: rf.JSONSchema.Strict}
	}
	return chatResponseFormat{}
}

// chatDataURL splits a base64 data: URL into its media type and payload.
func chatDataURL(u string) (mediaType, data string, ok bool) {
	rest, found := strings.CutPrefix(u, "data:")
	if !found {
		return "", "", false
	}
	meta, data, found := strings.Cut(rest, ",")
	if !found || !strings.HasSuffix(meta, ";base64") {
		return "", "", false
	}
	return strings.TrimSuffix(meta, ";base64"), data, true
}

// chatEmptyDataURL reports a data: URL without a payload, which upstreams
// reject.
func chatEmptyDataURL(u string) bool {
	_, data, ok := chatDataURL(u)
	return ok && strings.TrimSpace(data) == ""
}

// chatObjectJSON parses JSON that must be an object, for tool arguments a
// model wrote: anything else becomes an empty object.
func chatObjectJSON(s string) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(s), &m) != nil || m == nil {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(s)
}

// The tool name "python" is reserved by the Codex backend: it goes out under
// an alias and comes back under its own name.
const (
	chatReservedTool      = "python"
	chatReservedToolAlias = "python__st"
)

func chatToolNameOut(name string) string {
	if name == chatReservedTool {
		return chatReservedToolAlias
	}
	return name
}

func chatToolNameIn(name string) string {
	if name == chatReservedToolAlias {
		return chatReservedTool
	}
	return name
}

// chatPromptEstimate estimates the prompt of a request, for a stream the
// upstream cut short before it reported usage.
func chatPromptEstimate(r *chatRequest) uint64 {
	var n uint64
	for i := range r.Messages {
		m := &r.Messages[i]
		n += 4 + chatEstimateTokens(m.text()) + chatEstimateTokens(m.reasoning())
		for _, tc := range m.ToolCalls {
			n += chatEstimateTokens(tc.Function.Name + tc.Function.Arguments)
		}
		for _, p := range m.parts() {
			if p.Type == "image_url" || p.Type == "file" {
				n += 85 // the least an attachment costs
			}
		}
	}
	for _, t := range r.Tools {
		n += chatEstimateTokens(string(t.raw))
	}
	return n
}

// chatEstimateTokens is a tokenizer-free estimate: about 4 characters per
// token for Latin text and 1.5 for CJK (the node's own estimate).
func chatEstimateTokens(s string) uint64 {
	var latin, cjk uint64
	for _, r := range s {
		switch {
		case r >= 0x1100 && r <= 0x11FF, r >= 0x2E80 && r <= 0x9FFF, r >= 0xAC00 && r <= 0xD7AF, r >= 0xF900 && r <= 0xFAFF, r >= 0x20000 && r <= 0x3FFFF:
			cjk++
		default:
			latin++
		}
	}
	return (latin+3)/4 + (cjk*2+2)/3
}

// chatBase64Text decodes a base64 payload that must be UTF-8 text.
func chatBase64Text(data string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(data))
	if err != nil {
		return "", err
	}
	if !utf8.Valid(b) {
		return "", fmt.Errorf("not UTF-8 text")
	}
	return string(b), nil
}
