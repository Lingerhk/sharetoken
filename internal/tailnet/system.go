package tailnet

import (
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

// A machine that already runs Tailscale (or Headscale) has a second way to
// be reached directly: its address on that network, which every device of
// the same tailnet can dial as it is. libp2p does not announce it: it
// announces the addresses of the interface the default route uses, and the
// Tailscale interface is another one. With the tailnet on, a sharing node
// adds these addresses to its offer, and a buyer that is on a Tailscale
// network itself dials them with the tunnel's address. Buyers elsewhere
// cannot reach them, as with the LAN addresses an offer always carried.

// The address ranges Tailscale gives its devices: the carrier-grade NAT
// range, and Tailscale's own unique-local IPv6 prefix.
var systemRanges = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("fd7a:115c:a1e0::/48"),
}

// onSystemTailnet reports whether ip is a Tailscale device address.
func onSystemTailnet(ip netip.Addr) bool {
	ip = ip.Unmap()
	for _, r := range systemRanges {
		if r.Contains(ip) {
			return true
		}
	}
	return false
}

// interfaceIPs is this machine's interface addresses; a variable for tests.
var interfaceIPs = func() []netip.Addr {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []netip.Addr
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			if ip, ok := netip.AddrFromSlice(n.IP); ok {
				out = append(out, ip.Unmap())
			}
		}
	}
	return out
}

var (
	systemMu   sync.Mutex
	systemAt   time.Time
	systemLast []netip.Addr
)

// systemIPs is this machine's addresses on a Tailscale network, read at most
// every 30 seconds: none when the OS runs no Tailscale.
func systemIPs() []netip.Addr {
	systemMu.Lock()
	defer systemMu.Unlock()
	if !systemAt.IsZero() && time.Since(systemAt) < 30*time.Second {
		return systemLast
	}
	var out []netip.Addr
	for _, ip := range interfaceIPs() {
		if onSystemTailnet(ip) {
			out = append(out, ip)
		}
	}
	systemAt, systemLast = time.Now(), out
	return out
}

// SystemAddrs is where a host listening on listen can be dialed over the
// Tailscale network its machine is on: each listener on all interfaces, at
// each of the machine's Tailscale addresses. Empty without one.
func SystemAddrs(listen []ma.Multiaddr) []ma.Multiaddr {
	return systemAddrs(systemIPs(), listen)
}

func systemAddrs(ips []netip.Addr, listen []ma.Multiaddr) []ma.Multiaddr {
	var out []ma.Multiaddr
	for _, l := range listen {
		s := l.String()
		for _, ip := range ips {
			var rest string
			var ok bool
			if ip.Is4() {
				rest, ok = strings.CutPrefix(s, "/ip4/0.0.0.0/")
			} else {
				rest, ok = strings.CutPrefix(s, "/ip6/::/")
			}
			if !ok {
				continue
			}
			fam := "/ip4/"
			if ip.Is6() {
				fam = "/ip6/"
			}
			if m, err := ma.NewMultiaddr(fam + ip.String() + "/" + rest); err == nil {
				out = append(out, m)
			}
		}
	}
	return out
}

// systemPeerAddrs is the addresses among addrs that lie on a Tailscale
// network, when this machine is on one too (they mean nothing to it
// otherwise): what Reach dials next to the tunnel's address.
func systemPeerAddrs(addrs []ma.Multiaddr) []ma.Multiaddr {
	if len(systemIPs()) == 0 {
		return nil
	}
	var out []ma.Multiaddr
	for _, a := range addrs {
		if _, err := a.ValueForProtocol(ma.P_CIRCUIT); err == nil {
			continue
		}
		ip, err := manet.ToIP(a)
		if err != nil {
			continue
		}
		if nip, ok := netip.AddrFromSlice(ip); ok && onSystemTailnet(nip) {
			out = append(out, a)
		}
	}
	return out
}
