// Package attest is the hardware-attestation layer: it lets a buyer check that
// a node's keys live inside a confidential VM running a known, audited image.
//
// # WHAT IT GIVES A BUYER
//
// A node running in a TEE (AMD SEV-SNP or Intel TDX confidential VM) generates
// its receipt-signing key and its box key in guest memory the host cannot read,
// and asks the CPU for a quote over
//
//	ReportData = SHA-512(canon("sharetoken/attest/v1", receiptKey, boxPub))
//
// The CPU signs that together with the guest's launch measurement, a hash of
// the firmware, kernel, initrd and command line it booted. A buyer who verifies
// the quote up to the vendor's root key, finds the measurement in its
// allow-list, and recomputes ReportData from the keys in the node's offer knows:
//
//   - the prompt it seals to boxPub is decrypted only inside that image, so the
//     node operator cannot read it (the operator becomes untrusted), and
//   - a receipt signed with receiptKey was produced by that image, so the
//     claimed model is whatever the audited image serves.
//
// Both keys are bound, not just the signing key: binding only the receipt key
// would let a host keep the box key outside the VM and read prompts while the
// enclave signs receipts.
//
// # WHAT IT DOES NOT GIVE
//
//   - It is only as good as the image: the allow-listed measurement must belong
//     to a reproducible build that keeps keys in memory and serves a pinned
//     model. Package-level code cannot check that; publishing measurements is
//     an operational process.
//   - GPU memory is outside a CPU TEE. For confidential GPU inference the GPU
//     must run in confidential-computing mode (NVIDIA H100 and later) with its
//     own attestation; until that is verified too, treat GPU nodes as CPU-TEE
//     attested only.
//   - Side channels and vendor firmware bugs are out of scope (keep the
//     verifier libraries and TCB minimums current).
//
// Verification is split: a backend Verifier checks the hardware signature
// chain and returns Claims; Policy then decides whether to trust those claims
// for a given offer. Backends live in internal/attest/sevsnp and internal/attest/tdx so the core
// package (imported by the wire protocol) carries no vendor dependencies.
package attest

import (
	"bytes"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"sharetoken/internal/canon"
)

// Quote is a node's attestation evidence, carried in its signed offer.
type Quote struct {
	Backend  string `json:"backend"`  // "sev-snp", "tdx", or "mock" (development only)
	Evidence []byte `json:"evidence"` // backend-specific, signed by the hardware
}

// ReportData is the value a node binds into its quote: both of its offer keys.
func ReportData(receiptKey, boxPub []byte) [64]byte {
	return sha512.Sum512(canon.New("sharetoken/attest/v1").Bytes(receiptKey).Bytes(boxPub).Sum())
}

// Claims are the facts a hardware-verified quote attests to.
type Claims struct {
	Backend     string
	Measurement []byte   // launch measurement of the guest image
	ReportData  [64]byte // the guest-chosen value signed with it
	Debug       bool     // a debuggable guest exposes its memory to the host
	Detail      string   // backend specifics (product, TCB), informational
}

// Verifier checks a quote's signature chain up to the hardware vendor's root
// and returns what it attests. It applies no policy.
type Verifier interface {
	Verify(q *Quote) (*Claims, error)
}

// Attestor produces quotes from inside a TEE guest.
type Attestor interface {
	Backend() string
	Attest(reportData [64]byte) (*Quote, error)
}

// Errors returned by policy checks.
var (
	ErrUnknownBackend     = errors.New("attest: no verifier for this backend")
	ErrMeasurementUnknown = errors.New("attest: guest measurement is not in the allow-list")
	ErrDebugGuest         = errors.New("attest: guest is debuggable")
	ErrBindingMismatch    = errors.New("attest: quote is not bound to this offer's keys")
	ErrNoQuote            = errors.New("attest: offer carries no quote")
)

// Policy decides which verified guests to trust.
type Policy struct {
	// Measurements allow-lists guest images: lowercase hex measurement -> label.
	Measurements map[string]string `json:"measurements"`
	// AllowDebug accepts debuggable guests. Never set it outside testing.
	AllowDebug bool `json:"allow_debug,omitempty"`
}

// LoadPolicy reads a JSON policy file:
//
//	{"measurements": {"<hex launch measurement>": "sharetoken-node v1 / qwen2.5-7b"}}
func LoadPolicy(path string) (*Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Policy
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("attest: policy %s: %w", path, err)
	}
	norm := make(map[string]string, len(p.Measurements))
	for m, label := range p.Measurements {
		b, err := hex.DecodeString(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(m)), "0x"))
		if err != nil || len(b) == 0 {
			return nil, fmt.Errorf("attest: policy %s: bad measurement %q", path, m)
		}
		norm[hex.EncodeToString(b)] = label
	}
	p.Measurements = norm
	if len(p.Measurements) == 0 {
		return nil, fmt.Errorf("attest: policy %s allows no measurements", path)
	}
	return &p, nil
}

// Check applies the policy to claims presented for an offer with the given
// keys and returns the allow-list label of the image.
func (p *Policy) Check(c *Claims, receiptKey, boxPub []byte) (string, error) {
	want := ReportData(receiptKey, boxPub)
	if !bytes.Equal(c.ReportData[:], want[:]) {
		return "", ErrBindingMismatch
	}
	if c.Debug && !p.AllowDebug {
		return "", ErrDebugGuest
	}
	label, ok := p.Measurements[hex.EncodeToString(c.Measurement)]
	if !ok {
		return "", fmt.Errorf("%w: %x", ErrMeasurementUnknown, c.Measurement)
	}
	return label, nil
}

// Verify is the single check buyers and coordinators run on an offer's quote:
// hardware chain, then key binding, debug flag and measurement allow-list.
func Verify(v Verifier, p *Policy, q *Quote, receiptKey, boxPub []byte) (*Claims, string, error) {
	if q == nil {
		return nil, "", ErrNoQuote
	}
	c, err := v.Verify(q)
	if err != nil {
		return nil, "", err
	}
	label, err := p.Check(c, receiptKey, boxPub)
	if err != nil {
		return nil, "", err
	}
	return c, label, nil
}

// Multi dispatches verification on the quote's backend.
type Multi map[string]Verifier

// Verify implements Verifier.
func (m Multi) Verify(q *Quote) (*Claims, error) {
	v, ok := m[q.Backend]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownBackend, q.Backend)
	}
	c, err := v.Verify(q)
	if err != nil {
		return nil, err
	}
	if c.Backend != q.Backend {
		return nil, fmt.Errorf("attest: %s verifier returned %s claims", q.Backend, c.Backend)
	}
	return c, nil
}
