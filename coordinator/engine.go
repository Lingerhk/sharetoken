// Package coordinator matches buyers to nodes, issues signed job tickets,
// settles signed receipts against a credit ledger, judges disputes on their
// evidence and keeps node track records, including audit results.
//
// It is control plane only. Prompts and responses travel directly between
// buyer and node (or through a relay that sees only libp2p ciphertext) and are
// additionally sealed to the node's box key, so the coordinator never holds
// request or response content, with one deliberate exception: a buyer may
// disclose ONE job's key as dispute evidence (package evidence).
//
// All durable state (ledger, jobs, node records, audits) lives in SQLite and
// every state change is one transaction, so a crash or restart never loses a
// hold or pays twice. Offers are the only in-memory state; nodes re-gossip them.
package coordinator

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	mrand "math/rand/v2"
	"sort"
	"sync"
	"time"

	"sharetoken/attest"
	"sharetoken/evidence"
	"sharetoken/internal/canon"
	"sharetoken/internal/sqlite"
	"sharetoken/ledger"
	"sharetoken/protocol"
	"sharetoken/receipt"
	"sharetoken/tokens"
)

// Status is a job's settlement state.
type Status string

// Job lifecycle:
//
//	held --ack--> final
//	held --no ack--> pending --dispute window--> final
//	                 pending --dispute--> disputed --arbitration--> final | refunded
//	held | pending | disputed --evidence judged--> final (dismissed) | refunded (upheld) | disputed (unverifiable)
//	held --no settlement before deadline--> released
const (
	StatusHeld     Status = "held"
	StatusPending  Status = "pending"
	StatusDisputed Status = "disputed"
	StatusFinal    Status = "final"
	StatusReleased Status = "released"
	StatusRefunded Status = "refunded"
)

// Attestation lets the coordinator verify node quotes, so it can serve
// tickets that require a TEE. Buyers still verify quotes themselves.
type Attestation struct {
	Verifier attest.Verifier
	Policy   *attest.Policy
}

// Config tunes the engine. Zero values get sensible defaults.
type Config struct {
	DB              *sql.DB       // nil: a private in-memory database (tests, demos)
	FeeBps          int64         // platform fee in basis points of each settled job
	TicketTTL       time.Duration // time a node has to start a ticketed job
	SettleGrace     time.Duration // extra time for settlement to arrive before the hold is released
	DisputeWindow   time.Duration // unacknowledged settlements become final after this
	MaxOfferTTL     time.Duration // offers claiming a longer validity are rejected
	JobRetention    time.Duration // finished jobs are deleted after this; the journal is kept
	SuspendFor      time.Duration // matching excludes a node this long after repeated failed audits
	MinBudgetMicros uint64
	// DevCreditMicros credits each new peer once on its first ticket request.
	// For demos and testnets only: anyone can mint peer IDs.
	DevCreditMicros uint64
	Counter         tokens.Counter // judges dispute evidence; nil = heuristic
	Attestation     *Attestation   // optional
	Latency         func(node string) time.Duration
	Now             func() time.Time
	Log             *slog.Logger
}

func (c *Config) setDefaults() {
	if c.TicketTTL == 0 {
		c.TicketTTL = 2 * time.Minute
	}
	if c.SettleGrace == 0 {
		c.SettleGrace = 15 * time.Minute
	}
	if c.DisputeWindow == 0 {
		c.DisputeWindow = 24 * time.Hour
	}
	if c.MaxOfferTTL == 0 {
		c.MaxOfferTTL = 5 * time.Minute
	}
	if c.JobRetention == 0 {
		c.JobRetention = 30 * 24 * time.Hour
	}
	if c.SuspendFor == 0 {
		c.SuspendFor = 24 * time.Hour
	}
	if c.MinBudgetMicros == 0 {
		c.MinBudgetMicros = 1
	}
	if c.Counter == nil {
		c.Counter = tokens.Heuristic{}
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Log == nil {
		c.Log = slog.Default().With("component", "coordinator")
	}
}

// Reputation runs 0..1000. Losing a dispute or an audit costs far more than a
// success earns, so cheating does not pay once checks are frequent enough.
const (
	startReputation    = 500
	maxReputation      = 1000
	repPerSuccess      = 1
	repPerFailure      = 5
	repPerLostDispute  = 50
	repPerAuditPass    = 5
	repPerAuditFail    = 100
	auditFailsSuspend  = 2 // consecutive failed audits that suspend a node
	maxDisputeReasonsz = 512
)

type offerEntry struct {
	offer protocol.Offer
	tee   string // allow-list label of the verified TEE image; "" if not attested
}

// Engine is the coordinator's state machine. It is safe for concurrent use.
type Engine struct {
	self  string
	priv  ed25519.PrivateKey
	cfg   Config
	db    *sql.DB
	ownDB bool
	log   *slog.Logger

	mu     sync.Mutex // serializes transactions; guards offers
	offers map[string]*offerEntry

	teeMu    sync.Mutex
	teeCache map[[32]byte]string
}

// NewEngine opens the engine's state in cfg.DB, checks that the books replay
// from the journal, and signs tickets as self with priv.
func NewEngine(self string, priv ed25519.PrivateKey, cfg Config) (*Engine, error) {
	cfg.setDefaults()
	db, own := cfg.DB, false
	if db == nil {
		var err error
		if db, err = sqlite.Open(sqlite.Memory); err != nil {
			return nil, err
		}
		own = true
	}
	e := &Engine{
		self: self, priv: priv, cfg: cfg, db: db, ownDB: own, log: cfg.Log,
		offers: make(map[string]*offerEntry), teeCache: make(map[[32]byte]string),
	}
	if err := sqlite.Migrate(db, ledger.Schema, schema); err != nil {
		e.Close()
		return nil, err
	}
	err := e.update(func(tx *sql.Tx, led *ledger.Tx) error {
		// A database holds tickets signed by one coordinator key; refuse to
		// mix them with another's.
		var owner string
		switch err := tx.QueryRow(`SELECT value FROM meta WHERE key = 'coordinator'`).Scan(&owner); {
		case errors.Is(err, sql.ErrNoRows):
			if _, err := tx.Exec(`INSERT INTO meta (key, value) VALUES ('coordinator', ?)`, self); err != nil {
				return err
			}
		case err != nil:
			return err
		case owner != self:
			return fmt.Errorf("coordinator: database belongs to coordinator %s, not %s", owner, self)
		}
		return led.Reconcile()
	})
	if err != nil {
		e.Close()
		return nil, err
	}
	return e, nil
}

// Close releases the database if the engine opened it.
func (e *Engine) Close() {
	if e.ownDB {
		_ = e.db.Close()
	}
}

// Now is the engine's clock.
func (e *Engine) Now() time.Time { return e.cfg.Now() }

// MaxOfferTTL is the longest offer validity the engine accepts.
func (e *Engine) MaxOfferTTL() time.Duration { return e.cfg.MaxOfferTTL }

// update runs fn in one write transaction; any error rolls everything back.
func (e *Engine) update(fn func(tx *sql.Tx, led *ledger.Tx) error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.updateLocked(fn)
}

func (e *Engine) updateLocked(fn func(tx *sql.Tx, led *ledger.Tx) error) error {
	tx, err := e.db.Begin()
	if err != nil {
		return fmt.Errorf("coordinator: begin: %w", err)
	}
	defer tx.Rollback()
	if err := fn(tx, ledger.On(tx, e.cfg.Now())); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("coordinator: commit: %w", err)
	}
	return nil
}

// UpsertOffer records a node's offer if it is valid and newer than the one
// already held (old offers cannot be replayed). Any model ID is accepted: what
// a node serves is checked by audits and attestation, not by a list.
func (e *Engine) UpsertOffer(o *protocol.Offer) error {
	if err := o.Verify(e.cfg.Now(), e.cfg.MaxOfferTTL); err != nil {
		return protocol.Errorf(protocol.CodeBadRequest, "%v", err)
	}
	tee := e.verifyQuote(o)
	e.mu.Lock()
	defer e.mu.Unlock()
	if cur, ok := e.offers[o.Node]; ok && cur.offer.IssuedAt >= o.IssuedAt {
		return nil
	}
	e.offers[o.Node] = &offerEntry{offer: *o, tee: tee}
	return nil
}

// verifyQuote returns the allow-list label of the offer's attested image, or
// "" when there is no quote, no attestation config, or verification fails.
// Results are cached: nodes re-gossip the same quote every few seconds.
func (e *Engine) verifyQuote(o *protocol.Offer) string {
	a := e.cfg.Attestation
	if o.Quote == nil || a == nil || a.Verifier == nil || a.Policy == nil {
		return ""
	}
	key := sha256.Sum256(canon.New("sharetoken/quote-cache/v1").String(o.Quote.Backend).
		Bytes(o.Quote.Evidence).Bytes(o.ReceiptKey).Bytes(o.BoxPub).Sum())
	e.teeMu.Lock()
	label, ok := e.teeCache[key]
	e.teeMu.Unlock()
	if ok {
		return label
	}
	if _, l, err := attest.Verify(a.Verifier, a.Policy, o.Quote, o.ReceiptKey, o.BoxPub); err != nil {
		e.log.Warn("node attestation rejected", "node", o.Node, "backend", o.Quote.Backend, "err", err)
	} else {
		label = l
		e.log.Info("node attestation verified", "node", o.Node, "backend", o.Quote.Backend, "image", l)
	}
	e.teeMu.Lock()
	if len(e.teeCache) > 4096 {
		clear(e.teeCache)
	}
	e.teeCache[key] = label
	e.teeMu.Unlock()
	return label
}

// Offers returns the live offers.
func (e *Engine) Offers() []protocol.Offer {
	now := e.cfg.Now().UnixNano()
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]protocol.Offer, 0, len(e.offers))
	for _, oe := range e.offers {
		if oe.offer.ExpiresAt > now {
			out = append(out, oe.offer)
		}
	}
	return out
}

// Models aggregates live offers per model.
func (e *Engine) Models() []protocol.ModelSummary {
	now := e.cfg.Now().UnixNano()
	e.mu.Lock()
	defer e.mu.Unlock()
	agg := make(map[string]*protocol.ModelSummary)
	for _, oe := range e.offers {
		if oe.offer.ExpiresAt <= now {
			continue
		}
		for _, m := range oe.offer.Models {
			s := agg[m.Model]
			if s == nil {
				s = &protocol.ModelSummary{Model: m.Model, MinPriceIn: m.PriceInMicros, MinPriceOut: m.PriceOutMicros}
				agg[m.Model] = s
			}
			s.Nodes++
			if oe.tee != "" {
				s.AttestedNodes++
			}
			s.MinPriceIn = min(s.MinPriceIn, m.PriceInMicros)
			s.MinPriceOut = min(s.MinPriceOut, m.PriceOutMicros)
		}
	}
	out := make([]protocol.ModelSummary, 0, len(agg))
	for _, s := range agg {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}

// Deposit credits a peer's account (operators; payment rails are out of scope).
func (e *Engine) Deposit(peerID string, micros uint64) error {
	if micros == 0 || micros > math.MaxInt64 {
		return ledger.ErrInvalidAmount
	}
	return e.update(func(_ *sql.Tx, led *ledger.Tx) error {
		return led.Deposit(ledger.Account(peerID), int64(micros), "deposit")
	})
}

// Balance reports a peer's account.
func (e *Engine) Balance(peerID string) (protocol.BalanceResponse, error) {
	var out protocol.BalanceResponse
	acct := ledger.Account(peerID)
	err := e.update(func(_ *sql.Tx, led *ledger.Tx) (err error) {
		if out.AvailableMicros, err = led.Balance(acct); err != nil {
			return err
		}
		out.HeldMicros, err = led.Held(acct)
		return err
	})
	return out, err
}

// AccountBalance reads any ledger account (operators, tests).
func (e *Engine) AccountBalance(account string) (b int64, err error) {
	err = e.update(func(_ *sql.Tx, led *ledger.Tx) error { b, err = led.Balance(account); return err })
	return b, err
}

// CheckBooks replays the journal against the balances (operators, tests).
func (e *Engine) CheckBooks() error {
	return e.update(func(_ *sql.Tx, led *ledger.Tx) error { return led.Reconcile() })
}

// JobStatus reports a job's settlement state.
func (e *Engine) JobStatus(jobID string) (Status, bool) {
	var st Status
	err := e.update(func(tx *sql.Tx, _ *ledger.Tx) error {
		j, err := loadJob(tx, jobID)
		if err == nil && j != nil {
			st = j.status
		}
		return err
	})
	return st, err == nil && st != ""
}

type candidate struct {
	offer *protocol.Offer
	price protocol.ModelPrice
	stats *NodeStats
}

// IssueTicket matches buyer to a node for req.Model, reserves req.BudgetMicros
// from the buyer's account and returns a signed ticket plus the node's offer.
func (e *Engine) IssueTicket(buyer string, req protocol.TicketRequest) (*protocol.TicketResponse, error) {
	if req.Model == "" {
		return nil, protocol.Errorf(protocol.CodeBadRequest, "model is required")
	}
	if req.BudgetMicros < e.cfg.MinBudgetMicros || req.BudgetMicros > math.MaxInt64 {
		return nil, protocol.Errorf(protocol.CodeBadRequest, "budget must be between %d and %d micros", e.cfg.MinBudgetMicros, int64(math.MaxInt64))
	}
	now := e.cfg.Now()
	var resp *protocol.TicketResponse
	err := e.update(func(tx *sql.Tx, led *ledger.Tx) error {
		if err := e.grantDevCredit(tx, led, buyer, now); err != nil {
			return err
		}
		busy, err := busyNodes(tx)
		if err != nil {
			return err
		}
		excluded := make(map[string]bool, len(req.Exclude))
		for _, n := range req.Exclude {
			excluded[n] = true
		}
		var cands []candidate
		for node, oe := range e.offers {
			o := &oe.offer
			// A peer may not buy from itself: it would only churn fees and
			// farm reputation.
			if o.ExpiresAt <= now.UnixNano() || node == buyer || excluded[node] ||
				(req.Node != "" && node != req.Node) || (req.RequireTEE && oe.tee == "") {
				continue
			}
			p, ok := o.Price(req.Model)
			if !ok || (req.MaxPriceIn > 0 && p.PriceInMicros > req.MaxPriceIn) || (req.MaxPriceOut > 0 && p.PriceOutMicros > req.MaxPriceOut) {
				continue
			}
			if o.MaxConcurrency > 0 && busy[node] >= o.MaxConcurrency {
				continue
			}
			st, err := loadStats(tx, node)
			if err != nil {
				return err
			}
			if st.SuspendedUntil > now.UnixNano() {
				continue
			}
			cands = append(cands, candidate{offer: o, price: p, stats: st})
		}
		if len(cands) == 0 {
			what := "no node"
			if req.RequireTEE {
				what = "no attested node"
			}
			return protocol.Errorf(protocol.CodeNoOffers, "%s currently offers %q within the price limits", what, req.Model)
		}
		best := e.pick(cands)

		jobID, err := newJobID()
		if err != nil {
			return err
		}
		if err := led.Hold(jobID, ledger.Account(buyer), int64(req.BudgetMicros)); err != nil {
			if errors.Is(err, ledger.ErrInsufficientFunds) {
				return protocol.Errorf(protocol.CodeInsufficientFunds, "balance does not cover the %d micro budget", req.BudgetMicros)
			}
			return err
		}
		t := protocol.Ticket{
			JobID:          jobID,
			Model:          req.Model,
			Node:           best.offer.Node,
			NodeAddrs:      best.offer.Addrs,
			NodeBoxPub:     best.offer.BoxPub,
			NodeReceiptKey: best.offer.ReceiptKey,
			Buyer:          buyer,
			PriceInMicros:  best.price.PriceInMicros,
			PriceOutMicros: best.price.PriceOutMicros,
			ReservedMicros: req.BudgetMicros,
			IssuedAt:       now.UnixNano(),
			ExpiresAt:      now.Add(e.cfg.TicketTTL).UnixNano(),
			Coordinator:    e.self,
		}
		t.Sign(e.priv)
		j := &job{ticket: t, status: StatusHeld, createdAt: now.UnixNano(), releaseAt: now.Add(e.cfg.TicketTTL + e.cfg.SettleGrace).UnixNano()}
		if err := insertJob(tx, j); err != nil {
			return err
		}
		resp = &protocol.TicketResponse{Ticket: t, Offer: *best.offer}
		return nil
	})
	return resp, err
}

func (e *Engine) grantDevCredit(tx *sql.Tx, led *ledger.Tx, peer string, now time.Time) error {
	if e.cfg.DevCreditMicros == 0 || e.cfg.DevCreditMicros > math.MaxInt64 {
		return nil
	}
	// Audit identities are funded by the platform and never get dev credit.
	res, err := tx.Exec(`INSERT INTO peers (peer, dev_credited, created_at) VALUES (?, 1, ?)
		ON CONFLICT (peer) DO UPDATE SET dev_credited = 1 WHERE dev_credited = 0 AND auditor = 0`, peer, now.UnixNano())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	return led.Deposit(ledger.Account(peer), int64(e.cfg.DevCreditMicros), "dev credit")
}

// pick scores candidates: 0.45 price + 0.25 reputation + 0.15 latency +
// 0.15 success rate, each normalised to [0,1], with a little jitter to spread
// load between near-equal nodes.
func (e *Engine) pick(cands []candidate) candidate {
	minPrice := uint64(math.MaxUint64)
	minLat := time.Duration(math.MaxInt64)
	lats := make([]time.Duration, len(cands))
	for i, c := range cands {
		minPrice = min(minPrice, satAdd(c.price.PriceInMicros, c.price.PriceOutMicros))
		if e.cfg.Latency != nil {
			lats[i] = e.cfg.Latency(c.offer.Node)
		}
		if lats[i] > 0 {
			minLat = min(minLat, lats[i])
		}
	}
	best, bestScore := 0, -1.0
	for i, c := range cands {
		priceScore := 1.0
		if p := satAdd(c.price.PriceInMicros, c.price.PriceOutMicros); p > 0 {
			priceScore = float64(minPrice) / float64(p)
		}
		latScore := 0.5 // unknown latency
		if lats[i] > 0 {
			latScore = float64(minLat) / float64(lats[i])
		}
		repScore := float64(c.stats.Reputation) / maxReputation
		succScore := float64(c.stats.Successes+1) / float64(c.stats.Successes+c.stats.Failures+2)
		score := 0.45*priceScore + 0.25*repScore + 0.15*latScore + 0.15*succScore + 0.01*mrand.Float64()
		if score > bestScore {
			best, bestScore = i, score
		}
	}
	return cands[best]
}

// Settle pays out a job against its receipt. With a valid buyer ack it is
// final immediately; without one it waits out the dispute window.
func (e *Engine) Settle(submitter string, req protocol.SettleRequest) (*protocol.SettleResponse, error) {
	now := e.cfg.Now()
	var resp *protocol.SettleResponse
	err := e.update(func(tx *sql.Tx, led *ledger.Tx) error {
		j, err := loadJob(tx, req.Ticket.JobID)
		if err != nil {
			return err
		}
		if j == nil {
			return protocol.Errorf(protocol.CodeNotFound, "unknown job")
		}
		t := &j.ticket
		if !t.Equal(&req.Ticket) {
			return protocol.Errorf(protocol.CodeBadTicket, "ticket does not match the one issued")
		}
		if submitter != t.Node && submitter != t.Buyer {
			return protocol.Errorf(protocol.CodeForbidden, "only the ticketed node or buyer may settle")
		}
		switch j.status {
		case StatusReleased:
			return protocol.Errorf(protocol.CodeConflict, "ticket expired and its hold was released")
		case StatusRefunded:
			return protocol.Errorf(protocol.CodeConflict, "the buyer was refunded after a dispute")
		case StatusFinal, StatusDisputed:
			resp = settled(j)
			return nil
		}
		if err := protocol.CheckReceipt(t, &req.Receipt); err != nil {
			return err
		}
		r := &req.Receipt.Receipt
		ackOK := false
		if len(req.Ack) > 0 {
			if err := verifyAck(t, r, req.Ack); err != nil {
				return err
			}
			ackOK = true
		}

		if j.status == StatusPending {
			// Only an ack for the same receipt can move a pending job forward.
			if ackOK && bytes.Equal(r.Digest(), j.receipt.Receipt.Digest()) {
				if err := e.finalize(tx, led, j, now); err != nil {
					return err
				}
			}
			resp = settled(j)
			return nil
		}

		// Held -> final | disputed | pending.
		sr := req.Receipt
		j.receipt = &sr
		j.cost = protocol.TicketCost(t, r.PromptTokens, r.CompletionTokens)
		switch {
		case ackOK:
			if err := e.finalize(tx, led, j, now); err != nil {
				return err
			}
		case j.disputeReason != "":
			j.status = StatusDisputed
		default:
			j.status, j.finalizeAt = StatusPending, now.Add(e.cfg.DisputeWindow).UnixNano()
		}
		resp = settled(j)
		return saveJob(tx, j)
	})
	return resp, err
}

// finalize pays the node for j and records the success.
func (e *Engine) finalize(tx *sql.Tx, led *ledger.Tx, j *job, now time.Time) error {
	fee, err := led.Capture(j.ticket.JobID, ledger.Account(j.ticket.Node), int64(j.cost), e.cfg.FeeBps)
	if err != nil {
		return fmt.Errorf("coordinator: capture %s: %w", j.ticket.JobID, err)
	}
	j.fee, j.status, j.doneAt = fee, StatusFinal, now.UnixNano()
	if _, err := updateStats(tx, j.ticket.Node, func(s *NodeStats) {
		s.Successes++
		s.Reputation = min(maxReputation, s.Reputation+repPerSuccess)
	}); err != nil {
		return err
	}
	return saveJob(tx, j)
}

// refund returns j's hold to the buyer and charges the node a lost dispute.
func (e *Engine) refund(tx *sql.Tx, led *ledger.Tx, j *job, now time.Time) error {
	if err := led.Release(j.ticket.JobID, "refund (dispute upheld)"); err != nil {
		return fmt.Errorf("coordinator: refund %s: %w", j.ticket.JobID, err)
	}
	j.status, j.doneAt = StatusRefunded, now.UnixNano()
	if _, err := updateStats(tx, j.ticket.Node, func(s *NodeStats) {
		s.DisputesLost++
		s.Reputation = max(0, s.Reputation-repPerLostDispute)
	}); err != nil {
		return err
	}
	return saveJob(tx, j)
}

func settled(j *job) *protocol.SettleResponse {
	return &protocol.SettleResponse{Status: string(j.status), CostMicros: j.cost, FeeMicros: uint64(j.fee)}
}

func verifyAck(t *protocol.Ticket, r *receipt.Receipt, sig []byte) error {
	pub, err := protocol.PubKeyFromPeerID(t.Buyer)
	if err != nil {
		return protocol.Errorf(protocol.CodeBadTicket, "bad buyer id")
	}
	if !ed25519.Verify(pub, protocol.AckBytes(r), sig) {
		return protocol.Errorf(protocol.CodeBadRequest, "invalid buyer acknowledgement")
	}
	return nil
}

func clampReason(reason, fallback string) string {
	if len(reason) > maxDisputeReasonsz {
		reason = reason[:maxDisputeReasonsz]
	}
	if reason == "" {
		reason = fallback
	}
	return reason
}

// Dispute contests a job without evidence. Before the node settles it is
// recorded and applied on settlement; once pending it freezes the payout for
// arbitration.
func (e *Engine) Dispute(buyer string, req protocol.DisputeRequest) error {
	return e.update(func(tx *sql.Tx, _ *ledger.Tx) error {
		j, err := loadJob(tx, req.JobID)
		if err != nil {
			return err
		}
		if j == nil {
			return protocol.Errorf(protocol.CodeNotFound, "unknown job")
		}
		if buyer != j.ticket.Buyer {
			return protocol.Errorf(protocol.CodeForbidden, "only the buyer may dispute")
		}
		switch j.status {
		case StatusHeld:
			if j.disputeReason == "" {
				j.disputeReason = clampReason(req.Reason, "unspecified")
			}
		case StatusPending:
			j.status, j.disputeReason = StatusDisputed, clampReason(req.Reason, "unspecified")
		case StatusDisputed:
			return nil
		default:
			return protocol.Errorf(protocol.CodeConflict, "job is %s and can no longer be disputed", j.status)
		}
		return saveJob(tx, j)
	})
}

// EvidenceTarget returns what judging buyer's evidence for jobID needs: the
// ticket and the receipt the node settled with, if it has. Judging itself
// (decryption, token counting) runs outside the engine; see ApplyVerdict.
func (e *Engine) EvidenceTarget(buyer, jobID string) (*protocol.Ticket, *receipt.SignedReceipt, error) {
	var (
		t  *protocol.Ticket
		sr *receipt.SignedReceipt
	)
	err := e.update(func(tx *sql.Tx, _ *ledger.Tx) error {
		j, err := loadJob(tx, jobID)
		if err != nil {
			return err
		}
		if err := evidenceAllowed(j, buyer); err != nil {
			return err
		}
		t, sr = &j.ticket, j.receipt
		return nil
	})
	return t, sr, err
}

func evidenceAllowed(j *job, buyer string) error {
	switch {
	case j == nil:
		return protocol.Errorf(protocol.CodeNotFound, "unknown job")
	case buyer != j.ticket.Buyer:
		return protocol.Errorf(protocol.CodeForbidden, "only the buyer may submit evidence")
	case j.disputeOutcome != "":
		return protocol.Errorf(protocol.CodeConflict, "this job's evidence was already judged: %s", j.disputeOutcome)
	case j.status != StatusHeld && j.status != StatusPending && j.status != StatusDisputed:
		return protocol.Errorf(protocol.CodeConflict, "job is %s and can no longer be disputed", j.status)
	}
	return nil
}

// ApplyVerdict records a judged dispute and moves the money: upheld refunds
// the buyer, dismissed pays the node, unverifiable leaves the job disputed for
// manual arbitration. judged is the receipt the verdict is about.
func (e *Engine) ApplyVerdict(buyer, jobID, reason string, judged *receipt.SignedReceipt, v evidence.Verdict) (*protocol.Verdict, error) {
	now := e.cfg.Now()
	var out *protocol.Verdict
	err := e.update(func(tx *sql.Tx, led *ledger.Tx) error {
		j, err := loadJob(tx, jobID)
		if err != nil {
			return err
		}
		if err := evidenceAllowed(j, buyer); err != nil {
			return err
		}
		if j.receipt != nil && !bytes.Equal(j.receipt.Receipt.Digest(), judged.Receipt.Digest()) {
			// The node settled a different receipt while the evidence was judged.
			v = evidence.Verdict{Outcome: evidence.Upheld, Detail: "the node signed two different receipts for this job"}
		}
		if j.receipt == nil {
			j.receipt = judged
			j.cost = protocol.TicketCost(&j.ticket, judged.Receipt.PromptTokens, judged.Receipt.CompletionTokens)
		}
		j.disputeReason = clampReason(reason, "evidence submitted")
		j.disputeOutcome, j.disputeDetail = string(v.Outcome), v.Detail
		switch v.Outcome {
		case evidence.Upheld:
			err = e.refund(tx, led, j, now)
		case evidence.Dismissed:
			err = e.finalize(tx, led, j, now)
		default:
			j.status = StatusDisputed
			err = saveJob(tx, j)
		}
		out = &protocol.Verdict{Outcome: string(v.Outcome), Status: string(j.status), Detail: v.Detail}
		return err
	})
	return out, err
}

// ResolveDispute is manual arbitration of a disputed job: pay the node, or
// refund the buyer and charge the node a lost dispute.
func (e *Engine) ResolveDispute(jobID string, payNode bool) error {
	now := e.cfg.Now()
	return e.update(func(tx *sql.Tx, led *ledger.Tx) error {
		j, err := loadJob(tx, jobID)
		if err != nil {
			return err
		}
		if j == nil {
			return protocol.Errorf(protocol.CodeNotFound, "unknown job")
		}
		if j.status != StatusDisputed {
			return protocol.Errorf(protocol.CodeConflict, "job is %s, not disputed", j.status)
		}
		if payNode {
			return e.finalize(tx, led, j, now)
		}
		return e.refund(tx, led, j, now)
	})
}

// Tick runs time-based transitions: expire offers, release holds nobody
// settled, finalize pending jobs whose dispute window passed, delete old jobs
// and return audit identities' leftover credits to the platform.
func (e *Engine) Tick(now time.Time) {
	e.mu.Lock()
	for node, oe := range e.offers {
		if oe.offer.ExpiresAt <= now.UnixNano() {
			delete(e.offers, node)
		}
	}
	e.mu.Unlock()

	var held, pending, idleAuditors []string
	err := e.update(func(tx *sql.Tx, _ *ledger.Tx) (err error) {
		if held, err = queryStrings(tx, `SELECT job_id FROM jobs WHERE status = 'held' AND release_at <= ?`, now.UnixNano()); err != nil {
			return err
		}
		if pending, err = queryStrings(tx, `SELECT job_id FROM jobs WHERE status = 'pending' AND finalize_at <= ?`, now.UnixNano()); err != nil {
			return err
		}
		if _, err = tx.Exec(`DELETE FROM jobs WHERE status IN ('final', 'released', 'refunded') AND done_at < ?`,
			now.Add(-e.cfg.JobRetention).UnixNano()); err != nil {
			return err
		}
		idleAuditors, err = queryStrings(tx, `SELECT peer FROM peers p WHERE auditor = 1 AND created_at < ? AND NOT EXISTS (
			SELECT 1 FROM jobs j WHERE j.buyer = p.peer AND j.status IN ('held', 'pending', 'disputed'))`, now.Add(-time.Minute).UnixNano())
		return err
	})
	if err != nil {
		e.log.Error("tick failed", "err", err)
		return
	}
	for _, id := range held {
		e.tickJob(id, now, func(tx *sql.Tx, led *ledger.Tx, j *job) error {
			if j.status != StatusHeld || j.releaseAt > now.UnixNano() {
				return nil
			}
			if err := led.Release(id, "release (not settled in time)"); err != nil {
				return err
			}
			j.status, j.doneAt = StatusReleased, now.UnixNano()
			if _, err := updateStats(tx, j.ticket.Node, func(s *NodeStats) {
				s.Failures++
				s.Reputation = max(0, s.Reputation-repPerFailure)
			}); err != nil {
				return err
			}
			return saveJob(tx, j)
		})
	}
	for _, id := range pending {
		e.tickJob(id, now, func(tx *sql.Tx, led *ledger.Tx, j *job) error {
			if j.status != StatusPending || j.finalizeAt > now.UnixNano() {
				return nil
			}
			return e.finalize(tx, led, j, now)
		})
	}
	for _, peer := range idleAuditors {
		if err := e.update(func(tx *sql.Tx, led *ledger.Tx) error {
			acct := ledger.Account(peer)
			if b, err := led.Balance(acct); err != nil {
				return err
			} else if b > 0 {
				if err := led.Transfer(acct, ledger.PlatformFees, b, "audit funds returned"); err != nil {
					return err
				}
			}
			_, err := tx.Exec(`DELETE FROM peers WHERE peer = ?`, peer)
			return err
		}); err != nil {
			e.log.Error("returning audit funds failed", "peer", peer, "err", err)
		}
	}
}

func (e *Engine) tickJob(id string, now time.Time, fn func(tx *sql.Tx, led *ledger.Tx, j *job) error) {
	err := e.update(func(tx *sql.Tx, led *ledger.Tx) error {
		j, err := loadJob(tx, id)
		if err != nil || j == nil {
			return err
		}
		return fn(tx, led, j)
	})
	if err != nil {
		e.log.Error("deadline transition failed", "job", id, "err", err)
	}
}

func newJobID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func satAdd(a, b uint64) uint64 {
	if a > math.MaxUint64-b {
		return math.MaxUint64
	}
	return a + b
}
