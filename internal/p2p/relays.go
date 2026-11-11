package p2p

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	"github.com/libp2p/go-libp2p/p2p/protocol/ping"
	ma "github.com/multiformats/go-multiaddr"
)

// ParseRelay parses a relay's address: one full multiaddr
// (…/p2p/<relay id>), or several of the same relay separated by commas, e.g.
// its IPv4 and IPv6 addresses ("/ip4/…/p2p/X,/ip6/…/p2p/X"). A dual-stack
// relay registered that way is reachable from IPv4-only and IPv6-only users
// alike and bridges the two.
func ParseRelay(addr string) (peer.AddrInfo, error) {
	var out peer.AddrInfo
	for part := range strings.SplitSeq(addr, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		ai, err := peer.AddrInfoFromString(part)
		if err != nil {
			return peer.AddrInfo{}, fmt.Errorf("relay %q: %w", part, err)
		}
		if out.ID != "" && ai.ID != out.ID {
			return peer.AddrInfo{}, fmt.Errorf("relay %q: addresses of different peers (%s, %s)", addr, out.ID, ai.ID)
		}
		out.ID = ai.ID
		out.Addrs = append(out.Addrs, ai.Addrs...)
	}
	if out.ID == "" || len(out.Addrs) == 0 {
		return peer.AddrInfo{}, fmt.Errorf("relay %q has no address", addr)
	}
	return out, nil
}

// RelayAddr renders a relay's addresses in the form ParseRelay reads.
func RelayAddr(ai peer.AddrInfo) string {
	parts := make([]string, 0, len(ai.Addrs))
	for _, a := range ai.Addrs {
		parts = append(parts, a.String()+"/p2p/"+ai.ID.String())
	}
	return strings.Join(parts, ",")
}

// CircuitAddrs returns the addresses at which target is reachable through
// relay: <relay addr>/p2p/<relay>/p2p-circuit (the /p2p/<target> suffix is
// implied by the peerstore entry they are stored under).
func CircuitAddrs(relay peer.AddrInfo) []ma.Multiaddr {
	suffix, err := ma.NewMultiaddr("/p2p/" + relay.ID.String() + "/p2p-circuit")
	if err != nil {
		return nil
	}
	out := make([]ma.Multiaddr, 0, len(relay.Addrs))
	for _, a := range relay.Addrs {
		out = append(out, a.Encapsulate(suffix))
	}
	return out
}

// IsRelayed reports whether a connection runs through a circuit relay.
func IsRelayed(c network.Conn) bool {
	_, err := c.RemoteMultiaddr().ValueForProtocol(ma.P_CIRCUIT)
	return err == nil
}

// ---- measuring relays ----

// RelayRTT is one relay's measured round-trip time.
type RelayRTT struct {
	Addr string        `json:"addr"`
	ID   string        `json:"id"`
	RTT  time.Duration `json:"rtt_ns"` // median ping; 0 when Err is set
	Err  string        `json:"error,omitempty"`
	info peer.AddrInfo
}

// Info is the relay's address info.
func (r RelayRTT) Info() peer.AddrInfo { return r.info }

// OK reports whether the relay answered.
func (r RelayRTT) OK() bool { return r.Err == "" }

// MeasureRelays connects to every relay in parallel and pings it samples
// times. The result is sorted best first: reachable relays by median RTT,
// then unreachable ones.
func MeasureRelays(ctx context.Context, h host.Host, addrs []string, samples int) []RelayRTT {
	if samples <= 0 {
		samples = 3
	}
	out := make([]RelayRTT, len(addrs))
	var wg sync.WaitGroup
	for i, a := range addrs {
		out[i].Addr = a
		ai, err := ParseRelay(a)
		if err != nil {
			out[i].Err = err.Error()
			continue
		}
		out[i].info, out[i].ID = ai, ai.ID.String()
		wg.Add(1)
		go func(r *RelayRTT) {
			defer wg.Done()
			rtt, err := measure(ctx, h, r.info, samples)
			if err != nil {
				r.Err = err.Error()
				return
			}
			r.RTT = rtt
		}(&out[i])
	}
	wg.Wait()
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].OK() != out[j].OK() {
			return out[i].OK()
		}
		return out[i].RTT < out[j].RTT
	})
	return out
}

func measure(ctx context.Context, h host.Host, ai peer.AddrInfo, samples int) (time.Duration, error) {
	return measureWithin(ctx, h, ai, samples, 10*time.Second)
}

func measureWithin(ctx context.Context, h host.Host, ai peer.AddrInfo, samples int, timeout time.Duration) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := h.Connect(ctx, ai); err != nil {
		return 0, err
	}
	var rtts []time.Duration
	for res := range ping.Ping(ctx, h, ai.ID) {
		if res.Error != nil {
			return 0, res.Error
		}
		rtts = append(rtts, max(res.RTT, time.Microsecond)) // Windows can read 0 over loopback; 0 means "no answer" to callers
		if len(rtts) == samples {
			break
		}
	}
	if len(rtts) == 0 {
		return 0, errors.New("no ping reply")
	}
	slices.Sort(rtts)
	return rtts[len(rtts)/2], nil
}

// ---- holding reservations ----

// RelayStatus is one relay as a Reserver sees it.
type RelayStatus struct {
	Addr     string        `json:"addr"`
	Reserved bool          `json:"reserved"`
	Expires  time.Time     `json:"expires,omitzero"`
	Err      string        `json:"error,omitempty"`
	RTT      time.Duration `json:"rtt_ns,omitempty"` // latest ping of a reserved relay; 0 before the first
	Agent    string        `json:"agent,omitempty"`  // what the relay says it runs (libp2p identify), e.g. sharetoken-relay/1.2.3
}

type reservation struct {
	addr     string
	info     peer.AddrInfo
	rsv      *client.Reservation
	err      error
	retryAt  time.Time
	backoff  time.Duration
	inflight bool
	rtt      time.Duration // latest answered ping
	pingedAt time.Time     // when the last ping was started
	pinging  bool
}

// Reserved relays are pinged this often; the result is shown to users as the
// coordinator-side latency of each relay.
const pingEvery = 30 * time.Second

// Reserver keeps circuit-relay reservations on a set of relays, so that peers
// can reach this host through them although it accepts no direct
// connections. It renews reservations before they expire and re-reserves
// after a relay connection drops.
type Reserver struct {
	// Prepare, if set, runs before every reservation and renewal on a relay,
	// e.g. to present an admission pass. An error fails that attempt.
	Prepare func(ctx context.Context, relay peer.AddrInfo) error

	h        host.Host
	onChange func()
	log      *slog.Logger

	mu     sync.Mutex
	relays map[peer.ID]*reservation
	kick   chan struct{}
}

// NewReserver returns a Reserver for h. onChange, if set, is called (from the
// Reserver's goroutine) whenever the set of reserved relays changes.
func NewReserver(h host.Host, onChange func()) *Reserver {
	return &Reserver{h: h, onChange: onChange, relays: make(map[peer.ID]*reservation), kick: make(chan struct{}, 1),
		log: slog.Default().With("component", "relays")}
}

// SetRelays replaces the set of relays to hold reservations on. Reservations
// on relays still in the set are kept.
func (r *Reserver) SetRelays(addrs []string) error {
	want := make(map[peer.ID]*reservation, len(addrs))
	var errs []error
	for _, a := range addrs {
		ai, err := ParseRelay(a)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		want[ai.ID] = &reservation{addr: a, info: ai}
	}
	r.mu.Lock()
	changed := false
	for id, cur := range r.relays {
		if w, ok := want[id]; ok {
			cur.addr, cur.info = w.addr, w.info
			want[id] = cur
		} else {
			r.h.ConnManager().Unprotect(id, "sharetoken-relay")
			changed = changed || cur.rsv != nil
		}
	}
	r.relays = want
	r.mu.Unlock()
	r.Kick()
	if changed && r.onChange != nil {
		r.onChange()
	}
	return errors.Join(errs...)
}

// Kick makes Run re-check every relay now.
func (r *Reserver) Kick() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// Run maintains the reservations until ctx ends.
func (r *Reserver) Run(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		r.step(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-r.kick:
		}
	}
}

// renewBefore renews a reservation this long before it expires (relays grant
// one hour by default).
const renewBefore = 10 * time.Minute

func (r *Reserver) step(ctx context.Context) {
	now := time.Now()
	lostAny := false
	r.mu.Lock()
	for id, rs := range r.relays {
		connected := r.h.Network().Connectedness(id) == network.Connected
		if rs.rsv != nil && (!connected || now.After(rs.rsv.Expiration)) {
			rs.rsv, rs.err, lostAny = nil, errors.New("connection to relay lost"), true
		}
		if rs.rsv != nil && connected && !rs.pinging && now.Sub(rs.pingedAt) >= pingEvery {
			rs.pinging, rs.pingedAt = true, now
			go r.ping(ctx, id, rs.info)
		}
		due := rs.rsv == nil || time.Until(rs.rsv.Expiration) < renewBefore
		if !due || rs.inflight || now.Before(rs.retryAt) {
			continue
		}
		rs.inflight = true
		go r.reserve(ctx, id, rs.info)
	}
	r.mu.Unlock()
	if lostAny && r.onChange != nil {
		r.onChange()
	}
}

func (r *Reserver) reserve(ctx context.Context, id peer.ID, info peer.AddrInfo) {
	rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	r.h.ConnManager().Protect(id, "sharetoken-relay")
	var (
		rsv *client.Reservation
		err error
	)
	if r.Prepare != nil {
		err = r.Prepare(rctx, info)
	}
	if err == nil {
		rsv, err = client.Reserve(rctx, r.h, info)
	}
	changed := false
	r.mu.Lock()
	rs, ok := r.relays[id]
	if ok {
		rs.inflight = false
		if err != nil {
			changed = rs.rsv != nil
			rs.rsv, rs.err = nil, err
			rs.backoff = min(max(2*rs.backoff, 2*time.Second), time.Minute)
			rs.retryAt = time.Now().Add(rs.backoff)
		} else {
			changed = rs.rsv == nil
			rs.rsv, rs.err, rs.backoff, rs.retryAt = rsv, nil, 0, time.Time{}
		}
	}
	r.mu.Unlock()
	if !ok || ctx.Err() != nil {
		return
	}
	if err != nil {
		r.log.Warn("relay reservation failed", "relay", id, "err", err)
	} else if changed {
		r.log.Info("relay reservation held", "relay", id, "expires", rsv.Expiration.Format(time.RFC3339))
	}
	if changed && r.onChange != nil {
		r.onChange()
	}
}

// ping measures the round trip to a reserved relay. An unanswered ping
// clears the value: the reservation logic decides reachability.
func (r *Reserver) ping(ctx context.Context, id peer.ID, info peer.AddrInfo) {
	rtt, err := PingRelay(ctx, r.h, info, 5*time.Second)
	r.mu.Lock()
	defer r.mu.Unlock()
	rs, ok := r.relays[id]
	if !ok {
		return
	}
	rs.pinging = false
	if err != nil {
		rs.rtt = 0
		return
	}
	rs.rtt = max(rtt, time.Millisecond) // a LAN relay must not round to "unknown"
}

// CircuitAddrs returns this host's addresses through every relay it currently
// holds a reservation on.
func (r *Reserver) CircuitAddrs() []ma.Multiaddr {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]peer.ID, 0, len(r.relays))
	for id, rs := range r.relays {
		if rs.rsv != nil {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	var out []ma.Multiaddr
	for _, id := range ids {
		out = append(out, CircuitAddrs(r.relays[id].info)...)
	}
	return out
}

// Status reports every relay's reservation state.
func (r *Reserver) Status() []RelayStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]RelayStatus, 0, len(r.relays))
	for _, rs := range r.relays {
		st := RelayStatus{Addr: rs.addr, Reserved: rs.rsv != nil}
		if rs.rsv != nil {
			st.Expires, st.RTT = rs.rsv.Expiration, rs.rtt
		}
		if v, err := r.h.Peerstore().Get(rs.info.ID, "AgentVersion"); err == nil {
			st.Agent, _ = v.(string)
		}
		if rs.err != nil && rs.rsv == nil {
			st.Err = rs.err.Error()
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Addr < out[j].Addr })
	return out
}

// Reserved reports how many reservations are held.
func (r *Reserver) Reserved() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, rs := range r.relays {
		if rs.rsv != nil {
			n++
		}
	}
	return n
}
