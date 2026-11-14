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
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	lproto "github.com/libp2p/go-libp2p/core/protocol"
	ma "github.com/multiformats/go-multiaddr"

	"sharetoken/internal/attest"
	"sharetoken/internal/box"
	"sharetoken/internal/p2p"
	"sharetoken/internal/protocol"
	"sharetoken/internal/receipt"
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
	// node's first frame and between frames (defaults 30s each). The node
	// sends a progress frame every node.HeartbeatInterval while its model is
	// silent, so only a node (or path) that stopped working runs into them.
	// It is abandoned: a request that has delivered nothing to the caller yet
	// fails over to another node.
	FirstByteTimeout, IdleTimeout time.Duration
	// ResumeWindow is how long the buyer tries to resume a job whose stream
	// broke (default 45 s, within the node's node.ResumeWindow).
	ResumeWindow time.Duration
	// MaxSilence bounds how long a job may go without response data while
	// the node keeps sending progress frames (default 5 minutes): long
	// enough for a reasoning model, short enough for a hung model server.
	MaxSilence time.Duration
	// Attestation verifies nodes' TEE quotes; required for RequireTEE.
	Attestation *Attestation
	// OnCoordinatorUnreachable, if set, is called when a call to the
	// coordinator fails in transport (no answer, as opposed to a refusal):
	// the client then checks its relay path at once instead of at the next
	// health check. It must not block.
	OnCoordinatorUnreachable func()
	log                      *slog.Logger
	bg                       sync.WaitGroup // disputes being delivered in the background
	sticky                   stickyNodes
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
		FirstByteTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, MaxSilence: 5 * time.Minute, ResumeWindow: 45 * time.Second, log: slog.Default().With("component", "buyer")}
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
	// Session names the conversation the request belongs to (default: its
	// session_id, else its first messages). The node that served the
	// session last is asked for again while it is available.
	Session string
	// PreferNode is set by Chat from the session; callers leave it empty.
	PreferNode string
}

// Result is a completed, verified job.
type Result struct {
	Ticket            protocol.Ticket
	Receipt           receipt.SignedReceipt
	Content           string
	Reasoning         string // a thinking model's reasoning, billed as output
	Text              string // everything billed as output: content, reasoning, tool arguments
	ToolCalls         []protocol.ToolCall
	AnthropicThinking []json.RawMessage
	FinishReason      string
	CostMicros        uint64
	Verified          bool   // receipt passed every check and was acknowledged
	Attested          string // allow-list label of the node's verified TEE image, if any
	Timing            Timing // where the time and the bytes of the last attempt went
}

// Timing breaks one job down: time spent per step, the path to the node, and
// the bytes on the infer stream (frames as sent, before the transport's own
// encryption and multiplexing).
type Timing struct {
	TicketMs    float64 `json:"ticket_ms"`     // coordinator round trips, all attempts
	DialMs      float64 `json:"dial_ms"`       // opening the stream to the node
	UploadMs    float64 `json:"upload_ms"`     // writing the sealed request
	FirstByteMs float64 `json:"first_byte_ms"` // request written -> first response frame
	TotalMs     float64 `json:"total_ms"`      // Chat called -> receipt checked
	Reused      bool    `json:"reused"`        // a connection to the node existed already
	Relayed     bool    `json:"relayed"`       // the stream ran through a relay
	Via         string  `json:"via,omitempty"` // that relay's peer ID
	Preferred   bool    `json:"preferred"`     // the session's previous node served it again
	Attempts    int     `json:"attempts"`
	Resumes     int     `json:"resumes"`  // the stream broke and the job went on over a new one
	Frames      int     `json:"frames"`   // response data frames
	Progress    int     `json:"progress"` // progress frames (the model was silent)
	Chunks      int     `json:"chunks"`   // upstream payloads delivered
	UpBytes     int64   `json:"up_bytes"`
	DownBytes   int64   `json:"down_bytes"`
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
		timing   Timing
	)
	start := time.Now()
	// Compress the request while the coordinator picks a node: sealing needs
	// the ticket, compressing does not.
	encoded := make(chan encodedRequest, 1)
	go func() {
		b, err := protocol.EncodeRequest(req.Body)
		encoded <- encodedRequest{b, err}
	}()
	enc := sync.OnceValue(func() encodedRequest { return <-encoded })
	session := sessionKey(req)
	prefer := c.sticky.get(session)
	for attempt := 0; attempt <= c.Retries; attempt++ {
		timing.Attempts++
		treq := req
		if !slices.Contains(exclude, prefer) {
			treq.PreferNode = prefer
		}
		t0 := time.Now()
		tr, err := c.requestTicket(ctx, treq, exclude)
		timing.TicketMs += ms(time.Since(t0))
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
		e := enc()
		if e.err != nil {
			return nil, e.err
		}
		res, delivered, err := c.runJob(ctx, &tr.Ticket, e.plain, req.Body, onChunk, &timing)
		timing.Preferred = prefer != "" && tr.Ticket.Node == prefer
		if delivered || err == nil {
			c.sticky.put(session, tr.Ticket.Node)
		} else if tr.Ticket.Node == prefer {
			c.sticky.drop(session)
		}
		if res != nil {
			res.Attested = label
			timing.TotalMs = ms(time.Since(start))
			res.Timing = timing
		}
		c.logJob(&tr.Ticket, &timing, err)
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
			PreferNode:    req.PreferNode,
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
	chunks  [][]byte // sealed response frames, in arrival order
	hasher  *receipt.StreamHasher
	corrupt error // first frame that failed to open, decode or arrived out of order
}

// encodedRequest is a request compressed and padded for sealing.
type encodedRequest struct {
	plain []byte
	err   error
}

// runJob executes one ticket. delivered reports whether any output reached
// onChunk, after which failing over is no longer possible.
func (c *Client) runJob(ctx context.Context, t *protocol.Ticket, encoded, body []byte, onChunk func([]byte) error, tm *Timing) (res *Result, delivered bool, err error) {
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
	tm.Reused = c.h.Network().Connectedness(nodeID) == network.Connected
	t0 := time.Now()
	raw, err := c.h.NewStream(network.WithAllowLimitedConn(ctx, "sharetoken-infer"), nodeID, lproto.ID(protocol.InferProtocol))
	tm.DialMs = ms(time.Since(t0))
	if err != nil {
		return nil, false, err
	}
	js := &jobStream{st: &countingStream{Stream: raw}}
	defer func() { tm.UpBytes, tm.DownBytes = js.totals() }()
	defer func() { _ = js.cur().Close() }()
	// A caller that goes away (the HTTP client closed the connection) stops
	// the job: the node stops its model and bills what was delivered.
	defer context.AfterFunc(ctx, js.cancel)()
	st := js.cur()
	tm.Relayed = p2p.IsRelayed(raw.Conn())
	tm.Via = ""
	if tm.Relayed {
		if v, err := raw.Conn().RemoteMultiaddr().ValueForProtocol(ma.P_P2P); err == nil {
			tm.Via = v
		}
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = st.SetDeadline(dl)
	}

	// One fresh key per job: the request is sealed from it, and the response
	// stream key derives from it. Disclosing it later opens this job and no
	// other.
	tr := &transcript{hasher: receipt.NewStreamHasher()}
	if tr.jobKey, err = box.GenerateIdentity(); err != nil {
		return nil, false, err
	}
	if tr.request, err = box.SealFrom(tr.jobKey, t.NodeBoxPub, encoded, protocol.RequestAAD(t)); err != nil {
		return nil, false, err
	}
	key, err := tr.jobKey.NewStream(t.NodeBoxPub, protocol.ResponseKeyInfo(t, tr.jobKey.Public()))
	if err != nil {
		return nil, false, err
	}
	t0 = time.Now()
	if err := protocol.SendRequest(st, protocol.TypeInfer, protocol.InferRequest{Ticket: *t, BuyerBoxPub: tr.jobKey.Public(), RequestBytes: uint64(len(tr.request))}); err != nil {
		return nil, false, err
	}
	if err := protocol.SendSealedRequest(st, tr.request); err != nil {
		return nil, false, err
	}
	sent := time.Now()
	tm.UploadMs = ms(sent.Sub(t0))
	tm.Frames, tm.Progress, tm.Chunks = 0, 0, 0

	var (
		content, reasoning, all strings.Builder
		tools                   protocol.ToolCallAccumulator
		thinking                []json.RawMessage
		finish                  string
		sr                      receipt.SignedReceipt
	)
	dec := protocol.NewResponseDecoder()
	frames := 0
	lastData := sent
	resumedAt := 0 // frames received when the stream was last replaced
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
			_ = js.cur().SetReadDeadline(dl)
		}
		f, err := protocol.ReadStreamFrame(js.cur(), protocol.MaxFrameSize)
		if err != nil {
			if ctx.Err() == nil && !js.cancelled() && frames > resumedAt && c.ResumeWindow > 0 {
				// The node took the job and the stream broke (or went quiet
				// past every heartbeat): continue it over a new stream. Not
				// again before the new stream brought a frame: a node that
				// answers a resume and then says nothing is the problem, not
				// the path to it.
				_ = js.cur().Reset()
				if rerr := c.resume(ctx, js, nodeID, t, uint64(len(tr.chunks))); rerr == nil {
					tm.Resumes++
					resumedAt = frames
					c.log.Info("resumed a job over a new stream", "job", t.JobID, "from", len(tr.chunks), "after", err)
					continue
				}
			}
			if wait > 0 && isTimeout(err) && ctx.Err() == nil {
				_ = js.cur().Reset()
				err = fmt.Errorf("node stalled: nothing received for %v: %w", wait, err)
			}
			return nil, delivered, err
		}
		frames++
		switch {
		case f.Env == nil && f.Kind == protocol.BinProgress:
			tm.Progress++
			if c.MaxSilence > 0 && time.Since(lastData) > c.MaxSilence {
				js.cancel()
				return nil, delivered, fmt.Errorf("node stalled: no response data for %v", c.MaxSilence.Round(time.Second))
			}
		case f.Env == nil && f.Kind == protocol.BinData:
			seq, sealed, err := protocol.DecodeData(f.Payload)
			if err != nil {
				return nil, delivered, err
			}
			if len(tr.chunks) >= protocol.MaxEvidenceChunks {
				return nil, delivered, errors.New("node sent too many frames")
			}
			if tm.Frames == 0 {
				tm.FirstByteMs = ms(time.Since(sent))
			}
			lastData = time.Now()
			tm.Frames++
			pos := uint64(len(tr.chunks))
			tr.chunks = append(tr.chunks, sealed)
			tr.hasher.WriteChunk(sealed)
			if tr.corrupt != nil {
				continue // keep the transcript complete; deliver nothing more
			}
			plain, err := key.Open(pos, sealed, protocol.ChunkAAD(t, pos))
			if seq != pos || err != nil {
				tr.corrupt = fmt.Errorf("frame %d failed authentication", pos)
				continue
			}
			recs, err := dec.Frame(plain)
			if err != nil {
				tr.corrupt = fmt.Errorf("frame %d does not decode: %v", pos, err)
				continue
			}
			for _, payload := range recs {
				tm.Chunks++
				if info, err := protocol.ParseChunk(payload); err == nil {
					content.WriteString(info.Content)
					reasoning.WriteString(info.Reasoning)
					all.WriteString(info.Text())
					tools.Add(info.ToolCalls)
					thinking = append(thinking, info.AnthropicThinking...)
					if info.FinishReason != "" {
						finish = info.FinishReason
					}
				}
				if onChunk != nil {
					delivered = true
					if err := onChunk(payload); err != nil {
						js.cancel()
						return nil, true, err
					}
				}
			}
		case f.Env != nil && f.Env.Type == protocol.TypeReceipt:
			if err := f.Env.Decode(protocol.TypeReceipt, &sr); err != nil {
				return nil, delivered, err
			}
			break recv
		case f.Env != nil:
			// An error frame decodes to *protocol.Error; anything else is a
			// protocol violation.
			return nil, delivered, f.Env.Decode(protocol.TypeReceipt, nil)
		default:
			return nil, delivered, fmt.Errorf("unexpected frame of kind %d", f.Kind)
		}
	}
	if tr.corrupt == nil && !dec.Done() {
		tr.corrupt = errors.New("the response ended without its final frame")
	}

	res = &Result{Ticket: *t, Receipt: sr, Content: content.String(), Reasoning: reasoning.String(), Text: all.String(), ToolCalls: tools.Calls(), FinishReason: finish}
	res.AnthropicThinking = thinking
	if err := c.checkReceipt(t, &sr, tr, body, all.String()); err != nil {
		_ = js.send(protocol.TypeReject, protocol.Reject{Reason: err.Error()})
		return res, delivered, fmt.Errorf("%w: %v", ErrReceiptRejected, err)
	}
	ack := ed25519.Sign(c.keys.Signer, protocol.AckBytes(&sr.Receipt))
	if err := js.send(protocol.TypeAck, protocol.Ack{Sig: ack}); err != nil {
		// The node settles without the ack; the payout then waits out the
		// dispute window. The output itself is verified either way.
		c.log.Warn("could not deliver ack", "job", t.JobID, "err", err)
	}
	_ = js.cur().CloseWrite()
	res.CostMicros = protocol.TicketCost(t, sr.Receipt.PromptTokens, sr.Receipt.CompletionTokens)
	res.Verified = true
	return res, delivered, nil
}

// countingStream counts the bytes of a stream's frames.
type countingStream struct {
	network.Stream
	in, out atomic.Int64
}

func (s *countingStream) Read(p []byte) (int, error) {
	n, err := s.Stream.Read(p)
	s.in.Add(int64(n))
	return n, err
}

func (s *countingStream) Write(p []byte) (int, error) {
	n, err := s.Stream.Write(p)
	s.out.Add(int64(n))
	return n, err
}

// jobStream is a job's stream to the node, which a resume replaces. Frames
// the buyer writes (cancel, reject, ack) go through it, so that a cancel from
// another goroutine does not interleave with them.
type jobStream struct {
	mu      sync.Mutex
	st      *countingStream
	in, out int64 // bytes of the streams replaced
	stopped bool
}

func (j *jobStream) cur() *countingStream {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.st
}

func (j *jobStream) send(typ string, body any) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return protocol.Send(j.st, typ, body)
}

// cancel tells the node to stop the job, then closes the stream. It closes
// rather than resets: a reset makes the node's side discard what it has not
// read yet, the cancel frame included, and the node would wait for a resume.
func (j *jobStream) cancel() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.stopped {
		return
	}
	j.stopped = true
	_ = j.st.SetWriteDeadline(time.Now().Add(time.Second))
	if protocol.Send(j.st, protocol.TypeCancel, struct{}{}) == nil {
		_ = j.st.Close()
		return
	}
	_ = j.st.Reset()
}

func (j *jobStream) cancelled() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.stopped
}

func (j *jobStream) replace(st *countingStream) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.in += j.st.in.Load()
	j.out += j.st.out.Load()
	j.st = st
}

func (j *jobStream) totals() (up, down int64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.out + j.st.out.Load(), j.in + j.st.in.Load()
}

// resume opens a new stream to the node for job t, from frame from on. The
// old connection is dropped first: it may be dead without knowing it yet,
// and the dial then finds another path (another relay, or direct).
func (c *Client) resume(ctx context.Context, js *jobStream, nodeID peer.ID, t *protocol.Ticket, from uint64) error {
	deadline := time.Now().Add(c.ResumeWindow)
	for {
		_ = c.h.Network().ClosePeer(nodeID)
		dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		raw, err := c.h.NewStream(network.WithAllowLimitedConn(dctx, "sharetoken-infer"), nodeID, lproto.ID(protocol.InferProtocol))
		cancel()
		if err == nil {
			st := &countingStream{Stream: raw}
			if dl, ok := ctx.Deadline(); ok {
				_ = st.SetDeadline(dl)
			}
			if err = protocol.SendRequest(st, protocol.TypeResume, protocol.ResumeRequest{JobID: t.JobID, From: from}); err == nil {
				js.replace(st)
				return nil
			}
			_ = st.Reset()
		}
		if ctx.Err() != nil || js.cancelled() || time.Now().Add(time.Second).After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// logJob records one attempt's timing.
func (c *Client) logJob(t *protocol.Ticket, tm *Timing, err error) {
	args := []any{"job", t.JobID, "node", t.Node, "ticket_ms", tm.TicketMs, "dial_ms", tm.DialMs, "upload_ms", tm.UploadMs,
		"first_byte_ms", tm.FirstByteMs, "reused", tm.Reused, "relayed", tm.Relayed, "preferred", tm.Preferred,
		"frames", tm.Frames, "progress", tm.Progress, "chunks", tm.Chunks, "up_bytes", tm.UpBytes, "down_bytes", tm.DownBytes}
	if err != nil {
		c.log.Info("job attempt failed", append(args, "err", err)...)
		return
	}
	c.log.Info("job done", args...)
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
		Receipt:   sr,
		Chunks:    uint64(len(tr.chunks)),
	}
	request, chunks := tr.request, tr.chunks
	c.background(func() {
		for attempt := 0; attempt < 3; attempt++ {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			v, err := c.SubmitEvidence(ctx, &ev, request, chunks)
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

// SubmitEvidence discloses one job's key and transcript (the sealed request
// and the sealed response frames) to the coordinator and returns its verdict.
func (c *Client) SubmitEvidence(ctx context.Context, ev *protocol.Evidence, request []byte, chunks [][]byte) (*protocol.Verdict, error) {
	st, err := c.h.NewStream(network.WithAllowLimitedConn(ctx, "sharetoken-evidence"), c.coordinator.ID, lproto.ID(protocol.CtrlProtocol))
	if err != nil {
		c.noteCoordinatorErr(ctx, err)
		return nil, err
	}
	defer st.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = st.SetDeadline(dl)
	}
	ev.RequestBytes = uint64(len(request))
	if err := protocol.SendRequest(st, protocol.TypeEvidence, ev); err != nil {
		_ = st.Reset()
		return nil, err
	}
	if err := protocol.SendSealedRequest(st, request); err != nil {
		_ = st.Reset()
		return nil, err
	}
	for i, ch := range chunks {
		if err := protocol.SendData(st, uint64(i), ch); err != nil {
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
