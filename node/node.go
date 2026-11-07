// Package node is the seller side: it advertises signed offers over GossipSub
// and serves ticketed jobs on a direct libp2p stream.
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
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
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
	requestTimeout = 30 * time.Second
	ackTimeout     = 30 * time.Second
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
	Now            func() time.Time
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

// Service is a running node.
type Service struct {
	h      host.Host
	keys   *p2p.Keys
	cfg    Config
	id     identity
	models map[string]ModelConfig
	topic  *pubsub.Topic
	sem    chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	log    *slog.Logger

	mu   sync.Mutex
	seen map[string]int64 // job ID -> ticket expiry; rejects ticket replay
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
		ctx:    ctx,
		cancel: cancel,
		log:    slog.Default().With("component", "node"),
		seen:   make(map[string]int64),
	}
	h.Peerstore().AddAddrs(cfg.Coordinator.ID, cfg.Coordinator.Addrs, peerstore.PermanentAddrTTL)

	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		cancel()
		return nil, err
	}
	if s.topic, err = ps.Join(protocol.OffersTopic); err != nil {
		cancel()
		return nil, err
	}
	h.SetStreamHandler(lproto.ID(protocol.InferProtocol), s.handleInfer)
	go s.offerLoop()
	return s, nil
}

// Close stops the node (the host is owned by the caller).
func (s *Service) Close() {
	s.h.RemoveStreamHandler(lproto.ID(protocol.InferProtocol))
	s.cancel()
	_ = s.topic.Close()
}

func (s *Service) offerLoop() {
	for i := 0; ; i++ {
		if s.h.Network().Connectedness(s.cfg.Coordinator.ID) != network.Connected {
			cctx, cancel := context.WithTimeout(s.ctx, 10*time.Second)
			if err := s.h.Connect(cctx, s.cfg.Coordinator); err != nil && s.ctx.Err() == nil {
				s.log.Warn("cannot reach coordinator", "err", err)
			}
			cancel()
		}
		if err := s.publishOffer(); err != nil && s.ctx.Err() == nil {
			s.log.Warn("publishing offer failed", "err", err)
		}
		s.pruneSeen()
		wait := s.cfg.OfferInterval
		if i < 3 && wait > time.Second {
			wait = time.Second // announce quickly while gossip meshes form
		}
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (s *Service) publishOffer() error {
	now := s.cfg.Now()
	o := &protocol.Offer{
		Node:           s.id.self,
		Addrs:          p2p.AddrStrings(s.h),
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
	data, err := json.Marshal(o)
	if err != nil {
		return err
	}
	return s.topic.Publish(s.ctx, data)
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
		return
	}

	// The plaintext request exists only in memory for the life of this job; it
	// is never logged or written anywhere.
	plain, err := s.keys.Box.Open(req.Sealed, protocol.RequestAAD(t))
	if err != nil {
		fail(st, protocol.Errorf(protocol.CodeBadRequest, "cannot open sealed request"))
		return
	}
	body, estPrompt, err := UpstreamRequest(t, plain, mc.UpstreamModel)
	if err != nil {
		fail(st, err)
		return
	}

	_ = st.SetDeadline(time.Now().Add(s.cfg.JobTimeout))
	ctx, cancel := context.WithTimeout(s.ctx, s.cfg.JobTimeout)
	defer cancel()

	hasher := receipt.NewStreamHasher()
	var (
		seq   uint64
		usage *protocol.Usage
		text  strings.Builder
	)
	err = s.cfg.Upstream.Stream(ctx, body, func(payload []byte) error {
		if info, perr := protocol.ParseChunk(payload); perr == nil {
			text.WriteString(info.Text())
			if info.Usage != nil {
				usage = info.Usage
			}
		}
		sealed, err := box.Seal(req.BuyerBoxPub, payload, protocol.ChunkAAD(t, seq))
		if err != nil {
			return err
		}
		hasher.WriteChunk(sealed)
		if err := protocol.Send(st, protocol.TypeChunk, protocol.Chunk{Seq: seq, Sealed: sealed}); err != nil {
			return err
		}
		seq++
		return nil
	})
	if err == nil && seq == 0 {
		err = errors.New("upstream returned an empty stream")
	}
	if err != nil {
		// No receipt, no settlement: a failed job is never billed.
		s.log.Warn("job failed", "job", t.JobID, "err", err)
		fail(st, protocol.Errorf(protocol.CodeUpstream, "model server error"))
		return
	}

	pt, ct := estPrompt, protocol.EstimateTokens(text.String())
	if usage != nil {
		pt, ct = usage.PromptTokens, usage.CompletionTokens
	}
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	sr, err := receipt.Sign(s.keys.Signer, receipt.Receipt{
		JobID:            t.JobID,
		ModelID:          t.Model,
		NodeID:           s.id.receiptKey,
		RequestHash:      receipt.Hash(req.Sealed),
		ResponseHash:     hasher.Sum(),
		PromptTokens:     pt,
		CompletionTokens: ct,
		UnixNano:         s.cfg.Now().UnixNano(),
		Nonce:            nonce,
	})
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

// settle submits the receipt, retrying transient failures. A coded refusal
// from the coordinator is final.
func (s *Service) settle(t protocol.Ticket, sr receipt.SignedReceipt, ack []byte) {
	req := protocol.SettleRequest{Ticket: t, Receipt: sr, Ack: ack}
	backoff := time.Second
	for attempt := 0; attempt < 4; attempt++ {
		var resp protocol.SettleResponse
		err := p2p.Call(s.ctx, s.h, s.cfg.Coordinator.ID, protocol.CtrlProtocol, protocol.TypeSettle, req, protocol.TypeSettled, &resp)
		if err == nil {
			s.log.Info("job settled", "job", t.JobID, "status", resp.Status, "earned", resp.CostMicros-resp.FeeMicros)
			return
		}
		if code := protocol.CodeOf(err); code != "" && code != protocol.CodeInternal {
			s.log.Warn("settlement refused", "job", t.JobID, "err", err)
			return
		}
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
	}
	s.log.Error("settlement abandoned after retries", "job", t.JobID)
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
