package portal

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"sharetoken/internal/accounts"
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

// FeedbackPerIPHour caps the feedback accepted from one client address in an
// hour, across accounts: the per-user limit (accounts.FeedbackPerHour) alone
// lets someone with many accounts send that many times more. It is above the
// per-user limit so a few people behind one NAT still get through.
const FeedbackPerIPHour = 30

// takeFeedbackSlot reserves one of ip's hourly feedback slots and reports
// whether one was free. The caller gives it back with release when the
// feedback is not stored, so rejected input does not use up the address.
func (s *Server) takeFeedbackSlot(ip string) (release func(), ok bool) {
	s.feedbackMu.Lock()
	defer s.feedbackMu.Unlock()
	now := s.cfg.Now()
	cut := now.Add(-time.Hour)
	if len(s.feedbackIP) > 100_000 {
		clear(s.feedbackIP)
	}
	ts := s.feedbackIP[ip]
	for len(ts) > 0 && !ts[0].After(cut) {
		ts = ts[1:]
	}
	if len(ts) >= FeedbackPerIPHour {
		s.feedbackIP[ip] = ts
		return nil, false
	}
	s.feedbackIP[ip] = append(ts, now)
	return func() {
		s.feedbackMu.Lock()
		defer s.feedbackMu.Unlock()
		ts := s.feedbackIP[ip]
		for i := len(ts) - 1; i >= 0; i-- {
			if ts[i].Equal(now) {
				s.feedbackIP[ip] = append(ts[:i:i], ts[i+1:]...)
				break
			}
		}
		if len(s.feedbackIP[ip]) == 0 {
			delete(s.feedbackIP, ip)
		}
	}, true
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
	release, ok := s.takeFeedbackSlot(s.rateIP(r))
	if !ok {
		writeErr(w, http.StatusTooManyRequests, "throttled", "too much feedback from this address; try again later")
		return
	}
	f, err := s.store.AddFeedback(cu.user.ID, accounts.Feedback{Category: req.Category, Message: req.Message, Source: req.Source,
		AppVersion: req.AppVersion, OS: req.OS, Lang: req.Lang})
	if err != nil {
		release()
	}
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
	cu.note("status", req.Status)
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
