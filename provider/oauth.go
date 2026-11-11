package provider

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"sharetoken/node"
)

// An OAuth login takes two steps, so that it works on a machine without a
// browser and from a GUI alike: Start gives the address the user opens in any
// browser, and Finish takes what the browser ended up with — the code the
// provider's page shows, or the whole address of the page it redirected to.

// Login is a login in progress.
type Login struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	URL  string `json:"url"` // open this in a browser
	// Hint tells the user what to bring back.
	Hint string `json:"hint"`
	// Project is a Google Cloud project to use for a Gemini or Antigravity
	// login whose account has none of its own.
	Project string `json:"project,omitempty"`

	state, verifier, redirect string
	created                   time.Time
}

// oauthFlow is the login of one kind.
type oauthFlow struct {
	// start fills l.URL and l.Hint; state and verifier are already set.
	start func(l *Login) error
	// exchange turns the code into a token, with all the account details
	// requests will need.
	exchange func(ctx context.Context, l *Login, code string) (*Token, error)
	// refresh renews a token; fields it leaves empty keep their old values.
	refresh func(ctx context.Context, old *Token) (*Token, error)
	// upstream builds the model server, and models lists what a login can use.
	upstream func(e *Entry, ts TokenSource) (node.Upstream, error)
	models   func(ctx context.Context, e *Entry, ts TokenSource) ([]string, error)
}

var oauthFlows = map[string]*oauthFlow{}

// ErrOAuthUnavailable is returned for a kind whose OAuth login this build
// cannot do.
var ErrOAuthUnavailable = errors.New("this client has no OAuth login for this provider")

func flowOf(kind string) (*oauthFlow, error) {
	if f := oauthFlows[kind]; f != nil {
		return f, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrOAuthUnavailable, kind)
}

// OAuthSupported reports whether this build can log in to kind.
func OAuthSupported(kind string) bool { return oauthFlows[kind] != nil }

const loginTTL = 30 * time.Minute

var (
	loginMu sync.Mutex
	logins  = map[string]*Login{}
)

// StartLogin begins an OAuth login for a kind. project is only for Google
// logins, and usually empty.
func StartLogin(kind, project string) (*Login, error) {
	k, ok := LookupKind(kind)
	if !ok || !k.OAuth {
		return nil, fmt.Errorf("%q has no OAuth login", kind)
	}
	f, err := flowOf(k.ID)
	if err != nil {
		return nil, err
	}
	l := &Login{ID: randomHex(16), Type: k.ID, Project: project, state: randomHex(32), verifier: randomURLSafe(64), created: time.Now()}
	if err := f.start(l); err != nil {
		return nil, err
	}
	loginMu.Lock()
	defer loginMu.Unlock()
	for id, old := range logins {
		if time.Since(old.created) > loginTTL {
			delete(logins, id)
		}
	}
	logins[l.ID] = l
	return l, nil
}

// FinishLogin completes a login with what the user brought back from the
// browser: a code, "code#state", or the address of the page they landed on.
func FinishLogin(ctx context.Context, id, input string) (*Token, string, error) {
	loginMu.Lock()
	l := logins[id]
	loginMu.Unlock()
	if l == nil || time.Since(l.created) > loginTTL {
		return nil, "", errors.New("this login has expired: start again")
	}
	return l.Finish(ctx, input)
}

// Finish is FinishLogin for the Login itself.
func (l *Login) Finish(ctx context.Context, input string) (*Token, string, error) {
	f, err := flowOf(l.Type)
	if err != nil {
		return nil, "", err
	}
	code, state := parseCallback(input)
	if code == "" {
		return nil, "", errors.New("no authorization code in what was pasted")
	}
	if state != "" && state != l.state {
		return nil, "", errors.New("the code belongs to another login attempt: start again")
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	tok, err := f.exchange(ctx, l, code)
	if err != nil {
		return nil, "", err
	}
	loginMu.Lock()
	delete(logins, l.ID)
	loginMu.Unlock()
	return tok, l.Type, nil
}

// parseCallback finds the code (and the state, if it is there) in a pasted
// callback address, a "code#state" pair, or a bare code.
func parseCallback(input string) (code, state string) {
	input = strings.TrimSpace(input)
	if strings.Contains(input, "://") || strings.HasPrefix(input, "?") || strings.Contains(input, "code=") {
		q := input
		if i := strings.IndexAny(q, "?#"); i >= 0 {
			q = q[i+1:]
		}
		q = strings.ReplaceAll(q, "#", "&")
		if v, err := url.ParseQuery(q); err == nil && v.Get("code") != "" {
			return v.Get("code"), v.Get("state")
		}
	}
	code, state, _ = strings.Cut(input, "#")
	return strings.TrimSpace(code), strings.TrimSpace(state)
}

func oauthUpstream(e *Entry, ts TokenSource) (node.Upstream, error) {
	f, err := flowOf(e.Type)
	if err != nil {
		return nil, err
	}
	return f.upstream(e, ts)
}

func oauthModels(ctx context.Context, e *Entry, ts TokenSource) ([]string, error) {
	f, err := flowOf(e.Type)
	if err != nil {
		return nil, err
	}
	return f.models(ctx, e, ts)
}

// Tokens keeps the OAuth tokens of home's providers current: it hands out
// the saved token, renews one that is about to expire, and writes the new
// one back to the providers file.
type Tokens struct {
	Home string
	mu   sync.Mutex
	one  map[string]*sync.Mutex // per provider: one renewal at a time
}

// renewBefore: a token with less than this left is renewed before use.
const renewBefore = 5 * time.Minute

// For returns the token source of the provider called name.
func (t *Tokens) For(name string) TokenSource { return &tokenSource{t: t, name: name} }

type tokenSource struct {
	t    *Tokens
	name string
}

func (s *tokenSource) Token(ctx context.Context, force bool) (*Token, error) {
	s.t.mu.Lock()
	if s.t.one == nil {
		s.t.one = map[string]*sync.Mutex{}
	}
	mu := s.t.one[s.name]
	if mu == nil {
		mu = &sync.Mutex{}
		s.t.one[s.name] = mu
	}
	s.t.mu.Unlock()
	mu.Lock()
	defer mu.Unlock()

	f, err := Load(s.t.Home)
	if err != nil {
		return nil, err
	}
	e, err := f.Find(s.name)
	if err != nil {
		return nil, err
	}
	if e.OAuth == nil {
		return nil, fmt.Errorf("provider %s is not logged in", s.name)
	}
	tok := *e.OAuth
	fresh := tok.AccessToken != "" && (tok.ExpiresAt.IsZero() || time.Until(tok.ExpiresAt) > renewBefore)
	if fresh && !force {
		return &tok, nil
	}
	if tok.RefreshToken == "" {
		if fresh {
			return &tok, nil // nothing to renew it with: the server has the last word
		}
		return nil, fmt.Errorf("the login of provider %s has expired: `sharetoken provider login %s -name %s`", s.name, e.Type, s.name)
	}
	flow, err := flowOf(e.Type)
	if err != nil {
		return nil, err
	}
	rctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	nt, err := flow.refresh(rctx, &tok)
	if err != nil {
		if fresh {
			return &tok, nil
		}
		return nil, fmt.Errorf("renewing the login of provider %s: %w", s.name, err)
	}
	merged := mergeToken(&tok, nt)
	err = Update(s.t.Home, func(f *File) error {
		if cur := f.Get(s.name); cur != nil && cur.Auth == AuthOAuth {
			cur.OAuth = merged
		}
		return nil
	})
	return merged, err
}

// mergeToken keeps what a renewal did not send again.
func mergeToken(old, nt *Token) *Token {
	out := *nt
	if out.RefreshToken == "" {
		out.RefreshToken = old.RefreshToken
	}
	if out.Email == "" {
		out.Email = old.Email
	}
	if out.AccountID == "" {
		out.AccountID = old.AccountID
	}
	if out.ProjectID == "" {
		out.ProjectID = old.ProjectID
	}
	if out.Plan == "" {
		out.Plan = old.Plan
	}
	if len(out.Extra) == 0 {
		out.Extra = old.Extra
	}
	return &out
}

// fixedToken is the token source of a login that is not saved yet.
type fixedToken struct{ tok *Token }

func (f fixedToken) Token(context.Context, bool) (*Token, error) { return f.tok, nil }

// FixedToken is a token source that always gives tok.
func FixedToken(tok *Token) TokenSource { return fixedToken{tok} }

// withToken runs do with the current token, and once more with a renewed one
// when the server refuses the first before sending anything.
func withToken(ctx context.Context, ts TokenSource, do func(tok *Token) error) error {
	tok, err := ts.Token(ctx, false)
	if err != nil {
		return err
	}
	err = do(tok)
	var se *node.StatusError
	if errors.As(err, &se) && se.Status == http.StatusUnauthorized {
		nt, rerr := ts.Token(ctx, true)
		if rerr != nil || nt.AccessToken == tok.AccessToken {
			return err
		}
		return do(nt)
	}
	return err
}

// ---- shared pieces of the flows ----

var timeNow = time.Now

// randomUUID is a random (version 4) UUID.
func randomUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func randomURLSafe(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func randomHexBytes(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// challengeS256 is the PKCE challenge of a verifier.
func challengeS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// tokenResponse is what token endpoints answer.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Error        any    `json:"error"`
	ErrorDesc    string `json:"error_description"`
	// Anthropic names the account beside the token.
	Organization *struct {
		UUID string `json:"uuid"`
	} `json:"organization"`
	Account *struct {
		UUID  string `json:"uuid"`
		Email string `json:"email_address"`
	} `json:"account"`
}

func (r *tokenResponse) token() *Token {
	t := &Token{AccessToken: r.AccessToken, RefreshToken: r.RefreshToken}
	if r.ExpiresIn > 0 {
		t.ExpiresAt = time.Now().Add(time.Duration(r.ExpiresIn) * time.Second).UTC().Truncate(time.Second)
	}
	return t
}

// postToken posts a token request, form-encoded or as JSON.
func postToken(ctx context.Context, endpoint string, header http.Header, form url.Values, asJSON bool) (*tokenResponse, error) {
	var body io.Reader
	ct := "application/x-www-form-urlencoded"
	if asJSON {
		m := map[string]string{}
		for k := range form {
			m[k] = form.Get(k)
		}
		data, _ := json.Marshal(m)
		body, ct = strings.NewReader(string(data)), "application/json"
	} else {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return nil, err
	}
	if header != nil {
		req.Header = header.Clone()
	}
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out tokenResponse
	_ = json.Unmarshal(data, &out)
	if resp.StatusCode != http.StatusOK || out.AccessToken == "" {
		return nil, fmt.Errorf("the login server answered HTTP %d: %s", resp.StatusCode, clip(strings.TrimSpace(string(data)), 300))
	}
	return &out, nil
}

// jwtClaims reads the claims of a JWT without checking its signature: the
// token came straight from the issuer over TLS.
func jwtClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil
	}
	var m map[string]any
	_ = json.Unmarshal(data, &m)
	return m
}
