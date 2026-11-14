# 版本与升级

[English](../versioning.md) | **简体中文**

发布版本说明一个程序是哪份代码。接口版本决定两个程序能不能互通。两者分开管理，下面的规则约束所有改动。

## 发布版本

- **一个版本号：** 全仓只有一个 semver，写在根目录的 `VERSION` 文件里。客户端、桌面窗口壳、中继、协调器、门户和邮件服务共用一个 tag。
- **格式：** tag `v1.2.3` 上没有改动的构建是 `1.2.3`。其他构建是 `1.2.3-dev.<commit>[.dirty]`（[`scripts/build/version.sh`](../../scripts/build/version.sh)）。没有注入版本的 `go build` 是 `dev`。
- **注入：** [`scripts/build/ldflags.sh`](../../scripts/build/ldflags.sh) 把版本、提交和构建时间写入 `sharetoken/internal/version`。桌面窗口壳是独立的 module，用 `main.version`。
- **查看：** `portal|coordinator|mailserver|relay|sharetoken version`，每个程序日志的第一行，门户的 `GET /api/v1/version`，以及管理后台的“版本”页。

**从 1.0.1（2026-11-14）起，发布会升版本号。** 在此之前每次发布都是 1.0.0，靠移动 tag `v1.0.0`，所以各个 1.0.0 构建并不相同，`commit` 才能区分它们（`GET /api/v1/version`、管理后台“版本”页）；客户端靠比较提交和构建时间来发现同一版本的更新构建（见“升级提醒”）。协议 ID 和签名域在首次公开发布前仍沿用 1.0.0 / v1 的名字，格式变化仍然原地修改。

发版步骤：修改 `VERSION`；在更新日志页（[`internal/portal/web/changelog.html`](../../internal/portal/web/changelog.html)，网址 `/changelog/`）最上面加上这个版本及其功能，并补齐翻译；提交；`git tag v1.2.3`；然后构建。

## 接口版本

| 接口 | 版本在哪 | 当前 |
|---|---|---|
| 门户 HTTP API | 路径 | `/api/v1` |
| 客户端本地 API | 路径 | `/api/v1`（OpenAI 接口仍是 `/v1`） |
| 控制面（客户端 ↔ 协调器） | libp2p 协议 ID | `/sharetoken/ctrl/1.0.0` |
| 数据面（买家 ↔ 节点） | libp2p 协议 ID，加上每单协商的 `infer_version` | `/sharetoken/infer/1.0.2`，版本 1 |
| 中继凭证 | libp2p 协议 ID，加上签名域 | `/sharetoken/relay-pass/1.0.0`，`sharetoken/relay-pass/v1`（签名覆盖整段 body，所以新增字段不需要新版本） |
| 签名消息 | 签名域 | `sharetoken/offer/v1`、`sharetoken/ticket/v1`、`sharetoken/receipt/v1`、`sharetoken/ack/v1` |
| 数据库 | `schema_migrations`，按组件编号 | `core` 3，`mail` 1，`portal` 2 |
| 服务端配置 | YAML 文件里的 `version` | 1 |
| 客户端配置 | `config.json` 里的 `version` | 1 |

**客户端如何上报版本：**

- 对门户：每个请求带 `User-Agent: sharetoken-client/<版本> (<env>; <os>/<arch>)` 和 `X-ST-Client-Version`。每个响应带 `X-ST-Server-Version`。
- 对协调器和节点：打开请求的那一帧带 `ver`（`protocol.SendRequest`）。
- 所有 libp2p 节点：identify 里的 `UserAgent` 是 `sharetoken-<程序>/<版本> (<os>/<arch>)`。协调器据此记录中继的版本。
- Offer 里有已签名的 `client_version` 和 `infer_versions` 字段，Ticket 里有已签名的 `infer_version`。协调器只撮合 infer 版本相通的买卖双方。

## 版本策略

客户端版本策略存在共享数据库的 `meta.version_policy`。门户和协调器读的是同一条记录（协调器最多缓存 30 秒），所以修改它不需要重新部署。在管理后台的“版本”页修改，或者用：

```bash
portal -config portal.yaml version-policy                      # 查看
portal -config portal.yaml version-policy set -min 1.0.0 -recommended 1.0.0 -latest 1.0.0 \
  -download-url https://sharetoken.app/#download -allow-dev=false
```

| 操作 | 客户端低于最低版本、是开发构建而 `allow_dev` 关闭，或没有带版本时 |
|---|---|
| `POST /api/v1/auth/verify`、`/mfa`、`/devices` | 门户返回 426，错误码 `upgrade_required` |
| `announce`、`ticket_request` | 协调器返回 `upgrade_required`；本地 OpenAI 接口返回 426 |
| `settle`、`started`、`abandon`、`dispute`、`evidence`、`relay-pass`、账户查询 | **永不拦截：** 结算和争议涉及资金 |
| 网页登录（`/api/v1/web/*`）、`/api/v1/auth/code` | 不受影响 |

版本号是客户端自报的，所以这项校验只为兼容，不是安全手段。数据库里没有策略时不拦任何客户端。

开发构建 `1.2.3-dev.x` 按 `1.2.3` 比较。开发用的网络设 `allow_dev: true`；生产网络设为 `false`。

## 升级提醒

客户端（桌面应用的 daemon 和 `sharetoken up`）在启动时和之后每小时询问门户：`GET /api/v1/update?kind=desktop|cli&channel=prod|dev&os=…&arch=…`。门户返回：

- 版本策略（`min`、`recommended`、`latest`、`download_url`）。`latest` 为空时用下载目录里的发布；`download_url` 为空时是 `/#download`。
- `release`：下载目录里的 `RELEASE-<版本>.json`（发布时由 [`scripts/release/release-files.sh`](../../scripts/release/release-files.sh) 写入：版本、提交、构建时间），加上更新日志链接 `/changelog/#v<版本>`。
- `package`：这个发布里对应客户端种类、构建类型、系统和架构的安装包，带大小和 sha256。dev 客户端拿到的是 `sharetoken-cli-dev-*`。dev 桌面安装包不发布，所以 dev 桌面应用拿不到安装包。

客户端在以下情况判断有新版本：自己的版本低于 `min`（必须升级）、`recommended` 或 `latest`；或者发布是**同一版本号的更新构建**：提交不同、构建时间更晚，而且有这个客户端的安装包。1.0.1 之前每次发布都是 1.0.0，所以起作用的是后一条；同一版本号重新构建发布时，它仍然有用。一次发布的所有包共用 `ST_BUILD_TIME`，由 `ldflags.sh` 写进 `version.BuildTime`。

提醒出现在这些位置：

- 桌面应用页面顶部的一行：版本、发布说明的摘要、“查看详情”和“跳过此版本”。“查看详情”打开对话框：发布说明全文、下载链接（本系统的安装包，没有就是下载页）和“更新内容”。必须升级时显示为红色，说明已有作业照常结算，并且不能跳过。设置里的“检查更新”立即询问门户，有新版本（包括跳过的）就打开这个对话框。
- 托盘菜单：“有新版本 x”或“需要升级到 x”；点击打开窗口。
- 系统通知，每个新构建一次（通过 Wails 的通知接口；macOS 首次会请求权限）。daemon 在数据目录的 `update.json` 里记下已跳过和已通知的构建，所以重启后不会重复。
- 命令行：`sharetoken up` 在日志里写一行 WARN；`sharetoken status` 会显示；`sharetoken version --check` 直接询问门户，不需要客户端在运行。

本地 API：`GET /api/v1/session` 里的 `update`，以及有新版本时 `GET /api/v1/status` 里的 `update`；`POST /api/v1/update/check`、`POST /api/v1/update/skip` 和 `POST /api/v1/update/notified`（请求体 `{"key"}`；key 不是当前提供的构建时返回 409 `not_offered`）。

### 一键升级

客户端可以自己下载并安装新构建，前提是这个包带有**发布密钥**的签名。

- **签名：** 发布密钥是一把 Ed25519 密钥，私钥离线保存（默认 `local/keys/release-signing.pem`，或 `$ST_RELEASE_KEY`），在 GitHub Actions 上发布时存为 `release` 环境的 secret（见“发布安装包”），公钥编译进客户端（[`internal/client/upgrade.go`](../../internal/client/upgrade.go) 的 `ReleaseKey`，[`gui/update.go`](../../gui/update.go) 里有同一个，测试保证两处一致）。签名的对象是文件的 SHA-256（对 32 字节摘要做 Ed25519），这也是 Wails 更新器 `ed25519` 算法校验的内容。[`scripts/release/sign-packages.go`](../../scripts/release/sign-packages.go) 生成密钥和签名；`scripts/release/release-files.sh` 先确认密钥和客户端里的公钥一致，再给每个包签名，写成 `SIGNATURES-<版本>`（格式同 `SHA256SUMS`，每行 `<base64 签名>  <文件名>`）一起发布。
- **门户：** `GET /api/v1/update` 多返回 `install`：这个客户端能自己安装的文件，只有带签名时才有。macOS 桌面应用是 `sharetoken-<版本>-macos-<架构>.app.zip`（整个 .app 打成 zip，下载页不列出），Windows 是安装程序，Linux 是 `.deb`，命令行是压缩包。`package` 和 `install` 都带 `signature`。
- **下载：** daemon 的 `POST /api/v1/update/download`（请求体 `{"key"}`）在后台把 `install` 下载到数据目录的 `update/`，边下边算 SHA-256，校验和签名都对才保留文件；进度在 `session.update.download`（`downloading`、`ready`、`failed`）。没有签名、签名不对、校验和不对，一律不保留、不安装。
- **安装（桌面应用）：** 页面上的“立即更新”只在桌面窗口里出现：窗口壳在页面上加 `data-shell-update="1"`，页面再调用壳的 `/__shell/update`（POST `{key}` 开始，GET 查进度）。壳让 daemon 下载，自己再校验一遍签名，然后先停掉 daemon（进行中的作业会结束），再按系统安装：
  - macOS：用 Wails 的更新器（`pkg/updater`），它再校验一次摘要和签名，解压 zip，等应用退出后替换整个 .app、校验代码签名，然后重新打开；失败会恢复原来的 .app。应用要在用户可写的目录里，不能直接从磁盘映像或 App Translocation 的位置运行。
  - Windows：用 `cmd /c start` 启动安装程序 `/S /RELAUNCH`，让它不是应用的子进程（安装程序会 `taskkill /T` 结束应用），并且脱离当前 job。安装程序装完后，`/RELAUNCH` 让它重新打开应用。
  - Linux：`pkexec dpkg -i`（桌面会弹出管理员密码框，取消就不安装），装好后由一个小脚本等当前进程退出再启动新的窗口程序。只支持从 `.deb` 安装的副本（`/usr/bin`，dpkg 记录过的）。
- **安装（命令行）：** `sharetoken upgrade [-y]` 按本程序的安装方式选择安装包（`client.DetectInstallation`）：命令行压缩包就替换程序文件（Windows 上先把正在运行的程序改名为 `.old`，下次启动时删除）；在 macOS 应用里就替换 .app（应用不能在运行）；Windows 桌面安装就静默运行安装程序；`.deb` 就用 `sudo dpkg -i`（不在终端时用 `pkexec`）。版本号不变时也能重装同版本的新构建，所以用 `dpkg -i` 而不是 `apt-get install`。

**还没有：** 自动安装和分批推送；降级保护（签名只针对文件内容，不包含版本号：能篡改门户回复的人可以提供一个旧的、签过名的包）；用硬件保管发布密钥（现在它是一个文件，在 GitHub 上发布时是一个 secret，见“发布安装包”）。

### 发布说明

一次发布可以带一段自己的说明，告诉用户这个构建带来了什么。源文件是仓库里的 `release-notes/<语言>.md`（`en.md`、`zh-CN.md`，对应客户端的两种语言），需要说明的发布才写；这个目录不在发布之间保留，没有它的发布就不带说明（客户端只显示版本号）。发布时它们作为 `RELEASE-<版本>.<语言>.md` 和安装包放在一起；门户读到后放进 `release.notes`（按语言，每种语言有 `summary` 和 `body`），客户端原样转给页面。也可以直接改服务器下载目录里的这些文件，不用重新发布。

写法是一种很小的标记，客户端自己画，不会当作 HTML 解析：

| 写法 | 显示为 |
|---|---|
| `# 文字` | 标题 |
| `## 文字` | 小标题 |
| `- 文字` | 列表项 |
| 其他行 | 段落 |
| 行内的 `**文字**`、`` `文字` `` | 加粗、代码 |

**摘要**是第一个段落行（不是标题也不是列表项的第一行），显示在只有一行位置的地方：页面顶部的升级提示、系统通知、`sharetoken status`。要写得短：`scripts/release/release-files.sh` 要求中文、日文、韩文不超过 30 个字，其他语言不超过 90 个字符，超过就拒绝发布。门户最多保留 120 个字符。每个文件不超过 4096 字节，超过的那种语言不带说明，发布本身不受影响。页面按自己的语言取说明，没有就用英语。

## 升级规则

每个改动都要遵守这些规则：

1. **可兼容的变更：** 新增可选字段、新的帧类型或新的接口时，接口版本不变。JSON 解码一律忽略未知字段（门户的 `readJSON`、`protocol.Envelope`）。
2. **破坏性变更分三步：** 接收方先同时接受新旧两个版本；发送方再切到新版本；在管理后台确认旧版本无人使用之后再删除它。
3. **流协议：** 新旧处理函数并存，客户端用 `NewStream(新, 旧)` 协商。新的每单协议版本加进 `SupportedInferVersions`。
4. **签名消息：** 中转消息的程序会丢掉它不认识的字段，所以**新增签名字段必须换签名域**（`sharetoken/offer/v2`），验证方先同时接受两个域。[`internal/protocol/golden_test.go`](../../internal/protocol/golden_test.go) 固定了现有的编码；它失败就说明签名内容变了。旧版本回执的校验要一直保留，因为争议可能用到。
5. **数据库：** 新增一条编号迁移，已发布的迁移不再修改（[`internal/mysqldb/migrate.go`](../../internal/mysqldb/migrate.go)）。一个版本只做加法；删除放到下一个版本，这样回滚不用动数据。旧程序确实无法适应某个变更时设置 `MinReader`，它们会拒绝启动。
6. **配置：** 新增的配置项有默认值。不兼容的变更升 `version`，程序先同时接受两个版本。
7. **默认升级顺序：** 数据库（门户或协调器启动时自动迁移）→ 协调器和门户 → 中继 → 客户端。中继凭证的格式要变时，中继先于门户升级。
8. **下线旧客户端：** 先看管理后台里的客户端分布，确认之后再提高 `min_client`。

## 构建产物

```
dist/sharetoken-1.0.0-macos-arm64.dmg             scripts/build/build-gui.sh prod macos arm64
dist/sharetoken-1.0.0-windows-amd64.exe           scripts/build/build-gui.sh prod windows amd64
dist/sharetoken-1.0.0-linux-arm64.deb             scripts/build/build-gui-linux.sh prod arm64（在 Docker 里，任何系统都可以）
dist/sharetoken-cli-1.0.0-linux-amd64.tar.gz      scripts/build/build-cli.sh prod linux amd64（"all" 构建三个系统、两种架构）
dist/sharetoken-cli-1.0.0-windows-amd64.zip       scripts/build/build-cli.sh prod windows amd64
dist/relay-1.0.0-linux-amd64                      GOOS=linux GOARCH=amd64 scripts/build/build.sh relay
dist/sharetoken-1.0.0-macos-arm64.app.zip         同上：.app 打成 zip，一键升级用
dist/SHA256SUMS-1.0.0
dist/SIGNATURES-1.0.0                             scripts/release/release-files.sh 用发布密钥签名
```

**发布安装包。** 一次发布是 14 个包（桌面安装包、两个 macOS 的 .app.zip 和命令行压缩包，三个系统、两种架构），用发布密钥签名，放进门户的下载目录（门户配置里的 `downloads.dir`），整个目录一次替换；门户在 `/downloads/` 下提供下载。有两种做法，跑的是同一套脚本：

- **在 Mac 上：** `scripts/release/publish-packages.sh <版本> <user@host> <目录> [--with-dev-cli] [--owner <用户>]` 构建、签名、上传，在服务器上校验，然后替换目录。`--owner` 让脚本经 sudo 以门户的运行用户安装这些文件。`--with-dev-cli` 多加 6 个 `sharetoken-cli-dev-*` 包供测试：`GET /api/v1/downloads` 不列出它们，只能按文件名下载。
- **用 GitHub Actions：** 工作流“Client release”（[`.github/workflows/release-client.yml`](../../.github/workflows/release-client.yml)）。推送 tag `v<VERSION>`，或者移动它（`git tag -f -a v1.0.1 -m ... && git push -f origin v1.0.1`），就在 GitHub 的 runner 上构建：两种架构的 macOS 应用在 Apple 芯片的 runner 上构建，Windows 应用在 Linux 上交叉编译并用 makensis 打包，`.deb` 在每种架构各自的 Linux runner 上用 `build-gui-linux.sh` 的 Docker 镜像构建，命令行在 Linux 上构建。所有包用 `scripts/build/gui-builder.Dockerfile` 里的 Go 版本，构建时间相同。接着“Sign and publish on GitHub”签名，并创建带全部文件的 GitHub Release（tag 被移动时替换已有 Release 的文件）；“Publish to the portal”替换下载目录。在发布 tag 上手动“Run workflow”效果相同，并会询问是否发布到门户；在分支上运行是试运行：全部构建并检查，不签名也不发布，安装包作为这次运行的产物保留一周。tag 和 `VERSION` 不一致时运行失败。

每一步都是独立的脚本：各个构建脚本；[`scripts/release/release-files.sh`](../../scripts/release/release-files.sh) `<版本> [--with-dev-cli] [--unsigned]`，确认 `dist/` 里包齐全、检查发布说明，写出 `SHA256SUMS-<版本>`、`SIGNATURES-<版本>`、`RELEASE-<版本>.json` 和 `RELEASE-<版本>.<语言>.md`；[`scripts/release/upload-release.sh`](../../scripts/release/upload-release.sh) `<版本> <user@host> <目录> [--owner <用户>]`，只有每个校验和都对得上、每个签名都能用客户端内置的公钥验证通过时才上传。

工作流需要在 GitHub 上做这些设置（Settings → Environments；Settings → Secrets and variables → Actions）：

| 位置 | 名称 | 值 |
|---|---|---|
| 环境 `release` | secret `RELEASE_SIGNING_KEY` | 发布密钥的 PEM 文件（`local/keys/release-signing.pem`） |
| 环境 `portal` | secret `PORTAL_SSH_KEY` | 在门户服务器上安装文件的账号的 SSH 私钥；文件用 sudo 放到位，属主是门户的运行用户 `st-portal`（在工作流里写死） |
| | variable `PORTAL_SSH_TARGET` | `user@host` |
| | variable `PORTAL_DOWNLOADS_DIR` | 门户的 `downloads.dir` |
| | variable `PORTAL_SSH_KNOWN_HOSTS` | 服务器的 `known_hosts` 行（例如 `ssh-keyscan -t ed25519 <主机>` 的输出，并人工和服务器的密钥核对）；主机密钥严格校验 |
| 仓库 | variable `RELEASE_PUBLISH_PORTAL` | `true`：推送 tag 时也发布到门户；否则只有手动“Run workflow”才会 |
| | variable `RELEASE_WITH_DEV_CLI` | `true`：每次运行都加上 dev 命令行压缩包 |
| | secret `PUSHOVER_TOKEN`、`PUSHOVER_USER` | Pushover 的应用 token 和用户 key：“Notify”作业推送每次 tag 运行的结果（已发布、是否发布到门户，或失败并附运行链接）；没有它们时只给出警告 |

**发布密钥放在 GitHub 上。** 存成这个 secret 之后，发布密钥就不再只是离线保存：能让 `release` 作业运行的人，就能给一个所有客户端都会自行安装的包签名。给 `release` 和 `portal` 两个环境设置必需的审批人，只允许从 `v*` tag 部署，再加一条 tag 规则集，只让维护者创建或移动 `v*` tag。作业只在签名期间把密钥写进一个临时文件。没有这个 secret 时，发布运行在签名前停止，仍然可以像以前一样在 Mac 上发布。

程序名不带版本（`sharetoken`、`sharetoken-gui`；dev 构建是 `sharetoken-dev`、`sharetoken-gui-dev`）：窗口按固定的名字查找 daemon，用户的 PATH 在升级后也不用改。用 `scripts/build/build.sh` 构建的服务端程序则在文件名里带版本。

客户端安装包见 [desktop.md](desktop.md)，构建和测试见 [development.md](development.md)，运行服务端程序见 [self-hosting.md](self-hosting.md)。

## 测试

- `scripts/test/test.sh`：在 Docker 里启动 MySQL 8，运行全部 Go 测试。
- [`internal/protocol/golden_test.go`](../../internal/protocol/golden_test.go)：签名消息的固定向量。
- [`internal/mysqldb`](../../internal/mysqldb)：迁移顺序、中断后续跑、并发启动、旧程序遇到破坏性迁移。
- [`scripts/test/version_ui_test.mjs`](../../scripts/test/version_ui_test.mjs)：版本页面和升级提醒，在无头 Chrome 里测试。
- [`scripts/test/gui_update_test.sh`](../../scripts/test/gui_update_test.sh)：macOS 一键升级的端到端测试。在私有网络上构建旧版 A 和新版 B，发布签过名的 B，让 A 自己安装（`SHARETOKEN_GUI_SELFTEST=update`，窗口隐藏），确认换成了 B 并且 B 能启动；另一个密钥签的包会被拒绝。Windows 和 Linux 按同样的步骤在虚拟机里手工跑（安装 A，发布 B，运行 `sharetoken-gui-dev`，环境变量相同；Linux 测试机上临时加一条 polkit 规则让 `pkexec` 不用输密码）。
- **未实现：** 针对上一个发布版本的端到端测试（构建上一个 tag 的客户端，接到当前的协调器和门户上交易）。
