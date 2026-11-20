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

// Guessing codes. A code is only 30 random bits (six characters, to be easy
// to type) and a link token 47.6, so every wrong one counts: per account and
// per address over a quarter of an hour and over a day, with less for new
// accounts. Over everyone, guessers with many accounts and addresses are
// stopped whole: when wrong codes reach codeSurgeFails in an hour, codes stop
// working for everyone for surgePause (links go on); when wrong links reach
// linkSurgeFails, the public preview of links stops. Each pause is pushed to
// the administrators (pushover.go).
const (
	guessWindow        = 15 * time.Minute
	guessDay           = 24 * time.Hour
	userMaxFails       = 5              // wrong codes or links one account may enter in guessWindow
	userDayMaxFails    = 15             // … in guessDay
	ipMaxFails         = 10             // … one address in guessWindow (people behind one NAT share it)
	ipDayMaxFails      = 50             // … in guessDay
	newAccountAge      = 24 * time.Hour // an account younger than this gets newAccountMaxFails
	newAccountMaxFails = 3              // wrong codes or links a new account may enter in guessDay
	surgeWindow        = time.Hour
	codeSurgeFails     = 100  // wrong codes from everyone in surgeWindow that stop codes
	linkSurgeFails     = 2000 // wrong links from everyone in surgeWindow that stop link previews
	surgePause         = time.Hour
	codeFailsKey       = "inv:codes" // everyone's wrong codes
	linkFailsKey       = "inv:links" // everyone's wrong links
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

// codeGuard decides whether cu may try one more code (link false) or link
// from r. It returns the keys a wrong one is counted under, and the error code
// to refuse with: "throttled", "codes_paused", or "" to go on.
func (s *Server) codeGuard(r *http.Request, cu ctxUser, link bool) (keys []string, refuse string) {
	uid := strconv.FormatInt(cu.user.ID, 10)
	userKey, userDayKey := "inv:u:"+uid, "inv:ud:"+uid
	ipKeys, ipOK := s.ipGuard(r)
	keys = append([]string{userKey, userDayKey}, ipKeys...)
	if link {
		keys = append(keys, linkFailsKey)
	} else {
		keys = append(keys, codeFailsKey)
		if s.paused(&s.codesPaused) {
			return keys, "codes_paused"
		}
	}
	dayMax := userDayMaxFails
	if s.cfg.Now().Sub(time.Unix(0, cu.user.CreatedAt)) < newAccountAge {
		dayMax = newAccountMaxFails
	}
	if !ipOK || s.failCount(userKey, guessWindow) >= userMaxFails || s.failCount(userDayKey, guessDay) >= dayMax {
		return keys, "throttled"
	}
	return keys, ""
}

// ipGuard is codeGuard's part for r's address: its keys, and whether it may
// try one more.
func (s *Server) ipGuard(r *http.Request) (keys []string, ok bool) {
	ip := s.clientIP(r)
	ipKey, ipDayKey := "inv:ip:"+ip, "inv:ipd:"+ip
	ok = s.failCount(ipKey, guessWindow) < ipMaxFails && s.failCount(ipDayKey, guessDay) < ipDayMaxFails
	return []string{ipKey, ipDayKey}, ok
}

// paused reports whether the pause *until stands now.
func (s *Server) paused(until *time.Time) bool {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	return s.cfg.Now().Before(*until)
}

// guessFailed counts a wrong code (link false) or link under keys and pauses
// codes or link previews for everyone when too many came in from everyone.
func (s *Server) guessFailed(link bool, keys ...string) {
	s.recordFailure(keys...)
	key, limit, until, what := codeFailsKey, codeSurgeFails, &s.codesPaused, "codes"
	if link {
		key, limit, until, what = linkFailsKey, linkSurgeFails, &s.linksPaused, "link previews"
	}
	n := s.failCount(key, surgeWindow)
	if n < limit {
		return
	}
	now := s.cfg.Now()
	s.failMu.Lock()
	start := !now.Before(*until)
	if start {
		*until = now.Add(surgePause)
	}
	s.failMu.Unlock()
	if !start {
		return
	}
	s.log.Warn("pairing and invite "+what+" are being guessed: paused for everyone", "wrong", n, "window", surgeWindow.String(), "pause", surgePause.String())
	msg := fmt.Sprintf("%d wrong pairing or invite codes in the last %s, from all accounts together. "+
		"Codes are refused for everyone for %s; invite links still work. The portal's log has the details.", n, surgeWindow, surgePause)
	if link {
		msg = fmt.Sprintf("%d wrong invite links in the last %s, from all addresses together. "+
			"The public preview of links is refused for %s; signed-in users can still accept links. The portal's log has the details.", n, surgeWindow, surgePause)
	}
	s.push("ShareToken: invite "+what+" are being guessed", msg, 1)
}

// handlePreviewInvite shows who a link is from, for its page. It needs no
// account: a link token is too long to find by trying.
func (s *Server) handlePreviewInvite(w http.ResponseWriter, r *http.Request) {
	keys, ok := s.ipGuard(r)
	if !ok {
		writeErr(w, http.StatusTooManyRequests, "throttled", "too many wrong links; try again later")
		return
	}
	if s.paused(&s.linksPaused) {
		writeErr(w, http.StatusTooManyRequests, "links_paused", "too many wrong links from everyone: previews are paused for a while; sign in and accept the link instead")
		return
	}
	p, err := s.store.PreviewInvite(r.PathValue("kind"), r.PathValue("token"))
	if errors.Is(err, accounts.ErrNotFound) {
		s.guessFailed(true, append(keys, linkFailsKey)...)
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
	link := ref.Token != ""
	keys, refuse := s.codeGuard(r, cu, link)
	switch refuse {
	case "codes_paused":
		writeErr(w, http.StatusTooManyRequests, refuse, "too many wrong codes from everyone: codes are paused for up to an hour; open the invite link instead")
		return
	case "throttled":
		writeErr(w, http.StatusTooManyRequests, refuse, "too many wrong codes; try again later")
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
			s.guessFailed(link, keys...)
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
