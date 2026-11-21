package warmup

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"sharetoken/internal/provider"
)

// Routes adds the warm-up's API to mux:
//
//	GET  /api/v1/warmup                   Status
//	PUT  /api/v1/warmup                   {"paused": …, "claude": Local, "codex": Local}: what is given is set
//	PUT  /api/v1/warmup/providers/{name}  provider.WarmOptions; empty turns it off
//	POST /api/v1/warmup/run               {"target": "local:claude" | "codex" | "provider:<name>"}: warm up now
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/warmup", func(w http.ResponseWriter, r *http.Request) {
		st, err := s.Status(r.Context())
		answer(w, st, err)
	})
	mux.HandleFunc("PUT /api/v1/warmup", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Paused *bool  `json:"paused"`
			Claude *Local `json:"claude"`
			Codex  *Local `json:"codex"`
		}
		if err := readJSON(r, &req); err != nil {
			answer(w, nil, err)
			return
		}
		set, err := LoadSettings(s.home)
		if err == nil {
			if req.Paused != nil {
				set.Paused = *req.Paused
			}
			if req.Claude != nil {
				set.Claude = *req.Claude
			}
			if req.Codex != nil {
				set.Codex = *req.Codex
			}
			err = SaveSettings(s.home, set)
		}
		s.done(w, r, err)
	})
	mux.HandleFunc("PUT /api/v1/warmup/providers/{name}", func(w http.ResponseWriter, r *http.Request) {
		var req provider.WarmOptions
		if err := readJSON(r, &req); err != nil {
			answer(w, nil, err)
			return
		}
		_, err := provider.SetWarmup(s.home, r.PathValue("name"), &req)
		s.done(w, r, err)
	})
	mux.HandleFunc("POST /api/v1/warmup/run", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Target string `json:"target"`
		}
		if err := readJSON(r, &req); err != nil {
			answer(w, nil, err)
			return
		}
		t, err := s.RunNow(r.Context(), req.Target)
		if err != nil && !errors.Is(err, provider.ErrNotFound) && t.ID != "" {
			// sent and failed: the status tells why
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]string{"code": "warmup_failed", "message": err.Error()}, "target": t})
			return
		}
		answer(w, t, err)
	})
}

// done answers a change with the status, and looks again soon.
func (s *Service) done(w http.ResponseWriter, r *http.Request, err error) {
	if err != nil {
		answer(w, nil, err)
		return
	}
	s.Kick()
	st, err := s.Status(r.Context())
	answer(w, st, err)
}

func readJSON(r *http.Request, v any) error {
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(data)) == "" {
		return nil
	}
	if err := json.Unmarshal(data, v); err != nil {
		return errors.New("malformed JSON: " + err.Error())
	}
	return nil
}

func answer(w http.ResponseWriter, v any, err error) {
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, provider.ErrNotFound) {
			status = http.StatusNotFound
		}
		writeJSON(w, status, map[string]any{"error": map[string]string{"message": err.Error()}})
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
