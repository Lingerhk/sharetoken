package p2p

import (
	"context"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	lproto "github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"

	"sharetoken/protocol"
)

// HostConfig selects the networking features of a host.
//
// A seller node at home is typically behind NAT. It only makes outbound
// connections; through AutoRelay it reserves a slot on a public relay (the
// coordinator) and advertises a /p2p-circuit address in its offer. A buyer
// dials that address, and DCUtR then tries to upgrade the relayed connection
// to a direct one by hole punching. libp2p's Noise encryption runs end to end
// between buyer and node across the relay, so the relay sees only ciphertext.
type HostConfig struct {
	Listen       []string        // nil with DialOnly unset: libp2p's defaults
	DialOnly     bool            // listen nowhere (short-lived outbound-only hosts, e.g. auditors)
	NAT          bool            // UPnP/NAT-PMP port mapping, AutoNAT service, hole punching
	RelayService bool            // serve circuit-relay v2 without data limits (coordinator)
	ForcePublic  bool            // assume a public IP (lets the relay service start immediately)
	StaticRelays []peer.AddrInfo // reserve relay slots on these peers (nodes behind NAT)
}

// NewHost starts a libp2p host for k.
func NewHost(k *Keys, cfg HostConfig) (host.Host, error) {
	opts := []libp2p.Option{libp2p.Identity(k.Libp2p)}
	if cfg.DialOnly {
		// NoListenAddrs also drops the relay transport, which would leave
		// this host unable to dial /p2p-circuit addresses: an auditor could
		// then never reach a node behind NAT. Re-enable it for dialing.
		opts = append(opts, libp2p.NoListenAddrs, libp2p.EnableRelay())
	} else {
		opts = append(opts, libp2p.ListenAddrStrings(cfg.Listen...))
	}
	if cfg.NAT {
		opts = append(opts, libp2p.NATPortMap(), libp2p.EnableNATService(), libp2p.EnableHolePunching())
	}
	if cfg.ForcePublic {
		opts = append(opts, libp2p.ForceReachabilityPublic())
	}
	if cfg.RelayService {
		// Default relay v2 limits (2 min, 128 KiB) only suit hole-punch
		// signalling; streaming completions through the relay needs them off.
		opts = append(opts, libp2p.EnableRelayService(relay.WithInfiniteLimits()))
	}
	if len(cfg.StaticRelays) > 0 {
		opts = append(opts, libp2p.EnableAutoRelayWithStaticRelays(cfg.StaticRelays))
	}
	return libp2p.New(opts...)
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
func Call(ctx context.Context, h host.Host, p peer.ID, proto, reqType string, req any, respType string, resp any) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultCallTimeout)
		defer cancel()
	}
	s, err := h.NewStream(network.WithAllowLimitedConn(ctx, "sharetoken-ctrl"), p, lproto.ID(proto))
	if err != nil {
		return err
	}
	defer s.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = s.SetDeadline(dl)
	}
	if err := protocol.Send(s, reqType, req); err != nil {
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
