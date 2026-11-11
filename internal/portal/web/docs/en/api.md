# Local API

The running client serves two groups of endpoints on `http://127.0.0.1:{{port}}`: `/v1/` is the OpenAI-compatible interface, and `/api/v1/` is for querying and managing the client.

Only requests from your machine are accepted. Requests with a non-local `Origin` get 403. Errors always have the form `{"error":{"code","message"}}`.

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

## Examples {#examples}

::: macos linux
```bash
curl -s http://127.0.0.1:{{port}}/api/v1/status
curl -s http://127.0.0.1:{{port}}/api/v1/account
curl -s -X POST http://127.0.0.1:{{port}}/api/v1/share/stop
```
:::

::: windows
```powershell
Invoke-RestMethod http://127.0.0.1:{{port}}/api/v1/status | ConvertTo-Json -Depth 6
Invoke-RestMethod http://127.0.0.1:{{port}}/api/v1/account | ConvertTo-Json -Depth 6
Invoke-RestMethod http://127.0.0.1:{{port}}/api/v1/share/stop -Method Post
```
:::

## Security {#security}

> [!WARNING]
> These endpoints have no authentication for other processes on the machine. On a multi-user machine, other users can call `/v1` and spend your credit, and they can change the sharing settings. Do not change `http` to a non-local address, and do not forward the port.
