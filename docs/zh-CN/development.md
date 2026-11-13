# 开发

[English](../development.md) | **简体中文**

本文说明怎样构建、测试和打包这个项目。想了解代码做什么，先读 [architecture.md](architecture.md)。

## 1. 目录结构

```
cmd/         每个程序一个目录：sharetoken、relay、coordinator、portal、mailserver
internal/    全部库代码，这里没有任何对外的 Go API
gui/         桌面窗口壳（Wails），独立的 Go module，有自己的 go.mod
test/        基于真实 libp2p 主机的端到端测试（test/e2e、test/flow）
configs/     配置文件样板，测试会加载它们
docker/      容器镜像的入口脚本
scripts/     构建、打包、测试和演示脚本
docs/        文档：英文在这一层，简体中文在 docs/zh-CN
```

`internal/` 下的包，从线上协议往上：

| 层 | 包 |
|---|---|
| 线上协议与密码学 | `protocol`、`canon`、`box`、`receipt`、`attest`（`sevsnp`、`tdx`、`hw`）、`relaypass` |
| 核验 | `evidence`、`tokens`、`audit` |
| 网络 | `p2p`、`relay` |
| 用户端 | `client`（daemon、本机 API、`client/ui` 里的内嵌界面）、`buyer`、`node`、`provider` |
| 平台 | `coordinator`、`ledger`、`accounts`、`portal`（HTTP API 和 `portal/web` 里的网页）、`mailer` |
| 辅助 | `config`、`mysqldb`、`secfile`、`totp`、`version`、`cli` |

有两条规则让这个结构保持清晰。线上协议这一层的包不导入上面各层的任何东西。区分签名域的字符串（`sharetoken/receipt/v1` 这一类）属于协议的一部分：它们不是导入路径，包挪了位置也不改。签名编码一旦变化，`internal/protocol/golden_test.go` 就会失败。

## 2. 构建

需要 Go 1.26 或更新版本。服务端程序和命令行客户端不需要 cgo。

```bash
go build -o bin/ ./cmd/...
```

**构建类型。** 客户端按 `dev` 或 `prod` 构建，普通的 `go build` 是 `dev`，这样从工作目录构建出来的程序不会误连生产网络：

| | dev | prod |
|---|---|---|
| 门户 | 本项目的测试网络，或者 `$SHARETOKEN_PORTAL` | `https://sharetoken.app` |
| 状态目录 | `~/.sharetoken-dev` | `~/.sharetoken` |
| 本机端点 | `127.0.0.1:9887` | `127.0.0.1:9787` |

连接自己的网络时，用 dev 构建并设置 `SHARETOKEN_PORTAL`（[self-hosting.md](self-hosting.md)）。

**版本。** `VERSION` 文件里是发布版本号。`scripts/version.sh` 在发布 tag 上打印它，在其他位置打印 `<版本>-dev.<提交>`；`scripts/ldflags.sh` 把它写进程序。规则见 [versioning.md](versioning.md)。

| 脚本 | 产物 |
|---|---|
| `scripts/build.sh portal coordinator relay mailserver` | `dist/` 下的服务端程序，文件名带版本、系统和架构。用 `GOOS` 和 `GOARCH` 交叉编译。编译参数为 `-trimpath -ldflags "-s -w"`；`GOAMD64`/`GOARM64` 高于基线时面向更新的 CPU，并写进文件名（`GOAMD64=v3` → `portal-1.0.0-linux-amd64v3`） |
| `scripts/build-cli.sh <dev\|prod> [系统] [架构]` | 命令行客户端的压缩包 |
| `scripts/build-gui.sh <dev\|prod> [macos\|windows] [架构]` | 桌面应用及其安装包（`.dmg`、`.exe`）。`.dmg` 的窗口布局由 dmgbuild 写入（固定版本，首次使用时装到 `~/.cache/sharetoken/`；设置在 `scripts/dmg_settings.py`），构建时不会打开 Finder |
| `scripts/build-gui-linux.sh <dev\|prod> [架构]` | Linux 桌面包（`.deb`），在 Docker 里构建（`scripts/gui-builder.Dockerfile`） |
| `scripts/publish-packages.sh <版本> <user@host> <目录>` | 构建一个版本的全部包，并上传到门户的下载目录 |
| `docker build -t sharetoken .` | `docker-compose.yml` 使用的服务端镜像 |

桌面应用见 [desktop.md](desktop.md)。

## 3. 测试

代码只在 MySQL 8 上运行，测试也一样。`scripts/test.sh` 先启动一个 MySQL 容器（`scripts/local-mysql.sh`：只监听回环地址，数据放在内存里，两次运行之间保持运行），再执行 `go test`：

```bash
scripts/test.sh                        # go test ./...
scripts/test.sh -race ./...
scripts/test.sh ./internal/coordinator
```

要用现成的服务器，就设置 `SHARETOKEN_TEST_MYSQL=mysql://user@host:3306/`，然后直接运行 `go test`。每个测试都会创建自己的数据库。

Go 测试覆盖的内容：签名与防篡改、整数计费运算、账本（重启、回滚、篡改检测）、结算和争议的全部路径、抽检判定、SEV-SNP 和 TDX 的 quote 验证、门户 API 及其网页安全模型、基于真实 SMTP 的邮件服务，以及 `test/e2e` 里由真实 libp2p 主机组成的完整网络：故障切换、Ticket 重放与盗用、按证据退款、抽检抓出被替换的模型、中继宕机。

桌面模块有自己的测试：

```bash
(cd gui && go test ./...)
```

**浏览器测试。** 网页和桌面应用的页面用无头 Chrome 通过 DevTools 协议驱动。它们需要 Node 22 和 Chrome，不依赖任何包。`scripts/gui_stack.sh` 在临时目录里为它们启动一个私有网络：

```bash
scripts/gui_stack.sh start /tmp/st && . /tmp/st/env
node scripts/gui_ui_test.mjs /tmp/st /tmp/st/shots       # 桌面应用的页面
scripts/gui_stack.sh stop /tmp/st
```

其余几个（`portal_ui_test.mjs`、`settings_ui_test.mjs`、`theme_ui_test.mjs`、`i18n_ui_test.mjs`、`docs_ui_test.mjs`、`mail_ui_test.mjs`、`version_ui_test.mjs`）的文件开头写明了前提条件和运行方法。

**其他工具。**

| 脚本 | 用途 |
|---|---|
| `scripts/demo-local.sh` | 在 127.0.0.1 上用 Ollama 的真实模型跑完整网络，其中包括一个作弊的卖家 |
| `scripts/mock_upstream.py` | 一个快速、诚实的模型服务替身 |
| `scripts/inflating_upstream.py` | 一个虚报用量的不诚实替身，演示用 |
| `scripts/st_bench.py` | 对运行中的客户端做功能检查和压力测试 |
| `scripts/upstream_proxy.py` | 放在多个模型服务前面的本机代理。它的配置里有 API key，不提交 |
| `scripts/cli_test.sh`、`gui_shell_test.sh`、`gui_tray_test.sh` | 对已构建的包做的测试 |

## 4. 约定

- **机密不进仓库。** `.gitignore` 排除了密钥、密码文件、`.env` 文件和 `local/` 目录。真实环境的配置和部署脚本应该放在 `local/` 里。
- **配置文件里同样不放机密**：只写环境变量名或文件路径（[configuration.md](configuration.md)）。
- **只支持 MySQL，表结构用编号迁移**（`internal/mysqldb`）。改表结构要新增一条迁移，不能改旧的。
- **签名编码是冻结的。** 要改就需要新的签名域（[versioning.md](versioning.md)）。
- **网页没有构建步骤，也没有依赖**：纯 HTML、CSS 和 JavaScript，内嵌在程序里，Content-Security-Policy 不允许任何内联代码。
- **文档以英文为准。** `docs/` 下的每一页在 `docs/zh-CN/` 下都有同名的中文版，两边要一起改。
- 提交改动之前运行 `gofmt` 和 `go vet ./...`。
