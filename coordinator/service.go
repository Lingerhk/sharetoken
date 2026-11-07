package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	lproto "github.com/libp2p/go-libp2p/core/protocol"

	"sharetoken/evidence"
	"sharetoken/p2p"
	"sharetoken/protocol"
)

const ctrlTimeout = 30 * time.Second

// Service runs an Engine on a libp2p host: it gathers offers from GossipSub,
// answers control-plane requests, and drives time-based transitions.
type Service struct {
	Engine *Engine

	h      host.Host
	topic  *pubsub.Topic
	sub    *pubsub.Subscription
	cancel context.CancelFunc
	log    *slog.Logger
}

// NewService starts the coordinator on h, signing tickets with keys.
func NewService(ctx context.Context, h host.Host, keys *p2p.Keys, cfg Config) (*Service, error) {
	if cfg.Latency == nil {
		cfg.Latency = func(node string) time.Duration {
			pid, err := peer.Decode(node)
			if err != nil {
				return 0
			}
			return h.Peerstore().LatencyEWMA(pid)
		}
	}
	eng, err := NewEngine(keys.ID(), keys.Signer, cfg)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &Service{Engine: eng, h: h, cancel: cancel, log: slog.Default().With("component", "coordinator")}

	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		cancel()
		eng.Close()
		return nil, err
	}
	// Validate before an offer is delivered or relayed: it must parse, be
	// published by the node it describes, and carry that node's signature.
	err = ps.RegisterTopicValidator(protocol.OffersTopic,
		func(_ context.Context, _ peer.ID, m *pubsub.Message) pubsub.ValidationResult {
			var o protocol.Offer
			if json.Unmarshal(m.Data, &o) != nil || o.Node != m.GetFrom().String() || o.VerifySignature() != nil {
				return pubsub.ValidationReject
			}
			if o.Verify(eng.Now(), eng.MaxOfferTTL()) != nil {
				return pubsub.ValidationIgnore // stale or overlong, not malicious
			}
			m.ValidatorData = &o
			return pubsub.ValidationAccept
		})
	if err != nil {
		cancel()
		eng.Close()
		return nil, err
	}
	if s.topic, err = ps.Join(protocol.OffersTopic); err != nil {
		cancel()
		eng.Close()
		return nil, err
	}
	if s.sub, err = s.topic.Subscribe(); err != nil {
		cancel()
		eng.Close()
		return nil, err
	}
	h.SetStreamHandler(lproto.ID(protocol.CtrlProtocol), s.handleCtrl)
	go s.offerLoop(ctx)
	go s.tickLoop(ctx)
	return s, nil
}

// Close stops the service (the host and a caller-supplied database are owned
// by the caller).
func (s *Service) Close() {
	s.h.RemoveStreamHandler(lproto.ID(protocol.CtrlProtocol))
	s.cancel()
	s.sub.Cancel()
	_ = s.topic.Close()
	s.Engine.Close()
}

func (s *Service) offerLoop(ctx context.Context) {
	for {
		m, err := s.sub.Next(ctx)
		if err != nil {
			return
		}
		o, ok := m.ValidatorData.(*protocol.Offer)
		if !ok {
			continue
		}
		if err := s.Engine.UpsertOffer(o); err != nil {
			s.log.Debug("offer ignored", "node", o.Node, "err", err)
		}
	}
}

func (s *Service) tickLoop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.Engine.Tick(now)
		}
	}
}

func (s *Service) handleCtrl(st network.Stream) {
	defer st.Close()
	_ = st.SetDeadline(time.Now().Add(ctrlTimeout))
	remote := st.Conn().RemotePeer().String()
	env, err := protocol.Recv(st)
	if err != nil {
		_ = st.Reset()
		return
	}

	var (
		resp     any
		respType string
		herr     error
	)
	switch env.Type {
	case protocol.TypeTicketRequest:
		var req protocol.TicketRequest
		if herr = decode(env, &req); herr == nil {
			var tr *protocol.TicketResponse
			if tr, herr = s.Engine.IssueTicket(remote, req); herr == nil {
				resp, respType = tr, protocol.TypeTicket
				s.log.Info("ticket issued", "job", tr.Ticket.JobID, "model", req.Model, "node", tr.Ticket.Node, "buyer", remote)
			}
		}
	case protocol.TypeSettle:
		var req protocol.SettleRequest
		if herr = decode(env, &req); herr == nil {
			var sr *protocol.SettleResponse
			if sr, herr = s.Engine.Settle(remote, req); herr == nil {
				resp, respType = sr, protocol.TypeSettled
				s.log.Info("job settled", "job", req.Ticket.JobID, "status", sr.Status, "cost", sr.CostMicros, "fee", sr.FeeMicros)
			}
		}
	case protocol.TypeModels:
		resp, respType = protocol.ModelsResponse{Models: s.Engine.Models()}, protocol.TypeModels
	case protocol.TypeBalance:
		var b protocol.BalanceResponse
		if b, herr = s.Engine.Balance(remote); herr == nil {
			resp, respType = b, protocol.TypeBalance
		}
	case protocol.TypeDispute:
		var req protocol.DisputeRequest
		if herr = decode(env, &req); herr == nil {
			if herr = s.Engine.Dispute(remote, req); herr == nil {
				respType = protocol.TypeOK
				s.log.Warn("job disputed", "job", req.JobID, "buyer", remote, "reason", req.Reason)
			}
		}
	case protocol.TypeEvidence:
		s.handleEvidence(st, remote, env)
		return
	default:
		herr = protocol.Errorf(protocol.CodeBadRequest, "unknown request type %q", env.Type)
	}
	if herr != nil {
		s.sendError(st, env.Type, herr)
		return
	}
	_ = protocol.Send(st, respType, resp)
}

// sendError reports err to the peer; errors without a protocol code are
// internal, logged here and sent as a bare "internal error".
func (s *Service) sendError(st network.Stream, what string, err error) {
	if protocol.CodeOf(err) == "" {
		s.log.Error("request failed", "type", what, "err", err)
	}
	_ = protocol.SendError(st, err)
}

const evidenceTimeout = 5 * time.Minute

// handleEvidence judges a dispute on its evidence: the header frame, then the
// sealed response chunks, streamed through the judge without buffering them.
// Plaintext exists only inside the judge while it runs.
func (s *Service) handleEvidence(st network.Stream, remote string, env *protocol.Envelope) {
	_ = st.SetDeadline(time.Now().Add(evidenceTimeout))
	var ev protocol.Evidence
	if err := decode(env, &ev); err != nil {
		s.sendError(st, env.Type, err)
		return
	}
	if ev.Chunks > protocol.MaxEvidenceChunks {
		s.sendError(st, env.Type, protocol.Errorf(protocol.CodeBadRequest, "too many chunks"))
		return
	}
	t, settledReceipt, err := s.Engine.EvidenceTarget(remote, ev.JobID)
	if err != nil {
		s.sendError(st, env.Type, err)
		return
	}
	judge, err := evidence.New(t, settledReceipt, ev.Receipt, &ev)
	if errors.Is(err, evidence.ErrNoReceipt) {
		// Nothing was signed yet, so there is nothing to judge: record the
		// dispute so that a later settlement goes to arbitration.
		if err := s.Engine.Dispute(remote, protocol.DisputeRequest{JobID: ev.JobID, Reason: ev.Reason}); err != nil {
			s.sendError(st, env.Type, err)
			return
		}
		st2, _ := s.Engine.JobStatus(ev.JobID)
		_ = protocol.Send(st, protocol.TypeVerdict, protocol.Verdict{Outcome: "recorded", Status: string(st2), Detail: "no receipt yet; the dispute applies if the node settles"})
		return
	}
	if err != nil {
		s.sendError(st, env.Type, err)
		return
	}
	for i := uint64(0); i < ev.Chunks; i++ {
		cenv, err := protocol.Recv(st)
		if err != nil {
			_ = st.Reset()
			return
		}
		var ch protocol.Chunk
		if err := cenv.Decode(protocol.TypeChunk, &ch); err != nil {
			s.sendError(st, env.Type, protocol.Errorf(protocol.CodeBadRequest, "malformed evidence chunk"))
			return
		}
		if err := judge.Add(ch.Seq, ch.Sealed); err != nil {
			s.sendError(st, env.Type, protocol.Errorf(protocol.CodeBadRequest, "%v", err))
			return
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), evidenceTimeout)
	defer cancel()
	v := judge.Finish(ctx, s.Engine.cfg.Counter)
	verdict, err := s.Engine.ApplyVerdict(remote, ev.JobID, ev.Reason, judge.Receipt(), v)
	if err != nil {
		s.sendError(st, env.Type, err)
		return
	}
	s.log.Warn("dispute judged on evidence", "job", ev.JobID, "outcome", verdict.Outcome, "status", verdict.Status, "detail", verdict.Detail)
	_ = protocol.Send(st, protocol.TypeVerdict, verdict)
}

func decode(env *protocol.Envelope, v any) error {
	if err := env.Decode(env.Type, v); err != nil {
		return protocol.Errorf(protocol.CodeBadRequest, "malformed %s request", env.Type)
	}
	return nil
}
