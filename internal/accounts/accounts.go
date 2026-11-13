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
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"sharetoken/internal/canon"
	"sharetoken/internal/ledger"
	"sharetoken/internal/protocol"
)

// NoPasswords is the migration that followed Schema: people sign in with a
// mailed code, and nobody has a password.
const NoPasswords = `ALTER TABLE users DROP COLUMN pass_hash;`

// Schema creates the shared user-management tables.
const Schema = `
CREATE TABLE IF NOT EXISTS users (
	id         BIGINT PRIMARY KEY AUTO_INCREMENT,
	username   VARCHAR(191) NOT NULL UNIQUE,
	pass_hash  TEXT NOT NULL,
	is_admin   BIGINT NOT NULL DEFAULT 0,
	disabled   BIGINT NOT NULL DEFAULT 0,
	created_at BIGINT NOT NULL,
	mfa_secret      VARBINARY(255) NOT NULL DEFAULT '',
	mfa_enrolled_at BIGINT NOT NULL DEFAULT 0,
	mfa_last_step   BIGINT NOT NULL DEFAULT 0
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS api_tokens (
	id           BIGINT PRIMARY KEY AUTO_INCREMENT,
	user_id      BIGINT NOT NULL,
	name         VARCHAR(255) NOT NULL,
	hash         VARBINARY(64) NOT NULL UNIQUE,
	prefix       VARCHAR(64) NOT NULL,
	created_at   BIGINT NOT NULL,
	last_used_at BIGINT NOT NULL DEFAULT 0,
	expires_at   BIGINT NOT NULL DEFAULT 0,
	revoked      BIGINT NOT NULL DEFAULT 0,
	mfa          BIGINT NOT NULL DEFAULT 0,
	KEY api_tokens_by_user (user_id),
	FOREIGN KEY (user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS devices (
	peer         VARCHAR(191) PRIMARY KEY,
	user_id      BIGINT NOT NULL,
	name         VARCHAR(255) NOT NULL,
	created_at   BIGINT NOT NULL,
	last_seen_at BIGINT NOT NULL DEFAULT 0,
	client_version VARCHAR(64) NOT NULL DEFAULT '',
	KEY devices_by_user (user_id),
	FOREIGN KEY (user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS relays (
	id             BIGINT PRIMARY KEY AUTO_INCREMENT,
	addr           VARCHAR(2048) CHARACTER SET ascii COLLATE ascii_bin NOT NULL UNIQUE,
	name           VARCHAR(255) NOT NULL DEFAULT '',
	region         VARCHAR(255) NOT NULL DEFAULT '',
	enabled        BIGINT NOT NULL DEFAULT 1,
	coordinator_ok BIGINT NOT NULL DEFAULT 0,
	checked_at     BIGINT NOT NULL DEFAULT 0,
	detail         TEXT NOT NULL DEFAULT (''),
	rtt_ms         BIGINT NOT NULL DEFAULT 0,
	version        VARCHAR(64) NOT NULL DEFAULT ''
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS feedback (
	id          BIGINT PRIMARY KEY AUTO_INCREMENT,
	user_id     BIGINT NOT NULL,
	category    VARCHAR(16) NOT NULL,
	message     TEXT NOT NULL,
	source      VARCHAR(16) NOT NULL,
	app_version VARCHAR(64) NOT NULL DEFAULT '',
	os          VARCHAR(64) NOT NULL DEFAULT '',
	lang        VARCHAR(16) NOT NULL DEFAULT '',
	status      VARCHAR(16) NOT NULL,
	created_at  BIGINT NOT NULL,
	handled_at  BIGINT NOT NULL DEFAULT 0,
	handled_by  VARCHAR(255) NOT NULL DEFAULT '',
	KEY feedback_by_user (user_id, id),
	KEY feedback_by_status (status, id),
	FOREIGN KEY (user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
`

// Errors.
var (
	ErrUnknownDevice  = errors.New("accounts: device is not bound to any user")
	ErrDisabled       = errors.New("accounts: user is disabled")
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

// ---- names and tokens ----

var usernameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{2,31}$`)

// CheckUsername validates an account name: an email address (what every
// account the portal creates uses: people sign in with a code mailed there), or
// a plain username of 3-32 of [a-z0-9_.-], starting alphanumeric (accounts made
// directly in the store, such as tests').
func CheckUsername(u string) error {
	if strings.Contains(u, "@") {
		return CheckEmail(u)
	}
	if !usernameRE.MatchString(u) {
		return fmt.Errorf("%w: username must be 3-32 lowercase letters, digits, '_', '.' or '-'", ErrInvalid)
	}
	return nil
}

var emailRE = regexp.MustCompile(`^[a-z0-9_%+-]+(\.[a-z0-9_%+-]+)*@[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// MaxEmailLen is the longest accepted email address: what fits the username
// column on MySQL (VARCHAR(191)).
const MaxEmailLen = 191

// NormalizeEmail is the form an email address is stored and compared in.
func NormalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

// CheckEmail validates a normalized email address. It covers the addresses
// people have, not all of RFC 5321: internationalized domains must be in
// punycode. Whether the mailbox exists is a question for email verification.
func CheckEmail(e string) error {
	if len(e) > MaxEmailLen || !emailRE.MatchString(e) {
		return fmt.Errorf("%w: not a valid email address", ErrInvalid)
	}
	return nil
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
