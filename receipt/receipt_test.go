package receipt

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
)

func newNode(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func sample(pub ed25519.PublicKey, req, resp []byte) Receipt {
	return Receipt{
		JobID:            "job-123",
		ModelID:          "llama-3.3-70b",
		NodeID:           pub,
		RequestHash:      Hash(req),
		ResponseHash:     Hash(resp),
		PromptTokens:     12,
		CompletionTokens: 345,
		UnixNano:         1_700_000_000_000_000_000,
		Nonce:            []byte("0123456789abcdef"),
	}
}

func TestSignVerifyAndBuyerCheck(t *testing.T) {
	pub, priv := newNode(t)
	req, resp := []byte("ciphertext-of-request"), []byte("the model response bytes")

	sr, err := Sign(priv, sample(pub, req, resp))
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(sr); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if err := BuyerCheck(sr, "job-123", "llama-3.3-70b", pub, Hash(req), Hash(resp)); err != nil {
		t.Fatalf("BuyerCheck: %v", err)
	}
}

func TestBuyerCheckCatchesAlteredResponse(t *testing.T) {
	pub, priv := newNode(t)
	req, resp := []byte("req"), []byte("honest response")
	sr, _ := Sign(priv, sample(pub, req, resp))
	if err := BuyerCheck(sr, "job-123", "llama-3.3-70b", pub, Hash(req), Hash([]byte("tampered"))); err == nil {
		t.Fatal("expected response hash mismatch")
	}
}

func TestBuyerCheckCatchesModelSubstitution(t *testing.T) {
	pub, priv := newNode(t)
	req, resp := []byte("req"), []byte("resp")
	sr, _ := Sign(priv, sample(pub, req, resp))
	if err := BuyerCheck(sr, "job-123", "some-other-model", pub, Hash(req), Hash(resp)); err == nil {
		t.Fatal("expected model id mismatch")
	}
}

func TestBuyerCheckCatchesWrongNode(t *testing.T) {
	// A different node signs a perfectly consistent receipt: still rejected,
	// because the buyer ticketed a specific node key.
	pub, _ := newNode(t)
	otherPub, otherPriv := newNode(t)
	req, resp := []byte("req"), []byte("resp")
	sr, _ := Sign(otherPriv, sample(otherPub, req, resp))
	if err := BuyerCheck(sr, "job-123", "llama-3.3-70b", pub, Hash(req), Hash(resp)); err == nil {
		t.Fatal("expected unexpected-node rejection")
	}
}

func TestVerifyCatchesForgedSignature(t *testing.T) {
	pub, priv := newNode(t)
	sr, _ := Sign(priv, sample(pub, []byte("req"), []byte("resp")))
	sr.Sig[0] ^= 0x01
	if err := Verify(sr); err == nil {
		t.Fatal("expected signature failure")
	}
}

func TestSignRejectsMismatchedKey(t *testing.T) {
	pub, _ := newNode(t)
	_, otherPriv := newNode(t)
	if _, err := Sign(otherPriv, sample(pub, []byte("r"), []byte("s"))); err == nil {
		t.Fatal("expected key/NodeID mismatch error")
	}
}

func TestSignatureSurvivesJSONRoundTrip(t *testing.T) {
	pub, priv := newNode(t)
	sr, _ := Sign(priv, sample(pub, []byte("r"), []byte("s")))
	raw, err := json.Marshal(sr)
	if err != nil {
		t.Fatal(err)
	}
	var back SignedReceipt
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if err := Verify(&back); err != nil {
		t.Fatalf("receipt no longer verifies after JSON round trip: %v", err)
	}
	if !bytes.Equal(back.Receipt.Digest(), sr.Receipt.Digest()) {
		t.Fatal("digest changed across JSON round trip")
	}
}

func TestStreamHasherBindsChunkBoundaries(t *testing.T) {
	a := NewStreamHasher()
	a.WriteChunk([]byte("a"))
	a.WriteChunk([]byte("bc"))
	b := NewStreamHasher()
	b.WriteChunk([]byte("ab"))
	b.WriteChunk([]byte("c"))
	if bytes.Equal(a.Sum(), b.Sum()) {
		t.Fatal("[a][bc] and [ab][c] must hash differently")
	}
}
