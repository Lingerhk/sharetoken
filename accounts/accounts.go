// Package accounts holds the user-management state that the portal and the
// coordinator share through one database: users, their API tokens, the
// devices (libp2p peer IDs) bound to them, and the registry of relays.
//
// The portal writes it (sign-up, login, device binding, admin actions); the
// coordinator reads it to map the peer ID on an authenticated libp2p stream to
// the user account that pays and earns, and writes back relay health.
//
// A device is bound only with proof that its owner holds the device key: the
// device signs (username, peer ID, timestamp). Without that, anyone could bind
// someone else's node and collect its earnings.
package accounts

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"

	"sharetoken/internal/canon"
	"sharetoken/ledger"
	"sharetoken/protocol"
)

// Schema creates the shared user-management tables. It is idempotent.
const Schema = `
CREATE TABLE IF NOT EXISTS users (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	username   TEXT NOT NULL UNIQUE,
	pass_hash  TEXT NOT NULL,
	is_admin   INTEGER NOT NULL DEFAULT 0,
	disabled   INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	mfa_secret      BLOB NOT NULL DEFAULT x'',   -- TOTP secret, sealed by the portal; empty: not enrolled
	mfa_enrolled_at INTEGER NOT NULL DEFAULT 0,
	mfa_last_step   INTEGER NOT NULL DEFAULT 0   -- last accepted TOTP step (replay protection)
) STRICT;
CREATE TABLE IF NOT EXISTS api_tokens (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id      INTEGER NOT NULL REFERENCES users(id),
	name         TEXT NOT NULL,
	hash         BLOB NOT NULL UNIQUE,
	prefix       TEXT NOT NULL,
	created_at   INTEGER NOT NULL,
	last_used_at INTEGER NOT NULL DEFAULT 0,
	expires_at   INTEGER NOT NULL DEFAULT 0,
	revoked      INTEGER NOT NULL DEFAULT 0,
	mfa          INTEGER NOT NULL DEFAULT 0      -- issued after two-factor verification
) STRICT;
CREATE INDEX IF NOT EXISTS api_tokens_by_user ON api_tokens(user_id);
CREATE TABLE IF NOT EXISTS devices (
	peer         TEXT PRIMARY KEY,
	user_id      INTEGER NOT NULL REFERENCES users(id),
	name         TEXT NOT NULL,
	created_at   INTEGER NOT NULL,
	last_seen_at INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE INDEX IF NOT EXISTS devices_by_user ON devices(user_id);
CREATE TABLE IF NOT EXISTS relays (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	addr           TEXT NOT NULL UNIQUE,
	name           TEXT NOT NULL DEFAULT '',
	region         TEXT NOT NULL DEFAULT '',
	enabled        INTEGER NOT NULL DEFAULT 1,
	coordinator_ok INTEGER NOT NULL DEFAULT 0,
	checked_at     INTEGER NOT NULL DEFAULT 0,
	detail         TEXT NOT NULL DEFAULT '',
	rtt_ms         INTEGER NOT NULL DEFAULT 0
) STRICT;
`

// Errors.
var (
	ErrUnknownDevice  = errors.New("accounts: device is not bound to any user")
	ErrDisabled       = errors.New("accounts: user is disabled")
	ErrBadCredentials = errors.New("accounts: wrong username or password")
	ErrBadToken       = errors.New("accounts: invalid, expired or revoked token")
	ErrExists         = errors.New("accounts: already exists")
	ErrNotFound       = errors.New("accounts: not found")
	ErrBadSignature   = errors.New("accounts: device signature does not verify")
	ErrStale          = errors.New("accounts: device signature is too old or from the future")
	ErrBoundElsewhere = errors.New("accounts: device is bound to another user")
	ErrInvalid        = errors.New("accounts: invalid input")
)

// Querier is satisfied by *sql.DB and *sql.Tx.
type Querier interface {
	QueryRow(query string, args ...any) *sql.Row
}

// DeviceAccount maps a peer ID to the ledger account of the user it is bound
// to. It returns ErrUnknownDevice for unbound peers and ErrDisabled when the
// user is disabled.
func DeviceAccount(q Querier, peer string) (account string, userID int64, err error) {
	var disabled int
	err = q.QueryRow(`SELECT d.user_id, u.disabled FROM devices d JOIN users u ON u.id = d.user_id WHERE d.peer = ?`, peer).
		Scan(&userID, &disabled)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", 0, ErrUnknownDevice
	case err != nil:
		return "", 0, err
	case disabled != 0:
		return "", userID, ErrDisabled
	}
	return ledger.UserAccount(userID), userID, nil
}

// ---- passwords and tokens ----

var usernameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{2,31}$`)

// CheckUsername validates a username: 3-32 of [a-z0-9_.-], starting alphanumeric.
func CheckUsername(u string) error {
	if !usernameRE.MatchString(u) {
		return fmt.Errorf("%w: username must be 3-32 lowercase letters, digits, '_', '.' or '-'", ErrInvalid)
	}
	return nil
}

// CheckPassword enforces a minimum length.
func CheckPassword(p string) error {
	if len(p) < 8 || len(p) > 256 {
		return fmt.Errorf("%w: password must be 8-256 characters", ErrInvalid)
	}
	return nil
}

// argon2id parameters (OWASP minimum: 19 MiB, 2 passes, 1 lane), small enough
// for a 2 GB server handling concurrent logins.
const (
	argonTime    = 2
	argonMemory  = 19 * 1024
	argonThreads = 1
	argonKeyLen  = 32
)

// HashPassword returns an encoded argon2id hash.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("argon2id$%d$%d$%d$%s$%s", argonTime, argonMemory, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword checks pw against an encoded hash in constant time.
func VerifyPassword(encoded, pw string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "argon2id" {
		return false
	}
	t, err1 := strconv.ParseUint(parts[1], 10, 32)
	m, err2 := strconv.ParseUint(parts[2], 10, 32)
	p, err3 := strconv.ParseUint(parts[3], 10, 8)
	salt, err4 := base64.RawStdEncoding.DecodeString(parts[4])
	want, err5 := base64.RawStdEncoding.DecodeString(parts[5])
	if err := errors.Join(err1, err2, err3, err4, err5); err != nil || len(want) == 0 || m > 1<<20 || t > 16 {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, uint32(t), uint32(m), uint8(p), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// TokenPrefix marks sharetoken API tokens.
const TokenPrefix = "st_"

// NewToken returns a fresh bearer token and the hash that is stored for it.
func NewToken() (plain string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	plain = TokenPrefix + hex.EncodeToString(b)
	return plain, HashToken(plain), nil
}

// HashToken is how a token is stored and looked up: tokens are random, so a
// plain SHA-256 suffices.
func HashToken(plain string) []byte {
	h := sha256.Sum256([]byte(plain))
	return h[:]
}

// ---- device binding ----

// BindWindow bounds the age of a device-binding signature.
const BindWindow = 5 * time.Minute

// BindMessage is what a device signs to be bound to username.
func BindMessage(username, peer string, unixNano int64) []byte {
	return canon.New("sharetoken/device-bind/v1").String(username).String(peer).I64(unixNano).Sum()
}

// SignBind signs a binding of the device with key priv (peer ID peer) to username.
func SignBind(priv ed25519.PrivateKey, username, peer string, unixNano int64) []byte {
	return ed25519.Sign(priv, BindMessage(username, peer, unixNano))
}

// VerifyBind checks a device-binding signature and its freshness.
func VerifyBind(username, peer string, unixNano int64, sig []byte, now time.Time) error {
	if d := now.Sub(time.Unix(0, unixNano)); d > BindWindow || d < -BindWindow {
		return ErrStale
	}
	pub, err := protocol.PubKeyFromPeerID(peer)
	if err != nil {
		return fmt.Errorf("%w: bad peer id", ErrInvalid)
	}
	if !ed25519.Verify(pub, BindMessage(username, peer, unixNano), sig) {
		return ErrBadSignature
	}
	return nil
}
