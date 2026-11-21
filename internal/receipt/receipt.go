// Package receipt implements the response-integrity layer: a signed, verifiable
// transcript binding that makes a node accountable for exactly what it returned.
//
// # WHAT A VERIFIED RECEIPT PROVES
//
// The node identified by NodeID attested: "for the request whose bytes hash to
// RequestHash, I produced the response whose bytes hash to ResponseHash, using
// ModelID, consuming these tokens." The signature is non-repudiable and
// checkable offline by a dispute resolver. The buyer independently recomputes
// both hashes from what it actually sent and received (BuyerCheck), so the node
// cannot sign one transcript and deliver another.
//
// # WHAT IT DOES NOT PROVE ON ITS OWN
//
// That the bytes genuinely came from the claimed model. A dishonest node can
// sign a fabricated response. Receipts make cheating attributable and
// punishable, which is the foundation for the three ways this project closes
// that gap:
//
//   - Evidence (package evidence): a buyer discloses one job's key and the
//     judge checks the signed transcript, so unreadable output and inflated
//     usage are proven, not alleged.
//   - Sampling audits (package audit): the coordinator replays random jobs on
//     a reference copy of the model and penalizes mismatches; probabilistic,
//     the Proof-of-Sampling approach.
//   - Hardware attestation (package attest): the signing key lives in a TEE
//     whose code identity is measured, so a valid signature means the audited
//     image produced the output.
//
// For CLOSED vendor models there is no third option: the weights are secret
// (no recomputation) and vendors do not sign responses. For OPEN-WEIGHT models
// the verifier can rerun the model and count tokens with the public tokenizer,
// which is what makes end-to-end verification achievable in this project.
package receipt

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash"

	"sharetoken/internal/canon"
)

// Version identifies the canonical encoding, so verifiers reject unknown
// formats instead of misreading them.
const Version = "sharetoken/receipt/v1"

// Receipt is the transcript a node signs.
type Receipt struct {
	Version          string            `json:"v"`
	JobID            string            `json:"job_id"`
	ModelID          string            `json:"model"`
	NodeID           ed25519.PublicKey `json:"node_key"`  // the signer
	RequestHash      []byte            `json:"req_hash"`  // SHA-256 of the exact sealed request bytes
	ResponseHash     []byte            `json:"resp_hash"` // StreamHasher digest of the sealed response chunks
	PromptTokens     uint64            `json:"prompt_tokens"`
	CompletionTokens uint64            `json:"completion_tokens"`
	// CacheReadTokens and CacheWriteTokens are the part of PromptTokens the
	// model server read from and wrote to its prompt cache; they are priced
	// at the ticket's cache prices instead of its input price.
	CacheReadTokens  uint64 `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens uint64 `json:"cache_write_tokens,omitempty"`
	UnixNano         int64  `json:"ts"`
	Nonce            []byte `json:"nonce"` // random, prevents receipt reuse across jobs
}

// SignedReceipt is a Receipt plus the node's Ed25519 signature over its
// canonical encoding.
type SignedReceipt struct {
	Receipt Receipt `json:"receipt"`
	Sig     []byte  `json:"sig"`
}

// Sign produces a SignedReceipt. priv must correspond to r.NodeID.
func Sign(priv ed25519.PrivateKey, r Receipt) (*SignedReceipt, error) {
	if r.Version == "" {
		r.Version = Version
	}
	if !bytes.Equal(priv.Public().(ed25519.PublicKey), r.NodeID) {
		return nil, errors.New("receipt: private key does not match NodeID")
	}
	return &SignedReceipt{Receipt: r, Sig: ed25519.Sign(priv, r.canonical())}, nil
}

// Verify checks only the signature: that NodeID really attested to this exact
// receipt. It does not judge whether the content is honest; see BuyerCheck.
func Verify(sr *SignedReceipt) error {
	if sr == nil {
		return errors.New("receipt: nil")
	}
	if sr.Receipt.Version != Version {
		return errors.New("receipt: unknown version")
	}
	if len(sr.Receipt.NodeID) != ed25519.PublicKeySize {
		return errors.New("receipt: bad node key length")
	}
	if !ed25519.Verify(sr.Receipt.NodeID, sr.Receipt.canonical(), sr.Sig) {
		return errors.New("receipt: signature verification failed")
	}
	return nil
}

// BuyerCheck is the buyer-side gate. It verifies the signature, then confirms
// the receipt is bound to the expected job, model and node key, and that both
// hashes match what the buyer actually sent and received. Any mismatch means
// the node signed something other than what it served.
func BuyerCheck(sr *SignedReceipt, wantJobID, wantModelID string, wantNodeKey ed25519.PublicKey, localReqHash, localRespHash []byte) error {
	if err := Verify(sr); err != nil {
		return err
	}
	r := sr.Receipt
	switch {
	case !bytes.Equal(r.NodeID, wantNodeKey):
		return errors.New("receipt: signed by an unexpected node key")
	case r.JobID != wantJobID:
		return errors.New("receipt: job id mismatch")
	case r.ModelID != wantModelID:
		return errors.New("receipt: model id mismatch (possible model substitution)")
	case !bytes.Equal(r.RequestHash, localReqHash):
		return errors.New("receipt: request hash mismatch")
	case !bytes.Equal(r.ResponseHash, localRespHash):
		return errors.New("receipt: response hash mismatch (response was altered)")
	}
	return nil
}

// Digest is a SHA-256 over the canonical encoding. Buyers sign it to
// acknowledge a receipt; settlement refers to it.
func (r *Receipt) Digest() []byte {
	sum := sha256.Sum256(r.canonical())
	return sum[:]
}

func (r *Receipt) canonical() []byte {
	return canon.New(r.Version).
		String(r.JobID).
		String(r.ModelID).
		Bytes(r.NodeID).
		Bytes(r.RequestHash).
		Bytes(r.ResponseHash).
		U64(r.PromptTokens).
		U64(r.CompletionTokens).
		U64(r.CacheReadTokens).
		U64(r.CacheWriteTokens).
		I64(r.UnixNano).
		Bytes(r.Nonce).
		Sum()
}

// StreamHasher accumulates the response hash while chunks stream, so the node
// and the buyer reach the same ResponseHash without buffering the response.
type StreamHasher struct{ h hash.Hash }

// NewStreamHasher returns an empty hasher.
func NewStreamHasher() *StreamHasher { return &StreamHasher{h: sha256.New()} }

// WriteChunk adds one chunk, length-prefixed so chunk boundaries are part of
// the digest: [a][bc] and [ab][c] hash differently.
func (s *StreamHasher) WriteChunk(p []byte) {
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(p)))
	s.h.Write(l[:])
	s.h.Write(p)
}

// Sum returns the digest of all chunks written so far.
func (s *StreamHasher) Sum() []byte { return s.h.Sum(nil) }

// Hash is the one-shot SHA-256 used for request bodies.
func Hash(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}
