package attest

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func keysForTest() (receiptKey, boxPub []byte) {
	pub, _, _ := ed25519.GenerateKey(nil)
	box := make([]byte, 32)
	box[0] = 7
	return pub, box
}

func TestMockQuotePassesPolicyBoundToBothKeys(t *testing.T) {
	measurement := []byte("audited-node-image-v1")
	att, root := NewMockAttestor(measurement)
	v := Multi{MockBackend: MockVerifier{RootPub: root}}
	policy := &Policy{Measurements: map[string]string{hex.EncodeToString(measurement): "node v1"}}

	rk, bp := keysForTest()
	q, err := att.Attest(ReportData(rk, bp))
	if err != nil {
		t.Fatal(err)
	}
	if _, label, err := Verify(v, policy, q, rk, bp); err != nil || label != "node v1" {
		t.Fatalf("Verify = %q, %v", label, err)
	}

	// The quote binds the box key too: an operator who keeps the box key
	// outside the guest cannot reuse the guest's quote.
	otherBox := append([]byte{}, bp...)
	otherBox[1] = 9
	if _, _, err := Verify(v, policy, q, rk, otherBox); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("swapped box key: want ErrBindingMismatch, got %v", err)
	}
	otherReceipt, _ := keysForTest()
	if _, _, err := Verify(v, policy, q, otherReceipt, bp); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("swapped receipt key: want ErrBindingMismatch, got %v", err)
	}
}

func TestPolicyRejectsUnknownImagesForgedQuotesAndUnknownBackends(t *testing.T) {
	att, root := NewMockAttestor([]byte("unreviewed-image"))
	policy := &Policy{Measurements: map[string]string{hex.EncodeToString([]byte("audited")): "audited"}}
	rk, bp := keysForTest()
	q, _ := att.Attest(ReportData(rk, bp))

	if _, _, err := Verify(Multi{MockBackend: MockVerifier{RootPub: root}}, policy, q, rk, bp); !errors.Is(err, ErrMeasurementUnknown) {
		t.Fatalf("want ErrMeasurementUnknown, got %v", err)
	}
	_, otherRoot := NewMockAttestor(nil)
	if _, _, err := Verify(Multi{MockBackend: MockVerifier{RootPub: otherRoot}}, policy, q, rk, bp); err == nil {
		t.Fatal("a quote from an untrusted root verified")
	}
	if _, _, err := Verify(Multi{}, policy, q, rk, bp); !errors.Is(err, ErrUnknownBackend) {
		t.Fatalf("a mock quote must not verify without an explicitly trusted mock root: %v", err)
	}
	if _, _, err := Verify(Multi{}, policy, nil, rk, bp); !errors.Is(err, ErrNoQuote) {
		t.Fatalf("want ErrNoQuote, got %v", err)
	}
}

func TestLoadPolicyNormalizesMeasurements(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(`{"measurements":{"0xABCD":"image a"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.Measurements["abcd"] != "image a" {
		t.Fatalf("measurement not normalized: %v", p.Measurements)
	}
	if err := os.WriteFile(path, []byte(`{"measurements":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPolicy(path); err == nil {
		t.Fatal("an empty allow-list must be rejected rather than trusting nothing silently")
	}
}
