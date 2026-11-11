// Package flow is an end-to-end demonstration, as a test, of the two guarantees
// wired together: the coordinator routes only ciphertext (it never sees the
// prompt), and the buyer cryptographically verifies that the response it got is
// exactly what the node signed for the model it asked for.
//
// Run: go test ./flow -run Example -v
package flow

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	"sharetoken/internal/attest"
	"sharetoken/internal/box"
	"sharetoken/internal/receipt"
)

func TestEndToEnd(t *testing.T) {
	// --- Node setup: a stable receive key (X25519) and a signing identity ---
	// (Ed25519). In an attested deployment both keys are generated inside the
	// confidential VM and bound into its quote; here the mock backend stands in
	// for the hardware.
	nodeBox, _ := box.GenerateIdentity()
	nodeSignPub, nodeSignPriv, _ := ed25519.GenerateKey(rand.Reader)

	measurement := []byte("sha384:audited-node-image-v1")
	att, rootPub := attest.NewMockAttestor(measurement)
	quote, err := att.Attest(attest.ReportData(nodeSignPub, nodeBox.Public()))
	if err != nil {
		t.Fatal(err)
	}

	// Buyer verifies, once, that both of this node's keys live in the expected
	// audited image. From here a valid receipt signature means the audited code
	// produced the output and the prompt was only decrypted inside it.
	policy := &attest.Policy{Measurements: map[string]string{hex.EncodeToString(measurement): "audited image"}}
	verifier := attest.Multi{attest.MockBackend: attest.MockVerifier{RootPub: rootPub}}
	if _, _, err := attest.Verify(verifier, policy, quote, nodeSignPub, nodeBox.Public()); err != nil {
		t.Fatalf("attestation: %v", err)
	}

	const jobID = "job-abc"
	const modelID = "llama-3.3-70b-instruct"
	aad := []byte(jobID + "|" + modelID)

	// --- Buyer seals the prompt to the node. Whoever carries these bytes (a ---
	// relay, the network) cannot read them.
	prompt := []byte("Confidential: review this merger agreement and flag risks.")
	sealedReq, err := box.Seal(nodeBox.Public(), prompt, aad)
	if err != nil {
		t.Fatal(err)
	}
	// Buyer's ephemeral key to receive the response privately.
	buyerBox, _ := box.GenerateIdentity()

	// ===== what a relay sees =====
	// sealedReq + buyerBox.Public() inside libp2p's encrypted channel. No plaintext.

	// --- Node opens, "runs the model", and builds a signed receipt. ---
	gotPrompt, err := nodeBox.Open(sealedReq, aad)
	if err != nil {
		t.Fatalf("node decrypt: %v", err)
	}
	response := runModel(gotPrompt) // stand-in for the upstream call

	// Node seals the response back to the buyer and signs the transcript. The
	// hashes are over the exact bytes on the wire (here: the ciphertexts).
	sealedResp, err := box.Seal(buyerBox.Public(), response, aad)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, 16)
	rand.Read(nonce)
	sr, err := receipt.Sign(nodeSignPriv, receipt.Receipt{
		JobID:            jobID,
		ModelID:          modelID,
		NodeID:           nodeSignPub,
		RequestHash:      receipt.Hash(sealedReq),
		ResponseHash:     receipt.Hash(sealedResp),
		PromptTokens:     uint64(len(gotPrompt) / 4),
		CompletionTokens: uint64(len(response) / 4),
		UnixNano:         time.Now().UnixNano(),
		Nonce:            nonce,
	})
	if err != nil {
		t.Fatal(err)
	}

	// --- Buyer opens the response and verifies the receipt against the exact ---
	// bytes it sent and received.
	gotResp, err := buyerBox.Open(sealedResp, aad)
	if err != nil {
		t.Fatalf("buyer decrypt: %v", err)
	}
	if err := receipt.BuyerCheck(sr, jobID, modelID, nodeSignPub, receipt.Hash(sealedReq), receipt.Hash(sealedResp)); err != nil {
		t.Fatalf("buyer verification failed: %v", err)
	}
	if string(gotResp) != string(response) {
		t.Fatal("response round-trip mismatch")
	}

	// --- Negative: a node that alters the delivered response after signing is ---
	// caught, because the buyer's local hash no longer matches the receipt.
	tampered := receipt.Hash([]byte("a cheaper-model answer"))
	if err := receipt.BuyerCheck(sr, jobID, modelID, nodeSignPub, receipt.Hash(sealedReq), tampered); err == nil {
		t.Fatal("expected tamper to be detected")
	}
}

func runModel(prompt []byte) []byte {
	return append([]byte("ANSWER to: "), prompt...)
}
