package node

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"sharetoken/internal/apicompat"
	"sharetoken/internal/protocol"
)

// call is how a job reaches its model server: as the buyer sent it when the
// server speaks the request's API, converted (package apicompat) when it
// does not.
type call struct {
	req *Request
	tr  apicompat.Translator // converts the server's events back; nil passes them on
	// compact: a Codex compact request. native sends it to the server's
	// compact endpoint; otherwise summarize asks the model for a summary in
	// a normal request and answers with a compaction item holding it.
	native    bool
	summarize bool
}

// plan decides how prep reaches up. model is the marketplace model ID, the
// name the buyer sees in converted events.
func plan(prep *Prepared, up Upstream, model string) (*call, error) {
	req := prep.Request
	c := &call{}
	if req.Op == protocol.OpCompact {
		if _, ok := up.(Compactor); ok && slices.Contains(up.APIs(), protocol.APIResponses) {
			c.native = true
			c.req = &req
			return c, nil
		}
		body, err := compactRequest(req.Body)
		if err != nil {
			return nil, protocol.Errorf(protocol.CodeBadRequest, "compact request: %v", err)
		}
		req.Body, req.Op, c.summarize = body, "", true
	}
	if slices.Contains(up.APIs(), req.API) {
		c.req = &req
		return c, nil
	}
	target, ok := apicompat.Target(req.API, up.APIs())
	if !ok {
		return nil, protocol.Errorf(protocol.CodeBadRequest, "this node cannot serve the %s API", req.API)
	}
	conv, err := apicompat.Convert(req.API, target, req.Body, apicompat.Options{Model: model})
	if err != nil {
		return nil, protocol.Errorf(protocol.CodeBadRequest, "cannot convert the request from %s to %s: %v", req.API, target, err)
	}
	c.req = &Request{API: target, Header: req.Header, Body: conv.Body, Limit: req.Limit}
	c.tr = conv.Translator
	return c, nil
}

// run sends the call and writes the events in the buyer's API to emit.
func (c *call) run(ctx context.Context, up Upstream, emit func([]byte) error) error {
	out := emit
	var sum *summarizer
	if c.summarize {
		sum = &summarizer{emit: emit}
		out = sum.event
	}
	if c.native {
		return up.(Compactor).Compact(ctx, c.req, out)
	}
	send := out
	if c.tr != nil {
		send = func(p []byte) error { return c.tr.Event(p, out) }
	}
	if err := up.Stream(ctx, c.req, send); err != nil {
		return err
	}
	if c.tr != nil {
		if err := c.tr.Close(out); err != nil {
			return err
		}
	}
	if sum != nil {
		return sum.finish()
	}
	return nil
}

// compactRequest turns a Codex compact request into a normal Responses
// request that asks the model for a summary of the conversation.
func compactRequest(body []byte) ([]byte, error) {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	var input []any
	switch v := req["input"].(type) {
	case []any:
		input = v
	case string:
		input = []any{userMessage(v)}
	case map[string]any:
		input = []any{v}
	case nil:
	default:
		return nil, errors.New("input must be a string, an item or a list of items")
	}
	req["input"] = append(input, userMessage(apicompat.CompactPrompt))
	if tools, ok := req["tools"].([]any); ok && len(tools) > 0 {
		req["tool_choice"] = "none"
	}
	req["stream"] = true
	return json.Marshal(req)
}

func userMessage(text string) map[string]any {
	return map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": text}}}
}

// summarizer passes a summary request's events on, but answers with a
// compaction item that holds the summary instead of the summary message.
type summarizer struct {
	emit func([]byte) error
	text strings.Builder
	done bool
}

func (s *summarizer) event(payload []byte) error {
	var ev struct {
		Type     string         `json:"type"`
		Delta    string         `json:"delta"`
		Response map[string]any `json:"response"`
	}
	if json.Unmarshal(payload, &ev) != nil {
		return s.emit(payload)
	}
	switch ev.Type {
	case "response.output_text.delta":
		s.text.WriteString(ev.Delta)
	case "response.completed", "response.incomplete", "response.done":
		if ev.Response == nil {
			ev.Response = map[string]any{}
		}
		if s.text.Len() == 0 {
			s.text.WriteString(outputText(ev.Response["output"]))
		}
		summary := strings.TrimSpace(s.text.String())
		if i := strings.Index(summary, "<summary>"); i >= 0 {
			summary = strings.TrimSpace(strings.TrimSuffix(summary[i+len("<summary>"):], "</summary>"))
		}
		if summary == "" {
			return errors.New("the model returned an empty summary")
		}
		r := ev.Response
		r["output"] = []any{map[string]any{
			"id": "cmp_" + randomID(), "type": "compaction", "status": "completed",
			"encrypted_content": apicompat.EncodeCompaction(summary),
		}}
		r["status"] = "completed"
		delete(r, "incomplete_details")
		delete(r, "output_text")
		b, err := json.Marshal(map[string]any{"type": "response.completed", "sequence_number": seqOf(payload), "response": r})
		if err != nil {
			return err
		}
		s.done = true
		return s.emit(b)
	}
	return s.emit(payload)
}

func (s *summarizer) finish() error {
	if !s.done {
		return errors.New("the summary request ended without its response")
	}
	return nil
}

func outputText(output any) string {
	var b strings.Builder
	items, _ := output.([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m["type"] != "message" {
			continue
		}
		parts, _ := m["content"].([]any)
		for _, p := range parts {
			pm, _ := p.(map[string]any)
			if t, ok := pm["text"].(string); ok {
				b.WriteString(t)
			}
		}
	}
	return b.String()
}

func seqOf(payload []byte) int64 {
	var v struct {
		Seq int64 `json:"sequence_number"`
	}
	_ = json.Unmarshal(payload, &v)
	return v.Seq
}

func randomID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// requestSession names the conversation a request belongs to, for model
// server adapters that tie a conversation to an upstream session: a chat
// request's session_id, Claude Code's session (metadata.user_id), Codex's
// prompt_cache_key or session_id header.
func requestSession(api string, header protocol.RequestHeader, body []byte) (string, error) {
	var head struct {
		SessionID      string `json:"session_id"`
		PromptCacheKey string `json:"prompt_cache_key"`
		Metadata       struct {
			UserID string `json:"user_id"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(body, &head); err != nil {
		return "", protocol.Errorf(protocol.CodeBadRequest, "request is not a JSON object")
	}
	var s string
	switch api {
	case protocol.APIMessages:
		s = header.Get("x-claude-code-session-id")
		if s == "" {
			s = ClaudeCodeSession(head.Metadata.UserID)
		}
	case protocol.APIResponses:
		s = head.PromptCacheKey
		if s == "" {
			s = header.Get("session_id")
		}
	default:
		s = head.SessionID
	}
	if len(s) > 256 {
		return "", protocol.Errorf(protocol.CodeBadRequest, "session_id must be a string of at most 256 bytes")
	}
	return s, nil
}

// ClaudeCodeSession reads the session of Claude Code's metadata.user_id: a
// JSON object with session_id in current versions, "…_session_<id>" in
// older ones.
func ClaudeCodeSession(userID string) string {
	var v struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal([]byte(userID), &v) == nil && v.SessionID != "" {
		return v.SessionID
	}
	if _, after, ok := strings.Cut(userID, "_session_"); ok {
		return after
	}
	return ""
}
