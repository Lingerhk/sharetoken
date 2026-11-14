package e2e

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sharetoken/internal/buyer"
	"sharetoken/internal/node"
)

// pausingUpstream streams "Hello", stays silent for pause (a reasoning model
// thinking), then finishes; with pause < 0 it never finishes.
func pausingUpstream(t *testing.T, pause time.Duration) *httptest.Server {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n")
		fl.Flush()
		wait := make(<-chan time.Time)
		if pause >= 0 {
			wait = time.After(pause)
		}
		select {
		case <-wait:
		case <-release:
			return
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\", world!\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	return srv
}

func heartbeat(d time.Duration) func(*node.Config) { return func(c *node.Config) { c.Heartbeat = d } }

// A model that is silent for longer than the buyer's idle timeout is not
// taken for a dead node: the node's progress frames keep the job alive.
func TestProgressFramesKeepASilentJobAlive(t *testing.T) {
	m := newMarket(t)
	m.addNodeCfg(pausingUpstream(t, 2500*time.Millisecond).URL, priceIn, priceOut, nil, heartbeat(300*time.Millisecond))
	m.waitOffers(1)
	cl, _, _ := m.addBuyer(5_000_000)
	cl.IdleTimeout = time.Second
	var got strings.Builder
	res, err := cl.Chat(m.ctx, buyer.ChatRequest{Model: model, Body: []byte(body), BudgetMicros: 1_000_000}, func(p []byte) error {
		got.Write(p)
		return nil
	})
	if err != nil || res.Content != "Hello, world!" || !res.Verified {
		t.Fatalf("silent job: %v %+v", err, res)
	}
	if res.Timing.Progress < 3 {
		t.Fatalf("want progress frames during the pause, got %d", res.Timing.Progress)
	}
}

// Progress frames do not keep a hung model server alive forever: after
// MaxSilence without data the buyer gives up and, as nothing reached the
// caller, fails over.
func TestProgressFramesDoNotHideAHungModel(t *testing.T) {
	m := newMarket(t)
	_, hung := m.addNodeCfg(pausingUpstream(t, -1).URL, priceIn/2, priceOut/2, nil, heartbeat(200*time.Millisecond))
	_, healthy := m.addNode(mockUpstream(t, "mock-7b", false).URL, priceIn, priceOut)
	m.waitOffers(2)
	cl, _, _ := m.addBuyer(5_000_000)
	cl.IdleTimeout, cl.MaxSilence = time.Second, 2*time.Second
	start := time.Now()
	res, err := cl.Chat(m.ctx, buyer.ChatRequest{Model: model, Body: []byte(body), BudgetMicros: 1_000_000}, nil)
	if err != nil || res.Ticket.Node != healthy.ID() {
		t.Fatalf("want failover from the hung node %s: %v %+v", hung.ID(), err, res)
	}
	if d := time.Since(start); d > 8*time.Second {
		t.Fatalf("failover took %v", d)
	}
}

// A conversation stays on its node while the node is available, even when a
// cheaper one comes online; another conversation goes to the cheaper one.
func TestSessionStaysOnItsNode(t *testing.T) {
	m := newMarket(t)
	_, first := m.addNode(mockUpstream(t, "mock-7b", false).URL, priceIn, priceOut)
	m.waitOffers(1)
	cl, _, _ := m.addBuyer(5_000_000)
	chat := func(session string) *buyer.Result {
		t.Helper()
		res, err := cl.Chat(m.ctx, buyer.ChatRequest{Model: model, Body: []byte(body), BudgetMicros: 1_000_000, Session: session}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := chat("a"); res.Ticket.Node != first.ID() || res.Timing.Preferred {
		t.Fatalf("first turn: %s preferred=%v", res.Ticket.Node, res.Timing.Preferred)
	}
	_, cheap := m.addNode(mockUpstream(t, "mock-7b", false).URL, priceIn/2, priceOut/2)
	m.waitOffers(2)
	if res := chat("a"); res.Ticket.Node != first.ID() || !res.Timing.Preferred || !res.Timing.Reused {
		t.Fatalf("second turn: served by %s preferred=%v reused=%v, want %s", res.Ticket.Node, res.Timing.Preferred, res.Timing.Reused, first.ID())
	}
	if res := chat("b"); res.Ticket.Node != cheap.ID() {
		t.Fatalf("another session: served by %s, want the cheaper %s", res.Ticket.Node, cheap.ID())
	}
}

// A request near the 32 MiB limit that does not compress (an image in base64)
// travels in many frames and is served.
func TestLargeIncompressibleRequest(t *testing.T) {
	m := newMarket(t)
	m.addNode(mockUpstream(t, "mock-7b", false).URL, 1, 1)
	m.waitOffers(1)
	cl, _, _ := m.addBuyer(5_000_000)
	raw := make([]byte, 22<<20)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	img := base64.StdEncoding.EncodeToString(raw) // about 29 MiB
	req := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":[{"type":"text","text":"what is this?"},{"type":"image_url","image_url":{"url":"data:image/png;base64,%s"}}]}]}`, model, img)
	res, err := cl.Chat(m.ctx, buyer.ChatRequest{Model: model, Body: []byte(req), BudgetMicros: 1_000_000}, nil)
	if err != nil || res.Content != "Hello, world!" {
		t.Fatalf("large request: %v", err)
	}
	if res.Timing.UpBytes < int64(len(req)) {
		t.Fatalf("sent %d bytes for a %d byte request that does not compress", res.Timing.UpBytes, len(req))
	}
}
