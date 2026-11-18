package p2p

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	lproto "github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/host/autonat"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"
	"github.com/libp2p/go-libp2p/p2p/net/connmgr"
	circuitproto "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/proto"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	"github.com/libp2p/go-libp2p/p2p/protocol/holepunch"
	"github.com/libp2p/go-libp2p/p2p/protocol/identify"
	"github.com/libp2p/go-libp2p/p2p/protocol/ping"

	"sharetoken/internal/protocol"
	"sharetoken/internal/version"
)

// HostConfig selects the networking features of a host.
//
// In sharetoken every user host reaches the coordinator through a relay, and
// a user sharing a model holds a reservation on one or more relays (see
// Reserver) so that buyers can reach it behind NAT. libp2p's Noise encryption
// runs end to end across the relay, so a relay sees only ciphertext; DCUtR
// then tries to upgrade a relayed connection to a direct one by hole punching.
type HostConfig struct {
	Listen   []string // nil with DialOnly unset: libp2p's defaults
	DialOnly bool     // listen nowhere (short-lived outbound-only hosts, e.g. auditors)
	NAT      bool     // UPnP/NAT-PMP port mapping and hole punching
	// MaxConns raises the connection manager's watermarks and lifts the
	// resource manager's limits, for hubs that serve many peers (the
	// coordinator, relays). 0 keeps libp2p's defaults (trim above 192).
	MaxConns int
	// Memory caps what all peers together may reserve with the resource
	// manager, on a hub (MaxConns set). 0: a quarter of the machine's memory.
	Memory  int64
	Options []libp2p.Option // extra libp2p options
	Program string          // what this host runs, announced in libp2p's identify exchange: relay, coordinator, client
}

// NewHost starts a libp2p host for k.
func NewHost(k *Keys, cfg HostConfig) (host.Host, error) {
	opts := []libp2p.Option{libp2p.Identity(k.Libp2p)}
	if cfg.Program != "" {
		opts = append(opts, libp2p.UserAgent(version.AgentVersion(cfg.Program)))
	}
	if cfg.DialOnly {
		// NoListenAddrs also drops the relay transport, which would leave
		// this host unable to dial /p2p-circuit addresses: an auditor could
		// then never reach a node behind NAT. Re-enable it for dialing.
		opts = append(opts, libp2p.NoListenAddrs, libp2p.EnableRelay())
	} else {
		opts = append(opts, libp2p.ListenAddrStrings(cfg.Listen...))
	}
	if cfg.NAT {
		opts = append(opts, libp2p.NATPortMap(), libp2p.EnableHolePunching())
	}
	limits := ClientLimits()
	if cfg.MaxConns > 0 {
		cm, err := connmgr.NewConnManager(cfg.MaxConns*3/4, cfg.MaxConns)
		if err != nil {
			return nil, err
		}
		opts = append(opts, libp2p.ConnectionManager(cm))
		limits = hubLimits(cfg.MaxConns, cfg.Memory)
	}
	rm, err := rcmgr.NewResourceManager(rcmgr.NewFixedLimiter(limits))
	if err != nil {
		return nil, err
	}
	opts = append(opts, libp2p.ResourceManager(rm), tolerantQUIC())
	opts = append(opts, cfg.Options...)
	return libp2p.New(opts...)
}

// ClientLimits are the resource manager limits of a user host: libp2p's
// defaults scaled to the machine, except that a peer may have several pings
// in flight. libp2p allows three; a measurement of all relays, the link
// monitor's ping and a reservation's ping can overlap on the same relay, and
// a client that refuses its own ping stream would call the relay unreachable
// (it says "cannot reserve outbound stream: resource limit exceeded").
func ClientLimits() rcmgr.ConcreteLimitConfig {
	scaling := rcmgr.DefaultLimits
	libp2p.SetDefaultServiceLimits(&scaling)
	pingPeer := rcmgr.ResourceLimits{Streams: 16, StreamsInbound: 8, StreamsOutbound: 8}
	partial := rcmgr.PartialLimitConfig{
		ServicePeer:  map[string]rcmgr.ResourceLimits{ping.ServiceName: pingPeer},
		ProtocolPeer: map[lproto.ID]rcmgr.ResourceLimits{ping.ID: pingPeer},
	}
	// libp2p's AutoScale, but with the memory a cgroup allows.
	return partial.Build(scaling.Scale(int64(machineMemory())/8, numFDs()/2))
}

// HubLimits are the resource manager limits of a host that serves many peers
// (a relay, the coordinator). libp2p's defaults, scaled to this machine's
// memory and file descriptors, stay in force per peer, per connection and per
// stream: a stream may hold 16 MiB, a peer a few hundred streams and some
// tens of MiB, and so on. That is what bounds a flood of large frames (see
// protocol.RecvStream). Raised to fit maxConns are only the counts that grow
// with the number of users, which the scaled defaults put in the hundreds:
// system-wide connections, streams and descriptors, the transient scope
// (connections still shaking hands), and the circuit relay protocols and
// service, since every relayed connection is one long-lived hop stream on the
// relay and one stop stream on the coordinator, and the bundled services
// (ping, identify, autonat, hole punching). The system memory limit is a
// quarter of RAM (or of a cgroup's memory.max when that is lower) (libp2p's default is an eighth): the aggregate cap on what
// all peers can hold, legitimate buffers included.
func HubLimits(maxConns int) rcmgr.ConcreteLimitConfig { return hubLimits(maxConns, 0) }

// MachineMemory is the memory this process may use: the machine's, or a
// cgroup's limit when that is lower.
func MachineMemory() uint64 { return machineMemory() }

func hubLimits(maxConns int, memory int64) rcmgr.ConcreteLimitConfig {
	scaled := ClientLimits()
	conns := rcmgr.LimitVal(maxConns)
	streams := rcmgr.LimitVal(4 * maxConns)
	transient := rcmgr.LimitVal(max(maxConns/8, 64))
	mem := rcmgr.DefaultLimit64
	if memory > 0 {
		mem = rcmgr.LimitVal64(memory)
	} else if total := machineMemory(); total > 0 {
		mem = rcmgr.LimitVal64(total / 4)
	}
	// The bundled libp2p services and protocols: their default limits are a
	// few dozen streams plus a handful per GiB, sized for an ordinary node,
	// while here every user pings its relay every 10 s and reconnects en
	// masse after a restart. Streams scale with the users; memory stays under
	// the system cap.
	perUsers := rcmgr.ResourceLimits{
		Streams: 2 * conns, StreamsInbound: conns, StreamsOutbound: conns,
		Memory: mem,
	}
	services := map[string]rcmgr.ResourceLimits{}
	for _, name := range []string{relayv2.ServiceName, identify.ServiceName, ping.ServiceName, autonat.ServiceName, holepunch.ServiceName} {
		services[name] = perUsers
	}
	protocols := map[lproto.ID]rcmgr.ResourceLimits{}
	for _, id := range []lproto.ID{circuitproto.ProtoIDv2Hop, circuitproto.ProtoIDv2Stop, identify.ID, identify.IDPush, ping.ID, autonat.AutoNATProto, holepunch.Protocol} {
		protocols[id] = perUsers
	}
	partial := rcmgr.PartialLimitConfig{
		System: rcmgr.ResourceLimits{
			Conns: conns, ConnsInbound: conns, ConnsOutbound: conns,
			Streams: 2 * streams, StreamsInbound: streams, StreamsOutbound: streams,
			FD: conns, Memory: mem,
		},
		Transient: rcmgr.ResourceLimits{
			Conns: transient, ConnsInbound: transient, ConnsOutbound: transient,
			Streams: 2 * transient, StreamsInbound: transient, StreamsOutbound: transient,
			FD: transient,
		},
		Service:  services,
		Protocol: protocols,
	}
	return partial.Build(scaled)
}

// AddrStrings returns h's current addresses, for embedding in a signed offer.
func AddrStrings(h host.Host) []string {
	addrs := h.Addrs()
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.String())
	}
	return out
}

// FullAddrs returns h's addresses with the /p2p/<id> suffix, the form other
// peers are given to dial.
func FullAddrs(h host.Host) []string {
	maddrs, err := peer.AddrInfoToP2pAddrs(&peer.AddrInfo{ID: h.ID(), Addrs: h.Addrs()})
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(maddrs))
	for _, a := range maddrs {
		out = append(out, a.String())
	}
	return out
}

// DefaultCallTimeout bounds a control-plane round trip without a ctx deadline.
const DefaultCallTimeout = 30 * time.Second

// Call performs one request/response exchange on a fresh stream to p: send a
// reqType frame, half-close, read one respType frame into resp. Error frames
// come back as *protocol.Error.
// ErrNoStream marks a Call that could not open a stream to the peer (no
// connection and the dial failed): the request was never sent, so retrying
// it cannot duplicate it.
var ErrNoStream = errors.New("no stream to peer")

func Call(ctx context.Context, h host.Host, p peer.ID, proto, reqType string, req any, respType string, resp any) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultCallTimeout)
		defer cancel()
	}
	s, err := h.NewStream(network.WithAllowLimitedConn(ctx, "sharetoken-ctrl"), p, lproto.ID(proto))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNoStream, err)
	}
	defer s.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = s.SetDeadline(dl)
	}
	if err := protocol.SendRequest(s, reqType, req); err != nil {
		_ = s.Reset()
		return err
	}
	_ = s.CloseWrite()
	env, err := protocol.Recv(s)
	if err != nil {
		return err
	}
	return env.Decode(respType, resp)
}
