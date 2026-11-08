package portal

// Two-factor authentication for administrators.
//
// An admin's password alone never opens a session. After the password
// checks out the portal hands the client a short-lived challenge id and asks
// for a TOTP code (or, for an admin without a secret yet, for enrollment:
// the portal generates a secret, shows it as a QR code and secret text, and
// enrolls it once the client proves it with a first code). Tokens and
// sessions remember whether they were issued after a second factor; admin
// endpoints accept only those, so a promotion to admin or this feature's
// roll-out forces a fresh two-factor login. Secrets are stored sealed with
// AES-GCM under a key derived from the portal's own private key, so the
// database file alone does not yield working authenticator secrets.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/skip2/go-qrcode"

	"sharetoken/accounts"
	"sharetoken/internal/totp"
)

const (
	mfaIssuer        = "sharetoken"
	challengeTTL     = 5 * time.Minute
	challengeTries   = 5 // wrong codes before a challenge is discarded
	mfaPurposeCode   = "code"
	mfaPurposeEnroll = "enroll"
)

// A challenge is an authenticated-but-not-yet-complete login.
type mfaChallenge struct {
	userID  int64
	purpose string
	secret  []byte // enrollment: the pending secret
	tries   int
	expires time.Time
}

// deriveMFAKey turns the portal's signing key into the key that seals TOTP
// secrets. The signing key never leaves the server and is already what the
// relays trust, so it is a fitting root.
func deriveMFAKey(k ed25519.PrivateKey) []byte {
	h := sha256.New()
	h.Write([]byte("sharetoken portal mfa v1\x00"))
	h.Write(k.Seed())
	return h.Sum(nil)
}

var errMFAUnavailable = errors.New("portal: two-factor authentication is not configured (no key)")

func (s *Server) seal(plain []byte) ([]byte, error) {
	if len(s.mfaKey) == 0 {
		return nil, errMFAUnavailable
	}
	block, err := aes.NewCipher(s.mfaKey)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return g.Seal(nonce, nonce, plain, nil), nil
}

func (s *Server) open(sealed []byte) ([]byte, error) {
	if len(s.mfaKey) == 0 {
		return nil, errMFAUnavailable
	}
	block, err := aes.NewCipher(s.mfaKey)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(sealed) < g.NonceSize() {
		return nil, errors.New("portal: sealed secret too short")
	}
	return g.Open(nil, sealed[:g.NonceSize()], sealed[g.NonceSize():], nil)
}

// newChallenge stores a challenge and returns its id.
func (s *Server) newChallenge(userID int64, purpose string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id := base64.RawURLEncoding.EncodeToString(b)
	now := s.cfg.Now()
	s.mfaMu.Lock()
	defer s.mfaMu.Unlock()
	for k, c := range s.challenges { // lazy expiry
		if !c.expires.After(now) {
			delete(s.challenges, k)
		}
	}
	s.challenges[id] = &mfaChallenge{userID: userID, purpose: purpose, expires: now.Add(challengeTTL)}
	return id, nil
}

// challenge looks a challenge up; a missing or expired one is nil.
func (s *Server) challenge(id, purpose string) *mfaChallenge {
	s.mfaMu.Lock()
	defer s.mfaMu.Unlock()
	c := s.challenges[id]
	if c == nil {
		return nil
	}
	if !c.expires.After(s.cfg.Now()) {
		delete(s.challenges, id)
		return nil
	}
	if c.purpose != purpose { // the right challenge used at the wrong step
		return nil
	}
	return c
}

// failChallenge counts a wrong code; the challenge is dropped after
// challengeTries of them.
func (s *Server) failChallenge(id string) {
	s.mfaMu.Lock()
	defer s.mfaMu.Unlock()
	if c := s.challenges[id]; c != nil {
		c.tries++
		if c.tries >= challengeTries {
			delete(s.challenges, id)
		}
	}
}

func (s *Server) dropChallenge(id string) {
	s.mfaMu.Lock()
	delete(s.challenges, id)
	s.mfaMu.Unlock()
}

// writeChallenge tells the client the password was right but a second step
// is needed. It is a 401 like any other incomplete login.
func writeChallenge(w http.ResponseWriter, code, msg, challenge string) {
	var b struct {
		ErrorBody
		Challenge string `json:"challenge"`
	}
	b.Error.Code, b.Error.Message, b.Challenge = code, msg, challenge
	writeJSON(w, http.StatusUnauthorized, b)
}

// secondFactor decides what happens after a correct password. It returns
// (true, mfa) when the login may complete now, having written the response
// (a challenge) otherwise. code is a TOTP code sent along with the password
// (API clients); the web pages always use the challenge round-trip.
func (s *Server) secondFactor(w http.ResponseWriter, r *http.Request, u *accounts.User, code string) (proceed, mfa bool) {
	if !u.Admin {
		return true, false
	}
	if len(s.mfaKey) == 0 {
		s.internal(w, errMFAUnavailable)
		return false, false
	}
	if !u.MFAEnrolled() {
		id, err := s.newChallenge(u.ID, mfaPurposeEnroll)
		if err != nil {
			s.internal(w, err)
			return false, false
		}
		writeChallenge(w, "mfa_setup_required", "administrators must enroll two-factor authentication; finish it on the web portal", id)
		return false, false
	}
	if code == "" {
		id, err := s.newChallenge(u.ID, mfaPurposeCode)
		if err != nil {
			s.internal(w, err)
			return false, false
		}
		writeChallenge(w, "mfa_required", "a two-factor code is required", id)
		return false, false
	}
	ok, err := s.verifyCode(u, code)
	if err != nil {
		s.internal(w, err)
		return false, false
	}
	if !ok {
		s.recordFailure("u:"+u.Username, "ip:"+s.clientIP(r))
		s.log.Warn("two-factor code rejected", "user", u.Username, "ip", s.clientIP(r))
		writeErr(w, http.StatusUnauthorized, "bad_code", "wrong two-factor code")
		return false, false
	}
	return true, true
}

// verifyCode checks a TOTP code against the user's enrolled secret and burns
// the step it matched.
func (s *Server) verifyCode(u *accounts.User, code string) (bool, error) {
	sealed, last, err := s.store.MFASecret(u.ID)
	if err != nil {
		return false, err
	}
	if len(sealed) == 0 {
		return false, nil
	}
	secret, err := s.open(sealed)
	if err != nil {
		return false, err
	}
	step, ok := totp.Verify(secret, code, s.cfg.Now(), last)
	if !ok {
		return false, nil
	}
	if err := s.store.SetMFAStep(u.ID, step); err != nil {
		if errors.Is(err, accounts.ErrNotFound) { // raced with another login using the same code
			return false, nil
		}
		return false, err
	}
	return true, nil
}

type mfaRequest struct {
	Challenge string `json:"challenge"`
	Code      string `json:"code"`
	TokenName string `json:"token_name"`
}

// completeChallenge checks the code for a "code" challenge and returns the
// user when the login may finish.
func (s *Server) completeChallenge(w http.ResponseWriter, r *http.Request, req mfaRequest) *accounts.User {
	c := s.challenge(req.Challenge, mfaPurposeCode)
	if c == nil {
		writeErr(w, http.StatusUnauthorized, "challenge_expired", "start the login again")
		return nil
	}
	u, err := s.store.User(c.userID)
	if err != nil || u.Disabled || !u.Admin {
		s.dropChallenge(req.Challenge)
		writeErr(w, http.StatusUnauthorized, "challenge_expired", "start the login again")
		return nil
	}
	ip := s.clientIP(r)
	if s.throttled("u:"+u.Username) || s.throttled("ip:"+ip) {
		writeErr(w, http.StatusTooManyRequests, "throttled", "too many failed logins; try again in a few minutes")
		return nil
	}
	ok, err := s.verifyCode(u, req.Code)
	if err != nil {
		s.internal(w, err)
		return nil
	}
	if !ok {
		s.failChallenge(req.Challenge)
		s.recordFailure("u:"+u.Username, "ip:"+ip)
		s.log.Warn("two-factor code rejected", "user", u.Username, "ip", ip)
		writeErr(w, http.StatusUnauthorized, "bad_code", "wrong two-factor code")
		return nil
	}
	s.dropChallenge(req.Challenge)
	return u
}

// handleMFA finishes an API login: POST /api/v1/mfa {challenge, code, token_name}.
func (s *Server) handleMFA(w http.ResponseWriter, r *http.Request) {
	var req mfaRequest
	if !readJSON(w, r, &req) {
		return
	}
	if u := s.completeChallenge(w, r, req); u != nil {
		s.issueLoginToken(w, u, req.TokenName, http.StatusOK, true)
	}
}

// handleWebMFA finishes a browser login: POST /api/v1/web/mfa {challenge, code}.
func (s *Server) handleWebMFA(w http.ResponseWriter, r *http.Request) {
	var req mfaRequest
	if !readJSON(w, r, &req) {
		return
	}
	if u := s.completeChallenge(w, r, req); u != nil {
		s.startSession(w, r, u, http.StatusOK, true)
	}
}

// MFASetup is what the enrollment page shows.
type MFASetup struct {
	Secret string `json:"secret"` // base32, for typing into an authenticator
	URL    string `json:"url"`    // otpauth:// link
	QR     string `json:"qr"`     // data: URI of a PNG QR code of URL
}

// handleWebMFASetup starts enrollment for an "enroll" challenge:
// POST /api/v1/web/mfa/setup {challenge} → MFASetup. Calling it again returns
// the same pending secret.
func (s *Server) handleWebMFASetup(w http.ResponseWriter, r *http.Request) {
	var req mfaRequest
	if !readJSON(w, r, &req) {
		return
	}
	c := s.challenge(req.Challenge, mfaPurposeEnroll)
	if c == nil {
		writeErr(w, http.StatusUnauthorized, "challenge_expired", "start the login again")
		return
	}
	u, err := s.store.User(c.userID)
	if err != nil || u.Disabled || !u.Admin || u.MFAEnrolled() {
		s.dropChallenge(req.Challenge)
		writeErr(w, http.StatusUnauthorized, "challenge_expired", "start the login again")
		return
	}
	s.mfaMu.Lock()
	if c.secret == nil {
		c.secret, err = totp.NewSecret()
	}
	secret := c.secret
	s.mfaMu.Unlock()
	if err != nil {
		s.internal(w, err)
		return
	}
	u2 := totp.URL(mfaIssuer, u.Username, secret)
	png, err := qrcode.Encode(u2, qrcode.Medium, 220)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, MFASetup{Secret: totp.Encode(secret), URL: u2, QR: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)})
}

// handleWebMFAConfirm enrolls the pending secret once the client proves it:
// POST /api/v1/web/mfa/confirm {challenge, code} → session.
func (s *Server) handleWebMFAConfirm(w http.ResponseWriter, r *http.Request) {
	var req mfaRequest
	if !readJSON(w, r, &req) {
		return
	}
	c := s.challenge(req.Challenge, mfaPurposeEnroll)
	if c == nil || c.secret == nil {
		writeErr(w, http.StatusUnauthorized, "challenge_expired", "start the login again")
		return
	}
	u, err := s.store.User(c.userID)
	if err != nil || u.Disabled || !u.Admin || u.MFAEnrolled() {
		s.dropChallenge(req.Challenge)
		writeErr(w, http.StatusUnauthorized, "challenge_expired", "start the login again")
		return
	}
	ip := s.clientIP(r)
	if s.throttled("u:"+u.Username) || s.throttled("ip:"+ip) {
		writeErr(w, http.StatusTooManyRequests, "throttled", "too many failed logins; try again in a few minutes")
		return
	}
	step, ok := totp.Verify(c.secret, req.Code, s.cfg.Now(), 0)
	if !ok {
		s.failChallenge(req.Challenge)
		s.recordFailure("u:"+u.Username, "ip:"+ip)
		writeErr(w, http.StatusUnauthorized, "bad_code", "wrong code; scan the QR code again and enter the current one")
		return
	}
	sealed, err := s.seal(c.secret)
	if err != nil {
		s.internal(w, err)
		return
	}
	if err := s.store.SetMFASecret(u.ID, sealed); err != nil {
		s.accountErr(w, err)
		return
	}
	if err := s.store.SetMFAStep(u.ID, step); err != nil && !errors.Is(err, accounts.ErrNotFound) {
		s.internal(w, err)
		return
	}
	s.dropChallenge(req.Challenge)
	s.log.Info("two-factor enrolled", "user", u.Username, "ip", ip)
	u.MFAEnrolledAt = s.cfg.Now().UnixNano()
	s.startSession(w, r, u, http.StatusOK, true)
}

// handleAdminResetMFA removes a user's two-factor secret and revokes their
// tokens: POST /api/v1/admin/users/{name}/mfa/reset. An admin enrolls again
// at the next login (recovery when the authenticator is lost).
func (s *Server) handleAdminResetMFA(w http.ResponseWriter, r *http.Request, cu ctxUser) {
	u := s.userByPath(w, r)
	if u == nil {
		return
	}
	if err := s.store.ClearMFA(u.ID); err != nil {
		s.accountErr(w, err)
		return
	}
	revoked, err := s.store.RevokeAllTokens(u.ID)
	if err != nil {
		s.internal(w, err)
		return
	}
	s.log.Warn("two-factor reset by admin", "user", u.Username, "by", cu.user.Username, "tokens_revoked", revoked)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tokens_revoked": revoked})
}

// mfaCodeField is the JSON field API clients may send with their password.
func mfaCodeField(c credentials) string { return strings.TrimSpace(c.TOTP) }
