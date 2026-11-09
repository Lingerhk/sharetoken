# 安全审计（2026-11-09）

> 对整个仓库（门户、客户端、P2P / 中继 / 协调器、邮件、TEE 证明与账本、部署）做的一次代码级审计。六条高危和邮件、门户的主要中危项逐条对照代码核实过；其余是分模块审计的结论，修复前再读一遍代码。设计意图见 [design-v2.md](design-v2.md) §2、§4；「状态」列记录修复进度，修一条改一条。

## 高危

| # | 问题 | 位置 | 状态 |
|---|---|---|---|
| 1 | `sharetoken up` 模式的本地管理 API 对任意 localhost 页面和浏览器扩展开放：`Agent.Handler` 只套了 `guardOrigin`，缺少 daemon 模式的 `guardAPIOrigin`；`localOrigin` 放行所有 localhost 页面和任何非 http scheme。恶意页面可 `PUT /api/v1/share` 把上游改成攻击者服务器、把 `upstream_key_env` 指向 `OPENAI_API_KEY` 等变量，之后节点把该变量当 Bearer 发给攻击者，并把其他买家的明文 prompt 转发过去；还能读账户和账本、停止共享、经 `share/detect` 做 SSRF | `client/api.go` `Handler`、`localOrigin`；`client/daemon.go` `guardAPIOrigin` | **未修**。修法：`Agent.Handler` 对 `/api/v1/` 同样套 `guardAPIOrigin`；up 模式拒绝非 loopback 监听；`run.json` 带随机 token，本地 API 要求携带 |
| 2 | 中继与协调器可被无账户的远端打到内存耗尽：`Recv` 读到长度头就 `make([]byte, n)`（上限 8 MiB），枢纽进程的资源管理器是 `InfiniteLimits`，一条连接开几千个流各发一个 8 MiB 长度头然后停住即可 | `protocol/frame.go`、`p2p/host.go` | **已修** 390c240：帧按到达数据增长，`RecvStream` 先向流 scope 预留内存，`HubLimits`/`ClientLimits` 替换无限制 |
| 3 | 审计者可被卖家识别：审计 host 没有 `Program`，libp2p identify 发默认 UserAgent，真实买家是 `sharetoken-client/<ver>`；固定 prompt 模板、固定 `max_tokens`、固定预算是辅助指纹。节点可只对审计者诚实 | `audit/audit.go`、`p2p/host.go` | **未修**。修法：审计 host 用 `Program: "client"` 并与客户端相同的 libp2p 选项；prompt 从真实分布采样 |
| 4 | 争议裁决把解密后的 prompt 和回复作为 `messages` 发给参考模型，而生产参考模型是 DeepSeek 和 gpt-4o-mini：违背「prompt 不出网络」的承诺；恶意节点发坏 chunk 可迫使买家披露，恶意买家反复争议可烧公司 API 额度 | `tokens/tokens.go` `CountText`；`scripts/prod/coordinator.yaml` `references` | **未修**。修法：本地 tokenizer 或本地参考模型；若保留则在 UI 明示 |
| 5 | 本机工作树里的生产级机密是 0644：`config.env`（GoDaddy token）、`scripts/upstream_proxy.json` 与 `docs/test_env.txt`（三把公司 API key）、`docs/test-env.secrets.md`（生产门户 root 密码、MySQL 密码）。均未进 git 历史，但任何本机进程可读 | Mac 工作树 | **未修**（零代码）：`chmod 600`，轮换 GoDaddy token 和三把 key，prod root 绑定 TOTP |
| 6 | 门户、协调器、邮件服务以持有全部机密的同一账号（免密 sudo、docker 组）运行，单元无加固；门户一个 RCE 即可冒充协调器、随意发额度、签 DKIM、接管 DNS、经 docker 取 root | `scripts/prod/*.service`、`scripts/prod/deploy.sh` | **已修** 10fb298，dev 和 prod 都已切换，见 [server-layout.md](server-layout.md) |

## 中危

| # | 问题 | 位置 | 状态 |
|---|---|---|---|
| 7 | 本地 API 完全无鉴权：同机其他 OS 用户或任意本地进程不带 Origin 就能花 credit、登出、改共享配置 | `client/daemon.go`、`client/api.go` | 未修（与 #1 一起：`run.json` 随机 token） |
| 8 | 任何浏览器扩展和 localhost 页面可花费 credit（`/v1/chat/completions`，设计有意放行），无速率或花费上限 | `client/api.go` `localOrigin` | 未修，需产品决策 |
| 9 | `sharetoken up -http 0.0.0.0:…` 把无鉴权 API 暴露到局域网，且非 loopback 时跳过 Host 校验 | `client/agent.go`、`client/api.go` | 未修 |
| 10 | cookie 会话可铸造永久 Bearer token 并继承管理员 MFA 标记（`ttl_hours: 0`）；HttpOnly 的意义被同源脚本绕空 | `portal/portal.go` `handleNewToken` | 未修。修法：cookie 会话创建 token 要求重新输入密码或 TOTP，且不继承 `mfa` |
| 11 | 邮箱子地址（`me+1@`）刷注册赠送额度：不做 plus-addressing 归一化，生产开放注册且赠 5 credit | `accounts/accounts.go` `NormalizeEmail` | 未修 |
| 12 | 收件邮件的 DMARC 对齐域名从未加引号的显示名字符串反推，`"<x@attacker>" <ceo@sharetoken.app>` 会把 DKIM 对齐到攻击者域而显示「已验证」 | `mailer/inbound.go` `addressDomain` | 未修。修法：从 `AddressList("From")[0].Address` 取域名 |
| 13 | 验证码限流按 IP 键，IPv6 轮换可打满全站 500/小时上限（注册不可用，或借门户向任意邮箱发品牌邮件）；`VerifyCode` 的尝试计数非原子 | `mailer/codes.go` | 未修。修法：IPv6 按 /64 聚合；先 `UPDATE … attempts+1 WHERE attempts<5` 再比对 |
| 14 | 未认证的 `withdraw` 造成 `e.withdrawn` 无界增长，`IssuedAt` 设成 MaxInt64 永不清理 | `coordinator/engine.go` `WithdrawOffer` | 未修。修法：先 `accountOf`，拒绝未来时间戳 |
| 15 | 协调器控制面在认证前无限流，每个请求都拿全局锁加数据库写锁；有 TEE 策略时 `verifyQuote` 在认证前执行并可触发外网 CRL 请求 | `coordinator/engine.go` | 未修 |
| 16 | 争议 griefing：买家无需证据即可 `dispute` 冻结节点收款；反过来买家中途取消时客户端既不 ack 也不 dispute，节点可按预留上限虚报 | `coordinator/engine.go`、`buyer/client.go` | 未修 |
| 17 | TEE 验证只查签名链：无最低 TCB、无吊销、生产 `online_check: false`，quote 无 nonce 且按内容缓存 | `attest/sevsnp`、`attest/tdx` | 未修。修法：SNP 加 `validate.SnpAttestation` 与 `MinimumTCB`；TDX 开 `GetCollateral` |
| 18 | 账本无操作者字段，管理员发额度无上限且与系统赠送不可区分；`Reconcile` 只检测不平衡的篡改 | `ledger/ledger.go`、`portal/portal.go` `AdjustCredit` | 未修 |
| 19 | dev 环境（公网开放注册）与生产共用同一 MySQL 实例 | `scripts/dev/deploy.sh` | 未修 |
| 20 | mailserver 与门户同用户同目录，「受限数据库账号」在文件系统层被抵消 | 部署 | **已修**（#6） |

## 低危

- 登录错误区分「已禁用」与「密码错误」（`portal/portal.go`）。
- 限流 map 以无上限用户名为键，满 10 万键整体清空；`X-Forwarded-Proto` 对任意来源采信；`trusted_proxies` 配错静默失效。
- 管理员重置密码不通知用户；API 默认不吊销 token，与设计文档不符。
- 上游 4xx 正文透传给买家并写日志（`node/node.go`），可能泄露真实供应商和模型名。
- 门户客户端无 `CheckRedirect`；证书 pin 的是叶子证书而非 SPKI。
- 验证码写在邮件主题里；收件箱填满后所有收信 452，只能手工清理。
- `pprof_listen` 未强制 loopback（`cmd/coordinator`）。
- MySQL 层无 TLS 途径；备份时 root 密码曾出现在 `docker exec` 参数里（**已修**，#6 的 `st-mysql-root`）。
- `research_notes/` 既未被 ignore 又会被打进源码包发到共用服务器。
- `config.Secret` 对 0644 的机密文件只告警不拒绝。
- 依赖：`govulncheck` 报出 pion `dtls` 和 `stun` 两个 panic 类漏洞（经 libp2p 间接引入），升级即可。

## 已确认正确的部分

全部 SQL 参数化；token 只存哈希、argon2id、等时比较；Web 的自定义头加 Origin 校验、严格 CSP、无 innerHTML；MFA challenge、步数防重放、密钥加密存放；relay-pass 绑定 peer 且域分隔签名；box 用 X25519 加 HKDF 加 AES-GCM，AAD 绑定 job 和序号；收据链覆盖截断重排；金额运算防溢出；节点侧字段白名单，买家控不了上游 URL 和头；secfile 的 `O_EXCL` 加 Windows ACL；邮件服务不是开放中继、头注入已处理、验证码 CSPRNG 加盐哈希；TEE 的 `ReportData` 正确绑定两把公钥，无测试根进入生产路径。

## 建议顺序

1. 零代码：#5（chmod、轮换、prod root 绑 TOTP）。
2. #1、#7、#9：`guardAPIOrigin` 下沉到 `Agent`，up 模式拒绝非 loopback，本地 API 加 token。
3. #3：审计 host 伪装成普通客户端。
4. #4：争议裁决不把 prompt 发给商业 API。
5. 其余中危按编号。
