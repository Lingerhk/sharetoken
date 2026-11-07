package attest

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"

	"sharetoken/internal/canon"
)

// MockBackend names development quotes.
const MockBackend = "mock"

// MockAttestor is a DEVELOPMENT-ONLY stand-in for TEE hardware: an Ed25519
// "manufacturer" key plays the vendor root and signs (measurement, report
// data). It exercises the whole attested flow on a laptop, with no isolation
// whatsoever. Never put a MockVerifier in a production verifier set.
type MockAttestor struct {
	Measurement []byte
	root        ed25519.PrivateKey
}

// NewMockAttestor builds a mock backend with a fresh root key; give rootPub to
// the matching MockVerifier.
func NewMockAttestor(measurement []byte) (a *MockAttestor, rootPub ed25519.PublicKey) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	return &MockAttestor{Measurement: measurement, root: priv}, pub
}

type mockEvidence struct {
	Measurement []byte `json:"measurement"`
	ReportData  []byte `json:"report_data"`
	Sig         []byte `json:"sig"`
}

func mockSigned(measurement, reportData []byte) []byte {
	return canon.New("sharetoken/attest/mock/v1").Bytes(measurement).Bytes(reportData).Sum()
}

// Backend implements Attestor.
func (m *MockAttestor) Backend() string { return MockBackend }

// Attest implements Attestor.
func (m *MockAttestor) Attest(reportData [64]byte) (*Quote, error) {
	ev, err := json.Marshal(mockEvidence{
		Measurement: m.Measurement,
		ReportData:  reportData[:],
		Sig:         ed25519.Sign(m.root, mockSigned(m.Measurement, reportData[:])),
	})
	if err != nil {
		return nil, err
	}
	return &Quote{Backend: MockBackend, Evidence: ev}, nil
}

// MockVerifier verifies MockAttestor quotes against the dev root key.
type MockVerifier struct {
	RootPub ed25519.PublicKey
}

// Verify implements Verifier.
func (v MockVerifier) Verify(q *Quote) (*Claims, error) {
	if q == nil || q.Backend != MockBackend {
		return nil, errors.New("attest: not a mock quote")
	}
	var ev mockEvidence
	if err := json.Unmarshal(q.Evidence, &ev); err != nil || len(ev.ReportData) != 64 {
		return nil, errors.New("attest: malformed mock quote")
	}
	if len(v.RootPub) != ed25519.PublicKeySize || !ed25519.Verify(v.RootPub, mockSigned(ev.Measurement, ev.ReportData), ev.Sig) {
		return nil, errors.New("attest: mock quote signature invalid")
	}
	c := &Claims{Backend: MockBackend, Measurement: ev.Measurement, Detail: "mock (development only)"}
	copy(c.ReportData[:], ev.ReportData)
	return c, nil
}
