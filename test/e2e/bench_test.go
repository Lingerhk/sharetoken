//go:build bench

// Benchmarks of the buyer <-> node infer protocol through a relay, on the full
// in-process topology of arch_test.go (portal, MySQL, coordinator reachable
// only through relays, real client agents and their local /v1 API).
//
// The seller listens on IPv6 only and the buyer can dial IPv4 only, so every
// job runs over a relay circuit. A built-in upstream streams OpenAI-style
// chunks shaped by each scenario. The file uses only interfaces that exist
// before and after the protocol changes, so the same file measures both.
//
//	go test -tags bench -run TestBench -v -timeout 30m ./test/e2e/
//
// BENCH_OUT=<file> appends one JSON line per measurement; BENCH_REPS sets the
// repetitions per scenario (default 3).
package e2e

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/metrics"
	lproto "github.com/libp2p/go-libp2p/core/protocol"
	ma "github.com/multiformats/go-multiaddr"

	"sharetoken/internal/client"
	"sharetoken/internal/node"
	"sharetoken/internal/protocol"
)

// scenario shapes one request and the upstream's answer to it.
type scenario struct {
	Name         string
	PromptBytes  int           // approximate size of the request JSON
	Chunks       int           // streamed content chunks
	TokPerChunk  int           // words per chunk
	Delay        time.Duration // between chunks
	SilenceAfter int           // chunks before a pause (0: none)
	Silence      time.Duration
	Session      string // session_id sent with the request ("" : none)
	NoSettle     bool   // skip the pause that lets byte counters settle
}

// measurement is one request's outcome.
type measurement struct {
	Scenario   string  `json:"scenario"`
	Build      string  `json:"build"`
	OK         bool    `json:"ok"`
	Err        string  `json:"err,omitempty"`
	ReqBytes   int     `json:"req_bytes"`   // request JSON posted to the local API
	Chunks     int     `json:"chunks"`      // data lines the caller received
	UpBytes    int64   `json:"up_bytes"`    // infer-protocol bytes the buyer sent
	DownBytes  int64   `json:"down_bytes"`  // infer-protocol bytes the buyer received
	RelayBytes int64   `json:"relay_bytes"` // bytes all relays forwarded meanwhile
	DownPerChk float64 `json:"down_per_chunk"`
	TTFTms     float64 `json:"ttft_ms"`
	TotalMs    float64 `json:"total_ms"`
	Node       string  `json:"node,omitempty"`
}

var (
	corpusOnce  sync.Once
	corpusCode  []string // repository Go sources: tool results in agent prompts
	corpusWords []string // English words of the docs: model output
)

func loadCorpus(t *testing.T) {
	corpusOnce.Do(func() {
		root := "../.."
		_ = filepath.WalkDir(filepath.Join(root, "internal"), func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
				if b, err := os.ReadFile(p); err == nil {
					corpusCode = append(corpusCode, string(b))
				}
			}
			return nil
		})
		sort.Strings(corpusCode)
		for _, f := range []string{"docs/architecture.md", "docs/security.md", "README.md"} {
			if b, err := os.ReadFile(filepath.Join(root, f)); err == nil {
				for _, w := range strings.Fields(string(b)) {
					if len(w) < 20 && !strings.ContainsAny(w, "`|#*<>[]()") {
						corpusWords = append(corpusWords, w)
					}
				}
			}
		}
	})
	if len(corpusCode) == 0 || len(corpusWords) == 0 {
		t.Fatal("bench corpus is empty")
	}
}

// requestBody builds an agent-like chat request of about n bytes: a system
// prompt, then turns of user questions, assistant tool calls and tool results
// holding repository source files.
func requestBody(n int, session string) []byte {
	msgs := []map[string]any{{"role": "system", "content": "You are a coding agent. Read the files the tools return and answer precisely."}}
	size, i := 200, 0
	for size < n {
		code := corpusCode[i%len(corpusCode)]
		if rest := n - size; len(code) > rest {
			code = code[:max(rest, 1)]
		}
		id := fmt.Sprintf("call_%d", i)
		msgs = append(msgs,
			map[string]any{"role": "user", "content": fmt.Sprintf("Step %d: what does the next file do?", i)},
			map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{map[string]any{"id": id, "type": "function", "function": map[string]any{"name": "read_file", "arguments": fmt.Sprintf(`{"path":"file_%d.go"}`, i)}}}},
			map[string]any{"role": "tool", "tool_call_id": id, "content": code})
		size += len(code) + 300
		i++
	}
	msgs = append(msgs, map[string]any{"role": "user", "content": "Summarise what you read."})
	req := map[string]any{"model": model, "stream": true, "messages": msgs}
	if session != "" {
		req["session_id"] = session
	}
	b, _ := json.Marshal(req)
	return b
}

// benchUpstream streams scenario sc's answer as an OpenAI-compatible server
// (vLLM-style chunks) and reports usage the buyer finds plausible.
func benchUpstream(t *testing.T, sc *scenario) *httptest.Server {
	loadCorpus(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"mock-7b","object":"model"}]}`)
			return
		}
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r.Body)
		prompt, _ := protocol.EstimatePromptTokens(protocol.APIChat, buf.Bytes())
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		rng := rand.New(rand.NewPCG(1, 2))
		id := fmt.Sprintf("chatcmpl-%024x", rng.Uint64())
		created := time.Now().Unix()
		chunk := func(delta string, finish string) {
			f := "null"
			if finish != "" {
				f = strconv.Quote(finish)
			}
			fmt.Fprintf(w, "data: {\"id\":%q,\"object\":\"chat.completion.chunk\",\"created\":%d,\"model\":\"mock-7b\",\"system_fingerprint\":\"fp_44709d6fcb\",\"choices\":[{\"index\":0,\"delta\":%s,\"logprobs\":null,\"finish_reason\":%s}]}\n\n", id, created, delta, f)
			fl.Flush()
		}
		var text strings.Builder
		chunk(`{"role":"assistant","content":""}`, "")
		for i := 0; i < sc.Chunks; i++ {
			if sc.SilenceAfter > 0 && i == sc.SilenceAfter {
				select {
				case <-r.Context().Done():
					return
				case <-time.After(sc.Silence):
				}
			}
			var piece strings.Builder
			for k := 0; k < max(sc.TokPerChunk, 1); k++ {
				piece.WriteString(" " + corpusWords[rng.IntN(len(corpusWords))])
			}
			text.WriteString(piece.String())
			d, _ := json.Marshal(map[string]string{"content": piece.String()})
			chunk(string(d), "")
			if sc.Delay > 0 {
				time.Sleep(sc.Delay)
			}
		}
		chunk(`{}`, "stop")
		ct := protocol.EstimateTokens(text.String())
		fmt.Fprintf(w, "data: {\"id\":%q,\"object\":\"chat.completion.chunk\",\"created\":%d,\"model\":\"mock-7b\",\"choices\":[],\"usage\":{\"prompt_tokens\":%d,\"completion_tokens\":%d,\"total_tokens\":%d}}\n\n", id, created, prompt, ct, prompt+ct)
		fmt.Fprint(w, "data: [DONE]\n\n")
		fl.Flush()
	}))
	t.Cleanup(srv.Close)
	return srv
}

// benchUser is network.userWith with a bandwidth counter on the host.
func (n *network) benchUser(name string, share *client.ShareConfig, listen string) (*client.Agent, *metrics.BandwidthCounter) {
	n.t.Helper()
	fam := ma.P_IP4
	if strings.HasPrefix(listen, "/ip6/") {
		fam = ma.P_IP6
	}
	home := n.t.TempDir()
	u, err := n.portal.Store().CreateUser(name, false)
	if err != nil {
		n.t.Fatal(err)
	}
	if err := n.portal.AdjustCredit(u.ID, 5_000_000_000, "bench credit"); err != nil {
		n.t.Fatal(err)
	}
	token, _, err := n.portal.Store().IssueSessionToken(u.ID, "test", 0, false)
	if err != nil {
		n.t.Fatal(err)
	}
	cfg := client.DefaultConfig()
	cfg.Portal, cfg.User, cfg.Token, cfg.DeviceName = n.url, u.Username, token, name+"-laptop"
	cfg.HTTP = "127.0.0.1:0"
	cfg.Listen = []string{listen}
	cfg.NAT = false
	cfg.Buy.BudgetMicros = 100_000_000
	if share != nil {
		cfg.Share = *share
	}
	if err := client.SaveConfig(home, cfg); err != nil {
		n.t.Fatal(err)
	}
	bw := metrics.NewBandwidthCounter()
	a := client.NewAgent(home, cfg)
	a.HealthEvery, a.MeasureEvery = 2*time.Second, time.Hour
	a.HostOptions = []libp2p.Option{libp2p.ConnectionGater(onlyFamily{fam}), libp2p.BandwidthReporter(bw)}
	if err := a.Start(n.ctx); err != nil {
		n.t.Fatalf("starting %s: %v", name, err)
	}
	n.t.Cleanup(a.Close)
	return a, bw
}

func benchShare(upstream string) *client.ShareConfig {
	return &client.ShareConfig{
		Enabled: true, Upstream: upstream + "/v1", Concurrency: 4, Relays: 2,
		Models: []node.ModelConfig{{ID: model, UpstreamModel: "mock-7b", PriceInMicros: 1000, PriceOutMicros: 2000}},
	}
}

func (n *network) relayBytes() int64 {
	var sum int64
	for _, r := range n.relays {
		sum += r.Stats().BytesRelayed
	}
	return sum
}

// streamChat posts body to the buyer's local API and reads the stream.
func (n *network) streamChat(buyer *client.Agent, bw *metrics.BandwidthCounter, sc *scenario, body []byte) measurement {
	m := measurement{Scenario: sc.Name, Build: protocol.InferProtocol, ReqBytes: len(body)}
	pid := lproto.ID(protocol.InferProtocol)
	before, relay0 := bw.GetBandwidthForProtocol(pid), n.relayBytes()
	start := time.Now()
	req, _ := http.NewRequest(http.MethodPost, "http://"+buyer.Addr()+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+buyer.APIKey())
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		m.Err = err.Error()
		return m
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var b bytes.Buffer
		_, _ = b.ReadFrom(resp.Body)
		m.Err = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(b.String()))
		return m
	}
	sr := bufio.NewReader(resp.Body)
	for {
		line, err := sr.ReadString('\n')
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "data: [DONE]"):
			err = errDone
		case strings.HasPrefix(line, "data: "):
			if strings.Contains(line, `"error"`) && !strings.Contains(line, `"choices"`) {
				m.Err = line
			}
			if m.Chunks == 0 {
				m.TTFTms = float64(time.Since(start).Microseconds()) / 1000
			}
			m.Chunks++
		case strings.HasPrefix(line, ": sharetoken job="):
			if i := strings.Index(line, "node="); i >= 0 {
				m.Node = strings.Fields(line[i+5:])[0]
			}
			m.OK = true
		}
		if err != nil {
			break
		}
	}
	m.TotalMs = float64(time.Since(start).Microseconds()) / 1000
	if m.Err != "" {
		m.OK = false
	}
	// Let the receipt, ack and settlement finish, and the bandwidth meters
	// (updated once a second) catch up, before reading counters.
	if !sc.NoSettle {
		time.Sleep(2500 * time.Millisecond)
	}
	after := bw.GetBandwidthForProtocol(pid)
	m.UpBytes, m.DownBytes = after.TotalOut-before.TotalOut, after.TotalIn-before.TotalIn
	m.RelayBytes = n.relayBytes() - relay0
	if m.Chunks > 0 {
		m.DownPerChk = float64(m.DownBytes) / float64(m.Chunks)
	}
	return m
}

type doneErr struct{}

func (doneErr) Error() string { return "done" }

var errDone error = doneErr{}

func record(t *testing.T, m measurement) {
	t.Helper()
	line, _ := json.Marshal(m)
	t.Logf("%s", line)
	if out := os.Getenv("BENCH_OUT"); out != "" {
		f, err := os.OpenFile(out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err == nil {
			_, _ = f.Write(append(line, '\n'))
			_ = f.Close()
		}
	}
}

func reps() int {
	if n, err := strconv.Atoi(os.Getenv("BENCH_REPS")); err == nil && n > 0 {
		return n
	}
	return 3
}

// newBenchMarket starts the topology, sellers backed by upstream (each its own
// account) and one IPv4-only buyer.
func newBenchMarket(t *testing.T, sellers int, sc *scenario) (*network, *client.Agent, *metrics.BandwidthCounter, []*client.Agent) {
	n := newNetwork(t, 2)
	n.healthEvery = 2 * time.Second
	up := benchUpstream(t, sc)
	var nodes []*client.Agent
	for i := 0; i < sellers; i++ {
		s, _ := n.benchUser(fmt.Sprintf("seller%d", i), benchShare(up.URL), "/ip6/::1/tcp/0")
		nodes = append(nodes, s)
	}
	buyer, bw := n.benchUser("buyer", nil, "/ip4/127.0.0.1/tcp/0")
	waitFor(t, 30*time.Second, "offers", func() bool { return len(n.coord.Engine.Offers()) == sellers })
	return n, buyer, bw, nodes
}

// TestBenchTraffic: A1–A5, bytes and local latency per scenario.
func TestBenchTraffic(t *testing.T) {
	scenarios := []scenario{
		// Paced like real model servers: 50 and 100 tokens per second.
		{Name: "A1-short-50tps", PromptBytes: 1 << 10, Chunks: 200, TokPerChunk: 1, Delay: 20 * time.Millisecond},
		{Name: "A2-long-100tps", PromptBytes: 2 << 10, Chunks: 2000, TokPerChunk: 1, Delay: 10 * time.Millisecond},
		{Name: "A3-agent-300k", PromptBytes: 300 << 10, Chunks: 300, TokPerChunk: 1, Delay: 10 * time.Millisecond},
		{Name: "A4-request-6m", PromptBytes: 6 << 20, Chunks: 50, TokPerChunk: 1, Delay: 10 * time.Millisecond},
		// Anthropic-style chunks of several tokens, about 100 tokens per second.
		{Name: "A5-multi-token", PromptBytes: 2 << 10, Chunks: 500, TokPerChunk: 4, Delay: 40 * time.Millisecond},
		// Unpaced: the server emits as fast as it can (an upper bound for merging).
		{Name: "A2b-burst", PromptBytes: 2 << 10, Chunks: 2000, TokPerChunk: 1},
	}
	cur := &scenario{}
	sc := cur
	n, buyer, bw, _ := newBenchMarket(t, 1, sc)
	for _, s := range scenarios {
		*cur = s
		body := requestBody(s.PromptBytes, "")
		var ms []measurement
		for r := 0; r < reps(); r++ {
			m := n.streamChat(buyer, bw, cur, body)
			record(t, m)
			ms = append(ms, m)
		}
		summarise(t, s.Name, ms)
	}
}

// TestBenchSilence: A6, the upstream pauses 75s mid-stream (a reasoning model).
func TestBenchSilence(t *testing.T) {
	sc := &scenario{Name: "A6-silence-75s", PromptBytes: 1 << 10, Chunks: 20, TokPerChunk: 1, SilenceAfter: 5, Silence: 75 * time.Second}
	n, buyer, bw, _ := newBenchMarket(t, 1, sc)
	m := n.streamChat(buyer, bw, sc, requestBody(sc.PromptBytes, ""))
	record(t, m)
}

// TestBenchSticky: A7, sessions under load. Three sellers with two slots
// each serve six sessions that run concurrently, ten turns each; a turn has
// affinity when the node of the previous turn of its session serves it.
func TestBenchSticky(t *testing.T) {
	sc := &scenario{Name: "A7-sticky", PromptBytes: 4 << 10, Chunks: 40, TokPerChunk: 1, Delay: 10 * time.Millisecond}
	n, buyer, bw, _ := newBenchMarket(t, 3, sc)
	const sessions, turns = 6, 10
	var mu sync.Mutex
	var ms []measurement
	same, total := 0, 0
	var wg sync.WaitGroup
	for s := 0; s < sessions; s++ {
		wg.Add(1)
		go func(s int) {
			defer wg.Done()
			body := requestBody(sc.PromptBytes, fmt.Sprintf("bench-session-%d", s))
			prev := ""
			for i := 0; i < turns; i++ {
				m := n.streamChat(buyer, bw, &scenario{Name: sc.Name, NoSettle: true}, body)
				mu.Lock()
				record(t, m)
				ms = append(ms, m)
				if prev != "" && m.OK {
					total++
					if m.Node == prev {
						same++
					}
				}
				mu.Unlock()
				if m.OK {
					prev = m.Node
				}
			}
		}(s)
	}
	wg.Wait()
	t.Logf("SUMMARY %s affinity=%d/%d", sc.Name, same, total)
	summarise(t, sc.Name, ms)
}

// TestBenchStickyJoin: A7b, a cheaper seller comes online in the middle of
// four sessions (offers change all the time in a market). A session that
// moves to it loses its upstream prompt cache.
func TestBenchStickyJoin(t *testing.T) {
	sc := &scenario{Name: "A7b-sticky-join", PromptBytes: 4 << 10, Chunks: 20, TokPerChunk: 1}
	n, buyer, bw, _ := newBenchMarket(t, 1, sc)
	up := benchUpstream(t, sc)
	const sessions, turns, joinAfter = 4, 10, 3
	prev := make([]string, sessions)
	same, total := 0, 0
	var ms []measurement
	for i := 0; i < turns; i++ {
		if i == joinAfter {
			cheap := benchShare(up.URL)
			cheap.Models[0].PriceInMicros, cheap.Models[0].PriceOutMicros = 500, 1000
			n.benchUser("cheap-seller", cheap, "/ip6/::1/tcp/0")
			waitFor(t, 30*time.Second, "the cheaper offer", func() bool { return len(n.coord.Engine.Offers()) == 2 })
		}
		for s := 0; s < sessions; s++ {
			body := requestBody(sc.PromptBytes, fmt.Sprintf("bench-join-%d", s))
			m := n.streamChat(buyer, bw, &scenario{Name: sc.Name, NoSettle: true}, body)
			record(t, m)
			ms = append(ms, m)
			if prev[s] != "" && m.OK {
				total++
				if m.Node == prev[s] {
					same++
				}
			}
			if m.OK {
				prev[s] = m.Node
			}
		}
	}
	t.Logf("SUMMARY %s affinity=%d/%d", sc.Name, same, total)
	summarise(t, sc.Name, ms)
}

func summarise(t *testing.T, name string, ms []measurement) {
	med := func(f func(measurement) float64) float64 {
		var v []float64
		for _, m := range ms {
			if m.OK {
				v = append(v, f(m))
			}
		}
		if len(v) == 0 {
			return 0
		}
		slices.Sort(v)
		return v[len(v)/2]
	}
	ok := 0
	for _, m := range ms {
		if m.OK {
			ok++
		}
	}
	t.Logf("SUMMARY %s ok=%d/%d req=%dB up=%.0fB down=%.0fB down/chunk=%.1fB relay=%.0fB ttft=%.1fms total=%.1fms err=%s",
		name, ok, len(ms), cmp.Or(ms[0].ReqBytes, 0), med(func(m measurement) float64 { return float64(m.UpBytes) }),
		med(func(m measurement) float64 { return float64(m.DownBytes) }), med(func(m measurement) float64 { return m.DownPerChk }),
		med(func(m measurement) float64 { return float64(m.RelayBytes) }), med(func(m measurement) float64 { return m.TTFTms }),
		med(func(m measurement) float64 { return m.TotalMs }), ms[0].Err)
}
