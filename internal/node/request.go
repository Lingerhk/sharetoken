package node

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"sharetoken/internal/protocol"
)

// HardMaxOutputTokens caps any single chat job regardless of budget (see
// protocol.HardMaxOutput for the others).
const HardMaxOutputTokens = 32768

// The fields of each API's request a node passes upstream. Everything else is
// dropped, so a buyer cannot switch the model, multiply work (n, best_of),
// or enable server features the node did not price (hosted tools, stored
// responses, priority tiers). model, stream and the output limit are set by
// the node.
var (
	chatFields = fieldSet("messages", "temperature", "top_p", "stop", "presence_penalty", "frequency_penalty",
		"seed", "response_format", "tools", "tool_choice", "user", "parallel_tool_calls", "reasoning_effort")
	messagesFields = fieldSet("messages", "system", "tools", "tool_choice", "thinking", "temperature", "top_p", "top_k",
		"stop_sequences", "metadata", "context_management", "output_config")
	responsesFields = fieldSet("input", "instructions", "tools", "tool_choice", "parallel_tool_calls", "reasoning", "text",
		"include", "prompt_cache_key", "temperature", "top_p")
)

func fieldSet(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// Request is one request to a model server.
type Request struct {
	API    string                 // the API Body is in
	Header protocol.RequestHeader // the buyer's headers that matter to the model server
	Body   []byte                 // asks for a stream, with the model and output limit the node set
	Op     string                 // protocol.OpCompact, or empty for a plain request
	// Limit is the output-token ceiling of the job (Body carries it too,
	// where the API has a field for it).
	Limit uint64
}

// Prepared is a buyer's request made ready for the model server.
type Prepared struct {
	Request
	EstPrompt uint64 // the prompt estimate of the buyer's request
}

// UpstreamRequest is the exact request an honest node sends its model server
// for ticket t and the buyer's decrypted request (header and body), in the
// ticket's API, plus the prompt estimate it used. Output is capped against a
// padded prompt estimate, so chat-template overhead cannot push the real cost
// past the buyer's reservation. Auditors replay it against a reference copy
// of the model.
func UpstreamRequest(t *protocol.Ticket, header protocol.RequestHeader, body []byte, upstreamModel string) (*Prepared, error) {
	api := protocol.NormalAPI(t.API)
	if !protocol.ValidAPI(api) {
		return nil, protocol.Errorf(protocol.CodeBadRequest, "unknown API %q", t.API)
	}
	est, err := protocol.EstimatePromptTokens(api, body)
	if err != nil {
		return nil, protocol.Errorf(protocol.CodeBadRequest, "request is not a %s body", api)
	}
	op := header.Get(protocol.HeaderOp)
	if op != "" && (op != protocol.OpCompact || api != protocol.APIResponses) {
		return nil, protocol.Errorf(protocol.CodeBadRequest, "unknown operation %q", op)
	}
	limit := protocol.OutputLimit(t, est, protocol.RequestedOutput(api, body))
	if limit == 0 {
		return nil, protocol.Errorf(protocol.CodeBudget, "reservation does not cover the prompt; raise the budget")
	}
	b, err := prepareBody(api, body, upstreamModel, limit)
	if err != nil {
		return nil, err
	}
	return &Prepared{Request: Request{API: api, Header: upstreamHeader(api, header), Body: b, Op: op, Limit: limit}, EstPrompt: est}, nil
}

// prepareBody applies the API's field policy to a buyer's request body and
// sets the model, the stream and the output limit.
func prepareBody(api string, body []byte, upstreamModel string, limit uint64) ([]byte, error) {
	var in map[string]json.RawMessage
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, protocol.Errorf(protocol.CodeBadRequest, "request is not a JSON object")
	}
	var (
		out map[string]any
		err error
	)
	switch api {
	case protocol.APIChat:
		out, err = prepareChat(in, limit)
	case protocol.APIMessages:
		out, err = prepareMessages(in, limit)
	case protocol.APIResponses:
		out, err = prepareResponses(in, limit)
	default:
		return nil, protocol.Errorf(protocol.CodeBadRequest, "unknown API %q", api)
	}
	if err != nil {
		return nil, err
	}
	out["model"] = upstreamModel
	out["stream"] = true
	return json.Marshal(out)
}

// pick copies the allowed fields.
func pick(in map[string]json.RawMessage, allowed map[string]bool) map[string]any {
	out := make(map[string]any, len(allowed)+4)
	for k, v := range in {
		if allowed[k] && string(v) != "null" {
			out[k] = v
		}
	}
	return out
}

func required(in map[string]json.RawMessage, field string) error {
	if raw, ok := in[field]; !ok || len(raw) == 0 || string(raw) == "null" {
		return protocol.Errorf(protocol.CodeBadRequest, "%s is required", field)
	}
	return nil
}

func prepareChat(in map[string]json.RawMessage, limit uint64) (map[string]any, error) {
	if err := required(in, "messages"); err != nil {
		return nil, err
	}
	out := pick(in, chatFields)
	// Function tools run on the buyer's machine. Anything else a chat server
	// may know (web_search, code_execution, x_search) runs on the seller's
	// account, which the node did not price; a conversion to Messages or
	// Responses would even turn it into those APIs' hosted tools.
	if raw, ok := in["tools"]; ok {
		tools, kept, err := filterTools(raw, func(t map[string]any) bool {
			typ, _ := t["type"].(string)
			return typ == "" || typ == "function"
		})
		if err != nil {
			return nil, err
		}
		if kept == 0 {
			delete(out, "tools")
			delete(out, "tool_choice")
		} else {
			out["tools"] = tools
			if raw, ok := in["tool_choice"]; ok {
				var tc map[string]any
				if json.Unmarshal(raw, &tc) == nil {
					if typ, _ := tc["type"].(string); typ != "" && typ != "function" && typ != "allowed_tools" {
						out["tool_choice"] = "auto"
					}
				}
			}
		}
	}
	out["stream_options"] = map[string]bool{"include_usage": true}
	out["max_tokens"] = limit
	return out, nil
}

// Anthropic's server tools run on Anthropic's side and cost extra; their
// type names start with these. Client tools (no type, or custom, bash,
// text_editor, computer, memory) run on the buyer's machine.
var serverToolPrefixes = []string{"web_search", "web_fetch", "code_execution", "tool_search_tool", "mcp_toolset"}

func prepareMessages(in map[string]json.RawMessage, limit uint64) (map[string]any, error) {
	if err := required(in, "messages"); err != nil {
		return nil, err
	}
	out := pick(in, messagesFields)
	if raw, ok := in["tools"]; ok {
		tools, kept, err := filterTools(raw, func(t map[string]any) bool {
			typ, _ := t["type"].(string)
			for _, p := range serverToolPrefixes {
				if strings.HasPrefix(typ, p) {
					return false
				}
			}
			return true
		})
		if err != nil {
			return nil, err
		}
		if kept == 0 {
			delete(out, "tools")
			delete(out, "tool_choice")
		} else {
			out["tools"] = tools
		}
	}
	out["max_tokens"] = limit
	return out, nil
}

// The Responses tools a buyer's machine runs; the rest (web_search,
// file_search, code_interpreter, image_generation, computer_use_preview, mcp)
// run on OpenAI's side.
var clientResponsesTools = map[string]bool{"function": true, "custom": true, "namespace": true, "local_shell": true, "tool_search": true, "apply_patch": true, "shell": true}

func prepareResponses(in map[string]json.RawMessage, limit uint64) (map[string]any, error) {
	if err := required(in, "input"); err != nil {
		return nil, err
	}
	for _, f := range []string{"previous_response_id", "conversation"} {
		if raw, ok := in[f]; ok && string(raw) != "null" && string(raw) != `""` {
			return nil, protocol.Errorf(protocol.CodeBadRequest, "%s is not supported: send the whole conversation (store=false)", f)
		}
	}
	out := pick(in, responsesFields)
	if raw, ok := in["tools"]; ok {
		tools, kept, err := filterTools(raw, func(t map[string]any) bool {
			typ, _ := t["type"].(string)
			if typ == "tool_search" {
				exec, _ := t["execution"].(string)
				return exec == "client"
			}
			return clientResponsesTools[typ]
		})
		if err != nil {
			return nil, err
		}
		if kept == 0 {
			delete(out, "tools")
			delete(out, "tool_choice")
		} else {
			out["tools"] = tools
			// A choice that names a tool that is gone falls back to auto.
			if raw, ok := in["tool_choice"]; ok {
				var tc map[string]any
				if json.Unmarshal(raw, &tc) == nil {
					if typ, _ := tc["type"].(string); typ != "" && typ != "function" && typ != "custom" && typ != "allowed_tools" && !clientResponsesTools[typ] {
						out["tool_choice"] = "auto"
					}
				}
			}
		}
	}
	if raw, ok := in["include"]; ok {
		var inc []string
		_ = json.Unmarshal(raw, &inc)
		inc = slices.DeleteFunc(inc, func(s string) bool { return s != "reasoning.encrypted_content" })
		if len(inc) == 0 {
			delete(out, "include")
		} else {
			out["include"] = inc
		}
	}
	out["store"] = false
	out["max_output_tokens"] = limit
	return out, nil
}

// filterTools keeps the tools keep accepts.
func filterTools(raw json.RawMessage, keep func(map[string]any) bool) ([]any, int, error) {
	if string(raw) == "null" {
		return nil, 0, nil
	}
	var tools []map[string]any
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil, 0, protocol.Errorf(protocol.CodeBadRequest, "tools must be an array of objects")
	}
	var out []any
	for _, t := range tools {
		if keep(t) {
			out = append(out, t)
		}
	}
	return out, len(out), nil
}

// The request headers a node passes on. Anthropic betas are kept by family
// (the name without its date), so a newer date of a known beta passes.
var anthropicBetaFamilies = []string{
	"claude-code", "interleaved-thinking", "context-management", "fine-grained-tool-streaming", "effort",
	"context-1m", "advanced-tool-use", "per-turn-control", "mid-conversation-tool-changes", "thinking-token-count",
	"token-efficient-tools", "output-128k", "extended-cache-ttl", "prompt-caching-scope", "computer-use",
	"thinking-binding-controls", "mid-conversation-output-config", "redact-thinking",
}

func upstreamHeader(api string, in protocol.RequestHeader) protocol.RequestHeader {
	out := protocol.RequestHeader{}
	switch api {
	case protocol.APIMessages:
		if v := in.Get("anthropic-version"); headerToken(v, 32) {
			out["anthropic-version"] = v
		}
		var betas []string
		for _, b := range strings.Split(in.Get("anthropic-beta"), ",") {
			b = strings.TrimSpace(b)
			if headerToken(b, 96) && slices.ContainsFunc(anthropicBetaFamilies, func(f string) bool { return strings.HasPrefix(b, f+"-20") }) {
				betas = append(betas, b)
			}
		}
		if len(betas) > 0 {
			out["anthropic-beta"] = strings.Join(betas, ",")
		}
	case protocol.APIResponses:
		if v := in.Get("session_id"); headerToken(v, 128) {
			out["session_id"] = v
		}
		// Codex's turn, by which a ChatGPT seller keeps the backend's turn state.
		if v := in.Get("x-codex-turn-id"); headerToken(v, 128) {
			out["x-codex-turn-id"] = v
		}
	}
	return out
}

// headerToken reports whether v is a plain token of at most max bytes
// (letters, digits, '.', '_', '-', ':'). The buyer chooses these values and
// they go into the model server's HTTP headers: anything else is left out,
// so a control character cannot fail the seller's request.
func headerToken(v string, max int) bool {
	if v == "" || len(v) > max {
		return false
	}
	for _, c := range []byte(v) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '_', c == '-', c == ':':
		default:
			return false
		}
	}
	return true
}

// identity is what a ticket must be bound to for this node to accept it.
type identity struct {
	self        string
	coordinator string
	boxPub      []byte
	receiptKey  ed25519.PublicKey
}

// checkTicket accepts a ticket only if the trusted coordinator signed it for
// this node, for the buyer on the other end of the authenticated stream, at a
// price at or above the node's current one, and it has not expired.
func checkTicket(t *protocol.Ticket, remote string, id identity, models map[string]ModelConfig, now time.Time) (ModelConfig, error) {
	bad := func(msg string) (ModelConfig, error) {
		return ModelConfig{}, protocol.Errorf(protocol.CodeBadTicket, "%s", msg)
	}
	if err := t.Verify(id.coordinator); err != nil {
		return bad(err.Error())
	}
	switch {
	case !slices.Contains(protocol.SupportedInferVersions, t.InferVersion):
		return bad("this node does not speak the ticket's protocol version")
	case t.Node != id.self:
		return bad("ticket is for another node")
	case t.Buyer != remote:
		return bad("ticket was issued to a different buyer")
	case now.UnixNano() >= t.ExpiresAt:
		return bad("ticket expired")
	case !bytes.Equal(t.NodeBoxPub, id.boxPub) || !bytes.Equal(t.NodeReceiptKey, id.receiptKey):
		return bad("ticket keys do not match this node")
	case !protocol.ValidAPI(t.API):
		return bad("this node does not know the ticket's API")
	}
	mc, ok := models[t.Model]
	if !ok {
		return bad("model not served by this node")
	}
	if t.PriceInMicros < mc.PriceInMicros || t.PriceOutMicros < mc.PriceOutMicros ||
		t.PriceCacheRead < mc.cacheRead() || t.PriceCacheWrite < mc.cacheWrite() {
		return bad("ticket price is below this node's current price")
	}
	return mc, nil
}
