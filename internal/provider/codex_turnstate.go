package provider

import (
	"net/http"
	"sync"
	"time"
)

// The ChatGPT backend names a turn's routing in the x-codex-turn-state header
// of the turn's first reply, and Codex sends it back on every later request
// of that turn (each tool call's result), which the backend then serves as
// the same turn. Sent without it, each request of the turn is a new one
// there, queued again ("529 high load"). Codex never sees the header through
// the marketplace, so the seller keeps it: the state the account's reply
// gave for a turn goes with that turn's later requests to the same account
// (after magpie's codex_turnstate.go). A turn is the session and the turn_id
// Codex names.

const (
	// codexTurnStateFor is how long a turn's state is kept after its last
	// use: a turn's tool calls come seconds or minutes apart.
	codexTurnStateFor  = 2 * time.Hour
	codexTurnStateMost = 4096
)

// codexTurnStates is one upstream's turn states by account, session and turn.
// The zero value is ready to use.
type codexTurnStates struct {
	mu sync.Mutex
	m  map[string]codexTurnState
}

type codexTurnState struct {
	state string
	at    time.Time
}

// get is the state the account's reply gave for the turn, or "".
func (c *codexTurnStates) get(key string) string {
	if key == "" {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	st, ok := c.m[key]
	now := timeNow()
	if !ok || now.Sub(st.at) > codexTurnStateFor {
		delete(c.m, key)
		return ""
	}
	st.at = now
	c.m[key] = st
	return st.state
}

// put keeps the state a reply gave for the turn.
func (c *codexTurnStates) put(key, state string) {
	if key == "" || state == "" || len(state) > 4096 {
		return
	}
	now := timeNow()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]codexTurnState{}
	}
	if _, ok := c.m[key]; !ok && len(c.m) >= codexTurnStateMost {
		var oldest string
		for k, st := range c.m {
			if now.Sub(st.at) > codexTurnStateFor {
				delete(c.m, k)
			} else if oldest == "" || st.at.Before(c.m[oldest].at) {
				oldest = k
			}
		}
		if len(c.m) >= codexTurnStateMost {
			delete(c.m, oldest)
		}
	}
	c.m[key] = codexTurnState{state: state, at: now}
}

// codexTurnKey is the key of a request's turn at an account: "" when the
// request names no turn. The buyer sends the turn_id Codex gave as the
// x-codex-turn-id header (Codex's client_metadata does not reach the seller).
func codexTurnKey(account, session, turn string) string {
	if turn == "" {
		return ""
	}
	return account + "\x00" + session + "\x00" + turn
}

// turnStateTap is an HTTP client that hands each response's
// x-codex-turn-state to heard.
type turnStateTap struct {
	next  httpDoer
	heard func(string)
}

func (t turnStateTap) Do(r *http.Request) (*http.Response, error) {
	res, err := t.next.Do(r)
	if err == nil && res != nil {
		if v := res.Header.Get("X-Codex-Turn-State"); v != "" {
			t.heard(v)
		}
	}
	return res, err
}
