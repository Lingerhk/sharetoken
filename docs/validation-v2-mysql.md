# sharetoken v2 MySQL 迁移与分布式复测

> 2026-11-08，分支 `arch-v2`（基于 1ccf202）。门户和协调器从 armbian（SQLite）迁到 orb VM `st-db`（MySQL 8.0），用 [validation-v2-distributed.md](validation-v2-distributed.md) 的同一套脚本在同一个 7 机环境上复测。环境见 [test-env.md](test-env.md)，实现见 [design-v2.md §3.2](design-v2.md) 和 [guide-v2.md §2.2](guide-v2.md)。原始结果在 Mac 的 `~/st-test/results/mysql-*`、`chaos/`。

## 0. 结论

**MySQL 后端可用，功能、账本一致性和故障恢复与 SQLite 版一致，吞吐更高。** 本轮发现的唯一明显退化（杀 relay-sg 后约 3 分钟 `busy`）与数据库无关，是客户端选中继只看 RTT 的老问题，这次恰好暴露；已修复并复测，整场故障注入 0 个 `busy`，全程成功率 99.59–99.92%（§4.1、§4.2）。

- 全部单元/集成测试（22 个包）在 SQLite 和 MySQL 两种模式下都通过；新增 MySQL 跨进程写锁、迁移复制、列类型翻译的测试。
- 线上数据（9 个用户、12.4 万个作业、37 万条账目）用 `copy-db` 48 秒迁完，账本回放平衡；用户 token、设备绑定、TLS 指纹、管理员两步验证全部原样可用，6 个用户客户端无需重新登录。
- 19 项 API 功能用例 6 个节点全部通过（首轮 alice、bob 各有一项流式用例因两台机器刚重启的网络瞬断失败，重跑 19/19）。
- 并发阶梯：两个买家合计 50 / 73 / 56 rps（c=16/32/64），SQLite + ARM 时为 31 / 41 / 39，100% 成功；瓶颈从协调器转到卖家名额和网络。
- 故障注入：kill -9 协调器、重启门户、杀卖家、杀模型服务、杀 relay-us 的影响与 SQLite 版相同或更小；**门户在满负载下重启一次成功**（SQLite 版第 7 轮因 `SQLITE_BUSY` 退出）。去掉 relay-sg 故障窗口，成功率 99.07–99.90%；修复 §4.1 后整场（含所有故障窗口）99.59–99.92%。
- 30 分钟长稳（6 个买家，30 618 个请求）成功率 **99.88%**，失败全部在 CN↔SG 买卖双方之间的数据链路上；协调器、门户、客户端内存平稳，mysqld 在 buffer pool 填满后稳定在 2.7 GB。
- 复测中修了几个 MySQL 下才明显的问题（§5）：账本回放在 MySQL 上慢 3–5 倍；`coordinator check` 和**每个运维命令**（打开引擎时）都在全库写锁里做回放，期间交易停顿；回放的分组结果超过 16 MB 落盘，拖慢并发提交。修复后运维命令执行期间交易延迟不受影响（§5）。
- 最终版本已部署在 st-db；单元测试两种模式全部通过，6 个节点功能用例 19/19。

## 1. 环境变化

| | 之前 | 现在 |
|---|---|---|
| 门户 + 协调器 | armbian（aarch64 4 核 2 GB，eMMC），共用 SQLite `st.db` | orb VM `st-db`（x86_64，cgroup 限 **4 CPU / 8 GB**，Ubuntu 24.04），MySQL 8.0.46（InnoDB，buffer pool 2 GB，`READ-COMMITTED`，`innodb_flush_log_at_trx_commit=1`） |
| 协调器网络位置 | 家庭宽带 | Mac 上的 VM，出口和 Mac 相同（经代理，到 SG 11 ms、US 286 ms、CN 344 ms） |
| 门户访问 | 局域网 192.168.6.91 + 各条隧道 | OrbStack 把 :8092 转发到 Mac 的回环、局域网和 ZeroTier 地址；US/SG 隧道改指 Mac 回环；alice、bob、carol 的 `config.json` 改了门户 URL |
| 参考模型 | 经反向隧道访问 Mac 的 Ollama | 直接 `host.orb.internal:11434` |

中继、用户客户端、密钥都没变（协调器 peer ID、门户签名公钥、TLS 指纹不变），所以中继的 `-allow-peer`/`-portal-key` 和客户端的指纹不用改。

## 2. 迁移

1. 先用一份在线快照试迁到 `sharetoken_dry`：37.6 秒，`check` 平衡。
2. 正式切换：停 armbian 的协调器和门户（干净退出，WAL 已合并）→ 拷 `st.db`（650 MB）→ `coordinator -db st-armbian-final.db copy-db mysql://…/sharetoken`（47.9 秒，62 万行）→ `check` 平衡 → 在 st-db 启动协调器和门户 → 改隧道和 3 个客户端的门户地址。交易中断约 2 分钟（05:17–05:19）。
3. 切换后 20 秒内 6 个用户全部连上新协调器；另一个会话对新门户跑了管理员两步验证、普通会话、CSRF 拒绝、中继延迟和页面无 JS 错误的检查，全部通过（两步验证密钥由 `portal-pass.key` 派生，`mfa_secret` 是 `VARBINARY`，字节原样）。

MySQL 里的数据量：`jobs` 576 MB 数据 + 174 MB 索引（SQLite 文件 650 MB），全部在 2 GB buffer pool 内。

## 3. 功能与性能

### 3.1 API 功能（`onall.sh … func -m qwen2.5:0.5b --tools`）

| 买家 | 结果 |
|---|---|
| carol、ivy、sam、uma | 19/19 |
| alice、bob | 首轮 18/19，重跑 19/19 |

首轮的两个失败都是流式用例，发生在 alice、bob 客户端为改门户地址重启后约 1 分钟：alice → bob 的 QUIC 连接 `no route to host`，qwen 只有 alice、bob 两个卖家，没有别的节点可换（502）；bob → alice 的流中途 30 秒无数据。与数据库无关，重跑通过。

### 3.2 并发阶梯（mock-fast，alice 和 uma 同时压，每档 30 秒；卖家共 64 个名额）

| 每个买家并发 | alice | uma | 合计 | SQLite + armbian |
|---|---|---|---|---|
| 16 | 40.7 rps，100% | 9.2 rps，100% | **49.9 rps** | 31 rps |
| 32 | 68.8 rps，100% | 4.4 rps，100% | **73.2 rps** | 41 rps |
| 64 | 47.2 rps，100% | 8.4 rps，100% | **55.6 rps** | 39 rps |

c=64 时两个买家共 128 个请求争 64 个名额，排队使 p99 升到 10–45 秒（`QueueWait`），这是过载时的预期行为。期间协调器约 0.2 核、mysqld 约 0.5 核。

MySQL 端的代价（`performance_schema`）：`COMMIT` 平均 2.6 ms（VM 磁盘 fsync），每个作业约 2.4 次提交，即每个作业约 6 ms 串行提交时间，**协调器的理论上限约 160 作业/秒**；写锁行 `SELECT … FOR UPDATE` 平均 0.1 ms；整个测试期间门户和协调器之间的行锁等待只有 5 次，都在启动迁移时。若要更高吞吐，下一步是把一个作业的多次提交合并（批量结算）或用更快的磁盘，而不是放宽 `flush_log_at_trx_commit`（账本要求提交即落盘）。

门户读路径（12.4 万作业，压测进行中）：`/api/v1/me` 158 ms，`/usage` 84 ms，`/jobs`、`/ledger` 分页 5–8 ms，`portal users` 173 ms。

## 4. 故障注入（`chaos.sh`，18 分钟，与 SQLite 版第 7 轮相同的时间表）

| 买家 | 请求 | 全程成功率 | 去掉 relay-sg 窗口（+60–270 s） | SQLite 第 7 轮 |
|---|---|---|---|---|
| alice（流式 qwen，c=2） | 4 393 | 98.61% | **99.90%** | 98.2% |
| bob（mock，c=4） | 10 125 | 96.72% | **99.90%** | 98.5% |
| ivy（流式 mock，c=4） | 4 876 | 92.21% | —（逐请求日志未取回） | 98.6% |
| uma（mock，c=4） | 5 196 | 89.80% | **99.07%** | 98.7% |

| 故障 | 影响 | 与 SQLite 第 7 轮比较 |
|---|---|---|
| kill -9 协调器，10 秒后重启 | 启动（迁移检查 + 账本回放）后约 6 秒在 3 个中继上预约成功；各买家失败 4–8 个，约 30 秒 | 相同 |
| kill -9 卖家 sam | bob 1 个失败 | 相同（0） |
| 重启门户（满负载） | 0 失败，几秒内恢复 | **更好**：SQLite 版启动迁移遇 `SQLITE_BUSY` 退出，门户停了 14 分钟 |
| 杀 carol 的模型服务 | 0 失败 | 相同 |
| 杀 relay-us | uma 31 个失败，约 60 秒 | 相近（24 个） |
| 杀 relay-sg | 见 §4.1：bob、uma、ivy 约 3 分钟 `busy` | **更差**，与数据库无关；已修复，复测见 §4.2 |

故障全部结束后 `coordinator check`：账本平衡（经历了一次 kill -9）。

### 4.1 杀 relay-sg 后的 `busy`（客户端问题，已修复）

relay-sg 宕机后，sam（SG 的卖家）按 RTT 选了 relay-cn（SG→CN 241 ms，比 US 略低），但 CN 的国际链路丢包严重（test-env.md §7），sam 经它和协调器之间的公告、`started` 报告频繁 `connection write timeout`；sam 的健康检查发现「coordinator unreachable through relay-cn」后，重新选中继又选回 CN，直到 relay-sg 恢复（约 2.5 分钟）才回到 SG。

这段时间 sam 照常给买家服务（买家经其他路径连得到它），但 `started` 报告延迟 14–43 秒到达协调器。协调器把这些「已签发超过 10 秒仍未开始」的票据算作买家囤票，买家攒到 4 张就被拒（`busy: N earlier tickets were never used`）。uma、ivy 根本不经 relay-sg，也被波及。同一窗口 carol 的作业 `started` 都在 0.5 秒内到达，协调器和数据库本身正常。

SQLite 版第 7 轮没出现，是因为 sam 当时恰好选了 US（两者 RTT 只差十几毫秒）。

修复（两处）：

1. **客户端回避失败过的中继**（`client/agent.go`、`p2p/link.go`）。健康检查判定经当前中继到不了协调器后，按优先顺序逐个试其他可达的中继能否连上协调器（`failover`/`probeCoordinator`）：经某个中继连上了，才把原中继记入回避表 5 分钟（`relayAvoidFor`）并切过去；一个都连不上，说明是协调器本身宕了（重启中），不回避任何中继，留在 RTT 最低的中继上等它回来（最初的实现直接回避失败的中继，第一次复测中协调器 kill -9 时 carol 因此依次回避了 CN、US，被挤到丢包的国际链路上约 80 秒，`started` 报告丢失，uma 收到 3 个 `busy`）。可达的中继全在回避表里时清空回避表。`chooseRelay` 按「不在回避表且门户报告协调器可达 → 不在回避表 → 门户报告可达 → 任意可达」的顺序选，回避中的中继即使 RTT 最低也不选，只有别的都不通时才退回用它；当前中继在回避表里时不适用「没快 20% 就不换」的防抖。另外，当前中继的实时 ping 窗口（每 10 秒一次）里至少 5 次、丢包 ≥ 30%（`LinkMonitor.Lossy`）也按故障处理：记入回避表并平滑切换（协调器仍可达，进行中的请求在旧路径上做完，30 秒后关旧连接；只有协调器真的不可达才立即断开）。
2. **协调器调用失败立即触发故障切换**（`buyer/client.go`、`node/node.go`、`client/agent.go`）。买家和卖家对协调器的调用在传输层失败（没有收到回应；协调器的明确拒绝不算）时调用 `Agent.Kick`，与协调器的最后一条连接断开时也触发，健康检查立即运行，而不是等下一个 10 秒周期；被触发的检查至少间隔 2 秒。`p2p.Call` 打不开流时返回 `ErrNoStream`（请求没发出去），买家领票遇到它会重试最多 10 秒，正好覆盖中继切换，请求成功而不是直接失败；协调器真宕机时 10 秒后照常报错。
3. **协调器囤票判定跳过节点重连前的票**（`coordinator/engine.go` `unusedTickets`）。节点在票据签发之后重新连上协调器（丢了或换了中继）时，这张票的 `started` 报告可能还在节点的重试队列里，不算买家囤票；与已有的买家侧 `ConnectedSince` 对称。

新增测试：`chooseRelay` 的回避用例（含 sam 的场景：relay-sg 不通、relay-cn 最快但刚失败 → 选 relay-us）、`LinkMonitor.Lossy` 阈值、节点重连后旧票不算囤票，以及端到端测试 `TestRelayThatFailsTheCoordinatorIsAvoided`：两个本机中继，一个经延迟代理（+25 ms，RTT 明显更高），另一个 ping 最快但连接过滤器拒绝经它到协调器的电路——即 relay-cn 的情形。修复前客户端卡在最快的坏中继上（30 秒超时失败，复现了 sam 的现象）；修复后立即切到慢的中继，之后 10 次重新测量都不回到坏中继。同一测试先模拟协调器宕机（两个中继都到不了协调器）：客户端必须留在最快的中继上（用「失败即回避」的旧逻辑会离开，测试失败），恢复后经它重连。`TestFailedCoordinatorCallTriggersFailover` 关掉定时健康检查，杀掉买家所用的中继后立即发请求：0.3–0.5 秒内成功；去掉 `Kick` 则 502 失败。

### 4.2 修复后复测

全部单元/集成测试 SQLite（`-race`）和 MySQL 两种模式通过；新协调器和 6 个客户端部署后：

**定向复测**（4 个买家同 `chaos.sh` 负载，第 60 秒杀 relay-sg，第 150 秒恢复）：sam 在 relay-sg 停止后 6 秒判定协调器不可达，4 秒内选了 relay-us，SG 恢复后的下一次测量回到 SG；窗口内 **0 个 `busy`**。失败只有前 30 秒 alice、bob 的协调器路径（原本经 relay-sg）切换中继期间的 31 个 `failed to dial`，之后各买家 100%。

**完整 `chaos.sh`**（同一时间表，18 分钟），按修复步骤各跑一次：

| 买家 | 修复前 | A：回避表 | B：A + 立即切换 | **C：B + 先试再回避（最终）** |
|---|---|---|---|---|
| alice（流式 qwen，c=2） | 98.61% | 98.54% | 99.56% | **99.59%**（3 920 请求） |
| bob（mock，c=4） | 96.72% | 99.67% | 99.91% | **99.92%**（9 439） |
| ivy（流式 mock，c=4） | 92.21% | 99.52% | 99.69% | **99.75%**（3 942） |
| uma（mock，c=4） | 89.80% | 99.42% | 99.72% | **99.77%**（4 326） |
| `busy` | 约 3 分钟 | 0 | 3（协调器重启后，见上文 carol） | **0** |
| 杀 relay-sg 后 120 秒内失败 | — | 80 | 3 | **4**（其中 2 个是 qwen 唯一卖家满载） |
| 杀 relay-us 后失败 | 31 | 39 | 13 | **11** |

最终版本（C）：
- 杀 relay-sg：sam 经探测切到 relay-us，SG 恢复后的下一次测量回到 SG。
- kill -9 协调器：carol 记录「coordinator unreachable through every relay (restarting?); waiting on the best relay」，留在本地的 relay-cn，没有回避任何中继；各买家失败 4–14 个（重启期间正常）。
- kill -9 sam、重启门户、杀 carol 模型服务：除 alice 的 qwen 唯一卖家满载（`capacity`）外 0–1 个失败。
- 结束后 `coordinator check`：账本平衡。

已知局限：
- 探测有一个窄的竞态：协调器恰好在「经当前中继失败」和「经别的中继探测」之间恢复时，当前中继会被误回避（部署时 carol 被误判一次，约 90 秒后回到 CN）。
- 探测按偏好顺序逐个进行、每个最多 10 秒，经丢包中继的探测可能用满（sam 这次从发现到切走 19 秒，A 版本是 4 秒）。
- carol 有 4 个作业结算时被拒（`ticket expired and its hold was released`）：节点完成了工作，但结算晚于票据期限，没有拿到报酬。这是协调器结算期限策略的问题，与本次修改无关，未处理。

## 5. 复测中修复的问题

| # | 问题 | 修复 |
|---|---|---|
| M1 | 账本回放（`Reconcile`：协调器启动、`coordinator check`、门户 overview）在 MySQL 上 8–15 秒（SQLite 1.6 秒）。`NOT EXISTS (… debit = a OR credit = a)` 在 MySQL 不能走索引，对每个账户扫一遍全部账目（4.7 秒）；`UNION ALL` 全部账目再分组 3.0 秒 | 拆成两个 `NOT EXISTS`（0.001 秒），先按 `credit`、`debit` 各自分组再合并（1.7 秒）。`check` 15.3 → 3.9 秒，`portal overview` 7.9 → 2.2 秒（压测进行中测得）。两种写法 SQLite 也受益。新增测试：没有任何账目的余额（正负抵消、总和为 0）也能被发现 |
| M2 | `coordinator check`（`CheckBooks`）在写事务里做回放；更隐蔽的是 `NewEngine` 本身也在写事务里回放，而**每个运维命令**（`check`、`nodes`、`balance`、`disputes`…）和协调器启动都会建引擎。MySQL 上它们持有全库写锁数秒，期间所有结算、出票都要等（压测中 `check` 让交易停了约 15 秒）。SQLite 上同样持有写锁，只是回放快 | 回放改在只读事务里做（`CheckBooks`，`NewEngine` 只在写事务里做归属检查，回放调 `CheckBooks`）；`Reconcile` 的每项检查都是单条语句，各自看到一致的状态 |
| M5 | 回放的分组结果（约 15 万个账户）超过 MySQL 默认 `tmp_table_size` 16 MB，每次落盘 6 个临时表，和并发事务的提交抢磁盘 | 服务器配置 `tmp_table_size = 256M`（guide-v2.md §2.2 已写入建议） |
| M6 | 每次迁移（每个运维命令都会迁移）执行 `INSERT IGNORE INTO st_write_lock`，写锁被占用时要等到释放（平均 0.5 秒） | 先用不加锁的读判断，只有表为空才插入 |

M1、M2、M5 合起来的效果（压测 c=4 进行中执行 `coordinator check`）：

| | `check` 耗时 | 与 `check` 重叠的请求，最长延迟 | 其余请求，最长延迟 |
|---|---|---|---|
| 修复前 | 15.3 s | 交易停顿约 15 s | — |
| M1 + 只读 `CheckBooks` | 4.6 s | 2.46 s | 0.64 s |
| + `tmp_table_size` | 3.1 s | 1.65–2.00 s | 0.90–1.02 s |
| + 引擎启动不持锁回放、M6 | 3.2 s | **0.59 s** | 0.60–0.74 s |
| M3 | MySQL 每个新连接卡 10 秒（服务器对客户端 IP 做反向 DNS） | 服务器配置 `skip_name_resolve`；账户按 IP（`127.0.0.1`）授权 |
| M4 | 迁移时 SQLite 的空 BLOB（`x''`）读出来是 `[]byte(nil)`，写进 MySQL 变成 NULL 违反 NOT NULL；SQLite 连接池只有一个连接，游标未关时再查会死锁 | `copy-db` 先查好源表的 NOT NULL 列，把 nil 还原成空字节 |

开发中在 MySQL 模式测试里发现并修掉的不兼容：派生表要别名、`key` 是保留字（`meta.key` 加反引号）、`LIKE … ESCAPE '\'` 在 MySQL 里是未闭合的字符串（改用 `!`，并统一 `LOWER` 实现大小写不敏感）、`'a' || b`（改 `CONCAT`）、各处 `ON CONFLICT … excluded.`（改「先 UPDATE 再 INSERT」，靠全库写锁保证正确）。

## 6. 长稳（soak）

6 个买家各以 2 个并发持续请求 mock-fast 30 分钟（05:48–06:18，非流式），每 60 秒采样各进程内存和文件描述符（`sample.sh`，含 st-db 上的 mysqld）。

| 买家 → 主要卖家 | 请求数 | 成功率 | p50 / p90 / p99 | SQLite 版（40 分钟） |
|---|---|---|---|---|
| alice（Mac） | 9 437 | 100% | 0.25 / 0.56 / 0.59 s | 100% |
| bob（orb） | 9 476 | 100% | 0.25 / 0.55 / 0.58 s | 100% |
| uma（US） | 5 285 | 100% | 0.67 / 0.69 / 0.73 s | 100% |
| ivy（instabox） | 4 813 | 100% | 0.69 / 0.71 / 0.76 s | 100% |
| sam（SG）→ CN | 1 155 | 98.2% | 1.03 / 3.12 / 18.9 s | 99.8% |
| carol（CN）→ SG | 452 | 96.7% | 3.27 / 8.63 / 38.8 s | 99.8% |
| **合计** | **30 618** | **99.88%** | | 99.98% |

36 个失败全部在 CN↔SG 买卖双方之间的数据链路上：节点 60 秒无数据（`node stalled`，21 个）、流被重置（10 个）、买家连上节点前票据已过期（`bad_ticket`，5 个）。与 SQLite 版的 9 个失败是同几类，只是这次链路更差（两边的 p99 都比上次长）；不经过协调器和数据库。

资源（约 37 个采样点）：

| 进程 | 内存 开始 → 峰值 → 结束 | 文件描述符 |
|---|---|---|
| 协调器（st-db） | 70 → 81 → 69 MB | 15 → 15 |
| 门户（st-db） | 51 → 79 → 51 MB | 13 → 13 |
| mysqld（st-db） | 2 591 → 2 775 → 2 772 MB（buffer pool 2 GB 填满后持平） | — |
| 中继 US / SG / CN | 26→27 / 28→28 / 19→19 MB | 不变 |
| 客户端（6 个） | 24–75 MB，结束时不高于峰值，斜率 −14 ～ +3 MB/小时 | 不增长 |

注：长稳开始约 2 分钟时（05:49:29）执行过一次旧版本的 `coordinator check`（M2 修复前），它持有写锁约 15 秒：alice、bob、uma、ivy 各有 2 个请求在这一刻卡了约 15 秒，但都成功了。这正是发现 M2 的线索。

## 7. 安全

- MySQL 只监听 st-db 的 127.0.0.1。最初监听 0.0.0.0 时发现：OrbStack 会把 VM 端口暴露到 Mac 的局域网，而局域网来的连接在 VM 里显示为来自 127.0.0.1，主机限制失效，只剩密码一道防线；已改为只听回环（局域网 `connection refused` 已验证）。
- 数据库密码不出现在命令行：DSN 不带密码，从 `SHARETOKEN_DB_PASSWORD` 读取，st-db 上存在 `~/st-test/db.env`（0600）。
- 迁移用到的密钥（`coordinator.key`、`portal-pass.key`、`portal-tls/`）经 Mac 中转后已从 Mac 的临时目录删除；armbian 上的原件和 `st.db` 保留作回退。

## 8. 回退

armbian 的 `~/st-test` 原样保留（二进制、密钥、迁移前的 `st.db`）。回退：停 st-db 的两个进程 → armbian 上按旧命令启动（`-db st.db`）→ 恢复旧隧道和 3 个客户端的门户地址。注意切换之后在 MySQL 里产生的作业和账目不会自动回到 SQLite（需要的话用 `copy-db` 反向复制到一个新的 SQLite 文件）。
