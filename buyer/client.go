// Package buyer is the client side of the marketplace. Per request it:
//
//  1. asks the coordinator for a ticket, and checks both the coordinator's
//     signature and that the node keys, addresses and prices in it match the
//     node's own signed offer (so a coordinator cannot substitute its own key);
//     with an attestation policy it also verifies the node's TEE quote;
//  2. dials the ticketed node directly (libp2p authenticates its peer ID);
//  3. seals the request from a fresh per-job key to the node's box key and
//     receives the response sealed to that same key, checking every chunk's
//     authentication and order;
//  4. verifies the signed receipt against its own hashes and a usage estimate,
//     then signs an ack, or rejects it and disputes: with evidence (the job
//     key and the ciphertexts, revealing this one job to the coordinator) when
//     that can prove the node wrong, otherwise as a plain claim.
//
// While nothing has reached the caller, a failing node is replaced by another
// one; once output has been delivered it cannot be replayed.
package buyer

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	lproto "github.com/libp2p/go-libp2p/core/protocol"
	ma "github.com/multiformats/go-multiaddr"

	"sharetoken/attest"
	"sharetoken/box"
	"sharetoken/p2p"
	"sharetoken/protocol"
	"sharetoken/receipt"
)

// ErrReceiptRejected means output arrived but failed verification.
var ErrReceiptRejected = errors.New("buyer: receipt failed verification")

// errNotAttested marks a ticket for a node whose TEE quote did not verify.
var errNotAttested = errors.New("buyer: node is not attested")

const jobTimeout = 10 * time.Minute

// Client buys inference through a coordinator.
type Client struct {
	h           host.Host
	keys        *p2p.Keys
	coordinator peer.AddrInfo
	// Retries is how many other nodes to try when one fails before any output
	// reached the caller (default 2).
	Retries int
	// Evidence discloses a job's key and ciphertexts to the coordinator when
	// that proves the node at fault (default true). It reveals that one job's
	// prompt and response to the coordinator; with it off, such disputes are
	// filed as plain claims and wait for manual arbitration.
	Evidence bool
	// QueueWait is how long a request waits for a free node when every node
	// offering the model is at capacity, retrying with backoff (default 30s;
	// 0 fails at once with CodeCapacity).
	QueueWait time.Duration
	// FirstByteTimeout and IdleTimeout bound how long a job may wait for the
	// node's first frame and between frames (defaults 120s and 60s). A node
	// that stalls (a lossy path, a hung model server) is abandoned: a request
	// that has delivered nothing to the caller yet fails over to another node.
	FirstByteTimeout, IdleTimeout time.Duration
	// Attestation verifies nodes' TEE quotes; required for RequireTEE.
	Attestation *Attestation
	// OnCoordinatorUnreachable, if set, is called when a call to the
	// coordinator fails in transport (no answer, as opposed to a refusal):
	// the client then checks its relay path at once instead of at the next
	// health check. It must not block.
	OnCoordinatorUnreachable func()
	log                      *slog.Logger
	bg                       sync.WaitGroup // disputes being delivered in the background
}

// Attestation is the buyer's TEE trust configuration.
type Attestation struct {
	Verifier attest.Verifier
	Policy   *attest.Policy
}

// New returns a client using h and keys, trusting coordinator.
func New(h host.Host, keys *p2p.Keys, coordinator peer.AddrInfo) *Client {
	h.Peerstore().AddAddrs(coordinator.ID, coordinator.Addrs, peerstore.PermanentAddrTTL)
	return &Client{h: h, keys: keys, coordinator: coordinator, Retries: 2, Evidence: true, QueueWait: DefaultQueueWait,
		FirstByteTimeout: 120 * time.Second, IdleTimeout: 60 * time.Second, log: slog.Default().With("component", "buyer")}
}

// ChatRequest is one chat-completions call.
type ChatRequest struct {
	Model        string // marketplace model ID
	Body         []byte // OpenAI chat-completions JSON; the node overrides model/stream/max_tokens
	BudgetMicros uint64 // reserved up front; settlement never exceeds it
	// MaxTokens, when the request caps its output, lets the coordinator hold
	// only the worst-case cost of this request instead of the whole budget.
	MaxTokens   uint64
	MaxPriceIn  uint64 // optional per-million-token price ceilings
	MaxPriceOut uint64
	RequireTEE  bool   // only nodes whose quote passes the client's attestation policy
	Node        string // optional: only this node
}

// Result is a completed, verified job.
type Result struct {
	Ticket       protocol.Ticket
	Receipt      receipt.SignedReceipt
	Content      string
	Reasoning    string // a thinking model's reasoning, billed as output
	Text         string // everything billed as output: content, reasoning, tool arguments
	ToolCalls    []protocol.ToolCall
	FinishReason string
	CostMicros   uint64
	Verified     bool   // receipt passed every check and was acknowledged
	Attested     string // allow-list label of the node's verified TEE image, if any
}

// Chat runs one request. onChunk, if set, receives each decrypted chunk
// payload as it streams (verification happens when the receipt arrives).
//
// When output arrived but failed verification on the last node tried, Chat
// returns that unverified Result together with an ErrReceiptRejected error;
// the job has already been disputed.
func (c *Client) Chat(ctx context.Context, req ChatRequest, onChunk func(payload []byte) error) (*Result, error) {
	if req.Model == "" || len(req.Body) == 0 || req.BudgetMicros == 0 {
		return nil, errors.New("buyer: model, body and budget are required")
	}
	if req.RequireTEE && c.Attestation == nil {
		return nil, errors.New("buyer: RequireTEE needs an attestation policy")
	}
	var (
		exclude  []string
		lastErr  error
		lastRes  *Result
		rejected int // nodes that refused the request as invalid
	)
	for attempt := 0; attempt <= c.Retries; attempt++ {
		tr, err := c.requestTicket(ctx, req, exclude)
		if err != nil {
			if lastErr != nil && protocol.CodeOf(err) == protocol.CodeNoOffers {
				if protocol.CodeOf(lastErr) == protocol.CodeBadRequest {
					return nil, lastErr
				}
				return lastRes, fmt.Errorf("no healthy node left: %w", lastErr)
			}
			return nil, err
		}
		label, err := c.verifyTicket(tr, req)
		if errors.Is(err, errNotAttested) {
			// The coordinator vouched for the quote but we do not accept it;
			// its hold is released at the ticket's deadline.
			lastErr, exclude = err, append(exclude, tr.Ticket.Node)
			c.log.Warn("node failed attestation; trying another", "node", tr.Ticket.Node, "err", err)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("buyer: coordinator returned an invalid ticket: %w", err)
		}
		res, delivered, err := c.runJob(ctx, &tr.Ticket, req.Body, onChunk)
		if res != nil {
			res.Attested = label
		}
		if err == nil || delivered || ctx.Err() != nil {
			return res, err
		}
		if protocol.CodeOf(err) == protocol.CodeBadRequest {
			// The node's model server refused the request. Ask one more node in
			// case this one lies or is misconfigured; two refusals mean the
			// request itself is bad, so stop instead of trying every node.
			if rejected++; rejected >= 2 {
				return nil, err
			}
			lastErr, exclude = err, append(exclude, tr.Ticket.Node)
			c.log.Warn("node rejected the request as invalid; asking one more node", "node", tr.Ticket.Node, "err", err)
			continue
		}
		// Nothing reached the caller, so trying another node is invisible to
		// it. A job that failed without a receipt is pre-disputed, so the node
		// cannot later settle it without arbitration; rejected receipts were
		// already disputed by runJob.
		if !errors.Is(err, ErrReceiptRejected) {
			jobID := tr.Ticket.JobID
			c.background(func() { c.fileDispute(jobID, "job failed before delivering output: "+err.Error()) })
		}
		lastErr, lastRes = err, res
		exclude = append(exclude, tr.Ticket.Node)
		c.log.Warn("node failed before any output reached the caller; failing over", "node", tr.Ticket.Node, "err", err)
	}
	return lastRes, lastErr
}

// background runs fn as a tracked background task (dispute delivery).
func (c *Client) background(fn func()) {
	c.bg.Add(1)
	go func() {
		defer c.bg.Done()
		fn()
	}()
}

// WaitDisputes blocks until disputes filed in the background have been
// delivered and answered, or ctx ends. Call it before closing the host.
func (c *Client) WaitDisputes(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		c.bg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// DefaultQueueWait is the default Client.QueueWait.
const DefaultQueueWait = 30 * time.Second

// requestTicket asks the coordinator for a ticket. While every node is at
// capacity it waits and retries, up to QueueWait: a short queue on the buyer's
// side instead of failing requests whenever the network is momentarily full.
func (c *Client) requestTicket(ctx context.Context, req ChatRequest, exclude []string) (*protocol.TicketResponse, error) {
	deadline := time.Now().Add(c.QueueWait)
	// A request that never reached the coordinator (its relay path just
	// broke) is retried while the client fails over to another relay; the
	// request was not sent, so this cannot issue two tickets.
	noPathDeadline := time.Now().Add(noPathRetry)
	backoff := 200 * time.Millisecond
	for {
		tr, err := c.requestTicketOnce(ctx, req, exclude)
		retry := protocol.CodeOf(err) == protocol.CodeCapacity && !time.Now().Add(backoff).After(deadline) ||
			errors.Is(err, p2p.ErrNoStream) && !time.Now().Add(backoff).After(noPathDeadline)
		if !retry {
			return tr, err
		}
		// Jitter spreads out buyers that were refused at the same moment.
		wait := backoff/2 + time.Duration(rand.Int64N(int64(backoff)))
		select {
		case <-ctx.Done():
			return tr, err
		case <-time.After(wait):
		}
		backoff = min(backoff*3/2, 2*time.Second)
	}
}

// noPathRetry bounds how long a ticket request that could not reach the
// coordinator is retried: long enough for a relay failover (seconds), short
// enough that a coordinator that is down fails requests quickly.
const noPathRetry = 10 * time.Second

// call is p2p.Call to the coordinator, reporting transport failures.
func (c *Client) call(ctx context.Context, reqType string, req any, respType string, resp any) error {
	err := p2p.Call(ctx, c.h, c.coordinator.ID, protocol.CtrlProtocol, reqType, req, respType, resp)
	c.noteCoordinatorErr(ctx, err)
	return err
}

// noteCoordinatorErr calls OnCoordinatorUnreachable for an error that is not
// the coordinator's answer (and not the caller giving up).
func (c *Client) noteCoordinatorErr(ctx context.Context, err error) {
	if err != nil && protocol.CodeOf(err) == "" && ctx.Err() == nil && c.OnCoordinatorUnreachable != nil {
		c.OnCoordinatorUnreachable()
	}
}

func (c *Client) requestTicketOnce(ctx context.Context, req ChatRequest, exclude []string) (*protocol.TicketResponse, error) {
	var tr protocol.TicketResponse
	err := c.call(ctx, protocol.TypeTicketRequest,
		protocol.TicketRequest{
			InferVersions: protocol.SupportedInferVersions,
			Model:         req.Model,
			BudgetMicros:  req.BudgetMicros,
			MaxTokens:     req.MaxTokens,
			PromptTokens:  promptEst(req),
			MaxPriceIn:    req.MaxPriceIn,
			MaxPriceOut:   req.MaxPriceOut,
			Exclude:       exclude,
			Node:          req.Node,
			RequireTEE:    req.RequireTEE,
		}, protocol.TypeTicket, &tr)
	return &tr, err
}

// verifyTicket checks the coordinator's ticket against the request and against
// the node's own signed offer, and verifies the node's TEE quote when the
// client has an attestation policy. It returns the attested image label.
func (c *Client) verifyTicket(tr *protocol.TicketResponse, req ChatRequest) (string, error) {
	t, o := &tr.Ticket, &tr.Offer
	if !slices.Contains(protocol.SupportedInferVersions, t.InferVersion) {
		return "", errors.New("buyer: the ticket names a protocol version this client does not speak")
	}
	if err := t.Verify(c.coordinator.ID.String()); err != nil {
		return "", err
	}
	switch {
	case t.Buyer != c.keys.ID():
		return "", errors.New("ticket issued to another buyer")
	case t.Model != req.Model:
		return "", errors.New("ticket is for a different model")
	case req.Node != "" && t.Node != req.Node:
		return "", errors.New("ticket is for a different node than requested")
	case t.ReservedMicros > req.BudgetMicros:
		return "", errors.New("ticket reserves more than the budget")
	case req.MaxPriceIn > 0 && t.PriceInMicros > req.MaxPriceIn,
		req.MaxPriceOut > 0 && t.PriceOutMicros > req.MaxPriceOut:
		return "", errors.New("ticket price exceeds the limit")
	}
	if err := o.VerifySignature(); err != nil {
		return "", fmt.Errorf("node offer: %w", err)
	}
	p, ok := o.Price(t.Model)
	switch {
	case o.Node != t.Node:
		return "", errors.New("offer is from a different node")
	case !bytes.Equal(o.BoxPub, t.NodeBoxPub), !bytes.Equal(o.ReceiptKey, t.NodeReceiptKey):
		return "", errors.New("ticket keys differ from the node's signed offer")
	case !slices.Equal(o.Addrs, t.NodeAddrs):
		return "", errors.New("ticket addresses differ from the node's signed offer")
	case !ok || p.PriceInMicros != t.PriceInMicros || p.PriceOutMicros != t.PriceOutMicros:
		return "", errors.New("ticket price differs from the node's signed offer")
	}
	var label string
	if c.Attestation != nil && o.Quote != nil {
		// The quote binds both keys the ticket carries, so a verified quote
		// means the prompt is sealed to a key inside the attested image.
		_, l, err := attest.Verify(c.Attestation.Verifier, c.Attestation.Policy, o.Quote, o.ReceiptKey, o.BoxPub)
		if err != nil && req.RequireTEE {
			return "", fmt.Errorf("%w: %v", errNotAttested, err)
		}
		label = l
	}
	if req.RequireTEE && label == "" {
		return "", errNotAttested
	}
	return label, nil
}

// transcript is one job as the buyer saw it: everything needed to check the
// receipt, and to prove the node wrong if it is.
type transcript struct {
	jobKey  *box.Identity
	request []byte   // sealed request
	chunks  [][]byte // sealed response chunks, in arrival order
	hasher  *receipt.StreamHasher
	corrupt error // first chunk that failed to open or arrived out of order
}

// runJob executes one ticket. delivered reports whether any output reached
// onChunk, after which failing over is no longer possible.
func (c *Client) runJob(ctx context.Context, t *protocol.Ticket, body []byte, onChunk func([]byte) error) (res *Result, delivered bool, err error) {
	nodeID, err := peer.Decode(t.Node)
	if err != nil {
		return nil, false, err
	}
	addrs := make([]ma.Multiaddr, 0, len(t.NodeAddrs))
	for _, a := range t.NodeAddrs {
		if m, err := ma.NewMultiaddr(a); err == nil {
			addrs = append(addrs, m)
		}
	}
	c.h.Peerstore().AddAddrs(nodeID, addrs, peerstore.TempAddrTTL)

	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, jobTimeout)
		defer cancel()
	}
	st, err := c.h.NewStream(network.WithAllowLimitedConn(ctx, "sharetoken-infer"), nodeID, lproto.ID(protocol.InferProtocol))
	if err != nil {
		return nil, false, err
	}
	defer st.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = st.SetDeadline(dl)
	}

	// One fresh key per job: the request is sealed from it and the response to
	// it. Disclosing it later opens this job and no other.
	tr := &transcript{hasher: receipt.NewStreamHasher()}
	if tr.jobKey, err = box.GenerateIdentity(); err != nil {
		return nil, false, err
	}
	if tr.request, err = box.SealFrom(tr.jobKey, t.NodeBoxPub, body, protocol.RequestAAD(t)); err != nil {
		return nil, false, err
	}
	if err := protocol.SendRequest(st, protocol.TypeInfer, protocol.InferRequest{Ticket: *t, BuyerBoxPub: tr.jobKey.Public(), Sealed: tr.request}); err != nil {
		return nil, false, err
	}

	var (
		content, reasoning, all strings.Builder
		tools                   protocol.ToolCallAccumulator
		finish                  string
		sr                      receipt.SignedReceipt
	)
	frames := 0
recv:
	for {
		wait := c.IdleTimeout
		if frames == 0 {
			wait = c.FirstByteTimeout
		}
		if wait > 0 {
			dl := time.Now().Add(wait)
			if cd, ok := ctx.Deadline(); ok && cd.Before(dl) {
				dl = cd
			}
			_ = st.SetReadDeadline(dl)
		}
		env, err := protocol.Recv(st)
		if err != nil {
			if wait > 0 && isTimeout(err) && ctx.Err() == nil {
				_ = st.Reset()
				err = fmt.Errorf("node stalled: nothing received for %v: %w", wait, err)
			}
			return nil, delivered, err
		}
		frames++
		switch env.Type {
		case protocol.TypeChunk:
			var ch protocol.Chunk
			if err := env.Decode(protocol.TypeChunk, &ch); err != nil {
				return nil, delivered, err
			}
			if len(tr.chunks) >= protocol.MaxEvidenceChunks {
				return nil, delivered, errors.New("node sent too many chunks")
			}
			pos := uint64(len(tr.chunks))
			tr.chunks = append(tr.chunks, ch.Sealed)
			tr.hasher.WriteChunk(ch.Sealed)
			if tr.corrupt != nil {
				continue // keep the transcript complete; deliver nothing more
			}
			plain, err := tr.jobKey.Open(ch.Sealed, protocol.ChunkAAD(t, pos))
			if ch.Seq != pos || err != nil {
				tr.corrupt = fmt.Errorf("chunk %d failed authentication", pos)
				continue
			}
			if info, err := protocol.ParseChunk(plain); err == nil {
				content.WriteString(info.Content)
				reasoning.WriteString(info.Reasoning)
				all.WriteString(info.Text())
				tools.Add(info.ToolCalls)
				if info.FinishReason != "" {
					finish = info.FinishReason
				}
			}
			if onChunk != nil {
				delivered = true
				if err := onChunk(plain); err != nil {
					_ = st.Reset()
					return nil, true, err
				}
			}
		case protocol.TypeReceipt:
			if err := env.Decode(protocol.TypeReceipt, &sr); err != nil {
				return nil, delivered, err
			}
			break recv
		default:
			// An error frame decodes to *protocol.Error; anything else is a
			// protocol violation.
			return nil, delivered, env.Decode(protocol.TypeReceipt, nil)
		}
	}

	res = &Result{Ticket: *t, Receipt: sr, Content: content.String(), Reasoning: reasoning.String(), Text: all.String(), ToolCalls: tools.Calls(), FinishReason: finish}
	if err := c.checkReceipt(t, &sr, tr, body, all.String()); err != nil {
		_ = protocol.Send(st, protocol.TypeReject, protocol.Reject{Reason: err.Error()})
		return res, delivered, fmt.Errorf("%w: %v", ErrReceiptRejected, err)
	}
	ack := ed25519.Sign(c.keys.Signer, protocol.AckBytes(&sr.Receipt))
	if err := protocol.Send(st, protocol.TypeAck, protocol.Ack{Sig: ack}); err != nil {
		// The node settles without the ack; the payout then waits out the
		// dispute window. The output itself is verified either way.
		c.log.Warn("could not deliver ack", "job", t.JobID, "err", err)
	}
	_ = st.CloseWrite()
	res.CostMicros = protocol.TicketCost(t, sr.Receipt.PromptTokens, sr.Receipt.CompletionTokens)
	res.Verified = true
	return res, delivered, nil
}

// checkReceipt is the buyer-side gate. On failure it disputes the job: with
// evidence when the transcript can prove the node wrong (unreadable chunks,
// inflated usage), as a plain claim when it cannot (the node signed bytes
// other than those delivered).
func (c *Client) checkReceipt(t *protocol.Ticket, sr *receipt.SignedReceipt, tr *transcript, body []byte, text string) error {
	if err := receipt.BuyerCheck(sr, t.JobID, t.Model, t.NodeReceiptKey, receipt.Hash(tr.request), tr.hasher.Sum()); err != nil {
		c.background(func() { c.fileDispute(t.JobID, err.Error()) })
		return err
	}
	if tr.corrupt != nil {
		c.disputeWithEvidence(t, sr, tr, tr.corrupt.Error())
		return tr.corrupt
	}
	estPrompt, err := protocol.EstimatePromptTokens(body)
	if err != nil {
		return err
	}
	if err := protocol.UsagePlausible(sr.Receipt.PromptTokens, sr.Receipt.CompletionTokens, estPrompt, protocol.EstimateTokens(text)); err != nil {
		c.disputeWithEvidence(t, sr, tr, "implausible usage: "+err.Error())
		return err
	}
	return nil
}

func (c *Client) disputeWithEvidence(t *protocol.Ticket, sr *receipt.SignedReceipt, tr *transcript, reason string) {
	if !c.Evidence {
		c.background(func() { c.fileDispute(t.JobID, reason) })
		return
	}
	ev := protocol.Evidence{
		JobID:     t.JobID,
		Reason:    reason,
		JobSecret: tr.jobKey.Secret(),
		Request:   tr.request,
		Receipt:   sr,
		Chunks:    uint64(len(tr.chunks)),
	}
	chunks := tr.chunks
	c.background(func() {
		for attempt := 0; attempt < 3; attempt++ {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			v, err := c.SubmitEvidence(ctx, &ev, chunks)
			cancel()
			if err == nil {
				c.log.Warn("dispute decided on evidence", "job", t.JobID, "outcome", v.Outcome, "status", v.Status, "detail", v.Detail)
				return
			}
			if protocol.CodeOf(err) != "" {
				c.log.Warn("evidence refused", "job", t.JobID, "err", err)
				return
			}
			time.Sleep(time.Second << attempt)
		}
		c.log.Error("could not submit dispute evidence", "job", t.JobID)
	})
}

// SubmitEvidence discloses one job's key and transcript to the coordinator
// and returns its verdict.
func (c *Client) SubmitEvidence(ctx context.Context, ev *protocol.Evidence, chunks [][]byte) (*protocol.Verdict, error) {
	st, err := c.h.NewStream(network.WithAllowLimitedConn(ctx, "sharetoken-evidence"), c.coordinator.ID, lproto.ID(protocol.CtrlProtocol))
	if err != nil {
		c.noteCoordinatorErr(ctx, err)
		return nil, err
	}
	defer st.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = st.SetDeadline(dl)
	}
	if err := protocol.SendRequest(st, protocol.TypeEvidence, ev); err != nil {
		_ = st.Reset()
		return nil, err
	}
	for i, ch := range chunks {
		if err := protocol.Send(st, protocol.TypeChunk, protocol.Chunk{Seq: uint64(i), Sealed: ch}); err != nil {
			_ = st.Reset()
			return nil, err
		}
	}
	_ = st.CloseWrite()
	env, err := protocol.Recv(st)
	if err != nil {
		return nil, err
	}
	var v protocol.Verdict
	if err := env.Decode(protocol.TypeVerdict, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// fileDispute reports a job to the coordinator, retrying transient failures.
func (c *Client) fileDispute(jobID, reason string) {
	// Keep trying for as long as the job can still be held: a buyer whose own
	// path to the coordinator just broke (its relay died) files once it is
	// reconnected; otherwise the failed tickets would count as unused ones.
	deadline := time.Now().Add(disputeRetryWindow)
	backoff := time.Second
	for {
		ctx, cancel := context.WithTimeout(context.Background(), p2p.DefaultCallTimeout)
		err := c.Dispute(ctx, jobID, reason)
		cancel()
		if err == nil || protocol.CodeOf(err) != "" || time.Now().Add(backoff).After(deadline) {
			return
		}
		time.Sleep(backoff)
		backoff = min(backoff*2, 15*time.Second)
	}
}

// disputeRetryWindow covers the coordinator's default ticket lifetime plus
// its start grace, after which an unstarted job is released anyway.
const disputeRetryWindow = 3 * time.Minute

// Dispute contests a job's settlement without evidence.
func (c *Client) Dispute(ctx context.Context, jobID, reason string) error {
	return c.call(ctx, protocol.TypeDispute,
		protocol.DisputeRequest{JobID: jobID, Reason: reason}, protocol.TypeOK, nil)
}

// Models lists models with live offers.
func (c *Client) Models(ctx context.Context) ([]protocol.ModelSummary, error) {
	var resp protocol.ModelsResponse
	err := c.call(ctx, protocol.TypeModels, struct{}{}, protocol.TypeModels, &resp)
	return resp.Models, err
}

// Balance reports this peer's account.
func (c *Client) Balance(ctx context.Context) (protocol.BalanceResponse, error) {
	var resp protocol.BalanceResponse
	err := c.call(ctx, protocol.TypeBalance, struct{}{}, protocol.TypeBalance, &resp)
	return resp, err
}

// promptEst is the prompt estimate sent with a capped request (0 otherwise).
func promptEst(req ChatRequest) uint64 {
	if req.MaxTokens == 0 {
		return 0
	}
	n, err := protocol.EstimatePromptTokens(req.Body)
	if err != nil {
		return 0
	}
	return n
}

// isTimeout reports a read deadline expiring, whichever transport (yamux,
// QUIC) the stream runs on.
func isTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, os.ErrDeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}
