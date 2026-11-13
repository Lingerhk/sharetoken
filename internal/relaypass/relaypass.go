// Package relaypass is the admission ticket for relay reservations.
//
// A relay reservation is the scarce resource of a relay: it is what makes a
// peer reachable through it. Relays therefore reserve only for devices of
// registered users. The portal, which knows the users, signs a short-lived
// pass for a device (peer ID + user account + expiry, and the client release,
// OS and architecture for the relay's statistics); the device presents it
// to a relay on Protocol before reserving, and the relay verifies it offline
// against the portal's public key. Relays stay stateless and never talk to the
// portal or the database.
//
// Passes are short-lived (one hour by default) so that disabling a user takes
// effect at the next renewal without telling any relay, and a leaked pass is
// useless to others: it names the device's peer ID, which only the holder of
// the device key can authenticate as.
package relaypass

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"

	"sharetoken/internal/canon"
	"sharetoken/internal/p2p"
	"sharetoken/internal/version"
)

// Protocol is the libp2p protocol a device presents its pass on.
const Protocol = "/sharetoken/relay-pass/1.0.0"

// Frame types on Protocol.
const (
	TypePass = "relay_pass"
	TypeOK   = "ok"
)

// Version doubles as the signing domain. It changes only when a field relays
// rely on changes meaning; new fields are added to Claims without a new
// version, and relays that do not know them ignore them.
const Version = "sharetoken/relay-pass/v1"

// MaxSkew tolerates clock differences between portal and relay.
const MaxSkew = time.Minute

// MaxBody bounds a pass's signed body.
const MaxBody = 4 << 10

// Client is what the portal knew of the device's client when it signed: the
// release, operating system and CPU architecture the client reported. The
// portal checks their form, not their truth: a modified client can report
// anything, so relays use them for statistics, never for admission.
type Client struct {
	Version string `json:"version,omitempty"`
	OS      string `json:"os,omitempty"`   // GOOS: windows, linux, darwin
	Arch    string `json:"arch,omitempty"` // GOARCH: amd64, arm64
}

// Clean drops the fields that are not in the expected form, so that whatever a
// client reported can be signed, stored and used as a metric label.
func (c Client) Clean() Client {
	if !version.ValidVersionLabel(c.Version) {
		c.Version = ""
	}
	if !version.ValidPlatformName(c.OS) {
		c.OS = ""
	}
	if !version.ValidPlatformName(c.Arch) {
		c.Arch = ""
	}
	return c
}

// Claims is what a pass says.
type Claims struct {
	V         string `json:"v"`
	Peer      string `json:"peer"`    // device peer ID
	Account   string `json:"account"` // ledger account of its user, e.g. "user:3"; relays limit devices per account
	IssuedAt  int64  `json:"issued_at"`
	ExpiresAt int64  `json:"expires_at"`
	Client    Client `json:"client"`
}

// Pass admits one device to reserve on relays until ExpiresAt. The signature
// covers Body, the JSON of the claims, byte for byte as the portal wrote it, so
// the portal can add claims that older relays skip without breaking their
// signature check. Claims are decoded from Body; they are not sent separately.
type Pass struct {
	Body   []byte `json:"body"` // JSON of Claims (base64 on the wire, so no JSON layer can reformat it)
	Sig    []byte `json:"sig"`
	Claims `json:"-"`
}

func signingBytes(body []byte) []byte { return canon.New(Version).Bytes(body).Sum() }

// Issue returns a pass for peer of account, valid for ttl from now, signed with priv.
func Issue(priv ed25519.PrivateKey, peer, account string, client Client, now time.Time, ttl time.Duration) *Pass {
	c := Claims{V: Version, Peer: peer, Account: account, IssuedAt: now.UnixNano(), ExpiresAt: now.Add(ttl).UnixNano(), Client: client}
	body, err := json.Marshal(c)
	if err != nil {
		panic(err) // plain strings and integers
	}
	return &Pass{Body: body, Sig: ed25519.Sign(priv, signingBytes(body)), Claims: c}
}

// UnmarshalJSON reads the wire form and decodes the claims from the body.
// A body that does not decode leaves the claims empty, which Verify refuses.
func (p *Pass) UnmarshalJSON(b []byte) error {
	var w struct {
		Body []byte `json:"body"`
		Sig  []byte `json:"sig"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*p = Pass{Body: w.Body, Sig: w.Sig}
	if len(w.Body) <= MaxBody {
		_ = json.Unmarshal(w.Body, &p.Claims)
	}
	return nil
}

// Expires is the pass's expiry.
func (p *Pass) Expires() time.Time { return time.Unix(0, p.ExpiresAt) }

// Errors.
var (
	ErrBadSignature = errors.New("relaypass: signature does not verify with any trusted portal key")
	ErrExpired      = errors.New("relaypass: expired")
	ErrWrongPeer    = errors.New("relaypass: issued to another peer")
	ErrMalformed    = errors.New("relaypass: malformed")
)

// Verify checks that the pass was signed by one of keys, is valid at now, and
// was issued to peer (the authenticated identity presenting it).
func (p *Pass) Verify(keys []ed25519.PublicKey, peer string, now time.Time) error {
	switch {
	case len(p.Body) == 0 || len(p.Body) > MaxBody:
		return ErrMalformed
	case p.V != Version || p.Peer == "" || p.Account == "" || p.ExpiresAt <= p.IssuedAt:
		return ErrMalformed
	case p.Peer != peer:
		return ErrWrongPeer
	case now.UnixNano() >= p.ExpiresAt:
		return ErrExpired
	case p.IssuedAt > now.Add(MaxSkew).UnixNano():
		return fmt.Errorf("%w: issued in the future", ErrMalformed)
	}
	msg := signingBytes(p.Body)
	for _, k := range keys {
		if ed25519.Verify(k, msg, p.Sig) {
			return nil
		}
	}
	return ErrBadSignature
}

// FormatKey renders a portal public key for relay configuration.
func FormatKey(pub ed25519.PublicKey) string { return "ed25519:" + hex.EncodeToString(pub) }

// ParseKey parses a key rendered by FormatKey (the "ed25519:" prefix is optional).
func ParseKey(s string) (ed25519.PublicKey, error) {
	b, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(s), "ed25519:"))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("relaypass: bad portal key %q (want ed25519:<64 hex digits>)", s)
	}
	return ed25519.PublicKey(b), nil
}

// Present shows pass to the relay, which then admits this host's
// reservations until the pass expires.
func Present(ctx context.Context, h host.Host, relay peer.AddrInfo, pass *Pass) error {
	if len(relay.Addrs) > 0 {
		h.Peerstore().AddAddrs(relay.ID, relay.Addrs, peerstore.TempAddrTTL)
	}
	return p2p.Call(ctx, h, relay.ID, Protocol, TypePass, pass, TypeOK, nil)
}
