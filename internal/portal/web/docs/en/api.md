# Local API

The running client serves two groups of endpoints on `http://127.0.0.1:{{port}}`: `/v1/` is the model interface (OpenAI and Anthropic APIs), and `/api/v1/` is for querying and managing the client.

Only requests addressed to this machine (`127.0.0.1`, `localhost`) are accepted. Requests with a non-local `Origin` get 403, so web pages cannot call them. The management endpoints also want a key (see [Key](#key)). Errors have the form `{"error":{"code","message"}}` (on `/v1/messages`, Anthropic's form).

## Model endpoints {#openai}

| Endpoint | Description |
|---|---|
| `POST /v1/chat/completions` | Call a model in the OpenAI chat completions API. Supports `stream` and `max_tokens` |
| `POST /v1/messages` | Call a model in the Anthropic Messages API (Claude Code) |
| `POST /v1/messages/count_tokens` | Estimate a Messages request's input tokens, on this machine |
| `POST /v1/responses` | Call a model in the OpenAI Responses API (Codex) |
| `GET /v1/responses` | The Responses API over a WebSocket (Codex's `supports_websockets`) |
| `POST /v1/responses/compact` | Compact a Codex conversation |
| `GET /v1/models` | The models on the network (IDs with `st-` before them), with the APIs they are served in natively (`apis`), context windows and reasoning levels; with `?client_version=`, Codex's model list |

Request headers the endpoints read: `X-Sharetoken-Require-Native: true` (only nodes that need no conversion) and `X-Sharetoken-Node: <node ID>` (only that node). The network has no embeddings or image endpoints.

A request that carries Claude Code's or Codex's own vendor login (or an Anthropic or OpenAI API key) goes to the vendor as it is when its model does not start with `st-`; see [Keep your subscription](/docs/use-models#subscription).

For usage, see [Call models](/docs/use-models).

## Management endpoints {#admin}

Requests with a body need `Content-Type: application/json`.

| Endpoint | Description |
|---|---|
| `GET /api/v1/status` | Full status of connection, relays, sharing and warnings |
| `GET /api/v1/models` | Models (without `st-`), node count, TEE node count, lowest prices and native APIs |
| `GET /api/v1/account` | Balance, usage and devices |
| `GET /api/v1/jobs?role=buy&limit=20` | Recent jobs (`page` for more) |
| `GET /api/v1/ledger?limit=20` | Bills |
| `GET /api/v1/buy`, `PUT /api/v1/buy` | Request settings: `{"budget_micros": 2000000, "require_native": false}` |
| `POST /api/v1/relays/measure` | Measure again right away and return the result |
| `POST /api/v1/relays/private`, `DELETE /api/v1/relays/private` | Add or remove a [private relay](/docs/relay): `{"addr": "<address>"}`; removing also takes the relay's ID |
| `GET /api/v1/tailnet`, `PUT /api/v1/tailnet` | [Direct tunnels](/docs/running#tailnet): `enabled`, `transport`, `wait_ms`, `derp_map`; fields left out keep their value |
| `POST /api/v1/tailnet/reload` | Reload the tunnel settings from `config.json` |
| `GET /api/v1/share` | Read the sharing settings |
| `PUT /api/v1/share` | Change the sharing settings. They are validated, then applied immediately and saved |
| `POST /api/v1/share/start`, `POST /api/v1/share/stop` | Turn sharing on or pause it |
| `POST /api/v1/share/reload` | Reload the sharing settings from `config.json` |
| `GET /api/v1/share/detect` | List the models of the local model server |
| `GET /api/v1/providers` | The kinds of hosted API that can be added, and the providers added |
| `POST /api/v1/providers`, `PATCH /api/v1/providers/{name}`, `DELETE /api/v1/providers/{name}` | Add a provider with an API key, change it, or remove it |
| `GET /api/v1/providers/{name}/models`, `PUT /api/v1/providers/{name}/models`, `DELETE /api/v1/providers/{name}/models/{id}` | The models its credentials may use; sell a model or change its price; stop selling it |
| `POST /api/v1/providers/{name}/test` | Send one small request through it: `{"model": "…", "prompt": "…"}` |
| `GET /api/v1/pairing` | The sellers you are paired with, your buyers, your pairing codes, requests waiting for approval, and whether private mode is on |
| `POST /api/v1/pairing/redeem` | Buyer: pair with a seller. Body: `{"code": "…"}`, which may also be an invite link |
| `DELETE /api/v1/pairing/sellers/{id}` | Buyer: end the pairing with a seller |
| `POST /api/v1/pairing/codes`, `DELETE /api/v1/pairing/codes/{id}` | Seller: make a pairing code (`label`; `ttl_hours`, 168 at most; `max_uses`, 0 for any number; `approval`), or revoke one |
| `DELETE /api/v1/pairing/buyers/{id}` | Seller: remove one of your buyers |
| `PUT /api/v1/pairing/private` | Seller: turn private mode on or off. Body: `{"enabled": true}` |
| `GET /api/v1/sharing` | Your share partners with the tokens used each way in the last 24 hours, your open invites, requests waiting for approval and your sharing settings |
| `POST /api/v1/sharing/invites`, `DELETE /api/v1/sharing/invites/{id}` | Make an invite (`ttl_hours`, 168 at most; `max_uses`, 1 to 100, one by default; `approval`), or withdraw one |
| `POST /api/v1/sharing/join` | Become the share partner of whoever made the invite. Body: `{"code": "…"}`, which may also be an invite link |
| `DELETE /api/v1/sharing/partners/{id}` | Stop sharing with a partner |
| `PUT /api/v1/sharing/settings` | Partners only, the daily limit and the share. Body: `{"only_partners": false, "daily_limit_tokens": 0, "share_pct": 100}` |
| `POST /api/v1/pairing/requests/{id}/approve`, `DELETE /api/v1/pairing/requests/{id}` | Approve or reject a pairing request waiting for you; for share partners, `sharing` in place of `pairing` |
| `DELETE /api/v1/pairing/waiting/{id}` | Withdraw a request of your own that waits for approval (`sharing` likewise) |
| `POST /api/v1/invite/accept` | Accept an [invite link](/docs/share#invite-links) or a code of either kind: `{"link": "…"}` |
| `GET /api/v1/connect`, `POST /api/v1/connect` | How Claude Code and Codex are connected; connect or disconnect one: `{"tool": "claude", "subscription": true, "openai_provider": false, "force": false, "off": false}` |
| `GET /api/v1/official`, `PUT /api/v1/official` | The plans' usage and what happens when one runs out. `PUT` replaces all of it: `{"claude_fallback_off": false, "claude_fallback_model": "", "codex_guard_percent": 95, "codex_unlock": false}` |
| `POST /api/v1/official/unlock` | Turn the Codex Desktop unlock on or off: `{"on": true}` |
| `GET /api/v1/autostart`, `PUT /api/v1/autostart` | Start the client when you log in: `{"on": true}` |
| `GET /api/v1/warmup`, `PUT /api/v1/warmup` | Warm-up status and settings: `paused`, `claude`, `codex`; what is left out stays |
| `PUT /api/v1/warmup/providers/{name}` | A provider's warm-up (`times`, `on_reset`, `model`); an empty body turns it off |
| `POST /api/v1/warmup/run` | Warm up now: `{"target": "local:claude"}`, or `local:codex`, `provider:<name>` |
| `GET /api/v1/diagnostics`, `PUT /api/v1/diagnostics` | The crash report switch: `{"upload": true}` |
| `GET /api/v1/diagnostics/reports` | Crash reports waiting to be sent (redacted) |

Endpoints that need the network answer 401 `not_logged_in` while the client is signed out, and 503 `not_connected` while it is still joining. `official`, `connect`, `autostart`, `apikey`, `warmup` and `diagnostics` work in the desktop app and the background service before they join the network; the account, jobs, bills, `buy` and `tailnet` need only a sign-in. The `providers` endpoints take only requests without an `Origin` and the client's own page.

Other endpoints the desktop app uses for itself (sign-in, updates, feedback and such) are not listed here and may change between versions.

## Key {#key}

Each time the client starts, it makes a new key and writes it to `run.json` in its data directory (field `key`), which only you can read. Send it with every request to `/api/v1/` as `Authorization: Bearer <key>`; without it the answer is 401 `local_auth`. The `{{exe}}` commands and the desktop app read it themselves.

`/v1/` wants another key instead, the API key, which does not change at each start (`sk-st-…`, kept in `apikey.json` in the data directory; see [Local endpoint](/docs/use-models#endpoint)). The endpoints that manage it:

| Endpoint | Description |
|---|---|
| `GET /api/v1/apikey` | `{"required": true, "key": "sk-st-…"}` |
| `PUT /api/v1/apikey` | Require the key or not: `{"required": false}` |
| `POST /api/v1/apikey/rotate` | Make a new key; the old one stops working at once |

## Examples {#examples}

::: macos linux
```bash
KEY=$(python3 -c 'import json,os;print(json.load(open(os.path.expanduser("{{home}}/run.json")))["key"])')
curl -s -H "Authorization: Bearer $KEY" http://127.0.0.1:{{port}}/api/v1/status
curl -s -H "Authorization: Bearer $KEY" http://127.0.0.1:{{port}}/api/v1/account
curl -s -H "Authorization: Bearer $KEY" -X POST http://127.0.0.1:{{port}}/api/v1/share/stop
```
:::

::: windows
```powershell
$h = @{ Authorization = "Bearer " + (Get-Content "{{homewin}}\run.json" | ConvertFrom-Json).key }
Invoke-RestMethod http://127.0.0.1:{{port}}/api/v1/status -Headers $h | ConvertTo-Json -Depth 6
Invoke-RestMethod http://127.0.0.1:{{port}}/api/v1/account -Headers $h | ConvertTo-Json -Depth 6
Invoke-RestMethod http://127.0.0.1:{{port}}/api/v1/share/stop -Method Post -Headers $h
```
:::

## Security {#security}

> [!WARNING]
> `/v1/` wants the client's API key (see [Call models](/docs/use-models#endpoint)), and `/api/v1/` the key in `run.json`. If you turn the API key off (`{{exe}} apikey off`), other users of the machine can call `/v1/` and spend your credit. Do not change `http` to a non-local address, and do not forward the port.
