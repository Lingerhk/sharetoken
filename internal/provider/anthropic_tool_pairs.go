package provider

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Anthropic refuses a whole Messages request whose tool exchange does not
// pair: a tool_use the next user message does not answer ("messages.N:
// `tool_use` ids were found without `tool_result` blocks immediately
// after"), a tool_result answering no tool_use of the message before
// ("unexpected `tool_use_id` found in `tool_result` blocks"), or results
// after other content of their message. A reply cut off after a call, a
// stop while parallel calls ran, or a client that lost a result leaves such
// a history, and since every later request carries it, Claude Code shows
// "400 due to tool use concurrency issues" for the rest of the conversation
// (magpie's tool_blocks_paired.go). The conversions pair their output
// (apicompat chatPairTools, respPairCalls); a request that came in as
// Messages is mended here, before it is sent, rather than after a refused
// job the buyer would wait for.
//
// A request that pairs is left byte for byte as it came. A mended one keeps
// every message that needed no change as it came, and in one that did, its
// other members and blocks: a missing result is added as an error result,
// a result answering no call becomes the text it said, a second answer to
// the same call is left out, and results come before the other content.

// interruptedToolResult is the result given to a call that has none.
const interruptedToolResult = "[The result of this tool call is unavailable: the turn was interrupted.]"

// pairedToolBlocks is body, a Messages request, with its tool exchange
// mended where it does not pair.
func pairedToolBlocks(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"tool_use"`)) && !bytes.Contains(body, []byte(`"tool_result"`)) {
		return body
	}
	s, e, ok := memberSpan(body, "messages")
	if !ok {
		return body
	}
	mended, changed := pairedMessages(body[s:e])
	if !changed {
		return body
	}
	return splice(body, s, e, mended)
}

// toolTurn is the state of the calls of the assistant message in hand.
type toolTurn struct {
	pending  []string        // its tool_use ids, in order
	answered map[string]bool // which of them a result answered
}

func (t *toolTurn) open(id string) bool {
	for _, p := range t.pending {
		if p == id {
			return true
		}
	}
	return false
}

// missing is a synthetic error result for each call no result answered.
func (t *toolTurn) missing() []json.RawMessage {
	var out []json.RawMessage
	for _, id := range t.pending {
		if !t.answered[id] {
			out = append(out, syntheticToolResult(id))
		}
	}
	return out
}

// pairedMessages is msgs, a JSON array of messages, mended; false when it
// pairs already (or is not an array of objects).
func pairedMessages(msgs []byte) ([]byte, bool) {
	var els []json.RawMessage
	if json.Unmarshal(msgs, &els) != nil {
		return nil, false
	}
	var out []json.RawMessage
	changed := false
	turn := &toolTurn{}
	for _, el := range els {
		var m struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(el, &m) != nil {
			return nil, false
		}
		switch m.Role {
		case "assistant":
			if blocks := turn.missing(); len(blocks) > 0 {
				out = append(out, resultsMessage(blocks))
				changed = true
			}
			turn = &toolTurn{answered: map[string]bool{}}
			for _, b := range blockList(m.Content) {
				if typ, id, _ := blockIDs(b); typ == "tool_use" && id != "" {
					turn.pending = append(turn.pending, id)
				}
			}
			out = append(out, el)
		case "user":
			mended, touched := pairedUserTurn(el, m.Content, turn)
			turn = &toolTurn{}
			if touched {
				changed = true
			}
			out = append(out, mended)
		default:
			// A mid-conversation system message neither answers a call
			// nor closes one: it goes as it came.
			out = append(out, el)
		}
	}
	if blocks := turn.missing(); len(blocks) > 0 {
		out = append(out, resultsMessage(blocks))
		changed = true
	}
	if !changed {
		return nil, false
	}
	return rawArray(out), true
}

// pairedUserTurn is el, a user message whose content is content, answering
// exactly the calls of turn: its results first, each once, then the missing
// ones, then the rest as it came; false when it did so already.
func pairedUserTurn(el, content json.RawMessage, turn *toolTurn) (json.RawMessage, bool) {
	var kept, rest []json.RawMessage
	touched := false
	switch c := bytes.TrimSpace(content); {
	case len(c) > 0 && c[0] == '[':
		for _, b := range blockList(c) {
			typ, _, useID := blockIDs(b)
			switch {
			case typ != "tool_result":
				rest = append(rest, b)
			case !turn.open(useID):
				// answers no call of the message before
				rest = append(rest, resultAsText(b)...)
				touched = true
			case turn.answered[useID]:
				touched = true // a second answer: the first stands
			default:
				turn.answered[useID] = true
				if len(rest) > 0 {
					touched = true // after other content: moved first
				}
				kept = append(kept, b)
			}
		}
	case len(c) > 0 && c[0] == '"':
		rest = []json.RawMessage{c}
	}
	missing := turn.missing()
	if !touched && len(missing) == 0 {
		return el, false
	}
	if len(rest) == 1 && rest[0][0] == '"' {
		rest = textBlock(rest[0])
	}
	blocks := append(append(kept, missing...), rest...)
	if s, e, ok := memberSpan(el, "content"); ok {
		return splice(el, s, e, rawArray(blocks)), true
	}
	return resultsMessage(blocks), true
}

// blockList is the blocks of a content array; nil for anything else.
func blockList(content json.RawMessage) []json.RawMessage {
	var blocks []json.RawMessage
	if c := bytes.TrimSpace(content); len(c) == 0 || c[0] != '[' || json.Unmarshal(c, &blocks) != nil {
		return nil
	}
	return blocks
}

// blockIDs is a content block's type, id and tool_use_id.
func blockIDs(b json.RawMessage) (typ, id, useID string) {
	var v struct {
		Type      string `json:"type"`
		ID        string `json:"id"`
		ToolUseID string `json:"tool_use_id"`
	}
	_ = json.Unmarshal(b, &v)
	return v.Type, v.ID, v.ToolUseID
}

func syntheticToolResult(id string) json.RawMessage {
	b, _ := json.Marshal(struct {
		Type      string `json:"type"`
		ToolUseID string `json:"tool_use_id"`
		Content   string `json:"content"`
		IsError   bool   `json:"is_error"`
	}{"tool_result", id, interruptedToolResult, true})
	return b
}

func resultsMessage(blocks []json.RawMessage) json.RawMessage {
	return append(append([]byte(`{"role":"user","content":`), rawArray(blocks)...), '}')
}

// textBlock is a JSON string as a text block; an empty one gets a stand-in,
// as the API refuses an empty text block.
func textBlock(str []byte) []json.RawMessage {
	var s string
	if json.Unmarshal(str, &s) != nil || strings.TrimSpace(s) == "" {
		s = "Tool result received."
	}
	b, _ := json.Marshal(struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{"text", s})
	return []json.RawMessage{b}
}

// resultAsText is what a stray tool_result said, as blocks a user message
// may carry: its blocks as they came, or its text as a text block.
func resultAsText(b json.RawMessage) []json.RawMessage {
	var r struct {
		Content json.RawMessage `json:"content"`
	}
	_ = json.Unmarshal(b, &r)
	if blocks := blockList(r.Content); len(blocks) > 0 {
		return blocks
	}
	return textBlock(bytes.TrimSpace(r.Content))
}

// rawArray is a JSON array of els, each as it is.
func rawArray(els []json.RawMessage) []byte {
	var b bytes.Buffer
	b.WriteByte('[')
	for i, el := range els {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(el)
	}
	b.WriteByte(']')
	return b.Bytes()
}

// memberSpan is where the value of obj's top-level member key lies (the
// last one of that name, as a JSON decoder reads it); false when obj is not
// a JSON object or has no such member.
func memberSpan(obj []byte, key string) (start, end int, ok bool) {
	d := json.NewDecoder(bytes.NewReader(obj))
	if t, err := d.Token(); err != nil || t != json.Delim('{') {
		return 0, 0, false
	}
	for d.More() {
		t, err := d.Token()
		if err != nil {
			return 0, 0, false
		}
		k, _ := t.(string)
		i := int(d.InputOffset())
		for i < len(obj) && (obj[i] == ':' || obj[i] == ' ' || obj[i] == '\t' || obj[i] == '\n' || obj[i] == '\r') {
			i++
		}
		var v json.RawMessage
		if d.Decode(&v) != nil {
			return 0, 0, false
		}
		if k == key {
			start, end, ok = i, int(d.InputOffset()), true
		}
	}
	return start, end, ok
}

// splice is b with b[s:e] replaced by val.
func splice(b []byte, s, e int, val []byte) []byte {
	out := make([]byte, 0, len(b)-(e-s)+len(val))
	out = append(out, b[:s]...)
	out = append(out, val...)
	return append(out, b[e:]...)
}
