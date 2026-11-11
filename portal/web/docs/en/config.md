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
{{exe}} -home ~/sharetoken-second login -user other@example.com
{{exe}} -home ~/sharetoken-second up -http 127.0.0.1:9800
{{exe}} -home ~/sharetoken-second status
```
:::

::: windows
```powershell
{{exe}} -home "$HOME\sharetoken-second" login -user other@example.com
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
| `run.json` | The API address of the running client |
| `daemon.log` | The log of the service started by the desktop app |
| `telemetry.json` | The usage statistics switch and the install ID |

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
    "budget_micros": 200000,
    "max_price_in": 0,
    "max_price_out": 0,
    "evidence": true,
    "require_tee": false,
    "queue_seconds": 30,
    "first_byte_seconds": 120,
    "idle_seconds": 60
  }
}
```

| Field | Meaning | Default |
|---|---|---|
| `budget_micros` | The most to hold for each request, in micro-credit. Whatever is unused is returned | `200000` (0.2 credit) |
| `max_price_in` | The most you will pay per million input tokens. `0` means no limit | `0` |
| `max_price_out` | The most you will pay per million output tokens. `0` means no limit | `0` |
| `evidence` | If a node overstates usage, hand over the key for that job to get an automatic refund | `true` |
| `require_tee` | Send requests only to TEE nodes | `false` |
| `queue_seconds` | How long to wait when all nodes are full. A negative value fails immediately | `30` |
| `first_byte_seconds` | Time limit for the node's first data frame. On timeout, another node is used. Raise it for slow models | `120` |
| `idle_seconds` | Time limit between two frames. On timeout, the job is judged stuck. Raise it for slow models | `60` |

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
| `models` | The models you share. Usually managed with `share add` | Empty |
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
