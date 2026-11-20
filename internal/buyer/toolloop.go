package buyer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"sharetoken/internal/apicompat"
	"sharetoken/internal/protocol"
)

// A model can also loop across turns (after magpie's stoppedToolLoop): it
// asks for the same tool call, the agent runs it and sends back the same
// result, and the model asks for it again. Codex on a DeepSeek model read
// the same file 308 times in half an hour. Every turn of such a loop carries
// the whole conversation, so the loop is in the request itself: its newest
// turns are the same calls with the same arguments answered with the same
// results.
//
// A request whose last toolLoopRepeats turns are all that one exchange is
// answered here, in the caller's API, with a notice that ends the agent's
// turn, and is sent to no node: it costs nothing. A turn that got a
// different result (a poll whose answer moved, a file that changed) is
// progress and starts the count again, as does anything the user says.

// toolLoopRepeats is how many turns in a row of the same calls answered
// with the same results stop the next.
const toolLoopRepeats = 8

// toolResultMarks are what each API writes a tool result with, for a body
// with fewer than toolLoopRepeats of them to be let by unparsed.
var toolResultMarks = map[string][]byte{
	protocol.APIResponses: []byte(`_output"`),
	protocol.APIChat:      []byte(`"tool_call_id"`),
	protocol.APIMessages:  []byte(`"tool_result"`),
}

// turnPart is one thing said in a conversation, as the loop check reads it.
type turnPart struct {
	call   bool // a tool call (id, name, args)
	result bool // a tool result (id, text)
	id     string
	name   string
	args   string
	text   string
}

type turnMsg struct {
	role  string // "assistant" or "user" (the caller's side: results, words)
	parts []turnPart
}

// toolLoop is a conversation's newest exchange and how many turns in a row
// it was made.
type toolLoop struct {
	calls []turnPart
	turns int
}

func (l toolLoop) called() string {
	names := make([]string, 0, len(l.calls))
	for _, c := range l.calls {
		args := steadyArgs(c.args)
		if r := []rune(args); len(r) > 120 {
			args = string(r[:120]) + "…"
		}
		names = append(names, c.name+" "+args)
	}
	return strings.Join(names, "; ")
}

// notice is the answer the agent shows the user. It names neither
// sharetoken's internals nor a vendor, since it goes to the model with the
// next turn.
func (l toolLoop) notice() string {
	return fmt.Sprintf("This turn was stopped before it reached the model: the agent ran the same tool call %d times in a row and got the same result every time (%s). "+
		"The model is stuck repeating it, and each further turn would be one more paid request with no progress, so this one was not sent. "+
		"Tell the agent how to go on and the conversation continues.", l.turns, l.called())
}

// toolLoopOf is the loop body's conversation ends in, if any.
func toolLoopOf(api string, body []byte) (toolLoop, bool) {
	mark := toolResultMarks[protocol.NormalAPI(api)]
	if mark == nil || bytes.Count(body, mark) < toolLoopRepeats {
		return toolLoop{}, false
	}
	msgs, ok := conversation(protocol.NormalAPI(api), body)
	if !ok {
		return toolLoop{}, false
	}
	turns := exchanges(msgs)
	if len(turns) < toolLoopRepeats {
		return toolLoop{}, false
	}
	last := turns[len(turns)-1]
	if allWaits(last.calls) {
		return toolLoop{}, false
	}
	n := 0
	for i := len(turns) - 1; i >= 0 && turns[i].sum == last.sum; i-- {
		n++
	}
	if n < toolLoopRepeats {
		return toolLoop{}, false
	}
	return toolLoop{calls: last.calls, turns: n}, true
}

// conversation reads a request's turns in api.
func conversation(api string, body []byte) ([]turnMsg, bool) {
	switch api {
	case protocol.APIChat:
		return chatConversation(body)
	case protocol.APIMessages:
		return messagesConversation(body)
	case protocol.APIResponses:
		return responsesConversation(body)
	}
	return nil, false
}

func chatConversation(body []byte) ([]turnMsg, bool) {
	var r struct {
		Messages []struct {
			Role       string          `json:"role"`
			Content    json.RawMessage `json:"content"`
			ToolCallID string          `json:"tool_call_id"`
			ToolCalls  []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &r) != nil {
		return nil, false
	}
	var out []turnMsg
	for _, m := range r.Messages {
		switch m.Role {
		case "assistant":
			t := turnMsg{role: "assistant"}
			for _, c := range m.ToolCalls {
				t.parts = append(t.parts, turnPart{call: true, id: c.ID, name: c.Function.Name, args: c.Function.Arguments})
			}
			out = append(out, t)
		case "tool":
			out = append(out, turnMsg{role: "user", parts: []turnPart{{result: true, id: m.ToolCallID, text: string(m.Content)}}})
		default:
			out = append(out, turnMsg{role: "user", parts: []turnPart{{text: m.Role + "\x00" + string(m.Content)}}})
		}
	}
	return out, true
}

func messagesConversation(body []byte) ([]turnMsg, bool) {
	var r struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &r) != nil {
		return nil, false
	}
	var out []turnMsg
	for _, m := range r.Messages {
		t := turnMsg{role: "user"}
		if m.Role == "assistant" {
			t.role = "assistant"
		}
		var blocks []struct {
			Type      string          `json:"type"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
			ToolUseID string          `json:"tool_use_id"`
			Content   json.RawMessage `json:"content"`
			IsError   bool            `json:"is_error"`
			Text      string          `json:"text"`
		}
		if json.Unmarshal(m.Content, &blocks) != nil {
			t.parts = append(t.parts, turnPart{text: string(m.Content)})
			out = append(out, t)
			continue
		}
		for _, b := range blocks {
			switch b.Type {
			case "tool_use":
				t.parts = append(t.parts, turnPart{call: true, id: b.ID, name: b.Name, args: string(b.Input)})
			case "tool_result":
				text := string(b.Content)
				if b.IsError {
					text = "error\x00" + text
				}
				t.parts = append(t.parts, turnPart{result: true, id: b.ToolUseID, text: text})
			case "text":
				if t.role == "user" {
					t.parts = append(t.parts, turnPart{text: b.Text})
				}
			}
		}
		out = append(out, t)
	}
	return out, true
}

func responsesConversation(body []byte) ([]turnMsg, bool) {
	var r struct {
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(body, &r) != nil {
		return nil, false
	}
	var items []struct {
		Type      string          `json:"type"`
		Role      string          `json:"role"`
		Content   json.RawMessage `json:"content"`
		CallID    string          `json:"call_id"`
		Name      string          `json:"name"`
		Arguments string          `json:"arguments"`
		Input     string          `json:"input"`
		Output    json.RawMessage `json:"output"`
	}
	if json.Unmarshal(r.Input, &items) != nil {
		return nil, false
	}
	var out []turnMsg
	for _, it := range items {
		switch {
		case it.Type == "function_call" || it.Type == "custom_tool_call":
			out = append(out, turnMsg{role: "assistant", parts: []turnPart{{call: true, id: it.CallID, name: it.Name, args: it.Arguments + it.Input}}})
		case strings.HasSuffix(it.Type, "_output"):
			out = append(out, turnMsg{role: "user", parts: []turnPart{{result: true, id: it.CallID, text: string(it.Output)}}})
		case it.Type == "reasoning" || it.Role == "assistant":
			out = append(out, turnMsg{role: "assistant"})
		default:
			out = append(out, turnMsg{role: "user", parts: []turnPart{{text: it.Role + "\x00" + string(it.Content)}}})
		}
	}
	return out, true
}

// exchange is one turn of tool use: the assistant's calls and the results
// the agent sent back, summed without their ids, which differ every turn.
type exchange struct {
	sum   uint64
	calls []turnPart
}

// exchanges are the turns of tool use msgs end in, oldest first, back to
// the newest turn that was not one. Messages of one role in a row are one
// turn (a reasoning item and its call, a call's several outputs).
func exchanges(msgs []turnMsg) []exchange {
	var runs []turnMsg
	for _, m := range msgs {
		if n := len(runs); n > 0 && runs[n-1].role == m.role {
			runs[n-1].parts = append(runs[n-1].parts, m.parts...)
			continue
		}
		runs = append(runs, turnMsg{role: m.role, parts: slices.Clone(m.parts)})
	}
	var out []exchange
	for i := len(runs) - 1; i >= 1; i -= 2 {
		said, asked := runs[i], runs[i-1]
		if said.role != "user" || asked.role != "assistant" {
			break
		}
		e, ok := exchangeOf(asked.parts, said.parts)
		if !ok {
			break
		}
		out = append(out, e)
	}
	slices.Reverse(out)
	return out
}

// exchangeOf sums one turn: each call by its name and arguments with the
// result it got, and anything else the caller's side said beside them.
func exchangeOf(asked, said []turnPart) (exchange, bool) {
	var calls []turnPart
	for _, p := range asked {
		if p.call {
			calls = append(calls, p)
		}
	}
	results := map[string][]string{}
	var rest []string
	for _, p := range said {
		if p.result {
			results[p.id] = append(results[p.id], steadyResult(p.text))
		} else {
			rest = append(rest, p.text)
		}
	}
	if len(calls) == 0 || len(results) == 0 {
		return exchange{}, false
	}
	keys := make([]string, 0, len(calls)+1)
	for _, c := range calls {
		got := results[c.id]
		delete(results, c.id)
		keys = append(keys, c.name+"\x00"+steadyArgs(c.args)+"\x00"+strings.Join(got, "\x01"))
	}
	for _, got := range results { // results of no call here
		keys = append(keys, "\x00\x00"+strings.Join(got, "\x01"))
	}
	slices.Sort(keys)
	h := fnv.New64a()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0xff})
	}
	for _, k := range rest {
		h.Write([]byte(k))
		h.Write([]byte{0xfe})
	}
	return exchange{sum: h.Sum64(), calls: calls}, true
}

// steadyArgs is a call's arguments with their keys in one order, as two
// turns asking the same may write them differently.
func steadyArgs(raw string) string {
	var v any
	if json.Unmarshal([]byte(raw), &v) != nil {
		return raw
	}
	b, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return string(b)
}

// runNoise is what an agent's result says of the run rather than of what
// it found, which differs every time the same command runs: Codex's wall
// time, its unified exec's chunk id, its shell's duration_seconds.
var runNoise = []struct {
	mark string
	re   *regexp.Regexp
	with string
}{
	{"Wall time", regexp.MustCompile(`(?m)(Wall time:? )[0-9.]+( seconds?)`), "${1}#${2}"},
	{"Chunk ID: ", regexp.MustCompile(`(?m)(Chunk ID: )[^\s"\\]+`), "${1}#"},
	{"duration_seconds", regexp.MustCompile(`(\\?"duration_seconds\\?"\s*:\s*)[0-9.eE+-]+`), "${1}#"},
}

func steadyResult(s string) string {
	for _, n := range runNoise {
		if strings.Contains(s, n.mark) {
			s = n.re.ReplaceAllString(s, n.with)
		}
	}
	return s
}

// A call made to wait (sleep 60 while CI runs) is asked again with the
// same arguments and answered the same by design; the time it takes is the
// progress. A turn whose calls are all waits is never a loop.

var waitWords = map[string]bool{"sleep": true, "wait": true, "delay": true, "pause": true, "poll": true}

var durationKeys = map[string]bool{"duration": true, "duration_ms": true, "duration_s": true, "duration_seconds": true, "ms": true, "milliseconds": true,
	"seconds": true, "secs": true, "sec": true, "timeout": true, "timeout_ms": true, "delay": true, "delay_ms": true, "minutes": true}

func allWaits(calls []turnPart) bool {
	for _, c := range calls {
		if !isWait(c) {
			return false
		}
	}
	return len(calls) > 0
}

// isWait: the tool's own name (after any server__ or namespace prefix) has
// a wait word, or its arguments are only a duration.
func isWait(c turnPart) bool {
	name := c.name
	for _, sep := range []string{"__", ".", "/", ":"} {
		if i := strings.LastIndex(name, sep); i >= 0 {
			name = name[i+len(sep):]
		}
	}
	var words []string
	cur := []rune{}
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	for _, r := range name {
		switch {
		case r >= 'A' && r <= 'Z':
			flush()
			cur = append(cur, r)
		case r >= 'a' && r <= 'z':
			cur = append(cur, r)
		default:
			flush()
		}
	}
	flush()
	for _, w := range words {
		if waitWords[w] {
			return true
		}
	}
	var args map[string]json.RawMessage
	if json.Unmarshal([]byte(c.args), &args) != nil || len(args) == 0 {
		return false
	}
	for k, v := range args {
		var n float64
		if !durationKeys[strings.ToLower(k)] || json.Unmarshal(v, &n) != nil {
			return false
		}
	}
	return true
}

// localAnswer is text as a finished answer in api to the request body: the
// events a stream of it has (a chat stream, converted to api the way a
// node's answer would be).
func localAnswer(api, model string, body []byte, text string) ([][]byte, error) {
	id := fmt.Sprintf("chatcmpl-local-%d", time.Now().UnixNano())
	chunk := func(delta map[string]any, finish any) []byte {
		b, _ := json.Marshal(map[string]any{"id": id, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
		return b
	}
	usage, _ := json.Marshal(map[string]any{"id": id, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": model,
		"choices": []any{}, "usage": map[string]any{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0}})
	chat := [][]byte{chunk(map[string]any{"role": "assistant", "content": text}, nil), chunk(map[string]any{}, "stop"), usage}
	if protocol.NormalAPI(api) == protocol.APIChat {
		return chat, nil
	}
	conv, err := apicompat.Convert(protocol.NormalAPI(api), apicompat.Chat, body, apicompat.Options{Model: model})
	if err != nil {
		return nil, err
	}
	var out [][]byte
	emit := func(p []byte) error { out = append(out, append([]byte(nil), p...)); return nil }
	for _, c := range chat {
		if err := conv.Translator.Event(c, emit); err != nil {
			return nil, err
		}
	}
	if err := conv.Translator.Close(emit); err != nil {
		return nil, err
	}
	return out, nil
}

// answeredToolLoop answers req here when its conversation is stuck in a
// tool loop (as a stream or one JSON answer), and says whether it did. A
// compaction is never stopped: it is how a long loop ends.
func (p *Proxy) answeredToolLoop(w http.ResponseWriter, req ChatRequest, stream bool) bool {
	events, ok := p.toolLoopAnswer(req)
	if !ok {
		return false
	}
	w.Header().Set(HeaderLoopGuard, "tool_loop")
	if stream {
		out := &sseWriter{w: w, api: req.API}
		for _, ev := range events {
			_ = out.event(ev)
		}
		if protocol.NormalAPI(req.API) == protocol.APIChat {
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		}
		out.flush()
		return true
	}
	body, err := apicompat.Aggregate(protocol.NormalAPI(req.API), events)
	if err != nil {
		writeAPIError(w, req.API, http.StatusInternalServerError, err.Error(), "upstream_error")
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
	return true
}

// toolLoopAnswer is the answer's events when req is stuck in a tool loop
// and the guard is on.
func (p *Proxy) toolLoopAnswer(req ChatRequest) ([][]byte, bool) {
	if p.NoLoopGuard || req.Header[protocol.HeaderOp] == protocol.OpCompact {
		return nil, false
	}
	loop, ok := toolLoopOf(req.API, req.Body)
	if !ok {
		return nil, false
	}
	events, err := localAnswer(req.API, req.Model, req.Body, loop.notice())
	if err != nil {
		return nil, false
	}
	slog.Warn("loop guard answered a request stuck in a tool loop", "component", "buyer", "api", req.API, "turns", loop.turns, "calls", loop.called())
	return events, true
}
