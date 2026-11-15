package buyer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"sharetoken/internal/node"
	"sharetoken/internal/protocol"
)

// Session affinity. A conversation's turns resend the whole history; served
// by the node of the previous turn they find its connection open (often
// direct by then) and the model server's prompt cache warm, which cuts the
// first-token time and the node's cost. The buyer remembers the node per
// session and asks the coordinator for it (TicketRequest.PreferNode); the
// coordinator matches it when it is a candidate and picks as usual otherwise.
// The memory stays on this machine: the coordinator learns only which node a
// request preferred.

const (
	stickyTTL = 30 * time.Minute // longer than the model servers' prompt caches
	stickyMax = 4096
)

type stickyNodes struct {
	mu sync.Mutex
	m  map[string]stickyEntry
}

type stickyEntry struct {
	node string
	at   time.Time
}

func (s *stickyNodes) get(key string) string {
	if key == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.m[key]
	if !ok || time.Since(e.at) > stickyTTL {
		return ""
	}
	return e.node
}

func (s *stickyNodes) put(key, node string) {
	if key == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = make(map[string]stickyEntry)
	}
	if len(s.m) >= stickyMax {
		var oldest string
		var at time.Time
		for k, e := range s.m {
			if time.Since(e.at) > stickyTTL {
				delete(s.m, k)
			} else if oldest == "" || e.at.Before(at) {
				oldest, at = k, e.at
			}
		}
		if len(s.m) >= stickyMax {
			delete(s.m, oldest)
		}
	}
	s.m[key] = stickyEntry{node: node, at: time.Now()}
}

func (s *stickyNodes) drop(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
}

// sessionKey names the conversation a request belongs to: the caller's
// Session, else the session the tool names (a chat request's session_id,
// Claude Code's session header or metadata.user_id, Codex's prompt_cache_key
// or session_id header), else the request's model and first two messages or
// input items (the system prompt and the opening question), which every
// later turn of the same conversation repeats.
func sessionKey(req ChatRequest) string {
	if req.Session != "" {
		return "s:" + req.Session
	}
	var head struct {
		SessionID      string            `json:"session_id"`
		PromptCacheKey string            `json:"prompt_cache_key"`
		Messages       []json.RawMessage `json:"messages"`
		Input          json.RawMessage   `json:"input"`
		Metadata       struct {
			UserID string `json:"user_id"`
		} `json:"metadata"`
	}
	if json.Unmarshal(req.Body, &head) != nil {
		return ""
	}
	var id string
	switch protocol.NormalAPI(req.API) {
	case protocol.APIMessages:
		if id = req.Header.Get("x-claude-code-session-id"); id == "" {
			id = node.ClaudeCodeSession(head.Metadata.UserID)
		}
	case protocol.APIResponses:
		if id = head.PromptCacheKey; id == "" {
			id = req.Header.Get("session_id")
		}
	default:
		id = head.SessionID
	}
	if id != "" {
		return "s:" + id
	}
	items := head.Messages
	if protocol.NormalAPI(req.API) == protocol.APIResponses {
		items = nil
		_ = json.Unmarshal(head.Input, &items)
	}
	if len(items) == 0 {
		return ""
	}
	h := sha256.New()
	h.Write([]byte(req.Model))
	for _, m := range items[:min(2, len(items))] {
		h.Write([]byte{0})
		h.Write(m)
	}
	return "h:" + hex.EncodeToString(h.Sum(nil)[:16])
}
