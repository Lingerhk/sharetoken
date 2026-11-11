package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"time"

	"sharetoken/node"
)

// Grok: the login of the Grok CLI. Its API speaks chat completions as it is;
// requests must carry the CLI's identity or the proxy refuses a valid login.

const (
	grokClientID   = "b1a00492-073a-47ea-816f-4c329264a828"
	grokAuthURL    = "https://auth.x.ai/oauth2/authorize"
	grokTokenURL   = "https://auth.x.ai/oauth2/token"
	grokRedirect   = "http://127.0.0.1:56121/callback"
	grokScope      = "openid profile email offline_access grok-cli:access api:access"
	grokCLIBaseURL = "https://cli-chat-proxy.grok.com/v1"
	grokCLIVersion = "1.0.46" // the proxy refuses versions it considers too old: follow https://x.ai/cli/stable
)

var grokModels = []string{"grok-4.7", "grok-4.6", "grok-4.5", "grok-4.3", "grok-build-0.1", "grok-composer-2.5-fast",
	"grok-4.20-0309-reasoning", "grok-4.20-0309-non-reasoning", "grok-4.20-multi-agent-0309"}

func init() {
	oauthFlows[KindGrok] = &oauthFlow{
		start: func(l *Login) error {
			l.verifier = randomURLSafe(32)
			l.redirect = grokRedirect
			q := url.Values{
				"response_type":         {"code"},
				"client_id":             {grokClientID},
				"redirect_uri":          {l.redirect},
				"scope":                 {grokScope},
				"state":                 {l.state},
				"nonce":                 {randomHex(16)},
				"code_challenge":        {challengeS256(l.verifier)},
				"code_challenge_method": {"S256"},
				"plan":                  {"generic"},
				"referrer":              {"sharetoken"},
			}
			l.URL = grokAuthURL + "?" + q.Encode()
			l.Hint = "After signing in, the browser goes to a page at 127.0.0.1:56121 that does not load. Copy the whole address of that page from the address bar."
			return nil
		},
		exchange: func(ctx context.Context, l *Login, code string) (*Token, error) {
			r, err := postToken(ctx, grokTokenURL, grokAuthHeader(), url.Values{
				"grant_type":    {"authorization_code"},
				"client_id":     {grokClientID},
				"code":          {code},
				"redirect_uri":  {l.redirect},
				"code_verifier": {l.verifier},
			}, false)
			if err != nil {
				return nil, err
			}
			return grokToken(r), nil
		},
		refresh: func(ctx context.Context, old *Token) (*Token, error) {
			r, err := postToken(ctx, grokTokenURL, grokAuthHeader(), url.Values{
				"grant_type":    {"refresh_token"},
				"client_id":     {grokClientID},
				"refresh_token": {old.RefreshToken},
			}, false)
			if err != nil {
				return nil, err
			}
			return grokToken(r), nil
		},
		upstream: func(e *Entry, ts TokenSource) (node.Upstream, error) {
			return &openAIUpstream{base: e.URL(), ts: ts, auth: grokHeader, limits: outputLimits{set: e.caps()}}, nil
		},
		models: func(ctx context.Context, e *Entry, ts TokenSource) ([]string, error) {
			var ids []string
			err := withToken(ctx, ts, func(tok *Token) error {
				h := grokHeader(tok)
				if tok.AccountID != "" {
					h.Set("X-UserID", tok.AccountID)
				}
				if tok.Email != "" {
					h.Set("X-Email", tok.Email)
				}
				var err error
				ids, err = openAIModels(ctx, e.URL(), h)
				return err
			})
			if se, ok := err.(*node.StatusError); ok && se.Status == http.StatusUnauthorized {
				return nil, err
			}
			if len(ids) == 0 {
				return append([]string(nil), grokModels...), nil
			}
			return ids, nil
		},
	}
}

func grokAuthHeader() http.Header {
	return http.Header{"User-Agent": {"sharetoken-grok-oauth/1.0"}}
}

func grokToken(r *tokenResponse) *Token {
	t := r.token()
	if r.ExpiresIn <= 0 {
		t.ExpiresAt = timeNow().Add(6 * time.Hour).UTC()
	}
	for _, claims := range []map[string]any{jwtClaims(r.IDToken), jwtClaims(r.AccessToken)} {
		if t.Email == "" {
			t.Email, _ = claims["email"].(string)
		}
		if t.AccountID == "" {
			t.AccountID, _ = claims["sub"].(string)
		}
	}
	if tier, ok := jwtClaims(r.AccessToken)["tier"].(float64); ok {
		names := []string{"free", "supergrok", "x_basic", "x_premium", "x_premium_plus", "supergrok_heavy", "supergrok_lite", "supergrok_plus"}
		if i := int(tier); i >= 0 && i < len(names) {
			t.Plan = names[i]
		}
	}
	return t
}

// grokHeader is the Grok CLI's identity.
func grokHeader(tok *Token) http.Header {
	platform, arch := runtime.GOOS, runtime.GOARCH
	if platform == "darwin" {
		platform = "macos"
	}
	switch arch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	}
	h := http.Header{}
	h.Set("Authorization", "Bearer "+tok.AccessToken)
	h.Set("User-Agent", fmt.Sprintf("grok-pager/%s grok-shell/%s (%s; %s)", grokCLIVersion, grokCLIVersion, platform, arch))
	h.Set("x-grok-client-version", grokCLIVersion)
	h.Set("x-grok-client-identifier", "grok-pager")
	h.Set("x-grok-client-mode", "interactive")
	h.Set("X-XAI-Token-Auth", "xai-grok-cli")
	h.Set("x-authenticateresponse", "authenticate-response")
	return h
}
