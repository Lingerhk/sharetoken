# 桌面客户端（macOS / Linux / Windows）

范围：邮箱注册、登录、退出，查看账户和网络的基本信息，以及卖家模式的设置（“共享”页，见下）。

## 构建类型

编译时必须指定 dev 或 prod（`client/env.go`、`gui/env.go`），两种版本可以装在同一台机器上，互不影响。普通 `go build` 没有指定，按 dev 处理，防止误连生产。

| | dev | prod |
|---|---|---|
| 门户 | `https://dev.sharetoken.app` | `https://sharetoken.app` |
| 数据目录 | `~/.sharetoken-dev` | `~/.sharetoken` |
| 本地端口（备用） | 9887（9888–9897） | 9787（9788–9797） |
| 命令行和 daemon | `sharetoken-dev` | `sharetoken` |
| 窗口程序 | `sharetoken-gui-dev` | `sharetoken-gui-prod` |
| macOS 应用 | `ShareToken-dev.app`，`app.sharetoken.desktop.dev` | `ShareToken.app`，`app.sharetoken.desktop` |
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
  └────────────┘              └───────┘ 127.0.0.1│ /api/v1/…       见下表        │
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

## 共享页（卖家模式）

主界面顶部有“概览”和“共享”两个页签（`client/ui/app.js` 的 `shareView`），选择记在 `localStorage.st_view`。共享页只调用已有的本机 API，和命令行改的是同一份 `config.json` 和 `providers.json`，两边的修改互相可见。实现说明见 [providers.md](providers.md)。

| 区域 | 功能 | 接口 |
|---|---|---|
| 共享状态 | 开启、停止共享；数据卡片：在售模型、已完成和失败的作业、进行中 / 并发数、本次运行收益和最近报价时间、账户累计卖出收益；daemon 的告警 | `POST /api/v1/share/start`、`/stop`，`GET /api/v1/status`、`/api/v1/account` |
| 中继预约 | 共享运行时显示：每个中继是否已预约、延迟、到期时间 | `GET /api/v1/status` 的 `share.reservations` |
| 最近卖出的作业 | 最近 10 条卖出作业的模型、状态、tokens 和收益 | `GET /api/v1/jobs?role=sell&limit=10` |
| 厂商 API | 添加厂商（API key 或账号登录）、启用停用、更换凭据、删除 | `/api/v1/providers`、`/api/v1/providers/oauth/start`、`/finish` |
| 厂商的模型 | 从厂商的模型列表里选（或手填），设网络 ID、输入输出价、输出上限；测试、编辑、移除 | `/api/v1/providers/{name}/models`、`/test` |
| 本机模型服务 | 地址和并发数；探测模型后添加、编辑、移除 | `PUT /api/v1/share`、`GET /api/v1/share/detect` |

- 数据来自每 5 秒一次的 `GET /api/v1/status`（`share.providers`、`share.serving`、`warnings`），列表内容没变就不重绘。表单都在对话框里，轮询不会动它，输入不会丢。
- 这些接口由 Agent 提供，所以没连上网络时共享页只显示“连接网络后才能设置共享”。
- 价格在界面上按 credit / 百万 tokens 输入，保存为 micro-credit；默认 0.1 和 0.2，和 `share add` 相同。添加模型时，如果网络上已有同名模型，会显示它的最低价。
- 账号登录分两步：页面显示登录地址（可复制，也可点击打开），用户登录后把授权码或回调地址粘贴回来。
- 界面上的保护：没有模型时不能开启共享；同一个模型 ID 不能来自两个来源；新增厂商的名称和已有的重复时要求改名（接口会按“更换凭据”处理）；最后一个可出售的模型被移除或其厂商被停用时，共享一起关闭。
- 和命令行的差别：`share add` 会顺带开启共享，界面不会，由用户点“开启共享”。
- 模型表里的“网络最低价”来自 `GET /api/v1/models`（包含本机自己的报价）；厂商卡片显示登录令牌的有效期和附加请求头的名称。概览页多了一张“共享”卡片，点击进入共享页；网络区域显示到协调器的链路质量（平均延迟、抖动、丢包）。
- 界面只有英语和简体中文，没有保存过选择时跟随系统语言，其余语言一律英语（见 [i18n.md](i18n.md)）。
- 密钥只在提交时经过页面，接口返回的是打码后的形式；界面测试会检查页面和 `/api/v1/status` 里都没有密钥。
- daemon 的告警和厂商返回的错误是英文原文，界面原样显示。

## 菜单栏和托盘

macOS 在菜单栏、Windows 在通知区显示图标。Linux 没有托盘，关窗口仍是退出，daemon 一起停止。

| 操作 | 结果 |
|---|---|
| 关闭窗口（红色按钮、X、macOS 的 Cmd+W） | 只隐藏窗口。应用和 daemon 继续运行，共享和本地 API 不中断 |
| Windows 左键点图标 | 打开窗口（最小化时恢复） |
| macOS 点图标、Windows 右键点图标 | 菜单：状态行（不可点）、有新版本时“有新版本 x”（点击打开窗口）、打开、退出 |
| 菜单里的退出、macOS 的 Cmd+Q | 退出应用；daemon 如果是这个应用启动的，一起停止 |
| 再次启动程序 | 同一数据目录只保留一个实例：第二个进程唤回窗口后退出。dev 和 prod、不同数据目录互不影响 |

- 状态行每 3 秒从 `GET /api/v1/session` 更新，如 `Connected · a@example.com`、`Not signed in`、`The background service is not running`。
- 新版本：每个新构建发一次系统通知（`gui/notify.go`，点击打开窗口），Linux 没有托盘也会发。规则见 docs/versioning.md 的“升级提醒”。Windows 上 Wails 会在 HKCU 登记通知激活器，卸载程序会删掉。
- 菜单文字跟随页面语言：窗口每次加载页面都把语言报给窗口壳（`/__shell/lang`，不到 daemon）。文字在 `gui/tray_text.go`，测试会检查它和页面的语言列表、相同词条的译文是否一致。
- Windows 11 默认把新图标收进 `^` 溢出区，用户可以拖到任务栏上。
- 图标由 `go run packaging/gen_tray_icons.go` 生成（在 `gui/` 下运行）。macOS 是模板图，由系统配色；Windows 是彩色 ICO。
- 实现：macOS 直接用 AppKit（`tray_darwin.m`），常见的 Go 托盘库和 Wails 都定义了 `AppDelegate`，链接时冲突。Windows 用 `fyne.io/systray`，运行在单独的线程上。
- **Linux 的退出：** Wails 在 goroutine 里调用 `gtk_main_quit`，GTK 不是线程安全的，信号偶尔丢失：窗口没了，进程和 daemon 还在。`quit_linux.go` 用 `OnBeforeClose` 把这个调用排到 GTK 线程。
- **Windows 的焦点保护：** Wails 收到 `WM_SETFOCUS` 就调用 WebView2 的 `MoveFocus`，失败时直接 `os.Exit(1)`。窗口最小化或隐藏时它可能失败，所以 `tray_windows.go` 在窗口过程前拦下这种状态的消息，打开窗口也不走 Wails 的 `WindowShow`。升级 Wails 时要重新跑 `tray` 自检。

## 本地 API（daemon 新增）

错误统一为 `{"error":{"code","message"}}`，界面按 `code` 显示。

| 接口 | 说明 |
|---|---|
| `GET /api/v1/session` | `{state, logged_in, portal, user, device, peer, api, error}`。`state`：`logged_out`、`expired`（门户拒绝了 token，已自动退出）、`starting`、`retrying`（`error` 是原因）、`running`。未登录时 `portal` 是内置地址（见下），`user` 是上次用的值，用于预填 |
| `POST /api/v1/portal/probe` | `{}`（`portal` 可省略，省略即内置地址）→ `{portal, https, trusted, fingerprint, subject, not_after, known, pin_changed, pin_required, registration_open, signup_credit_micros}`。发送密码前调用 |
| `POST /api/v1/register` | `{pin, email, password, device}` → 201 + session |
| `POST /api/v1/login` | `{pin, account, password, totp, device}` → 200 + session。`account` 可以是邮箱或老用户名；管理员先得到 401 `mfa_required`，带 `totp` 重发 |
| `POST /api/v1/logout` | 停 Agent、吊销 token（门户不可达时跳过）、从 config.json 删除 token，其他设置保留 |
| 登录失效 | Agent 每分钟向门户确认一次 token（门户不可达时不算失效）。设备密钥在 token 吊销后仍能和协调器交易，所以失效后必须停下：daemon 转为 `expired`，`up` 退出并提示重新登录 |
| `GET /api/v1/account`、`/api/v1/jobs`、`/api/v1/ledger` | 门户透传，不依赖 Agent。门户返回 401 时会话转为 `expired` |
| 其余 `/api/*` 和 `/v1/*` | 同 `sharetoken up`（[guide-v2.md §3](guide-v2.md)）。未登录 401 `not_logged_in`，还没连上网络 503 `not_connected` |

**门户地址是内置的**：`client.DefaultPortal = https://sharetoken.app`，登录页不再要求输入地址，打开后自动连接；连不上时显示原因和“重试”按钮。请求体里省略 `portal`（或为空）就用内置地址。测试网/预发布用运行时环境变量 `SHARETOKEN_PORTAL=https://…`（daemon 和 CLI 都认；CLI 没有 `-portal` 参数），或构建时 `-ldflags "-X sharetoken/client.DefaultPortal=…"`。已登录的账户继续用登录时保存的门户。`go test` 的 `TestMain` 把它指到不可达的 `127.0.0.1:1`，测试不会碰到正式门户。

常见错误码：`bad_portal`、`insecure_portal`、`portal_unreachable`、`not_a_portal`、`pin_required`、`pin_mismatch`、`bad_request`、`exists`、`registration_closed`、`bad_credentials`、`throttled`、`mfa_required`、`bad_code`、`mfa_setup_required`、`already_logged_in`、`bind_failed`。

## 账号就是邮箱

- 自助注册用邮箱：`POST /api/v1/register` 新增 `email` 字段，规范化（去空格、转小写）后存为账户名，因此**没有 schema 变更**，MySQL 上也不用迁移。长度上限 191（`username` 列宽）。
- 老账号（`alice`）和管理员建的用户名账号继续可用；登录框两者都接受。
- 门户默认不验证邮箱是否真实存在，开启验证码后才验证（见下）。

### 邮箱验证码

门户开启 `accounts.email_verification` 后，注册必须先取验证码，用户名注册同时关闭，详见 [mail.md](mail.md)。`/api/v1/portal/probe` 返回 `email_verification`，界面据此显示验证码输入框；`POST /api/v1/register/code {pin, email}` 发送验证码，`POST /api/v1/register` 增加 `code` 字段。

## 安全

| 问题 | 做法 |
|---|---|
| 门户证书 | 发送密码前先探测。自签名证书必须由用户核对指纹并确认；确认后固定（pin），之后变化时拒绝并显示警告。其他机器上的明文 HTTP 直接拒绝（`-allow-http` 仅供测试网） |
| token | 只在 daemon 和 config.json（0600）里；API 不返回；页面拿不到。密码只转发给门户，不落盘、不进日志 |
| 恶意网页 | 校验 Host（防 DNS rebinding）。`/api/v1/` 只接受无 Origin 或同源请求，`/v1/` 同 `up` 只放行本地来源。JSON 接口要求 `Content-Type: application/json` |
| 页面 XSS | CSP 禁止内联脚本和外部资源；DOM 只用 `textContent` 构建 |
| 换账号 | 设备密钥属于一个账户。换账号登录时旧密钥改名为 `device.key.retired-<时间>` 并生成新密钥；钱在账户上，不受影响 |

**已知未解决：**
- 本地 API 对本机其他进程没有认证（和 `up` 相同）。多用户 Linux 上别的用户可以调用 `/v1` 花你的 credit，或者调 `/api/v1/logout`。要解决需要引入本地 secret（如 0600 文件 + 会话 cookie）。
- token 明文存于 config.json（0600；Windows 上是只给本人、SYSTEM、Administrators 的 ACL），和 CLI 相同，没有进系统钥匙串。

## 匿名使用统计

做法参考 cmux 的 PostHog 接入（`Sources/PostHogAnalytics.swift`、`coderouter/src/telemetry.rs`），只在 `sharetoken daemon` 里发送，`sharetoken up` 和服务端程序不发。

- **内置 key：** sharetoken 自己的 PostHog 项目 key 硬编码在 `client/telemetry.go`（US 区域），不需要配置文件。这种 key 只能写入事件，不能读数据。`TELEMETRY_KEY` 可在构建时替换，`TELEMETRY_OFF=1` 构建完全不发送的版本（没有开关），`TELEMETRY_HOST` 可指向自建 PostHog（只接受 https 或本机地址）。cmux 里那个 key 属于上游 Manaflow 的项目，不能用在这里。
- **只有正式构建才发送：** 内置 key 只在 prod 构建中生效。`go test`、`go run`、普通 `go build` 和 dev 构建都不发送，也不显示开关。`gui_stack.sh` 用假 key 和假收集器，`gui_shell_test.sh` 设置了 `SHARETOKEN_TELEMETRY=0`。
- **用户开关：** 默认开启，登录页和主界面都有复选框（`GET`/`PUT /api/v1/telemetry`），状态存在 `telemetry.json`。关闭时清空队列、删除安装 ID。`SHARETOKEN_TELEMETRY=0` 或 `DO_NOT_TRACK=1` 强制关闭并隐藏开关。文件损坏时按关闭处理。
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
scripts/build-gui.sh dev    # 必须指定 dev 或 prod。macOS: dist/ShareToken-dev.app 和 sharetoken-dev-<版本>-macos-<arch>.dmg；Linux: dist/sharetoken-dev-<版本>-linux-<arch>/ 和 .deb（其他系统上用 scripts/build-gui-linux.sh dev [arch]，在 Docker 里构建）
scripts/build-gui.sh prod   # 正式版：dist/ShareToken.app 和 sharetoken-<版本>-macos-<arch>.dmg 等
scripts/build-gui.sh dev windows arm64   # 任何系统上都能构建：dist/sharetoken-dev-<版本>-windows-<arch>/ 和安装程序 .exe（默认 amd64）
```

- macOS、Linux 版需要 cgo，**只能在目标系统上构建**（不能从 macOS 交叉编译 Linux 版）。Windows 版不需要 cgo，可以交叉编译；运行需要 WebView2 Runtime，也没有代码签名。Windows 调试环境和平台差异见 [windows-env.md](windows-env.md)。
- Linux 构建依赖（Ubuntu 24.04）：`build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev`；运行依赖 WebKitGTK。脚本检测到 4.1 时加 `webkit2_41` 标签，只有 4.0 的旧系统不加。Linux 包里的 `install-linux.sh` 安装到 `~/.local` 并添加应用菜单项。
- macOS 版只有 ad-hoc 签名：本机可运行，分发给别人需要 Developer ID 签名和公证，否则会被 Gatekeeper 拦下。
- 日志：窗口启动的 daemon 写到数据目录的 `daemon.log`（prod 是 `~/.sharetoken`，dev 是 `~/.sharetoken-dev`；Windows 在 `%USERPROFILE%` 下）。

## 命令行版

三个系统都提供不带窗口的命令行包，给服务器和不装桌面的用户。程序和桌面包里的 `sharetoken-<env>` 是同一个，名字也相同。

```bash
scripts/build-cli.sh dev               # 本机系统
scripts/build-cli.sh prod linux amd64  # macos、linux、windows，amd64、arm64；不需要 cgo，任何系统都能构建
scripts/build-cli.sh prod all          # 全部 6 个包
```

产物是 `dist/sharetoken-cli-<版本>-<系统>-<架构>.tar.gz`（dev 构建是 `sharetoken-cli-dev-…`）（Windows 是 `.zip`），列入 `dist/SHA256SUMS-<版本>`，里面有程序和 `README.txt`。macOS 程序只有链接器的 ad-hoc 签名，Windows 程序没有代码签名，下载后的提示和桌面版相同。

全流程测试：`scripts/gui_stack.sh start DIR && . DIR/env && PORTAL=$PORTAL PIN=$PIN scripts/cli_test.sh <sharetoken-dev 程序>`（注册、运行、查询、经网络购买推理、停止、登录、退出）。

Ubuntu 上的测试环境见 [linux-env.md](linux-env.md)。

也可以不用窗口：`sharetoken daemon` 后用浏览器打开 `http://127.0.0.1:9787/`（dev：`sharetoken-dev daemon`，端口 9887）。

## 测试

| 层 | 命令 | 覆盖 |
|---|---|---|
| Daemon API | `go test -race ./client` | 注册登录退出的正常和失败路径、证书确认和固定、MFA、token 吊销、用户停用、换账号、并发登录、请求防护、日志不含密钥 |
| 账号规则 | `go test ./accounts ./portal` | 邮箱校验、规范化、管理接口 URL 含 `@` 和 `+`。MySQL：`SHARETOKEN_TEST_MYSQL=… go test ./portal` |
| 全链路 API | `go test -race -run Daemon ./e2e` | 真实 libp2p 拓扑上：注册、经中继接入、买模型、记账、重启恢复、退出、网络恢复后自动接入 |
| 使用统计 | `go test -race -run Telemetry ./client` | 用假收集器验证事件格式、不含个人信息、无 key 时不发送、开关和环境变量、每日/每小时去重、收集器宕机或挂起时不影响使用 |
| 窗口壳 | `cd gui && go test -tags desktop,production .`（Linux 再加 `webkit2_41`） | 启动、连接、停止 daemon，端口冲突，转发 |
| 界面 | `scripts/gui_stack.sh start DIR && node scripts/gui_ui_test.mjs DIR` | 无头 Chrome 走全部界面流程，对接真实进程（中继、HTTPS 门户、协调器、卖家、daemon）。共享页用 `custom` 厂商指向 mock 模型服务：添加、选模型、定价、开启、测试、改价、本机模型、停用、移除；账号登录只验证到登录地址和错误的回调 |
| 真实 WebView | `scripts/gui_shell_test.sh <dev\|prod> <程序目录>` | 不显示窗口，用系统 WebView 加载页面并 POST 到真实 daemon，验证退出时 daemon 一起停 |
| 菜单栏和托盘 | `scripts/gui_tray_test.sh <dev\|prod> <程序目录>`（macOS；Windows 见 [windows-env.md](windows-env.md)） | 窗口和图标会显示约 15 秒。经系统路径操作真实的图标、菜单和窗口：关闭、打开、最小化恢复、快速连点、语言跟随、再次启动、退出后 daemon 停止 |
