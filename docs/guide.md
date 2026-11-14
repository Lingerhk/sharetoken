# ShareToken guide: using and operating the network

**English** | [简体中文](zh-CN/guide.md)

This guide covers the command-line client, how to run your own deployment binary by binary, and the two HTTP APIs (the client's local API and the portal's API).

- How the pieces fit together: [architecture.md](architecture.md). What the design does and does not protect: [security.md](security.md).
- Every setting of the server programs: [configuration.md](configuration.md).
- A deployment with Docker Compose: [self-hosting.md](self-hosting.md). Building and testing: [development.md](development.md).
- The guide for end users of the hosted service is at <https://sharetoken.app/docs/>.

Amounts: 1 credit = 1,000,000 micro-credits. Prices are micro-credits per million tokens.

## 1. Users

### 1.1 Getting started

```bash
# First time: sign in with a code mailed to you; a new address gets an account.
# This device is bound to the account.
sharetoken login -email you@example.com

# Buy only: start the client.
sharetoken up
#   OpenAI-compatible endpoint: http://127.0.0.1:9787/v1

# Earn credits: share a model of the local model server (default: Ollama at http://127.0.0.1:11434/v1).
sharetoken share detect                     # list the local models
sharetoken share add qwen2.5:7b             # same model ID on the network; -id renames it, -in/-out set the prices

# Or share a hosted API. Several providers can be configured and switched on and off one by one.
sharetoken provider add deepseek            # with an API key: anthropic, openai, gemini, grok, deepseek, custom (-url)
sharetoken provider login openai            # with an OAuth login: openai, gemini, antigravity, grok
sharetoken share add deepseek-flash -provider deepseek

sharetoken up                               # share commands take effect on a running client at once
```

Point any OpenAI-compatible tool at `http://127.0.0.1:9787/v1`, with the client's API key: `sharetoken apikey -q` prints it (`sharetoken apikey` for more; `apikey off` lets any request in, `apikey rotate` makes a new key).

There are no passwords. `login` mails a six-digit code and asks for it; the same step signs up an address that has no account yet (when the portal's registration is open). For scripts, `login -send` only mails the code and `login -code 123456` redeems it. With two-factor authentication on, `login` then asks for the authenticator code (or pass `-totp`).

### 1.2 Which portal the client uses

The portal address is built into the client; there is no `-portal` flag.

| Build | Portal | State directory | Local API |
|---|---|---|---|
| prod | `https://sharetoken.app`, the hosted service. `SHARETOKEN_PORTAL` is ignored. | `~/.sharetoken` | `127.0.0.1:9787` |
| dev (the default when the build type is not set) | The address in `SHARETOKEN_PORTAL`. Always set it: the built-in default is the project's own test network. | `~/.sharetoken-dev` | `127.0.0.1:9887` |

To use your own deployment, run a dev build with `SHARETOKEN_PORTAL=https://portal.example.com` set when you sign in; the address is then kept in `config.json`. Build types are described in [desktop.md](desktop.md).

Before it sends anything, the client decides how to trust the portal:

- `https://` with a certificate from a public CA: nothing more is needed.
- `https://` with a self-signed certificate: pass the fingerprint the operator published, `-portal-pin sha256:…`. Without the flag, the client shows the fingerprint and asks you to confirm it. The pin is saved in the configuration.
- `http://` to another machine: refused unless you pass `-allow-http`. Use that only on a test network.

### 1.3 Commands

| Command | What it does |
|---|---|
| `login [-email ADDR] [-send] [-code CODE] [-invite CODE] [-totp CODE]` | Sign in with a mailed code (an address without an account gets one) and bind this device. `-send` only mails the code; `-code` redeems a code already received. Also takes `-device NAME`, `-portal-pin sha256:…` and `-allow-http`. |
| `up [-http ADDR] [-listen MADDRS] [-nat=false]` | Run the client: buy, and share if sharing is on. The flags apply to this run only and are not saved. |
| `daemon` | The service behind the desktop app: like `up`, but it also runs while logged out. See [desktop.md](desktop.md). |
| `status` | Connection to the coordinator and the relay in use, the live latency, jitter and loss of that relay (the `link:` line), every relay's round-trip time and when it was measured, sharing, warnings. |
| `models` | Models on the network: nodes, nodes in a TEE, lowest prices. |
| `relays [-measure]` | Relay measurements and the live quality of the current relay. `-measure` measures again now. |
| `balance` | Available and held credits; jobs and tokens bought and sold. |
| `jobs [-role buy\|sell] [-n 20]` | Recent jobs. |
| `share` | Show the sharing settings. |
| `share detect [-upstream URL]` | List the models of the local model server. |
| `share add <model> [-provider NAME] [-id ID] [-in P] [-out P] [-max-output N]` | Share a model of the local model server, or of a provider. |
| `share rm <id>`, `share on`, `share off`, `share upstream <url>` | Stop sharing a model, switch sharing on or off, set the local model server. |
| `provider` | The kinds of provider that can be added, and the ones configured. |
| `provider add <kind> [-name N] [-url URL] [-header K=V] [-no-check]` | Add a hosted API with an API key. The key is read from a prompt, standard input, or `$SHARETOKEN_PROVIDER_KEY`; it is never a flag. |
| `provider login <kind> [-name N] [-project ID]` | Add one with an OAuth login: open the printed address, sign in, and paste the authorization code or the callback address back. |
| `provider models <name>`, `provider test <name> [-model M] [-prompt TEXT]` | List the models the credentials may use; send one small request through the provider. |
| `provider on <name>`, `provider off <name>`, `provider rm <name>` | Sell its models, stop, or remove it with its credentials. |
| `pair` | Who this account is paired with, and its pairing codes. |
| `pair code [-label L] [-ttl 24h] [-uses N]` | Seller: make a pairing code to hand to buyers. It lasts 24 hours unless `-ttl` says otherwise (at most 30 days) and admits any number of buyers unless `-uses` limits it. |
| `pair join <code>` | Buyer: pair with the seller that made the code. From then on the account buys only from the sellers it is paired with. |
| `pair leave <seller id>`, `pair rm <buyer id>`, `pair revoke <code id>` | End a pairing from the buyer's side or the seller's side; stop a code from admitting more buyers. |
| `pair private on\|off` | Seller: private mode. While it is on, only the buyers paired with this account are matched to it and see its models. |
| `partner` | Share partners: the partners, the tokens given to and used from each in the last 24 hours, open invites and the settings. |
| `partner invite [-ttl 24h]` | Make a one-person invite (at most 168 hours). Whoever joins with it becomes a share partner: each uses the other's nodes first, for free. |
| `partner join <code>` | Become the share partner of the user that made the invite. |
| `partner rm <user id>`, `partner revoke <invite id>` | Stop sharing with a partner; withdraw an open invite. |
| `partner set [-only on\|off] [-limit N] [-share PCT]` | `-only on`: use partners' nodes only, never buy on the network. `-limit`: tokens this account's nodes give all partners per 24 hours (0: no limit). `-share`: percent of each node's concurrent jobs partners may take (0 pauses sharing). |
| `skill [install\|uninstall\|show] [-agent claude\|codex\|all] [-force]` | The skill that teaches coding agents to use the client: `install` writes `skills/sharetoken/SKILL.md` under `~/.claude` (or `$CLAUDE_CONFIG_DIR`) and `~/.codex` (or `$CODEX_HOME`), by default for every agent found; the copy names this build's command, state directory and endpoint. Without a subcommand it shows which agents have it. A `SKILL.md` the client did not write is kept unless `-force`. The text is [`internal/agentskill/SKILL.md`](../internal/agentskill/SKILL.md). |
| `logout` | Revoke this device's token. A running client stops. |
| `config` | Show the configuration (the token is masked). |
| `version [--check]` | What this client was built for; `--check` asks the portal for a newer release. |

Notes:

- `-home DIR` before the command, or `$SHARETOKEN_HOME`, selects another state directory.
- A client's sign-in lasts 60 days (the portal's `sessions.login_ttl`).
- By default the client listens on IPv4 and IPv6, TCP and QUIC (`/ip4/0.0.0.0`, `/ip6/::`, port chosen by the system). A machine without IPv6 skips those listeners.
- No port needs to be opened. A seller holds reservations on relays; buyers reach it through a relay or by hole punching.
- Seller mode for hosted APIs is described in [providers.md](providers.md).
- Pairing is between accounts, not devices. A buyer may be paired with up to 3 sellers and a seller with up to 1000 buyers. A buyer with at least one pairing is matched only to its paired sellers and `models` lists only what they sell; a buyer with none buys from anyone. Sellers keep serving buyers that are not paired, unless they turn private mode on. Each side sees the other's account name masked (`al***@example.com`). See [architecture.md](architecture.md), section 2.13.
- Share partners are a separate, free way to share: a request first goes to a partner's node, at no cost to either side, and only when none can take it to the network, paid (or nowhere, with `partner set -only on`). An account may have up to 5 partners; an invite admits one person. Partners are used whatever the account's pairings. See [architecture.md](architecture.md), section 2.14.

### 1.4 Files in the state directory

| File | Content |
|---|---|
| `config.json` | Settings and the portal token (mode 0600). |
| `device.key` | The device's private key (mode 0600). Another machine is another device. |
| `providers.json` | Hosted-API providers and their credentials, kept apart from `config.json`. |
| `network.json` | The last relay list from the portal, used when the portal cannot be reached. |
| `run.json` | The API address of the running client and its key, for the other commands and the desktop app (mode 0600). |
| `apikey.json` | The OpenAI endpoint's API key and whether it is required (mode 0600; `sharetoken apikey`). |

### 1.5 config.json

```json
{
  "version": 1,
  "env": "dev",
  "portal": "https://portal.example.com",
  "portal_pin": "sha256:…",
  "user": "alice",
  "token": "st_…",
  "device_name": "laptop",
  "http": "127.0.0.1:9887",
  "nat": true,
  "relays": [],
  "buy": {
    "budget_micros": 200000,
    "max_price_in": 0,
    "max_price_out": 0,
    "evidence": true,
    "require_tee": false,
    "tee_policy": "",
    "queue_seconds": 30,
    "first_byte_seconds": 120,
    "idle_seconds": 60
  },
  "share": {
    "enabled": true,
    "upstream": "http://127.0.0.1:11434/v1",
    "concurrency": 2,
    "relays": 2,
    "models": [
      {"id": "qwen2.5:7b", "upstream_model": "qwen2.5:7b", "price_in": 100000, "price_out": 200000}
    ],
    "tee": "off"
  }
}
```

| Field | Meaning |
|---|---|
| `version`, `env` | Format of the file and the build type that wrote it. A client of the other build type refuses the file. |
| `portal`, `portal_pin` | Portal address; the pin is present only for a self-signed portal. |
| `user`, `token`, `device_name` | The login. `token` is a secret. |
| `http` | Address of the local API and the OpenAI endpoint. Keep it on loopback. |
| `listen` | libp2p listen addresses (omitted: the default described above). |
| `nat` | UPnP/NAT-PMP and hole punching. |
| `relays` | When not empty, replaces the relay list the portal gives. |
| `buy.budget_micros` | Credits reserved per request; the unused part is released. |
| `buy.max_price_in`, `buy.max_price_out` | Highest acceptable prices; 0 means no limit. |
| `buy.evidence` | Disclose the key of a provably bad job so that it is refunded automatically. |
| `buy.require_tee`, `buy.tee_policy`, `buy.tee_online` | Only buy from nodes in a TEE whose image is on the allow-list file `tee_policy`; `tee_online` also checks revocations with the vendor. |
| `buy.queue_seconds`, `buy.first_byte_seconds`, `buy.idle_seconds` | Waiting limits, see [section 3](#3-local-management-api). Defaults 30, 120 and 60. |
| `share.enabled` | Sharing is on. |
| `share.upstream`, `share.upstream_key_env` | OpenAI-compatible base URL of the local model server, and the environment variable holding its API key, if it needs one. |
| `share.concurrency` | Jobs served at once. |
| `share.relays` | How many relays to hold reservations on. |
| `share.models[]` | `id` (on the network), `upstream_model` (on the server), `price_in`, `price_out`, and optionally `max_output` (the model's own limit of output tokens per request). |
| `share.tee` | `off`, `auto`, `sev-snp` or `tdx` (confidential virtual machines). |

## 2. Operators

### 2.1 What a deployment consists of

The smallest deployment is one public machine running the portal, the coordinator, MySQL 8 and one relay. Add relays in other network locations as needed.

| Program | Role | Listens on |
|---|---|---|
| `relay` | Forwards encrypted traffic between users, and between users and the coordinator. Keeps no accounts, sees no plaintext. | A public TCP and UDP port (default 4001). |
| `portal` | Users, devices, credits, the web pages and the HTTP API. | HTTPS (default `:8092`). |
| `coordinator` | Matching, tickets, settlement, disputes, audits. Shares the database with the portal. | Nothing: it is reached only through the relays. |
| `mailserver` | Optional: verification codes, notices, a support mailbox. See [mail.md](mail.md). | Port 25, if it receives mail. |

Build all of them with `go build -o bin/ ./cmd/...`.

This section sets the programs up by hand. For Docker Compose see [self-hosting.md](self-hosting.md). Each server program reads a YAML file given with `-config` (examples in [`configs/`](../configs/)); flags given explicitly override the file. The examples below use flags where that is shorter. All settings are listed in [configuration.md](configuration.md).

### 2.2 MySQL

The database is MySQL 8 (InnoDB) only. `-db` (or `database.dsn`) is `mysql://user@host:port/database`. Put the password in the environment variable `SHARETOKEN_DB_PASSWORD`, or name a file with `database.password_file`; do not write it into the URL, where it would show in the process list. Other query parameters of the URL are passed to the server as session variables.

```sql
CREATE DATABASE sharetoken CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;
CREATE USER 'sharetoken'@'10.0.0.%' IDENTIFIED BY '…';
GRANT ALL ON sharetoken.* TO 'sharetoken'@'10.0.0.%';
```

- The portal and the coordinator create and upgrade the tables when they start, in either order. Their account needs the right to create and alter tables (`ALL ON sharetoken.*`). Schema versions are described in [versioning.md](versioning.md).
- Recommended server settings: `transaction_isolation = READ-COMMITTED` (the programs also set it on every session); `skip_name_resolve = ON` (otherwise every new connection can stall for seconds on reverse DNS); `innodb_flush_log_at_trx_commit = 1` (the ledger needs commits on disk); `tmp_table_size = 256M` (the ledger replay groups by account, and the 16 MB default spills to disk); an `innodb_buffer_pool_size` that holds the `jobs` table (about 6 KB of data and indexes per job). Allow port 3306 only from the machines that run the portal and the coordinator.
- Consistency: every write transaction first locks the single row of the table `st_write_lock`, so the writes of the portal and the coordinator are strictly serial. Balances are read, computed and written back, and concurrent writers would lose updates. Read-only queries are not affected. A writer waits at most 10 seconds for the lock; after that the database counts as busy (the portal answers 503 `busy`; at start both programs retry for a while).
- Tests: `scripts/test/test.sh` starts a MySQL 8 in Docker and runs `go test` against it. To use an existing server, set `SHARETOKEN_TEST_MYSQL=mysql://user@host:3306/`; each test creates a database named `sttest_<time>_<random>`, and later runs delete those older than an hour. More in [development.md](development.md).

### 2.3 Starting the programs

```bash
export SHARETOKEN_DB_PASSWORD=…
DSN=mysql://sharetoken@127.0.0.1:3306/sharetoken

# Two values the relays need. Each program creates its key the first time it runs.
portal -db $DSN pass-key             # ed25519:…  the public key relay passes are verified with
coordinator -db $DSN id              # 12D3Koo…   the coordinator's peer ID
```

**Relay**, one per public machine. Admission is set in the configuration file under `access.networks`: the portal's public key and the coordinator's peer ID. One relay can serve several networks ([`configs/relay.example.yaml`](../configs/relay.example.yaml)).

```bash
relay -config relay.yaml -key relay.key \
      -listen /ip4/0.0.0.0/tcp/8091,/ip6/::/tcp/8091,/ip4/0.0.0.0/udp/8091/quic-v1,/ip6/::/udp/8091/quic-v1 \
      -announce /ip4/203.0.113.7/tcp/8091,/ip6/2001:db8::7/tcp/8091 -status 127.0.0.1:8093
```

- Without `-listen` the relay listens on port 4001, IPv4 and IPv6, TCP and QUIC.
- On a cloud host behind 1:1 NAT, state the public addresses with `-announce`, IPv4 and IPv6 both.
- The relay prints `portal relay add '<address>' [name] [region]`. Copy the address to the portal as it is. On a machine without IPv6 it notes that IPv6-only users cannot reach it.
- `-status` serves JSON statistics at `/` and Prometheus metrics at `/metrics`. It has no authentication and must be a loopback address.
- With `access.networks` set, only devices of registered users that present a pass from the portal, and the coordinator, may reserve. Without it the relay is open to anyone.
- Keep `relay.key`: the address users know includes the peer ID.

**Portal**, on the same database as the coordinator:

```bash
portal -db $DSN serve -listen :8092 -open-registration -signup-credit 5000000
portal -db $DSN useradd -admin ops@example.com
portal -db $DSN relay add '/ip4/203.0.113.7/tcp/8091/p2p/12D3Koo…,/ip6/2001:db8::7/tcp/8091/p2p/12D3Koo…' relay-1 eu-west
```

- TLS: with `-tls-cert fullchain.pem -tls-key privkey.pem` the portal uses that certificate. Without one it creates a self-signed certificate in `portal-tls/` and prints its fingerprint `sha256:…`; publish the fingerprint to your users and keep the directory, since clients pin it. Behind a TLS-terminating reverse proxy use `-behind-tls-proxy`. Plain HTTP is served only on a loopback listen address, or with `-insecure-http` (test networks only). The same choices are `tls.mode` in the configuration file.
- One relay is one entry. Several addresses of the same peer ID are joined with commas. To change the addresses of a registered relay, `relay rm` it and add it again.
- Registration and the sign-up credit given here are the starting values. Once an administrator saves the settings page in the admin console, the saved values apply.
- Without `-open-registration` only administrators create users (`portal useradd <email>`, or the admin console).
- Everyone signs in with a code mailed to their address, so the portal needs a mail sender (`mail.from`) and a running mail server ([mail.md](mail.md)); without them nobody can sign in, administrators included.

**Coordinator**. It listens on no port and reserves on every registered relay by itself:

```bash
coordinator -db $DSN -fee-bps 500 \
  -reference id=qwen2.5:7b,upstream=qwen2.5:7b,url=http://127.0.0.1:11434/v1
```

`-reference` is optional: a reference copy of a model, used for audits of the nodes that serve it and for exact token counts in disputes. `-fee-bps` is the platform fee in basis points of each settled job.

### 2.4 Operator commands

| Command | What it does |
|---|---|
| `portal useradd [-admin] <email>` | Create an account. The name is an email address: the user signs in with a code mailed there. There are no passwords. |
| `portal rename <user> <email>` | Give an account a new name, which must be an email address nobody else has: for an older account named without one (such as `root`), which cannot sign in otherwise. Balance, devices, tokens and two-factor authentication stay. |
| `portal login-code <email>` | Print a sign-in code for an existing account instead of mailing it: for someone whose mail does not arrive. It works like a mailed code (web pages and clients, once, 10 minutes) and replaces the address's earlier codes; two-factor authentication still applies (`portal mfa-reset` clears it). Nothing is mailed; the audit log records it as `user.login_code`. Whoever runs it can sign in as that user, like a password reset by an operator used to. |
| `portal grant <user> <micros> [memo]` | Grant credits. A negative amount deducts; a balance cannot go below zero. |
| `portal users`, `portal user <user>`, `portal disable\|enable <user>` | List and inspect users. Disabling takes effect at once: tokens stop working and the coordinator refuses the user's devices. |
| `portal mfa-reset <user>` | Clear a user's two-factor secret and revoke their tokens (see [section 4.2](#42-browser-sessions-and-administrator-two-factor-authentication)). |
| `portal relay add\|ls\|rm\|enable\|disable` | The relay registry. The coordinator follows changes within 15 seconds. |
| `portal overview` | Users, credits in circulation, platform fees, open holds, the result of replaying the ledger, jobs by status, reachable relays. |
| `portal version-policy [set -min V -recommended V -latest V -download-url URL -allow-dev]` | Which client versions are served ([versioning.md](versioning.md)). |
| `portal pass-key` | The public key relays verify passes with. |
| `coordinator id` | The coordinator's peer ID. |
| `coordinator disputes`, `resolve <job-id> pay\|refund`, `nodes`, `audits`, `relays`, `check`, `balance <peer-id>` | Disputes and their manual resolution, node records, audits, relay registry, a check of the books, a device's balance. |
| `portal check-config`, `coordinator check-config`, `relay -check-config` | Validate the configuration and print the effective settings. |

Operator commands work on the database directly and can run beside the live services.

### 2.5 Keys, relay admission and capacity

- Back up `portal-pass.key` and `portal-tls/`. `portal-pass.key` signs relay passes. Unless `keys.mfa_key_file` is set, the key that encrypts administrators' two-factor secrets is derived from it as well, so replacing the pass key then forces administrators to enroll again.
- To replace the pass key: add the new public key to `portal_keys` of this network on every relay, replace the portal's key, and remove the old public key an hour later (the lifetime of a pass).
- Relay admission, per entry of `access.networks`: `portal_keys`, `allow_peers` (the coordinator), `max_devices_per_user`, `max_reservations`. On the portal: `-pass-key portal-pass.key` and `serve -pass-ttl 1h`.
- Relay capacity (the defaults are far above libp2p's): `-max-reservations 4096`, `-max-reservations-per-ip 64`, `-max-circuits 4096` (concurrent circuits to one destination; the coordinator receives one per online user), `-max-conns 8192`.

### 2.6 The portal's web pages

`portal serve` also serves the web pages. They are embedded in the binary ([`internal/portal/web`](../internal/portal/web)); nothing else has to be deployed.

| Address | For | Content |
|---|---|---|
| `/` | Everyone | The landing page: what the project is, its security design, downloads. |
| `/docs/`, `/changelog/` | Everyone | The user guide and the release notes. |
| `/downloads/<file>` | Everyone | Client packages from the directory `downloads.dir`; `GET /api/v1/downloads` lists the newest of each kind, and the landing page shows what is there. Without the directory nothing is offered. |
| `/app/` (`/login` redirects here) | Users | Sign-in by mailed code (which signs up a new address); overview (balance, holds, totals bought and sold); ledger; jobs (filtered by buy or sell); API tokens (the secret is shown once) and their revocation; renaming and unbinding devices; pairing (redeem a code, leave a seller; create and revoke codes, remove a buyer, private mode); network (coordinator, relay reachability); account (turn two-factor authentication on or off); feedback. |
| `/admin/` | Administrators | Dashboard and overview (ledger replay, circulation, fees, relays, job counts, account balances); users (search, create, grant or deduct credits, disable, make or unmake administrator, reset two-factor authentication, revoke tokens, unbind devices, ledger and jobs); relays (register, edit, enable, disable, remove); client versions; all jobs (by status and user); node records; inference audits; disputes and their manual resolution (pay the node or refund the buyer); pairings (who buys only from whom, private sellers); share partners (every share group, members in full); the global ledger; mail; feedback; the operation log; settings. |

- A browser session lasts 15 days by default (`serve -session-ttl`, or the settings page). It lives in an HttpOnly cookie that page scripts cannot read. On the API tokens page it is listed as "web session", the current one is marked, and any can be revoked.
- Administrators use the same account for `/admin/`. Anyone else who opens it is told they are not an administrator.
- With a self-signed certificate the browser warns that the certificate is not trusted; compare the fingerprint with the one `portal serve` printed before continuing. Otherwise give the portal a CA certificate, or put it behind a reverse proxy such as Caddy or nginx with `-behind-tls-proxy`, so that cookies are marked Secure.
- The pages call only the portal's own `/api/v1` endpoints and load nothing from third parties, so a deployment without internet access works.
- Resolving a dispute in the console is the same as `coordinator resolve`: the portal settles it in the shared database, with the fee rate the coordinator wrote to `meta.fee_bps` when it started.
- The user-facing pages are translated ([i18n.md](i18n.md)). The theme follows the system unless the visitor chooses one; the choice is kept in the browser.
- Browser tests of the pages are in `scripts/*_ui_test.mjs`; see [development.md](development.md).

## 3. Local management API

`sharetoken up` listens on the address in `http` (`127.0.0.1:9787` for a prod build). Requests carrying an `Origin` that is not local get 403, so a web page in the user's browser cannot spend credits. Every other program and user of the machine reaches the loopback address too, so `/api/v1/` also wants the key that the running client writes to `run.json` in its state directory at each start (mode 0600), as `Authorization: Bearer <key>`; without it the answer is 401 `local_auth`. The CLI reads it from there; a script can do the same:

```bash
curl -s -H "Authorization: Bearer $(python3 -c 'import json,os;print(json.load(open(os.path.expanduser("~/.sharetoken/run.json")))["key"])')" http://127.0.0.1:9787/api/v1/status
```

The OpenAI-compatible endpoint `/v1/` wants the API key from `apikey.json` instead (unless turned off with `sharetoken apikey off`), which stays the same across starts; `GET`/`PUT /api/v1/apikey` and `POST /api/v1/apikey/rotate` show and change it. `sharetoken daemon`, used by the desktop app, serves the same API plus sign-in endpoints that work while logged out; see [desktop.md](desktop.md).

| Method and path | Description |
|---|---|
| `GET /api/v1/status` | The full state: `version`, `peer`, `user`, `portal`, `started`, `api`, `coordinator{peer,connected,relayed,via,link}`, `measured_at` (time of the last full measurement), `relays[{addr,name,region,rtt_ms,error,coordinator_ok,current,reserved}]`, `buy`, `share{…settings, providers, serving, running, stats{jobs_served,jobs_failed,earned_micros,active_jobs,announce_error}, reservations}`, `warnings[]`, `update`. `coordinator.link{relay,samples,last_ms,avg_ms,min_ms,max_ms,jitter_ms,loss_pct,last_error,updated_at}` summarizes the last 30 pings of the current relay, one every 10 seconds; it is absent before the first ping. |
| `GET /api/v1/models` | Models on the network. |
| `GET /api/v1/account` | The portal's `/api/v1/me`, passed through: balance, usage, devices. |
| `GET /api/v1/jobs?role=buy\|sell&limit=` | The portal's job list, passed through. |
| `GET /api/v1/ledger?limit=&before=` | The portal's ledger, passed through. |
| `POST /api/v1/relays/measure` | Measure the relays now and return the result. |
| `GET /api/v1/share`, `PUT /api/v1/share` | Read or replace the sharing settings. The body is the `share` object; it is validated, applied at once and saved. |
| `POST /api/v1/share/start`, `POST /api/v1/share/stop` | Switch sharing on or off. |
| `POST /api/v1/share/reload` | Load the sharing settings from `config.json` again. |
| `GET /api/v1/share/detect[?upstream=]` | List the models of the local model server. |
| `/api/v1/providers…` | Hosted-API providers: list, add, switch, remove, their models, a test request, OAuth login. The routes are listed at the top of [`internal/client/providers.go`](../internal/client/providers.go). Answers never contain a key, and any request with an `Origin` other than the API's own address gets 403. |
| `GET /api/v1/pairing`, `POST /api/v1/pairing/codes`, `DELETE /api/v1/pairing/codes/{id}`, `POST /api/v1/pairing/redeem`, `DELETE /api/v1/pairing/sellers/{id}`, `DELETE /api/v1/pairing/buyers/{id}`, `PUT /api/v1/pairing/private` | Pairing, passed on to the portal's endpoints of the same paths with the user's token ([`internal/client/pairing.go`](../internal/client/pairing.go)). |
| `GET /api/v1/sharing`, `POST /api/v1/sharing/invites`, `DELETE /api/v1/sharing/invites/{id}`, `POST /api/v1/sharing/join`, `DELETE /api/v1/sharing/partners/{id}`, `PUT /api/v1/sharing/settings` | Share partners, passed on to the portal's endpoints of the same paths ([`internal/client/sharing.go`](../internal/client/sharing.go)). |
| `POST /api/v1/login/check` | Check now whether the login is still valid (`sharetoken logout` calls it). Otherwise this is checked once a minute; when the login has ended, `up` exits and the daemon goes to the state `expired`. |
| `POST /v1/chat/completions`, `GET /v1/models` | The OpenAI-compatible endpoint. Responses carry the headers `X-Sharetoken-Job`, `X-Sharetoken-Node`, `X-Sharetoken-Verified`, `X-Sharetoken-Cost-Micros`, and `X-Sharetoken-TEE` when the node is attested. |

How `/v1/chat/completions` behaves:

- **Holds.** When the request has `max_tokens` (or `max_completion_tokens`), only its worst-case cost at the matched node's prices is held. Without it, the whole `budget_micros` is held. Set `max_tokens` for batch or highly concurrent use.
- **Queueing.** When every node is at capacity, the request waits up to `queue_seconds` (default 30). If no slot frees up it gets 503 `capacity` with a `Retry-After` header.
- **Stalled nodes.** A node that sends no first frame within `first_byte_seconds` (default 120), or nothing for `idle_seconds` (default 60) between frames, counts as stalled. A request that has delivered no output yet is retried on another node; a stream that has already started ends with an error event. Raise both values for slow models with long prompts.
- **Disconnects.** When a streaming request is cut off midway, for example because the user pressed stop, only the part that was delivered is billed.
- **Errors.** `{"error":{"message":…,"type":"sharetoken_error","code":…}}` with: `bad_request` 400 (a request the model server refuses comes back with its reason), `budget` 400, `insufficient_funds` 402, `unauthorized` and `forbidden` 403 (for example a disabled account), `upgrade_required` 426 (the client is older than the network's minimum), `no_offers` 503 (no node offers the model), `capacity` 503 (full for the moment, retry), `upstream_error` and `receipt_rejected` 502.
- **Reasoning models.** Their reasoning counts against `max_tokens`. With a small `max_tokens`, `content` can be empty and only `reasoning_content` present.

## 4. Portal HTTP API

### 4.1 Endpoints

Authentication is `Authorization: Bearer st_…`. The portal's own pages use a cookie session instead ([section 4.2](#42-browser-sessions-and-administrator-two-factor-authentication)). Errors are always `{"error":{"code":"…","message":"…"}}`.

Lists take `limit` (default 50, at most 500), `page` or `offset`, `from` and `to`, and `format=csv` for an export.

| Method and path | Auth | Description |
|---|---|---|
| `GET /healthz` | none | `{"ok":true}`. |
| `GET /api/v1/info` | none | `{registration_open, signup_credit_micros, session_hours, passes_enabled, support_email, invite_required, default_page_size, code_sign_in}`: what the sign-in page needs. `code_sign_in` is always true: people sign in with a mailed code. |
| `GET /api/v1/network` | none | `{coordinator, relays:[{addr,name,region,ok,rtt_ms}], pass_key}`. Working relays come first, the faster of them at the top. `rtt_ms` is the coordinator's round trip to the relay, omitted until it has been measured. 503 `not_ready` before the coordinator has started for the first time. |
| `GET /api/v1/version`, `GET /api/v1/update`, `GET /api/v1/downloads` | none | Server version and client version policy; whether a newer client exists for `kind`, `channel`, `os`, `arch`; the client packages offered. See [versioning.md](versioning.md). |
| `POST /api/v1/auth/code` | none | `{email, invite_code?}`: mail a sign-in code ([mail.md](mail.md)) → `{ok, new_account, cooldown_seconds, expires_in_seconds}`. For an address without an account the registration rules are checked first: 403 `registration_closed`, `email_not_allowed`, `invite_required` (ask again with `invite_code`) or `bad_invite`; a disabled account gets 403 `account_disabled`. 429 `code_cooldown` within a minute of the last code, 429 `throttled` past the limits. 503 `mail_unavailable` when the portal has no mail sender. |
| `POST /api/v1/auth/verify` | none | `{email, code, invite_code?, token_name?, totp?}` → `{user, token, created}`, 201 when the address got a new account (with the sign-up credit), else 200. The token lives `sessions.login_ttl` (60 days by default). 401 `bad_code` for a wrong or expired code; wrong codes count per IP and per address, then 429 `throttled`. An account with two-factor authentication gets 401 `mfa_required` with a `challenge` (finish with `/api/v1/mfa`), unless `totp` holds a valid code; an administrator not enrolled yet gets 401 `mfa_setup_required` (enrollment is done on the web pages). |
| `POST /api/v1/mfa` | none | `{challenge, code, token_name}` → `{user, token}`. A challenge lasts 5 minutes and is discarded after 5 wrong codes; wrong codes count as failed sign-ins. |
| `POST /api/v1/logout` | yes | Revoke the current token. |
| `GET /api/v1/me` | yes | `{user, balance{account,available_micros,held_micros}, usage{bought,sold}, devices}`. |
| `POST /api/v1/account/mfa/setup` | yes | Start turning on two-factor authentication: → `{challenge, secret, url, qr}` (base32 secret, `otpauth://` link, QR code as a data URI). 409 `mfa_enrolled` when it is already on. |
| `POST /api/v1/account/mfa/enable` | yes | `{challenge, code}`: the first code from the app turns it on → `{user}`. 400 `bad_code`, 400 `challenge_expired`. A notice is mailed. |
| `POST /api/v1/account/mfa/disable` | yes | `{code}`: a current code turns it off → `{user}`. Administrators get 403 `mfa_required`: they must keep it. A notice is mailed. |
| `GET /api/v1/tokens`, `POST /api/v1/tokens`, `DELETE /api/v1/tokens/{id}` | yes | API tokens. `POST {name, ttl_hours}` (0: no expiry) returns the secret once. |
| `GET /api/v1/devices`, `POST /api/v1/devices`, `DELETE /api/v1/devices/{peer}` | yes | Devices. `POST {peer, name, ts, sig}`, where `sig` is the device's signature over `accounts.BindMessage` ([`internal/accounts`](../internal/accounts)). |
| `PATCH /api/v1/devices/{peer}` | yes | `{name}`: rename. |
| `GET /api/v1/ledger?limit=&before=` | yes | Ledger lines: positive amounts into the account, negative out of it, holds and refunds included. |
| `POST /api/v1/relay-pass` | yes | `{peer}` → a relay pass, only for a device bound to the caller. The client calls it by itself. |
| `GET /api/v1/jobs?role=&limit=&before=` | yes | Jobs: role, model, status, token counts, cost, earnings. |
| `GET /api/v1/usage` | yes | Totals bought and sold. |
| `POST /api/v1/feedback`, `GET /api/v1/feedback` | yes | Send feedback; list one's own. |
| `GET /api/v1/pairing` | yes | The caller's pairings: `sellers[]` and `buyers[]` (`id`, masked `username`, `label`, `since`), its live `codes[]` (`id`, `code`, `label`, `max_uses`, `used`, `expires_at`) `restricted` (true when it buys only from `sellers`), `private` (true when it sells only to `buyers`) and `max_sellers`. |
| `POST /api/v1/pairing/codes`, `DELETE /api/v1/pairing/codes/{id}` | yes | Make a code (`label`, `ttl_hours` up to 720, `max_uses`; zero means one day and no limit) and revoke one. Pairings made with a revoked code stay. 409 `limit` at 20 live codes. |
| `POST /api/v1/pairing/redeem` | yes | `{code}`: pair the caller, as a buyer, with the code's seller. 404 `bad_code` (wrong, expired, revoked or used up; counted like a failed login, then 429 `throttled`), 409 `own_code`, 409 `limit`. |
| `DELETE /api/v1/pairing/sellers/{id}`, `DELETE /api/v1/pairing/buyers/{id}` | yes | End a pairing as the buyer or as the seller. |
| `PUT /api/v1/pairing/private` | yes | `{enabled}`: turn the caller's private mode on or off. |
| `GET /api/v1/admin/pairings?user=` | admin | Every pairing: buyer, seller (full names), code label, time, whether the seller is private. `user` matches part of either name. |
| `GET /api/v1/sharing` | yes | The caller's share partners: `partners[]` (`id`, masked `username`, `group_id`, `since`, and the tokens `given` and `taken` and `jobs` between the two in the last `window_hours`), open `invites[]` (`id`, `code`, `created_at`, `expires_at`), `settings`, `served` (tokens its nodes served all partners in the window, what the daily limit counts), `window_hours` (24) and `max_partners` (5). |
| `POST /api/v1/sharing/invites`, `DELETE /api/v1/sharing/invites/{id}` | yes | Make a one-person invite (`ttl_hours` up to 168; zero means one day) and withdraw one. 409 `limit` at 10 open invites or 5 partners. |
| `POST /api/v1/sharing/join` | yes | `{code}`: become the share partner of the invite's maker. 404 `bad_code` (wrong, expired, revoked or used; counted like a failed login, then 429 `throttled`), 409 `own_code`, 409 `limit`. |
| `DELETE /api/v1/sharing/partners/{id}` | yes | Stop sharing with a partner. |
| `PUT /api/v1/sharing/settings` | yes | `{only_partners, daily_limit_tokens, share_pct}`: partners only; tokens the caller's nodes give all partners per rolling 24 hours (0: no limit); percent of each node's concurrent slots partner jobs may take (0 pauses, at least one slot otherwise). |
| `GET /api/v1/admin/share-groups?user=` | admin | Every share group with its members (full names) and creation time. `user` matches part of a member's name. |
| `GET /api/v1/admin/overview`, `GET /api/v1/admin/dashboard`, `GET /api/v1/admin/relay-history?range=` | admin | Platform totals and the ledger replay; the dashboard's figures; relay history. |
| `GET /api/v1/admin/users`, `POST /api/v1/admin/users`, `GET /api/v1/admin/users/{name}` | admin | List users, create one (`{username, is_admin}`, the name an email address), one user's details (balance, usage, devices, tokens). |
| `GET /api/v1/admin/users/search?q=&limit=&offset=` | admin | Search by user name (wildcards are matched literally). Each row has balance, holds, device and token counts; the total is returned. |
| `POST /api/v1/admin/users/{name}/credit` | admin | `{micros, memo}`; negative deducts. |
| `POST /api/v1/admin/users/{name}/disable`, `…/enable` | admin | Disable or enable (not oneself). |
| `POST /api/v1/admin/users/{name}/admin` | admin | `{is_admin}` (one cannot revoke one's own rights). |
| `POST /api/v1/admin/users/{name}/mfa/reset` | admin | Clear the user's two-factor secret and revoke all their tokens: the recovery for a lost authenticator. |
| `GET /api/v1/admin/users/{name}/tokens`, `DELETE …/tokens/{id}`, `POST …/tokens/revoke` | admin | List tokens, revoke one, revoke all. |
| `DELETE /api/v1/admin/users/{name}/devices/{peer}` | admin | Unbind a device. |
| `GET /api/v1/admin/users/{name}/ledger`, `…/jobs` | admin | The user's ledger and jobs. |
| `GET /api/v1/admin/relays`, `POST /api/v1/admin/relays`, `PATCH …/{id}`, `POST …/{id}/enable`, `POST …/{id}/disable`, `DELETE …/{id}` | admin | Relay registry: add, rename or set the region, enable, disable, remove. |
| `GET /api/v1/admin/jobs?status=&user=&account=&before=&limit=` | admin | All jobs, with both accounts, dispute reason and resolution. |
| `GET /api/v1/admin/nodes` | admin | Node reputation records, with the owning user. |
| `GET /api/v1/admin/audits?limit=` | admin | Inference audits. |
| `GET /api/v1/admin/disputes?limit=` | admin | Disputes, the unresolved first. |
| `POST /api/v1/admin/disputes/{job}/resolve` | admin | `{decision: "pay"\|"refund"}`: manual resolution. |
| `GET /api/v1/admin/ledger?account=&before=&limit=` | admin | The global journal, with the user name behind each `user:<id>` account. |
| `GET /api/v1/admin/accounts` | admin | Balance and holds of every ledger account. |
| `GET /api/v1/admin/versions`, `PUT /api/v1/admin/version-policy` | admin | Versions running on the network; the client version policy. |
| `GET /api/v1/admin/settings`, `PUT /api/v1/admin/settings`, `GET/POST /api/v1/admin/invites`, `DELETE /api/v1/admin/invites/{id}` | admin | The settings page (registration, mail addresses, sessions, rate limits, audit retention) and invite codes. |
| `GET /api/v1/admin/oplog` | admin | The operation log: changes made through the admin API and by the `portal` operator commands. |
| `/api/v1/admin/mail/…`, `/api/v1/admin/feedback…` | admin | The mail section ([mail.md](mail.md)) and user feedback. |

A client the version policy no longer serves gets 426 `upgrade_required` from `auth/verify`, `mfa` and device binding; what it already has keeps working.

### 4.2 Browser sessions and two-factor authentication

| Method and path | Description |
|---|---|
| `POST /api/v1/web/auth/verify` | `{email, code, invite_code?}`, after `POST /api/v1/auth/code`: the same checks as `auth/verify`. On success a token valid for the session lifetime (`sessions.web_session_ttl`, 15 days by default) is put in the cookie `st_session` (HttpOnly, SameSite=Strict, Secure over HTTPS); the response body is `{user}` and holds no token. |
| `POST /api/v1/web/mfa`, `POST /api/v1/web/mfa/setup`, `POST /api/v1/web/mfa/confirm` | The second step of a browser sign-in with two-factor authentication: check a code; for an administrator not enrolled yet, start enrollment (returns the base32 secret, an `otpauth://` link and a QR code as a data URI) and confirm it with the first code, which starts the session. |
| `POST /api/v1/web/logout` | Revoke the session and clear the cookie. |

A request authenticated by the cookie must also carry the header `X-Sharetoken-Web: 1`, and its `Origin`, when the browser sends one, must be the portal itself; otherwise it gets 403 `cross_site`. Bearer requests are not affected. The portal answers no CORS preflight, so another site cannot build such a request.

**Two-factor authentication is mandatory for administrators and optional for everyone else**, who turns it on in the account settings (`/api/v1/account/mfa/…` above). For an account that has it, the mailed code alone never starts a session. At an administrator's first sign-in the portal asks the administrator to scan a QR code, or type the secret, into an authenticator app (Google Authenticator, Microsoft Authenticator, 1Password and the like) and to enter the first 6-digit code. Every later sign-in needs a code: TOTP as in RFC 6238, a new code every 30 seconds, each code usable once, one period of clock difference tolerated either way.

Tokens and sessions record whether they passed the second step, and every `/api/v1/admin/*` endpoint accepts only those that did. After an ordinary user is made an administrator, their existing sessions are refused with 403 `mfa_required` until they sign in again.

The secret is stored encrypted with AES-GCM under `keys.mfa_key_file`, or under a key derived from the portal's pass key when that is not set.

If someone loses the authenticator, an administrator resets it on the user's page in the console, or an operator runs the following on the server. An administrator enrolls again at the next sign-in; anyone else signs in with the mailed code alone until they turn it on again.

```bash
portal -db mysql://sharetoken@127.0.0.1:3306/sharetoken mfa-reset <user>
```

## 5. Local demo

```bash
scripts/dev/demo-local.sh     # needs Docker (for MySQL), Ollama and the model gemma4:e4b; MODEL_UPSTREAM selects another model
```

The script builds the programs and starts two relays, a portal, a coordinator and three users on 127.0.0.1: alice, an honest seller; carol, a seller who overstates her usage; and bob, a buyer. It shows sign-in by code and device binding, relay selection, the cheat being caught and refunded on evidence, the auditor checking both sellers, the client switching relays when one dies, credits moving between accounts with the books balancing, and logs that contain no prompt. Everything is stopped when the script exits.
