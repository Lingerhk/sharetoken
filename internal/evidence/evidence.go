// Package evidence decides disputes on the facts.
//
// The protocol is built so that a buyer can prove what happened in one job by
// disclosing one key. The buyer seals its request from a fresh per-job X25519
// key and the node seals every response chunk to that same key; the node's
// receipt signs SHA-256 of the sealed request (whose first 32 bytes are the
// job key) and a hash over the sealed chunks. So a judge given the job's
// private key, the sealed request and the sealed chunks can check, without
// trusting the buyer:
//
//   - the request and the chunks are exactly what the node signed for (the
//     hashes match its receipt), so the buyer cannot fabricate a transcript;
//   - the disclosed key is the one the request committed to, so the buyer
//     cannot disclose a key that makes honest chunks look unreadable;
//   - whether each chunk opens under the job key: one that does not means the
//     node signed for output the buyer could not read;
//   - how many tokens the request and the response really contain, which a
//     reference tokenizer turns into an objective bound on billable usage.
//
// Nothing else is revealed: every job has its own key. The judge keeps
// plaintext in memory only while deciding and records just the outcome.
//
// What evidence cannot settle is a claim about bytes the node never signed
// ("the node sent me something else"): those come out Unverifiable and go to
// manual arbitration.
package evidence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"sharetoken/internal/box"
	"sharetoken/internal/protocol"
	"sharetoken/internal/receipt"
	"sharetoken/internal/tokens"
)

// Outcome of judging a dispute.
type Outcome string

const (
	// Upheld: the node is at fault. The buyer is refunded and the node loses
	// reputation.
	Upheld Outcome = "upheld"
	// Dismissed: the evidence supports the receipt. The node is paid.
	Dismissed Outcome = "dismissed"
	// Unverifiable: the evidence does not match what the node signed, so it
	// proves nothing either way. The job waits for manual arbitration.
	Unverifiable Outcome = "unverifiable"
)

// Verdict is a judge's decision with a human-readable reason.
type Verdict struct {
	Outcome Outcome
	Detail  string
}

// ErrNoReceipt means neither the node nor the buyer has a receipt for the job,
// so there is nothing to judge yet.
var ErrNoReceipt = errors.New("evidence: no receipt for this job")

const maxText = 4 << 20 // generated text kept for counting; far above any job's output

// Judge evaluates one job's evidence. Feed it the response chunks in order with
// Add, then call Finish.
type Judge struct {
	t        *protocol.Ticket
	r        *receipt.SignedReceipt
	key      *box.Identity
	request  []byte // decrypted request body
	early    *Verdict
	hasher   *receipt.StreamHasher
	next     uint64
	badChunk int64
	text     strings.Builder
}

// New starts judging ev for ticket t. settled is the receipt the node submitted
// for settlement, if any; presented is the receipt the buyer says it received.
// A presented receipt must be valid for the ticket; if it differs from the
// settled one, the node signed two transcripts for one job and is at fault.
func New(t *protocol.Ticket, settled, presented *receipt.SignedReceipt, ev *protocol.Evidence) (*Judge, error) {
	j := &Judge{t: t, hasher: receipt.NewStreamHasher(), badChunk: -1}
	if presented != nil {
		if err := protocol.CheckReceipt(t, presented); err != nil {
			if settled == nil {
				return nil, err
			}
			presented = nil // the settled receipt is authoritative anyway
		}
	}
	switch {
	case settled != nil:
		j.r = settled
		if presented != nil && !bytes.Equal(settled.Receipt.Digest(), presented.Receipt.Digest()) {
			j.decide(Upheld, "the node signed two different receipts for this job")
		}
	case presented != nil:
		j.r = presented
	default:
		return nil, ErrNoReceipt
	}

	switch key, err := box.IdentityFromSeed(ev.JobSecret); {
	case !bytes.Equal(receipt.Hash(ev.Request), j.r.Receipt.RequestHash):
		j.decide(Unverifiable, "the submitted request is not the one the node signed for")
	case err != nil:
		j.decide(Dismissed, "the disclosed job key is malformed")
	case len(ev.Request) < box.Overhead || !bytes.Equal(ev.Request[:box.PublicKeySize], key.Public()):
		j.decide(Dismissed, "the disclosed key is not the key this job's request was sealed from")
	default:
		j.key = key
		if j.request, err = key.OpenSent(t.NodeBoxPub, ev.Request, protocol.RequestAAD(t)); err != nil {
			j.decide(Dismissed, "the request does not open with the disclosed key")
		}
	}
	return j, nil
}

// decide records the first decisive finding; later ones do not override it.
func (j *Judge) decide(o Outcome, detail string) {
	if j.early == nil {
		j.early = &Verdict{Outcome: o, Detail: detail}
	}
}

// Receipt is the receipt being judged.
func (j *Judge) Receipt() *receipt.SignedReceipt { return j.r }

// Add feeds the next sealed response chunk, exactly as the buyer received it.
func (j *Judge) Add(seq uint64, sealed []byte) error {
	if seq != j.next {
		return fmt.Errorf("evidence: chunk %d out of order (want %d)", seq, j.next)
	}
	j.next++
	j.hasher.WriteChunk(sealed)
	if j.key == nil || j.badChunk >= 0 {
		return nil
	}
	plain, err := j.key.Open(sealed, protocol.ChunkAAD(j.t, seq))
	if err != nil {
		j.badChunk = int64(seq)
		return nil
	}
	if info, err := protocol.ParseChunk(plain); err == nil && j.text.Len() < maxText {
		j.text.WriteString(info.Text())
	}
	return nil
}

// Finish decides. counter counts tokens under the job's model; a reference
// counter gives tight bounds, the heuristic loose ones.
func (j *Judge) Finish(ctx context.Context, counter tokens.Counter) Verdict {
	if j.early != nil {
		return *j.early
	}
	r := &j.r.Receipt
	if !bytes.Equal(j.hasher.Sum(), r.ResponseHash) {
		return Verdict{Unverifiable, "the submitted chunks are not the ones the node signed for"}
	}
	if j.badChunk >= 0 {
		return Verdict{Upheld, fmt.Sprintf("chunk %d does not open with the job key: the node signed for output the buyer could not read", j.badChunk)}
	}
	if counter == nil {
		counter = tokens.Heuristic{}
	}
	exact := counter.Exact(j.t.Model)
	cp, err1 := counter.CountPrompt(ctx, j.t.Model, j.request)
	cc, err2 := counter.CountText(ctx, j.t.Model, j.text.String())
	if err1 != nil || err2 != nil {
		// The reference server is unavailable: judge on the estimate rather
		// than not at all. Its bounds are loose, so this cannot wrong an
		// honest node.
		exact = false
		cp, _ = tokens.Heuristic{}.CountPrompt(ctx, j.t.Model, j.request)
		cc, _ = tokens.Heuristic{}.CountText(ctx, j.t.Model, j.text.String())
	}
	how := "estimated"
	if exact {
		how = "counted with the model's tokenizer"
	}
	allowP, allowC := tokens.Allowance(exact, cp, cc)
	if r.PromptTokens > allowP || r.CompletionTokens > allowC {
		return Verdict{Upheld, fmt.Sprintf("billed %d prompt + %d completion tokens, but the transcript holds %d + %d (%s; at most %d + %d accepted)",
			r.PromptTokens, r.CompletionTokens, cp, cc, how, allowP, allowC)}
	}
	return Verdict{Dismissed, fmt.Sprintf("the transcript supports the receipt: %d + %d tokens %s, %d + %d billed",
		cp, cc, how, r.PromptTokens, r.CompletionTokens)}
}
