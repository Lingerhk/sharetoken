package buyer

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sharetoken/internal/protocol"
)

type fakeCoordinator struct {
	calls atomic.Int32
	mu    sync.Mutex
	model string
	err   error
	gate  chan struct{} // when set, each fetch waits for it
}

func (f *fakeCoordinator) fetch(ctx context.Context) ([]protocol.ModelSummary, error) {
	f.calls.Add(1)
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return []protocol.ModelSummary{{Model: f.model}}, nil
}

func (f *fakeCoordinator) set(model string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.model, f.err = model, err
}

func TestModelsCacheFreshStaleAndOld(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	var mu sync.Mutex
	c := &modelsCache{now: func() time.Time { mu.Lock(); defer mu.Unlock(); return now }}
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	f := &fakeCoordinator{model: "a"}
	get := func() string {
		t.Helper()
		ms, err := c.get(context.Background(), f.fetch)
		if err != nil || len(ms) != 1 {
			t.Fatalf("get: %v %v", ms, err)
		}
		return ms[0].Model
	}
	if got := get(); got != "a" || f.calls.Load() != 1 {
		t.Fatalf("first get: %s after %d asks", got, f.calls.Load())
	}
	f.set("b", nil)
	advance(30 * time.Second)
	if got := get(); got != "a" || f.calls.Load() != 1 {
		t.Fatalf("a fresh list is handed out without asking: %s, %d asks", got, f.calls.Load())
	}
	// Stale: the old list at once, one ask in the background.
	advance(time.Minute)
	if got := get(); got != "a" {
		t.Fatalf("a stale list is handed out at once, got %s", got)
	}
	waitAsks(t, c)
	if got := get(); got != "b" || f.calls.Load() != 2 {
		t.Fatalf("after the background ask: %s, %d asks", got, f.calls.Load())
	}
	// A failed refresh keeps the list.
	f.set("c", errors.New("coordinator down"))
	advance(2 * time.Minute)
	if got := get(); got != "b" {
		t.Fatalf("got %s", got)
	}
	waitAsks(t, c)
	if got := get(); got != "b" {
		t.Fatalf("a failed refresh must keep the list, got %s", got)
	}
	// Too old to hand out: the caller waits, and sees the failure.
	advance(time.Hour)
	if _, err := c.get(context.Background(), f.fetch); err == nil {
		t.Fatal("a list past modelsStale with the coordinator down must fail")
	}
	f.set("d", nil)
	if got := get(); got != "d" {
		t.Fatalf("got %s", got)
	}
}

// Callers that come together while no list is kept share one ask.
func TestModelsCacheSharesOneAsk(t *testing.T) {
	c := &modelsCache{}
	f := &fakeCoordinator{model: "a", gate: make(chan struct{})}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ms, err := c.get(context.Background(), f.fetch); err != nil || len(ms) != 1 {
				t.Errorf("get: %v %v", ms, err)
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(f.gate)
	wg.Wait()
	if n := f.calls.Load(); n != 1 {
		t.Fatalf("%d asks, want 1", n)
	}
	// A caller that gives up does not end the ask for the others.
	c2 := &modelsCache{}
	f2 := &fakeCoordinator{model: "a", gate: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c2.get(ctx, f2.fetch); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	close(f2.gate)
	if ms, err := c2.get(context.Background(), f2.fetch); err != nil || len(ms) != 1 || f2.calls.Load() != 1 {
		t.Fatalf("the ask must go on: %v %v after %d asks", ms, err, f2.calls.Load())
	}
}

func waitAsks(t *testing.T, c *modelsCache) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c.mu.Lock()
		idle := c.pending == nil
		c.mu.Unlock()
		if idle {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the background ask did not end")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
