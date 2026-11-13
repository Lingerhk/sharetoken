package provider

import (
	"fmt"
	"net/url"
	"strings"

	"sharetoken/internal/cli"
)

// Kind is a provider this client knows how to talk to.
type Kind struct {
	ID   string
	Name string
	// APIKey / OAuth: the ways to authenticate. KeyOptional: the API may need
	// no key (a custom server on the local network).
	APIKey, OAuth, KeyOptional bool
	BaseURL                    string // API address with a key
	OAuthURL                   string // API address with an OAuth login
	KeyPage                    string // where an account's API keys are made
	Note                       string // one line for the list of kinds
}

// Kind IDs.
const (
	KindDeepSeek    = "deepseek"
	KindOpenAI      = "openai"
	KindAnthropic   = "anthropic"
	KindGemini      = "gemini"
	KindAntigravity = "antigravity"
	KindGrok        = "grok"
	KindCustom      = "custom"
)

var kinds = []Kind{
	{ID: KindAnthropic, Name: "Anthropic", APIKey: true, OAuth: true,
		BaseURL: "https://api.anthropic.com", OAuthURL: "https://api.anthropic.com",
		KeyPage: "https://console.anthropic.com/settings/keys", Note: "API key, or a Claude subscription login"},
	{ID: KindOpenAI, Name: "OpenAI", APIKey: true, OAuth: true,
		BaseURL: "https://api.openai.com/v1", OAuthURL: "https://chatgpt.com/backend-api/codex",
		KeyPage: "https://platform.openai.com/api-keys", Note: "API key, or a ChatGPT (Codex) login"},
	{ID: KindGemini, Name: "Gemini", APIKey: true, OAuth: true,
		BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", OAuthURL: "https://cloudcode-pa.googleapis.com",
		KeyPage: "https://aistudio.google.com/apikey", Note: "AI Studio API key, or a Google (Gemini CLI) login"},
	{ID: KindAntigravity, Name: "Antigravity", OAuth: true,
		OAuthURL: "https://cloudcode-pa.googleapis.com", Note: "Google Antigravity login"},
	{ID: KindGrok, Name: "Grok", APIKey: true, OAuth: true,
		BaseURL: "https://api.x.ai/v1", OAuthURL: "https://cli-chat-proxy.grok.com/v1",
		KeyPage: "https://console.x.ai", Note: "xAI API key, or a Grok login"},
	{ID: KindDeepSeek, Name: "DeepSeek", APIKey: true,
		BaseURL: "https://api.deepseek.com", KeyPage: "https://platform.deepseek.com/api_keys", Note: "API key"},
	{ID: KindCustom, Name: "Custom", APIKey: true, KeyOptional: true,
		Note: "any OpenAI-compatible API: give its base URL with -url"},
}

// Kinds lists the providers this client knows.
func Kinds() []Kind { return append([]Kind(nil), kinds...) }

// LookupKind finds a kind by ID.
func LookupKind(id string) (Kind, bool) {
	for _, k := range kinds {
		if k.ID == strings.ToLower(id) {
			return k, true
		}
	}
	return Kind{}, false
}

// CheckSecretURL checks that a key or token may be sent to base: over HTTPS,
// or to a machine on the local network.
func CheckSecretURL(base string) error {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("bad API address %q", base)
	}
	if u.Scheme == "http" && !cli.IsLocalHost(u.Hostname()) {
		return fmt.Errorf("refusing to send a key over plain HTTP to %s: use https://", u.Host)
	}
	return nil
}
