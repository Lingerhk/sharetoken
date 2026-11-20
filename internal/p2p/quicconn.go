package p2p

import (
	"context"
	"errors"
	"net"
	"syscall"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/p2p/transport/quicreuse"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/quic-go/quic-go"
	"go.uber.org/fx"
	"golang.org/x/net/ipv4"
)

// A host's QUIC connections all share one UDP socket, and quic-go closes the
// socket's whole transport on any read error it does not take for a passing
// one (transport.go: listen). An ICMP error left on the socket by a packet
// sent to an address that cannot be reached is such an error on the next
// read: on macOS, a host that tried a peer's address on another interface
// (a VPN tunnel, ZeroTier, a bridge) read "recvmsg: can't assign requested
// address", and every connection on the socket broke at once ("quic:
// transport closed"), the coordinator's and the relays' with them, failing
// the jobs then under way. Such an error concerns one destination, not the
// socket: the read is tried again.

// unreachable reports whether err is the socket telling of one destination
// that cannot be reached.
func unreachable(err error) bool {
	return errors.Is(err, syscall.EADDRNOTAVAIL) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.EHOSTDOWN) || errors.Is(err, syscall.ECONNRESET)
}

// tolerantUDPConn is a UDP socket whose reads skip the errors unreachable
// names. It keeps the methods of *net.UDPConn that quic-go uses for ECN and
// segmentation offload, and gives quic-go the batch reader it reads with
// (sys_conn_oob.go: without one it makes its own from the file descriptor,
// which no method here would see).
type tolerantUDPConn struct {
	*net.UDPConn
	batch *ipv4.PacketConn
}

func (c tolerantUDPConn) ReadBatch(ms []ipv4.Message, flags int) (int, error) {
	for {
		n, err := c.batch.ReadBatch(ms, flags)
		if err == nil || !unreachable(err) {
			return n, err
		}
	}
}

func (c tolerantUDPConn) ReadMsgUDP(b, oob []byte) (n, oobn, flags int, addr *net.UDPAddr, err error) {
	for {
		n, oobn, flags, addr, err = c.UDPConn.ReadMsgUDP(b, oob)
		if err == nil || !unreachable(err) {
			return
		}
	}
}

func (c tolerantUDPConn) ReadFrom(b []byte) (int, net.Addr, error) {
	for {
		n, addr, err := c.UDPConn.ReadFrom(b)
		if err == nil || !unreachable(err) {
			return n, addr, err
		}
	}
}

func listenTolerantUDP(network string, laddr *net.UDPAddr) (net.PacketConn, error) {
	c, err := net.ListenUDP(network, laddr)
	if err != nil {
		return nil, err
	}
	return tolerantUDPConn{UDPConn: c, batch: ipv4.NewPacketConn(c)}, nil
}

// tolerantQUIC is libp2p's own QUIC connection manager (config.go, as
// libp2p.New makes it when given none), with tolerant sockets.
func tolerantQUIC() libp2p.Option {
	return libp2p.QUICReuse(func(key quic.StatelessResetKey, tokenGenerator quic.TokenGeneratorKey, rcmgr network.ResourceManager, lifecycle fx.Lifecycle) (*quicreuse.ConnManager, error) {
		cm, err := quicreuse.NewConnManager(key, tokenGenerator,
			quicreuse.OverrideListenUDP(listenTolerantUDP),
			quicreuse.ConnContext(func(ctx context.Context, clientInfo *quic.ClientInfo) (context.Context, error) {
				addr, err := quicreuse.ToQuicMultiaddr(clientInfo.RemoteAddr, quic.Version1)
				if err != nil {
					addr = nil
				}
				scope, err := rcmgr.OpenConnection(network.DirInbound, false, addr)
				if err != nil {
					return ctx, err
				}
				ctx = network.WithConnManagementScope(ctx, scope)
				context.AfterFunc(ctx, func() { scope.Done() })
				return ctx, nil
			}),
			quicreuse.VerifySourceAddress(func(addr net.Addr) bool { return rcmgr.VerifySourceAddress(addr) }),
			quicreuse.EnableMetrics(prometheus.DefaultRegisterer),
		)
		if err != nil {
			return nil, err
		}
		lifecycle.Append(fx.StopHook(cm.Close))
		return cm, nil
	})
}
