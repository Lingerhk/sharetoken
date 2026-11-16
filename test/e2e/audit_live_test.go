//go:build live

package e2e

// The whole audit path with real models, on the relay network of
// arch_test.go: two sellers sell deepseek-flash, one honestly through the
// DeepSeek API, one through DeepSeek's bigger deepseek-v4-pro (the same
// family: the hardest substitute to tell apart); the coordinator's auditor
// calibrates its DeepSeek reference and audits both through its relays.
//
//	DEEPSEEK_API_KEY=… OPENAI_API_KEY=… scripts/test/test.sh -tags live -run LiveAudit -v -timeout 30m ./test/e2e

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"

	"sharetoken/internal/audit"
	"sharetoken/internal/client"
	"sharetoken/internal/coordinator"
	"sharetoken/internal/node"
)

func TestLiveAuditThroughRelays(t *testing.T) {
	ds, oa := os.Getenv("DEEPSEEK_API_KEY"), os.Getenv("OPENAI_API_KEY")
	if ds == "" || oa == "" {
		t.Skip("needs DEEPSEEK_API_KEY and OPENAI_API_KEY")
	}
	t.Setenv("ST_LIVE_DS_KEY", ds)
	t.Setenv("ST_LIVE_OA_KEY", oa)
	const m = "deepseek-flash"
	n := newNetwork(t, 2)
	seller := func(name, url, keyEnv, upstreamModel string) *client.Agent {
		return n.user(name, &client.ShareConfig{Enabled: true, Upstream: url, UpstreamKeyEnv: keyEnv, Concurrency: 2, Relays: 2,
			Models: []node.ModelConfig{{ID: m, UpstreamModel: upstreamModel, PriceInMicros: priceIn, PriceOutMicros: priceOut}}})
	}
	honest := seller("honest", "https://api.deepseek.com/v1", "ST_LIVE_DS_KEY", "deepseek-flash")
	cheat := seller("cheat", "https://api.deepseek.com/v1", "ST_LIVE_DS_KEY", "deepseek-v4-pro")
	var in inbound
	in.watch(honest.Host().Network())
	waitFor(t, 30*time.Second, "both offers", func() bool { return len(n.coord.Engine.Offers()) == 2 })

	params := map[string]any{"reasoning_effort": "none"}
	v, err := audit.NewVerifier(audit.VerifyDeepSeekPrefix, "https://api.deepseek.com/v1", ds, m, params)
	if err != nil {
		t.Fatal(err)
	}
	aud, err := audit.New(n.coord.Engine, audit.Config{
		References: []audit.Reference{{Model: m, UpstreamModel: m, Upstream: &node.OpenAICompat{BaseURL: "https://api.deepseek.com/v1", APIKey: ds},
			Verifier: v, Params: params}},
		Coordinator:      peer.AddrInfo{ID: n.coordH},
		CoordinatorAddrs: n.coord.RelayAddrs,
		OfficialRelays:   n.coord.OfficialRelays,
		BudgetMicros:     1_000_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := aud.Calibrate(n.ctx, m, audit.CalibrateOptions{Samples: 15, Parallel: 8})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("calibration: threshold %.3f, %s", c.Threshold, c.Detail)

	h, err := aud.Audit(n.ctx, honest.PeerID(), m)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("honest: %s %.3f (threshold %.3f) %s", h.Verdict, h.Score, h.Threshold, h.Detail)
	ch, err := aud.Audit(n.ctx, cheat.PeerID(), m)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("cheat:  %s %.3f (threshold %.3f) %s", ch.Verdict, ch.Score, ch.Threshold, ch.Detail)
	if h.Verdict == coordinator.AuditMismatch || ch.Verdict != coordinator.AuditMismatch {
		t.Fatalf("verdicts: honest %s, cheat %s", h.Verdict, ch.Verdict)
	}
	if as, _ := n.coord.Engine.Audits(10); len(as) != 2 || as[0].Questions != 5 || as[0].Method != audit.MethodTF {
		t.Fatalf("records: %+v", as)
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	for p, addrs := range in.conns {
		if p == n.coordH || p == n.relays[0].Host.ID() || p == n.relays[1].Host.ID() {
			continue
		}
		for _, a := range addrs {
			if _, err := a.ValueForProtocol(ma.P_CIRCUIT); err != nil {
				t.Errorf("the honest node saw %s directly at %s", p, a)
			}
		}
	}
	if !strings.Contains(h.Scores, `"via":"`) {
		t.Fatalf("no relay recorded: %s", h.Scores)
	}
}

// The same with the questions carried by users' clients and asked in all
// three APIs: an honest DeepSeek seller must pass whatever API a question
// comes in (its answers are scored against the chat reference), and the
// substitute must still fail.
//
//	DEEPSEEK_API_KEY=… scripts/test/test.sh -tags live -run LiveCarriedAudit -v -timeout 30m ./test/e2e
func TestLiveCarriedAuditInEveryAPI(t *testing.T) {
	ds := os.Getenv("DEEPSEEK_API_KEY")
	if ds == "" {
		t.Skip("needs DEEPSEEK_API_KEY")
	}
	t.Setenv("ST_LIVE_DS_KEY", ds)
	const m = "deepseek-flash"
	n := newNetwork(t, 1)
	n.coord.Errands.SetPollAfter(time.Second)
	n.errandFirst = 200 * time.Millisecond
	seller := func(name, upstreamModel string) *client.Agent {
		return n.user(name, &client.ShareConfig{Enabled: true, Upstream: "https://api.deepseek.com/v1", UpstreamKeyEnv: "ST_LIVE_DS_KEY", Concurrency: 2, Relays: 2,
			Models: []node.ModelConfig{{ID: m, UpstreamModel: upstreamModel, PriceInMicros: priceIn, PriceOutMicros: priceOut}}})
	}
	honest := seller("honest", "deepseek-flash")
	cheat := seller("cheat", "deepseek-v4-pro")
	n.user("bob", nil)
	n.user("carol", nil)
	waitFor(t, 30*time.Second, "both offers", func() bool { return len(n.coord.Engine.Offers()) == 2 })
	waitFor(t, 30*time.Second, "carriers", func() bool {
		cs, _ := n.coord.Engine.ErrandCarriers(honest.PeerID(), n.coord.Errands.Carriers())
		return len(cs) >= 2
	})

	params := map[string]any{"reasoning_effort": "none"}
	v, err := audit.NewVerifier(audit.VerifyDeepSeekPrefix, "https://api.deepseek.com/v1", ds, m, params)
	if err != nil {
		t.Fatal(err)
	}
	newAuditor := func(apis ...string) *audit.Auditor {
		a, err := audit.New(n.coord.Engine, audit.Config{
			References: []audit.Reference{{Model: m, UpstreamModel: m, Upstream: &node.OpenAICompat{BaseURL: "https://api.deepseek.com/v1", APIKey: ds},
				Verifier: v, Params: params}},
			Coordinator: peer.AddrInfo{ID: n.coordH}, CoordinatorAddrs: n.coord.RelayAddrs, OfficialRelays: n.coord.OfficialRelays,
			Errands: n.coord.Errands, Delivery: audit.DeliveryCarried, APIs: apis, Questions: 4,
			Shadow: true, // three audits of the substitute: it must stay in the market
		})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	c, err := newAuditor().Calibrate(n.ctx, m, audit.CalibrateOptions{Samples: 15, Parallel: 8})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("calibration: threshold %.3f, %s", c.Threshold, c.Detail)
	for _, api := range []string{"chat", "messages", "responses"} {
		aud := newAuditor(api)
		h, err := aud.Audit(n.ctx, honest.PeerID(), m)
		if err != nil {
			t.Fatalf("%s: honest: %v", api, err)
		}
		t.Logf("%-9s honest: %s %.3f (threshold %.3f)", api, h.Verdict, h.Score, h.Threshold)
		ch, err := aud.Audit(n.ctx, cheat.PeerID(), m)
		if err != nil {
			t.Fatalf("%s: cheat: %v", api, err)
		}
		t.Logf("%-9s cheat:  %s %.3f (threshold %.3f)", api, ch.Verdict, ch.Score, ch.Threshold)
		if h.Verdict == coordinator.AuditMismatch || ch.Verdict != coordinator.AuditMismatch {
			t.Errorf("%s: verdicts: honest %s, cheat %s\n%s\n%s", api, h.Verdict, ch.Verdict, h.Scores, ch.Scores)
		}
		for _, q := range carriedScores(t, h) {
			// The other seller's device buys too, and may carry.
			if q.Carrier == "" || q.Carrier == honest.PeerID() || q.API != api {
				t.Errorf("%s: question %+v was not carried by a client in that API", api, q)
			}
		}
	}
}
