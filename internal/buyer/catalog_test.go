package buyer

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"sharetoken/internal/protocol"
)

func roundTrip(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestModelObjectMetadata(t *testing.T) {
	m := protocol.ModelSummary{Model: "claude-x", Nodes: 2, APIs: []string{protocol.APIMessages},
		Meta: &protocol.ModelMeta{ContextWindow: 200000, MaxOutputTokens: 64000, Reasoning: true, ReasoningLevels: []string{"low", "high"}}}
	o := roundTrip(t, modelObject(m))
	for k, want := range map[string]any{
		"id": "st-claude-x", "context_window": 200000.0, "context_length": 200000.0, "max_input_tokens": 200000.0,
		"max_output_tokens": 64000.0, "max_tokens": 64000.0, "reasoning": true, "type": "model",
	} {
		if o[k] != want {
			t.Errorf("%s = %v, want %v", k, o[k], want)
		}
	}
	levels := []any{"low", "high"}
	if !reflect.DeepEqual(o["supported_reasoning_levels"], levels) || !reflect.DeepEqual(o["reasoning_levels"], levels) {
		t.Errorf("levels: %v %v", o["supported_reasoning_levels"], o["reasoning_levels"])
	}
	caps := o["capabilities"].(map[string]any)
	effort := caps["effort"].(map[string]any)
	if caps["thinking"].(map[string]any)["supported"] != true || effort["supported"] != true ||
		effort["high"].(map[string]any)["supported"] != true || effort["max"].(map[string]any)["supported"] != false {
		t.Errorf("capabilities: %v", caps)
	}

	// A model no seller describes keeps the plain entry.
	o = roundTrip(t, modelObject(protocol.ModelSummary{Model: "local"}))
	for _, k := range []string{"context_window", "max_output_tokens", "reasoning", "capabilities"} {
		if _, ok := o[k]; ok {
			t.Errorf("an undescribed model has %s: %v", k, o)
		}
	}
	// One described as not reasoning says so.
	o = roundTrip(t, modelObject(protocol.ModelSummary{Model: "gpt-4o", Meta: &protocol.ModelMeta{ContextWindow: 128000}}))
	if o["reasoning"] != false || len(o["reasoning_levels"].([]any)) != 0 {
		t.Errorf("non-reasoning model: %v", o)
	}
}

func TestCodexManifestEntries(t *testing.T) {
	ms := []protocol.ModelSummary{
		{Model: "big", Meta: &protocol.ModelMeta{ContextWindow: 1000000, Reasoning: true, ReasoningLevels: []string{"low", "medium", "high", "max"}}},
		{Model: "small", Meta: &protocol.ModelMeta{ContextWindow: 128000}},
		{Model: "mystery-think"},
		{Model: "minimal-only", Meta: &protocol.ModelMeta{ContextWindow: 400000, ReasoningLevels: []string{"minimal", "low"}}},
	}
	got := roundTrip(t, codexManifest(ms))["models"].([]any)
	entry := func(i int) map[string]any { return got[i].(map[string]any) }
	efforts := func(e map[string]any) []string {
		var out []string
		for _, l := range e["supported_reasoning_levels"].([]any) {
			lm := l.(map[string]any)
			if lm["description"] == "" {
				t.Errorf("level %v has no description", lm)
			}
			out = append(out, lm["effort"].(string))
		}
		return out
	}
	big := entry(0)
	if big["context_window"] != 1000000.0 || big["max_context_window"] != 1000000.0 || big["auto_compact_token_limit"] != 272000.0 {
		t.Errorf("big window: %v %v %v", big["context_window"], big["max_context_window"], big["auto_compact_token_limit"])
	}
	if e := efforts(big); !reflect.DeepEqual(e, []string{"low", "medium", "high", "max"}) || big["default_reasoning_level"] != "medium" || big["supports_reasoning_summary_parameter"] != true {
		t.Errorf("big levels: %v default %v", e, big["default_reasoning_level"])
	}
	small := entry(1)
	if small["context_window"] != 128000.0 || small["auto_compact_token_limit"] != nil {
		t.Errorf("a window under 272K compacts where Codex says: %v %v", small["context_window"], small["auto_compact_token_limit"])
	}
	if e := efforts(small); !reflect.DeepEqual(e, []string{"none"}) || small["default_reasoning_level"] != "none" {
		t.Errorf("a model known not to reason: %v", e)
	}
	mystery := entry(2)
	if _, ok := mystery["context_window"]; ok || mystery["auto_compact_token_limit"] != 272000.0 {
		t.Errorf("unknown window: %v", mystery)
	}
	if e := efforts(mystery); !reflect.DeepEqual(e, []string{"low", "medium", "high"}) {
		t.Errorf("a model no seller describes is guessed from its name: %v", e)
	}
	if e := efforts(entry(3)); !reflect.DeepEqual(e, []string{"minimal", "low"}) || entry(3)["default_reasoning_level"] != "minimal" {
		t.Errorf("minimal-only: %v %v", e, entry(3)["default_reasoning_level"])
	}
	for i := range got {
		bi, _ := entry(i)["base_instructions"].(string)
		if !strings.HasPrefix(bi, "You are a coding agent running in the Codex CLI") || len(bi) < 10000 {
			t.Errorf("entry %d: base_instructions must be Codex's own for an unknown model, got %d bytes", i, len(bi))
		}
		if entry(i)["effective_context_window_percent"] != 95.0 {
			t.Errorf("entry %d: effective_context_window_percent", i)
		}
	}
}
