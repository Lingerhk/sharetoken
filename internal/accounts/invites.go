package accounts

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Invite links and approval.
//
// Every pairing code and share invite has two secrets: the code, short enough
// to type or read aloud (pairing.go), and a link token, which goes into a link
// on the portal's site, https://<portal>/p#<token> for a pairing code and
// /s#<token> for a share invite. The token comes after the "#", so it never
// reaches a server log or a Referer header. A link can be opened by someone
// who has no account and no client yet: the page shows who invites them
// (PreviewInvite, which only takes a token: a token is too long to guess, a
// code is not), signs them in and accepts the invite on their account. The
// desktop app takes the same links as sharetoken://p/<token>.
//
// The one who makes a code or invite may ask to approve whoever uses it. Then
// using it pairs nobody yet: it files a request (invite_requests), which the
// owner approves or rejects. A request lasts RequestTTL.

// InviteLinkSchema adds the link tokens, the approval switch and the requests
// waiting for approval (core migration 9).
const InviteLinkSchema = `ALTER TABLE pairing_codes ADD COLUMN link_token VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NULL,
	ADD COLUMN approval TINYINT NOT NULL DEFAULT 0,
	ADD UNIQUE KEY pairing_codes_by_link (link_token);
ALTER TABLE share_invites ADD COLUMN link_token VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NULL,
	ADD COLUMN approval TINYINT NOT NULL DEFAULT 0,
	ADD UNIQUE KEY share_invites_by_link (link_token);
CREATE TABLE IF NOT EXISTS invite_requests (
	id         BIGINT PRIMARY KEY AUTO_INCREMENT,
	kind       VARCHAR(8) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
	code_id    BIGINT NOT NULL,
	owner_id   BIGINT NOT NULL,
	user_id    BIGINT NOT NULL,
	created_at BIGINT NOT NULL,
	expires_at BIGINT NOT NULL,
	UNIQUE KEY invite_requests_once (kind, code_id, user_id),
	KEY invite_requests_by_owner (owner_id),
	KEY invite_requests_by_user (user_id),
	FOREIGN KEY (owner_id) REFERENCES users(id),
	FOREIGN KEY (user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
`

// Kinds of invite, as they appear in links.
const (
	KindPair  = "p" // a pairing code
	KindShare = "s" // a share invite
)

// Limits on links and requests.
const (
	LinkTokenLen       = 8                  // base62 characters: 47.6 bits
	oldLinkTokenLen    = 12                 // tokens made before, still valid until they expire
	RequestTTL         = 7 * 24 * time.Hour // how long a request waits for approval
	MaxWaitingRequests = 10                 // requests one user may have waiting
)

const linkAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

func newLinkToken() (string, error) {
	out := make([]byte, 0, LinkTokenLen)
	buf := make([]byte, 32)
	for len(out) < LinkTokenLen {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			// 248 = 4 × 62: taking the rest would favour the first letters.
			if b < 248 && len(out) < LinkTokenLen {
				out = append(out, linkAlphabet[b%62])
			}
		}
	}
	return string(out), nil
}

// ValidLinkToken reports whether s has the form of a link token.
func ValidLinkToken(s string) bool {
	if len(s) != LinkTokenLen && len(s) != oldLinkTokenLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !strings.ContainsRune(linkAlphabet, rune(s[i])) {
			return false
		}
	}
	return true
}

// InviteLink is the link for an invite of kind on the portal at base
// (https://sharetoken.app): base/p#token.
func InviteLink(base, kind, token string) string {
	return strings.TrimRight(base, "/") + "/" + kind + "#" + token
}

// InviteRef is an invite as someone entered it: a link (Kind and Token, and
// Host when the link names the portal it is from), a bare link token (Token
// only), or a code (Code).
type InviteRef struct {
	Kind  string `json:"kind,omitempty"`
	Token string `json:"token,omitempty"`
	Code  string `json:"code,omitempty"`
	Host  string `json:"host,omitempty"`
}

// ParseInvite reads what someone pasted or clicked: https://<portal>/p#<token>,
// sharetoken://p/<token>[?h=<portal host>], a bare token, or a code.
func ParseInvite(s string) InviteRef {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil {
			var ref InviteRef
			switch strings.ToLower(u.Scheme) {
			case "sharetoken":
				ref = InviteRef{Kind: strings.ToLower(u.Host), Token: strings.Trim(u.Path, "/"), Host: u.Query().Get("h")}
				if ref.Token == "" { // sharetoken:p/<token> or a host-less form
					ref.Kind, ref.Token, _ = strings.Cut(strings.Trim(u.Opaque+u.Path, "/"), "/")
				}
			case "http", "https":
				ref = InviteRef{Kind: strings.ToLower(strings.Trim(u.Path, "/")), Token: strings.Trim(u.Fragment, "/ "), Host: u.Host}
			}
			if ref.Kind != KindPair && ref.Kind != KindShare {
				ref.Kind = ""
			}
			return ref
		}
	}
	if ValidLinkToken(s) {
		return InviteRef{Token: s}
	}
	return InviteRef{Code: s}
}

// InvitePreview is what the page of a link shows before anyone signs in.
type InvitePreview struct {
	Kind      string `json:"kind"`
	Inviter   string `json:"inviter"` // the account name in full: the portal masks it
	Label     string `json:"label,omitempty"`
	Approval  bool   `json:"approval"`
	ExpiresAt int64  `json:"expires_at"`
}

// PreviewInvite describes the live code or invite of kind that token stands
// for. It is ErrNotFound for a token that is wrong, expired, revoked or used
// up, or whose owner is disabled.
func (s *Store) PreviewInvite(kind, token string) (*InvitePreview, error) {
	if !ValidLinkToken(token) {
		return nil, ErrNotFound
	}
	now := s.now().UnixNano()
	p := InvitePreview{Kind: kind}
	var err error
	switch kind {
	case KindPair:
		err = s.DB.QueryRow(`SELECT u.username, c.label, c.approval, c.expires_at FROM pairing_codes c JOIN users u ON u.id = c.seller_id
			WHERE c.link_token = ? AND u.disabled = 0 AND `+strings.ReplaceAll(pairingCodeLive, "revoked", "c.revoked"), token, now).
			Scan(&p.Inviter, &p.Label, &p.Approval, &p.ExpiresAt)
	case KindShare:
		err = s.DB.QueryRow(`SELECT u.username, i.approval, i.expires_at FROM share_invites i JOIN users u ON u.id = i.inviter_id
			WHERE i.link_token = ? AND u.disabled = 0 AND i.revoked = 0 AND i.used < i.max_uses AND i.expires_at > ?`, token, now).
			Scan(&p.Inviter, &p.Approval, &p.ExpiresAt)
	default:
		return nil, ErrNotFound
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// InviteRequest is a use of a code or invite that waits for its owner's
// approval. Username is the other side: the one who asks, in the owner's list,
// and the owner, in the list of the one who asks.
type InviteRequest struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	UserID    int64  `json:"user_id"`
	Username  string `json:"username"`
	Label     string `json:"label,omitempty"` // of the pairing code it was made with
	CreatedAt int64  `json:"created_at"`
	ExpiresAt int64  `json:"expires_at"`
}

// addRequest files userID's request to use code codeID of kind, made by
// ownerID, and returns when it was made and whether it is new: asking again
// keeps the first request.
func addRequest(tx *sql.Tx, kind string, codeID, ownerID, userID, now int64) (int64, bool, error) {
	var created, expires int64
	switch err := tx.QueryRow(`SELECT created_at, expires_at FROM invite_requests WHERE kind = ? AND code_id = ? AND user_id = ?`, kind, codeID, userID).
		Scan(&created, &expires); {
	case err == nil && expires > now:
		return created, false, nil
	case err == nil:
		if _, err := tx.Exec(`DELETE FROM invite_requests WHERE kind = ? AND code_id = ? AND user_id = ?`, kind, codeID, userID); err != nil {
			return 0, false, err
		}
	case !errors.Is(err, sql.ErrNoRows):
		return 0, false, err
	}
	var waiting int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM invite_requests WHERE user_id = ? AND expires_at > ?`, userID, now).Scan(&waiting); err != nil {
		return 0, false, err
	}
	if waiting >= MaxWaitingRequests {
		return 0, false, fmt.Errorf("%w: %d requests are waiting for approval; wait for them or withdraw one", ErrLimit, waiting)
	}
	_, err := tx.Exec(`INSERT INTO invite_requests (kind, code_id, owner_id, user_id, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		kind, codeID, ownerID, userID, now, now+int64(RequestTTL))
	return now, true, err
}

// InviteRequests lists the live requests of kind waiting for ownerID's
// approval, oldest first.
func (s *Store) InviteRequests(ownerID int64, kind string) ([]InviteRequest, error) {
	return s.requests(`r.owner_id = ?`, "r.user_id", ownerID, kind)
}

// WaitingRequests lists userID's live requests of kind, oldest first.
func (s *Store) WaitingRequests(userID int64, kind string) ([]InviteRequest, error) {
	return s.requests(`r.user_id = ?`, "r.owner_id", userID, kind)
}

func (s *Store) requests(where, other string, id int64, kind string) ([]InviteRequest, error) {
	rows, err := s.DB.Query(`SELECT r.id, r.kind, u.id, u.username, COALESCE(c.label, ''), r.created_at, r.expires_at
		FROM invite_requests r JOIN users u ON u.id = `+other+`
		LEFT JOIN pairing_codes c ON r.kind = 'p' AND c.id = r.code_id
		WHERE `+where+` AND r.kind = ? AND r.expires_at > ? ORDER BY r.id`, id, kind, s.now().UnixNano())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InviteRequest{}
	for rows.Next() {
		var r InviteRequest
		if err := rows.Scan(&r.ID, &r.Kind, &r.UserID, &r.Username, &r.Label, &r.CreatedAt, &r.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ApproveRequest carries out request id of ownerID: the one who asked is
// paired with ownerID, or becomes its partner, and comes back. It is
// ErrNotFound when the request is gone or expired, or the code or invite no
// longer admits anyone (revoked, used up); such a request is dropped. The
// limits are those of using the code directly (ErrLimit).
func (s *Store) ApproveRequest(ownerID, id int64) (*InviteRequest, error) {
	now := s.now().UnixNano()
	var out *InviteRequest
	gone := false // the request no longer stands: it is dropped, and that is kept
	err := s.tx(func(tx *sql.Tx) error {
		var r InviteRequest
		var codeID int64
		var disabled bool
		err := tx.QueryRow(`SELECT r.id, r.kind, r.code_id, r.user_id, u.username, u.disabled, r.created_at, r.expires_at
			FROM invite_requests r JOIN users u ON u.id = r.user_id WHERE r.id = ? AND r.owner_id = ? FOR UPDATE`, id, ownerID).
			Scan(&r.ID, &r.Kind, &codeID, &r.UserID, &r.Username, &disabled, &r.CreatedAt, &r.ExpiresAt)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		del := func() error {
			_, err := tx.Exec(`DELETE FROM invite_requests WHERE id = ?`, id)
			return err
		}
		drop := func() error { gone = true; return del() }
		if r.ExpiresAt <= now || disabled {
			return drop()
		}
		switch r.Kind {
		case KindPair:
			var revoked, maxUses, used int64
			err := tx.QueryRow(`SELECT revoked, max_uses, used, label FROM pairing_codes WHERE id = ? AND seller_id = ? FOR UPDATE`, codeID, ownerID).
				Scan(&revoked, &maxUses, &used, &r.Label)
			if errors.Is(err, sql.ErrNoRows) || err == nil && (revoked != 0 || maxUses > 0 && used >= maxUses) {
				return drop()
			}
			if err != nil {
				return err
			}
			var paired int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM pairings WHERE buyer_id = ? AND seller_id = ?`, r.UserID, ownerID).Scan(&paired); err != nil {
				return err
			}
			if paired == 0 {
				if err := pairTx(tx, r.UserID, ownerID, codeID, now); err != nil {
					return err
				}
			}
		case KindShare:
			var revoked, maxUses, used, groupID int64
			err := tx.QueryRow(`SELECT revoked, max_uses, used, group_id FROM share_invites WHERE id = ? AND inviter_id = ? FOR UPDATE`, codeID, ownerID).
				Scan(&revoked, &maxUses, &used, &groupID)
			if errors.Is(err, sql.ErrNoRows) || err == nil && (revoked != 0 || used >= maxUses) {
				return drop()
			}
			if err != nil {
				return err
			}
			if g, _, err := sharedGroup(tx, r.UserID, ownerID); err != nil {
				return err
			} else if g == 0 {
				if _, err := joinTx(tx, r.UserID, codeID, ownerID, groupID, now); err != nil {
					return err
				}
			}
		default:
			return drop()
		}
		out = &r
		return del()
	})
	if err != nil {
		return nil, err
	}
	if gone {
		return nil, ErrNotFound
	}
	return out, nil
}

// RejectRequest drops request id waiting for ownerID; WithdrawRequest drops
// one userID made.
func (s *Store) RejectRequest(ownerID, id int64) error {
	return s.tx(func(tx *sql.Tx) error {
		return mustAffect(tx.Exec(`DELETE FROM invite_requests WHERE id = ? AND owner_id = ?`, id, ownerID))
	})
}

// WithdrawRequest drops request id that userID made.
func (s *Store) WithdrawRequest(userID, id int64) error {
	return s.tx(func(tx *sql.Tx) error {
		return mustAffect(tx.Exec(`DELETE FROM invite_requests WHERE id = ? AND user_id = ?`, id, userID))
	})
}
