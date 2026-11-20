package provider

import (
	"crypto/sha256"
	"encoding/json"
	"sync"
	"time"

	"sharetoken/internal/apicompat"
)

// A Responses conversation that moved here from another seller carries that
// seller's encrypted reasoning (and compactions), which only the account that
// made them can read. The first request refused for them goes again without
// them (retryWithoutReasoning); the client sends the same items back on every
// later turn, so the ones this account refused are remembered, by a hash of
// their encrypted content, and left out of the conversation's later requests
// up front instead of being refused and sent again each turn. Reasoning this
// account made itself after the move is not among them and still goes along,
// for its cache and its train of thought (after magpie's refusedSeals).

const (
	sealsKeep      = 6 * time.Hour
	sealsMostKeys  = 4096 // conversations remembered per upstream
	sealsMostItems = 4096 // refused items remembered per conversation
)

type sealHash = [sha256.Size]byte

// refusedSeals is what one upstream (one account) refused, by conversation.
// The zero value is ready to use, and a nil one remembers nothing.
type refusedSeals struct {
	mu sync.Mutex
	m  map[string]*sealsRefused
}

type sealsRefused struct {
	at    time.Time
	seals map[sealHash]bool
}

// sealedItemKinds are the input items whose encrypted content is all there is
// of them: refused, they go whole.
var sealedItemKinds = map[string]bool{"reasoning": true, "compaction": true, "compaction_summary": true}

// sealsKey is the conversation a request belongs to, for this memory: its
// model and its session (the buyer's session_id) or prompt_cache_key.
func sealsKey(session string, body []byte) string {
	var q struct {
		Model string `json:"model"`
		Key   string `json:"prompt_cache_key"`
	}
	_ = json.Unmarshal(body, &q)
	conv := session
	if conv == "" {
		conv = q.Key
	}
	return q.Model + "\x00" + conv
}

// sealsOf are the hashes of the foreign-able items' encrypted content in a
// Responses request's input. Those sharetoken's conversions made are not
// among them: every seller reads those.
func sealsOf(body []byte) []sealHash {
	var q struct {
		Input []struct {
			Type string `json:"type"`
			Enc  string `json:"encrypted_content"`
		} `json:"input"`
	}
	if json.Unmarshal(body, &q) != nil {
		return nil
	}
	var out []sealHash
	for _, it := range q.Input {
		if sealedItemKinds[it.Type] && it.Enc != "" && !apicompat.IsConverted(it.Enc) {
			out = append(out, sha256.Sum256([]byte(it.Enc)))
		}
	}
	return out
}

// note remembers that the items sent had and left lacks were refused in the
// conversation key.
func (s *refusedSeals) note(key string, sent, left []byte) {
	if s == nil {
		return
	}
	kept := map[sealHash]bool{}
	for _, h := range sealsOf(left) {
		kept[h] = true
	}
	var gone []sealHash
	for _, h := range sealsOf(sent) {
		if !kept[h] {
			gone = append(gone, h)
		}
	}
	if len(gone) == 0 {
		return
	}
	now := timeNow()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]*sealsRefused{}
	}
	r := s.m[key]
	if r == nil {
		if len(s.m) >= sealsMostKeys {
			s.prune(now)
		}
		r = &sealsRefused{seals: map[sealHash]bool{}}
		s.m[key] = r
	}
	for _, h := range gone {
		if len(r.seals) >= sealsMostItems {
			break
		}
		r.seals[h] = true
	}
	r.at = now
}

// prune forgets conversations not seen for sealsKeep, and the oldest ones
// while there are still too many. The caller holds mu.
func (s *refusedSeals) prune(now time.Time) {
	var oldest string
	for k, r := range s.m {
		if now.Sub(r.at) > sealsKeep {
			delete(s.m, k)
			continue
		}
		if oldest == "" || r.at.Before(s.m[oldest].at) {
			oldest = k
		}
	}
	if len(s.m) >= sealsMostKeys && oldest != "" {
		delete(s.m, oldest)
	}
}

// without is body without the items this upstream already refused in the
// conversation key; false when there is nothing to take out.
func (s *refusedSeals) without(key string, body []byte) ([]byte, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.m[key]
	if r == nil {
		return nil, false
	}
	now := timeNow()
	if now.Sub(r.at) > sealsKeep {
		delete(s.m, key)
		return nil, false
	}
	r.at = now
	return withoutItems(body, func(m map[string]any) bool {
		typ, _ := m["type"].(string)
		enc, _ := m["encrypted_content"].(string)
		return sealedItemKinds[typ] && enc != "" && r.seals[sha256.Sum256([]byte(enc))]
	})
}

// retry is retryWithoutReasoning that remembers what was refused, and leaves
// it out of the conversation's later requests from the start.
func (s *refusedSeals) retry(key string, body []byte, do func([]byte) error) error {
	if pruned, ok := s.without(key, body); ok {
		body = pruned
	}
	return retryWithoutReasoningNoting(body, do, func(sent, left []byte) { s.note(key, sent, left) })
}
