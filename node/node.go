// Package node is the seller side of a user's client: it announces signed
// offers to the coordinator (through a relay) and serves ticketed jobs on a
// libp2p stream from the buyer, direct or relayed.
//
// Per job: verify the coordinator's ticket, open the sealed request, call the
// model server, seal each streamed chunk to the buyer's per-job key, sign a
// receipt over the exact ciphertexts, and submit it for settlement (with the
// buyer's ack when given). The model server's address and any key for it never
// leave this process.
//
// In a confidential VM the node's keys are generated in guest memory and its
// offer carries a hardware quote binding them (package attest), so buyers can
// check that only an audited image ever decrypts their prompts.
package node

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	lproto "github.com/libp2p/go-libp2p/core/protocol"

	"sharetoken/attest"
	"sharetoken/box"
	"sharetoken/p2p"
	"sharetoken/protocol"
	"sharetoken/receipt"
)

const (
	requestTimeout  = 30 * time.Second
	withdrawTimeout = 3 * time.Second // stopping is not held up for long by a coordinator out of reach
	// How long after a ticket expires the node keeps retrying its start report
	// and its settlement (the coordinator releases unstarted jobs 30s after
	// expiry, and holds started ones for a 15 minute settlement grace).
	startReportGrace  = 30 * time.Second
	settleRetryWindow = 14 * time.Minute
	ackTimeout        = 30 * time.Second
)

// ModelConfig maps a marketplace model ID to the upstream server's name for it
// and sets its price (micro-credits per million tokens).
type ModelConfig struct {
	ID             string `json:"id"`
	UpstreamModel  string `json:"upstream_model"`
	PriceInMicros  uint64 `json:"price_in"`
	PriceOutMicros uint64 `json:"price_out"`
}

// Config configures a node. Zero durations get defaults.
type Config struct {
	Coordinator    peer.AddrInfo
	Models         []ModelConfig
	Upstream       Upstream
	MaxConcurrency int
	OfferInterval  time.Duration
	OfferTTL       time.Duration
	JobTimeout     time.Duration
	Quote          *attest.Quote // optional TEE quote binding the receipt and box keys
	// Addrs returns the addresses to advertise (default: the host's own).
	// A node behind NAT adds its relay circuit addresses here.
	Addrs func() []string
	// OnCoordinatorUnreachable, if set, is called when a call to the
	// coordinator fails in transport (no answer, as opposed to a refusal), so
	// the client can fail over to another relay at once. It must not block.
	OnCoordinatorUnreachable func()
	Now                      func() time.Time
}

func (c *Config) setDefaults() {
	if c.MaxConcurrency <= 0 {
		c.MaxConcurrency = 2
	}
	if c.OfferInterval == 0 {
		c.OfferInterval = 15 * time.Second
	}
	if c.OfferTTL == 0 {
		c.OfferTTL = time.Minute
	}
	if c.JobTimeout == 0 {
		c.JobTimeout = 10 * time.Minute
	}
	if c.Now == nil {
		c.Now = time.Now
	}
}

// Stats is what a node has done since it started.
type Stats struct {
	JobsServed     uint64    `json:"jobs_served"`
	JobsFailed     uint64    `json:"jobs_failed"`
	EarnedMicros   uint64    `json:"earned_micros"` // settled and acked; unacked jobs pay out after the dispute window
	Active         int       `json:"active_jobs"`
	LastAnnounce   time.Time `json:"last_announce,omitzero"`
	AnnounceError  string    `json:"announce_error,omitempty"`
	AdvertisedAddr []string  `json:"advertised_addrs,omitempty"`
}

// Service is a running node.
type Service struct {
	h      host.Host
	keys   *p2p.Keys
	cfg    Config
	id     identity
	models map[string]ModelConfig
	sem    chan struct{}
	kick   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	log    *slog.Logger

	mu   sync.Mutex
	seen map[string]int64 // job ID -> ticket expiry; rejects ticket replay

	served, failed, earned atomic.Uint64
	statMu                 sync.Mutex
	lastAnnounce           time.Time
	announceErr            string
	advertised             []string
}

// NewService starts a node on h.
func NewService(ctx context.Context, h host.Host, keys *p2p.Keys, cfg Config) (*Service, error) {
	cfg.setDefaults()
	if cfg.Coordinator.ID == "" {
		return nil, errors.New("node: coordinator address is required")
	}
	if cfg.Upstream == nil {
		return nil, errors.New("node: upstream is required")
	}
	if len(cfg.Models) == 0 {
		return nil, errors.New("node: at least one model is required")
	}
	models := make(map[string]ModelConfig, len(cfg.Models))
	for _, m := range cfg.Models {
		if m.ID == "" || m.UpstreamModel == "" {
			return nil, fmt.Errorf("node: model %+v needs both id and upstream_model", m)
		}
		if _, dup := models[m.ID]; dup {
			return nil, fmt.Errorf("node: duplicate model id %q", m.ID)
		}
		models[m.ID] = m
	}

	ctx, cancel := context.WithCancel(ctx)
	s := &Service{
		h:    h,
		keys: keys,
		cfg:  cfg,
		id: identity{
			self:        keys.ID(),
			coordinator: cfg.Coordinator.ID.String(),
			boxPub:      keys.Box.Public(),
			receiptKey:  keys.ReceiptKey(),
		},
		models: models,
		sem:    make(chan struct{}, cfg.MaxConcurrency),
		kick:   make(chan struct{}, 1),
		ctx:    ctx,
		cancel: cancel,
		log:    slog.Default().With("component", "node"),
		seen:   make(map[string]int64),
	}
	if s.cfg.Addrs == nil {
		s.cfg.Addrs = func() []string { return p2p.AddrStrings(h) }
	}
	h.Peerstore().AddAddrs(cfg.Coordinator.ID, cfg.Coordinator.Addrs, peerstore.PermanentAddrTTL)
	h.SetStreamHandler(lproto.ID(protocol.InferProtocol), s.handleInfer)
	go s.offerLoop()
	return s, nil
}

// Close stops the node (the host is owned by the caller).
func (s *Service) Close() {
	s.h.RemoveStreamHandler(lproto.ID(protocol.InferProtocol))
	s.cancel()
	// Best effort: without it the offer expires by itself, and until then buyers
	// get tickets for a node that no longer answers.
	ctx, cancel := context.WithTimeout(context.Background(), withdrawTimeout)
	defer cancel()
	if err := p2p.Call(ctx, s.h, s.cfg.Coordinator.ID, protocol.CtrlProtocol, protocol.TypeWithdraw, protocol.WithdrawRequest{IssuedAt: s.cfg.Now().UnixNano()}, protocol.TypeOK, nil); err != nil {
		s.log.Debug("withdrawing the offer failed", "err", err)
	}
}

// Announce sends a fresh offer now (e.g. after the node's addresses changed).
func (s *Service) Announce() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Stats reports what the node has done.
func (s *Service) Stats() Stats {
	s.statMu.Lock()
	defer s.statMu.Unlock()
	return Stats{
		JobsServed: s.served.Load(), JobsFailed: s.failed.Load(), EarnedMicros: s.earned.Load(),
		Active: len(s.sem), LastAnnounce: s.lastAnnounce, AnnounceError: s.announceErr, AdvertisedAddr: s.advertised,
	}
}

// offerLoop announces the offer every OfferInterval; the announcement is also
// the node's heartbeat, and the offer expires at the coordinator when it stops.
func (s *Service) offerLoop() {
	for i := 0; ; i++ {
		err := s.announce()
		s.statMu.Lock()
		if err == nil {
			s.lastAnnounce, s.announceErr = s.cfg.Now(), ""
		} else if s.ctx.Err() == nil {
			if s.announceErr != err.Error() {
				s.log.Warn("announcing offer failed", "err", err)
			}
			s.announceErr = err.Error()
		}
		s.statMu.Unlock()
		s.pruneSeen()
		wait := s.cfg.OfferInterval
		if err != nil && wait > 2*time.Second {
			wait = 2 * time.Second // retry soon after a failure
		}
		select {
		case <-s.ctx.Done():
			return
		case <-s.kick:
		case <-time.After(wait):
		}
	}
}

func (s *Service) announce() error {
	now := s.cfg.Now()
	addrs := s.cfg.Addrs()
	o := &protocol.Offer{
		Node:           s.id.self,
		Addrs:          addrs,
		BoxPub:         s.id.boxPub,
		ReceiptKey:     s.id.receiptKey,
		MaxConcurrency: uint32(s.cfg.MaxConcurrency),
		IssuedAt:       now.UnixNano(),
		ExpiresAt:      now.Add(s.cfg.OfferTTL).UnixNano(),
		Quote:          s.cfg.Quote,
	}
	for _, m := range s.cfg.Models {
		o.Models = append(o.Models, protocol.ModelPrice{Model: m.ID, PriceInMicros: m.PriceInMicros, PriceOutMicros: m.PriceOutMicros})
	}
	o.Sign(s.keys.Signer)
	s.statMu.Lock()
	s.advertised = addrs
	s.statMu.Unlock()
	ctx, cancel := context.WithTimeout(s.ctx, 20*time.Second)
	defer cancel()
	return s.call(ctx, protocol.TypeAnnounce, o, protocol.TypeOK, nil)
}

func (s *Service) handleInfer(st network.Stream) {
	defer st.Close()
	remote := st.Conn().RemotePeer().String()
	_ = st.SetDeadline(time.Now().Add(requestTimeout))

	env, err := protocol.Recv(st)
	if err != nil {
		_ = st.Reset()
		return
	}
	if env.Ver == "" {
		fail(st, protocol.Errorf(protocol.CodeBadRequest, "the request does not say the client's version"))
		return
	}
	var req protocol.InferRequest
	if err := env.Decode(protocol.TypeInfer, &req); err != nil {
		fail(st, protocol.Errorf(protocol.CodeBadRequest, "malformed infer request"))
		return
	}
	t := &req.Ticket
	mc, err := checkTicket(t, remote, s.id, s.models, s.cfg.Now())
	if err != nil {
		fail(st, err)
		return
	}
	// The response is sealed to the key the request was sealed from, so the
	// signed request hash commits to it: that is what lets the buyer prove
	// this job's transcript to a dispute judge by disclosing one key.
	if len(req.BuyerBoxPub) != box.PublicKeySize || len(req.Sealed) < box.Overhead ||
		!bytes.Equal(req.BuyerBoxPub, req.Sealed[:box.PublicKeySize]) {
		fail(st, protocol.Errorf(protocol.CodeBadRequest, "the response key must be the key the request was sealed from"))
		return
	}
	if !s.markSeen(t.JobID, t.ExpiresAt) {
		fail(st, protocol.Errorf(protocol.CodeBadTicket, "ticket already used"))
		return
	}
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	default:
		fail(st, protocol.Errorf(protocol.CodeBusy, "node is at capacity"))
		go s.abandon(*t, "at capacity")
		return
	}

	// The plaintext request exists only in memory for the life of this job; it
	// is never logged or written anywhere.
	plain, err := s.keys.Box.Open(req.Sealed, protocol.RequestAAD(t))
	if err != nil {
		fail(st, protocol.Errorf(protocol.CodeBadRequest, "cannot open sealed request"))
		go s.abandonNoFault(*t, "unreadable request")
		return
	}
	body, estPrompt, err := UpstreamRequest(t, plain, mc.UpstreamModel)
	if err != nil {
		fail(st, err)
		go s.abandonNoFault(*t, "bad request")
		return
	}

	_ = st.SetDeadline(time.Now().Add(s.cfg.JobTimeout))
	ctx, cancel := context.WithTimeout(s.ctx, s.cfg.JobTimeout)
	defer cancel()
	// Tell the coordinator the buyer showed up (in the background: it must
	// not delay the first byte). If the job was already released for not
	// starting in time, nobody will pay for it: stop.
	go s.reportStarted(*t, cancel)

	hasher := receipt.NewStreamHasher()
	var (
		seq   uint64
		usage *protocol.Usage
		text  strings.Builder
	)
	err = s.cfg.Upstream.Stream(ctx, body, func(payload []byte) error {
		info, perr := protocol.ParseChunk(payload)
		sealed, err := box.Seal(req.BuyerBoxPub, payload, protocol.ChunkAAD(t, seq))
		if err != nil {
			return err
		}
		if err := protocol.Send(st, protocol.TypeChunk, protocol.Chunk{Seq: seq, Sealed: sealed}); err != nil {
			return fmt.Errorf("%w: %v", errBuyerGone, err)
		}
		// Only what reached the buyer's stream counts.
		hasher.WriteChunk(sealed)
		if perr == nil {
			text.WriteString(info.Text())
			if info.Usage != nil {
				usage = info.Usage
			}
		}
		seq++
		return nil
	})
	if err == nil && seq == 0 {
		err = errors.New("upstream returned an empty stream")
	}
	if errors.Is(err, errBuyerGone) {
		if seq == 0 {
			// Gone before any output: nothing to bill, and not this node's fault.
			s.log.Info("buyer left before the first chunk", "job", t.JobID)
			go s.abandonNoFault(*t, "buyer left before any output")
			return
		}
		// The buyer stopped reading mid-stream (a user pressing "stop"). The
		// output it received was produced and delivered: bill exactly the
		// chunks sent, settled without an ack, so the payout waits out the
		// dispute window like any unacknowledged job. Not a node failure.
		s.log.Info("buyer cancelled mid-stream; billing the delivered part", "job", t.JobID, "chunks", seq)
		cancel()
		sr, serr := s.signReceipt(t, req.Sealed, hasher.Sum(), estPrompt, protocol.EstimateTokens(text.String()))
		if serr == nil {
			go s.settle(*t, *sr, nil)
		}
		return
	}
	if err != nil {
		// No receipt, no settlement: a failed job is never billed.
		var se *StatusError
		if seq == 0 && errors.As(err, &se) && se.RequestRejected() {
			// The buyer's request is invalid: tell it why (the message goes to
			// the buyer only, over the end-to-end encrypted stream) and release
			// the job without counting it against this node.
			s.log.Info("model server rejected the request", "job", t.JobID, "status", se.Status)
			fail(st, protocol.Errorf(protocol.CodeBadRequest, "model server rejected the request (HTTP %d): %s", se.Status, clip(se.Body, 300)))
			go s.abandonNoFault(*t, "request rejected by the model server")
			return
		}
		s.failed.Add(1)
		s.log.Warn("job failed", "job", t.JobID, "err", err)
		fail(st, protocol.Errorf(protocol.CodeUpstream, "model server error"))
		go s.abandon(*t, "model server error")
		return
	}

	pt, ct := estPrompt, protocol.EstimateTokens(text.String())
	if usage != nil {
		pt, ct = usage.PromptTokens, usage.CompletionTokens
	}
	sr, err := s.signReceipt(t, req.Sealed, hasher.Sum(), pt, ct)
	if err != nil {
		fail(st, err)
		return
	}
	if err := protocol.Send(st, protocol.TypeReceipt, sr); err != nil {
		return
	}

	// Wait for the buyer's verdict. An ack lets the coordinator pay out at once;
	// without one the payout waits out the dispute window.
	_ = st.SetDeadline(time.Now().Add(ackTimeout))
	var ack []byte
	if env, err := protocol.Recv(st); err == nil {
		switch env.Type {
		case protocol.TypeAck:
			var a protocol.Ack
			if env.Decode(protocol.TypeAck, &a) == nil {
				ack = a.Sig
			}
		case protocol.TypeReject:
			var r protocol.Reject
			_ = env.Decode(protocol.TypeReject, &r)
			s.log.Warn("buyer rejected receipt", "job", t.JobID, "reason", r.Reason)
		}
	}
	go s.settle(*t, *sr, ack)
}

// errBuyerGone marks a failure to write to the buyer's stream.
var errBuyerGone = errors.New("buyer stream closed")

func (s *Service) signReceipt(t *protocol.Ticket, sealedReq, respHash []byte, pt, ct uint64) (*receipt.SignedReceipt, error) {
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	return receipt.Sign(s.keys.Signer, receipt.Receipt{
		JobID:            t.JobID,
		ModelID:          t.Model,
		NodeID:           s.id.receiptKey,
		RequestHash:      receipt.Hash(sealedReq),
		ResponseHash:     respHash,
		PromptTokens:     pt,
		CompletionTokens: ct,
		UnixNano:         s.cfg.Now().UnixNano(),
		Nonce:            nonce,
	})
}

// settle submits the receipt, retrying transient failures. A coded refusal
// from the coordinator is final.
func (s *Service) settle(t protocol.Ticket, sr receipt.SignedReceipt, ack []byte) {
	req := protocol.SettleRequest{Ticket: t, Receipt: sr, Ack: ack}
	// The buyer's hold stays until the coordinator's settlement deadline (the
	// ticket's lifetime plus a long grace), so a node whose path to the
	// coordinator is down for a while (relay failover, coordinator restart)
	// must keep trying rather than give up on work it already delivered.
	deadline := time.Unix(0, t.ExpiresAt).Add(settleRetryWindow)
	err := s.retryUntil(deadline, func(ctx context.Context) (bool, error) {
		var resp protocol.SettleResponse
		err := s.call(ctx, protocol.TypeSettle, req, protocol.TypeSettled, &resp)
		if err == nil {
			s.served.Add(1)
			if resp.Status == "final" {
				s.earned.Add(resp.CostMicros - resp.FeeMicros)
			}
			s.log.Info("job settled", "job", t.JobID, "status", resp.Status, "earned", resp.CostMicros-resp.FeeMicros)
			return true, nil
		}
		if code := protocol.CodeOf(err); code != "" && code != protocol.CodeInternal {
			s.log.Warn("settlement refused", "job", t.JobID, "err", err)
			return true, nil
		}
		return false, err
	})
	if err != nil && s.ctx.Err() == nil {
		s.log.Error("settlement abandoned after retries", "job", t.JobID, "err", err)
	}
}

// abandon tells the coordinator that a ticketed job will never be delivered,
// so the buyer's hold and this node's slot are released now rather than at
// the ticket's deadline.
func (s *Service) abandon(t protocol.Ticket, reason string) {
	s.sendAbandon(protocol.AbandonRequest{Ticket: t, Reason: reason})
}

// abandonNoFault releases a job the buyer's own request made impossible.
func (s *Service) abandonNoFault(t protocol.Ticket, reason string) {
	s.sendAbandon(protocol.AbandonRequest{Ticket: t, Reason: reason, NoFault: true})
}

func (s *Service) sendAbandon(req protocol.AbandonRequest) {
	t := req.Ticket
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()
	err := s.call(ctx, protocol.TypeAbandon,
		req, protocol.TypeOK, nil)
	if err != nil && s.ctx.Err() == nil {
		s.log.Warn("could not abandon job; its hold is released at the ticket's deadline", "job", t.JobID, "err", err)
	}
}

// call is p2p.Call to the coordinator, reporting transport failures.
func (s *Service) call(ctx context.Context, reqType string, req any, respType string, resp any) error {
	err := p2p.Call(ctx, s.h, s.cfg.Coordinator.ID, protocol.CtrlProtocol, reqType, req, respType, resp)
	if err != nil && protocol.CodeOf(err) == "" && ctx.Err() == nil && s.cfg.OnCoordinatorUnreachable != nil {
		s.cfg.OnCoordinatorUnreachable()
	}
	return err
}

// reportStarted reports a started job, retrying transient failures; cancel
// aborts the job when the coordinator says it was already released.
func (s *Service) reportStarted(t protocol.Ticket, cancel context.CancelFunc) {
	// The coordinator accepts a start until the ticket expires plus a grace;
	// keep trying that long, so a coordinator path that is failing over (relay
	// down, coordinator restarting) does not make a running job look unused.
	deadline := time.Unix(0, t.ExpiresAt).Add(startReportGrace)
	s.retryUntil(deadline, func(ctx context.Context) (bool, error) {
		err := s.call(ctx, protocol.TypeStarted,
			protocol.StartedRequest{Ticket: t}, protocol.TypeOK, nil)
		switch code := protocol.CodeOf(err); {
		case err == nil:
			return true, nil
		case code == protocol.CodeConflict:
			s.log.Warn("job was released before it started; aborting it", "job", t.JobID)
			cancel()
			return true, nil
		case code != "" && code != protocol.CodeInternal:
			s.log.Warn("start report refused", "job", t.JobID, "err", err)
			return true, nil
		}
		return false, err
	})
}

// retryUntil calls fn with exponential backoff (1s doubling, capped at 30s)
// until it reports done or deadline passes.
func (s *Service) retryUntil(deadline time.Time, fn func(ctx context.Context) (done bool, err error)) error {
	backoff := time.Second
	for {
		ctx, cancel := context.WithTimeout(s.ctx, 20*time.Second)
		done, err := fn(ctx)
		cancel()
		if done {
			return nil
		}
		if time.Now().Add(backoff).After(deadline) {
			return err
		}
		select {
		case <-s.ctx.Done():
			return s.ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func (s *Service) markSeen(jobID string, expires int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.seen[jobID]; dup {
		return false
	}
	s.seen[jobID] = expires
	return true
}

// pruneSeen forgets expired tickets; checkTicket rejects those anyway.
func (s *Service) pruneSeen() {
	now := s.cfg.Now().UnixNano()
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, exp := range s.seen {
		if exp < now {
			delete(s.seen, id)
		}
	}
}

func fail(st network.Stream, err error) { _ = protocol.SendError(st, err) }

// clip shortens s to at most n bytes for error messages.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
