# 本地 API

运行中的客户端在 `http://127.0.0.1:{{port}}` 上提供两组接口：`/v1/` 是模型接口（OpenAI 和 Anthropic 的接口），`/api/v1/` 用来查询和管理客户端。

只接受本机请求。带非本机 `Origin` 的请求返回 403。管理接口还需要 key（见 [Key](#key)）。错误的格式是 `{"error":{"code","message"}}`（`/v1/messages` 上是 Anthropic 的格式）。

## 模型接口 {#openai}

| 接口 | 说明 |
|---|---|
| `POST /v1/chat/completions` | 用 OpenAI chat completions 接口调用模型，支持 `stream`、`max_tokens` |
| `POST /v1/messages` | 用 Anthropic Messages 接口调用模型（Claude Code） |
| `POST /v1/messages/count_tokens` | 在本机估算一个 Messages 请求的输入 token |
| `POST /v1/responses` | 用 OpenAI Responses 接口调用模型（Codex） |
| `GET /v1/responses` | 走 WebSocket 的 Responses 接口（Codex 的 `supports_websockets`） |
| `POST /v1/responses/compact` | 压缩 Codex 的对话 |
| `GET /v1/models` | 网络上的模型，以及原生支持的接口（`apis`）；带 `?client_version=` 时返回 Codex 的模型清单 |

端点会读取的请求头：`X-Sharetoken-Require-Native: true`（只用不需要转换的节点）、`X-Sharetoken-Node: <节点 ID>`（只用这个节点）。

用法见[调用模型](/docs/use-models)。

## 管理接口 {#admin}

| 接口 | 说明 |
|---|---|
| `GET /api/v1/status` | 连接、中继、共享和告警的完整状态 |
| `GET /api/v1/models` | 模型、节点数、TEE 节点数和最低价 |
| `GET /api/v1/account` | 余额、用量和设备 |
| `GET /api/v1/jobs?role=buy&limit=20` | 最近的作业 |
| `GET /api/v1/ledger?limit=20` | 账单 |
| `POST /api/v1/relays/measure` | 立即重新测速，返回结果 |
| `GET /api/v1/share` | 读取共享设置 |
| `PUT /api/v1/share` | 修改共享设置，校验后立即生效并保存 |
| `POST /api/v1/share/start`、`POST /api/v1/share/stop` | 打开或暂停共享 |
| `POST /api/v1/share/reload` | 从 `config.json` 重新加载共享设置 |
| `GET /api/v1/share/detect` | 列出本机模型服务的模型 |
| `GET /api/v1/pairing` | 你配对的卖家、你的买家、你的配对码，以及私有模式是否开启 |
| `POST /api/v1/pairing/redeem` | 买家：和一个卖家配对。请求体：`{"code": "…"}` |
| `DELETE /api/v1/pairing/sellers/{id}` | 买家：解除和某个卖家的配对 |
| `POST /api/v1/pairing/codes`、`DELETE /api/v1/pairing/codes/{id}` | 卖家：生成配对码（`label`、`ttl_hours`、`max_uses`），或撤销一个 |
| `DELETE /api/v1/pairing/buyers/{id}` | 卖家：移除一个买家 |
| `PUT /api/v1/pairing/private` | 卖家：开启或关闭私有模式。请求体：`{"enabled": true}` |
| `GET /api/v1/sharing` | 你的共享伙伴及最近 24 小时双方互相用了多少 tokens、未使用的邀请码和你的共享设置 |
| `POST /api/v1/sharing/invites`、`DELETE /api/v1/sharing/invites/{id}` | 生成一个只能一人使用的邀请码（`ttl_hours`，最长 168），或撤销一个 |
| `POST /api/v1/sharing/join` | 成为邀请码生成者的共享伙伴。请求体：`{"code": "…"}` |
| `DELETE /api/v1/sharing/partners/{id}` | 停止和某个伙伴共享 |
| `PUT /api/v1/sharing/settings` | 只用伙伴、每日上限和并发比例。请求体：`{"only_partners": false, "daily_limit_tokens": 0, "share_pct": 100}` |

## Key {#key}

客户端每次启动都会生成新的 key，写进数据目录下只有你能读的 `run.json`（字段 `key`）。访问 `/api/v1/` 的每个请求都要带上 `Authorization: Bearer <key>`，否则返回 401 `local_auth`。`{{exe}}` 命令和桌面应用会自己读取。`/v1/` 用的是另一把不随启动变化的 API key（`{{exe}} apikey`；`GET /api/v1/apikey`、带 `{"required": bool}` 的 `PUT /api/v1/apikey`、`POST /api/v1/apikey/rotate`）。

## 示例 {#examples}

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

## 安全 {#security}

> [!WARNING]
> `/v1/` 需要客户端的 API key（见[调用模型](/docs/use-models#endpoint)），`/api/v1/` 需要 `run.json` 里的 key。如果关掉 API key（`{{exe}} apikey off`），本机别的用户就能调用 `/v1/` 花你的 credit。请不要把 `http` 改成非本机地址，也不要把端口转发出去。
