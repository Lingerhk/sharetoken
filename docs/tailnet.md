# Direct tunnels

**English** | [简体中文](zh-CN/tailnet.md)

A buyer and a seller that are both behind NAT reach each other through a relay unless libp2p's hole punching gets a direct connection, and it often does not. Since release 1.0.4 the client has a second way to a direct connection: a WireGuard tunnel between the two machines, set up with Tailscale's data plane. This document says what that is, how it works, how to turn it on and read its state, what was measured, and where it stops. The short form of the design is in [architecture.md](architecture.md) §2.17, the security statement in [security.md](security.md) §2.9.

## 1. What it is

The client embeds [tailcat](https://github.com/tailscale/tailcat), a library that packages Tailscale's data plane without its control plane: magicsock (path finding and NAT traversal), WireGuard, the DERP relays and a userspace network stack. With it a sharing node gets an address that any buyer holding a ticket can dial, wherever the two are.

- No Tailscale account, no system VPN, no administrator rights, no open port. Nothing on the machine's interfaces, routes or DNS changes: the tunnel ends in a network stack inside the client's own process.
- It is off by default. `sharetoken tailnet on` turns it on, or the desktop app under Settings → Network → "Direct tunnels". Both ends need it on.
- Inside the tunnel runs an ordinary libp2p connection, over QUIC (the default) or TCP. Everything above the connection is what it is on any other path.
- The relays stay: they carry the traffic with the coordinator, hold the sellers' reservations, and carry every job whose tunnel does not come up.

### 1.1 What it is for

Without tunnels, a job between two users behind NAT runs through a relay for its whole length unless hole punching succeeds: the request and the answer (a long context can be megabytes) pass through the relay, within the relay's bandwidth and connection limits, and at a latency the relay's place decides. Tailscale's traversal tries more than libp2p's does: it keeps probing, uses both address families and every local interface, and coordinates the punch through DERP. On the pair of networks measured (§8.2), libp2p stayed on the relay in three of four cases and took about 15 seconds in the fourth; with tunnels on, both directions went direct within seconds.

The gain is twofold: inference traffic comes off the relays, and more pairs of users get a direct connection.

### 1.2 What it does not do

- **It does not promise lower latency.** A direct path is as fast as the public route between the two machines. A relay close to one of them can be just as fast: on the measured pair the first byte of a small request came after 777 ms through the tunnel and 990 ms through the relay (§8.7).
- **It does not replace the relays or the coordinator.** A client with tunnels on still needs a relay to reach the coordinator, and a seller still reserves on relays.
- **It adds no protection and no access control.** Identity, confidentiality and billing rest on what they rested on before (§6).
- **It does not hide addresses.** Two directly connected machines see each other's IP address.

## 2. How it works

```
seller                                                buyer
libp2p host                                           libp2p host
 ├ TCP and QUIC transports (as before)                 ├ TCP and QUIC transports (as before)
 └ tailnet transport                                   └ tailnet transport
     tailcat server ─ WireGuard ─ DERP, then direct ─────  tailcat client (one per seller)
     port 4001 inside the tunnel (UDP and TCP)
        └── libp2p connection: QUIC, or TCP with Noise/TLS and yamux ──┘
                 on it: /sharetoken/infer/1.0.2 and every other protocol
```

The code is one libp2p transport, [`internal/tailnet`](../internal/tailnet), wired into the client in [`internal/client/tailnet.go`](../internal/client/tailnet.go).

### 2.1 The address in the offer

A sharing node with tunnels on starts a tailcat server in the DERP region nearest to it, listens inside the tunnel, and adds one more address to its offer: `/tailcat/<address>`. The address is a string that begins with `tc` and holds the server's public keys, a pre-shared key and the DERP region the server waits in. The multiaddr protocol `tailcat` has a code from the private-use range (`0x30746c`, [`addr.go`](../internal/tailnet/addr.go)).

- The address goes only into offers. It is kept out of the addresses libp2p's identify tells every peer, so only buyers the coordinator sends get it.
- The coordinator copies an offer's addresses into the ticket as it always did; to it they are strings.
- A program that does not know the protocol name fails to parse the address and skips it. That is what a client older than 1.0.4 does. A 1.0.4 client with tunnels off does not dial it.
- The server's keys are made anew every time the server starts. After a restart of the seller, or `tailnet off` and `on`, the next offer carries a new address and the old one leads nowhere.

### 2.2 DERP first, then direct

DERP relays are HTTPS servers that forward WireGuard packets by public key. A tunnel's first packets go through the DERP region named in the address, so a tunnel works as soon as both ends reach that region. Meanwhile magicsock probes for a direct UDP path between the two machines and moves the tunnel to it. Where no direct path exists, the tunnel stays on DERP and still works.

With TCP inside the tunnel, the first request to a new node connected in 1.3 to 1.8 seconds through DERP, and the tunnel was direct 1 to 3 seconds later (§8.2).

A buyer does not take the DERP hosts from the address. It looks the region up in its own DERP map (`tailnet.derp_map`) and uses its own map's entry; a region its map does not hold is refused, and the job stays on its other paths. A seller therefore cannot send buyers to hosts of its choice, and buyer and seller must use the same DERP map. The default is tailcat's public map on both sides.

### 2.3 The libp2p connection inside

The tunnel carries a libp2p connection to the node's listener on port 4001 inside the tunnel. The peer proves its identity in the handshake of that connection, as on any other: a connection to the tunnel address of one node that is answered by another peer ID is dropped. What follows is unchanged:

- tickets, receipts, acknowledgements, billing and disputes;
- the infer protocol with its compression, heartbeats and resuming;
- the three client APIs (chat completions, Messages, Responses), which end in the buyer's process before anything reaches a libp2p stream;
- the platform's audits: its auditor dials only a seller's circuit addresses on the network's relays.

The coordinator, the relays and the portal need no change and know nothing of tunnels. The first measurements ran against a 1.0.3 portal, coordinator and relays.

### 2.4 Dial order and falling back

For a job to a node the buyer has no connection to:

1. The ticket arrives with the node's addresses, the tunnel address among them.
2. The buyer starts a dial of the tunnel address on its own, with a direct connection required (`Transport.Reach` in [`reach.go`](../internal/tailnet/reach.go)). libp2p's dialer stops its other attempts as soon as one address connects, and that is usually the relay: left to itself it would never finish the tunnel. No such dial starts when a direct connection to the node already stands (the two are on one LAN, or hole punching won).
3. The job's stream is opened on whichever connection stands first. Among a node's addresses the tunnel address is dialed first; libp2p would dial an address kind it does not know last, a second after the others.
4. Once the tunnel's connection stands, libp2p opens every new stream to the node on it, since it prefers a direct connection to a relayed one. The relayed connection is left to end by itself.

So the first job to a node may run over the relay and the following ones over the tunnel. `tailnet.wait_ms` makes a job wait at step 2, up to that long, for the direct connection; a job that is being resumed never waits.

A node's offer lists every address it listens on, and a dial to an address of another network hangs until its timeout. The client therefore dials up to 64 addresses of a peer at a time, in place of libp2p's 8, so that those addresses do not hold up the tunnel or the relay address ([`internal/p2p/host.go`](../internal/p2p/host.go)).

The job runs over the relay, as it would without tunnels, when:

| Case | What happens |
|---|---|
| Either end has tunnels off, or is older than 1.0.4 | No tunnel address in the offer, or the address is skipped |
| The seller cannot reach a DERP relay | No address in its offer; it tries again after 5 seconds, then at doubling intervals up to 5 minutes, and `sharetoken tailnet` shows the reason |
| The seller's DERP region is not in the buyer's map | The buyer refuses the address |
| The tunnel dial fails or times out (30 seconds) | The node is not tried again for 1 minute |
| The tunnel ends under a job | The job resumes over the node's other paths (§8.5) |

### 2.5 QUIC or TCP inside the tunnel

A sharing node serves both on the tunnel's one port, under one address. The buyer chooses with `tailnet.transport`.

| | QUIC (default) | TCP |
|---|---|---|
| What runs | quic-go on one UDP flow of the tunnel per connection, with libp2p's TLS identity; streams are QUIC streams | The tunnel's userspace TCP, then libp2p's usual security handshake and yamux |
| Packets | At most 1232 bytes, the largest datagram the tunnel carries whole; no path MTU probing | The tunnel's TCP segments |
| When the connection starts | When the tunnel has found its direct path, or after 12 seconds on DERP | At once, through DERP; it is not slowed by the move to the direct path |
| A large request on a clean path | 4 MB in about 0.7 s | 4 MB in about 2 s, as through the relay |
| On a lossy or jittery link | A connection that stumbles while it ramps up stays slow (§5) | Slower than a good QUIC connection, steadier than a bad one |
| A network with no direct path | Every new connection waits 12 seconds first | No wait |
| Tunnel buyers of one seller at a time | Not counted against the per-subnet limit | All tunnel addresses lie in one /48, and libp2p's default of 64 connections per /48 bounds them |

**Why QUIC waits for the direct path.** quic-go ends a connection's slow start when round trips get longer than the shortest it has seen. A DERP relay between two machines on fast links is often the shorter way (175 ms against 200 ms on the measured pair). A QUIC connection that shook hands on DERP then took the direct path's longer round trips for congestion, left slow start at its first sixteen packets and grew by one packet per round trip from there: 4 MB in 8 seconds, against 0.7 seconds for a connection that began on the direct path. So the QUIC dial waits until the tunnel is direct, up to 12 seconds from the tunnel's first answer, and then starts on DERP if it has to. Jobs run over the node's other paths meanwhile ([`quic.go`](../internal/tailnet/quic.go)).

**The handshake may take 12 seconds as well.** The first WireGuard handshake message of a new tunnel is sometimes lost while the DERP path comes up, and WireGuard sends the next one five seconds later; quic-go's default limit of five seconds ended such handshakes.

**No automatic fallback from QUIC to TCP.** A buyer set to QUIC that finds no QUIC on a tunnel leaves the job on the node's other paths and tries again a minute later. A fallback that remembered a node as "TCP only" was tried and removed: one chance timeout pinned the buyer to TCP from then on.

A change of `tailnet.transport` holds for connections dialed from then on. Connections that stand stay as they are, and a client can have QUIC and TCP tunnels to different sellers at once.

### 2.6 A Tailscale network of the system

A machine that runs Tailscale itself has an address every device of its tailnet reaches directly (in `100.64.0.0/10` or `fd7a:115c:a1e0::/48`), on an interface libp2p does not announce. With tunnels on, a sharing node adds its listeners at that address to its offer, and a buyer on a Tailscale network dials them beside the tunnel address. The path shows as `system`. This mode has unit tests only (§9).

## 3. Settings and commands

### 3.1 Command line

| Command | Meaning |
|---|---|
| `sharetoken tailnet` | Whether it is on, the DERP region this machine is reachable through as a seller, each tunnel's path and transport |
| `sharetoken tailnet on` | Turn it on |
| `sharetoken tailnet on -transport tcp` | Turn it on with TCP inside the tunnel for the connections this client dials (`-transport quic` goes back to the default) |
| `sharetoken tailnet on -wait 3000` | A request to a node waits up to 3 seconds for a new tunnel before it uses the relay (0 to 10000; default 0) |
| `sharetoken tailnet on -derp-map <URL or file>` | Use the DERP relays of that map in place of tailcat's public ones |
| `sharetoken tailnet off` | Back to the relays and libp2p's own direct connections |

A flag left out of `tailnet on` keeps the value it had. The settings are saved to `config.json` first, and a running client takes them up at once: no restart. Turning tunnels off, or changing the DERP map, closes the tunnels at once; jobs on them resume over the nodes' other paths.

### 3.2 config.json

| Key | Default | Meaning |
|---|---|---|
| `tailnet.enabled` | false | Tunnels on or off |
| `tailnet.transport` | `quic` | What this client's connections to sellers run over inside the tunnel: `quic` or `tcp`. The default is not written to the file |
| `tailnet.wait_ms` | 0 | How long a request waits for the tunnel to a node it has no direct connection to yet (at most 10000). 0: it does not wait |
| `tailnet.derp_map` | empty | Where the DERP map comes from: empty for tailcat's public relays, an `http(s)` URL, or the path of a file. The fetched map is kept in the client's data directory, so a start without the map's server still finds its relays |

The full table of the client's configuration is in [configuration.md](configuration.md). The local API has the same settings: `GET` and `PUT /api/v1/tailnet` (fields left out of a `PUT` keep their value), `POST /api/v1/tailnet/reload` to take up the file again, and a `tailnet` section in `GET /api/v1/status` while tunnels are on.

### 3.3 Desktop app

Settings → Network has the switch "Direct tunnels". Ticking it applies at once and shows the same three settings, each with a line that explains it: the transport, the wait for a tunnel, and the DERP map (shown as the DERP relays). "This device" says whether buyers reach this machine through a tunnel, and the panel "Tunnels" lists every tunnel with its direction, peer, path, transport, latency and traffic. The status bar has the tunnels in one item: off; on with no tunnel yet; or the transport, the path, the number of tunnels and their average latency. The page reads and writes `/api/v1/tailnet`; before the network is joined the daemon saves the settings itself. The app is described in [desktop.md](desktop.md).

### 3.4 Reading `sharetoken tailnet`

```
tailnet on (quic), relays from <source of the DERP map>
   reachable as a seller through DERP region <code>
          PEER  PATH  OVER  ENDPOINT  RTT  CONNS  RX  TX
   to seller    …
   from buyer   …
```

| Part | Meaning |
|---|---|
| First line | On or off; the transport this client dials with; the DERP map's URL, or `file`; the wait, if one is set |
| `reachable as a seller through DERP region …` | Sharing is on and the tunnel server is up. Otherwise: `not reachable as a seller: <reason>`, or `not serving` (sharing is off, or the server is starting) |
| `this machine is on a Tailscale network …` | Shown when the system runs Tailscale (§2.6) |
| `to seller` rows | Tunnels this client dialed. `PEER`: the seller's peer ID. `CONNS`: connections open or being opened on the tunnel; a tunnel with 0 is waiting to be closed (§4) |
| `from buyer` rows | Buyers' tunnels to this node, with the bytes received (`RX`) and sent (`TX`). The buyer's peer ID is not known at this level |
| `PATH` | `direct`; `derp:<region>` while a DERP relay carries the packets; `tunnel` before the path is known |
| `OVER` | `quic` or `tcp`: what the tunnel's latest connection runs on |
| `ENDPOINT`, `RTT` | The other machine's address on a direct path, and the latest round trip |

`sharetoken status` prints the same lines while tunnels are on. A tunnel in use has its path checked every 30 seconds.

### 3.5 The `tailnet=` field of the timing

Every job's timing is in the response header `X-Sharetoken-Timing` and, for a streamed answer, in the closing comment line. A job that ran through a tunnel has one more field at the end:

```
X-Sharetoken-Timing: ticket_ms=… dial_ms=… upload_ms=… first_byte_ms=… total_ms=… relayed=false … tailnet=direct
```

| Value | Meaning |
|---|---|
| no `tailnet=` field | The job did not use a tunnel: `relayed=true` for a relay, `relayed=false` for another direct connection |
| `tailnet=direct` | Through the tunnel, packets straight between the two machines |
| `tailnet=derp:<region>` | Through the tunnel, packets still carried by that DERP region |
| `tailnet=system` | Over the system's Tailscale network (§2.6) |
| `tailnet=tunnel` | Through a tunnel whose path was not known yet |

The path is read when the job ends: a job that began on DERP and ended on the direct path says `direct`, and a resumed job names the connection it ended on. `dial_ms` near zero means the connection existed already.

## 4. Idle behaviour

| What | Closed when | Time |
|---|---|---|
| A buyer's connection through the tunnel to one seller | No new request was opened on it | 10 minutes, checked every 30 seconds: between 10 and 10.5 minutes. A request in progress keeps it |
| The tunnel itself (a WireGuard engine with its own UDP socket and DERP connection) | It has no connection | 2 minutes more |
| Tunnels to sellers | There are 16 and one more is needed | The one longest without a connection is closed. If every one has a connection the dial fails and the job uses the relay |
| A tunnel dial that failed | | Not tried again for 1 minute |
| The seller's side | Never while sharing is on | Its address changes when the client restarts or tunnels are turned off and on |

The idle time counts from the last request opened on the connection, not from what a check happens to see: a connection that carries a short request every minute is in use.

What the next request does after an idle close:

- **Within the 2 minutes**, the tunnel still stands with its direct path. The new connection needs no new WireGuard handshake.
- **After that**, the buyer dials the tunnel and the relay together and uses whichever connects first. Both were seen: when the tunnel came first, the request spent 0.8 to 2.8 seconds dialing and ran direct; when the relay came first, the request ran over the relay while the tunnel came up in the background, and the request sent 6 seconds later ran direct.

The request does not fail in either case. A client that should send the first request after a long pause through the tunnel as well can set `-wait`; that setting was not exercised on real machines (§9).

## 5. Choosing QUIC or TCP

QUIC is the default because on a clean path it is clearly faster for large requests. It has one failure mode that TCP inside the tunnel does not have, and the measurements show both.

- **Clean wired path, about 200 ms.** QUIC uploaded 4 MB in about 0.7 seconds. The tunnel's TCP and the relay both took about 2 seconds (§8.6).
- **A connection that stumbles early stays slow.** quic-go's congestion control is fixed: Reno with HyStart. A connection that loses one packet, or sees a round trip more than 16 ms longer than the shortest so far, while it is still ramping up leaves slow start; from there the window grows by one 1232-byte packet per round trip, which on a 200 ms path is about 30 KB/s gained for every second spent sending. Requests come in bursts with long pauses, and the window does not grow while nothing is sent, so such a connection is slow for as long as it lives; it lives as long as it is used once in ten minutes. quic-go offers no setting for this.
- **Unstable Wi-Fi.** A laptop on Wi-Fi, in the evening, opened six new QUIC connections; all six were slow: 4 MB took 4 to 34 seconds. Over the tunnel's TCP the same laptop in the same period took 2.4 to 5.6 seconds. In an earlier series the same laptop had sent 4 MB over QUIC in 0.77 to 0.92 seconds. This was one machine, and the cause was not isolated further than "packet loss or latency jitter during ramp-up".
- **Small requests.** Requests of tens of KB show no noticeable difference between the relay, TCP and QUIC.

**Recommendation.** Keep the default on wired and stable links. On a client whose link is lossy or jittery and that sends requests of several megabytes (many images, very long contexts), use `sharetoken tailnet on -transport tcp`. The setting is the buyer's, since the buyer uploads the request; a seller serves both.

## 6. Security

The statement is [security.md](security.md) §2.9. In short:

- **Nothing rests on the tunnel.** The libp2p handshake inside it proves the peer's identity, and prompts, tickets and receipts are protected as on any path. WireGuard is one more layer of encryption around that.
- **The tunnel address is not a secret.** Every buyer of the node gets it with the ticket, pre-shared key included. Whoever has it reaches one thing: the node's libp2p listener inside the tunnel, the same one its TCP and QUIC ports offer. The tunnel's network stack has no other service and leads to no network of the machine (no subnet routes, no exit node).
- **A DERP relay sees what a relay sees:** the two addresses, the WireGuard public keys, how much is exchanged and when. It does not see content. The difference is who runs it: the default DERP relays are tailcat's public ones, run by Tailscale and not by the network (§9).
- **A seller cannot choose where a buyer connects.** The buyer uses only DERP relays of its own map (§2.2).
- **Directly connected machines see each other's IP address,** as after a successful hole punch; with tunnels on that happens more often. A user who wants only relays to see their address leaves tunnels off and sets `nat` to false.
- **Bounds.** A buyer keeps at most 16 tunnels. A seller serves at most 64 QUIC handshakes at a time, and the resource manager's other limits apply to tunnel connections as to any.
- **More code in the process.** WireGuard, magicsock and a userspace network stack are a large dependency. tailcat is pinned to an unreleased commit and depends on a pre-release of `tailscale.com`. Features the client does not use (SSH, port mapping, captive-portal detection and others) are left out with build tags ([`scripts/build/client-tags.txt`](../scripts/build/client-tags.txt)); a test keeps the list in step with tailcat's own.
- **Off means off.** With tunnels off the client creates no engine and sends no packet; the transport is installed but dials and serves nothing.

## 7. Compatibility

### 7.1 Client APIs

`/v1/chat/completions`, `/v1/messages` and `/v1/responses`, streamed or not, with tool calls and Codex's WebSocket, end in the buyer's process and are converted there. The tunnel is below the libp2p connection, and a client program cannot tell it from another path except by the `tailnet=` field. See §8.3 and §8.4 for what was run.

### 7.2 Upstream protocols

A seller reaches its model server or hosted API over its own outgoing HTTPS, which the tunnel does not touch: the userspace stack changes no route, creates no interface and takes over no DNS. A provider's proxy setting works as before, and for the same reason tunnels do not get in the way of a system VPN.

### 7.3 Protocols inside the network

| Mechanism | Affected | Note |
|---|---|---|
| Infer protocol (compression, heartbeats, resuming) | No | An ordinary libp2p stream; a job whose tunnel ends resumes on another path |
| Offers, tickets, receipts, acknowledgements | No | The coordinator is still reached through relays only; an offer's addresses are strings to it |
| Signing domains, protocol IDs | No | Unchanged; no protocol upgrade |
| Audits by the platform's auditor | No | It dials only circuit addresses on the network's relays |
| Relay passes, private relays, pairing | No | Nothing changes on a relay; a private seller with a paired buyer was measured (§8.3) |
| Hole punching, UPnP | Coexist | Whichever gives a direct connection first is used; with one standing, no tunnel is started |

### 7.4 Mixed versions and settings

| Buyer | Seller | Result |
|---|---|---|
| 1.0.4, tunnels on | 1.0.4, tunnels on | Tunnel |
| 1.0.4, tunnels on | 1.0.4 with tunnels off, or older | No tunnel address in the offer: relay or libp2p's direct connection |
| 1.0.4 with tunnels off, or older | 1.0.4, tunnels on | The address is skipped: relay or libp2p's direct connection. A 1.0.3 buyer logged no parse error |
| 1.0.4, QUIC | A pre-release 1.0.4 build that serves only TCP in the tunnel | Relay: the QUIC handshake times out after 12 seconds, the buyer logs it and tries again a minute later. With `-transport tcp` the same buyer gets the tunnel |
| any | Coordinator, relays and portal of 1.0.3 | Works; tunnels need nothing from them |

### 7.5 Many clients behind one address

Unrelated to tunnels, but found while testing them: a relay or the coordinator used to accept 8 connections from one public IPv4 address, libp2p's default, so the ninth client behind one office or home address found every relay refusing it. From 1.0.4 relays and the coordinator accept 32 connections per IPv4 address and per IPv6 /56, and 256 per IPv6 /48, four times libp2p's defaults ([`internal/p2p/host.go`](../internal/p2p/host.go)). About 32 clients behind one address can use one relay. This one needs the relays and the coordinator to run 1.0.4.

A buyer that reaches none of a seller's relays, but still gets a ticket, was seen to reach the seller through the tunnel address alone.

### 7.6 Platforms

All six targets (macOS, Linux and Windows on amd64 and arm64) build with `CGO_ENABLED=0`. Linux needs neither `/dev/net/tun` nor `CAP_NET_ADMIN`; on the cloud VM of §8.1 the client ran as an ordinary user. Verified on real machines, each as buyer and as seller: macOS (arm64), Linux (amd64) and Windows 11 (§8.3).

## 8. Measured results

### 8.1 Test bed

- A laptop behind a home NAT (macOS, arm64), on Wi-Fi. In the first series of tunnel tests its `nat` setting was off (no port mapping, no hole punching), to stand for a network where libp2p gets no direct connection.
- A cloud VM behind NAT (Linux, amd64), run as an ordinary user. The two are about 200 ms apart.
- An x86 Linux VM (Ubuntu 24.04) and a Windows 11 VM on a wired LAN behind the same home connection.
- tailcat's public DERP relays.
- Sellers offered five models behind API keys (DeepSeek, two Anthropic models, one OpenAI model, one Gemini model) and, as a private seller, two models of a ChatGPT subscription. Their upstream APIs are chat completions, Messages and Responses.

This is one pair of networks. §9 lists what it leaves open.

### 8.2 Connecting

| Case | Result |
|---|---|
| No tunnels, `nat` on (the home router maps ports), cloud VM buys from the laptop | Relayed throughout (no upgrade within 30 seconds); first dial 5.9 s |
| No tunnels, `nat` on, laptop buys from the cloud VM | Hole punching succeeded after about 15 s |
| No tunnels, `nat` off on the laptop, both directions | Relayed throughout |
| Tunnels on (TCP inside), first request to a new node | Dial 1.3–1.8 s, path `derp:<region>` |
| The same tunnel, 1–3 s later | `direct`, round trip 204–216 ms |
| Requests after that | Dial about 0, all `relayed=false tailnet=direct` |
| Tunnels on (QUIC inside), first request, Linux VM / Windows VM as buyer | Dial 1.8 s / 2.2 s, direct |
| Tunnels on (QUIC inside), first request from the cloud VM, Linux VM / Windows VM as seller | Dial 0.9–1.3 s / 0.9–1.4 s, direct |
| Both ends on one LAN | No tunnel is started: the connection is direct already |

### 8.3 Protocol matrix

Each model runs 51 cases: 12 scenarios (basic, system prompt and turns, tool loop, truncated, reasoning, no reasoning, JSON schema, image, bad request, unknown model, long prompt, burst) over the three client APIs, streamed and not.

| Inside the tunnel | Buyer → seller | Models | Passed |
|---|---|---|---|
| TCP | Laptop → cloud VM | 5 behind API keys | 254 / 255 |
| TCP | Cloud VM → laptop | 5 behind API keys | 254 / 255 |
| TCP | Cloud VM → laptop, private seller with a paired buyer | 2 of a ChatGPT subscription | 102 / 102 |
| QUIC | Laptop → cloud VM | 5 behind API keys | 254 / 255 |
| QUIC | Cloud VM → laptop | 5 behind API keys | 255 / 255 |
| QUIC | Linux VM as buyer | 3 | 153 / 153 |
| QUIC | Windows VM as buyer | 3 | 151 / 153 |
| QUIC | Linux VM and Windows VM as sellers, together | | 102 / 102 |

Every failure is the same case: DeepSeek, image, not streamed. Asked for the colour of a plain red picture, the model answered "White". The request and the response were complete; the image scenario run again three times through the tunnel passed 9 of 9.

In the TCP rows the path of every request was counted from the buyer's log: 300 of 300 direct; 304 direct and 1 on DERP; 137 of 137 direct. None was relayed. In those runs neither side logged an error, and no acknowledgement of a receipt failed.

### 8.4 Real clients

One task (fix a bug in two places and make the tests pass), with the client program pointed at the buyer's local endpoint and the traffic in the tunnel.

| Client | TCP inside, laptop as buyer | TCP inside, cloud VM as buyer | QUIC inside, both directions |
|---|---|---|---|
| Claude Code with 5 models | 5 / 5 | 5 / 5 | 10 / 10 |
| Codex with 5 combinations (one model with WebSocket on and off, three more models) | 5 / 5 | 5 / 5 | 10 / 10 |
| Subscription seller: Codex with 2 models (WebSocket on, off), Claude Code with 1 | | 3 / 3 | |

43 of 43. The cloud VM ran Claude Code 2.1.288 and Codex CLI 0.160.0.

### 8.5 When the tunnel fails

| Case | TCP inside | QUIC inside |
|---|---|---|
| The buyer turns tunnels off during a streamed answer | Resumed once and finished over the relay; the client program saw no error | The same |
| The seller turns tunnels off during a streamed answer | Resumed once and finished over the relay; later requests relayed | The same |
| Tunnels turned on again | The next request starts a new tunnel; back to direct | Two pairs of requests over the relay (1.3 s each), direct from the tenth second |
| The seller's process restarts (new address) | The buyer's next request has a new tunnel in 3.5 s, direct | The next request dials the new tunnel in 0.97 s; direct from then on |
| The seller turns tunnels off and on three times | | The buyer is on the new address within 1–6 s each time |
| UDP between the two dropped (DERP only) | First dial 1.28 s; first byte of small requests 760–985 ms; upload 256 KB 0.75–0.79 s, 1 MB 1.55–2.0 s | Three requests stalled 9–11 s each, the time WireGuard took to notice that the direct path was gone; the connection did not break and nothing was resumed. Then small requests 1.0–1.3 s, upload 256 KB 0.94 s, 1 MB 2.9–3.1 s |
| UDP allowed again | Direct again within about a minute | Direct again after about 25 s |
| 1.0.3 buyer, 1.0.4 seller with tunnels on | All three APIs answer 200 over the relay | |
| 1.0.4 buyer with tunnels on, 1.0.3 seller | 200 over the relay; no tunnel is started | |
| 1.0.4 buyer on, 1.0.4 seller off | 200 over the relay | |
| QUIC buyer, seller that serves only TCP in the tunnel | | Every request succeeds over the relay (§7.4) |

### 8.6 Upload time by request size

A valid chat request with padding, sent by one buyer over each path in turn; the figure is the client's own `upload_ms`, the time until the request body is written. Round trip about 200 ms.

Cloud VM as buyer, seller on the wired LAN (relay and TCP: 4 runs each; QUIC: 8 runs per size, two on each of four new connections):

| Body | Relay | Tunnel, TCP | Tunnel, QUIC |
|---|---|---|---|
| 256 KB | 0.36–0.70 s | 0.80–0.85 s | 0.23–0.62 s |
| 1 MB | 1.09–1.29 s | 1.40–1.48 s | 0.28–0.93 s |
| 4 MB | 1.5–2.2 s | 1.8–2.3 s | 0.66–0.72 s (one connection of the four: 1.56 s) |
| 8 MB | 2.6–3.0 s | 2.6–2.8 s (two more runs: 10 s) | 0.85–1.0 s (one connection of the four: 4.0–4.3 s) |

Cloud VM as buyer, seller on Wi-Fi (the laptop), at the same time:

| Body | Relay | Tunnel, TCP | Tunnel, QUIC |
|---|---|---|---|
| 256 KB | 0.21–0.22 s | 0.85–0.87 s | 0.23–0.63 s |
| 1 MB | 0.64–0.73 s | 1.52–1.55 s | 0.61–0.62 s |
| 4 MB | 1.3–1.6 s | 2.4–3.0 s | 4.3–6.3 s |
| 8 MB | 1.8–2.3 s | 3.1–4.7 s | not measured on the final code |

On the connection to the Wi-Fi seller quic-go's own statistics showed one packet lost in every few thousand; each loss cut the congestion window to 0.7 of its size (812 KB, 568 KB, 398 KB), and on a 200 ms path it grows back slowly.

Laptop as buyer, cloud VM as seller (two of three rounds; in the third both were slow, 4 MB in 7.6–11.8 s). There is no relay figure for this direction: during the test the buyer could not reach the seller's relay (§7.5).

| Body | Tunnel, TCP | Tunnel, QUIC |
|---|---|---|
| 256 KB | 0.84–0.86 s | 0.22–0.67 s |
| 1 MB | 1.51–1.55 s | 0.29–0.45 s |
| 4 MB | 2.7–3.1 s | 0.77–0.92 s |
| 8 MB | 4.2–6.0 s | 1.1–1.5 s |

New QUIC connections, 4 MB sent twice on each, in a later series in the evening:

| Buyer | Connections | Fast | Slow |
|---|---|---|---|
| Linux VM, wired | 8 | 7: first send 1.39–1.48 s (with the ramp-up), second 0.66–0.76 s | 1: 5.3 s and 4.4 s |
| Laptop, Wi-Fi | 6 | 0 | 6: the best 4.1 s and 4.1 s, the others 12.8–33.7 s |

In the same half hour the laptop over the tunnel's TCP (two new connections) took 1.3–1.9 s for 1 MB (one run: 9.7 s) and 2.4–5.6 s for 4 MB; the wired Linux VM as buyer took, for 4 MB, 1.71–2.20 s over the relay, 2.04–2.47 s over TCP and 0.65–0.73 s over QUIC.

A slow connection does not recover by itself. One that came up while other requests were running sent eight large bodies in a row at 0.33, 0.55, 0.75, 0.96, 1.1, 1.2, 1.3 and 1.4 MB/s: a straight line of about 30 KB/s per second of sending (§5).

With TCP inside the tunnel, an earlier series over the same pair of networks found bodies up to a few hundred KB about as fast as over the relay, and bodies of 1 MB and more between as fast and several times slower, changing with direction and time of day (16 MB: 9–15 s against 4.5 s over the relay). tailcat's own speed test over the same tunnel showed 40–60 Mbit/s for TCP. The cause was put down to the tunnel's userspace TCP doing congestion control alone over the whole 200 ms path, where a relay splits the path into two shorter ones under kernel TCP; no packet capture confirmed it. That finding is why QUIC was added.

### 8.7 Small requests

Laptop as buyer, one model, TCP inside the tunnel:

| | Tunnel, direct | Relay |
|---|---|---|
| First byte, not streamed | 955 ms | 1021 ms |
| First byte, streamed | 777 ms | 990 ms |
| First dial to a new node | 1.3–1.8 s | 1.8–5.9 s |

### 8.8 Size and memory

| | Value |
|---|---|
| Client binary, macOS arm64, when tunnels were added | 33.1 MB → 36.9 MB (+3.9 MB, +12%; 39.9 MB without the build tags) |
| Client binary, Linux amd64, when tunnels were added | 34.4 MB → 39.1 MB |
| QUIC inside the tunnel, over a build with TCP only | +0.1 MB (quic-go was a dependency already) |
| Resident memory, tunnels off | 44–47 MB |
| Resident memory, tunnels on and serving, TCP inside (first series) | 48–50 MB; 57–69 MB while the matrix ran |
| Resident memory, buyer, idle to light load | 44 MB with TCP inside, 47 MB with QUIC |
| Resident memory, seller that served several buyers and the matrix | 56 MB with TCP inside, 80 MB with QUIC |

## 9. Limits and open points

- **The public DERP relays are someone else's free service.** tailcat's public relays are run by Tailscale, rate-limited and without a service guarantee, and their terms may change. They carried megabyte uploads in the tests (§8.5); heavier traffic through them was not tried. From a server in mainland China the public map and four of its regions answered, with TLS handshakes of 2 to 7 seconds: usable, not dependable. A network should run DERP relays of its own, and give both ends their map, before it turns tunnels on by default.
- **QUIC's congestion control cannot be tuned.** quic-go has Reno with HyStart and no option to change it. A connection that leaves slow start early stays slow on a long path (§5). Until that changes, `-transport tcp` is the answer for a client on a poor link.
- **A network with no direct path waits 12 seconds per new QUIC connection.** Where the tunnel can only stay on DERP, each new QUIC connection waits out the full 12 seconds before it starts there; requests use the relay meanwhile. TCP inside the tunnel has no such wait.
- **The first request after a long pause often uses the relay.** That is the design: the request does not wait for the tunnel unless `tailnet.wait_ms` says so.
- **A seller stays in the DERP region it picked at start.** While that region is down, new buyers get no tunnel and use the relay; tunnels that are direct already are not affected.
- **Not exercised on real machines:** the system's Tailscale network (§2.6), which has unit tests only, and `tailnet.wait_ms`.
- **One pair of networks.** A home NAT and a cloud NAT, about 200 ms apart, with at most three buyers on one seller at a time. There are no figures for symmetric or carrier-grade NAT, for direct-connection rates in general, for many buyers at once, or for heavy traffic through DERP.
- **Dependencies before their stable release.** tailcat's API may still change; an upgrade needs the tests of §8 run again.
