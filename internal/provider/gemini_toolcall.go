package provider

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
)

// A Gemini stream normally gives each function call whole, its args an
// object, and two calls of the same function with no id are two calls
// (parallel calls of one tool). Some backends instead stream a call's args
// as JSON text in pieces, each a string ("args": "{\"q\":"), and may end
// with the whole object once more (after sub2api's
// gemini_tool_call_boundary.go). Such a call is held until it is complete:
// its pieces are joined by the call's id, or, without one, as long as they
// read as the same call's; an object that follows them is its snapshot and
// replaces them.

// geminiCall is a functionCall part of the stream.
type geminiCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
	ID   string          `json:"id"`
}

// pendingCall is a call whose args came as text, not yet sent on.
type pendingCall struct {
	id, name, sig string
	args          string
}

// callArgs is a call's args as JSON text, and whether they came as a
// string (a piece of the text) rather than an object.
func callArgs(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s, true
		}
	}
	if len(raw) == 0 || string(raw) == "null" {
		return "{}", false
	}
	return string(raw), false
}

// continues tells whether a part (name, id, args) belongs to the pending
// call rather than starting another.
func (p *pendingCall) continues(name, id, args string, piece bool) bool {
	if name != p.name {
		return false
	}
	if id != "" && p.id != "" {
		return id == p.id
	}
	if !json.Valid([]byte(p.args)) {
		return true // not complete yet: whatever comes is the rest of it
	}
	if piece {
		// Complete already: only the same text again, or the text so far
		// with more after it, is still this call.
		return strings.HasPrefix(args, p.args)
	}
	return sameJSON(p.args, args)
}

// add takes a part that continues the call.
func (p *pendingCall) add(id, sig, args string, piece bool) {
	if p.id == "" {
		p.id = id
	}
	if p.sig == "" {
		p.sig = sig
	}
	switch {
	case !piece, strings.HasPrefix(args, p.args):
		// an object snapshot, or the whole text so far sent again (with
		// or without more after it)
		p.args = args
	default:
		p.args += args
	}
}

func sameJSON(a, b string) bool {
	var x, y any
	return json.Unmarshal([]byte(a), &x) == nil && json.Unmarshal([]byte(b), &y) == nil && reflect.DeepEqual(x, y)
}

// functionCall takes a functionCall part with its thought signature.
func (s *geminiStream) functionCall(fc *geminiCall, sig string) error {
	s.content = true
	args, piece := callArgs(fc.Args)
	if p := s.pending; p != nil {
		if p.continues(fc.Name, fc.ID, args, piece) {
			p.add(fc.ID, sig, args, piece)
			return nil
		}
		if err := s.flushCall(); err != nil {
			return err
		}
	}
	if piece {
		s.pending = &pendingCall{id: fc.ID, name: fc.Name, sig: sig, args: args}
		return nil
	}
	return s.sendCall(fc.ID, fc.Name, sig, args)
}

// flushCall sends the pending call on, if there is one.
func (s *geminiStream) flushCall() error {
	p := s.pending
	if p == nil {
		return nil
	}
	s.pending = nil
	args := p.args
	if strings.TrimSpace(args) == "" {
		args = "{}"
	}
	return s.sendCall(p.id, p.name, p.sig, args)
}

func (s *geminiStream) sendCall(id, name, sig, args string) error {
	if id == "" {
		id = "call_" + randomHex(12)
	}
	_, err := s.out.toolStart(signedID(id, sig, s.sigs), name, args)
	return err
}
