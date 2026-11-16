# Configuration and files

Most people do not need to change the configuration file. This page explains where data is kept and which settings you can adjust.

## State directory {#home}

::: macos linux
The default is `{{home}}`.
:::

::: windows
The default is `{{homewin}}`, which is usually `C:\Users\<username>\.sharetoken`.
:::

To use another directory, for example to run two accounts on one machine:

::: macos linux
```bash
{{exe}} -home ~/sharetoken-second login -email other@example.com
{{exe}} -home ~/sharetoken-second up -http 127.0.0.1:9800
{{exe}} -home ~/sharetoken-second status
```
:::

::: windows
```powershell
{{exe}} -home "$HOME\sharetoken-second" login -email other@example.com
{{exe}} -home "$HOME\sharetoken-second" up -http 127.0.0.1:9800
{{exe}} -home "$HOME\sharetoken-second" status
```
:::

You can also set the environment variable `SHARETOKEN_HOME`.

## Files in the directory {#files}

| File | Contents |
|---|---|
| `config.json` | Settings and the sign-in token. Readable only by you |
| `device.key` | The device private key. Readable only by you. On another machine it would be a new device |
| `network.json` | A cache of the relay list. If you delete it, it is fetched again automatically |
| `run.json` | The API address of the running client and the key of its management API |
| `apikey.json` | The OpenAI endpoint's API key and whether it is required (`{{exe}} apikey`) |
| `daemon.log` | The log of the service started by the desktop app |

> [!WARNING]
> `config.json` and `device.key` act as this device's sign-in credentials. Do not share them, commit them to a code repository, or put them in a backup others can read. Losing them does not lose any credit: just sign in again. Once the device key changes, it is a new device, and you can unbind the old device on the portal.

## View and change the configuration {#edit}

```bash
{{exe}} config
```

This command shows the file path and contents. To change a setting, open `config.json` in a text editor, then **restart the client** for it to take effect. Sharing settings are the exception. See [Sharing settings](#share).

The file is strict JSON and cannot contain comments. The examples below list only the fields you would change.

## Settings for calling models {#buy}

```json
{
  "buy": {
    "budget_micros": 2000000,
    "max_price_in": 0,
    "max_price_out": 0,
    "evidence": true,
    "require_tee": false,
    "require_native": false,
    "spot_checks": true,
    "queue_seconds": 30,
    "first_byte_seconds": 30,
    "idle_seconds": 30
  }
}
```

| Field | Meaning | Default |
|---|---|---|
| `budget_micros` | The most to hold for each request, in micro-credit. Whatever is unused is returned | `2000000` (2 credits) |
| `max_price_in` | The most you will pay per million input tokens. `0` means no limit | `0` |
| `max_price_out` | The most you will pay per million output tokens. `0` means no limit | `0` |
| `evidence` | If a node overstates usage, hand over the key for that job to get an automatic refund | `true` |
| `require_tee` | Send requests only to TEE nodes | `false` |
| `require_native` | Send requests only to nodes whose model server speaks the request's API natively, never converted ([Native and converted](/docs/use-models#native)). A request can also ask for it with the header `X-Sharetoken-Require-Native: true` | `false` |
| `spot_checks` | Help the platform spot-check sellers. The platform checks at random that sellers really run the model they advertise; with this on, the client now and then carries one short check to a seller, the way it sends your own requests, so the seller cannot tell a check from a buyer. The request and the answer are encrypted (your machine cannot read them) and it costs no credit. The seller sees your device's address as it does for any request you send | `true` |
| `queue_seconds` | How long to wait when all nodes are full. A negative value fails immediately | `30` |
| `first_byte_seconds` | Time limit for the node's first frame. On timeout, another node is used. Nodes send a progress frame every 10 seconds while the model is thinking, so slow models do not need a higher value | `30` |
| `idle_seconds` | Time limit between two frames. On timeout, the job is judged stuck. A job that gets only progress frames and no output for 5 minutes is judged stuck too | `30` |

## Sharing settings {#share}

```json
{
  "share": {
    "enabled": true,
    "upstream": "http://127.0.0.1:11434/v1",
    "upstream_key_env": "",
    "concurrency": 2,
    "relays": 2,
    "models": [
      { "id": "gemma-4-e4b", "upstream_model": "gemma4:e4b", "price_in": 100000, "price_out": 200000 }
    ],
    "tee": "off"
  }
}
```

| Field | Meaning | Default |
|---|---|---|
| `enabled` | Whether to take jobs | `false` |
| `upstream` | The OpenAI-compatible address of the local model server | Ollama: `http://127.0.0.1:11434/v1` |
| `upstream_key_env` | The name of the environment variable that holds the local model server's API key | Empty |
| `concurrency` | Number of requests handled at the same time | `2` |
| `relays` | How many relays to register on | `2` |
| `models` | The models you share. Usually managed with `share add`. A model may also set `price_cache_read` and `price_cache_write`, the prices of input tokens read from and written to the prompt cache; see [cache prices](/docs/share#cache-prices) | Empty |
| `tee` | Confidential VM type: `off`, `auto`, `sev-snp`, `tdx` | `off` |

Hosted APIs you share (providers) are not in this file: they are kept, with their API keys and logins, in `providers.json` in the same directory. Manage them with the [`provider` command](/docs/share#providers) rather than editing the file.

When you change settings with the `share` subcommands, the running client applies them immediately. After you edit the file by hand, run `{{exe}} share on` once more to reload it.

## Network settings {#network}

| Field | Meaning | Default |
|---|---|---|
| `http` | The address of the local API and the OpenAI endpoint. Keep it on a local address | `127.0.0.1:{{port}}` |
| `listen` | libp2p listen addresses | All network interfaces, IPv4 and IPv6, TCP and QUIC |
| `nat` | UPnP, NAT-PMP and hole punching | `true` |
| `relays` | If not empty, overrides the relay list from the portal | Empty |
