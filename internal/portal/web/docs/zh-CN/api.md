# 本地 API

运行中的客户端在 `http://127.0.0.1:{{port}}` 上提供两组接口：`/v1/` 是 OpenAI 兼容接口，`/api/v1/` 用来查询和管理客户端。

只接受本机请求。带非本机 `Origin` 的请求返回 403。错误统一为 `{"error":{"code","message"}}`。

## OpenAI 兼容接口 {#openai}

| 接口 | 说明 |
|---|---|
| `POST /v1/chat/completions` | 调用模型，支持 `stream`、`max_tokens` |
| `GET /v1/models` | 网络上的模型 |

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

## 示例 {#examples}

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

## 安全 {#security}

> [!WARNING]
> 这些接口对本机其他进程没有认证。多用户机器上，别的用户可以调用 `/v1` 花你的 credit，也可以修改共享设置。请不要把 `http` 改成非本机地址，也不要把端口转发出去。
