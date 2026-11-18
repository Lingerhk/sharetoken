package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"sharetoken/internal/node"
)

// httpClient has no overall timeout: streams run long. The job's context
// ends a request.
var httpClient = &http.Client{}

const maxSSELine = 4 << 20

// postSSE posts body and calls on with each "data:" payload of the answer.
// A non-200 answer is a *node.StatusError, which the node sorts into the
// buyer's fault (400, 413, 422) or its own.
func postSSE(ctx context.Context, url string, header http.Header, body []byte, on func(data []byte) error) error {
	_, err := postSSEDone(ctx, url, header, body, on)
	return err
}

// postSSEDone is postSSE that also tells whether the answer ended with the
// "[DONE]" line chat servers close a stream with.
func postSSEDone(ctx context.Context, url string, header http.Header, body []byte, on func(data []byte) error) (done bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header = header.Clone()
	req.Header.Set("Content-Type", "application/json")
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "text/event-stream")
	}
	// An idempotency key with no value is never sent, but lets Go's transport
	// send the request again on a new connection when the reused keep-alive
	// one turns out closed by the server before the request went out
	// ("EOF", "broken pipe"): the server never had it (after magpie's
	// replayable).
	req.Header["X-Idempotency-Key"] = nil
	resp, err := clientForContext(ctx).Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return false, responseStatus(resp, string(msg))
	}
	// A 200 that is one JSON document, not a stream: an error some servers
	// send this way, or an answer from a server that did not stream. Either
	// is told as it is rather than as a stream that ended early.
	if ct := resp.Header.Get("Content-Type"); strings.HasPrefix(ct, "application/json") {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if e := bareError(resp, msg); e != nil {
			return false, e
		}
		return false, &node.StatusError{Status: http.StatusBadGateway, Body: "the model server answered with JSON, not a stream: " + clip(strings.TrimSpace(string(msg)), 300)}
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), maxSSELine)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.HasPrefix(line, []byte("data:")) {
			// An error written as a bare JSON line between the events
			// (Code Assist does), not as data: it would otherwise be skipped
			// and the job fail as "the stream ended" (magpie: sse_bare.go).
			if bytes.HasPrefix(bytes.TrimSpace(line), []byte("{")) {
				if e := bareError(resp, line); e != nil {
					return done, e
				}
			}
			continue
		}
		data := bytes.TrimSpace(line[len("data:"):])
		if len(data) == 0 {
			continue
		}
		if string(data) == "[DONE]" {
			done = true
			continue
		}
		if err := on(data); err != nil {
			return done, err
		}
	}
	return done, sc.Err()
}

// bareError reads a JSON error object ({"error": {...}} or {"error": "…"})
// as the model server's refusal: its status from the error's numeric code
// (Google's), else from its type, else 502. nil when b is no error.
func bareError(resp *http.Response, b []byte) *node.StatusError {
	var v struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(bytes.TrimSpace(b), &v) != nil || len(v.Error) == 0 || string(v.Error) == "null" {
		return nil
	}
	var obj struct {
		Code    any    `json:"code"`
		Type    string `json:"type"`
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	status := http.StatusBadGateway
	if json.Unmarshal(v.Error, &obj) == nil {
		if c, ok := obj.Code.(float64); ok && c >= 400 && c < 600 {
			status = int(c)
		} else if strings.Contains(obj.Type, "invalid_request") || obj.Status == "INVALID_ARGUMENT" {
			status = http.StatusBadRequest
		} else if strings.Contains(obj.Type, "rate_limit") || obj.Status == "RESOURCE_EXHAUSTED" {
			status = http.StatusTooManyRequests
		} else if strings.Contains(obj.Type, "overloaded") || obj.Status == "UNAVAILABLE" {
			status = http.StatusServiceUnavailable
		}
	}
	e := &node.StatusError{Status: status, Body: clip(strings.TrimSpace(string(b)), 1000)}
	if status == http.StatusTooManyRequests {
		e.RetryAt = limitReset(resp.Header, string(b), time.Now())
	}
	return e
}

// getJSON fetches url and decodes the JSON answer.
func getJSON(ctx context.Context, url string, header http.Header, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header = header.Clone()
	req.Header.Set("Accept", "application/json")
	resp, err := clientForContext(ctx).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return responseStatus(resp, clip(strings.TrimSpace(string(body)), 300))
	}
	return json.Unmarshal(body, out)
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// chunks writes an answer as OpenAI chat.completion.chunk payloads: what the
// buyer's side reads, whatever the provider streamed.
type chunks struct {
	id      string
	model   string
	created int64
	emit    func([]byte) error
	started bool
	tools   int // tool calls started so far
}

func newChunks(model string, emit func([]byte) error) *chunks {
	return &chunks{id: "chatcmpl-" + randomHex(12), model: model, created: time.Now().Unix(), emit: emit}
}

func (c *chunks) send(delta map[string]any, finish string, usage map[string]any) error {
	out := map[string]any{"id": c.id, "object": "chat.completion.chunk", "created": c.created, "model": c.model}
	if delta != nil || finish != "" {
		if delta == nil {
			delta = map[string]any{}
		}
		if !c.started {
			delta["role"], c.started = "assistant", true
		}
		choice := map[string]any{"index": 0, "delta": delta, "finish_reason": nil}
		if finish != "" {
			choice["finish_reason"] = finish
		}
		out["choices"] = []any{choice}
	} else {
		out["choices"] = []any{}
	}
	if usage != nil {
		out["usage"] = usage
	}
	data, err := json.Marshal(out)
	if err != nil {
		return err
	}
	return c.emit(data)
}

func (c *chunks) text(s string) error {
	if s == "" {
		return nil
	}
	return c.send(map[string]any{"content": s}, "", nil)
}

func (c *chunks) reasoning(s string) error {
	if s == "" {
		return nil
	}
	return c.send(map[string]any{"reasoning_content": s}, "", nil)
}

// toolStart opens a tool call and returns its index for toolArgs.
func (c *chunks) toolStart(id, name, args string) (int, error) {
	i := c.tools
	c.tools++
	return i, c.send(map[string]any{"tool_calls": []any{map[string]any{
		"index": i, "id": id, "type": "function", "function": map[string]any{"name": name, "arguments": args},
	}}}, "", nil)
}

func (c *chunks) toolArgs(index int, args string) error {
	if args == "" {
		return nil
	}
	return c.send(map[string]any{"tool_calls": []any{map[string]any{
		"index": index, "function": map[string]any{"arguments": args},
	}}}, "", nil)
}

// finish closes the answer: the finish reason, then the token counts in a
// chunk of their own, as OpenAI sends them with stream_options.include_usage.
func (c *chunks) finish(reason string, prompt, completion uint64) error {
	return c.finishDetailed(reason, prompt, completion, 0, 0)
}

// finishDetailed is finish with the prompt tokens read from a cache and the
// completion tokens spent on reasoning.
func (c *chunks) finishDetailed(reason string, prompt, completion, cached, reasoning uint64) error {
	if reason == "" {
		reason = "stop"
	}
	if c.tools > 0 && reason == "stop" {
		reason = "tool_calls"
	}
	if err := c.send(nil, reason, nil); err != nil {
		return err
	}
	usage := map[string]any{"prompt_tokens": prompt, "completion_tokens": completion, "total_tokens": prompt + completion}
	if cached > 0 {
		usage["prompt_tokens_details"] = map[string]any{"cached_tokens": cached}
	}
	if reasoning > 0 {
		usage["completion_tokens_details"] = map[string]any{"reasoning_tokens": reasoning}
	}
	return c.send(nil, "", usage)
}
