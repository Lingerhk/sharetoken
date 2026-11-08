# 桌面客户端（macOS / Linux / Windows）

第一版范围：邮箱注册、登录、退出，查看账户和网络的基本信息。共享模型的设置仍用命令行（`/api/share` 已可用，界面以后加）。

## 构建类型

编译时必须指定 dev 或 prod（`client/env.go`、`gui/env.go`），两种版本可以装在同一台机器上，互不影响。普通 `go build` 没有指定，按 dev 处理，防止误连生产。

| | dev | prod |
|---|---|---|
| 门户 | `https://dev.sharetoken.app` | `https://sharetoken.app` |
| 数据目录 | `~/.sharetoken-dev` | `~/.sharetoken` |
| 本地端口（备用） | 9887（9888–9897） | 9787（9788–9797） |
| 命令行和 daemon | `sharetoken-dev` | `sharetoken-prod` |
| 窗口程序 | `sharetoken-gui-dev` | `sharetoken-gui-prod` |
| macOS 应用 | `sharetoken-dev.app`，`app.sharetoken.desktop.dev` | `sharetoken-prod.app`，`app.sharetoken.desktop` |
| 使用统计 | 不发送 | 发送 |
| `SHARETOKEN_PORTAL` | 生效（测试网络用） | 忽略 |

- `sharetoken-<env> version` 显示程序的构建类型、门户和目录。
- 窗口只连接同类型的 daemon，dev 窗口和页面标题都显示 DEV。
- **旧配置不兼容：** `config.json` 没有 `env` 字段时，启动时直接删除它和 `device.key`、`network.json`，回到未登录状态，需要重新登录。`telemetry.json` 保留，里面有用户的统计退出选择。
- `config.json` 的 `env` 是另一种类型时只报错，不删除，防止 `SHARETOKEN_HOME` 指错目录时删掉另一个环境的身份。

## 结构

```
sharetoken-gui（Wails 窗口，gui/）          sharetoken daemon（client/daemon.go）
  ┌────────────┐  同源 fetch   ┌───────┐  HTTP   ┌──────────────────────────┐
  │ 系统 WebView│ ───────────▶ │ 代理   │ ──────▶ │ /            页面（client/ui）│
  └────────────┘              └───────┘ 127.0.0.1│ /api/…       见下表        │
                                                 │ /v1/…、其余 /api → Agent   │
                                                 └──────────────────────────┘
                                                     │ HTTPS              │ libp2p
                                                     ▼                    ▼
                                                   门户              中继 → 协调器 / 卖家
```

- **所有逻辑都在 daemon 里**，界面只调本地 API。窗口只负责找到或启动 daemon、转发请求、退出时停掉自己启动的 daemon。
- 窗口壳是**独立的 Go module**（`gui/`）：只有它需要 WebView（macOS、Linux 上还需要 cgo），服务端程序和 CLI 的依赖、构建都不受影响。
- 页面由 daemon 提供，所以同一套功能可以脱离窗口测试：API 层用 Go 测试，界面用无头 Chrome。
- Daemon **未登录也能运行**（`sharetoken up` 不行）；登录后在后台拉起 Agent。网络连不上时自动重试（2 秒到 1 分钟退避），账户信息仍可查看，因为它只依赖门户。
- 已有 daemon 在运行时窗口直接连上去，退出时不动它。
- 默认端口 9787 被无关程序占用时（比如另一个数据目录上的 `sharetoken up`），daemon 改用 9788–9797 中第一个空闲端口，页面上显示实际地址。`config.json` 里写了其他端口时不换，直接报错。同一数据目录上已有 `sharetoken up` 时也报错，否则同一把设备密钥会同时在线两次。
- 窗口启动的 daemon 由它持有 stdin（`-stop-on-stdin-eof`）：窗口退出或崩溃时管道关闭，daemon 正常收尾后退出。

## 本地 API（daemon 新增）

错误统一为 `{"error":{"code","message"}}`，界面按 `code` 显示。

| 接口 | 说明 |
|---|---|
| `GET /api/session` | `{state, logged_in, portal, user, device, peer, api, error}`。`state`：`logged_out`、`expired`（门户拒绝了 token，已自动退出）、`starting`、`retrying`（`error` 是原因）、`running`。未登录时 `portal` 是内置地址（见下），`user` 是上次用的值，用于预填 |
| `POST /api/portal/probe` | `{}`（`portal` 可省略，省略即内置地址）→ `{portal, https, trusted, fingerprint, subject, not_after, known, pin_changed, pin_required, registration_open, signup_credit_micros}`。发送密码前调用 |
| `POST /api/register` | `{pin, email, password, device}` → 201 + session |
| `POST /api/login` | `{pin, account, password, totp, device}` → 200 + session。`account` 可以是邮箱或老用户名；管理员先得到 401 `mfa_required`，带 `totp` 重发 |
| `POST /api/logout` | 停 Agent、吊销 token（门户不可达时跳过）、从 config.json 删除 token，其他设置保留 |
| `GET /api/account`、`/api/jobs`、`/api/ledger` | 门户透传，不依赖 Agent。门户返回 401 时会话转为 `expired` |
| 其余 `/api/*` 和 `/v1/*` | 同 `sharetoken up`（[guide-v2.md §3](guide-v2.md)）。未登录 401 `not_logged_in`，还没连上网络 503 `not_connected` |

**门户地址是内置的**：`client.DefaultPortal = https://sharetoken.app`，登录页不再要求输入地址，打开后自动连接；连不上时显示原因和“重试”按钮。请求体里省略 `portal`（或为空）就用内置地址。测试网/预发布用运行时环境变量 `SHARETOKEN_PORTAL=https://…`（daemon 和 CLI 都认；CLI 没有 `-portal` 参数），或构建时 `-ldflags "-X sharetoken/client.DefaultPortal=…"`。已登录的账户继续用登录时保存的门户。`go test` 的 `TestMain` 把它指到不可达的 `127.0.0.1:1`，测试不会碰到正式门户。

常见错误码：`bad_portal`、`insecure_portal`、`portal_unreachable`、`not_a_portal`、`pin_required`、`pin_mismatch`、`bad_request`、`exists`、`registration_closed`、`bad_credentials`、`throttled`、`mfa_required`、`bad_code`、`mfa_setup_required`、`already_logged_in`、`bind_failed`。

## 账号就是邮箱

- 自助注册用邮箱：`POST /api/v1/register` 新增 `email` 字段，规范化（去空格、转小写）后存为账户名，因此**没有 schema 变更**，MySQL 上也不用迁移。长度上限 191（`username` 列宽）。
- 老账号（`alice`）和管理员建的用户名账号继续可用；登录框两者都接受。
- 门户默认不验证邮箱是否真实存在，开启验证码后才验证（见下）。

### 邮箱验证码

门户开启 `accounts.email_verification` 后，注册必须先取验证码，用户名注册同时关闭，详见 [mail.md](mail.md)。`/api/portal/probe` 返回 `email_verification`，界面据此显示验证码输入框；`POST /api/register/code {pin, email}` 发送验证码，`POST /api/register` 增加 `code` 字段。

## 安全

| 问题 | 做法 |
|---|---|
| 门户证书 | 发送密码前先探测。自签名证书必须由用户核对指纹并确认；确认后固定（pin），之后变化时拒绝并显示警告。其他机器上的明文 HTTP 直接拒绝（`-allow-http` 仅供测试网） |
| token | 只在 daemon 和 config.json（0600）里；API 不返回；页面拿不到。密码只转发给门户，不落盘、不进日志 |
| 恶意网页 | 校验 Host（防 DNS rebinding）。`/api/` 只接受无 Origin 或同源请求，`/v1/` 同 `up` 只放行本地来源。JSON 接口要求 `Content-Type: application/json` |
| 页面 XSS | CSP 禁止内联脚本和外部资源；DOM 只用 `textContent` 构建 |
| 换账号 | 设备密钥属于一个账户。换账号登录时旧密钥改名为 `device.key.retired-<时间>` 并生成新密钥；钱在账户上，不受影响 |

**已知未解决：**
- 本地 API 对本机其他进程没有认证（和 `up` 相同）。多用户 Linux 上别的用户可以调用 `/v1` 花你的 credit，或者调 `/api/logout`。要解决需要引入本地 secret（如 0600 文件 + 会话 cookie）。
- token 明文存于 config.json（0600；Windows 上是只给本人、SYSTEM、Administrators 的 ACL），和 CLI 相同，没有进系统钥匙串。

## 匿名使用统计

做法参考 cmux 的 PostHog 接入（`Sources/PostHogAnalytics.swift`、`coderouter/src/telemetry.rs`），只在 `sharetoken daemon` 里发送，`sharetoken up` 和服务端程序不发。

- **内置 key：** sharetoken 自己的 PostHog 项目 key 硬编码在 `client/telemetry.go`（US 区域），不需要配置文件。这种 key 只能写入事件，不能读数据。`TELEMETRY_KEY` 可在构建时替换，`TELEMETRY_OFF=1` 构建完全不发送的版本（没有开关），`TELEMETRY_HOST` 可指向自建 PostHog（只接受 https 或本机地址）。cmux 里那个 key 属于上游 Manaflow 的项目，不能用在这里。
- **只有正式构建才发送：** 内置 key 只在 `scripts/build-gui.sh` 这类设置了版本号的构建中生效。`go test`、`go run`、普通 `go build` 和 dev 构建都不发送，只有 prod 构建才发送，也不显示开关。`gui_stack.sh` 用假 key 和假收集器，`gui_shell_test.sh` 设置了 `SHARETOKEN_TELEMETRY=0`。
- **用户开关：** 默认开启，登录页和主界面都有复选框（`GET`/`PUT /api/telemetry`），状态存在 `telemetry.json`。关闭时清空队列、删除安装 ID。`SHARETOKEN_TELEMETRY=0` 或 `DO_NOT_TRACK=1` 强制关闭并隐藏开关。文件损坏时按关闭处理。
- **发送方式：** `POST <host>/batch/`，尽力而为：不阻塞调用方，失败即丢弃，不重试，退出时最多等 2 秒。

| 事件 | 附加字段 | 时机 |
|---|---|---|
| `sharetoken_daily_active`、`sharetoken_hourly_active` | `state`、`sharing` | 每个 UTC 日或小时最多一次，每 10 分钟检查 |
| `sharetoken_signed_in` | `method`（login、register） | 登录或注册成功 |
| `sharetoken_sign_in_failed` | `method`、`reason`（白名单错误码，其余为 other） | 登录或注册失败 |
| `sharetoken_signed_out` | `reason`（user、expired） | 退出或登录失效 |
| `sharetoken_network_joined` | `duration`（分桶） | 接入网络 |
| `sharetoken_network_join_failed` | 无 | 每次登录最多一次 |

每个事件还带 `schema_version`、`os`、`arch`、`app_version`，`distinct_id` 是随机的安装 ID（`install:<随机值>`），与账户和设备密钥无关。字段由固定结构体 `telemetryProps` 限定，不能发送邮箱、设备 ID、门户地址、错误原文、提示词或模型内容，新增字段时不要放自由文本。

PostHog 服务端仍能看到请求来源 IP：事件设置了 `$geoip_disable`，并关闭了 person profile，但要彻底不留 IP，需在 PostHog 项目设置里打开“Discard client IP data”。

## 构建

```bash
scripts/build-gui.sh dev    # 必须指定 dev 或 prod。macOS: dist/sharetoken-dev.app 和 zip；Linux: dist/sharetoken-dev-linux-<arch>/ 和 tar.gz
scripts/build-gui.sh prod   # 正式版：dist/sharetoken-prod.app 等
scripts/build-gui.sh dev windows arm64   # 任何系统上都能构建：dist/sharetoken-dev-windows-<arch>/ 和 zip（默认 amd64）
```

- macOS、Linux 版需要 cgo，**只能在目标系统上构建**（不能从 macOS 交叉编译 Linux 版）。Windows 版不需要 cgo，可以交叉编译；运行需要 WebView2 Runtime，也没有代码签名。Windows 调试环境和平台差异见 [windows-env.md](windows-env.md)。
- Linux 构建依赖（Ubuntu 24.04）：`build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev`；运行依赖 WebKitGTK。脚本检测到 4.1 时加 `webkit2_41` 标签，只有 4.0 的旧系统不加。Linux 包里的 `install-linux.sh` 安装到 `~/.local` 并添加应用菜单项。
- macOS 版只有 ad-hoc 签名：本机可运行，分发给别人需要 Developer ID 签名和公证，否则会被 Gatekeeper 拦下。
- 日志：窗口启动的 daemon 写到数据目录的 `daemon.log`（prod 是 `~/.sharetoken`，dev 是 `~/.sharetoken-dev`；Windows 在 `%USERPROFILE%` 下）。

也可以不用窗口：`sharetoken-prod daemon` 后用浏览器打开 `http://127.0.0.1:9787/`（dev：`sharetoken-dev daemon`，端口 9887）。

## 测试

| 层 | 命令 | 覆盖 |
|---|---|---|
| Daemon API | `go test -race ./client` | 注册登录退出的正常和失败路径、证书确认和固定、MFA、token 吊销、用户停用、换账号、并发登录、请求防护、日志不含密钥 |
| 账号规则 | `go test ./accounts ./portal` | 邮箱校验、规范化、管理接口 URL 含 `@` 和 `+`。MySQL：`SHARETOKEN_TEST_MYSQL=… go test ./portal` |
| 全链路 API | `go test -race -run Daemon ./e2e` | 真实 libp2p 拓扑上：注册、经中继接入、买模型、记账、重启恢复、退出、网络恢复后自动接入 |
| 使用统计 | `go test -race -run Telemetry ./client` | 用假收集器验证事件格式、不含个人信息、无 key 时不发送、开关和环境变量、每日/每小时去重、收集器宕机或挂起时不影响使用 |
| 窗口壳 | `cd gui && go test -tags desktop,production .`（Linux 再加 `webkit2_41`） | 启动、连接、停止 daemon，端口冲突，转发 |
| 界面 | `scripts/gui_stack.sh start DIR && node scripts/gui_ui_test.mjs DIR` | 无头 Chrome 走全部界面流程，对接真实进程（中继、HTTPS 门户、协调器、卖家、daemon） |
| 真实 WebView | `scripts/gui_shell_test.sh <程序目录>` | 不显示窗口，用系统 WebView 加载页面并 POST 到真实 daemon，验证退出时 daemon 一起停 |
