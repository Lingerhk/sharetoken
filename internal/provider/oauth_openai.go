package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"

	"sharetoken/internal/node"
	"sharetoken/internal/protocol"
)

// OpenAI: the ChatGPT login of the Codex CLI. Requests go to the ChatGPT
// Codex backend as Responses requests, with the account the login belongs to.

const (
	codexClientID  = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexAuthURL   = "https://auth.openai.com/oauth/authorize"
	codexTokenURL  = "https://auth.openai.com/oauth/token"
	codexRedirect  = "http://localhost:1455/auth/callback"
	codexScope     = "openid profile email offline_access"
	codexRefScope  = "openid profile email"
	codexVersion   = "0.146.0"
	codexOrigin    = "codex_cli_rs"
	codexUserAgent = codexOrigin + "/" + codexVersion + " (Ubuntu 22.4.0; x86_64) xterm-256color"
	codexAuthClaim = "https://api.openai.com/auth"
)

// codexModels is what a ChatGPT login can use when the backend's own list
// cannot be read.
var codexModels = []string{"gpt-5.5", "gpt-5.4", "gpt-5.4-mini", "gpt-5.3-codex", "gpt-5.2"}

func init() {
	oauthFlows[KindOpenAI] = &oauthFlow{
		start: func(l *Login) error {
			l.verifier = randomHexBytes(64) // this server wants the verifier in hex
			l.redirect = codexRedirect
			q := url.Values{
				"response_type":              {"code"},
				"client_id":                  {codexClientID},
				"redirect_uri":               {l.redirect},
				"scope":                      {codexScope},
				"state":                      {l.state},
				"code_challenge":             {challengeS256(l.verifier)},
				"code_challenge_method":      {"S256"},
				"id_token_add_organizations": {"true"},
				"codex_cli_simplified_flow":  {"true"},
			}
			l.URL = codexAuthURL + "?" + q.Encode()
			l.Hint = "After signing in, the browser goes to a page at localhost:1455 that does not load. Copy the whole address of that page from the address bar."
			return nil
		},
		exchange: func(ctx context.Context, l *Login, code string) (*Token, error) {
			r, err := postToken(ctx, codexTokenURL, codexAuthHeader(), url.Values{
				"grant_type":    {"authorization_code"},
				"client_id":     {codexClientID},
				"code":          {code},
				"redirect_uri":  {l.redirect},
				"code_verifier": {l.verifier},
			}, false)
			if err != nil {
				return nil, err
			}
			return codexToken(r), nil
		},
		refresh: func(ctx context.Context, old *Token) (*Token, error) {
			r, err := postToken(ctx, codexTokenURL, codexAuthHeader(), url.Values{
				"grant_type":    {"refresh_token"},
				"refresh_token": {old.RefreshToken},
				"client_id":     {codexClientID},
				"scope":         {codexRefScope},
			}, false)
			if err != nil {
				return nil, err
			}
			return codexToken(r), nil
		},
		upstream: func(e *Entry, ts TokenSource) (node.Upstream, error) {
			return &codexUpstream{base: e.URL(), ts: ts}, nil
		},
		models: func(ctx context.Context, e *Entry, ts TokenSource) ([]string, error) {
			var out struct {
				Models []struct {
					Slug string `json:"slug"`
					ID   string `json:"id"`
				} `json:"models"`
			}
			err := withToken(ctx, ts, func(tok *Token) error {
				return getJSON(ctx, e.URL()+"/models?client_version="+codexVersion, codexHeader(tok), &out)
			})
			var ids []string
			for _, m := range out.Models {
				if id := m.Slug; id != "" {
					ids = append(ids, id)
				} else if m.ID != "" {
					ids = append(ids, m.ID)
				}
			}
			if se, ok := err.(*node.StatusError); ok && (se.Status == http.StatusUnauthorized || se.Status == http.StatusForbidden) {
				return nil, err
			}
			if len(ids) == 0 {
				return append([]string(nil), codexModels...), nil
			}
			sort.Strings(ids)
			return ids, nil
		},
	}
}

func codexAuthHeader() http.Header {
	return http.Header{"User-Agent": {codexUserAgent}, "Originator": {codexOrigin}}
}

// codexToken reads the account the login belongs to from the ID token.
func codexToken(r *tokenResponse) *Token {
	t := r.token()
	claims := jwtClaims(r.IDToken)
	t.Email, _ = claims["email"].(string)
	if auth, ok := claims[codexAuthClaim].(map[string]any); ok {
		t.AccountID, _ = auth["chatgpt_account_id"].(string)
		t.Plan, _ = auth["chatgpt_plan_type"].(string)
	}
	return t
}

// codexHeader is what every request to the Codex backend carries. The
// originator must be the product named in the User-Agent, and the version
// the one in it: the backend answers 404 otherwise.
func codexHeader(tok *Token) http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+tok.AccessToken)
	if tok.AccountID != "" {
		h.Set("chatgpt-account-id", tok.AccountID)
	}
	h.Set("originator", codexOrigin)
	h.Set("User-Agent", codexUserAgent)
	h.Set("version", codexVersion)
	return h
}

type codexUpstream struct {
	base string
	ts   TokenSource
}

func (u *codexUpstream) APIs() []string { return []string{protocol.APIResponses} }

func (u *codexUpstream) Stream(ctx context.Context, r *node.Request, emit func([]byte) error) error {
	if r.API != protocol.APIResponses {
		return fmt.Errorf("codex: cannot send a %s request", r.API)
	}
	session := u.session(r)
	return withToken(ctx, u.ts, func(tok *Token) error {
		h := codexHeader(tok)
		h.Set("session_id", session)
		return retryWithoutReasoning(r.Body, func(body []byte) error {
			return streamCodex(ctx, u.base+"/responses", h, body, emit)
		})
	})
}

// Compact implements node.Compactor with the backend's /responses/compact.
func (u *codexUpstream) Compact(ctx context.Context, r *node.Request, emit func([]byte) error) error {
	session := u.session(r)
	return withToken(ctx, u.ts, func(tok *Token) error {
		h := codexHeader(tok)
		h.Set("session_id", session)
		return retryWithoutReasoning(r.Body, func(body []byte) error {
			return compactCodex(ctx, u.base+"/responses/compact", h, body, emit)
		})
	})
}

// session is the conversation's Codex session: the buyer's, which keeps the
// backend's prompt cache of the conversation, or a new one.
func (u *codexUpstream) session(r *node.Request) string {
	if s := r.Header.Get("session_id"); s != "" {
		return s
	}
	return randomUUID()
}
