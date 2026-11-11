// Package tdx verifies Intel TDX quotes (version 4) for package attest.
//
// Evidence is the raw quote the TD obtained through configfs-tsm: the TD
// report body (MRTD, RTMR0-3, REPORTDATA, TD attributes) signed by the
// Quoting Enclave's attestation key, which the PCK certificate chain embedded
// in the quote ties to Intel's SGX root CA (embedded in go-tdx-guest).
//
// The measurement policies allow-list is Measurement(body): one digest over
// the firmware (MRTD) and the boot chain (RTMR0-2). RTMR3 is excluded because
// the running guest may extend it.
//
// By default only the signature chain is checked, offline. Turning on
// GetCollateral also fetches TCB info and QE identity from Intel PCS and
// rejects platforms whose TCB is out of date.
package tdx

import (
	"crypto/sha512"
	"crypto/x509"
	"errors"
	"fmt"
	"time"

	"github.com/google/go-tdx-guest/abi"
	pb "github.com/google/go-tdx-guest/proto/tdx"
	"github.com/google/go-tdx-guest/verify"
	"github.com/google/go-tdx-guest/verify/trust"

	"sharetoken/internal/attest"
)

// Backend is the quote backend name.
const Backend = "tdx"

// Verifier checks TDX quotes. The zero value trusts Intel's root and works
// offline.
type Verifier struct {
	// TrustedRoots replaces Intel's root CA. Only tests set it.
	TrustedRoots *x509.CertPool
	// GetCollateral fetches TCB info and QE identity from Intel PCS and checks
	// the platform's TCB status.
	GetCollateral bool
	// CheckRevocations fetches Intel's CRLs.
	CheckRevocations bool
	// Now is the clock for certificate validity; nil means time.Now.
	Now func() time.Time
}

// Verify implements attest.Verifier.
func (v *Verifier) Verify(q *attest.Quote) (*attest.Claims, error) {
	if q == nil || q.Backend != Backend {
		return nil, errors.New("tdx: not a TDX quote")
	}
	parsed, err := abi.QuoteToProto(q.Evidence)
	if err != nil {
		return nil, fmt.Errorf("tdx: %w", err)
	}
	quote, ok := parsed.(*pb.QuoteV4)
	if !ok {
		return nil, fmt.Errorf("tdx: unsupported quote format %T", parsed)
	}
	now := time.Now()
	if v.Now != nil {
		now = v.Now()
	}
	opts := &verify.Options{
		GetCollateral:    v.GetCollateral,
		CheckRevocations: v.CheckRevocations,
		Getter:           trust.DefaultHTTPSGetter(),
		Now:              now,
		TrustedRoots:     v.TrustedRoots,
	}
	if err := verify.TdxQuote(quote, opts); err != nil {
		return nil, fmt.Errorf("tdx: %w", err)
	}
	body := quote.GetTdQuoteBody()
	attrs := body.GetTdAttributes()
	c := &attest.Claims{
		Backend:     Backend,
		Measurement: Measurement(body),
		Debug:       len(attrs) > 0 && attrs[0]&1 != 0, // TUD.DEBUG
		Detail:      fmt.Sprintf("mrtd=%x", body.GetMrTd()),
	}
	copy(c.ReportData[:], body.GetReportData())
	return c, nil
}

// Measurement is the image digest a TDX policy allow-lists:
// SHA-384(MRTD || RTMR0 || RTMR1 || RTMR2).
func Measurement(body *pb.TDQuoteBody) []byte {
	h := sha512.New384()
	h.Write(body.GetMrTd())
	for i, r := range body.GetRtmrs() {
		if i < 3 {
			h.Write(r)
		}
	}
	return h.Sum(nil)
}
