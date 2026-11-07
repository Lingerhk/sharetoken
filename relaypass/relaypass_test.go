package relaypass

import (
	"crypto/ed25519"
	"errors"
	"testing"
	"time"
)

func TestPassVerification(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	otherPub, otherPriv, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1_800_000_000, 0)
	p := Issue(priv, "peerA", "user:1", now, time.Hour)
	keys := []ed25519.PublicKey{otherPub, pub} // key rotation: any trusted key

	if err := p.Verify(keys, "peerA", now.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := p.Verify(keys, "peerB", now); !errors.Is(err, ErrWrongPeer) {
		t.Fatalf("other peer: %v", err)
	}
	if err := p.Verify(keys, "peerA", now.Add(time.Hour)); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired: %v", err)
	}
	if err := p.Verify([]ed25519.PublicKey{otherPub}, "peerA", now); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("untrusted portal: %v", err)
	}
	forged := *p
	forged.Account = "user:2"
	if err := forged.Verify(keys, "peerA", now); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("tampered account: %v", err)
	}
	stretched := *p
	stretched.ExpiresAt += int64(24 * time.Hour)
	if err := stretched.Verify(keys, "peerA", now.Add(2*time.Hour)); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("extended expiry: %v", err)
	}
	future := Issue(otherPriv, "peerA", "user:1", now.Add(time.Hour), time.Hour)
	if err := future.Verify(keys, "peerA", now); !errors.Is(err, ErrMalformed) {
		t.Fatalf("issued in the future: %v", err)
	}

	k, err := ParseKey(FormatKey(pub))
	if err != nil || !k.Equal(pub) {
		t.Fatalf("key round trip: %v", err)
	}
	if _, err := ParseKey("ed25519:abcd"); err == nil {
		t.Fatal("short key accepted")
	}
}
