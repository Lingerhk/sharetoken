package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"sharetoken/internal/connect"
	"sharetoken/internal/official"
)

// ConnectRequest connects a coding tool to the local endpoint, or takes it
// off (internal/connect).
type ConnectRequest struct {
	Tool string `json:"tool"` // "claude" or "codex"
	Off  bool   `json:"off,omitempty"`
	// Subscription keeps the tool's own vendor login; nil: when the tool is
	// signed in (DetectLogins).
	Subscription   *bool `json:"subscription,omitempty"`
	OpenAIProvider bool  `json:"openai_provider,omitempty"`
	Force          bool  `json:"force,omitempty"`
}

// ConnectView is what the app shows of the tools' connections.
type ConnectView struct {
	State          connect.State `json:"state"`
	ClaudePath     string        `json:"claude_path"`
	CodexPath      string        `json:"codex_path"`
	ClaudeSignedIn bool          `json:"claude_signed_in"`
	CodexChatGPT   bool          `json:"codex_chatgpt"`
}

// DetectLogins reports whether Claude Code is signed in to claude.ai and
// Codex with ChatGPT, from their own files (no credential is read).
func DetectLogins() (claude, codex bool) {
	h, _ := os.UserHomeDir()
	claudeJSON := filepath.Join(h, ".claude.json")
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		claudeJSON = filepath.Join(d, ".claude.json")
	}
	if data, err := os.ReadFile(claudeJSON); err == nil {
		var c struct {
			OAuth json.RawMessage `json:"oauthAccount"`
		}
		claude = json.Unmarshal(data, &c) == nil && len(c.OAuth) > 0 && string(c.OAuth) != "null"
	}
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(h, ".codex")
	}
	if data, err := os.ReadFile(filepath.Join(codexHome, "auth.json")); err == nil {
		var a struct {
			Tokens *struct {
				Account string `json:"account_id"`
			} `json:"tokens"`
		}
		codex = json.Unmarshal(data, &a) == nil && a.Tokens != nil
	}
	return claude, codex
}

// ConnectStatus is the tools' connections.
func ConnectStatus(home string) (ConnectView, error) {
	st, err := connect.LoadState(home)
	c, x := DetectLogins()
	return ConnectView{State: st, ClaudePath: connect.ClaudeSettingsPath(), CodexPath: connect.CodexConfigPath(), ClaudeSignedIn: c, CodexChatGPT: x}, err
}

// ApplyConnect connects a tool to the endpoint at baseURL (or takes it off)
// with the network's models (nil: those listed before).
func ApplyConnect(home, baseURL string, models []string, req ConnectRequest) error {
	key, err := LoadAPIKey(home)
	if err != nil {
		return err
	}
	settings, _ := official.LoadSettings(home)
	claudeIn, codexIn := DetectLogins()
	switch req.Tool {
	case "claude":
		if req.Off {
			return connect.ClaudeOff(home)
		}
		sub := claudeIn
		if req.Subscription != nil {
			sub = *req.Subscription
		}
		if models == nil {
			if st, _ := connect.LoadState(home); st.Claude != nil {
				models = listedModels(st.Claude)
			}
		}
		_, err = connect.Claude(home, connect.Options{BaseURL: baseURL, Key: key.Key, Models: models, Subscription: sub, Force: req.Force})
		return err
	case "codex":
		if req.Off {
			return connect.CodexOff(home)
		}
		sub := codexIn
		if req.Subscription != nil {
			sub = *req.Subscription
		}
		_, err = connect.Codex(home, connect.Options{BaseURL: baseURL, Key: key.Key, Subscription: sub,
			OpenAIProvider: req.OpenAIProvider, Unlock: settings.CodexUnlock && sub, Force: req.Force})
		return err
	}
	return errors.New("tool is claude or codex")
}

func listedModels(a *connect.Applied) []string {
	if a.Lines["models"] == "" {
		return nil
	}
	var out []string
	for _, m := range splitLines(a.Lines["models"]) {
		if m != "" {
			out = append(out, m)
		}
	}
	return out
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// SetCodexUnlock turns the Codex Desktop unlock on or off: the localhost
// certificate (made and trusted when on: the system asks the user), the
// setting (gw's when the client runs, else the file) and Codex's
// chatgpt_base_url. Codex has to be connected with its ChatGPT login.
func SetCodexUnlock(home, baseURL string, on bool, gw *official.Gateway) error {
	if on && runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		return errors.New("the Codex Desktop unlock is for macOS and Windows")
	}
	if on {
		if _, made, err := official.EnsureUnlockCert(home); err != nil {
			return err
		} else if made || !official.UnlockCertTrusted(home) {
			if err := official.TrustUnlockCert(home); err != nil {
				return errors.New("the certificate was not trusted: " + err.Error())
			}
		}
	}
	s, err := official.LoadSettings(home)
	if gw != nil {
		s = gw.Settings()
	} else if err != nil {
		return err
	}
	s.CodexUnlock = on
	if gw != nil {
		err = gw.SetSettings(s)
	} else {
		err = official.SaveSettings(home, s)
	}
	if err != nil {
		return err
	}
	st, _ := connect.LoadState(home)
	sub := true
	req := ConnectRequest{Tool: "codex", Subscription: &sub, Force: true}
	if st.Codex != nil {
		req.OpenAIProvider = st.Codex.Options.OpenAIProvider
	} else if !on {
		req.Off = true
	}
	if !req.Off {
		if err := ApplyConnect(home, baseURL, nil, req); err != nil {
			return err
		}
	}
	if !on {
		return official.UntrustUnlockCert(home)
	}
	return nil
}

// connectRoutes adds the management API's routes for the tools:
//
//	GET  /api/v1/connect          ConnectView
//	POST /api/v1/connect          ConnectRequest
//	POST /api/v1/official/unlock  {"on": bool}
func (d *Daemon) connectRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/connect", func(w http.ResponseWriter, r *http.Request) {
		v, err := ConnectStatus(d.home)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, v)
	})
	mux.HandleFunc("POST /api/v1/connect", jsonBody(func(w http.ResponseWriter, r *http.Request, req ConnectRequest) {
		if err := ApplyConnect(d.home, "http://"+d.addr, d.networkModels(r.Context()), req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		v, _ := ConnectStatus(d.home)
		writeJSON(w, http.StatusOK, v)
	}))
	mux.HandleFunc("GET /api/v1/autostart", func(w http.ResponseWriter, r *http.Request) {
		on, where := AutostartStatus()
		writeJSON(w, http.StatusOK, map[string]any{"on": on, "where": where})
	})
	mux.HandleFunc("PUT /api/v1/autostart", jsonBody(func(w http.ResponseWriter, r *http.Request, req struct {
		On bool `json:"on"`
	}) {
		if err := SetAutostart(d.home, req.On); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		on, where := AutostartStatus()
		writeJSON(w, http.StatusOK, map[string]any{"on": on, "where": where})
	}))
	mux.HandleFunc("POST /api/v1/official/unlock", jsonBody(func(w http.ResponseWriter, r *http.Request, req struct {
		On bool `json:"on"`
	}) {
		if err := SetCodexUnlock(d.home, "http://"+d.addr, req.On, d.official); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, d.official.Status())
	}))
}

// networkModels is the network's models while connected, else nil.
func (d *Daemon) networkModels(ctx context.Context) []string {
	d.mu.Lock()
	a := d.agent
	d.mu.Unlock()
	if a == nil || a.buyer == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ms, err := a.buyer.Models(ctx)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Model)
	}
	return out
}

// connectLoop keeps the connected tools' files in step with the endpoint:
// its address, the API key and the network's models.
func (d *Daemon) connectLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		if st, err := connect.LoadState(d.home); err == nil && (st.Claude != nil || st.Codex != nil) {
			if key, err := LoadAPIKey(d.home); err == nil {
				if err := connect.Refresh(d.home, "http://"+d.addr, key.Key, d.networkModels(ctx)); err != nil {
					d.log.Warn("could not update the connected tools' settings", "err", err)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
