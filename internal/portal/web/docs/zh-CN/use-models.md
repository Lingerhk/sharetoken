# 调用模型

客户端运行时在本机提供一个端点，同时支持 OpenAI（chat completions 和 Responses）和 Anthropic（Messages）的接口，OpenAI 客户端、Claude Code 和 Codex 都能直接接入。你的请求加密后只发给选中的节点，响应附带节点的签名收据，客户端自动核对。

## 本地端点 {#endpoint}

| 设置 | 值 |
|---|---|
| Base URL | `http://127.0.0.1:{{port}}/v1` |
| API key | 本客户端的 key，`sk-st-…`：`{{exe}} apikey -q` 会打印它，桌面应用在端点旁边也有 |
| 接口 | `POST /v1/chat/completions`（OpenAI）、`POST /v1/messages`（Anthropic，Claude Code）、`POST /v1/responses`（OpenAI Responses，Codex；也支持 WebSocket）、`GET /v1/models` |

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

## 接入 Claude Code {#claude-code}

Claude Code 用 Anthropic Messages 接口访问端点。把它指向客户端，模型从 `{{exe}} models` 里选：

::: macos linux
```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:{{port}}
export ANTHROPIC_AUTH_TOKEN=$({{exe}} apikey -q)
export ANTHROPIC_MODEL=claude-sonnet-4-6          # a model ID from `{{exe}} models`
export ANTHROPIC_SMALL_FAST_MODEL=claude-sonnet-4-6  # for Claude Code's background tasks
claude
```
:::

::: windows
```powershell
$env:ANTHROPIC_BASE_URL = 'http://127.0.0.1:{{port}}'
$env:ANTHROPIC_AUTH_TOKEN = ({{exe}} apikey -q)
$env:ANTHROPIC_MODEL = 'claude-sonnet-4-6'
$env:ANTHROPIC_SMALL_FAST_MODEL = 'claude-sonnet-4-6'
claude
```
:::

工具调用、thinking 和 prompt 缓存都和直连 Anthropic 一样。网络上的任何模型都能用，不限于 Claude：模型服务器不支持 Messages 接口的节点会转换请求（见[原生和转换](#native)）。对不认识的模型 ID，Claude Code 会提示它按 100 万 token 的上下文窗口处理。`/v1/messages/count_tokens` 在本机估算，不发到网络。

> [!TIP]
> 编程助手的 prompt 很长，每个请求都有几万 token。请求返回 `budget` 错误，说明每个请求的冻结额按模型价格覆盖不了 prompt；在[配置文件](/docs/config#buy)里调高 `buy.budget_micros`（默认 `2000000`，即 2 credit），或在桌面应用的“设置 > 调用 > 每个请求的预算”里修改。实际只按用量扣费。

## 接入 Codex {#codex}

Codex 用 OpenAI Responses 接口访问端点。在 `~/.codex/config.toml` 里加一个 provider：

```toml
model = "gpt-5.5"              # a model ID from `{{exe}} models`
model_provider = "sharetoken"

[model_providers.sharetoken]
name = "sharetoken"
base_url = "http://127.0.0.1:{{port}}/v1"
env_key = "SHARETOKEN_API_KEY"
wire_api = "responses"
supports_websockets = true     # optional: one connection for the whole session
```

Codex 只从目录文件里读取自定义 provider 的模型信息（上下文窗口、推理档位、何时压缩）；没有这个文件时，它会提示 "Model metadata … not found" 并自行猜测。把网络上的模型写进一个目录文件，并在 `config.toml` 顶层指向它：

```bash
{{exe}} models -codex ~/.codex/sharetoken-models.json
```

```toml
model_catalog_json = "/Users/you/.codex/sharetoken-models.json"   # the path the command printed
```

Codex 只在启动时读这个文件，用的模型有变化时请重新运行一次命令。

然后把 key 交给 Codex 并启动：

::: macos linux
```bash
export SHARETOKEN_API_KEY=$({{exe}} apikey -q)
codex
```
:::

::: windows
```powershell
$env:SHARETOKEN_API_KEY = ({{exe}} apikey -q)
codex
```
:::

工具调用（包括 `apply_patch`）、推理和上下文压缩，对网络上的任何模型都能用。走 WebSocket 时，Codex 每次只发新增的内容；节点在请求之间不保存任何东西，所以由客户端记住这条连接上的对话，再整段发出。走 HTTP 时客户端同样支持 `previous_response_id`（供 OpenAI Agents SDK 等工具使用）：它在内存里按 API key 保存最近的回答一小时，再把它们组成的对话整段发出；不认识的 id 返回 `previous_response_not_found`，`store: false` 的请求不保存。Codex 的 `/v1/responses/compact` 也支持：ChatGPT 登录的节点原生压缩，其他节点让模型写一段摘要。和 Claude Code 一样，请求返回 `budget` 错误时请调高 `buy.budget_micros`。

想让 Claude Code 或 Codex 替你查看客户端、调用模型、管理共享，见[在编程助手中使用](/docs/agents)。

## 原生和转换 {#native}

每个节点都支持三种接口。模型服务器原生支持请求所用接口的节点，原样转发；其他节点会转换请求，做法和 sub2api 等网关一样：

| 卖家的模型服务器 | 原生接口 |
|---|---|
| Anthropic（API key 或 Claude 登录） | Messages |
| OpenAI API key | chat completions、Responses |
| ChatGPT（Codex）登录 | Responses |
| DeepSeek API key | chat completions、Messages |
| Gemini、Grok 和其他 OpenAI 兼容服务器 | chat completions |

- **原生优先：** 只要有空闲的原生节点，就优先匹配它，即使转换节点更便宜。`/v1/models` 的 `apis` 字段列出每个模型的原生接口。
- **只要原生：** 在桌面应用的“设置 > 调用”里勾选“只用原生支持请求接口的卖家”，或在[配置文件](/docs/config#buy)里设置 `buy.require_native`，或给单个请求加请求头 `X-Sharetoken-Require-Native: true`。没有这样的节点时请求直接失败（`no_offers`），不会转换。
- **转换会丢掉什么：** 加密推理和 thinking 签名只对生成它们的模型有效，对话换到另一类模型时会被丢弃；服务端工具（联网搜索、代码执行）任何节点都不提供。
- **只用某个节点：** 请求头 `X-Sharetoken-Node: <节点 ID>` 只把请求发给这个节点，方便试用某个卖家。

回答在收到任何内容之前失败时，客户端照常换别的节点重试。

## 如何计费 {#billing}

- **按实际 token 结算：** 价格由节点报出，你按收据上的 token 数付费。
- **prompt 缓存：** 模型服务器从 prompt 缓存读出的输入 token 按节点的缓存读价格计费（默认是输入价格的 1/10），写入缓存的按缓存写价格计费（Anthropic 模型默认是输入价格的 1.25 倍，其他模型等于输入价格）。收据分别记下这两部分，门户的作业列表用 `*` 标出这类作业。
- **看不到的推理：** 有些模型的推理不完整展示（加密、只给摘要或不显示）。回答显示模型做了推理时，按模型服务器报告的输出 token 计费，上限是请求的输出上限（`max_tokens`、`max_output_tokens`）或冻结额能覆盖的量；否则输出按你收到的文本核对。
- **冻结：** 发请求时先冻结一笔钱，结束后退回没用完的部分。请求带了 `max_tokens` 时只按最坏情况冻结，不带时冻结整个预算（默认 2 credit）；余额不足预算时，只要够付 prompt 和一段简短回答，就冻结现有余额。
- **建议总是设置 `max_tokens`**，尤其是批量或高并发调用，否则容易因为冻结额过大而返回 `insufficient_funds`。
- **中途停止：** 你的程序中途断开流式请求（比如点了停止），节点会立即停止生成，只按已送达的部分计费。
- **网络中断：** 中继重启、切换网络等原因导致连接中断时，客户端会在 60 秒内自动接上，回答从断开的地方继续，不需要重发请求。

预算和价格上限在[配置文件](/docs/config#buy)里调整。

## 响应如何验证 {#verify}

非流式响应在收据验证通过后才交给你。流式响应边到边转发，结尾附上验证结果。响应头：

| 响应头 | 含义 |
|---|---|
| `X-Sharetoken-Verified` | 收据是否验证通过 |
| `X-Sharetoken-Job` | 作业 ID，查账和反馈时使用 |
| `X-Sharetoken-Node` | 服务这次请求的节点 |
| `X-Sharetoken-Cost-Micros` | 这次的费用，单位 micro-credit |
| `X-Sharetoken-Timing` | 耗时和流量：`ticket_ms`（撮合）、`dial_ms`（连接节点）、`first_byte_ms`（首字节）、`relayed`（是否经中继）、`preferred`（是否回到了上次的节点）、`frames`、`up_bytes`、`down_bytes` |

流式响应没有这些响应头，同样的信息在结尾的注释行 `: sharetoken job=… node=… verified=true …` 里，另外还有 `resumes`（中途接上了几次）。

节点虚报用量时，客户端拒签，并交出这一单的密钥作为证据，裁决后自动退款。这个行为由配置项 `buy.evidence` 控制，默认开启。

## 长对话和大请求 {#sessions}

- **回到同一个节点：** 同一段对话的后续请求，会优先交给上次服务它的节点。连接已经建好，模型服务器的 prompt 缓存也更容易命中，首字更快、费用可能更低。客户端按会话识别对话：chat 请求的 `session_id`、Claude Code 的会话、Codex 的 `prompt_cache_key`；都没有时，按模型和开头两条消息识别。那个节点满载或下线时，照常换别的节点。
- **请求大小：** 一个请求最大 32 MB（和多数云平台的 Claude 接口一致），可以带截图等图片。请求先压缩再发送，长对话历史通常压缩到原来的 1/3 到 1/5。

## 只用 TEE 节点 {#tee}

普通节点的运营者理论上能读到 prompt。敏感内容请只发给 TEE 节点，它们运行在机密虚拟机里，运营者读不到内存。在[配置文件](/docs/config#buy)里设置后重启客户端：

```json
{
  "buy": { "require_tee": true }
}
```

没有符合条件的节点时，请求会失败，而不是退回普通节点。`models` 的 `TEE` 列显示现有的 TEE 节点数。

## 只向认识的卖家购买 {#pairing}

默认情况下，你的请求会发给协调器选中的任意节点。如果只想发给你认识的卖家，就和他们配对。卖家给你一个配对码或[邀请链接](/docs/share#invite-links)，你用它配对：

```bash
{{exe}} pair join ABCDE-FGHIJ
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

错误的格式是 `{"error":{"code","message"}}`；`/v1/messages` 上是 Anthropic 的格式 `{"type":"error","error":{"type","message"}}`，Claude Code 会像显示 Anthropic 的错误一样显示它们。

| 错误码 | HTTP | 含义和处理 |
|---|---|---|
| `no_offers` | 503 | 没有节点提供这个模型。检查模型 ID，或换一个模型。如果你已经[配对](#pairing)，只有已配对的卖家算数 |
| `capacity` | 503 | 所有节点都满载，等待 30 秒后仍无空位。按 `Retry-After` 重试 |
| `insufficient_funds` | 402 | 余额不够冻结。设置更小的 `max_tokens`，或者赚一些 credit |
| `budget` | 400 | 这次请求的费用可能超出 `buy.budget_micros`。调小 `max_tokens` 或调大预算 |
| `bad_request` | 400 | 请求无效，消息里有原因。请求超过 32 MB 时返回 413 |
| `upstream_error`、`receipt_rejected` | 502 | 节点出错，或者收据没有通过验证。可以重试 |
| `unauthorized`、`forbidden` | 403 | 登录失效或账户被禁用。重新 `login` |
| `upgrade_required` | 426 | 客户端版本过低，见[升级](/docs/update#upgrade) |
| `not_connected` | 503 | 客户端还没连上网络，稍等或查看 `status` |

模型思考期间节点每 10 秒发一次心跳，所以长时间思考不会被判定为卡死；连续 5 分钟没有任何输出才会放弃。超时设置见[配置文件](/docs/config#buy)。

思考型模型的推理过程也计入 `max_tokens`。`max_tokens` 太小时 `content` 可能为空，只有 `reasoning_content`。
