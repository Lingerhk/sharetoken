// Package totp implements RFC 6238 time-based one-time passwords (SHA-1,
// six digits, 30-second steps), the flavour every authenticator app speaks.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	Digits = 6
	Period = 30 * time.Second
	// SecretSize is the length of a fresh secret: 160 bits, as RFC 4226 recommends.
	SecretSize = 20
	// Skew is how many steps either side of now a code is still accepted.
	Skew = 1
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewSecret returns a random secret.
func NewSecret() ([]byte, error) {
	s := make([]byte, SecretSize)
	if _, err := rand.Read(s); err != nil {
		return nil, err
	}
	return s, nil
}

// Encode renders a secret the way authenticator apps accept it when typed in.
func Encode(secret []byte) string { return b32.EncodeToString(secret) }

// Decode parses an Encode'd secret (case and spaces ignored).
func Decode(s string) ([]byte, error) {
	s = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(s), " ", ""))
	return b32.DecodeString(strings.TrimRight(s, "="))
}

// Step is the counter for time t.
func Step(t time.Time) int64 { return t.Unix() / int64(Period/time.Second) }

// CodeAt is the code for counter step.
func CodeAt(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, secret)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	n := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", Digits, n%1_000_000)
}

// Code is the code valid at time t.
func Code(secret []byte, t time.Time) string { return CodeAt(secret, Step(t)) }

// Verify checks code against the steps within Skew of now and returns the
// matching step. Steps at or before lastStep are refused, so a code can be
// used once only; callers persist the returned step.
func Verify(secret []byte, code string, now time.Time, lastStep int64) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != Digits {
		return 0, false
	}
	cur := Step(now)
	var matched int64
	ok := false
	// Check every candidate so timing does not reveal which step matched.
	for d := int64(-Skew); d <= Skew; d++ {
		step := cur + d
		if subtle.ConstantTimeCompare([]byte(CodeAt(secret, step)), []byte(code)) == 1 && step > lastStep {
			matched, ok = step, true
		}
	}
	return matched, ok
}

// URL is the otpauth:// link authenticator apps import (also what the QR
// code encodes).
func URL(issuer, account string, secret []byte) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{"secret": {Encode(secret)}, "issuer": {issuer}, "algorithm": {"SHA1"},
		"digits": {fmt.Sprint(Digits)}, "period": {fmt.Sprint(int(Period / time.Second))}}
	return "otpauth://totp/" + label + "?" + q.Encode()
}
