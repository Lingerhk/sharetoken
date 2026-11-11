package portal

// Forgotten passwords: a link by mail that works once.
//
// Only accounts named by an email address can use it; others have nowhere to
// send a link and ask an administrator. The address's answer never depends on
// whether it has an account, and the database work runs after the answer, so
// neither the response nor its timing tells strangers who has registered. The
// token is 256 random bits, kept only as a hash, in the fragment of the link so
// that it reaches no server log. A reset revokes every token and session of the
// account; an administrator's two-factor enrolment stays, so a mailbox alone
// never opens the console.

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"sharetoken/internal/accounts"
	"sharetoken/internal/mailer"
)

const (
	resetTTL      = 30 * time.Minute
	resetCooldown = 60 * time.Second
	resetsPerHour = 5   // per account
	resetsAllHour = 500 // all together: what an abuser can make this IP send
)

// resetActive reports whether the "forgot password" flow can work at all. The
// public address comes only from the configuration file: the links must not
// point wherever whoever edits the settings page wants.
func (s *Server) resetActive(st Settings) bool {
	return st.PasswordReset && s.cfg.PublicURL != "" && st.MailFrom != ""
}

// newOpaqueCode makes an invite code: n bytes of randomness, base32.
func newOpaqueCode(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b), nil
}

func hashResetToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

func (s *Server) resetRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/password/forgot", s.public(s.handleForgotPassword))
	mux.HandleFunc("POST /api/v1/password/reset", s.public(webOnly(s.handleResetPassword)))
}

// handleForgotPassword is POST /api/v1/password/forgot {email}.
func (s *Server) handleForgotPassword(w http.ResponseWriter, r *http.Request) {
	st := s.settings()
	if !s.resetActive(st) {
		writeErr(w, http.StatusNotFound, "not_supported", "this portal does not reset passwords by mail; ask an administrator")
		return
	}
	// As for sign-up codes: browsers only from the portal's own pages, or any
	// page could make visitors' browsers send mail. Other clients send no Origin.
	if r.Header.Get("Origin") != "" && !sameOrigin(r) {
		writeErr(w, http.StatusForbidden, "cross_site", "this endpoint is for the portal's own pages")
		return
	}
	var req struct {
		Email string `json:"email"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	ip := s.clientIP(r)
	if s.throttled("forgot:" + ip) {
		writeErr(w, http.StatusTooManyRequests, "throttled", "too many requests from this address; try again in a few minutes")
		return
	}
	s.recordFailure("forgot:" + ip)
	email := accounts.NormalizeEmail(req.Email)
	if accounts.CheckEmail(email) == nil {
		s.background.Add(1)
		go func() {
			defer s.background.Done()
			if err := s.sendResetLink(email, ip, st); err != nil {
				s.log.Warn("sending a password-reset link", "err", err)
			}
		}()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "expires_in_seconds": int(resetTTL / time.Second)})
}

// sendResetLink mails a link if email is an active account under the limits.
// Whatever it decides stays out of the HTTP answer.
func (s *Server) sendResetLink(email, ip string, st Settings) error {
	u, err := s.store.UserByName(email)
	if errors.Is(err, accounts.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if u.Disabled {
		return nil
	}
	now := s.cfg.Now()
	hourAgo := now.Add(-time.Hour).UnixNano()
	var last sql.NullInt64
	var forUser, total int
	if err := s.db.QueryRow(`SELECT MAX(created_at), COUNT(*) FROM password_resets WHERE user_id = ? AND created_at >= ?`, u.ID, hourAgo).Scan(&last, &forUser); err != nil {
		return err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM password_resets WHERE created_at >= ?`, hourAgo).Scan(&total); err != nil {
		return err
	}
	if (last.Valid && now.UnixNano()-last.Int64 < int64(resetCooldown)) || forUser >= resetsPerHour || total >= resetsAllHour {
		return nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	// Old rows only serve the limits above; a newer link replaces older ones.
	if _, err := s.db.Exec(`DELETE FROM password_resets WHERE created_at < ?`, now.Add(-48*time.Hour).UnixNano()); err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE password_resets SET consumed_at = ? WHERE user_id = ? AND consumed_at = 0`, now.UnixNano(), u.ID); err != nil {
		return err
	}
	if _, err := s.db.Exec(`INSERT INTO password_resets (user_id, token_hash, ip, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
		u.ID, hashResetToken(token), ip, now.UnixNano(), now.Add(resetTTL).UnixNano()); err != nil {
		return err
	}
	m := mailer.ResetMail(u.Username, s.cfg.PublicURL+"/app/#reset="+token, resetTTL, now)
	m.From, m.ReplyTo = st.MailFrom, s.supportAddress()
	_, err = s.mail.Enqueue(m)
	return err
}

// handleResetPassword is POST /api/v1/password/reset {token, new_password}.
func (s *Server) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
		New   string `json:"new_password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	ip := s.clientIP(r)
	if s.throttled("reset:" + ip) {
		writeErr(w, http.StatusTooManyRequests, "throttled", "too many wrong links from this address; try again in a few minutes")
		return
	}
	// Before the token: a short password must not use it up.
	if err := accounts.CheckPassword(req.New); err != nil {
		s.accountErr(w, err)
		return
	}
	badLink := func() {
		s.recordFailure("reset:" + ip)
		writeErr(w, http.StatusBadRequest, "bad_reset_link", "this link is wrong, used or expired; ask for a new one")
	}
	now := s.cfg.Now().UnixNano()
	var id, userID int64
	err := s.db.QueryRow(`SELECT id, user_id FROM password_resets WHERE token_hash = ? AND consumed_at = 0 AND expires_at > ?`,
		hashResetToken(strings.TrimSpace(req.Token)), now).Scan(&id, &userID)
	if errors.Is(err, sql.ErrNoRows) {
		badLink()
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	u, err := s.store.User(userID)
	if errors.Is(err, accounts.ErrNotFound) || (err == nil && u.Disabled) {
		badLink()
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	// Two requests with the same link: only one gets past this.
	res, err := s.db.Exec(`UPDATE password_resets SET consumed_at = ? WHERE id = ? AND consumed_at = 0`, now, id)
	if err != nil {
		s.internal(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		badLink()
		return
	}
	if err := s.store.SetPassword(u.ID, req.New); err != nil {
		s.accountErr(w, err)
		return
	}
	revoked, err := s.store.RevokeAllTokens(u.ID)
	if err != nil {
		s.internal(w, err)
		return
	}
	s.notify(mailer.PasswordChangedMail(u.Username, true, s.cfg.Now(), ip, s.supportAddress()))
	s.auditEvent(r, u, actionPasswordReset, u.Username, map[string]any{"tokens_revoked": revoked})
	s.log.Info("password reset by mail", "user", u.Username, "tokens_revoked", revoked, "ip", ip)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
