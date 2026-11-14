package e2e

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sharetoken/internal/attest"
	"sharetoken/internal/audit"
	"sharetoken/internal/buyer"
	"sharetoken/internal/coordinator"
	"sharetoken/internal/evidence"
	"sharetoken/internal/node"
)

// answerUpstream is a deterministic model server: it replies answer(prompt)
// word by word and reports usage by word count, plus extraCompletion tokens
// (a node that inflates its bill).
func answerUpstream(t *testing.T, answer func(prompt string) string, extraCompletion int) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		var prompt string
		if n := len(req.Messages); n > 0 {
			prompt = req.Messages[n-1].Content
		}
		reply := answer(prompt)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, word := range strings.SplitAfter(reply, " ") {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", word)
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprintf(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":%d,\"completion_tokens\":%d}}\n\n",
			len(strings.Fields(prompt))+8, len(strings.Fields(reply))+1+extraCompletion)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func echoAnswer(prompt string) string { return "You asked: " + prompt }

// waitDispute waits until the coordinator has judged or recorded a dispute
// against node and returns it.
func (m *market) waitDispute(node string, done func(coordinator.DisputeInfo) bool) coordinator.DisputeInfo {
	m.t.Helper()
	var got coordinator.DisputeInfo
	waitFor(m.t, 15*time.Second, "dispute", func() bool {
		ds, err := m.coord.Engine.Disputes(10)
		if err != nil {
			m.t.Fatal(err)
		}
		for _, d := range ds {
			if d.Node == node && done(d) {
				got = d
				return true
			}
		}
		return false
	})
	return got
}

// A node bills 50,000 completion tokens for a two-dozen-token answer. The
// buyer refuses to ack, discloses that one job's key and ciphertexts, and the
// coordinator refunds it on the evidence alone: no human involved.
func TestInflatedBillIsRefundedOnEvidence(t *testing.T) {
	m := newMarket(t)
	_, nodeKeys := m.addNode(answerUpstream(t, echoAnswer, 50_000).URL, priceIn, priceOut)
	m.waitOffers(1)
	cl, _, buyerKeys := m.addBuyer(5_000_000)

	_, err := cl.Chat(m.ctx, buyer.ChatRequest{Model: model, Body: []byte(body), BudgetMicros: 1_000_000}, nil)
	if !errors.Is(err, buyer.ErrReceiptRejected) {
		t.Fatalf("an inflated receipt must be rejected, got %v", err)
	}
	d := m.waitDispute(nodeKeys.ID(), func(d coordinator.DisputeInfo) bool { return d.Outcome != "" })
	if d.Outcome != string(evidence.Upheld) || d.Status != coordinator.StatusRefunded || !strings.Contains(d.Detail, "billed") {
		t.Fatalf("want an upheld, refunded dispute, got %+v", d)
	}
	waitFor(t, 5*time.Second, "refund", func() bool { return m.balance(buyerKeys) == 5_000_000 && m.held(buyerKeys) == 0 })
	if got := m.balance(nodeKeys); got != 0 {
		t.Fatalf("an over-billing node was paid %d", got)
	}
	if st, _ := m.coord.Engine.NodeStats(nodeKeys.ID()); st.DisputesLost != 1 {
		t.Fatalf("node record: %+v", st)
	}
	if err := m.coord.Engine.CheckBooks(); err != nil {
		t.Fatal(err)
	}
}

// With evidence disclosure turned off the coordinator learns nothing about
// the job, so the same dispute waits for an arbitrator instead.
func TestWithoutEvidenceTheDisputeWaitsForArbitration(t *testing.T) {
	m := newMarket(t)
	_, nodeKeys := m.addNode(answerUpstream(t, echoAnswer, 50_000).URL, priceIn, priceOut)
	m.waitOffers(1)
	cl, _, _ := m.addBuyer(5_000_000)
	cl.Evidence = false

	if _, err := cl.Chat(m.ctx, buyer.ChatRequest{Model: model, Body: []byte(body), BudgetMicros: 1_000_000}, nil); !errors.Is(err, buyer.ErrReceiptRejected) {
		t.Fatalf("want rejection, got %v", err)
	}
	d := m.waitDispute(nodeKeys.ID(), func(d coordinator.DisputeInfo) bool { return d.Status == coordinator.StatusDisputed })
	if d.Outcome != "" {
		t.Fatalf("no evidence was given, yet the dispute was judged: %+v", d)
	}
}

// The auditor buys from each node under a fresh identity and replays the
// request on its reference model. The honest node passes; a cheaper node
// serving a different model fails twice, is suspended, and buyers then get
// the honest node even though it costs more.
func TestAuditCatchesASubstitutedModel(t *testing.T) {
	m := newMarket(t)
	_, honest := m.addNode(answerUpstream(t, echoAnswer, 0).URL, 2*priceIn, 2*priceOut)
	_, cheat := m.addNode(answerUpstream(t, func(string) string {
		return "Sure! Here is a quick generic reply from a much smaller model."
	}, 0).URL, priceIn, priceOut)
	m.waitOffers(2)

	aud, err := audit.New(m.coord.Engine, audit.Config{
		References: []audit.Reference{{
			Model: model, UpstreamModel: "mock-7b",
			Upstream: &node.OpenAICompat{BaseURL: answerUpstream(t, echoAnswer, 0).URL + "/v1"},
		}},
		Coordinator: m.info,
	})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := aud.Audit(m.ctx, honest.ID(), model)
	if err != nil || rec.Verdict != coordinator.AuditMatch {
		t.Fatalf("honest node: %+v %v", rec, err)
	}
	for range 2 {
		if rec, err := aud.Audit(m.ctx, cheat.ID(), model); err != nil || rec.Verdict != coordinator.AuditMismatch {
			t.Fatalf("substituted model: %+v %v", rec, err)
		}
	}
	if st, _ := m.coord.Engine.NodeStats(cheat.ID()); st.SuspendedUntil == 0 || st.AuditsFailed != 2 {
		t.Fatalf("cheating node record: %+v", st)
	}

	cl, _, _ := m.addBuyer(5_000_000)
	res, err := cl.Chat(m.ctx, buyer.ChatRequest{Model: model, Body: []byte(body), BudgetMicros: 1_000_000}, nil)
	if err != nil || res.Ticket.Node != honest.ID() {
		t.Fatalf("a suspended node was matched: %v %v", res, err)
	}

	// Audits were paid like any job, out of platform funds; once settled, the
	// audit identities' leftovers go back to the platform.
	m.coord.Engine.Tick(time.Now().Add(2 * time.Minute))
	if err := m.coord.Engine.CheckBooks(); err != nil {
		t.Fatal(err)
	}
	if as, _ := m.coord.Engine.Audits(10); len(as) != 3 {
		t.Fatalf("want 3 recorded audits, got %d", len(as))
	}
}

// A buyer that requires a TEE gets only nodes whose quote binds their keys
// to an allow-listed image, verified by the buyer itself.
func TestRequireTEEUsesOnlyAttestedNodes(t *testing.T) {
	measurement := []byte("sharetoken-node-image-v1")
	att, root := attest.NewMockAttestor(measurement)
	verifier := attest.Multi{attest.MockBackend: attest.MockVerifier{RootPub: root}}
	policy := &attest.Policy{Measurements: map[string]string{hex.EncodeToString(measurement): "node image v1"}}

	m := newMarketWith(t, coordinator.Config{Attestation: &coordinator.Attestation{Verifier: verifier, Policy: policy}})
	_, teeNode := m.addNodeWith(mockUpstream(t, "mock-7b", false).URL, 3*priceIn, 3*priceOut, att)
	_, plainNode := m.addNode(mockUpstream(t, "mock-7b", false).URL, priceIn, priceOut)
	m.waitOffers(2)

	cl, _, _ := m.addBuyer(5_000_000)
	cl.Attestation = &buyer.Attestation{Verifier: verifier, Policy: policy}
	res, err := cl.Chat(m.ctx, buyer.ChatRequest{Model: model, Body: []byte(body), BudgetMicros: 1_000_000, RequireTEE: true}, nil)
	if err != nil || res.Ticket.Node != teeNode.ID() || res.Attested != "node image v1" || !res.Verified {
		t.Fatalf("require_tee: %+v %v", res, err)
	}
	// Another conversation (the same one would stay on its node).
	res, err = cl.Chat(m.ctx, buyer.ChatRequest{Model: model, Body: []byte(body), BudgetMicros: 1_000_000, Session: "another"}, nil)
	if err != nil || res.Ticket.Node != plainNode.ID() {
		t.Fatalf("without require_tee the cheaper node should win: %+v %v", res, err)
	}
	if ms, _ := cl.Models(m.ctx); len(ms) != 1 || ms[0].AttestedNodes != 1 {
		t.Fatalf("model listing: %+v", ms)
	}

	// A buyer whose own policy does not list this image refuses the node, even
	// though the coordinator vouched for it.
	strict, _, _ := m.addBuyer(5_000_000)
	strict.Attestation = &buyer.Attestation{Verifier: verifier, Policy: &attest.Policy{Measurements: map[string]string{"00ff": "another image"}}}
	if _, err := strict.Chat(m.ctx, buyer.ChatRequest{Model: model, Body: []byte(body), BudgetMicros: 1_000_000, RequireTEE: true}, nil); err == nil {
		t.Fatal("a buyer accepted a node attested to an image outside its policy")
	}
}

// An audit of a node that inflates its bill fails on the receipt itself, and
// the audit job is refunded on evidence before the audit identity goes away.
func TestAuditOfAnInflatingNodeIsAMismatchAndItsJobIsRefunded(t *testing.T) {
	m := newMarket(t)
	_, inflator := m.addNode(answerUpstream(t, echoAnswer, 50_000).URL, priceIn, priceOut)
	m.waitOffers(1)
	aud, err := audit.New(m.coord.Engine, audit.Config{
		References: []audit.Reference{{
			Model: model, UpstreamModel: "mock-7b",
			Upstream: &node.OpenAICompat{BaseURL: answerUpstream(t, echoAnswer, 0).URL + "/v1"},
		}},
		Coordinator: m.info,
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		rec, err := aud.Audit(m.ctx, inflator.ID(), model)
		if err != nil || rec.Verdict != coordinator.AuditMismatch || !strings.Contains(rec.Detail, "receipt rejected") {
			t.Fatalf("audit of an inflating node: %+v %v", rec, err)
		}
	}
	st, _ := m.coord.Engine.NodeStats(inflator.ID())
	if st.SuspendedUntil == 0 || st.DisputesLost != 2 {
		t.Fatalf("inflating node record: %+v", st)
	}
	if got := m.balance(inflator); got != 0 {
		t.Fatalf("an inflating node was paid %d for audit jobs", got)
	}
}
