# 架构

[English](../architecture.md) | **简体中文**

sharetoken 是一个 LLM 推理的点对点交易市场。本文说明系统的四个角色、它们之间怎样通信，以及背后的设计取舍。收据、基于证据的争议裁决、抽检和 TEE 证明是底层的密码学基础；本文讲的是建立在它们之上的角色划分和架构。

相关文档：命令与 API 见 [guide.md](guide.md)，配置文件见 [configuration.md](configuration.md)，威胁模型与安全保证见 [security.md](security.md)，自建网络见 [self-hosting.md](self-hosting.md)，构建与测试见 [development.md](development.md)。

## 1. 角色

| 角色 | 程序 | 做什么 | 不做什么 |
|---|---|---|---|
| **用户** | `sharetoken` | 一个进程同时是买家和卖家：本地 OpenAI 兼容端点（买），可选共享本机模型或托管 API（卖）；自动选择最优中继；提供本地管理 API，供 CLI 和桌面应用使用 | 不需要开放端口，不直连协调器 |
| **中继** | `relay` | 只做 libp2p circuit relay v2 转发；有多个，可由不同运营方部署 | 不记账、无状态、看不到明文（Noise 端到端） |
| **协调器** | `coordinator` | 算账平台：撮合、签 Ticket、结算、争议与证据裁决、抽检 | **不监听任何端口**，只能通过它在各中继上的预约访问；不管用户 |
| **门户** | `portal` | 用户管理后端（HTTP JSON API 和网页）：注册登录、API token、设备绑定、credit 余额与发放、账单与 token 用量、中继登记 | 不碰 p2p，不和协调器通信 |

门户与协调器**共用一个数据库**，彼此不直接通信。门户写账户与 credit 发放，协调器写作业与结算，两边都只通过复式账本、在数据库事务里动钱。

还有第五个程序 `mailserver`：投递门户排队的邮件（登录验证码、通知；没有密码，所以没有它谁都登录不了），并为少数几个邮箱收信，见 [mail.md](mail.md)。

```
                         HTTPS（验证码登录、余额、中继列表）
   ┌───────────────┐ ────────────────────────────────────────────► ┌──────────┐
   │ 用户 sharetoken │                                                │  portal  │
   │  买 + 卖        │                                                └────┬─────┘
   │  127.0.0.1:9787│      ┌─────────┐   ┌─────────┐                      │ 共用数据库
   └──────┬────────┘      │ relay A │   │ relay B │ …                 ┌────┴──────┐
          │ 测 RTT，       └──┬──────┘   └──┬──────┘                   │   MySQL   │
          │ 选最优            │             │                         │           │
          ├───────────────────┘ 控制面      │                         │           │
          │   /p2p-circuit ──────────────► 协调器在每个中继上都有预约 ◄─┤           │
          │                                 │                         └────┬──────┘
          │                                 └──── coordinator（无监听）─────┘
          │ 数据面：买家 ⇄ 卖家，直连或经中继（Noise + box 双层加密）
   ┌──────┴────────┐
   │ 另一个用户      │
   └───────────────┘
```

### 源码布局

| 路径 | 内容 |
|---|---|
| `cmd/sharetoken`、`cmd/relay`、`cmd/coordinator`、`cmd/portal`、`cmd/mailserver` | 各个可执行程序 |
| `internal/client`、`internal/buyer`、`internal/node`、`internal/provider` | 用户客户端：agent 与本地 API、买、卖、托管 API 的 provider |
| `internal/relay`、`internal/relaypass` | 中继及其准入凭证 |
| `internal/coordinator`、`internal/ledger`、`internal/audit`、`internal/tokens` | 撮合与结算、复式账本、抽检、参考模型的 token 计数 |
| `internal/portal`、`internal/accounts`、`internal/mailer`、`internal/totp` | 门户、账户存储、邮件、TOTP |
| `internal/p2p`、`internal/protocol`、`internal/box`、`internal/receipt`、`internal/evidence`、`internal/attest`、`internal/canon` | libp2p 主机与中继、线上帧格式与消息、内容加密、收据、证据、TEE 证明、规范化签名字节 |
| `internal/config`、`internal/mysqldb`、`internal/secfile`、`internal/cli`、`internal/version` | 配置文件、数据库层、私有文件、CLI 辅助、版本信息 |
| `gui/` | 桌面外壳（[desktop.md](desktop.md)） |
| `test/e2e`、`test/flow` | 端到端测试 |
| `configs/*.example.yaml` | 配置文件样板 |

## 2. 关键设计

### 2.1 钱属于用户，不属于 peer

- 门户把设备的 peer ID **绑定**到用户。协调器在每个经认证的 libp2p 流上把 peer 解析成账户 `user:<id>`（`accounts.DeviceAccount`）。一个用户可以有多台设备，在一台上卖出赚到的 credit，任何一台都能花。
- 每个作业记录 `buyer_acct`、`node_acct`、`prompt_tokens`、`completion_tokens`。付款方和收款方在**签 Ticket 时**固定下来，之后解绑设备也不会改变这一单的钱的去向。
- 撮合排除**同一用户**的所有设备，防止自买自卖刷手续费和信誉。
- 未绑定的设备，或所属用户已被禁用的设备，公告 Offer、申请 Ticket、查余额都返回 `unauthorized`。测试与本地演示可用 `-open-accounts`（`dev.open_accounts`）让每个 peer 自成一个账户。
- 抽检身份由平台出资，仍是 peer 级账户（`acct:<peer>`）。

### 2.2 绑定设备必须证明持有设备私钥

绑定请求带有设备对 `canon("sharetoken/device-bind/v1", 用户名, peer ID, 时间戳)` 的 Ed25519 签名。门户验签，并要求时间戳在 ±5 分钟内（`accounts.VerifyBind`）。否则任何人都能把别人的节点绑到自己名下、截走收入。已绑定到别的用户的设备不能被抢绑（`409 bound_elsewhere`）。TEE 里的节点只把密钥放在客户机内存中，每次启动都换新密钥，所以客户端启动时用保存的 token 自动重新绑定。

### 2.3 协调器只能经中继访问

- 协调器默认**不监听**（`DialOnly`）。启动后它在每个中继上预约（`p2p.Reserver` 在到期前续约、断线后重新预约）。用户只知道 `<relay>/p2p-circuit/p2p/<coordinator>`。
- 中继列表在数据库里。管理员用 `portal relay add` 登记中继；协调器每 15 秒同步一次列表（`network.relay_sync`），并把自己在每个中继上是否持有预约写回数据库（`relays.coordinator_ok`）。门户通过 `GET /api/v1/network` 把它交给用户。
- 控制面经中继时仍然是 Noise 端到端：中继看不到 Ticket 和收据，更看不到争议证据里披露的单次密钥。所以选择「协调器在中继上预约」，而不是「中继做应用层转发」，后者会让中继读到控制面的全部内容。
- 抽检器也经中继访问协调器和节点，和普通买家同路。

### 2.4 用户按网络质量选择中继

1. 客户端启动时从门户取中继列表。门户不可达时，用上次缓存在 `network.json` 里的列表。
2. 并行连接每个中继，各 ping 3 次，取中位数 RTT（`p2p.MeasureRelays`）。
3. `chooseRelay` 在门户标记为「协调器可达」的中继里选 RTT 最小的；都不可达时退而选任何能连上的。为了防抖动，只有新的最优比当前的快出 `max(5 ms, 20%)` 以上才切换。
4. 客户端每 10 秒检查一次到协调器的连接。不通就刷新门户列表、重新测速并立即切换。无论如何每 5 分钟做一次全量测速。
5. 实时链路质量：每次 10 秒检查时顺便 ping 一次当前中继（`p2p.LinkMonitor`）。最近 30 次结果（约 5 分钟）给出当前延迟、平均/最小/最大、抖动（相邻两次 RTT 差的平均）和丢包率，放在状态的 `coordinator.link` 里，供 CLI 和桌面应用显示。换中继时窗口清空重算。持续丢包的中继会被换掉，并在 5 分钟内不再选用。注意它测的是「本机到中继」这一段，不含中继到协调器或卖家那一段。
6. 共享模型时，客户端在最优的 2 个中继上预约（可配置），Offer 同时通告本机地址和中继地址；预约集合变化时立即重新公告。买家能直连就直连，DCUtR 打洞失败时走中继。

### 2.5 Offer 直接公告

节点每 15 秒经控制面发一次 `announce`，它同时是心跳。协调器校验公告者就是 Offer 的签名节点，并解析它的账户。

### 2.6 节点放弃无法交付的单

节点在交付任何内容之前失败时（模型服务出错、满载、请求解不开），向协调器发 `abandon`。协调器立即退回买家的冻结额，释放节点的并发名额，并记一次失败。只有被分配的节点能放弃，而且只能在结算前放弃。放弃等于放弃收款，所以节点没有说谎的动机。

### 2.7 买家拿了 Ticket 不来连

协调器必须分清「买家没来」和「任务还在运行」。

- 节点接下任务时（买家已连上、Ticket 校验通过、拿到并发名额），**在后台**向协调器发 `started`，不增加首字节延迟。协调器记下 `jobs.started_at`。
- Ticket 过期（有效期默认 1 分钟）后再过 30 秒仍未开始的单被释放：冻结额全数退回买家，**不扣节点信誉**，名额随之空出。节点最多被占 1.5 分钟。
- 已开始的单照常运行，直到结算或到结算截止时间；到那时才算节点失败。
- 防止囤票：一个买家账户里「签发超过 15 秒仍未开始」的 Ticket 达到 5 张时，不再给它新的 Ticket（`busy`）。正常的并发请求几秒内就会开始，不受影响。
- 迟到的 `started`（这一单已因未开始被释放）会收到 `conflict`，节点随即停止这一单。
- 参数：`coordinator.Config` 的 `TicketTTL`、`StartGrace`、`MaxStaleTickets`、`StaleAfter`（配置文件里的 `jobs.*`）。

剩余的代价：恶意买家仍能让节点为每张票空等最多 1.5 分钟，但每个账户这样的票数有上限，而账户要在门户注册。

### 2.8 门户默认 HTTPS

- `portal serve` 监听非回环地址时**默认使用 HTTPS**。给了 `-tls-cert`/`-tls-key`（比如 Let's Encrypt 证书）就用它；否则首次启动时生成自签名证书（ECDSA P-256，有效期 10 年，保存在 `-tls-dir`，默认 `portal-tls/`），并打印指纹 `sha256:…`。
- 明文 HTTP 只在三种情况下使用：监听回环地址（本机演示）、`-behind-tls-proxy`（前面的反向代理终止 TLS）、显式的 `-insecure-http`。配置文件里的 `tls.mode` 提供同样的选择。
- 客户端一侧：
  - CA 签发的证书按常规校验，不需要 pin。
  - 自签名证书：`login -portal-pin sha256:…` 必须与服务器证书一致，或者由用户交互确认运维公布的指纹（类似 SSH 首次连接）。指纹存进 `config.json`，以后只认这张证书。
  - 指纹不符时客户端拒绝连接，并提示可能有中间人攻击。
  - 客户端拒绝把登录验证码以明文 HTTP 发往别的机器（`-allow-http` 仅用于测试网）。
- 自签名证书要保留：换证书就换指纹，所有用户都得重新确认。

### 2.9 中继准入：门户签发的短期凭证

预约是中继的稀缺资源（有了预约，别人才能经它找到你），所以只给注册用户的设备。中继仍然不连门户、不连数据库：

1. 门户持有一把 Ed25519 签名密钥（`portal-pass.key`，`portal pass-key` 打印公钥）。用户客户端用自己的 token 调 `POST /api/v1/relay-pass {peer}`。门户只给**该用户已绑定**的设备签发 `{peer, account, 签发时间, 过期时间, client}`，并且用户不能已被禁用。凭证默认有效 1 小时（`-pass-ttl`）。`client` 是客户端随请求上报的版本、系统和架构（`X-ST-Client-Version`、`X-ST-Client-Platform: darwin/arm64`），门户只检查格式。
2. 每次预约或续约之前，客户端先在 `/sharetoken/relay-pass/1.0.0` 上把凭证交给中继（`p2p.Reserver.Prepare`）。凭证剩余不到 15 分钟时自动换新；门户暂时连不上时，客户端继续用未过期的凭证。
3. 中继用配置里的门户公钥离线验证：签名正确、未过期、凭证里的 peer 就是 libp2p 认证过的对方。通过后，在 ACL（`relayv2.WithACL`）里放行这台设备的预约，直到凭证过期。
4. 同一用户账户同时持有预约的设备数有上限（`max_devices_per_user`，默认 4）。这比按 IP 限制更准，不会误伤同一运营商 NAT 后面的大量用户。
5. 协调器不是用户，中继通过 `allow_peers` 放行它（`coordinator id` 打印 ID）。

一个中继可以服务多个互相独立的网络。中继配置文件里 `access.networks` 的每一项是一个网络，各有自己的门户公钥、协调器和限额（见 [configuration.md](configuration.md)）。

性质：

- **禁用用户最迟一小时内生效。** 门户不再给他签发凭证，下次续约就被中继拒绝。不需要通知任何中继。
- **凭证被偷也没用。** 凭证写明了 peer ID，没有那台设备的私钥就通不过 libp2p 认证；而且它很快过期。
- **只管预约，不管经中继的连接。** 能被连到的只有注册用户和协调器，它们各自认证来访者（协调器拒绝未绑定的设备，节点只认 Ticket）。抽检器的临时身份因此不受影响。
- **密钥轮换。** `portal_keys` 可以写多把密钥；新旧密钥并存的时间超过一个凭证有效期即可。
- 没有配置 `access.networks` 的中继是开放的，并打印警告。中继不接受凭证时，客户端照常尝试预约；开放中继会直接放行。
- **格式。** 线上的凭证是 `{body, sig}`。`body` 是门户写出的 claims JSON（以 base64 承载，任何一层都改不了它的字节），签名覆盖 `canon("sharetoken/relay-pass/v1", body)`。中继只解析认识的字段，不认识的忽略，所以门户增加字段不用升版本，也不用先升级中继。只有中继依赖的字段改变含义时才升版本；那时要先让中继同时接受两个版本，门户再开始签新的。
- **`client` 只用于统计。** 签名只证明「门户签发时客户端是这么说的」。改过的客户端可以自报任何值，所以中继从不拿它做准入。中继把它和 libp2p identify 里的 agent（`sharetoken-client/1.0.0 (darwin/arm64)`）比对，不一致的计入 `network_client_mismatches`。

### 2.10 IPv6 与双栈

- **默认双栈监听。** 客户端监听 `/ip4/0.0.0.0` 和 `/ip6/::`（TCP 与 QUIC），中继同样。在没有 IPv6 的机器上，IPv6 监听失败后被跳过（libp2p 只要有一个监听成功就能启动）。显式给出的 `-listen` 按原样使用。
- **为什么客户端也监听 IPv6。** 家庭宽带的 IPv6 通常是公网地址，没有 NAT。卖家公告了 IPv6 地址后，同样有 IPv6 的买家可以直连，不占中继带宽。只能主动连出的机器就只能走中继。
- **一个中继登记一条、多个地址。** 中继地址可以是同一 peer ID 的多个 multiaddr，用逗号连接（`/ip4/…/p2p/X,/ip6/…/p2p/X`）。`p2p.ParseRelay` 把它们合成一个 `AddrInfo`；测速、预约、电路地址都覆盖全部地址，每个用户用自己能连的那个。门户按 peer ID 每个中继只登记一条，并做规范化。中继启动时打印一行可以直接粘贴的 `portal relay add '…'`（优先用公告的域名，否则每个地址族取公网地址），没有 IPv6 地址时会提示一句。
- **双栈中继是桥。** 只有 IPv4 的用户和只有 IPv6 的用户可以经同一个双栈中继互相买卖。对中继来说两边各是一条普通连接，Noise 端到端加密不变。

### 2.11 本地 API

运行中的客户端在同一个回环地址上提供 OpenAI 兼容端点（`/v1`）和管理 API（`/api/v1`，见 [guide.md](guide.md)），正式版本是 `127.0.0.1:9787`。CLI 的 `status`、`models`、`relays`、`share` 命令都走这套 API，桌面应用（[desktop.md](desktop.md)）也一样。桌面应用运行的是 `sharetoken daemon`：同一个服务，只是没有人登录时它也在运行，并通过自己的 API 处理登录。

安全措施：

- 只绑定回环地址。客户端把实际地址写进状态目录里的 `run.json`，CLI 据此找到正在运行的客户端。
- **管理 API 需要 key。** 回环地址对本机所有程序和用户开放。客户端每次启动生成一个 key，写进 `run.json`（权限 0600，在 0700 的状态目录里）；`/api/v1/` 要求 `Authorization: Bearer <key>`（`client.requireLocalKey`）。桌面应用使用 daemon 之前先确认它持有这个 key：`GET /api/v1/hello?nonce=…` 返回 HMAC-SHA256(key, 地址, nonce)（`client.LocalProof`），所以占住端口的其他用户的程序既拿不到页面里的登录验证码，也拿不到 key。OpenAI 端点 `/v1/` 用另一把 key，存在 `apikey.json`（0600），不随启动变化，因为工具会把它存进自己的设置；用户可以关掉它（`client.apiKeyGate`）。
- **拒绝网页的跨域请求。** 带 `Origin`、且来源不是 localhost、回环地址或应用自定义 scheme（`tauri://`、`wails://`）的请求一律 403（`client.guardOrigin`），防止任意网页借用户的浏览器花用户的 credit。监听回环地址时还检查 `Host` 头，防 DNS rebinding。
- `config.json`（含门户 token）和设备私钥 `device.key` 的权限是 0600。状态目录见 [configuration.md](configuration.md)。

### 2.12 门户网页的安全模型

门户的页面编在二进制里一起提供：公开站点（`/`）、用户页面（`/app/`）和管理控制台（`/admin/`）。它们共用 `/api/v1`。

| 威胁 | 对策 |
|---|---|
| 会话被页面里的脚本窃取（XSS） | 会话 token 只放在 **HttpOnly** cookie 里，脚本读不到。cookie 是 `SameSite=Strict`，HTTPS 下带 `Secure` |
| 跨站请求伪造（含登录 CSRF） | cookie 只在请求同时带自定义头 `X-Sharetoken-Web: 1` 时才生效。浏览器带了 `Origin` 或 `Sec-Fetch-Site` 时，来源必须是门户本身。门户不答复 CORS 预检，所以别的站点发不出这种请求。登录和注册端点同样受保护。Bearer 认证不走 cookie，CLI 不受影响 |
| 注入脚本 | 严格的 CSP：`default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; font-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'`。页面没有内联脚本和样式，没有第三方资源，用 `textContent` 构建 DOM，不拼 HTML。另有 `X-Content-Type-Options: nosniff`、`Referrer-Policy: no-referrer`、`X-Frame-Options: DENY` |
| 猜登录验证码、批量注册 | 没有密码。登录验证码 10 分钟有效，可试 5 次；一个邮箱每小时 5 个验证码。错误的验证码按 IP 和按邮箱限流（5 分钟 8 次），注册和对新邮箱的拒绝按 IP 同样限流 |
| 管理员误操作 | 管理员不能禁用自己，也不能撤销自己的管理员权限。破坏性操作（禁用、吊销、退款）在页面上二次确认。所有管理操作都写日志（谁对谁做了什么） |
| 会话长期有效 | 浏览器会话默认 15 天（`-session-ttl`），客户端 token 默认 60 天（`-login-ttl`），都在 token 列表里可见，可以吊销。管理员可以吊销某个用户的全部 token |
| 门户在反向代理后面 | 使用 `-behind-tls-proxy`，或代理发来 `X-Forwarded-Proto: https` 时，cookie 仍标 Secure，Origin 校验按 https 比较 |

**争议裁决**（`POST /api/v1/admin/disputes/{job}/resolve`）由门户进程直接在共用数据库里结算（`coordinator.ResolveDispute`）。它和协调器自己的 finalize、refund 是同一段代码。**服务中的**协调器启动时把手续费率写进 `meta.fee_bps` 供门户读取（`Engine.RecordFeeRate`；运维子命令不会覆盖它）。`st_write_lock` 上的行锁保证两个进程的写事务串行。

**两步验证。** 管理员必须开启，其他人自选，在账户设置里开启（`/api/v1/account/mfa/…`；关闭需要当前动态码，开启和关闭都会发邮件通知）。开启了的账户，邮件验证码只换来一个 5 分钟有效的 challenge，不发任何 token。用户再提交一个 TOTP 码（`POST /api/v1/web/mfa` 或 `/api/v1/mfa`；API 客户端也可以在登录请求里直接带 `totp`）。未绑定的管理员先在网页上完成绑定：服务器生成 20 字节的密钥，以二维码和 base32 文本展示，首个验证码正确之后才写库。所以即使是第一次登录，也不存在没有第二因素的管理员会话。校验接受前后各一个周期，并记录最后接受的步数，同一个码不能重放。错码计入登录限流，一个 challenge 错 5 次即作废。`api_tokens.mfa` 记录 token 是否来自两步登录，`admin` 中间件要求它为真：用户成为管理员之前签发的 token 没有管理权限，必须重新登录。密钥存在 `users.mfa_secret` 里，用 AES-GCM 加密，加密密钥由门户签名私钥（`portal-pass.key`）派生，设置了 `keys.mfa_key_file` 时则用它；所以拿到数据库副本不等于拿到验证器密钥。没有这把密钥的门户进程（比如运维命令）无法让开启了两步验证的人登录，但 `portal mfa-reset` 不需要它。

### 2.13 配对：把买家绑定到它的卖家

卖家可以把一批买家留给自己，买家也可以限定提示词发给谁，都不需要另建一个网络。卖家生成配对码（`internal/accounts/pairing.go`）：80 位随机数，写成 16 个 base32 字符，默认有效一天，最长 30 天，可以不限买家数，也可以限定人数。买家在门户兑换后，两个账户就配对了。一个卖家可以有很多买家（最多 1000 个），一个买家最多配对 3 个卖家。任何一方都可以解除配对；撤销配对码不影响用它建立的配对。

执行这条规则的是协调器，不是客户端。给一个节点签发 Ticket 或列出模型时，协调器查出它的账户配对了哪些卖家；只要有，其他账户的节点一律排除，请求指定了节点也一样。没有配对的买家照常撮合，卖家也照常服务没有和它配对的买家，除非它开启了私有模式：这时只有和它配对的买家会被撮合过来，也只有他们能看到它的模型。私有卖家照样接受抽检：抽检身份和配对的买家一样能被撮合到它。每次请求都从数据库读配对，所以改动在下一次请求生效。

配对发生在账户之间：买家的所有设备都受它约束，卖家的所有设备都为它服务。门户只给双方看对方账户名的打码形式，因为配对码可能传到卖家意料之外的人手里。输错配对码和登录失败一样会被限流。

### 2.14 共享伙伴：免费使用彼此的节点

除了买卖，还有第二种共享方式：两个用户结成共享伙伴，各自免费使用对方的节点（`internal/accounts/sharing.go`、`internal/coordinator/sharing.go`）。一方生成邀请，邀请码的形式和配对码相同，默认有效一天，最长 7 天，只能让一个人加入；另一方在门户用它加入。一个用户最多有 5 个伙伴、10 个未用的邀请。

- **数据模型。** 伙伴关系以共享组保存：`share_groups`（带 `max_members`）、`share_members` 和 `share_invites`。目前每个邀请建一个两人组，和用户同在一个组里的人都是它的伙伴。表结构已经能容纳更多成员的共享池：组有自己的人数上限，邀请可以指向已有的组（`share_invites.group_id`），所以以后加共享池不需要迁移。`share_settings` 保存每个用户的设置；从没改过设置的用户全部共享，并在伙伴不可用时回到市场。
- **撮合顺序。** 有伙伴的用户请求模型时，`IssueTicket` 先在伙伴的在线节点里找。有节点能接，Ticket 就标为 `Shared`。只有都接不了（伙伴没提供这个模型、都离线或忙、或超出了他们共享的额度）时，请求才进入市场，照常付费；如果用户开启了"只用伙伴"，则请求失败，返回无供给或容量不足。不论用户有没有配对，伙伴都会参与撮合；私有模式只针对付费买家。模型列表在 `ModelSummary.SharedNodes` 里统计伙伴节点数；开启"只用伙伴"时只列出伙伴和用户自己的模型。
- **免费结算。** `Ticket.Shared` 是签名字段，双方都改不了。共享任务不冻结、不扣费、不收平台费；它的预留只用来限制输出长度，`protocol.TicketCost` 对它返回 0，任务记录标为 `jobs.shared`。回执、哈希和抽检和其他任务一样。`JobInfo` 和管理端的任务列表带 `Shared`。共享任务不改动节点的记录（成功、失败、败诉次数和信誉）：它们不花钱，否则同一个人的两个账户可以借它刷市场信誉，伙伴也可以零成本拉低它。
- **额度。** 每个用户限定自己给出的部分，所有伙伴合计。`daily_limit_tokens` 限制它的节点在滚动 24 小时内为伙伴处理的 token 数（提示词加输出），按已结算的共享任务计，所以达到额度时还在运行的任务会让它略微超出。`share_pct` 是每个节点 `MaxConcurrency` 并发槽位中伙伴任务可以占用的比例，至少一个槽位；0 表示暂停共享。超出任一额度后，伙伴到市场购买，可能仍被撮合到同样的节点，按其价格付费。

和配对一样，执行规则的是协调器，每次请求都从数据库读伙伴和设置。门户给双方看对方账户名的打码形式，以及过去 24 小时双方互相使用的 token 数；输错邀请码和输错配对码一样会被限流。任何一方都可以随时结束伙伴关系。管理后台可以查看共享组（`GET /api/v1/admin/share-groups`）。

### 2.15 数据面上的一单

买家和节点之间讲 `/sharetoken/infer/1.0.2`（`internal/protocol` 的 `frame.go`、`codec.go`；`internal/node/stream.go`；`internal/buyer`）。

- **请求。** 一个 JSON 的 `infer` 帧带上 Ticket、买家这一单的密钥和封装后请求的长度，随后是 1 MiB 一帧的二进制帧，装着封装后的请求。买家在协调器选节点的同时就压缩请求（带预置字典的 DEFLATE），并补齐到分档长度；封装要等拿到 Ticket。本地 API 接受最大 32 MiB 的请求 JSON，和多数提供 Claude 的云平台的 Messages 上限一致（Anthropic 自家 API 是 128 MB）；base64 图片压不动，这类请求基本按原大小传输。节点先验完 Ticket 才读请求，每一帧给 30 秒，而不是整个上传 30 秒。
- **响应。** 二进制数据帧承载一条压缩流，里面是模型服务器的 SSE 内容。节点最多每 50 毫秒切出一帧（`node.FrameInterval`；中继的 CPU 花在帧数上，不在字节上）：第一段内容立即发出，挨得很近的内容共用一帧。每帧补齐后用这一单的响应流密钥加密，序号作 nonce。回执、确认、拒签和错误仍是 JSON 帧。OpenAI 格式服务器的一个 token，在流上约占 55 字节；原来用 JSON、base64、每块换一把临时密钥，约占 455 字节。
- **静默。** 模型不出声时，节点每 10 秒发一个进度帧（`node.Config.Heartbeat`），所以买家的超时（等第一帧和帧间都是 30 秒）只会抓到停止工作的节点或链路，不会误杀思考几分钟的推理模型。进度帧也不会让卡死的模型服务器永远占着：连续 5 分钟没有数据（`buyer.Client.MaxSilence`），买家放弃这一单；如果还没有内容交给调用方，就换一个节点。
- **停止和续传。** 调用方停止（HTTP 客户端断开）时，买家发 `cancel` 再关闭流：节点立即停止模型，按已交付的部分计费。流的其他中断（中继重启、网络切换、30 秒没收到任何东西）不算这一单结束：节点让模型继续运行，把封装好的帧保留 60 秒（最多 8 MiB，`node.Config.ResumeWindow`）；买家在 45 秒内断开旧连接、重新拨号（经另一个中继或直连），发 `resume` 并带上它缺的第一帧的序号。节点核对新流来自 Ticket 上的买家，从那一帧开始重发，再接上实时输出和回执。每一帧只在第一次写出时计入回执的哈希，和买家那边一样，所以回执仍然对得上。买家没有回来，就和以前一样按它收到的帧计费。只有新流带来过帧之后，买家才会再次续传：续传成功之后又一声不响的节点，按卡住的节点处理，换节点。
- **会话粘性。** 买家按会话（请求的 `session_id`，没有时用模型名加前两条消息）记住上次服务它的节点，保留 30 分钟，下次请求时再要这个节点（Ticket 请求里的 `prefer_node`）。只要这个节点是候选（在线、没满、在买家的价格上限内），协调器就选它，即使别的节点打分更高：连接是热的（这时往往已经是直连），模型服务器的 prompt 缓存里也有这段对话。这个偏好除了这一个字段，不会离开买家的机器。
- **耗时。** 每一单都记录 Ticket、拨号、上传、首字节的耗时，是否经中继（以及经哪个中继）、是否复用了连接、帧数和字节数（`buyer.Timing`）。客户端写进日志，本地 API 通过 `X-Sharetoken-Timing` 和流式响应结尾的注释行返回。

## 3. 数据库

门户、协调器和邮件服务共用一个 MySQL 8（InnoDB）数据库（`internal/mysqldb`）。用 `-db mysql://user@host:port/db`（`database.dsn`）指定，可以放在另一台机器上。

| 表 | 写入方 | 读取方 |
|---|---|---|
| `users`、`api_tokens`、`devices` | 门户 | 协调器（用 `devices` 解析账户）、门户 |
| `pairing_codes`、`pairings`、`pairing_private` | 门户 | 协调器（用 `pairings` 和 `pairing_private` 撮合）、门户 |
| `share_groups`、`share_members`、`share_invites`、`share_settings` | 门户 | 协调器（用 `share_members` 和 `share_settings` 撮合）、门户 |
| `relays` | 门户（登记）、协调器（可达状态） | 门户、协调器 |
| `ledger_accounts`、`ledger_holds`、`ledger_entries` | 门户（发放与扣减）、协调器（冻结、结算、退款） | 两边 |
| `jobs`（`shared` 标记共享伙伴之间的任务）、`nodes`、`audits`、`peers`、`meta` | 协调器 | 门户（账单、用量、协调器 ID） |
| `portal_settings`、`admin_audit_log`、`invite_codes`、`relay_samples`、`feedback` | 门户 | 门户 |
| `mail_outbox`、`mail_inbox`、`mail_codes`、`mail_status` | 门户、邮件服务 | 门户、邮件服务（[mail.md](mail.md)） |
| `schema_migrations` | 执行迁移的那个进程 | 所有进程 |

- **迁移。** 表结构是一组编号迁移，分三个组件（`core`、`portal`、`mail`）。谁先启动谁执行，其他进程等待。规则见 [versioning.md](versioning.md)。
- **单写者。** 所有写事务都经过 `mysqldb.Begin`，它先锁住 `st_write_lock` 的唯一一行。门户和协调器的写入因此跨进程严格串行，事务里「读出再写回」的代码才是正确的。只读事务（门户的 `ledgerRead`）不取这把锁。
- **upsert。** 「先 UPDATE，没匹配到再 INSERT」。连接开启 `CLIENT_FOUND_ROWS`，所以 RowsAffected 按匹配的行计数。
- **排序规则。** `utf8mb4_bin`：字符串按字节比较。

## 4. 威胁与防御

完整的威胁模型和系统给出的保证见 [security.md](security.md)。下表把每个威胁对应到代码里实现防御的位置。

| 威胁 | 防御 | 代码 |
|---|---|---|
| 中继读取控制面或推理内容 | 控制面和数据面都是 Noise 端到端；推理内容另外再用 box 加密 | `internal/p2p`、`internal/box` |
| 中继宕机 | 用户自动切换中继；卖家同时在多个中继上预约；协调器在所有中继上预约 | `client.Agent.maintain`、`p2p.Reserver` |
| 有人绕过中继直连协调器 | 协调器不监听任何地址 | `cmd/coordinator`（`DialOnly`） |
| 把别人的节点绑到自己账户 | 绑定需要设备签名，并在时间窗内；已绑定的设备不能被抢 | `accounts.VerifyBind`、`accounts.Store.BindDevice` |
| 同一用户多设备自买自卖 | 撮合按账户排除 | `coordinator.Engine.IssueTicket` |
| token 被盗 | token 可以随时吊销；客户端 token 60 天、浏览器会话 15 天过期；只存它的 SHA-256 | `internal/accounts`、`internal/portal` |
| 猜登录验证码 | 没有密码。6 位验证码 10 分钟有效、每个可试 5 次，一个邮箱每小时 5 个；错误的验证码按 IP 和按邮箱限流（5 分钟 8 次，之后 429）。邮箱就是账户的钥匙，两步验证再加一道保护 | `internal/mailer/codes.go`、`portal.Server.throttled` |
| 恶意网页调用本地端点 | Origin 检查 | `client.guardOrigin` |
| 本机其他程序或用户调用本地管理 API，或冒充 daemon | `run.json`（0600）里的 key；桌面应用使用 daemon 前先核对它的 HMAC 证明 | `client.requireLocalKey`、`client.LocalProof`、`gui/supervisor.go` |
| 拿了 Ticket 不来连，占满节点名额 | 未开始的单在 Ticket 过期 30 秒后释放；账户有 5 张闲置票时不再发新票 | `coordinator.Engine.MarkStarted`、`coordinator.Engine.Tick` |
| 陌生人占用中继的预约名额 | 预约需要门户签发的短期凭证；按用户限制设备数 | `internal/relaypass`、`internal/relay/admission.go` |
| 窃听或篡改门户通信（登录验证码、token） | 默认 HTTPS；客户端 pin 自签名证书的指纹；拒绝把机密明文发往远端 | `internal/portal/tls.go`、`client.NewPortalClient` |
| 被禁用的用户继续交易 | 协调器每次请求都查用户状态；token 立即失效 | `accounts.DeviceAccount` |
| 网页会话被窃取、跨站请求伪造、注入脚本 | HttpOnly + SameSite cookie；自定义头 + Origin 校验；严格 CSP、无内联代码 | `internal/portal/web.go`（§2.12） |
| 管理员邮箱被别人读到 | 管理员强制 TOTP；没有第二因素的 token 进不了 `/admin/*`；错码限流、challenge 限次、验证码不能重放；密钥加密存放 | `internal/portal/mfa.go`、`internal/totp`（§2.12） |
| 陌生人用超大帧或海量流耗尽中继或协调器的内存 | 帧的缓冲区随实际到达的数据增长，不按声明的长度预分配。流处理器先向 libp2p 资源管理器预留帧大小，超出每流（16 MiB）、每 peer 或全局限额（内存的四分之一；中继是四分之三，`limits.memory`）就拒绝这一帧。只含几个字段的帧（凭证、ack）上限 64 KiB。枢纽进程只放宽随用户数增长的计数（全局连接、流、文件描述符；circuit、ping 等内建服务），其余沿用 libp2p 按机器规模缩放的默认值。用户端每个 peer 允许 8 个并发 ping（默认 3 个），免得测速与链路监控重叠时把中继误判为不可达 | `protocol.RecvStream`、`p2p.HubLimits`、`p2p.ClientLimits` |

## 5. 诚实的边界

- **经中继的连接不设准入。** 任何人都能经中继连到持有预约的 peer。对方认证后会拒绝，但中继的带宽已经花掉了。中继的流量只受总量限额约束。
- **拿票不来连仍有短暂的代价。** 每张这样的票最多让节点空等 1.5 分钟；每个账户的闲置票上限限制了总数。
- **自签名证书依赖首次确认。** 用户必须把指纹与运维公布的核对。有域名时更推荐 CA 证书。没有内置的 ACME 客户端：可以让 `-tls-cert` 指向 certbot 生成的证书，或者把门户放在 Caddy 或 nginx 后面。
- **协调器是单实例。** 账本由它和门户记录。
- **用量统计只覆盖保留期内的作业**（默认 30 天）。钱的历史在账本里永久保留。
- **抽检经费从平台手续费账户预支。** 抽检进行中该账户可能为负；结束后未用完的余额退回。
- 没有 TEE 时，节点的运营者能看到 prompt。抽检是概率性的。
