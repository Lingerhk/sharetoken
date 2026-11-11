// Package box implements the buyer<->node confidentiality layer ("sealed box").
//
// Problem it solves: nobody between the buyer and the node may read the prompt
// or the response — not the coordinator, not a circuit relay, not the network.
// Only the destination node can open the request; only the buyer can open the
// response.
//
// Scheme: per-message hybrid encryption.
//
//	ephemeral X25519 ECDH -> HKDF-SHA256 (stdlib crypto/hkdf) -> AES-256-GCM
//
// Every message uses a fresh ephemeral key, so a symmetric key is never reused
// and the sender needs no long-term key. The AAD binds each ciphertext to one
// job (and, for response chunks, one sequence number), so ciphertexts cannot be
// replayed into another job or reordered within a stream.
//
// # WHY AN APPLICATION LAYER ON TOP OF libp2p's NOISE CHANNEL
//
// libp2p already encrypts the buyer<->node connection end to end, even across
// a circuit relay. The sealed box adds two things the transport cannot:
//   - The encryption terminates at whoever holds the box key, which can be a TEE
//     enclave rather than the host's network stack. In an attested deployment
//     the node operator's OS terminates Noise but still cannot read the prompt.
//   - Receipts hash the sealed bytes, so the signed transcript is bound to
//     ciphertexts that only the intended parties could produce or open.
//
// HONEST LIMIT: without a TEE, the node operator decrypts the prompt in order to
// run the model. Encryption moves the trust boundary to the node; only
// attestation (package attest) removes the operator from it.
package box

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
)

const (
	// PublicKeySize is the size of an X25519 public key.
	PublicKeySize = 32
	// Overhead is the number of bytes Seal adds to a plaintext
	// (ephemeral key + nonce + GCM tag).
	Overhead = PublicKeySize + nonceLen + 16

	nonceLen = 12 // AES-GCM standard nonce
	keyLen   = 32 // AES-256
	boxInfo  = "sharetoken/box/v1"
)

// Identity is a party's X25519 keypair, used to receive sealed messages. A node
// publishes Public() in its signed offer; a buyer generates a fresh Identity per
// job to receive the response.
type Identity struct {
	priv *ecdh.PrivateKey
}

// GenerateIdentity creates a fresh X25519 keypair from the system CSPRNG.
func GenerateIdentity() (*Identity, error) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Identity{priv: priv}, nil
}

// IdentityFromSeed rebuilds an Identity from a 32-byte secret seed, so a node
// keeps a stable receive key across restarts.
func IdentityFromSeed(seed []byte) (*Identity, error) {
	if len(seed) != 32 {
		return nil, errors.New("box: x25519 seed must be 32 bytes")
	}
	priv, err := ecdh.X25519().NewPrivateKey(seed)
	if err != nil {
		return nil, err
	}
	return &Identity{priv: priv}, nil
}

// Public returns the 32-byte X25519 public key to seal messages to.
func (id *Identity) Public() []byte { return id.priv.PublicKey().Bytes() }

// Seal encrypts plaintext to recipientPub under a fresh ephemeral key. aad is
// authenticated but not encrypted; pass a job-bound value so the ciphertext is
// pinned to one job.
func Seal(recipientPub, plaintext, aad []byte) ([]byte, error) {
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return seal(eph, recipientPub, plaintext, aad)
}

// SealFrom is Seal with a caller-chosen sender key in place of the ephemeral
// one. A buyer seals each request from its per-job key: the request ciphertext
// then commits to the key the response is sealed to, and disclosing that one
// key later (dispute evidence) opens exactly this job and nothing else. Use a
// sender key for one message only.
func SealFrom(sender *Identity, recipientPub, plaintext, aad []byte) ([]byte, error) {
	return seal(sender.priv, recipientPub, plaintext, aad)
}

func seal(eph *ecdh.PrivateKey, recipientPub, plaintext, aad []byte) ([]byte, error) {
	rpub, err := ecdh.X25519().NewPublicKey(recipientPub)
	if err != nil {
		return nil, fmt.Errorf("box: bad recipient key: %w", err)
	}
	shared, err := eph.ECDH(rpub)
	if err != nil {
		return nil, err
	}
	ephPub := eph.PublicKey().Bytes()
	aead, err := newAEAD(shared, ephPub, recipientPub)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := make([]byte, 0, Overhead+len(plaintext))
	out = append(out, ephPub...)
	out = append(out, nonce...)
	return aead.Seal(out, nonce, plaintext, aad), nil
}

// Open reverses Seal. A tampered ciphertext, a wrong recipient, or a mismatched
// aad all return an error rather than garbage.
func (id *Identity) Open(sealed, aad []byte) ([]byte, error) {
	if len(sealed) < Overhead {
		return nil, errors.New("box: sealed message too short")
	}
	ephPub := sealed[:PublicKeySize]
	nonce := sealed[PublicKeySize : PublicKeySize+nonceLen]
	ct := sealed[PublicKeySize+nonceLen:]

	ephKey, err := ecdh.X25519().NewPublicKey(ephPub)
	if err != nil {
		return nil, err
	}
	shared, err := id.priv.ECDH(ephKey)
	if err != nil {
		return nil, err
	}
	aead, err := newAEAD(shared, ephPub, id.Public())
	if err != nil {
		return nil, err
	}
	return aead.Open(nil, nonce, ct, aad)
}

// OpenSent decrypts a message this identity sealed with SealFrom to
// recipientPub. It is how a dispute judge holding a disclosed per-job key reads
// the request that job sent.
func (id *Identity) OpenSent(recipientPub, sealed, aad []byte) ([]byte, error) {
	if len(sealed) < Overhead {
		return nil, errors.New("box: sealed message too short")
	}
	if !bytes.Equal(sealed[:PublicKeySize], id.Public()) {
		return nil, errors.New("box: message was not sealed from this key")
	}
	rpub, err := ecdh.X25519().NewPublicKey(recipientPub)
	if err != nil {
		return nil, fmt.Errorf("box: bad recipient key: %w", err)
	}
	shared, err := id.priv.ECDH(rpub)
	if err != nil {
		return nil, err
	}
	aead, err := newAEAD(shared, id.Public(), recipientPub)
	if err != nil {
		return nil, err
	}
	return aead.Open(nil, sealed[PublicKeySize:PublicKeySize+nonceLen], sealed[PublicKeySize+nonceLen:], aad)
}

// Secret returns the 32-byte private key. Export only per-job keys, and only
// as dispute evidence: whoever holds it can read that job's request and response.
func (id *Identity) Secret() []byte { return id.priv.Bytes() }

// newAEAD derives the message key, binding it to the exact (ephemeral,
// recipient) key pair so it cannot be misattributed to another recipient.
func newAEAD(shared, ephPub, recipientPub []byte) (cipher.AEAD, error) {
	info := boxInfo + string(ephPub) + string(recipientPub)
	key, err := hkdf.Key(sha256.New, shared, nil, info, keyLen)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
