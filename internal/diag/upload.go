package diag

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// Rejected wraps a send's error when the portal refused the report for good
// (it is malformed or too big): it is dropped instead of sent again.
type Rejected struct{ Err error }

func (r Rejected) Error() string { return "report rejected: " + r.Err.Error() }
func (r Rejected) Unwrap() error { return r.Err }

// ErrNotLoggedIn is what Send returns while nobody is logged in: the reports
// wait for a login.
var ErrNotLoggedIn = errors.New("diag: not logged in")

// Uploader sends the queued reports to the portal.
type Uploader struct {
	Queue   Queue
	Enabled func() bool                         // read before each round
	Send    func(context.Context, Report) error // with the user's login
	First   time.Duration                       // until the first round (0: 30s)
	Every   time.Duration                       // between rounds (0: 10m)

	mu       sync.Mutex
	lastSent int64
	lastErr  string
	kick     chan struct{}
}

// Status is the last round's outcome.
func (u *Uploader) Status() (lastSent int64, lastErr string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.lastSent, u.lastErr
}

// Kick starts a round now (after a login, or when reports were turned on).
func (u *Uploader) Kick() {
	select {
	case u.ch() <- struct{}{}:
	default:
	}
}

func (u *Uploader) ch() chan struct{} {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.kick == nil {
		u.kick = make(chan struct{}, 1)
	}
	return u.kick
}

// Run sends rounds until ctx ends.
func (u *Uploader) Run(ctx context.Context) {
	k := u.ch()
	first, every := cmpDur(u.First, 30*time.Second), cmpDur(u.Every, 10*time.Minute)
	t := time.NewTimer(first)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-k:
			if !t.Stop() {
				select {
				case <-t.C:
				default:
				}
			}
		}
		u.Flush(ctx)
		t.Reset(every)
	}
}

func cmpDur(v, def time.Duration) time.Duration {
	if v > 0 {
		return v
	}
	return def
}

// Flush sends every queued report once, and returns how many were sent. While
// reports are off it sends nothing and empties the queue.
func (u *Uploader) Flush(ctx context.Context) int {
	if u.Enabled != nil && !u.Enabled() {
		_ = u.Queue.Clear()
		return 0
	}
	reps, err := u.Queue.List()
	if err != nil || len(reps) == 0 {
		return 0
	}
	sent := 0
	var lastErr error
	for _, r := range reps {
		sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := u.Send(sctx, r)
		cancel()
		var rej Rejected
		switch {
		case err == nil:
			sent++
			_ = u.Queue.Remove(r.ID)
		case errors.As(err, &rej):
			slog.Warn("the portal refused a crash report; dropping it", "component", "diag", "err", err)
			_ = u.Queue.Remove(r.ID)
		default:
			lastErr = err // try again in the next round
		}
		if err != nil && !errors.As(err, &rej) {
			break // the portal is away, or nobody is logged in: the rest waits too
		}
	}
	u.mu.Lock()
	if sent > 0 {
		u.lastSent = time.Now().Unix()
	}
	u.lastErr = ""
	if lastErr != nil && !errors.Is(lastErr, ErrNotLoggedIn) {
		u.lastErr = lastErr.Error()
	}
	u.mu.Unlock()
	if sent > 0 {
		slog.Info("sent crash reports", "component", "diag", "count", sent)
	}
	return sent
}
