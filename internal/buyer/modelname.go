package buyer

import "strings"

// ModelPrefix marks the network's models in what the proxy lists: a tool
// that also keeps its own vendor login (Claude Code, Codex) shows both in
// one model menu, and the local endpoint tells them apart by it
// (internal/official). Sellers' offers and tickets carry the bare names; the
// prefix is taken off before a request is bought.
const ModelPrefix = "st-"

// ListedModel is how a network model is listed to tools.
func ListedModel(model string) string { return ModelPrefix + model }

// IsListedModel reports whether a tool named a network model by its listed
// name.
func IsListedModel(model string) bool {
	return strings.HasPrefix(strings.TrimSpace(model), ModelPrefix)
}

// NetworkModel is the offer name for what a tool asked for: the listed name
// without its prefix, and without the "[1m]" Claude Code appends to a model
// it runs with a 1M window. A bare name passes as it is, for tools set up
// before the prefix.
func NetworkModel(model string) string {
	m := strings.TrimSpace(model)
	m = strings.TrimSuffix(m, "[1m]")
	return strings.TrimPrefix(m, ModelPrefix)
}
