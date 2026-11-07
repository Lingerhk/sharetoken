// Package e2e runs the whole marketplace in-process over real libp2p hosts on
// localhost: a coordinator, seller nodes backed by a mock OpenAI-compatible
// model server, and buyers. Offers travel over GossipSub, jobs over direct
// buyer->node streams, settlement over the control protocol.
package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	lproto "github.com/libp2p/go-libp2p/core/protocol"

	"sharetoken/attest"
	"sharetoken/box"
	"sharetoken/buyer"
	"sharetoken/coordinator"
	"sharetoken/ledger"
	"sharetoken/node"
	"sharetoken/p2p"
	"sharetoken/protocol"
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
	h, k := newHost(m.t)
	var quote *attest.Quote
	if attestor != nil {
		q, err := attestor.Attest(attest.ReportData(k.ReceiptKey(), k.Box.Public()))
		if err != nil {
			m.t.Fatal(err)
		}
		quote = q
	}
	svc, err := node.NewService(m.ctx, h, k, node.Config{
		Coordinator:   m.info,
		Models:        []node.ModelConfig{{ID: model, UpstreamModel: "mock-7b", PriceInMicros: pin, PriceOutMicros: pout}},
		Upstream:      &node.OpenAICompat{BaseURL: upstreamURL + "/v1"},
		OfferInterval: 200 * time.Millisecond,
		Quote:         quote,
	})
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
	waitFor(m.t, 10*time.Second, fmt.Sprintf("%d offers over gossip", n), func() bool { return len(m.coord.Engine.Offers()) >= n })
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

	// The broken node's ticket was never settled; once its deadline passes the
	// hold returns to the buyer and the node is marked as failed.
	if held := m.held(buyerKeys); held != 1_000_000 {
		t.Fatalf("held %d, want the failed job's reservation", held)
	}
	m.coord.Engine.Tick(time.Now().Add(20 * time.Minute))
	if got := m.balance(buyerKeys); got != 5_000_000-20 {
		t.Fatalf("buyer balance %d after release, want %d", got, 5_000_000-20)
	}
	if got := m.balance(broken); got != 0 {
		t.Fatalf("the broken node must not be paid, got %d", got)
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
	sealed, _ := box.SealFrom(jobKey, tk.NodeBoxPub, []byte(body), protocol.RequestAAD(&tk))
	if err := protocol.Send(st, protocol.TypeInfer, protocol.InferRequest{Ticket: tk, BuyerBoxPub: jobKey.Public(), Sealed: sealed}); err != nil {
		return err
	}
	env, err := protocol.Recv(st)
	if err != nil {
		return err
	}
	return env.Decode(protocol.TypeChunk, nil)
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
