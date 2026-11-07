// Package p2p builds the libp2p side of sharetoken: key material, hosts with
// NAT traversal, and a small request/response helper over streams.
package p2p

import (
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	lcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"sharetoken/box"
)

// Keys is one party's key material, all derived from a single 32-byte seed:
//
//	seed --ed25519--> Signer: libp2p identity, and the key for offers,
//	                  tickets, receipts and acks
//	seed --HKDF("sharetoken/x25519/v1")--> Box: X25519 key requests are sealed to
//
// HKDF with a distinct label keeps the two keys cryptographically independent
// while leaving a single secret to back up and protect.
type Keys struct {
	Signer ed25519.PrivateKey
	Libp2p lcrypto.PrivKey
	Box    *box.Identity
	PeerID peer.ID
}

const seedSize = 32

// KeysFromSeed derives all key material from seed.
func KeysFromSeed(seed []byte) (*Keys, error) {
	if len(seed) != seedSize {
		return nil, fmt.Errorf("p2p: seed must be %d bytes", seedSize)
	}
	signer := ed25519.NewKeyFromSeed(seed)
	lk, err := lcrypto.UnmarshalEd25519PrivateKey(signer)
	if err != nil {
		return nil, err
	}
	pid, err := peer.IDFromPrivateKey(lk)
	if err != nil {
		return nil, err
	}
	boxSeed, err := hkdf.Key(sha256.New, seed, nil, "sharetoken/x25519/v1", 32)
	if err != nil {
		return nil, err
	}
	bx, err := box.IdentityFromSeed(boxSeed)
	if err != nil {
		return nil, err
	}
	return &Keys{Signer: signer, Libp2p: lk, Box: bx, PeerID: pid}, nil
}

// NewKeys generates fresh, unpersisted keys (tests and throwaway buyers).
func NewKeys() (*Keys, error) {
	seed := make([]byte, seedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	return KeysFromSeed(seed)
}

// LoadOrCreateKeys reads a hex seed from path, creating it (mode 0600) if it
// does not exist. It refuses a key file that other users can read.
func LoadOrCreateKeys(path string) (*Keys, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		seed := make([]byte, seedSize)
		if _, err := rand.Read(seed); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, err
		}
		_, werr := f.WriteString(hex.EncodeToString(seed) + "\n")
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return nil, werr
		}
		return KeysFromSeed(seed)
	}
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("p2p: key file %s is accessible by other users; run: chmod 600 %s", path, path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	seed, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, fmt.Errorf("p2p: key file %s is not a hex seed: %w", path, err)
	}
	return KeysFromSeed(seed)
}

// ID is the peer ID as a string, the form used in every signed message.
func (k *Keys) ID() string { return k.PeerID.String() }

// ReceiptKey is the public key receipts are verified against. Without a TEE it
// is the identity key; with one it would be the enclave-generated key.
func (k *Keys) ReceiptKey() ed25519.PublicKey { return k.Signer.Public().(ed25519.PublicKey) }
