package buyer

import (
	"context"
	"slices"
	"sync"
	"time"

	"sharetoken/internal/protocol"
)

// The model list is asked of the coordinator once and kept (after magpie's
// Codex list, #3734): Codex gives its /models up after 5 seconds and then
// runs on its default metadata, and every Codex start, every Claude Code
// start and every model picker asks again. Within modelsFresh the kept list
// is handed out as it is; past it, it is still handed out at once while one
// ask goes in the background (stale while revalidate), up to modelsStale;
// with none, or one older than that, callers wait on one shared ask.
const (
	modelsFresh = time.Minute
	modelsStale = 10 * time.Minute
	// modelsAskTimeout bounds a background ask, which no caller waits on.
	modelsAskTimeout = 30 * time.Second
)

// modelsCache holds the last model list. Its zero value is ready.
type modelsCache struct {
	mu      sync.Mutex
	list    []protocol.ModelSummary
	at      time.Time     // when list was fetched; zero: never
	pending chan struct{} // closed when the ask under way ends; nil: none
	err     error         // what the last ask that ended failed with
	now     func() time.Time
}

func (c *modelsCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// get is the model list, asked of fetch when the kept one is missing or
// old. A list past modelsFresh is returned at once and refreshed in the
// background; a failed refresh keeps it.
func (c *modelsCache) get(ctx context.Context, fetch func(context.Context) ([]protocol.ModelSummary, error)) ([]protocol.ModelSummary, error) {
	c.mu.Lock()
	age := c.clock().Sub(c.at)
	if !c.at.IsZero() && age < modelsStale {
		list := slices.Clone(c.list)
		if age >= modelsFresh && c.pending == nil {
			c.ask(fetch)
		}
		c.mu.Unlock()
		return list, nil
	}
	if c.pending == nil {
		c.ask(fetch)
	}
	wait := c.pending
	c.mu.Unlock()
	select {
	case <-wait:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil && (c.at.IsZero() || c.clock().Sub(c.at) >= modelsStale) {
		return nil, c.err
	}
	return slices.Clone(c.list), nil
}

// ask starts one fetch, on a context of its own: a caller that gives up
// does not end it, and the callers after get its answer. Called with mu
// held.
func (c *modelsCache) ask(fetch func(context.Context) ([]protocol.ModelSummary, error)) {
	done := make(chan struct{})
	c.pending = done
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), modelsAskTimeout)
		defer cancel()
		list, err := fetch(ctx)
		c.mu.Lock()
		defer c.mu.Unlock()
		c.err = err
		if err == nil {
			c.list, c.at = list, c.clock()
		}
		c.pending = nil
		close(done)
	}()
}
