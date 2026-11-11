# Architecture

**English** | [简体中文](zh-CN/architecture.md)

sharetoken is a peer-to-peer marketplace for LLM inference. This document describes the four roles of the system, how they talk to each other, and the design decisions behind them. Receipts, evidence-based dispute resolution, audits and TEE attestation are the cryptographic foundation underneath; this document covers the roles and the architecture built on top of them.

Related documents: [guide.md](guide.md) for commands and APIs, [configuration.md](configuration.md) for configuration files, [security.md](security.md) for the threat model and guarantees, [self-hosting.md](self-hosting.md) for running your own network, [development.md](development.md) for building and testing.

## 1. Roles

| Role | Program | What it does | What it does not do |
|---|---|---|---|
| **User** | `sharetoken` | One process is both buyer and seller: a local OpenAI-compatible endpoint (buying) and, optionally, sharing local models or hosted APIs (selling). Picks the best relay automatically. Serves a local management API used by the CLI and the desktop app | Needs no open port; never connects to the coordinator directly |
| **Relay** | `relay` | libp2p circuit relay v2 forwarding only. There are several, and different operators can run them | Keeps no accounts and no state; sees no plaintext (Noise end to end) |
| **Coordinator** | `coordinator` | The accounting platform: matching, signing tickets, settlement, disputes and evidence rulings, audits | **Listens on no port**; reachable only through its reservations on the relays. Does not manage users |
| **Portal** | `portal` | The user-management backend (HTTP JSON API and web pages): sign-up and login, API tokens, device binding, credit balances and grants, billing and token usage, the relay registry | Does not speak p2p and does not talk to the coordinator |

The portal and the coordinator **share one database** and never talk to each other directly. The portal writes accounts and credit grants, the coordinator writes jobs and settlements, and both move money only through the double-entry ledger, inside database transactions.

An optional fifth program, `mailserver`, delivers the mail the portal queues (verification codes, notices) and receives mail for a few mailboxes; see [mail.md](mail.md).

```
                         HTTPS (sign-up, login, balance, relay list)
   ┌───────────────┐ ────────────────────────────────────────────► ┌──────────┐
   │ user sharetoken│                                                │  portal  │
   │  buy + sell    │                                                └────┬─────┘
   │  127.0.0.1:9787│      ┌─────────┐   ┌─────────┐                      │ shared database
   └──────┬────────┘      │ relay A │   │ relay B │ …                 ┌────┴──────┐
          │ measure RTT,   └──┬──────┘   └──┬──────┘                   │   MySQL   │
          │ pick the best     │             │                         │           │
          ├───────────────────┘ control     │                         │           │
          │   /p2p-circuit ──────────────► the coordinator holds a   ◄─┤           │
          │                                 reservation on every relay └────┬──────┘
          │                                 └──── coordinator (no listener) ─┘
          │ data plane: buyer ⇄ seller, direct or relayed (Noise + box, two layers)
   ┌──────┴────────┐
   │ another user   │
   └───────────────┘
```

### Source layout

| Path | Contents |
|---|---|
| `cmd/sharetoken`, `cmd/relay`, `cmd/coordinator`, `cmd/portal`, `cmd/mailserver` | The binaries |
| `internal/client`, `internal/buyer`, `internal/node`, `internal/provider` | The user client: agent and local API, buying, selling, hosted-API providers |
| `internal/relay`, `internal/relaypass` | The relay and its admission passes |
| `internal/coordinator`, `internal/ledger`, `internal/audit`, `internal/tokens` | Matching and settlement, the double-entry ledger, audits, reference token counting |
| `internal/portal`, `internal/accounts`, `internal/mailer`, `internal/totp` | The portal, the account store, mail, TOTP |
| `internal/p2p`, `internal/protocol`, `internal/box`, `internal/receipt`, `internal/evidence`, `internal/attest`, `internal/canon` | libp2p hosts and relays, wire framing and messages, payload encryption, receipts, evidence, TEE attestation, canonical signing bytes |
| `internal/config`, `internal/mysqldb`, `internal/secfile`, `internal/cli`, `internal/version` | Configuration files, the database layer, private files, CLI helpers, version information |
| `gui/` | The desktop shell ([desktop.md](desktop.md)) |
| `test/e2e`, `test/flow` | End-to-end tests |
| `configs/*.example.yaml` | Example configuration files |

## 2. Key design

### 2.1 Money belongs to users, not to peers

- The portal **binds** a device's peer ID to a user. On every authenticated libp2p stream the coordinator resolves the peer to the account `user:<id>` (`accounts.DeviceAccount`). A user can have several devices, and credits earned by selling on one can be spent on any other.
- Each job records `buyer_acct`, `node_acct`, `prompt_tokens` and `completion_tokens`. The payer and the payee are fixed **when the ticket is signed**; unbinding a device later does not change where that job's money goes.
- Matching excludes every device of the **same user**, so nobody can trade with themselves to churn fees or farm reputation.
- A device that is unbound, or whose user is disabled, gets `unauthorized` when it announces an offer, requests a ticket or asks for its balance. For tests and local demos, `-open-accounts` (`dev.open_accounts`) gives every peer its own account.
- Audit identities are funded by the platform and remain peer-level accounts (`acct:<peer>`).

### 2.2 Binding a device requires proof of its private key

A bind request carries the device's Ed25519 signature over `canon("sharetoken/device-bind/v1", username, peer ID, timestamp)`. The portal verifies the signature and requires the timestamp to be within ±5 minutes (`accounts.VerifyBind`). Without this, anyone could bind someone else's node to their own account and collect its income. A device already bound to another user cannot be taken over (`409 bound_elsewhere`). A node in a TEE keeps its key only in guest memory and gets a new one at every start, so the client rebinds automatically at start-up with its saved token.

### 2.3 The coordinator is reachable only through relays

- By default the coordinator **does not listen** (`DialOnly`). After start-up it reserves a slot on every relay (`p2p.Reserver` renews before expiry and reserves again after a disconnect). Users only ever learn `<relay>/p2p-circuit/p2p/<coordinator>`.
- The relay list lives in the database. An administrator registers a relay with `portal relay add`; the coordinator syncs the list every 15 seconds (`network.relay_sync`) and writes back whether it holds a reservation on each relay (`relays.coordinator_ok`). The portal hands this to users in `GET /api/v1/network`.
- The control plane is still Noise end to end when relayed: a relay cannot see tickets or receipts, let alone the one-time keys disclosed in dispute evidence. This is why the coordinator reserves on relays instead of having relays forward at the application layer, which would let a relay read the entire control plane.
- The auditor also reaches the coordinator and the nodes through relays, on the same path as an ordinary buyer.

### 2.4 Users choose a relay by network quality

1. At start-up the client fetches the relay list from the portal. If the portal is unreachable it uses the last list it cached in `network.json`.
2. It connects to every relay in parallel and pings each three times, taking the median RTT (`p2p.MeasureRelays`).
3. `chooseRelay` picks the relay with the lowest RTT among those the portal marks as reachable by the coordinator; if none is, it falls back to any relay it can connect to. To avoid flapping it switches only when the new best is faster than the current one by more than `max(5 ms, 20%)`.
4. Every 10 seconds the client checks its connection to the coordinator. If it is down, the client refreshes the list from the portal, measures again and switches at once. A full measurement runs every 5 minutes regardless.
5. Live link quality: each 10-second check also pings the current relay once (`p2p.LinkMonitor`). The last 30 results (about 5 minutes) give the current latency, the average, minimum and maximum, the jitter (mean difference between consecutive RTTs) and the loss rate, shown under `coordinator.link` in the status for the CLI and the desktop app. The window restarts when the relay changes. A relay that keeps losing pings is switched away from and avoided for 5 minutes. Note that this measures the segment from this machine to the relay, not from the relay to the coordinator or to a seller.
6. When sharing, the client reserves on its best 2 relays (configurable) and its offer advertises both its own addresses and the relay addresses; when the set of reservations changes it announces again at once. A buyer connects directly when it can and goes through a relay when DCUtR hole punching fails.

### 2.5 Offers are announced directly

A node sends an `announce` over the control plane every 15 seconds; it doubles as the heartbeat. The coordinator checks that the announcer is the node that signed the offer and resolves its account.

### 2.6 A node abandons a job it cannot deliver

When a node fails before delivering anything (the model server errors, it is at capacity, the request cannot be decrypted), it sends `abandon` to the coordinator. The coordinator returns the buyer's hold immediately, frees the node's concurrency slot and records one failure. Only the assigned node can abandon a job, and only before settlement. Abandoning means giving up payment, so a node has no reason to lie.

### 2.7 A buyer that takes a ticket and never connects

The coordinator has to tell "the buyer never came" from "the job is still running".

- When a node takes a job (the buyer connected, the ticket verified, a concurrency slot acquired) it sends `started` to the coordinator **in the background**, adding nothing to the time to first byte. The coordinator records `jobs.started_at`.
- A job that has not started 30 seconds after its ticket expired (ticket lifetime: 1 minute by default) is released: the whole hold goes back to the buyer, the node's **reputation is not touched**, and its slot is free again. A node is held up for at most 1.5 minutes.
- A job that has started runs as usual until it is settled or reaches its settlement deadline; only then does it count as the node's failure.
- To stop ticket hoarding: once a buyer account holds 5 tickets that were issued more than 15 seconds ago and never started, it gets no new ticket (`busy`). Normal concurrent requests start within seconds and are not affected.
- A late `started` (the job was already released as never started) is answered with `conflict`, and the node stops working on that job.
- Parameters: `TicketTTL`, `StartGrace`, `MaxStaleTickets`, `StaleAfter` in `coordinator.Config` (`jobs.*` in the configuration file).

What remains: a malicious buyer can still make a node wait up to 1.5 minutes per ticket, but the number of such tickets per account is capped, and accounts are registered at the portal.

### 2.8 The portal defaults to HTTPS

- When `portal serve` listens on a non-loopback address it uses **HTTPS by default**. With `-tls-cert`/`-tls-key` (for example a Let's Encrypt certificate) it uses that certificate. Otherwise it generates a self-signed certificate on first start (ECDSA P-256, valid 10 years, kept in `-tls-dir`, default `portal-tls/`) and prints its fingerprint `sha256:…`.
- Plain HTTP is used only on a loopback address (local demos), with `-behind-tls-proxy` (a reverse proxy terminates TLS), or with an explicit `-insecure-http`. The same choices are available as `tls.mode` in the configuration file.
- On the client side:
  - A CA-issued certificate is verified the usual way; no pinning is needed.
  - For a self-signed certificate, `login`/`register -portal-pin sha256:…` must match the server's certificate, or the user confirms the operator's published fingerprint interactively (like a first SSH connection). The fingerprint is stored in `config.json`, and from then on only that certificate is accepted.
  - If the fingerprint does not match, the client refuses and warns of a possible man-in-the-middle attack.
  - The client refuses to send a password over plain HTTP to another machine (`-allow-http` is for test networks only).
- Keep the self-signed certificate: a new certificate means a new fingerprint, and every user has to confirm it again.

### 2.9 Relay admission: short-lived passes signed by the portal

A reservation is a relay's scarce resource (it is what makes a peer reachable through the relay), so it is given only to devices of registered users. The relay still never talks to the portal or the database:

1. The portal holds an Ed25519 signing key (`portal-pass.key`; `portal pass-key` prints the public key). The user client calls `POST /api/v1/relay-pass {peer}` with its token. The portal signs `{peer, account, issued at, expires at, client}` only for a device **bound to that user**, and only if the user is not disabled. A pass is valid for 1 hour by default (`-pass-ttl`). `client` is the version, OS and architecture the client reports with the request (`X-ST-Client-Version`, `X-ST-Client-Platform: darwin/arm64`); the portal checks only their form.
2. Before each reservation or renewal the client presents the pass to the relay on `/sharetoken/relay-pass/1.0.0` (`p2p.Reserver.Prepare`). A pass with less than 15 minutes left is replaced automatically; while the portal is unreachable the client keeps using its unexpired pass.
3. The relay verifies the pass offline against the portal public keys in its configuration: the signature is valid, the pass has not expired, and the peer named in it is the libp2p-authenticated remote peer. It then admits that device's reservations through the ACL (`relayv2.WithACL`) until the pass expires.
4. The number of devices of one user account holding reservations at the same time is capped (`max_devices_per_user`, default 4). This is more precise than an IP limit and does not penalize the many users behind one carrier NAT.
5. The coordinator is not a user; the relay admits it through `allow_peers` (`coordinator id` prints the ID).

A relay can serve several independent networks. Each entry of `access.networks` in the relay's configuration file names one network with its own portal keys, coordinator and limits (see [configuration.md](configuration.md)).

Properties:

- **Disabling a user takes effect within an hour at the latest.** The portal stops issuing passes, and the next renewal is refused by the relay. No relay needs to be told.
- **A stolen pass is useless.** It names a peer ID; without that device's private key the holder cannot pass libp2p authentication. It also expires soon.
- **Only reservations are gated, not connections through the relay.** The only peers that can be reached are registered users and coordinators, and each authenticates whoever reaches it (a coordinator refuses unbound devices, a node accepts only tickets). The auditor's temporary identities are therefore unaffected.
- **Key rotation.** `portal_keys` takes several keys; keep the old and the new one side by side for longer than one pass lifetime.
- A relay with no `access.networks` is open and prints a warning. A client whose relay does not accept the pass simply attempts the reservation; an open relay grants it.
- **Format.** On the wire a pass is `{body, sig}`. `body` is the claims JSON as the portal wrote it (carried as base64, so no layer can change its bytes), and the signature covers `canon("sharetoken/relay-pass/v1", body)`. A relay parses the fields it knows and ignores the rest, so the portal can add fields without a new version and without upgrading relays first. The version changes only when a field relays rely on changes meaning; relays must then accept both versions before the portal starts signing the new one.
- **`client` is for statistics only.** The signature proves only that this is what the client said when the portal signed. A modified client can report anything, so relays never use it for admission. A relay compares it with the agent string from libp2p identify (`sharetoken-client/1.0.0 (darwin/arm64)`) and counts disagreements in `network_client_mismatches`.

### 2.10 IPv6 and dual stack

- **Dual-stack listening by default.** The client listens on `/ip4/0.0.0.0` and `/ip6/::` (TCP and QUIC), and so does the relay. On a machine without IPv6 the IPv6 listeners fail and are skipped (libp2p starts as long as one listener succeeds). An explicit `-listen` is used as given.
- **Why the client listens on IPv6 too.** Residential IPv6 addresses are usually public and not behind NAT. When a seller advertises an IPv6 address, a buyer that also has IPv6 can connect directly and uses no relay bandwidth. A machine that can only dial out has to go through a relay.
- **One relay, one entry, several addresses.** A relay's address can be several multiaddrs of the same peer ID joined by commas (`/ip4/…/p2p/X,/ip6/…/p2p/X`). `p2p.ParseRelay` merges them into one `AddrInfo`; measurement, reservation and circuit addresses cover all of them, and each user uses the one it can reach. The portal registers one entry per peer ID and normalizes it. At start-up the relay prints a `portal relay add '…'` line ready to paste (announced DNS names first, otherwise public addresses per address family) and notes when it has no IPv6 address.
- **A dual-stack relay is a bridge.** An IPv4-only user and an IPv6-only user can trade through the same dual-stack relay. To the relay each side is an ordinary connection, and Noise end-to-end encryption is unchanged.

### 2.11 The local API

The running client serves the OpenAI-compatible endpoint (`/v1`) and the management API (`/api/v1`, see [guide.md](guide.md)) on one loopback address, `127.0.0.1:9787` in release builds. The CLI commands `status`, `models`, `relays` and `share` use this API, and so does the desktop app ([desktop.md](desktop.md)), which runs `sharetoken daemon`: the same service, except that it also runs with nobody logged in and handles sign-up and login through its API.

Safeguards:

- It binds to loopback. The client writes its actual address to `run.json` in its state directory, and the CLI finds the running client from there.
- **Cross-origin requests from web pages are refused.** A request with an `Origin` that is not localhost, a loopback address or an application scheme (`tauri://`, `wails://`) gets 403 (`client.guardOrigin`), so that no web page can spend the user's credits through the user's browser. On a loopback address the `Host` header is checked as well, against DNS rebinding.
- `config.json` (which holds the portal token) and the device key `device.key` have mode 0600. See [configuration.md](configuration.md) for the state directory.

### 2.12 Security model of the portal's web pages

The portal serves its pages from the binary: the public site (`/`), the user page (`/app/`) and the admin console (`/admin/`). They share `/api/v1`.

| Threat | Defence |
|---|---|
| Session stolen by a script in the page (XSS) | The session token lives only in an **HttpOnly** cookie that scripts cannot read. The cookie is `SameSite=Strict`, and `Secure` over HTTPS |
| Cross-site request forgery, including login CSRF | The cookie counts only when the request also carries the custom header `X-Sharetoken-Web: 1`. When the browser sends `Origin` or `Sec-Fetch-Site`, it must be the portal itself. The portal does not answer CORS preflights, so another site cannot send such a request. The login and sign-up endpoints are covered too. Bearer authentication does not use cookies, so the CLI is unaffected |
| Script injection | A strict CSP: `default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; font-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'`. The pages have no inline script or style and no third-party resources, and build the DOM with `textContent` rather than HTML strings. Also `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY` |
| Credential stuffing, mass sign-up | Failed logins are throttled (8 per 5 minutes for one username or one IP). Sign-ups are throttled per IP the same way. A wrong current password when changing the password counts too |
| Administrator mistakes | An administrator cannot disable themselves or revoke their own admin rights. Destructive actions (disable, revoke, refund) ask for confirmation in the page. Every admin action is logged (who did what to whom) |
| Long-lived sessions | A browser session lasts 24 hours by default (`-session-ttl`), is visible in the token list and can be revoked. Changing the password can revoke all other sessions; an administrator resetting a password can revoke all of that user's tokens |
| Portal behind a reverse proxy | With `-behind-tls-proxy`, or when the proxy sends `X-Forwarded-Proto: https`, cookies are still marked Secure and the Origin check compares against https |

**Dispute resolution** (`POST /api/v1/admin/disputes/{job}/resolve`) is settled by the portal process directly in the shared database (`coordinator.ResolveDispute`). It is the same code as the coordinator's own finalize and refund. The **serving** coordinator writes its fee rate to `meta.fee_bps` at start-up for the portal to read (`Engine.RecordFeeRate`; operator subcommands do not overwrite it). The row lock on `st_write_lock` keeps the two processes' write transactions serial.

**Two-factor authentication for administrators.** An administrator's password yields only a challenge valid for 5 minutes, never a token. An enrolled administrator then submits a TOTP code (`POST /api/v1/web/mfa` or `/api/v1/mfa`; API clients can also send `totp` in the login request). An administrator who is not enrolled first enrolls in the web page: the server generates a 20-byte secret, shows it as a QR code and as base32 text, and stores it only after the first code is correct. So even at the first login there is no admin session without the second factor. Verification accepts one period either side of now and records the last accepted step, so a code cannot be replayed. Wrong codes count towards the login throttle, and a challenge is discarded after 5 wrong codes. `api_tokens.mfa` records whether a token came from a two-factor login, and the `admin` middleware requires it: a token issued before the user became an administrator has no admin rights, and the user must log in again. The secret is stored in `users.mfa_secret` encrypted with AES-GCM under a key derived from the portal's signing key (`portal-pass.key`), or under `keys.mfa_key_file` when that is set, so a copy of the database does not reveal the authenticator secrets. A portal process without that key (an operator command, for instance) cannot log an administrator in, but `portal mfa-reset` does not need it. Ordinary users are not required to use two-factor authentication.

### 2.13 Pairing: a buyer bound to its sellers

A seller can keep a group of buyers to itself, and a buyer can limit whom its prompts go to, without a separate network. The seller makes a pairing code (`internal/accounts/pairing.go`): 80 random bits written as 16 base32 characters, valid for a day by default and at most 90 days, for any number of buyers or a set number. A buyer redeems it at the portal and the two accounts are paired. One seller can have many buyers (up to 1000) and one buyer several sellers (up to 20). Either side can end a pairing, and revoking a code leaves the pairings made with it.

The coordinator enforces it, not the client. When it issues a ticket or lists models for a peer, it looks up the sellers the peer's account is paired with; if there are any, every node of another account is left out, also when the request names a node. A buyer with no pairing is matched as before, and a seller keeps serving buyers that are not paired with it. Pairings are read from the database on each request, so a change takes effect on the next request.

Pairing is between accounts: every device of the buyer is bound by it and every device of the seller serves it. The portal shows each side only a masked form of the other's account name, because a code can travel further than its seller intended. Wrong codes are throttled like failed logins.

## 3. Database

The portal, the coordinator and the mail server share one MySQL 8 (InnoDB) database (`internal/mysqldb`). It is named with `-db mysql://user@host:port/db` (`database.dsn`) and can run on a separate machine.

| Tables | Written by | Read by |
|---|---|---|
| `users`, `api_tokens`, `devices` | Portal | Coordinator (`devices`, to resolve accounts), portal |
| `pairing_codes`, `pairings` | Portal | Coordinator (`pairings`, to match), portal |
| `relays` | Portal (registry), coordinator (reachability) | Portal, coordinator |
| `ledger_accounts`, `ledger_holds`, `ledger_entries` | Portal (grants and deductions), coordinator (holds, settlements, refunds) | Both |
| `jobs`, `nodes`, `audits`, `peers`, `meta` | Coordinator | Portal (billing, usage, coordinator ID) |
| `portal_settings`, `admin_audit_log`, `password_resets`, `invite_codes`, `relay_samples`, `feedback` | Portal | Portal |
| `mail_outbox`, `mail_inbox`, `mail_codes`, `mail_status` | Portal, mail server | Portal, mail server ([mail.md](mail.md)) |
| `schema_migrations` | Whichever process applies a migration | All processes |

- **Migrations.** The schema is a set of numbered migrations in three components (`core`, `portal`, `mail`). Whichever process starts first applies them and the others wait. The rules are in [versioning.md](versioning.md).
- **Single writer.** Every write transaction goes through `mysqldb.Begin`, which first locks the only row of `st_write_lock`. Writes of the portal and the coordinator are therefore strictly serial across processes, which makes read-then-write code inside a transaction correct. Read-only transactions (the portal's `ledgerRead`) do not take the lock.
- **Upsert.** "UPDATE first, INSERT if nothing matched". Connections set `CLIENT_FOUND_ROWS`, so RowsAffected counts matched rows.
- **Collation.** `utf8mb4_bin`: strings compare byte by byte.

## 4. Threats and defences

The full threat model and the guarantees the system makes are in [security.md](security.md). The table below maps each threat to where the defence lives in the code.

| Threat | Defence | Code |
|---|---|---|
| A relay reads the control plane or inference content | Control and data plane are both Noise end to end; inference content is additionally sealed with box | `internal/p2p`, `internal/box` |
| A relay goes down | Users switch relays automatically; sellers hold reservations on several relays; the coordinator reserves on all of them | `client.Agent.maintain`, `p2p.Reserver` |
| Someone connects to the coordinator directly, bypassing the relays | The coordinator listens on no address | `cmd/coordinator` (`DialOnly`) |
| Binding someone else's node to your account | Binding needs the device's signature within a time window; a bound device cannot be taken over | `accounts.VerifyBind`, `accounts.Store.BindDevice` |
| One user trading with themselves across devices | Matching excludes by account | `coordinator.Engine.IssueTicket` |
| A stolen token | Tokens can be revoked at any time; login tokens expire after 30 days; only their SHA-256 is stored | `internal/accounts`, `internal/portal` |
| Password brute force | argon2id; 8 failures within 5 minutes for one username or one IP give 429 | `portal.Server.throttled` |
| A malicious web page calls the local endpoint | Origin check | `client.guardOrigin` |
| Taking tickets without connecting, to fill a node's slots | A job that never started is released 30 seconds after its ticket expires; an account with 5 stale tickets gets no more | `coordinator.Engine.MarkStarted`, `coordinator.Engine.Tick` |
| Strangers occupying a relay's reservation slots | A reservation needs a short-lived pass signed by the portal; devices are limited per user | `internal/relaypass`, `internal/relay/admission.go` |
| Eavesdropping on or tampering with portal traffic (passwords, tokens) | HTTPS by default; the client pins a self-signed certificate's fingerprint; refuses to send secrets in plaintext to a remote host | `internal/portal/tls.go`, `client.NewPortalClient` |
| A disabled user keeps trading | The coordinator checks the user's state on every request; tokens stop working at once | `accounts.DeviceAccount` |
| Web session theft, cross-site request forgery, script injection | HttpOnly + SameSite cookie; custom header + Origin check; strict CSP, no inline code | `internal/portal/web.go` (§2.12) |
| An administrator's password is leaked or guessed | TOTP is mandatory for administrators; a token without the second factor cannot reach `/admin/*`; wrong codes are throttled, challenges have a try limit, codes cannot be replayed; secrets are stored encrypted | `internal/portal/mfa.go`, `internal/totp` (§2.12) |
| A stranger exhausts a relay's or the coordinator's memory with huge frames or floods of streams | A frame's buffer grows with the data that actually arrives, not with the announced length. Stream handlers first reserve the frame size with the libp2p resource manager and refuse the frame when it exceeds the per-stream (16 MiB), per-peer or system (a quarter of RAM) limit. Frames that hold only a few fields (passes, acks) are capped at 64 KiB. Hub processes raise only the counts that grow with the number of users (system-wide connections, streams and descriptors; the circuit, ping and other bundled services) and keep libp2p's machine-scaled defaults for everything else. User hosts allow 8 concurrent pings per peer instead of 3, so that overlapping measurements and link monitoring do not make a relay look unreachable | `protocol.RecvStream`, `p2p.HubLimits`, `p2p.ClientLimits` |

## 5. Honest limits

- **Connections through a relay are not gated.** Anyone can connect through a relay to a peer that holds a reservation. The peer authenticates and refuses them, but relay bandwidth is spent. A relay's traffic is bounded only by its overall limits.
- **Taking a ticket and not connecting still has a short cost.** Each such ticket can keep a node waiting for up to 1.5 minutes; the cap on stale tickets per account bounds how many.
- **A self-signed certificate depends on the first confirmation.** The user has to compare the fingerprint with the one the operator published. With a domain name a CA certificate is the better choice. There is no built-in ACME client: point `-tls-cert` at a certificate from certbot, or put the portal behind Caddy or nginx.
- **The coordinator is a single instance.** The ledger is kept by it and the portal.
- **Usage statistics cover only jobs within the retention period** (30 days by default). The history of money is kept in the ledger permanently.
- **Audits are paid in advance from the platform fee account.** While an audit is running that account can be negative; the unused balance returns when it ends.
- Without a TEE the operator of a node can see prompts. Audits are probabilistic.
