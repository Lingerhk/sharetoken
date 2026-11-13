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
		Models:         []protocol.ModelPrice{{Model: "m", PriceInMicros: 1, PriceOutMicros: 2}},
		MaxConcurrency: 3, IssuedAt: 4, ExpiresAt: 5, ClientVersion: "1.0.0", InferVersions: []uint32{1},
	}
	offer.Sign(priv)
	ticket := protocol.Ticket{
		JobID: "job", Model: "m", Node: "node", NodeAddrs: []string{"/ip4/127.0.0.1/tcp/1"}, NodeBoxPub: box32, NodeReceiptKey: key32,
		Buyer: "buyer", PriceInMicros: 1, PriceOutMicros: 2, ReservedMicros: 3, IssuedAt: 4, ExpiresAt: 5, Coordinator: "coord", InferVersion: 1,
	}
	ticket.Sign(priv)
	sr, err := receipt.Sign(priv, receipt.Receipt{JobID: "job", ModelID: "m", NodeID: priv.Public().(ed25519.PublicKey), RequestHash: key32, ResponseHash: key32,
		PromptTokens: 1, CompletionTokens: 2, UnixNano: 3, Nonce: key32})
	if err != nil {
		t.Fatal(err)
	}
	pass := relaypass.Issue(priv, "peer", "user:1", relaypass.Client{Version: "1.0.0", OS: "linux", Arch: "amd64"}, time.Unix(1, 0), time.Hour)

	for name, c := range map[string]struct {
		got  []byte
		want string
	}{
		"offer":   {offer.Sig, "510d4d42a5a9ab26b1a02639e5fbee7a6e42ddd2aba5cbb075a2b5b2da871758962a104da4c8bdd6a0e48c097e66d887a70f4ab23abb0b7a93f4d7d2370ad00c"},
		"ticket":  {ticket.Sig, "b17bf70946e4ff9f64236fa962a3c7268a8ec48f8aecc1f63e5bfb4e5f36de6bf5f838eb4ceda4ee047e5d7bfde1b9000c17e25c2f09148dbde1b59e3a5fea05"},
		"receipt": {sr.Sig, "f8e4b4e3ad30c7e76c9a011b1b2ce7e72a840bd376455c1c337967a3381ea53dfe0044cc4ac444b83a05a6b399ff8c2fbc0940eed166282e9c35898b0af5050f"},
		"ack":     {ed25519.Sign(priv, protocol.AckBytes(&sr.Receipt)), "abb31db93c13ae1c89ecc69f2d568f7f96633afc62b8ef8c75abd8ebe9eb1e1f43a760569d4660d1033d9346b882d32196edc00541409beea64f0b8ffff2c10c"},
		"pass":    {pass.Sig, "0880a433f11945095548d18cf91959cf2854cf82dc8003d0a31d2fd8723a845836387aa1dfb90db486650983578387f41482b9107869240c6d6f50d4fd3b3b08"},
		"aad":     {protocol.RequestAAD(&ticket), "000000197368617265746f6b656e2f6161642f726571756573742f7631000000036a6f62000000016d000000056275796572000000046e6f6465"},
		"chunk":   {protocol.ChunkAAD(&ticket, 7), "000000177368617265746f6b656e2f6161642f6368756e6b2f7631000000036a6f62000000016d0000000000000007"},
	} {
		if got := hex.EncodeToString(c.got); got != c.want {
			t.Errorf("%s: %s", name, got)
		}
	}
}
