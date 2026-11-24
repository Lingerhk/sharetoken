# 命令参考

所有命令的格式：

```bash
{{exe}} [-home DIR] [-log-dir DIR] <command> [flags]
```

`-home` 要写在命令**前面**，用来指定另一个数据目录。`-log-dir` 也写在命令前面，指定 `up` 和 `daemon` 把日志写到哪里（见[日志和崩溃报告](#logs)）。不带参数运行可以看到简短的帮助。

## 账户 {#account}

### login {#login}

用发到邮箱的验证码登录，并绑定这台设备。没有账户的邮箱会自动创建账户。

```bash
{{exe}} login -email you@example.com
```

| 参数 | 含义 |
|---|---|
| `-email ADDR` | 你的邮箱，不给时询问 |
| `-send` | 只发送验证码就退出，之后用 `-code` 登录 |
| `-code CODE` | 已经收到的验证码。不给时会发送一封并询问 |
| `-invite CODE` | 门户要求邀请码时，新账户的邀请码 |
| `-totp CODE` | 开启了两步验证时，身份验证器里的动态码。不给时询问 |
| `-device NAME` | 设备名称，默认是主机名 |
| `-portal-pin FP` | 门户使用自签名证书时，运营者公布的证书指纹（`sha256:…`）。不给时显示指纹并请你确认 |
| `-allow-http` | 允许用明文 HTTP 连接另一台机器上的门户，只用于测试网络 |

`-email` 默认是上次登录用的邮箱。`-send` 和 `-code` 不能同时使用。

### logout {#logout}

吊销这台机器的 token，并从配置文件删除。正在运行的客户端也会停止。

### balance {#balance}

显示余额、冻结额、买入和卖出的统计，以及设备数量。`account` 是它的别名。

### jobs {#jobs}

```bash
{{exe}} jobs -role sell -n 50
```

| 参数 | 含义 |
|---|---|
| `-role buy\|sell` | 只看买入或卖出，默认都看 |
| `-n N` | 显示多少条，默认 20 |

## 运行 {#run}

### up {#up}

运行客户端。`run` 是它的别名。需要先登录。

| 参数 | 含义 |
|---|---|
| `-http ADDR` | 本地 API 地址，默认 `127.0.0.1:{{port}}` |
| `-listen MADDRS` | libp2p 监听地址，逗号分隔 |
| `-nat=false` | 关闭 UPnP、NAT-PMP 和打洞 |

这些参数都只对这次运行有效，不写入配置文件。

### daemon {#daemon}

运行桌面应用背后的服务，提供[网页界面](/docs/desktop#browser-ui)。参数和 `up` 相同，另外接受 `-allow-http`（见 [login](#login)）。和 `up` 不同的是，它未登录时也能运行，注册和登录通过它的页面完成。

### status {#status}

显示连接、中继、共享和直连隧道的状态，有新版本时也会提示。需要客户端正在运行。

### models {#models}

列出网络上的模型、节点数、TEE 节点数和最低价。需要客户端正在运行。`-codex <文件>` 改为把这些模型写成 Codex 的模型目录文件（见[接入 Codex](/docs/use-models#codex)）。

### relays {#relays}

显示各中继的延迟。`-measure` 立即重新测速。需要客户端正在运行。

| 命令 | 含义 |
|---|---|
| `relays` | 每个中继、延迟，以及本客户端用它做什么 |
| `relays -measure` | 先重新测速 |
| `relays add <地址>` | 经自己的中继共享，见[自建中继](/docs/relay) |
| `relays remove <地址或 ID>` | 不再使用它 |

### tailnet {#tailnet}

直连隧道（见[直连隧道](/docs/running#tailnet)）。设置在客户端没运行时也能改，正在运行的客户端会立即按修改生效。

| 命令 | 含义 |
|---|---|
| `tailnet` | 是否打开、本机作为卖家经哪个 DERP 区域可达、每条隧道的路径和传输 |
| `tailnet on` | 打开。`-wait <毫秒>`：对新节点的请求最多等这么久隧道，超时就走中继（默认 0，最大 10000）；`-derp-map <URL 或文件>`：自己的 DERP 表（默认用 tailcat 的公共中继）；`-transport quic\|tcp`：本机连卖家时隧道里跑什么（默认 `quic`） |
| `tailnet off` | 关闭，回到中继和打洞 |

### apikey {#apikey}

本地端点的 API key（见[调用模型](/docs/use-models#endpoint)）。客户端没运行时也能用，正在运行的客户端会立即按修改生效。

| 命令 | 含义 |
|---|---|
| `apikey` | 显示端点、key，以及是否需要 key |
| `apikey -q` | 只打印 key，供脚本使用：`export OPENAI_API_KEY=$({{exe}} apikey -q)` |
| `apikey on` / `apikey off` | 要求 key（默认），或不带 key 也放行 |
| `apikey rotate` | 生成新 key，旧 key 立即失效 |

## 共享 {#share}

| 命令 | 作用 |
|---|---|
| `share` | 显示共享设置 |
| `share detect [-upstream URL]` | 列出本机模型服务的模型 |
| `share add <model> [-provider NAME] [-id ID] [-in P] [-out P] [-cache-read P] [-cache-write P] [-max-output N]` | 共享一个模型，同时打开共享。价格单位是每百万 token 的 micro-credit，默认 `-in 100000`、`-out 200000`；`-provider`：模型来自某个[托管 API](#providers)，默认是本机模型服务；`-id`：网络上的模型 ID，默认等于 `<model>`；`-max-output`：模型自己的单次输出上限 |
| `share rm <id>` | 移除一个模型（本机模型服务的或托管 API 的） |
| `share on`、`share off` | 打开或暂停共享 |
| `share upstream <url>` | 设置本机模型服务，默认 `http://127.0.0.1:11434/v1` |

`<model>` 必须紧跟在 `add` 后面，参数写在它之后。缓存价格见[缓存价格](/docs/share#cache-prices)。设置在客户端没运行时也能改，正在运行的客户端会立即生效。详见[共享模型](/docs/share)。

## 托管 API {#providers}

把用 API key 或账号登录接入的托管 API 的模型共享出去。凭据保存在数据目录的 `providers.json` 里，和配置文件分开。

| 命令 | 作用 |
|---|---|
| `provider` | 可以添加的类型，以及已经添加的 provider |
| `provider add <kind> [-name N] [-url URL] [-header K=V] [-no-check]` | 用 API key 添加：`anthropic`、`openai`、`gemini`、`grok`、`deepseek`，或任意 OpenAI 兼容接口 `custom`（用 `-url` 给出地址）。key 会询问，也可以从标准输入或 `SHARETOKEN_PROVIDER_KEY` 读取，不要写在命令行上。`-name`：名字，默认是类型；`-header`：额外的请求头，可重复；`-no-check`：不先向厂商验证 key |
| `provider login <kind> [-name N] [-project ID]` | 用账号登录添加：`anthropic`、`openai`、`gemini`、`antigravity`、`grok`。`-project`：Google 账号没有自己的 Code Assist 项目时，用哪个 Google Cloud 项目 |
| `provider login anthropic [-mode full\|inference] [-session-key]` | Anthropic：`-mode` 是授权范围；`-session-key` 改用 sessionKey cookie 登录（输入时不显示） |
| `provider models <name>` | 它的凭据可以用哪些模型 |
| `provider test <name> [-model M] [-prompt TEXT]` | 经它发一条小请求，显示回答和 token 数。默认问它出售的第一个模型 |
| `provider on <name>`、`provider off <name>` | 出售它的模型，或暂停 |
| `provider configure <name> [-proxy-mode environment\|direct\|custom] [-proxy URL] [-profile standard\|claude-code] [-sampling strict\|default] [-tls-profile native\|chrome] [-token-tls-profile native\|chrome]` | 代理、Anthropic 的请求方式和 TLS 设置。`add` 和 `login` 也接受这些参数。含密码的代理地址用 `-proxy-mode custom` 加 `SHARETOKEN_PROVIDER_PROXY` |
| `provider warmup <name> …` | 预热订阅账号的用量窗口，见[预热](#warmup) |
| `provider rm <name>` | 删除它和它的凭据 |

```bash
{{exe}} provider add deepseek
{{exe}} provider models deepseek
{{exe}} share add deepseek-flash -provider deepseek
```

详见[共享托管 API](/docs/share#providers)。

## 配对 {#pairing}

卖家生成配对码并分发出去。买家用它配对之后，只向已配对的卖家购买。一个配对码可以让多个买家配对。

| 命令 | 作用 |
|---|---|
| `pair` | 显示你和谁配对，以及你的配对码 |
| `pair code [-label L] [-ttl 24h] [-uses N] [-approve]` | 卖家：生成一个给买家用的配对码，同时打印它的邀请链接。默认 24 小时有效（最长 7 天），不限买家数量；`-approve`：每个买家要你确认才生效 |
| `pair join <code\|link>` | 买家：用配对码或邀请链接，和生成它的卖家配对 |
| `pair approve <request id>`、`pair reject <request id>` | 卖家：确认或拒绝一个等待你确认的买家 |
| `pair withdraw <request id>` | 买家：撤回自己等待确认的申请 |
| `pair leave <seller id>` | 买家：解除和某个卖家的配对 |
| `pair rm <buyer id>` | 卖家：移除一个买家 |
| `pair revoke <code id>` | 卖家：让配对码不再接纳新买家，已配对的买家不受影响 |
| `pair private on\|off` | 卖家：只为和你配对的买家服务，或者恢复为向所有人出售 |

```bash
{{exe}} pair code -label team -ttl 72h
{{exe}} pair join ABC-DEF
{{exe}} pair join https://sharetoken.app/p#7kQm3xV9
```

命令里的 id 就是 `pair` 列出的编号。详见[和买家配对](/docs/share#pairing)。

## 共享伙伴 {#partners}

两个用户配对成为伙伴后，双方优先使用对方的节点，不扣费；伙伴的节点不能接时，再到网络上付费购买。

| 命令 | 作用 |
|---|---|
| `partner` | 显示伙伴、最近 24 小时互相用了多少 tokens、未使用的邀请码和你的设置 |
| `partner invite [-ttl 24h] [-uses N] [-approve]` | 生成一个邀请码和它的邀请链接，默认只能一个人用（`-uses` 最多 100 人）。默认 24 小时有效（最长 168 小时）；`-approve`：对方要你确认才生效 |
| `partner join <code\|link>` | 用邀请码或邀请链接，成为生成它的人的伙伴 |
| `partner approve <request id>`、`partner reject <request id>` | 确认或拒绝一个等待你确认的人 |
| `partner withdraw <request id>` | 撤回自己等待确认的申请 |
| `partner rm <user id>` | 和某个伙伴解除共享 |
| `partner revoke <invite id>` | 撤销一个未使用的邀请码 |
| `partner set [-only on\|off] [-limit N] [-share P]` | `-only`：只用伙伴的节点；`-limit`：每 24 小时最多给所有伙伴的 tokens（0 不限）；`-share`：伙伴最多占用你每个节点并发的百分比（0 暂停） |

```bash
{{exe}} partner invite
{{exe}} partner join ABC-DEF
{{exe}} partner set -limit 200000 -share 50
```

详见[和伙伴互相共享](/docs/share#partners)。

## 邀请链接 {#invite-links}

配对码和伙伴邀请码都有一个邀请链接，形如 `https://sharetoken.app/p#…`（配对）或 `https://sharetoken.app/s#…`（共享伙伴），生成时会一起打印出来。收到链接的人不需要分清是哪一种：

| 命令 | 作用 |
|---|---|
| `accept <link\|code>` | 接受一个邀请链接，或任意一种邀请码 |

```bash
{{exe}} accept https://sharetoken.app/s#Hx82LpQe
```

链接属于哪个门户，就只能在登录那个门户的客户端里接受。详见[邀请链接](/docs/share#invite-links)。

## Claude Code 和 Codex {#connect}

`connect` 让 Claude Code 或 Codex 使用本地端点。它修改工具自己的配置文件，修改前先备份到数据目录的 `backups` 目录。工具已经登录官方账号（claude.ai 或 ChatGPT）时，默认保留这个登录：模型菜单里先列官方模型，后面是网络上的模型（以 `st-` 开头），每个请求按所选模型分流。

| 命令 | 作用 |
|---|---|
| `connect` | 两个工具是否已连接、配置文件位置、是否登录了官方账号 |
| `connect claude\|codex [-subscription auto\|on\|off] [-openai-provider] [-force] [-off]` | 连接一个工具。`-subscription`：是否保留官方登录，默认 `auto`（已登录就保留）；`-openai-provider`：Codex 使用它内置的 OpenAI provider（以前的对话仍会列出）；`-force`：替换指向其他网关的地址；`-off`：把配置文件恢复原样 |
| `official` | 客户端看到的订阅用量，以及额度用完时怎么处理 |
| `official fallback on\|off` | Claude Code：订阅额度用完后改由网络上的模型继续（默认打开） |
| `official fallback-model <model\|auto>` | 接手的模型，`auto` 表示选和所请求模型最接近的 |
| `official guard <percent>` | Codex：额度窗口用到这个百分比时暂停官方模型（默认 95） |
| `codex-unlock on\|off` | Codex Desktop（macOS、Windows）：ChatGPT 额度用完后不锁输入框，网络上的模型可以继续用。需要信任一张只对 localhost 有效的证书。不带参数时显示状态 |
| `autostart on\|off` | 登录系统时自动运行客户端。不带参数时显示状态 |

```bash
{{exe}} connect claude
{{exe}} connect codex -off
{{exe}} official fallback off
```

这些命令在客户端没运行时也能用。已连接的工具连官方模型也要经过客户端，所以客户端需要一直运行。改完后重启工具。详见[同时使用 Claude 或 ChatGPT 订阅](/docs/use-models#subscription)。

## 编程助手 {#agents}

`skill install` 让本机的 Claude Code 和 Codex 学会使用客户端：查看状态和余额、通过本地接口调用模型、共享模型。它在 `~/.claude` 和 `~/.codex` 下写入 `skills/sharetoken/SKILL.md`（Windows 上在 `%USERPROFILE%` 下；设置了 `CLAUDE_CONFIG_DIR`、`CODEX_HOME` 时以它们为准）。这个 skill 要求助手在修改设置或大量花费 credit 之前先征得你的同意。

| 命令 | 作用 |
|---|---|
| `skill` | 找到了哪些助手，各自是否已装好 skill |
| `skill install [-agent claude\|codex\|all] [-force]` | 安装，默认装给找到的每个助手。客户端更新后再运行一次 |
| `skill uninstall [-agent …] [-force]` | 卸载，默认从两个助手中都删除 |
| `skill show` | 打印 skill 内容 |

```bash
{{exe}} skill install
{{exe}} skill install -agent codex
```

同名但不是客户端写的文件不会被覆盖或删除，除非加 `-force`。已打开的助手会话重启后才会加载。详见[在编程助手中使用](/docs/agents)。

## 预热 {#warmup}

Claude 和 ChatGPT（Codex）订阅的 5 小时窗口从重置后的第一次请求开始计时。客户端可以在你设定的时间发一条极小的请求，让窗口提前开始，工作时更早重置：在 6:00 预热，窗口是 6–11、11–16、16–21；9:00 才开始用，一天只有两个窗口。本机的 Claude Code 和 Codex（通过它们自己的命令）以及你出售的 Claude、ChatGPT 登录都可以预热。不经过 sharetoken 网络。

| 命令 | 作用 |
|---|---|
| `warmup` | 所有可以预热的账号、设定的时间、最近一次预热和用量窗口 |
| `warmup set claude\|codex [-times 06:00,11:05] [-on-reset off\|week\|all] [-model M] [-binary PATH] [-off]` | 本机的 Claude Code 或 Codex。`-model`：用哪个模型发请求（`default` 表示最便宜的）；`-binary`：程序位置（`auto` 表示自动查找）；`-off`：关闭 |
| `provider warmup <名字> [-times 06:00] [-on-reset off\|week\|all] [-model M] [-off]` | 你出售的 Claude 或 ChatGPT 登录 |
| `warmup run [claude\|codex\|provider:<名字>]` | 立即预热（不指定就预热所有已设置的） |
| `warmup pause`、`warmup resume` | 暂停所有自动预热，或恢复 |

```bash
{{exe}} warmup set claude -times 06:00,11:05
{{exe}} warmup set codex -on-reset all
{{exe}} warmup run claude
```

到点时只有 5 小时窗口没在运行才会发请求；错过 1 小时以上（比如电脑在睡眠）就不补发。`-on-reset week` 表示周窗口一重置就启动，`all` 表示 5 小时窗口也一样。窗口已用完或正在使用超额额度时不发送。`-times none` 清除定时。设置在客户端没运行时也能改，但客户端运行时才会预热（桌面应用，或 `up`），`warmup run` 也需要它在运行；时间按本机时区。桌面应用的“设置 → 预热”里有同样的设置。

## 日志和崩溃报告 {#logs}

### logs {#logs-cmd}

```bash
{{exe}} logs
{{exe}} logs -n 50 -f
{{exe}} logs path
```

输出客户端日志的最后几行（默认 200 行，`-n` 指定行数），加 `-f` 会持续输出新的日志。`-gui` 改为输出桌面应用自己的日志。`logs path` 输出日志目录。无论客户端是怎么启动的（终端、桌面应用、开机自启），它都自己把日志写到该目录下的 `client.log`；文件超过 10 MB 时改名为 `client.log.1`，依次类推，保留 5 个旧文件（都是默认值）。目录和相关设置见[日志和崩溃报告](/docs/config#logs)。

### diagnostics {#diagnostics}

```bash
{{exe}} diagnostics
{{exe}} diagnostics off
{{exe}} diagnostics show
```

显示是否发送崩溃报告、判断出的所在地区、有几份待发送，以及日志目录。`on`、`off` 用来开关；`off` 同时删除待发送的报告。`show` 原样输出将要发送的内容。客户端崩溃或从 panic 中恢复时，会把脱敏后的报告随你的登录发给门户：错误信息、出错的代码位置，以及出错前的最后几行日志。默认开启，欧洲经济区默认关闭。报告包含哪些内容见[崩溃报告](/docs/config#crash-reports)。

## 其他 {#other}

| 命令 | 作用 |
|---|---|
| `config` | 显示配置文件路径和内容，token 只显示开头 |
| `version [--check]` | 显示版本、网络、门户、数据目录和本地 API 地址。不写任何文件。`--check` 再向门户查询有没有新版本 |
| `upgrade [-y]` | 安装最新版本：下载安装包、校验发布签名，按这份程序的安装方式安装。安装前会询问，`-y` 不询问。见[一键升级](/docs/update#one-click) |

## 环境变量 {#env}

| 变量 | 作用 |
|---|---|
| `SHARETOKEN_HOME` | 数据目录，和 `-home` 相同 |
| `SHARETOKEN_LOG_DIR` | 日志目录，优先于 `-log-dir` 和配置里的 `log.dir` |
| `SHARETOKEN_PROVIDER_KEY` | `provider add` 使用的 API key，代替交互输入 |
| `SHARETOKEN_PROVIDER_PROXY` | provider 的代理地址，在 `-proxy-mode custom` 而没给 `-proxy` 时使用。含密码的地址放在这里，不写在命令行上 |
| `SHARETOKEN_REGION` | 用两个字母表示你所在的国家（如 `DE`），在系统的地区设置不能反映你所在地时使用。它决定崩溃报告默认开还是关 |
