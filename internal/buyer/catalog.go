package buyer

import (
	_ "embed"
	"slices"
	"strings"

	"sharetoken/internal/protocol"
)

// codexPrompt is what Codex itself sends as the instructions of a model its
// catalog does not list: BASE_INSTRUCTIONS of openai/codex (Apache-2.0,
// rust-v0.166.0, models-manager/prompt.md, used by model_info_from_slug),
// word for word, as magpie carries it (codexcat.Prompt). A catalog entry
// with neither base_instructions nor model_messages makes Codex refuse a
// model_catalog_json whole; from a /models list it falls back to its
// defaults for the model and sends this prompt anyway. Every entry carries
// it, so a model of the network gets what Codex would send for it, and the
// list stays one Codex reads.
//
//go:embed codex_prompt.md
var codexPrompt string

// codexWorkingWindow is where Codex is told to compact a conversation on a
// model whose window is larger (magpie's settings.WorkingWindow): the 272K
// Codex holds OpenAI's own models to. Prompts past it are slow and dear on
// every model, and a model whose window is not known compacts there too:
// Codex compacts at no point on an entry with neither a window nor a limit,
// and after the model's "too long" only errors on (magpie's unknownWorking).
const codexWorkingWindow = 272000

// codexEffectivePct is the share of the window Codex counts as the
// conversation's room, as every entry of its own catalog says.
const codexEffectivePct = 95

// effortDescriptions are the words Codex's picker shows for each level, as
// its own catalog has them.
var effortDescriptions = map[string]string{
	"none":    "No reasoning",
	"minimal": "Minimal reasoning, the fastest responses",
	"low":     "Fast responses with lighter reasoning",
	"medium":  "Balances speed and reasoning depth for everyday tasks",
	"high":    "Greater reasoning depth for complex problems",
	"xhigh":   "Extra high reasoning depth for complex problems",
	"max":     "Maximum reasoning depth for the hardest problems",
}

// usualEfforts are the levels offered for a model that reasons without
// naming its own.
var usualEfforts = []string{"low", "medium", "high"}

// modelReasoning is whether a model reasons and at which levels: as its
// sellers say, else guessed from its name.
func modelReasoning(m protocol.ModelSummary) (bool, []string) {
	if meta := m.Meta; meta != nil {
		if len(meta.ReasoningLevels) > 0 {
			return true, meta.ReasoningLevels
		}
		if meta.Reasoning {
			return true, usualEfforts
		}
		if meta.ContextWindow > 0 {
			return false, nil // a model the seller knows, and not as reasoning
		}
	}
	if reasons(m.Model) {
		return true, usualEfforts
	}
	return false, nil
}

// modelObject is a model's entry in GET /v1/models: OpenAI's list item and
// Anthropic's model object at once (each reads its own fields), with the
// metadata the clients of either size their requests by (after magpie's
// modelObject): OpenRouter's context_length, LiteLLM's max_input_tokens and
// max_output_tokens, the context_window and reasoning levels Crush and
// others read, and Anthropic's max_input_tokens, max_tokens and
// capabilities. A field nobody knows is left out.
func modelObject(m protocol.ModelSummary) map[string]any {
	apis := m.APIs
	if apis == nil {
		apis = []string{}
	}
	o := map[string]any{
		"id": ListedModel(m.Model), "object": "model", "owned_by": "sharetoken", "created": 0,
		"type": "model", "display_name": "ShareToken " + m.Model, "created_at": "1970-01-01T00:00:00Z",
		"nodes": m.Nodes, "min_price_in": m.MinPriceIn, "min_price_out": m.MinPriceOut,
		"shared_nodes": m.SharedNodes, "apis": apis,
	}
	meta := m.Meta
	if meta == nil {
		return o
	}
	if w := meta.ContextWindow; w > 0 {
		o["context_window"], o["context_length"], o["max_input_tokens"] = w, w, w
	}
	if n := meta.MaxOutputTokens; n > 0 {
		o["max_output_tokens"], o["max_tokens"] = n, n
	}
	reasoning, levels := modelReasoning(m)
	o["reasoning"] = reasoning
	if levels == nil {
		levels = []string{}
	}
	o["supported_reasoning_levels"], o["reasoning_levels"] = levels, levels
	effort := map[string]any{"supported": len(levels) > 0}
	for _, e := range protocol.ReasoningEfforts {
		effort[e] = map[string]bool{"supported": slices.Contains(levels, e)}
	}
	o["capabilities"] = map[string]any{
		"thinking": map[string]any{"supported": reasoning},
		"effort":   effort,
	}
	return o
}

// codexManifest lists the models in the shape Codex's model picker reads
// (its models.json entries): every field it requires is present, the
// window, compaction and reasoning levels as the sellers say (else neutral
// values and levels guessed from the name), and the instructions Codex
// sends a model it does not know.
// CodexCatalog is the models as a Codex model_catalog_json file holds them.
// Codex reads a custom provider's models only from such a file (it never
// asks the provider's /models), so `sharetoken models -codex FILE` writes
// one for its config.
func CodexCatalog(ms []protocol.ModelSummary) map[string]any { return codexManifest(ms) }

func codexManifest(ms []protocol.ModelSummary) map[string]any {
	models := make([]map[string]any, 0, len(ms))
	for i, m := range ms {
		reasoning, efforts := modelReasoning(m)
		levels := make([]map[string]string, 0, len(efforts))
		for _, e := range efforts {
			levels = append(levels, map[string]string{"effort": e, "description": effortDescriptions[e]})
		}
		def := "none"
		if reasoning {
			def = defaultEffort(efforts)
		} else {
			levels = []map[string]string{{"effort": "none", "description": effortDescriptions["none"]}}
		}
		desc := "Served through sharetoken"
		if slices.Contains(m.APIs, protocol.APIResponses) {
			desc += " (native Responses)"
		}
		e := map[string]any{
			"slug": ListedModel(m.Model), "display_name": "ShareToken " + m.Model, "description": desc,
			"default_reasoning_level": def, "supported_reasoning_levels": levels,
			"shell_type": "shell_command", "visibility": "list", "supported_in_api": true, "priority": i,
			"additional_speed_tiers": []string{}, "service_tiers": []any{}, "default_service_tier": nil,
			"availability_nux": nil, "upgrade": nil, "model_messages": nil, "base_instructions": codexPrompt,
			"supports_reasoning_summary_parameter": reasoning, "default_reasoning_summary": "auto",
			"support_verbosity": false, "default_verbosity": nil, "apply_patch_tool_type": "freeform",
			"web_search_tool_type": "text", "truncation_policy": map[string]any{"mode": "tokens", "limit": 10000},
			"supports_image_detail_original": false, "supports_parallel_tool_calls": true,
			"effective_context_window_percent": codexEffectivePct, "experimental_supported_tools": []string{},
			"input_modalities": []string{"text", "image"}, "supports_search_tool": false,
		}
		codexWindow(e, m.Meta)
		models = append(models, e)
	}
	return map[string]any{"models": models}
}

// codexWindow sets an entry's window and where Codex compacts (after
// magpie's codexcat): the model's whole window as context_window and
// max_context_window, and auto_compact_token_limit at codexWorkingWindow
// when the window is larger, or alone when the window is not known. Codex
// compacts at the lesser of the limit and 90% of the window.
func codexWindow(e map[string]any, meta *protocol.ModelMeta) {
	var w uint64
	if meta != nil {
		w = meta.ContextWindow
	}
	if w == 0 {
		e["auto_compact_token_limit"] = codexWorkingWindow
		return
	}
	e["context_window"], e["max_context_window"] = w, w
	if w > codexWorkingWindow {
		e["auto_compact_token_limit"] = codexWorkingWindow
	} else {
		e["auto_compact_token_limit"] = nil
	}
}

// defaultEffort is the middle of the road (magpie's DefaultEffort): medium
// or high when offered, else the first level.
func defaultEffort(levels []string) string {
	for _, want := range []string{"medium", "high"} {
		if slices.Contains(levels, want) {
			return want
		}
	}
	return levels[0]
}

// reasons guesses whether a model reasons from its name, for a model no
// seller describes.
func reasons(model string) bool {
	m := strings.ToLower(model)
	for _, s := range []string{"gpt-5", "o1", "o3", "o4", "codex", "claude", "reason", "think", "r1", "deepseek-v4", "gemini-2.5", "gemini-3", "grok-4"} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}
