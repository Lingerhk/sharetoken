package accounts

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Share partners: two users who pair up use each other's nodes for free.
//
// This is the second way to share, next to buying and selling (pairing.go is
// part of that one: it only narrows who buys from whom, and every job is still
// paid). One user makes a share invite, the other joins with it, and from then
// on each of them is matched to the other's nodes first, and those jobs move
// no credits: no hold, no charge, no platform fee. When no partner node can
// take a request, the request goes to the market as usual (and is paid),
// unless the user turned on "partners only".
//
// Partners are kept as share groups, so that a pool of more than two members
// can be added later without changing the tables: today every group is made by
// one invite and has two members (MaxShareGroupMembers), and an invite may
// already name an existing group to join (share_invites.group_id) once groups
// may grow. Everyone a user shares a group with is its partner.
//
// Each user limits what it gives (share_settings): how many tokens its nodes
// serve its partners in a rolling 24 hours, and what part of each node's
// concurrent slots partner jobs may take; the coordinator applies both when it
// matches. The limit covers all of a user's partners together.

// SharingSchema holds share groups, their members, invites and each user's
// sharing settings. (Jobs served between partners are marked jobs.shared, in
// the coordinator's schema.)
const SharingSchema = `
CREATE TABLE IF NOT EXISTS share_groups (
	id          BIGINT PRIMARY KEY AUTO_INCREMENT,
	max_members BIGINT NOT NULL,
	created_at  BIGINT NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS share_members (
	group_id  BIGINT NOT NULL,
	user_id   BIGINT NOT NULL,
	joined_at BIGINT NOT NULL,
	PRIMARY KEY (group_id, user_id),
	KEY share_members_by_user (user_id),
	FOREIGN KEY (group_id) REFERENCES share_groups(id),
	FOREIGN KEY (user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS share_invites (
	id         BIGINT PRIMARY KEY AUTO_INCREMENT,
	inviter_id BIGINT NOT NULL,
	group_id   BIGINT NOT NULL DEFAULT 0,
	code       VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL UNIQUE,
	created_at BIGINT NOT NULL,
	expires_at BIGINT NOT NULL,
	used_by    BIGINT NOT NULL DEFAULT 0,
	used_at    BIGINT NOT NULL DEFAULT 0,
	revoked    BIGINT NOT NULL DEFAULT 0,
	KEY share_invites_by_inviter (inviter_id),
	FOREIGN KEY (inviter_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS share_settings (
	user_id            BIGINT PRIMARY KEY,
	only_partners      BIGINT NOT NULL DEFAULT 0,
	daily_limit_tokens BIGINT NOT NULL DEFAULT 0,
	share_pct          BIGINT NOT NULL DEFAULT 100,
	updated_at         BIGINT NOT NULL,
	FOREIGN KEY (user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
`

// Limits on sharing.
const (
	MaxShareGroupMembers = 2                  // members of a group made by an invite
	MaxSharePartners     = 5                  // partners one user may have
	MaxShareInvites      = 10                 // live invites one user may hold
	DefaultShareTTL      = 24 * time.Hour     // lifetime of an invite when none is asked for
	MaxShareTTL          = 7 * 24 * time.Hour // longest lifetime of an invite
	MaxShareDailyTokens  = 1 << 40            // the largest daily limit one may set
	ShareWindow          = 24 * time.Hour     // the window the daily limit counts over
)

// ShareInvite is an invite a user made. Code is the secret: whoever holds it
// becomes the user's partner. It admits one person.
type ShareInvite struct {
	ID        int64  `json:"id"`
	Code      string `json:"code"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt int64  `json:"expires_at"` // unix nanoseconds
}

// SharePartner is someone a user shares a group with.
type SharePartner struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	GroupID  int64  `json:"group_id"`
	Since    int64  `json:"since"`
	Disabled bool   `json:"disabled,omitempty"`
}

// ShareSettings is what a user gives its partners and takes from them.
type ShareSettings struct {
	// OnlyPartners: requests never go to the market, only to partners' nodes.
	OnlyPartners bool `json:"only_partners"`
	// DailyLimitTokens caps the tokens (prompt + completion) this user's nodes
	// serve all its partners together in a rolling 24 hours; 0: no limit.
	DailyLimitTokens int64 `json:"daily_limit_tokens"`
	// SharePct is the part (0-100) of each of this user's nodes' concurrent
	// slots that partner jobs may take; 0 pauses sharing.
	SharePct int64 `json:"share_pct"`
}

// DefaultShareSettings is what a user that never changed them has: share
// everything, fall back to the market.
var DefaultShareSettings = ShareSettings{SharePct: 100}

// Validate checks a settings change.
func (s ShareSettings) Validate() error {
	switch {
	case s.DailyLimitTokens < 0 || s.DailyLimitTokens > MaxShareDailyTokens:
		return fmt.Errorf("%w: the daily limit must be between 0 (no limit) and %d tokens", ErrInvalid, int64(MaxShareDailyTokens))
	case s.SharePct < 0 || s.SharePct > 100:
		return fmt.Errorf("%w: the share must be between 0 and 100 percent", ErrInvalid)
	}
	return nil
}

// PartnerSlots is how many of a node's maxConcurrency slots partner jobs may
// take at sharePct percent: at least one unless sharing is paused.
func PartnerSlots(maxConcurrency uint32, sharePct int64) uint32 {
	if sharePct <= 0 {
		return 0
	}
	if sharePct >= 100 || maxConcurrency == 0 {
		return maxConcurrency
	}
	return max(1, uint32(int64(maxConcurrency)*sharePct/100))
}

const shareInviteLive = `revoked = 0 AND used_by = 0 AND expires_at > ?`

// CreateShareInvite makes a one-person invite for userID. ttl 0 means
// DefaultShareTTL.
func (s *Store) CreateShareInvite(userID int64, ttl time.Duration) (*ShareInvite, error) {
	if ttl == 0 {
		ttl = DefaultShareTTL
	}
	if ttl < minPairingTTL || ttl > MaxShareTTL {
		return nil, fmt.Errorf("%w: an invite lasts between %s and %d days", ErrInvalid, minPairingTTL, int(MaxShareTTL/(24*time.Hour)))
	}
	code, err := newPairingCode()
	if err != nil {
		return nil, err
	}
	now := s.now()
	inv := &ShareInvite{Code: code, CreatedAt: now.UnixNano(), ExpiresAt: now.Add(ttl).UnixNano()}
	err = s.tx(func(tx *sql.Tx) error {
		var live int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM share_invites WHERE inviter_id = ? AND `+shareInviteLive, userID, now.UnixNano()).Scan(&live); err != nil {
			return err
		}
		if live >= MaxShareInvites {
			return fmt.Errorf("%w: %d invites are open; revoke one first", ErrLimit, live)
		}
		n, err := countPartners(tx, userID)
		if err != nil {
			return err
		}
		if n >= MaxSharePartners {
			return fmt.Errorf("%w: you have %d share partners; remove one first", ErrLimit, n)
		}
		res, err := tx.Exec(`INSERT INTO share_invites (inviter_id, code, created_at, expires_at) VALUES (?, ?, ?, ?)`,
			userID, code, inv.CreatedAt, inv.ExpiresAt)
		if err != nil {
			return err
		}
		inv.ID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return nil, err
	}
	return inv, nil
}

// ShareInvites lists userID's open invites, newest first.
func (s *Store) ShareInvites(userID int64) ([]ShareInvite, error) {
	rows, err := s.DB.Query(`SELECT id, code, created_at, expires_at FROM share_invites WHERE inviter_id = ? AND `+shareInviteLive+` ORDER BY id DESC`,
		userID, s.now().UnixNano())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ShareInvite{}
	for rows.Next() {
		var inv ShareInvite
		if err := rows.Scan(&inv.ID, &inv.Code, &inv.CreatedAt, &inv.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

// RevokeShareInvite withdraws an open invite.
func (s *Store) RevokeShareInvite(userID, id int64) error {
	return s.tx(func(tx *sql.Tx) error {
		return mustAffect(tx.Exec(`UPDATE share_invites SET revoked = 1 WHERE id = ? AND inviter_id = ? AND revoked = 0 AND used_by = 0`, id, userID))
	})
}

func countPartners(q interface {
	QueryRow(query string, args ...any) *sql.Row
}, userID int64) (int, error) {
	var n int
	err := q.QueryRow(`SELECT COUNT(DISTINCT o.user_id) FROM share_members m JOIN share_members o ON o.group_id = m.group_id AND o.user_id != m.user_id
		WHERE m.user_id = ?`, userID).Scan(&n)
	return n, err
}

// JoinShare makes userID a partner of whoever made code and returns that
// user. It is ErrNotFound for a code that is wrong, expired, revoked or used,
// ErrOwnCode for the user's own invite and ErrLimit when either side has as
// many partners as it may (or the group named by the invite is full). Joining
// someone one already shares with changes nothing and leaves the invite open.
func (s *Store) JoinShare(userID int64, code string) (*SharePartner, error) {
	code = NormalizePairingCode(code)
	if code == "" {
		return nil, ErrNotFound
	}
	now := s.now().UnixNano()
	var out *SharePartner
	err := s.tx(func(tx *sql.Tx) error {
		var inviteID, inviterID, groupID int64
		err := tx.QueryRow(`SELECT id, inviter_id, group_id FROM share_invites WHERE code = ? AND `+shareInviteLive+` FOR UPDATE`, code, now).
			Scan(&inviteID, &inviterID, &groupID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if inviterID == userID {
			return ErrOwnCode
		}
		inviter, err := scanUser(tx.QueryRow(`SELECT `+userColumns+` FROM users WHERE id = ?`, inviterID))
		if err != nil {
			return err
		}
		if inviter.Disabled {
			return ErrNotFound
		}
		out = &SharePartner{ID: inviter.ID, Username: inviter.Username, Since: now}
		var g, since int64
		switch err := tx.QueryRow(`SELECT m.group_id, GREATEST(m.joined_at, o.joined_at) FROM share_members m
			JOIN share_members o ON o.group_id = m.group_id AND o.user_id = ? WHERE m.user_id = ? ORDER BY m.group_id LIMIT 1`, inviterID, userID).Scan(&g, &since); {
		case err == nil:
			out.GroupID, out.Since = g, since
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		for _, id := range []int64{userID, inviterID} {
			n, err := countPartners(tx, id)
			if err != nil {
				return err
			}
			if n >= MaxSharePartners {
				if id == userID {
					return fmt.Errorf("%w: you have %d share partners; remove one first", ErrLimit, n)
				}
				return fmt.Errorf("%w: the inviter has as many share partners as it may", ErrLimit)
			}
		}
		if groupID == 0 {
			res, err := tx.Exec(`INSERT INTO share_groups (max_members, created_at) VALUES (?, ?)`, MaxShareGroupMembers, now)
			if err != nil {
				return err
			}
			if groupID, err = res.LastInsertId(); err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO share_members (group_id, user_id, joined_at) VALUES (?, ?, ?)`, groupID, inviterID, now); err != nil {
				return err
			}
		} else {
			// An invite into an existing pool (not made by the API yet).
			var members, maxMembers int64
			if err := tx.QueryRow(`SELECT (SELECT COUNT(*) FROM share_members WHERE group_id = g.id), g.max_members FROM share_groups g WHERE g.id = ? FOR UPDATE`, groupID).
				Scan(&members, &maxMembers); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return ErrNotFound
				}
				return err
			}
			if members >= maxMembers {
				return fmt.Errorf("%w: this share group is full", ErrLimit)
			}
		}
		if _, err := tx.Exec(`INSERT INTO share_members (group_id, user_id, joined_at) VALUES (?, ?, ?)`, groupID, userID, now); err != nil {
			return err
		}
		out.GroupID = groupID
		_, err = tx.Exec(`UPDATE share_invites SET used_by = ?, used_at = ?, group_id = ? WHERE id = ?`, userID, now, groupID, inviteID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SharePartners lists everyone userID shares a group with, newest first.
func (s *Store) SharePartners(userID int64) ([]SharePartner, error) {
	rows, err := s.DB.Query(`SELECT u.id, u.username, u.disabled, MIN(m.group_id), MAX(GREATEST(m.joined_at, o.joined_at))
		FROM share_members m JOIN share_members o ON o.group_id = m.group_id AND o.user_id != m.user_id
		JOIN users u ON u.id = o.user_id WHERE m.user_id = ? GROUP BY u.id, u.username, u.disabled ORDER BY 5 DESC, u.id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SharePartner{}
	for rows.Next() {
		var p SharePartner
		var disabled int
		if err := rows.Scan(&p.ID, &p.Username, &disabled, &p.GroupID, &p.Since); err != nil {
			return nil, err
		}
		p.Disabled = disabled != 0
		out = append(out, p)
	}
	return out, rows.Err()
}

// LeaveSharePartner ends userID's sharing with partnerID: userID leaves every
// group the two share, and a group left with fewer than two members is
// dissolved.
func (s *Store) LeaveSharePartner(userID, partnerID int64) error {
	return s.tx(func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT m.group_id FROM share_members m JOIN share_members o ON o.group_id = m.group_id AND o.user_id = ?
			WHERE m.user_id = ?`, partnerID, userID)
		if err != nil {
			return err
		}
		var groups []int64
		for rows.Next() {
			var g int64
			if err := rows.Scan(&g); err != nil {
				rows.Close()
				return err
			}
			groups = append(groups, g)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(groups) == 0 {
			return ErrNotFound
		}
		for _, g := range groups {
			if _, err := tx.Exec(`DELETE FROM share_members WHERE group_id = ? AND user_id = ?`, g, userID); err != nil {
				return err
			}
			if err := dissolveSmallGroup(tx, g); err != nil {
				return err
			}
		}
		return nil
	})
}

func dissolveSmallGroup(tx *sql.Tx, g int64) error {
	var left int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM share_members WHERE group_id = ?`, g).Scan(&left); err != nil {
		return err
	}
	if left >= 2 {
		return nil
	}
	if _, err := tx.Exec(`DELETE FROM share_members WHERE group_id = ?`, g); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE share_invites SET revoked = 1 WHERE group_id = ? AND used_by = 0`, g); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM share_groups WHERE id = ?`, g)
	return err
}

// ShareSettingsOf reads userID's settings, the defaults when it never set any.
func ShareSettingsOf(q interface {
	QueryRow(query string, args ...any) *sql.Row
}, userID int64) (ShareSettings, error) {
	st := DefaultShareSettings
	var only int64
	err := q.QueryRow(`SELECT only_partners, daily_limit_tokens, share_pct FROM share_settings WHERE user_id = ?`, userID).
		Scan(&only, &st.DailyLimitTokens, &st.SharePct)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultShareSettings, nil
	}
	st.OnlyPartners = only != 0
	return st, err
}

// ShareSettings reads userID's settings.
func (s *Store) ShareSettings(userID int64) (ShareSettings, error) {
	return ShareSettingsOf(s.DB, userID)
}

// SetShareSettings replaces userID's settings.
func (s *Store) SetShareSettings(userID int64, st ShareSettings) error {
	if err := st.Validate(); err != nil {
		return err
	}
	only := int64(0)
	if st.OnlyPartners {
		only = 1
	}
	now := s.now().UnixNano()
	return s.tx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO share_settings (user_id, only_partners, daily_limit_tokens, share_pct, updated_at) VALUES (?, ?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE only_partners = VALUES(only_partners), daily_limit_tokens = VALUES(daily_limit_tokens),
			share_pct = VALUES(share_pct), updated_at = VALUES(updated_at)`, userID, only, st.DailyLimitTokens, st.SharePct, now)
		return err
	})
}

// PartnerGrant is what one partner gives the user asking: its settings, which
// bound the jobs the user may run on its nodes.
type PartnerGrant struct {
	UserID int64
	ShareSettings
}

// PartnerGrants returns userID's partners that are not disabled, with their
// settings, and userID's own settings.
func PartnerGrants(q interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}, userID int64) (own ShareSettings, grants []PartnerGrant, err error) {
	if own, err = ShareSettingsOf(q, userID); err != nil {
		return own, nil, err
	}
	rows, err := q.Query(`SELECT DISTINCT o.user_id, COALESCE(s.daily_limit_tokens, 0), COALESCE(s.share_pct, ?)
		FROM share_members m JOIN share_members o ON o.group_id = m.group_id AND o.user_id != m.user_id
		JOIN users u ON u.id = o.user_id AND u.disabled = 0
		LEFT JOIN share_settings s ON s.user_id = o.user_id WHERE m.user_id = ?`, DefaultShareSettings.SharePct, userID)
	if err != nil {
		return own, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var g PartnerGrant
		if err := rows.Scan(&g.UserID, &g.DailyLimitTokens, &g.SharePct); err != nil {
			return own, nil, err
		}
		grants = append(grants, g)
	}
	return own, grants, rows.Err()
}

// ShareGroup is one group as an administrator sees it.
type ShareGroup struct {
	ID        int64    `json:"id"`
	CreatedAt int64    `json:"created_at"`
	Members   []string `json:"members"`
}

// ShareGroupQuery selects groups: User matches part of a member's name, From
// and To bound when the group was made (unix nanoseconds, To exclusive).
type ShareGroupQuery struct {
	User          string
	From, To      int64
	Offset, Limit int
}

func (f ShareGroupQuery) where() (string, []any) {
	where, args := `g.created_at >= ?`, []any{f.From}
	if f.To > 0 {
		where += ` AND g.created_at < ?`
		args = append(args, f.To)
	}
	if u := f.User; u != "" {
		where += ` AND EXISTS (SELECT 1 FROM share_members m JOIN users u ON u.id = m.user_id WHERE m.group_id = g.id AND LOWER(u.username) LIKE ? ESCAPE '!')`
		args = append(args, likePattern(u))
	}
	return where, args
}

// CountShareGroups counts the groups f selects.
func (s *Store) CountShareGroups(f ShareGroupQuery) (int64, error) {
	where, args := f.where()
	var n int64
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM share_groups g WHERE `+where, args...).Scan(&n)
	return n, err
}

// ListShareGroups lists the groups f selects with their members, newest first.
func (s *Store) ListShareGroups(f ShareGroupQuery) ([]ShareGroup, error) {
	where, args := f.where()
	rows, err := s.DB.Query(`SELECT g.id, g.created_at FROM share_groups g WHERE `+where+` ORDER BY g.created_at DESC, g.id DESC LIMIT ? OFFSET ?`,
		append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, err
	}
	out := []ShareGroup{}
	for rows.Next() {
		var g ShareGroup
		if err := rows.Scan(&g.ID, &g.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		g.Members = []string{}
		out = append(out, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		mr, err := s.DB.Query(`SELECT u.username FROM share_members m JOIN users u ON u.id = m.user_id WHERE m.group_id = ? ORDER BY m.joined_at, u.id`, out[i].ID)
		if err != nil {
			return nil, err
		}
		for mr.Next() {
			var name string
			if err := mr.Scan(&name); err != nil {
				mr.Close()
				return nil, err
			}
			out[i].Members = append(out[i].Members, name)
		}
		mr.Close()
		if err := mr.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}
