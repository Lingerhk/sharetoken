// Package modelinfo is what a seller knows of well-known models without
// asking anyone: their context window, output limit and reasoning levels,
// for the metadata its offers carry (protocol.ModelMeta).
//
// The table follows models.dev (MIT) as of 2026-11, which magpie bundles,
// with the older Claude and DeepSeek models this repository's conversions
// know (apicompat's chatAnthropicMaxOutput). A window is the input the model
// reads: OpenAI's models list a larger total of input and output, and only
// the input part is given here.
package modelinfo

import (
	"slices"
	"strings"

	"sharetoken/internal/protocol"
)

// known is one model, or the family of models whose ids it begins
// (Lookup).
type known struct {
	id        string
	window    uint64
	output    uint64
	reasoning bool
	levels    []string
}

// table is sorted by id; an entry equal to a shorter one it begins with is
// left out (that one covers it).
var table = []known{
	{"claude-3-5", 200000, 8192, false, nil},
	{"claude-3-7-sonnet", 200000, 64000, true, nil},
	{"claude-3-haiku", 200000, 4096, false, nil},
	{"claude-3-opus", 200000, 4096, false, nil},
	{"claude-3-sonnet", 200000, 4096, false, nil},
	{"claude-fable-5", 1000000, 128000, true, []string{"low", "medium", "high", "xhigh", "max"}},
	{"claude-haiku-4-5", 200000, 64000, true, nil},
	{"claude-haiku-5-5", 1000000, 128000, true, []string{"low", "medium", "high", "xhigh", "max"}},
	{"claude-opus-4-0", 200000, 32000, true, nil},
	{"claude-opus-4-1", 200000, 32000, true, nil},
	{"claude-opus-4-20250514", 200000, 32000, true, nil},
	{"claude-opus-4-5", 200000, 64000, true, []string{"low", "medium", "high"}},
	{"claude-opus-4-6", 1000000, 128000, true, []string{"low", "medium", "high", "max"}},
	{"claude-opus-4-7", 1000000, 128000, true, []string{"low", "medium", "high", "xhigh", "max"}},
	{"claude-opus-4-8", 1000000, 128000, true, []string{"low", "medium", "high", "xhigh", "max"}},
	{"claude-opus-5", 1000000, 128000, true, []string{"low", "medium", "high", "xhigh", "max"}},
	{"claude-sonnet-4-0", 200000, 64000, true, nil},
	{"claude-sonnet-4-20250514", 200000, 64000, true, nil},
	{"claude-sonnet-4-5", 200000, 64000, true, nil},
	{"claude-sonnet-4-6", 1000000, 128000, true, []string{"low", "medium", "high", "max"}},
	{"claude-sonnet-5", 1000000, 128000, true, []string{"low", "medium", "high", "xhigh", "max"}},
	{"deepseek-chat", 128000, 8192, false, nil},
	{"deepseek-flash", 1048576, 393216, true, []string{"low", "high", "max"}},
	{"deepseek-reasoner", 128000, 65536, true, nil},
	{"deepseek-v4-flash", 1048576, 393216, true, []string{"low", "high", "max"}},
	{"deepseek-v4-pro", 1048576, 393216, true, []string{"low", "high", "max"}},
	{"gemini-2.5-flash", 1048576, 65536, true, nil},
	{"gemini-2.5-pro", 1048576, 65536, true, nil},
	{"gemini-3-flash-preview", 1048576, 65536, true, []string{"minimal", "low", "medium", "high"}},
	{"gemini-3.1-flash-lite", 1048576, 65536, true, []string{"minimal", "low", "medium", "high"}},
	{"gemini-3.1-pro-preview", 1048576, 65536, true, []string{"low", "medium", "high"}},
	{"gemini-3.5-flash", 1048576, 65536, true, []string{"minimal", "low", "medium", "high"}},
	{"gemini-3.6-flash", 1048576, 65536, true, []string{"minimal", "low", "medium", "high"}},
	{"gemini-3.7-flash", 1048576, 65536, true, []string{"low", "medium", "high"}},
	{"gemini-3.8-flash", 1048576, 65536, true, []string{"low", "medium", "high"}},
	{"gemini-4-argon", 1048576, 32768, true, []string{"low", "medium", "high"}},
	{"gpt-4.1", 1047576, 32768, false, nil},
	{"gpt-4o", 128000, 16384, false, nil},
	{"gpt-4o-2024-05-13", 128000, 4096, false, nil},
	{"gpt-5", 272000, 128000, true, []string{"minimal", "low", "medium", "high"}},
	{"gpt-5-pro", 272000, 272000, true, []string{"high"}},
	{"gpt-5.1", 272000, 128000, true, []string{"none", "low", "medium", "high"}},
	{"gpt-5.2", 272000, 128000, true, []string{"none", "low", "medium", "high", "xhigh"}},
	{"gpt-5.2-pro", 272000, 128000, true, []string{"medium", "high", "xhigh"}},
	{"gpt-5.3-codex", 272000, 128000, true, []string{"none", "low", "medium", "high", "xhigh"}},
	{"gpt-5.3-codex-spark", 100000, 32000, true, []string{"none", "low", "medium", "high", "xhigh"}},
	{"gpt-5.4", 922000, 128000, true, []string{"none", "low", "medium", "high", "xhigh"}},
	{"gpt-5.4-mini", 272000, 128000, true, []string{"none", "low", "medium", "high", "xhigh"}},
	{"gpt-5.4-nano", 272000, 128000, true, []string{"none", "low", "medium", "high", "xhigh"}},
	{"gpt-5.4-pro", 922000, 128000, true, []string{"medium", "high", "xhigh"}},
	{"gpt-5.5", 922000, 128000, true, []string{"none", "low", "medium", "high", "xhigh"}},
	{"gpt-5.5-pro", 922000, 128000, true, []string{"medium", "high", "xhigh"}},
	{"gpt-5.6", 922000, 128000, true, []string{"none", "low", "medium", "high", "xhigh", "max"}},
	{"gpt-6-astra", 922000, 128000, true, []string{"low", "medium", "high", "xhigh", "max"}},
	{"gpt-6-luna", 922000, 128000, true, []string{"none", "low", "medium", "high", "xhigh", "max"}},
	{"gpt-6-sol", 922000, 128000, true, []string{"none", "low", "medium", "high", "xhigh", "max"}},
	{"gpt-6.1-sol", 922000, 128000, true, []string{"low", "medium", "high", "xhigh", "max"}},
	{"gpt-6.1-terra", 922000, 128000, true, []string{"none", "low", "medium", "high", "xhigh", "max"}},
	{"grok-4.20-0309-non-reasoning", 1000000, 30000, false, nil},
	{"grok-4.20-0309-reasoning", 1000000, 30000, true, nil},
	{"grok-4.20-multi-agent-0309", 1000000, 30000, true, []string{"low", "medium", "high", "xhigh"}},
	{"grok-4.3", 1000000, 30000, true, []string{"none", "low", "medium", "high", "xhigh"}},
	{"grok-4.5", 500000, 500000, true, []string{"low", "medium", "high", "xhigh"}},
	{"grok-4.6", 500000, 500000, true, []string{"low", "medium", "high", "xhigh"}},
	{"grok-4.7", 500000, 500000, true, []string{"low", "medium", "high", "xhigh"}},
	{"o1", 200000, 100000, true, []string{"low", "medium", "high"}},
	{"o3", 200000, 100000, true, []string{"low", "medium", "high"}},
	{"o4-mini", 200000, 100000, true, []string{"low", "medium", "high"}},
}

// Lookup is what the table says of model, or nil. A dated snapshot, a
// variant or a suffix (claude-sonnet-4-5-20250929, gpt-5-mini,
// claude-opus-4-6[1m]) takes the entry of the longest id it begins with,
// followed by a separator; a vendor or router prefix (openai/gpt-5.5,
// models/gemini-2.5-pro) is dropped, and Claude's dotted spelling
// (claude-opus-4.6, as relays write it) read as Anthropic's.
func Lookup(model string) *protocol.ModelMeta {
	id := normal(model)
	var best *known
	for i := range table {
		k := &table[i]
		if strings.HasPrefix(id, k.id) && boundary(id, len(k.id)) && (best == nil || len(k.id) > len(best.id)) {
			best = k
		}
	}
	if best == nil {
		return nil
	}
	return &protocol.ModelMeta{ContextWindow: best.window, MaxOutputTokens: best.output,
		Reasoning: best.reasoning, ReasoningLevels: slices.Clone(best.levels)}
}

func normal(model string) string {
	id := strings.ToLower(strings.TrimSpace(model))
	id = id[strings.LastIndex(id, "/")+1:]
	if strings.HasPrefix(id, "claude-") {
		id = strings.ReplaceAll(id, ".", "-")
	}
	return id
}

// boundary says whether id's prefix of n bytes ends a name: at the end, or
// before a separator. A dot is no separator: gpt-5.7 is not gpt-5.
func boundary(id string, n int) bool {
	return n == len(id) || strings.ContainsRune("-_:@[ ", rune(id[n]))
}

// Merge is the metadata of a model with the table's facts (known, may be
// nil) and what the seller set for it (own, may be nil): each field own
// sets wins. Levels own sets make the model a reasoning one.
func Merge(known, own *protocol.ModelMeta) *protocol.ModelMeta {
	if known == nil && own == nil {
		return nil
	}
	out := protocol.ModelMeta{}
	if known != nil {
		out = *known
		out.ReasoningLevels = slices.Clone(known.ReasoningLevels)
	}
	if own != nil {
		if own.ContextWindow > 0 {
			out.ContextWindow = own.ContextWindow
		}
		if own.MaxOutputTokens > 0 {
			out.MaxOutputTokens = own.MaxOutputTokens
		}
		if len(own.ReasoningLevels) > 0 {
			out.ReasoningLevels = slices.Clone(own.ReasoningLevels)
		}
		out.Reasoning = out.Reasoning || own.Reasoning
	}
	return out.Sane()
}
