package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
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
	// passback: the server is another vendor's Messages endpoint (DeepSeek's),
	// which signs no thinking and wants every thinking block back as it was.
	passback bool
	// shapes: what each model refused before (anthropic_shapes.go)
	shapes anthropicShapes
}

func (u *anthropicUpstream) APIs() []string { return []string{protocol.APIMessages} }

func (u *anthropicUpstream) Stream(ctx context.Context, r *node.Request, emit func([]byte) error) error {
	return u.stream(ctx, r, emit, 0)
}

// stream sends r; fixes counts the times it was sent again in a form the
// model server asked for in a refusal.
func (u *anthropicUpstream) stream(ctx context.Context, r *node.Request, emit func([]byte) error, fixes int) error {
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
	injected := u.oauth && !isClaudeCodeRequest(head)
	// A tool exchange that does not pair is mended before it is sent
	// (anthropic_tool_pairs.go); one that pairs goes as it came.
	body := pairedToolBlocks(r.Body)
	// The request as last sent, for learning from a refusal.
	var sentModel string
	var sentReq map[string]any
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
			if !u.passback {
				dropForeignThinking(req)
			}
			dropEmptyText(req)
			// Thinking off in the form the model takes, and without what it
			// refused before.
			apicompat.ClaudeThinkingOffAsTaken(req, model)
			u.shapes.apply(model, req)
			// Images as Claude takes them; another vendor's endpoint has
			// sides of its own, and only its cap is learnt.
			fitClaudeImages(req, u.shapes.imageCap(model), !u.passback)
			if err := validateAnthropicRequest(req, model, sampling); err != nil {
				return &node.StatusError{Status: http.StatusBadRequest, Body: err.Error()}
			}
			limit := u.limits.of(responseModel)
			if limit == 0 {
				limit = anthropicMaxOutput(model)
			}
			capOutput(req, limit)
			// The request as the buyer's, before the login's own instructions.
			var plain []byte
			if injected {
				plain, _ = json.Marshal(req)
			}
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
			betas := buyerBetas(r.Header.Get("anthropic-beta"), model, u.oauth, u.oauth && profile && injected)
			mergeBetas(h, strings.Join(betas, ","))
			dropToolsetStreamingBeta(h, req, model)
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
			sentModel, sentReq = model, nil
			_ = json.Unmarshal(out, &sentReq)
			if injected {
				node.BillingOf(ctx).PromptDiscount = injectedPrompt(plain, out)
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
	var se *node.StatusError
	// Newer models think adaptively and refuse a budget, older ones the
	// reverse: a request in the other form (a client that does not know the
	// model, a conversion) is sent once more in the one asked for.
	// A client that takes the model for a newer one may send what it does
	// not take: mid-conversation system messages go into the user turns.
	if errors.As(err, &se) && se.Status == http.StatusBadRequest && fixes < 3 {
		// What the model refused is learnt and left out from now on: the
		// same request goes again, sent without it.
		if sentReq != nil && u.shapes.learn(sentModel, se.Body, sentReq) {
			return u.stream(ctx, r, emit, fixes+1)
		}
		fixed, ok := swapThinking(r.Body, se.Body)
		if !ok {
			fixed, ok = systemToUser(r.Body, se.Body)
		}
		if !ok && asksBetweenTools.MatchString(se.Body) {
			fixed, ok = withBetweenTools(r.Body)
		}
		if ok {
			return u.stream(ctx, &node.Request{API: r.API, Header: r.Header, Body: fixed, Op: r.Op, Limit: r.Limit}, emit, fixes+1)
		}
	}
	// A conversation that moved from another seller's account may carry
	// thinking signed for that account, or lack the thinking this model
	// expects before a tool call: once refused, it goes without thinking.
	if errors.As(err, &se) && se.Status == http.StatusBadRequest && !u.passback && thinkingRefused(se.Body) {
		if stripped, ok := withoutThinking(r.Body); ok {
			return u.stream(ctx, &node.Request{API: r.API, Header: r.Header, Body: stripped, Op: r.Op, Limit: r.Limit}, emit, 2)
		}
	}
	return err
}

// thinkingRefused reports whether a refusal is about the thinking blocks of
// the history (the cases sub2api retries without thinking):
//
//	"Invalid `signature` in `thinking` block"
//	"Expected `thinking` or `redacted_thinking`, but found `tool_use`"
//	"`thinking` or `redacted_thinking` blocks in the latest assistant message cannot be modified"
func thinkingRefused(refusal string) bool {
	msg := strings.ToLower(refusal)
	switch {
	case strings.Contains(msg, "signature"), strings.Contains(msg, "thinking block must contain"):
		return true
	case strings.Contains(msg, "thinking"):
		return strings.Contains(msg, "expected") || strings.Contains(msg, "cannot be modified")
	}
	return false
}

// dropEmptyText removes the empty text blocks of the history (those inside
// tool results too): clients and conversions leave them behind, and the API
// refuses a request for one ("text content blocks must be non-empty").
func dropEmptyText(req map[string]any) {
	var clean func(blocks []any) []any
	clean = func(blocks []any) []any {
		out := blocks[:0]
		for _, raw := range blocks {
			b, _ := raw.(map[string]any)
			if text, isText := b["text"].(string); b["type"] == "text" && isText && text == "" {
				continue
			}
			if nested, ok := b["content"].([]any); ok && b["type"] == "tool_result" {
				b["content"] = clean(nested)
			}
			out = append(out, raw)
		}
		return out
	}
	for _, m := range messageList(req) {
		blocks, ok := m["content"].([]any)
		if !ok {
			continue
		}
		if blocks = clean(blocks); len(blocks) == 0 {
			blocks = append(blocks, map[string]any{"type": "text", "text": "(empty)"})
		}
		m["content"] = blocks
	}
}

// injectedPrompt estimates the prompt tokens the adapter added to the
// buyer's request: the estimate of what goes out over that of what came,
// and half as much again, as the estimate runs below what Claude counts for
// instructions in prose. The node takes it off the prompt it bills.
func injectedPrompt(buyer, sent []byte) uint64 {
	before, err := protocol.EstimatePromptTokens(protocol.APIMessages, buyer)
	if err != nil {
		return 0
	}
	after, err := protocol.EstimatePromptTokens(protocol.APIMessages, sent)
	if err != nil || after <= before {
		return 0
	}
	return (after - before) * 3 / 2
}

// swapThinking rewrites a request's thinking into the form the model server
// asked for in its refusal: adaptive with an effort instead of a budget, or a
// budget instead of adaptive.
func swapThinking(body []byte, refusal string) ([]byte, bool) {
	toAdaptive := strings.Contains(refusal, `thinking.type.enabled`) && strings.Contains(refusal, "not supported")
	toBudget := strings.Contains(refusal, `thinking.type.adaptive`) && strings.Contains(refusal, "not supported")
	if !toAdaptive && !toBudget {
		return nil, false
	}
	req, err := decodeObject(body)
	if err != nil {
		return nil, false
	}
	th, _ := req["thinking"].(map[string]any)
	switch {
	case toAdaptive && th != nil && th["type"] == "enabled":
		budget := number(th["budget_tokens"])
		effort := "high"
		switch {
		case budget <= 2048:
			effort = "low"
		case budget <= 8192:
			effort = "medium"
		}
		req["thinking"] = map[string]any{"type": "adaptive"}
		oc, _ := req["output_config"].(map[string]any)
		if oc == nil {
			oc = map[string]any{}
		}
		if _, ok := oc["effort"]; !ok {
			oc["effort"] = effort
		}
		req["output_config"] = oc
	case toBudget && th != nil && th["type"] == "adaptive":
		effort := "medium"
		if oc, ok := req["output_config"].(map[string]any); ok {
			if e, ok := oc["effort"].(string); ok {
				effort = e
			}
			delete(oc, "effort")
			if len(oc) == 0 {
				delete(req, "output_config")
			}
		}
		budget := map[string]uint64{"low": 1024, "medium": 4096, "high": 10240, "xhigh": 32768, "max": 32768}[effort]
		if budget == 0 {
			budget = 4096
		}
		if mt := number(req["max_tokens"]); mt > 0 && budget >= mt {
			budget = mt - 1
		}
		if budget < 1024 {
			delete(req, "thinking")
		} else {
			req["thinking"] = map[string]any{"type": "enabled", "budget_tokens": budget}
		}
	default:
		return nil, false
	}
	b, err := json.Marshal(req)
	return b, err == nil
}

// systemToUser answers a refusal of role "system" in messages: each such
// message becomes text of the user turn it joins, marked as Claude Code marks
// system reminders for models that take none.
func systemToUser(body []byte, refusal string) ([]byte, bool) {
	if !strings.Contains(refusal, "role 'system'") && !strings.Contains(refusal, `role "system"`) {
		return nil, false
	}
	req, err := decodeObject(body)
	if err != nil {
		return nil, false
	}
	var out []map[string]any
	changed := false
	for _, m := range messageList(req) {
		if m["role"] == "system" {
			changed = true
			var text strings.Builder
			if s, ok := m["content"].(string); ok {
				text.WriteString(s)
			}
			for _, b := range list(m["content"]) {
				if bm, _ := b.(map[string]any); bm["type"] == "text" {
					t, _ := bm["text"].(string)
					text.WriteString(t)
				}
			}
			m = map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "<system-reminder>\n" + text.String() + "\n</system-reminder>"}}}
		}
		if n := len(out); n > 0 && out[n-1]["role"] == m["role"] {
			out[n-1]["content"] = append(contentBlocks(out[n-1]["content"]), contentBlocks(m["content"])...)
			continue
		}
		out = append(out, m)
	}
	if !changed {
		return nil, false
	}
	req["messages"] = out
	b, err := json.Marshal(req)
	return b, err == nil
}

// contentBlocks is a message's content as blocks: a string is one text block.
func contentBlocks(c any) []any {
	if s, ok := c.(string); ok {
		return []any{map[string]any{"type": "text", "text": s}}
	}
	return list(c)
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
	// Thinking that is on with no block in the history is refused as well,
	// when the model expects one before a tool call that has none.
	if _, on := req["thinking"]; !changed && !on {
		return nil, false
	}
	delete(req, "thinking")
	// Context edits that clear thinking need thinking to be on.
	if cm, ok := req["context_management"].(map[string]any); ok {
		if edits, ok := cm["edits"].([]any); ok {
			kept := []any{}
			for _, raw := range edits {
				e, _ := raw.(map[string]any)
				if typ, _ := e["type"].(string); strings.HasPrefix(typ, "clear_thinking") {
					continue
				}
				kept = append(kept, raw)
			}
			cm["edits"] = kept
			if len(kept) == 0 {
				delete(cm, "edits")
			}
			if len(cm) == 0 {
				delete(req, "context_management")
			}
		}
	}
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

// buyerBetas is what of the buyer's betas goes out on the seller's account.
// The node kept the families it knows (node.upstreamHeader); which of them a
// request may carry depends on the account and the model, as in sub2api's
// beta policy:
//
//   - context-1m only goes to the models that have the long context as they
//     are (Sonnet 5 and later). Elsewhere the API refuses it, or bills the
//     long prompt at a rate the ticket's price does not cover.
//   - a subscription login never sends what Claude Code does not send
//     (output-128k, computer-use): the buyer would be switching features on
//     for the seller's account.
//   - a request rebuilt in Claude Code's shape (rebuilt) carries Claude
//     Code's own list, and of the buyer's only the betas that describe the
//     request's format. Among them per-turn-control and
//     mid-conversation-output-config, which let a message carry its own
//     output_config (the effort of one turn): without them the API refuses
//     such a message and the request is sent again without it
//     (anthropic_shapes.go), a refused job the beta saves.
func buyerBetas(client, model string, oauth, rebuilt bool) []string {
	is := func(b string, families ...string) bool {
		return slices.ContainsFunc(families, func(f string) bool { return strings.HasPrefix(b, f+"-20") })
	}
	var out []string
	for _, b := range strings.Split(client, ",") {
		b = strings.TrimSpace(b)
		switch {
		case b == "":
		case is(b, "context-1m") && model != "claude-sonnet-5" && !strings.HasPrefix(model, "claude-sonnet-5-"):
		case oauth && is(b, "output-128k", "computer-use"):
		case rebuilt && !is(b, "structured-outputs", "mid-conversation-tool-changes", "inline-tools", "per-turn-control", "mid-conversation-output-config"):
		default:
			out = append(out, b)
		}
	}
	return out
}

// dropToolsetStreamingBeta takes fine-grained-tool-streaming off a request
// that offers the computer or browser toolset to Sonnet 5.5 or Haiku 5.5:
// the two do not go together, and the API refuses the request.
func dropToolsetStreamingBeta(h http.Header, req map[string]any, model string) {
	if model != "claude-sonnet-5-5" && model != "claude-haiku-5-5" {
		return
	}
	toolset := slices.ContainsFunc(list(req["tools"]), func(t any) bool {
		m, _ := t.(map[string]any)
		return m["type"] == "computer_toolset_20260801" || m["type"] == "browser_toolset_20260801"
	})
	if !toolset {
		return
	}
	var kept []string
	for _, b := range strings.Split(h.Get("anthropic-beta"), ",") {
		if b = strings.TrimSpace(b); b != "" && !strings.HasPrefix(b, "fine-grained-tool-streaming-20") {
			kept = append(kept, b)
		}
	}
	if len(kept) == 0 {
		h.Del("anthropic-beta")
		return
	}
	h.Set("anthropic-beta", strings.Join(kept, ","))
}

// mergeBetas adds the buyer's betas (those buyerBetas kept) to those h asks
// for.
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
