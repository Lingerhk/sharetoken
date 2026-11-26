# 本地 API

运行中的客户端在 `http://127.0.0.1:{{port}}` 上提供两组接口：`/v1/` 是模型接口（OpenAI 和 Anthropic 的接口），`/api/v1/` 用来查询和管理客户端。

只接受发给本机地址（`127.0.0.1`、`localhost`）的请求。带非本机 `Origin` 的请求返回 403，所以网页不能调用它们。管理接口还需要 key（见 [Key](#key)）。错误的格式是 `{"error":{"code","message"}}`（`/v1/messages` 上是 Anthropic 的格式）。

## 模型接口 {#openai}

| 接口 | 说明 |
|---|---|
| `POST /v1/chat/completions` | 用 OpenAI chat completions 接口调用模型，支持 `stream`、`max_tokens` |
| `POST /v1/messages` | 用 Anthropic Messages 接口调用模型（Claude Code） |
| `POST /v1/messages/count_tokens` | 在本机估算一个 Messages 请求的输入 token |
| `POST /v1/responses` | 用 OpenAI Responses 接口调用模型（Codex） |
| `GET /v1/responses` | 走 WebSocket 的 Responses 接口（Codex 的 `supports_websockets`） |
| `POST /v1/responses/compact` | 压缩 Codex 的对话 |
| `GET /v1/models` | 网络上的模型（ID 前加 `st-`），以及原生支持的接口（`apis`）、上下文窗口和推理档位；带 `?client_version=` 时返回 Codex 的模型清单 |

端点会读取的请求头：`X-Sharetoken-Require-Native: true`（只用不需要转换的节点）、`X-Sharetoken-Node: <节点 ID>`（只用这个节点）。网络上没有 embeddings 和图片生成接口。

带着 Claude Code 或 Codex 自己的官方登录（或 Anthropic、OpenAI 的 API key）的请求，模型不以 `st-` 开头时原样转发给厂商，见[同时使用订阅](/docs/use-models#subscription)。

用法见[调用模型](/docs/use-models)。

## 管理接口 {#admin}

带请求体的请求要设 `Content-Type: application/json`。

| 接口 | 说明 |
|---|---|
| `GET /api/v1/status` | 连接、中继、共享和告警的完整状态 |
| `GET /api/v1/models` | 模型（不带 `st-`）、节点数、TEE 节点数、最低价和原生接口 |
| `GET /api/v1/account` | 余额、用量和设备 |
| `GET /api/v1/jobs?role=buy&limit=20` | 最近的作业（`page` 翻页） |
| `GET /api/v1/ledger?limit=20` | 账单 |
| `GET /api/v1/buy`、`PUT /api/v1/buy` | 调用设置：`{"budget_micros": 2000000, "require_native": false}` |
| `POST /api/v1/relays/measure` | 立即重新测速，返回结果 |
| `POST /api/v1/relays/private`、`DELETE /api/v1/relays/private` | 添加或移除[私有中继](/docs/relay)：`{"addr": "<地址>"}`；移除时也可以给中继的 ID |
| `GET /api/v1/tailnet`、`PUT /api/v1/tailnet` | [直连隧道](/docs/running#tailnet)：`enabled`、`transport`、`wait_ms`、`derp_map`，没给的字段不变 |
| `POST /api/v1/tailnet/reload` | 从 `config.json` 重新加载隧道设置 |
| `GET /api/v1/share` | 读取共享设置 |
| `PUT /api/v1/share` | 修改共享设置，校验后立即生效并保存 |
| `POST /api/v1/share/start`、`POST /api/v1/share/stop` | 打开或暂停共享 |
| `POST /api/v1/share/reload` | 从 `config.json` 重新加载共享设置 |
| `GET /api/v1/share/detect` | 列出本机模型服务的模型 |
| `GET /api/v1/providers` | 可以添加的托管 API 种类，以及已添加的 provider |
| `POST /api/v1/providers`、`PATCH /api/v1/providers/{name}`、`DELETE /api/v1/providers/{name}` | 用 API key 添加、修改或删除一个 provider |
| `GET /api/v1/providers/{name}/models`、`PUT /api/v1/providers/{name}/models`、`DELETE /api/v1/providers/{name}/models/{id}` | 它的凭据能用的模型；出售或改价一个模型；停止出售 |
| `POST /api/v1/providers/{name}/test` | 发一个小请求试试：`{"model": "…", "prompt": "…"}` |
| `GET /api/v1/pairing` | 你配对的卖家、你的买家、你的配对码、等待审批的请求，以及私有模式是否开启 |
| `POST /api/v1/pairing/redeem` | 买家：和一个卖家配对。请求体：`{"code": "…"}`，也可以是邀请链接 |
| `DELETE /api/v1/pairing/sellers/{id}` | 买家：解除和某个卖家的配对 |
| `POST /api/v1/pairing/codes`、`DELETE /api/v1/pairing/codes/{id}` | 卖家：生成配对码（`label`、`ttl_hours`，最长 168；`max_uses`，0 表示不限；`approval`），或撤销一个 |
| `DELETE /api/v1/pairing/buyers/{id}` | 卖家：移除一个买家 |
| `PUT /api/v1/pairing/private` | 卖家：开启或关闭私有模式。请求体：`{"enabled": true}` |
| `GET /api/v1/sharing` | 你的共享伙伴及最近 24 小时双方互相用了多少 tokens、未使用的邀请码、等待审批的请求和你的共享设置 |
| `POST /api/v1/sharing/invites`、`DELETE /api/v1/sharing/invites/{id}` | 生成一个邀请码（`ttl_hours`，最长 168；`max_uses`，1 到 100，默认一人；`approval`），或撤销一个 |
| `POST /api/v1/sharing/join` | 成为邀请码生成者的共享伙伴。请求体：`{"code": "…"}`，也可以是邀请链接 |
| `DELETE /api/v1/sharing/partners/{id}` | 停止和某个伙伴共享 |
| `PUT /api/v1/sharing/settings` | 只用伙伴、每日上限和并发比例。请求体：`{"only_partners": false, "daily_limit_tokens": 0, "share_pct": 100}` |
| `POST /api/v1/pairing/requests/{id}/approve`、`DELETE /api/v1/pairing/requests/{id}` | 批准或拒绝等你审批的配对请求；共享伙伴的请求把 `pairing` 换成 `sharing` |
| `DELETE /api/v1/pairing/waiting/{id}` | 撤回你自己发出、还在等审批的请求（`sharing` 同样） |
| `POST /api/v1/invite/accept` | 接受一个[邀请链接](/docs/share#invite-links)或任意一种邀请码：`{"link": "…"}` |
| `GET /api/v1/connect`、`POST /api/v1/connect` | Claude Code 和 Codex 的接入状态；接入或断开一个：`{"tool": "claude", "subscription": true, "openai_provider": false, "force": false, "off": false}` |
| `GET /api/v1/official`、`PUT /api/v1/official` | 订阅用量和额度用完后的处理。`PUT` 整组替换：`{"claude_fallback_off": false, "claude_fallback_model": "", "codex_guard_percent": 95, "codex_unlock": false}`。`codex_unlock` 只有在解锁用的证书已被系统信任后才能在这里打开，下一行的接口会完成这一步 |
| `POST /api/v1/official/unlock` | 打开或关闭 Codex Desktop 解锁：`{"on": true}` |
| `GET /api/v1/autostart`、`PUT /api/v1/autostart` | 登录系统时自动启动客户端：`{"on": true}` |
| `GET /api/v1/warmup`、`PUT /api/v1/warmup` | 预热的状态和设置：`paused`、`claude`、`codex`，没给的不变 |
| `PUT /api/v1/warmup/providers/{name}` | 一个 provider 的预热（`times`、`on_reset`、`model`）；空的请求体关闭它 |
| `POST /api/v1/warmup/run` | 立即预热：`{"target": "local:claude"}`，或 `local:codex`、`provider:<名字>` |
| `GET /api/v1/diagnostics`、`PUT /api/v1/diagnostics` | 崩溃报告的开关：`{"upload": true}` |
| `GET /api/v1/diagnostics/reports` | 等待发送的崩溃报告（已脱敏） |

需要连上网络的接口，在客户端没登录时返回 401 `not_logged_in`，还在连接时返回 503 `not_connected`。`official`、`connect`、`autostart`、`apikey`、`warmup` 和 `diagnostics` 在桌面应用和后台服务连上网络之前就能用；账户、作业、账单、`buy` 和 `tailnet` 只需要已登录。`providers` 接口只接受不带 `Origin` 的请求和客户端自己的页面。

桌面应用自己用的其他接口（登录、升级、反馈等）不在这里列出，可能随版本变化。

## Key {#key}

客户端每次启动都会生成新的 key，写进数据目录下只有你能读的 `run.json`（字段 `key`）。访问 `/api/v1/` 的每个请求都要带上 `Authorization: Bearer <key>`，否则返回 401 `local_auth`。`{{exe}}` 命令和桌面应用会自己读取。

`/v1/` 用的是另一把不随启动变化的 API key（`sk-st-…`，存在数据目录的 `apikey.json` 里，见[本地端点](/docs/use-models#endpoint)）。管理它的接口：

| 接口 | 说明 |
|---|---|
| `GET /api/v1/apikey` | `{"required": true, "key": "sk-st-…"}` |
| `PUT /api/v1/apikey` | 要求或不要求 key：`{"required": false}` |
| `POST /api/v1/apikey/rotate` | 生成新 key，旧 key 立即失效 |

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
