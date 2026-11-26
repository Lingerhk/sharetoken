package tailnet

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/connmgr"
	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	p2ptls "github.com/libp2p/go-libp2p/p2p/security/tls"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/quic-go/quic-go"
	"github.com/tailscale/tailcat"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

// Port is the port, inside the tunnel, a node's libp2p listener is on: UDP
// for QUIC and TCP (quic.go). It is on no machine's network: only the
// tunnel's own address has it.
const Port = 4001

// Config tunes a Transport.
type Config struct {
	// Enabled turns the tailnet on from the start; SetEnabled changes it.
	// Off, the transport is installed but dials nothing and serves nothing.
	Enabled bool
	// DERPMap says which DERP relays the tunnel may start through: empty
	// for tailcat's public ones, the URL of a DERP map, or the path of a
	// file holding one. Both ends must know the region a node waits in.
	DERPMap string
	// Dir, when set, is where the fetched DERP map is kept between runs.
	Dir string
	// IdleClose closes an outgoing tunnel connection that has carried no
	// stream for this long (0: 10 minutes). The next job dials it again.
	IdleClose time.Duration
	// Proto is what a connection this host dials through a tunnel runs on:
	// ProtoQUIC (the default, and what an empty value means) or ProtoTCP. A
	// node serves both.
	Proto string
	// MaxTunnels bounds the tunnels kept to nodes at once (0: 16). Each is
	// a WireGuard engine with its own UDP socket and relay connection.
	MaxTunnels int
	Log        *slog.Logger

	derpMap *tailcfg.DERPMap // a fixed map (tests)
	tcpOnly bool             // serve no QUIC, as a node without it (tests)
}

// Transport is the libp2p transport of tailnet addresses. One serves both
// roles: Serve makes this host reachable at an address of its own, and
// dialing a /tailcat address brings up a tunnel to that node.
type Transport struct {
	cfg     Config
	log     *slog.Logger
	key     key.NodePrivate // this process's identity towards the nodes it dials
	enabled atomic.Bool

	upgrader transport.Upgrader
	rcmgr    network.ResourceManager
	gater    connmgr.ConnectionGater
	self     peer.ID
	identity *p2ptls.Identity // names this host in a QUIC handshake
	quicConf *quic.Config
	proto    atomic.Value // string: Config.Proto

	qconns     sync.Map     // *quic.Conn -> *qconn
	quicClosed atomic.Int64 // when a QUIC connection last closed (UnixNano)

	mu       sync.Mutex
	maps     *derpMaps
	closed   bool
	server   *tailcat.Server
	addr     ma.Multiaddr // the server's
	region   string       // the DERP region the server waits in
	serveErr string
	serving  bool // a Serve is under way or done
	tunnels  map[key.NodePublic]*tunnel
	reaching map[peer.ID]*reach
}

// New returns a transport and the libp2p option that installs it, next to
// the transports the host names itself (p2p.NewHost: TCP and QUIC; naming a
// transport drops libp2p's defaults, WebSocket, WebTransport and WebRTC among
// them, which nothing here uses). The option keeps the tailnet address out of
// the addresses the host tells every peer it meets: only an offer carries
// it, to the buyers the coordinator sends.
func New(cfg Config) (*Transport, libp2p.Option, error) {
	maps, err := newDERPMaps(cfg.DERPMap, cfg.Dir)
	if err != nil {
		return nil, nil, err
	}
	if cfg.derpMap != nil {
		maps.fixed = cfg.derpMap
	}
	if cfg.IdleClose <= 0 {
		cfg.IdleClose = 10 * time.Minute
	}
	if cfg.MaxTunnels <= 0 {
		cfg.MaxTunnels = 16
	}
	lg := cfg.Log
	if lg == nil {
		lg = slog.Default()
	}
	t := &Transport{cfg: cfg, log: lg.With("component", "tailnet"), maps: maps, key: key.NewNode(),
		tunnels: map[key.NodePublic]*tunnel{}, reaching: map[peer.ID]*reach{}}
	t.enabled.Store(cfg.Enabled)
	if err := t.SetProto(cfg.Proto); err != nil {
		return nil, nil, err
	}
	t.quicConf = t.quicConfig()
	opt := libp2p.ChainOptions(
		libp2p.Transport(func(k ic.PrivKey, u transport.Upgrader, rcmgr network.ResourceManager, gater connmgr.ConnectionGater) (*Transport, error) {
			id, err := p2ptls.NewIdentity(k)
			if err != nil {
				return nil, err
			}
			self, err := peer.IDFromPrivateKey(k)
			if err != nil {
				return nil, err
			}
			t.upgrader, t.rcmgr, t.gater, t.identity, t.self = u, rcmgr, gater, id, self
			return t, nil
		}),
		libp2p.DialRanker(dialRanker),
		libp2p.AddrsFactory(func(addrs []ma.Multiaddr) []ma.Multiaddr {
			out := addrs[:0:0]
			for _, a := range addrs {
				if !Is(a) {
					out = append(out, a)
				}
			}
			return out
		}),
	)
	return t, opt, nil
}

// Enabled reports whether the tailnet is on.
func (t *Transport) Enabled() bool { return t.enabled.Load() }

// SetEnabled turns the tailnet on or off. Turning it off does not end what
// runs over it; Reset does.
func (t *Transport) SetEnabled(on bool) { t.enabled.Store(on) }

// SetProto changes what connections dialed from now on run on (see
// Config.Proto); those that stand stay as they are.
func (t *Transport) SetProto(proto string) error {
	switch proto {
	case "":
		proto = ProtoQUIC
	case ProtoQUIC, ProtoTCP:
	default:
		return fmt.Errorf("tailnet: unknown transport %q (quic, tcp)", proto)
	}
	t.proto.Store(proto)
	return nil
}

// Proto is what connections this host dials through a tunnel run on.
func (t *Transport) Proto() string { return t.proto.Load().(string) }

// SetDERPMap changes where the relay list comes from (see Config.DERPMap).
// Tunnels and the server keep the relays they started with until Reset.
func (t *Transport) SetDERPMap(src string) error {
	maps, err := newDERPMaps(src, t.cfg.Dir)
	if err != nil {
		return err
	}
	t.mu.Lock()
	t.maps, t.cfg.DERPMap = maps, src
	t.mu.Unlock()
	return nil
}

func (t *Transport) derpMaps() *derpMaps {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.maps
}

// Reset takes this host off the tailnet and closes every tunnel and the
// connections on them; streams under way break and their jobs resume over
// the node's other paths.
func (t *Transport) Reset(h host.Host) {
	t.StopServing(h)
	for _, c := range h.Network().Conns() {
		if Is(c.RemoteMultiaddr()) || Is(c.LocalMultiaddr()) {
			_ = c.Close()
		}
	}
	t.mu.Lock()
	tuns := make([]*tunnel, 0, len(t.tunnels))
	for _, tun := range t.tunnels {
		tuns = append(tuns, tun)
	}
	t.tunnels = map[key.NodePublic]*tunnel{}
	t.reaching = map[peer.ID]*reach{}
	t.serveErr = ""
	t.mu.Unlock()
	closeTunnels(tuns)
}

// logf is tailcat's logger: its engine says a great deal, all of it at debug.
func (t *Transport) logf(who string) func(format string, args ...any) {
	return func(format string, args ...any) {
		if !t.log.Enabled(context.Background(), slog.LevelDebug) {
			return
		}
		t.log.Debug(fmt.Sprintf(format, args...), "engine", who)
	}
}

// ---- libp2p transport ----

var _ transport.Transport = (*Transport)(nil)

func (t *Transport) Protocols() []int { return []int{P_TAILCAT} }
func (t *Transport) Proxy() bool      { return false }
func (t *Transport) String() string   { return "tailnet" }

// CanDial is false while the tailnet is off: libp2p then leaves tailnet
// addresses out of a dial without counting them as failures.
func (t *Transport) CanDial(addr ma.Multiaddr) bool { return t.enabled.Load() && Is(addr) }

// dialRanker is libp2p's order of dialing a peer's addresses, with the
// tunnel first. libp2p dials a kind of address it does not know last, a
// second after the others; the tunnel takes the longest to stand, and by
// then the dials to the peer's unreachable addresses hold every connection
// the peer may have (see Dial).
func dialRanker(addrs []ma.Multiaddr) []network.AddrDelay {
	var out []network.AddrDelay
	rest := make([]ma.Multiaddr, 0, len(addrs))
	for _, a := range addrs {
		if Is(a) {
			out = append(out, network.AddrDelay{Addr: a})
		} else {
			rest = append(rest, a)
		}
	}
	return append(out, swarm.DefaultDialRanker(rest)...)
}

// Dial brings up (or reuses) the tunnel to the node behind raddr and opens
// a libp2p connection through it.
func (t *Transport) Dial(ctx context.Context, raddr ma.Multiaddr, p peer.ID) (transport.CapableConn, error) {
	scope, err := t.rcmgr.OpenConnection(network.DirOutbound, false, raddr)
	if err != nil {
		return nil, err
	}
	// The peer now, as TCP and QUIC do, not when the tunnel stands: the
	// dials of the node's other addresses each hold one of the few
	// connections a peer may have while they wait for their timeouts, and
	// the tunnel, seconds later, would find none left.
	if err := scope.SetPeer(p); err != nil {
		scope.Done()
		return nil, fmt.Errorf("tailnet: %w", err)
	}
	c, err := t.dial(ctx, raddr, p, scope)
	if err != nil {
		scope.Done()
		// libp2p reports the first few failures of a dial, and a node has
		// more addresses than that: the tunnel's, the last to fail, is cut.
		t.log.Info("tunnel dial failed", "peer", p, "err", err)
		return nil, err
	}
	return c, nil
}

func (t *Transport) dial(ctx context.Context, raddr ma.Multiaddr, p peer.ID, scope network.ConnManagementScope) (transport.CapableConn, error) {
	tun, err := t.tunnel(ctx, raddr)
	if err != nil {
		return nil, err
	}
	t0 := time.Now()
	if t.Proto() == ProtoQUIC {
		cc, err := t.dialQUIC(ctx, tun, raddr, p, scope)
		if err != nil {
			tun.release()
			return nil, fmt.Errorf("tailnet: %w", err)
		}
		t.log.Debug("tunnel connection up", "peer", p, "proto", ProtoQUIC, "took", time.Since(t0).Round(time.Millisecond))
		tun.peer.Store(&p)
		tun.proto.Store(ProtoQUIC)
		tun.probe()
		return cc, nil
	}
	nc, err := tun.client.DialTCPPort(ctx, Port)
	if err != nil {
		tun.release()
		return nil, fmt.Errorf("tailnet: %w", err)
	}
	tun.started.Store(true)
	local, err := manet.FromNetAddr(nc.LocalAddr())
	if err != nil {
		local = raddr // never shown as ours: only its form matters
	}
	mc := &conn{Conn: nc, local: local, remote: raddr, done: tun.release}
	cc, err := t.upgrader.Upgrade(ctx, t, mc, network.DirOutbound, p, scope)
	if err != nil {
		_ = mc.Close()
		return nil, err
	}
	t.log.Debug("tunnel connection up", "peer", p, "proto", ProtoTCP, "took", time.Since(t0).Round(time.Millisecond))
	tun.peer.Store(&p)
	tun.proto.Store(ProtoTCP)
	tun.probe()
	return &usedConn{CapableConn: cc}, nil
}

// lastUse is when a tunnel connection last got a new stream. The sweep
// looks at a connection twice a minute and most jobs take seconds: by the
// streams it happened to see, a connection that carried a job every minute
// was idle at every look, and was closed after Config.IdleClose all the
// same, sending the next job over the relay.
type lastUse struct{ at atomic.Int64 }

func (u *lastUse) touch() { u.at.Store(time.Now().UnixNano()) }

// usedConn is the upgraded TCP connection through a tunnel, noting when it
// is used.
type usedConn struct {
	transport.CapableConn
	use lastUse
}

func (c *usedConn) OpenStream(ctx context.Context) (network.MuxedStream, error) {
	c.use.touch()
	return c.CapableConn.OpenStream(ctx)
}

func (c *usedConn) AcceptStream() (network.MuxedStream, error) {
	s, err := c.CapableConn.AcceptStream()
	if err == nil {
		c.use.touch()
	}
	return s, err
}

func (c *usedConn) As(target any) bool {
	if u, ok := target.(**lastUse); ok {
		*u = &c.use
		return true
	}
	return c.CapableConn.As(target)
}

// Listen serves libp2p on this host's tailcat server; Serve calls it
// through the host once the server is up.
func (t *Transport) Listen(laddr ma.Multiaddr) (transport.Listener, error) {
	t.mu.Lock()
	s, own := t.server, t.addr
	t.mu.Unlock()
	if s == nil || own == nil || !own.Equal(laddr) {
		return nil, errors.New("tailnet: only this host's own address can be listened on, once it serves")
	}
	ln, err := s.Listen(context.Background(), "tcp", fmt.Sprintf(":%d", Port))
	if err != nil {
		return nil, err
	}
	tcp := t.upgrader.UpgradeGatedMaListener(t, t.upgrader.GateMaListener(&listener{Listener: ln, addr: laddr}))
	if t.cfg.tcpOnly {
		return tcp, nil
	}
	udp, err := s.Listen(context.Background(), "udp", fmt.Sprintf(":%d", Port))
	if err != nil {
		_ = tcp.Close()
		return nil, err
	}
	return t.newTunnelListener(laddr, tcp, udp), nil
}

// conn is a tunnel's TCP connection with the multiaddrs libp2p asks for.
type conn struct {
	net.Conn
	local, remote ma.Multiaddr
	done          func()
	once          sync.Once
}

func (c *conn) LocalMultiaddr() ma.Multiaddr  { return c.local }
func (c *conn) RemoteMultiaddr() ma.Multiaddr { return c.remote }

func (c *conn) Close() error {
	err := c.Conn.Close()
	if c.done != nil {
		c.once.Do(c.done)
	}
	return err
}

// listener hands libp2p the connections buyers open through the tunnel.
type listener struct {
	net.Listener
	addr ma.Multiaddr
}

func (l *listener) Multiaddr() ma.Multiaddr { return l.addr }

func (l *listener) Accept() (manet.Conn, error) {
	for {
		nc, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		// The caller's address inside the tunnel (fd7a:…, derived from its
		// tunnel key): private, and never one this host would dial.
		remote, err := manet.FromNetAddr(nc.RemoteAddr())
		if err != nil {
			_ = nc.Close()
			continue
		}
		return &conn{Conn: nc, local: l.addr, remote: remote}, nil
	}
}

// ---- serving ----

// Serve makes h reachable over the tailnet: it starts this host's tailcat
// server on the nearest DERP region and has the host listen on it. It
// returns the address to advertise. Starting talks to the DERP relays and
// can take seconds or fail; the host works without it meanwhile.
func (t *Transport) Serve(ctx context.Context, h host.Host) (ma.Multiaddr, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, errors.New("tailnet: closed")
	}
	if t.addr != nil {
		a := t.addr
		t.mu.Unlock()
		return a, nil
	}
	if t.serving {
		t.mu.Unlock()
		return nil, errors.New("tailnet: already starting")
	}
	t.serving = true
	t.mu.Unlock()

	addr, err := t.serve(ctx, h)
	t.mu.Lock()
	defer t.mu.Unlock()
	if err != nil {
		t.serving, t.serveErr = false, err.Error()
		return nil, err
	}
	t.serveErr = ""
	return addr, nil
}

func (t *Transport) serve(ctx context.Context, h host.Host) (ma.Multiaddr, error) {
	if !t.enabled.Load() {
		return nil, errors.New("tailnet: off")
	}
	dm, err := t.derpMaps().get(ctx)
	if err != nil {
		return nil, fmt.Errorf("tailnet: DERP map: %w", err)
	}
	id, err := tailcat.PickBestRegion(ctx, dm)
	if err != nil {
		return nil, fmt.Errorf("tailnet: %w", err)
	}
	region := dm.Regions[id]
	if region == nil {
		return nil, errors.New("tailnet: no DERP region answered")
	}
	s := &tailcat.Server{Logf: t.logf("server"), Region: region}
	if err := s.StartContext(ctx); err != nil {
		return nil, fmt.Errorf("tailnet: %w", err)
	}
	addr, err := Multiaddr(s.TailcatAddr())
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		_ = s.Close()
		return nil, errors.New("tailnet: closed")
	}
	t.server, t.addr, t.region = s, addr, region.RegionCode
	t.mu.Unlock()
	if err := h.Network().Listen(addr); err != nil {
		t.mu.Lock()
		t.server, t.addr, t.region = nil, nil, ""
		t.mu.Unlock()
		_ = s.Close()
		return nil, fmt.Errorf("tailnet: %w", err)
	}
	t.log.Info("reachable over the tailnet", "region", region.RegionCode)
	return addr, nil
}

// Addr is the address this host serves at, nil while it does not.
func (t *Transport) Addr() ma.Multiaddr {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.addr
}

// StopServing takes this host off the tailnet; its tunnels to other nodes
// stay.
func (t *Transport) StopServing(h host.Host) {
	t.mu.Lock()
	s, addr := t.server, t.addr
	t.server, t.addr, t.region, t.serving = nil, nil, "", false
	t.mu.Unlock()
	if s == nil {
		return
	}
	if lc, ok := h.Network().(interface{ ListenClose(...ma.Multiaddr) error }); ok && addr != nil {
		_ = lc.ListenClose(addr)
	}
	for _, c := range h.Network().Conns() {
		if Is(c.LocalMultiaddr()) {
			_ = c.Close()
		}
	}
	t.closeServer(s)
}

// Close stops the server and every tunnel.
func (t *Transport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	s := t.server
	t.server, t.addr = nil, nil
	tuns := make([]*tunnel, 0, len(t.tunnels))
	for _, tun := range t.tunnels {
		tuns = append(tuns, tun)
	}
	t.tunnels = map[key.NodePublic]*tunnel{}
	t.mu.Unlock()
	closeTunnels(tuns)
	if s != nil {
		t.closeServer(s)
	}
	return nil
}

// ---- tunnels to nodes ----

// tunnel is the WireGuard tunnel to one node. tailcat keeps a client per
// server; connections to the node share it.
type tunnel struct {
	t      *Transport
	node   key.NodePublic
	client *tailcat.Client
	region string // the DERP region it starts through, as this client's map names it
	peer   atomic.Pointer[peer.ID]

	refs     int       // connections open or being opened; under t.mu
	idleFrom time.Time // when refs last fell to zero; under t.mu

	proto atomic.Value // string: what its latest connection runs on

	path    atomic.Pointer[Path]
	probing atomic.Bool
	started atomic.Bool // the client's network stack exists
	closed  atomic.Bool
}

// Path is how a tunnel's packets travel right now.
type Path struct {
	Direct   bool    `json:"direct"`             // straight between the two machines
	Endpoint string  `json:"endpoint,omitempty"` // the node's address on a direct path
	Relay    string  `json:"relay,omitempty"`    // the DERP region carrying it otherwise
	Millis   float64 `json:"ms,omitempty"`       // the latest round trip
}

func (p Path) String() string {
	if p.Direct {
		return "direct"
	}
	if p.Relay != "" {
		return "derp:" + p.Relay
	}
	return "derp"
}

// tunnel returns the tunnel to the node behind raddr, making it if needed,
// with one more reference.
func (t *Transport) tunnel(ctx context.Context, raddr ma.Multiaddr) (*tunnel, error) {
	a, ok := FromMultiaddr(raddr)
	if !ok {
		return nil, fmt.Errorf("tailnet: %s is not a tailnet address", raddr)
	}
	ci, err := tailcat.ParseAddr(a)
	if err != nil {
		return nil, fmt.Errorf("tailnet: %w", err)
	}
	if !t.enabled.Load() {
		return nil, errors.New("tailnet: off")
	}
	if err := t.derpMaps().trusted(ctx, &ci); err != nil {
		return nil, fmt.Errorf("tailnet: %w", err)
	}
	node := ci.ServerPublic.NodePublic

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, errors.New("tailnet: closed")
	}
	if tun := t.tunnels[node]; tun != nil {
		tun.refs++
		return tun, nil
	}
	if len(t.tunnels) >= t.cfg.MaxTunnels && !t.evictLocked() {
		return nil, fmt.Errorf("tailnet: %d tunnels are in use", len(t.tunnels))
	}
	tun := &tunnel{t: t, node: node, refs: 1, region: ci.Region[0].RegionCode,
		client: &tailcat.Client{Server: ci.Addr(), Key: t.key, Logf: t.logf("client")}}
	t.tunnels[node] = tun
	return tun, nil
}

// evictLocked closes the tunnel that has been without a connection longest.
func (t *Transport) evictLocked() bool {
	var victim *tunnel
	for _, tun := range t.tunnels {
		if tun.refs == 0 && (victim == nil || tun.idleFrom.Before(victim.idleFrom)) {
			victim = tun
		}
	}
	if victim == nil {
		return false
	}
	delete(t.tunnels, victim.node)
	go victim.close()
	return true
}

func (tun *tunnel) release() {
	tun.t.mu.Lock()
	defer tun.t.mu.Unlock()
	if tun.refs--; tun.refs == 0 {
		tun.idleFrom = time.Now()
	}
}

func (tun *tunnel) close() {
	if !tun.closed.CompareAndSwap(false, true) {
		return
	}
	if tun.started.Load() {
		tun.t.quicLinger()
		ctx, cancel := context.WithTimeout(context.Background(), drainTime)
		_ = tun.client.DrainTCP(ctx)
		cancel()
	}
	_ = tun.client.Close()
}

// quicLinger gives a QUIC connection closed a moment ago the time to get its
// closing packet through the engine that is about to stop; without it the
// other side learns of the end from its idle timeout.
func (t *Transport) quicLinger() {
	const linger = 150 * time.Millisecond
	if d := time.Since(time.Unix(0, t.quicClosed.Load())); d < linger {
		time.Sleep(linger - d)
	}
}

// drainTime is how long closing a tunnel or the server waits for the TCP
// connections inside it to finish closing. The whole TCP stack is in this
// process: an engine stopped right after a connection's Close never sends
// the FIN, and the other side keeps a connection that looks alive until its
// keepalive gives up, with the next job stalling on it.
const drainTime = time.Second

func (t *Transport) closeServer(s *tailcat.Server) {
	t.quicLinger()
	ctx, cancel := context.WithTimeout(context.Background(), drainTime)
	_ = s.DrainTCP(ctx)
	cancel()
	_ = s.Close()
}

func closeTunnels(tuns []*tunnel) {
	var wg sync.WaitGroup
	for _, tun := range tuns {
		wg.Go(tun.close)
	}
	wg.Wait()
}

// tunnelLinger is how long a tunnel without connections is kept: a
// connection that broke is usually dialed again at once, and the tunnel
// then has its direct path already.
const tunnelLinger = 2 * time.Minute

// probe learns the tunnel's path, and keeps asking for a while: the first
// packets go through the relay, and the answer changes once the direct path
// is found (asking also nudges the search along).
func (tun *tunnel) probe() {
	if !tun.probing.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer tun.probing.Store(false)
		for i := 0; i < 15 && !tun.closed.Load(); i++ {
			if p := tun.ping(3 * time.Second); p != nil && p.Direct {
				return
			}
			time.Sleep(time.Second)
		}
	}()
}

func (tun *tunnel) ping(timeout time.Duration) *Path {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	res, err := tun.client.DiscoPing(ctx)
	if err != nil || res == nil {
		return nil
	}
	p := &Path{Direct: res.Endpoint != "", Endpoint: res.Endpoint, Relay: res.DERPRegionCode, Millis: res.LatencySeconds * 1000}
	if !p.Direct && tun.region != "" {
		p.Relay = tun.region // the engine numbers the one region of the address
	}
	if old := tun.path.Swap(p); old == nil || old.Direct != p.Direct {
		tun.t.log.Info("tunnel path", "peer", tun.peerString(), "path", p.String(), "ms", fmt.Sprintf("%.1f", p.Millis))
	}
	return p
}

func (tun *tunnel) peerString() string {
	if p := tun.peer.Load(); p != nil {
		return p.String()
	}
	return ""
}

// PathOf reports how a connection's packets travel, if it runs over a
// tunnel this host dialed: "direct" or "derp:<region>"; "system" for a
// connection to the peer's address on a Tailscale network of the OS (how
// that one travels is the system's business). ok is false for any other
// connection.
func (t *Transport) PathOf(c network.Conn) (path string, ok bool) {
	if c == nil {
		return "", false
	}
	a, is := FromMultiaddr(c.RemoteMultiaddr())
	if !is {
		if len(systemPeerAddrs([]ma.Multiaddr{c.RemoteMultiaddr()})) == 1 {
			return "system", true
		}
		return "", false
	}
	ci, err := tailcat.ParseAddr(a)
	if err != nil {
		return "tunnel", true
	}
	t.mu.Lock()
	tun := t.tunnels[ci.ServerPublic.NodePublic]
	t.mu.Unlock()
	if tun == nil {
		return "tunnel", true
	}
	if p := tun.path.Load(); p != nil {
		return p.String(), true
	}
	return "tunnel", true
}
