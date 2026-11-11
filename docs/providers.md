# 卖家模式：托管 API（多 provider）

> 对应提交 `997be6c`（2026-11-11，分支 `feat/develop`）。本文件**不含任何密钥**。
> 面向用户的说明在门户文档 `portal/web/docs/{en,zh-CN}/share.md` 的 `#providers` 一节；本文是给开发者的实现说明和验证记录。

## 1. 做了什么

卖家除了共享本机模型服务（Ollama 等），还可以共享自己在模型厂商开通的服务：客户端把买家发来的 OpenAI chat-completions 请求改写成厂商自己的格式，用卖家的 API key 或 OAuth 登录转发出去，再把回答改写回 OpenAI 流式分片。买家侧没有任何变化，仍然只看到 OpenAI 格式。

- 一个卖家可以配多个 provider，同一类型可以配多个（用 `-name` 区分），各自可开可关。
- 本机模型和 provider 的模型可以同时卖。
- 凭据单独存在 `providers.json`，不进 `config.json`。
- 所有操作既有 CLI 子命令，也有本机 HTTP API（给 GUI 用）。

> **注意**
> - 买家的提示词以明文发给厂商，用量记在卖家的厂商账户上。
> - OAuth 登录用的是各厂商官方客户端的 client ID 和身份头，把订阅额度提供给他人使用很可能违反厂商条款，账号有被封的风险。用户文档里已写明“请确认厂商条款允许这种用法”。

## 2. 支持的类型

| 类型 | API key | OAuth 登录 | API key 地址 | OAuth 后端 | 上游格式 |
|---|---|---|---|---|---|
| `anthropic` | ✓ | 本文不涵盖（见 §9） | `api.anthropic.com` | — | Messages API |
| `openai` | ✓ | ✓（ChatGPT / Codex） | `api.openai.com/v1` | `chatgpt.com/backend-api/codex` | key：chat completions；OAuth：Responses |
| `gemini` | ✓ | ✓（Gemini CLI） | `generativelanguage.googleapis.com/v1beta/openai` | `cloudcode-pa.googleapis.com` | key：OpenAI 兼容；OAuth：原生 generateContent |
| `antigravity` | ✗ | ✓ | — | `cloudcode-pa.googleapis.com` | 原生 generateContent 加 Antigravity 封装 |
| `grok` | ✓ | ✓ | `api.x.ai/v1` | `cli-chat-proxy.grok.com/v1` | OpenAI 兼容 |
| `deepseek` | ✓ | ✗ | `api.deepseek.com` | — | OpenAI 兼容 |
| `custom` | ✓（可留空） | ✗ | 用 `-url` 指定 | — | OpenAI 兼容 |

类型表在 `provider/kind.go`。密钥和令牌只发往 HTTPS 地址或本地网络地址（`CheckSecretURL`）。

## 3. 代码结构

新包 `provider/`：

| 文件 | 内容 |
|---|---|
| `store.go` | `providers.json` 的读写：`File`、`Entry`、`Token`，`Load` / `Save` / `Update`，`View`（密钥打码） |
| `kind.go` | 类型表，`CheckSecretURL` |
| `build.go` | `Entry.Upstream`（配置 → `node.Upstream`）、`ListModels`、`Check`、`Probe`、`billable` |
| `ops.go` | CLI 和 API 共用的操作：`AddWithKey`、`SaveLogin`、`FinishLoginAndSave`、`SetEnabled`、`Remove`、`SetModel`、`RemoveModel`、`Models`、`Test`、`Views`、`KindViews` |
| `chat.go`、`sse.go` | 解析买家的 chat 请求；发 SSE 请求、把结果写成 OpenAI 分片 |
| `openai.go` | OpenAI 兼容上游，输出上限的学习与重试 |
| `anthropic.go` | chat ↔ Messages API |
| `responses.go` | chat ↔ Responses（Codex 后端） |
| `gemini.go` | chat ↔ Gemini 原生格式，工具 schema 清理 |
| `oauth.go` | 两步登录、令牌续期、401 重试 |
| `oauth_openai.go`、`oauth_google.go`、`oauth_grok.go` | 各家的登录流程和请求封装 |
| `provider_test.go` | 格式转换、流、存储、上限、隐藏推理、登录地址、令牌续期 |

其他改动：

- `node/node.go`：`ModelConfig` 增加 `MaxOutputTokens`（`max_output`）和每模型的 `Upstream`（不序列化）；节点不再要求全局 `Upstream`。
- `client/agent.go`：`shareModelsLocked` 把本机模型和已开启 provider 的模型合成一份报价；`watchedUpstream` 在凭据被拒时给卖家告警。
- `client/providers.go`：providers API。
- `client/api.go`：`ShareStatus` 增加 `Providers`、`Serving`。
- `cmd/sharetoken/main.go`：`provider` 子命令，`share add -provider`。
- `internal/cli/cli.go`：`ReadSecret`（不回显地读密钥）。
- `e2e/providers_test.go`：两个端到端测试。

## 4. providers.json

位置：客户端状态目录下的 `providers.json`，权限 0600，先写临时文件再改名。`version` 为 1；比当前程序新的版本会拒绝读取。

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
      "added_at": "2026-11-11T12:00:00Z"
    },
    {
      "name": "chatgpt",
      "type": "openai",
      "auth": "oauth",
      "enabled": true,
      "oauth": {
        "access_token": "<令牌>",
        "refresh_token": "<令牌>",
        "expires_at": "2026-11-11T13:00:00Z",
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
| `name` | 用户起的名字，文件内唯一；默认等于类型名 |
| `type` | 类型 ID（§2） |
| `auth` | `apikey` 或 `oauth` |
| `enabled` | 关掉后它的模型不再报价，配置和凭据保留 |
| `base_url` | 覆盖类型自带的地址；`custom` 必填 |
| `api_key` | 密钥 |
| `headers` | 每个请求附带的头，例如 `anthropic-workspace-id` |
| `oauth` | 登录得到的令牌；`project_id` 用于 Gemini / Antigravity，`plan`、`extra` 按类型使用 |
| `models` | 这个 provider 卖的模型：网络 ID、厂商侧模型名、价格、`max_output` |

模型条目的价格字段与 `config.json` 里本机模型的相同（`node.ModelConfig`）。

所有修改都经过 `provider.Update`（进程内串行的读-改-写），所以运行中的客户端续期令牌和命令行改设置不会互相覆盖。

## 5. 命令行

```bash
sharetoken provider                               # 列出可添加的类型和已配置的 provider
sharetoken provider add <类型> [-name N] [-url URL] [-header K=V] [-no-check]
sharetoken provider login <类型> [-name N] [-project ID]
sharetoken provider models <名字>                 # 这份凭据能用的模型
sharetoken provider test <名字> [-model M] [-prompt TEXT]
sharetoken provider on|off <名字>
sharetoken provider rm <名字>                     # 连同凭据一起删除

sharetoken share add <厂商模型名> -provider <名字> [-id ID] [-in P] [-out P] [-max-output N]
sharetoken share rm <ID>                          # 本机模型和 provider 的模型都会找
```

- **密钥输入**：隐藏提示、标准输入或环境变量 `SHARETOKEN_PROVIDER_KEY`。不提供命令行参数，因为参数会出现在进程列表和 shell 历史里。
- **添加时校验**：`provider add` 会向厂商列一次模型来确认密钥可用，被拒绝就不保存；`-no-check` 跳过。DeepSeek 还会读出余额。
- **立即生效**：`sharetoken up` 在运行时，这些命令改完文件后会让它重新加载，不用重启。
- **示例**：

```bash
sharetoken provider add deepseek
sharetoken share add deepseek-flash -provider deepseek
sharetoken provider add custom -name lab -url https://llm.example.com/v1
sharetoken provider add anthropic -header anthropic-workspace-id=<工作区 ID>
```

## 6. 本机 API

路由在 `client/providers.go`，挂在客户端的本机 API 上：

| 方法 | 路径 | 作用 |
|---|---|---|
| GET | `/api/v1/providers` | 可添加的类型和已配置的 provider |
| POST | `/api/v1/providers` | 用 API key 添加（`provider.AddRequest`） |
| PATCH | `/api/v1/providers/{name}` | `{"enabled": bool}` |
| DELETE | `/api/v1/providers/{name}` | 删除 |
| GET | `/api/v1/providers/{name}/models` | 凭据能用的模型 |
| PUT | `/api/v1/providers/{name}/models` | 添加或改价（`node.ModelConfig`） |
| DELETE | `/api/v1/providers/{name}/models/{id}` | 移除一个模型 |
| POST | `/api/v1/providers/{name}/test` | `{"model", "prompt"}`：经它发一次请求 |
| POST | `/api/v1/providers/oauth/start` | `{"type", "project"}` → 登录地址 |
| POST | `/api/v1/providers/oauth/finish` | `{"id", "code", "name"}` |

- 返回里从不包含密钥或令牌，只有打码后的形式。
- 带 `Origin` 且不等于 `http://<本机 host>` 的请求一律 403 `bad_origin`：密钥从这里进，任何网页都不许调用。
- 错误码：找不到 404，凭据被拒 401，模型 ID 已被占用 409，厂商返回错误 502，其余 400。
- `GET /api/v1/share` 的状态里增加了 `providers` 和 `serving`。

## 7. 请求改写

每个 provider 最终是一个 `node.Upstream`：输入买家的 chat-completions 请求体，输出 OpenAI `chat.completion.chunk`。

| 上游 | 改写要点 |
|---|---|
| OpenAI 兼容（deepseek、grok、custom、gemini key、openai key） | 基本透传，替换模型名；OpenAI 官方接口用 `max_completion_tokens` 代替 `max_tokens` |
| Anthropic Messages | system 消息提到顶层；工具定义和工具调用/结果转成 `tool_use` / `tool_result` 块；`x-api-key` 加 `anthropic-version: 2023-06-01`；流事件转回分片 |
| Responses（Codex 后端） | 消息转成 `input` 项；后端不接受输出上限，所以流在买家的 `max_tokens` 处截断；名为 `python` 的工具改名为 `python__st`，回来时还原 |
| Gemini 原生 | 消息转成 `contents` / `systemInstruction`，工具转成 `functionDeclarations`，`cleanSchema` 去掉 Gemini 不接受的 schema 字段 |
| Antigravity | 在 Gemini 原生格式外再包一层（`requestId`、`userAgent`、`requestType`、项目 ID）并加身份 system prompt；按套餐选正式或 daily 主机 |

### 输出上限

节点默认向上游要 32768 个输出 token，有些模型接受不了（例如 `gpt-4o-mini` 只到 16384，直接 400）。`outputLimits` 的处理：

1. 模型配置了 `max_output` 就用它封顶。
2. 否则照发；厂商以“上限是 N”拒绝时，从错误信息里读出 N，重发一次，之后这个模型一直用 N。

Anthropic 必须带 `max_tokens`，用 `anthropicMaxOutput` 给出默认值。

### 隐藏推理与计费

买家会核对卖家申报的输出 token：不得超过可见输出估算值的 2 倍加 64（`protocol.UsagePlausible`），超了整单拒收。带隐藏推理的模型（例如 `gpt-5-nano`）报的 token 远多于买家看到的内容。

`billable` 包在每个 provider 外面，把申报量压到这个上限以内，厂商原始数字保留在分片的 `completion_tokens_reported` 里。结果是：**隐藏推理的 token 卖家收不到钱**，定价时要算进去。`provider test` 遇到这种情况会提示。

## 8. OAuth 登录

登录分两步，这样没有浏览器的机器也能用：

1. `StartLogin(类型, project)` 生成登录地址（PKCE + state）。CLI 把地址打印出来，API 返回 `id` 和地址。
2. 用户在任意浏览器登录，把得到的授权码或整个回调地址粘贴回来；`FinishLogin` 解析（`parseCallback`）、换取令牌并保存。

| 类型 | 授权端 | 回调地址 | 备注 |
|---|---|---|---|
| `openai` | `auth.openai.com` | `localhost:1455` | 令牌里带 ChatGPT 账户 ID；模型列表取不到时用内置列表 |
| `gemini` | Google | `codeassist.google.com/authcode` | 页面上直接显示授权码；`-project` 指定 GCP 项目，否则经 `loadCodeAssist` / `onboardUser` 找 |
| `antigravity` | Google | `localhost:8085` | 同上找项目 |
| `grok` | `auth.x.ai` | `127.0.0.1:56121` | — |

回调地址是 localhost 的几家，浏览器最后会停在一个打不开的页面上，把地址栏里的地址整个粘贴回来即可。

令牌的维护（`Tokens` / `TokenSource`）：

- 距过期不到 5 分钟时续期，新令牌写回 `providers.json`。
- 请求得到 401 时强制续期并重试一次（`withToken`）。
- 续期失败或再次被拒，按凭据被拒处理（见下）。

### 凭据失效

上游返回 401、403 或 402 时，`watchedUpstream` 给卖家一条告警 `source:<名字>:credentials`，在 `sharetoken status` 和 API 里可见；修好之前这个 provider 的作业都会失败。其他错误不触发这条告警。

## 9. 验证记录

| 项目 | 状态 |
|---|---|
| DeepSeek API key | **已实测**：真实密钥，dev 环境端到端 |
| OpenAI API key | **已实测**：真实密钥，dev 环境端到端，含流式工具调用 |
| Anthropic API key | **未实测**：手头的密钥未绑定工作区，需要 `anthropic-workspace-id`；格式转换只用模拟服务测过 |
| Gemini、Grok API key | **未实测**：没有密钥 |
| OAuth：openai、gemini、antigravity、grok | **未实测登录**：只测了登录地址、令牌续期和格式转换，待手动验证 |
| Anthropic OAuth | **本文不涵盖**：`997be6c` 里没有注册登录流程；之后另有一份实现（`provider/oauth_anthropic.go`），其流程、请求改写和验证情况未写入本文 |

dev 端到端：卖家在 st-db 上挂 deepseek 和 openai 两个 provider，卖 `deepseek-flash`、`deepseek-v4-pro`、`gpt-4o-mini`、`gpt-5-nano`；买家在 instabox。作业都到 `final` 且 `verified=true`，卖家入账，providers API 不泄漏密钥，带外部 `Origin` 的请求得到 403。

自动测试：`scripts/test.sh ./...` 全部通过。`e2e/providers_test.go` 有两个用例：多个 provider 各走各的 API 成交；密钥被拒时卖家收到告警。

继续在 dev 上试：st-db 的 `~/st-sell` 保留了两个 provider 的配置，`cd ~/st-sell && ./sharetoken up`。环境说明见 [dev-env.md](dev-env.md)。

## 10. 已知限制

- `PUT /api/v1/share` 仍然可以改 `upstream_key_env`（[security-audit.md](security-audit.md) 第 1 项），与本功能无关但同属凭据面。
- GUI 还没有 provider 的配置界面，API 已备好。
- Codex 后端没有可靠的模型列表接口，取不到时用内置列表，新模型需要更新代码。
- 不同 provider 不能卖同一个网络模型 ID：后加载的那个被跳过并告警，`share add` 会提示改用 `-id`。
- 隐藏推理的 token 不计费（§7）。
