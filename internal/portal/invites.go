package portal

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"sharetoken/internal/accounts"
	"sharetoken/internal/mailer"
)

// Invite links (package accounts, invites.go): every pairing code and share
// invite also has a link, https://<portal>/p#<token> or /s#<token>. The page
// at that address (web/invite.html) shows who invites, through a preview that
// needs no account; the visitor signs in, or signs up, and accepts. The
// desktop app opens the same links as sharetoken://p/<token>.
//
// A code or invite may ask its owner to approve each use: using it then files
// a request, which the owner approves or rejects here.
//
//	GET    /api/v1/invite/{kind}/{token}           public: who invites (masked), label, expiry
//	POST   /api/v1/invite/accept                   {link} (or {kind, code}) → {kind, user, pending}
//	POST   /api/v1/pairing/requests/{id}/approve   seller: approve a buyer's request
//	DELETE /api/v1/pairing/requests/{id}           seller: reject it
//	DELETE /api/v1/pairing/waiting/{id}            buyer: withdraw one's own request
//	(and the same three under /api/v1/sharing/)

// Guessing codes. A code is 50 random bits and a link token 71, live codes are
// few, and every wrong one counts: per account and per address in a short
// window (maxFails in failWindow, as failed logins), per account over a day,
// and over everyone. When wrong codes pour in from everywhere at once, each
// account and address gets less, and the administrators hear of it.
const (
	dailyMaxFails      = 30             // wrong codes one account may enter in 24 hours
	newAccountAge      = time.Hour      // an account younger than this gets newAccountMaxFails
	newAccountMaxFails = 3              // wrong codes a new account may enter in failWindow
	surgeFails         = 300            // wrong codes from everyone in failWindow that count as an attack
	surgeMaxFails      = 3              // wrong codes each account and address may enter during one
	surgeAlertEvery    = time.Hour      // how often the administrators are told of one
	failDay            = 24 * time.Hour // the window of dailyMaxFails
	allFailsKey        = "inv:all"      // the key of everyone's wrong codes
)

// AcceptedInvite is what accepting an invite returns: its kind and the other
// side, masked. Pending: the owner approves each use, and has not yet.
type AcceptedInvite struct {
	Kind    string `json:"kind"`
	ID      int64  `json:"id"`
	User    string `json:"username"`
	Label   string `json:"label,omitempty"`
	Since   int64  `json:"since"`
	Pending bool   `json:"pending,omitempty"`
}

func (s *Server) inviteRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/invite/{kind}/{token}", s.public(s.handlePreviewInvite))
	mux.Handle("POST /api/v1/invite/accept", s.auth(s.handleAcceptInvite))
	for _, area := range []string{"pairing", "sharing"} {
		mux.Handle("POST /api/v1/"+area+"/requests/{id}/approve", s.auth(s.handleApproveRequest))
		mux.Handle("DELETE /api/v1/"+area+"/requests/{id}", s.auth(s.handleRejectRequest))
		mux.Handle("DELETE /api/v1/"+area+"/waiting/{id}", s.auth(s.handleWithdrawRequest))
	}
}

// failCount counts key's failures in the last window, dropping older ones.
func (s *Server) failCount(key string, window time.Duration) int {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	cut := s.cfg.Now().Add(-window)
	fs := s.fails[key]
	for len(fs) > 0 && fs[0].Before(cut) {
		fs = fs[1:]
	}
	if len(fs) == 0 {
		delete(s.fails, key)
	} else {
		s.fails[key] = fs
	}
	return len(fs)
}

// codeGuard decides whether cu may try one more code or link of scope ("pair"
// or "share") from r, and returns the keys a wrong one is counted under.
func (s *Server) codeGuard(r *http.Request, cu ctxUser, scope string) (keys []string, ok bool) {
	uid := strconv.FormatInt(cu.user.ID, 10)
	userKey, ipKey, dayKey := scope+":u:"+uid, scope+":ip:"+s.clientIP(r), "inv:day:u:"+uid
	keys = []string{userKey, ipKey, dayKey, allFailsKey}
	perUser, perIP := maxFails, maxFails
	if s.cfg.Now().Sub(time.Unix(0, cu.user.CreatedAt)) < newAccountAge {
		perUser = newAccountMaxFails
	}
	if s.failCount(allFailsKey, failWindow) >= surgeFails {
		perUser, perIP = min(perUser, surgeMaxFails), surgeMaxFails
		s.surgeAlert()
	}
	ok = s.failCount(userKey, failWindow) < perUser && s.failCount(ipKey, failWindow) < perIP && s.failCount(dayKey, failDay) < dailyMaxFails
	return keys, ok
}

// surgeAlert logs a wave of wrong codes and tells the administrators by
// Pushover (pushover.go), at most once per surgeAlertEvery.
func (s *Server) surgeAlert() {
	now := s.cfg.Now()
	s.failMu.Lock()
	due := now.Sub(s.surgeAlerted) >= surgeAlertEvery
	if due {
		s.surgeAlerted = now
	}
	s.failMu.Unlock()
	if !due {
		return
	}
	s.log.Warn("pairing and invite codes are being guessed: limits tightened", "wrong_codes", s.failCount(allFailsKey, failWindow), "window", failWindow.String())
	s.push("ShareToken: invite codes are being guessed",
		fmt.Sprintf("More than %d wrong pairing or invite codes in the last %s, from all accounts together. "+
			"Until that stops, each account and address may enter only %d wrong codes per %s. "+
			"The portal's log has the details.", surgeFails, failWindow, surgeMaxFails, failWindow), 1)
}

// handlePreviewInvite shows who a link is from, for its page. It needs no
// account: a link token is too long to find by trying.
func (s *Server) handlePreviewInvite(w http.ResponseWriter, r *http.Request) {
	ipKey := "inv:ip:" + s.clientIP(r)
	if s.failCount(ipKey, failWindow) >= maxFails*4 {
		writeErr(w, http.StatusTooManyRequests, "throttled", "too many wrong links; try again in a few minutes")
		return
	}
	p, err := s.store.PreviewInvite(r.PathValue("kind"), r.PathValue("token"))
	if errors.Is(err, accounts.ErrNotFound) {
		s.recordFailure(ipKey, allFailsKey)
		writeErr(w, http.StatusNotFound, "bad_code", "this link is wrong, expired, revoked or used up")
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	p.Inviter = maskName(p.Inviter)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, p)
}

// handleAcceptInvite uses a link, a bare link token or a code: {link}, or
// {kind, code}. A code or token that does not say its kind is tried as a
// pairing code first, then as a share invite.
func (s *Server) handleAcceptInvite(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	var req struct {
		Link string `json:"link"`
		Kind string `json:"kind"`
		Code string `json:"code"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	in := req.Link
	if in == "" {
		in = req.Code
	}
	ref := accounts.ParseInvite(in)
	kinds := []string{accounts.KindPair, accounts.KindShare}
	switch {
	case ref.Kind != "":
		kinds = []string{ref.Kind}
	case req.Kind == accounts.KindPair || req.Kind == accounts.KindShare:
		kinds = []string{req.Kind}
	}
	s.serveInvite(w, r, cu, kinds, ref, false)
}

// serveInvite uses ref as the first of kinds it is valid for and answers with
// the other side: as an AcceptedInvite, or, for the older routes (legacy), as
// the PairedUser or SharePartner they always returned.
func (s *Server) serveInvite(w http.ResponseWriter, r *http.Request, cu ctxUser, kinds []string, ref accounts.InviteRef, legacy bool) {
	scope := "pair"
	if kinds[0] == accounts.KindShare {
		scope = "share"
	}
	keys, ok := s.codeGuard(r, cu, scope)
	if !ok {
		writeErr(w, http.StatusTooManyRequests, "throttled", "too many wrong codes; try again later")
		return
	}
	if ref.Token == "" && ref.Code == "" {
		writeErr(w, http.StatusNotFound, "bad_code", "enter a code or a link")
		return
	}
	var out any
	var acc *AcceptedInvite
	fresh := false // a request for approval was filed just now
	err := accounts.ErrNotFound
	for _, kind := range kinds {
		switch kind {
		case accounts.KindPair:
			var p *accounts.PairedUser
			if ref.Token != "" {
				p, err = s.store.RedeemPairingLink(cu.user.ID, ref.Token)
			} else {
				p, err = s.store.RedeemPairingCode(cu.user.ID, ref.Code)
			}
			if err == nil {
				p.Username = maskName(p.Username)
				out, acc, fresh = p, &AcceptedInvite{Kind: kind, ID: p.ID, User: p.Username, Label: p.Label, Since: p.Since, Pending: p.Pending}, p.NewRequest
			}
		case accounts.KindShare:
			var p *accounts.SharePartner
			if ref.Token != "" {
				p, err = s.store.JoinShareLink(cu.user.ID, ref.Token)
			} else {
				p, err = s.store.JoinShare(cu.user.ID, ref.Code)
			}
			if err == nil {
				p.Username = maskName(p.Username)
				out, acc, fresh = p, &AcceptedInvite{Kind: kind, ID: p.ID, User: p.Username, Since: p.Since, Pending: p.Pending}, p.NewRequest
			}
		}
		if !errors.Is(err, accounts.ErrNotFound) {
			break
		}
	}
	if err != nil {
		if errors.Is(err, accounts.ErrNotFound) {
			s.recordFailure(keys...)
			msg := "this code is wrong, expired, revoked or used up"
			if len(kinds) == 1 && kinds[0] == accounts.KindPair {
				msg = "this pairing code is wrong, expired, revoked or used up"
			} else if len(kinds) == 1 {
				msg = "this invite code is wrong, expired, revoked or already used"
			}
			writeErr(w, http.StatusNotFound, "bad_code", msg)
			return
		}
		if acc == nil && len(kinds) == 1 && kinds[0] == accounts.KindShare {
			s.sharingErr(w, err)
			return
		}
		s.pairingErr(w, err)
		return
	}
	status := http.StatusOK
	if acc.Pending {
		status = http.StatusAccepted
		if fresh {
			s.notifyRequest(cu, acc)
		}
	}
	s.log.Info("invite used", "kind", acc.Kind, "user", cu.user.ID, "owner", acc.ID, "pending", acc.Pending, "link", ref.Token != "")
	if legacy {
		writeJSON(w, status, out)
		return
	}
	writeJSON(w, status, acc)
}

// notifyRequest tells the owner of a code that cu waits for its approval.
func (s *Server) notifyRequest(cu ctxUser, acc *AcceptedInvite) {
	owner, err := s.store.User(acc.ID)
	if err != nil {
		return
	}
	what, where := "pair with you as a buyer", "Pairing"
	if acc.Kind == accounts.KindShare {
		what, where = "become your share partner", "Sharing"
	}
	s.notify(mailer.Message{Kind: mailer.KindNotice, To: owner.Username, Subject: "ShareToken: " + maskName(cu.user.Username) + " asks to " + what,
		Text: fmt.Sprintf("%s used a code or link of yours and asks to %s.\r\n\r\n"+
			"Approve or reject the request on the %s page of the ShareToken app or the portal. It expires in %d days.\r\n"+
			"If you do not know this account, reject it, and revoke the code if it reached someone it should not have.\r\n",
			maskName(cu.user.Username), what, where, int(accounts.RequestTTL/(24*time.Hour)))})
}

func requestID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "bad request id")
		return 0, false
	}
	return id, true
}

// handleApproveRequest carries out a request waiting for the caller.
func (s *Server) handleApproveRequest(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	id, ok := requestID(w, r)
	if !ok {
		return
	}
	req, err := s.store.ApproveRequest(cu.user.ID, id)
	if err != nil {
		if errors.Is(err, accounts.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "not_found", "this request is gone: withdrawn, expired, or its code no longer admits anyone")
			return
		}
		if strings.Contains(r.URL.Path, "/sharing/") {
			s.sharingErr(w, err)
		} else {
			s.pairingErr(w, err)
		}
		return
	}
	s.log.Info("invite request approved", "kind", req.Kind, "owner", cu.user.ID, "user", req.UserID)
	what := "you are now paired with them, and buy only from the sellers you are paired with"
	if req.Kind == accounts.KindShare {
		what = "you are now share partners and use each other's nodes for free"
	}
	s.notify(mailer.Message{Kind: mailer.KindNotice, To: req.Username, Subject: "ShareToken: " + maskName(cu.user.Username) + " approved your request",
		Text: fmt.Sprintf("%s approved your request: %s.\r\n", maskName(cu.user.Username), what)})
	req.Username = maskName(req.Username)
	writeJSON(w, http.StatusOK, req)
}

func (s *Server) handleRejectRequest(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	id, ok := requestID(w, r)
	if !ok {
		return
	}
	if err := s.store.RejectRequest(cu.user.ID, id); err != nil {
		s.pairingErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleWithdrawRequest(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	id, ok := requestID(w, r)
	if !ok {
		return
	}
	if err := s.store.WithdrawRequest(cu.user.ID, id); err != nil {
		s.pairingErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func maskRequests(rs []accounts.InviteRequest) []accounts.InviteRequest {
	for i := range rs {
		rs[i].Username = maskName(rs[i].Username)
	}
	return rs
}
