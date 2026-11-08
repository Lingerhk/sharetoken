# dev 环境（dev-env）

本文只记录公开信息。密钥在 Mac 的 `~/st-test/dev-keys/`（目录 0700，不入库）。生产环境见 [prod-env.md](prod-env.md)。

## 拓扑

```
 用户 ──https://dev.sharetoken.app──> armbian nginx（Let's Encrypt）
                                        │ 局域网明文 HTTP
                                        ▼
                         树莓派 192.168.6.148：dev 门户 :8092 + dev 协调器
                                        │ SSH 隧道，127.0.0.1:3306
                                        ▼
                         armbian MySQL：库 sharetoken_dev，账号 sharetoken_dev
```

| 项 | 说明 |
|---|---|
| 域名 | `dev.sharetoken.app` 是 `sharetoken.app` 的 CNAME，公网 IP 变化时自动跟随，不用改 DDNS |
| 主机 | 树莓派 3B（1 GB，`ssh s0nnet@192.168.6.148`），目录 `~/sharetoken-dev`；机器上还有桌面环境、VLC 等 |
| 服务 | systemd `sharetoken-dev-dbtunnel`、`sharetoken-dev-coordinator`、`sharetoken-dev-portal`，单元里写了内存上限，但 Pi 的启动参数带 `cgroup_disable=memory`，**上限实际不生效**，只有 `GOMEMLIMIT` 这个软限制（实测两个进程共约 50 MB） |
| 数据库 | 和生产**同一个 MySQL 实例**，独立的库和账号，账号只能访问 `sharetoken_dev`。生产的 `backup.sh` 不备份这个库 |
| 隧道 | Pi 的 `dbtunnel.key` 在 armbian 的 `authorized_keys` 里限制为 `command="/usr/bin/false",restrict,port-forwarding,permitopen="127.0.0.1:3306"`，只能转发到 MySQL，不能执行命令。生产容器不用改端口绑定 |
| 协调器 peer ID | `12D3KooWDa9b2HPSjHYhxuVGhKQ1866qAsGTKmgwEB5oY4D7qSYt` |
| 门户 pass 公钥 | `ed25519:5262c7de2eb39f527d515a36082293f1c3426aaaf583883ad71312ad223ed011` |
| 管理员 | `root`，密码在 Mac 的 `~/st-test/dev-keys/admin.pass`，首次登录网页要绑两步验证 |
| 注册 | 开放，必须有邮箱验证码（[mail.md](mail.md)）；模型只有 mock。关闭时改 `scripts/dev/portal.yaml` 的 `open_registration` |
| 邮件 | systemd `sharetoken-dev-mail`，发件人 `team-dev@`；收信只有局域网可达 |
| 门户入口 | `http.trusted_proxies` 只信任 armbian 传来的 `X-Real-IP`；systemd `IPAddressAllow` 限制 8092 端口只有 armbian 和本机能访问。**armbian 到 Pi 这段是明文**，登录密码和 token 会以明文经过局域网，所以 dev 不要用和生产相同的密码 |
| 模型 | 只用 mock（`scripts/mock_upstream.py`），不放公司采购的 API：dev 在公网上，额度可能被消耗 |

部署：`scripts/dev/deploy.sh`（全部）、`scripts/dev/deploy.sh --bins`（只更新二进制和配置）。不碰中继和生产服务。

**注意：** dev 和生产共用 MySQL 实例（内存上限 600 MB），所以 dev 上不要跑压测，否则会拖累生产。

## 客户端

`scripts/build-gui.sh dev [windows [arch]]` 构建的程序内置 dev 门户，数据在 `~/.sharetoken-dev`，可以和 prod 版同时运行，见 [gui.md](gui.md)。

## 中继

中继由 prod 和 dev 共用，属于**生产设施**。配置在 `scripts/relay/conf/`，发布只用 `scripts/relay/deploy.sh`：

```bash
scripts/relay/deploy.sh lab-cn2 lab-stdb    # 实验中继
scripts/relay/deploy.sh --prod sg           # 生产在用的中继，必须带 --prod，逐台发布
```

| 中继 | 地址 | 网络 | 用途 |
|---|---|---|---|
| relay-us / sg / cn | 见 prod-env.md | prod + dev | 日常使用；dev 的设备合计最多 32 个预约 |
| lab-cn2 | `relay-cn2.sharetoken.app:4002` | 只有 dev | 压测、chaos、验证中继改动；公网可达，但跨境链路差 |
| lab-stdb | Mac 局域网 `192.168.6.125:4101`（orb VM st-db） | 只有 dev | **已随 st-db 停机，在 dev 门户里是停用状态**。压测时 `orb start st-db`，启动 `~/st-test/relay-lab/relay`，再 `portal relay enable`；只有局域网内可达，Mac 的 IP 变了要改配置并重新登记 |

dev 门户登记了 relay-sg、relay-us、relay-cn 和 lab-cn2。压测和 chaos 前，先在 dev 门户 `relay disable` 三个共用中继，让 dev 流量只走实验中继，结束后再 `enable`。

共用时的隔离：
- **账号：** 设备配额按“网络 + 账号”计算，两个网络的 `user:3` 互不影响。
- **容量：** dev 有预约总量上限。
- **交易：** 节点只接受自己协调器的票据，协调器拒绝未绑定的设备，流量端到端加密。
- **带宽无法隔离：** 中继不知道买家属于哪个网络，所以**压测和 chaos 不能走共用中继**。`scripts/testenv/chaos.sh` 现在只操作实验中继。
- **重启影响两边：** 该中继上的连接会断开，在途请求会失败。增删网络也要重启（只在启动时读配置）。中继的代码改动先在实验中继上验证。

## 压测（st-db）

st-db 平时停机（`orb stop st-db`，数据保留，测试期的用户和流水也在它的 MySQL 里）。它有 MySQL 8 + 4 CPU / 8 GB，压测时才启动（`orb start st-db`），走 dev 身份和实验中继：

1. 停掉 Pi 上的 dev 协调器：同一把协调器 key 不能同时在两处运行。
2. 把 `~/st-test/dev-keys/` 的 `coordinator.key`、`portal-pass.key` 放到 st-db 的 `~/st-test/`（先备份原文件）。st-db 上的管理员两步验证依赖旧 pass key，换后要重新建管理员。
3. 在 st-db 的门户里只登记实验中继。
4. 压完恢复 Pi 上的 dev 协调器。

`scripts/testenv` 发现 st-db 还是生产协调器 key 时会拒绝启动，防止和生产抢中继。
