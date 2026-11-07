package p2p

import (
	"context"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
)

// PingRelay pings a relay once (connecting first if needed) and returns the
// round-trip time.
func PingRelay(ctx context.Context, h host.Host, relay peer.AddrInfo, timeout time.Duration) (time.Duration, error) {
	return measureWithin(ctx, h, relay, 1, timeout)
}

// LinkQuality summarises recent pings of one link, for display.
type LinkQuality struct {
	Relay        string    `json:"relay"`
	Samples      int       `json:"samples"`           // pings in the window, answered or not
	LastMillis   float64   `json:"last_ms,omitempty"` // latest answered ping
	AvgMillis    float64   `json:"avg_ms,omitempty"`  // mean of answered pings
	MinMillis    float64   `json:"min_ms,omitempty"`
	MaxMillis    float64   `json:"max_ms,omitempty"`
	JitterMillis float64   `json:"jitter_ms"` // mean |difference| between consecutive answered pings
	LossPct      float64   `json:"loss_pct"`  // unanswered pings, percent of the window
	LastError    string    `json:"last_error,omitempty"`
	UpdatedAt    time.Time `json:"updated_at,omitzero"`
}

type linkSample struct {
	rtt time.Duration
	ok  bool
}

// LinkMonitor keeps a sliding window of ping results for one link. It
// restarts from empty when the link (relay) changes.
type LinkMonitor struct {
	mu      sync.Mutex
	size    int
	relay   string
	samples []linkSample
	lastErr string
	updated time.Time
}

// NewLinkMonitor returns a monitor that keeps the last size pings.
func NewLinkMonitor(size int) *LinkMonitor {
	if size <= 0 {
		size = 30
	}
	return &LinkMonitor{size: size}
}

// Add records one ping of relay; err means it went unanswered. A ping of a
// different relay than before discards the old window.
func (m *LinkMonitor) Add(relay string, rtt time.Duration, err error, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if relay != m.relay {
		m.relay, m.samples, m.lastErr = relay, nil, ""
	}
	s := linkSample{rtt: rtt, ok: err == nil}
	if err != nil {
		m.lastErr = err.Error()
	} else {
		m.lastErr = ""
	}
	m.samples = append(m.samples, s)
	if len(m.samples) > m.size {
		m.samples = m.samples[len(m.samples)-m.size:]
	}
	m.updated = now
}

// Reset forgets the window, e.g. when the link is switched to relay.
func (m *LinkMonitor) Reset(relay string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.relay, m.samples, m.lastErr, m.updated = relay, nil, "", time.Time{}
}

// Quality summarises the window; nil before the first ping.
func (m *LinkMonitor) Quality() *LinkQuality {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.samples) == 0 {
		return nil
	}
	q := &LinkQuality{Relay: m.relay, Samples: len(m.samples), LastError: m.lastErr, UpdatedAt: m.updated}
	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
	var (
		sum, jit, minR, maxR time.Duration
		ok, lost, pairs      int
		prev                 *linkSample
	)
	for i := range m.samples {
		s := &m.samples[i]
		if !s.ok {
			lost++
			continue
		}
		if ok == 0 || s.rtt < minR {
			minR = s.rtt
		}
		maxR = max(maxR, s.rtt)
		sum += s.rtt
		ok++
		if prev != nil {
			d := s.rtt - prev.rtt
			jit += max(d, -d)
			pairs++
		}
		prev = s
		q.LastMillis = ms(s.rtt)
	}
	if ok > 0 {
		q.AvgMillis, q.MinMillis, q.MaxMillis = ms(sum/time.Duration(ok)), ms(minR), ms(maxR)
	}
	if pairs > 0 {
		q.JitterMillis = ms(jit / time.Duration(pairs))
	}
	q.LossPct = 100 * float64(lost) / float64(len(m.samples))
	return q
}
