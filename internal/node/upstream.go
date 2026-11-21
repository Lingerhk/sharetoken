package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"sharetoken/internal/protocol"
)

// Upstream is the model server a node serves from.
type Upstream interface {
	// APIs lists the APIs the model server speaks natively
	// (protocol.APIChat, APIMessages, APIResponses). The node converts a
	// request in any other API into one of them (package apicompat).
	APIs() []string
	// Stream sends req (in one of APIs, asking for a stream) and calls emit
	// with each streamed event payload, in order: the JSON after "data:".
	Stream(ctx context.Context, req *Request, emit func(payload []byte) error) error
}

// Compactor is an Upstream with a native Codex /responses/compact endpoint.
// Compact answers req (a Responses compact request, not streamed) with one
// response.completed event whose response holds the compaction item.
// Upstreams without it get a summary request instead (see compact.go).
type Compactor interface {
	Compact(ctx context.Context, req *Request, emit func(payload []byte) error) error
}

type billingKey struct{}

// Billing is what a model server adapter tells the node about the bill of a
// job, through the job's context.
type Billing struct {
	// PromptCap, when set, caps the prompt tokens billed: an adapter that
	// adds instructions of its own to the buyer's request (a subscription
	// login's required system prompt) has the seller pay for them.
	PromptCap uint64
	// PromptDiscount, when set, is taken off the prompt tokens the model
	// server reported: an estimate of what the adapter added to the buyer's
	// request, which the seller pays for.
	PromptDiscount uint64
}

// Prompt lowers u, the usage of a job, to what the adapter lets the node
// bill of its prompt. reported tells that u is the model server's count
// rather than the node's estimate of the buyer's request, which holds
// nothing the adapter added.
func (b *Billing) Prompt(u protocol.Usage, reported bool) protocol.Usage {
	if b.PromptCap > 0 {
		u = u.CapPrompt(b.PromptCap)
	}
	if reported && b.PromptDiscount > 0 {
		u = u.CapPrompt(u.PromptTokens - min(u.PromptTokens, b.PromptDiscount))
	}
	return u
}

// WithBilling attaches b to ctx for the model server adapter to fill in.
func WithBilling(ctx context.Context, b *Billing) context.Context {
	return context.WithValue(ctx, billingKey{}, b)
}

// BillingOf returns the Billing of the job ctx belongs to (a throwaway one
// outside a job).
func BillingOf(ctx context.Context) *Billing {
	if b, ok := ctx.Value(billingKey{}).(*Billing); ok {
		return b
	}
	return &Billing{}
}

// OpenAICompat talks to any server exposing the OpenAI chat-completions API —
// the common interface of self-hosted inference servers:
//
//	Ollama     http://127.0.0.1:11434/v1
//	vLLM       http://127.0.0.1:8000/v1
//	llama.cpp  http://127.0.0.1:8080/v1
//	SGLang     http://127.0.0.1:30000/v1
//
// Point it at a model you are entitled to serve to others.
type OpenAICompat struct {
	BaseURL string       // up to and including /v1
	APIKey  string       // optional bearer token for the local server
	Client  *http.Client // defaults to a client without an overall timeout (streams run long)
}

const maxSSELine = 1 << 20

// StatusError is a non-200 answer from the model server.
type StatusError struct {
	Status    int
	Body      string
	RequestID string
	RetryAt   time.Time
}

func (e *StatusError) Error() string { return fmt.Sprintf("upstream status %d: %s", e.Status, e.Body) }

// RequestRejected reports whether the model server refused the request itself
// as invalid (400, 413, 422) rather than failing to serve it: such a job is
// the buyer's fault, not the node's. 401/403/404 (wrong key or model name)
// and 429/5xx remain node failures.
func (e *StatusError) RequestRejected() bool {
	switch e.Status {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
		return true
	}
	return false
}

// APIs implements Upstream: chat completions only.
func (u *OpenAICompat) APIs() []string { return []string{protocol.APIChat} }

// Stream implements Upstream.
func (u *OpenAICompat) Stream(ctx context.Context, r *Request, emit func([]byte) error) error {
	if r.API != protocol.APIChat {
		return fmt.Errorf("node: OpenAICompat speaks chat, not %s", r.API)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(u.BaseURL, "/")+"/chat/completions", bytes.NewReader(r.Body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if u.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+u.APIKey)
	}
	client := u.Client
	if client == nil {
		client = &http.Client{}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return &StatusError{Status: resp.StatusCode, Body: string(bytes.TrimSpace(msg))}
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), maxSSELine)
	finished := false
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue // comments, event:, id:, blank separators
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 {
			continue
		}
		if string(payload) == "[DONE]" {
			return nil
		}
		if msg, ok := chatStreamError(payload); ok {
			return &StatusError{Status: http.StatusBadGateway, Body: clip(msg, 300)}
		}
		finished = finished || chatFinished(payload)
		if err := emit(bytes.Clone(payload)); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if !finished {
		return errors.New("the stream ended before the answer did")
	}
	return nil
}

// chatStreamError reads an error a chat server sent inside its stream, in
// place of a chunk: {"error": {"message": …}} or {"error": "…"}.
func chatStreamError(payload []byte) (string, bool) {
	if !bytes.Contains(payload, []byte(`"error"`)) {
		return "", false
	}
	var ev struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(payload, &ev) != nil || len(ev.Error) == 0 || string(ev.Error) == "null" {
		return "", false
	}
	var obj struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	}
	if json.Unmarshal(ev.Error, &obj) == nil && (obj.Message != "" || obj.Type != "") {
		return strings.TrimSpace(obj.Type + " " + obj.Message), true
	}
	return strings.Trim(string(ev.Error), `"`), true
}

// chatFinished reports whether a chunk carries a finish_reason: the answer
// is complete, whether or not [DONE] follows.
func chatFinished(payload []byte) bool {
	if !bytes.Contains(payload, []byte(`"finish_reason"`)) {
		return false
	}
	info, err := protocol.ParseChunk(payload)
	return err == nil && info.FinishReason != ""
}
