# 调用模型

客户端运行时在本机提供 OpenAI 兼容端点。你的请求加密后只发给选中的节点，响应附带节点的签名收据，客户端自动核对。

## 本地端点 {#endpoint}

| 设置 | 值 |
|---|---|
| Base URL | `http://127.0.0.1:{{port}}/v1` |
| API key | 本客户端的 key，`sk-st-…`：`{{exe}} apikey -q` 会打印它，桌面应用在端点旁边也有 |
| 接口 | `POST /v1/chat/completions`、`GET /v1/models` |

先确认 `{{exe}} up` 在运行，或者[在后台运行](/docs/running#background)。

这个 key 防止本机的其他程序和用户通过端点花你的 credit。它一直不变，直到你生成新的，所以工具里只需填一次：

::: macos linux
```bash
export OPENAI_API_KEY=$({{exe}} apikey -q)
```
:::

::: windows
```powershell
$env:OPENAI_API_KEY = ({{exe}} apikey -q)
```
:::

| 命令 | 作用 |
|---|---|
| `{{exe}} apikey` | 显示端点、key，以及是否需要 key |
| `{{exe}} apikey rotate` | 生成新 key，旧 key 立即失效 |
| `{{exe}} apikey off` | 不带 key 也能调用。只适合没有别人使用的机器 |
| `{{exe}} apikey on` | 重新要求 key |

桌面应用在**设置**里有同样的开关和**重新生成**。正在运行的客户端会立即生效。不带 key 时端点返回 401 `invalid_api_key`；key 也可以放在 `x-api-key` 请求头里。

## 看有哪些模型 {#models}

```bash
{{exe}} models
```

```text
MODEL        NODES  TEE  MIN IN/Mtok  MIN OUT/Mtok
gemma-4-e4b  2      0    100000       200000
```

`NODES` 是在线节点数，`TEE` 是其中的 TEE 节点数，价格单位是 micro-credit / 百万 token。上面的价格换算下来，每百万输入 token 0.1 credit，每百万输出 token 0.2 credit。

## 发一个请求 {#request}

::: macos linux
```bash
curl http://127.0.0.1:{{port}}/v1/chat/completions \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gemma-4-e4b","messages":[{"role":"user","content":"Hello"}],"max_tokens":200}'
```

加 `-i` 可以看到响应头。
:::

::: windows
PowerShell 里引号容易出错，用 `Invoke-RestMethod` 最省事。

```powershell
$body = @{
  model = 'gemma-4-e4b'
  max_tokens = 200
  messages = @(@{ role = 'user'; content = 'Hello' })
} | ConvertTo-Json -Depth 5
$r = Invoke-RestMethod http://127.0.0.1:{{port}}/v1/chat/completions -Method Post -Headers @{ Authorization = "Bearer $env:OPENAI_API_KEY" } -ContentType 'application/json; charset=utf-8' -Body ([Text.Encoding]::UTF8.GetBytes($body))
$r.choices[0].message.content
```

想看响应头，把 `Invoke-RestMethod` 换成 `Invoke-WebRequest`，再读结果的 `.Headers`。
:::

## 接入 Python {#python}

::: macos linux
```bash
pip install openai
```
:::

::: windows
```powershell
py -m pip install openai
```
:::

```python
from openai import OpenAI

# api_key comes from $OPENAI_API_KEY (see above)
client = OpenAI(base_url="http://127.0.0.1:{{port}}/v1")

reply = client.chat.completions.create(
    model="gemma-4-e4b",
    messages=[{"role": "user", "content": "Explain quantum tunnelling in one sentence"}],
    max_tokens=500,
)
print(reply.choices[0].message.content)

stream = client.chat.completions.create(
    model="gemma-4-e4b",
    messages=[{"role": "user", "content": "Write a short poem"}],
    max_tokens=500,
    stream=True,
)
for chunk in stream:
    if chunk.choices and chunk.choices[0].delta.content:
        print(chunk.choices[0].delta.content, end="", flush=True)
```

## 接入 Node.js {#node}

```bash
npm install openai
```

```javascript
import OpenAI from "openai";

// apiKey comes from $OPENAI_API_KEY (see above)
const client = new OpenAI({ baseURL: "http://127.0.0.1:{{port}}/v1" });

const reply = await client.chat.completions.create({
  model: "gemma-4-e4b",
  messages: [{ role: "user", content: "Hello" }],
  max_tokens: 500,
});
console.log(reply.choices[0].message.content);
```

## 接入其他工具 {#tools}

支持自定义 OpenAI base URL 的工具都能用。不少工具读取这两个环境变量：

::: macos linux
```bash
export OPENAI_BASE_URL=http://127.0.0.1:{{port}}/v1
export OPENAI_API_KEY=$({{exe}} apikey -q)
```
:::

::: windows
```powershell
$env:OPENAI_BASE_URL = 'http://127.0.0.1:{{port}}/v1'
$env:OPENAI_API_KEY = ({{exe}} apikey -q)
```
:::

网页里的工具如果带着非本机的 `Origin` 调用，端点会返回 403，这是为了防止恶意网页花你的 credit。

想让 Claude Code 或 Codex 替你查看客户端、调用模型、管理共享，见[在编程助手中使用](/docs/agents)。

## 如何计费 {#billing}

- **按实际 token 结算：** 价格由节点报出，你按收据上的 token 数付费。
- **冻结：** 发请求时先冻结一笔钱，结束后退回没用完的部分。请求带了 `max_tokens` 时只按最坏情况冻结，不带时冻结整个预算（默认 0.2 credit）。
- **建议总是设置 `max_tokens`**，尤其是批量或高并发调用，否则容易因为冻结额过大而返回 `insufficient_funds`。
- **中途断开：** 流式请求中途断开时，只按已送达的部分计费。

预算和价格上限在[配置文件](/docs/config#buy)里调整。

## 响应如何验证 {#verify}

非流式响应在收据验证通过后才交给你。流式响应边到边转发，结尾附上验证结果。响应头：

| 响应头 | 含义 |
|---|---|
| `X-Sharetoken-Verified` | 收据是否验证通过 |
| `X-Sharetoken-Job` | 作业 ID，查账和反馈时使用 |
| `X-Sharetoken-Node` | 服务这次请求的节点 |
| `X-Sharetoken-Cost-Micros` | 这次的费用，单位 micro-credit |

节点虚报用量时，客户端拒签，并交出这一单的密钥作为证据，裁决后自动退款。这个行为由配置项 `buy.evidence` 控制，默认开启。

## 只用 TEE 节点 {#tee}

普通节点的运营者理论上能读到 prompt。敏感内容请只发给 TEE 节点，它们运行在机密虚拟机里，运营者读不到内存。在[配置文件](/docs/config#buy)里设置后重启客户端：

```json
{
  "buy": { "require_tee": true }
}
```

没有符合条件的节点时，请求会失败，而不是退回普通节点。`models` 的 `TEE` 列显示现有的 TEE 节点数。

## 只向认识的卖家购买 {#pairing}

默认情况下，你的请求会发给协调器选中的任意节点。如果只想发给你认识的卖家，就和他们配对。卖家给你一个配对码，你用它配对：

```bash
{{exe}} pair join ABCD-EFGH-IJKL-MNOP
```

此后这个账户只向已配对的卖家购买，名下所有设备都一样。`models` 和 `/v1/models` 也只列出他们的模型。

| 操作 | 命令 |
|---|---|
| 查看你配对的卖家 | `{{exe}} pair` |
| 解除和某个卖家的配对 | `{{exe}} pair leave 42` |

- **上限：** 同时最多和三个卖家配对。想换一个，先解除其中一个。
- **卖家不在时：** 已配对的卖家都不提供这个模型，或者都不在线、都满载时，请求会以 `no_offers` 或 `capacity` 失败，不会转给别的卖家。
- **恢复为向整个网络购买：** 解除所有卖家之后，请求又可以发给任意节点。
- **什么不变：** 价格、计费和收据校验都不变。已配对的卖家只要不是 TEE 节点，仍然能读到 prompt。

桌面应用的**概览 → 配对的卖家**和门户的[配对页面](/app/#/pairing)可以做同样的事。卖家一侧的说明见[和买家配对](/docs/share#pairing)。

## 错误和重试 {#errors}

错误统一为 `{"error":{"code","message"}}`。

| 错误码 | HTTP | 含义和处理 |
|---|---|---|
| `no_offers` | 503 | 没有节点提供这个模型。检查模型 ID，或换一个模型。如果你已经[配对](#pairing)，只有已配对的卖家算数 |
| `capacity` | 503 | 所有节点都满载，等待 30 秒后仍无空位。按 `Retry-After` 重试 |
| `insufficient_funds` | 402 | 余额不够冻结。设置更小的 `max_tokens`，或者赚一些 credit |
| `budget` | 400 | 这次请求的费用可能超出 `buy.budget_micros`。调小 `max_tokens` 或调大预算 |
| `bad_request` | 400 | 请求无效，消息里有原因 |
| `upstream_error`、`receipt_rejected` | 502 | 节点出错，或者收据没有通过验证。可以重试 |
| `unauthorized`、`forbidden` | 403 | 登录失效或账户被禁用。重新 `login` |
| `upgrade_required` | 426 | 客户端版本过低，见[升级](/docs/update#upgrade) |
| `not_connected` | 503 | 客户端还没连上网络，稍等或查看 `status` |

慢模型处理长 prompt 时可能被判定为卡死。超时设置见[配置文件](/docs/config#buy)。

思考型模型的推理过程也计入 `max_tokens`。`max_tokens` 太小时 `content` 可能为空，只有 `reasoning_content`。
