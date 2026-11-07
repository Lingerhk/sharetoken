// Command relay runs a sharetoken relay: a public host that only forwards
// encrypted libp2p traffic between users, and between users and the
// coordinator. It keeps no accounts and sees no plaintext.
//
//	relay -listen /ip4/0.0.0.0/tcp/8091 -announce /ip4/203.0.113.7/tcp/8091 \
//	      -portal-key ed25519:<portal pass-key> -allow-peer <coordinator peer id>
//
// Register the printed address with the portal (portal relay add <addr>);
// the coordinator then reserves a slot on it and users start measuring it.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"

	"sharetoken/internal/cli"
	"sharetoken/p2p"
	"sharetoken/relay"
	"sharetoken/relaypass"
)

func main() {
	keyPath := flag.String("key", "relay.key", "identity seed file (created with mode 0600 if missing); keep it: the address users know includes the peer ID")
	listen := flag.String("listen", "/ip4/0.0.0.0/tcp/4001,/ip4/0.0.0.0/udp/4001/quic-v1,/ip6/::/tcp/4001,/ip6/::/udp/4001/quic-v1",
		"comma-separated listen multiaddrs (IPv4 and IPv6 by default; IPv6 is skipped on hosts without it)")
	announce := flag.String("announce", "", "comma-separated public multiaddrs to advertise instead of the interface addresses (cloud VMs behind 1:1 NAT); list the IPv6 ones too")
	status := flag.String("status", "", "serve JSON stats at http://<addr>/ (e.g. 127.0.0.1:8093; keep it private)")
	maxRes := flag.Int("max-reservations", 4096, "maximum reservations held at once")
	maxPerIP := flag.Int("max-reservations-per-ip", 64, "maximum reservations from one IP address")
	maxCircuits := flag.Int("max-circuits", 4096, "maximum open circuits to one destination peer (the coordinator receives one per online user)")
	maxConns := flag.Int("max-conns", 8192, "connection-manager high watermark")
	var portalKeys, allowPeers cli.Repeated
	flag.Var(&portalKeys, "portal-key", "portal public key (ed25519:<hex>, from `portal pass-key`); reservations then need a pass it signed (repeatable, for key rotation)")
	flag.Var(&allowPeers, "allow-peer", "peer ID that may reserve without a pass: the coordinator (repeatable)")
	maxDevices := flag.Int("max-devices-per-user", 4, "devices of one user that may hold reservations at once")
	flag.Parse()

	cfg := relay.Config{
		Listen: cli.List(*listen), Announce: cli.List(*announce),
		MaxReservations: *maxRes, MaxReservationsPerIP: *maxPerIP, MaxCircuits: *maxCircuits, MaxConns: *maxConns,
		MaxDevicesPerAccount: *maxDevices,
	}
	for _, k := range portalKeys {
		pub, err := relaypass.ParseKey(k)
		if err != nil {
			log.Fatal(err)
		}
		cfg.PortalKeys = append(cfg.PortalKeys, pub)
	}
	for _, a := range allowPeers {
		id, err := peer.Decode(a)
		if err != nil {
			log.Fatalf("-allow-peer %s: %v", a, err)
		}
		cfg.AllowPeers = append(cfg.AllowPeers, id)
	}
	if len(cfg.PortalKeys) == 0 {
		log.Print("WARNING: no -portal-key: open relay, anyone may reserve; pass the portal's key to admit only registered users")
	} else if len(cfg.AllowPeers) == 0 {
		log.Print("WARNING: no -allow-peer: the coordinator cannot reserve here, so users cannot reach it through this relay")
	}

	keys, err := p2p.LoadOrCreateKeys(*keyPath)
	if err != nil {
		log.Fatal(err)
	}
	r, err := relay.New(keys, cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer r.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fmt.Println("relay", keys.ID())
	for _, a := range r.Addrs() {
		fmt.Println("  ", a)
	}
	fmt.Printf("register it with:\n   portal relay add '%s' [name] [region]\n", registerAddr(r.Host.ID(), r.Host.Addrs()))
	if !hasIPv6(r.Host.Addrs()) {
		log.Print("note: no IPv6 address; IPv6-only users cannot reach this relay")
	}

	if *status != "" {
		srv := &http.Server{Addr: *status, ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"peer": keys.ID(), "addrs": r.Addrs(), "peers": len(r.Host.Network().Peers()), "stats": r.Stats(),
			})
		})}
		go func() {
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("status server: %v", err)
			}
		}()
		defer srv.Close()
	}
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			st := r.Stats()
			log.Printf("peers=%d reservations=%d circuits=%d relayed=%dB denied(res/circ)=%d/%d passes(ok/bad)=%d/%d",
				len(r.Host.Network().Peers()), st.Reservations, st.Circuits, st.BytesRelayed, st.ReservationsDenied, st.CircuitsDenied,
				st.PassesAccepted, st.PassesRejected)
		}
	}
}

// registerAddr is the relay's address for `portal relay add`: for IPv4 and
// for IPv6, the public addresses if there are any, else the private ones;
// loopback only when there is nothing else (a local test). Behind 1:1 NAT pass
// -announce, and the hint is exact.
func registerAddr(id peer.ID, addrs []ma.Multiaddr) string {
	var pick []ma.Multiaddr
	for _, fam := range []int{ma.P_IP4, ma.P_IP6} {
		var public, private []ma.Multiaddr
		for _, a := range addrs {
			if _, err := a.ValueForProtocol(fam); err != nil || manet.IsIPLoopback(a) || manet.IsIP6LinkLocal(a) {
				continue
			}
			if manet.IsPublicAddr(a) {
				public = append(public, a)
			} else {
				private = append(private, a)
			}
		}
		if len(public) > 0 {
			pick = append(pick, public...)
		} else {
			pick = append(pick, private...)
		}
	}
	if len(pick) == 0 {
		pick = addrs
	}
	return p2p.RelayAddr(peer.AddrInfo{ID: id, Addrs: pick})
}

func hasIPv6(addrs []ma.Multiaddr) bool {
	for _, a := range addrs {
		if _, err := a.ValueForProtocol(ma.P_IP6); err == nil && !manet.IsIP6LinkLocal(a) {
			return true
		}
	}
	return false
}
