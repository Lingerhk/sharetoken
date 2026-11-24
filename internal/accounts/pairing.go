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
//
// A seller can also go private: then only the buyers paired with it are
// matched to it and see its models. Without that, a seller serves anyone.

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
CREATE TABLE IF NOT EXISTS pairing_private (
	user_id BIGINT PRIMARY KEY,
	since   BIGINT NOT NULL,
	FOREIGN KEY (user_id) REFERENCES users(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
`

// Limits on pairing.
const (
	MaxPairingCodes    = 20                 // live codes one seller may hold
	MaxPairedSellers   = 3                  // sellers one buyer may be paired with
	MaxPairedBuyers    = 1000               // buyers one seller may have
	DefaultPairingTTL  = 24 * time.Hour     // lifetime of a code when none is asked for
	MaxPairingTTL      = 7 * 24 * time.Hour // longest lifetime of a code
	minPairingTTL      = time.Minute
	pairingCodeLen     = 6 // base32 characters: 30 bits, so the portal limits wrong ones hard (portal/invites.go)
	maxPairingLabelLen = 64
)

// ErrOwnCode means a user tried to pair with itself.
var ErrOwnCode = errors.New("accounts: this is your own pairing code")

// ErrLimit means a pairing limit is reached.
var ErrLimit = errors.New("accounts: limit reached")

// PairingCode is a code a seller made. Code is the secret itself: the seller
// reads it again to share it, and whoever holds it can pair. LinkToken is the
// same secret for a link (invites.go): whoever opens the link can pair too.
type PairingCode struct {
	ID        int64  `json:"id"`
	Code      string `json:"code"`
	LinkToken string `json:"link_token"`
	Label     string `json:"label"`
	MaxUses   int64  `json:"max_uses"` // 0: no limit
	Used      int64  `json:"used"`
	Approval  bool   `json:"approval"` // each pairing waits for the seller to approve it
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
	// Pending: the seller asks to approve each pairing, and has not yet; the
	// pairing starts once it does (invites.go).
	Pending bool `json:"pending,omitempty"`
	// NewRequest: this call filed the request Pending stands for.
	NewRequest bool `json:"-"`
}

// NormalizePairingCode strips what people add when they pass a code on:
// spaces, dashes and lower case.
func NormalizePairingCode(code string) string {
	return strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "\t", "", "\n", "", "\r", "").Replace(code))
}

// FormatPairingCode writes a code in groups for reading aloud: two groups of
// three (ABC-DEF); codes made before in two of five (ten characters) or fours.
func FormatPairingCode(code string) string {
	group := 4
	switch len(code) {
	case pairingCodeLen:
		group = 3
	case 10:
		group = 5
	}
	var b strings.Builder
	for i, r := range code {
		if i > 0 && i%group == 0 {
			b.WriteByte('-')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func newPairingCode() (string, error) {
	b := make([]byte, (pairingCodeLen*5+7)/8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)[:pairingCodeLen], nil
}

// uniqueCode makes a code that table (pairing_codes or share_invites) does not
// hold yet. Codes are short, so a clash is rare but possible; the UNIQUE key
// still catches a race between two transactions.
func uniqueCode(tx *sql.Tx, table string) (string, error) {
	return uniqueValue(tx, table, "code", newPairingCode)
}

// uniqueValue makes a value with gen that column of table does not hold yet.
func uniqueValue(tx *sql.Tx, table, column string, gen func() (string, error)) (string, error) {
	for range 8 {
		code, err := gen()
		if err != nil {
			return "", err
		}
		var one int
		switch err := tx.QueryRow(`SELECT 1 FROM `+table+` WHERE `+column+` = ?`, code).Scan(&one); {
		case errors.Is(err, sql.ErrNoRows):
			return code, nil
		case err != nil:
			return "", err
		}
	}
	return "", errors.New("accounts: no free code found")
}

const pairingCodeColumns = `id, code, COALESCE(link_token, ''), label, max_uses, used, approval, created_at, expires_at`

// pairingCodeLive selects codes that can still be redeemed; its argument is now.
const pairingCodeLive = `revoked = 0 AND expires_at > ? AND (max_uses = 0 OR used < max_uses)`

func scanPairingCode(row interface{ Scan(...any) error }) (*PairingCode, error) {
	var c PairingCode
	if err := row.Scan(&c.ID, &c.Code, &c.LinkToken, &c.Label, &c.MaxUses, &c.Used, &c.Approval, &c.CreatedAt, &c.ExpiresAt); err != nil {
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
	return s.CreatePairingCodeWith(sellerID, PairingCodeOptions{Label: label, TTL: ttl, MaxUses: maxUses})
}

// PairingCodeOptions is what a seller chooses for a new code.
type PairingCodeOptions struct {
	Label    string
	TTL      time.Duration // 0: DefaultPairingTTL
	MaxUses  int64         // 0: any number of buyers
	Approval bool          // each pairing waits for the seller to approve it
}

// CreatePairingCodeWith makes a code for sellerID, and the link token that
// stands for it.
func (s *Store) CreatePairingCodeWith(sellerID int64, o PairingCodeOptions) (*PairingCode, error) {
	label, ttl, maxUses := strings.TrimSpace(o.Label), o.TTL, o.MaxUses
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
	now := s.now()
	var out *PairingCode
	err := s.tx(func(tx *sql.Tx) error {
		var live int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM pairing_codes WHERE seller_id = ? AND `+pairingCodeLive, sellerID, now.UnixNano()).Scan(&live); err != nil {
			return err
		}
		if live >= MaxPairingCodes {
			return fmt.Errorf("%w: %d pairing codes are in use; revoke one first", ErrLimit, live)
		}
		code, err := uniqueCode(tx, "pairing_codes")
		if err != nil {
			return err
		}
		token, err := uniqueValue(tx, "pairing_codes", "link_token", newLinkToken)
		if err != nil {
			return err
		}
		res, err := tx.Exec(`INSERT INTO pairing_codes (seller_id, code, link_token, label, max_uses, approval, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			sellerID, code, token, label, maxUses, boolInt(o.Approval), now.UnixNano(), now.Add(ttl).UnixNano())
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
// already paired with changes nothing and takes no use. When the code asks the
// seller to approve each pairing, nothing is paired yet: the request waits for
// the seller (invites.go) and the seller comes back with Pending set.
func (s *Store) RedeemPairingCode(buyerID int64, code string) (*PairedUser, error) {
	code = NormalizePairingCode(code)
	if code == "" {
		return nil, ErrNotFound
	}
	return s.redeemPairing(buyerID, "code", code)
}

// RedeemPairingLink is RedeemPairingCode for the link token of a code.
func (s *Store) RedeemPairingLink(buyerID int64, token string) (*PairedUser, error) {
	if !ValidLinkToken(token) {
		return nil, ErrNotFound
	}
	return s.redeemPairing(buyerID, "link_token", token)
}

func (s *Store) redeemPairing(buyerID int64, column, value string) (*PairedUser, error) {
	now := s.now().UnixNano()
	var out *PairedUser
	err := s.tx(func(tx *sql.Tx) error {
		var codeID, sellerID int64
		var label string
		var approval bool
		err := tx.QueryRow(`SELECT id, seller_id, label, approval FROM pairing_codes WHERE `+column+` = ? AND `+pairingCodeLive+` FOR UPDATE`, value, now).
			Scan(&codeID, &sellerID, &label, &approval)
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
		if err := pairingLimits(tx, buyerID, sellerID); err != nil {
			return err
		}
		if approval {
			out.Pending = true
			out.Since, out.NewRequest, err = addRequest(tx, KindPair, codeID, sellerID, buyerID, now)
			return err
		}
		return pairTx(tx, buyerID, sellerID, codeID, now)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// pairingLimits fails with ErrLimit when buyerID may not pair with one more
// seller, or sellerID with one more buyer.
func pairingLimits(tx *sql.Tx, buyerID, sellerID int64) error {
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
	return nil
}

// pairTx pairs buyerID with sellerID through code codeID, which takes a use.
func pairTx(tx *sql.Tx, buyerID, sellerID, codeID, now int64) error {
	if err := pairingLimits(tx, buyerID, sellerID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO pairings (buyer_id, seller_id, code_id, created_at) VALUES (?, ?, ?, ?)`, buyerID, sellerID, codeID, now); err != nil {
		return err
	}
	_, err := tx.Exec(`UPDATE pairing_codes SET used = used + 1 WHERE id = ?`, codeID)
	return err
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
	return queryIDs(q, `SELECT seller_id FROM pairings WHERE buyer_id = ?`, buyerID)
}

// SetPrivateSeller turns userID's private mode on or off: a private seller
// serves only the buyers paired with it.
func (s *Store) SetPrivateSeller(userID int64, on bool) error {
	now := s.now().UnixNano()
	return s.tx(func(tx *sql.Tx) error {
		if !on {
			_, err := tx.Exec(`DELETE FROM pairing_private WHERE user_id = ?`, userID)
			return err
		}
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM pairing_private WHERE user_id = ?`, userID).Scan(&n); err != nil || n > 0 {
			return err
		}
		_, err := tx.Exec(`INSERT INTO pairing_private (user_id, since) VALUES (?, ?)`, userID, now)
		return err
	})
}

// PrivateSeller reports whether userID sells to its paired buyers only.
func (s *Store) PrivateSeller(userID int64) (bool, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM pairing_private WHERE user_id = ?`, userID).Scan(&n)
	return n > 0, err
}

// PrivateSellers returns every user that sells to its paired buyers only.
func PrivateSellers(q interface {
	Query(query string, args ...any) (*sql.Rows, error)
}) ([]int64, error) {
	return queryIDs(q, `SELECT user_id FROM pairing_private`)
}

func queryIDs(q interface {
	Query(query string, args ...any) (*sql.Rows, error)
}, query string, args ...any) ([]int64, error) {
	rows, err := q.Query(query, args...)
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

// Pairing is one pairing as an administrator sees it: both account names in
// full, the label of the code it was made with, and whether the seller is
// private.
type Pairing struct {
	BuyerID       int64  `json:"buyer_id"`
	Buyer         string `json:"buyer"`
	SellerID      int64  `json:"seller_id"`
	Seller        string `json:"seller"`
	Label         string `json:"label"`
	Since         int64  `json:"since"`
	SellerPrivate bool   `json:"seller_private"`
}

// PairingQuery selects pairings: User matches part of either account name,
// From and To bound when the pairing was made (unix nanoseconds, To exclusive).
type PairingQuery struct {
	User          string
	From, To      int64
	Offset, Limit int
}

func (f PairingQuery) where() (string, []any) {
	where, args := `p.created_at >= ?`, []any{f.From}
	if f.To > 0 {
		where += ` AND p.created_at < ?`
		args = append(args, f.To)
	}
	if u := strings.TrimSpace(f.User); u != "" {
		like := likePattern(u)
		where += ` AND (LOWER(b.username) LIKE ? ESCAPE '!' OR LOWER(s.username) LIKE ? ESCAPE '!')`
		args = append(args, like, like)
	}
	return where, args
}

const pairingJoin = ` FROM pairings p JOIN users b ON b.id = p.buyer_id JOIN users s ON s.id = p.seller_id `

// CountPairings counts the pairings f selects.
func (s *Store) CountPairings(f PairingQuery) (int64, error) {
	where, args := f.where()
	var n int64
	err := s.DB.QueryRow(`SELECT COUNT(*)`+pairingJoin+`WHERE `+where, args...).Scan(&n)
	return n, err
}

// ListPairings lists the pairings f selects, newest first.
func (s *Store) ListPairings(f PairingQuery) ([]Pairing, error) {
	where, args := f.where()
	rows, err := s.DB.Query(`SELECT p.buyer_id, b.username, p.seller_id, s.username, COALESCE(c.label, ''), p.created_at,
		EXISTS (SELECT 1 FROM pairing_private v WHERE v.user_id = p.seller_id)`+pairingJoin+`
		LEFT JOIN pairing_codes c ON c.id = p.code_id
		WHERE `+where+` ORDER BY p.created_at DESC, p.buyer_id DESC, p.seller_id DESC LIMIT ? OFFSET ?`, append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Pairing{}
	for rows.Next() {
		var p Pairing
		var private int
		if err := rows.Scan(&p.BuyerID, &p.Buyer, &p.SellerID, &p.Seller, &p.Label, &p.Since, &private); err != nil {
			return nil, err
		}
		p.SellerPrivate = private != 0
		out = append(out, p)
	}
	return out, rows.Err()
}
