// Package audit is the L2 sampling executor: the coordinator checks, at
// random times, that nodes really serve the open-weight model they advertise.
//
// An audit is a mystery-shopper job. The auditor creates a fresh identity,
// funds it from the platform's fees, and buys one completion from the target
// node through the ordinary buyer client, so the node sees a new buyer like
// any other and every receipt check applies. The challenge is a short,
// randomized prompt with greedy decoding (temperature 0, fixed seed). The
// auditor then sends the byte-identical upstream request an honest node would
// have sent (node.UpstreamRequest) to its own reference copy of the model and
// compares:
//
//   - output: token-level similarity of the two texts. Same weights under
//     greedy decoding give the same or nearly the same text; a smaller,
//     quantized-away or different model does not;
//   - usage: the reference server's own token counts bound what the node may
//     bill for the same transcript.
//
// A clear mismatch costs reputation, and consecutive mismatches suspend the
// node from matching (coordinator.RecordAudit). Results in between are
// inconclusive and cost nothing, so numeric noise across hardware does not
// punish honest nodes. This is Proof-of-Sampling in practice: with audit rate
// p and penalty S, cheating a fraction of jobs has negative expected value.
//
// Limits, stated plainly: a node that can tell audits from real traffic can
// behave only when audited. Prompts are randomized and the identity is fresh,
// but audit connections come from the coordinator's network location (as do
// relayed buyers); operators who need stronger cover should run the auditor
// behind a separate egress. And only models with a reference can be audited.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	mrand "math/rand/v2"
	"strings"
	"time"
	"unicode"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"

	"sharetoken/buyer"
	"sharetoken/coordinator"
	"sharetoken/node"
	"sharetoken/p2p"
	"sharetoken/protocol"
)

// Reference is a trusted copy of one marketplace model.
type Reference struct {
	Model         string        // marketplace model ID
	UpstreamModel string        // the reference server's name for it
	Upstream      node.Upstream // e.g. &node.OpenAICompat{BaseURL: "http://127.0.0.1:11434/v1"}
}

// Config configures the auditor. Zero values get defaults.
type Config struct {
	References  []Reference
	Coordinator peer.AddrInfo // where audit identities buy: this coordinator
	// CoordinatorAddrs, if set, supplies the coordinator's current addresses
	// for each audit (its relay circuit addresses, which change over time).
	CoordinatorAddrs func() []ma.Multiaddr
	Interval         time.Duration // mean time between audits in Run (default 10m)
	BudgetMicros     uint64        // reserved per audit job (default 200,000)
	MaxTokens        int           // output cap of a challenge (default 96)
	MatchAt          float64       // similarity at or above which outputs match (default 0.8)
	MismatchBelow    float64       // similarity below which they do not (default 0.4)
	Prompts          []string      // challenge prompts; default: randomized built-ins
}

// Auditor runs audits against the engine's live offers.
type Auditor struct {
	eng  *coordinator.Engine
	cfg  Config
	refs map[string]Reference
	log  *slog.Logger
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
	if cfg.MatchAt == 0 {
		cfg.MatchAt = 0.8
	}
	if cfg.MismatchBelow == 0 {
		cfg.MismatchBelow = 0.4
	}
	refs := make(map[string]Reference, len(cfg.References))
	for _, r := range cfg.References {
		if r.Model == "" || r.UpstreamModel == "" || r.Upstream == nil {
			return nil, fmt.Errorf("audit: incomplete reference %+v", r)
		}
		refs[r.Model] = r
	}
	return &Auditor{eng: eng, cfg: cfg, refs: refs, log: slog.Default().With("component", "auditor")}, nil
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
// reference, so every node faces the same audit rate.
func (a *Auditor) pickTarget() (node, model string, ok bool) {
	type pair struct{ node, model string }
	var pairs []pair
	for _, o := range a.eng.LiveOffers() {
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

// Audit runs one audit of nodeID serving model and records the verdict. It
// returns an error, and records nothing, when the job could not run at all
// (the node's failure is already counted by the normal job lifecycle).
func (a *Auditor) Audit(ctx context.Context, nodeID, model string) (*coordinator.AuditRecord, error) {
	ref, ok := a.refs[model]
	if !ok {
		return nil, fmt.Errorf("audit: no reference for %q", model)
	}
	keys, err := p2p.NewKeys()
	if err != nil {
		return nil, err
	}
	h, err := p2p.NewHost(keys, p2p.HostConfig{DialOnly: true})
	if err != nil {
		return nil, err
	}
	defer h.Close()
	if err := a.eng.RegisterAuditor(keys.ID(), a.cfg.BudgetMicros); err != nil {
		return nil, err
	}
	coord := a.cfg.Coordinator
	if a.cfg.CoordinatorAddrs != nil {
		coord.Addrs = a.cfg.CoordinatorAddrs()
	}
	cl := buyer.New(h, keys, coord)
	cl.Retries = 0 // audit this node, never another
	defer func() {
		// A rejected receipt is disputed in the background; deliver it before
		// the identity's host goes away, or the node would be paid by default.
		wctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		_ = cl.WaitDisputes(wctx)
		cancel()
	}()

	prompt := a.challenge()
	body, err := json.Marshal(map[string]any{
		"model":       model,
		"messages":    []map[string]string{{"role": "user", "content": prompt}},
		"temperature": 0,
		"seed":        mrand.IntN(1 << 30),
		"max_tokens":  a.cfg.MaxTokens,
	})
	if err != nil {
		return nil, err
	}
	res, err := cl.Chat(ctx, buyer.ChatRequest{Model: model, Body: body, BudgetMicros: a.cfg.BudgetMicros, Node: nodeID}, nil)
	rec := &coordinator.AuditRecord{Node: nodeID, Model: model}
	switch {
	case errors.Is(err, buyer.ErrReceiptRejected) && res != nil:
		// The node's own receipt failed verification: unreadable output or
		// implausible usage. The buyer client has already disputed it.
		rec.JobID, rec.Verdict, rec.Detail = res.Ticket.JobID, coordinator.AuditMismatch, "receipt rejected: "+err.Error()
		return a.record(rec)
	case err != nil:
		return nil, err
	}
	rec.JobID = res.Ticket.JobID

	refText, refUsage, err := a.reference(ctx, ref, &res.Ticket, body)
	if err != nil {
		return nil, fmt.Errorf("audit: reference model: %w", err)
	}
	rec.Similarity = Similarity(res.Text, refText)
	rec.Verdict, rec.Detail = a.judge(rec.Similarity, res, refText, refUsage)
	return a.record(rec)
}

func (a *Auditor) record(rec *coordinator.AuditRecord) (*coordinator.AuditRecord, error) {
	st, err := a.eng.RecordAudit(*rec)
	if err != nil {
		return nil, err
	}
	a.log.Info("audit", "node", rec.Node, "model", rec.Model, "job", rec.JobID, "verdict", rec.Verdict,
		"similarity", fmt.Sprintf("%.2f", rec.Similarity), "reputation", st.Reputation, "detail", rec.Detail)
	return rec, nil
}

// reference runs the exact upstream request an honest node would have sent.
func (a *Auditor) reference(ctx context.Context, ref Reference, t *protocol.Ticket, plain []byte) (string, *protocol.Usage, error) {
	body, _, err := node.UpstreamRequest(t, plain, ref.UpstreamModel)
	if err != nil {
		return "", nil, err
	}
	var (
		text  strings.Builder
		usage *protocol.Usage
	)
	err = ref.Upstream.Stream(ctx, body, func(payload []byte) error {
		if info, err := protocol.ParseChunk(payload); err == nil {
			text.WriteString(info.Text())
			if info.Usage != nil {
				usage = info.Usage
			}
		}
		return nil
	})
	return text.String(), usage, err
}

// judge turns a similarity and the two usage reports into a verdict.
func (a *Auditor) judge(sim float64, res *buyer.Result, refText string, refUsage *protocol.Usage) (string, string) {
	r := res.Receipt.Receipt
	if refUsage != nil {
		if limit := refUsage.PromptTokens + refUsage.PromptTokens/20 + 16; r.PromptTokens > limit {
			return coordinator.AuditMismatch, fmt.Sprintf("billed %d prompt tokens; the reference model counts %d", r.PromptTokens, refUsage.PromptTokens)
		}
		if limit := refUsage.CompletionTokens + refUsage.CompletionTokens/20 + 16; sim >= a.cfg.MatchAt && r.CompletionTokens > limit {
			return coordinator.AuditMismatch, fmt.Sprintf("billed %d completion tokens for the reference's output of %d", r.CompletionTokens, refUsage.CompletionTokens)
		}
	}
	switch {
	case strings.TrimSpace(res.Text) == "" && strings.TrimSpace(refText) == "":
		return coordinator.AuditInconclusive, "both outputs empty"
	case sim >= a.cfg.MatchAt:
		return coordinator.AuditMatch, fmt.Sprintf("output matches the reference (similarity %.2f)", sim)
	case sim < a.cfg.MismatchBelow:
		return coordinator.AuditMismatch, fmt.Sprintf("output differs from the reference model (similarity %.2f)", sim)
	default:
		return coordinator.AuditInconclusive, fmt.Sprintf("similarity %.2f is between thresholds", sim)
	}
}

// Similarity is 2*LCS/(len(a)+len(b)) over word, number, punctuation and CJK
// character tokens: 1 for identical text, near 1 when greedy decoding drifts
// late, low for a different model.
func Similarity(a, b string) float64 {
	ta, tb := tokenize(a), tokenize(b)
	if len(ta)+len(tb) == 0 {
		return 1
	}
	const maxTokens = 4000
	ta, tb = ta[:min(len(ta), maxTokens)], tb[:min(len(tb), maxTokens)]
	prev := make([]int, len(tb)+1)
	cur := make([]int, len(tb)+1)
	for i := 1; i <= len(ta); i++ {
		for j := 1; j <= len(tb); j++ {
			if ta[i-1] == tb[j-1] {
				cur[j] = prev[j-1] + 1
			} else {
				cur[j] = max(prev[j], cur[j-1])
			}
		}
		prev, cur = cur, prev
	}
	return 2 * float64(prev[len(tb)]) / float64(len(ta)+len(tb))
}

func tokenize(s string) []string {
	var (
		out  []string
		word strings.Builder
	)
	flush := func() {
		if word.Len() > 0 {
			out = append(out, strings.ToLower(word.String()))
			word.Reset()
		}
	}
	for _, r := range s {
		switch {
		case isCJK(r):
			flush()
			out = append(out, string(r))
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '\'':
			word.WriteRune(r)
		case unicode.IsSpace(r):
			flush()
		default:
			flush()
			out = append(out, string(r))
		}
	}
	flush()
	return out
}

func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0x3400 && r <= 0x4DBF) || (r >= 0x3040 && r <= 0x30FF) || (r >= 0xAC00 && r <= 0xD7AF)
}

// challenge returns a short prompt with a tightly constrained answer, so that
// honest greedy decoding is stable and the answer cannot be cached.
func (a *Auditor) challenge() string {
	if len(a.cfg.Prompts) > 0 {
		return a.cfg.Prompts[mrand.IntN(len(a.cfg.Prompts))]
	}
	words := []string{"harbor", "lantern", "meadow", "copper", "glacier", "orchard", "compass", "velvet", "thunder", "saffron"}
	w := words[mrand.IntN(len(words))]
	x, y := 12+mrand.IntN(88), 3+mrand.IntN(17)
	switch mrand.IntN(6) {
	case 0:
		return fmt.Sprintf("What is %d multiplied by %d? Reply with the number only.", x, y)
	case 1:
		return fmt.Sprintf("List the first %d prime numbers greater than %d, separated by commas, nothing else.", 3+y%4, x)
	case 2:
		return fmt.Sprintf("Spell the word %q backwards, letter by letter, separated by spaces.", w)
	case 3:
		return fmt.Sprintf("Give exactly three synonyms for %q as a comma-separated list.", w)
	case 4:
		return fmt.Sprintf("Convert %d degrees Celsius to Fahrenheit. Reply with one line: the formula and the result.", x)
	default:
		return fmt.Sprintf("Write one short sentence that uses the word %q and the number %d.", w, int(math.Abs(float64(x-y))))
	}
}
