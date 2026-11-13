# Command reference

The format of every command:

```bash
{{exe}} [-home DIR] <command> [flags]
```

`-home` goes **before** the command. It selects another state directory. Run with no arguments for short help.

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

### logout {#logout}

Revoke this machine's token and remove it from the configuration file.

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

Run the client. `run` is an alias.

| Option | Meaning |
|---|---|
| `-http ADDR` | Local API address. Default `127.0.0.1:{{port}}` |
| `-listen MADDRS` | libp2p listen addresses, comma-separated |
| `-nat=false` | Turn off UPnP, NAT-PMP and hole punching |

These options apply to this run only. They are not written to the configuration file.

### daemon {#daemon}

Run the service behind the desktop app, which serves the [web interface](/docs/desktop#browser-ui). Its options are the same as `up`. Unlike `up`, it can run while signed out, and registration and sign-in happen on its page.

### status {#status}

Show the connection, relay and sharing status. The client must be running.

### models {#models}

List the models on the network, the node count, the TEE node count and the lowest price. The client must be running.

### relays {#relays}

Show the latency of each relay. `-measure` measures again right away. The client must be running.

## Share {#share}

| Command | What it does |
|---|---|
| `share` | Show the sharing settings |
| `share detect [-upstream URL]` | List the models of the local model server |
| `share add <model> [-id ID] [-in P] [-out P]` | Share a model and turn sharing on |
| `share rm <id>` | Remove a model |
| `share on`, `share off` | Turn sharing on or pause it |
| `share upstream <url>` | Set the local model server. Default `http://127.0.0.1:11434/v1` |

`<model>` must come right after `add`, and options go after it. See [Share a model](/docs/share).

## Pairing {#pairing}

A seller hands out a pairing code. A buyer that joins with it buys only from the sellers it is paired with. One code admits many buyers.

| Command | What it does |
|---|---|
| `pair` | Show who you are paired with, and your pairing codes |
| `pair code [-label L] [-ttl 24h] [-uses N]` | Seller: make a code for your buyers. It lasts 24 hours (30 days at most) and admits any number of buyers unless you say otherwise |
| `pair join <code>` | Buyer: pair with the seller that made the code |
| `pair leave <seller id>` | Buyer: end the pairing with a seller |
| `pair rm <buyer id>` | Seller: remove one of your buyers |
| `pair revoke <code id>` | Seller: stop a code from admitting more buyers. Buyers already paired stay |
| `pair private on\|off` | Seller: serve only the buyers paired with you, or anyone again |

```bash
{{exe}} pair code -label team -ttl 72h
{{exe}} pair join ABCD-EFGH-IJKL-MNOP
```

The ids are the ones `pair` lists. See [Pair with your buyers](/docs/share#pairing).

## Coding agents {#agents}

`skill install` teaches Claude Code and Codex on this computer to use the client: check its status and balance, call models through the local endpoint, share a model. It writes `skills/sharetoken/SKILL.md` under `~/.claude` and `~/.codex` (`%USERPROFILE%` on Windows; `CLAUDE_CONFIG_DIR` and `CODEX_HOME` move them). The skill asks the agent to check with you before it changes settings or spends many credits.

| Command | What it does |
|---|---|
| `skill` | Which agents were found, and whether they have the skill |
| `skill install [-agent claude\|codex\|all]` | Install it, by default for every agent found. Run it again after updating the client |
| `skill uninstall [-agent …]` | Remove it again |
| `skill show` | Print the skill |

```bash
{{exe}} skill install
{{exe}} skill install -agent codex
```

A file of the same name that the client did not write is left alone unless you add `-force`. Restart an open agent session to pick the skill up. See [Use with coding agents](/docs/agents).

## Other {#other}

| Command | What it does |
|---|---|
| `config` | Show the configuration file path and contents. Only the start of the token is shown |
| `version` | Show the version, network, portal, state directory and local API address. Writes no files |

## Environment variables {#env}

| Variable | What it does |
|---|---|
| `SHARETOKEN_HOME` | State directory. Same as `-home` |
