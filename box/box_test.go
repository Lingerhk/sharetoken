package box

import (
	"bytes"
	"testing"
)

func TestSealOpenRoundTrip(t *testing.T) {
	node, err := GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte("private prompt: summarise the attached contract")
	aad := []byte("job-123|claude-sonnet")

	sealed, err := Seal(node.Public(), msg, aad)
	if err != nil {
		t.Fatal(err)
	}
	got, err := node.Open(sealed, aad)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, msg) {
		t.Fatalf("round trip mismatch: got %q want %q", got, msg)
	}
}

func TestOpenFailsOnWrongAAD(t *testing.T) {
	node, _ := GenerateIdentity()
	sealed, _ := Seal(node.Public(), []byte("secret"), []byte("job-123"))
	if _, err := node.Open(sealed, []byte("job-999")); err == nil {
		t.Fatal("expected auth failure on mismatched aad, got nil")
	}
}

func TestOpenFailsOnWrongRecipient(t *testing.T) {
	node, _ := GenerateIdentity()
	other, _ := GenerateIdentity()
	sealed, _ := Seal(node.Public(), []byte("secret"), nil)
	if _, err := other.Open(sealed, nil); err == nil {
		t.Fatal("expected failure decrypting with wrong key, got nil")
	}
}

func TestOpenFailsOnTamper(t *testing.T) {
	node, _ := GenerateIdentity()
	sealed, _ := Seal(node.Public(), []byte("secret"), nil)
	sealed[len(sealed)-1] ^= 0x01 // flip a ciphertext bit
	if _, err := node.Open(sealed, nil); err == nil {
		t.Fatal("expected failure on tampered ciphertext, got nil")
	}
}

func TestSeedIsStable(t *testing.T) {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i)
	}
	a, err := IdentityFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := IdentityFromSeed(seed)
	if !bytes.Equal(a.Public(), b.Public()) {
		t.Fatal("same seed produced different public keys")
	}
}

func TestSealFromLetsTheSenderKeyOpenItsOwnMessage(t *testing.T) {
	node, _ := GenerateIdentity()
	job, _ := GenerateIdentity()
	aad := []byte("job-1")
	sealed, err := SealFrom(job, node.Public(), []byte("prompt"), aad)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sealed[:PublicKeySize], job.Public()) {
		t.Fatal("a SealFrom ciphertext must start with the sender's public key")
	}
	if got, err := node.Open(sealed, aad); err != nil || string(got) != "prompt" {
		t.Fatalf("recipient open: %q %v", got, err)
	}
	// The disclosed job key alone (as a dispute judge would rebuild it) opens it.
	judgeKey, err := IdentityFromSeed(job.Secret())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := judgeKey.OpenSent(node.Public(), sealed, aad); err != nil || string(got) != "prompt" {
		t.Fatalf("sender open: %q %v", got, err)
	}
	other, _ := GenerateIdentity()
	if _, err := other.OpenSent(node.Public(), sealed, aad); err == nil {
		t.Fatal("another key opened a message it did not send")
	}
	if _, err := judgeKey.OpenSent(other.Public(), sealed, aad); err == nil {
		t.Fatal("opened against the wrong recipient")
	}
	if _, err := judgeKey.OpenSent(node.Public(), sealed, []byte("job-2")); err == nil {
		t.Fatal("opened under the wrong aad")
	}
}
