package protocol_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"sharetoken/p2p"
	"sharetoken/protocol"
)

func mustKeys(t *testing.T) *p2p.Keys {
	t.Helper()
	k, err := p2p.NewKeys()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func signedOffer(k *p2p.Keys, now time.Time) *protocol.Offer {
	o := &protocol.Offer{
		Node:           k.ID(),
		Addrs:          []string{"/ip4/127.0.0.1/tcp/4002"},
		BoxPub:         k.Box.Public(),
		ReceiptKey:     k.ReceiptKey(),
		Models:         []protocol.ModelPrice{{Model: "m", PriceInMicros: 10, PriceOutMicros: 20}},
		MaxConcurrency: 2,
		IssuedAt:       now.UnixNano(),
		ExpiresAt:      now.Add(time.Minute).UnixNano(),
	}
	o.Sign(k.Signer)
	return o
}

func TestOfferSignVerifySurvivesJSON(t *testing.T) {
	now := time.Now()
	o := signedOffer(mustKeys(t), now)
	raw, err := json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	var back protocol.Offer
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if err := back.Verify(now, 5*time.Minute); err != nil {
		t.Fatalf("offer does not verify after JSON round trip: %v", err)
	}
}

func TestOfferRejectsTamperingAndImpersonation(t *testing.T) {
	now := time.Now()
	k, other := mustKeys(t), mustKeys(t)
	cases := map[string]func(o *protocol.Offer){
		"price changed after signing": func(o *protocol.Offer) { o.Models[0].PriceInMicros = 1 },
		"claims another node's ID":    func(o *protocol.Offer) { o.Node = other.ID() },
		"box key swapped (MITM)":      func(o *protocol.Offer) { o.BoxPub = other.Box.Public() },
		"receipt key swapped":         func(o *protocol.Offer) { o.ReceiptKey = other.ReceiptKey() },
		"address redirected":          func(o *protocol.Offer) { o.Addrs = []string{"/ip4/6.6.6.6/tcp/1"} },
	}
	for name, mutate := range cases {
		o := signedOffer(k, now)
		mutate(o)
		if o.Verify(now, 5*time.Minute) == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestOfferValidityWindow(t *testing.T) {
	now := time.Now()
	o := signedOffer(mustKeys(t), now)
	if o.Verify(now.Add(2*time.Minute), 5*time.Minute) == nil {
		t.Error("expired offer accepted")
	}
	if o.Verify(now, 30*time.Second) == nil {
		t.Error("offer longer than the max TTL accepted")
	}
}

func TestTicketOnlyFromTrustedCoordinator(t *testing.T) {
	coord, other := mustKeys(t), mustKeys(t)
	tk := protocol.Ticket{JobID: "j", Model: "m", Node: "n", Buyer: "b", ReservedMicros: 100, Coordinator: coord.ID()}
	tk.Sign(coord.Signer)
	if err := tk.Verify(coord.ID()); err != nil {
		t.Fatal(err)
	}
	if tk.Verify(other.ID()) == nil {
		t.Error("accepted a ticket while trusting a different coordinator")
	}
	forged := tk
	forged.Coordinator = other.ID()
	forged.Sign(other.Signer)
	if forged.Verify(coord.ID()) == nil {
		t.Error("accepted a ticket signed by an untrusted coordinator")
	}
	tampered := tk
	tampered.ReservedMicros = 1_000_000
	if tampered.Verify(coord.ID()) == nil {
		t.Error("accepted a ticket whose reservation was raised after signing")
	}
}

func TestAADBindsJobParties(t *testing.T) {
	a := &protocol.Ticket{JobID: "j", Model: "m", Buyer: "b1", Node: "n"}
	b := &protocol.Ticket{JobID: "j", Model: "m", Buyer: "b2", Node: "n"}
	if bytes.Equal(protocol.RequestAAD(a), protocol.RequestAAD(b)) {
		t.Error("request AAD must differ per buyer")
	}
	if bytes.Equal(protocol.ChunkAAD(a, 0), protocol.ChunkAAD(a, 1)) {
		t.Error("chunk AAD must differ per sequence number")
	}
}

func TestFrameRoundTripAndErrors(t *testing.T) {
	var buf bytes.Buffer
	if err := protocol.Send(&buf, protocol.TypeChunk, protocol.Chunk{Seq: 7, Sealed: []byte{1, 2, 3}}); err != nil {
		t.Fatal(err)
	}
	env, err := protocol.Recv(&buf)
	if err != nil {
		t.Fatal(err)
	}
	var c protocol.Chunk
	if err := env.Decode(protocol.TypeChunk, &c); err != nil || c.Seq != 7 {
		t.Fatalf("decode: %v %+v", err, c)
	}

	buf.Reset()
	_ = protocol.SendError(&buf, protocol.Errorf(protocol.CodeBusy, "full"))
	env, _ = protocol.Recv(&buf)
	if err := env.Decode(protocol.TypeChunk, &c); protocol.CodeOf(err) != protocol.CodeBusy {
		t.Fatalf("want busy error, got %v", err)
	}

	buf.Reset()
	_ = protocol.SendError(&buf, errors.New("open /home/op/secret.key: denied"))
	env, _ = protocol.Recv(&buf)
	if err := env.Decode(protocol.TypeChunk, &c); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("internal error details leaked to the peer: %v", err)
	}
}

func TestRecvRejectsOversizedFrame(t *testing.T) {
	if _, err := protocol.Recv(bytes.NewReader([]byte{0xff, 0xff, 0xff, 0xff})); err == nil {
		t.Fatal("a 4 GiB length prefix must be refused before allocating")
	}
}

func TestCostMicros(t *testing.T) {
	if c, _ := protocol.CostMicros(1, 0, 1, 0); c != 1 {
		t.Errorf("fractional micro must round up, got %d", c)
	}
	if c, _ := protocol.CostMicros(10, 5, 1_000_000, 2_000_000); c != 20 {
		t.Errorf("10 in + 5 out at 1/2 credits per M = 20 micros, got %d", c)
	}
	if _, err := protocol.CostMicros(math.MaxUint64, 0, 2, 0); !errors.Is(err, protocol.ErrOverflow) {
		t.Errorf("want overflow, got %v", err)
	}
}

func TestMaxOutputTokensIsTheExactBudgetCap(t *testing.T) {
	for _, reserved := range []uint64{1, 999, 1_000_000} {
		for _, prompt := range []uint64{0, 50, 10_000} {
			for _, pin := range []uint64{0, 1, 3_000_000} {
				for _, pout := range []uint64{1, 7, 15_000_000} {
					m := protocol.MaxOutputTokens(reserved, prompt, pin, pout)
					if m > 0 {
						if c, err := protocol.CostMicros(prompt, m, pin, pout); err != nil || c > reserved {
							t.Fatalf("cap %d costs %d > reserved %d (p=%d in=%d out=%d)", m, c, reserved, prompt, pin, pout)
						}
					}
					if c, err := protocol.CostMicros(prompt, m+1, pin, pout); err == nil && c <= reserved {
						t.Fatalf("cap %d is not maximal (p=%d in=%d out=%d r=%d)", m, prompt, pin, pout, reserved)
					}
				}
			}
		}
	}
}

func TestEstimates(t *testing.T) {
	if got := protocol.EstimateTokens("hello world!"); got != 3 {
		t.Errorf("latin estimate = %d, want 3", got)
	}
	if got := protocol.EstimateTokens("你好世界"); got != 3 {
		t.Errorf("CJK estimate = %d, want 3", got)
	}
	body := []byte(`{"messages":[{"role":"system","content":"be brief"},{"role":"user","content":[{"type":"text","text":"hello world!"}]}]}`)
	got, err := protocol.EstimatePromptTokens(body)
	if err != nil {
		t.Fatal(err)
	}
	if want := uint64(4+2) + uint64(4+3); got != want {
		t.Errorf("prompt estimate = %d, want %d", got, want)
	}
}

func TestUsagePlausible(t *testing.T) {
	if err := protocol.UsagePlausible(120, 60, 100, 50); err != nil {
		t.Errorf("honest usage rejected: %v", err)
	}
	if protocol.UsagePlausible(100, 5_000, 100, 50) == nil {
		t.Error("100x inflated completion accepted")
	}
}

func TestParseChunk(t *testing.T) {
	info, err := protocol.ParseChunk([]byte(`{"choices":[{"delta":{"content":"Hi","reasoning_content":"think","tool_calls":[{"function":{"arguments":"{}"}}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4}}`))
	if err != nil {
		t.Fatal(err)
	}
	if info.Content != "Hi" || info.Reasoning != "think" || info.ToolArgs != "{}" || info.FinishReason != "stop" {
		t.Errorf("unexpected chunk info %+v", info)
	}
	if info.Usage == nil || info.Usage.CompletionTokens != 4 {
		t.Errorf("usage not parsed: %+v", info.Usage)
	}
	if info.Text() != "Hithink{}" {
		t.Errorf("Text() = %q", info.Text())
	}
}

func TestToolCallsAreAssembledFromStreamedFragments(t *testing.T) {
	chunks := []string{
		`{"choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"get_weather","arguments":""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"get_time","arguments":"{}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"Paris\"}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	}
	var acc protocol.ToolCallAccumulator
	var args string
	for _, c := range chunks {
		info, err := protocol.ParseChunk([]byte(c))
		if err != nil {
			t.Fatal(err)
		}
		acc.Add(info.ToolCalls)
		args += info.ToolArgs
	}
	got := acc.Calls()
	if len(got) != 2 || got[0].ID != "call_a" || got[0].Function.Name != "get_weather" || got[0].Function.Arguments != `{"city":"Paris"}` ||
		got[1].ID != "call_b" || got[1].Function.Name != "get_time" || got[1].Function.Arguments != "{}" {
		t.Fatalf("assembled %+v", got)
	}
	if args != `{"city":{}"Paris"}` { // arrival order; only its length is billed
		t.Fatalf("billed tool text %q", args)
	}
	var none protocol.ToolCallAccumulator
	if none.Calls() != nil {
		t.Fatal("no tool calls must be nil (omitted from the response)")
	}
}

func TestHoldForCoversMaxTokens(t *testing.T) {
	for _, c := range []struct{ prompt, maxTok, pin, pout uint64 }{
		{10, 32, 10_000, 10_000}, {1645, 16, 20_000, 40_000}, {0, 1, 1, 1}, {5000, 4096, 100_000, 200_000}, {7, 3, 3_000_000, 7_000_000},
	} {
		h := protocol.HoldFor(c.prompt, c.maxTok, c.pin, c.pout)
		if got := protocol.MaxOutputTokens(h, protocol.PaddedPrompt(c.prompt), c.pin, c.pout); got < c.maxTok {
			t.Errorf("%+v: hold %d allows %d output tokens", c, h, got)
		}
		// A prompt up to twice the estimate is still covered in full.
		full := protocol.TicketCost(&protocol.Ticket{PriceInMicros: c.pin, PriceOutMicros: c.pout, ReservedMicros: 1 << 62}, 2*c.prompt, c.maxTok)
		if full > h {
			t.Errorf("%+v: hold %d < cost %d", c, h, full)
		}
	}
	if protocol.HoldFor(1<<62, 1<<62, 1<<20, 1<<20) != math.MaxUint64 {
		t.Error("overflow must saturate")
	}
}
