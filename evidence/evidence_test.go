package evidence

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"
	"testing"

	"sharetoken/box"
	"sharetoken/protocol"
	"sharetoken/receipt"
	"sharetoken/tokens"
)

// job is one completed job as both sides hold it after the stream ends.
type job struct {
	t       protocol.Ticket
	nodeKey ed25519.PrivateKey
	jobKey  *box.Identity
	request []byte   // sealed request
	chunks  [][]byte // sealed chunks
	receipt *receipt.SignedReceipt
}

const body = `{"messages":[{"role":"user","content":"Please greet the whole wide world politely"}]}`

func newJob(t *testing.T, completionTokens uint64, mutate func(payloads []string, jobPub []byte, j *job) [][]byte) *job {
	t.Helper()
	nodePub, nodePriv, _ := ed25519.GenerateKey(nil)
	nodeBox, _ := box.GenerateIdentity()
	jobKey, _ := box.GenerateIdentity()
	j := &job{
		t: protocol.Ticket{
			JobID: "job-1", Model: "m", NodeBoxPub: nodeBox.Public(), NodeReceiptKey: nodePub,
			Buyer: "buyer", Node: "node", PriceInMicros: 1, PriceOutMicros: 1, ReservedMicros: 1_000_000,
		},
		nodeKey: nodePriv,
		jobKey:  jobKey,
	}
	var err error
	if j.request, err = box.SealFrom(jobKey, nodeBox.Public(), []byte(body), protocol.RequestAAD(&j.t)); err != nil {
		t.Fatal(err)
	}
	payloads := []string{
		`{"choices":[{"delta":{"content":"Hello, "}}]}`,
		`{"choices":[{"delta":{"content":"wide world!"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":5}}`,
	}
	if mutate != nil {
		j.chunks = mutate(payloads, jobKey.Public(), j)
	} else {
		for i, p := range payloads {
			c, err := box.Seal(jobKey.Public(), []byte(p), protocol.ChunkAAD(&j.t, uint64(i)))
			if err != nil {
				t.Fatal(err)
			}
			j.chunks = append(j.chunks, c)
		}
	}
	j.receipt = j.sign(t, 20, completionTokens)
	return j
}

func (j *job) sign(t *testing.T, pt, ct uint64) *receipt.SignedReceipt {
	h := receipt.NewStreamHasher()
	for _, c := range j.chunks {
		h.WriteChunk(c)
	}
	sr, err := receipt.Sign(j.nodeKey, receipt.Receipt{
		JobID: j.t.JobID, ModelID: j.t.Model, NodeID: j.nodeKey.Public().(ed25519.PublicKey),
		RequestHash: receipt.Hash(j.request), ResponseHash: h.Sum(),
		PromptTokens: pt, CompletionTokens: ct, UnixNano: 1, Nonce: []byte(fmt.Sprint(pt, ct)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return sr
}

func (j *job) evidence() *protocol.Evidence {
	return &protocol.Evidence{JobID: j.t.JobID, JobSecret: j.jobKey.Secret(), Request: j.request, Receipt: j.receipt, Chunks: uint64(len(j.chunks))}
}

func judge(t *testing.T, j *job, settled *receipt.SignedReceipt, ev *protocol.Evidence, chunks [][]byte, c tokens.Counter) Verdict {
	t.Helper()
	jd, err := New(&j.t, settled, ev.Receipt, ev)
	if err != nil {
		t.Fatal(err)
	}
	for i, ch := range chunks {
		if err := jd.Add(uint64(i), ch); err != nil {
			t.Fatal(err)
		}
	}
	return jd.Finish(context.Background(), c)
}

func TestHonestReceiptIsDismissed(t *testing.T) {
	j := newJob(t, 5, nil)
	v := judge(t, j, j.receipt, j.evidence(), j.chunks, tokens.Heuristic{})
	if v.Outcome != Dismissed {
		t.Fatalf("want dismissed, got %+v", v)
	}
}

func TestInflatedUsageIsUpheld(t *testing.T) {
	j := newJob(t, 5_000, nil) // five words of output billed as 5,000 tokens
	v := judge(t, j, j.receipt, j.evidence(), j.chunks, tokens.Heuristic{})
	if v.Outcome != Upheld || !strings.Contains(v.Detail, "billed 20 prompt + 5000 completion") {
		t.Fatalf("want upheld, got %+v", v)
	}
}

// exactCounter stands in for a reference server: it counts words.
type exactCounter struct{}

func (exactCounter) CountPrompt(_ context.Context, _ string, b []byte) (uint64, error) {
	return uint64(len(strings.Fields(string(b)))), nil
}
func (exactCounter) CountText(_ context.Context, _, s string) (uint64, error) {
	return uint64(len(strings.Fields(s))), nil
}
func (exactCounter) Exact(string) bool { return true }

func TestExactCountsCatchModestInflationTheHeuristicAllows(t *testing.T) {
	j := newJob(t, 60, nil) // 3 words billed as 60 tokens: within 2x+64, far above an exact 3+16
	if v := judge(t, j, j.receipt, j.evidence(), j.chunks, tokens.Heuristic{}); v.Outcome != Dismissed {
		t.Fatalf("heuristic: want dismissed (loose bound), got %+v", v)
	}
	if v := judge(t, j, j.receipt, j.evidence(), j.chunks, exactCounter{}); v.Outcome != Upheld {
		t.Fatalf("exact: want upheld, got %+v", v)
	}
}

func TestUnreadableChunkIsUpheld(t *testing.T) {
	j := newJob(t, 5, func(payloads []string, jobPub []byte, j *job) [][]byte {
		var out [][]byte
		stranger, _ := box.GenerateIdentity()
		for i, p := range payloads {
			to := jobPub
			if i == 1 {
				to = stranger.Public() // sealed to a key the buyer does not hold
			}
			c, _ := box.Seal(to, []byte(p), protocol.ChunkAAD(&j.t, uint64(i)))
			out = append(out, c)
		}
		return out
	})
	v := judge(t, j, j.receipt, j.evidence(), j.chunks, tokens.Heuristic{})
	if v.Outcome != Upheld || !strings.Contains(v.Detail, "chunk 1") {
		t.Fatalf("want upheld for chunk 1, got %+v", v)
	}
}

func TestBuyerCannotFabricateOrSwapKeys(t *testing.T) {
	j := newJob(t, 5, nil)

	// Fabricated chunks do not hash to the receipt: unverifiable, not upheld.
	forged := append([][]byte{}, j.chunks...)
	forged[0], _ = box.Seal(j.jobKey.Public(), []byte(`{"choices":[{"delta":{"content":"garbage"}}]}`), protocol.ChunkAAD(&j.t, 0))
	if v := judge(t, j, j.receipt, j.evidence(), forged, tokens.Heuristic{}); v.Outcome != Unverifiable {
		t.Fatalf("fabricated chunks: want unverifiable, got %+v", v)
	}

	// Disclosing a different key, to make honest chunks look unreadable, fails:
	// the request commits to the real key.
	other, _ := box.GenerateIdentity()
	ev := j.evidence()
	ev.JobSecret = other.Secret()
	if v := judge(t, j, j.receipt, ev, j.chunks, tokens.Heuristic{}); v.Outcome != Dismissed {
		t.Fatalf("swapped key: want dismissed, got %+v", v)
	}

	// A different request than the one signed for proves nothing.
	ev = j.evidence()
	ev.Request, _ = box.SealFrom(j.jobKey, j.t.NodeBoxPub, []byte(`{"messages":[]}`), protocol.RequestAAD(&j.t))
	if v := judge(t, j, j.receipt, ev, j.chunks, tokens.Heuristic{}); v.Outcome != Unverifiable {
		t.Fatalf("other request: want unverifiable, got %+v", v)
	}
}

func TestTwoReceiptsForOneJobIsUpheld(t *testing.T) {
	j := newJob(t, 5, nil)
	settled := j.sign(t, 20, 50) // what the node submitted for payment
	v := judge(t, j, settled, j.evidence(), j.chunks, tokens.Heuristic{})
	if v.Outcome != Upheld || !strings.Contains(v.Detail, "two different receipts") {
		t.Fatalf("want upheld for equivocation, got %+v", v)
	}
}

func TestReceiptRequirementsAndOrdering(t *testing.T) {
	j := newJob(t, 5, nil)
	ev := j.evidence()
	ev.Receipt = nil
	if _, err := New(&j.t, nil, nil, ev); !errors.Is(err, ErrNoReceipt) {
		t.Fatalf("want ErrNoReceipt, got %v", err)
	}
	// A presented receipt signed by someone else is refused outright.
	_, mallory, _ := ed25519.GenerateKey(nil)
	forged := *j.receipt
	forged.Sig = ed25519.Sign(mallory, []byte("x"))
	if _, err := New(&j.t, nil, &forged, j.evidence()); err == nil {
		t.Fatal("accepted a receipt the node did not sign")
	}
	jd, _ := New(&j.t, j.receipt, nil, j.evidence())
	if err := jd.Add(1, j.chunks[1]); err == nil {
		t.Fatal("accepted chunks out of order")
	}
}
