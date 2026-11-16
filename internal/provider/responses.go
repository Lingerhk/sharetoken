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
	"unicode"

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
	// The backend takes the input as a list of items only.
	if text, ok := req["input"].(string); ok {
		req["input"] = []any{map[string]any{"type": "message", "role": "user", "content": []any{
			map[string]any{"type": "input_text", "text": text}}}}
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
	// The backend's models know "none" for what older ones called "minimal".
	if reasoning, ok := req["reasoning"].(map[string]any); ok && reasoning["effort"] == "minimal" {
		reasoning["effort"] = "none"
	}
	// The backend cannot read a compaction item sharetoken made (from
	// another model's summary): it becomes the summary again.
	if input, ok := req["input"].([]any); ok {
		out := input[:0]
		for _, it := range input {
			m, _ := it.(map[string]any)
			// Nothing is stored (store=false), so the backend finds no item
			// by its ID: a reference to one cannot be resolved, and an item
			// sent whole is refused for the ID it had in an earlier answer
			// ("Item with id 'rs_…' not found"). Codex sends none.
			if m["type"] == "item_reference" {
				continue
			}
			delete(m, "id")
			// The backend takes no system messages; Codex's own are developer ones.
			if m["role"] == "system" {
				m["role"] = "developer"
			}
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
// outgrows limit: the buyer gets what its reservation covers, ended as the
// API ends an answer that reached max_output_tokens.
type codexStream struct {
	emit      func([]byte) error
	renamed   bool
	limit     uint64
	estPrompt uint64

	count    tokenCounter   // the visible output so far
	response map[string]any // from response.created
	items    []any          // output items done so far
	open     *codexItem     // the item being written
	seq      int64
	done     bool
}

// codexItem is an output item the backend has started and not finished.
type codexItem struct {
	index        int64          // output_index
	item         map[string]any // as response.output_item.added gave it
	parts        []any          // a message's content parts done so far
	partOpen     bool           // a message part is being written
	refusal      bool           // that part is a refusal, not text
	contentIndex int64
	text         strings.Builder // of the open part, or the call's arguments or input
}

// tokenCounter is protocol.EstimateTokens of a text that arrives in pieces,
// without reading the whole text again for each piece.
type tokenCounter struct{ latin, cjk uint64 }

func (c *tokenCounter) add(s string) {
	for _, r := range s {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			c.cjk++
		} else {
			c.latin++
		}
	}
}

func (c *tokenCounter) tokens() uint64 { return (c.latin+3)/4 + (c.cjk*2+2)/3 }

var errOutputLimit = fmt.Errorf("output limit reached")

func (s *codexStream) event(data []byte) error {
	var ev struct {
		Type         string          `json:"type"`
		Delta        string          `json:"delta"`
		Seq          int64           `json:"sequence_number"`
		OutputIndex  int64           `json:"output_index"`
		ContentIndex int64           `json:"content_index"`
		Item         json.RawMessage `json:"item"`
		Part         json.RawMessage `json:"part"`
		Response     map[string]any  `json:"response"`
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
	case "response.output_item.added":
		var item map[string]any
		_ = json.Unmarshal(ev.Item, &item)
		s.open = &codexItem{index: ev.OutputIndex, item: item}
	case "response.content_part.added":
		if o := s.open; o != nil {
			o.partOpen, o.refusal, o.contentIndex = true, false, ev.ContentIndex
			o.text.Reset()
		}
	case "response.content_part.done":
		if o := s.open; o != nil {
			var part any
			_ = json.Unmarshal(ev.Part, &part)
			o.parts, o.partOpen = append(o.parts, part), false
			o.text.Reset()
		}
	case "response.output_item.done":
		var item any
		_ = json.Unmarshal(ev.Item, &item)
		s.items = append(s.items, item)
		s.open = nil
	case "response.completed", "response.incomplete", "response.done":
		s.done = true
	}
	if err := s.out(data); err != nil {
		return err
	}
	switch ev.Type {
	case "response.output_text.delta", "response.reasoning_summary_text.delta", "response.reasoning_text.delta",
		"response.function_call_arguments.delta", "response.custom_tool_call_input.delta", "response.refusal.delta":
		if o := s.open; o != nil {
			switch ev.Type {
			case "response.output_text.delta", "response.refusal.delta":
				o.partOpen, o.contentIndex = true, ev.ContentIndex
				o.refusal = ev.Type == "response.refusal.delta"
			}
			o.text.WriteString(ev.Delta)
		}
		s.count.add(ev.Delta)
		if s.limit > 0 && s.count.tokens() > s.limit {
			return errOutputLimit
		}
	}
	return nil
}

// out passes one event on, under the tool name the buyer knows.
func (s *codexStream) out(data []byte) error {
	if s.renamed && bytes.Contains(data, []byte(reservedToolAlias)) {
		data = []byte(strings.ReplaceAll(string(data), `"name":"`+reservedToolAlias+`"`, `"name":"`+reservedTool+`"`))
	}
	return s.emit(append([]byte(nil), data...))
}

// send passes on an event made here, numbered after the backend's.
func (s *codexStream) send(ev map[string]any) error {
	s.seq++
	ev["sequence_number"] = s.seq
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	return s.out(b)
}

// closeOpen ends the item the cut interrupted, as the API ends one that
// reached the output limit: the events that close its text or arguments,
// then the item with status incomplete. It returns the item, or nil when
// there is none to give: unfinished reasoning has no encrypted content yet,
// and a later request could not send it back.
func (s *codexStream) closeOpen() (map[string]any, error) {
	o := s.open
	s.open = nil
	if o == nil || o.item == nil {
		return nil, nil
	}
	item, text := o.item, o.text.String()
	at := func(typ string) map[string]any {
		return map[string]any{"type": typ, "item_id": item["id"], "output_index": o.index}
	}
	switch item["type"] {
	case "message":
		parts := o.parts
		if parts == nil {
			parts = []any{}
		}
		if o.partOpen {
			done, part := at("response.output_text.done"), map[string]any{"type": "output_text", "text": text, "annotations": []any{}}
			done["text"] = text
			if o.refusal {
				done, part = at("response.refusal.done"), map[string]any{"type": "refusal", "refusal": text}
				done["refusal"] = text
			}
			done["content_index"] = o.contentIndex
			if err := s.send(done); err != nil {
				return nil, err
			}
			partDone := at("response.content_part.done")
			partDone["content_index"], partDone["part"] = o.contentIndex, part
			if err := s.send(partDone); err != nil {
				return nil, err
			}
			parts = append(parts, part)
		}
		item["content"] = parts
	case "function_call":
		done := at("response.function_call_arguments.done")
		done["arguments"], item["arguments"] = text, text
		if err := s.send(done); err != nil {
			return nil, err
		}
	case "custom_tool_call":
		done := at("response.custom_tool_call_input.done")
		done["input"], item["input"] = text, text
		if err := s.send(done); err != nil {
			return nil, err
		}
	default:
		return nil, nil
	}
	item["status"] = "incomplete"
	return item, s.send(map[string]any{"type": "response.output_item.done", "output_index": o.index, "item": item})
}

// end closes a stream cut at the output limit: the open item, then
// response.incomplete with the output so far and the usage of what was sent.
func (s *codexStream) end() error {
	items := s.items
	if items == nil {
		items = []any{}
	}
	item, err := s.closeOpen()
	if err != nil {
		return err
	}
	if item != nil {
		items = append(items, item)
	}
	r := s.response
	if r == nil {
		r = map[string]any{"id": "resp_" + randomHex(12), "object": "response", "created_at": time.Now().Unix()}
	}
	r["output"] = items
	out := s.count.tokens()
	r["usage"] = map[string]any{"input_tokens": s.estPrompt, "output_tokens": out, "total_tokens": s.estPrompt + out,
		"input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens_details": map[string]any{"reasoning_tokens": 0}}
	r["status"] = "incomplete"
	r["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
	return s.send(map[string]any{"type": "response.incomplete", "response": r})
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
		return st.end()
	case err != nil:
		return err
	case !st.done:
		// The backend's stream stopped without its terminal event: the
		// answer is not whole, and is not passed off as one.
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

// retryWithoutReasoning sends body, and again without what the upstream
// could not decrypt when it refused the request for its encrypted content:
// reasoning only decrypts at the account that made it, and a conversation
// that moved from another seller carries that seller's. The reasoning items
// go first; if the request is refused again, so do the backend's compaction
// items (those sharetoken made hold a readable summary and stay).
func retryWithoutReasoning(body []byte, do func([]byte) error) error {
	err := do(body)
	for _, strip := range []func([]byte) ([]byte, bool){withoutReasoning, withoutForeignCompaction} {
		var se *node.StatusError
		if !errors.As(err, &se) || se.Status != http.StatusBadRequest || !strings.Contains(strings.ToLower(se.Body), "encrypted") {
			return err
		}
		if stripped, ok := strip(body); ok {
			body = stripped
			err = do(body)
		}
	}
	return err
}

// withoutReasoning removes the reasoning items from a Responses request's input.
func withoutReasoning(body []byte) ([]byte, bool) {
	return withoutItems(body, func(m map[string]any) bool { return m["type"] == "reasoning" })
}

// withoutForeignCompaction removes from a Responses request's input the
// compaction items a model server made: opaque to every account but its own.
func withoutForeignCompaction(body []byte) ([]byte, bool) {
	return withoutItems(body, func(m map[string]any) bool {
		if m["type"] != "compaction" && m["type"] != "compaction_summary" {
			return false
		}
		enc, _ := m["encrypted_content"].(string)
		return !apicompat.IsConverted(enc)
	})
}

// withoutItems removes the input items drop accepts; false when there are none.
func withoutItems(body []byte, drop func(map[string]any) bool) ([]byte, bool) {
	req, err := decodeObject(body)
	if err != nil {
		return nil, false
	}
	input, _ := req["input"].([]any)
	var out []any
	for _, it := range input {
		if m, _ := it.(map[string]any); m != nil && drop(m) {
			continue
		}
		out = append(out, it)
	}
	if len(out) == len(input) {
		return nil, false
	}
	if out == nil {
		out = []any{}
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
