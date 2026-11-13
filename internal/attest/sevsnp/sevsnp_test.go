package sevsnp

import (
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	sevabi "github.com/google/go-sev-guest/abi"
	sevtest "github.com/google/go-sev-guest/testing"
	"github.com/google/go-sev-guest/verify/trust"
	"google.golang.org/protobuf/proto"

	"sharetoken/internal/attest"
)

// fakeAMD is a test-only AMD key hierarchy (ARK -> ASK -> VCEK) from
// go-sev-guest. Verification runs the real chain and signature checks
// against it instead of against AMD's production roots.
type fakeAMD struct {
	signer *sevtest.AmdSigner
	roots  map[string][]*trust.AMDRootCerts
}

func newFakeAMD(t *testing.T) *fakeAMD {
	t.Helper()
	signer, err := sevtest.DefaultTestOnlyCertChain(sevtest.GetProductName(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	line := sevtest.GetProductLine()
	return &fakeAMD{signer: signer, roots: map[string][]*trust.AMDRootCerts{line: {{
		Product:      line,
		ProductLine:  line,
		ProductCerts: &trust.ProductCerts{Ark: signer.Ark, Ask: signer.Ask, Asvk: signer.Asvk},
	}}}}
}

// quote builds a signed report as the AMD secure processor would.
func (f *fakeAMD) quote(t *testing.T, reportData [64]byte, measurement []byte, policy sevabi.SnpPolicy, tamper bool) *attest.Quote {
	t.Helper()
	raw := sevtest.CreateRawReport(&sevtest.TestReportOptions{
		ReportData: reportData[:],
		FMS:        sevabi.MaskedCpuid1EaxFromSevProduct(sevabi.DefaultSevProduct()),
		Version:    3,
	})
	binary.LittleEndian.PutUint64(raw[0x08:0x10], sevabi.SnpPolicyToBytes(policy))
	copy(raw[0x90:0xC0], measurement)
	report := raw[:sevabi.ReportSize]
	r, s, err := f.signer.Sign(sevabi.SignedComponent(report))
	if err != nil {
		t.Fatal(err)
	}
	if err := sevabi.SetSignature(r, s, report); err != nil {
		t.Fatal(err)
	}
	if tamper {
		report[0x90] ^= 1 // change the measurement after signing
	}
	table, err := f.signer.CertTableBytes()
	if err != nil {
		t.Fatal(err)
	}
	att, err := sevabi.ReportCertsToProto(append(append([]byte{}, report...), table...))
	if err != nil {
		t.Fatal(err)
	}
	ev, err := proto.Marshal(att)
	if err != nil {
		t.Fatal(err)
	}
	return &attest.Quote{Backend: Backend, Evidence: ev}
}

func testKeys() (receiptKey, boxPub []byte) {
	pub, _, _ := ed25519.GenerateKey(nil)
	box := make([]byte, 32)
	box[3] = 1
	return pub, box
}

func TestVerifiedReportPassesPolicy(t *testing.T) {
	amd := newFakeAMD(t)
	rk, bp := testKeys()
	measurement := make([]byte, 48)
	measurement[0] = 0xAB
	q := amd.quote(t, attest.ReportData(rk, bp), measurement, sevabi.SnpPolicy{SMT: true}, false)

	v := &Verifier{TrustedRoots: amd.roots}
	c, err := v.Verify(q)
	if err != nil {
		t.Fatal(err)
	}
	if c.Debug || c.Backend != Backend || hex.EncodeToString(c.Measurement) != hex.EncodeToString(measurement) {
		t.Fatalf("unexpected claims %+v", c)
	}
	policy := &attest.Policy{Measurements: map[string]string{hex.EncodeToString(measurement): "node image v1"}}
	if _, label, err := attest.Verify(attest.Multi{Backend: v}, policy, q, rk, bp); err != nil || label != "node image v1" {
		t.Fatalf("policy: %q %v", label, err)
	}
	otherRK, _ := testKeys()
	if _, _, err := attest.Verify(attest.Multi{Backend: v}, policy, q, otherRK, bp); !errors.Is(err, attest.ErrBindingMismatch) {
		t.Fatalf("binding: want ErrBindingMismatch, got %v", err)
	}
}

func TestRejectsTamperedDebugMigratableAndUntrustedReports(t *testing.T) {
	amd := newFakeAMD(t)
	rk, bp := testKeys()
	rd := attest.ReportData(rk, bp)
	m := make([]byte, 48)
	v := &Verifier{TrustedRoots: amd.roots}

	if _, err := v.Verify(amd.quote(t, rd, m, sevabi.SnpPolicy{}, true)); err == nil {
		t.Fatal("a report altered after signing verified")
	}

	c, err := v.Verify(amd.quote(t, rd, m, sevabi.SnpPolicy{Debug: true}, false))
	if err != nil {
		t.Fatal(err)
	}
	policy := &attest.Policy{Measurements: map[string]string{hex.EncodeToString(m): "img"}}
	if _, err := policy.Check(c, rk, bp); !errors.Is(err, attest.ErrDebugGuest) {
		t.Fatalf("debug guest: want ErrDebugGuest, got %v", err)
	}

	if _, err := v.Verify(amd.quote(t, rd, m, sevabi.SnpPolicy{MigrateMA: true}, false)); err == nil {
		t.Fatal("a guest that allows a migration agent verified")
	}

	// The zero Verifier trusts only AMD's real roots, not the test hierarchy.
	if _, err := (&Verifier{}).Verify(amd.quote(t, rd, m, sevabi.SnpPolicy{}, false)); err == nil {
		t.Fatal("a report from a non-AMD key hierarchy verified against AMD's roots")
	}
}
