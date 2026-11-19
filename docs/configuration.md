# Configuration

**English** | [简体中文](zh-CN/configuration.md)

The server programs (`relay`, `coordinator`, `portal`, `mailserver`) are configured with a YAML file, command-line flags, or both. The user client keeps its settings in a JSON file in its state directory. The code is in `internal/config`; the example files are [`configs/relay.example.yaml`](../configs/relay.example.yaml), [`configs/coordinator.example.yaml`](../configs/coordinator.example.yaml), [`configs/portal.example.yaml`](../configs/portal.example.yaml) and [`configs/mail.example.yaml`](../configs/mail.example.yaml).

For the commands themselves see [guide.md](guide.md); for a complete deployment with Docker Compose see [self-hosting.md](self-hosting.md).

## 1. Conventions shared by the server programs

| Topic | Rule |
|---|---|
| Entry point | Every binary takes `-config <path>`. Without it the program runs from flags and built-in defaults only. There is no implicit search, so a YAML file that happens to be in the current directory is never picked up by accident |
| Precedence | Built-in defaults < the YAML file < flags **given explicitly** on the command line. A flag that is not given does not override the file |
| Subcommands | Operator commands such as `portal useradd …` and `coordinator balance …` read the same file (they use only `database`, `identity` and `keys`) |
| Parsing | Unknown fields are an error, which catches misspelled keys |
| Version | Top-level `version: 1`. A file with another version is refused |
| Paths | A relative path is resolved against the directory of the YAML file, not the working directory |
| Durations and sizes | Durations are written `30s`, `10m`, `720h`. Sizes are written `16384`, `16KiB`, `4MiB`, `1GiB` |
| Secrets | No plaintext secrets in the YAML file: use `password_env` / `password_file` and `api_key_env`. A secret file that other users can read produces a warning |
| Validation | The whole configuration is validated at start-up and all errors are reported at once. `coordinator -config x.yaml check-config`, `portal -config x.yaml check-config`, `mailserver -config x.yaml check-config` and `relay -config x.yaml -check-config` validate the file and print the effective settings, then exit. The output holds only environment variable names and file paths, no secrets |
| Reloading | The configuration is read once, at start-up. Restart the program to apply a change |
| Logging | `log.level` (`debug`, `info`, `warn`, `error`), `log.format` (`text`, `json`) and `log.file` (empty: stderr) are the same in every program. Defaults: `info`, `text`, stderr |

**Not in a configuration file, but in the database:** the relay list (`portal relay add`), users, devices and credits, administrators' two-factor secrets, and the portal's system settings (section 4). This is run-time data, managed by the portal and synced by the coordinator.

## 2. relay

Example: [`configs/relay.example.yaml`](../configs/relay.example.yaml). The access networks can only be set in the file.

| Key | Flag | Default | Meaning |
|---|---|---|---|
| `identity.key_file` | `-key` | `relay.key` | Identity seed, created with mode 0600 if missing. The relay's address contains the peer ID derived from it, so keep the file |
| `network.listen` | `-listen` | TCP and QUIC on IPv4 and IPv6, port 4001 | Listen multiaddrs. IPv6 listeners are skipped on a host without IPv6 |
| `network.announce` | `-announce` | empty | Public multiaddrs to advertise instead of the interface addresses: needed behind 1:1 NAT. `/dns4` and `/dns6` names are accepted; when a name is announced, the `portal relay add` hint the relay prints uses only the names |
| `network.max_conns` | `-max-conns` | 8192 | Connection-manager high watermark. The resource manager's system-wide connection and stream counts are raised to fit it |
| `access.networks` | — | empty | The networks this relay serves, one entry each. Empty means an open relay: anyone may reserve (a warning is printed) |
| `access.networks[].name` | — | — | Required, unique |
| `access.networks[].portal_keys` | — | — | Required. The network's portal pass keys (`portal pass-key`); several during a key rotation. A key cannot appear in two networks |
| `access.networks[].allow_peers` | — | empty | Peers that may reserve without a pass: the network's coordinator (`coordinator id`). A peer cannot appear in two networks. Empty produces a warning, since the coordinator then cannot reserve here |
| `access.networks[].allow_accounts` | — | empty | Makes the relay private: only devices of these accounts of the network (`user:3`) may reserve. A private relay lists no `allow_peers`. `relay init -account … -out relay.yaml` writes such a file ([security.md](security.md) §2.8) |
| `access.networks[].portal_url` | — | empty | The network's portal. The relay reads the pass key from it at start and every 6 hours, in addition to `portal_keys`, so that a relay nobody maintains survives a key rotation. https only |
| `access.networks[].max_devices_per_user` | — | 4 | Devices of one user account that may hold reservations at once. Accounts are counted within their network |
| `access.networks[].max_reservations` | — | 0 | Cap on the reservations of all devices of this network together; the coordinator does not count. 0: only `limits.max_reservations` applies |
| `limits.max_reservations` | `-max-reservations` | 4096 | Reservations held at once, all networks together |
| `limits.max_reservations_per_ip` | `-max-reservations-per-ip` | 64 | Reservations from one IP address |
| `limits.max_circuits` | `-max-circuits` | 4096 | Open circuits to one destination peer (the coordinator receives one per online user) |
| `limits.reservation_ttl` | — | 1h | Lifetime of a reservation; at least 1m |
| `limits.circuit_buffer` | — | 16KiB | Buffer per circuit |
| `limits.memory` | — | three quarters of the machine's (or the cgroup's) memory | What all peers may reserve with libp2p's resource manager. yamux reserves 256 KiB per stream and a circuit is two streams, although an idle circuit holds about 30 KiB, so on a small machine this, not the memory in use, caps TCP circuits (about 510 on 453 MB). The relay also sets a soft Go memory limit of 85% unless `GOMEMLIMIT` is set |
| `status.listen` | `-status` | empty | Serves JSON statistics at `/` and Prometheus metrics at `/metrics`. There is no authentication, so only a loopback address such as `127.0.0.1:8093` is accepted |
| `status.stats_interval` | — | 5m | Interval of the periodic statistics log line; 0 turns it off |
| `log.*` | — | info / text / stderr | See section 1 |

Two more flags have no key: `-check-config` (section 1) and `-print-id`, which prints the peer ID of the identity key (creating the key if missing) and exits.

## 3. coordinator

Example: [`configs/coordinator.example.yaml`](../configs/coordinator.example.yaml).

| Key | Flag | Default | Meaning |
|---|---|---|---|
| `identity.key_file` | `-key` | `coordinator.key` | Identity seed, created with mode 0600 if missing. Relays list the peer ID derived from it in `allow_peers`; back it up |
| `database.dsn` | `-db` | none, required | `mysql://user@host:port/db`. The same database as the portal |
| `database.password_env` | — | `SHARETOKEN_DB_PASSWORD` | Environment variable holding the database password |
| `database.password_file` | — | empty | File holding the database password |
| `database.max_open_conns` | — | 32 | Connection pool size |
| `database.max_idle_conns` | — | 8 | Idle connections kept |
| `database.conn_max_lifetime` | — | 30m | Lifetime of a pooled connection |
| `database.connect_timeout` | — | 10s | Connect timeout |
| `database.lock_wait_timeout` | — | 10s | `innodb_lock_wait_timeout` |
| `database.startup_busy_retry` | — | 2m | How long start-up retries when the database is busy |
| `network.listen` | `-listen` | empty | Listen multiaddrs. Empty: dial out only, reachable through relays |
| `network.max_conns` | `-max-conns` | 8192 | Connection-manager high watermark; every online user holds one relayed connection |
| `network.relays` | `-relay` (repeatable) | empty | Relays to reserve on besides those registered in the database, as full multiaddrs ending in `/p2p/<id>` |
| `network.relay_sync` | — | 15s | Interval of syncing the relay list and status with the database |
| `market.fee_bps` | `-fee-bps` | 500 | Platform fee in basis points of each settled job (500 = 5%); 0–10000 |
| `market.min_budget_micros` | — | 1 | Smallest hold for one ticket |
| `market.warm_up` | — | 30s | After a restart, requests with no matching offer are answered with `capacity` for this long, so buyers queue instead of failing |
| `jobs.ticket_ttl` | — | 1m | How long a node has to start a job; at least 10s |
| `jobs.start_grace` | — | 30s | How long after the ticket expires a job that never started is released |
| `jobs.reconnect_grace` | — | 2m | Release is postponed this long after a node reconnects |
| `jobs.max_stale_tickets` | — | 5 | Stale, unstarted tickets a buyer account may hold before it gets no more |
| `jobs.stale_after` | — | 15s | Age at which an unstarted ticket counts as stale |
| `jobs.settle_grace` | — | 15m | Extra time for a settlement to arrive |
| `jobs.dispute_window` | — | 24h | An unconfirmed settlement becomes final after this long |
| `jobs.max_offer_ttl` | — | 5m | Upper bound on the lifetime an offer may claim |
| `jobs.job_retention` | — | 720h | How long finished jobs are kept (the ledger is kept forever) |
| `jobs.suspend_for` | — | 24h | How long a node that failed repeated audits is left out of matching |
| `references[]` | `-reference` (repeatable) | empty | Reference copies of models: they enable audits and exact token counts in disputes. Each entry has `id` (marketplace model ID), `upstream` (model name on the server), `url` (OpenAI-compatible base URL), `api_key_env` or `api_key_file`, `verify` (how audits check answers: `vllm`, `deepseek-prefix`, `openai-div`, or `none` for the bill only; default `none`), `verify_url` (base URL audits score against; default `url`) and `params` (extra fields of audit requests, e.g. `{reasoning_effort: none}` for a model that thinks by default) |
| `audit.interval` | `-audit-interval` | 10m | Mean time between audits; 0 turns audits off |
| `audit.budget_micros` | `-audit-budget` | 200000 | Hold per question, paid from platform fees |
| `audit.prompts_file` | `-audit-prompts` | empty | Private question pool, one per line: plain text, or `{"system": …, "messages": […]}`. Empty: built-in open questions |
| `audit.max_tokens` | — | 96 | Output limit of a question |
| `audit.questions` | `-audit-questions` | 5 | Questions per audit; the verdict is on all of them together |
| `audit.spread` | `-audit-spread` | 2m | Random pause of up to this long between an audit's questions |
| `audit.score_tokens` | — | 64 | Positions scored per answer (`deepseek-prefix` makes one call per position) |
| `audit.threshold` | — | 0.85 | Mean top-1 rate below which a node serves another model, until a calibration measures one for the reference |
| `audit.margin` | — | 0.03 | `threshold + margin` and above is a match; in between, inconclusive |
| `audit.div_fails` | — | 2 | `openai-div`: failed answers that make a mismatch; none is a match |
| `audit.shadow` | `-audit-shadow` | true | Record verdicts without touching nodes' records (a receipt that fails its checks still counts) |
| `audit.calibrate_every` | — | 24h | How often each scoring reference is calibrated; 0: only by `coordinator audit calibrate` |
| `audit.calibrate_samples` | — | 40 | Questions per calibration; at least 10 |
| `audit.delivery` | — | auto | How a question reaches a node. `auto`: a user's client that could buy from the node carries it (sealed, paid by the platform), and an audit identity asks through an official relay when no such client is there; `carried`: clients only; `direct`: audit identities only |
| `audit.carry_poll` | — | 1m | How often clients ask for a question to carry (while audits run; every 10 minutes otherwise). At least 5s |
| `audit.carry_wait` | — | 3m | How long a carried question may take to come back. At least 10s |
| `audit.apis` | — | chat, messages, responses | The APIs questions are asked in, one drawn per question. A reference can narrow it with `apis` |
| `tee.policy_file` | `-tee-policy` | empty | JSON allow-list of TEE images; setting it enables `require_tee` matching |
| `tee.online_check` | `-tee-online` | false | Also check revocations and TCB status with AMD and Intel (needs network access) |
| `debug.pprof_listen` | `-debug` | empty | Serves Go pprof profiles, e.g. `127.0.0.1:6060`; keep it on loopback |
| `dev.open_accounts` | `-open-accounts` | false | Development only: peers not bound to a user trade from their own account |
| `dev.credit_micros` | `-dev-credit` | 0 | Development only: credit granted to each new peer on first use |
| `log.*` | — | info / text / stderr | See section 1 |

Notes:

- Constraints checked at start-up: `jobs.start_grace < jobs.settle_grace < jobs.dispute_window ≤ jobs.job_retention`; `0 < audit.threshold < audit.threshold + audit.margin ≤ 1`; `1 ≤ audit.div_fails ≤ audit.questions`; reference IDs are unique and `verify` is one of the four kinds.
- `-reference id=…,upstream=…,url=…[,key-env=VAR]`, when given, replaces the whole `references[]` list. `key-env` corresponds to `api_key_env`, `verify` to `verify`.
- `audit.interval: 0` turns audits off but keeps the reference models for exact token counting. This suits models served by a commercial API: their output is not deterministic, so audits would misjudge, and each audit costs money.
- Hosted APIs are not fully deterministic, so audits no longer compare how alike two texts are; they check the node's answer token by token (`docs/security.md` §2.4): DeepSeek with `deepseek-prefix`, OpenAI's non-reasoning models with `openai-div`; models that report no logprobs (Claude, Gemini) can only use `none`.
- Setting either `dev.*` option prints a warning at start-up. Never use them on a public network.

## 4. portal

Example: [`configs/portal.example.yaml`](../configs/portal.example.yaml). `-db` and `-pass-key` go before the command; the other flags belong to `portal serve`.

| Key | Flag | Default | Meaning |
|---|---|---|---|
| `database.*` | `-db` for `database.dsn` | as for the coordinator | The same keys as the coordinator's `database` section, without `startup_busy_retry` |
| `keys.pass_key_file` | `-pass-key` | `portal-pass.key` | The key that signs relay passes, created with mode 0600 if missing. Relays list its public key in `portal_keys` |
| `keys.mfa_key_file` | — | empty | A 32-byte key (64 hex characters) that encrypts administrators' TOTP secrets. Empty: derived from the pass key. With its own key, rotating the pass key does not invalidate administrators' two-factor enrollment |
| `http.listen` | `serve -listen` | `:8092` | Listen address |
| `http.trusted_proxies` | — | empty | IP addresses of reverse proxies on other machines whose `X-Real-IP` is believed (used for login throttling). A proxy on loopback is always believed |
| `http.read_header_timeout` | — | 10s | Server timeout |
| `http.read_timeout` | — | 30s | Server timeout |
| `http.write_timeout` | — | 60s | Server timeout |
| `http.shutdown_timeout` | — | 5s | Time allowed for requests in flight at shutdown |
| `tls.mode` | `serve -insecure-http` sets `insecure`; `serve -behind-tls-proxy` sets `proxy` | `auto` | See below |
| `tls.cert_file`, `tls.key_file` | `serve -tls-cert`, `-tls-key` | empty | Certificate and key, for example from Let's Encrypt. They go together |
| `tls.self_signed_dir` | `serve -tls-dir` | `portal-tls` | Where the self-signed certificate is kept. Keep it: clients pin its fingerprint |
| `tls.self_signed_hosts` | `serve -tls-hosts` | empty | Host names and IPs in the self-signed certificate |
| `accounts.open_registration` | `serve -open-registration` | false | An address without an account gets one at its first sign-in; otherwise administrators create users (`portal useradd <email>`) |
| `accounts.signup_credit_micros` | `serve -signup-credit` | 0 | Credit granted to each new user |
| `accounts.mfa_issuer` | — | empty (shown as ShareToken) | Name shown in authenticator apps |
| `mail.from` | — | empty | Sender of the portal's mail, e.g. `ShareToken <team@example.com>`. Everyone signs in with a code mailed from here, so without it (and a running `mailserver`) nobody can sign in. Empty: no mail is sent |
| `mail.support` | — | empty | Address for user feedback; administrators' replies are sent from it |
| `downloads.dir` | — | empty | Directory of client packages served at `/downloads/`. Empty: no downloads |
| `relay_metrics.*` | — | off | Reads each relay's `/metrics` over SSH for the admin dashboard: `key_file`, `known_hosts_file`, `interval` (1m), `timeout` (20s) and `targets[]` with `relay` (name in the registry), `ssh` (`user@host[:port]`) and `command` |
| `sessions.login_ttl` | `serve -login-ttl` | 1440h | Lifetime of the token a client gets at sign-in (60 days) |
| `sessions.web_session_ttl` | `serve -session-ttl` | 360h | Lifetime of browser sessions (15 days) |
| `sessions.pass_ttl` | `serve -pass-ttl` | 1h | Lifetime of relay passes; at least 5m. Disabling a user takes effect on relays within this time |
| `log.*` | — | info / text / stderr | See section 1 |

`tls.mode` values:

| Mode | Behaviour |
|---|---|
| `auto` | With `cert_file`: use it. Otherwise plain HTTP on a loopback address and a self-signed certificate on any other address |
| `files` | Use `cert_file` and `key_file` |
| `self-signed` | A self-signed certificate; clients pin its fingerprint |
| `proxy` | A reverse proxy terminates TLS in front of the portal: the portal serves plain HTTP but marks cookies Secure |
| `insecure` | Plain HTTP on a non-loopback address. Test networks only |

### System settings stored in the database

The values edited on the admin console's settings page (`/admin/#/settings`) are stored in the database (table `portal_settings`) and take effect when saved, without a restart. For `accounts.open_registration`, `accounts.signup_credit_micros`, `mail.from`, `mail.support`, `sessions.login_ttl` and `sessions.web_session_ttl`, the value in `portal.yaml` is only the default **until settings have been saved once**. After the first save the database wins and changing the file has no effect. Everything else (database, keys, listen address, TLS, downloads directory) is set only in the file.

## 5. mailserver

`mailserver` reads [`configs/mail.example.yaml`](../configs/mail.example.yaml), with the same conventions as section 1 and the same `database` section. Its keys and the portal's mail settings are explained in [mail.md](mail.md).

## 6. The user client

The client is not configured with YAML. It keeps its settings and state in one directory:

- `$SHARETOKEN_HOME` if set, or the directory given with `sharetoken -home DIR`;
- otherwise `~/.sharetoken` for release builds and `~/.sharetoken-dev` for development builds. A binary built from source without a build type is a development build.

The directory is created with mode 0700. Files in it:

| File | Mode | Contents |
|---|---|---|
| `config.json` | 0600 | Settings, and the portal API token of the logged-in user |
| `device.key` | 0600 | The device's private key: its identity on the network. A seller running in a TEE keeps its key in memory only and writes no file |
| `network.json` | 0600 | The last network information from the portal, used when the portal is unreachable |
| `run.json` | 0600 | Address and PID of the running client, so the CLI can find it; removed on exit |
| `providers.json` | 0600 | Hosted APIs the user sells through, with their keys and tokens ([providers.md](providers.md)) |
| `update.json` | 0600 | The result of the last check for a new release |
| `daemon.log` | 0600 | Log of the service started by the desktop app ([desktop.md](desktop.md)) |

`config.json` is written by the client itself (`login`, `share …`, and the local API used by the desktop app); `sharetoken config` prints it. It is written to a temporary file and renamed, so a crash does not leave half a file. Its fields:

| Field | Default | Meaning |
|---|---|---|
| `version` | 1 | Format of the file |
| `env` | the build type | Build type that wrote the file (`prod` or `dev`) |
| `portal`, `portal_pin` | — | Portal base URL, and the pinned fingerprint of a self-signed portal certificate |
| `user`, `token`, `device_name` | — | The logged-in user, the portal API token (secret), and the device's name in the portal's device list |
| `http` | `127.0.0.1:9787` (release), `127.0.0.1:9887` (development) | Address of the local API and the OpenAI-compatible endpoint; keep it on loopback |
| `listen` | TCP and QUIC on IPv4 and IPv6, any port | libp2p listen addresses |
| `nat` | true | UPnP/NAT-PMP and hole punching |
| `relays` | empty | Overrides the relays the portal lists |
| `private_relays` | empty | Relays the user runs (at most 8). A sharing node reserves on them; nothing else uses them, and traffic with the coordinator never does (`sharetoken relays add`, [security.md](security.md) §2.8) |
| `tailnet.enabled` | false | Direct tunnels over Tailscale's data plane between a buyer and a seller that both have it on (`sharetoken tailnet on`, [architecture.md](architecture.md) §2.17) |
| `tailnet.wait_ms` | 0 | How long a request to a node waits for a new tunnel before it uses the relay (at most 10000) |
| `tailnet.transport` | quic | What a buyer's connections run over inside the tunnel: `quic` or `tcp`. A sharing node serves both |
| `tailnet.derp_map` | empty | Where the DERP relays come from: a URL or a file holding a DERP map; empty for tailcat's public relays. Buyer and seller need the same |
| `buy.budget_micros` | 2000000 | Credit reserved per request; the unused part is refunded |
| `buy.max_price_in`, `buy.max_price_out` | 0 (no limit) | Price limits per million prompt and completion tokens |
| `buy.evidence` | true | Disclose a provably bad job's key for an automatic refund |
| `buy.require_tee`, `buy.tee_policy`, `buy.tee_online` | off | Buy only from attested TEE nodes; allow-list file of trusted images; online revocation check |
| `buy.spot_checks` | on | Carry the platform's audits of sellers: now and then one short, sealed job goes to a seller through this client as if it were its own. It costs no credit and the client cannot read it ([security.md](security.md) §2.4). Turned off only here: the app has no switch for it |
| `buy.require_native` | off | Buy only from nodes whose model server speaks the request's API natively ([apis.md](apis.md)) |
| `buy.loop_guard` | on | Stop paying for a model stuck in a loop: a streamed answer whose reasoning or text goes round in place (the last 256 lines and sentences all among the same 24) is cut and ended as a length stop (`finish_reason` `length`, `stop_reason` `max_tokens`, `response.incomplete`), and the node stops the model; a request whose last 8 turns are the same tool call with the same result is answered locally with a notice and sent to no node. Header `X-Sharetoken-Loop-Guard` or the stream comment `loop_guard=` says when it acted. `false` turns both off |
| `buy.queue_seconds` | 30 | How long a request waits for a free node when all are busy; negative: fail at once |
| `buy.first_byte_seconds`, `buy.idle_seconds` | 30, 30 | How long a job may wait for the node's first frame, and between frames. Nodes send a progress frame every 10 seconds while the model is silent |
| `share.enabled` | false | Sharing on or off |
| `share.upstream`, `share.upstream_key_env` | `http://127.0.0.1:11434/v1`, empty | OpenAI-compatible base URL of the local model server, and the environment variable holding its API key |
| `share.concurrency` | 2 | Concurrent jobs |
| `share.models` | empty | Models shared from the local model server |
| `share.relays` | 2 | Number of the network's relays to hold reservations on |
| `share.keep_official` | false | With private relays: hold those reservations also while a private relay works. Off, the network's relays are the fallback after 30 seconds without one |
| `share.tee` | `off` | `off`, `auto`, `sev-snp` or `tdx` |

The `up` and `daemon` flags `-http`, `-listen` and `-nat` override the file for that run and are not saved.

The client refuses a `config.json` written by the other build type, and one written by a newer client. A file of an older, unsupported format is deleted together with the device key and the network cache, and the user signs in again.

Environment variables:

| Variable | Meaning |
|---|---|
| `SHARETOKEN_HOME` | State directory |
| `SHARETOKEN_PORTAL` | Portal for a new sign-in. Ignored by release builds, so the environment cannot send sign-in codes elsewhere |
| `SHARETOKEN_PROVIDER_KEY` | API key for `provider add`, instead of a prompt |
