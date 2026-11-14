package provider

import "fmt"

type cacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

func (c *cacheControl) valid() error {
	if c == nil {
		return nil
	}
	if c.Type != "ephemeral" || (c.TTL != "" && c.TTL != "5m" && c.TTL != "1h") {
		return fmt.Errorf("cache_control must be ephemeral with ttl 5m or 1h")
	}
	return nil
}

func (c *cacheControl) value() map[string]any {
	m := map[string]any{"type": c.Type}
	if c.TTL != "" {
		m["ttl"] = c.TTL
	}
	return m
}

// Normalize after all injected and client breakpoints are present. Execution
// order is tools, system, messages; removal priority follows sub2api's policy.
func normalizeAnthropicCache(req map[string]any) {
	var tools, system, messages []map[string]any
	collect := func(raw []any, dst *[]map[string]any) {
		for _, item := range raw {
			b, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if b["type"] == "thinking" || b["type"] == "redacted_thinking" {
				delete(b, "cache_control")
				continue
			}
			if _, ok := b["cache_control"].(map[string]any); ok {
				*dst = append(*dst, b)
			}
		}
	}
	if raw, ok := req["tools"].([]any); ok {
		collect(raw, &tools)
	}
	if raw, ok := req["system"].([]any); ok {
		collect(raw, &system)
	}
	if raw, ok := req["messages"].([]map[string]any); ok {
		for _, msg := range raw {
			if blocks, ok := msg["content"].([]any); ok {
				collect(blocks, &messages)
			}
		}
	}
	remaining := len(tools) + len(system) + len(messages) - 4
	remove := func(b map[string]any) {
		if remaining > 0 {
			delete(b, "cache_control")
			remaining--
		}
	}
	for i := len(tools) - 1; i >= 0; i-- {
		remove(tools[i])
	}
	for _, b := range messages {
		remove(b)
	}
	for i := len(system) - 1; i >= 0; i-- {
		remove(system[i])
	}
	ordered := append(append(append([]map[string]any{}, tools...), system...), messages...)
	longer := false
	for i := len(ordered) - 1; i >= 0; i-- {
		cc, ok := ordered[i]["cache_control"].(map[string]any)
		if !ok {
			continue
		}
		if cc["ttl"] == "1h" {
			longer = true
		}
		if longer {
			cc["ttl"] = "1h"
		}
	}
}
