package audit

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"sharetoken/internal/coordinator"
	"sharetoken/internal/mysqldb"
	"sharetoken/internal/node"
	"sharetoken/internal/p2p"
	"sharetoken/internal/protocol"
)

// The platform pays for every audit question, so a node priced far above its
// model's market is never chosen for an audit: its price would be the
// platform's loss on each one.
func TestPickTargetSkipsOverpricedNodes(t *testing.T) {
	db, err := mysqldb.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ck, err := p2p.NewKeys()
	if err != nil {
		t.Fatal(err)
	}
	eng, err := coordinator.NewEngine(ck.ID(), ck.Signer, coordinator.Config{DB: db, OpenAccounts: true})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	offer := func(price uint64) string {
		k, err := p2p.NewKeys()
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		o := &protocol.Offer{Node: k.ID(), Addrs: []string{"/ip4/127.0.0.1/tcp/1"}, BoxPub: k.Box.Public(), ReceiptKey: k.ReceiptKey(),
			Models:         []protocol.ModelPrice{{Model: "m", PriceInMicros: price, PriceOutMicros: 2 * price}, {Model: "unreferenced", PriceInMicros: 1, PriceOutMicros: 1}},
			MaxConcurrency: 2, IssuedAt: now.UnixNano(), ExpiresAt: now.Add(time.Minute).UnixNano()}
		o.Sign(k.Signer)
		if err := eng.UpsertOffer(o); err != nil {
			t.Fatal(err)
		}
		return k.ID()
	}
	fair := map[string]bool{offer(100_000): true, offer(150_000): true, offer(300_000): true}
	greedy := offer(100_000_000)

	a, err := New(eng, Config{Coordinator: peer.AddrInfo{ID: ck.PeerID},
		References: []Reference{{Model: "m", UpstreamModel: "m", Upstream: &node.OpenAICompat{BaseURL: "http://127.0.0.1:1/v1"}}}})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for range 400 {
		n, model, ok := a.pickTarget()
		if !ok || model != "m" {
			t.Fatalf("target %q %q %v", n, model, ok)
		}
		if n == greedy {
			t.Fatal("the node at a thousand times the market's price was chosen for an audit")
		}
		seen[n] = true
	}
	if len(seen) != len(fair) {
		t.Fatalf("%d of the %d fairly priced nodes were chosen", len(seen), len(fair))
	}
}
