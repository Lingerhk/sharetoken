package node

import (
	"encoding/json"
	"testing"
	"time"

	"sharetoken/internal/p2p"
	"sharetoken/internal/protocol"
)

func TestPrepareUpstreamBodyEnforcesNodePolicy(t *testing.T) {
	in := []byte(`{"model":"gpt-something","n":8,"best_of":4,"stream":false,"max_tokens":500,
		"temperature":0.2,"messages":[{"role":"user","content":"hi"}],"logprobs":true,"session_id":"local-session"}`)
	out, err := prepareUpstreamBody(in, "qwen2.5:7b", 300)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "qwen2.5:7b" {
		t.Errorf("model not overridden: %v", got["model"])
	}
	for _, k := range []string{"n", "best_of", "logprobs", "session_id"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s must be dropped", k)
		}
	}
	if got["stream"] != true {
		t.Error("stream must be forced on")
	}
	if so, _ := got["stream_options"].(map[string]any); so["include_usage"] != true {
		t.Error("usage reporting must be requested")
	}
	if got["max_tokens"].(float64) != 300 {
		t.Errorf("max_tokens = %v, want the budget cap 300", got["max_tokens"])
	}
	if got["temperature"].(float64) != 0.2 {
		t.Error("allowed sampling parameters must pass through")
	}
}

func TestPrepareUpstreamBodyLimits(t *testing.T) {
	out, _ := prepareUpstreamBody([]byte(`{"max_completion_tokens":50,"messages":[{"role":"user","content":"x"}]}`), "m", 1_000_000)
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	if got["max_tokens"].(float64) != 50 {
		t.Errorf("buyer's own lower limit must win, got %v", got["max_tokens"])
	}
	out, _ = prepareUpstreamBody([]byte(`{"messages":[{"role":"user","content":"x"}]}`), "m", 1<<40)
	_ = json.Unmarshal(out, &got)
	if got["max_tokens"].(float64) != HardMaxOutputTokens {
		t.Errorf("hard cap not applied, got %v", got["max_tokens"])
	}
	if _, err := prepareUpstreamBody([]byte(`{"messages":[{"role":"user","content":"x"}]}`), "m", 0); protocol.CodeOf(err) != protocol.CodeBudget {
		t.Errorf("zero budget: want budget error, got %v", err)
	}
	if _, err := prepareUpstreamBody([]byte(`{"prompt":"x"}`), "m", 10); protocol.CodeOf(err) != protocol.CodeBadRequest {
		t.Errorf("missing messages: want bad_request, got %v", err)
	}
}

func TestCheckTicket(t *testing.T) {
	keys := func() *p2p.Keys {
		k, err := p2p.NewKeys()
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	coord, self, buyer, mallory := keys(), keys(), keys(), keys()
	now := time.Now()
	id := identity{self: self.ID(), coordinator: coord.ID(), boxPub: self.Box.Public(), receiptKey: self.ReceiptKey()}
	models := map[string]ModelConfig{"m": {ID: "m", UpstreamModel: "u", PriceInMicros: 10, PriceOutMicros: 20}}

	base := func() protocol.Ticket {
		return protocol.Ticket{InferVersion: protocol.InferVersion,
			JobID: "j", Model: "m", Node: self.ID(), NodeBoxPub: self.Box.Public(), NodeReceiptKey: self.ReceiptKey(),
			Buyer: buyer.ID(), PriceInMicros: 10, PriceOutMicros: 20, ReservedMicros: 100,
			ExpiresAt: now.Add(time.Minute).UnixNano(), Coordinator: coord.ID(),
		}
	}
	signed := func(mut func(*protocol.Ticket), signer *p2p.Keys) *protocol.Ticket {
		tk := base()
		if mut != nil {
			mut(&tk)
		}
		tk.Sign(signer.Signer)
		return &tk
	}

	if _, err := checkTicket(signed(nil, coord), buyer.ID(), id, models, now); err != nil {
		t.Fatalf("valid ticket rejected: %v", err)
	}
	cases := map[string]struct {
		tk     *protocol.Ticket
		remote string
	}{
		"presented by someone else":   {signed(nil, coord), mallory.ID()},
		"signed by an untrusted peer": {signed(func(tk *protocol.Ticket) { tk.Coordinator = mallory.ID() }, mallory), buyer.ID()},
		"for another node":            {signed(func(tk *protocol.Ticket) { tk.Node = mallory.ID() }, coord), buyer.ID()},
		"expired":                     {signed(func(tk *protocol.Ticket) { tk.ExpiresAt = now.Add(-time.Second).UnixNano() }, coord), buyer.ID()},
		"price below current offer":   {signed(func(tk *protocol.Ticket) { tk.PriceOutMicros = 1 }, coord), buyer.ID()},
		"foreign box key":             {signed(func(tk *protocol.Ticket) { tk.NodeBoxPub = mallory.Box.Public() }, coord), buyer.ID()},
		"model not served":            {signed(func(tk *protocol.Ticket) { tk.Model = "other" }, coord), buyer.ID()},
	}
	for name, c := range cases {
		if _, err := checkTicket(c.tk, c.remote, id, models, now); protocol.CodeOf(err) != protocol.CodeBadTicket {
			t.Errorf("%s: want bad_ticket, got %v", name, err)
		}
	}
}
