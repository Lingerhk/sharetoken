package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"sharetoken/internal/apicompat"
	"sharetoken/internal/node"
	"sharetoken/internal/protocol"
)

// The ChatGPT (Codex) backend speaks the OpenAI Responses API, with its own
// rules: no output limit, no sampling settings, never stored, instructions
// always present, and the tool name "python" reserved. A Responses request
// goes out adapted to them and the events come back as they are.

const (
	reservedTool      = "python"
	reservedToolAlias = "python__st"
)

// codexRequest adapts a Responses request to the Codex backend. It returns
// the request, the output limit the node set (the backend takes none, so the
// stream is cut there), and whether the reserved tool name was renamed.
func codexRequest(body []byte) (req map[string]any, limit uint64, renamed bool, err error) {
	if req, err = decodeObject(body); err != nil {
		return nil, 0, false, err
	}
	limit = number(req["max_output_tokens"])
	for _, k := range []string{"max_output_tokens", "temperature", "top_p", "max_tokens"} {
		delete(req, k)
	}
	if _, ok := req["instructions"].(string); !ok {
		req["instructions"] = ""
	}
	req["store"] = false
	req["stream"] = true
	include, _ := req["include"].([]any)
	if !slices.Contains(include, any("reasoning.encrypted_content")) {
		req["include"] = append(include, "reasoning.encrypted_content")
	}
	rename := func(m map[string]any) {
		if m != nil && m["name"] == reservedTool {
			m["name"], renamed = reservedToolAlias, true
		}
	}
	tools, _ := req["tools"].([]any)
	for _, t := range tools {
		if m, _ := t.(map[string]any); m["type"] == "function" {
			rename(m)
		}
	}
	if tc, ok := req["tool_choice"].(map[string]any); ok {
		rename(tc)
	}
	// The backend cannot read a compaction item sharetoken made (from
	// another model's summary): it becomes the summary again.
	if input, ok := req["input"].([]any); ok {
		out := input[:0]
		for _, it := range input {
			m, _ := it.(map[string]any)
			switch m["type"] {
			case "reasoning":
				// Reasoning another model's thinking was carried in.
				if enc, _ := m["encrypted_content"].(string); apicompat.IsConverted(enc) {
					continue
				}
			case "function_call":
				rename(m)
			case "compaction", "compaction_summary":
				enc, _ := m["encrypted_content"].(string)
				if summary, ok := apicompat.DecodeCompaction(enc); ok {
					it = map[string]any{"type": "message", "role": "user", "content": []any{
						map[string]any{"type": "input_text", "text": apicompat.SummaryMessage(summary)}}}
				}
			}
			out = append(out, it)
		}
		req["input"] = out
	}
	return req, limit, renamed, nil
}

// codexStream passes the backend's events on. It puts the reserved tool name
// back, fails on error events, and cuts the stream when the visible output
// outgrows limit: the buyer gets what its reservation covers, ended by a
// response.incomplete event as if the backend had stopped there.
type codexStream struct {
	emit      func([]byte) error
	renamed   bool
	limit     uint64
	estPrompt uint64

	text     strings.Builder
	response map[string]any // from response.created
	items    []any          // output items done so far
	seq      int64
	done     bool
	started  bool
}

var errOutputLimit = fmt.Errorf("output limit reached")

func (s *codexStream) event(data []byte) error {
	var ev struct {
		Type     string          `json:"type"`
		Delta    string          `json:"delta"`
		Seq      int64           `json:"sequence_number"`
		Item     json.RawMessage `json:"item"`
		Response map[string]any  `json:"response"`
	}
	if json.Unmarshal(data, &ev) != nil {
		return nil
	}
	s.seq = max(s.seq, ev.Seq)
	switch ev.Type {
	case "error", "response.failed":
		_, msg := eventType(data)
		return &node.StatusError{Status: http.StatusBadGateway, Body: clip(ev.Type+": "+msg, 300)}
	case "response.created":
		s.response = ev.Response
	case "response.output_item.done":
		var item any
		_ = json.Unmarshal(ev.Item, &item)
		s.items = append(s.items, item)
	case "response.completed", "response.incomplete", "response.done":
		s.done = true
	}
	if s.renamed && strings.Contains(string(data), reservedToolAlias) {
		data = []byte(strings.ReplaceAll(string(data), `"name":"`+reservedToolAlias+`"`, `"name":"`+reservedTool+`"`))
	}
	if err := s.emit(append([]byte(nil), data...)); err != nil {
		return err
	}
	switch ev.Type {
	case "response.output_text.delta", "response.reasoning_summary_text.delta", "response.reasoning_text.delta",
		"response.function_call_arguments.delta", "response.custom_tool_call_input.delta", "response.refusal.delta":
		s.started = true
		s.text.WriteString(ev.Delta)
		if s.limit > 0 && protocol.EstimateTokens(s.text.String()) > s.limit {
			return errOutputLimit
		}
	case "response.output_item.added":
		s.started = true
	}
	return nil
}

// end closes a stream the backend did not finish (cut at the limit, or ended
// early after some output) with a terminal event of its own.
func (s *codexStream) end(reason string) error {
	r := s.response
	if r == nil {
		r = map[string]any{"id": "resp_" + randomHex(12), "object": "response", "created_at": time.Now().Unix()}
	}
	r["output"] = s.items
	out := protocol.EstimateTokens(s.text.String())
	r["usage"] = map[string]any{"input_tokens": s.estPrompt, "output_tokens": out, "total_tokens": s.estPrompt + out,
		"input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens_details": map[string]any{"reasoning_tokens": 0}}
	typ := "response.completed"
	r["status"] = "completed"
	if reason != "" {
		typ = "response.incomplete"
		r["status"] = "incomplete"
		r["incomplete_details"] = map[string]any{"reason": reason}
	}
	b, err := json.Marshal(map[string]any{"type": typ, "sequence_number": s.seq + 1, "response": r})
	if err != nil {
		return err
	}
	return s.emit(b)
}

// streamCodex posts a Codex Responses request and passes the answer on.
func streamCodex(ctx context.Context, url string, header http.Header, body []byte, emit func([]byte) error) error {
	req, limit, renamed, err := codexRequest(body)
	if err != nil {
		return &node.StatusError{Status: http.StatusBadRequest, Body: err.Error()}
	}
	out, err := json.Marshal(req)
	if err != nil {
		return err
	}
	estPrompt, _ := protocol.EstimatePromptTokens(protocol.APIResponses, body)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	st := &codexStream{emit: emit, renamed: renamed, limit: limit, estPrompt: estPrompt}
	err = postSSE(ctx, url, header, out, st.event)
	switch {
	case err == errOutputLimit:
		return st.end("max_output_tokens")
	case err != nil:
		return err
	case !st.done && st.started:
		return st.end("")
	case !st.done:
		return fmt.Errorf("the stream ended before the response did")
	}
	return nil
}

// compactCodex sends a compact request to the backend's /responses/compact
// and passes its answer on as one response.completed event.
func compactCodex(ctx context.Context, url string, header http.Header, body []byte, emit func([]byte) error) error {
	req, _, _, err := codexRequest(body)
	if err != nil {
		return &node.StatusError{Status: http.StatusBadRequest, Body: err.Error()}
	}
	for _, k := range []string{"stream", "include", "store", "tool_choice"} {
		delete(req, k)
	}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	hr.Header = header.Clone()
	hr.Header.Set("Content-Type", "application/json")
	hr.Header.Set("Accept", "application/json")
	res, err := clientForContext(ctx).Do(hr)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		return responseStatus(res, clip(strings.TrimSpace(string(raw)), 4096))
	}
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil {
		return &node.StatusError{Status: http.StatusBadGateway, Body: "the compact answer is not JSON"}
	}
	if _, ok := resp["output"].([]any); !ok {
		return &node.StatusError{Status: http.StatusBadGateway, Body: "the compact answer has no output"}
	}
	b, err := json.Marshal(map[string]any{"type": "response.completed", "sequence_number": 0, "response": resp})
	if err != nil {
		return err
	}
	return emit(b)
}

// retryWithoutReasoning sends body, and once more without its reasoning
// items when the upstream refused their encrypted content: reasoning only
// decrypts at the account that made it, and a conversation that moved from
// another seller carries that seller's.
func retryWithoutReasoning(body []byte, do func([]byte) error) error {
	err := do(body)
	var se *node.StatusError
	if !errors.As(err, &se) || se.Status != http.StatusBadRequest || !strings.Contains(strings.ToLower(se.Body), "encrypted") {
		return err
	}
	stripped, ok := withoutReasoning(body)
	if !ok {
		return err
	}
	return do(stripped)
}

// withoutReasoning removes the reasoning items from a Responses request's input.
func withoutReasoning(body []byte) ([]byte, bool) {
	req, err := decodeObject(body)
	if err != nil {
		return nil, false
	}
	input, _ := req["input"].([]any)
	out := input[:0]
	for _, it := range input {
		if m, _ := it.(map[string]any); m["type"] == "reasoning" {
			continue
		}
		out = append(out, it)
	}
	if len(out) == len(input) {
		return nil, false
	}
	req["input"] = out
	b, err := json.Marshal(req)
	return b, err == nil
}

// dropConvertedReasoning removes from a Responses request the reasoning
// items sharetoken's conversions made: an OpenAI model server cannot read
// them.
func dropConvertedReasoning(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"sharetoken-`)) {
		return body
	}
	req, err := decodeObject(body)
	if err != nil {
		return body
	}
	input, _ := req["input"].([]any)
	out := input[:0]
	for _, it := range input {
		if m, _ := it.(map[string]any); m["type"] == "reasoning" {
			if enc, _ := m["encrypted_content"].(string); apicompat.IsConverted(enc) {
				continue
			}
		}
		out = append(out, it)
	}
	req["input"] = out
	b, err := json.Marshal(req)
	if err != nil {
		return body
	}
	return b
}
