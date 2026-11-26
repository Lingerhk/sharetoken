# 预热：提前启动用量窗口

[English](../warmup.md) | **简体中文**

Claude 订阅和 ChatGPT（Codex）订阅的 5 小时窗口，从窗口重置后账号的第一次请求开始计时，而不是在固定时刻开始。9:00 开始工作，窗口是 9–14、14–19；在 6:00 发一条极小的请求，窗口就是 6–11、11–16、16–21：第一个窗口基本没用，11:00 就重置，而不是 14:00。周窗口也一样：重置后一天没人用，它的结束时间也会顺延一天。

预热就是在用户希望窗口开始的时刻，发出这样一条极小的请求。参考 magpie 的每日预热实现。代码在 `internal/warmup`；卖家侧在 `internal/provider/warm.go` 和 `limits.go`。

## 1. 预热哪些账号

| 账号 | 请求怎么发 | 设置存在哪里 |
|---|---|---|
| 本机的 Claude Code（`local:claude`） | `claude` CLI，用用户自己登录的账号 | `warmup.json` |
| 本机的 Codex（`local:codex`） | `codex` CLI，走它的 ChatGPT 登录 | `warmup.json` |
| 卖家的 Claude 或 ChatGPT 登录（`provider:<id>`） | 该 provider 自己的上游，和它接单走同一条路 | `providers.json` 里该 provider 的 `warmup` |

都不经过 sharetoken 网络：不走中继、协调器、门户，不涉及买家、账本和节点。provider 的预热不是订单：不占并发名额，不产生收益也不扣 credit。只有 `anthropic` 和 `openai` 类型的 OAuth 登录有窗口可以预热；API key 类 provider 没有。

## 2. 什么时候发

每个账号有：

- **每日时间**（`times`，本机时间，最多 6 个，如 `06:00`、`11:05`）。到点时如果 5 小时窗口没在运行，就发一条请求。每个时间点每天只处理一次：记录了最近处理过的时间点，重启后不会重发。到点时机器在睡眠，醒来后 1 小时内补发；超过 1 小时就不发了，因为晚了几个小时才启动的窗口不是用户想要的。到点时窗口还在运行、但会在这 1 小时内结束的，等它结束后再启动。
- **窗口重置时预热**（`on_reset`）：`week` 表示周窗口一重置就启动，`all` 表示 5 小时窗口也一样。如果 5 小时窗口现在启动会跨过下一个每日时间，就等到那个时间再启动（2:00 结束的窗口不会马上重启，否则会跑过 6:00）；5 小时窗口空闲时，周窗口也一起等，因为一条请求会同时启动两者。从没读到过的窗口先启动一次，之后距上次预热满一个窗口长度再启动。预热后读到的窗口仍然没启动时，15 分钟后再试，每次间隔翻倍，最多 4 小时。

以下情况不发：

- 仍在运行的窗口已用完，或账号正在使用超额额度或 credits，或厂商因额度拒绝了这个账号：再发请求会按超额计费，或者直接失败（`used_up`）；
- provider 最近 10 分钟内成功处理过请求：窗口肯定在运行（`running`）；
- provider 已停用（`disabled`），或正处于 429 冷却中；
- 找不到 agent 的程序（`not_installed`）；
- 所有预热已暂停（`paused`；“立即预热”仍然可用）。

预热失败时 5 分钟后重试，每个时间点最多试 3 次。

服务每分钟按墙钟时间检查一次，所以机器睡醒后能马上发现错过的时间点。客户端必须在运行：桌面应用（`sharetoken daemon`，登录与否都行）或 `sharetoken up`。两者都不需要门户。

## 3. 不花额度地读取窗口

| 账号 | 从哪里读 |
|---|---|
| Claude Code | `claude -p /usage`，Claude Code 在本地自己回答、不调用模型：`Current session: 34% used · resets Nov 20 at 11:50am (Asia/Singapore)` |
| Codex | 用 `$CODEX_HOME/auth.json`（默认 `~/.codex`）里的 token 只读请求 `GET https://chatgpt.com/backend-api/wham/usage`；这里从不刷新 token。登录信息存在钥匙串里，或 token 已过期时，按“未知”处理 |
| provider | 经过它的每个响应头（订单或预热）：`anthropic-ratelimit-unified-5h-utilization` / `-reset`（以及 `7d`、`-status`、`-representative-claim`），`x-codex-primary-*` / `x-codex-secondary-*`（`used-percent`、`window-minutes`、`reset-at` 或 `reset-after-seconds`）。不额外发请求 |

“窗口未启动”指：用量为 0，并且重置时间未知，或距现在还有整整一个窗口（容差 10 分钟）。ChatGPT 后端对空闲窗口报告的重置时间就是“现在 + 一个窗口”。什么都读不到时，每日时间照样发请求：往正在运行的窗口里多发一条只花几个 token，不会改变什么。

本机 agent 的窗口在每日时间发请求前读一次；开启了重置时预热时每 30 分钟读一次（某个窗口到了重置时间也会马上读）；每次预热后再读一次。provider 的窗口随它的请求免费得到。

## 4. 请求本身

- **Claude Code**：在一个新的空目录里，stdin 输入 `hi`：
  `claude -p --output-format json --model haiku --tools "" --strict-mcp-config --setting-sources "" --no-session-persistence`。
  不带工具、MCP 服务、用户设置和 hooks，也不保存会话。
- **Codex**：`codex exec --skip-git-repo-check --ephemeral -s read-only -c model_provider="openai" -c model_reasoning_effort="low" [-m 模型] hi`，同样在新的空目录里运行。
- 两者运行时都去掉 `ANTHROPIC_API_KEY`、`ANTHROPIC_AUTH_TOKEN`、`ANTHROPIC_BASE_URL`、`OPENAI_API_KEY`、`OPENAI_BASE_URL` 等变量，否则请求会走 API key 或别的端点（包括 sharetoken 自己的），而不是订阅。程序所在目录和常见安装目录会加进 `PATH`，因为桌面应用启动时 PATH 很短，而 npm 安装的程序是 node 脚本。2 分钟内没有结果的命令会连同整个进程树一起结束（Windows 上不弹控制台窗口）。
- **provider**：一条流式 chat 请求，`max_tokens` 16，内容为 `hi`，走 `Entry.Upstream`：和订单相同的请求伪装、代理、TLS 和 token 续期。模型优先用设置里指定的，其次是该系列的小模型（Haiku；Codex 的 `mini`），再次是该 provider 在卖的模型；某个模型被拒（404，或与模型相关的 400）就换下一个。

agent 程序的查找顺序：设置里的路径（`binary`，必须是绝对路径，指向名为 `claude` 或 `codex` 的可执行文件，Windows 上可带 `.exe`、`.cmd`、`.bat`）→ `PATH` → 常见安装目录（`~/.claude/local`、`~/.local/bin`、Homebrew、`%APPDATA%\npm` 等）。终端应用的包装脚本（`…shims/` 目录）只在找不到其他程序时才用。

## 5. 设置与状态

`warmup.json`（0600），由 CLI 或本机 API 修改；运行中的客户端每次检查时都重新读取：

```json
{
  "paused": false,
  "claude": {"enabled": true, "times": ["06:00", "11:05"], "on_reset": "all", "model": "", "binary": ""},
  "codex":  {"enabled": false}
}
```

provider 的设置在 `providers.json` 里（同名重新登录后保留）：

```json
{"name": "max", "type": "anthropic", "auth": "oauth", "warmup": {"times": ["06:00"], "on_reset": "week", "model": ""}}
```

`warmup-state.json`（0600）按账号记录：最近一次预热、结果和错误、最近处理过的时间点、失败次数、重置时预热的退避，以及最近读到的窗口。

## 6. 命令行

```
sharetoken warmup                                   # 所有账号、时间、最近结果和窗口
sharetoken warmup set claude|codex [-times 06:00,11:05] [-on-reset off|week|all] [-model M] [-binary PATH] [-off]
sharetoken provider warmup NAME [-times 06:00] [-on-reset off|week|all] [-model M] [-off]
sharetoken warmup run [claude|codex|provider:NAME]  # 立即预热；不指定就预热所有已设置的
sharetoken warmup pause | resume
```

`-times none` 清空时间；`-model default` 恢复默认模型；`-binary auto` 恢复自动查找。客户端没运行时也能修改设置；`run` 需要客户端在运行。

## 7. 本机 API

桌面守护进程（登录与否都行）和 `sharetoken up` 都提供，需要本机 API key：

| 方法和路径 | 作用 |
|---|---|
| `GET /api/v1/warmup` | `{paused, zone, settings, targets: [...]}`；每个 target 包含设置、`skip`、`binary`/`version`、`next` 和状态（`last`、`last_try`、`result`、`error`、`last_model`、`usage`） |
| `PUT /api/v1/warmup` | `{"paused": …, "claude": {…}, "codex": {…}}`：给出的部分整体替换 |
| `PUT /api/v1/warmup/providers/{name}` | 该 provider 的 `{times, on_reset, model}`；`{}` 表示关闭 |
| `POST /api/v1/warmup/run` | `{"target": "local:claude" \| "codex" \| "provider:NAME"}`：立即预热；失败时返回 502 和该 target |

`GET /api/v1/providers` 对 Claude 或 ChatGPT 登录还会返回 `warm_capable`、`warmup` 和 `usage`（它的响应告诉的窗口）。

桌面应用在“设置 → 预热”里显示这些内容；“共享 → 厂商 API”里每个 provider 卡片也会显示它的窗口和预热设置。

## 8. 验证情况与限制

- 单元测试：`go test ./internal/warmup ./internal/provider`（每日时间、补发期限、重启、运行中和即将结束的窗口、额度用完、最近有订单、失败重试、暂停、重置时预热及其等待每日时间、`/usage` 和 `/wham/usage` 解析、用量响应头、在 mock 服务上通过 Codex 上游发 provider 预热）。
- 界面：`scripts/test/gui_ui_test.mjs` 在页面里设置预热，并对一个替身 `claude` 执行立即预热。
- 手工验证（macOS，以及 PVE 上的 Ubuntu 24.04 和 Windows 11 虚拟机；Claude Code 2.1.329、Codex 0.171.0）：立即预热成功，之后读到了窗口；到了每日时间而 5 小时窗口正在运行时记为 `running`，没有发请求；重启后不重发；暂停时不发。客户端运行环境故意设得很苛刻：`PATH` 只有 `/usr/bin:/bin`（Windows 上去掉了 npm、nodejs、`.local\bin` 目录），没有 `USER`，并设置了假的 `ANTHROPIC_*` / `OPENAI_*` 变量。Windows 上原生的 `claude.exe` 和 npm 的 `claude.cmd`、`codex.cmd` 都能用；空参数（`--tools ""`）经过 `cmd.exe` 后能原样传给程序。`/usage` 的日期在 macOS 上是 `Nov 20 at 11:50am`，在 Linux 和 Windows 上是 `Nov 20, 10:30pm`，两种都能解析。
- 还没有在真实的卖家订阅上验证；两家厂商的响应头按 Claude Code 和 Codex 自己的读法解析。
- 机器必须醒着，客户端必须在运行。想在机器睡眠时也预热，请定时唤醒机器（macOS 用 `pmset repeat wake …`，Windows 用任务计划程序并勾选“唤醒计算机”）；客户端不修改系统设置。
- 本机 agent 预热的是它的 CLI 当前登录的账号。同一个账号如果既在本机用、又被卖家出售，两边会各自预热；在运行中的窗口里多发一条 `hi` 没有害处。
- `/usage` 的输出格式会随 Claude Code 版本变化；解析不了时按“未知”处理，每日时间照样发请求。
