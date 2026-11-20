package apicompat

import (
	"bytes"
	"encoding/json"
)

// An assistant message in a Responses answer says its phase, as OpenAI's own
// models' do: "commentary" for what the model said on the way (before a
// call), "final_answer" for the answer that ends the turn. Codex folds a
// finished turn's commentary and calls under "Worked for …" and reads a
// message without a phase as an answer, so every progress line of a
// converted model stayed unfolded (after magpie's phase.go).
//
// A message's phase is known only from what follows it: a call or another
// message makes it commentary; the answer completing with nothing but
// reasoning after it makes it the final answer. An answer cut short guesses
// no final answer, and a phase the upstream wrote is kept. In a stream, a
// message's output_item.done waits, with what comes after it, until that is
// known.

const (
	phaseCommentary = "commentary"
	phaseFinal      = "final_answer"
	// phaseHeldMost is how many events may wait behind a message whose phase
	// is not known yet; past it they go on, the message without a phase.
	phaseHeldMost = 512
)

// unphased reports whether an output item is an assistant message with no
// phase of its own.
func unphased(it map[string]any) bool {
	if it == nil || it["type"] != "message" {
		return false
	}
	if r, ok := it["role"]; ok && r != "assistant" {
		return false
	}
	p, _ := it["phase"].(string)
	return p == ""
}

// phaseEvent passes one event of a converted stream through the phase
// hold: it returns the events to send now, in order.
func (s *respStream) phaseEvent(ev map[string]any) []map[string]any {
	typ, _ := ev["type"].(string)
	switch typ {
	case "response.completed", "response.incomplete":
		// The answer's output says, in output order, what follows each
		// message (parallel calls may have been added before a message that
		// ends after them); its items are the ones the held events carry.
		if res, _ := ev["response"].(map[string]any); res != nil {
			out, _ := res["output"].([]any)
			phaseOutput(out, typ == "response.completed")
		}
		return append(s.releaseHeld(""), ev)
	}
	if s.held != nil {
		switch typ {
		case "response.output_item.added":
			if it, _ := ev["item"].(map[string]any); it == nil || it["type"] != "reasoning" {
				return append(s.releaseHeld(phaseCommentary), ev)
			}
		case "response.failed", "error":
			return append(s.releaseHeld(""), ev)
		}
		s.held = append(s.held, ev)
		if len(s.held) > phaseHeldMost {
			return s.releaseHeld("")
		}
		return nil
	}
	if typ == "response.output_item.done" {
		if it, _ := ev["item"].(map[string]any); unphased(it) {
			s.held = []map[string]any{ev}
			return nil
		}
	}
	return []map[string]any{ev}
}

// releaseHeld is the events waiting, the message among them given phase (or
// left as it is, for ""). The message's item is the one the answer's output
// holds, so the terminal event says the same phase.
func (s *respStream) releaseHeld(phase string) []map[string]any {
	held := s.held
	s.held = nil
	if phase != "" && len(held) > 0 {
		if it, _ := held[0]["item"].(map[string]any); unphased(it) {
			it["phase"] = phase
		}
	}
	return held
}

// phaseOutput gives each assistant message of an answer's output its phase,
// and reports whether it gave any. done is whether the answer completed:
// only then is the last message the final answer.
func phaseOutput(output []any, done bool) bool {
	last := -1 // the last item that is not reasoning
	for i, it := range output {
		if m, _ := it.(map[string]any); m != nil && m["type"] != "reasoning" {
			last = i
		}
	}
	changed := false
	for i, it := range output {
		m, _ := it.(map[string]any)
		if !unphased(m) {
			continue
		}
		switch {
		case i < last:
			m["phase"] = phaseCommentary
		case i == last && done:
			m["phase"] = phaseFinal
		default:
			continue
		}
		changed = true
	}
	return changed
}

// phaseRawOutput is phaseOutput of an answer's output as JSON; false when
// nothing changed.
func phaseRawOutput(raw json.RawMessage, done bool) (json.RawMessage, bool) {
	if !bytes.Contains(raw, []byte(`"message"`)) {
		return nil, false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var out []any
	if d.Decode(&out) != nil || !phaseOutput(out, done) {
		return nil, false
	}
	b, err := respMarshal(out)
	return b, err == nil
}
