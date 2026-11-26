package portal

import (
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// The portal's failure counters: wrong sign-in and two-factor codes, refused
// sign-ups, wrong pairing codes and invite links. They are per process, like
// the rate limiter; there is one portal.
//
// A table holds a bounded number of keys. When it is full it drops the keys
// whose failures have all aged out, and if that frees nothing it takes no new
// key until some do: a key that is being counted is never dropped, so filling
// the table resets nobody's counter. The counters live in three tables, so
// that keys anyone can make (one per client address) cannot crowd out the
// ones per account, and neither can touch the counters over everyone that
// pause codes and link previews (invites.go).

const (
	maxFailKeys    = 200_000
	failSweepEvery = 10 * time.Second // a full table is searched for expired keys at most this often
	failWarnEvery  = time.Hour
)

type failLog struct {
	at     []time.Time
	window time.Duration // how long a failure counts
}

func (l *failLog) prune(now time.Time) {
	cut := now.Add(-l.window)
	i := 0
	for i < len(l.at) && l.at[i].Before(cut) {
		i++
	}
	l.at = l.at[i:]
}

type failTable struct {
	mu     sync.Mutex
	logs   map[string]*failLog
	max    int // 0: maxFailKeys
	swept  time.Time
	warned time.Time
	onFull func(keys int) // told, at most every failWarnEvery, that a key found no room
}

// log returns key's failures with the aged ones dropped, or nil when it has
// none. The caller holds t.mu.
func (t *failTable) log(key string, now time.Time) *failLog {
	l := t.logs[key]
	if l == nil {
		return nil
	}
	if l.prune(now); len(l.at) == 0 {
		delete(t.logs, key)
		return nil
	}
	return l
}

// room makes key's log, or returns nil when the table is full of keys that
// still count. The caller holds t.mu.
func (t *failTable) room(key string, window time.Duration, now time.Time) *failLog {
	limit := t.max
	if limit == 0 {
		limit = maxFailKeys
	}
	if len(t.logs) >= limit && now.Sub(t.swept) >= failSweepEvery {
		t.swept = now
		for k, l := range t.logs {
			if l.prune(now); len(l.at) == 0 {
				delete(t.logs, k)
			}
		}
	}
	if len(t.logs) >= limit {
		if t.onFull != nil && now.Sub(t.warned) >= failWarnEvery {
			t.warned = now
			go t.onFull(len(t.logs))
		}
		return nil
	}
	if t.logs == nil {
		t.logs = make(map[string]*failLog)
	}
	l := &failLog{window: window}
	t.logs[key] = l
	return l
}

// count is how many of key's failures fall in the last window.
func (t *failTable) count(key string, window time.Duration, now time.Time) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	l := t.log(key, now)
	if l == nil {
		return 0
	}
	cut := now.Add(-window)
	n := len(l.at)
	for _, at := range l.at {
		if !at.Before(cut) {
			break
		}
		n--
	}
	return n
}

// add counts a failure of key, which counts for window.
func (t *failTable) add(key string, window time.Duration, now time.Time) {
	t.take(key, window, int(^uint(0)>>1), now)
}

// take counts a failure of key unless limit of them already fall in window,
// and reports whether it did: checking and counting are one step, so parallel
// requests cannot all pass a limit that only one of them should. A key the
// full table has no room for passes uncounted.
func (t *failTable) take(key string, window time.Duration, limit int, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	l := t.log(key, now)
	if l == nil {
		if limit <= 0 {
			return false
		}
		if l = t.room(key, window, now); l == nil {
			return true
		}
	}
	if len(l.at) >= limit {
		return false
	}
	l.at = append(l.at, now)
	return true
}

// giveBack removes the failure of key that take counted at at: what was
// reserved turned out to be no failure.
func (t *failTable) giveBack(key string, at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	l := t.logs[key]
	if l == nil {
		return
	}
	for i := len(l.at) - 1; i >= 0; i-- {
		if l.at[i].Equal(at) {
			l.at = append(l.at[:i:i], l.at[i+1:]...)
			break
		}
	}
	if len(l.at) == 0 {
		delete(t.logs, key)
	}
}

func (t *failTable) keys() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.logs)
}

// ---- the server's counters ----

const (
	failWindow = 5 * time.Minute
	maxFails   = 8
)

// failsFor is the table key is counted in: the counters over everyone, those
// per account (only accounts make them), and the rest, per client address.
func (s *Server) failsFor(key string) *failTable {
	switch {
	case key == codeFailsKey || key == linkFailsKey:
		return &s.siteFails
	case strings.HasPrefix(key, "u:"), strings.HasPrefix(key, "inv:u:"), strings.HasPrefix(key, "inv:ud:"), strings.HasPrefix(key, "mail:"):
		return &s.acctFails
	}
	return &s.fails
}

// failWindowOf is how long a failure of key counts.
func failWindowOf(key string) time.Duration {
	switch {
	case key == codeFailsKey || key == linkFailsKey:
		return surgeWindow
	case strings.HasPrefix(key, "inv:ud:"), strings.HasPrefix(key, "inv:ipd:"), strings.HasPrefix(key, "mail:day:"):
		return guessDay
	case strings.HasPrefix(key, "mail:"):
		return time.Hour
	case strings.HasPrefix(key, "inv:"):
		return guessWindow
	}
	return failWindow
}

// failCount counts key's failures in the last window.
func (s *Server) failCount(key string, window time.Duration) int {
	return s.failsFor(key).count(key, window, s.cfg.Now())
}

func (s *Server) throttled(key string) bool { return s.failCount(key, failWindow) >= maxFails }

func (s *Server) recordFailure(keys ...string) {
	now := s.cfg.Now()
	for _, k := range keys {
		s.failsFor(k).add(k, failWindowOf(k), now)
	}
}

// failLimit is one counter an attempt is held against: at most max in the
// key's window.
type failLimit struct {
	key string
	max int
}

// reserve counts an attempt under every limit before it is checked, and
// reports the first limit that is already used up (-1: none; nothing is
// counted then for the others). The attempt stays counted when it turns out
// wrong; release takes it back when it was right. Counting first is what
// makes the limits hold for requests sent together.
func (s *Server) reserve(limits ...failLimit) (release func(), refused int) {
	now := s.cfg.Now()
	release = func() {
		for _, l := range limits {
			s.failsFor(l.key).giveBack(l.key, now)
		}
	}
	for i, l := range limits {
		if !s.failsFor(l.key).take(l.key, failWindowOf(l.key), l.max, now) {
			for _, taken := range limits[:i] {
				s.failsFor(taken.key).giveBack(taken.key, now)
			}
			return func() {}, i
		}
	}
	return release, -1
}

// reserveThrottle is reserve for the sign-in throttles (maxFails in
// failWindow per key); ok false means one of them is used up.
func (s *Server) reserveThrottle(keys ...string) (release func(), ok bool) {
	limits := make([]failLimit, len(keys))
	for i, k := range keys {
		limits[i] = failLimit{k, maxFails}
	}
	release, refused := s.reserve(limits...)
	return release, refused < 0
}

// failTableFull tells the administrators that a table of counters is full of
// live keys: someone makes failures from very many addresses or accounts.
func (s *Server) failTableFull(what string) func(int) {
	return func(keys int) {
		s.log.Warn("failure counters are full: new "+what+" are not counted until old ones age out", "keys", keys)
		s.push("ShareToken: failure counters are full",
			"The portal counts failed sign-ins and wrong codes for "+what+", and has no room for more. "+
				"Counters already running keep counting; new ones start once old ones age out. The portal's log has the details.", 1)
	}
}

// ---- client addresses ----

// rateKey is the form of a client address that limits are keyed on. An IPv6
// address counts as its /64: a host normally has all of one, so a limit on
// the full address would be no limit.
func rateKey(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	a = a.Unmap().WithZone("")
	if a.Is4() {
		return a.String()
	}
	p, err := a.Prefix(64)
	if err != nil {
		return a.String()
	}
	return p.String()
}

// rateIP is r's client address as limits count it (rateKey). Logs and the
// audit trail keep the full address (clientIP).
func (s *Server) rateIP(r *http.Request) string { return rateKey(s.clientIP(r)) }
