package portal

// Signing in, without passwords. New and existing accounts take the same path:
//
//	POST /api/v1/auth/code {email, invite_code}
//
// mails a six-digit code to the address and answers with a nonce: the code
// only redeems together with it, so it is worth nothing to anyone but who
// asked, and nobody else's guesses count against it. An address without an
// account gets one when the code is redeemed, provided the registration rules
// let it sign up; those rules are checked before any mail goes out, so mail
// only reaches people who can finish. The answer says whether the address is
// new, which the pages use to mention the sign-up credit or ask for an invite
// code.
//
//	POST /api/v1/auth/verify     {email, code, nonce, invite_code, token_name, totp} (clients: a token)
//	POST /api/v1/web/auth/verify {email, code, nonce, invite_code}                   (the portal's pages: a session)
//
// redeem the code. A code an operator issued ("portal login-code") needs no
// nonce. An account with two-factor authentication gets a challenge
// instead, finished with POST /api/v1/mfa or /api/v1/web/mfa (mfa.go); clients
// may also send the authenticator code along as totp. An administrator must
// have two-factor authentication: one without it enrolls on the web pages.

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"sharetoken/internal/accounts"
	"sharetoken/internal/mailer"
)

type codeRequest struct {
	Email  string `json:"email"`
	Invite string `json:"invite_code"`
}

type signInRequest struct {
	Email     string `json:"email"`
	Code      string `json:"code"`        // the mailed code
	Nonce     string `json:"nonce"`       // what the request for that code returned
	Invite    string `json:"invite_code"` // a new account, when the settings require one
	TokenName string `json:"token_name"`  // clients: what the token is listed as
	TOTP      string `json:"totp"`        // clients: an authenticator code, saving the challenge round trip
}

// AuthResponse is what a client's sign-in returns.
type AuthResponse struct {
	User    *accounts.User `json:"user"`
	Token   string         `json:"token"`
	Created bool           `json:"created,omitempty"` // the address had no account before
}

// signInEmail checks and normalizes the address of a request, writing the
// error response itself.
func signInEmail(w http.ResponseWriter, raw string) (string, bool) {
	email := accounts.NormalizeEmail(raw)
	if err := accounts.CheckEmail(email); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_email", "enter a valid email address")
		return "", false
	}
	return email, true
}

// accountFor looks the address up. For an address without an account it
// applies the registration rules, writing the refusal itself. Refusals of a new
// address count against the caller's IP, so the answers are no fast directory
// of who has an account. A disabled account comes back like any other: only
// someone who holds a code for it is told (signIn), and nobody holds one.
func (s *Server) accountFor(w http.ResponseWriter, ip, email, invite string, st Settings) (u *accounts.User, isNew, ok bool) {
	if s.throttled("lookup:" + ip) {
		writeErr(w, http.StatusTooManyRequests, "throttled", "too many requests from this address; try again in a few minutes")
		return nil, false, false
	}
	u, err := s.store.UserByName(email)
	switch {
	case errors.Is(err, accounts.ErrNotFound):
	case err != nil:
		s.internal(w, err)
		return nil, false, false
	default:
		return u, false, true
	}
	if !st.OpenRegistration {
		s.recordFailure("lookup:" + ip)
		writeErr(w, http.StatusForbidden, "registration_closed", "this address has no account, and registration is closed; ask an administrator for an account")
		return nil, true, false
	}
	if !s.checkSignupRules(w, ip, email, invite, st) {
		s.recordFailure("lookup:" + ip)
		return nil, true, false
	}
	return nil, true, true
}

func (s *Server) handleAuthCode(w http.ResponseWriter, r *http.Request) {
	st := s.settings()
	if st.MailFrom == "" {
		writeErr(w, http.StatusServiceUnavailable, "mail_unavailable", "this portal cannot send mail, so nobody can sign in; tell the administrator")
		return
	}
	// Browsers may only call this from the portal's own pages, or any page could
	// make visitors' browsers send mail. Other clients send no Origin.
	if r.Header.Get("Origin") != "" && !sameOrigin(r) {
		writeErr(w, http.StatusForbidden, "cross_site", "this endpoint is for the portal's own pages")
		return
	}
	var req codeRequest
	if !readJSON(w, r, &req) {
		return
	}
	email, ok := signInEmail(w, req.Email)
	if !ok {
		return
	}
	ip := s.rateIP(r)
	u, isNew, ok := s.accountFor(w, ip, email, req.Invite, st)
	if !ok {
		return
	}
	// A disabled account is answered, limited and counted like a working one;
	// its mail says that it is disabled and carries no code.
	nonce, err := s.mail.RequestCode(mailer.CodeRequest{Purpose: mailer.PurposeLogin, Email: email, IP: ip, Known: !isNew,
		NoCode: u != nil && u.Disabled, From: st.MailFrom, ReplyTo: s.supportAddress()})
	switch {
	case errors.Is(err, mailer.ErrCodeCooldown):
		writeErr(w, http.StatusTooManyRequests, "code_cooldown", "a code was just sent to this address; wait a minute before asking again")
	case errors.Is(err, mailer.ErrCodeLimit):
		if errors.Is(err, mailer.ErrNewCodeBudget) || errors.Is(err, mailer.ErrKnownCodeBudget) {
			s.codeBudgetSpent(!isNew)
		}
		writeErr(w, http.StatusTooManyRequests, "throttled", "too many codes requested; try again later")
	case err != nil:
		s.internal(w, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "nonce": nonce, "new_account": isNew,
			"cooldown_seconds": int(mailer.CodeCooldown / time.Second), "expires_in_seconds": int(mailer.CodeTTL / time.Second)})
	}
}

// codeBudgetSpent tells the administrators, once an hour, that the portal has
// mailed all the sign-in codes it mails in an hour to addresses with an
// account (known) or without one: people of that kind cannot sign in until
// the hour moves on.
func (s *Server) codeBudgetSpent(known bool) {
	what, effect := "addresses without an account", "Nobody new can sign up until the hour moves on; people who have an account still get their codes."
	if known {
		what, effect = "addresses with an account", "People who are not signed in cannot sign in until the hour moves on (\"portal login-code <email>\" on the server still lets one in)."
	}
	if !s.codeAlerts.take(what, 1, s.cfg.Now()) {
		return
	}
	s.log.Warn("sign-in codes: the hourly budget over everyone is used up", "for", what)
	s.push("ShareToken: sign-in codes used up", "The portal has mailed every sign-in code it mails in an hour to "+what+". "+effect+
		" Someone may be asking for codes in bulk: the mail outbox shows where they go.", 1)
}

// signIn redeems a code and returns its account, creating it for a new
// address. On failure it has written the response. The second factor is the
// caller's.
func (s *Server) signIn(w http.ResponseWriter, r *http.Request, req signInRequest) (u *accounts.User, created, ok bool) {
	st := s.settings()
	email, ok := signInEmail(w, req.Email)
	if !ok {
		return nil, false, false
	}
	ip := s.rateIP(r)
	// Wrong codes count against who sends them and, in the database, against
	// the code of the nonce they come with: never against the address, or
	// anyone could lock its owner out. The attempt is counted before the code
	// is checked and given back when it was right, so guesses sent together
	// cannot all get past the limit.
	release, free := s.reserveThrottle("code:" + ip)
	if !free {
		writeErr(w, http.StatusTooManyRequests, "throttled", "too many wrong codes; try again in a few minutes")
		return nil, false, false
	}
	u, isNew, ok := s.accountFor(w, ip, email, req.Invite, st)
	if !ok {
		release()
		return nil, false, false
	}
	if isNew && s.throttled("reg:"+ip) {
		release()
		writeErr(w, http.StatusTooManyRequests, "throttled", "too many sign-ups from this address; try again in a few minutes")
		return nil, false, false
	}
	// Last: a code is used up only by a request that can otherwise succeed.
	nonce := strings.TrimSpace(req.Nonce)
	switch err := s.mail.VerifyCode(mailer.PurposeLogin, email, nonce, req.Code); {
	case errors.Is(err, mailer.ErrBadCode):
		msg := "wrong or expired code"
		if nonce == "" {
			// A client from before codes were bound to their request ends up here.
			msg += "; a code only works where it was asked for, so ask for a new one here (an app too old to do that must be updated)"
		}
		writeErr(w, http.StatusUnauthorized, "bad_code", msg)
		return nil, false, false
	case err != nil:
		release()
		s.internal(w, err)
		return nil, false, false
	}
	release()
	if !isNew {
		if u.Disabled {
			writeErr(w, http.StatusForbidden, "account_disabled", "this account is disabled; contact support")
			return nil, false, false
		}
		return u, false, true
	}
	if st.InviteRequired {
		ok, err := s.useInvite(req.Invite)
		if err != nil {
			s.internal(w, err)
			return nil, false, false
		}
		if !ok {
			writeErr(w, http.StatusForbidden, "bad_invite", "this invite code is wrong, used up or expired")
			return nil, false, false
		}
	}
	u, err := s.CreateUser(email, false)
	if errors.Is(err, accounts.ErrExists) {
		// Another sign-in of the same address got there first.
		if st.InviteRequired {
			s.returnInvite(req.Invite)
		}
		u, err = s.store.UserByName(email)
		if err != nil {
			s.internal(w, err)
			return nil, false, false
		}
		return u, false, true
	}
	if err != nil {
		if st.InviteRequired && u == nil {
			s.returnInvite(req.Invite)
		}
		s.accountErr(w, err)
		return nil, false, false
	}
	s.recordFailure("reg:" + ip)
	s.log.Info("user registered", "user", u.Username, "id", u.ID, "ip", ip)
	return u, true, true
}

func status(created bool) int {
	if created {
		return http.StatusCreated
	}
	return http.StatusOK
}

// handleAuthVerify signs a client in: a token.
func (s *Server) handleAuthVerify(w http.ResponseWriter, r *http.Request) {
	var req signInRequest
	if !readJSON(w, r, &req) {
		return
	}
	u, created, ok := s.signIn(w, r, req)
	if !ok {
		return
	}
	proceed, mfa := s.secondFactor(w, r, u, strings.TrimSpace(req.TOTP))
	if !proceed {
		return
	}
	s.issueLoginToken(w, u, req.TokenName, status(created), mfa, created)
}

// handleWebAuthVerify signs the portal's pages in: a session cookie.
func (s *Server) handleWebAuthVerify(w http.ResponseWriter, r *http.Request) {
	var req signInRequest
	if !readJSON(w, r, &req) {
		return
	}
	req.TOTP = "" // the pages always take the challenge round trip
	u, created, ok := s.signIn(w, r, req)
	if !ok {
		return
	}
	proceed, mfa := s.secondFactor(w, r, u, "")
	if !proceed {
		return
	}
	s.startSession(w, r, u, status(created), mfa)
}

func (s *Server) issueLoginToken(w http.ResponseWriter, u *accounts.User, name string, status int, mfa, created bool) {
	if name = strings.TrimSpace(name); name == "" {
		name = "login"
	}
	plain, _, err := s.store.IssueSessionToken(u.ID, name, s.loginTTL(), mfa)
	if err != nil {
		s.accountErr(w, err)
		return
	}
	writeJSON(w, status, AuthResponse{User: u, Token: plain, Created: created})
}

// IssueSignInCode makes a sign-in code for an existing account without mailing
// it: what the operator command "portal login-code" prints, for someone who
// cannot get the mail. The code signs in on the web pages and in clients in
// place of a mailed one (once, for mailer.CodeTTL), and needs no nonce: it is
// twelve digits, too long to guess. Two-factor authentication still applies.
func (s *Server) IssueSignInCode(email string) (code string, expires time.Time, err error) {
	email = accounts.NormalizeEmail(email)
	u, err := s.store.UserByName(email)
	if err != nil {
		return "", time.Time{}, err
	}
	if u.Disabled {
		return "", time.Time{}, accounts.ErrDisabled
	}
	return s.mail.IssueCode(mailer.PurposeLogin, u.Username)
}
