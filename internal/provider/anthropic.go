package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"sharetoken/internal/apicompat"
	"sharetoken/internal/node"
	"sharetoken/internal/protocol"
)

// Anthropic speaks the Messages API natively: a Messages request goes out as
// it came (with the adaptations an API key or a subscription login needs),
// and its events come back as they are. Requests in the other APIs reach it
// converted by the node (package apicompat).

const anthropicVersion = "2023-06-01"

// claudeCodeIdentity is the identity line used by both OAuth request profiles.
const claudeCodeIdentity = "You are Claude Code, Anthropic's official CLI for Claude."

type anthropicUpstream struct {
	base   string // without /v1
	limits outputLimits
	// API-key path: header carries the key. OAuth path: ts gives the bearer
	// token; oauth selects the configured OAuth request profile.
	header http.Header
	ts     TokenSource
	oauth  bool
	entry  *Entry
}

func (u *anthropicUpstream) APIs() []string { return []string{protocol.APIMessages} }

func (u *anthropicUpstream) Stream(ctx context.Context, r *node.Request, emit func([]byte) error) error {
	if r.API != protocol.APIMessages {
		return fmt.Errorf("anthropic: cannot send a %s request", r.API)
	}
	head, err := decodeObject(r.Body)
	if err != nil {
		return &node.StatusError{Status: http.StatusBadRequest, Body: err.Error()}
	}
	responseModel, _ := head["model"].(string)
	sampling := ""
	if u.entry != nil && u.entry.Anthropic != nil {
		sampling = u.entry.Anthropic.Sampling
	}
	profile := u.entry != nil && u.entry.claudeProfile()
	endpoint, err := anthropicMessagesURL(u.base, profile)
	if err != nil {
		return err
	}
	// The seller pays for instructions a subscription login has to add to a
	// request that is not Claude Code's own.
	if u.oauth && !isClaudeCodeRequest(head) {
		if est, err := protocol.EstimatePromptTokens(protocol.APIMessages, r.Body); err == nil {
			node.BillingOf(ctx).PromptCap = est
		}
	}
	body := r.Body
	err = u.limits.retry(func() error {
		send := func(tok *Token) error {
			// A fresh copy each time: the adaptations below change it.
			req, err := decodeObject(body)
			if err != nil {
				return &node.StatusError{Status: http.StatusBadRequest, Body: err.Error()}
			}
			if u.oauth {
				if model := claudeOAuthModelIDs[responseModel]; model != "" {
					req["model"] = model
				}
			}
			model, _ := req["model"].(string)
			dropForeignThinking(req)
			if err := validateAnthropicRequest(req, model, sampling); err != nil {
				return &node.StatusError{Status: http.StatusBadRequest, Body: err.Error()}
			}
			limit := u.limits.of(responseModel)
			if limit == 0 {
				limit = anthropicMaxOutput(model)
			}
			capOutput(req, limit)
			h := u.header.Clone()
			if u.oauth {
				h = anthropicOAuthHeader(tok)
				if profile {
					h, err = applyClaudeOAuthProfile(ctx, req, u.entry, tok)
					if err != nil {
						return err
					}
				} else {
					req["system"] = claudeCodeSystem(req["system"])
				}
			}
			if h == nil {
				h = http.Header{}
			}
			mergeBetas(h, r.Header.Get("anthropic-beta"))
			version := r.Header.Get("anthropic-version")
			if version == "" {
				version = anthropicVersion
			}
			h.Set("anthropic-version", version)
			normalizeAnthropicCache(req)
			out, err := json.Marshal(req)
			if err != nil {
				return err
			}
			done := false
			err = postSSE(ctx, endpoint, h, out, func(data []byte) error {
				typ, msg := eventType(data)
				switch typ {
				case "error":
					status := http.StatusBadGateway
					if strings.Contains(msg, "overloaded") {
						status = http.StatusServiceUnavailable
					}
					return &node.StatusError{Status: status, Body: clip(msg, 300)}
				case "message_stop":
					done = true
				}
				return emit(append([]byte(nil), data...))
			})
			if err == nil && !done {
				err = fmt.Errorf("the stream ended before the message did")
			}
			return err
		}
		if u.ts != nil {
			return withToken(ctx, u.ts, send)
		}
		return send(nil)
	}, r.Body)
	// A conversation that moved from another seller's account may carry
	// thinking signed for that account: once refused, it goes without.
	var se *node.StatusError
	if errors.As(err, &se) && se.Status == http.StatusBadRequest && strings.Contains(strings.ToLower(se.Body), "signature") {
		if stripped, ok := withoutThinking(r.Body); ok {
			body = stripped
			return u.Stream(ctx, &node.Request{API: r.API, Header: r.Header, Body: body, Op: r.Op, Limit: r.Limit}, emit)
		}
	}
	return err
}

// withoutThinking removes every thinking block from a Messages request's
// history (and the thinking setting, which a tool loop without its thinking
// would make the API refuse).
func withoutThinking(body []byte) ([]byte, bool) {
	req, err := decodeObject(body)
	if err != nil {
		return nil, false
	}
	changed := false
	for _, m := range messageList(req) {
		blocks, ok := m["content"].([]any)
		if !ok {
			continue
		}
		out := blocks[:0]
		for _, raw := range blocks {
			if b, _ := raw.(map[string]any); b["type"] == "thinking" || b["type"] == "redacted_thinking" {
				changed = true
				continue
			}
			out = append(out, raw)
		}
		if len(out) == 0 {
			out = append(out, map[string]any{"type": "text", "text": "(empty)"})
		}
		m["content"] = out
	}
	if !changed {
		return nil, false
	}
	delete(req, "thinking")
	b, err := json.Marshal(req)
	return b, err == nil
}

// dropForeignThinking removes thinking blocks from the history that no
// Anthropic model signed: those another upstream's reasoning was carried in
// (apicompat), and those a chat history gave back without a signature. The
// API refuses them; the model's own stay, as it needs them during a tool loop.
func dropForeignThinking(req map[string]any) {
	for _, m := range messageList(req) {
		blocks, ok := m["content"].([]any)
		if !ok {
			continue
		}
		out := blocks[:0]
		for _, raw := range blocks {
			b, _ := raw.(map[string]any)
			switch b["type"] {
			case "thinking":
				sig, _ := b["signature"].(string)
				if sig == "" || apicompat.IsConverted(sig) {
					continue
				}
			case "redacted_thinking":
				if d, _ := b["data"].(string); d == "" {
					continue
				}
			}
			out = append(out, raw)
		}
		if len(out) == 0 {
			out = append(out, map[string]any{"type": "text", "text": "(empty)"})
		}
		m["content"] = out
	}
}

// decodeObject decodes a JSON object keeping numbers exact.
func decodeObject(body []byte) (map[string]any, error) {
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	var m map[string]any
	if err := d.Decode(&m); err != nil || m == nil {
		return nil, fmt.Errorf("the request is not a JSON object")
	}
	return m, nil
}

// eventType reads an event's type and, for an error event, its message.
func eventType(data []byte) (typ, msg string) {
	var ev struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
		Message  string `json:"message"`
		Response struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"response"`
	}
	if json.Unmarshal(data, &ev) != nil {
		return "", ""
	}
	msg = strings.TrimSpace(strings.Join([]string{ev.Error.Type, ev.Error.Code, ev.Error.Message, ev.Message, ev.Response.Error.Code, ev.Response.Error.Message}, " "))
	return ev.Type, msg
}

// mergeBetas adds the buyer's betas (already filtered by the node) to those
// h asks for.
func mergeBetas(h http.Header, client string) {
	var all []string
	seen := map[string]bool{}
	for _, list := range []string{h.Get("anthropic-beta"), client} {
		for _, b := range strings.Split(list, ",") {
			if b = strings.TrimSpace(b); b != "" && !seen[b] {
				seen[b] = true
				all = append(all, b)
			}
		}
	}
	if len(all) > 0 {
		h.Set("anthropic-beta", strings.Join(all, ","))
	}
}

// capOutput lowers max_tokens to the model's limit, and a thinking budget
// below it (Anthropic wants budget_tokens < max_tokens).
func capOutput(req map[string]any, limit uint64) {
	maxTokens := number(req["max_tokens"])
	if maxTokens == 0 || maxTokens > limit {
		maxTokens = limit
	}
	req["max_tokens"] = maxTokens
	if th, ok := req["thinking"].(map[string]any); ok && th["type"] == "enabled" {
		if b := number(th["budget_tokens"]); b >= maxTokens {
			if maxTokens <= 1024 {
				delete(req, "thinking")
			} else {
				th["budget_tokens"] = maxTokens - 1
			}
		}
	}
}

func number(v any) uint64 {
	switch n := v.(type) {
	case json.Number:
		i, err := n.Int64()
		if err != nil || i < 0 {
			f, _ := n.Float64()
			return uint64(max(f, 0))
		}
		return uint64(i)
	case float64:
		return uint64(max(n, 0))
	case uint64:
		return n
	case int:
		return uint64(max(n, 0))
	}
	return 0
}

// isClaudeCodeRequest reports whether the request is Claude Code's own (its
// system prompt opens with Claude Code's billing line).
func isClaudeCodeRequest(req map[string]any) bool {
	blocks, _ := req["system"].([]any)
	if len(blocks) == 0 {
		return false
	}
	b, _ := blocks[0].(map[string]any)
	text, _ := b["text"].(string)
	return strings.HasPrefix(text, "x-anthropic-billing-header:")
}

// claudeOAuthModelIDs gives the dated ID of the short model names a
// subscription login does not take.
var claudeOAuthModelIDs = map[string]string{
	"claude-sonnet-4-5": "claude-sonnet-4-5-20250929",
	"claude-opus-4-5":   "claude-opus-4-5-20251101",
	"claude-haiku-4-5":  "claude-haiku-4-5-20251001",
}

// claudeCodeSystem puts the Claude Code identity line before the request's
// system prompt (a string or blocks, or none), unless it is there already.
func claudeCodeSystem(system any) []any {
	id := map[string]any{"type": "text", "text": claudeCodeIdentity}
	var blocks []any
	switch s := system.(type) {
	case string:
		if s != "" {
			blocks = []any{map[string]any{"type": "text", "text": s}}
		}
	case []any:
		blocks = s
	}
	for _, raw := range blocks {
		if b, ok := raw.(map[string]any); ok && b["text"] == claudeCodeIdentity {
			return blocks
		}
	}
	return append([]any{id}, blocks...)
}

// anthropicMaxOutput is the output limit of a Claude model, for those that
// refuse a larger one.
func anthropicMaxOutput(model string) uint64 {
	switch {
	case strings.Contains(model, "claude-3-haiku"), strings.Contains(model, "claude-3-opus"), strings.Contains(model, "claude-3-sonnet"):
		return 4096
	case strings.Contains(model, "claude-3-5"):
		return 8192
	case strings.Contains(model, "opus-4-0"), strings.Contains(model, "opus-4-1"), strings.Contains(model, "opus-4-2025"):
		return 32000
	}
	return 64000 // Sonnet 4.x and later models; the node asks for no more than the reservation covers
}

func anthropicModels(ctx context.Context, base string, header http.Header) ([]string, error) {
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	header.Set("anthropic-version", anthropicVersion)
	if err := getJSON(ctx, base+"/v1/models?limit=1000", header, &out); err != nil {
		return nil, err
	}
	ids := []string{}
	for _, m := range out.Data {
		ids = append(ids, m.ID)
	}
	return ids, nil
}
