# 配置

[English](../configuration.md) | **简体中文**

服务端程序（`relay`、`coordinator`、`portal`、`mailserver`）用 YAML 文件、命令行 flag 或两者一起配置。用户客户端把设置放在状态目录里的一个 JSON 文件中。代码在 `internal/config`；样板文件是 [`configs/relay.example.yaml`](../../configs/relay.example.yaml)、[`configs/coordinator.example.yaml`](../../configs/coordinator.example.yaml)、[`configs/portal.example.yaml`](../../configs/portal.example.yaml) 和 [`configs/mail.example.yaml`](../../configs/mail.example.yaml)。

命令本身见 [guide.md](guide.md)；用 Docker Compose 部署整套系统见 [self-hosting.md](self-hosting.md)。

## 1. 服务端程序共用的约定

| 项 | 约定 |
|---|---|
| 入口 | 每个程序都接受 `-config <path>`。不给它时，程序只用 flag 和内置默认值运行。不做隐式查找，所以当前目录里碰巧有个 YAML 文件也不会被误读 |
| 优先级 | 内置默认值 < YAML 文件 < 命令行上**显式给出的** flag。没有给出的 flag 不覆盖文件 |
| 子命令 | `portal useradd …`、`coordinator balance …` 等运维命令读同一份文件（只用 `database`、`identity`、`keys`） |
| 解析 | 未知字段直接报错，用来发现拼错的键 |
| 版本 | 顶层 `version: 1`。其他版本的文件被拒绝 |
| 路径 | 相对路径相对于 YAML 文件所在的目录解析，不是相对于工作目录 |
| 时长与大小 | 时长写作 `30s`、`10m`、`720h`。大小写作 `16384`、`16KiB`、`4MiB`、`1GiB` |
| 机密 | YAML 文件里不放明文机密：用 `password_env` / `password_file` 和 `api_key_env`。机密文件能被其他用户读取时会告警 |
| 校验 | 启动时校验全部配置，一次列出所有错误。`coordinator -config x.yaml check-config`、`portal -config x.yaml check-config`、`mailserver -config x.yaml check-config` 和 `relay -config x.yaml -check-config` 校验文件、打印生效的配置，然后退出。输出里只有环境变量名和文件路径，没有机密 |
| 重新加载 | 配置只在启动时读一次。改动后重启程序生效 |
| 日志 | `log.level`（`debug`、`info`、`warn`、`error`）、`log.format`（`text`、`json`）和 `log.file`（空：stderr）在每个程序里都一样。默认值：`info`、`text`、stderr |

**不在配置文件里、而在数据库里的：** 中继列表（`portal relay add`）、用户、设备与额度、管理员的两步验证密钥，以及门户的系统设置（第 4 节）。这些是运行时数据，由门户管理、协调器同步。

## 2. relay

样板：[`configs/relay.example.yaml`](../../configs/relay.example.yaml)。准入网络只能在文件里设置。

| 键 | flag | 默认值 | 含义 |
|---|---|---|---|
| `identity.key_file` | `-key` | `relay.key` | 身份种子，不存在时以 0600 权限创建。中继的地址里包含由它派生的 peer ID，所以要保留这个文件 |
| `network.listen` | `-listen` | IPv4 和 IPv6 上的 TCP 与 QUIC，端口 4001 | 监听的 multiaddr。主机没有 IPv6 时跳过 IPv6 监听 |
| `network.announce` | `-announce` | 空 | 对外公告的公网 multiaddr，代替网卡地址：在 1:1 NAT 后面需要它。接受 `/dns4`、`/dns6` 域名；公告了域名时，中继打印的 `portal relay add` 提示只用域名地址 |
| `network.max_conns` | `-max-conns` | 8192 | 连接管理器的高水位。资源管理器的全局连接数和流数按它放宽 |
| `access.networks` | — | 空 | 本中继服务的网络，每个网络一项。为空表示开放中继：任何人都可以预约（会打印警告） |
| `access.networks[].name` | — | — | 必填，不可重复 |
| `access.networks[].portal_keys` | — | — | 必填。该网络门户的凭证公钥（`portal pass-key`）；密钥轮换期间可以有多把。同一把密钥不能出现在两个网络里 |
| `access.networks[].allow_peers` | — | 空 | 不需要凭证就能预约的 peer：该网络的协调器（`coordinator id`）。同一个 peer 不能出现在两个网络里。为空时告警，因为协调器将无法在这里预约 |
| `access.networks[].max_devices_per_user` | — | 4 | 同一用户账户同时持有预约的设备数。账户只在本网络内计数 |
| `access.networks[].max_reservations` | — | 0 | 该网络全部设备的预约合计上限；协调器不计入。0：只受 `limits.max_reservations` 限制 |
| `limits.max_reservations` | `-max-reservations` | 4096 | 同时持有的预约数，所有网络合计 |
| `limits.max_reservations_per_ip` | `-max-reservations-per-ip` | 64 | 来自同一 IP 地址的预约数 |
| `limits.max_circuits` | `-max-circuits` | 4096 | 到同一目的 peer 的并发电路数（协调器每个在线用户占一条） |
| `limits.reservation_ttl` | — | 1h | 预约的有效期；至少 1m |
| `limits.circuit_buffer` | — | 16KiB | 每条电路的缓冲区 |
| `limits.memory` | — | 本机（或 cgroup）内存的四分之三 | 所有对端合计可以向 libp2p 资源管理器预留的内存。yamux 每条流预留 256 KiB，一条电路是两条流，而一条空闲电路实际只占约 30 KiB，所以在小内存机器上，限制 TCP 电路数的是它，不是实际用量（453 MB 时约 510 条）。中继还会设置 85% 的 Go 软内存上限，除非设置了 `GOMEMLIMIT` |
| `status.listen` | `-status` | 空 | 在 `/` 提供 JSON 统计，在 `/metrics` 提供 Prometheus 指标。没有认证，所以只接受 `127.0.0.1:8093` 这样的回环地址 |
| `status.stats_interval` | — | 5m | 周期性统计日志的间隔；0 关闭 |
| `log.*` | — | info / text / stderr | 见第 1 节 |

还有两个 flag 没有对应的键：`-check-config`（第 1 节）和 `-print-id`，后者打印身份密钥的 peer ID（密钥不存在时先创建）然后退出。

## 3. coordinator

样板：[`configs/coordinator.example.yaml`](../../configs/coordinator.example.yaml)。

| 键 | flag | 默认值 | 含义 |
|---|---|---|---|
| `identity.key_file` | `-key` | `coordinator.key` | 身份种子，不存在时以 0600 权限创建。中继在 `allow_peers` 里写的就是由它派生的 peer ID；务必备份 |
| `database.dsn` | `-db` | 无，必填 | `mysql://user@host:port/db`。和门户是同一个库 |
| `database.password_env` | — | `SHARETOKEN_DB_PASSWORD` | 存放数据库密码的环境变量 |
| `database.password_file` | — | 空 | 存放数据库密码的文件 |
| `database.max_open_conns` | — | 32 | 连接池大小 |
| `database.max_idle_conns` | — | 8 | 保留的空闲连接数 |
| `database.conn_max_lifetime` | — | 30m | 池中连接的最长寿命 |
| `database.connect_timeout` | — | 10s | 连接超时 |
| `database.lock_wait_timeout` | — | 10s | `innodb_lock_wait_timeout` |
| `database.startup_busy_retry` | — | 2m | 启动时数据库忙，重试多久 |
| `network.listen` | `-listen` | 空 | 监听的 multiaddr。为空：只拨出，经中继可达 |
| `network.max_conns` | `-max-conns` | 8192 | 连接管理器的高水位；每个在线用户占一条经中继的连接 |
| `network.relays` | `-relay`（可重复） | 空 | 除数据库里登记的中继之外，额外预约的中继，写完整的 multiaddr，以 `/p2p/<id>` 结尾 |
| `network.relay_sync` | — | 15s | 与数据库同步中继列表和状态的间隔 |
| `market.fee_bps` | `-fee-bps` | 500 | 平台手续费，按每笔结算的基点计（500 = 5%）；0–10000 |
| `market.min_budget_micros` | — | 1 | 一张票的最小冻结额 |
| `market.warm_up` | — | 30s | 重启后这段时间内，没有匹配 Offer 的请求返回 `capacity`，让买家排队而不是失败 |
| `jobs.ticket_ttl` | — | 1m | 节点开始执行一单的时限；至少 10s |
| `jobs.start_grace` | — | 30s | Ticket 过期后再等多久，释放始终没有开始的单 |
| `jobs.reconnect_grace` | — | 2m | 节点重连后，释放推迟这么久 |
| `jobs.max_stale_tickets` | — | 5 | 一个买家账户持有的闲置未开始的票达到这个数后，不再发新票 |
| `jobs.stale_after` | — | 15s | 未开始的票签发多久后算闲置 |
| `jobs.settle_grace` | — | 15m | 等待结算到达的额外时间 |
| `jobs.dispute_window` | — | 24h | 未确认的结算过这么久后自动生效 |
| `jobs.max_offer_ttl` | — | 5m | Offer 可声明的有效期上限 |
| `jobs.job_retention` | — | 720h | 已完成作业保留多久（账本永久保留） |
| `jobs.suspend_for` | — | 24h | 抽检多次失败的节点被排除在撮合之外多久 |
| `references[]` | `-reference`（可重复） | 空 | 模型的参考副本：有了它才能抽检，争议时才能精确计 token。每项有 `id`（市场模型 ID）、`upstream`（上游服务里的模型名）、`url`（OpenAI 兼容的 base URL）和 `api_key_env` |
| `audit.interval` | `-audit-interval` | 10m | 抽检的平均间隔；0 关闭抽检 |
| `audit.budget_micros` | `-audit-budget` | 200000 | 每次抽检的冻结额，由平台手续费支付 |
| `audit.prompts_file` | `-audit-prompts` | 空 | 挑战 prompt，每行一个。为空：内置的随机 prompt |
| `audit.max_tokens` | — | 96 | 挑战的输出上限 |
| `audit.match_at` | — | 0.8 | 相似度达到或超过它算一致 |
| `audit.mismatch_below` | — | 0.4 | 相似度低于它算不一致 |
| `tee.policy_file` | `-tee-policy` | 空 | TEE 镜像白名单 JSON；设置后启用 `require_tee` 撮合 |
| `tee.online_check` | `-tee-online` | false | 另外向 AMD 和 Intel 查询吊销与 TCB 状态（需要联网） |
| `debug.pprof_listen` | `-debug` | 空 | 提供 Go pprof，例如 `127.0.0.1:6060`；只放在回环地址上 |
| `dev.open_accounts` | `-open-accounts` | false | 仅用于开发：未绑定用户的 peer 用自己的账户交易 |
| `dev.credit_micros` | `-dev-credit` | 0 | 仅用于开发：每个新 peer 首次使用时赠送的额度 |
| `log.*` | — | info / text / stderr | 见第 1 节 |

说明：

- 启动时检查的约束：`jobs.start_grace < jobs.settle_grace < jobs.dispute_window ≤ jobs.job_retention`；`0 < audit.mismatch_below < audit.match_at ≤ 1`；参考模型的 ID 不能重复。
- 给了 `-reference id=…,upstream=…,url=…[,key-env=VAR]` 时，它整体替换 `references[]`。`key-env` 对应 `api_key_env`。
- `audit.interval: 0` 关闭抽检，但保留参考模型用于精确计 token。这适合由商业 API 提供的模型：它们的输出不确定，抽检会误判，而且每次抽检都要花钱。
- 设置任何一个 `dev.*` 选项都会在启动时打印警告。绝不要在公开网络上使用。

## 4. portal

样板：[`configs/portal.example.yaml`](../../configs/portal.example.yaml)。`-db` 和 `-pass-key` 写在命令之前；其余 flag 属于 `portal serve`。

| 键 | flag | 默认值 | 含义 |
|---|---|---|---|
| `database.*` | `-db` 对应 `database.dsn` | 同协调器 | 与协调器 `database` 一节的键相同，但没有 `startup_busy_retry` |
| `keys.pass_key_file` | `-pass-key` | `portal-pass.key` | 签发中继凭证的密钥，不存在时以 0600 权限创建。中继在 `portal_keys` 里写它的公钥 |
| `keys.mfa_key_file` | — | 空 | 加密管理员 TOTP 密钥的 32 字节密钥（64 个十六进制字符）。为空：从凭证密钥派生。单独配置后，轮换凭证密钥不会让管理员的两步验证失效 |
| `http.listen` | `serve -listen` | `:8092` | 监听地址 |
| `http.trusted_proxies` | — | 空 | 其他机器上的反向代理的 IP 地址，门户采信它们传来的 `X-Real-IP`（用于登录限流）。回环上的代理始终采信 |
| `http.read_header_timeout` | — | 10s | 服务端超时 |
| `http.read_timeout` | — | 30s | 服务端超时 |
| `http.write_timeout` | — | 60s | 服务端超时 |
| `http.shutdown_timeout` | — | 5s | 关闭时留给进行中请求的时间 |
| `tls.mode` | `serve -insecure-http` 设为 `insecure`；`serve -behind-tls-proxy` 设为 `proxy` | `auto` | 见下文 |
| `tls.cert_file`、`tls.key_file` | `serve -tls-cert`、`-tls-key` | 空 | 证书和私钥，比如来自 Let's Encrypt。两者必须同时给出 |
| `tls.self_signed_dir` | `serve -tls-dir` | `portal-tls` | 自签名证书保存的位置。要保留它：客户端 pin 了它的指纹 |
| `tls.self_signed_hosts` | `serve -tls-hosts` | 空 | 自签名证书里的域名和 IP |
| `accounts.open_registration` | `serve -open-registration` | false | 没有账户的邮箱第一次登录时创建账户；否则由管理员创建用户（`portal useradd <邮箱>`） |
| `accounts.signup_credit_micros` | `serve -signup-credit` | 0 | 每个新用户获赠的额度 |
| `accounts.mfa_issuer` | — | 空（显示为 ShareToken） | 在验证器应用里显示的名称 |
| `mail.from` | — | 空 | 门户所发邮件的发件人，例如 `ShareToken <team@example.com>`。所有人都用从这里发出的验证码登录，没有它（以及运行中的 `mailserver`）谁都登录不了。为空：不发邮件 |
| `mail.support` | — | 空 | 用户反馈的地址；管理员的回复也从这里发出 |
| `downloads.dir` | — | 空 | 客户端安装包所在的目录，在 `/downloads/` 提供下载。为空：不提供下载 |
| `relay_metrics.*` | — | 关闭 | 经 SSH 读取每个中继的 `/metrics`，供管理控制台的仪表盘使用：`key_file`、`known_hosts_file`、`interval`（1m）、`timeout`（20s），以及 `targets[]`，每项有 `relay`（中继登记里的名称）、`ssh`（`user@host[:port]`）和 `command` |
| `sessions.login_ttl` | `serve -login-ttl` | 1440h | 客户端登录时得到的 token 的有效期（60 天） |
| `sessions.web_session_ttl` | `serve -session-ttl` | 360h | 浏览器会话的有效期（15 天） |
| `sessions.pass_ttl` | `serve -pass-ttl` | 1h | 中继凭证的有效期；至少 5m。禁用用户最迟在这段时间内在中继上生效 |
| `log.*` | — | info / text / stderr | 见第 1 节 |

`tls.mode` 的取值：

| 模式 | 行为 |
|---|---|
| `auto` | 有 `cert_file` 就用它。否则回环地址用明文 HTTP，其他地址用自签名证书 |
| `files` | 使用 `cert_file` 和 `key_file` |
| `self-signed` | 自签名证书；客户端 pin 它的指纹 |
| `proxy` | 门户前面有反向代理终止 TLS：门户自己走明文 HTTP，但把 cookie 标为 Secure |
| `insecure` | 在非回环地址上使用明文 HTTP。仅用于测试网 |

### 存在数据库里的系统设置

管理控制台设置页（`/admin/#/settings`）上编辑的值存在数据库里（`portal_settings` 表），保存后立即生效，不用重启。对 `accounts.open_registration`、`accounts.signup_credit_micros`、`mail.from`、`mail.support`、`sessions.login_ttl` 和 `sessions.web_session_ttl` 来说，`portal.yaml` 里的值只是**还没有保存过设置时**的默认值。第一次保存之后以数据库为准，再改文件不起作用。其余配置（数据库、密钥、监听地址、TLS、下载目录）只在文件里设置。

## 5. mailserver

`mailserver` 读 [`configs/mail.example.yaml`](../../configs/mail.example.yaml)，约定同第 1 节，`database` 一节也相同。它的各个键以及门户的邮件设置在 [mail.md](mail.md) 里说明。

## 6. 用户客户端

客户端不用 YAML 配置。它把设置和状态放在一个目录里：

- 设置了 `$SHARETOKEN_HOME` 就用它，或者用 `sharetoken -home DIR` 给出的目录；
- 否则正式版本用 `~/.sharetoken`，开发版本用 `~/.sharetoken-dev`。从源码构建时没有指定构建类型的程序是开发版本。

这个目录以 0700 权限创建。其中的文件：

| 文件 | 权限 | 内容 |
|---|---|---|
| `config.json` | 0600 | 设置，以及已登录用户的门户 API token |
| `device.key` | 0600 | 设备私钥：它在网络中的身份。在 TEE 里运行的卖家只把密钥放在内存里，不写文件 |
| `network.json` | 0600 | 上次从门户取得的网络信息，门户不可达时使用 |
| `run.json` | 0600 | 正在运行的客户端的地址和 PID，CLI 据此找到它；退出时删除 |
| `providers.json` | 0600 | 用户用来出售的托管 API，以及它们的 key 和 token（[providers.md](providers.md)） |
| `update.json` | 0600 | 上次检查新版本的结果 |
| `warmup.json` | 0600 | 本机 Claude Code 和 Codex 的预热设置（[warmup.md](warmup.md)） |
| `warmup-state.json` | 0600 | 预热做过什么、读到了什么，重启后据此不重复发送 |
| `daemon.log` | 0600 | 桌面应用启动的服务的日志（[desktop.md](desktop.md)） |

`config.json` 由客户端自己写（`login`、`share …`，以及桌面应用使用的本地 API）；`sharetoken config` 打印它。写入时先写临时文件再改名，崩溃不会留下半个文件。它的字段：

| 字段 | 默认值 | 含义 |
|---|---|---|
| `version` | 1 | 文件的格式 |
| `env` | 构建类型 | 写这个文件的构建类型（`prod` 或 `dev`） |
| `portal`、`portal_pin` | — | 门户的 base URL，以及 pin 住的门户自签名证书指纹 |
| `user`、`token`、`device_name` | — | 已登录的用户、门户 API token（机密）、设备在门户设备列表里的名称 |
| `http` | `127.0.0.1:9787`（正式版本）、`127.0.0.1:9887`（开发版本） | 本地 API 和 OpenAI 兼容端点的地址；应保持在回环地址上 |
| `listen` | IPv4 和 IPv6 上的 TCP 与 QUIC，端口任意 | libp2p 监听地址 |
| `nat` | true | UPnP/NAT-PMP 和打洞 |
| `relays` | 空 | 覆盖门户给出的中继列表 |
| `buy.budget_micros` | 200000 | 每个请求冻结的额度；没用完的部分退回 |
| `buy.max_price_in`、`buy.max_price_out` | 0（不限） | 每百万 prompt token 和 completion token 的价格上限 |
| `buy.evidence` | true | 对可以证明有问题的单披露密钥，换取自动退款 |
| `buy.require_tee`、`buy.tee_policy`、`buy.tee_online` | 关闭 | 只从经过证明的 TEE 节点购买；可信镜像的白名单文件；联网查询吊销状态 |
| `buy.queue_seconds` | 30 | 所有节点都忙时，请求等待空闲节点多久；负数：立即失败 |
| `buy.first_byte_seconds`、`buy.idle_seconds` | 30、30 | 一单等待节点第一帧的时间上限，以及两帧之间的时间上限。模型不出声时，节点每 10 秒发一个进度帧 |
| `share.enabled` | false | 共享的开关 |
| `share.upstream`、`share.upstream_key_env` | `http://127.0.0.1:11434/v1`、空 | 本机模型服务的 OpenAI 兼容 base URL，以及存放其 API key 的环境变量 |
| `share.concurrency` | 2 | 并发作业数 |
| `share.models` | 空 | 从本机模型服务共享的模型 |
| `share.relays` | 2 | 在几个中继上持有预约 |
| `share.tee` | `off` | `off`、`auto`、`sev-snp` 或 `tdx` |

`up` 和 `daemon` 的 `-http`、`-listen`、`-nat` 只在这一次运行中覆盖文件，不保存。

客户端拒绝另一种构建类型写的 `config.json`，也拒绝更新的客户端写的文件。格式更旧、已不支持的文件会连同设备私钥和网络缓存一起删除，用户重新登录。

环境变量：

| 变量 | 含义 |
|---|---|
| `SHARETOKEN_HOME` | 状态目录 |
| `SHARETOKEN_PORTAL` | 新登录使用的门户。正式版本忽略它，这样环境变量无法把登录验证码引到别处 |
| `SHARETOKEN_PROVIDER_KEY` | `provider add` 用的 API key，代替交互输入 |
