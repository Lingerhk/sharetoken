// Package audit is the L2 sampling executor: the coordinator checks, at
// random times, that nodes really serve the model they advertise.
//
// An audit is a mystery-shopper job. The auditor creates a fresh identity,
// funds it from the platform's fees, and buys K answers (Config.Questions)
// from the target node through the ordinary buyer client, so every receipt
// check applies. The questions are open-ended (prompts.go): a question with
// one right answer is answered word for word by any capable model.
//
// Each answer is then scored token by token against a trusted reference copy
// of the model (verify.go):
//
//   - tf (teacher forcing): the reference reads the node's answer and
//     reports at every position whether the node's token is its own first
//     choice. An honest node scores near 1 (calibrated per reference: the
//     reference scoring its own answers), another model clearly lower. The
//     verdict is on the mean over the K questions: single questions overlap.
//   - div (first divergence), for references that report log-probabilities
//     only for their own output: at the one position where the node's answer
//     leaves the reference's greedy answer, the node's token must be a near
//     tie (within 1 nat of the first choice).
//   - none: the reference reports neither (Claude, Gemini): only the bill is
//     checked, and the model is shown as not audited.
//
// The bill is checked against the reference's own token counts. A clear
// mismatch costs reputation, and consecutive mismatches suspend the node
// from matching (coordinator.RecordAudit); results in between are
// inconclusive and cost nothing. In shadow mode (Config.Shadow, and for a
// reference whose honest scores drifted) verdicts are recorded only.
//
// Delivery. The auditor runs on the coordinator's machine, so a direct
// connection would show nodes the coordinator's address. With
// Config.OfficialRelays set it dials nodes only through circuits of the
// relays the coordinator itself reserves on: it drops every direct address
// and every circuit through any other relay from the ticket, and a
// connection gater refuses any other dial (addresses learned later from the
// node included). Its host listens nowhere and never hole punches, so the
// connection is never upgraded to a direct one. A node then sees an
// official relay's address, as for any buyer coming through that relay.
// Trades over other paths are not audited (they are under delegated audit,
// a later step), and a node that offers no official circuit is not a target.
//
// Limits, stated plainly: this shows that the node's answers behave like the
// reference model's, not a cryptographic proof. A node that can tell audits
// from real traffic can behave only when audited: the questions look like
// ordinary chat, but each audit identity is fresh and the decoding is
// greedy.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	mrand "math/rand/v2"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/control"
	lnet "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"

	"sharetoken/internal/buyer"
	"sharetoken/internal/coordinator"
	"sharetoken/internal/node"
	"sharetoken/internal/p2p"
	"sharetoken/internal/protocol"
)

// Reference is a trusted copy of one marketplace model.
type Reference struct {
	Model         string        // marketplace model ID
	UpstreamModel string        // the reference server's name for it
	Upstream      node.Upstream // the reference server, for billing counts
	// Verifier scores answers against the reference. nil: the model is
	// checked for its bill only.
	Verifier Verifier
	// Params are extra fields of every challenge request, e.g.
	// {"reasoning_effort": "none"} for a model that thinks by default: with
	// the output limit of a challenge spent on hidden reasoning there would
	// be no answer to score.
	Params map[string]any
}

// Config configures the auditor. Zero values get defaults.
type Config struct {
	References  []Reference
	Coordinator peer.AddrInfo // where audit identities buy: this coordinator
	// CoordinatorAddrs, if set, supplies the coordinator's current addresses
	// for each audit (its relay circuit addresses, which change over time).
	CoordinatorAddrs func() []ma.Multiaddr
	// OfficialRelays, if set, lists the relays audit connections may run
	// through: nodes are dialed only by circuits through them. nil allows
	// any address (tests on one machine).
	OfficialRelays func() []peer.ID
	Interval       time.Duration // mean time between audits in Run (default 10m)
	BudgetMicros   uint64        // reserved per question (default 200,000)
	MaxTokens      int           // output cap of a question (default 96)
	Questions      int           // questions per audit (default 5)
	// Spread spaces an audit's questions out: a random wait up to Spread
	// before each question after the first (default 0: back to back).
	Spread time.Duration
	// ScoreTokens caps the positions scored per answer (default 64): every
	// position is one reference call on a prefix-completion reference.
	ScoreTokens int
	// Threshold is the tf mean below which the node serves another model,
	// until a calibration of the reference provides one (default 0.85).
	Threshold float64
	// Margin: a tf mean at or above Threshold+Margin is a match; between
	// the two it is inconclusive (default 0.03).
	Margin float64
	// DivFails is how many failed first-divergence checks among the
	// questions make a mismatch (default 2 of 5); none is a match.
	DivFails int
	// Shadow records verdicts without touching nodes' records.
	Shadow  bool
	Prompts []Challenge // question pool; default: built-in open questions
}

// Auditor runs audits against the engine's live offers.
type Auditor struct {
	eng  *coordinator.Engine
	cfg  Config
	refs map[string]Reference
	log  *slog.Logger
	mu   sync.Mutex
	rnd  *mrand.Rand
}

// New returns an auditor for the models in cfg.References.
func New(eng *coordinator.Engine, cfg Config) (*Auditor, error) {
	if len(cfg.References) == 0 {
		return nil, errors.New("audit: no reference models")
	}
	if cfg.Coordinator.ID == "" {
		return nil, errors.New("audit: coordinator address is required")
	}
	if cfg.Interval == 0 {
		cfg.Interval = 10 * time.Minute
	}
	if cfg.BudgetMicros == 0 {
		cfg.BudgetMicros = 200_000
	}
	if cfg.MaxTokens == 0 {
		cfg.MaxTokens = 96
	}
	if cfg.Questions == 0 {
		cfg.Questions = 5
	}
	if cfg.ScoreTokens == 0 {
		cfg.ScoreTokens = 64
	}
	if cfg.Threshold == 0 {
		cfg.Threshold = 0.85
	}
	if cfg.Margin == 0 {
		cfg.Margin = 0.03
	}
	if cfg.DivFails == 0 {
		cfg.DivFails = 2
	}
	refs := make(map[string]Reference, len(cfg.References))
	for _, r := range cfg.References {
		if r.Model == "" || r.UpstreamModel == "" || r.Upstream == nil {
			return nil, fmt.Errorf("audit: incomplete reference %+v", r)
		}
		refs[r.Model] = r
	}
	return &Auditor{eng: eng, cfg: cfg, refs: refs, log: slog.Default().With("component", "auditor"),
		rnd: mrand.New(mrand.NewPCG(mrand.Uint64(), mrand.Uint64()))}, nil
}

// Run audits a random node serving a referenced model at random intervals
// (exponential, mean cfg.Interval) until ctx ends.
func (a *Auditor) Run(ctx context.Context) {
	for {
		wait := time.Duration(mrand.ExpFloat64() * float64(a.cfg.Interval))
		wait = min(max(wait, a.cfg.Interval/4), 4*a.cfg.Interval)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		node, model, ok := a.pickTarget()
		if !ok {
			continue
		}
		if _, err := a.Audit(ctx, node, model); err != nil && ctx.Err() == nil {
			a.log.Warn("audit could not run", "node", node, "model", model, "err", err)
		}
	}
}

// pickTarget chooses uniformly among live (node, model) pairs that have a
// reference and that the auditor can reach, so every node faces the same
// audit rate.
func (a *Auditor) pickTarget() (node, model string, ok bool) {
	type pair struct{ node, model string }
	var pairs []pair
	official := a.official()
	for _, o := range a.eng.LiveOffers() {
		if official != nil && len(officialCircuits(parseAddrs(o.Addrs), official)) == 0 {
			continue
		}
		for _, m := range o.Models {
			if _, ok := a.refs[m.Model]; ok {
				pairs = append(pairs, pair{o.Node, m.Model})
			}
		}
	}
	if len(pairs) == 0 {
		return "", "", false
	}
	p := pairs[mrand.IntN(len(pairs))]
	return p.node, p.model, true
}

// official returns the official relays as a set, or nil when audits may
// dial anything.
func (a *Auditor) official() map[peer.ID]bool {
	if a.cfg.OfficialRelays == nil {
		return nil
	}
	set := map[peer.ID]bool{}
	for _, id := range a.cfg.OfficialRelays() {
		set[id] = true
	}
	return set
}

// questionResult is one answered and scored question.
type questionResult struct {
	Job      string    `json:"job"`
	Via      string    `json:"via,omitempty"` // the relay the job ran through
	Billed   [2]uint64 `json:"billed"`        // prompt, completion tokens on the receipt
	Score    *Score    `json:"score"`
	Question string    `json:"question"`
	Answer   string    `json:"answer"`
}

// Audit runs one audit of nodeID serving model and records the verdict. It
// returns an error, and records nothing, when the audit could not run at all
// (the node's failure is already counted by the normal job lifecycle).
func (a *Auditor) Audit(ctx context.Context, nodeID, model string) (*coordinator.AuditRecord, error) {
	ref, ok := a.refs[model]
	if !ok {
		return nil, fmt.Errorf("audit: no reference for %q", model)
	}
	v := ref.Verifier
	if v == nil {
		v = noVerifier{}
	}
	official := a.official()
	if official != nil && len(official) == 0 {
		return nil, errors.New("audit: no official relay to reach nodes through")
	}
	cl, closeID, err := a.identity(official, uint64(a.cfg.Questions))
	if err != nil {
		return nil, err
	}
	defer closeID()

	rec := &coordinator.AuditRecord{Node: nodeID, Model: model, Method: v.Method(), Shadow: a.cfg.Shadow}
	type answered struct {
		res *buyer.Result
		q   Question
	}
	var (
		answers []answered
		results []questionResult
	)
	for i := range a.cfg.Questions {
		if i > 0 && a.cfg.Spread > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(mrand.Int64N(int64(a.cfg.Spread)))):
			}
		}
		ch := a.challenge()
		req := map[string]any{"model": model, "messages": ch.messages(), "temperature": 0, "max_tokens": a.cfg.MaxTokens}
		for k, v := range ref.Params {
			req[k] = v
		}
		body, err := json.Marshal(req)
		if err != nil {
			return nil, err
		}
		res, err := cl.Chat(ctx, buyer.ChatRequest{Model: model, Body: body, BudgetMicros: a.cfg.BudgetMicros, Node: nodeID}, nil)
		if rec.JobID == "" && res != nil {
			rec.JobID = res.Ticket.JobID
		}
		switch {
		case errors.Is(err, buyer.ErrReceiptRejected) && res != nil:
			// The node's own receipt failed verification: unreadable output or
			// implausible usage. The buyer client has already disputed it.
			rec.Questions = i + 1
			rec.Verdict, rec.Detail = coordinator.AuditMismatch, "receipt rejected: "+err.Error()
			rec.Shadow = false // a receipt proven wrong needs no calibration
			return a.record(rec)
		case err != nil:
			var pe *protocol.Error
			if errors.As(err, &pe) && pe.Code == protocol.CodeBadRequest && strings.Contains(pe.Message, "model server rejected") {
				// The node's model refused a request the reference model takes
				// (e.g. a parameter only the advertised model knows): a sign of
				// another model, but also of a passing upstream fault. Leave a
				// record, without penalty.
				rec.Questions = i + 1
				rec.Verdict, rec.Detail = coordinator.AuditInconclusive, fmt.Sprintf("question %d: the node's model server rejected the request: %s", i+1, pe.Message)
				return a.record(rec)
			}
			return nil, err
		}
		if official != nil {
			// The filter and the gater make this impossible; check anyway.
			if via, _ := peer.Decode(res.Timing.Via); !res.Timing.Relayed || !official[via] {
				return nil, fmt.Errorf("audit: job %s did not run through an official relay", res.Ticket.JobID)
			}
		}
		q, err := question(&res.Ticket, body, ref.UpstreamModel)
		if err != nil {
			return nil, err
		}
		answers = append(answers, answered{res, q})
	}
	// Score once every answer is in: scoring can take a minute per answer
	// (one reference call per token), and the node should see the questions
	// arrive like a buyer's, not paced by the reference.
	for _, an := range answers {
		res, q := an.res, an.q
		sc, err := v.Score(ctx, q, res.Content, res.FinishReason != "length", a.cfg.ScoreTokens)
		if err != nil {
			return nil, fmt.Errorf("audit: reference model: %w", err)
		}
		r := res.Receipt.Receipt
		results = append(results, questionResult{Job: res.Ticket.JobID, Via: res.Timing.Via, Billed: [2]uint64{r.PromptTokens, r.CompletionTokens},
			Score: sc, Question: q.Messages[len(q.Messages)-1].Content, Answer: res.Content})
	}
	rec.Questions = len(results)
	threshold, drift := a.threshold(model)
	rec.Threshold = threshold
	if drift {
		rec.Shadow = true
	}
	rec.Verdict, rec.Score, rec.Detail = a.judge(v.Method(), results, threshold)
	if drift {
		rec.Detail += " (reference drifted since its last calibration: recorded without penalty)"
	}
	if js, err := json.Marshal(results); err == nil {
		rec.Scores = string(js)
	}
	return a.record(rec)
}

// identity creates a fresh, funded audit identity and its buyer client.
func (a *Auditor) identity(official map[peer.ID]bool, questions uint64) (*buyer.Client, func(), error) {
	keys, err := p2p.NewKeys()
	if err != nil {
		return nil, nil, err
	}
	hc := p2p.HostConfig{DialOnly: true} // never hole punches: NAT stays off
	if official != nil {
		hc.Options = []libp2p.Option{libp2p.ConnectionGater(officialGater{official: official, coordinator: a.cfg.Coordinator.ID})}
	}
	h, err := p2p.NewHost(keys, hc)
	if err != nil {
		return nil, nil, err
	}
	if err := a.eng.RegisterAuditor(keys.ID(), a.cfg.BudgetMicros*questions); err != nil {
		h.Close()
		return nil, nil, err
	}
	coord := a.cfg.Coordinator
	if a.cfg.CoordinatorAddrs != nil {
		coord.Addrs = a.cfg.CoordinatorAddrs()
	}
	cl := buyer.New(h, keys, coord)
	cl.Retries = 0 // audit this node, never another
	if official != nil {
		cl.NodeAddrs = func(addrs []ma.Multiaddr) []ma.Multiaddr { return officialCircuits(addrs, official) }
	}
	return cl, func() {
		// A rejected receipt is disputed in the background; deliver it before
		// the identity's host goes away, or the node would be paid by default.
		wctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		_ = cl.WaitDisputes(wctx)
		cancel()
		h.Close()
	}, nil
}

func (a *Auditor) record(rec *coordinator.AuditRecord) (*coordinator.AuditRecord, error) {
	st, err := a.eng.RecordAudit(*rec)
	if err != nil {
		return nil, err
	}
	a.log.Info("audit", "node", rec.Node, "model", rec.Model, "job", rec.JobID, "method", rec.Method, "verdict", rec.Verdict,
		"score", fmt.Sprintf("%.3f", rec.Score), "threshold", fmt.Sprintf("%.3f", rec.Threshold), "shadow", rec.Shadow,
		"reputation", st.Reputation, "detail", rec.Detail)
	return rec, nil
}

// threshold is the tf threshold for model: its latest calibration's, else
// the configured one. drift reports a calibration that saw the reference's
// honest scores move.
func (a *Auditor) threshold(model string) (float64, bool) {
	c, err := a.eng.LatestCalibration(model)
	if err != nil {
		a.log.Warn("reading calibration", "model", model, "err", err)
	}
	if c == nil || c.Threshold <= 0 {
		return a.cfg.Threshold, false
	}
	return c.Threshold, c.Drift
}

// question is the challenge as an honest node sent it to its model: the
// node's request rewriting applied (node.UpstreamRequest).
func question(t *protocol.Ticket, plain []byte, upstreamModel string) (Question, error) {
	prep, err := node.UpstreamRequest(t, nil, plain, upstreamModel)
	if err != nil {
		return Question{}, err
	}
	var b struct {
		Messages  []Message `json:"messages"`
		MaxTokens int       `json:"max_tokens"`
		MaxCT     int       `json:"max_completion_tokens"`
	}
	if err := json.Unmarshal(prep.Body, &b); err != nil {
		return Question{}, err
	}
	if len(b.Messages) == 0 {
		return Question{}, errors.New("audit: the upstream request has no messages")
	}
	return Question{Messages: b.Messages, MaxTokens: max(b.MaxTokens, b.MaxCT)}, nil
}

// judge turns the scored questions into a verdict, the audit's score and a
// detail line.
func (a *Auditor) judge(method string, rs []questionResult, threshold float64) (verdict string, score float64, detail string) {
	if len(rs) == 0 {
		return coordinator.AuditInconclusive, 0, "no question answered"
	}
	switch method {
	case MethodTF:
		var sum float64
		for _, r := range rs {
			sum += r.Score.Top1
		}
		score = sum / float64(len(rs))
	case MethodDiv:
		for _, r := range rs {
			if r.Score.Fail {
				score++
			}
		}
	}
	identity := score >= threshold+a.cfg.Margin // tf: the answers are the reference model's
	if method == MethodDiv {
		identity = score == 0
	}
	// The bill: the node may not claim more tokens than the reference counts
	// for the same text (slack for chat-template differences).
	for i, r := range rs {
		s := r.Score
		if s.RefPrompt > 0 {
			if limit := s.RefPrompt + s.RefPrompt/20 + 16; r.Billed[0] > limit {
				return coordinator.AuditMismatch, score, fmt.Sprintf("question %d: billed %d prompt tokens; the reference model counts %d", i+1, r.Billed[0], s.RefPrompt)
			}
		}
		// Completion counts compare only once the answer is the reference
		// model's, and the whole of it was scored.
		if s.RefCompletion > 0 && identity && (method == MethodDiv || s.Tokens < a.cfg.ScoreTokens) {
			if limit := s.RefCompletion + s.RefCompletion/20 + 16; r.Billed[1] > limit {
				return coordinator.AuditMismatch, score, fmt.Sprintf("question %d: billed %d completion tokens for an answer the reference counts as %d", i+1, r.Billed[1], s.RefCompletion)
			}
		}
	}
	n := len(rs)
	switch method {
	case MethodTF:
		switch {
		case identity:
			return coordinator.AuditMatch, score, fmt.Sprintf("answers are the reference model's: mean top-1 %.3f over %d questions (threshold %.3f)", score, n, threshold)
		case score < threshold:
			return coordinator.AuditMismatch, score, fmt.Sprintf("answers differ from the reference model: mean top-1 %.3f over %d questions, below %.3f", score, n, threshold)
		default:
			return coordinator.AuditInconclusive, score, fmt.Sprintf("mean top-1 %.3f over %d questions is within %.2f of the threshold %.3f", score, n, a.cfg.Margin, threshold)
		}
	case MethodDiv:
		fails := int(score)
		switch {
		case fails == 0:
			return coordinator.AuditMatch, score, fmt.Sprintf("no answer of %d leaves the reference model's beyond a near tie", n)
		case fails >= a.cfg.DivFails:
			return coordinator.AuditMismatch, score, fmt.Sprintf("%d of %d answers leave the reference model's where it is sure", fails, n)
		default:
			return coordinator.AuditInconclusive, score, fmt.Sprintf("%d of %d answers leave the reference model's where it is sure", fails, n)
		}
	}
	return coordinator.AuditInconclusive, 0, fmt.Sprintf("bill checked on %d questions; the model itself cannot be checked against this reference", n)
}

// challenge draws a question from the pool.
func (a *Auditor) challenge() Challenge {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.cfg.Prompts) > 0 {
		return a.cfg.Prompts[a.rnd.IntN(len(a.cfg.Prompts))]
	}
	return builtinChallenge(a.rnd)
}

// ---- delivery: official relay circuits only ----

func parseAddrs(ss []string) []ma.Multiaddr {
	out := make([]ma.Multiaddr, 0, len(ss))
	for _, s := range ss {
		if m, err := ma.NewMultiaddr(s); err == nil {
			out = append(out, m)
		}
	}
	return out
}

// circuitRelay returns the relay of a /p2p-circuit address.
func circuitRelay(a ma.Multiaddr) (peer.ID, bool) {
	var relay peer.ID
	found, ok := false, false
	ma.ForEach(a, func(c ma.Component) bool {
		switch c.Protocol().Code {
		case ma.P_P2P:
			if !found {
				relay, _ = peer.Decode(c.Value())
			}
		case ma.P_CIRCUIT:
			found = true
			return false
		}
		return true
	})
	if found && relay != "" {
		ok = true
	}
	return relay, ok
}

// officialCircuits keeps the circuit addresses through official relays.
func officialCircuits(addrs []ma.Multiaddr, official map[peer.ID]bool) []ma.Multiaddr {
	var out []ma.Multiaddr
	for _, a := range addrs {
		if r, ok := circuitRelay(a); ok && official[r] {
			out = append(out, a)
		}
	}
	return out
}

// officialGater lets an audit host dial only official relays (any address:
// that is how a circuit starts), circuits through them, and the coordinator.
type officialGater struct {
	official    map[peer.ID]bool
	coordinator peer.ID
}

func (g officialGater) InterceptPeerDial(peer.ID) bool { return true }

func (g officialGater) InterceptAddrDial(p peer.ID, a ma.Multiaddr) bool {
	if r, ok := circuitRelay(a); ok {
		return g.official[r]
	}
	return g.official[p] || p == g.coordinator
}

func (g officialGater) InterceptAccept(lnet.ConnMultiaddrs) bool { return false }

func (g officialGater) InterceptSecured(lnet.Direction, peer.ID, lnet.ConnMultiaddrs) bool {
	return true
}

func (g officialGater) InterceptUpgraded(lnet.Conn) (bool, control.DisconnectReason) { return true, 0 }

// ---- statistics ----

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

// quantile is the q-quantile of sorted xs (linear interpolation).
func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return math.NaN()
	}
	pos := q * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := min(lo+1, len(sorted)-1)
	return sorted[lo] + (pos-float64(lo))*(sorted[hi]-sorted[lo])
}

// kMeans draws n bootstrap means of k values from xs, sorted.
func kMeans(xs []float64, k, n int, r *mrand.Rand) []float64 {
	out := make([]float64, n)
	for i := range out {
		var s float64
		for range k {
			s += xs[r.IntN(len(xs))]
		}
		out[i] = s / float64(k)
	}
	slices.Sort(out)
	return out
}
