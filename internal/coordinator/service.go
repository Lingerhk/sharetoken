package coordinator

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	lproto "github.com/libp2p/go-libp2p/core/protocol"
	ma "github.com/multiformats/go-multiaddr"

	"sharetoken/internal/evidence"
	"sharetoken/internal/p2p"
	"sharetoken/internal/protocol"
)

const ctrlTimeout = 30 * time.Second

// Service runs an Engine on a libp2p host: it answers control-plane requests
// (offers announced by nodes, tickets, settlements, disputes) and drives
// time-based transitions.
//
// Users never connect to the coordinator directly. It holds a reservation on
// every relay (Config.Relays plus the enabled relays in the database), and
// users reach it at <relay>/p2p-circuit/p2p/<coordinator>. Relay health is
// written back to the database, where the portal reads it.
type Service struct {
	Engine *Engine

	h       host.Host
	relays  *p2p.Reserver
	static  []string
	cancel  context.CancelFunc
	log     *slog.Logger
	relayMu sync.Mutex

	relayDirty chan struct{}
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
	if cfg.WarmUp == 0 {
		cfg.WarmUp = 30 * time.Second
	}
	if cfg.ConnectedSince == nil {
		cfg.ConnectedSince = trackConnections(h)
	}
	if cfg.NodeOnline == nil {
		cfg.NodeOnline = func(node string) bool {
			pid, err := peer.Decode(node)
			return err == nil && h.Network().Connectedness(pid) == network.Connected
		}
	}
	eng, err := NewEngine(keys.ID(), keys.Signer, cfg)
	if err != nil {
		return nil, err
	}
	if err := eng.RecordFeeRate(); err != nil {
		eng.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &Service{Engine: eng, h: h, static: cfg.Relays, cancel: cancel, log: slog.Default().With("component", "coordinator")}
	s.relayDirty = make(chan struct{}, 1)
	s.relays = p2p.NewReserver(h, func() {
		select { // record relay health promptly; the portal hands it to users
		case s.relayDirty <- struct{}{}:
		default:
		}
	})
	h.SetStreamHandler(lproto.ID(protocol.CtrlProtocol), s.handleCtrl)
	if err := s.syncRelays(); err != nil {
		s.log.Warn("relay list", "err", err)
	}
	go s.relays.Run(ctx)
	go s.relayLoop(ctx, cfg.RelaySync)
	go s.tickLoop(ctx)
	return s, nil
}

// Close stops the service (the host and a caller-supplied database are owned
// by the caller).
func (s *Service) Close() {
	s.h.RemoveStreamHandler(lproto.ID(protocol.CtrlProtocol))
	s.cancel()
	s.Engine.Close()
}

// Relays reports the coordinator's relay reservations.
func (s *Service) Relays() []p2p.RelayStatus { return s.relays.Status() }

// RelayAddrs returns the coordinator's circuit addresses (without the
// trailing /p2p/<coordinator>), the form a peerstore holds.
func (s *Service) RelayAddrs() []ma.Multiaddr { return s.relays.CircuitAddrs() }

// CircuitAddrs returns the coordinator's addresses through its relays.
func (s *Service) CircuitAddrs() []string {
	var out []string
	for _, a := range s.relays.CircuitAddrs() {
		out = append(out, a.String()+"/p2p/"+s.h.ID().String())
	}
	return out
}

// syncRelays reserves on the static relays plus the enabled relays in the
// database.
func (s *Service) syncRelays() error {
	s.relayMu.Lock()
	defer s.relayMu.Unlock()
	addrs := slices.Clone(s.static)
	rs, err := s.Engine.registeredRelays()
	for _, r := range rs {
		if !slices.Contains(addrs, r.Addr) {
			addrs = append(addrs, r.Addr)
		}
	}
	return errors.Join(err, s.relays.SetRelays(addrs))
}

// relayLoop picks up relays added or removed in the database and records
// which ones the coordinator is reachable through.
func (s *Service) relayLoop(ctx context.Context, every time.Duration) {
	if every <= 0 {
		every = 15 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.syncRelays(); err != nil {
				s.log.Warn("relay list", "err", err)
			}
		case <-s.relayDirty:
		}
		if err := s.Engine.recordRelayStatus(s.relays.Status()); err != nil {
			s.log.Warn("recording relay status failed", "err", err)
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
	env, err := protocol.RecvStream(st, protocol.MaxFrameSize)
	if err != nil {
		_ = st.Reset()
		return
	}
	if env.Ver == "" {
		s.sendError(st, env.Type, protocol.Errorf(protocol.CodeBadRequest, "the request does not say the client's version"))
		return
	}
	// Only new business is refused: an old client can still settle, report and
	// dispute the jobs it already has.
	if env.Type == protocol.TypeAnnounce || env.Type == protocol.TypeTicketRequest {
		if err := s.Engine.CheckClient(env.Ver); err != nil {
			s.sendError(st, env.Type, err)
			return
		}
	}

	var (
		resp     any
		respType string
		herr     error
	)
	switch env.Type {
	case protocol.TypeAnnounce:
		var o protocol.Offer
		if herr = decode(env, &o); herr == nil {
			if o.Node != remote {
				herr = protocol.Errorf(protocol.CodeForbidden, "a node may only announce its own offer")
			} else if herr = s.Engine.UpsertOffer(&o); herr == nil {
				respType = protocol.TypeOK
			}
		}
	case protocol.TypeWithdraw:
		var req protocol.WithdrawRequest
		if herr = decode(env, &req); herr == nil {
			if herr = s.Engine.WithdrawOffer(remote, req.IssuedAt); herr == nil {
				respType = protocol.TypeOK
			}
		}
	case protocol.TypeAbandon:
		var req protocol.AbandonRequest
		if herr = decode(env, &req); herr == nil {
			if herr = s.Engine.Abandon(remote, req); herr == nil {
				respType = protocol.TypeOK
				s.log.Info("job abandoned by its node", "job", req.Ticket.JobID, "reason", req.Reason)
			}
		}
	case protocol.TypeStarted:
		var req protocol.StartedRequest
		if herr = decode(env, &req); herr == nil {
			if herr = s.Engine.MarkStarted(remote, req.Ticket); herr == nil {
				respType = protocol.TypeOK
			}
		}
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
		var ms []protocol.ModelSummary
		if ms, herr = s.Engine.ModelsFor(remote); herr == nil {
			resp, respType = protocol.ModelsResponse{Models: ms}, protocol.TypeModels
		}
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
	sendErr := protocol.Send(st, respType, resp)
	if tr, ok := resp.(*protocol.TicketResponse); ok && sendErr != nil {
		// The buyer never got the ticket (its connection dropped, it switched
		// relays): release it now instead of leaving the hold and the node's
		// slot taken, and the ticket counted as unused, until it expires.
		if rerr := s.Engine.ReleaseUndelivered(tr.Ticket.JobID); rerr != nil {
			s.log.Warn("could not release an undelivered ticket", "job", tr.Ticket.JobID, "err", rerr)
		} else {
			s.log.Info("ticket reply undelivered; released", "job", tr.Ticket.JobID, "err", sendErr)
		}
	}
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
	sealedReq, err := protocol.ReadSealedRequest(func(max int) (*protocol.Frame, error) { return protocol.ReadStreamFrame(st, max) }, ev.RequestBytes, nil)
	if err != nil {
		if protocol.CodeOf(err) != "" {
			s.sendError(st, env.Type, err)
		} else {
			_ = st.Reset()
		}
		return
	}
	judge, err := evidence.New(t, settledReceipt, ev.Receipt, &ev, sealedReq)
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
		f, err := protocol.ReadStreamFrame(st, protocol.MaxFrameSize)
		if err != nil {
			_ = st.Reset()
			return
		}
		if f.Env != nil || f.Kind != protocol.BinData {
			s.sendError(st, env.Type, protocol.Errorf(protocol.CodeBadRequest, "malformed evidence frame"))
			return
		}
		seq, sealed, err := protocol.DecodeData(f.Payload)
		if err != nil {
			s.sendError(st, env.Type, protocol.Errorf(protocol.CodeBadRequest, "malformed evidence frame"))
			return
		}
		if err := judge.Add(seq, sealed); err != nil {
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

// trackConnections records since when each peer has been continuously
// connected to h, for Config.ConnectedSince.
func trackConnections(h host.Host) func(peer string) time.Time {
	var mu sync.Mutex
	since := make(map[peer.ID]time.Time)
	h.Network().Notify(&network.NotifyBundle{
		ConnectedF: func(n network.Network, c network.Conn) {
			mu.Lock()
			defer mu.Unlock()
			if _, ok := since[c.RemotePeer()]; !ok {
				since[c.RemotePeer()] = time.Now()
			}
		},
		DisconnectedF: func(n network.Network, c network.Conn) {
			if n.Connectedness(c.RemotePeer()) == network.Connected {
				return // another connection remains
			}
			mu.Lock()
			defer mu.Unlock()
			delete(since, c.RemotePeer())
		},
	})
	return func(p string) time.Time {
		id, err := peer.Decode(p)
		if err != nil {
			return time.Time{}
		}
		mu.Lock()
		defer mu.Unlock()
		if t, ok := since[id]; ok {
			return t
		}
		if h.Network().Connectedness(id) == network.Connected {
			now := time.Now() // connected before tracking began: count from now on
			since[id] = now
			return now
		}
		return time.Time{}
	}
}
