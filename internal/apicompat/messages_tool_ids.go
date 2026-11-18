package apicompat

// Claude Code keeps every tool_use id of a conversation in one set and drops
// a block whose id it has seen before; a turn whose only block is dropped
// shows as "[Tool use interrupted]", and the tool loop stops. A chat or
// Responses upstream may number its calls by their place in the answer
// (call_0, fc_0 on every turn), so the id of one turn's call comes again on
// the next. A call whose id the request's history already holds goes to the
// client salted: the mark, then the upstream's id and a random salt in
// base64url, which nothing the client holds names. When the client sends it
// back, the conversion reads the upstream's own id out of it again, so the
// upstream sees the ids it made (after magpie's tool_use_ids.go). Nothing is
// stored: the salted id carries what it stands for, and works on whichever
// seller the next turn goes to.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// saltedIDMark starts a salted tool_use id. Anthropic takes ids of
// [A-Za-z0-9_-], which base64url keeps to.
const saltedIDMark = "toolu_st_"

// saltCallID is id salted for a Messages client.
func saltCallID(id string) string {
	return saltedIDMark + base64.RawURLEncoding.EncodeToString([]byte(id+"\x00"+msgRandomHex(6)))
}

// unsaltCallID is the id saltCallID was given, and whether id was salted.
func unsaltCallID(id string) (string, bool) {
	rest, ok := strings.CutPrefix(id, saltedIDMark)
	if !ok {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(rest)
	if err != nil || !utf8.Valid(raw) {
		return "", false
	}
	at := bytes.IndexByte(raw, 0)
	if at <= 0 || at == len(raw)-1 {
		return "", false
	}
	return string(raw[:at]), true
}

// msgUnsaltIDs gives the request's tool_use and tool_result blocks back the
// ids the upstream made, and returns every tool_use id of its history (as
// the upstream knows them): the ids the client already holds.
func msgUnsaltIDs(r *msgRequest) map[string]bool {
	held := map[string]bool{}
	for i := range r.Messages {
		m := &r.Messages[i]
		salted := bytes.Contains(m.Content, []byte(saltedIDMark))
		if !salted && !bytes.Contains(m.Content, []byte(`"tool_use"`)) {
			continue
		}
		var blocks []map[string]json.RawMessage
		if json.Unmarshal(m.Content, &blocks) != nil {
			continue
		}
		changed := false
		for _, b := range blocks {
			for _, field := range []string{"id", "tool_use_id"} {
				var id string
				if json.Unmarshal(b[field], &id) != nil || id == "" {
					continue
				}
				if raw, ok := unsaltCallID(id); ok {
					id = raw
					b[field], _ = json.Marshal(id)
					changed = true
				}
				var typ string
				if _ = json.Unmarshal(b["type"], &typ); typ == "tool_use" && field == "id" && m.Role == "assistant" {
					held[id] = true
				}
			}
		}
		if changed {
			if out, err := json.Marshal(blocks); err == nil {
				m.Content = out
			}
		}
	}
	return held
}

// clientID is the id a tool_use block the upstream named id goes to the
// client under: salted when the client already holds id, as it is otherwise.
// Either way the client holds it from then on.
func (w *msgWriter) clientID(id string) string {
	if w.held == nil {
		w.held = map[string]bool{}
	}
	if w.held[id] {
		id = saltCallID(id)
	}
	w.held[id] = true
	return id
}
