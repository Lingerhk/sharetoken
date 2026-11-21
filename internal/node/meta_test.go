package node

import (
	"reflect"
	"testing"

	"sharetoken/internal/protocol"
)

func TestOfferMeta(t *testing.T) {
	cases := []struct {
		name string
		m    ModelConfig
		want *protocol.ModelMeta
	}{
		{"known by its upstream name", ModelConfig{ID: "fast", UpstreamModel: "claude-sonnet-4-5-20250929"},
			&protocol.ModelMeta{ContextWindow: 200000, MaxOutputTokens: 64000, Reasoning: true}},
		{"known by its network name", ModelConfig{ID: "gpt-4o", UpstreamModel: "my-deployment"},
			&protocol.ModelMeta{ContextWindow: 128000, MaxOutputTokens: 16384}},
		{"unknown", ModelConfig{ID: "llama-local", UpstreamModel: "llama3"}, nil},
		{"seller's own settings win", ModelConfig{ID: "x", UpstreamModel: "gpt-4o", Meta: &protocol.ModelMeta{ContextWindow: 64000, ReasoningLevels: []string{"high"}}},
			&protocol.ModelMeta{ContextWindow: 64000, MaxOutputTokens: 16384, Reasoning: true, ReasoningLevels: []string{"high"}}},
		{"the node's cap for the model", ModelConfig{ID: "x", UpstreamModel: "gpt-4o", MaxOutputTokens: 4096},
			&protocol.ModelMeta{ContextWindow: 128000, MaxOutputTokens: 4096}},
		{"the cap alone", ModelConfig{ID: "x", UpstreamModel: "llama3", MaxOutputTokens: 4096},
			&protocol.ModelMeta{MaxOutputTokens: 4096}},
		{"no more than a job may have", ModelConfig{ID: "x", UpstreamModel: "deepseek-v4-pro"},
			&protocol.ModelMeta{ContextWindow: 1048576, MaxOutputTokens: 128000, Reasoning: true, ReasoningLevels: []string{"low", "high", "max"}}},
	}
	for _, c := range cases {
		if got := c.m.Price().Meta; !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}
