// Package sevsnp verifies AMD SEV-SNP attestation reports for package attest.
//
// Evidence is a serialized go-sev-guest Attestation: the 1184-byte report the
// AMD secure processor signed (ECDSA P-384 with the chip's VCEK) plus the VCEK
// certificate and the ASK/ARK chain. Verification checks that chain up to
// AMD's root keys (embedded in go-sev-guest), the report signature, and that
// the guest policy forbids a migration agent; Claims then carry the launch
// MEASUREMENT, REPORT_DATA and the debug bit for attest.Policy to judge.
//
// A node's Attestor (package internal/attest/hw) ships the full certificate chain, so
// verifying needs no network unless revocation checking is turned on.
package sevsnp

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/go-sev-guest/abi"
	spb "github.com/google/go-sev-guest/proto/sevsnp"
	"github.com/google/go-sev-guest/verify"
	"github.com/google/go-sev-guest/verify/trust"
	"google.golang.org/protobuf/proto"

	"sharetoken/internal/attest"
)

// Backend is the quote backend name.
const Backend = "sev-snp"

// Verifier checks SEV-SNP quotes. The zero value trusts AMD's published roots
// and works offline.
type Verifier struct {
	// TrustedRoots replaces AMD's root certificates. Only tests set it.
	TrustedRoots map[string][]*trust.AMDRootCerts
	// CheckRevocations downloads AMD's CRL to check the VCEK and ASK.
	CheckRevocations bool
	// FetchMissingCerts downloads a VCEK the quote lacks from AMD's KDS.
	FetchMissingCerts bool
	// Now is the clock for certificate validity; nil means time.Now.
	Now func() time.Time
}

// Verify implements attest.Verifier.
func (v *Verifier) Verify(q *attest.Quote) (*attest.Claims, error) {
	if q == nil || q.Backend != Backend {
		return nil, errors.New("sev-snp: not an SEV-SNP quote")
	}
	var att spb.Attestation
	if err := proto.Unmarshal(q.Evidence, &att); err != nil || att.GetReport() == nil {
		return nil, errors.New("sev-snp: malformed attestation")
	}
	now := time.Now()
	if v.Now != nil {
		now = v.Now()
	}
	opts := &verify.Options{
		TrustedRoots:        v.TrustedRoots,
		CheckRevocations:    v.CheckRevocations,
		DisableCertFetching: !v.FetchMissingCerts && !v.CheckRevocations,
		Getter:              trust.DefaultHTTPSGetter(),
		Now:                 now,
	}
	if err := verify.SnpAttestation(&att, opts); err != nil {
		return nil, fmt.Errorf("sev-snp: %w", err)
	}
	r := att.GetReport()
	policy, err := abi.ParseSnpPolicy(r.GetPolicy())
	if err != nil {
		return nil, fmt.Errorf("sev-snp: %w", err)
	}
	if policy.MigrateMA {
		// A migration agent can export guest memory: not confidential.
		return nil, errors.New("sev-snp: guest policy allows a migration agent")
	}
	c := &attest.Claims{
		Backend:     Backend,
		Measurement: r.GetMeasurement(),
		Debug:       policy.Debug,
		Detail:      fmt.Sprintf("vmpl=%d reported_tcb=%#x", r.GetVmpl(), r.GetReportedTcb()),
	}
	copy(c.ReportData[:], r.GetReportData())
	return c, nil
}
