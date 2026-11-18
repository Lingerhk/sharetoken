package buyer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A model can fall into a loop it never leaves (after magpie's loopGuard,
// #1359): DeepSeek's reasoning went on as "Now." "Writing." "Go." "Let me
// write." "OK." over and over, a word or two changing between rounds, until
// the answer's token limit, and the buyer paid for every token of it.
// loopGuard reads an answer's events as the caller is sent them, in any of
// the three APIs, and says when its reasoning or its text has become such
// a loop, for the proxy to end the answer there as if it had reached its
// output limit (streamCloser) and stop the job.
//
// The answer is cut into units, its lines and sentences. A loop is a long
// run of units that are nearly all the same few: of the last loopWindow
// units of reasoning, or of text, no more than loopDistinct different ones.
// Ordinary writing does not come near it, even when lines repeat: code, a
// table, a list or a log differs from line to line in its names, numbers
// and cells, and a unit with no word in it (a brace, a rule, a row of
// numbers) is not counted at all. The cycle need not be exact.
type loopGuard struct {
	think unitRun
	text  unitRun
}

const (
	// loopWindow is how many units of a channel are looked at: 256 lines
	// of the reported loop were about 600 tokens.
	loopWindow = 256
	// loopDistinct is the most different units the window may hold and
	// still be a loop: the reported one had 9.
	loopDistinct = 24
	// unitMost is how long a unit grows without a break before it is taken
	// whole, and unitKept how much of it is told apart.
	unitMost = 4096
	unitKept = 256
	// runOnUnits is how many units cut at unitMost in a row, each the same
	// few words over and over, are a loop: some 2,000 tokens of it.
	runOnUnits = 2
	// runOnDistinct is the most different words a run-on loop has.
	runOnDistinct = 8
)

// unitRun is one channel's units: its reasoning's, or its text's.
type unitRun struct {
	cur      strings.Builder
	stop     bool // cur ends in a sentence's stop: a space after it ends the unit
	lettered bool // cur has a letter, for a stop after it to end a sentence
	ring     [loopWindow]uint64
	n        int // units seen
	counts   map[uint64]*unitCount
	runOn    int // units in a row cut at unitMost as the same few words
}

type unitCount struct {
	n    int
	text string // as first written, for the log to quote
	at   int
}

// loopTrip is what loopGuard found.
type loopTrip struct {
	reasoning bool
	units     []string // the different units, the most frequent first
	window    int
	runOn     bool // the loop had no break; window is in characters
}

func (t loopTrip) String() string {
	what := "text"
	if t.reasoning {
		what = "reasoning"
	}
	quoted := make([]string, 0, 6)
	for i, u := range t.units {
		if i == 6 {
			quoted = append(quoted, "…")
			break
		}
		quoted = append(quoted, fmt.Sprintf("%q", u))
	}
	if t.runOn {
		return fmt.Sprintf("the model's %s repeated itself for %d characters without a break (%s)", what, t.window, strings.Join(quoted, " "))
	}
	return fmt.Sprintf("the model's %s went round in a loop, its last %d lines and sentences all among the same %d (%s)", what, t.window, len(t.units), strings.Join(quoted, " "))
}

// feed reads the next event payload of an answer in api.
func (g *loopGuard) feed(payload []byte) (loopTrip, bool) {
	think, text := eventWords(payload)
	if think != "" {
		if t, ok := g.think.add(think); ok {
			t.reasoning = true
			return t, true
		}
	}
	if text != "" {
		return g.text.add(text)
	}
	return loopTrip{}, false
}

// eventWords is what one event says, in any of the APIs: its reasoning and
// its text. Calls, starts, stops and usage say nothing here.
func eventWords(payload []byte) (think, text string) {
	if !bytes.Contains(payload, []byte(`delta`)) {
		return "", ""
	}
	var v struct {
		Type    string          `json:"type"`
		Delta   json.RawMessage `json:"delta"`
		Choices []struct {
			Delta struct {
				Content          json.RawMessage `json:"content"`
				ReasoningContent json.RawMessage `json:"reasoning_content"`
				Reasoning        json.RawMessage `json:"reasoning"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if json.Unmarshal(payload, &v) != nil {
		return "", ""
	}
	switch v.Type {
	case "content_block_delta": // Messages
		var d struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		}
		if json.Unmarshal(v.Delta, &d) == nil {
			switch d.Type {
			case "text_delta":
				return "", d.Text
			case "thinking_delta":
				return d.Thinking, ""
			}
		}
		return "", ""
	case "response.output_text.delta": // Responses
		return "", jsonString(v.Delta)
	case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
		return jsonString(v.Delta), ""
	case "":
	default:
		return "", ""
	}
	var th, tx strings.Builder // chat
	for _, c := range v.Choices {
		r := jsonString(c.Delta.ReasoningContent)
		if r == "" {
			r = jsonString(c.Delta.Reasoning)
		}
		th.WriteString(r)
		tx.WriteString(jsonString(c.Delta.Content))
	}
	return th.String(), tx.String()
}

func jsonString(raw json.RawMessage) string {
	var s string
	if len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

// add reads s, the next of the channel.
func (r *unitRun) add(s string) (loopTrip, bool) {
	for _, c := range s {
		if c == '\n' || r.stop && unicode.IsSpace(c) {
			if t, ok := r.end(); ok {
				return t, true
			}
			continue
		}
		r.cur.WriteRune(c)
		switch c {
		case '.', '!', '?':
			r.stop = r.lettered // a sentence's stop, not a list's number (1.) or a decimal
		case '。', '！', '？':
			if t, ok := r.end(); ok {
				return t, true
			}
			continue
		default:
			r.stop = false
			r.lettered = r.lettered || unicode.IsLetter(c)
		}
		if r.cur.Len() >= unitMost {
			// A unit this long has had no break: a loop with none in it
			// ("mcp mcp mcp …") is never 256 units before the limit, so
			// it is read on its own.
			if words, ok := runOnWords(r.cur.String()); ok {
				if r.runOn++; r.runOn >= runOnUnits {
					return loopTrip{units: words, window: r.runOn * unitMost, runOn: true}, true
				}
			} else {
				r.runOn = 0
			}
			if t, ok := r.end(); ok {
				return t, true
			}
		}
	}
	return loopTrip{}, false
}

// end takes the unit written so far, and says whether the window has
// become a loop with it.
func (r *unitRun) end() (loopTrip, bool) {
	raw := r.cur.String()
	if r.cur.Len() < unitMost {
		// a break; a long unit before it that still runs on keeps the count
		if len(raw) < unitMost/8 {
			r.runOn = 0
		} else if _, ok := runOnWords(raw); !ok {
			r.runOn = 0
		}
	}
	r.cur.Reset()
	r.stop, r.lettered = false, false
	key, ok := unitKey(raw)
	if !ok {
		return loopTrip{}, false
	}
	h := fnv.New64a()
	h.Write([]byte(key))
	sum := h.Sum64()
	if r.counts == nil {
		r.counts = map[uint64]*unitCount{}
	}
	slot := r.n % loopWindow
	if r.n >= loopWindow {
		old := r.ring[slot]
		if c := r.counts[old]; c != nil {
			if c.n--; c.n == 0 {
				delete(r.counts, old)
			}
		}
	}
	r.ring[slot] = sum
	c := r.counts[sum]
	if c == nil {
		text := strings.TrimSpace(raw)
		if utf8.RuneCountInString(text) > 40 {
			text = string([]rune(text)[:40]) + "…"
		}
		c = &unitCount{text: text, at: r.n}
		r.counts[sum] = c
	}
	c.n++
	r.n++
	if r.n < loopWindow || len(r.counts) > loopDistinct {
		return loopTrip{}, false
	}
	all := make([]*unitCount, 0, len(r.counts))
	for _, c := range r.counts {
		all = append(all, c)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].n != all[j].n {
			return all[i].n > all[j].n
		}
		return all[i].at < all[j].at
	})
	t := loopTrip{window: loopWindow}
	for _, c := range all {
		t.units = append(t.units, c.text)
	}
	return t, true
}

// runOnWords says whether s, written with no break, is the same few words
// over and over ("mcp mcp mcp …" or "mcpmcpmcp…"), and which. A long line of
// prose, code, JSON, base64 or a table row has many different words, or
// none.
func runOnWords(s string) ([]string, bool) {
	if !hasWord(s) {
		return nil, false
	}
	// one piece repeated, a word or a few with no space between
	for p := 1; p <= 64 && 4*p <= len(s); p++ {
		i := p
		for i < len(s) && s[i] == s[i-p] {
			i++
		}
		if i == len(s) {
			start := 0
			for start < p && !utf8.RuneStart(s[start]) {
				start++
			}
			end := min(start+3*p, len(s))
			for end < len(s) && !utf8.RuneStart(s[end]) {
				end++
			}
			return []string{strings.TrimSpace(s[start:end])}, true
		}
	}
	counts := map[string]int{}
	var order []string
	words := 0
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(c rune) bool {
		return !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '_'
	}) {
		words++
		if counts[w]++; counts[w] == 1 {
			order = append(order, w)
		}
		if len(counts) > runOnDistinct {
			return nil, false
		}
	}
	if words*32 < len(s) {
		return nil, false // a few long words: not read as a loop
	}
	sort.SliceStable(order, func(i, j int) bool { return counts[order[i]] > counts[order[j]] })
	return order, true
}

// unitKey is what tells a unit apart, and whether it counts: its words,
// without case or its closing stop. A unit with no word in it is left out.
func unitKey(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	s = strings.TrimRightFunc(s, func(c rune) bool {
		return strings.ContainsRune(".!?。！？,，;；:：…", c) || unicode.IsSpace(c)
	})
	if !hasWord(s) {
		return "", false
	}
	s = strings.ToLower(s)
	if len(s) > unitKept {
		s = s[:unitKept]
	}
	return s, true
}

// hasWord says whether s has a word in it: a run of letters with no digit
// in it (Now, Writing, 好), not 0x00, v2 or a hash.
func hasWord(s string) bool {
	letters, digits := false, false
	for _, c := range s + " " {
		if unicode.IsLetter(c) || unicode.IsDigit(c) || c == '_' {
			letters = letters || unicode.IsLetter(c)
			digits = digits || unicode.IsDigit(c)
			continue
		}
		if letters && !digits {
			return true
		}
		letters, digits = false, false
	}
	return false
}
