---
name: sharetoken
description: Use the ShareToken client (the `sharetoken` command) on this machine - buy model inference from the ShareToken network through its local OpenAI-compatible endpoint, check the client's status, balance, jobs, models and relays, share a local model or a hosted API to earn credits, and manage pairing. Use when the user mentions ShareToken, sharetoken, its credits, or wants to call or sell models through it.
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
| OpenAI-compatible endpoint | `http://@HTTP@/v1` (only while the client runs) |

The examples below write `sharetoken`; use the command above if `sharetoken` is not on the PATH.
@PLATFORM@
## Ground rules

- **Credits are money.** Requests through the endpoint spend the user's credits. Do not send large batches, loops or long generations without the user's go-ahead, and set `max_tokens` on requests.
- **Ask before changing state:** `logout`, `share on/off/add/rm`, `provider add/login/on/off/rm`, `pair join/leave/rm/revoke/private`. Reading (`status`, `balance`, `jobs`, `models`, `relays`, `share`, `provider`, `pair`, `config`) is always fine.
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

Any OpenAI-compatible tool works with base URL `http://@HTTP@/v1` and any API key:

```bash
curl -s http://@HTTP@/v1/chat/completions -H 'Content-Type: application/json' \
  -d '{"model":"MODEL_ID","max_tokens":256,"messages":[{"role":"user","content":"Hello"}]}'
```

```python
from openai import OpenAI
client = OpenAI(base_url="http://@HTTP@/v1", api_key="sharetoken")
r = client.chat.completions.create(model="MODEL_ID", max_tokens=256,
                                   messages=[{"role": "user", "content": "Hello"}])
```

- Only `POST /v1/chat/completions` and `GET /v1/models` exist (streaming works). There is no embeddings or Anthropic-format endpoint.
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
sharetoken version --check             # is a newer client released?
```

## Sell: share a model

Sharing commands take effect at once on a running client.

```bash
sharetoken share                        # current sharing settings
sharetoken share detect                 # models of the local model server (default Ollama, http://127.0.0.1:11434/v1)
sharetoken share upstream URL           # another OpenAI-compatible local server (vLLM, LM Studio, llama.cpp …)
sharetoken share add qwen2.5:7b [-id NAME] [-in PRICE] [-out PRICE] [-max-output N]
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

## Local management API

While the client runs, `http://@HTTP@/api/v1/…` serves JSON for scripts: `GET status`, `GET models`, `GET account`, `GET jobs?role=buy&limit=20`, `GET share`, `GET pairing`. Prefer the commands above unless JSON is needed.

## More

`sharetoken` with no arguments prints every command and flag. The user guide is at the portal's `/docs/`.
