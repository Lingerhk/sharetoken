package apicompat

// Reading a Messages request (as Claude Code sends it), shared by the
// conversions to Responses and to chat.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

type msgRequest struct {
	Model         string          `json:"model"`
	System        json.RawMessage `json:"system"`
	Messages      []msgMessage    `json:"messages"`
	Tools         []msgTool       `json:"tools"`
	ToolChoice    json.RawMessage `json:"tool_choice"`
	Thinking      *msgThinking    `json:"thinking"`
	OutputConfig  *msgOutputCfg   `json:"output_config"`
	Metadata      *msgMetadata    `json:"metadata"`
	StopSequences []string        `json:"stop_sequences"`
	Temperature   *float64        `json:"temperature"`
	TopP          *float64        `json:"top_p"`
	MaxTokens     int             `json:"max_tokens"`
}

type msgThinking struct {
	Type         string `json:"type"` // enabled, adaptive, disabled
	BudgetTokens int    `json:"budget_tokens"`
}

type msgOutputCfg struct {
	Effort string          `json:"effort"`
	Format json.RawMessage `json:"format"` // structured output: {"type":"json_schema","schema":{...}}
}

// msgSchemaName names the schema of a structured output for the OpenAI APIs,
// which want one; Messages has none.
const msgSchemaName = "answer"

// msgOutputSchema is the JSON schema output_config.format asks the answer to
// follow, nil when it asks for none.
func msgOutputSchema(r *msgRequest) json.RawMessage {
	if r.OutputConfig == nil {
		return nil
	}
	var f struct {
		Type   string          `json:"type"`
		Schema json.RawMessage `json:"schema"`
	}
	if json.Unmarshal(r.OutputConfig.Format, &f) != nil || f.Type != "json_schema" || len(f.Schema) == 0 || string(f.Schema) == "null" {
		return nil
	}
	return f.Schema
}

type msgMetadata struct {
	UserID string `json:"user_id"`
}

type msgMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type msgTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// msgContent is one content block of a request.
type msgContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
	// thinking, redacted_thinking
	Thinking  string `json:"thinking"`
	Signature string `json:"signature"`
	// tool_use
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
	// tool_result
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
	// image, document
	Source *msgSource `json:"source"`
	Title  string     `json:"title"`
	// tool_reference (in a tool_result)
	ToolName string `json:"tool_name"`
}

type msgSource struct {
	Type      string          `json:"type"` // base64, url, text, content
	MediaType string          `json:"media_type"`
	Data      string          `json:"data"`
	URL       string          `json:"url"`
	Content   json.RawMessage `json:"content"` // type content: string or blocks
}

func msgParseRequest(body []byte) (*msgRequest, error) {
	var r msgRequest
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("apicompat: messages request: %w", err)
	}
	if r.Model == "" {
		return nil, fmt.Errorf("apicompat: messages request has no model")
	}
	return &r, nil
}

// msgBlocks reads message content: a string is one text block.
func msgBlocks(raw json.RawMessage) ([]msgContent, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []msgContent{{Type: "text", Text: s}}, nil
	}
	var blocks []msgContent
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, fmt.Errorf("apicompat: messages content: %w", err)
	}
	return blocks, nil
}

// msgSystemTexts returns the system prompt's text blocks. Claude Code's
// billing header block (it changes with every client version, and it is no
// instruction) is left out.
func msgSystemTexts(raw json.RawMessage) ([]string, error) {
	blocks, err := msgBlocks(raw)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" && !strings.HasPrefix(b.Text, "x-anthropic-billing-header:") {
			out = append(out, b.Text)
		}
	}
	return out, nil
}

// msgIsClientTool reports whether a tool is one the client runs (a custom
// tool). Server tools (web search, code execution, ...) and Anthropic-defined
// tools have no counterpart on an OpenAI upstream.
func msgIsClientTool(t msgTool) bool {
	return (t.Type == "" || t.Type == "custom") && t.Name != ""
}

// msgToolParameters makes a tool schema acceptable as OpenAI function
// parameters, which need an object schema with properties.
func msgToolParameters(schema json.RawMessage) json.RawMessage {
	empty := json.RawMessage(`{"type":"object","properties":{}}`)
	if len(schema) == 0 || string(schema) == "null" {
		return empty
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(schema, &m) != nil {
		return empty
	}
	if _, ok := m["type"]; !ok {
		if _, hasProps := m["properties"]; hasProps {
			m["type"] = json.RawMessage(`"object"`)
		}
	}
	if string(m["type"]) == `"object"` {
		if _, ok := m["properties"]; !ok {
			m["properties"] = json.RawMessage(`{}`)
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return schema
	}
	return b
}

// OpenAI function names are at most 64 characters; Claude allows longer ones
// (MCP tools: mcp__server__tool). A longer name is cut and made unique with a
// hash, and the events give the client its own name back.
const msgMaxToolName = 64

// msgToolNames maps client tool names to upstream ones (only the names that
// change) and back.
type msgToolNames struct {
	up   map[string]string
	back map[string]string
}

func msgNewToolNames(tools []msgTool, msgs []msgMessage) *msgToolNames {
	n := &msgToolNames{up: map[string]string{}, back: map[string]string{}}
	addName := func(name string) {
		if len(name) <= msgMaxToolName || n.up[name] != "" {
			return
		}
		sum := sha256.Sum256([]byte(name))
		short := name[:msgMaxToolName-9] + "_" + hex.EncodeToString(sum[:4])
		n.up[name] = short
		n.back[short] = name
	}
	for _, t := range tools {
		addName(t.Name)
	}
	for _, m := range msgs {
		blocks, _ := msgBlocks(m.Content)
		for _, b := range blocks {
			if b.Type == "tool_use" {
				addName(b.Name)
			}
		}
	}
	return n
}

func (n *msgToolNames) toUpstream(name string) string {
	if s := n.up[name]; s != "" {
		return s
	}
	return name
}

func (n *msgToolNames) toClient(name string) string {
	if s := n.back[name]; s != "" {
		return s
	}
	return name
}

// msgEffort is the OpenAI reasoning effort for a request: "none" when thinking
// is disabled, else output_config.effort (Anthropic's max is OpenAI's xhigh),
// else one derived from an enabled thinking budget, else medium.
func msgEffort(r *msgRequest) string {
	if r.Thinking != nil && r.Thinking.Type == "disabled" {
		return "none"
	}
	effort := ""
	if r.OutputConfig != nil {
		effort = strings.ToLower(strings.TrimSpace(r.OutputConfig.Effort))
	}
	if effort == "" && r.Thinking != nil && r.Thinking.Type == "enabled" && r.Thinking.BudgetTokens > 0 {
		switch b := r.Thinking.BudgetTokens; {
		case b < 4096:
			effort = "low"
		case b < 16384:
			effort = "medium"
		default:
			effort = "high"
		}
	}
	switch effort {
	case "":
		return "medium"
	case "max":
		return "xhigh"
	}
	return effort
}

// msgModelGeneration is N of a gpt-N model name.
func msgModelGeneration(model string) (int, bool) {
	rest, ok := strings.CutPrefix(strings.ToLower(strings.TrimSpace(model)), "gpt-")
	if !ok {
		return 0, false
	}
	n, digits := 0, 0
	for _, c := range rest {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
		digits++
	}
	return n, digits > 0
}

// msgReasoningOnly reports whether an OpenAI model is a reasoning model that
// rejects sampling parameters (temperature, top_p).
func msgReasoningOnly(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if n, ok := msgModelGeneration(m); ok {
		return n >= 5
	}
	for _, p := range []string{"o1", "o3", "o4"} {
		if m == p || strings.HasPrefix(m, p+"-") {
			return true
		}
	}
	return false
}

// msgNoReasoning reports whether a model is known to reject reasoning
// parameters (an OpenAI model before the reasoning generations).
func msgNoReasoning(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if n, ok := msgModelGeneration(m); ok {
		return n < 5
	}
	return strings.HasPrefix(m, "chatgpt-")
}

// msgPromptCacheKey is a prompt_cache_key for a Claude Code conversation: a
// hash of its metadata.user_id session (device, account, session), else of
// the text blocks it marks with cache_control. The ids themselves never reach
// the upstream.
func msgPromptCacheKey(r *msgRequest) string {
	if r.Metadata != nil {
		if dev, acct, sess := msgParseUserID(r.Metadata.UserID); sess != "" {
			return msgHashKey("session|" + dev + "|" + acct + "|" + sess)
		}
	}
	var parts []string
	var sys []struct {
		Type         string          `json:"type"`
		Text         string          `json:"text"`
		CacheControl json.RawMessage `json:"cache_control"`
	}
	if json.Unmarshal(r.System, &sys) == nil {
		for _, b := range sys {
			if b.Type == "text" && len(b.CacheControl) > 0 && strings.TrimSpace(b.Text) != "" {
				parts = append(parts, "system:"+strings.TrimSpace(b.Text))
			}
		}
	}
	for _, m := range r.Messages {
		if m.Role != "user" {
			continue
		}
		var blocks []struct {
			Type         string          `json:"type"`
			Text         string          `json:"text"`
			CacheControl json.RawMessage `json:"cache_control"`
		}
		if json.Unmarshal(m.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type == "text" && len(b.CacheControl) > 0 && strings.TrimSpace(b.Text) != "" {
				parts = append(parts, "user:"+strings.TrimSpace(b.Text))
				break
			}
		}
		break // the first user message only: later ones move
	}
	if len(parts) == 0 {
		return ""
	}
	return msgHashKey("cache|" + strings.Join(parts, "\n"))
}

func msgHashKey(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return "cc-" + hex.EncodeToString(sum[:16])
}

// msgParseUserID reads Claude Code's metadata.user_id: JSON
// {"device_id","account_uuid","session_id"} (2.1.78 on) or the older
// user_<device>_account_<uuid>_session_<uuid>.
func msgParseUserID(raw string) (device, account, session string) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "{") {
		var j struct {
			DeviceID    string `json:"device_id"`
			AccountUUID string `json:"account_uuid"`
			SessionID   string `json:"session_id"`
		}
		if json.Unmarshal([]byte(raw), &j) != nil {
			return "", "", ""
		}
		return j.DeviceID, j.AccountUUID, j.SessionID
	}
	rest, ok := strings.CutPrefix(raw, "user_")
	if !ok {
		return "", "", ""
	}
	dev, rest, ok := strings.Cut(rest, "_account_")
	if !ok {
		return "", "", ""
	}
	acct, sess, ok := strings.Cut(rest, "_session_")
	if !ok {
		return "", "", ""
	}
	return dev, acct, sess
}

// msgDataURI is a base64 source as a data URI.
func msgDataURI(src *msgSource, fallbackType string) string {
	if src == nil || src.Data == "" {
		return ""
	}
	mt := src.MediaType
	if mt == "" {
		mt = fallbackType
	}
	return "data:" + mt + ";base64," + src.Data
}

// msgImageURL is the URL (or data URI) of an image block.
func msgImageURL(b msgContent) string {
	if b.Source == nil {
		return ""
	}
	if b.Source.Type == "url" {
		return b.Source.URL
	}
	return msgDataURI(b.Source, "image/png")
}

// msgDocumentText is the text of a plain-text document (source type text or
// content), with its title; ok is false for a file (PDF) document.
func msgDocumentText(b msgContent) (string, bool) {
	if b.Source == nil {
		return "", false
	}
	var text string
	switch b.Source.Type {
	case "text":
		text = b.Source.Data
	case "content":
		blocks, _ := msgBlocks(b.Source.Content)
		var parts []string
		for _, c := range blocks {
			if c.Type == "text" && c.Text != "" {
				parts = append(parts, c.Text)
			}
		}
		text = strings.Join(parts, "\n\n")
	default:
		return "", false
	}
	if b.Title != "" {
		text = b.Title + "\n\n" + text
	}
	return text, true
}

// msgDocumentName is a file name for a document block.
func msgDocumentName(b msgContent) string {
	if b.Title != "" {
		return b.Title
	}
	return "document.pdf"
}

// msgToolResult splits a tool_result's content into the text the tool call's
// output carries and the images (and file documents) that go to the model in
// a user message after it, as an OpenAI tool output is text.
func msgToolResult(b msgContent) (string, []msgContent) {
	blocks, _ := msgBlocks(b.Content)
	var texts []string
	var media []msgContent
	for _, c := range blocks {
		switch c.Type {
		case "text":
			if c.Text != "" {
				texts = append(texts, c.Text)
			}
		case "image":
			media = append(media, c)
		case "document":
			if t, ok := msgDocumentText(c); ok {
				texts = append(texts, t)
			} else {
				media = append(media, c)
			}
		case "tool_reference":
			// A tool found by a tool search; its definition is in the tools.
			texts = append(texts, "Tool loaded: "+c.ToolName)
		case "search_result":
			if t, ok := msgSearchResultText(c); ok {
				texts = append(texts, t)
			}
		}
	}
	text := strings.Join(texts, "\n\n")
	if text == "" {
		text = "(empty)"
	}
	if b.IsError {
		text = "Error: " + text
	}
	return text, media
}

// msgSearchResultText renders a search_result block as text.
func msgSearchResultText(c msgContent) (string, bool) {
	var parts []string
	if c.Title != "" {
		parts = append(parts, c.Title)
	}
	inner, _ := msgBlocks(c.Content)
	for _, t := range inner {
		if t.Type == "text" && t.Text != "" {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, "\n"), len(parts) > 0
}

// msgPairs counts the tool calls that can be sent: a tool_use needs a
// tool_result and a tool_result its tool_use (OpenAI upstreams reject either
// alone). Histories that lost one of them (trimmed or interrupted
// conversations) still convert. Only a user turn's results count (the
// converters read them nowhere else). An ID a history uses again pairs in
// order of appearance: as many of its calls as it has results, and as many
// results as calls; msgTake takes them one by one.
func msgPairs(msgs []msgMessage) (uses, results map[string]int) {
	uses, results = map[string]int{}, map[string]int{}
	for _, m := range msgs {
		blocks, _ := msgBlocks(m.Content)
		for _, b := range blocks {
			switch {
			case b.Type == "tool_use" && m.Role == "assistant" && b.ID != "":
				uses[b.ID]++
			case b.Type == "tool_result" && m.Role != "assistant" && m.Role != "system" && b.ToolUseID != "":
				results[b.ToolUseID]++
			}
		}
	}
	for id, n := range uses {
		n = min(n, results[id])
		uses[id], results[id] = n, n
	}
	for id := range results {
		if uses[id] == 0 {
			results[id] = 0
		}
	}
	return uses, results
}

// msgTake reports whether one more block with id pairs, and counts it.
func msgTake(left map[string]int, id string) bool {
	if left[id] == 0 {
		return false
	}
	left[id]--
	return true
}

// msgToolArgs is a tool_use input as function call arguments.
func msgToolArgs(input json.RawMessage) string {
	s := strings.TrimSpace(string(input))
	if s == "" || s == "null" {
		return "{}"
	}
	return s
}
