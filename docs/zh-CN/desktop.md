# 桌面客户端（macOS、Linux、Windows）

[English](../desktop.md) | **简体中文**

桌面客户端的范围：用邮箱注册、登录、退出，查看账户和网络的基本信息，以及卖家模式的设置（“共享”页，见下）。

## 构建类型

每个客户端在编译时确定为 `dev` 或 `prod`（[`internal/client/env.go`](../../internal/client/env.go)、[`gui/env.go`](../../gui/env.go)）。两种版本可以装在同一台机器上，互不影响。普通 `go build` 没有指定类型，按 `dev` 处理，防止漏写参数时误连生产环境。

| | dev | prod |
|---|---|---|
| 门户 | `https://dev.sharetoken.app` | `https://sharetoken.app` |
| 数据目录 | `~/.sharetoken-dev` | `~/.sharetoken` |
| 本地端口（备用） | 9887（9888–9897） | 9787（9788–9797） |
| 命令行和 daemon | `sharetoken-dev` | `sharetoken` |
| 窗口程序 | `sharetoken-gui-dev` | `sharetoken-gui` |
| macOS 应用 | `ShareToken-dev.app`，`app.sharetoken.desktop.dev` | `ShareToken.app`，`app.sharetoken.desktop` |
| `SHARETOKEN_PORTAL` | 生效（测试网络用） | 忽略 |

- `sharetoken version`（`sharetoken-dev version`）显示构建类型、门户和数据目录。
- 窗口只连接同类型的 daemon。dev 窗口和页面标题都显示 DEV。
- `SHARETOKEN_HOME` 可以改变数据目录。
- `config.json` 里记有文件格式版本和写入它的构建类型。更新的客户端写的文件会被拒绝。格式过旧、不再支持的文件会连同 `device.key`、`network.json` 一起删除，用户需要重新登录。另一种构建类型的文件只报错、不删除，这样 `SHARETOKEN_HOME` 指错目录时不会删掉另一个环境的身份。

## 结构

```
sharetoken-gui（Wails 窗口，gui/）          sharetoken daemon（internal/client/daemon.go）

  ┌──────────────┐  fetch   ┌───────┐  HTTP       ┌──────────────────────────────────────┐
  │ 系统 WebView │ ───────▶ │ 代理  │ ──────────▶ │ /          页面（internal/client/ui） │
  └──────────────┘ （同源） └───────┘  127.0.0.1  │ /api/v1/…  见下表                     │
                                                  │ /v1/…、其余 /api → Agent              │
                                                  └──────────────────────────────────────┘
                                                      │ HTTPS               │ libp2p
                                                      ▼                     ▼
                                                    门户            中继 → 协调器 / 卖家
```

- **所有逻辑都在 daemon 里。** 页面只调用本地 API。窗口负责找到或启动 daemon、转发请求，并在退出时停掉自己启动的 daemon。
- 窗口壳是**独立的 Go module**（`gui/`）。只有它需要 WebView（macOS、Linux 上还需要 cgo），所以服务端程序和命令行的依赖、构建都不受影响。
- 页面由 daemon 提供，所以全部功能可以脱离窗口测试：API 用 Go 测试，页面用无头 Chrome。
- Daemon **未登录也能运行**（`sharetoken up` 不行）。登录后它在后台启动 Agent。网络连不上时自动重试，间隔从 2 秒退避到 1 分钟；账户信息仍可查看，因为它只依赖门户。
- 已有 daemon 在运行时，窗口直接连上去，退出时不动它。
- 默认端口被无关程序占用时，窗口把 daemon 启动在第一个空闲的备用端口上，页面显示实际地址。`config.json` 里指定的端口不会被替换，这时直接报错。同一数据目录上已有 `sharetoken up` 在运行时也报错，否则同一把设备密钥会同时在线两次。
- 窗口持有它所启动的 daemon 的标准输入（`-stop-on-stdin-eof`）。窗口退出或崩溃时管道关闭，daemon 正常收尾后退出。

## 窗口布局

窗口是一个小窗口（默认 880×600，最小 760×520），页面按应用而不是网页来排版（[`internal/client/ui/app.css`](../../internal/client/ui/app.css)）：

- **窗口本身不滚动。** 左侧是图标导航（概览、共享、配对、记录；底部是反馈和设置），中间是当前视图，底部是状态栏。长列表在各自的面板里滚动；窗口太矮放不下时，滚动的是视图，不是窗口。
- **视图**：概览（余额等卡片、本地接口地址、网络和可用模型并排）、共享（标签：状态、厂商 API、本机模型）、配对（标签：我的卖家、我的买家）、记录（标签：全部、卖出）、设置（账户和退出登录、语言、版本和检查更新、反馈、我的设备）。所有视图都留在页面里，轮询会更新隐藏的视图，切换时立即显示。
- **按需展开**：面板的说明文字收在标题旁的“?”里，点击才显示；反馈是一个对话框；升级提示只占一行，详情在对话框里。
- **状态栏**：连接状态、当前中继和它的延迟、账户、版本。
- **延迟的颜色**和门户一致：低于 80 ms 绿色，低于 250 ms 黄色，其余红色（`app.js` 的 `rtt`）。网络面板的中继表、链路质量、中继预约和状态栏都用它。
- 文字默认不可选中，只有适合复制的内容（地址、代码、表格、提示）可以选中。
- **macOS** 隐藏标题栏（`mac.TitleBarHiddenInset`），页面一直铺到窗口顶部。窗口壳在页面加载后给 `<html>` 加上 `os-<系统>` 类；`os-darwin` 下侧边栏给红绿灯按钮留出位置，侧边栏和视图标题栏可以拖动窗口（`--wails-draggable`），其中的按钮不可拖。Windows 和 Linux 保留系统标题栏。

## 共享页（卖家模式）

侧边栏的“共享”打开共享视图（[`internal/client/ui/app.js`](../../internal/client/ui/app.js) 的 `shareView`），当前视图记在 `localStorage.st_view`。共享页只调用本机 API，和命令行改的是同一份 `config.json` 和 `providers.json`，两边的修改互相可见。实现说明见 [providers.md](providers.md)。

| 区域 | 功能 | 接口 |
|---|---|---|
| 共享状态 | 开启、停止共享。数据卡片：在售模型、已完成和失败的作业、进行中 / 并发数、本次运行收益和最近报价时间、账户累计卖出收益。daemon 的告警 | `POST /api/v1/share/start`、`/stop`，`GET /api/v1/status`、`/api/v1/account` |
| 中继预约 | 共享运行时显示：每个中继是否已预约、延迟、到期时间 | `GET /api/v1/status` 的 `share.reservations` |
| 最近卖出的作业 | 最近 10 条卖出作业的模型、状态、tokens 和收益 | `GET /api/v1/jobs?role=sell&limit=10` |
| 厂商 API | 添加厂商（API key 或账号登录）、启用停用、更换凭据、删除 | `/api/v1/providers`、`/api/v1/providers/oauth/start`、`/finish` |
| 厂商的模型 | 从厂商的模型列表里选（或手填），设网络 ID、输入输出价、输出上限；测试、编辑、移除 | `/api/v1/providers/{name}/models`、`/test` |
| 本机模型服务 | 地址和并发数；探测模型后添加、编辑、移除 | `PUT /api/v1/share`、`GET /api/v1/share/detect` |
| 配对 | 私有模式（只为配对的买家服务）。生成配对码（备注；有效期 1、7 或 30 天；可配对的买家数）、复制、撤销；和本账户配对的买家，以及移除其中一个 | `GET /api/v1/pairing`、`POST /api/v1/pairing/codes`、`DELETE /api/v1/pairing/codes/{id}`、`DELETE /api/v1/pairing/buyers/{id}`、`PUT /api/v1/pairing/private` |

- 数据来自每 5 秒一次的 `GET /api/v1/status`（`share.providers`、`share.serving`、`warnings`）。列表内容没变就不重绘。表单都在对话框里，轮询不会动它，输入不会丢。
- 这些接口由 Agent 提供，所以在接入网络之前，共享页只提示连接网络后才能设置共享。
- 价格在界面上按 credit / 百万 tokens 输入，保存为 micro-credit。默认 0.1 和 0.2，和 `share add` 相同。添加模型时，如果网络上已有同名模型，表单会显示它的最低价。
- 账号登录分两步：页面显示登录地址（可复制，也可点击打开），用户登录后把授权码或回调地址粘贴回来。
- 界面上的保护：没有模型时不能开启共享；同一个模型 ID 不能来自两个来源；新增厂商的名称和已有的重复时要求改名（接口会按“更换凭据”处理）；最后一个可出售的模型被移除或其厂商被停用时，共享一起关闭。
- 和命令行的一处差别：`share add` 会顺带开启共享，界面不会，由用户点“开启共享”。
- 模型表里的“网络最低价”来自 `GET /api/v1/models`（包含本机自己的报价）。厂商卡片显示登录令牌的有效期和附加请求头的名称。概览页有一张“共享”卡片，点击进入共享页；网络区域显示到协调器的链路质量（平均延迟、抖动、丢包）。
- 配对（`app.js` 的 `pairingView`）有自己的视图，分两个标签。“我的卖家”：输入卖家的配对码，查看本账户配对的卖家，解除其中一个（`POST /api/v1/pairing/redeem`、`DELETE /api/v1/pairing/sellers/{id}`）；账户有配对时，这里会说明它只向这些卖家购买，“可用模型”也只列出他们的模型。“我的买家”：私有模式、配对码和配对的买家。配对保存在门户，所以接入网络之前就能用；每 15 秒重新读取一次。
- 客户端有英语和简体中文两种语言。没有保存过选择时跟随系统语言，其他系统语言一律显示英语（见 [i18n.md](i18n.md)）。
- 密钥只在提交时经过页面，接口返回的是打码后的形式。界面测试会检查页面和 `/api/v1/status` 里都没有密钥。
- daemon 的告警和厂商返回的错误按英文原文显示。

## 菜单栏和托盘

macOS 在菜单栏、Windows 在通知区显示图标。Linux 没有托盘：关闭窗口就是退出应用，daemon 一起停止。

| 操作 | 结果 |
|---|---|
| 关闭窗口（红色按钮、X、macOS 的 Cmd+W） | 只隐藏窗口。应用和 daemon 继续运行，共享和本地 API 不中断 |
| Windows 左键点图标 | 打开窗口（最小化时恢复） |
| macOS 点图标、Windows 右键点图标 | 菜单：状态行、信息行（当前中继和延迟、共享中的在售模型数、可用 credit，都不可点）、有新版本时的更新行（点击打开窗口）、打开、退出 |
| 菜单里的退出、macOS 的 Cmd+Q | 退出应用；daemon 如果是这个应用启动的，一起停止 |
| 再次启动程序 | 同一数据目录只保留一个实例：第二个进程唤回窗口后退出。dev 和 prod、不同数据目录互不影响 |

- 状态行每 3 秒从 `GET /api/v1/session` 更新，例如 `已连接 · a@example.com`、`未登录`、`后台服务未运行`。
- 信息行只在已连接时显示，同样每 3 秒从 `GET /api/v1/status` 更新：`中继：relay-sg · 42 ms`（延迟取当前中继的实时 ping 均值，没有时取上次测速）、`正在共享 · 在售模型：2`。`可用 credit：4.99` 每分钟从 `GET /api/v1/account` 取一次，因为它要问门户。鼠标停在图标上的提示也带上中继和延迟。
- 新版本的系统通知会带上发布说明里的一句话摘要（见 [versioning.md](versioning.md) 的“发布说明”）。
- 新版本：每个新构建发一次系统通知（[`gui/notify.go`](../../gui/notify.go)，点击打开窗口）。Linux 没有托盘也会发。规则见 [versioning.md](versioning.md) 的“升级提醒”。Windows 上 Wails 会在 HKCU 登记通知激活器，卸载程序会删掉它。
- 菜单文字跟随页面语言：页面每次加载都把自己的语言报给窗口壳（`/__shell/lang`，不到 daemon）。文字在 [`gui/tray_text.go`](../../gui/tray_text.go)，有测试检查它和页面的语言列表、相同词条的译文是否一致。
- Windows 11 默认把新图标收进 `^` 溢出区，用户可以把它拖到任务栏上。
- 图标由 `go run packaging/gen_tray_icons.go` 生成（在 `gui/` 下运行）。macOS 是模板图，由系统配色；Windows 是彩色 ICO。
- 实现：macOS 直接用 AppKit（`tray_darwin.m`），因为常见的 Go 托盘库和 Wails 各自定义了 `AppDelegate`，链接时冲突。Windows 用 `fyne.io/systray`，运行在单独的线程上。
- **Linux 的退出：** Wails 在 goroutine 里调用 `gtk_main_quit`。GTK 不是线程安全的，信号偶尔丢失：窗口没了，进程和 daemon 还在。`quit_linux.go` 用 `OnBeforeClose` 把这个调用排到 GTK 线程上。
- **Windows 的焦点保护：** Wails 收到 `WM_SETFOCUS` 就调用 WebView2 的 `MoveFocus`，失败时直接 `os.Exit(1)`，而窗口最小化或隐藏时它可能失败。`tray_windows.go` 在 Wails 的窗口过程之前拦下这种状态下的消息，打开窗口也不走 Wails 的 `WindowShow`。升级 Wails 后要重新跑托盘自检。

## daemon 的本地 API

错误统一为 `{"error":{"code","message"}}`，页面按 `code` 选择显示的文字。

| 接口 | 说明 |
|---|---|
| `GET /api/v1/session` | `{env, state, logged_in, first_run, portal, user, device, peer, api, error, version, api_version, update}`。`state` 是 `logged_out`、`expired`（门户拒绝了 token，daemon 已退出登录）、`starting`、`retrying`（`error` 是原因）或 `running`。未登录时 `portal` 是内置地址（见下），`user` 是上次用的值，用于预填表单。`first_run` 为 true 表示 daemon 启动时没有 `config.json`，且之后还没有人登录过：这时应用先显示注册页，而不是登录页 |
| `POST /api/v1/portal/probe` | `{}`（`portal` 可省略，默认是内置地址）→ `{portal, https, trusted, fingerprint, subject, not_after, known, pin_changed, pin_required, registration_open, signup_credit_micros, email_verification, support_email}`。发送密码之前调用 |
| `POST /api/v1/register/code` | `{pin, email}`：门户要求验证码时，发送验证码 |
| `POST /api/v1/register` | `{pin, email, password, code, device}` → 201 和会话 |
| `POST /api/v1/login` | `{pin, account, password, totp, device}` → 200 和会话。`account` 是邮箱或用户名。管理员先得到 401 `mfa_required`，带上 `totp` 重发 |
| `POST /api/v1/logout` | 停止 Agent、吊销 token（门户不可达时跳过），并从 `config.json` 删除 token；其他设置保留 |
| `GET /api/v1/account`、`/api/v1/jobs`、`/api/v1/ledger` | 透传到门户，不依赖 Agent。门户返回 401 时会话转为 `expired` |
| `POST /api/v1/update/check`、`/skip`、`/notified` | 升级提醒，见 [versioning.md](versioning.md) |
| `POST /api/v1/feedback`、`GET /api/v1/feedback/mine` | 向门户提交反馈，以及列出本账户提交过的反馈 |
| `/api/v1/pairing…` | 配对码和配对，转给门户（[guide.md](guide.md) 第 3 节） |
| 其余 `/api/*` 和 `/v1/*` | 同 `sharetoken up`（见 [guide.md](guide.md)）。未登录时 401 `not_logged_in`，还没接入网络时 503 `not_connected` |

**登录失效。** Agent 每分钟向门户确认一次 token 是否仍然有效（门户不可达不算被拒绝）。设备密钥在 token 吊销后仍能和协调器交易，所以客户端必须停下：daemon 转为 `expired`，`sharetoken up` 退出并提示重新登录。

**门户地址是内置的。** 它就是构建类型对应的门户（`client.BuiltInPortal`）。登录页不要求输入地址，打开后自动连接；连不上时显示原因和“重试”按钮。请求里省略 `portal`（或为空）就用内置地址。dev 构建可以用环境变量 `SHARETOKEN_PORTAL=https://…` 指向测试网络（daemon 和命令行都认；命令行没有 `-portal` 参数）。prod 构建忽略它，这样环境变量无法把密码引到别处。已登录的账户继续用登录时保存的门户。这个包的 Go 测试在 `TestMain` 里把该变量指到不可达的 `127.0.0.1:1`，所以测试不会碰到真实门户。

常见错误码：`bad_portal`、`insecure_portal`、`portal_unreachable`、`not_a_portal`、`pin_required`、`pin_mismatch`、`bad_request`、`exists`、`registration_closed`、`email_required`、`code_cooldown`、`bad_credentials`、`throttled`、`mfa_required`、`bad_code`、`mfa_setup_required`、`already_logged_in`、`bind_failed`、`bad_origin`、`bad_host`。

## 账号就是邮箱

- 自助注册使用邮箱。邮箱规范化（去空格、转小写）后存为账户名，长度上限 191 个字符。
- 管理员用普通用户名创建的账号同样可用；登录框两者都接受。
- 门户默认不验证邮箱是否真实存在，开启验证码后才验证。

### 邮箱验证码

门户设置 `accounts.email_verification` 后，注册需要邮件验证码，用户名注册同时关闭，详见 [mail.md](mail.md)。`/api/v1/portal/probe` 返回 `email_verification`，页面据此显示验证码输入框。`POST /api/v1/register/code {pin, email}` 发送验证码，`POST /api/v1/register` 用 `code` 字段带上它。

## 安全

| 问题 | 客户端的做法 |
|---|---|
| 门户证书 | 发送密码前先探测门户。自签名证书必须由用户核对指纹并确认；确认后固定（pin），之后发生变化时拒绝并显示警告。发往其他机器的明文 HTTP 直接拒绝（`-allow-http` 仅供测试网络） |
| token | 只在 daemon 和 `config.json`（权限 0600）里。API 不返回它，页面读不到它。密码只转发给门户，不落盘、不进日志 |
| 恶意网页 | 校验 `Host` 头（防 DNS rebinding）。`/api/v1/` 只接受没有 `Origin` 或同源的请求；`/v1/` 和 `sharetoken up` 一样只放行本地来源。JSON 接口要求 `Content-Type: application/json` |
| 页面 XSS | CSP 禁止内联脚本和外部资源；DOM 只用 `textContent` 构建 |
| 换账号 | 设备密钥属于一个账户。换账号登录时旧密钥改名为 `device.key.retired-<时间>` 并生成新密钥。credit 记在账户上，不受影响 |

**已知未解决：**

- 本地 API 对本机其他进程没有认证（`sharetoken up` 也一样）。在多用户的 Linux 机器上，别的用户可以调用 `/v1` 花你的 credit，或者调用 `/api/v1/logout`。要解决需要引入本地 secret（例如 0600 文件加会话 cookie）。
- token 明文存于 `config.json`（0600；Windows 上是只给本人、SYSTEM 和 Administrators 的 ACL），和命令行相同，没有放进系统钥匙串。

## 构建

```bash
scripts/build-gui.sh dev                 # 必须指定构建类型；为本机系统构建
scripts/build-gui.sh prod macos arm64    # 在 Mac 上，两种架构都可以
scripts/build-gui.sh prod windows amd64  # 任何系统上都能构建
scripts/build-gui-linux.sh prod arm64    # Linux 包，任何装有 Docker 的系统上都能构建
```

| 系统 | `dist/` 里的产物（prod 的名字；dev 构建会带 `-dev`） |
|---|---|
| macOS | `ShareToken.app`（dev：`ShareToken-dev.app`）和 `sharetoken-<版本>-macos-<架构>.dmg` |
| Linux | `sharetoken-<版本>-linux-<架构>/` 和 `sharetoken-<版本>-linux-<架构>.deb` |
| Windows | `sharetoken-<版本>-windows-<架构>/` 和安装程序 `sharetoken-<版本>-windows-<架构>.exe` |

`dist/SHA256SUMS-<版本>` 列出这些安装包。

- macOS 和 Linux 的窗口壳需要 cgo，要在目标系统上构建（Linux 版不能从 macOS 交叉编译；`scripts/build-gui-linux.sh` 改为在 Docker 容器里构建）。Windows 的窗口壳不需要 cgo，可以在任何系统上交叉编译。它运行时需要 WebView2 Runtime，没有代码签名。安装程序用 `makensis` 制作；没有安装 `makensis` 时，在 `scripts/build-gui-linux.sh` 的 Docker 镜像里制作。
- Linux 构建依赖（Debian/Ubuntu 24.04 的包名）：`build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev`。运行时需要 WebKitGTK。脚本检测到 WebKitGTK 4.1 时加上 `webkit2_41` 构建标签，只有 4.0 的旧系统不加。`.deb` 把程序装到 `/usr/bin`，并添加应用菜单项。
- macOS 应用只有 ad-hoc 签名。它能在构建它的机器上运行；分发给别人需要 Developer ID 签名和公证，否则会被 Gatekeeper 拦下。
- 日志：窗口启动的 daemon 写到数据目录的 `daemon.log`（prod 是 `~/.sharetoken`，dev 是 `~/.sharetoken-dev`；Windows 上在 `%USERPROFILE%` 下）。

## 命令行包

三个系统都有不带窗口的包，给服务器和不想装桌面应用的用户。里面的程序和桌面包里的 `sharetoken`（或 `sharetoken-dev`）是同一个，名字也相同。

```bash
scripts/build-cli.sh dev               # 本机系统
scripts/build-cli.sh prod linux amd64  # macos、linux 或 windows；amd64 或 arm64。不需要 cgo，任何系统都能构建任何目标
scripts/build-cli.sh prod all          # 全部六个包
```

产物是 `dist/sharetoken-cli-<版本>-<系统>-<架构>.tar.gz`（Windows 是 `.zip`；dev 构建名为 `sharetoken-cli-dev-…`），列入 `dist/SHA256SUMS-<版本>`。包里有程序和 `README.txt`。macOS 程序只有链接器的 ad-hoc 签名，Windows 程序没有代码签名，所以下载后系统给出的提示和桌面应用相同。

全流程测试（注册、运行、查询、经网络购买一次推理、停止、登录、退出）对接 `scripts/gui_stack.sh` 的本机网络。它需要 dev 构建，因为 prod 构建忽略 `SHARETOKEN_PORTAL`：

```bash
scripts/gui_stack.sh start DIR && . DIR/env
PORTAL=$PORTAL PIN=$PIN scripts/cli_test.sh <sharetoken-dev 程序的路径>
scripts/gui_stack.sh stop DIR
```

页面也可以不通过窗口使用：运行 `sharetoken daemon`，然后用浏览器打开 `http://127.0.0.1:9787/`（dev：`sharetoken-dev daemon`，端口 9887）。

## 测试

通用的构建和测试说明见 [development.md](development.md)。`scripts/test.sh` 带着本机的 MySQL 服务运行 `go test`，依赖门户的测试需要它。桌面客户端自己的测试：

| 层 | 命令 | 覆盖 |
|---|---|---|
| Daemon API | `scripts/test.sh -race ./internal/client` | 注册、登录、退出的正常和失败路径；证书确认和固定；MFA；token 吊销；用户停用；换账号；并发登录；请求防护；日志不含密钥 |
| 账号规则 | `scripts/test.sh ./internal/accounts ./internal/portal` | 邮箱校验和规范化；管理接口 URL 含 `@` 和 `+` |
| 全链路 API | `scripts/test.sh -race -run Daemon ./test/e2e` | 在真实的 libp2p 拓扑上：注册、经中继接入、购买模型、记账、重启后恢复、退出、网络恢复后自动接入 |
| 窗口壳 | `cd gui && go test -tags desktop,production .`（Linux 再加 `webkit2_41`） | 启动、连接、停止 daemon；端口冲突；转发 |
| 页面 | `scripts/gui_stack.sh start DIR && node scripts/gui_ui_test.mjs DIR` | 无头 Chrome 走完页面的全部流程，对接真实进程（中继、HTTPS 门户、协调器、卖家、daemon）。共享页用 `custom` 厂商指向 mock 模型服务：添加、选模型、定价、开启、测试、改价、本机模型、停用、移除。账号登录只验证到登录地址和错误的回调。配对：错误的码和自己的码，和不卖任何模型的卖家配对（没有模型、请求被拒）以及和有模型的卖家配对，生成和撤销配对码，买家加入和被移除，在页面上开关私有模式及其对未配对买家的效果 |
| 真实 WebView | `scripts/gui_shell_test.sh <dev\|prod> <程序目录>` | 不显示窗口，用系统 WebView 加载页面并向真实 daemon 发 POST；检查应用退出时 daemon 一起停止 |
| 菜单栏和托盘 | `scripts/gui_tray_test.sh <dev\|prod> <程序目录>`（macOS） | 窗口和图标会显示约 15 秒。经系统路径操作真实的图标、菜单和窗口：关闭、打开、最小化和恢复、快速连点、语言跟随、再次启动、退出后 daemon 停止。Windows 上用 `SHARETOKEN_GUI_SELFTEST=tray` 启动应用即可运行同一套自检 |
