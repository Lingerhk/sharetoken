package buyer

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"sharetoken/internal/protocol"
)

// Proxy exposes the marketplace as a local OpenAI-compatible endpoint, so any
// tool that speaks chat-completions can use it by pointing its base URL here.
// Bind it to loopback: every request spends this peer's credits.
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
}

const maxProxyBody = 4 << 20

// Handler returns the HTTP handler.
func (p *Proxy) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", p.chat)
	mux.HandleFunc("GET /v1/models", p.models)
	return mux
}

func (p *Proxy) chat(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxProxyBody))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large", protocol.CodeBadRequest)
		return
	}
	var head struct {
		Model    string            `json:"model"`
		Stream   bool              `json:"stream"`
		Messages []json.RawMessage `json:"messages"`
		// Output caps (max_completion_tokens is the newer OpenAI name).
		MaxTokens           uint64 `json:"max_tokens"`
		MaxCompletionTokens uint64 `json:"max_completion_tokens"`
	}
	if err := json.Unmarshal(body, &head); err != nil || head.Model == "" {
		writeError(w, http.StatusBadRequest, "body must be a JSON chat-completions request with a model", protocol.CodeBadRequest)
		return
	}
	if len(head.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "messages must be a non-empty array", protocol.CodeBadRequest)
		return
	}
	maxTok := head.MaxTokens
	if head.MaxCompletionTokens > 0 && (maxTok == 0 || head.MaxCompletionTokens < maxTok) {
		maxTok = head.MaxCompletionTokens
	}
	req := ChatRequest{Model: head.Model, Body: body, BudgetMicros: p.BudgetMicros, MaxTokens: maxTok, MaxPriceIn: p.MaxPriceIn, MaxPriceOut: p.MaxPriceOut, RequireTEE: p.RequireTEE}
	if head.Stream {
		p.stream(w, r, req)
		return
	}

	res, err := p.Client.Chat(r.Context(), req, nil)
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
	pt, ct := res.Receipt.Receipt.PromptTokens, res.Receipt.Receipt.CompletionTokens
	msg := map[string]any{"role": "assistant", "content": res.Content}
	if res.Reasoning != "" {
		msg["reasoning_content"] = res.Reasoning
	}
	if len(res.ToolCalls) > 0 {
		msg["tool_calls"] = res.ToolCalls
		if res.Content == "" {
			msg["content"] = nil
		}
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
		"usage": map[string]any{"prompt_tokens": pt, "completion_tokens": ct, "total_tokens": pt + ct},
	})
}

func (p *Proxy) stream(w http.ResponseWriter, r *http.Request, req ChatRequest) {
	flusher, _ := w.(http.Flusher)
	started := false
	res, err := p.Client.Chat(r.Context(), req, func(payload []byte) error {
		if !started {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			started = true
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	})
	if !started {
		if err == nil {
			err = errors.New("empty response")
		}
		writeError(w, statusFor(err), err.Error(), codeFor(err))
		return
	}
	if err != nil {
		// Streamed content cannot be recalled; flag the failed verification.
		_, _ = fmt.Fprintf(w, "data: %s\n\n", errorJSON(err.Error(), codeFor(err)))
	} else {
		_, _ = fmt.Fprintf(w, ": sharetoken job=%s node=%s verified=true cost_micros=%d tee=%q\n\n",
			res.Ticket.JobID, res.Ticket.Node, res.CostMicros, res.Attested)
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func (p *Proxy) models(w http.ResponseWriter, r *http.Request) {
	ms, err := p.Client.Models(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error(), codeFor(err))
		return
	}
	data := make([]map[string]any, 0, len(ms))
	for _, m := range ms {
		data = append(data, map[string]any{
			"id": m.Model, "object": "model", "owned_by": "sharetoken",
			"nodes": m.Nodes, "min_price_in": m.MinPriceIn, "min_price_out": m.MinPriceOut,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

func setJobHeaders(h http.Header, res *Result) {
	h.Set("X-Sharetoken-Job", res.Ticket.JobID)
	h.Set("X-Sharetoken-Node", res.Ticket.Node)
	h.Set("X-Sharetoken-Verified", strconv.FormatBool(res.Verified))
	h.Set("X-Sharetoken-Cost-Micros", strconv.FormatUint(res.CostMicros, 10))
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
	w.Header().Set("Content-Type", "application/json")
	if code == protocol.CodeCapacity {
		w.Header().Set("Retry-After", "2")
	}
	w.WriteHeader(status)
	_, _ = w.Write(errorJSON(msg, code))
}

func errorJSON(msg, code string) []byte {
	b, _ := json.Marshal(map[string]any{"error": map[string]string{"message": msg, "type": "sharetoken_error", "code": code}})
	return b
}
