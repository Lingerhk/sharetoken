package e2e

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"

	"sharetoken/internal/audit"
	"sharetoken/internal/buyer"
	"sharetoken/internal/client"
	"sharetoken/internal/coordinator"
	"sharetoken/internal/ledger"
	"sharetoken/internal/node"
	"sharetoken/internal/p2p"
	"sharetoken/internal/protocol"
	"sharetoken/internal/receipt"
)

// carrier is a buyer whose client asks the coordinator for errands.
func (m *market) carrier(credit uint64) (*buyer.Client, host.Host, *p2p.Keys) {
	m.t.Helper()
	cl, h, k := m.addBuyer(credit)
	go cl.RunErrands(m.ctx, 50*time.Millisecond, nil)
	return cl, h, k
}

// waitCarriers waits until n clients are there to carry questions.
func (m *market) waitCarriers(n int) {
	m.t.Helper()
	waitFor(m.t, 10*time.Second, "clients asking for errands", func() bool { return len(m.coord.Errands.Carriers()) >= n })
}

// carriedScores reads an audit record's per-question results.
func carriedScores(t *testing.T, rec *coordinator.AuditRecord) []struct{ Job, API, Carrier, Via string } {
	t.Helper()
	var qs []struct{ Job, API, Carrier, Via string }
	if err := json.Unmarshal([]byte(rec.Scores), &qs); err != nil {
		t.Fatal(err)
	}
	return qs
}

// Users' clients carry the audit's questions: the node is asked by devices
// that buy on the market, never by an audit identity; the clients pay
// nothing; the platform pays the node; and a substituted model is caught the
// same.
func TestCarriedAuditCatchesASubstitutedModel(t *testing.T) {
	m := newMarket(t)
	m.coord.Errands.SetPollAfter(time.Second)
	hh, honest := m.addNode(answerUpstream(t, echoAnswer, 0).URL, 2*priceIn, 2*priceOut)
	ch, cheat := m.addNode(answerUpstream(t, cheatAnswer, 0).URL, priceIn, priceOut)
	var sawHonest, sawCheat inbound
	sawHonest.watch(hh.Network())
	sawCheat.watch(ch.Network())
	m.waitOffers(2)

	const credit = 3_000_000
	carriers := map[string]*p2p.Keys{}
	for range 3 {
		_, _, k := m.carrier(credit)
		carriers[k.ID()] = k
	}
	m.waitCarriers(3)

	aud := m.auditor(audit.Config{Errands: m.coord.Errands, Delivery: audit.DeliveryCarried})
	rec, err := aud.Audit(m.ctx, honest.ID(), model)
	if err != nil || rec.Verdict != coordinator.AuditMatch {
		t.Fatalf("honest node: %+v %v", rec, err)
	}
	apis := map[string]bool{}
	check := func(rec *coordinator.AuditRecord) {
		t.Helper()
		qs := carriedScores(t, rec)
		if len(qs) != 3 {
			t.Fatalf("questions: %s", rec.Scores)
		}
		for _, q := range qs {
			if carriers[q.Carrier] == nil {
				t.Fatalf("a question was not carried by a client: %+v", q)
			}
			apis[q.API] = true
		}
	}
	check(rec)
	for range 2 {
		rec, err := aud.Audit(m.ctx, cheat.ID(), model)
		if err != nil || rec.Verdict != coordinator.AuditMismatch {
			t.Fatalf("substituted model: %+v %v", rec, err)
		}
		check(rec)
	}
	if st, _ := m.coord.Engine.NodeStats(cheat.ID()); st.SuspendedUntil == 0 || st.AuditsFailed != 2 {
		t.Fatalf("cheating node record: %+v", st)
	}
	if len(apis) < 2 {
		t.Fatalf("nine questions were all asked in one API: %v", apis)
	}

	// Each node met the carriers and the coordinator, nobody else: there is
	// no audit identity for it to recognise.
	for _, in := range []*inbound{&sawHonest, &sawCheat} {
		in.mu.Lock()
		for p := range in.conns {
			if carriers[p.String()] == nil && p != m.info.ID {
				t.Errorf("a node was dialled by %s, which is no carrier", p)
			}
		}
		in.mu.Unlock()
	}

	// Carrying cost the clients nothing, and nothing of theirs stays held.
	waitFor(t, 10*time.Second, "the carried jobs to settle", func() bool {
		for _, k := range carriers {
			if m.held(k) != 0 {
				return false
			}
		}
		return m.account(coordinator.AuditAccount) >= 0
	})
	for id, k := range carriers {
		if b := m.balance(k); b != credit {
			t.Fatalf("carrier %s: balance %d, want %d", id, b, credit)
		}
	}
	// The nodes were paid, by the platform.
	if m.balance(honest) <= 0 || m.balance(cheat) <= 0 {
		t.Fatalf("nodes were not paid: %d %d", m.balance(honest), m.balance(cheat))
	}
	m.coord.Engine.Tick(time.Now().Add(2 * time.Minute))
	if b := m.account(coordinator.AuditAccount); b != 0 {
		t.Fatalf("audit funds left behind: %d", b)
	}
	if err := m.coord.Engine.CheckBooks(); err != nil {
		t.Fatal(err)
	}
	// A carried job is none of the carrier's purchases.
	for id := range carriers {
		jobs, err := coordinator.QueryJobs(m.db, coordinator.JobQuery{Acct: string(ledger.Account(id)), Role: "buy", Limit: 10})
		if err != nil || len(jobs) != 0 {
			t.Fatalf("carried jobs show up as the carrier's: %+v", jobs)
		}
	}
}

// With nobody to carry a question, the auditor asks from an identity of its
// own (auto), or not at all (carried).
func TestAuditWithoutCarriersAsksDirectly(t *testing.T) {
	m := newMarket(t)
	m.coord.Errands.SetPollAfter(time.Second)
	_, honest := m.addNode(answerUpstream(t, echoAnswer, 0).URL, priceIn, priceOut)
	m.waitOffers(1)

	if _, err := m.auditor(audit.Config{Errands: m.coord.Errands, Delivery: audit.DeliveryCarried}).Audit(m.ctx, honest.ID(), model); err == nil {
		t.Fatal("a carried-only audit ran without carriers")
	}
	rec, err := m.auditor(audit.Config{Errands: m.coord.Errands}).Audit(m.ctx, honest.ID(), model)
	if err != nil || rec.Verdict != coordinator.AuditMatch {
		t.Fatalf("audit: %+v %v", rec, err)
	}
	for _, q := range carriedScores(t, rec) {
		if q.Carrier != "" {
			t.Fatalf("carried by %s, with no carrier there", q.Carrier)
		}
	}
	// The node's own device never carries a question to itself.
	if cs, err := m.coord.Engine.ErrandCarriers(honest.ID(), []string{honest.ID()}); err != nil || len(cs) != 0 {
		t.Fatalf("carriers of a node's own audit: %v %v", cs, err)
	}
}

// crooked asks for errands like a client and hands back what tamper makes of
// the node's answer.
func (m *market) crooked(tamper func(res *buyer.Result, rep *protocol.ErrandReport, chunks [][]byte) [][]byte) *p2p.Keys {
	m.t.Helper()
	cl, h, k := m.addBuyer(1_000_000)
	go func() {
		for m.ctx.Err() == nil {
			var e protocol.Errand
			if err := p2p.Call(m.ctx, h, m.info.ID, protocol.CtrlProtocol, protocol.TypeErrandPoll, protocol.ErrandPoll{}, protocol.TypeErrand, &e); err != nil || e.Job == nil {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			res, err := cl.Carry(m.ctx, e.Job)
			if err != nil {
				m.t.Errorf("carrying: %v", err)
				return
			}
			rep := protocol.ErrandReport{JobID: e.Job.Ticket.JobID, Receipt: &res.Receipt}
			chunks := tamper(res, &rep, res.Sealed)
			rep.Chunks = uint64(len(chunks))
			st, err := h.NewStream(m.ctx, m.info.ID, protocol.CtrlProtocol)
			if err != nil {
				return
			}
			_ = protocol.SendRequest(st, protocol.TypeErrandReport, rep)
			for i, c := range chunks {
				_ = protocol.SendData(st, uint64(i), c)
			}
			_ = st.CloseWrite()
			_, _ = protocol.Recv(st)
			_ = st.Close()
		}
	}()
	return k
}

// A carrier cannot change what it carries: frames it altered or dropped do
// not match what the node signed for, and a receipt it made up is not the
// node's. The node is not blamed, and the auditor asks again itself.
func TestTamperingCarrierCannotFrameANode(t *testing.T) {
	for name, tamper := range map[string]func(*buyer.Result, *protocol.ErrandReport, [][]byte) [][]byte{
		"altered frame": func(_ *buyer.Result, _ *protocol.ErrandReport, cs [][]byte) [][]byte {
			out := append([][]byte(nil), cs...)
			out[0] = append([]byte(nil), out[0]...)
			out[0][len(out[0])-1] ^= 1
			return out
		},
		"dropped frames": func(_ *buyer.Result, _ *protocol.ErrandReport, cs [][]byte) [][]byte { return cs[:1] },
		"forged receipt": func(res *buyer.Result, rep *protocol.ErrandReport, cs [][]byte) [][]byte {
			forged := res.Receipt
			forged.Receipt.CompletionTokens += 5000
			rep.Receipt = &receipt.SignedReceipt{Receipt: forged.Receipt, Sig: forged.Sig}
			return cs
		},
		"no receipt": func(_ *buyer.Result, rep *protocol.ErrandReport, _ [][]byte) [][]byte {
			rep.Receipt, rep.Error = nil, "the node sent garbage"
			return nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			m := newMarket(t)
			m.coord.Errands.SetPollAfter(time.Second)
			_, honest := m.addNode(answerUpstream(t, echoAnswer, 0).URL, priceIn, priceOut)
			m.waitOffers(1)
			bad := m.crooked(tamper)
			m.waitCarriers(1)

			if rec, err := m.auditor(audit.Config{Errands: m.coord.Errands, Delivery: audit.DeliveryCarried}).Audit(m.ctx, honest.ID(), model); err == nil {
				t.Fatalf("a tampered answer was judged: %+v", rec)
			}
			rec, err := m.auditor(audit.Config{Errands: m.coord.Errands}).Audit(m.ctx, honest.ID(), model)
			if err != nil || rec.Verdict != coordinator.AuditMatch {
				t.Fatalf("audit: %+v %v", rec, err)
			}
			if st, _ := m.coord.Engine.NodeStats(honest.ID()); st.AuditsFailed != 0 || st.DisputesLost != 0 {
				t.Fatalf("the node was blamed for its carrier: %+v", st)
			}
			if as, _ := m.coord.Engine.Audits(10); len(as) != 1 {
				t.Fatalf("recorded audits: %+v", as)
			}
			if b := m.balance(bad); b != 1_000_000 {
				t.Fatalf("the carrier's balance moved: %d", b)
			}
			// It is not asked again soon: every answer it spoils costs the
			// platform a job.
			if cs := m.coord.Errands.Carriers(); len(cs) != 0 {
				t.Fatalf("a carrier that spoiled its errand is still used: %v", cs)
			}
		})
	}
}

// A carrier has no say over a carried job beyond handing it back: it cannot
// dispute it, cannot hand back someone else's, and a ticket made for it is no
// use to another device.
func TestCarrierCannotDisputeOrStealAnErrand(t *testing.T) {
	m := newMarket(t)
	_, honest := m.addNode(answerUpstream(t, echoAnswer, 0).URL, priceIn, priceOut)
	m.waitOffers(1)
	cl, h, k := m.addBuyer(1_000_000)
	_, other, _ := m.addBuyer(1_000_000)

	tr, err := m.coord.Engine.IssueErrandTicket(k.ID(), protocol.TicketRequest{
		InferVersions: protocol.SupportedInferVersions, Model: model, Node: honest.ID(), BudgetMicros: 1_000_000, MaxTokens: 64, PromptTokens: 20})
	if err != nil {
		t.Fatal(err)
	}
	if m.held(k) != 0 || m.balance(k) != 1_000_000 {
		t.Fatalf("an errand ticket held the carrier's credit: held %d, balance %d", m.held(k), m.balance(k))
	}
	if err := sendInfer(t, other, tr.Ticket); err == nil {
		t.Fatal("another device ran the carrier's ticket")
	}
	err = p2p.Call(m.ctx, h, m.info.ID, protocol.CtrlProtocol, protocol.TypeDispute,
		protocol.DisputeRequest{JobID: tr.Ticket.JobID, Reason: "it was bad"}, protocol.TypeOK, nil)
	if err == nil || !strings.Contains(err.Error(), "errand") {
		t.Fatalf("a carrier disputed an errand: %v", err)
	}
	// Nobody posted this job on the board: a report for it is refused.
	err = p2p.Call(m.ctx, h, m.info.ID, protocol.CtrlProtocol, protocol.TypeErrandReport,
		protocol.ErrandReport{JobID: tr.Ticket.JobID, Error: "x"}, protocol.TypeOK, nil)
	if protocol.CodeOf(err) != protocol.CodeNotFound {
		t.Fatalf("a report for a job the device does not carry: %v", err)
	}
	_ = cl
}

// The spot-check setting: a client that opted out never asks, so it is never
// chosen; one that did not carries questions through its ordinary relayed
// connection, under its ordinary identity.
func TestClientsCarryAuditsUnlessOptedOut(t *testing.T) {
	n := newNetwork(t, 1)
	n.coord.Errands.SetPollAfter(time.Second)
	alice := n.user("alice", shareMock(answerUpstream(t, echoAnswer, 0).URL, priceIn, priceOut))
	var in inbound
	in.watch(alice.Host().Network())
	n.errandFirst = 100 * time.Millisecond
	const lo = "/ip4/127.0.0.1/tcp/0"
	bob := n.userWith("bob", nil, lo, onlyFamily{ma.P_IP4}, nil)
	off := false
	carol := n.userWith("carol", nil, lo, onlyFamily{ma.P_IP4}, func(c *client.Config) { c.Buy.SpotChecks = &off })
	waitFor(t, 20*time.Second, "alice's offer", func() bool { return len(n.coord.Engine.Offers()) == 1 })
	waitFor(t, 20*time.Second, "bob asking for errands", func() bool {
		cs, _ := n.coord.Engine.ErrandCarriers(alice.PeerID(), n.coord.Errands.Carriers())
		return len(cs) == 1 && cs[0] == bob.PeerID()
	})
	time.Sleep(1500 * time.Millisecond) // carol, opted out, would have asked by now
	for _, d := range n.coord.Errands.Carriers() {
		if d == carol.PeerID() {
			t.Fatal("a client that opted out asked for errands")
		}
	}

	a, err := audit.New(n.coord.Engine, audit.Config{
		References: []audit.Reference{{Model: model, UpstreamModel: "mock-7b",
			Upstream: &node.OpenAICompat{BaseURL: answerUpstream(t, echoAnswer, 0).URL + "/v1"}, Verifier: answerVerifier{echoAnswer}}},
		Coordinator: peer.AddrInfo{ID: n.coordH}, CoordinatorAddrs: n.coord.RelayAddrs, OfficialRelays: n.coord.OfficialRelays,
		Errands: n.coord.Errands, Delivery: audit.DeliveryCarried, Questions: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	before := n.me(bob).Balance.AvailableMicros
	rec, err := a.Audit(n.ctx, alice.PeerID(), model)
	if err != nil || rec.Verdict != coordinator.AuditMatch {
		t.Fatalf("audit: %+v %v", rec, err)
	}
	for _, q := range carriedScores(t, rec) {
		if q.Carrier != bob.PeerID() {
			t.Fatalf("carried by %q, want bob", q.Carrier)
		}
	}
	if after := n.me(bob).Balance.AvailableMicros; after != before || n.me(bob).Balance.HeldMicros != 0 {
		t.Fatalf("carrying cost bob credit: %d -> %d", before, after)
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	relayID := n.relays[0].Host.ID()
	for p := range in.conns {
		if s := p.String(); s != bob.PeerID() && s != carol.PeerID() && p != n.coordH && p != relayID {
			t.Errorf("alice was dialled by %s", p)
		}
	}
	// The setting is part of the buying settings the app shows and saves.
	var v client.BuySettingsView
	getJSON(t, carol, "/api/v1/buy", &v)
	if v.SpotChecks == nil || *v.SpotChecks {
		t.Fatalf("carol's settings: %+v", v)
	}
	getJSON(t, bob, "/api/v1/buy", &v)
	if v.SpotChecks == nil || !*v.SpotChecks {
		t.Fatalf("bob's settings: %+v", v)
	}
}
