package client

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"sharetoken/node"
	"sharetoken/provider"
)

// The providers API: what `sharetoken provider` does, for a GUI.
//
//	GET    /api/v1/providers                  kinds that can be added, and the configured providers
//	POST   /api/v1/providers                  add one with an API key (provider.AddRequest)
//	PATCH  /api/v1/providers/{name}           {"enabled": bool}
//	DELETE /api/v1/providers/{name}
//	GET    /api/v1/providers/{name}/models    models the credentials may use
//	PUT    /api/v1/providers/{name}/models    add or reprice a model to sell (node.ModelConfig)
//	DELETE /api/v1/providers/{name}/models/{id}
//	POST   /api/v1/providers/{name}/test      {"model": …, "prompt": …}: one request through it
//	POST   /api/v1/providers/oauth/start      {"type": …, "project": …} → where to sign in
//	POST   /api/v1/providers/oauth/finish     {"id": …, "code": …, "name": …}
//
// Answers never hold a key or a token. Changes apply to the running node at once.
func (a *Agent) providerRoutes(mux *http.ServeMux) {
	handle := func(pattern string, h func(w http.ResponseWriter, r *http.Request) (any, error)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			// Keys go in here: no web page may call this, not even a local one.
			if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host {
				writeErrorCode(w, http.StatusForbidden, "bad_origin", "the providers API is only for the sharetoken app")
				return
			}
			out, err := h(w, r)
			if err != nil {
				status := http.StatusBadRequest
				switch {
				case errors.Is(err, provider.ErrNotFound):
					status = http.StatusNotFound
				case errors.Is(err, provider.ErrKeyRefused):
					status = http.StatusUnauthorized
				case errors.Is(err, provider.ErrModelTaken):
					status = http.StatusConflict
				}
				var se *node.StatusError
				if errors.As(err, &se) {
					status = http.StatusBadGateway
				}
				writeError(w, status, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, out)
		})
	}
	read := func(r *http.Request, v any) error {
		data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			return err
		}
		if len(strings.TrimSpace(string(data))) == 0 {
			return nil
		}
		if err := json.Unmarshal(data, v); err != nil {
			return errors.New("malformed JSON: " + err.Error())
		}
		return nil
	}
	// changed applies a change to the running node; that it could not is told
	// by a warning in the status, not by failing the change, which is saved.
	changed := func() { _ = a.ReloadShare() }

	handle("GET /api/v1/providers", func(w http.ResponseWriter, r *http.Request) (any, error) {
		views, err := provider.Views(a.home)
		return map[string]any{"kinds": provider.KindViews(), "providers": views}, err
	})
	handle("POST /api/v1/providers", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var req provider.AddRequest
		if err := read(r, &req); err != nil {
			return nil, err
		}
		out, err := provider.AddWithKey(r.Context(), a.home, req)
		if err == nil {
			changed()
		}
		return out, err
	})
	handle("PATCH /api/v1/providers/{name}", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var req struct {
			Enabled *bool `json:"enabled"`
		}
		if err := read(r, &req); err != nil {
			return nil, err
		}
		if req.Enabled == nil {
			return nil, errors.New("nothing to change: give \"enabled\"")
		}
		v, err := provider.SetEnabled(a.home, r.PathValue("name"), *req.Enabled)
		if err == nil {
			changed()
		}
		return v, err
	})
	handle("DELETE /api/v1/providers/{name}", func(w http.ResponseWriter, r *http.Request) (any, error) {
		err := provider.Remove(a.home, r.PathValue("name"))
		if err == nil {
			changed()
		}
		return map[string]bool{"ok": err == nil}, err
	})
	handle("GET /api/v1/providers/{name}/models", func(w http.ResponseWriter, r *http.Request) (any, error) {
		ms, err := provider.Models(r.Context(), a.home, r.PathValue("name"))
		return map[string]any{"models": ms}, err
	})
	handle("PUT /api/v1/providers/{name}/models", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var m node.ModelConfig
		if err := read(r, &m); err != nil {
			return nil, err
		}
		if m.ID == "" {
			m.ID = m.UpstreamModel
		}
		a.mu.Lock()
		var local []string
		for _, lm := range a.cfg.Share.Models {
			local = append(local, lm.ID)
		}
		a.mu.Unlock()
		v, err := provider.SetModel(a.home, r.PathValue("name"), m, local)
		if err == nil {
			changed()
		}
		return v, err
	})
	handle("DELETE /api/v1/providers/{name}/models/{id}", func(w http.ResponseWriter, r *http.Request) (any, error) {
		from, err := provider.RemoveModel(a.home, r.PathValue("id"))
		if err == nil && from == "" {
			err = provider.ErrNotFound
		}
		if err == nil {
			changed()
		}
		return map[string]bool{"ok": err == nil}, err
	})
	handle("POST /api/v1/providers/{name}/test", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var req struct {
			Model  string `json:"model"`
			Prompt string `json:"prompt"`
		}
		if err := read(r, &req); err != nil {
			return nil, err
		}
		res, model, err := provider.Test(r.Context(), a.home, r.PathValue("name"), req.Model, req.Prompt)
		return map[string]any{"model": model, "result": res}, err
	})
	handle("POST /api/v1/providers/oauth/start", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var req struct {
			Type    string `json:"type"`
			Project string `json:"project"`
		}
		if err := read(r, &req); err != nil {
			return nil, err
		}
		return provider.StartLogin(req.Type, req.Project)
	})
	handle("POST /api/v1/providers/oauth/finish", func(w http.ResponseWriter, r *http.Request) (any, error) {
		var req struct {
			ID   string `json:"id"`
			Code string `json:"code"`
			Name string `json:"name"`
		}
		if err := read(r, &req); err != nil {
			return nil, err
		}
		out, err := provider.FinishLoginAndSave(r.Context(), a.home, req.ID, req.Code, req.Name)
		if err == nil {
			changed()
		}
		return out, err
	})
}
