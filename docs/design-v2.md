# sharetoken 设计文档 v2：用户 / 中继 / 协调器 / 门户 四角色架构

> 版本 v2（2026-11-07，分支 `arch-v2`）。v1 的密码学机制（收据、证据裁决、抽检、TEE）全部保留，见 [design-v1.md](design-v1.md)；本文只讲 v2 的架构调整。使用与部署见 [guide-v2.md](guide-v2.md)，测试与实测见 [validation-v2.md](validation-v2.md)。

## 1. 角色

| 角色 | 程序 | 做什么 | 不做什么 |
|---|---|---|---|
| **用户** | `sharetoken` | 一个进程同时是买家和卖家：本地 OpenAI 兼容端点（买），可选共享本机模型（卖）；自动选最优中继；本地管理 API（给以后的 GUI 用） | 不需要开放端口，不直连协调器 |
| **中继** | `relay` | 只做 libp2p circuit relay v2 转发；有多个，可由不同运营方部署 | 不记账、无状态、看不到明文（Noise 端到端） |
| **协调器** | `coordinator` | 算账平台：撮合、签 Ticket、结算、争议与证据裁决、抽检 | **不监听任何端口**，只通过在每个中继上的预约被访问；不管用户 |
| **门户** | `portal` | 用户管理后端（HTTP JSON API）：注册登录、API token、设备绑定、credit 余额与发放、账单与 token 用量、中继登记 | 不碰 p2p，不和协调器通信 |

门户与协调器**共用一个数据库**，彼此不直接通信：门户写账户与 credit 发放，协调器写作业与结算，两边都只通过复式账本、在数据库事务里动钱。

```
                         HTTPS（注册、登录、余额、中继列表）
   ┌──────────────┐ ─────────────────────────────────────────────► ┌──────────┐
   │ 用户 sharetoken│                                                 │  portal  │
   │  买 + 卖       │                                                 └────┬─────┘
   │  127.0.0.1:9787│      ┌─────────┐   ┌─────────┐                      │ 共用数据库
   └──────┬───────┘      │ relay A │   │ relay B │ …                 ┌────┴──────┐
          │ 测 RTT，选最优 └──┬──────┘   └──┬──────┘                   │  SQLite   │
          ├──────────────────┘ 控制面        │                         │(以后 MySQL)│
          │   /p2p-circuit ───────────────► 协调器在每个中继上都有预约 ◄─┤           │
          │                                 │                         └────┬──────┘
          │                                 └──── coordinator（无监听）─────┘
          │ 数据面：买家 ⇄ 卖家，直连或经中继（Noise + box 双层加密）
   ┌──────┴───────┐
   │ 另一个用户     │
   └──────────────┘
```

## 2. 关键设计

### 2.1 钱属于用户，不属于 peer

- v1：一个 peer ID 一个账户。v2：门户把设备的 peer ID **绑定**到用户，协调器在每个经认证的 libp2p 流上把 peer 解析成 `user:<id>` 账户（`accounts.DeviceAccount`）。一个用户可以有多台设备，卖出赚到的钱任何一台都能花。
- 作业表新增 `buyer_acct`、`node_acct`、`prompt_tokens`、`completion_tokens`：付款方和收款方在**签 Ticket 时**固定下来，之后解绑设备也不会改变这一单的去向。
- 撮合排除**同一用户**的所有设备（v1 只排除同一 peer），防止自买自卖刷手续费和信誉。
- 未绑定或已禁用用户的设备：公告 Offer、申请 Ticket、查余额都返回 `unauthorized`。测试与本地演示可用 `-open-accounts` 退回 v1 的「一个 peer 一个账户」。
- 抽检身份由平台出资，仍是 peer 级账户（`acct:<peer>`）。

### 2.2 设备绑定必须证明持有设备私钥

绑定请求带设备对 `canon("sharetoken/device-bind/v1", 用户名, peer ID, 时间戳)` 的 Ed25519 签名，门户验签并要求时间戳在 ±5 分钟内。否则任何人都能把别人的节点绑到自己名下、截走收入。已绑定到别的用户的设备不能被抢绑（`409 bound_elsewhere`）。TEE 节点每次启动换新密钥，客户端启动时用保存的 token 自动重新绑定。

### 2.3 协调器只能经中继访问

- 协调器默认 **不监听**（`DialOnly`），启动后在每个中继上预约（`p2p.Reserver`，到期前续约、断线重约），用户只知道 `<relay>/p2p-circuit/p2p/<coordinator>`。
- 中继列表来自数据库：管理员用 `portal relay add` 登记，协调器每 15 秒同步一次，**预约状态一变就**把每个中继是否可达写回数据库（`relays.coordinator_ok`），门户 `GET /api/v1/network` 把它交给用户。
- 控制面经中继也仍然是 Noise 端到端：中继看不到 Ticket、收据，更看不到争议证据里的单次密钥。所以选择「协调器在中继上预约」而不是「中继做应用层转发」——后者会让中继读到控制面全部内容。
- 抽检器也经中继访问协调器和节点，和普通买家同路。

### 2.4 用户按网络质量选择中继

1. 启动时从门户取中继列表（门户不可达时用上次缓存的 `network.json`）。
2. 并行连接每个中继，各 ping 3 次取中位数 RTT（`p2p.MeasureRelays`）。
3. `chooseRelay`：在门户标记「协调器可达」的中继里选 RTT 最小的；都不可达时退而选任何能连上的。为防抖动，只有新的最优比当前快 `max(5ms, 20%)` 才切换。
4. 每 10 秒检查到协调器的连接，不通就先刷新门户列表、再重测并立即切换；每 5 分钟例行重测。
5. 实时链路质量：每次 10 秒检查时顺便 ping 一次当前中继（`p2p.LinkMonitor`），保留最近 30 次（约 5 分钟），算出当前延迟、平均/最小/最大、抖动（相邻两次 RTT 差的平均）和丢包率，放在状态的 `coordinator.link` 里给 CLI 和 GUI 显示。换中继时窗口清空重算。全量测速仍是 5 分钟一次，这项只多一次 ping，开销可以忽略。注意它测的是「本机到中继」这一段，不含中继到协调器或卖家那段。
6. 共享模型时在最优的 2 个中继上预约（可配置），Offer 里同时通告本机地址和中继地址；预约集合变化时立即重新公告。买家能直连就直连，DCUtR 打洞失败时走中继。

### 2.5 Offer 改为直接公告

v1 用 GossipSub 广播 Offer；v2 只有一个协调器，改为每 15 秒经控制面发一次 `announce`（同时是心跳），去掉了 pubsub 依赖。协调器校验「公告者 == Offer 的签名节点」并解析其账户。

### 2.6 节点放弃失败的单（修复 v1 已知问题 3.2）

节点在交付任何内容之前失败（模型服务出错、满载、请求解不开）时，向协调器发 `abandon`：协调器立即退回买家的冻结额并释放节点的并发名额，记一次失败。只有被分配的节点能放弃、且只能在结算前放弃——放弃等于放弃收款，节点没有说谎的动机。v1 中这样的单要冻结约 17 分钟并占着节点名额。

### 2.7 买家拿了 Ticket 不来连（修复另一半）

协调器原先分不清「买家没来」和「任务还在长时间运行」，只能等结算截止（约 17 分钟）。现在：

- 节点接下任务时（买家连上、Ticket 校验通过、拿到并发名额）**在后台**向协调器发 `started`，不增加首字节延迟；协调器记下 `jobs.started_at`。
- Ticket 过期（默认 2 分钟）再过 30 秒仍未开始的单被释放：冻结额全数退回买家，**不扣节点信誉**，名额随之空出。节点最多被占 2.5 分钟，而不是 17 分钟。
- 已开始的单照旧，等结算或到截止时间（这时才算节点失败）。
- 防止反复刷票：一个买家账户里「签发超过 15 秒仍未开始」的 Ticket 达到 5 张时，不再给它新的 Ticket（`busy`）。正常并发请求几秒内就会开始，不受影响。
- 迟到的 `started`（单已因未开始被释放）会收到 `conflict`，节点随即中止这一单，避免白干。
- 参数：`TicketTTL`、`StartGrace`、`MaxStaleTickets`、`StaleAfter`（`coordinator.Config`）。

剩余的代价：恶意买家仍能让节点每张票空等最多 2.5 分钟，但同一账户最多 4 张，而账户要在门户注册。

### 2.8 门户默认 HTTPS

- `portal serve` 监听非回环地址时**默认 HTTPS**：提供 `-tls-cert/-tls-key`（如 Let's Encrypt 证书）就用它；否则首次启动生成自签名证书（ECDSA P-256，10 年，保存在 `-tls-dir`，默认 `portal-tls/`），并打印指纹 `sha256:…`。
- 明文 HTTP 只在监听回环地址（本机演示）或显式 `-insecure-http` 时使用。
- 客户端：
  - CA 签发的证书按常规校验，不需要 pin；
  - 自签名证书：`login/register -portal-pin sha256:…` 必须与服务器证书一致，或者交互确认运维公布的指纹（类似 SSH 首次连接）；指纹存进 `config.json`，以后每次连接都只认这张证书；
  - 指纹不符时拒绝并提示可能被中间人攻击；
  - 拒绝把密码以明文 HTTP 发往非本机地址（`-allow-http` 仅限测试网）。
- 自签名证书要保留：换证书即换指纹，所有用户都得重新确认。

### 2.9 中继准入：门户签发的短期凭证

预约是中继的稀缺资源（有了预约，别人才能经它找到你），所以只给注册用户的设备。中继仍然不连门户、不连数据库：

1. 门户持有一把 Ed25519 签名密钥（`portal-pass.key`，`portal pass-key` 打印公钥）。用户客户端用自己的 token 调 `POST /api/v1/relay-pass {peer}`；门户只给**本人已绑定**、用户未被禁用的设备签发 `{peer, account, 签发时间, 过期时间}`，默认有效 1 小时（`-pass-ttl`）。
2. 客户端每次预约或续约前，先在 `/sharetoken/relay-pass/1.0.0` 上把凭证交给中继（`p2p.Reserver.Prepare`）。凭证剩不到 15 分钟就自动换新；门户暂时连不上时继续用未过期的旧凭证。
3. 中继用配置的门户公钥离线验证：签名正确、未过期、凭证里的 peer 就是 libp2p 认证过的对方。通过后在 ACL（`relayv2.WithACL`）里放行这台设备的预约，直到凭证过期。
4. 同一用户账户同时持有预约的设备数有上限（`-max-devices-per-user`，默认 4），比按 IP 限制更准，不会误伤同一运营商 NAT 后的大量用户。
5. 协调器不是用户，在中继上用 `-allow-peer <coordinator id>` 放行（`coordinator id` 打印 ID）。

性质：
- **禁用用户最迟一小时内生效**：门户不再给他新凭证，下次续约就被中继拒绝；不需要通知任何中继。
- **凭证偷了也没用**：凭证写明 peer ID，没有那台设备的私钥就通不过 libp2p 认证；而且很快过期。
- **只管预约，不管经中继连接**：能被连到的只有注册用户和协调器，它们各自认证来访者（协调器拒绝未绑定设备，节点只认 Ticket）；抽检器的临时身份因此不受影响。
- **密钥轮换**：中继的 `-portal-key` 可以写多个，新旧密钥并存一段时间（一个凭证有效期以上）即可。
- 中继不配 `-portal-key` 时保持开放并打印警告；客户端遇到不支持凭证协议的旧中继照常预约。

### 2.10 IPv6 / 双栈

- **默认双栈监听**：客户端默认监听 `/ip4/0.0.0.0` 和 `/ip6/::`（TCP + QUIC），中继同理。没有 IPv6 的机器上 IPv6 监听失败会被跳过（libp2p 只要有一个监听成功就能启动）。旧配置里保存的「只有 IPv4」默认值启动时自动升级，手动指定的 `-listen` 不变。
- **为什么客户端也要监听 IPv6**：家宽 IPv6 通常是公网地址、没有 NAT，卖家公告了 IPv6 地址后，同样有 IPv6 的买家可以直连，不占中继带宽；只能主动连出时就只能走中继。
- **一个中继登记一条、多个地址**：中继地址可以是同一 peer ID 的多个 multiaddr 用逗号连接（`/ip4/…/p2p/X,/ip6/…/p2p/X`）。`p2p.ParseRelay` 把它们合成一个 `AddrInfo`，测速、预约、电路地址都覆盖全部地址，各用户用自己能连的那个。门户按 peer ID 去重并规范化。中继启动时直接打印可以粘贴的 `portal relay add '…'`（每个地址族优先公网地址）；没有 IPv6 时提示一句。
- **双栈中继是桥**：只有 IPv4 的用户和只有 IPv6 的用户经同一个双栈中继互相买卖。中继对两边各是一条普通连接，Noise 端到端加密不变。

### 2.11 用户端为 GUI 准备

`sharetoken up` 在 `127.0.0.1:9787` 同时提供 OpenAI 端点（`/v1`）和管理 API（`/api`，见 guide-v2.md §3）。CLI 的 `status`、`models`、`relays`、`share` 都走这套 API，GUI 直接复用即可。安全措施：

- 只绑 loopback；`up` 把实际地址写进 `~/.sharetoken/run.json`，CLI 据此找到正在运行的客户端。
- **拒绝网页跨域请求**：带 `Origin` 且不是 localhost / 回环 / 应用自定义 scheme（`tauri://`、`wails://`）的请求一律 403，防止任意网页借用户浏览器花用户的 credit。
- 配置文件 `config.json` 权限 0600（含门户 token）；设备私钥 `device.key` 0600。

### 2.12 门户网页的安全模型

门户把用户页面（`/`）和管理控制台（`/admin/`）编进二进制一起提供，两套页面共用 `/api/v1`。威胁与对策：

| 威胁 | 对策 |
|---|---|
| 会话被页面脚本（XSS）窃取 | 会话 token 只放在 **HttpOnly** cookie，脚本读不到；cookie `SameSite=Strict`，HTTPS 下 `Secure` |
| 跨站请求伪造（含登录 CSRF） | cookie 只在请求同时带自定义头 `X-Sharetoken-Web: 1` 时生效；浏览器附带 `Origin`/`Sec-Fetch-Site` 时必须是门户本身；门户不答复 CORS 预检，所以别的站点发不出这种请求。登录/注册端点同样受此限制。Bearer 认证不走 cookie，CLI 不受影响 |
| 注入脚本 | 严格 CSP：`default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'`，页面没有任何内联脚本/样式，也没有第三方资源；页面用 `textContent` 构建 DOM，不拼 HTML；`X-Content-Type-Options: nosniff`、`Referrer-Policy: no-referrer`、`X-Frame-Options: DENY` |
| 撞库、刷注册 | 登录失败限流沿用（同用户名/同 IP 5 分钟 8 次）；注册按 IP 同样限流；改密码答错当前密码也计入 |
| 管理员误操作 | 不能禁用自己、不能撤销自己的管理员；禁用、吊销、退款等破坏性操作页面二次确认；所有管理操作写日志（谁对谁做了什么） |
| 会话长期有效 | 浏览器会话默认 24 小时（`-session-ttl`），在 token 列表里可见并可吊销；改密码可一键吊销其他会话；管理员重置密码默认吊销该用户全部 token |
| 门户被反向代理 | `-behind-tls-proxy` 或 `X-Forwarded-Proto: https` 时 cookie 仍标 Secure，Origin 校验按 https 比较 |

争议裁决（`POST /api/v1/admin/disputes/{job}/resolve`）由门户进程直接在共用数据库里结算（`coordinator.ResolveDispute`）：和协调器的 `finalize/refund` 是同一段代码，**服务中的**协调器启动时把手续费率写进 `meta.fee_bps` 供门户读取（`Engine.RecordFeeRate`，运维子命令不会覆盖它）；SQLite 的 `BEGIN IMMEDIATE` 保证两个进程的写事务串行。

**管理员两步验证。** 管理员的密码只换来一个 5 分钟有效的 challenge，不发任何 token。已绑定的管理员再交一个 TOTP 码（`POST /api/v1/web/mfa` 或 `/api/v1/mfa`；API 客户端也可在登录请求里直接带 `totp`）；未绑定的管理员先在网页完成绑定（服务器生成 20 字节密钥，以二维码和 base32 文本展示，首个验证码正确才写库），所以第一次登录也不存在"没有二步的管理员会话"。校验接受前后各一个周期，并记录最后接受的步数，同一码不能重放；错码计入登录限流，一个 challenge 错 5 次作废。`api_tokens.mfa` 记录 token 是否来自两步登录，`admin` 中间件要求它为真：提升为管理员、或本功能上线之前的旧 token 都拿不到管理权限，必须重新登录。密钥用门户签名私钥（`portal-pass.key`）派生的 AES-GCM 密钥加密后存于 `users.mfa_secret`，拿到数据库文件不等于拿到验证器密钥；没有该密钥的门户进程（如管理命令）无法让管理员登录，但 `portal mfa-reset` 不需要它。普通用户不要求两步验证。

## 3. 数据库

### 3.1 现在：SQLite，门户与协调器放在同一台机器

两个进程打开同一个文件（WAL、`busy_timeout`、`BEGIN IMMEDIATE`），SQLite 负责跨进程串行化写事务。谁先启动都会建全表（`coordinator.Migrate`），v1 数据库原地升级（补列）。

| 表 | 写入方 | 读取方 |
|---|---|---|
| `users`、`api_tokens`、`devices` | 门户 | 协调器（`devices` 解析账户）、门户 |
| `relays` | 门户（登记）、协调器（可达状态） | 门户、协调器 |
| `ledger_*` | 门户（发放/扣减）、协调器（冻结/结算/退款） | 两边 |
| `jobs`、`nodes`、`audits`、`peers`、`meta` | 协调器 | 门户（账单、用量、协调器 ID） |

### 3.2 以后：MySQL（建议），门户和协调器分机部署

问题「使用 MySQL？」的回答：**是**，等门户和协调器需要分开部署、或要多个门户实例时，用 MySQL 8（InnoDB）远程连接。SQLite 不能跨机器共享。迁移要处理的点（都集中在 `ledger`、`accounts`、`coordinator/store.go`、`internal/sqlite`）：

| SQLite 写法 | MySQL 对应 |
|---|---|
| `STRICT` 表、`INTEGER PRIMARY KEY AUTOINCREMENT` | 去掉 STRICT；`BIGINT AUTO_INCREMENT` |
| `INSERT … ON CONFLICT (k) DO UPDATE` | `INSERT … ON DUPLICATE KEY UPDATE` |
| `BEGIN IMMEDIATE` 单写者；门户只读路径用只读（deferred）事务，WAL 下不取写锁、不被协调器的写阻塞；写路径遇 SQLITE_BUSY 返回 503，启动迁移遇忙重试 | 事务内对账户行 `SELECT … FOR UPDATE`；协调器仍是单进程，内存锁照旧；只读事务换成 `START TRANSACTION READ ONLY` |
| 部分索引 `… WHERE open = 1`、`… WHERE dispute_outcome != ''` | 普通索引 `(owner, open)`、`(dispute_outcome, created_at)` |
| 列表查询用两段 `ORDER BY … LIMIT` 子查询 `UNION ALL` 合并（代替 `a = ? OR b = ?`，避免对整段历史排序）；`+status = ?` 禁用索引 | `UNION ALL` 写法原样可用；`+col` 技巧换成 `IGNORE INDEX` 或直接建 `(acct, status, created_at)` 索引 |
| `pragma_table_info` 补列 | `information_schema.COLUMNS` |
| TEXT 主键/唯一键 | `VARCHAR(128)`（peer ID 52 字符） |
| 对账 CTE | MySQL 8 支持，原样可用 |

**已实现（2026-11-08）**：`-db mysql://user@host:port/db` 选择 MySQL（`internal/sqlite/mysql.go`）。实际做法和上表的出入：

- 表结构是单独的一份 `SchemaMySQL`（`ledger`、`accounts`、`coordinator` 各一份，索引直接写在 `CREATE TABLE` 里）；部分索引改成普通复合索引，`TEXT` 主键/索引列改成 `VARCHAR(191)`，二进制列用 `VARBINARY`/`MEDIUMBLOB`，排序规则 `utf8mb4_bin`（与 SQLite 一样按字节比较）。
- DML 只有一份，写成两边都能执行的形式：upsert 改为「先 UPDATE，没匹配到再 INSERT」（连接开启 `CLIENT_FOUND_ROWS`，RowsAffected 和 SQLite 一样按匹配行计数）；`'a' || b` 改为 `CONCAT`；派生表加别名；`meta.key` 用反引号；`LIKE` 的转义字符换成 `!`，并对两边做 `LOWER`。
- 单写者：没有逐个改成 `SELECT … FOR UPDATE`，而是所有写事务经 `sqlite.Begin` 先锁 `st_write_lock` 的唯一一行，门户和协调器的写入跨进程严格串行，原有「事务内读出再写回」的代码不用改；只读事务（门户的 `ledgerRead`）不取这把锁。`TestMySQLWritersAreSerialized` 验证两个连接池并发读改写不丢更新。
- `+status` 技巧在 MySQL 里是无操作（一元加号被解析器丢弃），保留不影响结果。
- 迁移：`coordinator copy-db <DSN>` 逐表复制到空库。

测试环境的门户和协调器已于 2026-11-08 迁到 orb VM `st-db` 上的 MySQL 8.0（见 test-env.md 和 validation-v2-mysql.md）。

## 4. 威胁对照（v2 新增部分）

| 威胁 | 防御 | 代码 |
|---|---|---|
| 中继读取控制面或推理内容 | 控制面和数据面都是 Noise 端到端，推理内容再加 box | `p2p`、`box` |
| 中继宕机 | 用户自动切换中继；卖家同时在多个中继预约；协调器在所有中继预约 | `client.maintain`、`p2p.Reserver` |
| 有人直连协调器绕过中继 | 协调器不监听任何地址 | `cmd/coordinator`（`DialOnly`） |
| 把别人的节点绑到自己账户 | 绑定需设备签名 + 时间窗；已绑定设备不能被抢 | `accounts.VerifyBind`、`Store.BindDevice` |
| 同一用户多设备自买自卖 | 撮合按账户排除 | `Engine.IssueTicket` |
| 被盗 token | token 可随时吊销；登录 token 30 天过期；只存 SHA-256 | `accounts`、`portal` |
| 密码暴力破解 | argon2id；同用户名/同 IP 5 分钟内失败 8 次即 429 | `portal.throttled` |
| 恶意网页调用本地端点 | Origin 检查 | `client.guardOrigin` |
| 拿 Ticket 不来连，占满节点名额 | 未开始的单在 Ticket 过期 + 30 秒后释放；每账户最多 4 张闲置票 | `Engine.MarkStarted`、`Engine.Tick` |
| 陌生节点占用中继预约名额 | 预约需门户签发的短期凭证，按用户限设备数 | `relaypass`、`relay/admission.go` |
| 窃听或篡改门户通信（密码、token） | 默认 HTTPS；自签名证书由客户端 pin 指纹；拒绝明文发往远端 | `portal/tls.go`、`client.NewPortalClient` |
| 禁用用户继续交易 | 协调器每次请求都查用户状态；token 立即失效 | `accounts.DeviceAccount` |
| 网页会话被窃取 / 跨站请求伪造 / 注入脚本 | HttpOnly + SameSite cookie；自定义头 + Origin 校验；严格 CSP、无内联代码 | `portal/web.go`（§2.12） |
| 管理员口令泄露 / 被猜中 | 管理员强制 TOTP 两步验证；无二步的 token 进不了 `/admin/*`；错码限流、challenge 限次、防重放；密钥加密存放 | `portal/mfa.go`、`internal/totp`（§2.12） |

## 5. 诚实的边界

- **经中继的连接不设准入**：任何人都能经中继连到已预约的用户（会被对方认证后拒绝，但消耗中继带宽）。中继的流量上限仍靠总量参数。
- **拿票不来连仍有短暂代价**：每张票最多让节点空等 2.5 分钟（每账户最多 4 张）。
- **自签名证书依赖首次确认**：用户必须把指纹与运维公布的核对；有域名时更推荐 CA 证书。没有内置 ACME 自动签发，可以用 `-tls-cert` 指向 certbot 生成的证书或放在 Caddy/nginx 后面。
- **IPv6 只在本机回环验证过**：双栈和跨地址族桥接在 `::1` 上测过，公网 IPv6、运营商防火墙、只有 v6 的 NAT64 网络未实测。
- **协调器单实例**，账本仍由它和门户记录（与 v1 相同）。
- **用量统计只覆盖保留期内的作业**（默认 30 天）；钱的历史在账本里永久保留。
- **抽检经费从平台手续费账户预支**，抽检进行中平台账户可能为负，结束后余额自动退回。
- v1 的其余边界（无 TEE 时运营者可见 prompt、抽检是概率性的等）不变。

## 6. 下一步

1. ~~MySQL 驱动（§3.2），门户与协调器分机部署。~~ 已实现（2026-11-08）。
2. GUI：基于 `/api`（状态、中继、共享开关、账单）做桌面端。门户网页已就绪（§2.12）。
3. 门户的支付通道（目前 credit 只能由管理员发放）。
