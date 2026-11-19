---
name: sharetoken
description: Use the ShareToken client (the `sharetoken` command) on this machine - buy model inference from the ShareToken network through its local endpoint (OpenAI chat completions and Responses, Anthropic Messages), check the client's status, balance, jobs, models and relays, share a local model or a hosted API to earn credits, and manage pairing. Use when the user mentions ShareToken, sharetoken, its credits, or wants to call or sell models through it.
---

<!-- Installed by `sharetoken skill install`; `sharetoken skill uninstall` removes it. -->

# ShareToken client

ShareToken is a peer-to-peer market for model inference. The `sharetoken` command on this machine is the user's client: it buys inference from other people's nodes with the user's credits, and it can share (sell) a local model or a hosted API to earn credits.

This copy of the skill was installed by a @ENV@ build of the client:

| | |
|---|---|
| Command | `@CMD@` |
| Portal | @PORTAL@ |
| State directory | `@HOME@` (settings in `config.json`; `-home DIR` or `$SHARETOKEN_HOME` selects another) |
| Model endpoint | `http://@HTTP@/v1` (OpenAI and Anthropic APIs; only while the client runs) |

The examples below write `sharetoken`; use the command above if `sharetoken` is not on the PATH.
@PLATFORM@
## Ground rules

- **Credits are money.** Requests through the endpoint spend the user's credits. Do not send large batches, loops or long generations without the user's go-ahead, and set `max_tokens` on requests.
- **Ask before changing state:** `logout`, `share on/off/add/rm`, `provider add/login/on/off/rm`, `pair join/leave/rm/revoke/private`, `partner invite/join/rm/revoke/set`, `apikey on/off/rotate`, `relays add/remove`, `tailnet on/off`. Reading (`status`, `balance`, `jobs`, `models`, `relays`, `tailnet`, `share`, `provider`, `pair`, `partner`, `config`) is always fine.
- **Never put secrets on a command line.** A provider API key goes in through standard input or `$SHARETOKEN_PROVIDER_KEY`, never as an argument. Never print the `token` from `config.json` or `providers.json`; `sharetoken config` masks it.
- **Sign-in needs the user.** `login` mails a six-digit code to the user's address. Ask the user for the address and then for the code; do not guess them.
- **`up` runs until stopped.** Start it in the background (or ask the user to run it in a terminal); never wait on it in the foreground.

## Is the client ready?

```bash
sharetoken version          # build type, portal, state directory, local API address
sharetoken status           # "the client is not running" when no client runs
```

- `not logged in` → sign in (below).
- `the client is not running` → start the client: `sharetoken up` in the background. It is ready once `status` shows the coordinator `connected`. The desktop app runs the same client (`sharetoken daemon`); if it is open, nothing needs starting.
- `status` prints `warnings:` lines: read them to the user.

## Sign in

```bash
sharetoken login -email ADDR -send     # mail the code, exit
sharetoken login -email ADDR -code 123456 [-totp 654321]
```

Two steps, because the code arrives by mail. An address without an account gets one (when registration is open; then `-invite CODE` may be needed). `-totp` is the authenticator code for accounts with two-factor authentication; without it the command asks for it on the terminal. The sign-in lasts 60 days.

## Buy: call models

```bash
sharetoken models          # model IDs on the network, nodes, lowest prices
```

Any OpenAI-compatible tool works with base URL `http://@HTTP@/v1` and the client's API key, which `@CMD@ apikey -q` prints (it stays the same until `@CMD@ apikey rotate`; `@CMD@ apikey` says whether it is required). Pass it straight on; do not print it or write it into files or messages:

```bash
curl -s http://@HTTP@/v1/chat/completions -H "Authorization: Bearer $(@CMD@ apikey -q)" -H 'Content-Type: application/json' \
  -d '{"model":"MODEL_ID","max_tokens":256,"messages":[{"role":"user","content":"Hello"}]}'
```

```python
from openai import OpenAI
import subprocess
key = subprocess.run(["@CMD@", "apikey", "-q"], capture_output=True, text=True, check=True).stdout.strip()
client = OpenAI(base_url="http://@HTTP@/v1", api_key=key)
r = client.chat.completions.create(model="MODEL_ID", max_tokens=256,
                                   messages=[{"role": "user", "content": "Hello"}])
```

- Endpoints: `POST /v1/chat/completions` (OpenAI), `POST /v1/messages` and `/v1/messages/count_tokens` (Anthropic: Claude Code with `ANTHROPIC_BASE_URL=http://@HTTP@` and `ANTHROPIC_AUTH_TOKEN` set to the key), `POST /v1/responses` (OpenAI Responses: Codex with a provider whose `base_url` is `http://@HTTP@/v1` and `wire_api = "responses"`; Codex reads a custom provider's models only from a catalog file: `sharetoken models -codex FILE` writes one, named by `model_catalog_json` at the top of its config.toml), `GET /v1/models` (`apis` lists the APIs each model is served in natively). Streaming works; there is no embeddings endpoint.
- Any model works in any of the three APIs: a node whose model server speaks another one converts the request. `X-Sharetoken-Require-Native: true` (or `buy.require_native`) takes only nodes that need no conversion.
- Set `max_tokens`: only its worst-case cost is held. Without it the whole per-request budget (`buy.budget_micros`) is held. Reasoning models spend part of `max_tokens` on reasoning; a small value can leave `content` empty.
- Response headers: `X-Sharetoken-Job`, `X-Sharetoken-Node`, `X-Sharetoken-Verified`, `X-Sharetoken-Cost-Micros`, and `X-Sharetoken-TEE` for an attested node.
- Errors are `{"error":{"code":…}}`: `no_offers` 503 (nobody sells that model: check `models`), `capacity` 503 (busy, retry after `Retry-After`), `insufficient_funds` 402, `budget` 400, `upgrade_required` 426 (`sharetoken version --check`), `upstream_error` / `receipt_rejected` 502 (retry; the client already tried another node when it could).
- Requests with a non-local `Origin` header are refused: call from a program, not from a web page.

Amounts: 1 credit = 1,000,000 micro-credits. Prices are micro-credits per million tokens (input / output).

## Account

```bash
sharetoken balance                     # available and held credits, tokens bought and sold
sharetoken jobs [-role buy|sell] [-n 20]
sharetoken relays [-measure]           # relay round-trip times; the link quality of the current relay
sharetoken relays add <address>        # share through a relay the user runs themselves (a private relay)
sharetoken relays remove <address|id>  # stop using it
sharetoken tailnet                     # direct tunnels (off by default): whether on, each tunnel's path (direct, or derp:<region>) and transport
sharetoken tailnet on | off            # direct WireGuard tunnels between buyer and seller in place of the relay; both ends need it on
sharetoken tailnet on -transport tcp   # run TCP inside the tunnel in place of QUIC (the default), for this machine's connections to sellers
sharetoken version --check             # is a newer client released?
sharetoken upgrade                      # install it (asks first; checks the release signature). Only when the user asks
```

## Sell: share a model

Sharing commands take effect at once on a running client.

```bash
sharetoken share                        # current sharing settings
sharetoken share detect                 # models of the local model server (default Ollama, http://127.0.0.1:11434/v1)
sharetoken share upstream URL           # another OpenAI-compatible local server (vLLM, LM Studio, llama.cpp …)
sharetoken share add qwen2.5:7b [-id NAME] [-in PRICE] [-out PRICE] [-cache-read PRICE] [-cache-write PRICE] [-max-output N]
sharetoken share rm ID
sharetoken share on | off
```

Hosted APIs with the user's own credentials (kept in `providers.json`):

```bash
sharetoken provider                                  # kinds that can be added, and the ones configured
printf '%s' "$KEY" | sharetoken provider add deepseek   # API key: anthropic, openai, gemini, grok, deepseek, custom (-url URL)
sharetoken provider login openai                     # OAuth: openai, anthropic, gemini, antigravity, grok (interactive: the user opens a link)
sharetoken provider models NAME
sharetoken provider test NAME [-model M]
sharetoken share add MODEL -provider NAME            # then share one of its models
sharetoken provider on | off | rm NAME
```

`provider login` prints an address the user must open in a browser and then asks for the code or callback address: hand that step to the user.

## Pairing

A buyer paired with sellers buys only from them (at most 3). A seller in private mode serves only its paired buyers.

```bash
sharetoken pair                                   # pairings and live codes
sharetoken pair code [-label L] [-ttl 24h] [-uses N]   # seller: make a code for buyers
sharetoken pair join CODE                         # buyer
sharetoken pair leave SELLER_ID | pair rm BUYER_ID | pair revoke CODE_ID
sharetoken pair private on | off
```

## Share partners

Two users who pair as partners use each other's nodes first and free (no credits move); when no partner node can take a request it goes to the network and is paid, unless `-only on`. Each limits what it gives.

```bash
sharetoken partner                                # partners, tokens given/used in 24h, open invites, settings
sharetoken partner invite [-ttl 24h]              # a one-person invite code
sharetoken partner join CODE
sharetoken partner rm USER_ID | partner revoke INVITE_ID
sharetoken partner set [-only on|off] [-limit TOKENS_PER_24H] [-share PERCENT]
```

## Local management API

While the client runs, `http://@HTTP@/api/v1/…` serves JSON for scripts: `GET status`, `GET models`, `GET account`, `GET jobs?role=buy&limit=20`, `GET share`, `GET pairing`, `GET sharing`. Each request needs the key the client writes to `@HOME@/run.json` at each start (field `key`), as `Authorization: Bearer <key>`; without it the answer is 401 `local_auth`. Never print the key or put it in files or messages. Prefer the commands above unless JSON is needed.

## More

`sharetoken` with no arguments prints every command and flag. The user guide is at <@PORTAL@/docs/>.
