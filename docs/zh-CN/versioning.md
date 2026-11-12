# 版本与升级

[English](../versioning.md) | **简体中文**

发布版本说明一个程序是哪份代码。接口版本决定两个程序能不能互通。两者分开管理，下面的规则约束所有改动。

## 发布版本

- **一个版本号：** 全仓只有一个 semver，写在根目录的 `VERSION` 文件里。客户端、桌面窗口壳、中继、协调器、门户和邮件服务共用一个 tag。
- **格式：** tag `v1.2.3` 上没有改动的构建是 `1.2.3`。其他构建是 `1.2.3-dev.<commit>[.dirty]`（[`scripts/version.sh`](../../scripts/version.sh)）。没有注入版本的 `go build` 是 `dev`。
- **注入：** [`scripts/ldflags.sh`](../../scripts/ldflags.sh) 把版本、提交和构建时间写入 `sharetoken/internal/version`。桌面窗口壳是独立的 module，用 `main.version`。
- **查看：** `portal|coordinator|mailserver|relay|sharetoken version`，每个程序日志的第一行，门户的 `GET /api/v1/version`，以及管理后台的“版本”页。

**首次公开发布之前，版本保持 1.0.0。** 发布时不升版本号，而是把 tag `v1.0.0` 移到要发布的提交上（`git tag -f -a v1.0.0 -m ...`）。这段时间里“1.0.0”不代表一份固定的代码，`commit` 才是（`GET /api/v1/version`、管理后台“版本”页）；客户端靠比较提交和构建时间来发现更新的构建（见“升级提醒”）。

发版步骤：修改 `VERSION`；在更新日志页（[`internal/portal/web/changelog.html`](../../internal/portal/web/changelog.html)，网址 `/changelog/`）最上面加上这个版本及其功能，并补齐翻译；提交；`git tag v1.2.3`；然后构建。

## 接口版本

| 接口 | 版本在哪 | 当前 |
|---|---|---|
| 门户 HTTP API | 路径 | `/api/v1` |
| 客户端本地 API | 路径 | `/api/v1`（OpenAI 接口仍是 `/v1`） |
| 控制面（客户端 ↔ 协调器） | libp2p 协议 ID | `/sharetoken/ctrl/1.0.0` |
| 数据面（买家 ↔ 节点） | libp2p 协议 ID，加上每单协商的 `infer_version` | `/sharetoken/infer/1.0.0`，版本 1 |
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
| `POST /api/v1/register`、`/login`、`/mfa`、`/devices` | 门户返回 426，错误码 `upgrade_required` |
| `announce`、`ticket_request` | 协调器返回 `upgrade_required`；本地 OpenAI 接口返回 426 |
| `settle`、`started`、`abandon`、`dispute`、`evidence`、`relay-pass`、账户查询 | **永不拦截：** 结算和争议涉及资金 |
| 网页登录和注册（`/api/v1/web/*`）、`/api/v1/register/code` | 不受影响 |

版本号是客户端自报的，所以这项校验只为兼容，不是安全手段。数据库里没有策略时不拦任何客户端。

开发构建 `1.2.3-dev.x` 按 `1.2.3` 比较。开发用的网络设 `allow_dev: true`；生产网络设为 `false`。

## 升级提醒

客户端（桌面应用的 daemon 和 `sharetoken up`）在启动时和之后每小时询问门户：`GET /api/v1/update?kind=desktop|cli&channel=prod|dev&os=…&arch=…`。门户返回：

- 版本策略（`min`、`recommended`、`latest`、`download_url`）。`latest` 为空时用下载目录里的发布；`download_url` 为空时是 `/#download`。
- `release`：下载目录里的 `RELEASE-<版本>.json`（由 [`scripts/publish-packages.sh`](../../scripts/publish-packages.sh) 写入：版本、提交、构建时间），加上更新日志链接 `/changelog/#v<版本>`。
- `package`：这个发布里对应客户端种类、构建类型、系统和架构的安装包，带大小和 sha256。dev 客户端拿到的是 `sharetoken-cli-dev-*`。dev 桌面安装包不发布，所以 dev 桌面应用拿不到安装包。

客户端在以下情况判断有新版本：自己的版本低于 `min`（必须升级）、`recommended` 或 `latest`；或者发布是**同一版本号的更新构建**：提交不同、构建时间更晚，而且有这个客户端的安装包。版本保持 1.0.0 期间，起作用的是后一条。一次发布的所有包共用 `ST_BUILD_TIME`，由 `ldflags.sh` 写进 `version.BuildTime`。

提醒出现在这些位置：

- 桌面应用页面顶部的一行：版本、发布说明的摘要、“查看详情”和“跳过此版本”。“查看详情”打开对话框：发布说明全文、下载链接（本系统的安装包，没有就是下载页）和“更新内容”。必须升级时显示为红色，说明已有作业照常结算，并且不能跳过。设置里的“检查更新”立即询问门户，有新版本（包括跳过的）就打开这个对话框。
- 托盘菜单：“有新版本 x”或“需要升级到 x”；点击打开窗口。
- 系统通知，每个新构建一次（通过 Wails 的通知接口；macOS 首次会请求权限）。daemon 在数据目录的 `update.json` 里记下已跳过和已通知的构建，所以重启后不会重复。
- 命令行：`sharetoken up` 在日志里写一行 WARN；`sharetoken status` 会显示；`sharetoken version --check` 直接询问门户，不需要客户端在运行。

本地 API：`GET /api/v1/session` 里的 `update`，以及有新版本时 `GET /api/v1/status` 里的 `update`；`POST /api/v1/update/check`、`POST /api/v1/update/skip` 和 `POST /api/v1/update/notified`（请求体 `{"key"}`；key 不是当前提供的构建时返回 409 `not_offered`）。

**未实现：** 下载和安装由用户手动完成。更新信息没有签名；客户端只显示链接和 sha256，从不执行下载的文件。要做自动升级，先要用离线密钥给发布信息签名。

### 发布说明

一次发布可以带一段自己的说明，告诉用户这个构建带来了什么。源文件是仓库里的 `release-notes/<语言>.md`（现在有 `en.md` 和 `zh-CN.md`，对应客户端的两种语言），每次发布前改写。`publish-packages.sh` 把它们作为 `RELEASE-<版本>.<语言>.md` 和安装包一起发布；门户读到后放进 `release.notes`（按语言，每种语言有 `summary` 和 `body`），客户端原样转给页面。也可以直接改服务器下载目录里的这些文件，不用重新发布。

写法是一种很小的标记，客户端自己画，不会当作 HTML 解析：

| 写法 | 显示为 |
|---|---|
| `# 文字` | 标题 |
| `## 文字` | 小标题 |
| `- 文字` | 列表项 |
| 其他行 | 段落 |
| 行内的 `**文字**`、`` `文字` `` | 加粗、代码 |

**摘要**是第一个段落行（不是标题也不是列表项的第一行），显示在只有一行位置的地方：页面顶部的升级提示、系统通知、`sharetoken status`。要写得短：`publish-packages.sh` 要求中文、日文、韩文不超过 30 个字，其他语言不超过 90 个字符，超过就拒绝发布。门户最多保留 120 个字符。每个文件不超过 4096 字节，超过的那种语言不带说明，发布本身不受影响。页面按自己的语言取说明，没有就用英语。

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
dist/sharetoken-1.0.0-macos-arm64.dmg             scripts/build-gui.sh prod macos arm64
dist/sharetoken-1.0.0-windows-amd64.exe           scripts/build-gui.sh prod windows amd64
dist/sharetoken-1.0.0-linux-arm64.deb             scripts/build-gui-linux.sh prod arm64（在 Docker 里，任何系统都可以）
dist/sharetoken-cli-1.0.0-linux-amd64.tar.gz      scripts/build-cli.sh prod linux amd64（"all" 构建三个系统、两种架构）
dist/sharetoken-cli-1.0.0-windows-amd64.zip       scripts/build-cli.sh prod windows amd64
dist/relay-1.0.0-linux-amd64                      GOOS=linux GOARCH=amd64 scripts/build.sh relay
dist/SHA256SUMS-1.0.0
```

**发布安装包：** `scripts/publish-packages.sh <版本> <user@host> <目录> [--with-dev-cli] [--owner <用户>]` 在 Mac 上运行，构建 12 个包（桌面安装包和命令行压缩包，三个系统、两种架构）。它上传这些包，在服务器上校验，然后整体替换门户的下载目录（门户配置里的 `downloads.dir`）；门户在 `/downloads/` 下提供下载。`--owner` 让脚本经 sudo 以门户的运行用户安装这些文件。`--with-dev-cli` 多加 6 个 `sharetoken-cli-dev-*` 包供测试：`GET /api/v1/downloads` 不列出它们，只能按文件名下载。

程序名不带版本（`sharetoken`、`sharetoken-gui`；dev 构建是 `sharetoken-dev`、`sharetoken-gui-dev`）：窗口按固定的名字查找 daemon，用户的 PATH 在升级后也不用改。用 `scripts/build.sh` 构建的服务端程序则在文件名里带版本。

客户端安装包见 [desktop.md](desktop.md)，构建和测试见 [development.md](development.md)，运行服务端程序见 [self-hosting.md](self-hosting.md)。

## 测试

- `scripts/test.sh`：在 Docker 里启动 MySQL 8，运行全部 Go 测试。
- [`internal/protocol/golden_test.go`](../../internal/protocol/golden_test.go)：签名消息的固定向量。
- [`internal/mysqldb`](../../internal/mysqldb)：迁移顺序、中断后续跑、并发启动、旧程序遇到破坏性迁移。
- [`scripts/version_ui_test.mjs`](../../scripts/version_ui_test.mjs)：版本页面和升级提醒，在无头 Chrome 里测试。
- **未实现：** 针对上一个发布版本的端到端测试（构建上一个 tag 的客户端，接到当前的协调器和门户上交易）。
