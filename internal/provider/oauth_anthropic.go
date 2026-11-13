package provider

import (
	"context"
	"net/http"
	"net/url"

	"sharetoken/internal/node"
)

// Anthropic: the Claude subscription login of Claude Code. A Claude Pro or
// Max account signs in through claude.com, approves, and the page shows a
// "code#state" to paste back. Requests then go to the Messages API as that
// account, carrying the bearer token and the beta that makes the API take it
// in place of an API key. The API serves such a token to Claude Code only,
// so every request leads with Claude Code's own system line (claudeCodeIdentity,
// in anthropic.go).

const (
	claudeClientID  = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	claudeAuthURL   = "https://claude.com/cai/oauth/authorize"
	claudeTokenURL  = "https://platform.claude.com/v1/oauth/token"
	claudeRedirect  = "https://platform.claude.com/oauth/code/callback"
	claudeScope     = "org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"
	claudeOAuthBeta = "oauth-2025-04-20" // without this the API will not take an OAuth token
)

// claudeOAuthModels is what a subscription login may use when the models
// endpoint cannot be listed with it.
var claudeOAuthModels = []string{
	"claude-fable-5-1", "claude-fable-5",
	"claude-opus-5-5", "claude-opus-5", "claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6", "claude-opus-4-5-20251101",
	"claude-sonnet-5-5", "claude-sonnet-5", "claude-sonnet-4-6", "claude-sonnet-4-5-20250929",
	"claude-haiku-5-5", "claude-haiku-4-5-20251001",
}

func init() {
	oauthFlows[KindAnthropic] = &oauthFlow{
		start: func(l *Login) error {
			l.redirect = claudeRedirect
			q := url.Values{
				"code":                  {"true"},
				"client_id":             {claudeClientID},
				"response_type":         {"code"},
				"redirect_uri":          {l.redirect},
				"scope":                 {claudeScope},
				"state":                 {l.state},
				"code_challenge":        {challengeS256(l.verifier)},
				"code_challenge_method": {"S256"},
			}
			l.URL = claudeAuthURL + "?" + q.Encode()
			l.Hint = "After approving, the page shows an authorization code that looks like `code#state`. Copy the whole thing and paste it back."
			return nil
		},
		exchange: func(ctx context.Context, l *Login, code string) (*Token, error) {
			r, err := postToken(ctx, claudeTokenURL, claudeOAuthUserAgent(), url.Values{
				"grant_type":    {"authorization_code"},
				"code":          {code},
				"state":         {l.state}, // the token server checks it against the authorize request
				"client_id":     {claudeClientID},
				"redirect_uri":  {l.redirect},
				"code_verifier": {l.verifier},
			}, true)
			if err != nil {
				return nil, err
			}
			return claudeToken(r), nil
		},
		refresh: func(ctx context.Context, old *Token) (*Token, error) {
			r, err := postToken(ctx, claudeTokenURL, claudeOAuthUserAgent(), url.Values{
				"grant_type":    {"refresh_token"},
				"refresh_token": {old.RefreshToken},
				"client_id":     {claudeClientID},
			}, true)
			if err != nil {
				return nil, err
			}
			return claudeToken(r), nil
		},
		upstream: func(e *Entry, ts TokenSource) (node.Upstream, error) {
			return &anthropicUpstream{base: e.URL(), limits: outputLimits{set: e.caps()}, ts: ts, oauth: true}, nil
		},
		models: func(ctx context.Context, e *Entry, ts TokenSource) ([]string, error) {
			var ids []string
			err := withToken(ctx, ts, func(tok *Token) error {
				var err error
				ids, err = anthropicModels(ctx, e.URL(), anthropicOAuthHeader(tok))
				return err
			})
			if se, ok := err.(*node.StatusError); ok && se.Status == http.StatusUnauthorized {
				return nil, err
			}
			if len(ids) == 0 {
				return append([]string(nil), claudeOAuthModels...), nil
			}
			return ids, nil
		},
	}
}

// claudeToken is the token of an answer from the token server, with the
// account it names: the email address, and the organization the login acts for.
func claudeToken(r *tokenResponse) *Token {
	t := r.token()
	if r.Account != nil {
		t.Email = r.Account.Email
	}
	if r.Organization != nil {
		t.AccountID = r.Organization.UUID
	}
	return t
}

// claudeOAuthUserAgent names this client to the token server, which refuses a
// request with no User-Agent.
func claudeOAuthUserAgent() http.Header {
	return http.Header{"User-Agent": {"sharetoken-anthropic-oauth/1.0"}}
}

// anthropicOAuthHeader authenticates a Messages request made with a Claude
// subscription login: the bearer token and the beta that lets the API take
// it, and no x-api-key.
func anthropicOAuthHeader(tok *Token) http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+tok.AccessToken)
	h.Set("anthropic-beta", claudeOAuthBeta)
	return h
}
