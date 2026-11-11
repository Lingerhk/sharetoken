package p2p

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

func TestLinkMonitorWindow(t *testing.T) {
	m := NewLinkMonitor(4)
	if m.Quality() != nil {
		t.Fatal("quality before any ping")
	}
	now := time.Unix(1_800_000_000, 0)
	ms := time.Millisecond
	m.Add("r1", 10*ms, nil, now)
	m.Add("r1", 0, errors.New("timeout"), now)
	m.Add("r1", 14*ms, nil, now)
	m.Add("r1", 12*ms, nil, now)
	q := m.Quality()
	// answered 10, 14, 12: avg 12, jitter (4+2)/2 = 3, one of four lost.
	if q.Relay != "r1" || q.Samples != 4 || q.LastMillis != 12 || q.AvgMillis != 12 || q.MinMillis != 10 ||
		q.MaxMillis != 14 || q.JitterMillis != 3 || q.LossPct != 25 || q.LastError != "" {
		t.Fatalf("quality: %+v", q)
	}

	// The window slides: the oldest (10 ms) and the loss drop out.
	m.Add("r1", 16*ms, nil, now)
	m.Add("r1", 16*ms, nil, now)
	if q := m.Quality(); q.Samples != 4 || q.LossPct != 0 || q.MinMillis != 12 || q.AvgMillis != 14.5 {
		t.Fatalf("after sliding: %+v", q)
	}

	// Another relay starts a fresh window; all lost reports the error.
	m.Add("r2", 0, errors.New("no route"), now)
	if q := m.Quality(); q.Relay != "r2" || q.Samples != 1 || q.LossPct != 100 || q.AvgMillis != 0 || q.LastError != "no route" {
		t.Fatalf("new relay: %+v", q)
	}
	m.Reset("r3")
	if m.Quality() != nil {
		t.Fatal("quality after reset")
	}
}

func TestPingRelay(t *testing.T) {
	r := newRelayHost(t)
	h := newRelayHost(t)
	ai := peer.AddrInfo{ID: r.ID(), Addrs: r.Addrs()}
	rtt, err := PingRelay(context.Background(), h, ai, 5*time.Second)
	if err != nil || rtt <= 0 {
		t.Fatalf("rtt %v err %v", rtt, err)
	}
	_ = r.Close()
	if _, err := PingRelay(context.Background(), h, ai, time.Second); err == nil {
		t.Fatal("ping of a closed relay succeeded")
	}
}

func TestLinkMonitorLossy(t *testing.T) {
	m := NewLinkMonitor(30)
	now := time.Unix(0, 0)
	lost := errors.New("timeout")
	for range 3 {
		m.Add("r", 0, lost, now)
	}
	if m.Lossy() {
		t.Fatal("lossy on too few samples")
	}
	for range 7 {
		m.Add("r", time.Millisecond, nil, now)
	}
	if !m.Lossy() { // 3 of 10 lost
		t.Fatal("30% loss not reported")
	}
	m.Add("r", time.Millisecond, nil, now)
	if m.Lossy() { // 3 of 11
		t.Fatal("27% loss reported as lossy")
	}
	m.Add("other", 0, lost, now) // a new relay starts a new window
	if m.Lossy() {
		t.Fatal("old relay's losses carried over")
	}
}
