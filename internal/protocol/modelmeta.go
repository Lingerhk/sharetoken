package protocol

import "slices"

// ModelMeta is what a seller knows of a model besides its price: how much
// it reads and writes, and whether and how it reasons. Coding tools size
// their conversations and their reasoning settings by it (Codex compacts
// before the window is full, Crush sends an effort only to a model that
// lists levels). Every field is optional: zero means unknown.
type ModelMeta struct {
	// ContextWindow is the most prompt tokens the model reads.
	ContextWindow uint64 `json:"context_window,omitempty"`
	// MaxOutputTokens is the most tokens one answer may have.
	MaxOutputTokens uint64 `json:"max_output_tokens,omitempty"`
	// Reasoning says the model thinks before it answers.
	Reasoning bool `json:"reasoning,omitempty"`
	// ReasoningLevels are the reasoning efforts it takes, least first
	// ("none", "minimal", "low", "medium", "high", "xhigh", "max"); empty
	// when it reasons without levels or that is not known.
	ReasoningLevels []string `json:"reasoning_levels,omitempty"`
}

// ReasoningEfforts are the effort names a model's levels may use, least
// first.
var ReasoningEfforts = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}

// maxMetaTokens bounds a window or an output limit a node may claim.
const maxMetaTokens = 100_000_000

// Sane is m with what no model has dropped (a window past 100M tokens, an
// unknown effort, a level twice), the levels in ReasoningEfforts' order;
// nil when nothing is left. Coordinators keep only sane metadata, since
// nodes write it.
func (m *ModelMeta) Sane() *ModelMeta {
	if m == nil {
		return nil
	}
	out := ModelMeta{Reasoning: m.Reasoning}
	if m.ContextWindow <= maxMetaTokens {
		out.ContextWindow = m.ContextWindow
	}
	if m.MaxOutputTokens <= maxMetaTokens {
		out.MaxOutputTokens = m.MaxOutputTokens
	}
	for _, e := range ReasoningEfforts {
		if slices.Contains(m.ReasoningLevels, e) {
			out.ReasoningLevels = append(out.ReasoningLevels, e)
		}
	}
	if len(out.ReasoningLevels) > 0 {
		out.Reasoning = true
	}
	if out.ContextWindow == 0 && out.MaxOutputTokens == 0 && !out.Reasoning {
		return nil
	}
	return &out
}

// MergeMeta is what holds for every node of a model, given what two of them
// say: the smaller window and output limit (a buyer's request may go to
// either), reasoning when either reasons, and the levels both take (either's
// when the other names none). Nil says nothing. The result shares nothing
// with a or b.
func MergeMeta(a, b *ModelMeta) *ModelMeta {
	switch {
	case a == nil && b == nil:
		return nil
	case a == nil:
		a = &ModelMeta{}
	case b == nil:
		b = &ModelMeta{}
	}
	least := func(x, y uint64) uint64 {
		if x == 0 || y != 0 && y < x {
			return y
		}
		return x
	}
	out := &ModelMeta{
		ContextWindow:   least(a.ContextWindow, b.ContextWindow),
		MaxOutputTokens: least(a.MaxOutputTokens, b.MaxOutputTokens),
		Reasoning:       a.Reasoning || b.Reasoning,
	}
	switch {
	case len(a.ReasoningLevels) == 0:
		out.ReasoningLevels = slices.Clone(b.ReasoningLevels)
	case len(b.ReasoningLevels) == 0:
		out.ReasoningLevels = slices.Clone(a.ReasoningLevels)
	default:
		for _, e := range a.ReasoningLevels {
			if slices.Contains(b.ReasoningLevels, e) {
				out.ReasoningLevels = append(out.ReasoningLevels, e)
			}
		}
	}
	return out
}
