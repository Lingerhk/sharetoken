package e2e

import (
	"strings"
	"sync"
	"testing"
	"time"

	lnet "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"

	"sharetoken/internal/audit"
	"sharetoken/internal/coordinator"
	"sharetoken/internal/node"
	"sharetoken/internal/p2p"
)

func cheatAnswer(string) string {
	return "Sure! Here is a quick generic reply from a much smaller model."
}

func (m *market) auditor(cfg audit.Config) *audit.Auditor {
	m.t.Helper()
	cfg.References = []audit.Reference{{
		Model: model, UpstreamModel: "mock-7b",
		Upstream: &node.OpenAICompat{BaseURL: answerUpstream(m.t, echoAnswer, 0).URL + "/v1"},
		Verifier: answerVerifier{echoAnswer},
	}}
	cfg.Coordinator = m.info
	cfg.Questions = 3
	a, err := audit.New(m.coord.Engine, cfg)
	if err != nil {
		m.t.Fatal(err)
	}
	return a
}

// In shadow mode a failed audit is recorded, but the node keeps its
// reputation and stays in the market.
func TestShadowAuditsLeaveNodeRecordsAlone(t *testing.T) {
	m := newMarket(t)
	_, cheat := m.addNode(answerUpstream(t, cheatAnswer, 0).URL, priceIn, priceOut)
	m.waitOffers(1)
	aud := m.auditor(audit.Config{Shadow: true})
	for range 2 {
		rec, err := aud.Audit(m.ctx, cheat.ID(), model)
		if err != nil || rec.Verdict != coordinator.AuditMismatch || !rec.Shadow {
			t.Fatalf("shadow audit: %+v %v", rec, err)
		}
	}
	st, _ := m.coord.Engine.NodeStats(cheat.ID())
	if st.AuditsFailed != 0 || st.SuspendedUntil != 0 || st.AuditFailStreak != 0 {
		t.Fatalf("a shadow audit touched the node's record: %+v", st)
	}
	if as, _ := m.coord.Engine.Audits(10); len(as) != 2 || !as[0].Shadow {
		t.Fatalf("recorded: %+v", as)
	}
}

// A calibration sets the threshold audits use; one that saw the reference
// drift puts its audits in shadow until the next.
func TestCalibrationSetsTheThresholdAndDriftShadowsAudits(t *testing.T) {
	m := newMarket(t)
	_, cheat := m.addNode(answerUpstream(t, cheatAnswer, 0).URL, priceIn, priceOut)
	m.waitOffers(1)
	aud := m.auditor(audit.Config{})
	c, err := aud.Calibrate(m.ctx, model, audit.CalibrateOptions{Samples: 12})
	if err != nil || c.Samples != 12 || c.HonestMean != 1 || c.Threshold != 0.97 || c.Drift {
		t.Fatalf("calibration: %+v %v", c, err)
	}
	rec, err := aud.Audit(m.ctx, cheat.ID(), model)
	if err != nil || rec.Threshold != 0.97 || rec.Shadow || rec.Verdict != coordinator.AuditMismatch {
		t.Fatalf("audit after calibration: %+v %v", rec, err)
	}

	// The provider changed the model behind the name: honest scores moved.
	if err := m.coord.Engine.SaveCalibration(coordinator.AuditCalibration{Model: model, Method: audit.MethodTF, Samples: 40,
		HonestMean: 0.8, HonestMedian: 0.8, HonestP001: 0.7, Threshold: 0.68, Drift: true}); err != nil {
		t.Fatal(err)
	}
	rec, err = aud.Audit(m.ctx, cheat.ID(), model)
	if err != nil || !rec.Shadow || rec.Threshold != 0.68 || !strings.Contains(rec.Detail, "drifted") {
		t.Fatalf("audit after a drifted calibration: %+v %v", rec, err)
	}
	if st, _ := m.coord.Engine.NodeStats(cheat.ID()); st.AuditsFailed != 1 || st.SuspendedUntil != 0 {
		t.Fatalf("node record: %+v", st)
	}
}

// inbound records the connections a host accepts.
type inbound struct {
	mu    sync.Mutex
	conns map[peer.ID][]ma.Multiaddr
}

func (in *inbound) watch(n lnet.Network) {
	in.conns = map[peer.ID][]ma.Multiaddr{}
	n.Notify(&lnet.NotifyBundle{ConnectedF: func(_ lnet.Network, c lnet.Conn) {
		in.mu.Lock()
		defer in.mu.Unlock()
		in.conns[c.RemotePeer()] = append(in.conns[c.RemotePeer()], c.RemoteMultiaddr())
	}})
}

// Audits reach a node only through circuits of the coordinator's own
// relays: never directly (that would show the node the coordinator's
// address) and never through another relay (its operator would see the
// coordinator's address, and could tell audits apart).
func TestAuditsReachNodesOnlyThroughOfficialRelays(t *testing.T) {
	n := newNetwork(t, 2)
	alice := n.user("alice", shareMock(answerUpstream(t, echoAnswer, 0).URL, priceIn, priceOut))
	var in inbound
	in.watch(alice.Host().Network())

	relayA, relayB := n.relays[0].Host.ID(), n.relays[1].Host.ID()
	var offer []string
	waitFor(t, 20*time.Second, "an offer with direct and circuit addresses through both relays", func() bool {
		os := n.coord.Engine.Offers()
		if len(os) != 1 {
			return false
		}
		offer = os[0].Addrs
		direct, viaA, viaB := false, false, false
		for _, a := range offer {
			switch {
			case !strings.Contains(a, "/p2p-circuit"):
				direct = true
			case strings.Contains(a, relayA.String()):
				viaA = true
			case strings.Contains(a, relayB.String()):
				viaB = true
			}
		}
		return direct && viaA && viaB
	})

	// Only relay A is official, as if B were someone's private relay.
	official := func() []peer.ID { return []peer.ID{relayA} }
	if got := n.coord.OfficialRelays(); len(got) != 2 {
		t.Fatalf("the coordinator's relays: %v", got)
	}
	newAuditor := func(official func() []peer.ID) *audit.Auditor {
		a, err := audit.New(n.coord.Engine, audit.Config{
			References: []audit.Reference{{Model: model, UpstreamModel: "mock-7b",
				Upstream: &node.OpenAICompat{BaseURL: answerUpstream(t, echoAnswer, 0).URL + "/v1"}, Verifier: answerVerifier{echoAnswer}}},
			Coordinator:      peer.AddrInfo{ID: n.coordH},
			CoordinatorAddrs: n.coord.RelayAddrs,
			OfficialRelays:   official,
			Questions:        2,
		})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	rec, err := newAuditor(official).Audit(n.ctx, alice.PeerID(), model)
	if err != nil || rec.Verdict != coordinator.AuditMatch {
		t.Fatalf("audit: %+v %v", rec, err)
	}
	if !strings.Contains(rec.Scores, `"via":"`+relayA.String()+`"`) || strings.Contains(rec.Scores, relayB.String()) {
		t.Fatalf("the audit's jobs did not all run through relay A: %s", rec.Scores)
	}

	// The node saw the audit identity only through relay A.
	in.mu.Lock()
	var auditConns int
	for p, addrs := range in.conns {
		if p == n.coordH || p == relayA || p == relayB {
			continue
		}
		for _, a := range addrs {
			auditConns++
			r, err := a.ValueForProtocol(ma.P_P2P)
			if _, cerr := a.ValueForProtocol(ma.P_CIRCUIT); cerr != nil || err != nil || r != relayA.String() {
				t.Errorf("node saw peer %s at %s: not a circuit through relay A", p, a)
			}
		}
	}
	in.mu.Unlock()
	if auditConns == 0 {
		t.Fatal("the node saw no audit connection")
	}
	for _, c := range alice.Host().Network().ConnsToPeer(n.coordH) {
		if !p2p.IsRelayed(c) {
			t.Fatalf("direct connection to the coordinator: %s", c.RemoteMultiaddr())
		}
	}

}
