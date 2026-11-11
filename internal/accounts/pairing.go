package accounts

import (
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Pairing ties buyers to sellers.
//
// A seller makes a pairing code and hands it to the people it wants as its
// buyers. A buyer who redeems the code is paired with that seller, and from
// then on buys only from the sellers it is paired with: the coordinator leaves
// every other node out when it matches that buyer's requests and lists models
// for it. One seller can have many buyers, and one code admits as many of them
// as its use limit allows. A buyer with no pairing buys from anyone, as before.
//
// Pairing is between user accounts, not devices: all of a buyer's devices are
// bound by it, and all of a seller's devices serve it. Either side can end it.

// PairingSchema holds pairing codes and the pairings made with them.
const PairingSchema = `
CREATE TABLE IF NOT EXISTS pairing_codes (
	id         BIGINT PRIMARY KEY AUTO_INCREMENT,
	seller_id  BIGINT NOT NULL,
	code       VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL UNIQUE,
	label      VARCHAR(64) NOT NULL DEFAULT '',
	max_uses   BIGINT NOT NULL DEFAULT 0,
	used       BIGINT NOT NULL DEFAULT 0,
	created_at BIGINT NOT NULL,
	expires_at BIGINT NOT NULL DEFAULT 0,
	revoked    BIGINT NOT NULL DEFAULT 0,
	KEY pairing_codes_by_seller (seller_id),
	FOREIGN KEY (seller_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
CREATE TABLE IF NOT EXISTS pairings (
	buyer_id   BIGINT NOT NULL,
	seller_id  BIGINT NOT NULL,
	code_id    BIGINT NOT NULL DEFAULT 0,
	created_at BIGINT NOT NULL,
	PRIMARY KEY (buyer_id, seller_id),
	KEY pairings_by_seller (seller_id),
	FOREIGN KEY (buyer_id) REFERENCES users(id),
	FOREIGN KEY (seller_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
`

// Limits on pairing.
const (
	MaxPairingCodes    = 20                  // live codes one seller may hold
	MaxPairedSellers   = 20                  // sellers one buyer may be paired with
	MaxPairedBuyers    = 1000                // buyers one seller may have
	DefaultPairingTTL  = 24 * time.Hour      // lifetime of a code when none is asked for
	MaxPairingTTL      = 90 * 24 * time.Hour // longest lifetime of a code
	minPairingTTL      = time.Minute
	pairingCodeBytes   = 10 // 80 bits: 16 base32 characters
	maxPairingLabelLen = 64
)

// ErrOwnCode means a user tried to pair with itself.
var ErrOwnCode = errors.New("accounts: this is your own pairing code")

// ErrLimit means a pairing limit is reached.
var ErrLimit = errors.New("accounts: limit reached")

// PairingCode is a code a seller made. Code is the secret itself: the seller
// reads it again to share it, and whoever holds it can pair.
type PairingCode struct {
	ID        int64  `json:"id"`
	Code      string `json:"code"`
	Label     string `json:"label"`
	MaxUses   int64  `json:"max_uses"` // 0: no limit
	Used      int64  `json:"used"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt int64  `json:"expires_at"` // unix nanoseconds
}

// PairedUser is the other side of a pairing, as one side sees it.
type PairedUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Label    string `json:"label,omitempty"` // label of the code the pairing was made with
	Since    int64  `json:"since"`
	Disabled bool   `json:"disabled,omitempty"`
}

// NormalizePairingCode strips what people add when they pass a code on:
// spaces, dashes and lower case.
func NormalizePairingCode(code string) string {
	return strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "\t", "", "\n", "", "\r", "").Replace(code))
}

// FormatPairingCode writes a code in groups of four for reading aloud.
func FormatPairingCode(code string) string {
	var b strings.Builder
	for i, r := range code {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func newPairingCode() (string, error) {
	b := make([]byte, pairingCodeBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b), nil
}

const pairingCodeColumns = `id, code, label, max_uses, used, created_at, expires_at`

// pairingCodeLive selects codes that can still be redeemed; its argument is now.
const pairingCodeLive = `revoked = 0 AND expires_at > ? AND (max_uses = 0 OR used < max_uses)`

func scanPairingCode(row interface{ Scan(...any) error }) (*PairingCode, error) {
	var c PairingCode
	if err := row.Scan(&c.ID, &c.Code, &c.Label, &c.MaxUses, &c.Used, &c.CreatedAt, &c.ExpiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &c, nil
}

// CreatePairingCode makes a code for sellerID. ttl 0 means DefaultPairingTTL;
// maxUses 0 means any number of buyers.
func (s *Store) CreatePairingCode(sellerID int64, label string, ttl time.Duration, maxUses int64) (*PairingCode, error) {
	label = strings.TrimSpace(label)
	if ttl == 0 {
		ttl = DefaultPairingTTL
	}
	switch {
	case len(label) > maxPairingLabelLen:
		return nil, fmt.Errorf("%w: the label must be at most %d characters", ErrInvalid, maxPairingLabelLen)
	case ttl < minPairingTTL || ttl > MaxPairingTTL:
		return nil, fmt.Errorf("%w: a code lasts between %s and %d days", ErrInvalid, minPairingTTL, int(MaxPairingTTL/(24*time.Hour)))
	case maxUses < 0 || maxUses > MaxPairedBuyers:
		return nil, fmt.Errorf("%w: uses must be between 0 (no limit) and %d", ErrInvalid, MaxPairedBuyers)
	}
	code, err := newPairingCode()
	if err != nil {
		return nil, err
	}
	now := s.now()
	var out *PairingCode
	err = s.tx(func(tx *sql.Tx) error {
		var live int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM pairing_codes WHERE seller_id = ? AND `+pairingCodeLive, sellerID, now.UnixNano()).Scan(&live); err != nil {
			return err
		}
		if live >= MaxPairingCodes {
			return fmt.Errorf("%w: %d pairing codes are in use; revoke one first", ErrLimit, live)
		}
		res, err := tx.Exec(`INSERT INTO pairing_codes (seller_id, code, label, max_uses, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
			sellerID, code, label, maxUses, now.UnixNano(), now.Add(ttl).UnixNano())
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		out, err = scanPairingCode(tx.QueryRow(`SELECT `+pairingCodeColumns+` FROM pairing_codes WHERE id = ?`, id))
		return err
	})
	return out, err
}

// PairingCodes lists sellerID's codes that can still be redeemed, newest first.
func (s *Store) PairingCodes(sellerID int64) ([]PairingCode, error) {
	rows, err := s.DB.Query(`SELECT `+pairingCodeColumns+` FROM pairing_codes WHERE seller_id = ? AND `+pairingCodeLive+` ORDER BY id DESC`,
		sellerID, s.now().UnixNano())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PairingCode{}
	for rows.Next() {
		c, err := scanPairingCode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// RevokePairingCode stops a code from admitting anyone else. The pairings made
// with it stay.
func (s *Store) RevokePairingCode(sellerID, id int64) error {
	return s.tx(func(tx *sql.Tx) error {
		return mustAffect(tx.Exec(`UPDATE pairing_codes SET revoked = 1 WHERE id = ? AND seller_id = ? AND revoked = 0`, id, sellerID))
	})
}

// RedeemPairingCode pairs buyerID with the seller that made code and returns
// that seller. It is ErrNotFound for a code that is wrong, expired, revoked or
// used up, ErrOwnCode for the buyer's own code, and ErrLimit when either side
// has as many pairings as it may. Redeeming a code of a seller the buyer is
// already paired with changes nothing and takes no use.
func (s *Store) RedeemPairingCode(buyerID int64, code string) (*PairedUser, error) {
	code = NormalizePairingCode(code)
	if code == "" {
		return nil, ErrNotFound
	}
	now := s.now().UnixNano()
	var out *PairedUser
	err := s.tx(func(tx *sql.Tx) error {
		var codeID, sellerID int64
		var label string
		err := tx.QueryRow(`SELECT id, seller_id, label FROM pairing_codes WHERE code = ? AND `+pairingCodeLive, code, now).Scan(&codeID, &sellerID, &label)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if sellerID == buyerID {
			return ErrOwnCode
		}
		seller, err := scanUser(tx.QueryRow(`SELECT `+userColumns+` FROM users WHERE id = ?`, sellerID))
		if err != nil {
			return err
		}
		if seller.Disabled {
			return ErrNotFound
		}
		out = &PairedUser{ID: seller.ID, Username: seller.Username, Label: label, Since: now}
		var since int64
		switch err := tx.QueryRow(`SELECT created_at FROM pairings WHERE buyer_id = ? AND seller_id = ?`, buyerID, sellerID).Scan(&since); {
		case err == nil:
			out.Since = since
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		var sellers, buyers int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM pairings WHERE buyer_id = ?`, buyerID).Scan(&sellers); err != nil {
			return err
		}
		if sellers >= MaxPairedSellers {
			return fmt.Errorf("%w: you are paired with %d sellers; leave one first", ErrLimit, sellers)
		}
		if err := tx.QueryRow(`SELECT COUNT(*) FROM pairings WHERE seller_id = ?`, sellerID).Scan(&buyers); err != nil {
			return err
		}
		if buyers >= MaxPairedBuyers {
			return fmt.Errorf("%w: this seller has as many buyers as it may", ErrLimit)
		}
		if _, err := tx.Exec(`INSERT INTO pairings (buyer_id, seller_id, code_id, created_at) VALUES (?, ?, ?, ?)`, buyerID, sellerID, codeID, now); err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE pairing_codes SET used = used + 1 WHERE id = ?`, codeID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Pairings lists who userID is paired with: the sellers it buys from, and the
// buyers that buy from it. Newest first.
func (s *Store) Pairings(userID int64) (sellers, buyers []PairedUser, err error) {
	list := func(mine, other string) ([]PairedUser, error) {
		rows, err := s.DB.Query(`SELECT u.id, u.username, u.disabled, p.created_at, COALESCE(c.label, '')
			FROM pairings p JOIN users u ON u.id = p.`+other+` LEFT JOIN pairing_codes c ON c.id = p.code_id
			WHERE p.`+mine+` = ? ORDER BY p.created_at DESC, u.id DESC`, userID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []PairedUser{}
		for rows.Next() {
			var p PairedUser
			var disabled int
			if err := rows.Scan(&p.ID, &p.Username, &disabled, &p.Since, &p.Label); err != nil {
				return nil, err
			}
			p.Disabled = disabled != 0
			out = append(out, p)
		}
		return out, rows.Err()
	}
	if sellers, err = list("buyer_id", "seller_id"); err != nil {
		return nil, nil, err
	}
	buyers, err = list("seller_id", "buyer_id")
	return sellers, buyers, err
}

// Unpair ends the pairing of buyerID with sellerID.
func (s *Store) Unpair(buyerID, sellerID int64) error {
	return s.tx(func(tx *sql.Tx) error {
		return mustAffect(tx.Exec(`DELETE FROM pairings WHERE buyer_id = ? AND seller_id = ?`, buyerID, sellerID))
	})
}

// PairedSellers returns the users buyerID is paired with. An empty result
// means the buyer is not paired and may buy from anyone.
func PairedSellers(q interface {
	Query(query string, args ...any) (*sql.Rows, error)
}, buyerID int64) ([]int64, error) {
	rows, err := q.Query(`SELECT seller_id FROM pairings WHERE buyer_id = ?`, buyerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
