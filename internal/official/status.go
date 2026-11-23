package official

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"sharetoken/internal/provider"
)

// Status is what the gateway knows of the vendor logins it has seen.
type Status struct {
	Settings Settings      `json:"settings"`
	Claude   ClaudeStatus  `json:"claude"`
	Codex    []CodexStatus `json:"codex"`
	Unlock   UnlockStatus  `json:"unlock"`
}

// ClaudeStatus is the Claude plan's usage and the fallbacks to the network.
type ClaudeStatus struct {
	Usage        *provider.UsageLimits `json:"usage,omitempty"`
	HeldUntil    time.Time             `json:"held_until,omitzero"`
	Fallbacks    int                   `json:"fallbacks"`
	LastFallback time.Time             `json:"last_fallback,omitzero"`
	LastModel    string                `json:"last_model,omitempty"`
}

// CodexStatus is a ChatGPT account's usage (by a hash of its ID).
type CodexStatus struct {
	Account string               `json:"account"`
	Usage   provider.UsageLimits `json:"usage"`
	Credits bool                 `json:"credits"`
	Held    int                  `json:"held"`
}

// UnlockStatus is the Codex Desktop unlock's state.
type UnlockStatus struct {
	Listening bool   `json:"listening"`
	Port      int    `json:"port"`
	Trusted   bool   `json:"trusted"`
	Error     string `json:"error,omitempty"`
}

// Status returns the gateway's state.
func (g *Gateway) Status() Status {
	g.mu.Lock()
	s := Status{Settings: g.settings, Codex: []CodexStatus{}}
	if g.claude.haveUsage {
		u := g.claude.usage
		s.Claude.Usage = &u
	}
	if g.claude.heldUntil.After(now()) {
		s.Claude.HeldUntil = g.claude.heldUntil
	}
	s.Claude.Fallbacks, s.Claude.LastFallback, s.Claude.LastModel = g.claude.fallbacks, g.claude.lastFall, g.claude.lastModel
	for id, st := range g.codex {
		if !st.have {
			continue
		}
		sum := sha256.Sum256([]byte(id))
		s.Codex = append(s.Codex, CodexStatus{Account: hex.EncodeToString(sum[:4]), Usage: st.usage, Credits: st.credits, Held: st.held})
	}
	s.Unlock = UnlockStatus{Listening: len(g.unlockSrv) > 0, Port: UnlockPort(), Error: g.unlockErr}
	g.mu.Unlock()
	if s.Settings.CodexUnlock {
		s.Unlock.Trusted = UnlockCertTrusted(g.home)
	}
	return s
}

// Routes adds the management API's routes:
//
//	GET /api/v1/official   Status
//	PUT /api/v1/official   Settings
func (g *Gateway) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/official", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, g.Status())
	})
	mux.HandleFunc("PUT /api/v1/official", func(w http.ResponseWriter, r *http.Request) {
		var s Settings
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&s); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed JSON"})
			return
		}
		if s.CodexGuardPercent < 0 || s.CodexGuardPercent > 100 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "codex_guard_percent is 0 to 100"})
			return
		}
		if err := g.SetSettings(s); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, g.Status())
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
