package tailnet

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/netip"
	"sync"
	"time"

	ic "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/transport"
	p2ptls "github.com/libp2p/go-libp2p/p2p/security/tls"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
	"github.com/quic-go/quic-go"
	"github.com/tailscale/tailcat"
)

// QUIC inside the tunnel.
//
// The tunnel carries IP packets, and its TCP is the engine's userspace
// stack: on a long path it starts slowly, starts over after every pause and
// recovers from a lost packet over seconds, which a request of a megabyte
// or more feels (a relay splits the same path into two short ones, each
// under a kernel's TCP). So a connection through the tunnel is by default a
// QUIC connection over the tunnel's UDP: quic-go does the congestion
// control and loss recovery, with libp2p's TLS handshake naming both peers
// as it does for QUIC on the open network. Streams are QUIC streams; there
// is no yamux and no second handshake.
//
// tailcat hands a server one packet connection per client flow (source
// address and port) and a client one per dial, so each QUIC connection has
// a quic.Transport of its own on both sides, and nothing is shared between
// buyers.
//
// The node serves TCP on the same port as well, for a buyer set to "tcp".

// quicHandshake is how long a QUIC handshake may go without an answer. A new
// tunnel can be silent for five seconds: the first WireGuard handshake
// message is sometimes lost while the path through the relay comes up, and
// WireGuard sends the next one five seconds later.
var quicHandshake = 12 * time.Second

const (
	// ProtoQUIC and ProtoTCP are what a connection through the tunnel runs
	// on (Config.Proto).
	ProtoQUIC = "quic"
	ProtoTCP  = "tcp"

	// quicAccept bounds the handshake on a flow a client opened.
	quicAccept = 20 * time.Second
	// maxPendingFlows bounds the flows whose handshake is under way: each
	// costs a quic.Transport, and any holder of the address can open one.
	maxPendingFlows = 64
)

// quicConfig is libp2p's QUIC configuration with the tunnel's packet size:
// the tunnel's MTU is fixed, so there is nothing to discover.
func (t *Transport) quicConfig() *quic.Config {
	return &quic.Config{
		Versions:                   []quic.Version{quic.Version1},
		MaxIncomingStreams:         256,
		MaxIncomingUniStreams:      -1,
		MaxStreamReceiveWindow:     10 << 20,
		MaxConnectionReceiveWindow: 15 << 20,
		KeepAlivePeriod:            15 * time.Second,
		HandshakeIdleTimeout:       quicHandshake,
		InitialPacketSize:          tailcat.MaxUDPPayload,
		DisablePathMTUDiscovery:    true,
		AllowConnectionWindowIncrease: func(qc *quic.Conn, delta uint64) bool {
			if c, ok := t.qconns.Load(qc); ok {
				return c.(*qconn).scope.ReserveMemory(int(delta), network.ReservationPriorityMedium) == nil
			}
			return true
		},
	}
}

// packetConn is a tunnel's connected UDP flow as the unconnected socket
// quic-go asks for: every packet goes to, and comes from, the flow's peer.
type packetConn struct {
	tailcat.ConnPacketConn
	local, remote *net.UDPAddr
}

func newPacketConn(c tailcat.ConnPacketConn) *packetConn {
	return &packetConn{ConnPacketConn: c, local: udpAddr(c.LocalAddr()), remote: udpAddr(c.RemoteAddr())}
}

func udpAddr(a net.Addr) *net.UDPAddr {
	if u, ok := a.(*net.UDPAddr); ok {
		return u
	}
	if a != nil {
		if ap, err := netip.ParseAddrPort(a.String()); err == nil {
			return net.UDPAddrFromAddrPort(ap)
		}
	}
	return &net.UDPAddr{IP: net.IPv6unspecified}
}

func (c *packetConn) ReadFrom(b []byte) (int, net.Addr, error) {
	n, err := c.ConnPacketConn.Read(b)
	return n, c.remote, err
}

func (c *packetConn) WriteTo(b []byte, _ net.Addr) (int, error) { return c.ConnPacketConn.Write(b) }
func (c *packetConn) LocalAddr() net.Addr                       { return c.local }

// SetReadBuffer and SetWriteBuffer are asked for by quic-go, which warns of
// a socket it cannot size. The tunnel's sockets are the engine's own, with
// buffers of megabytes; where the flow can be sized it is.
func (c *packetConn) SetReadBuffer(n int) error {
	if s, ok := c.ConnPacketConn.(interface{ SetReadBuffer(int) error }); ok {
		return s.SetReadBuffer(n)
	}
	return nil
}

func (c *packetConn) SetWriteBuffer(n int) error {
	if s, ok := c.ConnPacketConn.(interface{ SetWriteBuffer(int) error }); ok {
		return s.SetWriteBuffer(n)
	}
	return nil
}

func (c *packetConn) RemoteAddr() net.Addr { return c.remote }

// settleTime is how long a QUIC dial waits, once the tunnel answers through
// its relay, for the tunnel to find its direct path (between two home
// networks that took 5 to 7 seconds).
var settleTime = 12 * time.Second

// settle waits for the tunnel's direct path before a QUIC connection starts
// on it.
//
// A tunnel starts through its DERP relay and moves to the direct path a
// second or so later, and QUIC must not shake hands before that. Its slow
// start ends when round trips get longer than the shortest it has seen
// (quic-go: by an eighth, at most 16 ms), and a relay that sits between the
// two ends on fast links is often the shorter way: the connection then
// takes every round trip of the direct path for a growing queue, leaves
// slow start at its first sixteen packets and grows by one packet per round
// trip from there. Measured over 200 ms: 4 MB in 8 s through a connection
// that began on the relay, 0.7 s through one that began on the direct path.
// Meanwhile the job runs over the node's other paths, as it does while any
// tunnel comes up. A tunnel that stays on its relay is used after
// settleTime. TCP through the tunnel needs none of this: it starts on the
// relay and is not slowed by the move.
func (tun *tunnel) settle(ctx context.Context) error {
	var up time.Time // when the tunnel first answered
	for !tun.closed.Load() {
		p := tun.ping(time.Second)
		switch {
		case p != nil && p.Direct:
			return nil
		case p != nil && up.IsZero():
			up = time.Now()
		}
		if !up.IsZero() && time.Since(up) >= settleTime {
			return nil
		}
		select {
		case <-ctx.Done():
			if up.IsZero() {
				return fmt.Errorf("the tunnel did not come up: %w", ctx.Err())
			}
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return errors.New("the tunnel is closed")
}

// dialQUIC opens a QUIC connection to p through tun.
func (t *Transport) dialQUIC(ctx context.Context, tun *tunnel, raddr ma.Multiaddr, p peer.ID, scope network.ConnManagementScope) (transport.CapableConn, error) {
	if err := tun.settle(ctx); err != nil {
		return nil, err
	}
	uc, err := tun.client.DialUDPPort(ctx, Port)
	if err != nil {
		return nil, err
	}
	tun.started.Store(true)
	pc := newPacketConn(uc)
	qt := &quic.Transport{Conn: pc}
	shut := func() {
		_ = qt.Close()
		_ = uc.Close()
	}
	tlsConf, keyCh := t.identity.ConfigForPeer(p)
	qc, err := qt.Dial(ctx, pc.remote, tlsConf, t.quicConf)
	if err != nil {
		shut()
		return nil, err
	}
	var pub ic.PubKey
	select {
	case pub = <-keyCh:
	default:
	}
	if pub == nil {
		_ = qc.CloseWithError(1, "")
		shut()
		return nil, errors.New("tailnet: the node's key did not come with the handshake")
	}
	local, err := manet.FromNetAddr(pc.local)
	if err != nil {
		local = raddr
	}
	c := &qconn{qc: qc, t: t, scope: scope, localPeer: t.self, remotePeer: p, remoteKey: pub,
		local: local, remote: raddr, shut: func() { shut(); tun.release() }}
	if t.gater != nil && !t.gater.InterceptSecured(network.DirOutbound, p, c) {
		_ = qc.CloseWithError(quic.ApplicationErrorCode(network.ConnGated), "connection gated")
		shut()
		return nil, errors.New("tailnet: secured connection gated")
	}
	t.qconns.Store(qc, c)
	return c, nil
}

// qconn is a QUIC connection through a tunnel as libp2p's connection.
type qconn struct {
	qc    *quic.Conn
	t     *Transport
	scope network.ConnManagementScope

	localPeer, remotePeer peer.ID
	remoteKey             ic.PubKey
	local, remote         ma.Multiaddr

	shut func() // closes the flow's socket (and, dialed, gives the tunnel back)
	once sync.Once
	use  lastUse
}

var _ transport.CapableConn = (*qconn)(nil)

func (c *qconn) Close() error { return c.closeWith(0) }

func (c *qconn) CloseWithError(code network.ConnErrorCode) error {
	return c.closeWith(quic.ApplicationErrorCode(code))
}

func (c *qconn) closeWith(code quic.ApplicationErrorCode) error {
	c.t.qconns.Delete(c.qc)
	err := c.qc.CloseWithError(code, "")
	c.once.Do(func() {
		c.scope.Done()
		c.t.quicClosed.Store(time.Now().UnixNano())
		c.shut()
	})
	return err
}

func (c *qconn) IsClosed() bool { return c.qc.Context().Err() != nil }

func (c *qconn) As(target any) bool {
	switch t := target.(type) {
	case **quic.Conn:
		*t = c.qc
		return true
	case **lastUse:
		*t = &c.use
		return true
	}
	return false
}

func (c *qconn) OpenStream(ctx context.Context) (network.MuxedStream, error) {
	c.use.touch()
	s, err := c.qc.OpenStreamSync(ctx)
	if err != nil {
		return nil, quicError(err)
	}
	return qstream{s}, nil
}

func (c *qconn) AcceptStream() (network.MuxedStream, error) {
	s, err := c.qc.AcceptStream(context.Background())
	if err != nil {
		return nil, quicError(err)
	}
	c.use.touch()
	return qstream{s}, nil
}

func (c *qconn) LocalPeer() peer.ID             { return c.localPeer }
func (c *qconn) RemotePeer() peer.ID            { return c.remotePeer }
func (c *qconn) RemotePublicKey() ic.PubKey     { return c.remoteKey }
func (c *qconn) LocalMultiaddr() ma.Multiaddr   { return c.local }
func (c *qconn) RemoteMultiaddr() ma.Multiaddr  { return c.remote }
func (c *qconn) Transport() transport.Transport { return c.t }
func (c *qconn) Scope() network.ConnScope       { return c.scope }
func (c *qconn) ConnState() network.ConnectionState {
	return network.ConnectionState{Transport: "quic-v1"}
}

// qstream is a QUIC stream with libp2p's errors and its meaning of Close
// (libp2p's own QUIC transport does the same).
type qstream struct{ *quic.Stream }

var _ network.MuxedStream = qstream{}

func quicError(err error) error {
	if err == nil {
		return nil
	}
	var se *quic.StreamError
	if errors.As(err, &se) {
		code := network.StreamCodeOutOfRange
		if se.ErrorCode <= math.MaxUint32 {
			code = network.StreamErrorCode(se.ErrorCode)
		}
		return &network.StreamError{ErrorCode: code, Remote: se.Remote, TransportError: se}
	}
	var ae *quic.ApplicationError
	if errors.As(err, &ae) {
		code := network.ConnCodeOutOfRange
		if ae.ErrorCode <= math.MaxUint32 {
			code = network.ConnErrorCode(ae.ErrorCode)
		}
		return &network.ConnError{ErrorCode: code, Remote: ae.Remote, TransportError: ae}
	}
	return err
}

func (s qstream) Read(b []byte) (int, error) {
	n, err := s.Stream.Read(b)
	return n, quicError(err)
}

func (s qstream) Write(b []byte) (int, error) {
	n, err := s.Stream.Write(b)
	return n, quicError(err)
}

func (s qstream) Reset() error {
	s.Stream.CancelRead(0)
	s.Stream.CancelWrite(0)
	return nil
}

func (s qstream) ResetWithError(code network.StreamErrorCode) error {
	s.Stream.CancelRead(quic.StreamErrorCode(code))
	s.Stream.CancelWrite(quic.StreamErrorCode(code))
	return nil
}

func (s qstream) Close() error {
	s.Stream.CancelRead(0)
	return s.Stream.Close()
}

func (s qstream) CloseRead() error {
	s.Stream.CancelRead(0)
	return nil
}

func (s qstream) CloseWrite() error { return s.Stream.Close() }

// ---- the node's side ----

// tunnelListener is a node's listener inside its tunnel: connections over
// TCP, upgraded by libp2p as on any TCP listener, and QUIC connections.
type tunnelListener struct {
	t    *Transport
	addr ma.Multiaddr
	tcp  transport.Listener
	udp  net.Listener

	ctx    context.Context
	cancel context.CancelFunc
	conns  chan transport.CapableConn
	errs   chan error
	flows  chan struct{} // one token per flow in its handshake
	once   sync.Once
}

func (t *Transport) newTunnelListener(addr ma.Multiaddr, tcp transport.Listener, udp net.Listener) *tunnelListener {
	ctx, cancel := context.WithCancel(context.Background())
	l := &tunnelListener{t: t, addr: addr, tcp: tcp, udp: udp, ctx: ctx, cancel: cancel,
		conns: make(chan transport.CapableConn, 8), errs: make(chan error, 2), flows: make(chan struct{}, maxPendingFlows)}
	go l.acceptTCP()
	go l.acceptUDP()
	return l
}

func (l *tunnelListener) Multiaddr() ma.Multiaddr { return l.addr }
func (l *tunnelListener) Addr() net.Addr          { return l.tcp.Addr() }

func (l *tunnelListener) Accept() (transport.CapableConn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case err := <-l.errs:
		return nil, err
	case <-l.ctx.Done():
		return nil, transport.ErrListenerClosed
	}
}

func (l *tunnelListener) Close() error {
	l.once.Do(func() {
		l.cancel()
		_ = l.tcp.Close()
		_ = l.udp.Close()
	})
	return nil
}

func (l *tunnelListener) deliver(c transport.CapableConn) {
	select {
	case l.conns <- c:
	case <-l.ctx.Done():
		_ = c.Close()
	}
}

func (l *tunnelListener) acceptTCP() {
	for {
		c, err := l.tcp.Accept()
		if err != nil {
			select {
			case l.errs <- err:
			case <-l.ctx.Done():
			}
			return
		}
		l.deliver(c)
	}
}

func (l *tunnelListener) acceptUDP() {
	for {
		flow, err := l.udp.Accept()
		if err != nil {
			return // the listener closed; TCP's error ends Accept
		}
		uc, ok := flow.(tailcat.ConnPacketConn)
		if !ok {
			_ = flow.Close()
			continue
		}
		select {
		case l.flows <- struct{}{}:
			go l.serveFlow(uc)
		default:
			_ = flow.Close()
		}
	}
}

// serveFlow takes the one QUIC connection a client's flow carries.
func (l *tunnelListener) serveFlow(uc tailcat.ConnPacketConn) {
	t := l.t
	pc := newPacketConn(uc)
	qt := &quic.Transport{Conn: pc}
	var ql *quic.Listener
	shut := func() {
		if ql != nil {
			_ = ql.Close()
		}
		_ = qt.Close()
		_ = uc.Close()
	}
	c, err := func() (*qconn, error) {
		defer func() { <-l.flows }()
		tlsConf, _ := t.identity.ConfigForPeer("")
		var err error
		if ql, err = qt.Listen(tlsConf, t.quicConf); err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(l.ctx, quicAccept)
		defer cancel()
		qc, err := ql.Accept(ctx)
		if err != nil {
			return nil, err
		}
		c, err := l.wrap(qc, pc, shut)
		if err != nil {
			_ = qc.CloseWithError(1, "")
			return nil, err
		}
		return c, nil
	}()
	if err != nil {
		shut()
		if l.ctx.Err() == nil {
			t.log.Debug("no QUIC connection on a tunnel flow", "from", pc.remote, "err", err)
		}
		return
	}
	t.qconns.Store(c.qc, c)
	l.deliver(c)
}

func (l *tunnelListener) wrap(qc *quic.Conn, pc *packetConn, shut func()) (*qconn, error) {
	t := l.t
	pub, err := p2ptls.PubKeyFromCertChain(qc.ConnectionState().TLS.PeerCertificates)
	if err != nil {
		return nil, err
	}
	p, err := peer.IDFromPublicKey(pub)
	if err != nil {
		return nil, err
	}
	// The caller's address inside the tunnel (fd7a:…, derived from its
	// tunnel key): private, and never one this host would dial.
	remote, err := manet.FromNetAddr(pc.remote)
	if err != nil {
		return nil, err
	}
	// The resource manager counts connections per address block, and every
	// tunnel address is in the one block Tailscale uses: counted there, a
	// node could not have more buyers than one block may have connections.
	// The listener's own address stands in; the other limits hold.
	scope, err := t.rcmgr.OpenConnection(network.DirInbound, false, l.addr)
	if err != nil {
		return nil, err
	}
	if err := scope.SetPeer(p); err != nil {
		scope.Done()
		return nil, err
	}
	c := &qconn{qc: qc, t: t, scope: scope, localPeer: t.self, remotePeer: p, remoteKey: pub,
		local: l.addr, remote: remote, shut: shut}
	if t.gater != nil && !(t.gater.InterceptAccept(c) && t.gater.InterceptSecured(network.DirInbound, p, c)) {
		scope.Done()
		return nil, fmt.Errorf("tailnet: connection from %s gated", p)
	}
	return c, nil
}
