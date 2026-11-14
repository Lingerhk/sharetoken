// Package e2e runs the whole marketplace in-process over real libp2p hosts on
// localhost: a coordinator, seller nodes backed by a mock OpenAI-compatible
// model server, and buyers. Nodes announce offers over the control protocol,
// jobs run on buyer->node streams, settlement over the control protocol.
//
// These tests connect peers to the coordinator directly and use open accounts
// (one account per peer); arch_test.go runs the full topology: relays,
// portal-managed user accounts, and coordinator reachable only via relays.
package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sharetoken/internal/mysqldb"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	lproto "github.com/libp2p/go-libp2p/core/protocol"

	"sharetoken/internal/attest"
	"sharetoken/internal/box"
	"sharetoken/internal/buyer"
	"sharetoken/internal/coordinator"
	"sharetoken/internal/ledger"
	"sharetoken/internal/node"
	"sharetoken/internal/p2p"
	"sharetoken/internal/protocol"
)

const (
	model    = "test-model"
	priceIn  = 1_000_000 // 1 micro per prompt token
	priceOut = 2_000_000 // 2 micros per completion token
	feeBps   = 800
	body     = `{"model":"test-model","n":5,"messages":[{"role":"user","content":"hi"}]}`
)

func newHost(t *testing.T) (host.Host, *p2p.Keys) {
	t.Helper()
	k, err := p2p.NewKeys()
	if err != nil {
		t.Fatal(err)
	}
	h, err := p2p.NewHost(k, p2p.HostConfig{Listen: []string{"/ip4/127.0.0.1/tcp/0"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h, k
}

// mockUpstream streams "Hello, world!" and reports 10 prompt / 5 completion
// tokens. It asserts the node rewrote the buyer's request before forwarding.
func mockUpstream(t *testing.T, upstreamModel string, broken bool) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if broken {
			http.Error(w, "model crashed", http.StatusInternalServerError)
			return
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("upstream got non-JSON body: %v", err)
		}
		so, _ := req["stream_options"].(map[string]any)
		mt, _ := req["max_tokens"].(float64)
		switch {
		case req["model"] != upstreamModel:
			t.Errorf("node forwarded model %v, want %s", req["model"], upstreamModel)
		case req["stream"] != true || so["include_usage"] != true:
			t.Errorf("node must force streaming with usage, got %v", req)
		case req["n"] != nil:
			t.Errorf("node forwarded n=%v; buyers must not multiply work", req["n"])
		case mt <= 0 || mt > node.HardMaxOutputTokens:
			t.Errorf("node sent max_tokens=%v", mt)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for _, piece := range []string{"Hello", ", ", "world!"} {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", piece)
			fl.Flush()
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

type market struct {
	t     *testing.T
	ctx   context.Context
	coord *coordinator.Service
	info  peer.AddrInfo
}

func newMarket(t *testing.T) *market { return newMarketWith(t, coordinator.Config{}) }

func newMarketWith(t *testing.T, cfg coordinator.Config) *market {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h, k := newHost(t)
	cfg.FeeBps = feeBps
	cfg.OpenAccounts = true
	db, err := mysqldb.OpenTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	cfg.DB = db
	svc, err := coordinator.NewService(ctx, h, k, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	return &market{t: t, ctx: ctx, coord: svc, info: peer.AddrInfo{ID: h.ID(), Addrs: h.Addrs()}}
}

func (m *market) addNode(upstreamURL string, pin, pout uint64) (host.Host, *p2p.Keys) {
	m.t.Helper()
	return m.addNodeWith(upstreamURL, pin, pout, nil)
}

// addNodeWith starts a node; attestor, if set, makes it a (mock) TEE node.
func (m *market) addNodeWith(upstreamURL string, pin, pout uint64, attestor attest.Attestor) (host.Host, *p2p.Keys) {
	m.t.Helper()
	return m.addNodeCfg(upstreamURL, pin, pout, attestor, nil)
}

// addNodeCfg is addNodeWith with edit adjusting the node's configuration.
func (m *market) addNodeCfg(upstreamURL string, pin, pout uint64, attestor attest.Attestor, edit func(*node.Config)) (host.Host, *p2p.Keys) {
	m.t.Helper()
	h, k := newHost(m.t)
	var quote *attest.Quote
	if attestor != nil {
		q, err := attestor.Attest(attest.ReportData(k.ReceiptKey(), k.Box.Public()))
		if err != nil {
			m.t.Fatal(err)
		}
		quote = q
	}
	cfg := node.Config{
		Coordinator:   m.info,
		Models:        []node.ModelConfig{{ID: model, UpstreamModel: "mock-7b", PriceInMicros: pin, PriceOutMicros: pout}},
		Upstream:      &node.OpenAICompat{BaseURL: upstreamURL + "/v1"},
		OfferInterval: 200 * time.Millisecond,
		Quote:         quote,
	}
	if edit != nil {
		edit(&cfg)
	}
	svc, err := node.NewService(m.ctx, h, k, cfg)
	if err != nil {
		m.t.Fatal(err)
	}
	m.t.Cleanup(svc.Close)
	return h, k
}

func (m *market) addBuyer(credit uint64) (*buyer.Client, host.Host, *p2p.Keys) {
	m.t.Helper()
	h, k := newHost(m.t)
	if credit > 0 {
		if err := m.coord.Engine.Deposit(k.ID(), credit); err != nil {
			m.t.Fatal(err)
		}
	}
	return buyer.New(h, k, m.info), h, k
}

func (m *market) waitOffers(n int) {
	m.t.Helper()
	waitFor(m.t, 10*time.Second, fmt.Sprintf("%d offers announced", n), func() bool { return len(m.coord.Engine.Offers()) >= n })
}

func (m *market) waitStatus(jobID string, want coordinator.Status) {
	m.t.Helper()
	waitFor(m.t, 10*time.Second, "job "+string(want), func() bool {
		st, _ := m.coord.Engine.JobStatus(jobID)
		return st == want
	})
}

func (m *market) balance(k *p2p.Keys) int64 { return m.account(ledger.Account(k.ID())) }

func (m *market) account(acct string) int64 {
	m.t.Helper()
	b, err := m.coord.Engine.AccountBalance(acct)
	if err != nil {
		m.t.Fatal(err)
	}
	return b
}

func (m *market) held(k *p2p.Keys) int64 {
	m.t.Helper()
	b, err := m.coord.Engine.Balance(k.ID())
	if err != nil {
		m.t.Fatal(err)
	}
	return b.HeldMicros
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestVerifiedJobSettlesWithAck(t *testing.T) {
	m := newMarket(t)
	_, nodeKeys := m.addNode(mockUpstream(t, "mock-7b", false).URL, priceIn, priceOut)
	m.waitOffers(1)
	cl, _, buyerKeys := m.addBuyer(5_000_000)

	var chunks int
	res, err := cl.Chat(m.ctx, buyer.ChatRequest{Model: model, Body: []byte(body), BudgetMicros: 1_000_000},
		func([]byte) error { chunks++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "Hello, world!" || res.FinishReason != "stop" || !res.Verified || chunks != 5 {
		t.Fatalf("unexpected result: %+v (chunks=%d)", res, chunks)
	}
	if res.CostMicros != 20 { // 10*1 + 5*2
		t.Fatalf("cost %d, want 20", res.CostMicros)
	}
	m.waitStatus(res.Ticket.JobID, coordinator.StatusFinal)

	// 8% of 20 = 1.6 -> fee 1, node earns 19; the unused reservation is refunded.
	if got := m.balance(buyerKeys); got != 5_000_000-20 {
		t.Errorf("buyer balance %d, want %d", got, 5_000_000-20)
	}
	if got := m.balance(nodeKeys); got != 19 {
		t.Errorf("node earned %d, want 19", got)
	}
	if got := m.account(ledger.PlatformFees); got != 1 {
		t.Errorf("platform fee %d, want 1", got)
	}
	if err := m.coord.Engine.CheckBooks(); err != nil {
		t.Errorf("books do not balance: %v", err)
	}
}

func TestFailoverBeforeFirstByte(t *testing.T) {
	m := newMarket(t)
	// The broken node is cheaper, so it is matched first.
	_, broken := m.addNode(mockUpstream(t, "mock-7b", true).URL, priceIn/2, priceOut/2)
	_, healthy := m.addNode(mockUpstream(t, "mock-7b", false).URL, priceIn, priceOut)
	m.waitOffers(2)
	cl, _, buyerKeys := m.addBuyer(5_000_000)

	res, err := cl.Chat(m.ctx, buyer.ChatRequest{Model: model, Body: []byte(body), BudgetMicros: 1_000_000}, nil)
	if err != nil {
		t.Fatalf("failover should hide the broken node: %v", err)
	}
	if res.Ticket.Node != healthy.ID() || res.Content != "Hello, world!" {
		t.Fatalf("served by %s, want the healthy node", res.Ticket.Node)
	}
	m.waitStatus(res.Ticket.JobID, coordinator.StatusFinal)

	// The broken node abandoned its ticket: the buyer's hold came back at
	// once (not at the deadline) and the node is marked as failed, unpaid.
	waitFor(t, 10*time.Second, "the abandoned hold to be released", func() bool { return m.held(buyerKeys) == 0 })
	if got := m.balance(buyerKeys); got != 5_000_000-20 {
		t.Fatalf("buyer balance %d after release, want %d", got, 5_000_000-20)
	}
	if got := m.balance(broken); got != 0 {
		t.Fatalf("the broken node must not be paid, got %d", got)
	}
	if st, _ := m.coord.Engine.NodeStats(broken.ID()); st.Failures != 1 {
		t.Fatalf("broken node failures = %d, want 1", st.Failures)
	}
}

// An invalid request that the model servers reject (HTTP 400) is returned to
// the caller as bad_request after at most two nodes, and costs the nodes no
// reputation: a buyer cannot drain honest nodes by sending garbage.
func TestRejectedRequestIsNotANodeFailure(t *testing.T) {
	m := newMarket(t)
	var calls atomic.Int32
	rejecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"[] is too short - 'messages'"}}`)
	}))
	t.Cleanup(rejecting.Close)
	var nodes []*p2p.Keys
	for i := 0; i < 3; i++ {
		_, k := m.addNode(rejecting.URL, priceIn, priceOut)
		nodes = append(nodes, k)
	}
	m.waitOffers(3)
	cl, _, buyerKeys := m.addBuyer(5_000_000)

	_, err := cl.Chat(m.ctx, buyer.ChatRequest{Model: model, Body: []byte(body), BudgetMicros: 1_000_000}, nil)
	if protocol.CodeOf(err) != protocol.CodeBadRequest || !strings.Contains(err.Error(), "too short") {
		t.Fatalf("want bad_request carrying the model server's reason, got %v", err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("request tried on %d nodes, want 2 (one confirmation, then stop)", n)
	}
	waitFor(t, 10*time.Second, "the holds to be released", func() bool { return m.held(buyerKeys) == 0 })
	if got := m.balance(buyerKeys); got != 5_000_000 {
		t.Fatalf("buyer charged for rejected requests: balance %d", got)
	}
	for _, k := range nodes {
		if st, _ := m.coord.Engine.NodeStats(k.ID()); st.Failures != 0 {
			t.Fatalf("node %s penalized for the buyer's bad request: %+v", k.ID(), st)
		}
	}
}

// A buyer that stops reading mid-stream (a user pressing "stop") pays for
// the chunks it was sent, settled without an ack (pending through the dispute
// window), and the node is not penalized. Leaving before the first chunk
// costs nothing.
func TestBuyerCancelMidStreamBillsDeliveredPart(t *testing.T) {
	m := newMarketWith(t, coordinator.Config{DisputeWindow: time.Second})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for i := 0; i < 200; i++ {
			if _, err := fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"word%03d \"}}]}\n\n", i); err != nil {
				return
			}
			fl.Flush()
			time.Sleep(10 * time.Millisecond)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(slow.Close)
	_, nodeKeys := m.addNode(slow.URL, priceIn, priceOut)
	m.waitOffers(1)
	cl, _, buyerKeys := m.addBuyer(5_000_000)

	got := 0
	stop := errors.New("user pressed stop")
	res, err := cl.Chat(m.ctx, buyer.ChatRequest{Model: model, Body: []byte(body), BudgetMicros: 1_000_000}, func([]byte) error {
		if got++; got == 5 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || res != nil {
		t.Fatalf("want the caller's error and no result, got %v / %v", res, err)
	}
	// Charged for the delivered part only: a few of the 200 chunks.
	full := int64(protocol.TicketCost(&protocol.Ticket{PriceInMicros: priceIn, PriceOutMicros: priceOut, ReservedMicros: 1 << 40}, 20, 400))
	waitFor(t, 15*time.Second, "the cancelled job to be settled (pending, then final after the dispute window)", func() bool {
		return m.held(buyerKeys) == 0 && m.balance(buyerKeys) < 5_000_000
	})
	if paid := 5_000_000 - m.balance(buyerKeys); paid <= 0 || paid >= full/4 {
		t.Fatalf("paid %d for ~5 of 200 chunks (full answer would cost %d)", paid, full)
	}
	if st, _ := m.coord.Engine.NodeStats(nodeKeys.ID()); st.Failures != 0 {
		t.Fatalf("node penalized for the buyer's cancel: %+v", st)
	}
}

// A node that stalls mid-answer (lossy path, hung model server) is abandoned
// after the idle timeout; a non-streaming request, which has delivered
// nothing to the caller yet, fails over to another node instead of hanging.
func TestStalledNodeIsAbandonedAfterIdleTimeout(t *testing.T) {
	m := newMarket(t)
	release := make(chan struct{})
	stalling := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n")
		w.(http.Flusher).Flush()
		select { // then nothing
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); stalling.Close() })
	_, stalled := m.addNode(stalling.URL, priceIn/2, priceOut/2) // cheaper: matched first
	_, healthy := m.addNode(mockUpstream(t, "mock-7b", false).URL, priceIn, priceOut)
	m.waitOffers(2)
	cl, _, _ := m.addBuyer(5_000_000)
	cl.IdleTimeout = time.Second

	start := time.Now()
	res, err := cl.Chat(m.ctx, buyer.ChatRequest{Model: model, Body: []byte(body), BudgetMicros: 1_000_000}, nil)
	if err != nil {
		t.Fatalf("want failover to the healthy node, got %v", err)
	}
	if res.Ticket.Node != healthy.ID() || res.Content != "Hello, world!" {
		t.Fatalf("served by %s (stalled node is %s)", res.Ticket.Node, stalled.ID())
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("failover took %v", d)
	}
}

// sendInfer opens a raw infer stream from h and returns the node's first reply.
func sendInfer(t *testing.T, h host.Host, tk protocol.Ticket) error {
	t.Helper()
	nodeID, _ := peer.Decode(tk.Node)
	st, err := h.NewStream(context.Background(), nodeID, lproto.ID(protocol.InferProtocol))
	if err != nil {
		return err
	}
	defer st.Close()
	jobKey, _ := box.GenerateIdentity()
	encoded, _ := protocol.EncodeRequest([]byte(body))
	sealed, _ := box.SealFrom(jobKey, tk.NodeBoxPub, encoded, protocol.RequestAAD(&tk))
	if err := protocol.SendRequest(st, protocol.TypeInfer, protocol.InferRequest{Ticket: tk, BuyerBoxPub: jobKey.Public()}); err != nil {
		return err
	}
	if err := protocol.SendBinary(st, protocol.BinRequest, sealed); err != nil {
		return err
	}
	f, err := protocol.ReadFrame(st, protocol.MaxFrameSize)
	if err != nil {
		return err
	}
	if f.Env == nil {
		return nil // response data or progress: the node took the job
	}
	return f.Env.Decode("data", nil)
}

func TestNodeRejectsReplayedAndStolenTickets(t *testing.T) {
	m := newMarket(t)
	nodeHost, _ := m.addNode(mockUpstream(t, "mock-7b", false).URL, priceIn, priceOut)
	m.waitOffers(1)
	cl, buyerHost, _ := m.addBuyer(5_000_000)

	res, err := cl.Chat(m.ctx, buyer.ChatRequest{Model: model, Body: []byte(body), BudgetMicros: 1_000_000}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Same buyer replays the ticket: refused, so a job can't be run twice and paid once.
	if err := sendInfer(t, buyerHost, res.Ticket); protocol.CodeOf(err) != protocol.CodeBadTicket {
		t.Fatalf("replayed ticket: want bad_ticket, got %v", err)
	}

	// Another peer presents a stolen, still-valid ticket. libp2p authenticated
	// Eve's peer ID, which is not the buyer the ticket names.
	eveHost, _ := newHost(t)
	eveHost.Peerstore().AddAddrs(nodeHost.ID(), nodeHost.Addrs(), time.Minute)
	if err := sendInfer(t, eveHost, res.Ticket); protocol.CodeOf(err) != protocol.CodeBadTicket {
		t.Fatalf("stolen ticket: want bad_ticket, got %v", err)
	}
}

func TestInsufficientFundsIsRefusedUpfront(t *testing.T) {
	m := newMarket(t)
	m.addNode(mockUpstream(t, "mock-7b", false).URL, priceIn, priceOut)
	m.waitOffers(1)
	cl, _, _ := m.addBuyer(0)
	_, err := cl.Chat(m.ctx, buyer.ChatRequest{Model: model, Body: []byte(body), BudgetMicros: 1_000}, nil)
	if protocol.CodeOf(err) != protocol.CodeInsufficientFunds {
		t.Fatalf("want insufficient_funds, got %v", err)
	}
}

func TestLocalProxyIsOpenAICompatible(t *testing.T) {
	m := newMarket(t)
	m.addNode(mockUpstream(t, "mock-7b", false).URL, priceIn, priceOut)
	m.waitOffers(1)
	cl, _, _ := m.addBuyer(5_000_000)
	srv := httptest.NewServer((&buyer.Proxy{Client: cl, BudgetMicros: 1_000_000}).Handler())
	t.Cleanup(srv.Close)

	// Non-streaming: returned only after the receipt verified.
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Choices []struct {
			Message struct{ Content string } `json:"message"`
		} `json:"choices"`
		Usage struct {
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || len(out.Choices) != 1 || out.Choices[0].Message.Content != "Hello, world!" || out.Usage.CompletionTokens != 5 {
		t.Fatalf("bad non-stream response: %d %+v", resp.StatusCode, out)
	}
	if resp.Header.Get("X-Sharetoken-Verified") != "true" {
		t.Fatal("missing verification header")
	}

	// Streaming: SSE passthrough, then a verification comment and [DONE].
	streamBody := strings.Replace(body, `"n":5`, `"stream":true`, 1)
	resp, err = http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(streamBody))
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if l := sc.Text(); l != "" {
			lines = append(lines, l)
		}
	}
	resp.Body.Close()
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, `"content":"Hello"`) || !strings.Contains(joined, "verified=true") || lines[len(lines)-1] != "data: [DONE]" {
		t.Fatalf("bad SSE stream:\n%s", joined)
	}

	// Model listing.
	resp, err = http.Get(srv.URL + "/v1/models")
	if err != nil {
		t.Fatal(err)
	}
	listing, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(listing), `"id":"test-model"`) {
		t.Fatalf("model missing from listing: %s", listing)
	}
}

// A buyer that takes a ticket and never connects blocks the node only until
// the ticket expires (plus a grace), not until the settlement deadline.
func TestNoShowTicketFreesTheNodeSoon(t *testing.T) {
	m := newMarketWith(t, coordinator.Config{TicketTTL: 2 * time.Second, StartGrace: 500 * time.Millisecond})
	_, nodeKeys := m.addNode(mockUpstream(t, "mock-7b", false).URL, priceIn, priceOut)
	m.waitOffers(1)
	// The node serves 2 at once (default); take both slots without connecting.
	_, _, noShow := m.addBuyer(5_000_000)
	for i := 0; i < 2; i++ {
		if _, err := m.coord.Engine.IssueTicket(noShow.ID(), protocol.TicketRequest{InferVersions: protocol.SupportedInferVersions, Model: model, BudgetMicros: 1000}); err != nil {
			t.Fatal(err)
		}
	}
	cl, _, buyerKeys := m.addBuyer(5_000_000)
	cl.QueueWait = 0
	if _, err := cl.Chat(m.ctx, buyer.ChatRequest{Model: model, Body: []byte(body), BudgetMicros: 1_000_000}, nil); protocol.CodeOf(err) != protocol.CodeCapacity {
		t.Fatalf("node with both slots taken: want capacity, got %v", err)
	}
	// With a queue the request waits for the no-show holds to be released
	// and then succeeds, instead of failing.
	cl.QueueWait = 10 * time.Second
	start := time.Now()
	res, err := cl.Chat(m.ctx, buyer.ChatRequest{Model: model, Body: []byte(body), BudgetMicros: 1_000_000}, nil)
	if err != nil || !res.Verified {
		t.Fatalf("queued request: %v", err)
	}
	if time.Since(start) < time.Second {
		t.Fatalf("served after %v, before the no-show tickets' start deadline", time.Since(start))
	}
	// Their slots are free from the start deadline on; the holds go back at
	// the coordinator's next tick.
	waitFor(t, 5*time.Second, "the no-show holds to be released", func() bool { return m.held(noShow) == 0 })
	m.waitStatus(res.Ticket.JobID, coordinator.StatusFinal)
	if got := m.balance(buyerKeys); got != 5_000_000-20 {
		t.Fatalf("buyer balance %d", got)
	}
	if st, _ := m.coord.Engine.NodeStats(nodeKeys.ID()); st.Failures != 0 {
		t.Fatalf("no-shows must not count against the node: %+v", st)
	}
}

func waitUntil(timeout time.Duration, cond func() bool) bool {
	for deadline := time.Now().Add(timeout); !cond(); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			return false
		}
	}
	return true
}
