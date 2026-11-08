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
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"
	"github.com/libp2p/go-libp2p/p2p/net/connmgr"

	"sharetoken/protocol"
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
	Options  []libp2p.Option // extra libp2p options
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
		opts = append(opts, libp2p.NATPortMap(), libp2p.EnableHolePunching())
	}
	if cfg.MaxConns > 0 {
		cm, err := connmgr.NewConnManager(cfg.MaxConns*3/4, cfg.MaxConns)
		if err != nil {
			return nil, err
		}
		rm, err := rcmgr.NewResourceManager(rcmgr.NewFixedLimiter(rcmgr.InfiniteLimits))
		if err != nil {
			return nil, err
		}
		opts = append(opts, libp2p.ConnectionManager(cm), libp2p.ResourceManager(rm))
	}
	opts = append(opts, cfg.Options...)
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
