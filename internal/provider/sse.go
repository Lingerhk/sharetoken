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
)

// httpClient has no overall timeout: streams run long. The job's context
// ends a request.
var httpClient = &http.Client{}

const maxSSELine = 4 << 20

// postSSE posts body and calls on with each "data:" payload of the answer.
// A non-200 answer is a *node.StatusError, which the node sorts into the
// buyer's fault (400, 413, 422) or its own.
func postSSE(ctx context.Context, url string, header http.Header, body []byte, on func(data []byte) error) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header = header.Clone()
	req.Header.Set("Content-Type", "application/json")
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "text/event-stream")
	}
	resp, err := clientForContext(ctx).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return responseStatus(resp, string(msg))
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), maxSSELine)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[len("data:"):])
		if len(data) == 0 || string(data) == "[DONE]" {
			continue
		}
		if err := on(data); err != nil {
			return err
		}
	}
	return sc.Err()
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
	if reason == "" {
		reason = "stop"
	}
	if c.tools > 0 && reason == "stop" {
		reason = "tool_calls"
	}
	if err := c.send(nil, reason, nil); err != nil {
		return err
	}
	return c.send(nil, "", map[string]any{"prompt_tokens": prompt, "completion_tokens": completion, "total_tokens": prompt + completion})
}
