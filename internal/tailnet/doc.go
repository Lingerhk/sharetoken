// Package tailnet carries libp2p connections between two users over
// Tailscale's data plane, as the tailcat library packages it: a WireGuard
// tunnel whose packets start out through a DERP relay and move to a direct
// UDP path once magicsock has traversed the NATs between the two machines.
// No Tailscale account, control server or system VPN is involved, and
// nothing on the machine's routing or DNS changes: the tunnel ends in a
// userspace network stack inside this process.
//
// It is one more libp2p transport. A sharing node listens on a tailcat
// server and advertises its address in its offer as /tailcat/<address>; a
// buyer that has the tailnet on dials it beside the node's other addresses.
// libp2p's own security (Noise or TLS, bound to the peer ID) and stream
// multiplexing run inside the tunnel, so everything above the connection
// (tickets, the infer protocol, receipts) is the same as over any other
// path, and the peer a connection belongs to is proved by libp2p, not by
// the tunnel.
//
// Compared with the circuit relays this path costs a relay nothing once it
// is direct, and it reaches direct paths libp2p's hole punching does not:
// magicsock keeps probing, uses both address families and every local
// interface, and falls back to DERP only for as long as it has to.
package tailnet
