package tailnet

import (
	"context"
	"sort"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	"tailscale.com/types/key"
)

// A buyer's first stream to a node goes over whatever connects first, and
// that is the circuit relay: the node's relay is one connection away, the
// tunnel several round trips through DERP. libp2p's dialer stops the other
// attempts when one succeeds, so left to itself it would never finish the
// tunnel. Reach therefore dials the node's tailnet address on its own, as
// libp2p's hole punching does for the addresses it knows: with a direct
// connection required. Once it stands, libp2p opens every new stream on it
// (it prefers a direct connection to a relayed one), and the relayed
// connection is left to end by itself.

// reach is one attempt to bring up the direct connection to a peer.
type reach struct {
	done    chan struct{}
	err     error
	retryAt time.Time
}

// reachTimeout bounds one attempt; reachRetry is how long a failed attempt
// keeps the next one off (a node that cannot be reached this way is asked
// again for every job otherwise).
const (
	reachTimeout = 30 * time.Second
	reachRetry   = time.Minute
)

// Reach makes sure a direct connection to p is being set up through the
// tailnet address among addrs (and its addresses on a Tailscale network this
// machine is on too), if there is one and no direct connection stands yet. It waits up to wait for it (0: not at all) and reports
// whether p has a direct connection when it returns.
func (t *Transport) Reach(ctx context.Context, h host.Host, p peer.ID, addrs []ma.Multiaddr, wait time.Duration) bool {
	if HasDirect(h, p) {
		return true
	}
	if !t.enabled.Load() {
		return false
	}
	tn := append(Addrs(addrs), systemPeerAddrs(addrs)...)
	if len(tn) == 0 {
		return false
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return false
	}
	r := t.reaching[p]
	switch {
	case r != nil && r.err == nil && !isDone(r.done): // under way
	case r != nil && r.err != nil && time.Now().Before(r.retryAt):
		t.mu.Unlock()
		return false
	default:
		r = &reach{done: make(chan struct{})}
		t.reaching[p] = r
		go t.connect(h, p, tn, r)
	}
	t.mu.Unlock()
	if wait <= 0 {
		return false
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-r.done:
	case <-timer.C:
	case <-ctx.Done():
	}
	return HasDirect(h, p)
}

func (t *Transport) connect(h host.Host, p peer.ID, addrs []ma.Multiaddr, r *reach) {
	ctx, cancel := context.WithTimeout(context.Background(), reachTimeout)
	defer cancel()
	t0 := time.Now()
	err := h.Connect(network.WithForceDirectDial(ctx, "tailnet"), peer.AddrInfo{ID: p, Addrs: addrs})
	t.mu.Lock()
	r.err = err
	if err != nil {
		r.retryAt = time.Now().Add(reachRetry)
	} else {
		delete(t.reaching, p)
	}
	t.mu.Unlock()
	close(r.done)
	if err != nil {
		t.log.Info("no tunnel to the node; staying on its other paths", "peer", p, "err", err)
		return
	}
	// Connect also returns when another direct connection came first (the
	// node is on this network, or libp2p's hole punch won).
	for _, c := range h.Network().ConnsToPeer(p) {
		if Is(c.RemoteMultiaddr()) {
			t.log.Info("direct connection over the tailnet", "peer", p, "took", time.Since(t0).Round(time.Millisecond))
			return
		}
	}
	for _, c := range h.Network().ConnsToPeer(p) {
		if _, err := c.RemoteMultiaddr().ValueForProtocol(ma.P_CIRCUIT); err != nil {
			t.log.Info("direct connection without a tunnel", "peer", p, "addr", c.RemoteMultiaddr(), "took", time.Since(t0).Round(time.Millisecond))
			return
		}
	}
}

func isDone(c chan struct{}) bool {
	select {
	case <-c:
		return true
	default:
		return false
	}
}

// HasDirect reports whether h has a connection to p that does not run
// through a circuit relay. (A relayed connection is told by its address:
// libp2p marks it limited only when the relay caps its circuits.)
func HasDirect(h host.Host, p peer.ID) bool {
	for _, c := range h.Network().ConnsToPeer(p) {
		if _, err := c.RemoteMultiaddr().ValueForProtocol(ma.P_CIRCUIT); err != nil && !c.IsClosed() {
			return true
		}
	}
	return false
}

// ---- upkeep ----

// Run closes what is no longer used, until ctx ends: outgoing tunnel
// connections that carried no stream for Config.IdleClose, and tunnels
// without a connection. It also keeps each tunnel's path current.
func (t *Transport) Run(ctx context.Context, h host.Host) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	idle := map[network.Conn]time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			t.sweep(h, idle, now)
		}
	}
}

func (t *Transport) sweep(h host.Host, idle map[network.Conn]time.Time, now time.Time) {
	live := map[network.Conn]bool{}
	for _, c := range h.Network().Conns() {
		if c.Stat().Direction != network.DirOutbound || !Is(c.RemoteMultiaddr()) {
			continue
		}
		live[c] = true
		if len(c.GetStreams()) > 0 {
			delete(idle, c)
			continue
		}
		since, ok := idle[c]
		if !ok {
			idle[c] = now
			continue
		}
		if now.Sub(since) >= t.cfg.IdleClose {
			t.log.Debug("closing an idle tunnel connection", "peer", c.RemotePeer())
			_ = c.Close()
			delete(idle, c)
		}
	}
	for c := range idle {
		if !live[c] {
			delete(idle, c)
		}
	}

	t.mu.Lock()
	var gone, used []*tunnel
	for node, tun := range t.tunnels {
		switch {
		case tun.refs > 0:
			used = append(used, tun)
		case now.Sub(tun.idleFrom) >= tunnelLinger:
			delete(t.tunnels, node)
			gone = append(gone, tun)
		}
	}
	for p, r := range t.reaching {
		if r.err != nil && now.After(r.retryAt) {
			delete(t.reaching, p)
		}
	}
	t.mu.Unlock()
	for _, tun := range gone {
		tun.close()
	}
	for _, tun := range used {
		go tun.ping(3 * time.Second)
	}
}

// ---- status ----

// Status is the tailnet as the local API shows it.
type Status struct {
	Enabled bool   `json:"enabled"`
	DERPMap string `json:"derp_map,omitempty"` // where the relay list comes from
	// Serving: this host is reachable at Addr through the DERP region Region.
	Serving bool   `json:"serving"`
	Addr    string `json:"addr,omitempty"`
	Region  string `json:"region,omitempty"`
	Error   string `json:"error,omitempty"` // why it does not serve
	// System is this machine's addresses on a Tailscale network of the OS:
	// a sharing node announces its listeners there too.
	System []string `json:"system,omitempty"`
	// Tunnels are the tunnels to and from other users.
	Tunnels []TunnelStatus `json:"tunnels,omitempty"`
}

// TunnelStatus is one tunnel.
type TunnelStatus struct {
	Peer     string  `json:"peer,omitempty"` // libp2p peer ID, known for tunnels this host dialed
	Dir      string  `json:"dir"`            // "out": this host dialed; "in": a buyer's
	Path     string  `json:"path"`           // direct, derp:<region>, or tunnel before it is known
	Endpoint string  `json:"endpoint,omitempty"`
	Millis   float64 `json:"ms,omitempty"`
	Conns    int     `json:"conns,omitempty"`
	RxBytes  int64   `json:"rx_bytes,omitempty"`
	TxBytes  int64   `json:"tx_bytes,omitempty"`
}

// Status reports the tailnet's state.
func (t *Transport) Status() Status {
	t.mu.Lock()
	st := Status{Enabled: t.enabled.Load(), DERPMap: t.maps.source(), Serving: t.addr != nil, Region: t.region, Error: t.serveErr}
	if t.addr != nil {
		st.Addr = t.addr.String()
	}
	if st.Enabled {
		for _, ip := range systemIPs() {
			st.System = append(st.System, ip.String())
		}
	}
	s := t.server
	tuns := make([]*tunnel, 0, len(t.tunnels))
	refs := map[key.NodePublic]int{}
	for _, tun := range t.tunnels {
		tuns = append(tuns, tun)
		refs[tun.node] = tun.refs
	}
	t.mu.Unlock()

	for _, tun := range tuns {
		ts := TunnelStatus{Peer: tun.peerString(), Dir: "out", Path: "tunnel", Conns: refs[tun.node]}
		if p := tun.path.Load(); p != nil {
			ts.Path, ts.Endpoint, ts.Millis = p.String(), p.Endpoint, p.Millis
		}
		st.Tunnels = append(st.Tunnels, ts)
	}
	if s != nil {
		if ss := s.Status(); ss != nil {
			for _, ps := range ss.Peer {
				if ps == nil || ps.LastHandshake.IsZero() {
					continue
				}
				ts := TunnelStatus{Dir: "in", RxBytes: ps.RxBytes, TxBytes: ps.TxBytes}
				switch {
				case ps.CurAddr != "":
					ts.Path, ts.Endpoint = "direct", ps.CurAddr
				case ps.Relay != "":
					ts.Path = "derp:" + ps.Relay
				default:
					ts.Path = "derp"
				}
				st.Tunnels = append(st.Tunnels, ts)
			}
		}
	}
	sort.SliceStable(st.Tunnels, func(i, j int) bool {
		a, b := st.Tunnels[i], st.Tunnels[j]
		if a.Dir != b.Dir {
			return a.Dir > b.Dir
		}
		return a.Peer+a.Endpoint < b.Peer+b.Endpoint
	})
	return st
}
