# Local API

The running client serves two groups of endpoints on `http://127.0.0.1:{{port}}`: `/v1/` is the OpenAI-compatible interface, and `/api/v1/` is for querying and managing the client.

Only requests from your machine are accepted. Requests with a non-local `Origin` get 403. The management endpoints also want a key (see [Key](#key)). Errors always have the form `{"error":{"code","message"}}`.

## OpenAI-compatible endpoints {#openai}

| Endpoint | Description |
|---|---|
| `POST /v1/chat/completions` | Call a model. Supports `stream` and `max_tokens` |
| `GET /v1/models` | The models on the network |

For usage, see [Call models](/docs/use-models).

## Management endpoints {#admin}

| Endpoint | Description |
|---|---|
| `GET /api/v1/status` | Full status of connection, relays, sharing and warnings |
| `GET /api/v1/models` | Models, node count, TEE node count and lowest price |
| `GET /api/v1/account` | Balance, usage and devices |
| `GET /api/v1/jobs?role=buy&limit=20` | Recent jobs |
| `GET /api/v1/ledger?limit=20` | Bills |
| `POST /api/v1/relays/measure` | Measure again right away and return the result |
| `GET /api/v1/share` | Read the sharing settings |
| `PUT /api/v1/share` | Change the sharing settings. They are validated, then applied immediately and saved |
| `POST /api/v1/share/start`, `POST /api/v1/share/stop` | Turn sharing on or pause it |
| `POST /api/v1/share/reload` | Reload the sharing settings from `config.json` |
| `GET /api/v1/share/detect` | List the models of the local model server |
| `GET /api/v1/pairing` | The sellers you are paired with, your buyers, your pairing codes, and whether private mode is on |
| `POST /api/v1/pairing/redeem` | Buyer: pair with a seller. Body: `{"code": "…"}` |
| `DELETE /api/v1/pairing/sellers/{id}` | Buyer: end the pairing with a seller |
| `POST /api/v1/pairing/codes`, `DELETE /api/v1/pairing/codes/{id}` | Seller: make a pairing code (`label`, `ttl_hours`, `max_uses`), or revoke one |
| `DELETE /api/v1/pairing/buyers/{id}` | Seller: remove one of your buyers |
| `PUT /api/v1/pairing/private` | Seller: turn private mode on or off. Body: `{"enabled": true}` |
| `GET /api/v1/sharing` | Your share partners with the tokens used each way in the last 24 hours, your open invites and your sharing settings |
| `POST /api/v1/sharing/invites`, `DELETE /api/v1/sharing/invites/{id}` | Make a one-person invite (`ttl_hours`, 168 at most), or withdraw one |
| `POST /api/v1/sharing/join` | Become the share partner of whoever made the invite. Body: `{"code": "…"}` |
| `DELETE /api/v1/sharing/partners/{id}` | Stop sharing with a partner |
| `PUT /api/v1/sharing/settings` | Partners only, the daily limit and the share. Body: `{"only_partners": false, "daily_limit_tokens": 0, "share_pct": 100}` |

## Key {#key}

Each time the client starts, it makes a new key and writes it to `run.json` in its data directory (field `key`), which only you can read. Send it with every request to `/api/v1/` as `Authorization: Bearer <key>`; without it the answer is 401 `local_auth`. The `{{exe}}` commands and the desktop app read it themselves. `/v1/` wants the API key instead, which does not change at each start (`{{exe}} apikey`; `GET /api/v1/apikey`, `PUT /api/v1/apikey` with `{"required": bool}`, `POST /api/v1/apikey/rotate`).

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
