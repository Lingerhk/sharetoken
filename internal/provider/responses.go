package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
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
	// The backend checks tool schemas' regular expressions as Python does.
	cleanToolPatterns(req)
	// The backend cannot read a compaction item sharetoken made (from
	// another model's summary): it becomes the summary again.
	if input, ok := req["input"].([]any); ok {
		declared := additionalToolsIDs(input)
		out := input[:0]
		for _, it := range input {
			m, _ := it.(map[string]any)
			// Nothing is stored (store=false), so the backend finds no item
			// by its ID: a reference to one cannot be resolved, and an item
			// sent whole is refused for the ID it had in an earlier answer
			// ("Item with id 'rs_…' not found"). Codex sends none, but for
			// the additional_tools declarations of this same request: their
			// IDs name what is declared here, not something stored, and
			// without them the backend's native tool discovery falls back to
			// plain function calls.
			if m["type"] == "item_reference" {
				id, _ := m["id"].(string)
				if to, ok := declared[id]; ok {
					m["id"] = to
					out = append(out, it)
				}
				continue
			}
			if m["type"] == "additional_tools" {
				if id, ok := m["id"].(string); ok {
					m["id"] = declared[id]
				}
			} else {
				delete(m, "id")
			}
			// Context Codex gives from outside the conversation, as an
			// output without a call: the backend refuses that over HTTP.
			if text, ok := codexExternalContext(m); ok {
				it = map[string]any{"type": "message", "role": "user", "content": []any{
					map[string]any{"type": "input_text", "text": text}}}
				out = append(out, it)
				continue
			}
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

// additionalToolsIDs maps the ID of each additional_tools item in input to
// the one it goes with: a UUID-shaped one (at_ and 8-4-4-4-12 hex digits)
// as the 32 digits without dashes, which is how the backend takes it, the
// others as they are.
func additionalToolsIDs(input []any) map[string]string {
	ids := map[string]string{}
	for _, it := range input {
		if m, _ := it.(map[string]any); m["type"] == "additional_tools" {
			if id, ok := m["id"].(string); ok {
				ids[id] = compactToolsID(id)
			}
		}
	}
	return ids
}

func compactToolsID(id string) string {
	if len(id) != 39 || !strings.HasPrefix(id, "at_") || id[11] != '-' || id[16] != '-' || id[21] != '-' || id[26] != '-' {
		return id
	}
	hex := strings.ReplaceAll(id[3:], "-", "")
	for _, r := range hex {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return id
		}
	}
	return "at_" + hex
}

// codexExternalContext is apicompat.ExternalContext of an input item.
func codexExternalContext(m map[string]any) (string, bool) {
	str := func(k string) string { s, _ := m[k].(string); return s }
	callID := str("call_id")
	if v, ok := m["call_id"]; ok && v != nil && callID == "" {
		if _, isString := v.(string); !isString {
			return "", false // a call_id of another kind is no missing one
		}
	}
	typ, _ := m["type"].(string)
	return apicompat.ExternalContext(typ, str("namespace"), callID, str("name"), str("output"))
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
		return responsesStreamError(data)
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
	err = edgeReset(postSSE(ctx, url, header, out, st.event))
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
		return edgeReset(responseStatus(res, clip(strings.TrimSpace(string(raw)), 4096)))
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
//
// A request refused for a tool schema's regular expression (OpenAI reads
// them as Python does, and MCP servers write them for JavaScript) goes again
// first, without the expressions Python cannot read.
func retryWithoutReasoning(body []byte, do func([]byte) error) error {
	return retryWithoutReasoningNoting(body, do, nil)
}

// retryWithoutReasoningNoting is retryWithoutReasoning that tells refused,
// when the request without some items was answered, what was sent and what
// went in its place: those items are the ones the upstream could not read
// (refusedSeals remembers them).
func retryWithoutReasoningNoting(body []byte, do func([]byte) error, refused func(sent, left []byte)) error {
	err := do(body)
	if refusedPattern(err) {
		if cleaned, ok := withoutToolPatterns(body); ok {
			body = cleaned
			err = do(body)
		}
	}
	first := body
	stripped := false
	for _, strip := range []func([]byte) ([]byte, bool){withoutReasoning, withoutForeignCompaction} {
		if !refusedForeignReasoning(err) {
			break
		}
		if less, ok := strip(body); ok {
			body, stripped = less, true
			err = do(body)
		}
	}
	if stripped && err == nil && refused != nil {
		refused(first, body)
	}
	return err
}

// foreignReasoning is how upstreams refuse encrypted content another account
// made: OpenAI's "The encrypted content for item rs_… could not be verified"
// (invalid_encrypted_content), and "Encrypted content could not be decrypted
// or parsed" for another account's web search; xAI's "Could not decrypt the
// provided encrypted_content"; Azure's "invalid encrypted summary"; and xAI's
// "Could not decode the compaction blob", which says nothing of encrypted
// content.
var foreignReasoning = regexp.MustCompile(`(?i)invalid_encrypted_content|invalid[ _]encrypted[ _](summary|content)|encrypted[ _]content.{0,80}could not be (verified|decrypted)|could not (decrypt|verify).{0,40}encrypted[ _]content|could not (decode|decrypt).{0,20}compaction[ _]blob`)

// refusedForeignReasoning tells whether err is a refusal of the request's
// encrypted content.
func refusedForeignReasoning(err error) bool {
	var se *node.StatusError
	if !errors.As(err, &se) || se.Status != http.StatusBadRequest {
		return false
	}
	return foreignReasoning.MatchString(se.Body) || strings.Contains(strings.ToLower(se.Body), "encrypted")
}

// refusedPattern tells whether err is a refusal of a tool schema's regular
// expression.
func refusedPattern(err error) bool {
	var se *node.StatusError
	if !errors.As(err, &se) || se.Status != http.StatusBadRequest {
		return false
	}
	b := strings.ToLower(se.Body)
	return strings.Contains(b, "regex") || strings.Contains(b, "regular expression") ||
		strings.Contains(b, "look-behind") || strings.Contains(b, "lookbehind") ||
		strings.Contains(b, "pattern") && strings.Contains(b, "invalid") && strings.Contains(b, "schema")
}

// responsesStreamError is the error a Responses stream's error or
// response.failed event stands for. Most are the server's failure (502),
// but some say what the buyer should do, and keep the upstream's code in
// a JSON body as an HTTP answer would carry it:
//
//   - context_length_exceeded, context_too_large: 400. The conversation is
//     too long for the model on any seller; the client compacts it when it
//     reads that code (Codex does), and another seller would only fail the
//     same way.
//   - server_is_overloaded, slow_down: 503, the server sheds load for a while.
//   - rate limits and usage limits: 429, until the reset the error tells.
func responsesStreamError(data []byte) *node.StatusError {
	var ev struct {
		Type     string         `json:"type"`
		Code     any            `json:"code"`
		Message  any            `json:"message"`
		Error    map[string]any `json:"error"`
		Response struct {
			Error map[string]any `json:"error"`
		} `json:"response"`
	}
	_ = json.Unmarshal(data, &ev)
	// The error is the event's error, the failed response's, or the event
	// itself (the API's error event has code and message at its top).
	e := ev.Error
	if errCode(e) == "" && errCode(ev.Response.Error) != "" {
		e = ev.Response.Error
	}
	if errCode(e) == "" && ev.Code != nil {
		e = map[string]any{"code": ev.Code, "message": ev.Message}
	}
	code := errCode(e)
	msg, _ := e["message"].(string)
	text := strings.ToLower(code + " " + msg)
	status := http.StatusBadGateway
	switch {
	case code == "context_length_exceeded" || code == "context_too_large" ||
		strings.Contains(text, "context_length_exceeded") || strings.Contains(text, "context_too_large"):
		status = http.StatusBadRequest
	case code == "server_is_overloaded" || code == "slow_down":
		status = http.StatusServiceUnavailable
	case strings.Contains(code, "rate_limit") || strings.Contains(code, "usage_limit"):
		status = http.StatusTooManyRequests
	default:
		_, m := eventType(data)
		return &node.StatusError{Status: http.StatusBadGateway, Body: clip(ev.Type+": "+m, 300)}
	}
	out := make(map[string]any, len(e))
	for k, v := range e {
		out[k] = v
	}
	out["code"], out["message"] = code, clip(msg, 2000)
	body, _ := json.Marshal(map[string]any{"error": out})
	se := &node.StatusError{Status: status, Body: string(body)}
	if status == http.StatusTooManyRequests {
		// limitReset reads the reset of a limit by the error's type, which
		// in a stream may come as its code only.
		if code == "usage_limit_reached" || code == "rate_limit_exceeded" {
			out["type"] = code
		}
		doc, _ := json.Marshal(map[string]any{"error": out})
		se.RetryAt = limitReset(nil, string(doc), timeNow())
	}
	return se
}

// errCode is an error object's code, or its type when it has none.
func errCode(e map[string]any) string {
	if c, ok := e["code"].(string); ok && strings.TrimSpace(c) != "" {
		return strings.ToLower(strings.TrimSpace(c))
	}
	t, _ := e["type"].(string)
	return strings.ToLower(strings.TrimSpace(t))
}

// ChatGPT's edge (Envoy) answers for the backend when it closed the
// connection before replying: 503 "upstream connect error or
// disconnect/reset before headers", or, when the request was too large for
// Envoy to keep for its own retry, 507 "exceeded request buffer limit while
// retrying upstream". Both are that same reset, which passes when asked
// again; the 507 is read as a 503 so that it is.
var edgeUnbuffered = regexp.MustCompile(`(?i)exceeded request buffer limit while retrying`)

func edgeReset(err error) error {
	var se *node.StatusError
	if errors.As(err, &se) && se.Status == http.StatusInsufficientStorage && edgeUnbuffered.MatchString(se.Body) {
		se.Status = http.StatusServiceUnavailable
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
// them. Codex's external context, an output without a call it refuses,
// goes as the user message it stands for.
func dropConvertedReasoning(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"sharetoken-`)) && !bytes.Contains(body, []byte(`"agent"`)) {
		return body
	}
	req, err := decodeObject(body)
	if err != nil {
		return body
	}
	input, _ := req["input"].([]any)
	out := input[:0]
	for _, it := range input {
		m, _ := it.(map[string]any)
		if m["type"] == "reasoning" {
			if enc, _ := m["encrypted_content"].(string); apicompat.IsConverted(enc) {
				continue
			}
		}
		if text, ok := codexExternalContext(m); ok {
			it = map[string]any{"type": "message", "role": "user", "content": []any{
				map[string]any{"type": "input_text", "text": text}}}
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

// OpenAI checks a tool schema's "pattern" with Python's re, which refuses
// what JavaScript's and other engines take: lookaround, (?<name>…) and
// (?'name'…) groups, and backslash escapes of letters it does not know
// (\p{L}, \R, …). Clients write such schemas (zod for MCP servers), and the
// whole request is refused for one. Such a pattern is dropped, and so is a
// patternProperties key that is one; an object that lost such a key no
// longer refuses other properties (additionalProperties: false would now
// refuse what the dropped key allowed). As sub2api does.

// unsupportedPattern tells whether Python's re refuses pattern.
func unsupportedPattern(pattern string) bool {
	inClass := false
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '\\':
			if i+1 >= len(pattern) {
				return true
			}
			c := pattern[i+1]
			if (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') && !strings.ContainsRune("abfnrtvdDsSwWAZBNuUx", rune(c)) {
				return true
			}
			i++
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '(':
			if inClass || i+1 >= len(pattern) || pattern[i+1] != '?' {
				continue
			}
			if i+2 >= len(pattern) {
				return true
			}
			switch pattern[i+2] {
			case '=', '!', '<', '\'':
				return true
			}
		}
	}
	return false
}

// cleanToolPatterns removes from a decoded Responses request's tool schemas
// the patterns Python cannot read: those of its tools, and of the tools
// additional_tools input items declare. It tells whether it changed any.
func cleanToolPatterns(req map[string]any) bool {
	changed := patternTools(req["tools"])
	input, _ := req["input"].([]any)
	for _, it := range input {
		if m, _ := it.(map[string]any); m != nil {
			changed = patternTools(m["tools"]) || changed
		}
	}
	return changed
}

// withoutToolPatterns is cleanToolPatterns of a request body; false when
// nothing changed.
func withoutToolPatterns(body []byte) ([]byte, bool) {
	req, err := decodeObject(body)
	if err != nil || !cleanToolPatterns(req) {
		return nil, false
	}
	b, err := json.Marshal(req)
	return b, err == nil
}

// patternTools cleans a list of tools: a function's parameters (also under
// "function", as chat writes it, or input_schema), and the tools of a
// namespace.
func patternTools(v any) bool {
	tools, _ := v.([]any)
	changed := false
	for _, t := range tools {
		m, _ := t.(map[string]any)
		if m == nil {
			continue
		}
		for _, k := range []string{"parameters", "input_schema"} {
			changed = patternSchema(m[k], 0) || changed
		}
		if f, _ := m["function"].(map[string]any); f != nil {
			changed = patternSchema(f["parameters"], 0) || changed
			changed = patternTools(f["tools"]) || changed
		}
		changed = patternTools(m["tools"]) || changed
	}
	return changed
}

// maxSchemaDepth bounds the walk of a schema a client wrote.
const maxSchemaDepth = 128

// patternSchema cleans one schema and those inside it. It does not look into
// keywords whose values are instances, not schemas (default, examples,
// const, enum).
func patternSchema(v any, depth int) bool {
	s, _ := v.(map[string]any)
	if s == nil || depth > maxSchemaDepth {
		return false
	}
	changed := false
	if p, ok := s["pattern"].(string); ok && unsupportedPattern(p) {
		delete(s, "pattern")
		changed = true
	}
	if pp, ok := s["patternProperties"].(map[string]any); ok {
		lost := false
		for k, sub := range pp {
			if unsupportedPattern(k) {
				delete(pp, k)
				lost = true
				continue
			}
			changed = patternSchema(sub, depth+1) || changed
		}
		if lost {
			changed = true
			if len(pp) == 0 {
				delete(s, "patternProperties")
			}
			if ap, ok := s["additionalProperties"]; ok && ap != true {
				s["additionalProperties"] = true
			}
		}
	}
	for _, k := range []string{"additionalProperties", "additionalItems", "contains", "not", "if", "then", "else",
		"propertyNames", "unevaluatedProperties", "unevaluatedItems", "contentSchema"} {
		changed = patternSchema(s[k], depth+1) || changed
	}
	for _, k := range []string{"items", "anyOf", "oneOf", "allOf", "prefixItems"} {
		switch sub := s[k].(type) {
		case map[string]any:
			changed = patternSchema(sub, depth+1) || changed
		case []any:
			for _, x := range sub {
				changed = patternSchema(x, depth+1) || changed
			}
		}
	}
	for _, k := range []string{"properties", "$defs", "definitions", "dependentSchemas", "dependencies"} {
		if m, ok := s[k].(map[string]any); ok {
			for _, sub := range m {
				changed = patternSchema(sub, depth+1) || changed
			}
		}
	}
	return changed
}
