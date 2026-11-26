package mailer

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"math/big"
	"strings"
	"time"
	"unicode"

	"sharetoken/internal/mysqldb"
)

// Verification-code limits. Everything is counted in the database, so limits
// hold across portal restarts, and counting and storing a code are one
// transaction, so requests sent together cannot all pass a limit. A code
// guesser gets maxCodeAttempts tries on one code and can only get a new code
// at maxCodesPerEmail an hour: about 25 guesses an hour against a million
// possibilities.
//
// The portal as a whole mails at most MaxKnownCodesPerHour codes to addresses
// that have an account and MaxNewCodesPerHour to addresses that have none.
// They are two budgets because the second is the one anyone can use up, with
// made-up addresses: people who have an account still get their codes then.
const (
	CodeTTL              = 10 * time.Minute
	CodeCooldown         = 60 * time.Second
	maxCodeAttempts      = 5
	maxCodesPerEmail     = 5       // per hour
	MaxCodesPerIP        = 20      // per hour; the portal passes an IPv6 address as its /64
	MaxKnownCodesPerHour = 500     // all together, to addresses with an account
	MaxNewCodesPerHour   = 300     // all together, to addresses without one
	PurposeLogin         = "login" // signing in; an address without an account gets one
)

// Errors of code requests.
var (
	ErrCodeCooldown = errors.New("mailer: a code was just sent to this address")
	ErrCodeLimit    = errors.New("mailer: too many codes requested")
	// The two budgets over everyone; both are an ErrCodeLimit.
	ErrKnownCodeBudget = fmt.Errorf("%w (all addresses with an account together)", ErrCodeLimit)
	ErrNewCodeBudget   = fmt.Errorf("%w (all addresses without an account together)", ErrCodeLimit)
	ErrBadCode         = errors.New("mailer: wrong or expired code")
)

// CodeRequest asks for a verification code to be mailed.
type CodeRequest struct {
	Purpose string
	Email   string // normalized
	IP      string // what the per-address limit counts: the portal's rateKey
	Known   bool   // the address has an account: which budget over everyone it counts against
	// NoCode: the mail says that the address's account is disabled, and
	// carries no code. Everything else, limits included, is as for any request,
	// so the answer does not tell a disabled account from a working one.
	NoCode  bool
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

// newOperatorCode is the code IssueCode hands out. It is bound to nobody's
// request and gets no cap on wrong guesses (a cap would let anyone use it up),
// so it is long enough not to be guessed at all.
func newOperatorCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%012d", n.Int64()), nil
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

func hashNonce(nonce string) []byte {
	h := sha256.Sum256([]byte(nonce))
	return h[:]
}

// OperatorIP marks codes an operator issued (IssueCode): they were not asked
// for from any address and do not count against the limits on asking.
const OperatorIP = "operator"

// RequestCode rate-limits, records and mails a verification code. It returns
// the nonce the code is bound to: only who asked has it, and VerifyCode wants
// it back with the code. An address can have several codes waiting, each with
// its own nonce and its own count of wrong guesses, so nobody else's requests
// or guesses touch the code of who asked first.
//
// Counting and storing happen in one transaction under the database's write
// lock (mysqldb.Begin): of requests sent together, each sees the ones before it.
func (s *Store) RequestCode(r CodeRequest) (nonce string, err error) {
	now := s.now()
	// Old rows only serve the limits above.
	if _, err := s.DB.Exec(`DELETE FROM mail_codes WHERE created_at < ?`, now.Add(-48*time.Hour).Unix()); err != nil {
		return "", err
	}
	code, err := newCode()
	if err != nil {
		return "", err
	}
	random := make([]byte, 32) // the salt and the nonce
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	salt := random[:16]
	nonce = base64.RawURLEncoding.EncodeToString(random[16:])
	exp := now.Add(CodeTTL)
	m := verifyMail(r.Email, code)
	if r.NoCode {
		m = disabledMail(r.Email, r.ReplyTo)
	}
	m.Sensitive = !r.NoCode
	m.ExpiresAt = exp
	m.From, m.ReplyTo = r.From, r.ReplyTo

	tx, err := mysqldb.Begin(s.DB)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var last, forEmail, forIP, forKind int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(CASE WHEN email = ? THEN created_at END), 0), COALESCE(SUM(email = ?), 0),
		COALESCE(SUM(ip = ?), 0), COALESCE(SUM(known = ?), 0) FROM mail_codes WHERE created_at >= ? AND ip <> ?`,
		r.Email, r.Email, r.IP, boolInt(r.Known), now.Add(-time.Hour).Unix(), OperatorIP).Scan(&last, &forEmail, &forIP, &forKind); err != nil {
		return "", err
	}
	switch {
	case last > 0 && now.Sub(time.Unix(last, 0)) < CodeCooldown:
		return "", ErrCodeCooldown
	case forEmail >= maxCodesPerEmail || forIP >= MaxCodesPerIP:
		return "", ErrCodeLimit
	case r.Known && forKind >= MaxKnownCodesPerHour:
		return "", ErrKnownCodeBudget
	case !r.Known && forKind >= MaxNewCodesPerHour:
		return "", ErrNewCodeBudget
	}
	if _, err := tx.Exec(`INSERT INTO mail_codes (purpose, email, ip, salt, code_hash, nonce_hash, known, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Purpose, r.Email, r.IP, salt, hashCode(salt, r.Purpose, r.Email, code), hashNonce(nonce), boolInt(r.Known), exp.Unix(), now.Unix()); err != nil {
		return "", err
	}
	// With the code, or not at all: a code nobody is mailed would only use up the address's hour.
	if _, err := enqueue(tx, now, m); err != nil {
		return "", err
	}
	return nonce, tx.Commit()
}

// IssueCode records a code and returns it instead of mailing it: an operator
// hands it to the account holder (or uses it) on the server's command line.
// It is longer than a mailed code, redeems without a nonce, and replaces the
// address's earlier operator codes; mailed codes stay as they are.
func (s *Store) IssueCode(purpose, email string) (code string, expires time.Time, err error) {
	now := s.now()
	if code, err = newOperatorCode(); err != nil {
		return "", time.Time{}, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", time.Time{}, err
	}
	if _, err := s.DB.Exec(`UPDATE mail_codes SET consumed = 1 WHERE email = ? AND purpose = ? AND ip = ? AND consumed = 0`, email, purpose, OperatorIP); err != nil {
		return "", time.Time{}, err
	}
	exp := now.Add(CodeTTL)
	if _, err := s.DB.Exec(`INSERT INTO mail_codes (purpose, email, ip, salt, code_hash, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		purpose, email, OperatorIP, salt, hashCode(salt, purpose, email, code), exp.Unix(), now.Unix()); err != nil {
		return "", time.Time{}, err
	}
	return code, exp, nil
}

// cleanCode drops what people add when they type a code: spaces and dashes.
func cleanCode(code string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || unicode.IsSpace(r) {
			return -1
		}
		return r
	}, code)
}

// VerifyCode redeems a code: the one mailed for the request that returned
// nonce, or a code an operator issued for the address (those need no nonce).
// It succeeds at most once per code. A guess is counted against the code of
// its nonce before it is compared, so one code is tested at most
// maxCodeAttempts times however the guesses arrive; a guess without that
// code's nonce counts against nothing of the address.
func (s *Store) VerifyCode(purpose, email, nonce, code string) error {
	code = cleanCode(code)
	now := s.now().Unix()
	redeem := func(id int64) error {
		res, err := s.DB.Exec(`UPDATE mail_codes SET consumed = 1 WHERE id = ? AND consumed = 0`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrBadCode // a parallel request redeemed it first
		}
		return nil
	}
	var id int64
	var salt, want []byte
	if nonce != "" {
		err := s.DB.QueryRow(`SELECT id, salt, code_hash FROM mail_codes WHERE email = ? AND purpose = ? AND nonce_hash = ? AND consumed = 0 AND expires_at >= ?
			ORDER BY id DESC LIMIT 1`, email, purpose, hashNonce(nonce), now).Scan(&id, &salt, &want)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return err
		default:
			res, err := s.DB.Exec(`UPDATE mail_codes SET attempts = attempts + 1 WHERE id = ? AND consumed = 0 AND attempts < ?`, id, maxCodeAttempts)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 1 && subtle.ConstantTimeCompare(hashCode(salt, purpose, email, code), want) == 1 {
				return redeem(id)
			}
		}
	}
	err := s.DB.QueryRow(`SELECT id, salt, code_hash FROM mail_codes WHERE email = ? AND purpose = ? AND ip = ? AND consumed = 0 AND expires_at >= ?
		ORDER BY id DESC LIMIT 1`, email, purpose, OperatorIP, now).Scan(&id, &salt, &want)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrBadCode
	case err != nil:
		return err
	}
	if subtle.ConstantTimeCompare(hashCode(salt, purpose, email, code), want) != 1 {
		return ErrBadCode
	}
	return redeem(id)
}

// ---- messages ----

var verifyHTML = template.Must(template.New("verify").Parse(`<!doctype html>
<html lang="en"><body style="margin:0;padding:24px;background:#f5f6f8;font-family:-apple-system,'Segoe UI',Roboto,'PingFang SC','Microsoft YaHei',sans-serif;color:#1f2933">
<div style="max-width:480px;margin:0 auto;background:#fff;border-radius:8px;padding:32px">
<h1 style="font-size:18px;margin:0 0 16px">Your ShareToken sign-in code</h1>
<p style="margin:0 0 16px">Use this code to sign in to ShareToken. If this address has no account yet, signing in creates one.</p>
<p style="font-size:32px;font-weight:600;letter-spacing:6px;margin:0 0 16px">{{.Code}}</p>
<p style="margin:0 0 8px;color:#52606d">The code is valid for {{.Minutes}} minutes. Do not share it with anyone. ShareToken staff will never ask you for it.</p>
<p style="margin:0;color:#52606d">If this wasn't you, you can ignore this email.</p>
</div></body></html>`))

func verifyMail(email, code string) Message {
	minutes := int(CodeTTL / time.Minute)
	var html strings.Builder
	_ = verifyHTML.Execute(&html, map[string]any{"Code": code, "Minutes": minutes})
	return Message{
		Kind: KindVerify,
		To:   email,
		// No code here: subjects show on lock screens and in the provider's logs.
		Subject: "Your ShareToken sign-in code",
		Text: fmt.Sprintf("Use this code to sign in to ShareToken. If this address has no account yet, signing in creates one.\r\n\r\n    %s\r\n\r\nThe code is valid for %d minutes. Do not share it with anyone. ShareToken staff will never ask you for it.\r\nIf this wasn't you, you can ignore this email.\r\n",
			code, minutes),
		HTML: html.String(),
	}
}

// disabledMail is what a request for a code gets when the address's account is
// disabled: the holder learns why no code comes, and nobody else learns
// anything from the portal's answer.
func disabledMail(email, support string) Message {
	contact := "contact your ShareToken administrator"
	if support != "" {
		contact = "write to " + support
	}
	return Message{
		Kind:    KindNotice,
		To:      email,
		Subject: "Your ShareToken account is disabled",
		Text: fmt.Sprintf("A sign-in code was asked for this address, but its ShareToken account is disabled, so no code was sent.\r\n\r\n"+
			"If you think this is a mistake, %s.\r\nIf this wasn't you, you can ignore this email.\r\n", contact),
	}
}

var mfaChangedHTML = template.Must(template.New("mfa-changed").Parse(`<!doctype html>
<html lang="en"><body style="margin:0;padding:24px;background:#f5f6f8;font-family:-apple-system,'Segoe UI',Roboto,'PingFang SC','Microsoft YaHei',sans-serif;color:#1f2933">
<div style="max-width:480px;margin:0 auto;background:#fff;border-radius:8px;padding:32px">
<h1 style="font-size:18px;margin:0 0 16px">{{.Title}}</h1>
<p style="margin:0 0 16px">{{.Lead}}</p>
<table style="border-collapse:collapse;margin:0 0 16px;font-size:14px">
<tr><td style="padding:4px 16px 4px 0;color:#52606d">Account</td><td style="padding:4px 0;word-break:break-all">{{.Account}}</td></tr>
<tr><td style="padding:4px 16px 4px 0;color:#52606d">Time</td><td style="padding:4px 0">{{.Time}}</td></tr>
{{if .IP}}<tr><td style="padding:4px 16px 4px 0;color:#52606d">IP address</td><td style="padding:4px 0">{{.IP}}</td></tr>{{end}}
</table>
<p style="margin:0 0 8px;color:#52606d">If this was you, there is nothing more to do.</p>
<p style="margin:0;color:#52606d">If this wasn't you, {{if .Support}}contact <a href="mailto:{{.Support}}" style="color:#0d9488">{{.Support}}</a> immediately{{else}}contact your ShareToken administrator immediately{{end}}. ShareToken staff will never ask you for a sign-in code.</p>
</div></body></html>`))

// MFAChangedMail tells an account that two-factor authentication was turned
// on or off for it. ip may be empty, support too.
func MFAChangedMail(to string, enabled bool, when time.Time, ip, support string) Message {
	title, lead := "Two-factor authentication is on", "Two-factor authentication was just turned on for your ShareToken account. Signing in now also asks for a code from your authenticator app."
	if !enabled {
		title, lead = "Two-factor authentication is off", "Two-factor authentication was just turned off for your ShareToken account. Signing in now only needs the code mailed to this address."
	}
	at := when.UTC().Format("2006-01-02 15:04 UTC")
	var html strings.Builder
	_ = mfaChangedHTML.Execute(&html, map[string]any{"Title": title, "Lead": lead, "Account": to, "Time": at, "IP": ip, "Support": support})
	var text strings.Builder
	fmt.Fprintf(&text, "%s\r\n\r\n    Account: %s\r\n    Time: %s\r\n", lead, to, at)
	if ip != "" {
		fmt.Fprintf(&text, "    IP address: %s\r\n", ip)
	}
	text.WriteString("\r\nIf this was you, there is nothing more to do.\r\n")
	if support != "" {
		fmt.Fprintf(&text, "If this wasn't you, contact %s immediately. ShareToken staff will never ask you for a sign-in code.\r\n", support)
	} else {
		text.WriteString("If this wasn't you, contact your ShareToken administrator immediately. ShareToken staff will never ask you for a sign-in code.\r\n")
	}
	return Message{Kind: KindNotice, To: to, Subject: "ShareToken: " + title, Text: text.String(), HTML: html.String()}
}
