package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"sharetoken/internal/apicompat"
	"sharetoken/internal/box"
	"sharetoken/internal/buyer"
	"sharetoken/internal/coordinator"
	"sharetoken/internal/evidence"
	"sharetoken/internal/node"
	"sharetoken/internal/protocol"
	"sharetoken/internal/receipt"
)

// How a question reaches a node (Config.Delivery).
const (
	// DeliveryAuto has a user's client carry the question when one is there
	// that could have bought from the node itself, and sends it from an audit
	// identity through an official relay otherwise.
	DeliveryAuto = "auto"
	// DeliveryCarried only ever has clients carry questions.
	DeliveryCarried = "carried"
	// DeliveryDirect only ever sends them from audit identities.
	DeliveryDirect = "direct"
)

// Deliveries lists the valid audit.delivery values.
var Deliveries = []string{DeliveryAuto, DeliveryCarried, DeliveryDirect}

// answer is one question answered by the node.
type answer struct {
	ticket  protocol.Ticket
	receipt receipt.Receipt
	content string
	finish  string
	via     string // the relay the job ran through, if any
	carrier string // the device that carried it; "" when an audit identity asked
}

// proven is a node's receipt proven wrong by the job's own transcript:
// unreadable output or an implausible bill. It needs no reference model.
type proven struct {
	jobID  string
	detail string
}

func (e *proven) Error() string { return "receipt rejected: " + e.detail }

// refused is the node's model server rejecting the question.
type refused struct {
	jobID string
	msg   string
}

func (e *refused) Error() string { return e.msg }

var errNoCarrier = errors.New("audit: no client there to carry the question")

// How long a carrier is left out after losing a job (a client that went
// offline does that too) and after handing back something the node did not
// sign.
const (
	benchLost   = time.Hour
	benchForged = 24 * time.Hour
)

// delivery is the state of one audit's questions: the audit identity, made
// when the first question has to go out directly.
type delivery struct {
	a        *Auditor
	official map[peer.ID]bool
	cl       *buyer.Client
	closeID  func()
}

func (d *delivery) close() {
	if d.closeID != nil {
		d.closeID()
	}
}

// ask puts one question (body, in api) to the node.
func (d *delivery) ask(ctx context.Context, nodeID, model, api string, body []byte) (*answer, error) {
	a := d.a
	if a.cfg.Delivery != DeliveryDirect && a.cfg.Errands != nil {
		an, err := a.askCarried(ctx, nodeID, model, api, body)
		var p *proven
		if err == nil || errors.As(err, &p) || ctx.Err() != nil {
			return an, err
		}
		if a.cfg.Delivery == DeliveryCarried {
			return nil, err
		}
		if !errors.Is(err, errNoCarrier) {
			a.log.Info("a carried question did not come back; asking directly", "node", nodeID, "err", err)
		}
	} else if a.cfg.Delivery == DeliveryCarried {
		return nil, errNoCarrier
	}
	return d.askDirect(ctx, nodeID, model, api, body)
}

// askDirect buys the answer from an audit identity of the auditor's own.
func (d *delivery) askDirect(ctx context.Context, nodeID, model, api string, body []byte) (*answer, error) {
	a := d.a
	if d.official != nil && len(d.official) == 0 {
		return nil, errors.New("audit: no official relay to reach nodes through")
	}
	if d.cl == nil {
		cl, closeID, err := a.identity(d.official, uint64(a.cfg.Questions))
		if err != nil {
			return nil, err
		}
		d.cl, d.closeID = cl, closeID
	}
	res, err := d.cl.Chat(ctx, buyer.ChatRequest{Model: model, API: api, Body: body, BudgetMicros: a.cfg.BudgetMicros,
		MaxTokens: protocol.RequestedOutput(api, body), Node: nodeID}, nil)
	switch {
	case errors.Is(err, buyer.ErrReceiptRejected) && res != nil:
		// The buyer client has already disputed it.
		return nil, &proven{jobID: res.Ticket.JobID, detail: strings.TrimPrefix(err.Error(), buyer.ErrReceiptRejected.Error()+": ")}
	case err != nil:
		var pe *protocol.Error
		if errors.As(err, &pe) && pe.Code == protocol.CodeBadRequest && strings.Contains(pe.Message, "model server rejected") {
			return nil, &refused{msg: pe.Message}
		}
		return nil, err
	}
	if d.official != nil {
		// The filter and the gater make this impossible; check anyway.
		if via, _ := peer.Decode(res.Timing.Via); !res.Timing.Relayed || !d.official[via] {
			return nil, fmt.Errorf("audit: job %s did not run through an official relay", res.Ticket.JobID)
		}
	}
	return &answer{ticket: res.Ticket, receipt: res.Receipt.Receipt, content: res.Content, finish: res.FinishReason, via: res.Timing.Via}, nil
}

// askCarried has a user's client carry the question (coordinator/errand.go).
// The auditor seals the request with a job key it keeps, so the carrier
// reads neither question nor answer; the node's receipt, which signs the
// hashes of both, shows that what came back is what the node sent.
func (a *Auditor) askCarried(ctx context.Context, nodeID, model, api string, body []byte) (*answer, error) {
	board := a.cfg.Errands
	carriers, err := a.eng.ErrandCarriers(nodeID, board.Carriers())
	if err != nil {
		return nil, err
	}
	if len(carriers) == 0 {
		return nil, errNoCarrier
	}
	carrier := carriers[mrand.IntN(len(carriers))]
	est, err := protocol.EstimatePromptTokens(api, body)
	if err != nil {
		return nil, err
	}
	joined, err := protocol.JoinRequest(nil, body)
	if err != nil {
		return nil, err
	}
	encoded, err := protocol.EncodeRequest(joined)
	if err != nil {
		return nil, err
	}
	// The job is made when the carrier asks for it, on another goroutine.
	var (
		mu     sync.Mutex
		ticket protocol.Ticket
		key    *box.Identity
		sealed []byte
	)
	build := func() (*protocol.ErrandJob, error) {
		// The ticket is what a client's own request for this body gets: the
		// same budget, the same hold.
		tr, err := a.eng.IssueErrandTicket(carrier, protocol.TicketRequest{
			InferVersions: protocol.SupportedInferVersions, Model: model, API: api, Node: nodeID,
			BudgetMicros: a.cfg.CarryBudgetMicros, MaxTokens: protocol.RequestedOutput(api, body), PromptTokens: est,
		})
		if err != nil {
			return nil, err
		}
		k, err := box.GenerateIdentity()
		if err != nil {
			return nil, err
		}
		s, err := box.SealFrom(k, tr.Ticket.NodeBoxPub, encoded, protocol.RequestAAD(&tr.Ticket))
		if err != nil {
			return nil, err
		}
		mu.Lock()
		ticket, key, sealed = tr.Ticket, k, s
		mu.Unlock()
		return &protocol.ErrandJob{Ticket: tr.Ticket, Offer: tr.Offer, BuyerBoxPub: k.Public(), Request: s}, nil
	}
	result, withdraw, err := board.Post(carrier, build)
	if err != nil {
		return nil, err
	}
	defer withdraw()
	var res coordinator.ErrandResult
	select {
	case res = <-result:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(board.Window() + a.cfg.CarryWait):
		res.Error = "the carrier did not come back in time"
	}
	mu.Lock()
	t, jobKey, request := ticket, key, sealed
	mu.Unlock()
	if res.Receipt == nil {
		if t.JobID != "" {
			// The carrier took the job and brought nothing back: its word
			// for why is no evidence against the node. A ticket it never
			// used is freed now; one the node has started runs its course.
			_ = a.eng.ReleaseUndelivered(t.JobID)
			board.Bench(carrier, benchLost)
		}
		return nil, fmt.Errorf("audit: carrier %s: %s", carrier, res.Error)
	}
	if jobKey == nil {
		return nil, errors.New("audit: a result for a job that was never made")
	}
	judge, err := evidence.New(&t, nil, res.Receipt, &protocol.Evidence{JobID: t.JobID, JobSecret: jobKey.Secret()}, request)
	if err != nil {
		board.Bench(carrier, benchForged)
		return nil, fmt.Errorf("audit: carrier %s: %w", carrier, err)
	}
	for i, c := range res.Chunks {
		if err := judge.Add(uint64(i), c); err != nil {
			board.Bench(carrier, benchForged)
			return nil, fmt.Errorf("audit: carrier %s: %w", carrier, err)
		}
	}
	switch v := judge.Finish(ctx, nil); v.Outcome {
	case evidence.Unverifiable:
		// Not what the node signed for: the carrier changed or lost frames.
		board.Bench(carrier, benchForged)
		return nil, fmt.Errorf("audit: carrier %s: %s", carrier, v.Detail)
	case evidence.Upheld:
		return nil, &proven{jobID: t.JobID, detail: v.Detail}
	}
	content, finish := judge.Answer()
	return &answer{ticket: t, receipt: res.Receipt.Receipt, content: content, finish: finish, via: res.Via, carrier: carrier}, nil
}

// challengeBody is a challenge as a client speaking api sends it. It is
// written as a chat request and converted (package apicompat), so a
// reference's Params mean the same in every API.
func challengeBody(api, model string, ch Challenge, params map[string]any, maxTokens int) ([]byte, error) {
	req := map[string]any{"model": model, "messages": ch.messages(), "temperature": 0, "max_tokens": maxTokens}
	for k, v := range params {
		req[k] = v
	}
	body, err := json.Marshal(req)
	if err != nil || protocol.NormalAPI(api) == protocol.APIChat {
		return body, err
	}
	conv, err := apicompat.Convert(protocol.APIChat, api, body, apicompat.Options{Model: model})
	if err != nil {
		return nil, err
	}
	// A client's own request: it names the marketplace model and need not ask
	// for a stream (the node sets both).
	var out map[string]json.RawMessage
	if err := json.Unmarshal(conv.Body, &out); err != nil {
		return nil, err
	}
	out["model"], _ = json.Marshal(model)
	delete(out, "stream")
	delete(out, "stream_options")
	return json.Marshal(out)
}

// question is the challenge as an honest node put it to its model: the
// node's request rewriting applied (node.UpstreamRequest), and read as chat
// messages, which is what the reference takes.
func question(t *protocol.Ticket, plain []byte, upstreamModel string) (Question, error) {
	prep, err := node.UpstreamRequest(t, nil, plain, upstreamModel)
	if err != nil {
		return Question{}, err
	}
	body := prep.Body
	if prep.API != protocol.APIChat {
		conv, err := apicompat.Convert(prep.API, protocol.APIChat, body, apicompat.Options{Model: upstreamModel})
		if err != nil {
			return Question{}, err
		}
		body = conv.Body
	}
	var b struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		MaxTokens int `json:"max_tokens"`
		MaxCT     int `json:"max_completion_tokens"`
	}
	if err := json.Unmarshal(body, &b); err != nil {
		return Question{}, err
	}
	q := Question{MaxTokens: max(b.MaxTokens, b.MaxCT)}
	for _, m := range b.Messages {
		q.Messages = append(q.Messages, Message{Role: m.Role, Content: contentText(m.Content)})
	}
	if len(q.Messages) == 0 {
		return Question{}, errors.New("audit: the upstream request has no messages")
	}
	return q, nil
}

// contentText reads a chat message's content: a string, or text parts.
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &parts)
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}
