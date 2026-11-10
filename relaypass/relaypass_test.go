package relaypass

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// relayed is p as a relay receives it: through JSON.
func relayed(t *testing.T, p *Pass) *Pass {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var out Pass
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return &out
}

// The wire form carries the signed body and the signature; the claims come out
// of the body on arrival, the client's build included.
func TestPassWireForm(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1_800_000_000, 0)
	want := Client{Version: "1.2.3-dev.abc", OS: "windows", Arch: "amd64"}
	p := Issue(priv, "peerA", "user:1", want, now, time.Hour)
	b, _ := json.Marshal(p)
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil || len(top) != 2 || top["body"] == nil || top["sig"] == nil {
		t.Fatalf("wire form %s", b)
	}
	got := relayed(t, p)
	if err := got.Verify([]ed25519.PublicKey{pub}, "peerA", now); err != nil {
		t.Fatal(err)
	}
	if got.Client != want || got.Account != "user:1" || !got.Expires().Equal(now.Add(time.Hour)) {
		t.Fatalf("claims %+v", got.Claims)
	}
}

// A portal may sign claims a relay does not know yet: the relay ignores them
// and the signature still holds. That is what lets the portal add fields
// without a new version and an ordered rollout.
func TestPassUnknownClaims(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1_800_000_000, 0)
	body := []byte(`{"v":"` + Version + `","peer":"peerA","account":"user:1","issued_at":` + i64(now) + `,"expires_at":` + i64(now.Add(time.Hour)) +
		`,"client":{"os":"linux","gpu":"h100"},"region":"sg"}`)
	p := relayed(t, &Pass{Body: body, Sig: ed25519.Sign(priv, signingBytes(body))})
	if err := p.Verify([]ed25519.PublicKey{pub}, "peerA", now); err != nil {
		t.Fatal(err)
	}
	if p.Client.OS != "linux" || p.Account != "user:1" {
		t.Fatalf("claims %+v", p.Claims)
	}

	// Not JSON, too big, or the old version: malformed.
	for name, body := range map[string][]byte{
		"garbage":       []byte("not json"),
		"too big":       append(bytes.Clone(body[:len(body)-1]), []byte(`,"pad":"`+strings.Repeat("x", MaxBody)+`"}`)...),
		"other version": bytes.Replace(body, []byte(Version), []byte("sharetoken/relay-pass/v0"), 1),
	} {
		p := relayed(t, &Pass{Body: body, Sig: ed25519.Sign(priv, signingBytes(body))})
		if err := p.Verify([]ed25519.PublicKey{pub}, "peerA", now); !errors.Is(err, ErrMalformed) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func i64(t time.Time) string { b, _ := json.Marshal(t.UnixNano()); return string(b) }

func TestClientClean(t *testing.T) {
	for in, want := range map[Client]Client{
		{Version: "1.0.0", OS: "darwin", Arch: "arm64"}:                  {Version: "1.0.0", OS: "darwin", Arch: "arm64"},
		{Version: "1.0.0 (evil)", OS: "Darwin", Arch: "arm64\n"}:         {},
		{Version: strings.Repeat("1", 65), OS: "linux", Arch: "riscv64"}: {OS: "linux", Arch: "riscv64"},
		{Version: "1.0.0-dev.abc+meta", OS: `x"y`, Arch: ""}:             {Version: "1.0.0-dev.abc+meta"},
	} {
		if got := in.Clean(); got != want {
			t.Errorf("%+v: %+v", in, got)
		}
	}
}

func TestPassVerification(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	otherPub, otherPriv, _ := ed25519.GenerateKey(nil)
	now := time.Unix(1_800_000_000, 0)
	p := Issue(priv, "peerA", "user:1", Client{Version: "1.0.0", OS: "darwin", Arch: "arm64"}, now, time.Hour)
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
	// What a relay checks is the body: any change to it breaks the signature.
	for name, edit := range map[string]func(*Claims){
		"account":        func(c *Claims) { c.Account = "user:2" },
		"expiry":         func(c *Claims) { c.ExpiresAt += int64(24 * time.Hour) },
		"client version": func(c *Claims) { c.Client.Version = "9.9.9" },
		"client os":      func(c *Claims) { c.Client.OS = "windows" },
	} {
		c := p.Claims
		edit(&c)
		body, _ := json.Marshal(c)
		forged := relayed(t, &Pass{Body: body, Sig: p.Sig})
		if err := forged.Verify(keys, "peerA", now); !errors.Is(err, ErrBadSignature) {
			t.Fatalf("tampered %s: %v", name, err)
		}
	}
	future := Issue(otherPriv, "peerA", "user:1", Client{}, now.Add(time.Hour), time.Hour)
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
