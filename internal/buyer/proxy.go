package buyer

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"sharetoken/internal/apicompat"
	"sharetoken/internal/protocol"
)

// Proxy exposes the marketplace as local endpoints in the three APIs coding
// tools and SDKs speak, so any of them can use it by pointing its base URL
// here:
//
//	POST /v1/chat/completions           OpenAI chat completions
//	POST /v1/messages                   Anthropic Messages (Claude Code)
//	POST /v1/messages/count_tokens      a local estimate
//	POST /v1/responses                  OpenAI Responses (Codex)
//	GET  /v1/responses                  the same over a WebSocket (Codex)
//	POST /v1/responses/compact          Codex's conversation compaction
//	GET  /v1/models                     the models on offer
//
// A request goes to the network in the API it came in; the node passes it to
// its model server as it is or converts it. Bind the proxy to loopback: every
// request spends this peer's credits.
//
// Non-streaming responses are returned only after the receipt verifies.
// Streaming responses are forwarded as they arrive (that is what streaming
// means); the verdict follows as a final SSE comment, or an error event if
// verification fails.
type Proxy struct {
	Client       *Client
	BudgetMicros uint64 // reserved per request
	MaxPriceIn   uint64
	MaxPriceOut  uint64
	RequireTEE   bool // only nodes whose TEE quote passes the client's policy
	// RequireNative buys only from nodes whose model server speaks the
	// request's API natively (no conversion). A request can also ask for it
	// with the header X-Sharetoken-Require-Native: true.
	RequireNative bool
	// NoLoopGuard turns the loop guard off (loopguard.go, toolloop.go): with
	// it on, an answer whose reasoning or text goes round in a loop is cut
	// as a length stop, and a request whose last turns are the same tool
	// call with the same result is answered here and sent nowhere.
	NoLoopGuard bool

	mu sync.RWMutex // guards BudgetMicros and RequireNative once serving (SetBuy)

	// prev keeps recent Responses answers for previous_response_id over
	// HTTP; compactFails counts each conversation's failed compactions
	prev         prevStore
	compactFails compactFails
}

// SetBuy changes the budget and RequireNative of a running proxy.
func (p *Proxy) SetBuy(budgetMicros uint64, requireNative bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.BudgetMicros, p.RequireNative = budgetMicros, requireNative
}

const maxProxyBody = protocol.MaxRequestBody

// HeaderRequireNative asks for a node that needs no conversion.
const HeaderRequireNative = "X-Sharetoken-Require-Native"

// headerCodexTurn carries Codex's turn_id to the seller (codexTurnID).
const headerCodexTurn = "x-codex-turn-id"

// HeaderNode asks for one node only (by peer ID): to try or debug a seller.
const HeaderNode = "X-Sharetoken-Node"

// Handler returns the HTTP handler.
func (p *Proxy) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", p.chat)
	mux.HandleFunc("POST /v1/messages", p.messages)
	mux.HandleFunc("POST /v1/messages/count_tokens", p.countTokens)
	mux.HandleFunc("POST /v1/responses", p.responses)
	mux.HandleFunc("POST /v1/responses/compact", p.compact)
	mux.HandleFunc("GET /v1/responses", p.responsesSocket)
	mux.HandleFunc("GET /v1/models", p.models)
	return mux
}

// readBody reads a request body within the size limit.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxProxyBody))
	if err != nil {
		return nil, false
	}
	return body, true
}

// request is the network request for body in api.
func (p *Proxy) request(r *http.Request, api, model string, body []byte) ChatRequest {
	p.mu.RLock()
	budget, native := p.BudgetMicros, p.RequireNative
	p.mu.RUnlock()
	header := forwardHeader(api, r.Header)
	if api == protocol.APIResponses {
		if turn := codexTurnID(r.Header, body); turn != "" {
			header[headerCodexTurn] = turn
		}
	}
	return ChatRequest{
		Model: model, API: api, Header: header, Body: body,
		BudgetMicros: budget, MaxTokens: protocol.RequestedOutput(api, body),
		MaxPriceIn: p.MaxPriceIn, MaxPriceOut: p.MaxPriceOut, RequireTEE: p.RequireTEE,
		RequireNative: native || truthy(r.Header.Get(HeaderRequireNative)),
		Node:          strings.TrimSpace(r.Header.Get(HeaderNode)),
	}
}

// codexTurnID is the turn Codex names for a Responses request: in the body's
// client_metadata (as it sends it), or in its x-codex-turn-metadata header.
// The node drops client_metadata, and the seller keeps the ChatGPT backend's
// turn state by it, so it goes as a header of its own.
func codexTurnID(h http.Header, body []byte) string {
	var q struct {
		Meta map[string]json.RawMessage `json:"client_metadata"`
	}
	_ = json.Unmarshal(body, &q)
	var turn string
	_ = json.Unmarshal(q.Meta["turn_id"], &turn)
	if turn != "" {
		return turn
	}
	raw := q.Meta["x-codex-turn-metadata"]
	var text string
	if json.Unmarshal(raw, &text) == nil {
		raw = []byte(text)
	}
	if len(raw) == 0 {
		raw = []byte(h.Get("x-codex-turn-metadata"))
	}
	var m struct {
		Turn string `json:"turn_id"`
	}
	_ = json.Unmarshal(raw, &m)
	return m.Turn
}

func truthy(s string) bool {
	b, err := strconv.ParseBool(strings.TrimSpace(s))
	return err == nil && b
}

// forwardHeader picks the headers of a tool's request the model server may
// need; the node filters them again.
func forwardHeader(api string, h http.Header) protocol.RequestHeader {
	out := protocol.RequestHeader{}
	keep := func(name, as string) {
		if v := strings.TrimSpace(h.Get(name)); v != "" && len(v) <= 2048 {
			out[as] = v
		}
	}
	switch api {
	case protocol.APIMessages:
		keep("anthropic-version", "anthropic-version")
		keep("anthropic-beta", "anthropic-beta")
		keep("x-claude-code-session-id", "x-claude-code-session-id")
	case protocol.APIResponses:
		keep("session_id", "session_id")
		keep("session-id", "session_id")
	}
	return out
}

// sseWriter writes a job's events to the tool as server-sent events, with
// the event names Messages and Responses clients expect.
type sseWriter struct {
	w       http.ResponseWriter
	api     string
	started bool
	// keepalives: the client's (Codex takes a keepalive event), when the
	// request came, and when the client last got anything
	codex     bool
	begun     time.Time
	lastWrite time.Time
	// tidy keeps a Responses client that is not Codex to the standard
	// events; watch sees each event as the client gets it
	tidy  *respTidy
	watch func([]byte)
}

// keepaliveEvery is the least time between two keepalives; a stream with no
// headers sent yet is opened for them only after keepaliveOpen, so that an
// error before it is still told with its status.
const (
	keepaliveEvery = 15 * time.Second
	keepaliveOpen  = 30 * time.Second
)

// keepalive tells a client waiting on a slow model that the answer is on
// its way (after magpie's keepAlive): Claude Code gives up on a stream after
// 300s without a byte, Codex after 300s without an event (comments do not
// count for it), and local proxies sooner. A Messages client gets a ping
// event, Codex the keepalive event its backend sends, others a comment.
func (s *sseWriter) keepalive() {
	now := time.Now()
	if !s.started && now.Sub(s.begun) < keepaliveOpen || now.Sub(s.lastWrite) < keepaliveEvery {
		return
	}
	if !s.started {
		s.w.Header().Set("Content-Type", "text/event-stream")
		s.w.Header().Set("Cache-Control", "no-cache")
		s.w.WriteHeader(http.StatusOK)
		s.started = true
	}
	switch {
	case s.api == protocol.APIMessages:
		_, _ = fmt.Fprint(s.w, "event: ping\ndata: {\"type\":\"ping\"}\n\n")
	case s.api == protocol.APIResponses && s.codex:
		_, _ = fmt.Fprint(s.w, "event: keepalive\ndata: {\"type\":\"keepalive\"}\n\n")
	default:
		_, _ = fmt.Fprint(s.w, ": keepalive\n\n")
	}
	s.lastWrite = now
	s.flush()
}

// codexClient reports whether r comes from Codex (its originator header or
// user agent).
func codexClient(r *http.Request) bool {
	o := strings.ToLower(r.Header.Get("originator") + " " + r.Header.Get("User-Agent"))
	return strings.Contains(o, "codex")
}

func (s *sseWriter) event(payload []byte) error {
	if s.tidy != nil {
		var what tidyResult
		if payload, what = s.tidy.event(payload); what != tidyKeep {
			if what == tidyComment && s.started {
				_, _ = fmt.Fprint(s.w, ": keepalive\n\n")
				s.lastWrite = time.Now()
				s.flush()
			}
			return nil
		}
	}
	if s.watch != nil {
		s.watch(payload)
	}
	if !s.started {
		s.w.Header().Set("Content-Type", "text/event-stream")
		s.w.Header().Set("Cache-Control", "no-cache")
		s.w.WriteHeader(http.StatusOK)
		s.started = true
	}
	if name := apicompat.EventName(s.api, payload); name != "" {
		if _, err := fmt.Fprintf(s.w, "event: %s\n", name); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", payload); err != nil {
		return err
	}
	s.lastWrite = time.Now()
	s.flush()
	return nil
}

func (s *sseWriter) flush() {
	if f, ok := s.w.(http.Flusher); ok {
		f.Flush()
	}
}

// stream runs a streaming request and writes its events as they arrive.
func (p *Proxy) stream(w http.ResponseWriter, r *http.Request, req ChatRequest) {
	_ = p.streamWith(w, r, req, nil)
}

// streamWith is stream that shows watch each event the client gets, and
// returns the request's error.
func (p *Proxy) streamWith(w http.ResponseWriter, r *http.Request, req ChatRequest, watch func([]byte)) error {
	now := time.Now()
	out := &sseWriter{w: w, api: req.API, codex: codexClient(r), begun: now, lastWrite: now, watch: watch}
	if req.API == protocol.APIResponses && !out.codex {
		out.tidy = &respTidy{}
	}
	req.OnProgress = out.keepalive
	if p.answeredToolLoop(w, req, true) {
		return nil
	}
	deliver, guard := p.guardStream(req.API, out.codex, out.event)
	res, err := p.Client.Chat(r.Context(), req, deliver)
	if guard.cut(err) {
		endCut(out, req.API, guard)
		return nil
	}
	if !out.started {
		if err == nil {
			err = errors.New("empty response")
		}
		if tooLong(err) {
			writeTooLong(w, req.API, true, err.Error())
			return err
		}
		writeAPIError(w, req.API, statusFor(err), err.Error(), codeFor(err))
		return err
	}
	if err != nil && tooLong(err) {
		_ = out.event(tooLongEvent(req.API, err.Error()))
	} else if err != nil {
		// Streamed content cannot be recalled; flag the failed verification.
		_ = out.event(streamErrorJSON(req.API, err.Error(), codeFor(err)))
	} else {
		tm := &res.Timing
		_, _ = fmt.Fprintf(w, ": sharetoken job=%s node=%s verified=true cost_micros=%d tee=%q ticket_ms=%.1f dial_ms=%.1f first_byte_ms=%.1f relayed=%t preferred=%t frames=%d resumes=%d up_bytes=%d down_bytes=%d%s\n\n",
			res.Ticket.JobID, res.Ticket.Node, res.CostMicros, res.Attested, tm.TicketMs, tm.DialMs, tm.FirstByteMs, tm.Relayed, tm.Preferred, tm.Frames, tm.Resumes, tm.UpBytes, tm.DownBytes, tailnetNote(tm))
	}
	if req.API == protocol.APIChat {
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}
	out.flush()
	return err
}

// aggregate runs a request whose tool wants one JSON answer: the events are
// collected and turned into it (apicompat.Aggregate) once the receipt checks.
func (p *Proxy) aggregate(w http.ResponseWriter, r *http.Request, req ChatRequest) {
	_, _ = p.aggregateBody(w, r, req)
}

// aggregateBody is aggregate that returns the answer it wrote, or the
// request's error (already told to the client).
func (p *Proxy) aggregateBody(w http.ResponseWriter, r *http.Request, req ChatRequest) ([]byte, error) {
	req.Collect = true
	if p.answeredToolLoop(w, req, false) {
		return nil, nil
	}
	res, err := p.Client.Chat(r.Context(), req, nil)
	if err != nil && tooLong(err) {
		writeTooLong(w, req.API, false, err.Error())
		return nil, err
	}
	if err != nil {
		writeAPIError(w, req.API, statusFor(err), err.Error(), codeFor(err))
		return nil, err
	}
	body, err := apicompat.Aggregate(req.API, res.Payloads)
	if err != nil {
		writeAPIError(w, req.API, http.StatusBadGateway, "the answer cannot be read: "+err.Error(), "upstream_error")
		return nil, err
	}
	setJobHeaders(w.Header(), res)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
	return body, nil
}

func (p *Proxy) chat(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large", protocol.CodeBadRequest)
		return
	}
	var head struct {
		Model    string            `json:"model"`
		Stream   bool              `json:"stream"`
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &head); err != nil || head.Model == "" {
		writeError(w, http.StatusBadRequest, "body must be a JSON chat-completions request with a model", protocol.CodeBadRequest)
		return
	}
	if len(head.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "messages must be a non-empty array", protocol.CodeBadRequest)
		return
	}
	req := p.request(r, protocol.APIChat, head.Model, body)
	if head.Stream {
		p.stream(w, r, req)
		return
	}
	if p.answeredToolLoop(w, req, false) {
		return
	}
	res, err := p.Client.Chat(r.Context(), req, nil)
	if err != nil && tooLong(err) {
		writeTooLong(w, protocol.APIChat, false, err.Error())
		return
	}
	if err != nil {
		writeError(w, statusFor(err), err.Error(), codeFor(err))
		return
	}
	setJobHeaders(w.Header(), res)
	w.Header().Set("Content-Type", "application/json")
	finish := res.FinishReason
	if finish == "" {
		finish = "stop"
	}
	rc := &res.Receipt.Receipt
	pt, ct := rc.PromptTokens, rc.CompletionTokens
	msg := map[string]any{"role": "assistant", "content": res.Content}
	if res.Reasoning != "" {
		msg["reasoning_content"] = res.Reasoning
	}
	if len(res.AnthropicThinking) > 0 {
		msg["anthropic_thinking"] = res.AnthropicThinking
	}
	if len(res.ToolCalls) > 0 {
		msg["tool_calls"] = res.ToolCalls
		if res.Content == "" {
			msg["content"] = nil
		}
	}
	usage := map[string]any{"prompt_tokens": pt, "completion_tokens": ct, "total_tokens": pt + ct}
	if rc.CacheReadTokens > 0 || rc.CacheWriteTokens > 0 {
		usage["prompt_tokens_details"] = map[string]any{"cached_tokens": rc.CacheReadTokens, "cache_creation_tokens": rc.CacheWriteTokens}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":      "chatcmpl-" + res.Ticket.JobID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   res.Ticket.Model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       msg,
			"finish_reason": finish,
		}},
		"usage": usage,
	})
}

func setJobHeaders(h http.Header, res *Result) {
	h.Set("X-Sharetoken-Job", res.Ticket.JobID)
	h.Set("X-Sharetoken-Node", res.Ticket.Node)
	h.Set("X-Sharetoken-Verified", strconv.FormatBool(res.Verified))
	h.Set("X-Sharetoken-Cost-Micros", strconv.FormatUint(res.CostMicros, 10))
	tm := &res.Timing
	h.Set("X-Sharetoken-Timing", fmt.Sprintf("ticket_ms=%.1f dial_ms=%.1f upload_ms=%.1f first_byte_ms=%.1f total_ms=%.1f relayed=%t preferred=%t frames=%d up_bytes=%d down_bytes=%d%s",
		tm.TicketMs, tm.DialMs, tm.UploadMs, tm.FirstByteMs, tm.TotalMs, tm.Relayed, tm.Preferred, tm.Frames, tm.UpBytes, tm.DownBytes, tailnetNote(tm)))
	if res.Attested != "" {
		h.Set("X-Sharetoken-TEE", res.Attested)
	}
}

func codeFor(err error) string {
	if c := protocol.CodeOf(err); c != "" {
		return c
	}
	if errors.Is(err, ErrReceiptRejected) {
		return "receipt_rejected"
	}
	return "upstream_error"
}

func statusFor(err error) int {
	switch codeFor(err) {
	case protocol.CodeBadRequest, protocol.CodeBudget:
		return http.StatusBadRequest
	case protocol.CodeInsufficientFunds:
		return http.StatusPaymentRequired
	case protocol.CodeUnauthorized, protocol.CodeForbidden:
		return http.StatusForbidden // e.g. the user account was disabled
	case protocol.CodeNoOffers, protocol.CodeCapacity:
		return http.StatusServiceUnavailable
	case protocol.CodeUpgradeRequired:
		return http.StatusUpgradeRequired
	default:
		return http.StatusBadGateway
	}
}

func writeError(w http.ResponseWriter, status int, msg, code string) {
	writeAPIError(w, protocol.APIChat, status, msg, code)
}

// writeAPIError answers with an error in the shape the API's clients read.
func writeAPIError(w http.ResponseWriter, api string, status int, msg, code string) {
	w.Header().Set("Content-Type", "application/json")
	if code == protocol.CodeCapacity {
		w.Header().Set("Retry-After", "2")
	}
	w.WriteHeader(status)
	if api == protocol.APIMessages {
		_, _ = w.Write(anthropicErrorJSON(status, msg))
		return
	}
	_, _ = w.Write(errorJSON(msg, code))
}

func errorJSON(msg, code string) []byte {
	b, _ := json.Marshal(map[string]any{"error": map[string]string{"message": msg, "type": "sharetoken_error", "code": code}})
	return b
}

// anthropicErrorJSON is an error as the Anthropic API sends it.
func anthropicErrorJSON(status int, msg string) []byte {
	typ := "api_error"
	switch status {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge:
		typ = "invalid_request_error"
	case http.StatusUnauthorized:
		typ = "authentication_error"
	case http.StatusPaymentRequired, http.StatusForbidden:
		typ = "permission_error"
	case http.StatusNotFound:
		typ = "not_found_error"
	case http.StatusTooManyRequests:
		typ = "rate_limit_error"
	case http.StatusServiceUnavailable:
		typ = "overloaded_error"
	}
	b, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]string{"type": typ, "message": msg}})
	return b
}

// streamErrorJSON is the error event that ends a stream whose output arrived
// but failed verification (or broke off).
func streamErrorJSON(api, msg, code string) []byte {
	switch api {
	case protocol.APIMessages:
		return anthropicErrorJSON(http.StatusBadGateway, msg)
	case protocol.APIResponses:
		b, _ := json.Marshal(map[string]any{"type": "error", "code": code, "message": msg, "param": nil})
		return b
	}
	return errorJSON(msg, code)
}

// tailnetNote is the timing line's last field for a job that ran through a
// tailnet tunnel (" tailnet=direct", " tailnet=derp:tok"), and nothing for
// any other job.
func tailnetNote(tm *Timing) string {
	if tm.Tailnet == "" {
		return ""
	}
	return " tailnet=" + tm.Tailnet
}
