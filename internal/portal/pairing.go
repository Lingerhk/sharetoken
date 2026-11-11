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
}

func (s *Server) pairingRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/pairing", s.auth(s.handlePairing))
	mux.Handle("POST /api/v1/pairing/codes", s.auth(s.handleNewPairingCode))
	mux.Handle("DELETE /api/v1/pairing/codes/{id}", s.auth(s.handleRevokePairingCode))
	mux.Handle("POST /api/v1/pairing/redeem", s.auth(s.handleRedeemPairingCode))
	mux.Handle("DELETE /api/v1/pairing/sellers/{id}", s.auth(s.handleUnpair(false)))
	mux.Handle("DELETE /api/v1/pairing/buyers/{id}", s.auth(s.handleUnpair(true)))
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
	return &PairingView{Sellers: maskPaired(sellers), Buyers: maskPaired(buyers), Codes: formatCodes(codes), Restricted: len(sellers) > 0}, nil
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
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.TTLHours < 0 || req.TTLHours > accounts.MaxPairingTTL.Hours() {
		writeErr(w, http.StatusBadRequest, "bad_request", "ttl_hours is out of range")
		return
	}
	c, err := s.store.CreatePairingCode(cu.user.ID, req.Label, time.Duration(req.TTLHours*float64(time.Hour)), req.MaxUses)
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
	// Wrong codes count like failed logins, so nobody searches for a live one.
	keys := []string{"pair:u:" + strconv.FormatInt(cu.user.ID, 10), "pair:ip:" + s.clientIP(r)}
	if s.throttled(keys[0]) || s.throttled(keys[1]) {
		writeErr(w, http.StatusTooManyRequests, "throttled", "too many wrong pairing codes; try again in a few minutes")
		return
	}
	seller, err := s.store.RedeemPairingCode(cu.user.ID, req.Code)
	if err != nil {
		if errors.Is(err, accounts.ErrNotFound) {
			s.recordFailure(keys...)
			writeErr(w, http.StatusNotFound, "bad_code", "this pairing code is wrong, expired, revoked or used up")
			return
		}
		s.pairingErr(w, err)
		return
	}
	seller.Username = maskName(seller.Username)
	s.log.Info("paired", "buyer", cu.user.ID, "seller", seller.ID)
	writeJSON(w, http.StatusOK, seller)
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
