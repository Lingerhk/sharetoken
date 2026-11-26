// Package official lets a coding tool keep its own vendor login next to the
// network (after ferry's "with subscription" mode). Claude Code and Codex
// are pointed at the local endpoint but stay signed in to claude.ai or
// ChatGPT; each request then carries the user's own credentials, and the
// gateway routes it by the model it names:
//
//   - a network model (listed as st-…, buyer.ModelPrefix) is bought on the
//     network as before, with the client's API key in X-Sharetoken-Key;
//   - any other model goes to the vendor's own endpoint, as it came, with
//     the user's own credentials: the user's plan pays for it.
//
// The credentials are passed on to the vendor's fixed hosts only, never
// stored, logged or sent to the network. On the way the gateway reads the
// plan's usage from the vendor's answers, and
//
//   - for Claude Code, when the plan's allowance is used up (a 429), it
//     answers the same request from the network instead (on by default,
//     Settings.ClaudeFallbackOff), until the window resets;
//   - for Codex, it holds the official models at Settings.CodexGuardPercent
//     with a message to switch to a network model, as Codex Desktop locks
//     its input once the plan is spent (ferry's usage guard), unless the
//     Codex Desktop unlock is on (unlock.go).
package official

import (
	"context"
	"net/http"
	"strings"
)

// Cred is the kind of vendor credentials a request carries.
type Cred int

const (
	// CredNone: no vendor credentials (the client's own API key or none).
	CredNone Cred = iota
	// CredClaudeOAuth: Claude Code signed in to claude.ai (Bearer token and
	// an oauth-… beta).
	CredClaudeOAuth
	// CredAnthropicKey: an Anthropic API key (x-api-key sk-ant-…).
	CredAnthropicKey
	// CredChatGPT: Codex signed in with ChatGPT (Bearer token and
	// chatgpt-account-id).
	CredChatGPT
	// CredOpenAIKey: an OpenAI API key (Bearer sk-…).
	CredOpenAIKey
)

func (c Cred) String() string {
	switch c {
	case CredClaudeOAuth:
		return "claude_oauth"
	case CredAnthropicKey:
		return "anthropic_key"
	case CredChatGPT:
		return "chatgpt"
	case CredOpenAIKey:
		return "openai_key"
	}
	return "none"
}

// anthropic: whether the credentials are Anthropic's.
func (c Cred) anthropic() bool { return c == CredClaudeOAuth || c == CredAnthropicKey }

// openai: whether the credentials are OpenAI's.
func (c Cred) openai() bool { return c == CredChatGPT || c == CredOpenAIKey }

// ClientKeyPrefix starts the local endpoint's own API keys, which are never
// a vendor's.
const ClientKeyPrefix = "sk-st-"

// HeaderKey carries the client's API key when Authorization holds the
// vendor's credentials.
const HeaderKey = "X-Sharetoken-Key"

// Classify tells which vendor credentials h carries.
func Classify(h http.Header) Cred {
	bearer, ok := strings.CutPrefix(strings.TrimSpace(h.Get("Authorization")), "Bearer ")
	bearer = strings.TrimSpace(bearer)
	if !ok || bearer == "" || strings.HasPrefix(bearer, ClientKeyPrefix) {
		bearer = ""
	}
	switch {
	case bearer != "" && strings.TrimSpace(h.Get("chatgpt-account-id")) != "":
		return CredChatGPT
	case bearer != "" && oauthBeta(h.Get("anthropic-beta")):
		return CredClaudeOAuth
	case strings.HasPrefix(strings.TrimSpace(h.Get("x-api-key")), "sk-ant-"):
		return CredAnthropicKey
	case strings.HasPrefix(bearer, "sk-ant-"):
		return CredAnthropicKey
	case strings.HasPrefix(bearer, "sk-"):
		return CredOpenAIKey
	}
	return CredNone
}

func oauthBeta(betas string) bool {
	for _, b := range strings.Split(betas, ",") {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(b)), "oauth-") {
			return true
		}
	}
	return false
}

type keyMissing struct{}

// WithoutKey marks a request that came with vendor credentials but without
// the client's API key: it may use the vendor, not the network.
func WithoutKey(ctx context.Context) context.Context {
	return context.WithValue(ctx, keyMissing{}, true)
}

// KeyMissing reports whether ctx was marked by WithoutKey.
func KeyMissing(ctx context.Context) bool {
	v, _ := ctx.Value(keyMissing{}).(bool)
	return v
}

type keyShown struct{}

// WithKey marks a /backend-api request that came with the client's API key
// in its path (/k/<key>/backend-api/…): its sender reads this user's files,
// so it may be lent Codex's own login (serveBackend).
func WithKey(ctx context.Context) context.Context {
	return context.WithValue(ctx, keyShown{}, true)
}

// HasKey reports whether ctx was marked by WithKey.
func HasKey(ctx context.Context) bool {
	v, _ := ctx.Value(keyShown{}).(bool)
	return v
}
