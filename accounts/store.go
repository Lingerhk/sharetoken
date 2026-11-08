package accounts

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"sharetoken/internal/sqlite"
	"sharetoken/p2p"
)

// User is one account holder.
type User struct {
	ID            int64  `json:"id"`
	Username      string `json:"username"`
	Admin         bool   `json:"is_admin"`
	Disabled      bool   `json:"disabled"`
	CreatedAt     int64  `json:"created_at"`                // unix nanos
	MFAEnrolledAt int64  `json:"mfa_enrolled_at,omitempty"` // unix nanos; 0: no two-factor secret enrolled
}

// MFAEnrolled reports whether the user has a two-factor secret.
func (u *User) MFAEnrolled() bool { return u.MFAEnrolledAt != 0 }

// Token describes an API token (never its secret).
type Token struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Prefix     string `json:"prefix"` // first characters, to tell tokens apart
	CreatedAt  int64  `json:"created_at"`
	LastUsedAt int64  `json:"last_used_at,omitempty"`
	ExpiresAt  int64  `json:"expires_at,omitempty"`
	MFA        bool   `json:"mfa"` // issued after two-factor verification (admin endpoints require it)
}

// Device is a peer ID bound to a user.
type Device struct {
	Peer       string `json:"peer"`
	Name       string `json:"name"`
	CreatedAt  int64  `json:"created_at"`
	LastSeenAt int64  `json:"last_seen_at,omitempty"`
}

// Relay is a registered relay.
type Relay struct {
	ID            int64  `json:"id"`
	Addr          string `json:"addr"` // full multiaddr ending in /p2p/<relay peer id>; IPv4 and IPv6 ones joined by commas
	Name          string `json:"name,omitempty"`
	Region        string `json:"region,omitempty"`
	Enabled       bool   `json:"enabled"`
	CoordinatorOK bool   `json:"coordinator_ok"` // the coordinator holds a reservation on it
	CheckedAt     int64  `json:"checked_at,omitempty"`
	Detail        string `json:"detail,omitempty"`
	RTTMillis     int64  `json:"rtt_ms,omitempty"` // coordinator → relay round trip, 0 when unknown
}

// Store reads and writes the user-management tables.
type Store struct {
	DB  *sql.DB
	Now func() time.Time

	touched sync.Map // token id → unix nanos of the last last_used_at write attempt
}

// touchWait bounds the last_used_at write: while another process holds the
// write lock a read request must not stall for the whole busy timeout.
const touchWait = 250 * time.Millisecond

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Store) tx(fn func(tx *sql.Tx) error) error {
	tx, err := sqlite.Begin(s.DB)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

const userColumns = `id, username, is_admin, disabled, created_at, mfa_enrolled_at`

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	var admin, disabled int
	if err := row.Scan(&u.ID, &u.Username, &admin, &disabled, &u.CreatedAt, &u.MFAEnrolledAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	u.Admin, u.Disabled = admin != 0, disabled != 0
	return &u, nil
}

// CreateUser registers a user.
func (s *Store) CreateUser(username, password string, admin bool) (*User, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if err := CheckUsername(username); err != nil {
		return nil, err
	}
	if err := CheckPassword(password); err != nil {
		return nil, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	var u *User
	err = s.tx(func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM users WHERE username = ?`, username).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("%w: username %q is taken", ErrExists, username)
		}
		res, err := tx.Exec(`INSERT INTO users (username, pass_hash, is_admin, created_at) VALUES (?, ?, ?, ?)`,
			username, hash, boolInt(admin), s.now().UnixNano())
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		u, err = scanUser(tx.QueryRow(`SELECT `+userColumns+` FROM users WHERE id = ?`, id))
		return err
	})
	return u, err
}

// Authenticate checks a username and password.
func (s *Store) Authenticate(username, password string) (*User, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	var hash string
	var id int64
	err := s.DB.QueryRow(`SELECT id, pass_hash FROM users WHERE username = ?`, username).Scan(&id, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		// Spend the same time as a real check, so timing does not reveal
		// which usernames exist.
		VerifyPassword(dummyHash, password)
		return nil, ErrBadCredentials
	}
	if err != nil {
		return nil, err
	}
	if !VerifyPassword(hash, password) {
		return nil, ErrBadCredentials
	}
	u, err := s.User(id)
	if err != nil {
		return nil, err
	}
	if u.Disabled {
		return nil, ErrDisabled
	}
	return u, nil
}

var dummyHash, _ = HashPassword("sharetoken-dummy-password")

// SetPassword replaces a user's password.
func (s *Store) SetPassword(userID int64, password string) error {
	if err := CheckPassword(password); err != nil {
		return err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	return s.tx(func(tx *sql.Tx) error {
		return mustAffect(tx.Exec(`UPDATE users SET pass_hash = ? WHERE id = ?`, hash, userID))
	})
}

// User loads a user by ID.
func (s *Store) User(id int64) (*User, error) {
	return scanUser(s.DB.QueryRow(`SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

// UserByName loads a user by username.
func (s *Store) UserByName(username string) (*User, error) {
	return scanUser(s.DB.QueryRow(`SELECT `+userColumns+` FROM users WHERE username = ?`, strings.ToLower(strings.TrimSpace(username))))
}

// Users lists users, oldest first.
func (s *Store) Users(limit, offset int) ([]User, error) {
	rows, err := s.DB.Query(`SELECT `+userColumns+` FROM users ORDER BY id LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// SetDisabled disables or re-enables a user. A disabled user cannot log in,
// its tokens stop working, and the coordinator refuses its devices.
func (s *Store) SetDisabled(userID int64, disabled bool) error {
	return s.tx(func(tx *sql.Tx) error {
		return mustAffect(tx.Exec(`UPDATE users SET disabled = ? WHERE id = ?`, boolInt(disabled), userID))
	})
}

// SetAdmin grants or revokes admin rights.
func (s *Store) SetAdmin(userID int64, admin bool) error {
	return s.tx(func(tx *sql.Tx) error {
		return mustAffect(tx.Exec(`UPDATE users SET is_admin = ? WHERE id = ?`, boolInt(admin), userID))
	})
}

// IssueToken creates an API token for a user; ttl 0 never expires. The
// plain token is returned once and never stored.
func (s *Store) IssueToken(userID int64, name string, ttl time.Duration) (string, *Token, error) {
	return s.IssueSessionToken(userID, name, ttl, false)
}

// IssueSessionToken is IssueToken with the token's two-factor flag: true
// when the holder proved a second factor in this login. Admin endpoints
// accept only such tokens.
func (s *Store) IssueSessionToken(userID int64, name string, ttl time.Duration, mfa bool) (string, *Token, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return "", nil, fmt.Errorf("%w: token name must be 1-64 characters", ErrInvalid)
	}
	plain, hash, err := NewToken()
	if err != nil {
		return "", nil, err
	}
	now := s.now()
	t := &Token{Name: name, Prefix: plain[:len(TokenPrefix)+6], CreatedAt: now.UnixNano(), MFA: mfa}
	if ttl > 0 {
		t.ExpiresAt = now.Add(ttl).UnixNano()
	}
	err = s.tx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`INSERT INTO api_tokens (user_id, name, hash, prefix, created_at, expires_at, mfa) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			userID, t.Name, hash, t.Prefix, t.CreatedAt, t.ExpiresAt, boolInt(mfa))
		if err != nil {
			return err
		}
		t.ID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return "", nil, err
	}
	return plain, t, nil
}

// UserByToken resolves a bearer token to its active user, the token's ID and
// whether the token was issued after two-factor verification.
func (s *Store) UserByToken(plain string) (*User, int64, bool, error) {
	if !strings.HasPrefix(plain, TokenPrefix) {
		return nil, 0, false, ErrBadToken
	}
	hash := HashToken(plain)
	var (
		tokenID, userID, expires, lastUsed int64
		revoked, mfa                       int
		stored                             []byte
	)
	err := s.DB.QueryRow(`SELECT id, user_id, hash, expires_at, revoked, last_used_at, mfa FROM api_tokens WHERE hash = ?`, hash).
		Scan(&tokenID, &userID, &stored, &expires, &revoked, &lastUsed, &mfa)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, false, ErrBadToken
	}
	if err != nil {
		return nil, 0, false, err
	}
	now := s.now()
	if !bytes.Equal(stored, hash) || revoked != 0 || (expires != 0 && expires <= now.UnixNano()) {
		return nil, 0, false, ErrBadToken
	}
	u, err := s.User(userID)
	if err != nil {
		return nil, 0, false, err
	}
	if u.Disabled {
		return nil, 0, false, ErrDisabled
	}
	// Record use at most once a minute: a busy client must not turn every
	// read into a write. The attempt is remembered even when it fails, so a
	// locked database costs each token at most touchWait per minute.
	if now.UnixNano()-lastUsed > int64(time.Minute) {
		if last, ok := s.touched.Load(tokenID); !ok || now.UnixNano()-last.(int64) > int64(time.Minute) {
			s.touched.Store(tokenID, now.UnixNano())
			ctx, cancel := context.WithTimeout(context.Background(), touchWait)
			if tx, err := s.DB.BeginTx(ctx, nil); err == nil {
				if _, err := tx.Exec(`UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, now.UnixNano(), tokenID); err == nil {
					_ = tx.Commit()
				} else {
					_ = tx.Rollback()
				}
			}
			cancel()
		}
	}
	return u, tokenID, mfa != 0, nil
}

// Tokens lists a user's live tokens.
func (s *Store) Tokens(userID int64) ([]Token, error) {
	rows, err := s.DB.Query(`SELECT id, name, prefix, created_at, last_used_at, expires_at, mfa FROM api_tokens
		WHERE user_id = ? AND revoked = 0 AND (expires_at = 0 OR expires_at > ?) ORDER BY id`, userID, s.now().UnixNano())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Token{}
	for rows.Next() {
		var t Token
		var mfa int
		if err := rows.Scan(&t.ID, &t.Name, &t.Prefix, &t.CreatedAt, &t.LastUsedAt, &t.ExpiresAt, &mfa); err != nil {
			return nil, err
		}
		t.MFA = mfa != 0
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeToken revokes one of a user's tokens.
func (s *Store) RevokeToken(userID, tokenID int64) error {
	return s.tx(func(tx *sql.Tx) error {
		return mustAffect(tx.Exec(`UPDATE api_tokens SET revoked = 1 WHERE id = ? AND user_id = ? AND revoked = 0`, tokenID, userID))
	})
}

// BindDevice binds peer to u after checking the device's signature over
// (username, peer, unixNano). Re-binding a device to the same user renames it.
func (s *Store) BindDevice(u *User, peer, name string, unixNano int64, sig []byte) (*Device, error) {
	if err := VerifyBind(u.Username, peer, unixNano, sig, s.now()); err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	if len(name) > 64 {
		name = name[:64]
	}
	if name == "" {
		name = "device"
	}
	now := s.now().UnixNano()
	var d *Device
	err := s.tx(func(tx *sql.Tx) error {
		var owner int64
		switch err := tx.QueryRow(`SELECT user_id FROM devices WHERE peer = ?`, peer).Scan(&owner); {
		case errors.Is(err, sql.ErrNoRows):
			if _, err := tx.Exec(`INSERT INTO devices (peer, user_id, name, created_at) VALUES (?, ?, ?, ?)`, peer, u.ID, name, now); err != nil {
				return err
			}
		case err != nil:
			return err
		case owner != u.ID:
			return ErrBoundElsewhere
		default:
			if _, err := tx.Exec(`UPDATE devices SET name = ? WHERE peer = ?`, name, peer); err != nil {
				return err
			}
		}
		d = &Device{}
		return tx.QueryRow(`SELECT peer, name, created_at, last_seen_at FROM devices WHERE peer = ?`, peer).
			Scan(&d.Peer, &d.Name, &d.CreatedAt, &d.LastSeenAt)
	})
	return d, err
}

// Devices lists a user's devices.
func (s *Store) Devices(userID int64) ([]Device, error) {
	rows, err := s.DB.Query(`SELECT peer, name, created_at, last_seen_at FROM devices WHERE user_id = ? ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.Peer, &d.Name, &d.CreatedAt, &d.LastSeenAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// UnbindDevice removes one of a user's devices; the coordinator then refuses it.
func (s *Store) UnbindDevice(userID int64, peer string) error {
	return s.tx(func(tx *sql.Tx) error {
		return mustAffect(tx.Exec(`DELETE FROM devices WHERE peer = ? AND user_id = ?`, peer, userID))
	})
}

// TouchDevice records that a device was seen, at most once per interval.
func TouchDevice(tx *sql.Tx, peer string, now time.Time, interval time.Duration) error {
	_, err := tx.Exec(`UPDATE devices SET last_seen_at = ? WHERE peer = ? AND last_seen_at < ?`,
		now.UnixNano(), peer, now.Add(-interval).UnixNano())
	return err
}

// ---- relays ----

// AddRelay registers a relay by its full multiaddr.
func (s *Store) AddRelay(addr, name, region string) (*Relay, error) {
	ai, err := p2p.ParseRelay(addr)
	if err != nil {
		return nil, fmt.Errorf("%w: %v (want …/p2p/<peer id>, or the relay's IPv4 and IPv6 addresses joined by a comma)", ErrInvalid, err)
	}
	addr = p2p.RelayAddr(ai) // canonical, so the same relay is not registered twice
	name, region = strings.TrimSpace(name), strings.TrimSpace(region)
	if len(name) > 64 || len(region) > 64 {
		return nil, fmt.Errorf("%w: name and region must be at most 64 characters", ErrInvalid)
	}
	var r *Relay
	err = s.tx(func(tx *sql.Tx) error {
		var n int
		// One entry per relay (peer ID), however its addresses are written.
		if err := tx.QueryRow(`SELECT COUNT(*) FROM relays WHERE addr LIKE ?`, "%/p2p/"+ai.ID.String()+"%").Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("%w: relay %s is already registered (remove it first to change its addresses)", ErrExists, ai.ID)
		}
		res, err := tx.Exec(`INSERT INTO relays (addr, name, region) VALUES (?, ?, ?)`, addr, name, region)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		r, err = scanRelay(tx.QueryRow(`SELECT `+relayColumns+` FROM relays WHERE id = ?`, id))
		return err
	})
	return r, err
}

const relayColumns = `id, addr, name, region, enabled, coordinator_ok, checked_at, detail, rtt_ms`

func scanRelay(row interface{ Scan(...any) error }) (*Relay, error) {
	var r Relay
	var enabled, ok int
	if err := row.Scan(&r.ID, &r.Addr, &r.Name, &r.Region, &enabled, &ok, &r.CheckedAt, &r.Detail, &r.RTTMillis); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.Enabled, r.CoordinatorOK = enabled != 0, ok != 0
	return &r, nil
}

// Relays lists relays; onlyEnabled drops disabled ones.
func Relays(q interface {
	Query(string, ...any) (*sql.Rows, error)
}, onlyEnabled bool) ([]Relay, error) {
	query := `SELECT ` + relayColumns + ` FROM relays`
	if onlyEnabled {
		query += ` WHERE enabled = 1`
	}
	rows, err := q.Query(query + ` ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Relay{}
	for rows.Next() {
		r, err := scanRelay(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// Relays lists relays.
func (s *Store) Relays(onlyEnabled bool) ([]Relay, error) { return Relays(s.DB, onlyEnabled) }

// SetRelayEnabled enables or disables a relay.
func (s *Store) SetRelayEnabled(id int64, enabled bool) error {
	return s.tx(func(tx *sql.Tx) error {
		return mustAffect(tx.Exec(`UPDATE relays SET enabled = ? WHERE id = ?`, boolInt(enabled), id))
	})
}

// RemoveRelay deletes a relay.
func (s *Store) RemoveRelay(id int64) error {
	return s.tx(func(tx *sql.Tx) error { return mustAffect(tx.Exec(`DELETE FROM relays WHERE id = ?`, id)) })
}

// SetRelayStatus records whether the coordinator holds a reservation on the
// relay at addr and its latest round trip to it in milliseconds (0: not
// measured). The coordinator calls this.
func SetRelayStatus(tx *sql.Tx, addr string, ok bool, detail string, rttMillis int64, at time.Time) error {
	if len(detail) > 256 {
		detail = detail[:256]
	}
	_, err := tx.Exec(`UPDATE relays SET coordinator_ok = ?, detail = ?, checked_at = ?, rtt_ms = ? WHERE addr = ?`,
		boolInt(ok), detail, at.UnixNano(), max(rttMillis, 0), addr)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func mustAffect(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- additions for the web portal ----

// UserCount is the number of users whose name contains q ("" for all).
func (s *Store) UserCount(q string) (int64, error) {
	var n int64
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM users WHERE LOWER(username) LIKE ? ESCAPE '!'`, likePattern(q)).Scan(&n)
	return n, err
}

// UserRow is a user with the account summary operators list.
type UserRow struct {
	User
	AvailableMicros int64 `json:"available_micros"`
	HeldMicros      int64 `json:"held_micros"`
	Devices         int64 `json:"devices"`
	Tokens          int64 `json:"tokens"`
}

// SearchUsers lists users whose name contains q ("" for all), newest first.
func (s *Store) SearchUsers(q string, limit, offset int) ([]UserRow, error) {
	rows, err := s.DB.Query(`SELECT u.id, u.username, u.is_admin, u.disabled, u.created_at,
		COALESCE((SELECT balance FROM ledger_accounts WHERE account = CONCAT('user:', u.id)), 0),
		COALESCE((SELECT SUM(amount) FROM ledger_holds WHERE owner = CONCAT('user:', u.id) AND open = 1), 0),
		(SELECT COUNT(*) FROM devices WHERE user_id = u.id),
		(SELECT COUNT(*) FROM api_tokens WHERE user_id = u.id AND revoked = 0 AND (expires_at = 0 OR expires_at > ?))
		FROM users u WHERE LOWER(u.username) LIKE ? ESCAPE '!' ORDER BY u.id DESC LIMIT ? OFFSET ?`,
		s.now().UnixNano(), likePattern(q), limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserRow{}
	for rows.Next() {
		var r UserRow
		var admin, disabled int
		if err := rows.Scan(&r.ID, &r.Username, &admin, &disabled, &r.CreatedAt, &r.AvailableMicros, &r.HeldMicros, &r.Devices, &r.Tokens); err != nil {
			return nil, err
		}
		r.Admin, r.Disabled = admin != 0, disabled != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// likePattern turns a search string into a LIKE pattern, escaping wildcards
// so users cannot search with them. The escape character is '!' (a backslash
// is itself an escape in MySQL string literals); matching is case-insensitive
// on both databases (LOWER on both sides).
func likePattern(q string) string {
	q = strings.ToLower(strings.TrimSpace(q))
	r := strings.NewReplacer(`!`, `!!`, `%`, `!%`, `_`, `!_`)
	return "%" + r.Replace(q) + "%"
}

// RevokeAllTokens revokes every live token of a user (a compromised account,
// or a password reset). It returns how many were revoked.
func (s *Store) RevokeAllTokens(userID int64) (int64, error) {
	var n int64
	err := s.tx(func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE api_tokens SET revoked = 1 WHERE user_id = ? AND revoked = 0`, userID)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	return n, err
}

// RenameDevice renames one of a user's devices.
func (s *Store) RenameDevice(userID int64, peer, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return fmt.Errorf("%w: device name must be 1-64 characters", ErrInvalid)
	}
	return s.tx(func(tx *sql.Tx) error {
		return mustAffect(tx.Exec(`UPDATE devices SET name = ? WHERE peer = ? AND user_id = ?`, name, peer, userID))
	})
}

// UpdateRelay changes a relay's name and region.
func (s *Store) UpdateRelay(id int64, name, region string) error {
	name, region = strings.TrimSpace(name), strings.TrimSpace(region)
	if len(name) > 64 || len(region) > 64 {
		return fmt.Errorf("%w: name and region must be at most 64 characters", ErrInvalid)
	}
	return s.tx(func(tx *sql.Tx) error {
		return mustAffect(tx.Exec(`UPDATE relays SET name = ?, region = ? WHERE id = ?`, name, region, id))
	})
}

// ---- two-factor secrets ----

// MFASecret returns the user's sealed TOTP secret (empty when not enrolled)
// and the last TOTP step accepted for them.
func (s *Store) MFASecret(userID int64) (sealed []byte, lastStep int64, err error) {
	err = s.DB.QueryRow(`SELECT mfa_secret, mfa_last_step FROM users WHERE id = ?`, userID).Scan(&sealed, &lastStep)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, ErrNotFound
	}
	return sealed, lastStep, err
}

// SetMFASecret enrolls a sealed TOTP secret for the user, resetting the
// replay counter.
func (s *Store) SetMFASecret(userID int64, sealed []byte) error {
	if len(sealed) == 0 {
		return fmt.Errorf("%w: empty secret", ErrInvalid)
	}
	return s.tx(func(tx *sql.Tx) error {
		return mustAffect(tx.Exec(`UPDATE users SET mfa_secret = ?, mfa_enrolled_at = ?, mfa_last_step = 0 WHERE id = ?`,
			sealed, s.now().UnixNano(), userID))
	})
}

// ClearMFA removes the user's two-factor secret; an admin must enroll again
// at the next login.
func (s *Store) ClearMFA(userID int64) error {
	return s.tx(func(tx *sql.Tx) error {
		return mustAffect(tx.Exec(`UPDATE users SET mfa_secret = x'', mfa_enrolled_at = 0, mfa_last_step = 0 WHERE id = ?`, userID))
	})
}

// SetMFAStep records the TOTP step just accepted, so the same code is not
// accepted twice.
func (s *Store) SetMFAStep(userID, step int64) error {
	return s.tx(func(tx *sql.Tx) error {
		return mustAffect(tx.Exec(`UPDATE users SET mfa_last_step = ? WHERE id = ? AND mfa_last_step < ?`, step, userID, step))
	})
}
