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
// Users reach the coordinator only through relays (package relay); it may sit
// on a private network and never accepts direct connections from users.
//
// Money belongs to user accounts, not to peers: the portal binds each device's
// peer ID to a user (package accounts, in the same database), and the engine
// resolves the peer on every authenticated request to the account that pays
// (buyer) or earns (node).
//
// All durable state (ledger, jobs, node records, audits, accounts) lives in
// one database and every state change is one transaction, so a crash or
// restart never loses a hold or pays twice. Offers are the only in-memory
// state; nodes re-announce them every few seconds.
package coordinator

import (
	"bytes"
	"context"
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
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"sharetoken/internal/accounts"
	"sharetoken/internal/attest"
	"sharetoken/internal/canon"
	"sharetoken/internal/evidence"
	"sharetoken/internal/ledger"
	"sharetoken/internal/mysqldb"
	"sharetoken/internal/protocol"
	"sharetoken/internal/receipt"
	"sharetoken/internal/tokens"
	"sharetoken/internal/version"
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
	DB         *sql.DB       // nil: a private in-memory database (tests, demos)
	FeeBps     int64         // platform fee in basis points of each settled job
	TicketTTL  time.Duration // time a node has to start a ticketed job
	StartGrace time.Duration // after the ticket expires, an unstarted job is released this late (default 30s)
	// ReconnectGrace postpones that early release while the job's node
	// reconnected to the coordinator after the ticket was issued (it lost or
	// switched its relay, so its start report may still be on its way): the
	// job is released no earlier than this long after the reconnect
	// (default 2m; it is still released at the settlement deadline). Such a
	// job no longer takes one of the node's slots.
	ReconnectGrace time.Duration
	// MaxStaleTickets is how many held tickets a buyer account may have that
	// were issued over StaleAfter ago and never started; beyond it the buyer
	// gets no new tickets until they are released (default 5).
	MaxStaleTickets int
	StaleAfter      time.Duration // default 15s
	SettleGrace     time.Duration // extra time for settlement to arrive before the hold is released
	DisputeWindow   time.Duration // unacknowledged settlements become final after this
	MaxOfferTTL     time.Duration // offers claiming a longer validity are rejected
	JobRetention    time.Duration // finished jobs are deleted after this; the journal is kept
	SuspendFor      time.Duration // matching excludes a node this long after repeated failed audits
	MinBudgetMicros uint64
	// DevCreditMicros credits each new peer once on its first ticket request.
	// For demos and testnets only: anyone can mint peer IDs.
	DevCreditMicros uint64
	// OpenAccounts gives every peer that is not bound to a user its own
	// account (ledger.Account(peer)) instead of refusing it. For tests and
	// local demos without a portal; never on a public network.
	OpenAccounts bool
	Counter      tokens.Counter // judges dispute evidence; nil = heuristic
	Attestation  *Attestation   // optional
	Relays       []string       // relays to reserve on besides those registered in the database
	RelaySync    time.Duration  // how often the relay list and status are synced (default 15s)
	Latency      func(node string) time.Duration
	// NodeOnline reports whether a node is currently connected to the
	// coordinator. Nodes that are not are skipped when matching: a crashed
	// node, or one whose relay just died, cannot be reached by buyers either,
	// and could not report starts or settle (nil: every offer counts as online).
	NodeOnline func(node string) bool
	// WarmUp is how long after start the coordinator answers "no node offers
	// this model" with CodeCapacity instead of CodeNoOffers: offers live in
	// memory, so right after a restart they are still being re-announced and
	// buyers should queue rather than fail (0: off; NewService uses 30s,
	// twice the nodes' announce interval).
	WarmUp time.Duration
	// ConnectedSince reports since when a peer has been continuously
	// connected (zero if it is not). A buyer's unused ticket only counts
	// against it if the buyer stayed connected since the ticket was issued:
	// otherwise the ticket reply may never have reached it (nil: always).
	ConnectedSince func(peer string) time.Time
	Now            func() time.Time
	Log            *slog.Logger
}

// Defaults returns a Config with every tunable at its default (no database,
// counter or hooks), for configuration files to start from.
func Defaults() Config {
	var c Config
	c.setDefaults()
	c.Counter, c.Now = nil, nil
	return c
}

func (c *Config) setDefaults() {
	if c.TicketTTL == 0 {
		// Buyers start a job within seconds of getting its ticket; a short
		// lifetime bounds how long a ticket whose reply was lost (a dropped
		// connection, a coordinator crash) keeps a node slot and a hold.
		c.TicketTTL = time.Minute
	}
	if c.StartGrace == 0 {
		c.StartGrace = 30 * time.Second
	}
	if c.ReconnectGrace == 0 {
		c.ReconnectGrace = 2 * time.Minute
	}
	if c.MaxStaleTickets == 0 {
		c.MaxStaleTickets = 5
	}
	if c.StaleAfter == 0 {
		// Start reports normally arrive within 0.3-2.5 s; 15 s leaves room for
		// a node on a lossy relay path.
		c.StaleAfter = 15 * time.Second
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
	acct  string // ledger account the node earns into
	tee   string // allow-list label of the verified TEE image; "" if not attested
}

// Engine is the coordinator's state machine. It is safe for concurrent use.
type Engine struct {
	self string
	priv ed25519.PrivateKey
	cfg  Config
	db   *sql.DB
	log  *slog.Logger

	policy *accounts.PolicyCache

	mu     sync.Mutex // serializes transactions; guards offers
	offers map[string]*offerEntry
	// withdrawn holds, for each node that withdrew its offer, when it did: an
	// announcement issued before then, still on its way, must not revive it.
	withdrawn map[string]int64

	teeMu    sync.Mutex
	teeCache map[[32]byte]string

	started   time.Time // for WarmUp
	lastPurge time.Time // last purge of expired job history (under mu)
}

// NewEngine opens the engine's state in cfg.DB, checks that the books replay
// from the journal, and signs tickets as self with priv.
func NewEngine(self string, priv ed25519.PrivateKey, cfg Config) (*Engine, error) {
	cfg.setDefaults()
	db := cfg.DB
	if db == nil {
		return nil, errors.New("coordinator: no database")
	}
	e := &Engine{
		self: self, priv: priv, cfg: cfg, db: db, log: cfg.Log, policy: &accounts.PolicyCache{DB: db, TTL: 30 * time.Second},
		offers: make(map[string]*offerEntry), withdrawn: make(map[string]int64), teeCache: make(map[[32]byte]string), started: cfg.Now(),
	}
	if err := Migrate(db); err != nil {
		e.Close()
		return nil, err
	}
	err := e.update(func(tx *sql.Tx, led *ledger.Tx) error {
		// A database holds tickets signed by one coordinator key; refuse to
		// mix them with another's.
		var owner string
		switch err := tx.QueryRow("SELECT value FROM meta WHERE `key` = 'coordinator'").Scan(&owner); {
		case errors.Is(err, sql.ErrNoRows):
			if _, err := tx.Exec("INSERT INTO meta (`key`, value) VALUES ('coordinator', ?)", self); err != nil {
				return err
			}
		case err != nil:
			return err
		case owner != self:
			return fmt.Errorf("coordinator: database belongs to coordinator %s, not %s", owner, self)
		}
		return nil
	})
	if err == nil {
		// Replay the journal outside the write lock: it takes seconds on a
		// large ledger, and every admin command opens an engine while the
		// live coordinator and the portal keep writing.
		err = e.CheckBooks()
	}
	if err != nil {
		e.Close()
		return nil, err
	}
	return e, nil
}

// RecordFeeRate publishes the engine's fee rate in the database for the
// portal, which resolves disputes in its own process. Only the serving
// coordinator calls it: admin commands open the engine with a zero fee and
// must not overwrite the live value.
func (e *Engine) RecordFeeRate() error {
	return e.update(func(tx *sql.Tx, _ *ledger.Tx) error {
		v := strconv.FormatInt(e.cfg.FeeBps, 10)
		if err := upsert(tx, "UPDATE meta SET value = ? WHERE `key` = 'fee_bps'", []any{v},
			"INSERT INTO meta (`key`, value) VALUES ('fee_bps', ?)", []any{v}); err != nil {
			return err
		}
		return upsert(tx, "UPDATE meta SET value = ? WHERE `key` = 'coordinator_version'", []any{version.Version},
			"INSERT INTO meta (`key`, value) VALUES ('coordinator_version', ?)", []any{version.Version})
	})
}

// CheckClient refuses a client version the network's policy does not serve.
func (e *Engine) CheckClient(v string) error {
	if why := e.policy.Get(e.cfg.Now()).Refuses(v); why != "" {
		return protocol.Errorf(protocol.CodeUpgradeRequired, "%s", why)
	}
	return nil
}

// Close stops the engine. The database belongs to the caller.
func (e *Engine) Close() {}

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
	tx, err := mysqldb.Begin(e.db)
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

// accountOf returns the ledger account that pays and earns for peer: its
// user's account, an audit identity's own account, or (OpenAccounts) the
// peer's own account. Unbound and disabled devices get CodeUnauthorized.
func (e *Engine) accountOf(tx *sql.Tx, peer string) (string, error) {
	acct, _, err := accounts.DeviceAccount(tx, peer)
	switch {
	case err == nil:
		return acct, nil
	case errors.Is(err, accounts.ErrDisabled):
		return "", protocol.Errorf(protocol.CodeUnauthorized, "this device's user account is disabled")
	case !errors.Is(err, accounts.ErrUnknownDevice):
		return "", err
	}
	var auditor int
	switch err := tx.QueryRow(`SELECT auditor FROM peers WHERE peer = ?`, peer).Scan(&auditor); {
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return "", err
	case auditor == 1, e.cfg.OpenAccounts:
		return ledger.Account(peer), nil
	}
	return "", protocol.Errorf(protocol.CodeUnauthorized, "device %s is not bound to a user; log in with the sharetoken client first", peer)
}

// AccountOf resolves peer to its ledger account (operators, portal, tests).
func (e *Engine) AccountOf(peer string) (acct string, err error) {
	err = e.update(func(tx *sql.Tx, _ *ledger.Tx) error { acct, err = e.accountOf(tx, peer); return err })
	return acct, err
}

// UpsertOffer records a node's offer if it is valid, the node is a device of
// an active user, and the offer is newer than the one already held (old
// offers cannot be replayed). Any model ID is accepted: what a node serves is
// checked by audits and attestation, not by a list.
func (e *Engine) UpsertOffer(o *protocol.Offer) error {
	now := e.cfg.Now()
	if err := o.Verify(now, e.cfg.MaxOfferTTL); err != nil {
		return protocol.Errorf(protocol.CodeBadRequest, "%v", err)
	}
	tee := e.verifyQuote(o)
	return e.update(func(tx *sql.Tx, _ *ledger.Tx) error {
		acct, err := e.accountOf(tx, o.Node)
		if err != nil {
			return err
		}
		if cur, ok := e.offers[o.Node]; ok && cur.offer.IssuedAt >= o.IssuedAt {
			return nil
		}
		if at, ok := e.withdrawn[o.Node]; ok && o.IssuedAt <= at {
			return nil
		}
		e.offers[o.Node] = &offerEntry{offer: saneOffer(o), acct: acct, tee: tee}
		if err := accounts.SetDeviceVersionTx(tx, o.Node, o.ClientVersion); err != nil {
			return err
		}
		return accounts.TouchDevice(tx, o.Node, now, 5*time.Minute)
	})
}

// saneOffer is a copy of o whose model metadata, which is not signed, holds
// only what a model could have (protocol.ModelMeta.Sane).
func saneOffer(o *protocol.Offer) protocol.Offer {
	out := *o
	out.Models = slices.Clone(o.Models)
	for i := range out.Models {
		out.Models[i].Meta = out.Models[i].Meta.Sane()
	}
	return out
}

// WithdrawOffer removes node's offer at once: the node stopped sharing, and
// buyers should not get tickets for it until the offer expires.
func (e *Engine) WithdrawOffer(node string, issuedAt int64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.offers, node)
	e.withdrawn[node] = issuedAt
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
func (e *Engine) Models() []protocol.ModelSummary { return e.models(nil, nil) }

// ModelsFor is Models as peer may buy them: a device of a paired buyer sees
// the offers of the sellers it is paired with, nobody sees a private seller
// it is not paired with, and a user always sees its own offers.
//
// The nodes of the peer's share partners that share now (not paused, not past
// their daily limit) count among SharedNodes; with "partners only" on, only
// partners' nodes (and the user's own) are listed.
func (e *Engine) ModelsFor(peer string) ([]protocol.ModelSummary, error) {
	sc, err := scopeOf(e.db, peer, e.cfg.Now())
	if err != nil {
		return nil, err
	}
	return e.models(func(acct string) bool {
		_, partner := sc.partners[acct]
		return acct == sc.own || partner || (!sc.onlyPartners && sc.allows(acct))
	}, func(acct string) bool { g, ok := sc.partners[acct]; return ok && g.sharePct > 0 && !g.exhausted }), nil
}

// buyScope is which sellers a buyer may be matched to (package accounts,
// pairing.go): a buyer with pairings buys only from the sellers it is paired
// with, and a private seller serves only the buyers paired with it.
type buyScope struct {
	own     string          // the buyer's account; empty when it is not a user's device
	paired  map[string]bool // accounts of the sellers the buyer is paired with
	private map[string]bool // accounts that serve their paired buyers only
	auditor bool            // an audit identity: private sellers are audited like any other

	// Share partners (sharing.go): their nodes serve the buyer for free.
	partners     map[string]partnerGrant
	onlyPartners bool // never go to the market
}

// restricted says the buyer buys only from its paired sellers.
func (s *buyScope) restricted() bool { return len(s.paired) > 0 }

// allows says whether the buyer may be matched to a node of account acct.
func (s *buyScope) allows(acct string) bool {
	switch {
	case s.paired[acct]:
		return true
	case s.restricted():
		return false
	}
	return !s.private[acct] || s.auditor
}

// scopeOf reads the pairings that apply to peer as a buyer. They are read on
// every request, so a change takes effect on the next one.
func scopeOf(q interface {
	accounts.Querier
	Query(query string, args ...any) (*sql.Rows, error)
}, peer string, now time.Time) (*buyScope, error) {
	sc := &buyScope{}
	ids, err := accounts.PrivateSellers(q)
	if err != nil {
		return nil, err
	}
	sc.private = userAccounts(ids)
	acct, userID, err := accounts.DeviceAccount(q, peer)
	switch {
	case err == nil:
		sc.own = acct
		if ids, err = accounts.PairedSellers(q, userID); err != nil {
			return nil, err
		}
		sc.paired = userAccounts(ids)
		if err := readPartners(q, sc, userID, now); err != nil {
			return nil, err
		}
	case errors.Is(err, accounts.ErrUnknownDevice), errors.Is(err, accounts.ErrDisabled):
		// Not a user's device: an audit identity, or a peer of an open network.
		var auditor int
		if err := q.QueryRow(`SELECT auditor FROM peers WHERE peer = ?`, peer).Scan(&auditor); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		sc.auditor = auditor == 1
	default:
		return nil, err
	}
	return sc, nil
}

func userAccounts(ids []int64) map[string]bool {
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[ledger.UserAccount(id)] = true
	}
	return out
}

// models aggregates the live offers of the accounts show accepts (nil: of
// everyone); shared says which of them are the asker's share partners.
func (e *Engine) models(show, shared func(acct string) bool) []protocol.ModelSummary {
	now := e.cfg.Now().UnixNano()
	e.mu.Lock()
	defer e.mu.Unlock()
	agg := make(map[string]*protocol.ModelSummary)
	for _, oe := range e.offers {
		if oe.offer.ExpiresAt <= now || (show != nil && !show(oe.acct)) {
			continue
		}
		for _, m := range oe.offer.Models {
			s := agg[m.Model]
			if s == nil {
				s = &protocol.ModelSummary{Model: m.Model, MinPriceIn: m.PriceInMicros, MinPriceOut: m.PriceOutMicros}
				agg[m.Model] = s
			}
			s.Nodes++
			if shared != nil && shared(oe.acct) {
				s.SharedNodes++
			}
			if oe.tee != "" {
				s.AttestedNodes++
			}
			s.Meta = protocol.MergeMeta(s.Meta, m.Meta)
			s.MinPriceIn = min(s.MinPriceIn, m.PriceInMicros)
			s.MinPriceOut = min(s.MinPriceOut, m.PriceOutMicros)
			for _, api := range m.APIs {
				if !slices.Contains(s.APIs, api) {
					s.APIs = append(s.APIs, api)
				}
			}
		}
	}
	out := make([]protocol.ModelSummary, 0, len(agg))
	for _, s := range agg {
		slices.SortFunc(s.APIs, func(a, b string) int { return slices.Index(protocol.AllAPIs, a) - slices.Index(protocol.AllAPIs, b) })
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out
}

// Deposit credits the account of a peer (tests, open-account demos; users
// are credited through the portal).
func (e *Engine) Deposit(peerID string, micros uint64) error {
	if micros == 0 || micros > math.MaxInt64 {
		return ledger.ErrInvalidAmount
	}
	return e.update(func(tx *sql.Tx, led *ledger.Tx) error {
		acct, err := e.accountOf(tx, peerID)
		if err != nil {
			return err
		}
		return led.Deposit(acct, int64(micros), "deposit")
	})
}

// Balance reports the account of a peer.
func (e *Engine) Balance(peerID string) (protocol.BalanceResponse, error) {
	var out protocol.BalanceResponse
	err := e.update(func(tx *sql.Tx, led *ledger.Tx) error {
		acct, err := e.accountOf(tx, peerID)
		if err != nil {
			return err
		}
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
// It reads in a read-only transaction: the replay takes seconds on a large
// journal and must not hold the write lock (on MySQL the database-wide one,
// see mysqldb.Begin) while the live coordinator is settling jobs. Each check
// of Reconcile is one statement, so it sees a consistent state.
func (e *Engine) CheckBooks() error {
	tx, err := e.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	return ledger.On(tx, e.cfg.Now()).Reconcile()
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

// minAnswerTokens is the output a reservation must cover at least when the
// balance is too small for the whole budget.
const minAnswerTokens = 256

type candidate struct {
	offer *protocol.Offer
	acct  string
	price protocol.ModelPrice
	stats *NodeStats

	inferVersion uint32
	native       bool // the node's model server speaks the request's API
}

// IssueTicket matches buyer to a node for req.Model, reserves req.BudgetMicros
// from the buyer's account and returns a signed ticket plus the node's offer.
func (e *Engine) IssueTicket(buyer string, req protocol.TicketRequest) (*protocol.TicketResponse, error) {
	return e.issueTicket(buyer, req, false)
}

// issueTicket issues a ticket to buyer. errand: the job is the platform's
// own, carried by buyer (errand.go): it is paid from AuditAccount, and the
// buyer's account, credit and ticket record play no part. The node must be
// one the buyer could have bought from on the market itself.
func (e *Engine) issueTicket(buyer string, req protocol.TicketRequest, errand bool) (*protocol.TicketResponse, error) {
	if req.Model == "" {
		return nil, protocol.Errorf(protocol.CodeBadRequest, "model is required")
	}
	if len(req.InferVersions) == 0 {
		return nil, protocol.Errorf(protocol.CodeBadRequest, "infer_versions is required")
	}
	if !protocol.ValidAPI(req.API) {
		return nil, protocol.Errorf(protocol.CodeBadRequest, "unknown API %q", req.API)
	}
	req.API = protocol.NormalAPI(req.API)
	if req.BudgetMicros < e.cfg.MinBudgetMicros || req.BudgetMicros > math.MaxInt64 {
		return nil, protocol.Errorf(protocol.CodeBadRequest, "budget must be between %d and %d micros", e.cfg.MinBudgetMicros, int64(math.MaxInt64))
	}
	now := e.cfg.Now()
	var resp *protocol.TicketResponse
	err := e.update(func(tx *sql.Tx, led *ledger.Tx) error {
		buyerAcct, err := e.accountOf(tx, buyer)
		if err != nil {
			return err
		}
		// A paired buyer buys only from the sellers it is paired with, and a
		// private seller serves only its paired buyers.
		scope, err := scopeOf(tx, buyer, now)
		if err != nil {
			return err
		}
		payer := buyerAcct
		if errand {
			if scope.auditor || scope.onlyPartners {
				return protocol.Errorf(protocol.CodeForbidden, "this device does not buy on the market")
			}
			payer = AuditAccount
		} else {
			if err := e.grantDevCredit(tx, led, buyer, buyerAcct, now); err != nil {
				return err
			}
			if err := accounts.TouchDevice(tx, buyer, now, 5*time.Minute); err != nil {
				return err
			}
		}
		// A buyer that keeps taking tickets and never connects would block
		// nodes' slots; cap the tickets it leaves unused.
		// Tickets the buyer disputed as failed (it could not reach the node: a
		// node that crashed or lost its relay) are its bad luck, not hoarding:
		// they count against a 4x higher limit, so an honest buyer is not locked
		// out by a few dead nodes while a hoarder stays capped.
		// A node that is offline (its relay just died, it is reconnecting)
		// cannot report starts, so its tickets say nothing about the buyer.
		if !errand {
			stale, failed, err := e.unusedTickets(tx, buyerAcct, now)
			if err != nil {
				return err
			}
			if stale >= e.cfg.MaxStaleTickets || failed >= 4*e.cfg.MaxStaleTickets {
				return protocol.Errorf(protocol.CodeBusy, "%d earlier tickets were never used (%d more reported failed); they are released when they expire", stale, failed)
			}
		}
		busy, sharedBusy, err := busyNodes(tx, now.Add(-e.cfg.StartGrace).UnixNano())
		if err != nil {
			return err
		}
		excluded := make(map[string]bool, len(req.Exclude))
		for _, n := range req.Exclude {
			excluded[n] = true
		}
		// gather collects the nodes accept takes that can run the job now,
		// and counts those skipped only because they are at capacity.
		// Price limits apply to paid jobs only.
		gather := func(accept func(node string, oe *offerEntry) bool, paid bool) (cands []candidate, full int, err error) {
			for node, oe := range e.offers {
				o := &oe.offer
				// A user may not buy from itself (from any of its devices): it
				// would only churn fees and farm reputation.
				if o.ExpiresAt <= now.UnixNano() || node == buyer || oe.acct == buyerAcct || excluded[node] ||
					(req.Node != "" && node != req.Node) || (req.RequireTEE && oe.tee == "") || !accept(node, oe) {
					continue
				}
				if e.cfg.NodeOnline != nil && !e.cfg.NodeOnline(node) {
					continue
				}
				inferVersion := protocol.BestInferVersion(o.InferVersions, req.InferVersions)
				if inferVersion == 0 { // buyer and node have no protocol in common
					continue
				}
				p, ok := o.Price(req.Model)
				if !ok || (paid && ((req.MaxPriceIn > 0 && p.PriceInMicros > req.MaxPriceIn) || (req.MaxPriceOut > 0 && p.PriceOutMicros > req.MaxPriceOut))) {
					continue
				}
				if req.RequireNative && !p.Native(req.API) {
					continue
				}
				st, err := loadStats(tx, node)
				if err != nil {
					return nil, 0, err
				}
				if st.SuspendedUntil > now.UnixNano() {
					continue
				}
				if o.MaxConcurrency > 0 && busy[node] >= o.MaxConcurrency {
					full++
					continue
				}
				cands = append(cands, candidate{offer: o, acct: oe.acct, price: p, stats: st, inferVersion: inferVersion, native: p.Native(req.API)})
			}
			return cands, full, nil
		}
		// Share partners first: their nodes serve the buyer for free, within
		// what each partner gives (its daily tokens and its part of each
		// node's slots).
		shared := false
		var cands []candidate
		// sharedFull counts partner nodes that offer something but are busy,
		// at capacity or past what their owner shares.
		full, sharedFull, partnerOffers := 0, 0, false
		if len(scope.partners) > 0 && !errand {
			var busyFull int
			if cands, busyFull, err = gather(func(node string, oe *offerEntry) bool {
				g, ok := scope.partners[oe.acct]
				if !ok {
					return false
				}
				partnerOffers = partnerOffers || slices.ContainsFunc(oe.offer.Models, func(m protocol.ModelPrice) bool { return m.Model == req.Model })
				if g.exhausted {
					sharedFull++
					return false
				}
				if slots := accounts.PartnerSlots(oe.offer.MaxConcurrency, g.sharePct); oe.offer.MaxConcurrency > 0 && sharedBusy[node] >= slots {
					sharedFull++
					return false
				}
				return true
			}, false); err != nil {
				return err
			}
			sharedFull += busyFull
			shared = len(cands) > 0
		}
		if !shared && scope.onlyPartners {
			if sharedFull > 0 && partnerOffers {
				return protocol.Errorf(protocol.CodeCapacity, "your share partners' nodes offering %q are busy or used up what they share today", req.Model)
			}
			return protocol.Errorf(protocol.CodeNoOffers, "none of your share partners' nodes currently offers %q", req.Model)
		}
		if !shared {
			if cands, full, err = gather(func(_ string, oe *offerEntry) bool {
				// An errand goes where the carrier would pay: a partner's node
				// serves it for free, with a ticket that says so.
				_, partner := scope.partners[oe.acct]
				return scope.allows(oe.acct) && !(errand && partner)
			}, true); err != nil {
				return err
			}
		}
		if len(cands) == 0 && full > 0 {
			return protocol.Errorf(protocol.CodeCapacity, "all %d nodes offering %q are at capacity", full, req.Model)
		}
		// (Only for a first ask: a buyer that already excluded nodes after
		// failures has seen offers, and should hear there are no more.)
		if len(cands) == 0 && len(req.Exclude) == 0 && e.cfg.WarmUp > 0 && now.Before(e.started.Add(e.cfg.WarmUp)) {
			return protocol.Errorf(protocol.CodeCapacity, "the coordinator just started and nodes are re-announcing; retry shortly")
		}
		if len(cands) == 0 {
			what := "no node"
			if req.RequireTEE {
				what = "no attested node"
			}
			if req.RequireNative {
				what += " speaking the " + req.API + " API natively"
			}
			if scope.restricted() {
				what += " of the sellers you are paired with"
			}
			for _, oe := range e.offers {
				if oe.acct == buyerAcct && slices.ContainsFunc(oe.offer.Models, func(m protocol.ModelPrice) bool { return m.Model == req.Model }) {
					what = "no other user's node" // the model list shows it, so say why it cannot be bought
					break
				}
			}
			return protocol.Errorf(protocol.CodeNoOffers, "%s currently offers %q within the price limits", what, req.Model)
		}
		// Nodes whose model server speaks the request's API come first: no
		// conversion, nothing lost in it. The others convert.
		if native := slices.DeleteFunc(slices.Clone(cands), func(c candidate) bool { return !c.native }); len(native) > 0 {
			cands = native
		}
		best := e.pick(cands)
		// A session's previous node, while it is a candidate: its connection
		// to the buyer is warm and its model server holds the session's
		// prompt cache, worth more than a slightly better score.
		if req.PreferNode != "" {
			for _, c := range cands {
				if c.offer.Node == req.PreferNode {
					best = c
					break
				}
			}
		}

		jobID, err := newJobID()
		if err != nil {
			return err
		}
		hold := req.BudgetMicros
		if req.MaxTokens > 0 && req.PromptTokens > 0 {
			hold = min(hold, max(e.cfg.MinBudgetMicros, protocol.HoldFor(req.PromptTokens, req.MaxTokens, best.price.Prices().PromptCeiling(), best.price.PriceOutMicros)))
		}
		// A shared job moves no credits; its reservation only caps the output.
		if errand {
			// The platform pays: exactly the hold is funded, so whatever comes
			// back from it is surplus (Tick returns it).
			if hold > math.MaxInt64 {
				return ledger.ErrInvalidAmount
			}
			if err := led.Fund(payer, int64(hold), "audit funding"); err != nil {
				return err
			}
		}
		if !shared {
			err := led.Hold(jobID, payer, int64(hold))
			if errors.Is(err, ledger.ErrInsufficientFunds) {
				// The balance does not cover the whole budget (a request
				// without an output limit holds all of it): hold what there
				// is, when that pays for the prompt and a short answer. A
				// smaller reservation only caps this request's output.
				need := max(e.cfg.MinBudgetMicros, protocol.HoldFor(req.PromptTokens, minAnswerTokens, best.price.Prices().PromptCeiling(), best.price.PriceOutMicros))
				if avail, berr := led.Balance(payer); berr == nil && avail > 0 && uint64(avail) >= need && uint64(avail) < hold {
					hold = uint64(avail)
					err = led.Hold(jobID, payer, avail)
				}
			}
			if err != nil {
				if errors.Is(err, ledger.ErrInsufficientFunds) {
					return protocol.Errorf(protocol.CodeInsufficientFunds, "balance does not cover the %d micro reservation", hold)
				}
				return err
			}
		}
		t := protocol.Ticket{
			InferVersion:    best.inferVersion,
			JobID:           jobID,
			Model:           req.Model,
			Node:            best.offer.Node,
			NodeAddrs:       best.offer.Addrs,
			NodeBoxPub:      best.offer.BoxPub,
			NodeReceiptKey:  best.offer.ReceiptKey,
			Buyer:           buyer,
			PriceInMicros:   best.price.PriceInMicros,
			PriceOutMicros:  best.price.PriceOutMicros,
			PriceCacheRead:  best.price.PriceCacheRead,
			PriceCacheWrite: best.price.PriceCacheWrite,
			API:             req.API,
			ReservedMicros:  hold,
			IssuedAt:        now.UnixNano(),
			ExpiresAt:       now.Add(e.cfg.TicketTTL).UnixNano(),
			Coordinator:     e.self,
			Shared:          shared,
		}
		t.Sign(e.priv)
		j := &job{ticket: t, buyerAcct: payer, nodeAcct: best.acct, status: StatusHeld,
			createdAt: now.UnixNano(), releaseAt: now.Add(e.cfg.TicketTTL + e.cfg.SettleGrace).UnixNano()}
		if err := insertJob(tx, j); err != nil {
			return err
		}
		resp = &protocol.TicketResponse{Ticket: t, Offer: *best.offer}
		return nil
	})
	return resp, err
}

// unusedTickets counts a buyer's held tickets issued over StaleAfter ago that
// never started, split into plain (possibly hoarded) ones and ones the buyer
// reported failed. Tickets of nodes that are not connected right now are
// skipped (their start reports cannot arrive), and so are tickets issued to a
// device before its current connection (the reply may never have reached it)
// or to a node before its current connection (its start report may be late).
func (e *Engine) unusedTickets(tx *sql.Tx, buyerAcct string, now time.Time) (stale, failed int, err error) {
	rows, err := tx.Query(`SELECT buyer, node, created_at, dispute_reason != '' FROM jobs
		WHERE buyer_acct = ? AND status = 'held' AND started_at = 0 AND created_at < ?`,
		buyerAcct, now.Add(-e.cfg.StaleAfter).UnixNano())
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var device, node string
		var created int64
		var disputed bool
		if err := rows.Scan(&device, &node, &created, &disputed); err != nil {
			return 0, 0, err
		}
		if e.cfg.NodeOnline != nil && !e.cfg.NodeOnline(node) {
			continue
		}
		if e.cfg.ConnectedSince != nil {
			// The device reconnected after the ticket was issued (or is gone):
			// the ticket reply may have been lost with the old connection.
			if since := e.cfg.ConnectedSince(device); since.IsZero() || since.UnixNano() > created {
				continue
			}
			// The node reconnected after the ticket was issued (it lost or
			// switched its relay): its start report may still be waiting in
			// its retry queue, so the ticket says nothing about the buyer.
			if since := e.cfg.ConnectedSince(node); !since.IsZero() && since.UnixNano() > created {
				continue
			}
		}
		if disputed {
			failed++
		} else {
			stale++
		}
	}
	return stale, failed, rows.Err()
}

func (e *Engine) grantDevCredit(tx *sql.Tx, led *ledger.Tx, peer, acct string, now time.Time) error {
	if e.cfg.DevCreditMicros == 0 || e.cfg.DevCreditMicros > math.MaxInt64 {
		return nil
	}
	// Audit identities are funded by the platform and never get dev credit.
	var credited, auditor int
	switch err := tx.QueryRow(`SELECT dev_credited, auditor FROM peers WHERE peer = ?`, peer).Scan(&credited, &auditor); {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.Exec(`INSERT INTO peers (peer, dev_credited, created_at) VALUES (?, 1, ?)`, peer, now.UnixNano()); err != nil {
			return err
		}
	case err != nil:
		return err
	case credited != 0 || auditor != 0:
		return nil
	default:
		if _, err := tx.Exec(`UPDATE peers SET dev_credited = 1 WHERE peer = ?`, peer); err != nil {
			return err
		}
	}
	return led.Deposit(acct, int64(e.cfg.DevCreditMicros), "dev credit")
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
			if j.startedAt == 0 && j.receipt == nil && len(req.Ack) > 0 && now.UnixNano() < j.releaseAt {
				var late bool
				if late, err = e.settleLate(tx, led, j, &req, now); err != nil || late {
					resp = settled(j)
					return err
				}
			}
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
		j.cost = protocol.TicketCost(t, protocol.ReceiptUsage(r))
		switch {
		case ackOK:
			if err := e.finalize(tx, led, j, now); err != nil {
				return err
			}
		case j.disputeReason != "":
			j.status = StatusDisputed
		default:
			// No acknowledgement, typically a buyer that cancelled mid-stream.
			// Only the billed cost waits out the dispute window: the rest of
			// the reservation, a whole request budget when the request set no
			// max_tokens, goes back to the buyer now.
			if err := shrinkHold(led, j, max(int64(j.cost), 1)); err != nil {
				return err
			}
			j.status, j.finalizeAt = StatusPending, now.Add(e.cfg.DisputeWindow).UnixNano()
		}
		resp = settled(j)
		return saveJob(tx, j)
	})
	return resp, err
}

// settleLate pays out a job that was released early as never started (its
// start report was lost, e.g. on a lossy relay path) when the settlement
// still arrives before the job's settlement deadline with the buyer's
// acknowledgement: the buyer confirmed it received the output, so it is
// charged the receipt's cost (not the whole budget) and the node is paid as
// usual. It returns false, changing nothing, when the buyer cannot cover the
// cost any more (the released hold was spent): the settlement is refused.
func (e *Engine) settleLate(tx *sql.Tx, led *ledger.Tx, j *job, req *protocol.SettleRequest, now time.Time) (bool, error) {
	t := &j.ticket
	if err := protocol.CheckReceipt(t, &req.Receipt); err != nil {
		return false, err
	}
	r := &req.Receipt.Receipt
	if err := verifyAck(t, r, req.Ack); err != nil {
		return false, err
	}
	cost := protocol.TicketCost(t, protocol.ReceiptUsage(r))
	var fee int64
	if cost > 0 {
		lateID := t.JobID + ":late"
		switch err := led.Hold(lateID, j.buyerAcct, int64(cost)); {
		case errors.Is(err, ledger.ErrInsufficientFunds):
			e.log.Warn("late settlement refused: the buyer can no longer pay", "job", t.JobID, "cost", cost)
			return false, nil
		case err != nil:
			return false, err
		}
		var err error
		if fee, err = led.Capture(lateID, j.nodeAcct, int64(cost), e.cfg.FeeBps); err != nil {
			return false, fmt.Errorf("coordinator: late capture %s: %w", t.JobID, err)
		}
	}
	sr := req.Receipt
	j.receipt, j.cost, j.fee = &sr, cost, fee
	j.status, j.doneAt, j.disputeDetail = StatusFinal, now.UnixNano(), ""
	if _, err := jobStats(tx, j, func(s *NodeStats) {
		s.Successes++
		s.Reputation = min(maxReputation, s.Reputation+repPerSuccess)
	}); err != nil {
		return false, err
	}
	e.log.Info("settled a job released early as never started", "job", t.JobID, "cost", cost)
	return true, saveJob(tx, j)
}

// Abandon releases a held job its node gave up before delivering anything:
// the buyer gets the whole hold back now, and the node's slot is free again.
// It counts as a failure for the node, like a ticket left to expire.
func (e *Engine) Abandon(submitter string, req protocol.AbandonRequest) error {
	now := e.cfg.Now()
	return e.update(func(tx *sql.Tx, led *ledger.Tx) error {
		j, err := loadJob(tx, req.Ticket.JobID)
		if err != nil {
			return err
		}
		if j == nil {
			return protocol.Errorf(protocol.CodeNotFound, "unknown job")
		}
		if !j.ticket.Equal(&req.Ticket) {
			return protocol.Errorf(protocol.CodeBadTicket, "ticket does not match the one issued")
		}
		if submitter != j.ticket.Node {
			return protocol.Errorf(protocol.CodeForbidden, "only the ticketed node may abandon a job")
		}
		switch {
		case j.status == StatusReleased:
			return nil
		case j.status != StatusHeld || j.receipt != nil:
			return protocol.Errorf(protocol.CodeConflict, "job is %s and can no longer be abandoned", j.status)
		}
		if err := releaseHold(led, j, "release (node abandoned the job)"); err != nil {
			return err
		}
		j.status, j.doneAt = StatusReleased, now.UnixNano()
		j.disputeDetail = clampReason("abandoned by the node: "+req.Reason, "")
		if req.NoFault {
			// The buyer's request was invalid: releasing it is not a node failure,
			// otherwise any buyer could drain honest nodes' reputation for free.
			return saveJob(tx, j)
		}
		if _, err := jobStats(tx, j, func(s *NodeStats) {
			s.Failures++
			s.Reputation = max(0, s.Reputation-repPerFailure)
		}); err != nil {
			return err
		}
		return saveJob(tx, j)
	})
}

// ReleaseUndelivered releases a held job whose ticket could not be sent to
// the buyer. It is a no-op if the job already started or moved on (the reply
// may have got through after all). Nobody is penalized.
func (e *Engine) ReleaseUndelivered(jobID string) error {
	now := e.cfg.Now()
	return e.update(func(tx *sql.Tx, led *ledger.Tx) error {
		j, err := loadJob(tx, jobID)
		if err != nil || j == nil {
			return err
		}
		if j.status != StatusHeld || j.startedAt != 0 || j.receipt != nil {
			return nil
		}
		if err := releaseHold(led, j, "release (ticket reply undelivered)"); err != nil {
			return err
		}
		j.status, j.doneAt = StatusReleased, now.UnixNano()
		j.disputeDetail = "released: the ticket reply could not be delivered"
		return saveJob(tx, j)
	})
}

// MarkStarted records that the buyer started a held job (reported by its
// node). It fails with CodeConflict when the job was already released for
// never starting: the node should then stop serving it, as it will not be paid.
func (e *Engine) MarkStarted(submitter string, t protocol.Ticket) error {
	now := e.cfg.Now()
	return e.update(func(tx *sql.Tx, _ *ledger.Tx) error {
		j, err := loadJob(tx, t.JobID)
		if err != nil {
			return err
		}
		if j == nil {
			return protocol.Errorf(protocol.CodeNotFound, "unknown job")
		}
		if !j.ticket.Equal(&t) {
			return protocol.Errorf(protocol.CodeBadTicket, "ticket does not match the one issued")
		}
		if submitter != j.ticket.Node {
			return protocol.Errorf(protocol.CodeForbidden, "only the ticketed node may report a start")
		}
		switch {
		case j.status == StatusReleased && j.startedAt == 0:
			return protocol.Errorf(protocol.CodeConflict, "the job was released because it did not start in time")
		case j.status != StatusHeld || j.startedAt != 0:
			return nil
		}
		j.startedAt = now.UnixNano()
		return saveJob(tx, j)
	})
}

// finalize pays the node for j and records the success.
func (e *Engine) finalize(tx *sql.Tx, led *ledger.Tx, j *job, now time.Time) error {
	return finalizeJob(tx, led, j, now, e.cfg.FeeBps)
}

// finalizeJob captures j's hold for the node (minus the platform fee) and
// credits the node's record. It is shared with the portal's dispute resolution.
func finalizeJob(tx *sql.Tx, led *ledger.Tx, j *job, now time.Time, feeBps int64) error {
	var fee int64
	if !j.ticket.Shared {
		var err error
		if fee, err = led.Capture(j.ticket.JobID, j.nodeAcct, int64(j.cost), feeBps); err != nil {
			return fmt.Errorf("coordinator: capture %s: %w", j.ticket.JobID, err)
		}
	}
	j.fee, j.status, j.doneAt = fee, StatusFinal, now.UnixNano()
	if _, err := jobStats(tx, j, func(s *NodeStats) {
		s.Successes++
		s.Reputation = min(maxReputation, s.Reputation+repPerSuccess)
	}); err != nil {
		return err
	}
	return saveJob(tx, j)
}

// refund returns j's hold to the buyer and charges the node a lost dispute.
func (e *Engine) refund(tx *sql.Tx, led *ledger.Tx, j *job, now time.Time) error {
	return refundJob(tx, led, j, now)
}

// refundJob returns j's hold to the buyer and charges the node a lost dispute.
func refundJob(tx *sql.Tx, led *ledger.Tx, j *job, now time.Time) error {
	if err := releaseHold(led, j, "refund (dispute upheld)"); err != nil {
		return fmt.Errorf("coordinator: refund %s: %w", j.ticket.JobID, err)
	}
	j.status, j.doneAt = StatusRefunded, now.UnixNano()
	if _, err := jobStats(tx, j, func(s *NodeStats) {
		s.DisputesLost++
		s.Reputation = max(0, s.Reputation-repPerLostDispute)
	}); err != nil {
		return err
	}
	return saveJob(tx, j)
}

// jobStats applies fn to the record of j's node. Jobs between share partners
// leave it as it is: they cost nothing, so two accounts of one person could
// otherwise farm reputation with them, and a partner could harm it for free.
func jobStats(tx *sql.Tx, j *job, fn func(s *NodeStats)) (*NodeStats, error) {
	if j.ticket.Shared {
		return nil, nil
	}
	return updateStats(tx, j.ticket.Node, fn)
}

// releaseHold returns j's hold to the buyer; a shared job has none.
func releaseHold(led *ledger.Tx, j *job, memo string) error {
	if j.ticket.Shared {
		return nil
	}
	return led.Release(j.ticket.JobID, memo)
}

// shrinkHold gives back what of j's hold exceeds keep; a shared job has none.
func shrinkHold(led *ledger.Tx, j *job, keep int64) error {
	if j.ticket.Shared {
		return nil
	}
	return led.ShrinkHold(j.ticket.JobID, keep)
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
		if j.buyerAcct == AuditAccount {
			return protocol.Errorf(protocol.CodeForbidden, "an errand is judged by the coordinator")
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
	case j.buyerAcct == AuditAccount:
		return protocol.Errorf(protocol.CodeForbidden, "an errand is judged by the coordinator")
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
			j.cost = protocol.TicketCost(&j.ticket, protocol.ReceiptUsage(&judged.Receipt))
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
	for node, at := range e.withdrawn {
		if at <= now.Add(-e.cfg.MaxOfferTTL).UnixNano() {
			delete(e.withdrawn, node)
		}
	}
	e.mu.Unlock()

	var held, unstarted, pending, idleAuditors []string
	sweepAudit := false
	err := e.update(func(tx *sql.Tx, _ *ledger.Tx) (err error) {
		if held, err = queryStrings(tx, `SELECT job_id FROM jobs WHERE status = 'held' AND release_at <= ?`, now.UnixNano()); err != nil {
			return err
		}
		if unstarted, err = queryStrings(tx, `SELECT job_id FROM jobs WHERE status = 'held' AND started_at = 0
			AND ticket_expires > 0 AND ticket_expires <= ?`, now.Add(-e.cfg.StartGrace).UnixNano()); err != nil {
			return err
		}
		if pending, err = queryStrings(tx, `SELECT job_id FROM jobs WHERE status = 'pending' AND finalize_at <= ?`, now.UnixNano()); err != nil {
			return err
		}
		// Purge expired history once a minute, not every tick: it runs under the
		// write lock, and (with the (status, done_at) index) only touches the
		// rows it deletes.
		if now.Sub(e.lastPurge) >= time.Minute {
			e.lastPurge = now
			sweepAudit = true
			if _, err = tx.Exec(`DELETE FROM jobs WHERE status IN ('final', 'released', 'refunded') AND done_at < ?`,
				now.Add(-e.cfg.JobRetention).UnixNano()); err != nil {
				return err
			}
		}
		idleAuditors, err = queryStrings(tx, `SELECT peer FROM peers p WHERE auditor = 1 AND created_at < ? AND NOT EXISTS (
			SELECT 1 FROM jobs j WHERE j.buyer = p.peer AND j.status IN ('held', 'pending', 'disputed'))`, now.Add(-AuditorLifetime).UnixNano())
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
			if err := releaseHold(led, j, "release (not settled in time)"); err != nil {
				return err
			}
			j.status, j.doneAt = StatusReleased, now.UnixNano()
			if _, err := jobStats(tx, j, func(s *NodeStats) {
				s.Failures++
				s.Reputation = max(0, s.Reputation-repPerFailure)
			}); err != nil {
				return err
			}
			return saveJob(tx, j)
		})
	}
	for _, id := range unstarted {
		// The ticket expired and the node never reported a start: the buyer
		// never connected (or gave up). Not the node's fault: no penalty.
		e.tickJob(id, now, func(tx *sql.Tx, led *ledger.Tx, j *job) error {
			if j.status != StatusHeld || j.startedAt != 0 || j.receipt != nil {
				return nil
			}
			if e.cfg.ConnectedSince != nil {
				// The node reconnected after the ticket was issued: its start
				// report (or settlement) may be late rather than missing.
				if since := e.cfg.ConnectedSince(j.ticket.Node); !since.IsZero() && since.UnixNano() > j.createdAt &&
					now.Before(since.Add(e.cfg.ReconnectGrace)) {
					return nil
				}
			}
			if err := releaseHold(led, j, "release (job never started)"); err != nil {
				return err
			}
			j.status, j.doneAt = StatusReleased, now.UnixNano()
			j.disputeDetail = "never started: the buyer did not connect before the ticket expired"
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
	if sweepAudit {
		if err := e.returnAuditFunds(); err != nil {
			e.log.Error("returning audit funds failed", "err", err)
		}
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
