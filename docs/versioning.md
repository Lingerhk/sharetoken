# 版本与升级

发布版本说明“这是哪份代码”，接口版本决定“能不能互通”，两者分开管理。从 1.0.0 起，下面的规则约束所有改动。

## 版本号

**正式上线前版本统一是 1.0.0。** 发布时不升版本号，而是把 tag `v1.0.0` 移到要发布的提交上（`git tag -f -a v1.0.0 -m ...`）。因此上线前“1.0.0”不代表一份固定的代码，看具体是哪份要看 `commit`（`GET /api/v1/version`、管理后台“版本”页）。下面的规则从正式上线起生效。

- **发布版本：** 全仓一个 semver，写在根目录 `VERSION`。客户端、GUI、中继、协调器、门户、邮件服务同一个 tag。
- **格式：** tag `v1.2.3` 上没有改动的构建是 `1.2.3`；其他构建是 `1.2.3-dev.<commit>[.dirty]`（`scripts/version.sh`）。没有注入版本的 `go build` 是 `dev`。
- **注入：** `scripts/ldflags.sh` 写入 `internal/version`，GUI 外壳是独立 module，用 `main.version`。
- **查看：** `portal|coordinator|mailserver|relay|sharetoken version`，启动日志第一行，门户 `GET /api/v1/version`，管理后台“版本”页。

发版：改 `VERSION`，在更新日志页（`portal/web/changelog.html`，网址 `/changelog/`）最上面加这个版本的功能并补齐翻译，提交，`git tag v1.2.3`，再构建。生产部署脚本拒绝 `-dev` 版本。

## 接口版本

| 接口 | 版本在哪 | 现在 |
|---|---|---|
| 门户 HTTP API | 路径 | `/api/v1` |
| 客户端本地 API | 路径 | `/api/v1`（OpenAI 接口仍是 `/v1`） |
| 控制面（客户端↔协调器） | libp2p 协议 ID | `/sharetoken/ctrl/1.0.0` |
| 数据面（买家↔节点） | libp2p 协议 ID + 每单协商的 `infer_version` | `/sharetoken/infer/1.0.0`，版本 1 |
| 中继凭证 | libp2p 协议 ID + 签名域 | `/sharetoken/relay-pass/1.0.0`，`relay-pass/v1` |
| 签名消息 | 签名域 | `offer/v1`、`ticket/v1`、`receipt/v1`、`ack/v1` |
| 数据库 | `schema_migrations`，按组件编号 | `core` 1，`mail` 1 |
| 服务端配置 | YAML 的 `version` | 1 |
| 客户端配置 | `config.json` 的 `version` | 1 |

**客户端如何上报版本：**

- 门户：每个请求带 `User-Agent: sharetoken-client/<版本> (<env>; <os>/<arch>)` 和 `X-ST-Client-Version`；每个响应带 `X-ST-Server-Version`。
- 协调器和节点：打开请求的帧带 `ver`（`protocol.SendRequest`），缺失就拒绝。
- 所有 libp2p 节点：identify 里的 `UserAgent` 是 `sharetoken-<程序>/<版本>`，协调器据此记录中继版本。
- Offer 里有已签名的 `client_version` 和 `infer_versions`，Ticket 里有已签名的 `infer_version`。协调器只撮合协议相通的买卖双方。

## 版本策略

客户端版本策略存在共享库的 `meta.version_policy`，门户和协调器读同一份（协调器最多缓存 30 秒），不用重新部署。在管理后台“版本”页改，或者：

```bash
portal -config portal.yaml version-policy                      # 查看
portal -config portal.yaml version-policy set -min 1.0.0 -recommended 1.0.0 -latest 1.0.0 \
  -download-url https://sharetoken.app/#download -allow-dev=false
```

| 操作 | 低于最低版本、开发构建（`allow_dev` 关闭时）或没带版本时 |
|---|---|
| `POST /api/v1/register`、`/login`、`/mfa`、`/devices` | 门户返回 426，错误码 `upgrade_required` |
| `announce`、`ticket_request` | 协调器返回 `upgrade_required`；本地 OpenAI 接口返回 426 |
| `settle`、`started`、`abandon`、`dispute`、`evidence`、`relay-pass`、账户查询 | **永不拦截**：结算和争议涉及资金 |
| 网页登录和注册（`/api/v1/web/*`）、`/api/v1/register/code` | 不受影响 |

客户端启动时和之后每小时查 `GET /api/v1/version`，结果放在本地 `GET /api/v1/session` 的 `update` 里，界面显示升级提示。版本号是客户端自报的，这套校验只为兼容，不作安全手段。库里没有策略时不拦任何客户端。

开发构建 `1.2.3-dev.x` 按 `1.2.3` 比较。dev 环境设 `allow_dev: true`，prod 设 `false`。

## 升级规则

上线后每个改动都要遵守：

1. **可兼容的变更：** 加可选字段、加新帧类型、加新接口，接口版本不变。JSON 解码一律忽略未知字段（门户 `readJSON`、`protocol.Envelope`）。
2. **破坏性变更分三步：** 接收方先同时接受新旧版本，发送方再切到新版本，后台确认旧版本无人使用后再删除。
3. **流协议：** 新旧处理函数并存，客户端用 `NewStream(新, 旧)` 协商。每单的协议版本加进 `SupportedInferVersions`。
4. **签名消息：** 消息中转时旧程序会丢掉不认识的字段，所以**新增签名字段必须换签名域**（`offer/v2`），验证方先接受两个域。`protocol/golden_test.go` 固定了现有编码，测试失败就说明改了签名内容。历史回执的旧版本校验要一直保留，因为争议可能用到。
5. **数据库：** 新增一条编号迁移，已发布的迁移不再修改。一个版本只做加法，删除放到下一个版本，这样回滚不用动数据。老程序确实无法工作时设 `MinReader`，它们会拒绝启动。
6. **配置：** 新增配置项有默认值。不兼容时升 `version`，程序先接受新旧两个版本。
7. **默认升级顺序：** 数据库（门户或协调器启动时自动迁移）→ 协调器和门户 → 中继 → 客户端。relay pass 的格式要变时，中继先于门户升级。
8. **下线旧客户端：** 看后台的客户端分布，确认后再提高 `min_client`。

## 产物

```
dist/sharetoken-1.0.0-macos-arm64.dmg             scripts/build-gui.sh prod macos arm64
dist/sharetoken-1.0.0-windows-amd64.exe           scripts/build-gui.sh prod windows amd64
dist/sharetoken-1.0.0-linux-arm64.deb             scripts/build-gui-linux.sh prod arm64（用 Docker，任何系统）
dist/sharetoken-cli-1.0.0-linux-amd64.tar.gz      scripts/build-cli.sh prod linux amd64（all 构建三个系统、两种架构）
dist/sharetoken-cli-1.0.0-windows-amd64.zip       scripts/build-cli.sh prod windows amd64
dist/relay-1.0.0-linux-amd64                      GOOS=linux GOARCH=amd64 scripts/build.sh relay
dist/SHA256SUMS-1.0.0
```

**安装包的发布：** `scripts/publish-packages.sh <版本> <user@host> <目录> [--with-dev-cli]` 在 Mac 上构建 12 个包（桌面安装包和命令行包，三个系统、两种架构），上传并在服务器上校验后整体替换门户的 `downloads` 目录（`downloads.dir`），门户在 `/downloads/` 提供下载。dev 和 prod 门户展示的都是正式版的包：dev 部署时加上 `--with-dev-cli`，多传 6 个 `sharetoken-cli-dev-*` 供测试，`GET /api/v1/downloads` 不列出它们，只能按文件名下载。


可执行文件名不带版本（`sharetoken`、`sharetoken-gui`，dev 构建是 `sharetoken-dev`、`sharetoken-gui-dev`）：GUI 按固定名字查找 daemon，用户的 PATH 也不用变。

服务器上文件放在 `releases/<版本>/`，程序名是软链，上一版的软链留在 `<程序>.prev`，保留最近 5 个版本：

```bash
# 回滚（门户为例，其他程序相同）
cd ~/sharetoken-prod && sudo systemctl stop sharetoken-portal && mv portal.prev portal && sudo systemctl start sharetoken-portal
```

## 测试

- `scripts/test.sh`：在 Docker 里起 MySQL 8 跑全部 Go 测试。
- `protocol/golden_test.go`：签名消息的固定向量。
- `internal/mysqldb`：迁移顺序、中断后重跑、并发启动、老程序遇到破坏性迁移。
- 上一版本的端到端测试从第二个发布版本开始有比较对象，做法是构建上一个 tag 的客户端，接到当前的协调器和门户上跑交易。
