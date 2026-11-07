# sharetoken v2 验证报告

> 版本 v2（2026-11-07，分支 `arch-v2`）。设计见 [design-v2.md](design-v2.md)。

## 1. 自动化测试

`go test -race ./...` 全部通过。v2 新增/改动的测试：

| 测试 | 覆盖 |
|---|---|
| `e2e/arch_test.go` `TestUsersTradeThroughRelays` | 完整拓扑在一个进程里：2 个中继、**不监听**的协调器、门户（与协调器经两个独立连接共用同一个 SQLite 文件）、两个用户客户端互为买卖；断言到协调器的所有连接都是中继连接；门户里两人的余额、用量、设备、平台手续费、账本回放；杀掉 bob 使用的中继后 bob 自动切换、alice 经第二个预约仍可被访问，请求继续成功 |
| `TestUnboundAndDisabledUsersAreRefused` | 未绑定的 peer 经中继访问协调器被拒（`unauthorized`）；管理员禁用用户后其客户端的请求被拒 |
| `TestLocalAPIRefusesWebPages` | 本地端点拒绝带外部 `Origin` 的请求 |
| `coordinator/accounts_test.go` | 未绑定设备被拒；同一用户多设备共用账户；用户不会匹配到自己的任一节点；作业历史与用量；禁用用户 |
| `TestAbandonReleasesTheHoldAtOnce`、`TestFailoverBeforeFirstByte` | 节点放弃失败的单：冻结额立即退回、并发名额释放、幂等、已结算的单不能放弃 |
| `TestMigrateUpgradesAV1Database` | v1 数据库原地升级，旧作业仍付给原 peer 账户 |
| `portal/portal_test.go` | 注册/登录/限流、token 创建列举吊销、登出、设备绑定（伪造签名、过期签名、抢绑都被拒）、管理员发放与扣减（不能扣成负数）、账单、禁用、中继登记、`/network`、平台汇总 |
| `p2p/relays_test.go` | 只监听回环的主机经中继预约后可被访问且连接是中继连接；预约中的中继每 30 秒 ping 一次、往返时间出现在状态里；断开中继后自动重新预约；中继测速排序（可达在前、不可达/坏地址在后） |
| `client/client_test.go` | 中继选择（最快、优先协调器可达、防抖、故障切换）、Origin 判定、配置读写与校验 |
| 原有全部测试 | v1 的收据、证据裁决、抽检、TEE、账本测试改用 `OpenAccounts` 后原样通过 |

## 2. 本机演示（真实模型）

`scripts/demo-local.sh`，Ollama gemma4:e4b，全部在 127.0.0.1：

| 步骤 | 结果 |
|---|---|
| 门户 `/network` | 协调器启动后立即显示两个中继 `ok:true`（预约状态变化即写库） |
| bob 的客户端 | 测得 relay-b 0.3 ms、relay-a 0.4 ms，经 relay-b 连上协调器（relayed） |
| bob 请求，carol（更便宜、虚报 ×100）先被匹配 | 买家拒签 → 证据裁决「billed 29 + 3000，实际 29 + 29」→ 退款；自动切到 alice，`X-Sharetoken-Verified: true` |
| 抽检 | alice 相似度 1.00；carol 连续两次不符，信誉 200，暂停接单 |
| alice 买 | 她自己的节点不会匹配给她，carol 已暂停 → `no_offers` |
| 杀掉 bob 当前用的中继 | bob 切换到另一个中继，流式请求成功（`verified=true`） |
| 账户 | alice 卖出 4 单（含抽检单）赚 91；bob 买 2 单花 67；carol 0；`books_balance: true` |
| 隐私 | 中继、门户、协调器、三个客户端的日志里都没有 prompt 原文 |

## 3. 多机实测

| 机器 | 网络位置 | 运行 |
|---|---|---|
| 云服务器（CentOS 7，2 GB） | 公网 119.29.250.173，安全组只开 8090–8095 | relay-tecs（:8091）、门户（:8092）、协调器（与门户共用 `v2.db`，**无监听端口**）、用户 bob（只买） |
| armbian（aarch64，2 GB） | 家庭局域网，ZeroTier 192.168.8.91 | relay-home（只监听 ZeroTier 地址）、用户 carol（虚报用量的卖家，只绑定局域网地址） |
| Mac（Ollama gemma4:e4b） | 家庭局域网 | 用户 alice（诚实卖家 + 买家，只绑定局域网地址） |

协调器的参考模型和 carol 的上游都经 SSH 反向隧道接到 Mac 的 Ollama。用户的 libp2p 只绑局域网或回环地址、关闭 UPnP，保证跨机流量必须走中继。

| 验证项 | 结果 |
|---|---|
| 协调器只经中继可达 | 启动后在两个中继上都拿到预约；门户 `/network` 两个都 `ok:true` |
| **按网络质量选中继** | alice（Mac）：relay-home **4.6 ms** vs relay-tecs 356.7 ms → 选 home；carol（armbian）：home 0.4 ms vs tecs 363.2 ms → 选 home；bob（云）：relay-tecs **1.1 ms** vs home 336.7 ms → 选 tecs。三人到协调器的连接都是 `relayed` |
| 卖家多中继预约 | alice、carol 各在两个中继上预约，Offer 同时通告两条中继地址 |
| 跨公网交易 | bob（云）请求 → 先匹配 carol → 拒签（报 23,700，实际 232）→ 证据退款 → 切到 alice（Mac，经中继）→ 已验证回答，总耗时 31 s（含两次真实生成） |
| 自己不买自己 | alice 买时只剩 carol 可选，被抓后返回 `no healthy node left`，未被扣费 |
| 中继流量 | relay-tecs：5 条线路、463 KB；relay-home：2 条线路、31 KB；无拒绝 |
| **中继宕机切换** | 杀掉 relay-home 后，alice 在约 7 秒内检测到协调器不可达，刷新门户列表、切到 relay-tecs，共享预约留在 relay-tecs，继续接单 |
| 流式请求遇到作弊节点 | 内容已发出无法换节点，末尾补 `receipt_rejected` 错误事件，钱退回（符合设计） |
| 抽检 | alice 通过（相似度 1.00）；carol 不符，信誉降到 200 |
| 账户 | alice 卖 2 单赚 107；bob 买 1 单花 92；carol 0（被抓的单全部退款）；alice 那次被拒的买单状态 `refunded`，未扣费 |
| 账本 | `coordinator check` 回放一致；`portal overview` `books_balance: true` |
| 隐私 | 三台机器上中继、门户、协调器、客户端的日志都没有 prompt 原文 |

## 4. 本轮发现并已修复的问题

1. **门户报告中继可达有延迟**：协调器原先每 15 秒才把预约状态写库，刚启动时门户返回 `ok:false`，客户端可能因此避开正常的中继。改为预约状态一变就写库；客户端重测和故障切换前也会刷新门户列表。
2. **CLI 连错客户端**：`up -http` 改了端口但 `status` 仍连默认端口。`up` 现在把实际地址写进 `run.json`，CLI 优先使用。
3. **节点失败的单冻结 17 分钟、占用名额**（v1 已知问题 3.2 的一半）：新增 `abandon`，节点在交付前失败时立即释放（design-v2 §2.6）。演示中卖家上游故障时，这个问题曾让买家 0.2 credit 被冻结、卖家被挤出撮合。
4. **作业历史显示已退款单的费用**：已退款/已释放的单费用显示为 0。
5. **部署注意**：远程用 `ssh host 'setsid nohup … &'` 启动常驻进程时，ssh 会话可能不退出，需要给本地 ssh 加超时（进程不受影响）。

## 5. 后续修复：拿票不来连、门户明文

| 测试 | 结果 |
|---|---|
| `TestTicketsNeverStartedAreReleasedEarly` | 节点满载时，未开始的票在 TTL + 宽限期后释放、退款、节点不扣分；已开始的单保持冻结；2 张闲置票后拒发第 3 张（`busy`）；买家不能冒充节点报告开始；迟到的开始报告返回 `conflict` |
| `TestUnsettledHoldIsReleased`（改） | 已开始但没结算的单，Ticket 过期后仍保持冻结，到结算截止才释放并记节点失败 |
| `e2e` `TestNoShowTicketFreesTheNodeSoon` | 真实 libp2p：不来连的买家占满节点 2 个名额 → 别的买家 `no_offers` → 约 2.5 秒（测试参数）后释放，正常买单成功，节点失败数 0 |
| `TestSelfSignedCertIsStable` | 自签名证书重启后不变，私钥 0600 |
| `client` `TestPortalPinning` | HTTPS 门户：pin 正确能注册；不 pin、pin 到别的证书都被拒；探测得到正确指纹且判定为不受信任；明文远端 URL 识别 |
| 命令行实测 | 门户监听局域网地址默认 HTTPS 并打印指纹；非交互无 pin → 显示指纹并要求 `-portal-pin`；错 pin → 拒绝并提示中间人风险；对的 pin → 注册成功，指纹存入配置，`balance` 复用；`http://192.168.6.125` → 拒绝发送密码；重启后指纹不变 |
| `scripts/demo-local.sh` | 真实模型全流程再跑一遍，全部通过；生成耗时较长的单没有被误释放 |

## 6. 中继准入（门户签发的短期凭证）

| 测试 | 结果 |
|---|---|
| `relaypass` `TestPassVerification` | 正确凭证通过；换 peer、过期、别的门户签的、改了账户或延长有效期、签发时间在未来的都被拒；公钥格式往返 |
| `relay` `TestRelayAdmitsOnlyPassHolders` | 真实 libp2p：无凭证不能预约；白名单里的协调器不需要凭证；别的门户签的 / 签给别的 peer 的 / 过期的凭证被拒；同一账户第 3 台设备超限被拒、其他账户不受影响、有设备离开后名额空出；统计 4 通过 3 拒绝 |
| `relay` `TestRelayWithoutKeysIsOpen` | 不配门户公钥时仍为开放中继 |
| `portal` `TestRelayPassEndpoint` | 未绑定设备 403；绑定后签发的凭证能验证、账户正确；别人的设备 403；未启用凭证时 404 |
| `e2e` `TestRelaysAdmitOnlyRegisteredDevices` | 完整拓扑下中继要求凭证：卖家自动领凭证并预约成功；无账户的节点预约被拒；门户不给不属于调用者的设备签发；被禁用的用户领不到凭证 |
| `e2e` 原有拓扑测试 | 中继改为要求凭证后，互为买卖与中继宕机切换（包括在幸存中继上重新预约）全部照常通过 |
| `scripts/demo-local.sh` | 中继带 `-portal-key/-allow-peer` 启动：relay-a 上 3 个预约（协调器 + 两个卖家），2 张凭证通过 0 拒绝；全流程通过 |

## 7. 中继实时链路质量

| 测试 | 结果 |
|---|---|
| `p2p` `TestLinkMonitorWindow` | 10/超时/14/12 ms：平均 12、抖动 3、丢包 25%；窗口滑动后旧样本移出；换中继窗口清空；全丢时报告错误 |
| `p2p` `TestPingRelay` | 真实 libp2p ping 中继得到正 RTT；中继关闭后 ping 失败 |
| `e2e` `TestUsersTradeThroughRelays` | 健康检查期间 `coordinator.link` 积累样本、指向正在用的中继、丢包 0；中继宕机切换后 `link` 跟随新中继重算 |
| `scripts/demo-local.sh` | 杀掉 relay-a 后 `sharetoken status` 显示 `link: 1.5 ms now … over 1 pings`，窗口已切到 relay-b |

## 8. IPv6 / 双栈

| 测试 | 结果 |
|---|---|
| `p2p` `TestParseDualStackRelay` | 「v4,v6」合成一个中继；规范化往返；不同 peer 的地址混在一起被拒；空地址被拒 |
| `p2p` `TestDualStackRelayBridgesIPv4AndIPv6` | 只能拨 IPv6 的卖家经 IPv6 预约，只能拨 IPv4 的买家经 IPv4 连上它（必然经中继），ping 通 |
| `portal` `TestDualStackRelayRegistration` | 双栈地址规范化后登记；同一中继换个地址再登记被拒（按 peer ID 去重）；缺 peer ID 被拒 |
| `client` `TestConfigListensOnIPv6ByDefault` | 旧的仅 IPv4 默认配置升级为双栈；手动指定的不变 |
| `e2e` `TestIPv4AndIPv6OnlyUsersTrade` | 双栈中继下，只有 IPv6 的用户和只有 IPv4 的用户互相买卖成功；各自只用自己的地址族连中继；彼此之间只经中继；都连上协调器 |
| `e2e` 原有拓扑测试 | 中继全部改为双栈登记后照常通过（包括宕机切换） |
| `scripts/demo-local.sh` | 中继监听 127.0.0.1 + ::1 并按打印的命令登记；alice 只监听 IPv6，bob（IPv4）买到 alice 的服务；中继宕机切换正常 |
| 中继二进制（本机） | 带公网 IPv6 的 Mac 上默认双栈启动，打印的登记命令同时包含 v4 和 v6 地址 |

## 9. 门户网页（用户门户 + 管理控制台）

| 测试 | 结果 |
|---|---|
| `portal` `TestWebSessionsAndCSRF` | 不带页面头的登录/注册被拒（登录 CSRF）；注册后 cookie 为 HttpOnly、SameSite=Strict、Path=/、Max-Age=会话时长、明文 HTTP 下无 Secure，响应体不含 token；只有 cookie 没有头 → 403；头正确但 `Origin` 为别的站点、`Origin: null`、`Sec-Fetch-Site: cross-site` → 403；门户自己的 Origin 通过；Bearer 认证不受影响；会话在 token 列表里标记为当前；登出清 cookie 并吊销；改密码可吊销其他 token 而保留当前会话；设备改名只限本人 |
| `TestSecureCookieOverTLS` | HTTPS 下 cookie 带 Secure、响应带 HSTS；https 门户收到 http Origin 视为跨站 |
| `TestPagesAndSecurityHeaders` | `/`、`/admin/`、`/static/*` 正常；`/static/index.html`、不存在的文件、`/nope` 404；`/admin` 301 到 `/admin/`；每个响应都带 CSP、nosniff、X-Frame-Options、Referrer-Policy；HTML 里没有内联脚本/样式/事件属性 |
| `TestRegistrationIsRateLimited` | 同 IP 第 9 次注册 429，登录不受影响 |
| `TestAdminConsoleEndpoints` | 全部管理端点对普通用户 403；用户搜索带总数、余额、设备数、token 数，`%`/`_` 按字面匹配；授予/撤销管理员、不能撤销自己；吊销单个 token、重置密码并吊销全部、吊销全部；管理员解绑设备、用户详情含 token；中继改名/地区与长度校验；作业状态/用户参数校验；账本流水带用户名、账户过滤与分页；账户余额表；总览新增计数 |
| `TestResolveDisputeFromPortal` | 种入两单争议：退款单退回买家冻结额、节点记争议败诉；付款单按 `meta.fee_bps` 扣手续费结算给节点；重复裁决 409、未知作业 404、非法决定 400；账本回放平衡 |
| `coordinator` `TestPortalSideDisputeResolution` | 真实引擎产生的争议，由门户侧 `ResolveDispute` 退款/付款，结果与引擎自身的 `ResolveDispute` 一致（余额、信誉、状态、账本） |
| `coordinator` `TestQueryJobsUsesIndexes`、`TestDisputesAndJournalUseIndexes` | 真实数据（armbian 7.5 万作业、383 MB）下门户曾慢到秒级：`(buyer_acct = ? OR node_acct = ?)` 走多索引 OR 再对该账户全部历史排序，争议列表 `status = 'disputed' OR dispute_outcome != ''` 全表扫描，无筛选列表对全表排序。改为两段按索引顺序各取 limit 行再合并（UNION ALL，引擎禁止自买自卖、账本禁止 debit = credit，故不重复），并补索引 `jobs_by_created`、`jobs_by_status_created`、部分索引 `jobs_judged`；账户 + 状态筛选用 `+status = ?` 防止规划器改走状态索引。测试用 `EXPLAIN QUERY PLAN` 断言每种筛选组合都走预期索引、不出现全表扫描/排序；合成 7.5 万行下全部查询 ≤ 1.2 ms（原 OR 写法 4–28 ms，armbian 上更慢） |
| `TestReadsDoNotNeedWriteLock` | 混沌压测中协调器长期占着写锁，门户只读请求（`/me`、账单、总览）原本也用 `BEGIN IMMEDIATE` 排队等锁，单连接池又把等待串起来：实测 `/me` 10–30 s 后 SQLITE_BUSY，门户重启时 Migrate 等锁 10 s 后直接退出（02:31 起宕机 14 分钟）。修复：只读路径改用只读（deferred）事务，WAL 下不取写锁；token `last_used_at` 的每分钟一次写入用 250 ms 的 context 截止并在内存里记住尝试时间；真正写入遇 SQLITE_BUSY 返回 503 + `Retry-After`；启动迁移遇 SQLITE_BUSY 每 2 s 重试最多 2 分钟（`internal/sqlite.IsBusy`，`TestIsBusy`）。测试从第二个连接持有 `BEGIN IMMEDIATE`，断言 `/me`、`/ledger`、`/jobs`、`/tokens`、`/devices` 全部 200 且远快于 busy 超时，写入得 503，释放锁后写入成功。armbian 压测中部署后 `/me` 0.14 s、其余 ≤ 0.07 s，无 busy 错误 |
| `TestNetworkShowsRelayLatency` | 协调器回写的中继往返时间（`relays.rtt_ms`，新库建表、老库 `AddColumn` 补列）经 `/api/v1/network` 的 `rtt_ms` 暴露；排序为可达且延迟低的在前、未测得的其次、不可达的最后；管理端中继列表同样带 `rtt_ms` |
| `internal/totp` `TestRFC6238Vectors` 等 | RFC 6238 附录 B 测试向量；±1 周期窗口、更早/重放的码被拒、短码被拒、空格忽略；base32 编解码与 otpauth 链接 |
| `portal` `TestAdminLoginRequiresTwoFactor` | 普通用户仅密码即可登录；管理员密码正确只得 `mfa_setup_required` + challenge、无 cookie；绑定 challenge 不能当验码 challenge 用；`setup` 返回二维码（PNG data URI）、otpauth 链接、密钥且重复调用返回同一密钥；错码 401 `bad_code`，正确码建立会话、`/me` 显示已绑定、管理端 200，challenge 用后即废；从两步会话创建的 token 带 `mfa`，直接签发的普通 token 访问管理端 403 `mfa_required`；再次登录得 `mfa_required`，绑定时用过的码重放被拒，下一周期的码通过；API 登录两步（`/api/v1/mfa`）与一步（`totp` 字段）都可，错 `totp` 401；一个 challenge 错 5 次后 `challenge_expired`，失败计入限流（第 8 次后 429）；另一管理员重置后目标的 token 全部失效、再登录须重新绑定；库中密文不含明文密钥；没有密钥的门户拒绝管理员登录（500）但普通用户不受影响。`TestAdminConsoleEndpoints`：提升为管理员后旧会话 403 `mfa_required`，撤销后 403 `forbidden` |
| `scripts/portal_ui_test.mjs`（无头 Chrome，CDP 驱动，51 项） | **用户门户**：注册页签随 `registration_open` 显示；错密码提示；登录后总览/账单/作业渲染且无 `null/undefined/NaN`；普通用户看不到控制台入口；用 `<img onerror>` 作 token 名 → 页面按字面显示、不生成元素、不执行；新 token 明文只显示一次且可作 Bearer 使用，吊销后 401；页面内不带头的 fetch → 403，`document.cookie` 看不到会话；网络页列出中继；改密码成功并改回；登出回到登录页且会话失效；页面注册新用户。**管理控制台**：管理员首次登录被要求绑定两步验证（页面显示二维码与 32 位 base32 密钥，错码被拒，用脚本按 RFC 6238 算出的码完成绑定；密钥存于截图目录供后续运行走验码步骤）；非管理员被拒；总览、账户余额；用户搜索、详情、发放 credit 入账、禁用/启用；自己的「撤销管理员/禁用」按钮禁用且 API 拒绝；新建用户；中继编辑/停用/启用、非法地址报错不崩；争议页退款裁决后作业页显示「已退款」，状态筛选生效；节点/审计/账本页渲染；无 JS 异常 |
| curl 安全探针 | 64 KB 以上请求体 400；未知 JSON 字段 400；`/static/../web.go` 被规范化为 404；伪造 cookie 值 401；CORS 预检 405（无 `Access-Control-*`）；普通用户访问管理端点 403；吊销别人的 token 404；GET 登录端点 405 |

## 10. 未覆盖

- MySQL（测试环境只有一台已停止的 MySQL，本版用 SQLite；迁移点见 design-v2 §3.2）。
- 中继凭证未在三机环境复测（本机演示用的是真实的独立进程和真实中继）。
- 门户 HTTPS 未在多机环境复测（上面的命令行实测在 Mac 局域网地址上进行）。
- 打洞（DCUtR）成功后的直连：本次刻意关闭了 UPnP 并只绑局域网地址，以验证中继路径。
- 公网 IPv6：双栈与跨地址族桥接只在本机 `::1` 上验证；同为 IPv6 的买卖双方直连（不经中继）未测。
- 大规模压测：中继和协调器的连接上限已调高，但未做数千用户级别的压测。
