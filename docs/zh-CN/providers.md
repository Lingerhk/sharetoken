# 卖家模式：托管 API（provider）

[English](../providers.md) | **简体中文**

本文面向开发者，说明卖家如何共享由模型厂商托管的模型。面向用户的说明在门户的文档页面 [`internal/portal/web/docs/zh-CN/share.md`](../../internal/portal/web/docs/zh-CN/share.md)（`#providers` 一节）。本文不含任何密钥。

## 1. 概述

卖家除了共享本机的模型服务（Ollama 等），还可以共享自己在模型厂商开通的服务。客户端把买家发来的 OpenAI chat-completions 请求改写成厂商自己的格式，用卖家的 API key 或 OAuth 登录发出去，再把回答改写回 OpenAI 流式分片。买家侧没有任何变化，仍然只看到 OpenAI 格式。

- 一个卖家可以配置多个 provider，同一类型也可以配多个（用 `-name` 区分），各自可开可关。
- 本机模型和 provider 的模型可以同时出售。
- 凭据单独存在 `providers.json`，不进 `config.json`。
- 每项操作既有命令行子命令，也有本机 HTTP 接口（桌面应用使用，见 [desktop.md](desktop.md)）。

> **注意**
> - 买家的提示词以明文发给厂商，用量记在卖家在该厂商的账户上。
> - OAuth 登录使用各厂商自家客户端程序的 client ID 和身份请求头。
> - 出售托管 API 的访问受该厂商服务条款约束。确认条款是否允许，由用户自己负责。

## 2. provider 类型

| 类型 | API key | OAuth 登录 | 用 key 时的地址 | 用登录时的地址 | 上游格式 |
|---|---|---|---|---|---|
| `anthropic` | 支持 | 支持（Claude 订阅） | `api.anthropic.com` | `api.anthropic.com` | Messages API |
| `openai` | 支持 | 支持（ChatGPT / Codex） | `api.openai.com/v1` | `chatgpt.com/backend-api/codex` | key：chat completions；登录：Responses |
| `gemini` | 支持 | 支持（Gemini CLI） | `generativelanguage.googleapis.com/v1beta/openai` | `cloudcode-pa.googleapis.com` | key：OpenAI 兼容；登录：原生 generateContent |
| `antigravity` | 不支持 | 支持 | — | `cloudcode-pa.googleapis.com` | 原生 generateContent 加 Antigravity 封装 |
| `grok` | 支持 | 支持 | `api.x.ai/v1` | `cli-chat-proxy.grok.com/v1` | OpenAI 兼容 |
| `deepseek` | 支持 | 不支持 | `api.deepseek.com` | — | OpenAI 兼容 |
| `custom` | 支持（可留空） | 不支持 | 用 `-url` 指定 | — | OpenAI 兼容 |

类型定义在 [`internal/provider/kind.go`](../../internal/provider/kind.go)。密钥和令牌只会发往 HTTPS 地址或本地网络上的机器（`CheckSecretURL`）。

## 3. 代码结构

包 [`internal/provider`](../../internal/provider)：

| 文件 | 内容 |
|---|---|
| `store.go` | `providers.json` 的读写：`File`、`Entry`、`Token`；`Load`、`Save`、`Update`；`View`（密钥打码） |
| `kind.go` | 类型表，`CheckSecretURL` |
| `build.go` | `Entry.Upstream`（配置 → `node.Upstream`）、`ListModels`、`Check`、`Probe`、`billable` |
| `ops.go` | 命令行和 API 共用的操作：`AddWithKey`、`SaveLogin`、`FinishLoginAndSave`、`SetEnabled`、`Remove`、`SetModel`、`RemoveModel`、`Models`、`Test`、`Views`、`KindViews` |
| `chat.go`、`sse.go` | 解析买家的 chat 请求；发送 SSE 请求，并把结果写成 OpenAI 分片 |
| `openai.go` | OpenAI 兼容上游；输出上限的学习与重试 |
| `anthropic.go` | chat ↔ Messages API |
| `responses.go` | chat ↔ Responses（Codex 后端） |
| `gemini.go` | chat ↔ Gemini 原生格式；工具 schema 清理 |
| `oauth.go` | 两步登录、令牌续期、401 重试 |
| `oauth_anthropic.go`、`oauth_openai.go`、`oauth_google.go`、`oauth_grok.go` | 各家的登录流程和请求封装 |
| `provider_test.go` | 格式转换、流、存储、上限、隐藏推理、登录地址、令牌续期 |

其他位置的相关代码：

- [`internal/node/node.go`](../../internal/node/node.go)：`ModelConfig` 带有 `MaxOutputTokens`（`max_output`）和每个模型自己的 `Upstream`（不序列化）。节点不需要全局的 `Upstream`。
- [`internal/client/agent.go`](../../internal/client/agent.go)：`shareModelsLocked` 把本机模型和所有已开启 provider 的模型合成一份报价；`watchedUpstream` 在凭据被拒时给卖家告警。
- [`internal/client/providers.go`](../../internal/client/providers.go)：providers API。
- [`internal/client/api.go`](../../internal/client/api.go)：`ShareStatus` 带有 `Providers` 和 `Serving`。
- [`cmd/sharetoken/main.go`](../../cmd/sharetoken/main.go)：`provider` 子命令和 `share add -provider`。
- [`internal/cli/cli.go`](../../internal/cli/cli.go)：`ReadSecret`（不回显地读取密钥）。
- [`test/e2e/providers_test.go`](../../test/e2e/providers_test.go)：两个端到端测试。

## 4. providers.json

文件是客户端状态目录下的 `providers.json`，权限 0600，先写临时文件再改名。`version` 为 1；版本比程序认识的更高的文件会被拒绝。

```json
{
  "version": 1,
  "providers": [
    {
      "name": "deepseek",
      "type": "deepseek",
      "auth": "apikey",
      "enabled": true,
      "api_key": "<密钥>",
      "models": [
        {"id": "deepseek-flash", "upstream_model": "deepseek-flash", "price_in": 100000, "price_out": 200000, "max_output": 8192}
      ],
      "added_at": "2030-01-01T12:00:00Z"
    },
    {
      "name": "chatgpt",
      "type": "openai",
      "auth": "oauth",
      "enabled": true,
      "oauth": {
        "access_token": "<令牌>",
        "refresh_token": "<令牌>",
        "expires_at": "2030-01-01T13:00:00Z",
        "email": "…",
        "account_id": "…"
      },
      "models": []
    }
  ]
}
```

| 字段 | 含义 |
|---|---|
| `name` | 用户给 provider 起的名字，文件内唯一；默认等于类型名 |
| `type` | 类型 ID（第 2 节） |
| `auth` | `apikey` 或 `oauth` |
| `enabled` | 关闭后它的模型不再报价；配置和凭据保留 |
| `base_url` | 覆盖类型自带的地址；`custom` 必填 |
| `api_key` | 密钥 |
| `headers` | 每个请求都附带的请求头，例如 `anthropic-workspace-id` |
| `oauth` | 登录得到的令牌。`account_id` 是令牌所代表的账户（ChatGPT 账户、Anthropic 组织）；`project_id` 用于 Gemini 和 Antigravity；`plan`、`extra` 按类型使用 |
| `models` | 这个 provider 出售的模型：网络 ID、厂商侧的模型名、价格、`max_output` |

模型条目的字段与 `config.json` 里本机模型的相同（`node.ModelConfig`）。

所有修改都经过 `provider.Update`：它重新读取文件、应用修改再写回（在进程内串行），所以运行中的客户端续期的令牌和命令修改的设置不会互相覆盖。

## 5. 命令行

```bash
sharetoken provider                               # 可添加的类型，以及已配置的 provider
sharetoken provider add <类型> [-name N] [-url URL] [-header K=V] [-no-check]
sharetoken provider login <类型> [-name N] [-project ID]
sharetoken provider models <名字>                 # 这份凭据可以使用的模型
sharetoken provider test <名字> [-model M] [-prompt TEXT]
sharetoken provider on|off <名字>
sharetoken provider rm <名字>                     # 连同凭据一起删除

sharetoken share add <厂商模型名> -provider <名字> [-id ID] [-in P] [-out P] [-max-output N]
sharetoken share rm <ID>                          # 在本机模型和 provider 的模型里查找
```

- **输入密钥：** 隐藏输入的提示、标准输入，或环境变量 `SHARETOKEN_PROVIDER_KEY`。没有对应的命令行参数，因为参数会出现在进程列表和 shell 历史里。
- **添加时校验：** `provider add` 会向厂商列一次模型，确认密钥可用，被拒绝的密钥不保存；`-no-check` 跳过这一步。对 DeepSeek 还会读出账户余额。
- **立即生效：** `sharetoken up` 在运行时，这些命令改完文件后会通知运行中的客户端重新加载，不需要重启。
- **示例：**

```bash
sharetoken provider add deepseek
sharetoken share add deepseek-flash -provider deepseek
sharetoken provider add custom -name lab -url https://llm.example.com/v1
sharetoken provider add anthropic -header anthropic-workspace-id=<工作区 ID>
```

## 6. 本机 API

路由在 [`internal/client/providers.go`](../../internal/client/providers.go)，挂在客户端的本机 API 上：

| 方法 | 路径 | 作用 |
|---|---|---|
| GET | `/api/v1/providers` | 可添加的类型（带 `needs_url` 和 `key_optional`，页面据此决定显示哪些输入框）和已配置的 provider |
| POST | `/api/v1/providers` | 用 API key 添加（`provider.AddRequest`） |
| PATCH | `/api/v1/providers/{name}` | `{"enabled": bool}` |
| DELETE | `/api/v1/providers/{name}` | 删除 |
| GET | `/api/v1/providers/{name}/models` | 凭据可以使用的模型 |
| PUT | `/api/v1/providers/{name}/models` | 添加模型或改价（`node.ModelConfig`） |
| DELETE | `/api/v1/providers/{name}/models/{id}` | 移除一个模型 |
| POST | `/api/v1/providers/{name}/test` | `{"model", "prompt"}`：经它发送一次请求 |
| POST | `/api/v1/providers/oauth/start` | `{"type", "project"}` → 登录地址 |
| POST | `/api/v1/providers/oauth/finish` | `{"id", "code", "name"}` |

- 返回内容里从不包含密钥或令牌，只有打码后的形式。
- 带 `Origin` 且不等于 `http://<本机 host>` 的请求得到 403 `bad_origin`。密钥从这里进入，所以任何网页都不许调用。
- 状态码：找不到 404，凭据被拒 401，模型 ID 已被占用 409，厂商返回错误 502，其余 400。
- `GET /api/v1/share` 返回的状态里包含 `providers` 和 `serving`。

## 7. 请求改写

每个 provider 最终是一个 `node.Upstream`：输入买家的 chat-completions 请求体，输出 OpenAI 的 `chat.completion.chunk` 对象。

| 上游 | 改写内容 |
|---|---|
| OpenAI 兼容（deepseek、grok、custom、用 key 的 gemini、用 key 的 openai） | 基本透传，替换模型名。OpenAI 官方接口用 `max_completion_tokens` 代替 `max_tokens` |
| Anthropic Messages | system 消息提到顶层；工具定义、工具调用和工具结果转成 `tool_use` / `tool_result` 块；`x-api-key` 加 `anthropic-version: 2023-06-01`；流事件转回分片 |
| 用登录的 Anthropic Messages | 同上，但用 bearer 令牌和 `anthropic-beta: oauth-2025-04-20` 请求头代替 `x-api-key`。这个 API 只为 Claude Code 提供这种令牌的服务，所以请求的 system prompt 以 Claude Code 的身份行开头。短名 `claude-sonnet-4-5`、`claude-opus-4-5` 和 `claude-haiku-4-5` 按带日期的 ID 发出 |
| Responses（Codex 后端） | 消息转成 `input` 项。后端不接受输出上限，所以流在买家的 `max_tokens` 处截断。名为 `python` 的工具发出时改名为 `python__st`，返回时还原 |
| Gemini 原生 | 消息转成 `contents` / `systemInstruction`，工具转成 `functionDeclarations`，`cleanSchema` 去掉 Gemini 不接受的 schema 字段 |
| Antigravity | 在 Gemini 原生请求外再包一层（`requestId`、`userAgent`、`requestType`、项目 ID），并加上身份 system prompt；按套餐选择正式主机或 daily 主机 |

### 输出上限

节点默认向上游要求最多 32768 个输出 token，有些模型不接受（例如 `gpt-4o-mini` 只到 16384，直接返回 400）。`outputLimits` 的处理：

1. 模型配置了 `max_output` 时，按这个值封顶。
2. 否则请求照原样发出。厂商以“上限是 N”拒绝时，从错误信息里读出 N，重发一次，之后这个模型一直用 N。

Anthropic 必须带 `max_tokens`；`anthropicMaxOutput` 给出默认值。

### 隐藏推理与计费

买家会核对卖家申报的输出 token：不得超过可见输出估算值的 2 倍加 64（`protocol.UsagePlausible`），超出的作业整单拒收。带隐藏推理的模型（例如 `gpt-5-nano`）报出的 token 远多于买家看到的内容。

`billable` 包在每个 provider 外面，把申报量压到这个上限以内；厂商原始的数字保留在分片的 `completion_tokens_reported` 里。结果是：**隐藏推理的 token 卖家收不到钱**，定价时要把这一点算进去。`provider test` 遇到这种情况会提示。

## 8. OAuth 登录

登录分两步，所以没有浏览器的机器也能用：

1. `StartLogin(类型, project)` 生成登录地址（PKCE 加 state）。命令行把地址打印出来；API 返回 `id` 和地址。
2. 用户在任意浏览器里登录，把得到的授权码或整个回调地址粘贴回来。`FinishLogin` 解析它（`parseCallback`）、换取令牌并保存。

| 类型 | 授权端 | 回调地址 | 备注 |
|---|---|---|---|
| `anthropic` | `claude.com` | `platform.claude.com/oauth/code/callback` | 页面显示形如 `code#state` 的授权码，整个粘贴回来。令牌里保留账户的邮箱和组织。列不出模型时用内置列表 |
| `openai` | `auth.openai.com` | `localhost:1455` | 令牌里带 ChatGPT 账户 ID。读不到后端的模型列表时用内置列表 |
| `gemini` | Google | `codeassist.google.com/authcode` | 页面上显示授权码。`-project` 指定 Google Cloud 项目；否则经 `loadCodeAssist` / `onboardUser` 查找 |
| `antigravity` | Google | `localhost:8085` | 用同样的方式查找项目 |
| `grok` | `auth.x.ai` | `127.0.0.1:56121` | — |

回调地址是 localhost 的几家，浏览器最后会停在一个打不开的页面上。把地址栏里的地址整个粘贴回来即可。

已开始但没有完成的登录在 30 分钟后失效。

令牌的维护（`Tokens` / `TokenSource`）：

- 令牌剩余有效期不到 5 分钟时续期，新令牌写回 `providers.json`。
- 请求得到 401 时强制续期并重试一次（`withToken`）。
- 续期失败或请求再次被拒时，按凭据被拒处理（见下）。

### 凭据被拒

上游返回 401 或 403（凭据被拒）或 402（余额不足）时，`watchedUpstream` 给卖家一条告警 `source:<名字>:credentials`，在 `sharetoken status` 和 API 里可见。修好之前，经过这个 provider 的作业都会失败。其他错误不触发这条告警。

## 9. 验证情况

| 项目 | 状态 |
|---|---|
| 格式转换、流、输出上限、隐藏推理、存储、回调解析、每种登录的登录地址、令牌续期 | 由对接模拟服务的单元测试覆盖（[`internal/provider/provider_test.go`](../../internal/provider/provider_test.go)） |
| 多个 provider 各自经自己的 API 成交；密钥被拒时卖家收到告警 | 由 [`test/e2e/providers_test.go`](../../test/e2e/providers_test.go) 的两个端到端测试覆盖，对接模拟服务 |
| 用 API key 的 DeepSeek | 已对接真实服务测试，经网络端到端 |
| 用 API key 的 OpenAI | 已对接真实服务端到端测试，包含流式工具调用 |
| 用 API key 的 Anthropic | 未对接真实服务测试；格式转换只用模拟服务测过 |
| 用 API key 的 Gemini 和 Grok | 未对接真实服务测试 |
| OAuth 登录（anthropic、openai、gemini、antigravity、grok） | 本仓库里没有任何测试对接真实服务验证过。测试覆盖登录地址、令牌续期和格式转换；登录和换取令牌需要手动验证 |

对接真实服务的测试里，作业都到达 `final` 且 `verified=true`，卖家入账，providers API 没有泄漏密钥，带外部 `Origin` 的请求得到 403。

自动测试用 `scripts/test/test.sh ./...` 运行（见 [development.md](development.md)）。

## 10. 已知限制

- `PUT /api/v1/share` 仍然可以设置 `upstream_key_env`，即保存本机模型服务密钥的环境变量名。它与 provider 无关，但属于同一个凭据面。
- Codex 后端没有可靠的模型列表接口。读不到时用内置列表，新模型需要改代码。其他登录方式的内置列表也是如此。
- 两个 provider 不能出售同一个网络模型 ID：后加载的那个被跳过并告警，`share add` 会提示用 `-id` 换一个 ID。
- 隐藏推理的 token 不计费（第 7 节）。
