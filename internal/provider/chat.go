package provider

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// chatRequest is the OpenAI chat-completions request a node sends upstream:
// the fields it forwards from the buyer (node.forwardedFields) and those it
// sets itself.
type chatRequest struct {
	Model            string          `json:"model"`
	Messages         []chatMessage   `json:"messages"`
	MaxTokens        uint64          `json:"max_tokens"`
	Temperature      *float64        `json:"temperature"`
	TopP             *float64        `json:"top_p"`
	Stop             json.RawMessage `json:"stop"`
	Tools            []chatTool      `json:"tools"`
	ToolChoice       json.RawMessage `json:"tool_choice"`
	ResponseFormat   json.RawMessage `json:"response_format"`
	Seed             *int64          `json:"seed"`
	User             string          `json:"user"`
	PresencePenalty  *float64        `json:"presence_penalty"`
	FrequencyPenalty *float64        `json:"frequency_penalty"`
}

type chatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"` // a string, or parts
	Name       string          `json:"name"`
	ToolCalls  []chatToolCall  `json:"tool_calls"`
	ToolCallID string          `json:"tool_call_id"`
}

type chatToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

// chatPart is one part of a message's content.
type chatPart struct {
	Type     string `json:"type"` // text, image_url
	Text     string `json:"text"`
	ImageURL struct {
		URL string `json:"url"`
	} `json:"image_url"`
}

func parseChat(body []byte) (*chatRequest, error) {
	var r chatRequest
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("not a chat-completions request: %w", err)
	}
	return &r, nil
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
	_ = json.Unmarshal(m.Content, &ps)
	return ps
}

// text is the text of a message's content, without its images.
func (m *chatMessage) text() string {
	var b strings.Builder
	for _, p := range m.parts() {
		if p.Type == "text" || p.Type == "" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
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

// toolChoice reads tool_choice: mode is "auto", "none" or "required", or
// name is the one function to call.
func (r *chatRequest) toolChoice() (mode, name string) {
	if len(r.ToolChoice) == 0 || string(r.ToolChoice) == "null" {
		return "", ""
	}
	if json.Unmarshal(r.ToolChoice, &mode) == nil {
		return mode, ""
	}
	var obj struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	_ = json.Unmarshal(r.ToolChoice, &obj)
	return "", obj.Function.Name
}

// jsonSchema reads response_format: wantJSON for json_object and json_schema,
// and the schema of the latter.
func (r *chatRequest) jsonSchema() (wantJSON bool, name string, schema json.RawMessage) {
	var rf struct {
		Type       string `json:"type"`
		JSONSchema struct {
			Name   string          `json:"name"`
			Schema json.RawMessage `json:"schema"`
		} `json:"json_schema"`
	}
	if len(r.ResponseFormat) == 0 || json.Unmarshal(r.ResponseFormat, &rf) != nil {
		return false, "", nil
	}
	switch rf.Type {
	case "json_object":
		return true, "", nil
	case "json_schema":
		return true, rf.JSONSchema.Name, rf.JSONSchema.Schema
	}
	return false, "", nil
}

// dataURL splits a data: URL into its media type and base64 payload.
func dataURL(u string) (mediaType, data string, ok bool) {
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

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// rawObject parses JSON that must be an object, for tool arguments a model
// wrote: anything else becomes an empty object.
func rawObject(s string) json.RawMessage {
	var m map[string]any
	if json.Unmarshal([]byte(s), &m) != nil || m == nil {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(s)
}
