package provider

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Rules mirror sub2api 530c5e3a4's validateClaude55Request for the fields this
// adapter accepts. A model is matched exactly, after our supported aliases.
type anthropicCapabilities struct {
	lockedSampling bool
	topPRange      bool
	forcedTools    bool
}

func anthropicModelCapabilities(model string) anthropicCapabilities {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "claude-sonnet-5-5":
		return anthropicCapabilities{lockedSampling: true, topPRange: true}
	case "claude-haiku-5-5":
		return anthropicCapabilities{lockedSampling: true, forcedTools: true}
	case "claude-fable-5-2", "claude-mythos-5-2":
		return anthropicCapabilities{lockedSampling: true}
	case "claude-opus-5-5":
		return anthropicCapabilities{}
	default:
		return anthropicCapabilities{forcedTools: true}
	}
}

// validateAnthropicRequest checks a Messages request's sampling and
// tool_choice against what model takes; with sampling "default" it drops the
// sampling settings the model would refuse instead.
func validateAnthropicRequest(req map[string]any, model, sampling string) error {
	caps := anthropicModelCapabilities(model)
	if tc, ok := req["tool_choice"].(map[string]any); ok && !caps.forcedTools {
		if typ := tc["type"]; typ == "any" || typ == "tool" {
			return fmt.Errorf("%s does not support forced tool_choice; use auto or none", model)
		}
	}
	if !caps.lockedSampling {
		return nil
	}
	if sampling == "default" {
		delete(req, "temperature")
		delete(req, "top_p")
		return nil
	}
	temp, hasTemp := floatField(req, "temperature")
	topP, hasTopP := floatField(req, "top_p")
	if hasTemp && temp != 1 {
		return fmt.Errorf("%s requires temperature=1 or no temperature", model)
	}
	if hasTopP && ((!caps.topPRange && topP != .99) || (caps.topPRange && (topP < .99 || topP > 1))) {
		return fmt.Errorf("%s does not support this top_p; omit it or use 0.99", model)
	}
	if hasTemp && hasTopP {
		return fmt.Errorf("%s does not allow temperature and top_p together; omit one", model)
	}
	return nil
}

func floatField(req map[string]any, k string) (float64, bool) {
	switch v := req[k].(type) {
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	case float64:
		return v, true
	}
	return 0, false
}
