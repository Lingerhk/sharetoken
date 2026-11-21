package apicompat

// The Messages side of a converted stream: the events a Messages client
// (Claude Code) gets, built from a Responses or chat upstream.
//
// A Messages stream has one content block open at a time, numbered from zero
// in order. Upstreams do not always keep to that (chat tool calls may
// interleave, a Responses stream may send an item's text only on its done
// event, a reasoning item may hold nothing until it is done), so blocks go
// through msgWriter: a queue of blocks where only the head is live. Deltas for
// the head are sent at once; deltas for a block behind it are kept and sent
// when it becomes the head. Nothing an upstream sends is dropped, and the
// client always sees a well-formed sequence.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// Block kinds.
const (
	msgKindText     = "text"
	msgKindThinking = "thinking"
	msgKindToolUse  = "tool_use"
)

// msgSignaturePrefix marks a thinking block signature that carries a Responses
// upstream's encrypted reasoning (encrypted_content), so that the next request
// can give it back as a reasoning item, and so that it is never mistaken for
// an Anthropic signature.
const msgSignaturePrefix = "sharetoken-reasoning-v1:"

// IsConvertedThinkingSignature reports whether a Messages thinking block
// signature was made by this package from a Responses upstream's encrypted
// reasoning. Such a signature (like the empty signature of a thinking block
// converted from a chat upstream) means nothing to an Anthropic upstream,
// which rejects it: a request passed through to one should drop those
// thinking blocks.
func IsConvertedThinkingSignature(sig string) bool {
	return strings.HasPrefix(sig, msgSignaturePrefix)
}

func msgRandomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func msgNewID() string { return "msg_" + msgRandomHex(12) }

// msgUsage is a Messages usage: input_tokens excludes the cached tokens.
type msgUsage struct {
	Input         int
	Output        int
	CacheRead     int
	CacheCreation int
}

func (u msgUsage) json() map[string]int {
	return map[string]int{
		"input_tokens":                u.Input,
		"cache_creation_input_tokens": u.CacheCreation,
		"cache_read_input_tokens":     u.CacheRead,
		"output_tokens":               u.Output,
	}
}

// msgUsageFromOpenAI reads a Responses or chat usage object. Both count
// cached tokens inside input_tokens (prompt_tokens); Messages does not, so
// cache reads and writes are taken out. Cache writes are not an OpenAI field,
// and some chat servers report cache reads outside the details: the spellings
// used by OpenAI-compatible upstreams (and sharetoken) are read, nested ones
// first.
func msgUsageFromOpenAI(raw json.RawMessage) (msgUsage, bool) {
	var u struct {
		InputTokens      *int `json:"input_tokens"`
		OutputTokens     *int `json:"output_tokens"`
		PromptTokens     *int `json:"prompt_tokens"`
		CompletionTokens *int `json:"completion_tokens"`

		CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
		CacheWriteInputTokens    *int `json:"cache_write_input_tokens"`
		CacheCreationTokens      *int `json:"cache_creation_tokens"`
		CacheWriteTokens         *int `json:"cache_write_tokens"`

		// Cache reads outside the details: sub2api's spellings, Kimi's
		// cached_tokens, DeepSeek's prompt_cache_hit_tokens.
		CacheReadInputTokens *int `json:"cache_read_input_tokens"`
		CacheReadTokens      *int `json:"cache_read_tokens"`
		CachedTokens         *int `json:"cached_tokens"`
		PromptCacheHitTokens *int `json:"prompt_cache_hit_tokens"`

		InputDetails  *msgOpenAITokenDetails `json:"input_tokens_details"`
		PromptDetails *msgOpenAITokenDetails `json:"prompt_tokens_details"`
	}
	if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &u) != nil {
		return msgUsage{}, false
	}
	pick := func(ps ...*int) int {
		for _, p := range ps {
			if p != nil {
				return max(*p, 0)
			}
		}
		return 0
	}
	total := pick(u.InputTokens, u.PromptTokens)
	out := msgUsage{Output: pick(u.OutputTokens, u.CompletionTokens)}
	var nested []*int
	for _, d := range []*msgOpenAITokenDetails{u.InputDetails, u.PromptDetails} {
		if d != nil {
			if out.CacheRead == 0 && d.CachedTokens != nil {
				out.CacheRead = max(*d.CachedTokens, 0)
			}
			nested = append(nested, d.CacheWriteTokens, d.CacheCreationTokens)
		}
	}
	if out.CacheRead == 0 {
		out.CacheRead = firstPositive(u.CacheReadInputTokens, u.CacheReadTokens, u.CachedTokens, u.PromptCacheHitTokens)
	}
	out.CacheCreation = pick(append(nested, u.CacheCreationInputTokens, u.CacheWriteInputTokens,
		u.CacheCreationTokens, u.CacheWriteTokens)...)
	out.Input = max(total-out.CacheRead-out.CacheCreation, 0)
	return out, true
}

type msgOpenAITokenDetails struct {
	CachedTokens        *int `json:"cached_tokens"`
	CacheCreationTokens *int `json:"cache_creation_tokens"`
	CacheWriteTokens    *int `json:"cache_write_tokens"`
}

// msgBlock is one content block on its way to the client.
type msgBlock struct {
	key  string
	kind string
	id   string // tool_use
	name string // tool_use; empty until known
	// named is false for a chat tool call whose name has not arrived yet: the
	// block cannot start before it.
	named bool
	// hold keeps a tool's arguments until the block closes, so they can be
	// sanitized as a whole (see msgSanitizeToolInput).
	hold bool

	all      strings.Builder // everything added, sent or not
	pending  strings.Builder // added but not yet sent
	sig      string          // thinking: signature, sent before the stop
	finished bool            // the upstream is done with it
	live     bool            // its content_block_start was sent
	index    int
}

// msgWriter turns blocks into Messages events, collected in out.
type msgWriter struct {
	id    string
	model string
	usage msgUsage // what message_start carries

	out     [][]byte
	started bool // message_start sent
	stopped bool // message_stop sent
	next    int  // index of the next block
	queue   []*msgBlock
	byKey   map[string]*msgBlock

	blocks  int  // blocks started
	toolUse bool // a tool_use block was opened
	// held are the tool_use ids the client holds (clientID).
	held map[string]bool
}

func newMsgWriter(model string) *msgWriter {
	return &msgWriter{id: msgNewID(), model: model, byKey: map[string]*msgBlock{}}
}

func (w *msgWriter) put(v any) {
	b, err := json.Marshal(v)
	if err != nil { // only plain maps and strings are marshalled
		panic(err)
	}
	w.out = append(w.out, b)
}

// flush hands the collected events to emit.
func (w *msgWriter) flush(emit func([]byte) error) error {
	out := w.out
	w.out = nil
	for _, b := range out {
		if err := emit(b); err != nil {
			return err
		}
	}
	return nil
}

// start sends message_start. It comes with the first block (or the end of
// the message), not with the upstream's first event, so that a stream that
// fails before producing anything has sent the client nothing: the node can
// still answer with an error, or try elsewhere.
func (w *msgWriter) start() {
	if w.started {
		return
	}
	w.started = true
	w.put(map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            w.id,
			"type":          "message",
			"role":          "assistant",
			"model":         w.model,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         w.usage.json(),
		},
	})
}

// block returns the block with key, nil when there is none (or it was closed).
func (w *msgWriter) block(key string) *msgBlock { return w.byKey[key] }

// open adds a block at the end of the queue; an open block with the same key
// is returned as it is.
func (w *msgWriter) open(key, kind string) *msgBlock {
	if b := w.byKey[key]; b != nil {
		return b
	}
	b := &msgBlock{key: key, kind: kind, named: true}
	if kind == msgKindToolUse {
		w.toolUse = true
	}
	w.byKey[key] = b
	w.queue = append(w.queue, b)
	return b
}

// add appends content to a block: text, thinking or tool arguments.
func (w *msgWriter) add(b *msgBlock, s string) {
	if s == "" || b == nil {
		return
	}
	b.all.WriteString(s)
	b.pending.WriteString(s)
	w.pump()
}

func (w *msgWriter) finish(b *msgBlock) {
	if b == nil {
		return
	}
	b.finished = true
	w.pump()
}

func (w *msgWriter) finishAll() {
	for _, b := range w.queue {
		b.finished = true
	}
	w.pump()
}

// pump starts and closes blocks at the head of the queue as far as it can,
// and sends the head's pending content.
func (w *msgWriter) pump() {
	for len(w.queue) > 0 {
		b := w.queue[0]
		if !b.live {
			empty := b.all.Len() == 0 && b.sig == ""
			switch {
			case b.kind != msgKindToolUse && empty && b.finished:
				// Nothing ever arrived: no block at all.
				w.drop(b)
				continue
			case b.kind != msgKindToolUse && empty, !b.named && !b.finished:
				return // wait for content (or a name)
			}
			w.startBlock(b)
		}
		w.sendPending(b)
		if !b.finished {
			return
		}
		w.closeBlock(b)
	}
}

func (w *msgWriter) drop(b *msgBlock) {
	w.queue = w.queue[1:]
	delete(w.byKey, b.key)
}

// dropCut removes the tool_use blocks that have not started and whose input
// is not complete JSON, for an answer cut off by its output limit or a content
// filter. A block that has started cannot be taken back: it ends with the
// input it has, as a Messages upstream ends one at max_tokens.
func (w *msgWriter) dropCut() {
	keep := w.queue[:0]
	for _, b := range w.queue {
		if b.kind == msgKindToolUse && !b.live && !json.Valid([]byte(b.all.String())) {
			delete(w.byKey, b.key)
			continue
		}
		keep = append(keep, b)
	}
	w.queue = keep
}

func (w *msgWriter) startBlock(b *msgBlock) {
	w.start()
	b.live = true
	b.index = w.next
	w.blocks++
	var cb map[string]any
	switch b.kind {
	case msgKindText:
		cb = map[string]any{"type": "text", "text": ""}
	case msgKindThinking:
		cb = map[string]any{"type": "thinking", "thinking": "", "signature": ""}
	case msgKindToolUse:
		b.id = w.clientID(b.id)
		cb = map[string]any{"type": "tool_use", "id": b.id, "name": b.name, "input": map[string]any{}}
	}
	w.put(map[string]any{"type": "content_block_start", "index": b.index, "content_block": cb})
}

func (w *msgWriter) delta(b *msgBlock, d map[string]any) {
	w.put(map[string]any{"type": "content_block_delta", "index": b.index, "delta": d})
}

func (w *msgWriter) sendPending(b *msgBlock) {
	if b.pending.Len() == 0 || (b.hold && !b.finished) {
		return
	}
	s := b.pending.String()
	b.pending.Reset()
	switch b.kind {
	case msgKindText:
		w.delta(b, map[string]any{"type": "text_delta", "text": s})
	case msgKindThinking:
		w.delta(b, map[string]any{"type": "thinking_delta", "thinking": s})
	case msgKindToolUse:
		if b.hold {
			s = msgSanitizeToolInput(b.name, s)
		}
		w.delta(b, map[string]any{"type": "input_json_delta", "partial_json": s})
	}
}

func (w *msgWriter) closeBlock(b *msgBlock) {
	w.queue = w.queue[1:]
	delete(w.byKey, b.key)
	if b.kind == msgKindToolUse && b.all.Len() == 0 {
		// Clients build the input from the deltas alone.
		w.delta(b, map[string]any{"type": "input_json_delta", "partial_json": "{}"})
	}
	if b.kind == msgKindThinking && b.sig != "" {
		w.delta(b, map[string]any{"type": "signature_delta", "signature": b.sig})
	}
	w.put(map[string]any{"type": "content_block_stop", "index": b.index})
	w.next++
}

// stop closes every block and ends the message.
func (w *msgWriter) stop(reason string, usage msgUsage) {
	if w.stopped {
		return
	}
	w.finishAll()
	w.start()
	w.put(map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": reason, "stop_sequence": nil},
		"usage": usage.json(),
	})
	w.put(map[string]any{"type": "message_stop"})
	w.stopped = true
}

// msgSanitizeToolInput fixes tool input that OpenAI models fill in a way the
// client rejects. Claude Code's Read tool fails on pages:"" (models fill
// every optional string), so an empty pages is removed.
func msgSanitizeToolInput(name, args string) string {
	if name != "Read" {
		return args
	}
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(args), &m) != nil {
		return args
	}
	if p, ok := m["pages"]; !ok || string(p) != `""` {
		return args
	}
	delete(m, "pages")
	b, err := json.Marshal(m)
	if err != nil {
		return args
	}
	return string(b)
}
