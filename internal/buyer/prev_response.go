package buyer

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// A Responses client may send only a turn's new input and name the response
// it follows with previous_response_id, as OpenAI's API lets it (the OpenAI
// Agents SDK does). Nodes keep nothing between jobs, so the proxy keeps each
// response it gave such a client, its whole input and its output, and a
// request that names one is given the conversation whole before it goes out
// (after magpie's prev_response.go). Kept in memory only, bounded in count,
// bytes and time, under the API key the client used: one key never reads
// another's. A request that says store false (Codex over HTTP) has nothing
// kept.

const (
	prevTTL      = time.Hour
	prevMost     = 256      // responses kept
	prevMostSize = 64 << 20 // bytes of items kept, counted per response
)

type prevKey struct{ scope, id string }

// prevStore is the responses kept, the newest first. The zero value is
// ready to use.
type prevStore struct {
	mu   sync.Mutex
	ll   *list.List
	m    map[prevKey]*list.Element
	size int
}

type prevEntry struct {
	key  prevKey
	turn *wsTurn
	size int
	at   time.Time
}

// get is the kept response id of scope, nil when there is none (never kept,
// or forgotten).
func (s *prevStore) get(scope, id string) *wsTurn {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[prevKey{scope, id}]
	if !ok {
		return nil
	}
	pe := e.Value.(*prevEntry)
	if time.Since(pe.at) > prevTTL {
		s.drop(e)
		return nil
	}
	s.ll.MoveToFront(e)
	return pe.turn
}

// put keeps a response.
func (s *prevStore) put(scope string, t *wsTurn) {
	if t == nil || t.id == "" {
		return
	}
	size := 0
	for _, it := range t.items {
		size += len(it)
	}
	if size > prevMostSize {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m, s.ll = map[prevKey]*list.Element{}, list.New()
	}
	k := prevKey{scope, t.id}
	if e, ok := s.m[k]; ok {
		s.drop(e)
	}
	s.m[k] = s.ll.PushFront(&prevEntry{key: k, turn: t, size: size, at: time.Now()})
	s.size += size
	for s.ll.Len() > prevMost || s.size > prevMostSize {
		s.drop(s.ll.Back())
	}
	for e := s.ll.Back(); e != nil && time.Since(e.Value.(*prevEntry).at) > prevTTL; e = s.ll.Back() {
		s.drop(e)
	}
}

func (s *prevStore) drop(e *list.Element) {
	pe := e.Value.(*prevEntry)
	s.ll.Remove(e)
	delete(s.m, pe.key)
	s.size -= pe.size
}

// prevScope is whose responses a request may name: those given under the
// same API key (all of a client without one share a scope).
func prevScope(r *http.Request) string {
	h := sha256.Sum256([]byte(r.Header.Get("Authorization") + "\x00" + r.Header.Get("x-api-key")))
	return hex.EncodeToString(h[:16])
}

// turnWatch reads, from the events of a response, what is kept of it.
type turnWatch struct {
	id     string
	output []json.RawMessage
	done   []json.RawMessage // output_item.done items, for a terminal event without output
}

func (w *turnWatch) see(payload []byte) {
	var ev struct {
		Type     string          `json:"type"`
		Item     json.RawMessage `json:"item"`
		Response struct {
			ID     string            `json:"id"`
			Output []json.RawMessage `json:"output"`
		} `json:"response"`
	}
	if json.Unmarshal(payload, &ev) != nil {
		return
	}
	switch ev.Type {
	case "response.created":
		w.id = ev.Response.ID
	case "response.output_item.done":
		w.done = append(w.done, ev.Item)
	case "response.completed", "response.incomplete", "response.done":
		w.id, w.output = ev.Response.ID, ev.Response.Output
	}
}

// turn is the response seen, after input: nil when it gave no id.
func (w *turnWatch) turn(input []json.RawMessage) *wsTurn {
	if w.id == "" {
		return nil
	}
	output := w.output
	if len(output) == 0 {
		output = w.done
	}
	items := make([]json.RawMessage, 0, len(input)+len(output))
	return &wsTurn{id: w.id, items: append(append(items, input...), output...)}
}

// prevExpand is a Responses body that followed prior, with the conversation
// whole in its input and previous_response_id removed; it returns the whole
// input too.
func prevExpand(body []byte, prior *wsTurn) (out []byte, input []json.RawMessage, err error) {
	var req map[string]json.RawMessage
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, nil, err
	}
	fresh, err := inputItems(req["input"])
	if err != nil {
		return nil, nil, err
	}
	input = append(append(make([]json.RawMessage, 0, len(prior.items)+len(fresh)), prior.items...), fresh...)
	req["input"], _ = json.Marshal(input)
	delete(req, "previous_response_id")
	out, err = json.Marshal(req)
	return out, input, err
}

// storeOff reports whether a Responses body says store false.
func storeOff(body []byte) bool {
	var q struct {
		Store *bool `json:"store"`
	}
	return json.Unmarshal(body, &q) == nil && q.Store != nil && !*q.Store
}

// writePrevNotFound answers a request naming a response that is not kept,
// as OpenAI does.
func writePrevNotFound(w http.ResponseWriter, id string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	b, _ := json.Marshal(map[string]any{"error": map[string]any{
		"message": "Previous response with id '" + id + "' not found.",
		"type":    "invalid_request_error", "param": "previous_response_id", "code": "previous_response_not_found"}})
	_, _ = w.Write(b)
}
