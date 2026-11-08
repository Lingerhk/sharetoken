package portal

import (
	"errors"
	"net/http"
	"strconv"

	"sharetoken/accounts"
)

// FeedbackRequest is what a user sends. Source, version and OS say where it
// came from; they are the sender's own claim, shown to admins as a hint.
type FeedbackRequest struct {
	Category   string `json:"category"`
	Message    string `json:"message"`
	Source     string `json:"source"`
	AppVersion string `json:"app_version,omitempty"`
	OS         string `json:"os,omitempty"`
	Lang       string `json:"lang,omitempty"`
}

func (s *Server) feedbackRoutes(mux *http.ServeMux) {
	mux.Handle("POST /api/v1/feedback", s.auth(s.handleSendFeedback))
	mux.Handle("GET /api/v1/feedback", s.auth(s.handleMyFeedback))
	mux.Handle("GET /api/v1/admin/feedback", s.admin(s.handleAdminFeedback))
	mux.Handle("POST /api/v1/admin/feedback/{id}/status", s.admin(s.handleFeedbackStatus))
}

func (s *Server) handleSendFeedback(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	var req FeedbackRequest
	if !readJSON(w, r, &req) {
		return
	}
	f, err := s.store.AddFeedback(cu.user.ID, accounts.Feedback{Category: req.Category, Message: req.Message, Source: req.Source,
		AppVersion: req.AppVersion, OS: req.OS, Lang: req.Lang})
	switch {
	case errors.Is(err, accounts.ErrInvalid):
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
	case errors.Is(err, accounts.ErrTooMuchFeedback):
		writeErr(w, http.StatusTooManyRequests, "throttled", err.Error())
	case err != nil:
		s.internal(w, err)
	default:
		s.log.Info("feedback received", "id", f.ID, "user", cu.user.Username, "source", f.Source)
		writeJSON(w, http.StatusCreated, map[string]int64{"id": f.ID})
	}
}

func (s *Server) handleMyFeedback(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	fs, err := s.store.UserFeedback(cu.user.ID, queryInt(r, "before", 0), queryLimit(r))
	if err != nil {
		s.internal(w, err)
		return
	}
	// Who handled it is internal.
	for i := range fs {
		fs[i].HandledBy = ""
	}
	writeJSON(w, http.StatusOK, map[string]any{"feedback": fs})
}

func (s *Server) handleAdminFeedback(w http.ResponseWriter, r *http.Request, _ ctxUser) {
	fs, err := s.store.AllFeedback(r.URL.Query().Get("status"), queryInt(r, "before", 0), queryLimit(r))
	if err != nil {
		s.internal(w, err)
		return
	}
	n, err := s.store.NewFeedbackCount()
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"feedback": fs, "new": n})
}

func (s *Server) handleFeedbackStatus(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "bad feedback id")
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	err = s.store.SetFeedbackStatus(id, req.Status, cu.user.Username)
	switch {
	case errors.Is(err, accounts.ErrInvalid):
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
	case errors.Is(err, accounts.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not_found", "no such feedback")
	case err != nil:
		s.internal(w, err)
	default:
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}
