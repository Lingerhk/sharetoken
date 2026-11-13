package tdx

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/google/go-tdx-guest/testing/testdata"

	"sharetoken/internal/attest"
)

// testdata.RawQuote is a production TDX quote from a Sapphire Rapids machine,
// signed through Intel's real PCK chain. Its certificates were valid in 2023.
var quoteTime = time.Date(2023, time.July, 1, 1, 0, 0, 0, time.UTC)

func TestRealQuoteVerifiesAgainstIntelRoot(t *testing.T) {
	v := &Verifier{Now: func() time.Time { return quoteTime }}
	q := &attest.Quote{Backend: Backend, Evidence: testdata.RawQuote}
	c, err := v.Verify(q)
	if err != nil {
		t.Fatalf("Intel production quote: %v", err)
	}
	if len(c.Measurement) != 48 || c.Backend != Backend {
		t.Fatalf("claims %+v", c)
	}

	// The quote is genuine, but its REPORTDATA binds someone else's keys, so
	// it cannot vouch for an offer's keys.
	policy := &attest.Policy{Measurements: map[string]string{hex.EncodeToString(c.Measurement): "sample"}, AllowDebug: true}
	rk, _, _ := ed25519.GenerateKey(nil)
	if _, err := policy.Check(c, rk, make([]byte, 32)); !errors.Is(err, attest.ErrBindingMismatch) {
		t.Fatalf("want ErrBindingMismatch, got %v", err)
	}
}

func TestRejectsTamperedAndExpiredQuotes(t *testing.T) {
	v := &Verifier{Now: func() time.Time { return quoteTime }}
	tampered := bytes.Clone(testdata.RawQuote)
	tampered[48+200] ^= 1 // inside the signed TD report body
	if _, err := v.Verify(&attest.Quote{Backend: Backend, Evidence: tampered}); err == nil {
		t.Fatal("a quote altered after signing verified")
	}
	late := &Verifier{Now: func() time.Time { return time.Date(2053, 1, 1, 0, 0, 0, 0, time.UTC) }}
	if _, err := late.Verify(&attest.Quote{Backend: Backend, Evidence: testdata.RawQuote}); err == nil {
		t.Fatal("a quote verified after its certificates expired")
	}
	if _, err := v.Verify(&attest.Quote{Backend: "sev-snp", Evidence: testdata.RawQuote}); err == nil {
		t.Fatal("verified a quote labelled with another backend")
	}
}
