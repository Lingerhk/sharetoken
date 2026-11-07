package node

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Upstream is the model server a node serves from.
type Upstream interface {
	// Stream sends body (an OpenAI chat-completions request with stream=true)
	// and calls emit with each streamed chunk payload, in order.
	Stream(ctx context.Context, body []byte, emit func(payload []byte) error) error
}

// OpenAICompat talks to any server exposing the OpenAI chat-completions API —
// the common interface of self-hosted inference servers:
//
//	Ollama     http://127.0.0.1:11434/v1
//	vLLM       http://127.0.0.1:8000/v1
//	llama.cpp  http://127.0.0.1:8080/v1
//	SGLang     http://127.0.0.1:30000/v1
//
// Point it at a model you are entitled to serve to others. Third-party
// commercial APIs generally forbid sharing or reselling access to their keys
// unless you have their express permission.
type OpenAICompat struct {
	BaseURL string       // up to and including /v1
	APIKey  string       // optional bearer token for the local server
	Client  *http.Client // defaults to a client without an overall timeout (streams run long)
}

const maxSSELine = 1 << 20

// StatusError is a non-200 answer from the model server.
type StatusError struct {
	Status int
	Body   string
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

// Stream implements Upstream.
func (u *OpenAICompat) Stream(ctx context.Context, body []byte, emit func([]byte) error) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(u.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
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
		if err := emit(bytes.Clone(payload)); err != nil {
			return err
		}
	}
	return sc.Err()
}
