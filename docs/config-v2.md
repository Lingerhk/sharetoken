# 配置文件化方案（v2）

> 状态：已实现（2026-11-08）。代码 `internal/config`，样板 `configs/{relay,coordinator,portal}.example.yaml`，测试环境实际使用的配置 `scripts/testenv/conf/`。

## 1. 通用约定（三个服务端共用）

| 项 | 约定 |
|---|---|
| 入口 | 每个二进制新增 `-config <path>`；不传则纯 flag 模式（与之前完全兼容）。不做隐式查找，避免“当前目录恰好有个 yaml”这类意外 |
| 优先级 | 内置默认 < yaml < **显式传入的** CLI flag（用 `flag.Visit` 判断，未传的 flag 不覆盖 yaml）。现有测试脚本 / `scripts/testenv` 不用改 |
| 子命令 | `portal useradd …`、`coordinator balance …` 等运维命令读同一份 yaml（只用 `database` / `identity` / `keys`） |
| 解析 | `go.yaml.in/yaml/v3`（已在 go.sum 中），`KnownFields(true)`，未知字段直接报错 |
| 版本 | 顶层 `version: 1`，以后不兼容变更时升版本并给迁移提示 |
| 路径 | 相对路径相对于 yaml 所在目录解析（systemd 下 cwd 不可靠） |
| 时长/大小 | `30s`/`10m`/`720h`；大小 `16KiB` |
| 机密 | yaml 里不放明文：`password_env` / `password_file`、`api_key_env`；`*_file` 权限宽于 0600 时告警 |
| 校验 | 启动时统一校验，一次性列出全部错误；`coordinator|portal -config x.yaml check-config`、`relay -config x.yaml -check-config` 只校验并打印生效配置（配置里只有环境变量名/文件路径，没有机密） |
| 热加载 | 第一版不做。后续可考虑 relay 的 `access.networks` 支持 SIGHUP（便于轮换 pass key） |
| 日志 | 统一 `log.level/format/file`，接到已有的 `Log *slog.Logger` 字段 |

**不进配置文件、继续放数据库的**：中继列表（`portal relay add`）、用户/设备/额度、管理员 MFA 密钥。这些是运行时数据，由 portal 管理、coordinator 同步。

## 2. 配置项清单

图例：**F** = 现有 flag 迁入；**H** = 原硬编码，新提出为可配（默认值不变）；**D** = 仅开发用。

### relay

| yaml | 来源 | 默认 |
|---|---|---|
| identity.key_file | F `-key` | relay.key |
| network.listen | F `-listen` | tcp/quic × v4/v6 :4001 |
| network.announce | F `-announce` | 空 |
| network.max_conns | F `-max-conns` | 8192 |
| access.networks | 列表，每项一个网络（prod、dev）；只能写在配置文件里 | 空 = 开放中继（告警） |
| access.networks[].name | 必填，不可重复 | |
| access.networks[].portal_keys | 必填，不可与其他网络重复 | |
| access.networks[].allow_peers | 该网络的协调器，不可与其他网络重复 | 空（告警） |
| access.networks[].max_devices_per_user | 账号只在本网络内计数 | 4 |
| access.networks[].max_reservations | 该网络的设备合计上限，协调器不计 | 0 = 不单独限制 |
| limits.max_reservations | F | 4096 |
| limits.max_reservations_per_ip | F | 64 |
| limits.max_circuits | F | 4096 |
| limits.reservation_ttl | H `relay.Config.ReservationTTL` | 1h |
| limits.circuit_buffer | H `relay.Config.BufferSize` | 16KiB |
| status.listen | F `-status` | 空；`/` 是 JSON，`/metrics` 是 Prometheus 指标，只接受回环地址 |
| status.stats_interval | H main.go 5m ticker | 5m |
| log.* | 新增 | info/text/stderr |

### coordinator

| yaml | 来源 | 默认 |
|---|---|---|
| identity.key_file | F `-key` | coordinator.key |
| database.dsn | F `-db` | 无，必填（mysql://） |
| database.password_env/file | 原 `$SHARETOKEN_DB_PASSWORD` 写死 | SHARETOKEN_DB_PASSWORD |
| database.max_open_conns / max_idle_conns / conn_max_lifetime | H `internal/mysqldb` | 32 / 8 / 30m |
| database.connect_timeout / lock_wait_timeout | H 同上 | 10s / 10s |
| database.startup_busy_retry | H main.go | 2m |
| network.listen / max_conns / relays | F | 空 / 8192 / 空 |
| network.relay_sync | H `Config.RelaySync` | 15s |
| market.fee_bps | F | 500 |
| market.min_budget_micros / warm_up | H | 1 / 30s |
| jobs.ticket_ttl … suspend_for（10 项） | H `coordinator.Config` 各时序字段 | 见样板 |
| references[] | F `-reference id=,upstream=,url=,key-env=`（改为结构化列表） | 空 |
| audit.interval / budget_micros / prompts_file | F | 10m / 200000 / 内置 |
| audit.max_tokens / match_at / mismatch_below | H `audit.Config` | 96 / 0.8 / 0.4 |
| tee.policy_file / online_check | F | 空 / false |
| debug.pprof_listen | F `-debug` | 空 |
| dev.open_accounts / credit_micros | F，**D** | false / 0 |

### portal

| yaml | 来源 | 默认 |
|---|---|---|
| database.* | F `-db` + 同 coordinator 的连接池项 | — |
| keys.pass_key_file | F `-pass-key` | portal-pass.key |
| keys.mfa_key_file | H `portal.Config.MFAKey`（目前未暴露，从 pass key 派生） | 空 = 派生 |
| http.listen | F `serve -listen` | :8092 |
| http.read_header/read/write/shutdown_timeout | H main.go | 10s/30s/60s/5s |
| tls.mode | F `-insecure-http` + `-behind-tls-proxy` + 回环判断，合并为一个枚举 | auto |
| tls.cert_file / key_file / self_signed_dir / self_signed_hosts | F | — |
| accounts.open_registration / signup_credit_micros | F | false / 0 |
| sessions.login_ttl / web_session_ttl / pass_ttl | F | 720h / 24h / 1h |

### 实现时的决定

1. `jobs.*` 全部暴露，校验约束：`ticket_ttl ≥ 10s`、`start_grace < settle_grace < dispute_window ≤ job_retention`；`audit` 要求 `0 < mismatch_below < match_at ≤ 1`；`fee_bps` 0–10000。
2. `dev.*` 保留启动告警，未做 build tag 禁用。
3. `tls.mode` 枚举已实现；`-insecure-http` → `insecure`，`-behind-tls-proxy` → `proxy`（纯 HTTP + Secure Cookie）。
4. `scripts/testenv` 已切到 yaml（`env.sh` 的 `coord_start`/`portal_start`/`relay_start` 都用 `-config`，`deploy.sh` 先下发配置并 check 再重启）。中继的 systemd unit 见 `scripts/relay/sharetoken-relay.service`。
5. 中继 `announce` 支持 `/dns4`、`/dns6`；有域名时 relay 打印的 `portal relay add` 提示只用域名地址。
6. coordinator 的 `-reference` flag 仍可用，给了就整体替换 `references[]`；`references[].api_key_env` 对应旧的 `key-env=`。
7. `audit.interval: 0` 关闭抽检但保留参考模型的精确计数（商业 API 模型输出不确定，抽检会误判，而且每次都要花钱）。

## 3. 用户侧（client / GUI）初步方案

现状：`~/.sharetoken/`（或 `$SHARETOKEN_HOME`）下 `config.json`（含 portal token 明文）、`device.key`、`network.json`。

### 3.1 目录按 OS 规范拆分

| 类别 | 内容 | macOS | Windows | Linux |
|---|---|---|---|---|
| config | 用户偏好：portal、买/卖设置、模型列表 | `~/Library/Application Support/ShareToken/` | `%APPDATA%\ShareToken\` | `$XDG_CONFIG_HOME/sharetoken/`（~/.config） |
| data | device.key（若不进钥匙串）、本地账单/历史 | 同上 | `%LOCALAPPDATA%\ShareToken\` | `$XDG_DATA_HOME/sharetoken/`（~/.local/share） |
| cache | network.json（portal 不可达时的回退）、中继测速结果 | `~/Library/Caches/ShareToken/` | `%LOCALAPPDATA%\ShareToken\cache\` | `$XDG_CACHE_HOME/sharetoken/` |
| logs | 滚动日志（GUI 可一键打包反馈） | `~/Library/Logs/ShareToken/` | `%LOCALAPPDATA%\ShareToken\logs\` | `$XDG_STATE_HOME/sharetoken/logs/` |
| secrets | portal token、device.key | Keychain | Credential Manager / DPAPI | Secret Service（libsecret）；无桌面环境回退 0600 文件 |

Go 侧用 `os.UserConfigDir/UserCacheDir` + 少量补充；钥匙串用 `github.com/zalando/go-keyring` 一类库。
`$SHARETOKEN_HOME` 保留为“便携模式”：设置后所有东西都放这一个目录（U 盘、测试、多实例）。

### 3.2 配置与机密分离

- `config` 里不再出现 `token`；`device.key` 优先进钥匙串（它就是用户在网络中的身份，泄露 = 被冒充卖家/买家）。
- 上游 API key：GUI 里直接填，存钥匙串；`upstream_key_env` 继续支持（无头/服务器场景）。

### 3.3 进程模型与单一写入者

- 后台 daemon（现在的 client agent）+ 前台 GUI，GUI 只通过已有的回环 `/api` 读写配置，**daemon 是配置文件的唯一写入者**，避免 GUI 与 CLI 并发改文件。
- `/api` 加本地鉴权（启动时生成随机 token 写入 data 目录 0600，GUI 读取），防止同机其它程序/网页通过 localhost 调接口改配置、花额度。
- 自启动：macOS LaunchAgent、Windows 当前用户登录项/计划任务、Linux `systemd --user`。

### 3.4 格式

客户端配置主要由 GUI 写，人很少手改：建议**保留 JSON**（机器写 YAML 会丢注释，没收益），加 `version` 字段做迁移；首次启动自动把旧 `~/.sharetoken/config.json` 迁到新目录、token/key 迁进钥匙串，旧文件改名 `.migrated` 保留。若希望与服务端统一成 YAML 也可以，代价不大。

### mailserver 与门户的邮件配置

`mailserver` 的配置见 `configs/mail.example.yaml`，门户新增 `accounts.email_verification`、`mail.from`、`mail.support`。说明见 [mail.md](mail.md)。
