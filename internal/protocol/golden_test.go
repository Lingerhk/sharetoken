package protocol_test

import (
	"crypto/ed25519"
	"encoding/hex"
	"testing"
	"time"

	"sharetoken/internal/protocol"
	"sharetoken/internal/receipt"
	"sharetoken/internal/relaypass"
)

// Signed messages must not change by accident: clients already running verify
// and re-send them. If one of these fails, the signed encoding changed. That
// needs a new signing domain (docs/versioning.md), not a new value here.
func TestSignedEncodingsAreStable(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	box32, key32 := make([]byte, 32), make([]byte, 32)

	offer := protocol.Offer{
		Node: "node", Addrs: []string{"/ip4/127.0.0.1/tcp/1"}, BoxPub: box32, ReceiptKey: key32,
		Models:         []protocol.ModelPrice{{Model: "m", PriceInMicros: 1, PriceOutMicros: 2, PriceCacheRead: 3, PriceCacheWrite: 4, APIs: []string{"chat", "messages"}}},
		MaxConcurrency: 3, IssuedAt: 4, ExpiresAt: 5, ClientVersion: "1.0.0", InferVersions: []uint32{1},
	}
	offer.Sign(priv)
	ticket := protocol.Ticket{
		JobID: "job", Model: "m", Node: "node", NodeAddrs: []string{"/ip4/127.0.0.1/tcp/1"}, NodeBoxPub: box32, NodeReceiptKey: key32,
		Buyer: "buyer", PriceInMicros: 1, PriceOutMicros: 2, ReservedMicros: 3, IssuedAt: 4, ExpiresAt: 5, Coordinator: "coord", InferVersion: 1, Shared: true,
		PriceCacheRead: 6, PriceCacheWrite: 7, API: "responses",
	}
	ticket.Sign(priv)
	sr, err := receipt.Sign(priv, receipt.Receipt{JobID: "job", ModelID: "m", NodeID: priv.Public().(ed25519.PublicKey), RequestHash: key32, ResponseHash: key32,
		PromptTokens: 1, CompletionTokens: 2, CacheReadTokens: 4, CacheWriteTokens: 5, UnixNano: 3, Nonce: key32})
	if err != nil {
		t.Fatal(err)
	}
	pass := relaypass.Issue(priv, "peer", "user:1", relaypass.Client{Version: "1.0.0", OS: "linux", Arch: "amd64"}, time.Unix(1, 0), time.Hour)

	for name, c := range map[string]struct {
		got  []byte
		want string
	}{
		"offer":   {offer.Sig, "b9e77d9247ce3cdde49fc40c13d974feec353f7dbc19f39d494066e12f8b6f898ea024df749f8574d0f2e9b5d516c34c1279d02858bdb850f23d4980f9466c04"},
		"ticket":  {ticket.Sig, "f8fb9e0dbf7a9e822cc2d184732a156a7adc519e04b1c895a19189b7de43b87617268cc4b1cb32baba7732482a7181333abc76d91510836ba4bdfa4272e1d504"},
		"receipt": {sr.Sig, "5e544751b6d100496d7a53227926672f74443eff554ecb55e97383fa3d6db30c10b0b0e15c6ee67768b88298ea7422f6d5f4152c9df70a09b00e6c8f1e3d6901"},
		"ack":     {ed25519.Sign(priv, protocol.AckBytes(&sr.Receipt)), "c1f23a00f3cc3b7becc79e3b47d8746ffd009ee0d538dd998956ecaa802abb66bf072831b19f259099e45e21dd1a09976b1ee8076c76cf0bba55e510f149970d"},
		"pass":    {pass.Sig, "0880a433f11945095548d18cf91959cf2854cf82dc8003d0a31d2fd8723a845836387aa1dfb90db486650983578387f41482b9107869240c6d6f50d4fd3b3b08"},
		"aad":     {protocol.RequestAAD(&ticket), "000000197368617265746f6b656e2f6161642f726571756573742f7631000000036a6f62000000016d000000056275796572000000046e6f6465"},
		"chunk":   {protocol.ChunkAAD(&ticket, 7), "000000177368617265746f6b656e2f6161642f6368756e6b2f7631000000036a6f62000000016d0000000000000007"},
	} {
		if got := hex.EncodeToString(c.got); got != c.want {
			t.Errorf("%s: %s", name, got)
		}
	}
}
