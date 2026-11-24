# Command reference

The format of every command:

```bash
{{exe}} [-home DIR] [-log-dir DIR] <command> [flags]
```

`-home` goes **before** the command. It selects another state directory. `-log-dir` also goes before the command: it chooses where `up` and `daemon` write their log ([Logs and crash reports](#logs)). Run with no arguments for short help.

## Account {#account}

### login {#login}

Sign in with a code mailed to you, and bind this device. An address without an account gets one.

```bash
{{exe}} login -email you@example.com
```

| Option | Meaning |
|---|---|
| `-email ADDR` | Your email address. Asked for if omitted |
| `-send` | Only mail the code and exit. Sign in later with `-code` |
| `-code CODE` | A code you already received. If omitted, one is sent and you are asked for it |
| `-invite CODE` | Invite code, when the portal needs one for a new account |
| `-totp CODE` | The authenticator code, when two-factor authentication is on. Asked for if omitted |
| `-device NAME` | Device name. Defaults to the host name |
| `-portal-pin FP` | The certificate fingerprint (`sha256:…`) the operator published, for a portal with a self-signed certificate. If omitted, the fingerprint is shown and you are asked to confirm it |
| `-allow-http` | Allow plain HTTP to a portal on another machine. For test networks only |

`-email` defaults to the address you last signed in with. `-send` and `-code` cannot be used together.

### logout {#logout}

Revoke this machine's token and remove it from the configuration file. A running client stops too.

### balance {#balance}

Show the balance, the amount on hold, buy and sell totals, and the number of devices. `account` is an alias.

### jobs {#jobs}

```bash
{{exe}} jobs -role sell -n 50
```

| Option | Meaning |
|---|---|
| `-role buy\|sell` | Show only buys or only sells. Both by default |
| `-n N` | How many to show. Default 20 |

## Run {#run}

### up {#up}

Run the client. `run` is an alias. You must be signed in.

| Option | Meaning |
|---|---|
| `-http ADDR` | Local API address. Default `127.0.0.1:{{port}}` |
| `-listen MADDRS` | libp2p listen addresses, comma-separated |
| `-nat=false` | Turn off UPnP, NAT-PMP and hole punching |

These options apply to this run only. They are not written to the configuration file.

### daemon {#daemon}

Run the service behind the desktop app, which serves the [web interface](/docs/desktop#browser-ui). Its options are the same as `up`, plus `-allow-http` (see [login](#login)). Unlike `up`, it can run while signed out, and registration and sign-in happen on its page.

### status {#status}

Show the connection, relay, sharing and direct tunnel status, and a newer release if there is one. The client must be running.

### models {#models}

List the models on the network, the node count, the TEE node count and the lowest price. The client must be running. `-codex <file>` writes them to a Codex model catalog file instead (see [Codex](/docs/use-models#codex)).

### relays {#relays}

Show the latency of each relay. `-measure` measures again right away. The client must be running.

| Command | Meaning |
|---|---|
| `relays` | Every relay, its latency, and what this client uses it for |
| `relays -measure` | Measure again first |
| `relays add <address>` | Share through a relay of your own, see [Run your own relay](/docs/relay) |
| `relays remove <address or ID>` | Stop using it |

### tailnet {#tailnet}

Direct tunnels (see [Direct tunnels](/docs/running#tailnet)). The settings can be changed while the client is stopped; a running client applies changes at once.

| Command | Meaning |
|---|---|
| `tailnet` | Whether it is on, the DERP region this machine is reachable through as a seller, and each tunnel's path and transport |
| `tailnet on` | Turn it on. `-wait <ms>`: how long a request to a new node waits for the tunnel before it uses the relay (default 0, at most 10000); `-derp-map <URL or file>`: a DERP map of your own (default: tailcat's public relays); `-transport quic\|tcp`: what this machine's connections to sellers run over inside the tunnel (default `quic`) |
| `tailnet off` | Turn it off: back to relays and hole punching |

### apikey {#apikey}

The local endpoint's API key (see [Call models](/docs/use-models#endpoint)). It works while the client is stopped too; a running client follows a change at once.

| Command | Meaning |
|---|---|
| `apikey` | The endpoint, the key, and whether it is required |
| `apikey -q` | The key only, for scripts: `export OPENAI_API_KEY=$({{exe}} apikey -q)` |
| `apikey on` / `apikey off` | Require the key (the default), or let any request in |
| `apikey rotate` | Make a new key. The old one stops working at once |

## Share {#share}

| Command | What it does |
|---|---|
| `share` | Show the sharing settings |
| `share detect [-upstream URL]` | List the models of the local model server |
| `share add <model> [-provider NAME] [-id ID] [-in P] [-out P] [-cache-read P] [-cache-write P] [-max-output N]` | Share a model and turn sharing on. Prices are micro-credits per million tokens, by default `-in 100000` and `-out 200000`; `-provider`: the model comes from a [hosted API](#providers) rather than the local model server; `-id`: the model ID on the network, by default `<model>`; `-max-output`: the model's own output limit per request |
| `share rm <id>` | Remove a model (of the local model server or a hosted API) |
| `share on`, `share off` | Turn sharing on or pause it |
| `share upstream <url>` | Set the local model server. Default `http://127.0.0.1:11434/v1` |

`<model>` must come right after `add`, and options go after it. For cache prices see [Cache prices](/docs/share#cache-prices). The settings can be changed while the client is stopped; a running client applies them at once. See [Share a model](/docs/share).

## Hosted APIs {#providers}

Share the models of a hosted API that you add with an API key or an account login. The credentials are kept in `providers.json` in the state directory, apart from the configuration file.

| Command | What it does |
|---|---|
| `provider` | The kinds that can be added, and the providers already added |
| `provider add <kind> [-name N] [-url URL] [-header K=V] [-no-check]` | Add one with an API key: `anthropic`, `openai`, `gemini`, `grok`, `deepseek`, or `custom` for any OpenAI-compatible API (its address with `-url`). The key is asked for, or read from standard input or `SHARETOKEN_PROVIDER_KEY`; never put it on the command line. `-name`: its name, by default the kind; `-header`: an extra request header, repeatable; `-no-check`: do not check the key with the provider first |
| `provider login <kind> [-name N] [-project ID]` | Add one with an account login: `anthropic`, `openai`, `gemini`, `antigravity`, `grok`. `-project`: the Google Cloud project to use when a Google account has no Code Assist project of its own |
| `provider login anthropic [-mode full\|inference] [-session-key]` | Anthropic: `-mode` is the authorization scope; `-session-key` signs in with a sessionKey cookie instead (typed without echo) |
| `provider models <name>` | The models its credentials may use |
| `provider test <name> [-model M] [-prompt TEXT]` | Send one small request through it and show the answer and token counts. By default it asks the first model it sells |
| `provider on <name>`, `provider off <name>` | Sell its models, or pause |
| `provider configure <name> [-proxy-mode environment\|direct\|custom] [-proxy URL] [-profile standard\|claude-code] [-sampling strict\|default] [-tls-profile native\|chrome] [-token-tls-profile native\|chrome]` | Proxy, Anthropic request profile and TLS settings. `add` and `login` take these options too. For a proxy address with a password, use `-proxy-mode custom` and `SHARETOKEN_PROVIDER_PROXY` |
| `provider warmup <name> …` | Warm up a subscription login's usage windows, see [Warm-up](#warmup) |
| `provider rm <name>` | Remove it and its credentials |

```bash
{{exe}} provider add deepseek
{{exe}} provider models deepseek
{{exe}} share add deepseek-flash -provider deepseek
```

See [Share a hosted API](/docs/share#providers).

## Pairing {#pairing}

A seller hands out a pairing code. A buyer that joins with it buys only from the sellers it is paired with. One code admits many buyers.

| Command | What it does |
|---|---|
| `pair` | Show who you are paired with, and your pairing codes |
| `pair code [-label L] [-ttl 24h] [-uses N] [-approve]` | Seller: make a code for your buyers, and print its invite link. It lasts 24 hours (7 days at most) and admits any number of buyers unless you say otherwise; `-approve`: each buyer waits for your approval |
| `pair join <code\|link>` | Buyer: pair with the seller that made the code or link |
| `pair approve <request id>`, `pair reject <request id>` | Seller: approve or reject a buyer waiting for your approval |
| `pair withdraw <request id>` | Buyer: withdraw your own request |
| `pair leave <seller id>` | Buyer: end the pairing with a seller |
| `pair rm <buyer id>` | Seller: remove one of your buyers |
| `pair revoke <code id>` | Seller: stop a code from admitting more buyers. Buyers already paired stay |
| `pair private on\|off` | Seller: serve only the buyers paired with you, or anyone again |

```bash
{{exe}} pair code -label team -ttl 72h
{{exe}} pair join ABC-DEF
{{exe}} pair join https://sharetoken.app/p#7kQm3xV9
```

The ids are the ones `pair` lists. See [Pair with your buyers](/docs/share#pairing).

## Share partners {#partners}

Two users who pair up as partners use each other's nodes first, for free; when no partner node can take a request, it goes to the network and is paid.

| Command | What it does |
|---|---|
| `partner` | Show your partners, the tokens you gave and used in the last 24 hours, open invites and your settings |
| `partner invite [-ttl 24h] [-uses N] [-approve]` | Make an invite for one person (or N, up to 100), and print its invite link. It lasts 24 hours unless you pass `-ttl` (168 hours at most); `-approve`: they wait for your approval |
| `partner join <code\|link>` | Become the partner of whoever made the invite or link |
| `partner approve <request id>`, `partner reject <request id>` | Approve or reject someone waiting for your approval |
| `partner withdraw <request id>` | Withdraw your own request |
| `partner rm <user id>` | Stop sharing with a partner |
| `partner revoke <invite id>` | Withdraw an open invite |
| `partner set [-only on\|off] [-limit N] [-share P]` | `-only`: use partners' nodes only; `-limit`: tokens your nodes give all partners per 24 hours (0: no limit); `-share`: percent of each of your nodes' concurrent jobs partners may take (0 pauses) |

```bash
{{exe}} partner invite
{{exe}} partner join ABC-DEF
{{exe}} partner set -limit 200000 -share 50
```

See [Share with partners](/docs/share#partners).

## Invite links {#invite-links}

Every pairing code and share invite also has an invite link, `https://sharetoken.app/p#…` (pairing) or `https://sharetoken.app/s#…` (share partners), printed when it is made. Whoever gets a link need not know which kind it is:

| Command | What it does |
|---|---|
| `accept <link\|code>` | Accept an invite link, or a code of either kind |

```bash
{{exe}} accept https://sharetoken.app/s#Hx82LpQe
```

A link can only be accepted by a client signed in to the portal it is from. See [Invite links](/docs/share#invite-links).

## Claude Code and Codex {#connect}

`connect` points Claude Code or Codex at the local endpoint. It changes the tool's own configuration file, after copying it to the `backups` directory in the state directory. If the tool is signed in to its own account (claude.ai or ChatGPT), it keeps that login by default: its model menu lists the official models, then the network's (starting with `st-`), and each request goes where its model is.

| Command | What it does |
|---|---|
| `connect` | Whether each tool is connected, where its configuration file is, and whether it is signed in to its own account |
| `connect claude\|codex [-subscription auto\|on\|off] [-openai-provider] [-force] [-off]` | Connect a tool. `-subscription`: keep its own login, by default `auto` (when it is signed in); `-openai-provider`: Codex uses its built-in OpenAI provider (earlier conversations stay listed); `-force`: replace an address that points at another gateway; `-off`: put the configuration file back as it was |
| `official` | The plan usage the client has seen, and what happens when a plan runs out |
| `official fallback on\|off` | Claude Code: when the plan is used up, go on with a model from the network (on by default) |
| `official fallback-model <model\|auto>` | The model that takes over; `auto` picks the one closest to the model asked for |
| `official guard <percent>` | Codex: hold the official models once a usage window reaches this percentage (default 95) |
| `codex-unlock on\|off` | Codex Desktop (macOS, Windows): a used-up ChatGPT plan does not lock the input, so the network's models go on. You trust a certificate valid for localhost only. Without an argument it shows the state |
| `autostart on\|off` | Run the client when you log in to the computer. Without an argument it shows the state |

```bash
{{exe}} connect claude
{{exe}} connect codex -off
{{exe}} official fallback off
```

These commands work while the client is stopped. A connected tool reaches even its official models through the client, so the client has to keep running. Restart the tool after a change. See [Use a Claude or ChatGPT subscription too](/docs/use-models#subscription).

## Coding agents {#agents}

`skill install` teaches Claude Code and Codex on this computer to use the client: check its status and balance, call models through the local endpoint, share a model. It writes `skills/sharetoken/SKILL.md` under `~/.claude` and `~/.codex` (`%USERPROFILE%` on Windows; `CLAUDE_CONFIG_DIR` and `CODEX_HOME` move them). The skill asks the agent to check with you before it changes settings or spends many credits.

| Command | What it does |
|---|---|
| `skill` | Which agents were found, and whether they have the skill |
| `skill install [-agent claude\|codex\|all] [-force]` | Install it, by default for every agent found. Run it again after updating the client |
| `skill uninstall [-agent …] [-force]` | Remove it again, by default from both agents |
| `skill show` | Print the skill |

```bash
{{exe}} skill install
{{exe}} skill install -agent codex
```

A file of the same name that the client did not write is left alone unless you add `-force`. Restart an open agent session to pick the skill up. See [Use with coding agents](/docs/agents).

## Warm-up {#warmup}

A Claude or ChatGPT (Codex) subscription's 5-hour window starts at the first request after it resets. The client can send one tiny request at times of day you choose, so a window starts early and resets sooner while you work: 6:00 gives the windows 6–11, 11–16 and 16–21 where starting work at 9:00 gives two. It works for the Claude Code and Codex on this computer, through their own command, and for the Claude and ChatGPT logins you sell. Nothing goes through the sharetoken network.

| Command | What it does |
|---|---|
| `warmup` | Every account there is to warm up, its times, the last warm-up and its usage windows |
| `warmup set claude\|codex [-times 06:00,11:05] [-on-reset off\|week\|all] [-model M] [-binary PATH] [-off]` | The Claude Code or Codex of this computer. `-model`: the model to ask (`default` for the cheapest); `-binary`: where the program is (`auto` to find it); `-off`: turn it off |
| `provider warmup <name> [-times 06:00] [-on-reset off\|week\|all] [-model M] [-off]` | A Claude or ChatGPT login you sell |
| `warmup run [claude\|codex\|provider:<name>]` | Warm up now (default: every one that is set) |
| `warmup pause`, `warmup resume` | Stop every automatic warm-up, or go on |

```bash
{{exe}} warmup set claude -times 06:00,11:05
{{exe}} warmup set codex -on-reset all
{{exe}} warmup run claude
```

At each time, a request is sent only if the 5-hour window isn't running; a time missed by more than an hour (the computer asleep) is skipped. `-on-reset week` starts the weekly window as soon as it resets, `all` the 5-hour one too. Nothing is sent while a window is used up or extra usage is in use. `-times none` clears the times. The settings can be changed while the client is stopped, but warm-ups happen only while it runs (the desktop app, or `up`), and `warmup run` needs it running too; times are this computer's local time. The desktop app has the same settings under Settings → Warm-up.

## Logs and crash reports {#logs}

### logs {#logs-cmd}

```bash
{{exe}} logs
{{exe}} logs -n 50 -f
{{exe}} logs path
```

Prints the last lines of the client's log (default 200; `-n` sets how many), and with `-f` keeps printing new lines. `-gui` prints the desktop app's own log instead. `logs path` prints the log directory. The client writes the log itself, whichever way it was started (in a terminal, by the desktop app, or at login), to `client.log` in that directory; a file over 10 MB is renamed `client.log.1` and so on, and five old files are kept (both are defaults). See [Logs and crash reports](/docs/config#logs) for the directory and its settings.

### diagnostics {#diagnostics}

```bash
{{exe}} diagnostics
{{exe}} diagnostics off
{{exe}} diagnostics show
```

Shows whether crash reports are sent, the region the client believes you are in, how many reports wait to be sent, and the log directory. `on` and `off` change it; `off` also deletes the reports that were waiting. `show` prints exactly what would be sent. When the client crashes or recovers from a panic, it sends the portal a redacted report with your login: the error, where in the code it happened, and the last lines of the log before it. It is on unless you turn it off, except in the European Economic Area, where it starts off. See [Crash reports](/docs/config#crash-reports) for what a report contains.

## Other {#other}

| Command | What it does |
|---|---|
| `config` | Show the configuration file path and contents. Only the start of the token is shown |
| `version [--check]` | Show the version, network, portal, state directory and local API address. Writes no files. `--check` also asks the portal whether there is a newer release |
| `upgrade [-y]` | Install the newest release: fetch its package, check the release signature, install it the way this copy was installed. It asks first; `-y` does not ask. See [One-click update](/docs/update#one-click) |

## Environment variables {#env}

| Variable | What it does |
|---|---|
| `SHARETOKEN_HOME` | State directory. Same as `-home` |
| `SHARETOKEN_LOG_DIR` | Log directory. Takes precedence over `-log-dir` and the configuration's `log.dir` |
| `SHARETOKEN_PROVIDER_KEY` | The API key for `provider add`, instead of typing it |
| `SHARETOKEN_PROVIDER_PROXY` | A provider's proxy address, used with `-proxy-mode custom` when `-proxy` is not given. Put an address with a password here, not on the command line |
| `SHARETOKEN_REGION` | Your country as two letters (such as `DE`), when the system's region settings do not say where you are. It decides whether crash reports start on or off |
