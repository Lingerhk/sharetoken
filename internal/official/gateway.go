package official

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"sharetoken/internal/buyer"
	"sharetoken/internal/protocol"
)

// SettingsFile holds Settings in the client's home.
const SettingsFile = "official.json"

// Settings are the user's choices for the vendor logins.
type Settings struct {
	// ClaudeFallbackOff stops answering Claude Code from the network when its
	// plan is used up (the fallback is on by default).
	ClaudeFallbackOff bool `json:"claude_fallback_off,omitempty"`
	// ClaudeFallbackModel is the network model (bare offer name) that takes
	// over; "" picks one like the model asked for (pickFallback).
	ClaudeFallbackModel string `json:"claude_fallback_model,omitempty"`
	// CodexGuardPercent holds Codex's official models once a window of the
	// ChatGPT plan reaches it; 0 means the default (95), 100 or more turns
	// the guard off. The Codex Desktop unlock turns it off as well.
	CodexGuardPercent float64 `json:"codex_guard_percent,omitempty"`
	// CodexUnlock: Codex Desktop's ChatGPT backend requests go through the
	// gateway, which keeps a spent plan from locking its input (unlock.go).
	CodexUnlock bool `json:"codex_unlock,omitempty"`
}

// DefaultCodexGuard is CodexGuardPercent's default.
const DefaultCodexGuard = 95

func (s Settings) codexGuard() float64 {
	switch {
	case s.CodexUnlock:
		return 100
	case s.CodexGuardPercent <= 0:
		return DefaultCodexGuard
	}
	return s.CodexGuardPercent
}

// LoadSettings reads home's SettingsFile; a missing file is the defaults.
func LoadSettings(home string) (Settings, error) {
	var s Settings
	data, err := os.ReadFile(filepath.Join(home, SettingsFile))
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(data, &s)
}

// SaveSettings writes home's SettingsFile.
func SaveSettings(home string, s Settings) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(home, SettingsFile+".tmp")
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(home, SettingsFile))
}

// Upstreams are the vendors' endpoints. Only tests change them (and
// SHARETOKEN_OFFICIAL_* in a development build's environment,
// EnvUpstreams): the users' credentials go nowhere else.
type Upstreams struct {
	Anthropic      string // https://api.anthropic.com
	ChatGPT        string // https://chatgpt.com/backend-api/codex
	ChatGPTBackend string // https://chatgpt.com/backend-api
	OpenAI         string // https://api.openai.com/v1
}

// DefaultUpstreams are the vendors' own endpoints.
var DefaultUpstreams = Upstreams{
	Anthropic:      "https://api.anthropic.com",
	ChatGPT:        "https://chatgpt.com/backend-api/codex",
	ChatGPTBackend: "https://chatgpt.com/backend-api",
	OpenAI:         "https://api.openai.com/v1",
}

// ModelsFunc lists the network's models (buyer.Client.Models).
type ModelsFunc func(context.Context) ([]protocol.ModelSummary, error)

// Gateway routes the local endpoint's requests between the network and the
// vendors. One serves a client for its life, across its network sessions.
type Gateway struct {
	home string
	up   Upstreams
	hc   *http.Client
	log  *slog.Logger

	mu       sync.Mutex
	settings Settings
	claude   claudeState
	codex    map[string]*codexState // by ChatGPT account
	turns    turnModels
	official officialModels

	ctx       context.Context // the client's life (StartUnlock)
	unlockSrv []*http.Server
	unlockErr string
}

// New returns the gateway of the client whose state is in home.
func New(home string, log *slog.Logger) *Gateway {
	if log == nil {
		log = slog.Default()
	}
	s, err := LoadSettings(home)
	if err != nil {
		log.Warn("official settings unreadable, using defaults", "component", "official", "err", err)
	}
	return &Gateway{home: home, up: DefaultUpstreams, hc: newUpstreamClient(), log: log.With("component", "official"),
		settings: s, claude: claudeState{logins: map[string]*claudeLogin{}}, codex: map[string]*codexState{}, turns: newTurnModels(500)}
}

// EnvUpstreams are the vendors' endpoints with what SHARETOKEN_OFFICIAL_ANTHROPIC
// and SHARETOKEN_OFFICIAL_CHATGPT name in their place. The caller decides
// whether the environment may: a production build's may not (the client's
// newGateway), or it could send the user's logins and prompts anywhere.
func EnvUpstreams() Upstreams {
	up := DefaultUpstreams
	if v := os.Getenv("SHARETOKEN_OFFICIAL_ANTHROPIC"); v != "" {
		up.Anthropic = v
	}
	if v := os.Getenv("SHARETOKEN_OFFICIAL_CHATGPT"); v != "" {
		up.ChatGPT = v
		up.ChatGPTBackend = strings.TrimSuffix(strings.TrimRight(v, "/"), "/codex")
	}
	return up
}

// SetUpstreams points the gateway at other endpoints (tests, and
// EnvUpstreams in a development build).
func (g *Gateway) SetUpstreams(u Upstreams) { g.up = u }

// Upstreams are the endpoints the gateway sends to.
func (g *Gateway) Upstreams() Upstreams { return g.up }

// Settings returns the current settings.
func (g *Gateway) Settings() Settings {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.settings
}

// SetSettings saves and applies s.
func (g *Gateway) SetSettings(s Settings) error {
	if err := SaveSettings(g.home, s); err != nil {
		return err
	}
	g.mu.Lock()
	g.settings = s
	g.mu.Unlock()
	g.syncUnlock()
	return nil
}

// Wrap returns the local /v1 endpoint: network serves the network's
// requests (the buyer proxy, or what says the client is not connected), and
// models lists the network's models (nil while not connected).
func (g *Gateway) Wrap(network http.Handler, models ModelsFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cred := Classify(r.Header)
		if cred == CredNone {
			network.ServeHTTP(w, r)
			return
		}
		p := path.Clean("/" + r.URL.Path)
		switch {
		case r.Method == http.MethodGet && p == "/v1/responses" && isWebSocket(r):
			if cred.openai() {
				// Codex falls back to HTTP for the session (as with ferry):
				// a request's model is known only inside the socket.
				writeVendorError(w, cred, http.StatusUpgradeRequired, "this endpoint answers Codex over HTTP, not WebSockets")
				return
			}
			g.toNetwork(w, r, nil, "", network)
			return
		case r.Method == http.MethodGet && p == "/v1/models":
			g.serveModels(w, r, cred, network, models)
			return
		case r.Method != http.MethodPost:
			g.passthrough(w, r, cred, nil)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, protocol.MaxRequestBody))
		if err != nil {
			writeVendorError(w, cred, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		model := bodyModel(body)
		switch {
		case p == "/v1/chat/completions":
			g.toNetwork(w, r, body, "", network)
		case p == "/v1/images/generations" || p == "/v1/images/edits":
			g.images(w, r, cred, body)
		case buyer.IsListedModel(model):
			if cred == CredChatGPT && strings.HasPrefix(p, "/v1/responses") {
				g.turns.note(r.Header, body, model)
			}
			g.toNetwork(w, r, body, "", network)
		case cred.openai() && strings.HasPrefix(p, "/v1/responses"):
			if parent := g.turns.parentOfSubagent(r.Header, body); parent != "" {
				// A subagent of a turn on a network model stays on it.
				g.toNetwork(w, r, body, parent, network)
				return
			}
			g.turns.note(r.Header, body, model)
			g.codexResponses(w, r, cred, body, strings.TrimPrefix(p, "/v1/"))
		case cred.anthropic() && p == "/v1/messages":
			g.claudeMessages(w, r, cred, body, model, network, models)
		default:
			g.passthrough(w, r, cred, body)
		}
	})
}

// toNetwork serves a request from the network, with its body (nil: the
// request's own) and, when model is set, that model in place of its own.
// The vendor's credentials are taken off first: they are the user's alone.
func (g *Gateway) toNetwork(w http.ResponseWriter, r *http.Request, body []byte, model string, network http.Handler) {
	if KeyMissing(r.Context()) {
		writeVendorError(w, Classify(r.Header), http.StatusUnauthorized,
			"ShareToken models want the client's API key in the "+HeaderKey+" header: run `sharetoken connect` again, or see `sharetoken apikey`")
		return
	}
	if model != "" && body != nil {
		if b, err := setBodyModel(body, model); err == nil {
			body = b
		}
	}
	r2 := r.Clone(r.Context())
	r2.Header = r.Header.Clone()
	for _, h := range []string{"Authorization", "X-Api-Key", "Chatgpt-Account-Id", "Cookie", HeaderKey} {
		r2.Header.Del(h)
	}
	if body != nil {
		r2.Body = io.NopCloser(bytes.NewReader(body))
		r2.ContentLength = int64(len(body))
	}
	network.ServeHTTP(w, r2)
}

// bodyModel is a request body's model ("" if none).
func bodyModel(body []byte) string {
	var q struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &q)
	return q.Model
}

// setBodyModel returns body with its model replaced.
func setBodyModel(body []byte, model string) ([]byte, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	m["model"], _ = json.Marshal(model)
	return json.Marshal(m)
}

func isWebSocket(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// writeVendorError answers with an error in the shape the vendor's clients
// read.
func writeVendorError(w http.ResponseWriter, cred Cred, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if cred.anthropic() {
		typ := "api_error"
		switch status {
		case http.StatusBadRequest, http.StatusRequestEntityTooLarge:
			typ = "invalid_request_error"
		case http.StatusUnauthorized:
			typ = "authentication_error"
		case http.StatusTooManyRequests:
			typ = "rate_limit_error"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]string{"type": typ, "message": msg}})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg, "type": "sharetoken_error", "code": nil}})
}

// now is the clock (tests set it).
var now = time.Now
