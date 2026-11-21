package portal

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"sharetoken/internal/accounts"
)

// Pairing: a seller makes a code, the buyers it hands the code to redeem it,
// and from then on those buyers buy only from the sellers they are paired
// with (package accounts, pairing.go; the coordinator enforces it when it
// matches). Both sides manage it here, with the client's token or a browser
// session.

// PairingView is what GET /api/v1/pairing returns: the caller's side of every
// pairing it is part of.
type PairingView struct {
	// Sellers the caller buys from. Not empty: the caller buys only from them.
	Sellers []accounts.PairedUser `json:"sellers"`
	// Buyers that buy from the caller, and the codes that admit more of them.
	Buyers []accounts.PairedUser  `json:"buyers"`
	Codes  []accounts.PairingCode `json:"codes"`
	// Restricted says the caller's purchases are limited to Sellers.
	Restricted bool `json:"restricted"`
	// Private says the caller, as a seller, serves only Buyers.
	Private bool `json:"private"`
	// MaxSellers is how many sellers one buyer may be paired with.
	MaxSellers int `json:"max_sellers"`
	// Requests wait for the caller to approve them, as a seller; Waiting are
	// the caller's own, waiting for a seller.
	Requests []accounts.InviteRequest `json:"requests"`
	Waiting  []accounts.InviteRequest `json:"waiting"`
}

func (s *Server) pairingRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/pairing", s.auth(s.handlePairing))
	mux.Handle("POST /api/v1/pairing/codes", s.auth(s.handleNewPairingCode))
	mux.Handle("DELETE /api/v1/pairing/codes/{id}", s.auth(s.handleRevokePairingCode))
	mux.Handle("POST /api/v1/pairing/redeem", s.auth(s.handleRedeemPairingCode))
	mux.Handle("DELETE /api/v1/pairing/sellers/{id}", s.auth(s.handleUnpair(false)))
	mux.Handle("DELETE /api/v1/pairing/buyers/{id}", s.auth(s.handleUnpair(true)))
	mux.Handle("PUT /api/v1/pairing/private", s.auth(s.handlePrivateSeller))
	mux.Handle("GET /api/v1/admin/pairings", s.admin(s.handleAdminPairings))
}

// maskName hides most of an account name from the other side of a pairing. A
// code can travel further than its seller meant it to, and account names are
// usually email addresses: enough stays to recognise someone you know.
func maskName(name string) string {
	local, domain, isMail := strings.Cut(name, "@")
	r := []rune(local)
	keep := min(2, len(r))
	if len(r) <= 2 {
		keep = min(1, len(r))
	}
	masked := string(r[:keep]) + "***"
	if isMail {
		return masked + "@" + domain
	}
	return masked
}

func maskPaired(ps []accounts.PairedUser) []accounts.PairedUser {
	for i := range ps {
		ps[i].Username = maskName(ps[i].Username)
	}
	return ps
}

func formatCodes(cs []accounts.PairingCode) []accounts.PairingCode {
	for i := range cs {
		cs[i].Code = accounts.FormatPairingCode(cs[i].Code)
	}
	return cs
}

func (s *Server) pairingView(userID int64) (*PairingView, error) {
	sellers, buyers, err := s.store.Pairings(userID)
	if err != nil {
		return nil, err
	}
	codes, err := s.store.PairingCodes(userID)
	if err != nil {
		return nil, err
	}
	private, err := s.store.PrivateSeller(userID)
	if err != nil {
		return nil, err
	}
	requests, err := s.store.InviteRequests(userID, accounts.KindPair)
	if err != nil {
		return nil, err
	}
	waiting, err := s.store.WaitingRequests(userID, accounts.KindPair)
	if err != nil {
		return nil, err
	}
	return &PairingView{Sellers: maskPaired(sellers), Buyers: maskPaired(buyers), Codes: formatCodes(codes), Restricted: len(sellers) > 0,
		Private: private, MaxSellers: accounts.MaxPairedSellers, Requests: maskRequests(requests), Waiting: maskRequests(waiting)}, nil
}

func (s *Server) handlePairing(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	v, err := s.pairingView(cu.user.ID)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) pairingErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, accounts.ErrOwnCode):
		writeErr(w, http.StatusConflict, "own_code", "this is your own pairing code; give it to the buyers you want to pair with")
	case errors.Is(err, accounts.ErrLimit):
		writeErr(w, http.StatusConflict, "limit", err.Error())
	default:
		s.accountErr(w, err)
	}
}

func (s *Server) handleNewPairingCode(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	var req struct {
		Label    string  `json:"label"`
		TTLHours float64 `json:"ttl_hours"` // 0: the default, one day
		MaxUses  int64   `json:"max_uses"`  // 0: any number of buyers
		Approval bool    `json:"approval"`  // each pairing waits for the caller's approval
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.TTLHours < 0 || req.TTLHours > accounts.MaxPairingTTL.Hours() {
		writeErr(w, http.StatusBadRequest, "bad_request", "ttl_hours is out of range")
		return
	}
	c, err := s.store.CreatePairingCodeWith(cu.user.ID, accounts.PairingCodeOptions{Label: req.Label, TTL: time.Duration(req.TTLHours * float64(time.Hour)), MaxUses: req.MaxUses, Approval: req.Approval})
	if err != nil {
		s.pairingErr(w, err)
		return
	}
	c.Code = accounts.FormatPairingCode(c.Code)
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) handleRevokePairingCode(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "bad code id")
		return
	}
	if err := s.store.RevokePairingCode(cu.user.ID, id); err != nil {
		s.pairingErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleRedeemPairingCode(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	var req struct {
		Code string `json:"code"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	// The code may also be a link to it, or its bare link token. Wrong codes
	// count like failed logins, so nobody searches for a live one (invites.go).
	ref := accounts.ParseInvite(req.Code)
	if ref.Kind == accounts.KindShare {
		writeErr(w, http.StatusBadRequest, "wrong_kind", "this is a link to a share invite, not to a pairing code")
		return
	}
	s.serveInvite(w, r, cu, []string{accounts.KindPair}, ref, true)
}

// handleUnpair ends one pairing: asSeller removes one of the caller's buyers,
// otherwise the caller leaves one of its sellers.
func (s *Server) handleUnpair(asSeller bool) authedHandler {
	return func(w http.ResponseWriter, r *http.Request, cu ctxUser) {
		other, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", "bad user id")
			return
		}
		buyer, seller := cu.user.ID, other
		if asSeller {
			buyer, seller = other, cu.user.ID
		}
		if err := s.store.Unpair(buyer, seller); err != nil {
			s.pairingErr(w, err)
			return
		}
		s.log.Info("unpaired", "buyer", buyer, "seller", seller, "by", cu.user.ID)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

// handlePrivateSeller turns the caller's private mode on or off: a private
// seller serves only the buyers paired with it.
func (s *Server) handlePrivateSeller(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if err := s.store.SetPrivateSeller(cu.user.ID, req.Enabled); err != nil {
		s.internal(w, err)
		return
	}
	s.log.Info("private seller", "user", cu.user.ID, "enabled", req.Enabled)
	writeJSON(w, http.StatusOK, map[string]bool{"private": req.Enabled})
}

// handleAdminPairings lists every pairing, with both account names in full.
// ?user= matches part of either name.
func (s *Server) handleAdminPairings(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	p, ok := parseList(w, r)
	if !ok {
		return
	}
	f := accounts.PairingQuery{User: r.URL.Query().Get("user"), From: p.From, To: p.To}
	serveList(s, w, r, cu, p, listing[accounts.Pairing]{
		Key: "pairings", File: "pairings",
		Columns: []csvColumn[accounts.Pairing]{
			{"paired", func(p accounts.Pairing) string { return csvTime(p.Since) }, true},
			{"buyer", func(p accounts.Pairing) string { return p.Buyer }, false},
			{"seller", func(p accounts.Pairing) string { return p.Seller }, false},
			{"seller_private", func(p accounts.Pairing) string { return strconv.FormatBool(p.SellerPrivate) }, true},
			{"label", func(p accounts.Pairing) string { return p.Label }, false},
		},
		Count: func() (int64, error) { return s.store.CountPairings(f) },
		Fetch: func(offset, limit int) ([]accounts.Pairing, error) {
			q := f
			q.Offset, q.Limit = offset, limit
			return s.store.ListPairings(q)
		},
	})
}
