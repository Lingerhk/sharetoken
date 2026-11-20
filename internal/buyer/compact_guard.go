package buyer

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"sharetoken/internal/protocol"
)

// A compaction that keeps failing stops being asked for a while. Codex asks
// to compact a long conversation again at every step of a turn once it is
// past its limit, and drops a failed compaction without a word; a
// conversation whose summaries never come (a seller cutting each one short)
// would pay for the whole conversation as input at every step, the
// conversation never shorter. After compactFailsMost failures in a row of
// one conversation on one model, each within compactHeldFor of the one
// before, the next is answered at once with a 400, which Codex does not ask
// again, until compactHeldFor has passed since the last failure. A summary
// that comes clears the count. A conversation is Codex's prompt_cache_key; a
// request without one is not counted (after magpie's codex_compact_guard.go).

const (
	compactFailsMost = 3
	compactHeldFor   = 5 * time.Minute
)

// compactFails counts failed compactions by conversation. The zero value is
// ready to use.
type compactFails struct {
	mu sync.Mutex
	m  map[string]compactFail
}

type compactFail struct {
	n    int
	last time.Time
	why  string
}

// compactKey is the model and conversation of a compaction request, "" for
// one that names no conversation.
func compactKey(body []byte) string {
	var q struct {
		Model string `json:"model"`
		Key   string `json:"prompt_cache_key"`
	}
	if json.Unmarshal(body, &q) != nil || q.Key == "" {
		return ""
	}
	return q.Model + "\x00" + q.Key
}

// held says the conversation's compaction failed compactFailsMost times in
// a row lately, and what to tell Codex instead of asking again.
func (f *compactFails) held(key, model string) (string, bool) {
	if key == "" {
		return "", false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.m[key]
	if !ok || c.n < compactFailsMost || time.Since(c.last) >= compactHeldFor {
		return "", false
	}
	left := (compactHeldFor - time.Since(c.last)).Round(time.Second)
	return fmt.Sprintf("couldn't compact the conversation: %s failed %d times in a row (last: %s); sharetoken waits %s before asking again. To compact now, switch this conversation to another model.",
		model, c.n, c.why, left), true
}

// failed counts a failed compaction, unless nothing was paid for it and it
// is fixed elsewhere: the account (credits, budget, sign-in).
func (f *compactFails) failed(key string, err error) {
	if key == "" || err == nil {
		return
	}
	switch codeFor(err) {
	case protocol.CodeUnauthorized, protocol.CodeForbidden, protocol.CodeInsufficientFunds, protocol.CodeBudget:
		return
	}
	why := err.Error()
	if len(why) > 300 {
		why = why[:300] + "…"
	}
	now := time.Now()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.m == nil {
		f.m = map[string]compactFail{}
	}
	c := f.m[key]
	if now.Sub(c.last) >= compactHeldFor {
		c.n = 0 // the ones before are too long ago to say it keeps failing
	}
	c.n, c.last, c.why = c.n+1, now, why
	f.m[key] = c
	for k, v := range f.m {
		if now.Sub(v.last) >= compactHeldFor {
			delete(f.m, k)
		}
	}
}

// done clears the conversation's count: its summary came.
func (f *compactFails) done(key string) {
	if key == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.m, key)
}
