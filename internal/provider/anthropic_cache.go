package provider

type cacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
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
	for _, msg := range messageList(req) {
		if blocks, ok := msg["content"].([]any); ok {
			collect(blocks, &messages)
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

// messageList returns a Messages request's messages, whether they were
// decoded from JSON ([]any) or built here ([]map[string]any).
func messageList(req map[string]any) []map[string]any {
	switch v := req["messages"].(type) {
	case []map[string]any:
		return v
	case []any:
		out := make([]map[string]any, 0, len(v))
		for _, m := range v {
			if mm, ok := m.(map[string]any); ok {
				out = append(out, mm)
			}
		}
		return out
	}
	return nil
}
