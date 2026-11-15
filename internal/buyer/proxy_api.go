package buyer

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"sharetoken/internal/protocol"
)

// messages serves POST /v1/messages (Anthropic Messages, Claude Code).
func (p *Proxy) messages(w http.ResponseWriter, r *http.Request) {
	const api = protocol.APIMessages
	body, ok := readBody(w, r)
	if !ok {
		writeAPIError(w, api, http.StatusRequestEntityTooLarge, "request body too large", protocol.CodeBadRequest)
		return
	}
	var head struct {
		Model    string            `json:"model"`
		Stream   bool              `json:"stream"`
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &head); err != nil || head.Model == "" {
		writeAPIError(w, api, http.StatusBadRequest, "body must be a JSON Messages request with a model", protocol.CodeBadRequest)
		return
	}
	if len(head.Messages) == 0 {
		writeAPIError(w, api, http.StatusBadRequest, "messages must be a non-empty array", protocol.CodeBadRequest)
		return
	}
	req := p.request(r, api, head.Model, body)
	if head.Stream {
		p.stream(w, r, req)
		return
	}
	p.aggregate(w, r, req)
}

// countTokens serves POST /v1/messages/count_tokens with a local estimate:
// asking a node would cost a job.
func (p *Proxy) countTokens(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		writeAPIError(w, protocol.APIMessages, http.StatusRequestEntityTooLarge, "request body too large", protocol.CodeBadRequest)
		return
	}
	n, err := protocol.EstimatePromptTokens(protocol.APIMessages, body)
	if err != nil {
		writeAPIError(w, protocol.APIMessages, http.StatusBadRequest, "body must be a JSON Messages request", protocol.CodeBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]uint64{"input_tokens": n})
}

// responsesHead is what the proxy reads of a Responses request.
type responsesHead struct {
	Model              string          `json:"model"`
	Stream             bool            `json:"stream"`
	Input              json.RawMessage `json:"input"`
	PreviousResponseID string          `json:"previous_response_id"`
}

func (h *responsesHead) check() string {
	switch {
	case h.Model == "":
		return "body must be a JSON Responses request with a model"
	case len(h.Input) == 0 || string(h.Input) == "null":
		return "input is required"
	case h.PreviousResponseID != "":
		return "previous_response_id is not supported over HTTP: send the whole conversation (store=false)"
	}
	return ""
}

// responses serves POST /v1/responses (OpenAI Responses, Codex).
func (p *Proxy) responses(w http.ResponseWriter, r *http.Request) {
	const api = protocol.APIResponses
	body, ok := readBody(w, r)
	if !ok {
		writeAPIError(w, api, http.StatusRequestEntityTooLarge, "request body too large", protocol.CodeBadRequest)
		return
	}
	var head responsesHead
	if err := json.Unmarshal(body, &head); err != nil {
		head = responsesHead{}
	}
	if msg := head.check(); msg != "" {
		writeAPIError(w, api, http.StatusBadRequest, msg, protocol.CodeBadRequest)
		return
	}
	req := p.request(r, api, head.Model, body)
	if head.Stream {
		p.stream(w, r, req)
		return
	}
	p.aggregate(w, r, req)
}

// compact serves POST /v1/responses/compact: Codex asks for its
// conversation to be compacted, and gets a response whose output is one
// compaction item.
func (p *Proxy) compact(w http.ResponseWriter, r *http.Request) {
	const api = protocol.APIResponses
	body, ok := readBody(w, r)
	if !ok {
		writeAPIError(w, api, http.StatusRequestEntityTooLarge, "request body too large", protocol.CodeBadRequest)
		return
	}
	var head responsesHead
	if err := json.Unmarshal(body, &head); err != nil {
		head = responsesHead{}
	}
	if msg := head.check(); msg != "" {
		writeAPIError(w, api, http.StatusBadRequest, msg, protocol.CodeBadRequest)
		return
	}
	req := p.request(r, api, head.Model, body)
	req.Header[protocol.HeaderOp] = protocol.OpCompact
	p.aggregate(w, r, req)
}

// models serves GET /v1/models: in OpenAI's list format with Anthropic's
// fields as well (both read their own), or as Codex's model manifest when
// Codex asks (?client_version=).
func (p *Proxy) models(w http.ResponseWriter, r *http.Request) {
	ms, err := p.Client.Models(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error(), codeFor(err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Query().Has("client_version") {
		_ = json.NewEncoder(w).Encode(codexManifest(ms))
		return
	}
	data := make([]map[string]any, 0, len(ms))
	for _, m := range ms {
		apis := m.APIs
		if apis == nil {
			apis = []string{}
		}
		data = append(data, map[string]any{
			"id": m.Model, "object": "model", "owned_by": "sharetoken", "created": 0,
			"type": "model", "display_name": m.Model, "created_at": "1970-01-01T00:00:00Z",
			"nodes": m.Nodes, "min_price_in": m.MinPriceIn, "min_price_out": m.MinPriceOut,
			"shared_nodes": m.SharedNodes, "apis": apis,
		})
	}
	resp := map[string]any{"object": "list", "data": data, "has_more": false}
	if len(data) > 0 {
		resp["first_id"], resp["last_id"] = data[0]["id"], data[len(data)-1]["id"]
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// codexManifest lists the models in the shape Codex's model picker reads:
// every field it requires is present, with neutral values. A model whose
// name says it reasons offers the usual reasoning levels.
func codexManifest(ms []protocol.ModelSummary) map[string]any {
	models := make([]map[string]any, 0, len(ms))
	for i, m := range ms {
		levels := []map[string]string{{"effort": "none", "description": "No reasoning"}}
		def := "none"
		if reasons(m.Model) {
			levels = []map[string]string{
				{"effort": "low", "description": "Fast responses with lighter reasoning"},
				{"effort": "medium", "description": "Balances speed and reasoning depth"},
				{"effort": "high", "description": "Greater reasoning depth for complex problems"},
			}
			def = "medium"
		}
		desc := "Served through sharetoken"
		if slices.Contains(m.APIs, protocol.APIResponses) {
			desc += " (native Responses)"
		}
		models = append(models, map[string]any{
			"slug": m.Model, "display_name": m.Model, "description": desc,
			"default_reasoning_level": def, "supported_reasoning_levels": levels,
			"shell_type": "shell_command", "visibility": "list", "supported_in_api": true, "priority": i,
			"additional_speed_tiers": []string{}, "service_tiers": []any{}, "default_service_tier": nil,
			"availability_nux": nil, "upgrade": nil, "model_messages": nil, "base_instructions": nil,
			"supports_reasoning_summary_parameter": reasons(m.Model), "default_reasoning_summary": "auto",
			"support_verbosity": false, "default_verbosity": nil, "apply_patch_tool_type": "freeform",
			"web_search_tool_type": "text", "truncation_policy": map[string]any{"mode": "bytes", "limit": 10000},
			"supports_image_detail_original": false, "supports_parallel_tool_calls": true,
			"context_window": 200000, "max_context_window": 200000, "auto_compact_token_limit": nil,
			"effective_context_window_percent": 95, "experimental_supported_tools": []string{},
			"input_modalities": []string{"text", "image"}, "supports_search_tool": false,
		})
	}
	return map[string]any{"models": models}
}

// reasons guesses whether a model reasons from its name.
func reasons(model string) bool {
	m := strings.ToLower(model)
	for _, s := range []string{"gpt-5", "o1", "o3", "o4", "codex", "claude", "reason", "think", "r1", "deepseek-v4", "gemini-2.5", "gemini-3", "grok-4"} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}
