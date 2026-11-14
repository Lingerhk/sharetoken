package provider

import (
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

func validateAnthropicParameters(chat *chatRequest, sampling string) error {
	caps := anthropicModelCapabilities(chat.Model)
	mode, name := chat.toolChoice()
	if !caps.forcedTools && (name != "" || mode == "required") {
		return fmt.Errorf("%s does not support forced tool_choice; use auto or none", chat.Model)
	}
	if !caps.lockedSampling {
		return nil
	}
	if sampling == "default" {
		chat.Temperature, chat.TopP = nil, nil
		return nil
	}
	if chat.Temperature != nil && *chat.Temperature != 1 {
		return fmt.Errorf("%s requires temperature=1 or no temperature", chat.Model)
	}
	if p := chat.TopP; p != nil && ((!caps.topPRange && *p != .99) || (caps.topPRange && (*p < .99 || *p > 1))) {
		return fmt.Errorf("%s does not support this top_p; omit it or use 0.99", chat.Model)
	}
	if chat.Temperature != nil && chat.TopP != nil {
		return fmt.Errorf("%s does not allow temperature and top_p together; omit one", chat.Model)
	}
	return nil
}
