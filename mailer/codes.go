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
	// A newer code replaces older ones.
	if _, err := s.DB.Exec(`UPDATE mail_codes SET consumed = 1 WHERE email = ? AND purpose = ? AND consumed = 0`, r.Email, r.Purpose); err != nil {
		return err
	}
	exp := now.Add(CodeTTL)
	if _, err := s.DB.Exec(`INSERT INTO mail_codes (purpose, email, ip, salt, code_hash, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.Purpose, r.Email, r.IP, salt, hash, exp.Unix(), now.Unix()); err != nil {
		return err
	}
	m := verifyMail(r.Email, code)
	m.Sensitive = true
	m.ExpiresAt = exp
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

var resetHTML = template.Must(template.New("reset").Parse(`<!doctype html>
<html lang="en"><body style="margin:0;padding:24px;background:#f5f6f8;font-family:-apple-system,'Segoe UI',Roboto,'PingFang SC','Microsoft YaHei',sans-serif;color:#1f2933">
<div style="max-width:480px;margin:0 auto;background:#fff;border-radius:8px;padding:32px">
<h1 style="font-size:18px;margin:0 0 16px">Reset your ShareToken password</h1>
<p style="margin:0 0 16px">Someone (possibly you) asked to reset the password of the ShareToken account with this email address.</p>
<p style="margin:0 0 16px"><a href="{{.Link}}" style="display:inline-block;padding:10px 18px;background:#0d9488;color:#fff;border-radius:6px;text-decoration:none">Choose a new password</a></p>
<p style="margin:0 0 16px;word-break:break-all;color:#52606d">{{.Link}}</p>
<p style="margin:0 0 8px;color:#52606d">The link works once and is valid for {{.Minutes}} minutes. Do not share it with anyone. ShareToken staff will never ask you for it.</p>
<p style="margin:0;color:#52606d">If this wasn't you, you can ignore this email. Your password has not changed.</p>
</div></body></html>`))

// ResetMail mails a password-reset link. Like a verification code it is as good
// as a password while it lives: administrators do not get to read it.
func ResetMail(to, link string, ttl time.Duration, now time.Time) Message {
	minutes := int(ttl / time.Minute)
	var html strings.Builder
	_ = resetHTML.Execute(&html, map[string]any{"Link": link, "Minutes": minutes})
	return Message{
		Kind:      KindVerify,
		Sensitive: true,
		ExpiresAt: now.Add(ttl),
		To:        to,
		Subject:   "Reset your ShareToken password",
		Text: fmt.Sprintf("Someone (possibly you) asked to reset the password of the ShareToken account with this email address. To choose a new password, open:\r\n\r\n    %s\r\n\r\nThe link works once and is valid for %d minutes. Do not share it with anyone. ShareToken staff will never ask you for it.\r\nIf this wasn't you, you can ignore this email. Your password has not changed.\r\n",
			link, minutes),
		HTML: html.String(),
	}
}

var passwordChangedHTML = template.Must(template.New("password-changed").Parse(`<!doctype html>
<html lang="en"><body style="margin:0;padding:24px;background:#f5f6f8;font-family:-apple-system,'Segoe UI',Roboto,'PingFang SC','Microsoft YaHei',sans-serif;color:#1f2933">
<div style="max-width:480px;margin:0 auto;background:#fff;border-radius:8px;padding:32px">
<h1 style="font-size:18px;margin:0 0 16px">Your ShareToken password was changed</h1>
<p style="margin:0 0 16px">{{.Lead}}</p>
<table style="border-collapse:collapse;margin:0 0 16px;font-size:14px">
<tr><td style="padding:4px 16px 4px 0;color:#52606d">Account</td><td style="padding:4px 0;word-break:break-all">{{.Account}}</td></tr>
<tr><td style="padding:4px 16px 4px 0;color:#52606d">Time</td><td style="padding:4px 0">{{.Time}}</td></tr>
{{if .IP}}<tr><td style="padding:4px 16px 4px 0;color:#52606d">IP address</td><td style="padding:4px 0">{{.IP}}</td></tr>{{end}}
</table>
<p style="margin:0 0 8px;color:#52606d">If this was you, there is nothing more to do.</p>
<p style="margin:0;color:#52606d">If this wasn't you, {{if .Support}}contact <a href="mailto:{{.Support}}" style="color:#0d9488">{{.Support}}</a> immediately{{else}}contact your ShareToken administrator immediately{{end}}. ShareToken staff will never ask you for your password.</p>
</div></body></html>`))

// PasswordChangedMail tells an account its password changed: in the settings
// (viaLink false) or with a reset link from this mailbox (viaLink true, which
// also signed every device out). ip may be empty, support too.
func PasswordChangedMail(to string, viaLink bool, when time.Time, ip, support string) Message {
	lead := "The password of your ShareToken account was just changed."
	if viaLink {
		lead = "The password of your ShareToken account was just reset with a link sent to this address. Every device and browser was signed out."
	}
	at := when.UTC().Format("2006-01-02 15:04 UTC")
	var html strings.Builder
	_ = passwordChangedHTML.Execute(&html, map[string]any{"Lead": lead, "Account": to, "Time": at, "IP": ip, "Support": support})
	var text strings.Builder
	fmt.Fprintf(&text, "%s\r\n\r\n    Account: %s\r\n    Time: %s\r\n", lead, to, at)
	if ip != "" {
		fmt.Fprintf(&text, "    IP address: %s\r\n", ip)
	}
	text.WriteString("\r\nIf this was you, there is nothing more to do.\r\n")
	if support != "" {
		fmt.Fprintf(&text, "If this wasn't you, contact %s immediately. ShareToken staff will never ask you for your password.\r\n", support)
	} else {
		text.WriteString("If this wasn't you, contact your ShareToken administrator immediately. ShareToken staff will never ask you for your password.\r\n")
	}
	return Message{Kind: KindNotice, To: to, Subject: "Your ShareToken password was changed", Text: text.String(), HTML: html.String()}
}
