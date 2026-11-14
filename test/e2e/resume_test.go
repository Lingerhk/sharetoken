package e2e

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/host"

	"sharetoken/internal/buyer"
	"sharetoken/internal/coordinator"
	"sharetoken/internal/node"
)

// pacedUpstream streams n words, one every delay, and reports when the
// node's request to it ended early (the node stopped the model).
func pacedUpstream(t *testing.T, n int, delay time.Duration) (*httptest.Server, <-chan time.Time) {
	stopped := make(chan time.Time, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for i := 0; i < n; i++ {
			select {
			case <-r.Context().Done():
				stopped <- time.Now()
				return
			case <-time.After(delay):
			}
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"w%03d \"}}]}\n\n", i)
			fl.Flush()
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprintf(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":%d}}\n\n", 2*n)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv, stopped
}

func wantWords(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "w%03d ", i)
	}
	return b.String()
}

// The connection between buyer and node breaks in the middle of a stream:
// the buyer resumes the job on a new connection and gets the whole answer,
// verified, with every payload exactly once.
func TestJobResumesAfterTheConnectionBreaks(t *testing.T) {
	for _, side := range []string{"buyer", "node"} {
		t.Run(side, func(t *testing.T) {
			m := newMarket(t)
			up, _ := pacedUpstream(t, 60, 15*time.Millisecond)
			nodeHost, _ := m.addNode(up.URL, priceIn, priceOut)
			m.waitOffers(1)
			cl, buyerHost, _ := m.addBuyer(5_000_000)
			var once sync.Once
			var got strings.Builder
			chunks := 0
			res, err := cl.Chat(m.ctx, buyer.ChatRequest{Model: model, Body: []byte(body), BudgetMicros: 1_000_000}, func(p []byte) error {
				got.Write(p)
				if chunks++; chunks == 10 {
					once.Do(func() {
						var h, other host.Host = buyerHost, nodeHost
						if side == "node" {
							h, other = nodeHost, buyerHost
						}
						_ = h.Network().ClosePeer(other.ID())
					})
				}
				return nil
			})
			if err != nil {
				t.Fatalf("job failed: %v", err)
			}
			if res.Content != wantWords(60) || !res.Verified {
				t.Fatalf("content %q verified=%v", res.Content, res.Verified)
			}
			if res.Timing.Resumes != 1 {
				t.Fatalf("resumes = %d, want 1", res.Timing.Resumes)
			}
		})
	}
}

// A caller that stops (the user pressed stop) makes the node stop its model
// at once rather than wait for a resume, and pays for the part delivered.
func TestCancelStopsTheModelAtOnce(t *testing.T) {
	m := newMarketWith(t, coordinator.Config{DisputeWindow: time.Second})
	up, stopped := pacedUpstream(t, 400, 20*time.Millisecond)
	m.addNode(up.URL, priceIn, priceOut)
	m.waitOffers(1)
	cl, _, buyerKeys := m.addBuyer(5_000_000)
	stop := errors.New("user pressed stop")
	n := 0
	var at time.Time
	_, err := cl.Chat(m.ctx, buyer.ChatRequest{Model: model, Body: []byte(body), BudgetMicros: 1_000_000}, func([]byte) error {
		if n++; n == 5 {
			at = time.Now()
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatalf("want the caller's error, got %v", err)
	}
	select {
	case when := <-stopped:
		if d := when.Sub(at); d > 2*time.Second {
			t.Fatalf("the model ran on for %v after the cancel", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the node did not stop its model")
	}
	waitFor(t, 15*time.Second, "the delivered part to be billed", func() bool {
		return m.held(buyerKeys) == 0 && m.balance(buyerKeys) < 5_000_000
	})
}

// A buyer that never comes back: after the resume window the node stops the
// model and bills what reached the buyer, without penalty.
func TestBrokenJobIsBilledAfterTheResumeWindow(t *testing.T) {
	m := newMarketWith(t, coordinator.Config{DisputeWindow: time.Second})
	up, stopped := pacedUpstream(t, 400, 20*time.Millisecond)
	_, nodeKeys := m.addNodeCfg(up.URL, priceIn, priceOut, nil, func(c *node.Config) { c.ResumeWindow = time.Second })
	m.waitOffers(1)
	cl, buyerHost, buyerKeys := m.addBuyer(5_000_000)
	cl.ResumeWindow = 0 // this buyer does not resume
	var n atomic.Int32
	_, err := cl.Chat(m.ctx, buyer.ChatRequest{Model: model, Body: []byte(body), BudgetMicros: 1_000_000}, func([]byte) error {
		if n.Add(1) == 5 {
			_ = buyerHost.Network().ClosePeer(nodeKeys.PeerID)
		}
		return nil
	})
	if err == nil {
		t.Fatal("the job went on without the buyer")
	}
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("the node did not stop its model after the resume window")
	}
	waitFor(t, 15*time.Second, "the delivered part to be billed", func() bool {
		return m.held(buyerKeys) == 0 && m.balance(buyerKeys) < 5_000_000
	})
	if st, _ := m.coord.Engine.NodeStats(nodeKeys.ID()); st.Failures != 0 {
		t.Fatalf("node penalized for the buyer leaving: %+v", st)
	}
}
