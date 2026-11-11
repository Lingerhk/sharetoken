# Command reference

The format of every command:

```bash
{{exe}} [-home DIR] <command> [flags]
```

`-home` goes **before** the command. It selects another state directory. Run with no arguments for short help.

## Account {#account}

### register {#register}

Create an account and bind this device.

```bash
{{exe}} register -email you@example.com
```

| Option | Meaning |
|---|---|
| `-email ADDR` | The email address to register with |
| `-code CODE` | A verification code you already received. If omitted, one is sent and you are asked for it |
| `-device NAME` | Device name. Defaults to the host name |

### login {#login}

Sign in and bind this device.

```bash
{{exe}} login -user you@example.com
```

| Option | Meaning |
|---|---|
| `-user NAME` | Email or user name. Asked for if omitted |
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
| `pair code [-label L] [-ttl 24h] [-uses N]` | Seller: make a code for your buyers. It lasts 24 hours and admits any number of buyers unless you say otherwise |
| `pair join <code>` | Buyer: pair with the seller that made the code |
| `pair leave <seller id>` | Buyer: end the pairing with a seller |
| `pair rm <buyer id>` | Seller: remove one of your buyers |
| `pair revoke <code id>` | Seller: stop a code from admitting more buyers. Buyers already paired stay |

```bash
{{exe}} pair code -label team -ttl 72h
{{exe}} pair join ABCD-EFGH-IJKL-MNOP
```

The ids are the ones `pair` lists. See [Pair with your buyers](/docs/share#pairing).

## Other {#other}

| Command | What it does |
|---|---|
| `config` | Show the configuration file path and contents. Only the start of the token is shown |
| `version` | Show the version, network, portal, state directory and local API address. Writes no files |

## Environment variables {#env}

| Variable | What it does |
|---|---|
| `SHARETOKEN_HOME` | State directory. Same as `-home` |
| `SHARETOKEN_PASSWORD` | Password used by login and register, for scripts. See [Sign in from a script](/docs/account#login-script) |
| `SHARETOKEN_TELEMETRY=0`, `DO_NOT_TRACK=1` | Turn off the anonymous usage statistics of `daemon` |
