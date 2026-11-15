# 三种模型接口

[English](../apis.md) | **简体中文**

买家的本地端点同时提供三种接口，网络上按原样传递：

| 客户端 | 接口 | 端点 |
|---|---|---|
| OpenAI SDK 和工具 | chat completions（`chat`） | `POST /v1/chat/completions` |
| Claude Code | Anthropic Messages（`messages`） | `POST /v1/messages`、`POST /v1/messages/count_tokens` |
| Codex | OpenAI Responses（`responses`） | `POST /v1/responses`、`GET /v1/responses`（WebSocket）、`POST /v1/responses/compact` |

`GET /v1/models` 列出模型和它们原生支持的接口（`apis`）；带 `?client_version=` 时返回 Codex 的模型清单。

设计参考 sub2api（做同样事情的网关）：**模型服务器支持请求所用的接口就原样透传，不支持才转换**。sub2api 收到请求时就知道上游是谁，sharetoken 的买家却不知道会匹配到哪个节点。所以买家按原接口发出请求，协调器优先匹配原生支持该接口的节点，节点在模型服务器不支持时转换。

```
Claude Code --/v1/messages--+                                       +- 模型服务器支持：透传
Codex -------/v1/responses--+- 买家 --- ticket.api --- 节点 --------+
OpenAI 客户端 -/v1/chat/...--+  （原样）   （原生优先）              +- 不支持：转换（internal/apicompat）
```

## 协议（infer 1.0.2）

下面的字段都纳入签名；协议 ID 和签名域没有变（1.0.2 的旧编码没有在已发布的客户端之间使用过）。

- **`TicketRequest.API`**、**`TicketRequest.RequireNative`**：请求的接口（空表示 `chat`），以及是否只要原生支持它的节点。
- **`Ticket.API`**：节点按它读取请求、写出每个响应事件；裁决和审计也按它读取。
- **`ModelPrice.APIs`**：节点的模型服务器对该模型原生支持的接口（`node.Upstream.APIs`）。
- **`ModelPrice`、`Ticket` 的 `PriceCacheRead`、`PriceCacheWrite`**：从模型服务器的 prompt 缓存读出、写入缓存的 prompt token 的价格。
- **`Receipt.CacheReadTokens`、`Receipt.CacheWriteTokens`**：`PromptTokens` 中按上面两个价格计费的部分。
- **密封请求**是请求头加请求体：`uvarint(请求头 JSON 长度) || 请求头 JSON || 请求体`（`protocol.JoinRequest`）。请求头里是模型服务器需要的客户端请求头（`anthropic-version`、`anthropic-beta`、Claude Code 和 Codex 的会话 ID），以及 sharetoken 自己的 `x-sharetoken-op: compact`。买家挑选（`buyer.forwardHeader`），节点再过滤一遍（`node.upstreamHeader`：Anthropic beta 按家族放行）。
- **响应记录**是 SSE 事件的数据（`data:` 后面的 JSON）。Messages 和 Responses 的数据里用 `type` 带着事件名，买家据此还原 `event:` 行（`apicompat.EventName`）。

## 匹配

`coordinator.Engine.IssueTicket` 在候选节点里只要有原生支持请求接口的，就只在这些节点里选（先于价格、信誉和会话粘性）；`RequireNative` 时完全跳过其他节点，一个都没有就回答 `no_offers`。`ModelSummary.APIs` 是各节点原生接口的并集。

## 节点

[`node.UpstreamRequest`](../../internal/node/request.go) 按接口执行字段规则，买家不能换模型、放大工作量，也不能打开卖家没有定价的功能：

| 接口 | 放行 | 删除 | 节点设置 |
|---|---|---|---|
| chat | messages、采样参数、stop、惩罚项、seed、response_format、tools、tool_choice、user、parallel_tool_calls、reasoning_effort | 其他（n、logprobs 等） | model、stream、stream_options、max_tokens |
| messages | messages、system、客户端工具、tool_choice、thinking、temperature、top_p、top_k、stop_sequences、metadata、context_management、output_config | 服务端工具（web_search、web_fetch、code_execution、tool_search_tool、mcp_toolset）、container、mcp_servers、service_tier | model、stream、max_tokens |
| responses | input、instructions、客户端工具（function、custom、namespace、local_shell、execution 为 client 的 tool_search 等）、tool_choice、parallel_tool_calls、reasoning、text、include（只留 `reasoning.encrypted_content`）、prompt_cache_key、temperature、top_p | 托管工具、background、service_tier、metadata；`previous_response_id` 和 `conversation` 直接拒绝 | model、stream、store=false、max_output_tokens |

输出上限是 `protocol.OutputLimit`：按加宽后的 prompt 估算，预留额度能覆盖的量，不超过 `protocol.HardMaxOutput`（chat 32768，其他 128000），也不超过请求自己要求的量。买家算出同样的数。

[`node.plan`](../../internal/node/plan.go) 接着判断：`Upstream.APIs()` 包含请求的接口就透传；否则用 `apicompat.Convert` 转成上游支持的最接近的接口（`apicompat.Target`：Responses 和 Messages 能承载 chat 承载不了的推理和工具项），再把事件转回来。

### 上下文压缩

压缩请求（`x-sharetoken-op: compact`，接口为 responses）在模型服务器有 `/responses/compact` 时发给它（`node.Compactor`，即 Codex 后端）。否则节点发一个普通请求，最后一条用户消息要求写摘要（`apicompat.CompactPrompt`），回答一个 `compaction` 项，它的 `encrypted_content` 是 `sharetoken-compaction-v1:` 加摘要的 base64。之后的请求带着这种项时，任何上游（包括读不懂它的 Codex 后端）都会收到一条 `<conversation_summary>…</conversation_summary>` 用户消息；Codex 后端生成的不透明项只发给 Codex 后端。Codex 使用自定义 provider 时，通常用自己的一个普通请求做压缩，不需要特殊处理。

## 转换：`internal/apicompat`

为 sharetoken 编写，参考 sub2api 的 `apicompat`（LGPL-3.0：参考的是行为和测试用例，没有用它的代码）。按客户端接口分组，每组都有请求转换、流式转换和非流式聚合：

| 客户端接口 | 文件 | 转换成 |
|---|---|---|
| messages | `messages_*.go` | Responses、chat |
| responses | `responses_*.go` | Messages、chat（包括 Codex 的客户端工具：custom、namespace、tool_search、local_shell） |
| chat | `chat_*.go` | Responses、Messages（包括 `anthropic_thinking` 回传和 `cache_control`） |

跨接口传递的内容：工具调用（ID 双向映射、名称合法化并还原、配对修正）、图片和文档、推理（Messages 的 thinking ↔ Responses 的 reasoning 项；签名和加密推理以 `sharetoken-` 前缀放在另一种接口的不透明字段里，见 `apicompat.IsConverted`，在到达读不懂它的模型服务器之前删掉）、停止原因、用量（包括缓存读写）。请求一律走流式；要整段 JSON 的客户端由买家这边聚合事件（`apicompat.Aggregate`）。

测试：每个方向的表驱动测试、真实抓取的 Claude Code 和 Codex 请求（`internal/apicompat/testdata/captured`），以及 `test/e2e/multiapi_test.go`（每种客户端接口对 Messages、Responses、chat 三种模型服务器，压缩、WebSocket、计费）。

## Provider

| Provider | 原生接口 |
|---|---|
| `anthropic`（key 或登录） | messages |
| `openai` key | chat、responses（`/responses`） |
| `openai` 登录（Codex 后端） | responses、compact |
| `deepseek` key | chat、messages（`/anthropic/v1/messages`） |
| `gemini`、`grok`、`custom`、本机服务器 | chat（Gemini 和 Antigravity 登录：chat，在 provider 内转成 Gemini 自己的格式） |

透传时保留各 provider 自己的适配：Claude 登录的请求头和 `claude-code` 配置（Claude Code 的请求保留它的 system，只换计费行和账户）、cache 断点规范化、删掉不是 Anthropic 模型签名的 thinking 块（签名被拒时去掉 thinking 重发一次）；Codex 后端的规则（instructions 必填、不设输出上限、`python` 改名、`store=false`），并在作业的上限处截断流；别的账户生成的推理项被拒时去掉重试；Gemini 丢了 thought signature 的工具调用补上占位签名。

## 计费

- **用量**从买家收到的事件里读（`protocol.ParseEvent`、`protocol.UsageMeter`）：chat 取最后的 usage，Messages 合并 `message_start` 和 `message_delta`，Responses 取终止事件。缓存读写来自 `prompt_tokens_details`、`cache_read_input_tokens` / `cache_creation_input_tokens`、`input_tokens_details`（也认 DeepSeek 的 `prompt_cache_hit_tokens` 和 Gemini 的 `cachedContentTokenCount`）。
- **费用**（`protocol.Cost`）：未缓存 prompt × 输入价 + 缓存读 × 缓存读价 + 缓存写 × 缓存写价 + 输出 × 输出价。冻结额和输出上限按输入价和缓存写价中较高的一个算。
- **买家接受的范围**（`protocol.UsagePlausible`）：prompt ≤ 3 × 估算 + 512（`EstimatePromptTokens` 按接口估算，包括图片和文件）；缓存数在 prompt 之内；输出 ≤ 2 × 可见输出估算 + 64，回答显示有隐藏推理时（thinking 或 reasoning 项、redacted 或 omitted thinking、上报了推理 token）放宽到作业的输出上限。节点把用量降到这个范围内再签名（`protocol.BillableUsage`），诚实节点的收据不会被拒；自己往请求里加指令的 provider 会限制计费的 prompt（`node.Billing.PromptCap`）。裁决用同样的规则。
- jobs 表记录 `api`、`cache_read_tokens`、`cache_write_tokens`（core 迁移 7）；门户的作业列表和 CSV 导出会显示它们。
