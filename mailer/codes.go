package mailer

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"math/big"
	"strings"
	"time"
)

// Verification-code limits. Everything is counted in the database, so limits
// hold across portal restarts. A code guesser gets maxCodeAttempts tries on one
// code and can only get a new code at maxCodesPerEmail an hour: about 25 guesses
// an hour against a million possibilities.
const (
	CodeTTL          = 10 * time.Minute
	CodeCooldown     = 60 * time.Second
	maxCodeAttempts  = 5
	maxCodesPerEmail = 5   // per hour
	maxCodesPerIP    = 20  // per hour
	maxCodesPerHour  = 500 // all together: a floor under what an abuser can make this IP send
	PurposeRegister  = "register"
)

// Errors of code requests.
var (
	ErrCodeCooldown = errors.New("mailer: a code was just sent to this address")
	ErrCodeLimit    = errors.New("mailer: too many codes requested")
	ErrBadCode      = errors.New("mailer: wrong or expired code")
)

// CodeRequest asks for a verification code to be mailed.
type CodeRequest struct {
	Purpose string
	Email   string // normalized
	IP      string
	From    string
	ReplyTo string
	// Taken says the address already has an account: nothing can be redeemed,
	// and the mail says so. The caller answers the same either way, so the
	// response does not tell strangers who has registered.
	Taken bool
}

func newCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func hashCode(salt []byte, purpose, email, code string) []byte {
	h := sha256.New()
	h.Write(salt)
	for _, part := range []string{purpose, email, code} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return h.Sum(nil)
}

// RequestCode rate-limits, records and mails a verification code.
func (s *Store) RequestCode(r CodeRequest) error {
	now := s.now()
	// Old rows only serve the limits above.
	if _, err := s.DB.Exec(`DELETE FROM mail_codes WHERE created_at < ?`, now.Add(-48*time.Hour).Unix()); err != nil {
		return err
	}
	hourAgo := now.Add(-time.Hour).Unix()
	var last sql.NullInt64
	var forEmail, forIP, total int
	if err := s.DB.QueryRow(`SELECT MAX(created_at), COUNT(*) FROM mail_codes WHERE email = ? AND created_at >= ?`, r.Email, hourAgo).Scan(&last, &forEmail); err != nil {
		return err
	}
	if last.Valid && now.Sub(time.Unix(last.Int64, 0)) < CodeCooldown {
		return ErrCodeCooldown
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM mail_codes WHERE ip = ? AND created_at >= ?`, r.IP, hourAgo).Scan(&forIP); err != nil {
		return err
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM mail_codes WHERE created_at >= ?`, hourAgo).Scan(&total); err != nil {
		return err
	}
	if forEmail >= maxCodesPerEmail || forIP >= maxCodesPerIP || total >= maxCodesPerHour {
		return ErrCodeLimit
	}
	code, err := newCode()
	if err != nil {
		return err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	hash := hashCode(salt, r.Purpose, r.Email, code)
	if r.Taken {
		// A row is still written so that the limits apply, but no code can match it.
		hash = make([]byte, sha256.Size)
	}
	// A newer code replaces older ones.
	if _, err := s.DB.Exec(`UPDATE mail_codes SET consumed = 1 WHERE email = ? AND purpose = ? AND consumed = 0`, r.Email, r.Purpose); err != nil {
		return err
	}
	exp := now.Add(CodeTTL)
	if _, err := s.DB.Exec(`INSERT INTO mail_codes (purpose, email, ip, salt, code_hash, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.Purpose, r.Email, r.IP, salt, hash, exp.Unix(), now.Unix()); err != nil {
		return err
	}
	var m Message
	if r.Taken {
		m = accountExistsMail(r.Email)
	} else {
		m = verifyMail(r.Email, code)
		m.Sensitive = true
		m.ExpiresAt = exp
	}
	m.From, m.ReplyTo = r.From, r.ReplyTo
	_, err = s.Enqueue(m)
	return err
}

// VerifyCode redeems a code. It succeeds at most once per code, and each wrong
// guess counts against the newest code of the address.
func (s *Store) VerifyCode(purpose, email, code string) error {
	code = strings.TrimSpace(code)
	var id, attempts, exp int64
	var salt, want []byte
	err := s.DB.QueryRow(`SELECT id, attempts, expires_at, salt, code_hash FROM mail_codes
		WHERE email = ? AND purpose = ? AND consumed = 0 ORDER BY id DESC LIMIT 1`, email, purpose).Scan(&id, &attempts, &exp, &salt, &want)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrBadCode
	}
	if err != nil {
		return err
	}
	if s.now().Unix() > exp || attempts >= maxCodeAttempts {
		return ErrBadCode
	}
	if subtle.ConstantTimeCompare(hashCode(salt, purpose, email, code), want) != 1 {
		if _, err := s.DB.Exec(`UPDATE mail_codes SET attempts = attempts + 1 WHERE id = ?`, id); err != nil {
			return err
		}
		return ErrBadCode
	}
	res, err := s.DB.Exec(`UPDATE mail_codes SET consumed = 1 WHERE id = ? AND consumed = 0 AND attempts < ?`, id, maxCodeAttempts)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrBadCode // a parallel request redeemed it first
	}
	return nil
}

// ---- messages ----

var verifyHTML = template.Must(template.New("verify").Parse(`<!doctype html>
<html lang="en"><body style="margin:0;padding:24px;background:#f5f6f8;font-family:-apple-system,'Segoe UI',Roboto,'PingFang SC','Microsoft YaHei',sans-serif;color:#1f2933">
<div style="max-width:480px;margin:0 auto;background:#fff;border-radius:8px;padding:32px">
<h1 style="font-size:18px;margin:0 0 16px">Your ShareToken verification code</h1>
<p style="margin:0 0 16px">You are signing up for ShareToken with this email address. Your verification code is:</p>
<p style="font-size:32px;font-weight:600;letter-spacing:6px;margin:0 0 16px">{{.Code}}</p>
<p style="margin:0 0 8px;color:#52606d">The code is valid for {{.Minutes}} minutes. Do not share it with anyone. ShareToken staff will never ask you for it.</p>
<p style="margin:0;color:#52606d">If this wasn't you, you can ignore this email.</p>
</div></body></html>`))

func verifyMail(email, code string) Message {
	minutes := int(CodeTTL / time.Minute)
	var html strings.Builder
	_ = verifyHTML.Execute(&html, map[string]any{"Code": code, "Minutes": minutes})
	return Message{
		Kind:    KindVerify,
		To:      email,
		Subject: "Your ShareToken verification code: " + code,
		Text: fmt.Sprintf("You are signing up for ShareToken with this email address. Your verification code is:\r\n\r\n    %s\r\n\r\nThe code is valid for %d minutes. Do not share it with anyone. ShareToken staff will never ask you for it.\r\nIf this wasn't you, you can ignore this email.\r\n",
			code, minutes),
		HTML: html.String(),
	}
}

func accountExistsMail(email string) Message {
	return Message{
		Kind:    KindNotice,
		To:      email,
		Subject: "ShareToken: this email is already registered",
		Text:    "Someone (possibly you) tried to sign up for ShareToken with this email address, but it already has an account. Please log in instead.\r\nIf this wasn't you, you can ignore this email. Your account has not changed.\r\n",
	}
}

// NoticeMail is a short plain-text notification to a user.
func NoticeMail(to, subject, text string) Message {
	return Message{Kind: KindNotice, To: to, Subject: subject, Text: text}
}
