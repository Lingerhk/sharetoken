package e2e

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	lnet "github.com/libp2p/go-libp2p/core/network"
	lproto "github.com/libp2p/go-libp2p/core/protocol"

	"sharetoken/internal/buyer"
	"sharetoken/internal/coordinator"
	"sharetoken/internal/node"
	"sharetoken/internal/p2p"
	"sharetoken/internal/protocol"
	"sharetoken/internal/receipt"
)

// inflate replaces a settlement's receipt by one the node signs for far more
// than it delivered, without the buyer's ack (which is for the real one).
func inflate(t *testing.T, k **p2p.Keys) func(*node.Config) {
	return func(c *node.Config) {
		c.TamperSettle = func(req *protocol.SettleRequest) {
			r := req.Receipt.Receipt
			r.PromptTokens, r.CompletionTokens = 200_000, 200_000
			r.Nonce = []byte("fedcba9876543210")
			sr, err := receipt.Sign((*k).Signer, r)
			if err != nil {
				t.Error(err)
				return
			}
			req.Receipt, req.Ack = *sr, nil
		}
	}
}

// A node serves the job, shows the buyer an honest receipt, and settles with
// another it signed for the whole reservation (the audit's CO-1). The buyer
// brings the coordinator the receipt it acknowledged, so the job is settled
// at that receipt's cost, or refunded when the node's claim got there first.
func TestNodeCannotSettleAnotherReceiptThanItShowed(t *testing.T) {
	m := newMarketWith(t, coordinator.Config{DisputeWindow: time.Second})
	var nodeKeys *p2p.Keys
	_, nodeKeys = m.addNodeCfg(mockUpstream(t, "mock-7b", false).URL, priceIn, priceOut, nil, inflate(t, &nodeKeys))
	m.waitOffers(1)
	cl, _, buyerKeys := m.addBuyer(5_000_000)

	res, err := cl.Chat(m.ctx, buyer.ChatRequest{Model: model, Body: []byte(body), BudgetMicros: 1_000_000}, nil)
	if err != nil || !res.Verified || res.CostMicros != 20 {
		t.Fatalf("want a verified job at 20, got %+v %v", res, err)
	}
	ctx, cancel := context.WithTimeout(m.ctx, 10*time.Second)
	defer cancel()
	if err := cl.WaitDisputes(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "the job to be settled for good", func() bool {
		st, _ := m.coord.Engine.JobStatus(res.Ticket.JobID)
		return st == coordinator.StatusFinal || st == coordinator.StatusRefunded
	})
	// Past the dispute window, with every settlement attempt of the node in.
	time.Sleep(2 * time.Second)
	m.coord.Engine.Tick(time.Now())
	if paid := 5_000_000 - m.balance(buyerKeys); paid != 20 && paid != 0 {
		t.Fatalf("the buyer paid %d: neither the receipt it checked (20) nor a refund", paid)
	}
	if m.held(buyerKeys) != 0 {
		t.Fatalf("held %d after settlement", m.held(buyerKeys))
	}
	if got := m.balance(nodeKeys); got > 19 {
		t.Fatalf("the node earned %d with a receipt the buyer never saw", got)
	}
	if err := m.coord.Engine.CheckBooks(); err != nil {
		t.Fatal(err)
	}
}

// The stream stops after some output reached the caller, and the node then
// settles a receipt for the whole reservation (the audit's BN-1, the path of
// every streaming client). The buyer told the coordinator what arrived, so
// the claim goes to arbitration instead of being paid after the window.
func TestInflatedReceiptAfterABrokenStreamIsDisputed(t *testing.T) {
	m := newMarketWith(t, coordinator.Config{DisputeWindow: time.Second})
	up, _ := pacedUpstream(t, 400, 20*time.Millisecond)
	var nodeKeys *p2p.Keys
	_, nodeKeys = m.addNodeCfg(up.URL, priceIn, priceOut, nil, func(c *node.Config) {
		c.ResumeWindow = time.Second
		inflate(t, &nodeKeys)(c)
	})
	m.waitOffers(1)
	cl, buyerHost, buyerKeys := m.addBuyer(5_000_000)
	cl.ResumeWindow = 0
	n := 0
	_, err := cl.Chat(m.ctx, buyer.ChatRequest{Model: model, Body: []byte(body), BudgetMicros: 1_000_000}, func([]byte) error {
		if n++; n == 5 {
			_ = buyerHost.Network().ClosePeer(nodeKeys.PeerID)
		}
		return nil
	})
	if err == nil {
		t.Fatal("the job went on without the buyer")
	}
	d := m.waitDispute(nodeKeys.ID(), func(d coordinator.DisputeInfo) bool { return d.Status == coordinator.StatusDisputed })
	if !strings.Contains(d.Reason, "accepts at most") {
		t.Fatalf("dispute reason %q", d.Reason)
	}
	time.Sleep(1500 * time.Millisecond)
	m.coord.Engine.Tick(time.Now())
	if st, _ := m.coord.Engine.JobStatus(d.JobID); st != coordinator.StatusDisputed {
		t.Fatalf("the job is %s after the dispute window", st)
	}
	if got := m.balance(nodeKeys); got != 0 {
		t.Fatalf("the node was paid %d", got)
	}
	_ = buyerKeys
}

// The caller stops mid-stream and the node bills more than was delivered:
// the same defence. (An honest node's bill for the delivered part passes:
// TestBuyerCancelMidStreamBillsDeliveredPart.)
func TestInflatedReceiptAfterACancelIsDisputed(t *testing.T) {
	m := newMarketWith(t, coordinator.Config{DisputeWindow: time.Second})
	up, _ := pacedUpstream(t, 400, 10*time.Millisecond)
	var nodeKeys *p2p.Keys
	_, nodeKeys = m.addNodeCfg(up.URL, priceIn, priceOut, nil, inflate(t, &nodeKeys))
	m.waitOffers(1)
	cl, _, _ := m.addBuyer(5_000_000)
	stop := errors.New("user pressed stop")
	n := 0
	if _, err := cl.Chat(m.ctx, buyer.ChatRequest{Model: model, Body: []byte(body), BudgetMicros: 1_000_000}, func([]byte) error {
		if n++; n == 5 {
			return stop
		}
		return nil
	}); !errors.Is(err, stop) {
		t.Fatalf("want the caller's error, got %v", err)
	}
	m.waitDispute(nodeKeys.ID(), func(d coordinator.DisputeInfo) bool { return d.Status == coordinator.StatusDisputed })
	if got := m.balance(nodeKeys); got != 0 {
		t.Fatalf("the node was paid %d", got)
	}
}

// A node answers "bad request" and settles a receipt of its own all the same
// (BN-1's first path). The buyer pre-disputes refused jobs, so the claim is
// never paid out on the node's word.
func TestReceiptAfterARefusalIsDisputed(t *testing.T) {
	m := newMarketWith(t, coordinator.Config{DisputeWindow: time.Second})
	h, k := newHost(t)
	now := time.Now()
	offer := &protocol.Offer{
		Node: k.ID(), BoxPub: k.Box.Public(), ReceiptKey: k.ReceiptKey(),
		Models:         []protocol.ModelPrice{{Model: model, PriceInMicros: priceIn, PriceOutMicros: priceOut}},
		MaxConcurrency: 4, IssuedAt: now.UnixNano(), ExpiresAt: now.Add(time.Minute).UnixNano(),
	}
	for _, a := range h.Addrs() {
		offer.Addrs = append(offer.Addrs, a.String())
	}
	offer.Sign(k.Signer)
	if err := h.Connect(m.ctx, m.info); err != nil { // only connected nodes are matched
		t.Fatal(err)
	}
	if err := m.coord.Engine.UpsertOffer(offer); err != nil {
		t.Fatal(err)
	}
	settled := make(chan string, 4)
	h.SetStreamHandler(lproto.ID(protocol.InferProtocol), func(st lnet.Stream) {
		defer st.Close()
		env, err := protocol.RecvStream(st, protocol.MaxSmallFrame)
		if err != nil {
			return
		}
		var req protocol.InferRequest
		if env.Decode(protocol.TypeInfer, &req) != nil {
			return
		}
		tk := req.Ticket
		// Megabytes on the wire that the buyer would have to quote in its dispute (BN-4).
		_ = protocol.Send(st, protocol.TypeError, protocol.Errorf(protocol.CodeBadRequest, "%s", strings.Repeat("<&>", 200_000)))
		sr, err := receipt.Sign(k.Signer, receipt.Receipt{
			JobID: tk.JobID, ModelID: tk.Model, NodeID: k.ReceiptKey(),
			RequestHash: receipt.Hash([]byte("req")), ResponseHash: receipt.Hash([]byte("resp")),
			PromptTokens: 200_000, CompletionTokens: 200_000, UnixNano: time.Now().UnixNano(), Nonce: []byte("0123456789abcdef"),
		})
		if err != nil {
			return
		}
		if _, err := m.coord.Engine.Settle(k.ID(), protocol.SettleRequest{Ticket: tk, Receipt: *sr}); err == nil {
			settled <- tk.JobID
		}
	})
	cl, _, buyerKeys := m.addBuyer(5_000_000)

	_, err := cl.Chat(m.ctx, buyer.ChatRequest{Model: model, Body: []byte(body), BudgetMicros: 1_000_000}, nil)
	if protocol.CodeOf(err) != protocol.CodeBadRequest {
		t.Fatalf("want bad_request, got %v", err)
	}
	if len(err.Error()) > 2000 {
		t.Fatalf("the node's error reached the caller at %d bytes", len(err.Error()))
	}
	ctx, cancel := context.WithTimeout(m.ctx, 15*time.Second)
	defer cancel()
	if err := cl.WaitDisputes(ctx); err != nil {
		t.Fatalf("the disputes were not delivered: %v", err)
	}
	var jobID string
	select {
	case jobID = <-settled:
	case <-time.After(5 * time.Second):
		t.Fatal("the node did not get to settle")
	}
	m.waitStatus(jobID, coordinator.StatusDisputed)
	time.Sleep(1500 * time.Millisecond)
	m.coord.Engine.Tick(time.Now())
	if st, _ := m.coord.Engine.JobStatus(jobID); st != coordinator.StatusDisputed {
		t.Fatalf("the job is %s after the dispute window", st)
	}
	if got := m.balance(k); got != 0 {
		t.Fatalf("the node was paid %d", got)
	}
	if got := m.balance(buyerKeys) + m.held(buyerKeys); got != 5_000_000 {
		t.Fatalf("the buyer's balance and holds come to %d", got)
	}
}

// A buyer that sends a prompt under a second, differently capitalised field
// to shrink the estimate (the audit's BN-2) is billed for what the model
// server counted: the estimate, and so the bound of the node's bill, covers
// the whole body.
func TestPromptCannotHideBehindACapitalisedField(t *testing.T) {
	m := newMarket(t)
	const promptTokens = 6000
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprintf(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":%d,\"completion_tokens\":1}}\n\n", promptTokens)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(up.Close)
	_, nodeKeys := m.addNode(up.URL, priceIn, priceOut)
	m.waitOffers(1)
	cl, _, _ := m.addBuyer(50_000_000)
	big := strings.Repeat("lorem ipsum dolor sit amet ", 900) // 24 kB, about 6000 tokens
	tricky := `{"messages":[{"role":"user","content":"` + big + `"}],"Messages":[{"role":"user","content":"hi"}]}`
	res, err := cl.Chat(m.ctx, buyer.ChatRequest{Model: model, Body: []byte(tricky), BudgetMicros: 10_000_000}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Receipt.Receipt.PromptTokens; got != promptTokens {
		t.Fatalf("billed %d prompt tokens, the model server counted %d", got, promptTokens)
	}
	m.waitStatus(res.Ticket.JobID, coordinator.StatusFinal)
	if got := m.balance(nodeKeys); got < promptTokens*9/10 {
		t.Fatalf("the node earned %d for a %d-token prompt", got, promptTokens)
	}
}
