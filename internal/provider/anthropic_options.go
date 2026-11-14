package provider

import "fmt"

// AnthropicOptions selects a complete request profile. Standard retains the
// original adapter; claude-code uses the versioned OAuth compatibility profile.
type AnthropicOptions struct {
	Profile  string `json:"profile,omitempty"`
	Sampling string `json:"sampling,omitempty"` // strict or default (discard unsupported sampling)
}

func (o AnthropicOptions) validate(auth string) error {
	switch o.Profile {
	case "", "standard":
	case "claude-code":
		if auth != AuthOAuth {
			return fmt.Errorf("claude-code profile requires Anthropic OAuth")
		}
	default:
		return fmt.Errorf("Anthropic profile must be standard or claude-code")
	}
	if o.Sampling != "" && o.Sampling != "strict" && o.Sampling != "default" {
		return fmt.Errorf("Anthropic sampling must be strict or default")
	}
	return nil
}

func (e *Entry) claudeProfile() bool {
	return e.Type == KindAnthropic && e.Auth == AuthOAuth && e.Anthropic != nil && e.Anthropic.Profile == "claude-code"
}

// AnthropicIdentity survives token rotation but is replaced with the account.
type AnthropicIdentity struct {
	DeviceID   string `json:"device_id"`
	SessionKey string `json:"session_key"`
}

func (e *Entry) ensureIdentity() {
	if e.ID == "" {
		e.ID = randomUUID()
	}
	if e.Type == KindAnthropic && e.Auth == AuthOAuth && e.Identity == nil {
		e.Identity = &AnthropicIdentity{DeviceID: randomHexBytes(32), SessionKey: randomHexBytes(32)}
	}
}

func sameAnthropicAccount(a, b *Token) bool {
	if a == nil || b == nil {
		return false
	}
	if a.AccountUUID != "" && b.AccountUUID != "" {
		return a.AccountUUID == b.AccountUUID
	}
	return a.RefreshToken != "" && a.RefreshToken == b.RefreshToken
}
