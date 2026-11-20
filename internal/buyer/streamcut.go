package buyer

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"sharetoken/internal/protocol"
)

// loopCutError ends a job whose answer the loop guard cut: the caller has
// what was streamed, closed as a length stop, and the node stops the model
// and bills what it delivered.
type loopCutError struct{ trip loopTrip }

func (e *loopCutError) Error() string { return "the answer was stopped: " + e.trip.String() }

// streamGuard watches a streamed answer for a loop (loopGuard) and keeps
// what is needed to end it cleanly in the caller's API (streamCloser).
type streamGuard struct {
	loops  loopGuard
	closer streamCloser
	trip   *loopTrip
}

func newStreamGuard(api string, codex bool) *streamGuard {
	return &streamGuard{closer: streamCloser{api: protocol.NormalAPI(api), codex: codex}}
}

// wrap hands each event to deliver, then fails the job with a
// *loopCutError once the answer has become a loop.
func (g *streamGuard) wrap(deliver func([]byte) error) func([]byte) error {
	return func(p []byte) error {
		if err := deliver(p); err != nil {
			return err
		}
		g.closer.observe(p)
		if t, ok := g.loops.feed(p); ok {
			g.trip = &t
			return &loopCutError{trip: t}
		}
		return nil
	}
}

// streamCloser follows a streamed answer and gives the events that end it
// where it is, as a model that reached its output limit would: chat's
// finish_reason length, Messages' open blocks stopped and stop_reason
// max_tokens, Responses' open items done and response.incomplete with
// max_output_tokens.
type streamCloser struct {
	api string
	// codex: the caller is Codex, which takes response.incomplete for a
	// broken stream and asks again (paying again for the same loop), so
	// its answer ends with response.completed carrying the incomplete
	// response instead.
	codex bool
	// chat: the last chunk's id, model and created
	chatID, chatModel string
	chatCreated       int64
	// Messages: the content blocks started and not stopped
	openBlocks map[int]bool
	// Responses: the response as created, the items under way and done
	response map[string]any
	items    map[int]*openItem
	done     map[int]json.RawMessage
}

type openItem struct {
	item map[string]any
	text []byte
}

func (c *streamCloser) observe(p []byte) {
	var ev struct {
		Type        string          `json:"type"`
		ID          string          `json:"id"`
		Model       string          `json:"model"`
		Created     int64           `json:"created"`
		Index       int             `json:"index"`
		OutputIndex int             `json:"output_index"`
		Delta       json.RawMessage `json:"delta"`
		Item        json.RawMessage `json:"item"`
		Response    map[string]any  `json:"response"`
	}
	if json.Unmarshal(p, &ev) != nil {
		return
	}
	switch c.api {
	case protocol.APIChat:
		if ev.ID != "" {
			c.chatID, c.chatModel, c.chatCreated = ev.ID, ev.Model, ev.Created
		}
	case protocol.APIMessages:
		if c.openBlocks == nil {
			c.openBlocks = map[int]bool{}
		}
		switch ev.Type {
		case "content_block_start":
			c.openBlocks[ev.Index] = true
		case "content_block_stop":
			delete(c.openBlocks, ev.Index)
		}
	case protocol.APIResponses:
		c.observeResponses(ev.Type, ev.OutputIndex, ev.Delta, ev.Item, ev.Response)
	}
}

func (c *streamCloser) observeResponses(typ string, idx int, delta, item json.RawMessage, resp map[string]any) {
	if c.items == nil {
		c.items, c.done = map[int]*openItem{}, map[int]json.RawMessage{}
	}
	switch typ {
	case "response.created", "response.in_progress":
		if resp != nil {
			c.response = resp
		}
	case "response.output_item.added":
		var it map[string]any
		if json.Unmarshal(item, &it) == nil {
			c.items[idx] = &openItem{item: it}
		}
	case "response.output_text.delta", "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if it := c.items[idx]; it != nil {
			it.text = append(it.text, jsonString(delta)...)
		}
	case "response.output_item.done":
		delete(c.items, idx)
		c.done[idx] = append(json.RawMessage(nil), item...)
	}
}

// closing is the events that end the answer as a length stop.
func (c *streamCloser) closing() [][]byte {
	switch c.api {
	case protocol.APIChat:
		b, _ := json.Marshal(map[string]any{"id": c.chatID, "object": "chat.completion.chunk", "created": c.chatCreated, "model": c.chatModel,
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "length"}}})
		return [][]byte{b}
	case protocol.APIMessages:
		var out [][]byte
		for _, i := range slices.Sorted(maps.Keys(c.openBlocks)) {
			b, _ := json.Marshal(map[string]any{"type": "content_block_stop", "index": i})
			out = append(out, b)
		}
		delta, _ := json.Marshal(map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "max_tokens", "stop_sequence": nil}, "usage": map[string]any{"output_tokens": 0}})
		return append(out, delta, []byte(`{"type":"message_stop"}`))
	case protocol.APIResponses:
		return c.closeResponses()
	}
	return nil
}

// closeResponses ends the items under way (a message with its text so far,
// reasoning with its summary so far; a call cut off is dropped: half its
// arguments would be run) and the response as incomplete.
func (c *streamCloser) closeResponses() [][]byte {
	var out [][]byte
	for _, idx := range slices.Sorted(maps.Keys(c.items)) {
		it := c.items[idx]
		item := maps.Clone(it.item)
		switch item["type"] {
		case "message":
			item["status"] = "incomplete"
			item["content"] = []any{map[string]any{"type": "output_text", "text": string(it.text), "annotations": []any{}}}
		case "reasoning":
			if len(it.text) > 0 {
				item["summary"] = []any{map[string]any{"type": "summary_text", "text": string(it.text)}}
			}
		default:
			continue
		}
		b, _ := json.Marshal(item)
		c.done[idx] = b
		ev, _ := json.Marshal(map[string]any{"type": "response.output_item.done", "output_index": idx, "item": json.RawMessage(b)})
		out = append(out, ev)
	}
	resp := maps.Clone(c.response)
	if resp == nil {
		id := make([]byte, 12)
		_, _ = rand.Read(id)
		resp = map[string]any{"id": "resp_" + hex.EncodeToString(id), "object": "response"}
	}
	output := make([]json.RawMessage, 0, len(c.done))
	for _, idx := range slices.Sorted(maps.Keys(c.done)) {
		output = append(output, c.done[idx])
	}
	resp["status"] = "incomplete"
	resp["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
	resp["output"] = output
	typ := "response.incomplete"
	if c.codex {
		typ = "response.completed"
	}
	ev, _ := json.Marshal(map[string]any{"type": typ, "response": resp})
	return append(out, ev)
}

// HeaderLoopGuard says on an answer the loop guard ended or gave what it
// did: "tool_loop" (answered here, sent nowhere) or "text_loop".
const HeaderLoopGuard = "X-Sharetoken-Loop-Guard"

// guardStream is deliver behind the loop guard when it is on (the guard is
// nil when it is off); codex says the caller is Codex (see streamCloser).
func (p *Proxy) guardStream(api string, codex bool, deliver func([]byte) error) (func([]byte) error, *streamGuard) {
	if p.NoLoopGuard {
		return deliver, nil
	}
	g := newStreamGuard(api, codex)
	return g.wrap(deliver), g
}

// cut reports whether err is the guard having stopped the answer.
func (g *streamGuard) cut(err error) bool {
	var lc *loopCutError
	return g != nil && g.trip != nil && errors.As(err, &lc)
}

// endCut ends a stream the guard stopped: the closing events of a length
// stop, a comment saying why, and chat's [DONE].
func endCut(out *sseWriter, api string, g *streamGuard) {
	slog.Warn("loop guard stopped an answer", "component", "buyer", "api", api, "loop", g.trip.String())
	for _, ev := range g.closer.closing() {
		_ = out.event(ev)
	}
	_, _ = fmt.Fprintf(out.w, ": sharetoken loop_guard=text_loop %s\n\n", strings.ReplaceAll(g.trip.String(), "\n", " "))
	if api == protocol.APIChat {
		_, _ = fmt.Fprint(out.w, "data: [DONE]\n\n")
	}
	out.flush()
}
