# ShareToken 使用与部署指南

[English](../guide.md) | **简体中文**

本文介绍命令行客户端的用法、如何逐个程序地部署一套自己的网络，以及两套 HTTP API（客户端的本地 API 和门户的 API）。

- 各部分如何配合：[architecture.md](architecture.md)。设计能防什么、不能防什么：[security.md](security.md)。
- 服务端程序的全部配置项：[configuration.md](configuration.md)。
- 用 Docker Compose 部署：[self-hosting.md](self-hosting.md)。构建与测试：[development.md](development.md)。
- 托管服务面向最终用户的指南在 <https://sharetoken.app/docs/>。

金额单位：1 credit = 1,000,000 micro-credit。价格是「micro-credit / 百万 token」。

## 1. 用户

### 1.1 开始使用

```bash
# 首次：用发到邮箱的验证码登录；没有账户的邮箱会自动创建账户。
# 本机设备会绑定到账户。
sharetoken login -email you@example.com

# 只想用：直接启动客户端。
sharetoken up
#   OpenAI-compatible endpoint: http://127.0.0.1:9787/v1

# 想赚 credit：共享本机模型服务上的模型（默认是 Ollama，http://127.0.0.1:11434/v1）。
sharetoken share detect                     # 列出本机模型
sharetoken share add qwen2.5:7b             # 网络上的模型 ID 默认同名；-id 改名，-in/-out 定价

# 或者共享托管 API。可以配置多个 provider，各自开关。
sharetoken provider add deepseek            # 用 API key：anthropic、openai、gemini、grok、deepseek、custom（-url）
sharetoken provider login openai            # 用 OAuth 登录：openai、gemini、antigravity、grok
sharetoken share add deepseek-flash -provider deepseek

sharetoken up                               # 客户端已在运行时，share 命令立即生效
```

任何 OpenAI 兼容的工具，把 base URL 设为 `http://127.0.0.1:9787/v1` 即可。API key 随便填。

没有密码。`login` 发送 6 位验证码并提示输入；还没有账户的邮箱在同一步注册（门户开放注册时）。脚本里可以分两步：`login -send` 只发送验证码，`login -code 123456` 用它登录。开启了两步验证时，`login` 接着询问身份验证器里的动态码（或用 `-totp` 给出）。

### 1.2 客户端连接哪个门户

门户地址内置在客户端里，没有 `-portal` 参数。

| 构建类型 | 门户 | 状态目录 | 本地 API |
|---|---|---|---|
| prod | `https://sharetoken.app`，即托管服务。忽略 `SHARETOKEN_PORTAL`。 | `~/.sharetoken` | `127.0.0.1:9787` |
| dev（未指定构建类型时的默认值） | `SHARETOKEN_PORTAL` 里的地址。务必设置：内置的默认值是本项目自己的测试网络。 | `~/.sharetoken-dev` | `127.0.0.1:9887` |

要连接自己的部署，用 dev 构建，并在登录时设置 `SHARETOKEN_PORTAL=https://portal.example.com`；之后地址保存在 `config.json` 里。构建类型见 [desktop.md](desktop.md)。

发送任何内容之前，客户端先决定如何信任门户：

- `https://`，证书由公共 CA 签发：不需要别的。
- `https://`，自签名证书：用 `-portal-pin sha256:…` 给出运维公布的指纹。不带这个参数时，客户端显示指纹并请你确认。指纹保存在配置里。
- `http://` 且门户在另一台机器上：拒绝，除非加 `-allow-http`。只在测试网使用。

### 1.3 命令

| 命令 | 作用 |
|---|---|
| `login [-email ADDR] [-send] [-code CODE] [-invite CODE] [-totp CODE]` | 用邮件验证码登录（没有账户的邮箱会创建账户），并绑定本机设备。`-send` 只发送验证码；`-code` 使用已收到的验证码。也接受 `-device NAME`、`-portal-pin sha256:…` 和 `-allow-http`。 |
| `up [-http ADDR] [-listen MADDRS] [-nat=false]` | 运行客户端：买入，开启共享时同时卖出。这些参数只对本次运行有效，不写入配置。 |
| `daemon` | 桌面应用背后的服务：和 `up` 一样，但未登录时也运行。见 [desktop.md](desktop.md)。 |
| `status` | 与协调器的连接和所用中继，当前中继的实时延迟、抖动和丢包（`link:` 行），各中继的往返时间及测量时间，共享情况，告警。 |
| `models` | 网络上的模型：节点数、TEE 节点数、最低价。 |
| `relays [-measure]` | 中继测速结果和当前中继的实时链路质量。`-measure` 立即重测。 |
| `balance` | 可用和冻结的 credit；买入、卖出的作业数和 token 数。 |
| `jobs [-role buy\|sell] [-n 20]` | 最近的作业。 |
| `share` | 查看共享设置。 |
| `share detect [-upstream URL]` | 列出本机模型服务上的模型。 |
| `share add <model> [-provider NAME] [-id ID] [-in P] [-out P] [-max-output N]` | 共享本机模型服务或某个 provider 的一个模型。 |
| `share rm <id>`、`share on`、`share off`、`share upstream <url>` | 停止共享某个模型，开关共享，设置本机模型服务地址。 |
| `provider` | 可添加的 provider 类型，以及已配置的 provider。 |
| `provider add <kind> [-name N] [-url URL] [-header K=V] [-no-check]` | 用 API key 添加托管 API。key 从提示、标准输入或 `$SHARETOKEN_PROVIDER_KEY` 读取，不作为参数传入。 |
| `provider login <kind> [-name N] [-project ID]` | 用 OAuth 登录添加：打开打印出的地址登录，再把授权码或回调地址粘贴回来。 |
| `provider models <name>`、`provider test <name> [-model M] [-prompt TEXT]` | 列出凭据可用的模型；经该 provider 发一个小请求。 |
| `provider on <name>`、`provider off <name>`、`provider rm <name>` | 开始出售它的模型、停止，或连同凭据一起删除。 |
| `pair` | 本账户和谁配对，以及它的配对码。 |
| `pair code [-label L] [-ttl 24h] [-uses N]` | 卖家：生成一个配对码发给买家。默认有效 24 小时，`-ttl` 可改（最长 30 天）；默认不限买家数，`-uses` 可限制。 |
| `pair join <code>` | 买家：和生成这个配对码的卖家配对。此后本账户只向配对的卖家购买。 |
| `pair leave <seller id>`、`pair rm <buyer id>`、`pair revoke <code id>` | 买家一侧或卖家一侧解除配对；让一个配对码不再接纳新买家。 |
| `pair private on\|off` | 卖家：私有模式。开启期间，只有和本账户配对的买家会被撮合过来，也只有他们能看到它的模型。 |
| `skill [install\|uninstall\|show] [-agent claude\|codex\|all] [-force]` | 教编程助手使用客户端的 skill：`install` 在 `~/.claude`（或 `$CLAUDE_CONFIG_DIR`）和 `~/.codex`（或 `$CODEX_HOME`）下写入 `skills/sharetoken/SKILL.md`，默认装给找到的每个助手；写入的内容带有本构建的命令、数据目录和接口地址。不带子命令时显示哪些助手已装。不是客户端写的 `SKILL.md` 不会被覆盖，除非加 `-force`。正文见 [`internal/agentskill/SKILL.md`](../../internal/agentskill/SKILL.md)。 |
| `logout` | 吊销本机的 token。运行中的客户端随即停止。 |
| `config` | 查看配置（token 打码）。 |
| `version [--check]` | 本客户端的构建信息；`--check` 向门户查询是否有新版本。 |

说明：

- 命令前的 `-home DIR` 或环境变量 `$SHARETOKEN_HOME` 指定另一个状态目录。
- 客户端的登录有效 60 天（门户的 `sessions.login_ttl`）。
- 客户端默认同时监听 IPv4 和 IPv6、TCP 和 QUIC（`/ip4/0.0.0.0`、`/ip6/::`，端口由系统分配）。没有 IPv6 的机器自动跳过对应监听。
- 不需要开放任何端口。卖家在中继上预约，买家经中继或打洞连过来。
- 出售托管 API 的卖家模式见 [providers.md](providers.md)。
- 配对发生在账户之间，不是设备之间。一个买家最多配对 3 个卖家，一个卖家最多 1000 个买家。买家只要有一个配对，就只会被撮合到配对的卖家，`models` 也只列出他们出售的模型；没有配对的买家可以向任何人购买。卖家照常服务没有配对的买家，除非开启了私有模式。双方看到的对方账户名是打码的（`al***@example.com`）。见 [architecture.md](architecture.md) 第 2.13 节。

### 1.4 状态目录里的文件

| 文件 | 内容 |
|---|---|
| `config.json` | 设置和门户 token（权限 0600）。 |
| `device.key` | 设备私钥（权限 0600）。换一台机器就是另一个设备。 |
| `providers.json` | 托管 API 的 provider 及其凭据，与 `config.json` 分开存放。 |
| `network.json` | 上次从门户取得的中继列表，门户不可达时使用。 |
| `run.json` | 运行中客户端的 API 地址和 key，供其它命令和桌面应用使用（权限 0600）。 |

### 1.5 config.json

```json
{
  "version": 1,
  "env": "dev",
  "portal": "https://portal.example.com",
  "portal_pin": "sha256:…",
  "user": "alice",
  "token": "st_…",
  "device_name": "laptop",
  "http": "127.0.0.1:9887",
  "nat": true,
  "relays": [],
  "buy": {
    "budget_micros": 200000,
    "max_price_in": 0,
    "max_price_out": 0,
    "evidence": true,
    "require_tee": false,
    "tee_policy": "",
    "queue_seconds": 30,
    "first_byte_seconds": 120,
    "idle_seconds": 60
  },
  "share": {
    "enabled": true,
    "upstream": "http://127.0.0.1:11434/v1",
    "concurrency": 2,
    "relays": 2,
    "models": [
      {"id": "qwen2.5:7b", "upstream_model": "qwen2.5:7b", "price_in": 100000, "price_out": 200000}
    ],
    "tee": "off"
  }
}
```

| 字段 | 含义 |
|---|---|
| `version`、`env` | 文件格式，以及写入该文件的构建类型。另一种构建类型的客户端拒绝读取。 |
| `portal`、`portal_pin` | 门户地址；只有自签名证书的门户才有指纹。 |
| `user`、`token`、`device_name` | 登录信息。`token` 是机密。 |
| `http` | 本地 API 和 OpenAI 端点的地址。请保持在回环地址上。 |
| `listen` | libp2p 监听地址（省略时为上文所述的默认值）。 |
| `nat` | UPnP/NAT-PMP 和打洞。 |
| `relays` | 非空时，取代门户给出的中继列表。 |
| `buy.budget_micros` | 每个请求冻结的 credit；没用完的部分会释放。 |
| `buy.max_price_in`、`buy.max_price_out` | 可接受的最高价；0 表示不限。 |
| `buy.evidence` | 对可证明有问题的作业公开其密钥，以便自动退款。 |
| `buy.require_tee`、`buy.tee_policy`、`buy.tee_online` | 只向 TEE 中的节点购买，且节点镜像须在 `tee_policy` 指向的白名单文件里；`tee_online` 还向厂商查询吊销状态。 |
| `buy.queue_seconds`、`buy.first_byte_seconds`、`buy.idle_seconds` | 等待上限，见[第 3 节](#3-本地管理-api)。默认 30、120、60。 |
| `share.enabled` | 共享已开启。 |
| `share.upstream`、`share.upstream_key_env` | 本机模型服务的 OpenAI 兼容 base URL，以及存放其 API key 的环境变量名（需要 key 时）。 |
| `share.concurrency` | 同时服务的作业数。 |
| `share.relays` | 在几个中继上保持预约。 |
| `share.models[]` | `id`（网络上的 ID）、`upstream_model`（模型服务上的名字）、`price_in`、`price_out`，以及可选的 `max_output`（该模型自身每个请求的输出 token 上限）。 |
| `share.tee` | `off`、`auto`、`sev-snp` 或 `tdx`（机密虚拟机）。 |

## 2. 运维

### 2.1 一套部署包含什么

最小部署是一台公网机器，运行门户、协调器、MySQL 8 和一个中继。需要时在其它网络位置增加中继。

| 程序 | 作用 | 监听 |
|---|---|---|
| `relay` | 在用户之间、用户与协调器之间转发加密流量。不保存账户，看不到明文。 | 一个公网 TCP 和 UDP 端口（默认 4001）。 |
| `portal` | 用户、设备、credit、网页和 HTTP API。 | HTTPS（默认 `:8092`）。 |
| `coordinator` | 撮合、票据、结算、争议、抽检。与门户共用数据库。 | 不监听：只能经中继访问。 |
| `mailserver` | 可选：验证码、通知、客服邮箱。见 [mail.md](mail.md)。 | 需要收信时监听 25 端口。 |

用 `go build -o bin/ ./cmd/...` 构建全部程序。

本节介绍手工部署各个程序。Docker Compose 方式见 [self-hosting.md](self-hosting.md)。每个服务端程序都读取 `-config` 指定的 YAML 文件（样板在 [`configs/`](../../configs/)）；显式给出的命令行参数覆盖文件。下面的例子在更简短的地方直接用参数。全部配置项见 [configuration.md](configuration.md)。

### 2.2 MySQL

数据库只支持 MySQL 8（InnoDB）。`-db`（或 `database.dsn`）写成 `mysql://user@host:port/database`。密码放在环境变量 `SHARETOKEN_DB_PASSWORD` 里，或者用 `database.password_file` 指定一个文件；不要写进 URL，否则会出现在进程列表里。URL 里的其它查询参数作为会话变量传给服务器。

```sql
CREATE DATABASE sharetoken CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;
CREATE USER 'sharetoken'@'10.0.0.%' IDENTIFIED BY '…';
GRANT ALL ON sharetoken.* TO 'sharetoken'@'10.0.0.%';
```

- 门户和协调器启动时创建并升级表结构，谁先启动都行。它们的账号需要建表、改表的权限（`ALL ON sharetoken.*`）。表结构版本见 [versioning.md](versioning.md)。
- 建议的服务器设置：`transaction_isolation = READ-COMMITTED`（程序也会在每个会话上设置）；`skip_name_resolve = ON`（否则每个新连接都可能因反向 DNS 卡住几秒）；`innodb_flush_log_at_trx_commit = 1`（账本要求提交即落盘）；`tmp_table_size = 256M`（账本回放按账户分组，默认的 16 MB 会落盘）；`innodb_buffer_pool_size` 要放得下 `jobs` 表（每个作业的数据加索引约 6 KB）。只允许门户和协调器所在的机器连接 3306 端口。
- 一致性：每个写事务先锁住 `st_write_lock` 表里唯一的一行，门户和协调器的写入因此严格串行。余额是「读出、计算、写回」，并发写入会丢失更新。只读查询不受影响。写入方最多等锁 10 秒，超时按数据库忙处理（门户返回 503 `busy`；启动时两个程序都会重试一段时间）。
- 测试：`scripts/test.sh` 在 Docker 里启动一个 MySQL 8，再对它运行 `go test`。要使用现成的服务器，设置 `SHARETOKEN_TEST_MYSQL=mysql://user@host:3306/`；每个测试创建一个名为 `sttest_<时间>_<随机>` 的库，超过一小时的库由之后的测试删除。详见 [development.md](development.md)。

### 2.3 启动各个程序

```bash
export SHARETOKEN_DB_PASSWORD=…
DSN=mysql://sharetoken@127.0.0.1:3306/sharetoken

# 中继需要的两个值。两个程序各自在首次运行时生成密钥。
portal -db $DSN pass-key             # ed25519:…  验证中继凭证用的公钥
coordinator -db $DSN id              # 12D3Koo…   协调器的 peer ID
```

**中继**，每台公网机器一个。准入写在配置文件的 `access.networks` 里：门户的公钥和协调器的 peer ID。一个中继可以服务多个网络（[`configs/relay.example.yaml`](../../configs/relay.example.yaml)）。

```bash
relay -config relay.yaml -key relay.key \
      -listen /ip4/0.0.0.0/tcp/8091,/ip6/::/tcp/8091,/ip4/0.0.0.0/udp/8091/quic-v1,/ip6/::/udp/8091/quic-v1 \
      -announce /ip4/203.0.113.7/tcp/8091,/ip6/2001:db8::7/tcp/8091 -status 127.0.0.1:8093
```

- 不写 `-listen` 时，中继监听 4001 端口，IPv4 和 IPv6、TCP 和 QUIC。
- 云主机在 1:1 NAT 后面时，用 `-announce` 声明公网地址，IPv4 和 IPv6 都写上。
- 中继会打印 `portal relay add '<地址>' [name] [region]`。把地址原样复制到门户。机器没有 IPv6 时，它会提示只有 IPv6 的用户无法到达。
- `-status` 在 `/` 提供 JSON 统计，在 `/metrics` 提供 Prometheus 指标。它没有认证，必须是回环地址。
- 配置了 `access.networks` 时，只有出示门户凭证的注册用户设备和协调器可以预约。不配置则是谁都能用的开放中继。
- 保留好 `relay.key`：用户知道的地址里包含 peer ID。

**门户**，与协调器使用同一个库：

```bash
portal -db $DSN serve -listen :8092 -open-registration -signup-credit 5000000
portal -db $DSN useradd -admin ops@example.com
portal -db $DSN relay add '/ip4/203.0.113.7/tcp/8091/p2p/12D3Koo…,/ip6/2001:db8::7/tcp/8091/p2p/12D3Koo…' relay-1 eu-west
```

- TLS：给出 `-tls-cert fullchain.pem -tls-key privkey.pem` 时，门户使用该证书。没有证书时，它在 `portal-tls/` 里生成自签名证书并打印指纹 `sha256:…`；把指纹公布给用户，并保留这个目录，因为客户端固定了这个指纹。前面有终止 TLS 的反向代理时用 `-behind-tls-proxy`。明文 HTTP 只在监听回环地址时使用，或者加 `-insecure-http`（仅测试网）。这些选择对应配置文件里的 `tls.mode`。
- 一个中继登记一条。同一 peer ID 的多个地址用逗号连接。已登记的中继要改地址，先 `relay rm` 再重新添加。
- 这里给出的开放注册和注册赠送额度是初始值。管理员在后台的系统设置页保存之后，以保存的值为准。
- 不加 `-open-registration` 时，只有管理员能创建用户（`portal useradd <邮箱>` 或管理后台）。
- 所有人都用发到邮箱的验证码登录，所以门户必须配置发件人（`mail.from`）并运行邮件服务（[mail.md](mail.md)）；否则谁都登录不了，管理员也一样。

**协调器**。它不监听端口，自己在所有登记的中继上预约：

```bash
coordinator -db $DSN -fee-bps 500 \
  -reference id=qwen2.5:7b,upstream=qwen2.5:7b,url=http://127.0.0.1:11434/v1
```

`-reference` 可选：模型的一份参考副本，用于抽检提供该模型的节点，并在争议中精确计数 token。`-fee-bps` 是平台手续费，按每笔结算作业的万分比计。

### 2.4 运维命令

| 命令 | 作用 |
|---|---|
| `portal useradd [-admin] <email>` | 创建账户。账户名是邮箱，用户用发到那里的验证码登录。没有密码。 |
| `portal rename <user> <email>` | 给账户改名，新名字必须是没有被占用的邮箱：用于没有邮箱的老账户（比如 `root`），不改名就登录不了。余额、设备、token 和两步验证都保留。 |
| `portal login-code <email>` | 给已有账户生成一个登录验证码并直接打印，不发邮件：用于收不到邮件的用户。它和邮件里的验证码一样用（网页和客户端都行，只能用一次，10 分钟有效），并作废这个地址之前的验证码；两步验证照常要求（`portal mfa-reset` 可清除）。审计日志记为 `user.login_code`。运行它的人能以该用户身份登录，相当于以前运维重置密码。 |
| `portal grant <user> <micros> [memo]` | 发放 credit。负数为扣减；余额不能扣到负数。 |
| `portal users`、`portal user <user>`、`portal disable\|enable <user>` | 列出和查看用户。禁用立即生效：token 失效，协调器拒绝该用户的设备。 |
| `portal mfa-reset <user>` | 清除用户的两步验证密钥并吊销其 token（见[第 4.2 节](#42-浏览器会话与两步验证)）。 |
| `portal relay add\|ls\|rm\|enable\|disable` | 中继登记。协调器在 15 秒内跟进变化。 |
| `portal overview` | 用户数、流通中的 credit、平台手续费、未结冻结额、账本回放结果、各状态的作业数、可达的中继数。 |
| `portal version-policy [set -min V -recommended V -latest V -download-url URL -allow-dev]` | 服务哪些客户端版本（[versioning.md](versioning.md)）。 |
| `portal pass-key` | 中继验证凭证用的公钥。 |
| `coordinator id` | 协调器的 peer ID。 |
| `coordinator disputes`、`resolve <job-id> pay\|refund`、`nodes`、`audits`、`relays`、`check`、`balance <peer-id>` | 争议及其人工裁决、节点记录、抽检、中继登记、账本检查、某个设备的余额。 |
| `portal check-config`、`coordinator check-config`、`relay -check-config` | 校验配置并打印生效的设置。 |

运维命令直接读写数据库，可以和运行中的服务同时执行。

### 2.5 密钥、中继准入与容量

- 备份 `portal-pass.key` 和 `portal-tls/`。`portal-pass.key` 用来签发中继凭证。没有设置 `keys.mfa_key_file` 时，加密管理员两步验证密钥的密钥也由它派生，这种情况下更换 pass key 会迫使管理员重新绑定。
- 更换 pass key：先在所有中继该网络的 `portal_keys` 里加上新公钥，再换门户的密钥，一小时（凭证的有效期）后去掉旧公钥。
- 中继准入，`access.networks` 的每一项：`portal_keys`、`allow_peers`（协调器）、`max_devices_per_user`、`max_reservations`。门户一侧：`-pass-key portal-pass.key` 和 `serve -pass-ttl 1h`。
- 中继容量（默认值远高于 libp2p 的默认值）：`-max-reservations 4096`、`-max-reservations-per-ip 64`、`-max-circuits 4096`（到同一目标的并发线路数；协调器每个在线用户占一条）、`-max-conns 8192`。

### 2.6 门户网页

`portal serve` 同时提供网页。它们内嵌在二进制里（[`internal/portal/web`](../../internal/portal/web)），不需要另外部署。

| 地址 | 面向 | 内容 |
|---|---|---|
| `/` | 所有人 | 官网：项目介绍、安全设计、下载。 |
| `/docs/`、`/changelog/` | 所有人 | 用户指南和更新日志。 |
| `/downloads/<file>` | 所有人 | `downloads.dir` 目录里的客户端安装包；`GET /api/v1/downloads` 列出每一类的最新版本，官网显示实际存在的包。没有配置目录时不提供下载。 |
| `/app/`（`/login` 跳转到这里） | 用户 | 邮件验证码登录（新邮箱同时注册）；总览（余额、冻结额、买卖汇总）；账单；作业（按买入或卖出筛选）；API token（明文只显示一次）及其吊销；设备改名和解绑；配对（兑换配对码、解除卖家；生成和撤销配对码、移除买家、私有模式）；网络（协调器、中继可达性）；账户（开启或关闭两步验证）；反馈。 |
| `/admin/` | 管理员 | 仪表盘和总览（账本回放、流通额、手续费、中继、作业统计、各账户余额）；用户（搜索、新建、发放或扣减 credit、禁用、设置或撤销管理员、重置两步验证、吊销 token、解绑设备、账单与作业）；中继（登记、编辑、启用、停用、删除）；客户端版本；全部作业（按状态和用户筛选）；节点记录；推理审计；争议及其人工裁决（付款给节点或退款给买家）；配对（谁只向谁购买、私有卖家）；全局账本；邮件；反馈；操作日志；系统设置。 |

- 浏览器会话默认 15 天（`serve -session-ttl`，或系统设置页）。它存放在 HttpOnly cookie 里，页面脚本读不到。在 API Token 页上它显示为「web session」，当前会话有标记，任何一个都可以吊销。
- 管理员用同一个账号进入 `/admin/`。其他人打开它只会看到「不是管理员」。
- 使用自签名证书时，浏览器会提示证书不受信任；核对 `portal serve` 打印的指纹后再继续。否则给门户配置 CA 证书，或者把它放在 Caddy、nginx 等反向代理后面并加 `-behind-tls-proxy`，让 cookie 带上 Secure。
- 页面只调用门户自己的 `/api/v1` 接口，不加载任何第三方资源，所以没有外网的部署也能用。
- 在后台裁决争议与 `coordinator resolve` 等价：门户直接在共用数据库里结算，手续费率取协调器启动时写入 `meta.fee_bps` 的值。
- 面向用户的页面有多语言翻译（[i18n.md](i18n.md)）。主题跟随系统，除非访问者自己选择；选择保存在浏览器里。
- 页面的浏览器测试在 `scripts/*_ui_test.mjs`；见 [development.md](development.md)。

## 3. 本地管理 API

`sharetoken up` 监听 `http` 里的地址（prod 构建为 `127.0.0.1:9787`）。带有非本地 `Origin` 的请求返回 403，所以用户浏览器里的网页无法花掉 credit。本机的其他程序和用户同样能访问回环地址，所以 `/api/v1/` 还要求运行中的客户端每次启动时写进状态目录 `run.json`（权限 0600）的 key，形式为 `Authorization: Bearer <key>`；没有 key 返回 401 `local_auth`。CLI 从这里读取；脚本也可以这样做：

```bash
curl -s -H "Authorization: Bearer $(python3 -c 'import json,os;print(json.load(open(os.path.expanduser("~/.sharetoken/run.json")))["key"])')" http://127.0.0.1:9787/api/v1/status
```

OpenAI 兼容端点 `/v1/` 不需要 key。桌面应用使用的 `sharetoken daemon` 提供同一套 API，另加未登录时可用的登录接口；见 [desktop.md](desktop.md)。

| 方法与路径 | 说明 |
|---|---|
| `GET /api/v1/status` | 完整状态：`version`、`peer`、`user`、`portal`、`started`、`api`、`coordinator{peer,connected,relayed,via,link}`、`measured_at`（上次全量测速的时间）、`relays[{addr,name,region,rtt_ms,error,coordinator_ok,current,reserved}]`、`buy`、`share{…设置, providers, serving, running, stats{jobs_served,jobs_failed,earned_micros,active_jobs,announce_error}, reservations}`、`warnings[]`、`update`。`coordinator.link{relay,samples,last_ms,avg_ms,min_ms,max_ms,jitter_ms,loss_pct,last_error,updated_at}` 是当前中继最近 30 次 ping 的统计，每 10 秒一次；首次 ping 之前不出现。 |
| `GET /api/v1/models` | 网络上的模型。 |
| `GET /api/v1/account` | 透传门户的 `/api/v1/me`：余额、用量、设备。 |
| `GET /api/v1/jobs?role=buy\|sell&limit=` | 透传门户的作业列表。 |
| `GET /api/v1/ledger?limit=&before=` | 透传门户的账单。 |
| `POST /api/v1/relays/measure` | 立即重测中继并返回结果。 |
| `GET /api/v1/share`、`PUT /api/v1/share` | 读取或替换共享设置。请求体是 `share` 对象；校验后立即生效并保存。 |
| `POST /api/v1/share/start`、`POST /api/v1/share/stop` | 开启或关闭共享。 |
| `POST /api/v1/share/reload` | 从 `config.json` 重新加载共享设置。 |
| `GET /api/v1/share/detect[?upstream=]` | 列出本机模型服务上的模型。 |
| `/api/v1/providers…` | 托管 API 的 provider：列出、添加、开关、删除、可用模型、测试请求、OAuth 登录。路由列在 [`internal/client/providers.go`](../../internal/client/providers.go) 的文件头。返回内容里从不包含 key；`Origin` 不是 API 自身地址的请求一律 403。 |
| `GET /api/v1/pairing`、`POST /api/v1/pairing/codes`、`DELETE /api/v1/pairing/codes/{id}`、`POST /api/v1/pairing/redeem`、`DELETE /api/v1/pairing/sellers/{id}`、`DELETE /api/v1/pairing/buyers/{id}`、`PUT /api/v1/pairing/private` | 配对，带着用户的 token 转给门户的同名接口（[`internal/client/pairing.go`](../../internal/client/pairing.go)）。 |
| `POST /api/v1/login/check` | 立即检查登录是否仍然有效（`sharetoken logout` 会调用）。平时每分钟检查一次；登录失效后，`up` 退出，daemon 转为 `expired` 状态。 |
| `POST /v1/chat/completions`、`GET /v1/models` | OpenAI 兼容端点。响应带有 `X-Sharetoken-Job`、`X-Sharetoken-Node`、`X-Sharetoken-Verified`、`X-Sharetoken-Cost-Micros` 这几个头，节点经过证明时还有 `X-Sharetoken-TEE`。 |

`/v1/chat/completions` 的行为：

- **冻结额。** 请求带 `max_tokens`（或 `max_completion_tokens`）时，只按所匹配节点的价格冻结它最坏情况下的成本。不带时冻结整个 `budget_micros`。批量或高并发调用时请设置 `max_tokens`。
- **排队。** 所有节点都满载时，请求最多等待 `queue_seconds`（默认 30）。仍然没有空位就返回 503 `capacity`，并带 `Retry-After` 头。
- **节点卡死。** 节点在 `first_byte_seconds`（默认 120）内没有发出第一帧，或者两帧之间超过 `idle_seconds`（默认 60）没有数据，就判定为卡死。还没有输出的请求换一个节点重试；已经开始输出的流以错误事件结束。慢模型处理长 prompt 时请调大这两个值。
- **中途断开。** 流式请求中途断开（例如用户点了停止）时，只按已经送达的部分计费。
- **错误。** `{"error":{"message":…,"type":"sharetoken_error","code":…}}`，其中：`bad_request` 400（模型服务拒绝的请求会带上原因）、`budget` 400、`insufficient_funds` 402、`unauthorized` 和 `forbidden` 403（例如账户被禁用）、`upgrade_required` 426（客户端低于网络要求的最低版本）、`no_offers` 503（没有节点提供该模型）、`capacity` 503（暂时满载，可重试）、`upstream_error` 和 `receipt_rejected` 502。
- **推理模型。** 推理过程计入 `max_tokens`。`max_tokens` 太小时，`content` 可能为空，只有 `reasoning_content`。

## 4. 门户 HTTP API

### 4.1 接口

认证方式是 `Authorization: Bearer st_…`。门户自己的页面改用 cookie 会话（[第 4.2 节](#42-浏览器会话与两步验证)）。错误统一为 `{"error":{"code":"…","message":"…"}}`。

列表接口接受 `limit`（默认 50，最大 500）、`page` 或 `offset`、`from` 和 `to`，以及用于导出的 `format=csv`。

| 方法与路径 | 认证 | 说明 |
|---|---|---|
| `GET /healthz` | 无 | `{"ok":true}`。 |
| `GET /api/v1/info` | 无 | `{registration_open, signup_credit_micros, session_hours, passes_enabled, support_email, invite_required, default_page_size, code_sign_in}`：登录页需要的信息。`code_sign_in` 恒为 true：用邮件验证码登录。 |
| `GET /api/v1/network` | 无 | `{coordinator, relays:[{addr,name,region,ok,rtt_ms}], pass_key}`。可用的中继在前，其中延迟低的排在最前。`rtt_ms` 是协调器到该中继的往返时间，测得之前省略。协调器从未启动过时返回 503 `not_ready`。 |
| `GET /api/v1/version`、`GET /api/v1/update`、`GET /api/v1/downloads` | 无 | 服务端版本和客户端版本策略；针对 `kind`、`channel`、`os`、`arch` 是否有更新的客户端；提供下载的客户端安装包。见 [versioning.md](versioning.md)。 |
| `POST /api/v1/auth/code` | 无 | `{email, invite_code?}`：发送登录验证码（[mail.md](mail.md)）→ `{ok, new_account, cooldown_seconds, expires_in_seconds}`。没有账户的邮箱先检查注册规则：403 `registration_closed`、`email_not_allowed`、`invite_required`（带上 `invite_code` 再请求）或 `bad_invite`；被禁用的账户 403 `account_disabled`。距上一个验证码不到一分钟 429 `code_cooldown`，超过限额 429 `throttled`。门户没有发件人时 503 `mail_unavailable`。 |
| `POST /api/v1/auth/verify` | 无 | `{email, code, invite_code?, token_name?, totp?}` → `{user, token, created}`；邮箱新建了账户（并获得注册赠送额度）时 201，否则 200。token 有效期是 `sessions.login_ttl`（默认 60 天）。验证码错误或过期 401 `bad_code`；错误次数按 IP 和邮箱分别计数，之后 429 `throttled`。开启了两步验证的账户得到 401 `mfa_required` 并带 `challenge`（用 `/api/v1/mfa` 完成），除非 `totp` 里带了正确的动态码；尚未绑定的管理员得到 401 `mfa_setup_required`（绑定在网页上完成）。 |
| `POST /api/v1/mfa` | 无 | `{challenge, code, token_name}` → `{user, token}`。challenge 有效 5 分钟，错 5 次作废；错误的动态码计入登录失败。 |
| `POST /api/v1/logout` | 是 | 吊销当前 token。 |
| `GET /api/v1/me` | 是 | `{user, balance{account,available_micros,held_micros}, usage{bought,sold}, devices}`。 |
| `POST /api/v1/account/mfa/setup` | 是 | 开始开启两步验证：→ `{challenge, secret, url, qr}`（base32 密钥、`otpauth://` 链接、data URI 形式的二维码）。已开启时 409 `mfa_enrolled`。 |
| `POST /api/v1/account/mfa/enable` | 是 | `{challenge, code}`：用 App 里的第一个动态码开启 → `{user}`。400 `bad_code`、400 `challenge_expired`。会发邮件通知。 |
| `POST /api/v1/account/mfa/disable` | 是 | `{code}`：用当前动态码关闭 → `{user}`。管理员得到 403 `mfa_required`，必须保留。会发邮件通知。 |
| `GET /api/v1/tokens`、`POST /api/v1/tokens`、`DELETE /api/v1/tokens/{id}` | 是 | API token。`POST {name, ttl_hours}`（0 为不过期）只返回一次明文。 |
| `GET /api/v1/devices`、`POST /api/v1/devices`、`DELETE /api/v1/devices/{peer}` | 是 | 设备。`POST {peer, name, ts, sig}`，其中 `sig` 是设备对 `accounts.BindMessage` 的签名（[`internal/accounts`](../../internal/accounts)）。 |
| `PATCH /api/v1/devices/{peer}` | 是 | `{name}`：改名。 |
| `GET /api/v1/ledger?limit=&before=` | 是 | 账单：正数入账，负数出账，包含冻结和退款明细。 |
| `POST /api/v1/relay-pass` | 是 | `{peer}` → 中继凭证，只发给调用者已绑定的设备。客户端自动调用。 |
| `GET /api/v1/jobs?role=&limit=&before=` | 是 | 作业：角色、模型、状态、token 数、费用、收入。 |
| `GET /api/v1/usage` | 是 | 买入和卖出汇总。 |
| `POST /api/v1/feedback`、`GET /api/v1/feedback` | 是 | 提交反馈；列出自己的反馈。 |
| `GET /api/v1/pairing` | 是 | 调用者的配对：`sellers[]` 和 `buyers[]`（`id`、打码的 `username`、`label`、`since`），有效的 `codes[]`（`id`、`code`、`label`、`max_uses`、`used`、`expires_at`），`restricted`（为 true 时只向 `sellers` 购买）、`private`（为 true 时只卖给 `buyers`）和 `max_sellers`。 |
| `POST /api/v1/pairing/codes`、`DELETE /api/v1/pairing/codes/{id}` | 是 | 生成配对码（`label`、`ttl_hours` 最大 720、`max_uses`；0 表示一天、不限人数）和撤销配对码。用被撤销的码建立的配对保留。有效配对码达到 20 个时 409 `limit`。 |
| `POST /api/v1/pairing/redeem` | 是 | `{code}`：调用者作为买家和配对码的卖家配对。404 `bad_code`（错误、过期、已撤销或已用完；和登录失败一样计数，之后 429 `throttled`），409 `own_code`，409 `limit`。 |
| `DELETE /api/v1/pairing/sellers/{id}`、`DELETE /api/v1/pairing/buyers/{id}` | 是 | 以买家或卖家身份解除一个配对。 |
| `PUT /api/v1/pairing/private` | 是 | `{enabled}`：开启或关闭调用者的私有模式。 |
| `GET /api/v1/admin/pairings?user=` | 管理员 | 全部配对：买家、卖家（完整账户名）、配对码备注、时间、卖家是否私有。`user` 匹配任一方账户名的一部分。 |
| `GET /api/v1/admin/overview`、`GET /api/v1/admin/dashboard`、`GET /api/v1/admin/relay-history?range=` | 管理员 | 平台汇总与账本回放；仪表盘数据；中继历史。 |
| `GET /api/v1/admin/users`、`POST /api/v1/admin/users`、`GET /api/v1/admin/users/{name}` | 管理员 | 用户列表、创建用户（`{username, is_admin}`，名字是邮箱）、用户详情（余额、用量、设备、token）。 |
| `GET /api/v1/admin/users/search?q=&limit=&offset=` | 管理员 | 按用户名搜索（通配符按字面匹配）。每行带余额、冻结额、设备数和 token 数；同时返回总数。 |
| `POST /api/v1/admin/users/{name}/credit` | 管理员 | `{micros, memo}`；负数为扣减。 |
| `POST /api/v1/admin/users/{name}/disable`、`…/enable` | 管理员 | 禁用或启用（不能对自己）。 |
| `POST /api/v1/admin/users/{name}/admin` | 管理员 | `{is_admin}`（不能撤销自己的权限）。 |
| `POST /api/v1/admin/users/{name}/mfa/reset` | 管理员 | 清除该用户的两步验证密钥并吊销其全部 token：丢失验证器时的恢复手段。 |
| `GET /api/v1/admin/users/{name}/tokens`、`DELETE …/tokens/{id}`、`POST …/tokens/revoke` | 管理员 | 列出 token、吊销一个、吊销全部。 |
| `DELETE /api/v1/admin/users/{name}/devices/{peer}` | 管理员 | 解绑设备。 |
| `GET /api/v1/admin/users/{name}/ledger`、`…/jobs` | 管理员 | 该用户的账单和作业。 |
| `GET /api/v1/admin/relays`、`POST /api/v1/admin/relays`、`PATCH …/{id}`、`POST …/{id}/enable`、`POST …/{id}/disable`、`DELETE …/{id}` | 管理员 | 中继登记：添加、改名或设置地区、启用、停用、删除。 |
| `GET /api/v1/admin/jobs?status=&user=&account=&before=&limit=` | 管理员 | 全部作业，含双方账户、争议原因和裁决。 |
| `GET /api/v1/admin/nodes` | 管理员 | 节点信誉记录，附所属用户。 |
| `GET /api/v1/admin/audits?limit=` | 管理员 | 推理审计记录。 |
| `GET /api/v1/admin/disputes?limit=` | 管理员 | 争议，未裁决的在前。 |
| `POST /api/v1/admin/disputes/{job}/resolve` | 管理员 | `{decision: "pay"\|"refund"}`：人工裁决。 |
| `GET /api/v1/admin/ledger?account=&before=&limit=` | 管理员 | 全局账本流水，附每个 `user:<id>` 账户对应的用户名。 |
| `GET /api/v1/admin/accounts` | 管理员 | 所有账本账户的余额和冻结额。 |
| `GET /api/v1/admin/versions`、`PUT /api/v1/admin/version-policy` | 管理员 | 网络上运行的版本；客户端版本策略。 |
| `GET /api/v1/admin/settings`、`PUT /api/v1/admin/settings`、`GET/POST /api/v1/admin/invites`、`DELETE /api/v1/admin/invites/{id}` | 管理员 | 系统设置页（注册、邮件地址、会话、限流、审计保留期）和邀请码。 |
| `GET /api/v1/admin/oplog` | 管理员 | 操作日志：通过管理 API 和 `portal` 运维命令所做的更改。 |
| `/api/v1/admin/mail/…`、`/api/v1/admin/feedback…` | 管理员 | 邮件部分（[mail.md](mail.md)）和用户反馈。 |

版本策略不再服务的客户端，在 `auth/verify`、`mfa` 和绑定设备时得到 426 `upgrade_required`；它已有的东西继续可用。

### 4.2 浏览器会话与两步验证

| 方法与路径 | 说明 |
|---|---|
| `POST /api/v1/web/auth/verify` | `{email, code, invite_code?}`，在 `POST /api/v1/auth/code` 之后调用，检查和 `auth/verify` 相同。成功后，把一个有效期为会话时长（`sessions.web_session_ttl`，默认 15 天）的 token 放进 `st_session` cookie（HttpOnly、SameSite=Strict，HTTPS 下带 Secure）；响应体是 `{user}`，没有 token。 |
| `POST /api/v1/web/mfa`、`POST /api/v1/web/mfa/setup`、`POST /api/v1/web/mfa/confirm` | 开启了两步验证时浏览器登录的第二步：校验动态码；尚未绑定的管理员开始绑定（返回 base32 密钥、`otpauth://` 链接和 data URI 形式的二维码），再用第一个动态码确认绑定并建立会话。 |
| `POST /api/v1/web/logout` | 吊销会话并清除 cookie。 |

用 cookie 认证的请求还必须带请求头 `X-Sharetoken-Web: 1`，并且 `Origin`（浏览器附带时）必须是门户自己；否则返回 403 `cross_site`。Bearer 请求不受影响。门户不响应 CORS 预检，所以别的网站构造不出这样的请求。

**管理员必须使用两步验证，其他人可以自选**，在账户设置里开启（上面的 `/api/v1/account/mfa/…`）。开启了的账户，只凭邮件验证码永远不会建立会话。管理员第一次登录时，门户要求他用验证器应用（Google Authenticator、Microsoft Authenticator、1Password 等）扫描二维码或输入密钥，并填入第一个 6 位验证码。此后每次登录都需要验证码：RFC 6238 定义的 TOTP，每 30 秒换一次，每个验证码只能用一次，前后各容忍一个周期的时钟偏差。

token 和会话会记录是否通过了第二步，所有 `/api/v1/admin/*` 接口只接受通过了的那些。把普通用户提升为管理员之后，其现有会话会被拒绝（403 `mfa_required`），直到重新登录。

密钥用 `keys.mfa_key_file` 以 AES-GCM 加密后存放；没有设置时，使用由门户 pass key 派生的密钥。

有人丢失验证器时，由管理员在后台该用户的页面上重置，或者由运维在服务器上执行下面的命令。管理员下次登录时重新绑定；其他人之后只用邮件验证码登录，直到再次开启。

```bash
portal -db mysql://sharetoken@127.0.0.1:3306/sharetoken mfa-reset <user>
```

## 5. 本机演示

```bash
scripts/demo-local.sh     # 需要 Docker（用于 MySQL）、Ollama 和模型 gemma4:e4b；MODEL_UPSTREAM 可换成别的模型
```

脚本构建各个程序，并在 127.0.0.1 上启动两个中继、一个门户、一个协调器和三个用户：alice，诚实的卖家；carol，虚报用量的卖家；bob，买家。它演示验证码登录与设备绑定、中继选择、作弊被抓并按证据退款、抽检两个卖家、一个中继宕机后客户端切换、credit 在账户之间流动且账目平衡，以及日志里没有任何 prompt。脚本退出时停止所有进程。
