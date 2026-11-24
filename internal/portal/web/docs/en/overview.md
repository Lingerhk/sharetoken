# Overview

ShareToken is a P2P network for sharing open-weight models. This guide explains how to install and use the client on macOS, Linux and Windows.

## What the client does {#what}

One client and one account can both buy and sell:

- **Call models:** The client opens an endpoint on your machine in the OpenAI and Anthropic APIs. OpenAI tools, Claude Code and Codex work once you change the base URL.
- **Share models:** Share the models served by your local Ollama or similar service with the network and earn credit while the machine is idle. You do not need a public IP or open ports.

## Choose an edition {#editions}

| Edition | Good for | Program |
|---|---|---|
| Command line | Servers, scripts, machines without a desktop | `{{exe}}` |
| Desktop app | Users who want a graphical interface to sign in and check status | A window program plus the same `{{exe}}` |

Both editions use the same program and the same state directory. You only need one on a machine, and you can also use both. See [Desktop app](/docs/desktop).

## Where to start {#start}

::: cards
[Desktop app](/docs/desktop) Download the installer, then sign in with your email in a graphical interface.
[Quick start](/docs/quickstart) Full steps for every system, from download to your first model call.
[Install the command-line client](/docs/install) Pick the architecture, verify the package, set up PATH.
[Call models](/docs/use-models) Examples for curl, PowerShell, Python and Node.js.
[Share a model](/docs/share) Connect Ollama, set prices, check your earnings.
[Run the client](/docs/running) Check status and run in the background after boot.
[Use with coding agents](/docs/agents) Install the skill for Claude Code and Codex.
[Troubleshooting](/docs/troubleshooting) Causes and fixes for common errors.
:::

Use the system switch at the top of the page and the commands change to match. Press `/` to search the whole guide.

## Basic concepts {#concepts}

| Name | Meaning |
|---|---|
| credit | The unit of account in the network. 1 credit = 1,000,000 micro-credit. Prices are in micro-credit per million tokens |
| portal | [This website](/app/). Sign-up, sign-in, bills, devices, pairing and API tokens live here |
| device | One device key on one machine, bound to your account. It spends your credit, and its earnings go to your account |
| relay | A public node that forwards encrypted traffic. It cannot read the content and keeps no accounts |
| coordinator | Handles matching, settlement and disputes, and keeps the ledger |
| job | One model call. The node signs a receipt, and the buyer checks it before settlement |
| pairing | A tie between a buyer and a seller, made with the seller's pairing code. A paired buyer buys only from its sellers. See [Buy only from sellers you know](/docs/use-models#pairing) |
| share partner | Someone you pair with by invite code so that each of you uses the other's nodes first, for free. It is the second way to share, next to buying and selling. See [Share with partners for free](/docs/share#partners) |

## Privacy boundary {#privacy}

> [!WARNING]
> **Ordinary nodes can read your prompt.** Running a model requires decrypting the prompt, so a node's operator can in principle see the content. Relays, the coordinator and network eavesdroppers cannot. For sensitive content, use only TEE nodes. See [Use only TEE nodes](/docs/use-models#tee).

Crash reports: when the client crashes, it sends the portal a redacted report with your login: the error, where in the code it happened, and the client's own log lines before it, without e-mail addresses, keys, IP addresses or paths. Prompts and answers are never logged, so they are never in a report. Reports are on by default and off by default in the European Economic Area; turn them off in Settings → General or with `{{exe}} diagnostics off`. See [Crash reports](/docs/config#crash-reports).

The coordinator is still the trusted party for the ledger: it cannot forge receipts, but it is the one that records the ledger. Audits are probabilistic. They make cheating unprofitable over time, but they do not guarantee that every job is checked.
