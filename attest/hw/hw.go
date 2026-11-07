// Package hw produces attestation quotes from inside a confidential VM. Only
// a node running in a TEE imports it.
//
// Both backends use the Linux guest drivers through Google's go-sev-guest and
// go-tdx-guest clients (configfs-tsm on kernels >= 6.7, the legacy device
// ioctl otherwise). On any other platform New returns an error.
package hw

import (
	"errors"
	"fmt"
	"os"

	sevclient "github.com/google/go-sev-guest/client"
	sevverify "github.com/google/go-sev-guest/verify"
	tdxclient "github.com/google/go-tdx-guest/client"
	"google.golang.org/protobuf/proto"

	"sharetoken/attest"
	"sharetoken/attest/sevsnp"
	"sharetoken/attest/tdx"
)

// New returns an attestor for backend: "sev-snp", "tdx", or "auto" to detect
// the TEE this process runs in.
func New(backend string) (attest.Attestor, error) {
	if backend == "auto" {
		switch {
		case exists("/dev/sev-guest"):
			backend = sevsnp.Backend
		case exists("/dev/tdx_guest"):
			backend = tdx.Backend
		default:
			return nil, errors.New("attest: no SEV-SNP or TDX guest device found; is this a confidential VM?")
		}
	}
	switch backend {
	case sevsnp.Backend:
		qp, err := sevclient.GetQuoteProvider()
		if err != nil {
			return nil, fmt.Errorf("attest: sev-snp: %w", err)
		}
		if !qp.IsSupported() {
			return nil, errors.New("attest: sev-snp guest attestation is not available")
		}
		return &snp{qp: qp}, nil
	case tdx.Backend:
		qp, err := tdxclient.GetQuoteProvider()
		if err != nil {
			return nil, fmt.Errorf("attest: tdx: %w", err)
		}
		if err := qp.IsSupported(); err != nil {
			return nil, fmt.Errorf("attest: tdx: %w", err)
		}
		return &tdxAttestor{qp: qp}, nil
	}
	return nil, fmt.Errorf("attest: unknown backend %q (want sev-snp, tdx or auto)", backend)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

type snp struct{ qp sevclient.QuoteProvider }

func (*snp) Backend() string { return sevsnp.Backend }

// Attest asks the AMD secure processor for a report over reportData, then
// completes its certificate chain from AMD's KDS if the host did not supply
// it and verifies the result, so verifiers need no network.
func (a *snp) Attest(reportData [64]byte) (*attest.Quote, error) {
	att, err := sevclient.GetQuoteProto(a.qp, reportData)
	if err != nil {
		return nil, fmt.Errorf("attest: sev-snp report: %w", err)
	}
	if err := sevverify.SnpAttestation(att, sevverify.DefaultOptions()); err != nil {
		return nil, fmt.Errorf("attest: sev-snp self-check: %w", err)
	}
	ev, err := proto.Marshal(att)
	if err != nil {
		return nil, err
	}
	return &attest.Quote{Backend: sevsnp.Backend, Evidence: ev}, nil
}

type tdxAttestor struct{ qp tdxclient.QuoteProvider }

func (*tdxAttestor) Backend() string { return tdx.Backend }

// Attest asks the TDX module and quoting enclave for a quote over reportData.
// The quote embeds its PCK certificate chain.
func (a *tdxAttestor) Attest(reportData [64]byte) (*attest.Quote, error) {
	raw, err := tdxclient.GetRawQuote(a.qp, reportData)
	if err != nil {
		return nil, fmt.Errorf("attest: tdx quote: %w", err)
	}
	q := &attest.Quote{Backend: tdx.Backend, Evidence: raw}
	if _, err := (&tdx.Verifier{}).Verify(q); err != nil {
		return nil, fmt.Errorf("attest: tdx self-check: %w", err)
	}
	return q, nil
}
