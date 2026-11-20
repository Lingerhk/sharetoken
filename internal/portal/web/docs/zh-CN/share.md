# 共享模型

把本机的模型共享到网络，按实际 token 赚 credit。不需要公网 IP，也不需要开端口。

> [!WARNING]
> **运行模型必须解密，你的机器能看到别人发来的 prompt。** 请不要记录或保存这些内容。只共享你有权共享的模型。

## 第 1 步：准备模型服务 {#server}

默认对接本机的 Ollama。其他 OpenAI 兼容的模型服务见[使用其他模型服务](#other-server)；共享在模型厂商开通的服务见[共享托管 API](#providers)。

::: macos
```bash
brew install ollama
brew services start ollama
ollama pull gemma4:e4b
```

也可以从 [ollama.com](https://ollama.com/download) 下载应用，打开后它会一直在后台运行。
:::

::: linux
```bash
curl -fsSL https://ollama.com/install.sh | sh
ollama pull gemma4:e4b
```

安装脚本会把 Ollama 注册成 systemd 服务，开机自动运行。
:::

::: windows
```powershell
winget install Ollama.Ollama
ollama pull gemma4:e4b
```

也可以从 [ollama.com](https://ollama.com/download) 下载安装程序。它在通知区运行，登录 Windows 时自动启动。
:::

确认客户端能看到模型：

```bash
{{exe}} share detect
```

## 第 2 步：添加模型 {#add}

```bash
{{exe}} share add gemma4:e4b -id gemma-4-e4b
```

| 参数 | 含义 | 默认值 |
|---|---|---|
| `-id` | 模型在网络上的 ID，买家用它调用 | 和本机模型同名 |
| `-in` | 每百万输入 token 的价格，单位 micro-credit | `100000`（0.1 credit） |
| `-out` | 每百万输出 token 的价格，单位 micro-credit | `200000`（0.2 credit） |
| `-cache-read` | 模型服务器从 prompt 缓存读出的输入 token，每百万的价格 | `-in` 的 1/10 |
| `-cache-write` | 模型服务器写入 prompt 缓存的输入 token，每百万的价格 | Anthropic 模型为 `-in` 的 1.25 倍，其他等于 `-in` |

> [!TIP]
> 其他节点已经在共享同一个模型时，请使用相同的 ID，买家才能一起选到。先用 `{{exe}} models` 看看现有的 ID 和价格。协调器综合价格、信誉、延迟和成功率选择节点，价格的权重最高。

添加模型会自动打开共享。

## 第 3 步：上线接单 {#up}

```bash
{{exe}} up
```

`up` 已经在运行时，`share` 命令立即生效，不用重启。长期接单请[在后台运行](/docs/running#background)。

确认状态：

```bash
{{exe}} status
```

看到 `sharing 1 model(s): ...` 就说明已经上线。如果有 `offer not accepted: ...`，后面是被拒绝的原因。

## 查看收益 {#earnings}

```bash
{{exe}} balance
{{exe}} jobs -role sell -n 20
```

- **结算：** 买家确认（Ack）后结算；买家没有确认的单子，过了 24 小时争议窗口自动成交。所以收益可能先计入 `pending`。
- **手续费：** 平台从每单收取 5%。
- 门户的[作业](/app/#/jobs)和[账单](/app/#/ledger)页面有明细。

## 管理共享 {#manage}

| 操作 | 命令 |
|---|---|
| 查看设置 | `{{exe}} share` |
| 列出本机模型 | `{{exe}} share detect` |
| 改价格（重新添加同一个 ID） | `{{exe}} share add gemma4:e4b -id gemma-4-e4b -in 80000 -out 150000` |
| 移除模型 | `{{exe}} share rm gemma-4-e4b` |
| 暂停接单 | `{{exe}} share off` |
| 恢复接单 | `{{exe}} share on` |

## 使用其他模型服务 {#other-server}

任何提供 OpenAI 兼容接口的本机服务都可以，比如 LM Studio、vLLM 或 llama.cpp：

```bash
{{exe}} share upstream http://127.0.0.1:1234/v1
{{exe}} share detect
{{exe}} share add my-local-model -id my-model
```

本机的模型服务需要 API key 时，把 key 放在一个环境变量里，再在[配置文件](/docs/config#share)的 `share.upstream_key_env` 里写上变量名。

## 共享托管 API {#providers}

除了本机运行的模型，也可以共享你在模型厂商开通的服务：客户端把买家的请求转给厂商，再把回答转回去。买家始终用 OpenAI 格式，各家格式的差别由客户端转换。每个这样的来源叫一个 provider，可以配置多个，各自开关。

> [!WARNING]
> 请求以明文发给厂商，用量记在你在厂商的账户上。请先确认厂商的服务条款允许这样使用，并把价格定在成本之上。

| 类型 | API key | 登录（OAuth） |
|---|---|---|
| `anthropic` | 支持 | — |
| `openai` | 支持 | 支持（ChatGPT 账号） |
| `gemini` | 支持 | 支持（Google 账号） |
| `antigravity` | — | 支持（Google 账号） |
| `grok` | 支持 | 支持 |
| `deepseek` | 支持 | — |
| `custom` | 支持：任何 OpenAI 兼容的 API，用 `-url` 给出地址 | — |

### 用 API key 添加 {#provider-key}

```bash
{{exe}} provider add deepseek
```

客户端会提示输入 API key（输入时不显示），向厂商确认 key 可用后保存：

```text
DeepSeek API key:
saved provider deepseek (DeepSeek, apikey) sk-…9b35
   models: deepseek-flash, deepseek-v4-pro
   balance 327.18 CNY
```

然后添加要共享的模型并上线：

```bash
{{exe}} share add deepseek-flash -provider deepseek -in 100000 -out 200000
{{exe}} up
```

其他 OpenAI 兼容的 API 用 `custom`，同一类型要配多个账号时用 `-name` 起不同的名字：

```bash
{{exe}} provider add custom -name myapi -url https://api.example.com/v1
{{exe}} provider add openai -name openai-team2
```

- **脚本里使用：** 没有终端时从标准输入读一行，也可以放在环境变量 `SHARETOKEN_PROVIDER_KEY` 里。不提供命令行参数，是为了不让 key 留在命令历史里。
- **Anthropic 的 key 没有绑定工作区时：** 加上 `-header anthropic-workspace-id=工作区ID`。
- **模型的输出上限：** 厂商因为输出上限拒绝请求时，客户端会按厂商给出的上限自动重发，之后一直沿用。想预先指定，在 `share add` 时用 `-max-output`。
- **看不见的推理在输出上限内计费：** 有些模型的推理不完整返回（加密、只给摘要或不显示），但厂商照样计入用量。回答显示模型做了推理时，买家按上报的输出 token 付费，上限是这次作业的输出上限；超出上限的部分，以及没有推理迹象的回答里看不见的输出，不计费。`{{exe}} provider test` 会显示无法计费的上报数（`reported`）。
- **三种接口：** 买家用 chat completions、Anthropic Messages（Claude Code）或 OpenAI Responses（Codex）调用。厂商原生支持的请求原样转发，其他的在你的机器上转换。原生请求优先匹配给你：Anthropic 拿到 Claude Code 的请求，ChatGPT 登录拿到 Codex 的，OpenAI 和 DeepSeek 的 key 拿到 chat 和各自的接口。

### 缓存价格 {#cache-prices}

模型服务器对从 prompt 缓存读出的输入收费更低，Anthropic 对写入缓存的输入收费更高。买家按厂商上报的缓存用量、以你的缓存价格付这部分 token：用 `share add` 的 `-cache-read`、`-cache-write` 设置，或在配置文件里写 `price_cache_read`、`price_cache_write`。默认值（读为输入价格的 1/10；写在 Anthropic 模型上为输入价格的 1.25 倍，其他模型等于输入价格）和厂商自己的比例一致。

### 用账号登录添加 {#provider-login}

```bash
{{exe}} provider login openai
```

客户端显示一个登录地址。在任意一台电脑的浏览器里打开并登录，然后按提示把授权码、或者浏览器最后跳转到的那个打不开的页面的完整地址粘贴回来。登录凭据会自动续期。Claude 和 ChatGPT 的登录可以每天定时预热 5 小时窗口，见[预热](/docs/cli#warmup)。

Google 账号没有自带项目时，`gemini` 和 `antigravity` 需要用 `-project` 指定一个 Google Cloud 项目 ID。

### 管理 provider {#provider-manage}

| 操作 | 命令 |
|---|---|
| 查看可添加的类型和已配置的 provider | `{{exe}} provider` |
| 列出 provider 可用的模型 | `{{exe}} provider models deepseek` |
| 发一条测试请求 | `{{exe}} provider test deepseek -model deepseek-flash` |
| 暂停、恢复一个 provider | `{{exe}} provider off deepseek`、`{{exe}} provider on deepseek` |
| 换 key（重新添加同名的，模型保留） | `{{exe}} provider add deepseek` |
| 删除 provider 及其凭据 | `{{exe}} provider rm deepseek` |

- **凭据的保存：** key 和登录凭据写在状态目录的 `providers.json` 里，和 `config.json` 分开，文件只有你的账户能读。命令和本机 API 都不显示完整的 key。
- **凭据失效或余额用完：** 作业会失败，`{{exe}} status` 里出现 `warning: deepseek refused its credentials ...` 或 `... is out of balance ...`。失败的作业记在你的节点上，请尽快处理。

## 和伙伴互相共享（免费） {#partners}

共享有两种方式。一种是买卖：买家付 credit，卖家赚 credit，下面「和买家配对」也属于这一种，只是限定了谁向谁买。另一种是共享伙伴：两个人配对成为伙伴之后，双方都可以使用对方的节点，**不扣费**，没有冻结、扣款，也不收平台手续费。适合两个人各有一些模型或 API 额度、想互相补位的情况。

一方生成邀请码（打印出来的形如 `ABCD-EFGH-IJKL-MNOP`，默认 24 小时有效，用 `-ttl` 修改，最长 168 小时），另一方输入：

```bash
{{exe}} partner invite
{{exe}} partner join ABCD-EFGH-IJKL-MNOP
```

之后，你请求一个模型时，协调器先在伙伴的在线节点里找。伙伴的节点能接，这次请求就是免费的。伙伴没有这个模型、不在线、正忙，或者已经用完了伙伴愿意给的额度，请求就照常到网络上付费购买。

| 操作 | 命令 |
|---|---|
| 查看伙伴、最近 24 小时互相用了多少 tokens、未使用的邀请码和你的设置 | `{{exe}} partner` |
| 只用伙伴的节点，不到网络上付费购买 | `{{exe}} partner set -only on`（`-only off` 恢复） |
| 每 24 小时最多给所有伙伴多少 tokens | `{{exe}} partner set -limit 200000`（`0` 表示不限） |
| 伙伴最多占用你每个节点并发的百分比 | `{{exe}} partner set -share 50`（`100` 表示不限，`0` 表示暂停共享） |
| 撤销一个未使用的邀请码 | `{{exe}} partner revoke 7` |
| 和某个伙伴解除共享 | `{{exe}} partner rm 42` |

- **限制只管你给出去的：** 每日上限按最近 24 小时滚动计算，所有伙伴合计。它在一次作业结束后才计入，所以到达上限时正在进行的作业可能让实际用量略超出。并发比例按每个节点的并发数算，至少 1 个，例如并发 4、比例 50% 时，伙伴最多同时占 2 个。
- **超出限制之后：** 伙伴会去网络上付费购买，网络上也可能再次撮合到你的节点，那时按你的标价正常付费。
- **只用伙伴的节点：** 打开后，伙伴的节点都不能接时，请求直接失败，不会产生费用。「可用模型」也只列出伙伴（和你自己）的模型。
- **信誉：** 伙伴之间的作业不计入节点的成功次数和信誉，市场撮合不受影响。
- **数量：** 一个邀请码只能用一次。一个账户最多同时有 5 个伙伴。
- **双方能看到什么：** 伙伴账户名的一部分，以及最近 24 小时双方互相用了多少 tokens。
- **解除：** 任何一方都可以随时解除，解除后双方都不能再免费使用对方的节点。
- **私有模式：** 下面「和买家配对」里的私有模式只针对付费的买家，伙伴不受影响。

桌面应用里在 **配对 → 共享伙伴**。

## 和买家配对 {#pairing}

配对把买家和你绑定在一起。生成一个配对码，交给你想要的买家。买家用配对码配对之后，他的请求只会发给已配对的卖家，他名下所有设备都一样。一个配对码可以让多个买家配对，数量由你决定，所以一个团队可以共用一个。

```bash
{{exe}} pair code -label team
```

命令会打印配对码，例如 `ABCD-EFGH-IJKL-MNOP`。默认 24 小时有效（用 `-ttl` 修改，最长 30 天），不限买家数量（用 `-uses` 限制）。然后每个买家运行：

```bash
{{exe}} pair join ABCD-EFGH-IJKL-MNOP
```

| 操作 | 命令 |
|---|---|
| 查看你的买家、你配对的卖家和你的配对码 | `{{exe}} pair` |
| 移除一个买家 | `{{exe}} pair rm 42` |
| 让配对码不再接纳新买家 | `{{exe}} pair revoke 7` |
| 只为配对的买家服务（私有模式），或恢复为向所有人出售 | `{{exe}} pair private on`、`{{exe}} pair private off` |
| 作为买家，解除和某个卖家的配对 | `{{exe}} pair leave 42` |

- **已配对的买家能看到什么：** 只有已配对卖家的模型。这些卖家都不提供某个模型，或者都不在线时，请求会失败，不会转给别的卖家。
- **什么不变：** 价格、计费、收据和抽检与其他作业完全一样。只是配对并不会把其他人挡在外面：没有和任何人配对的买家仍然可能被撮合到你这里。
- **私有模式：** 开启后，只有和你配对的买家会被撮合到你这里，也只有他们能看到你的模型，其他人会被分给别的卖家。关闭后恢复为向所有人出售。
- **买家的上限：** 一个买家同时最多和三个卖家配对。
- **双方能看到什么：** 你能看到每个买家账户名的一部分，他们也能看到你的一部分。
- **解除配对：** 任何一方都可以随时解除。买家没有任何配对之后，又可以向任意卖家购买。

桌面应用里有同样的功能：卖家在**配对 → 我的买家**，买家在**配对 → 我的卖家**；门户的[配对页面](/app/#/pairing)也一样。

## 并发和性能 {#tuning}

同时处理的请求数默认是 2，由配置文件里的 `share.concurrency` 控制。按你的机器能力调整，修改后运行下面的命令让它生效：

```bash
{{exe}} share on
```

## 防止作弊的机制 {#audit}

协调器会以新身份随机下几单，由参考模型逐 token 核对回答。连续两次不符，节点暂停接单 24 小时。请确保你共享的模型和它的 ID 相符，也不要虚报用量：买家会自己重算哈希，并能凭证据退款。
