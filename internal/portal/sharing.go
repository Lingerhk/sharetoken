package portal

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"sharetoken/internal/accounts"
	"sharetoken/internal/coordinator"
	"sharetoken/internal/ledger"
)

// Share partners (package accounts, sharing.go): one user makes an invite,
// another joins with it, and from then on the two use each other's nodes for
// free. Each limits what it gives in its settings; the coordinator applies
// them when it matches.

// SharingView is what GET /api/v1/sharing returns.
type SharingView struct {
	Partners []PartnerView          `json:"partners"`
	Invites  []accounts.ShareInvite `json:"invites"`
	Settings accounts.ShareSettings `json:"settings"`
	// Served is the tokens the caller's nodes served all its partners in the
	// last WindowHours: what DailyLimitTokens counts.
	Served      int64 `json:"served"`
	WindowHours int   `json:"window_hours"`
	MaxPartners int   `json:"max_partners"`
}

// PartnerView is one partner and the tokens that moved between the two in the
// last WindowHours.
type PartnerView struct {
	accounts.SharePartner
	coordinator.ShareFlow
}

func (s *Server) sharingRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/sharing", s.auth(s.handleSharing))
	mux.Handle("POST /api/v1/sharing/invites", s.auth(s.handleNewShareInvite))
	mux.Handle("DELETE /api/v1/sharing/invites/{id}", s.auth(s.handleRevokeShareInvite))
	mux.Handle("POST /api/v1/sharing/join", s.auth(s.handleJoinShare))
	mux.Handle("DELETE /api/v1/sharing/partners/{id}", s.auth(s.handleLeaveSharePartner))
	mux.Handle("PUT /api/v1/sharing/settings", s.auth(s.handleShareSettings))
	mux.Handle("GET /api/v1/admin/share-groups", s.admin(s.handleAdminShareGroups))
}

func (s *Server) sharingView(userID int64) (*SharingView, error) {
	partners, err := s.store.SharePartners(userID)
	if err != nil {
		return nil, err
	}
	invites, err := s.store.ShareInvites(userID)
	if err != nil {
		return nil, err
	}
	settings, err := s.store.ShareSettings(userID)
	if err != nil {
		return nil, err
	}
	acct := ledger.UserAccount(userID)
	since := time.Now().Add(-accounts.ShareWindow).UnixNano()
	flows, err := coordinator.SharedFlows(s.db, acct, since)
	if err != nil {
		return nil, err
	}
	v := &SharingView{Partners: make([]PartnerView, 0, len(partners)), Invites: invites, Settings: settings,
		WindowHours: int(accounts.ShareWindow / time.Hour), MaxPartners: accounts.MaxSharePartners}
	for _, f := range flows {
		v.Served += f.Given
	}
	for _, p := range partners {
		f := flows[ledger.UserAccount(p.ID)]
		p.Username = maskName(p.Username)
		v.Partners = append(v.Partners, PartnerView{SharePartner: p, ShareFlow: f})
	}
	for i := range v.Invites {
		v.Invites[i].Code = accounts.FormatPairingCode(v.Invites[i].Code)
	}
	return v, nil
}

func (s *Server) handleSharing(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	v, err := s.sharingView(cu.user.ID)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) sharingErr(w http.ResponseWriter, err error) {
	if errors.Is(err, accounts.ErrOwnCode) {
		writeErr(w, http.StatusConflict, "own_code", "this is your own invite; give it to the person you want to share with")
		return
	}
	s.pairingErr(w, err)
}

func (s *Server) handleNewShareInvite(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	var req struct {
		TTLHours float64 `json:"ttl_hours"` // 0: the default, one day
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.TTLHours < 0 || req.TTLHours > accounts.MaxShareTTL.Hours() {
		writeErr(w, http.StatusBadRequest, "bad_request", "ttl_hours is out of range")
		return
	}
	inv, err := s.store.CreateShareInvite(cu.user.ID, time.Duration(req.TTLHours*float64(time.Hour)))
	if err != nil {
		s.sharingErr(w, err)
		return
	}
	inv.Code = accounts.FormatPairingCode(inv.Code)
	writeJSON(w, http.StatusCreated, inv)
}

func (s *Server) handleRevokeShareInvite(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "bad invite id")
		return
	}
	if err := s.store.RevokeShareInvite(cu.user.ID, id); err != nil {
		s.sharingErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleJoinShare(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	var req struct {
		Code string `json:"code"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	// Wrong codes count like failed logins, so nobody searches for a live one.
	keys := []string{"share:u:" + strconv.FormatInt(cu.user.ID, 10), "share:ip:" + s.clientIP(r)}
	if s.throttled(keys[0]) || s.throttled(keys[1]) {
		writeErr(w, http.StatusTooManyRequests, "throttled", "too many wrong invite codes; try again in a few minutes")
		return
	}
	p, err := s.store.JoinShare(cu.user.ID, req.Code)
	if err != nil {
		if errors.Is(err, accounts.ErrNotFound) {
			s.recordFailure(keys...)
			writeErr(w, http.StatusNotFound, "bad_code", "this invite code is wrong, expired, revoked or already used")
			return
		}
		s.sharingErr(w, err)
		return
	}
	p.Username = maskName(p.Username)
	s.log.Info("share partners", "user", cu.user.ID, "partner", p.ID, "group", p.GroupID)
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleLeaveSharePartner(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	other, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "bad user id")
		return
	}
	if err := s.store.LeaveSharePartner(cu.user.ID, other); err != nil {
		s.sharingErr(w, err)
		return
	}
	s.log.Info("share partners parted", "user", cu.user.ID, "partner", other)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleShareSettings(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	var req accounts.ShareSettings
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.store.SetShareSettings(cu.user.ID, req); err != nil {
		s.sharingErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, req)
}

// handleAdminShareGroups lists every share group with its members' names in
// full. ?user= matches part of a member's name.
func (s *Server) handleAdminShareGroups(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	p, ok := parseList(w, r)
	if !ok {
		return
	}
	f := accounts.ShareGroupQuery{User: r.URL.Query().Get("user"), From: p.From, To: p.To}
	serveList(s, w, r, cu, p, listing[accounts.ShareGroup]{
		Key: "groups", File: "share-groups",
		Columns: []csvColumn[accounts.ShareGroup]{
			{"created", func(g accounts.ShareGroup) string { return csvTime(g.CreatedAt) }, true},
			{"members", func(g accounts.ShareGroup) string { return strings.Join(g.Members, " ") }, false},
		},
		Count: func() (int64, error) { return s.store.CountShareGroups(f) },
		Fetch: func(offset, limit int) ([]accounts.ShareGroup, error) {
			q := f
			q.Offset, q.Limit = offset, limit
			return s.store.ListShareGroups(q)
		},
	})
}
