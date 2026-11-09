# sharetoken 分布式测试环境

> 用于 v2 及以后版本的多机实测。本文件**不含任何密钥**：测试账户密码见 `docs/test-env.secrets.md`（已加入 `.gitignore`），第三方 API key 不属于本环境（见 §8）。
> 最近一次完整部署：2026-11-07（validation-v2-distributed.md 的测试即在此环境上完成）。
> **2026-11-08 晚起，门户 + 协调器 + MySQL 正式部署（2026-11-09 起在云主机 165.227.3.194 上，域名 https://sharetoken.app，见 [prod-env.md](prod-env.md)；armbian 改跑 dev，见 [dev-env.md](dev-env.md)）；orb VM `st-db` 只用于压测。** st-db 曾承载门户和协调器（见 validation-v2-mysql.md），现在它们已停：**压测不再停生产，也不再用生产的中继**：st-db 换成 dev 身份，走实验中继，见 [dev-env.md](dev-env.md)。下文 §3–§4 描述的是 st-db 压测拓扑，其中“门户在 st-db”只在压测期间成立。

## 1. 拓扑

```
                 ┌────────── relay-us 165.227.115.27:4001 (v4+v6, TCP+QUIC) ──────────┐
 用户 uma (US)  ─┤                                                                     │
 用户 ivy (instabox, AWS, IPv6) ─ relay-us                                            │
 用户 sam (SG)  ─┼────────── relay-sg 201.79.122.100:4001 (v4+v6, TCP+QUIC) ──────────┼── 协调器 (orb VM st-db, Mac 上,
 用户 alice (Mac)、bob (orb VM) ─ relay-sg                                           │    无监听端口，只在 3 个中继上预约)
 用户 carol (CN) ┴────────── relay-cn 119.29.250.173:8091 (仅 v4 TCP) ────────────────┘
                                                                                         门户 + MySQL 也在 st-db（https://127.0.0.1:8092，经 Mac 转发）
```

| 主机 | 地址 | 规格 | 角色 | 目录 |
|---|---|---|---|---|
| **orb VM** `st-db`（压测用） | 192.168.139.91（OrbStack NAT，IP 可能变，用 `orb run -m st-db`） | x86_64，**限 4 CPU / 8 GB**（cgroup：`nproc`/`free` 显示的是宿主值，`/sys/fs/cgroup/cpu.max`、`memory.max` 才是限额），Ubuntu 24.04 | MySQL 8.0（只听 127.0.0.1）+ 门户 :8092（HTTPS 自签名）+ 协调器 | `~/st-test` |
| **armbian（dev）** | 192.168.6.91（LAN）、192.168.8.91（ZeroTier）；公网 27.125.141.245（动态，路由器映射 80/443） | aarch64 4 核 2 GB，eMMC | **dev** 门户 + 协调器 + Docker MySQL + nginx（Let's Encrypt） | `/srv/sharetoken`、`/etc/sharetoken`（见 prod-env.md） |
| **US** | 165.227.115.27 / 2604:a880:800:14:0:3:d0f4:b000 | x86_64 1 核 453 MB，Ubuntu | relay-us :4001；用户 uma（只买） | `~/st-test` |
| **SG** | 201.79.122.100 / 2400:6180:0:d2:0:3:50d5:3000 | x86_64 1 核 453 MB，Ubuntu | relay-sg :4001；用户 sam（卖 mock-fast + 买） | `~/st-test` |
| **CN**（TECS） | 119.29.250.173（**无公网 IPv6**） | x86_64 2 核 2 GB，CentOS 7，与 frps/blog 等共用 | relay-cn **:8091**；用户 carol（卖 mock-fast + 买） | `~/st-test`（源码编译在 `~/sharetoken-v2/src2`） |
| **Mac** | 192.168.6.125 | Apple Silicon，Ollama 0.40 | 用户 alice（卖 gemma4:e4b、qwen2.5:0.5b + 买）；所有 SSH 隧道的发起方 | 客户端 home 见 secrets 文件 |
| **orb VM** `ada-dev` | 192.168.139.242（OrbStack NAT） | x86_64 13 核 16 GB | 用户 bob（经 `host.orb.internal:11434` 卖 Mac 上的 qwen2.5:0.5b + 买） | `~/st-test` |
| **instabox** `srv-01` | 172.16.0.2，出口 IPv6 2600:1f14:…（AWS） | 4 vCPU 8 GB | 用户 ivy（只买） | `~/st-test` |

进入方式：`ssh s0nnet@<IP>`（Mac 上已配好免密）；orb：`orb run -m ada-dev bash -c '…'`；instabox：`orb run -m ada-dev bash -c "instabox ssh srv-01 -- '…'"`（instabox 闲置会自动挂起，`instabox ssh` 会唤醒它）。

## 2. 固定标识

| 项 | 值 |
|---|---|
| 协调器 peer ID | `12D3KooWFAqCYkUS2Kt2KRWQ1mWF82Em4mRbB5nD4V7Fr5PmWfUh`（`~/st-test/coordinator.key`） |
| 门户凭证公钥 | `ed25519:0c8928fec72a6b1502eb83e8104cfdc56d740c54d040e18a7eed05edaaeb8dc3`（`~/st-test/portal-pass.key`） |
| 门户 TLS 指纹（`-portal-pin`） | `sha256:0e7c55a96cbe62e3f2f68e1f8ec9c34e8fa77c14e58f0ac7caf4dfc5fd59a717`（`~/st-test/portal-tls/`，务必保留） |
| relay-us | `12D3KooWCkJbNJWo1foq74TVtR9VKmfPK7rY8sSU3QgM87nmMitC` |
| relay-sg | `12D3KooWLCSbc4QX8etk5kRoQeBWQdsX1tuVcAdA81KEL3QthP9H` |
| relay-cn | `12D3KooWBBqzikVVSqjpjy3LX4JfHTg3e8S98zKhiCU2xpafvRZi` |

三个中继已登记在门户（st-db 上 `./portal -db $DSN relay ls`，`DSN=mysql://sharetoken@127.0.0.1:3306/sharetoken`，先 `set -a; . ./db.env; set +a`），各自的 `relay.key` 在 `~/st-test`；**不要删除 key 文件**，否则 peer ID 变了要重新登记。

## 3. 门户访问路径（门户暂不暴露公网）

| 用户所在 | 门户 URL | 通道 |
|---|---|---|
| Mac | `https://127.0.0.1:8092` | OrbStack 把 st-db 监听的端口转发到 Mac 的 127.0.0.1（监听 0.0.0.0 的端口还会出现在 Mac 的局域网和 ZeroTier 地址上） |
| CN | `https://192.168.8.123:8092` | ZeroTier → Mac 的 ZeroTier 地址 → st-db |
| US、SG | `https://127.0.0.1:8092` | Mac 发起的反向隧道 `ssh -N -R 127.0.0.1:8092:127.0.0.1:8092 s0nnet@<US/SG>` |
| orb VM `ada-dev` | `https://host.orb.internal:8092` | 经 Mac 回环转发到 st-db（`st-db.orb.local` 在 ada-dev 里解析不了；直接用 VM IP 也通，但 IP 会变） |
| instabox | `https://127.0.0.1:8092` | instabox 禁止 SSH 远程转发；在 instabox 上 `ssh -N -i ~/.ssh/st_tunnel -L 127.0.0.1:8092:127.0.0.1:8092 s0nnet@165.227.115.27`。该 key 在 US 的 `authorized_keys` 里受限为 `restrict,port-forwarding,permitopen="127.0.0.1:8092",command="/bin/false"`，只能转发到 US 上的门户隧道 |

协调器的参考模型（抽检 / 争议精确计数）直接访问 Mac 的 Ollama：`http://host.orb.internal:11434/v1`（不再需要 armbian 时期的 21434 反向隧道）。

MySQL 只监听 st-db 的 127.0.0.1:3306（OrbStack 转发到 Mac 的 127.0.0.1:3306，局域网不可达；曾经监听 0.0.0.0 时，局域网连接经 OrbStack 转发后在 VM 里显示为来自 127.0.0.1，只剩密码一道防线，所以改成只听回环）。服务器配置在 st-db 的 `/etc/mysql/mysql.conf.d/sharetoken.cnf`：`READ-COMMITTED`、`skip_name_resolve`（不加的话每个新连接卡 10 秒做反向 DNS）、`innodb_buffer_pool_size = 2G`、`tmp_table_size = 256M`、`utf8mb4_bin`。

**这些隧道都从 Mac 发起，Mac 休眠或重启后需要重建**：`scripts/testenv/tunnels.sh`。门户只用于注册/登录、领中继凭证（1 小时有效）和账户查询，不在交易数据路径上：隧道断开期间已在线的客户端照常交易，约 1 小时后中继凭证过期才会受影响。

## 4. 启动与停止

所有远程常驻进程用 `setsid nohup … < /dev/null &` 启动，本地 ssh 加 `timeout`。**杀进程只用 `pkill -x <进程名>` 或按 PID**：`pkill -f` 会匹配到 ssh 自身的命令行，而 `pkill -x python3` 会误杀同用户的其他 python 服务（CN 是共用机器）。

2026-11-08 起三个服务端都用 YAML 配置（docs/config-v2.md）。仓库里的 `scripts/testenv/conf/` 是各主机实际使用的配置（不含机密），`deploy.sh` 会把它们拷到各主机的 `~/st-test/`，先 check-config 再重启。

```bash
# st-db（orb run -m st-db bash；env.sh 里的 coord_start / portal_start 做的就是这些）
cd ~/st-test                                         # DB 密码在 db.pass（0600，database.password_file）
./portal -config portal.yaml serve                   # open_registration: false（公司内部，只由管理员建号）
./coordinator -config coordinator.yaml               # 参考模型 deepseek-flash、gpt-4o-mini，audit.interval 0
# 运维命令照旧用 -db（先 set -a; . ./db.env; set +a），或者 -config coordinator.yaml / portal.yaml
# 中继（US / SG / CN；~/st-test/relay.yaml = conf/relay-<us|sg|cn>.yaml，宣告 /dns4、/dns6 域名）
./relay -config relay.yaml
# 用户客户端（每台用户机器；Mac 上的 home 见 secrets 文件）。配置备份：<home>/config.json.bak-ollama
SHARETOKEN_HOME=~/st-test/<user> ./sharetoken up
# mock 上游（CN、SG；见 §6）
MOCK_WORDS=48 MOCK_DELAY_MS=5 python3 mock_upstream.py 18080 mock-fast
```

一键脚本（在 Mac 上运行，见 `scripts/testenv/README.md`）：`deploy.sh`（交叉编译并滚动更新协调器和全部客户端）、`status.sh`（6 个用户的连接状态）、`tunnels.sh`（重建隧道）、`onall.sh`（6 个买家同时压测）、`chaos.sh`（故障注入）。

## 5. 用户与共享

| 用户 | 机器 | 卖 | 上游 | 并发 |
|---|---|---|---|---|
| alice | Mac | gemma4:e4b（in 100000 / out 200000）、qwen2.5:0.5b（20000 / 40000） | Ollama 127.0.0.1:11434 | 2 |
| bob | orb VM（本地 API **127.0.0.1:9788**） | qwen2.5:0.5b（20000 / 40000） | `http://host.orb.internal:11434/v1`（Mac 的 Ollama） | 2 |
| carol | CN | mock-fast（10000 / 10000） | mock 127.0.0.1:18080 | 32 |
| sam | SG | mock-fast（10000 / 10000） | mock 127.0.0.1:18080 | 32 |
| uma | US | — | — | — |
| ivy | instabox | — | — | — |

管理员账户 `root`；用户注册时得 5 credit，压测前在 st-db 上用 `./portal -db $DSN grant <user> 1000000000 "load test"` 加额度。

## 6. 测试工具

- `scripts/st_bench.py`（只用标准库，兼容 CentOS 7 的 Python 3.6）
  - `func -m <model> [--tools]`：19 项功能用例（单次、流式、include_usage、多轮 + system、max_tokens、中文、长 prompt、采样参数、JSON 模式、工具调用、各类错误码、Origin 拦截、中途断开、并发）
  - `load -m <model> -c <并发> (-n N | -d 秒) [--stream] [--max-tokens K] [--json OUT] [--log 每请求日志] [--slow 秒]`
- `scripts/mock_upstream.py <port> [model…]`：诚实的 OpenAI 兼容假上游（`MOCK_WORDS`、`MOCK_DELAY_MS`、`MOCK_FAIL_PCT`），把网络和平台开销与模型速度分开测。
- 协调器 pprof：st-db 上 `curl -o p.prof 'http://127.0.0.1:6060/debug/pprof/profile?seconds=20'`，Mac 上 `go tool pprof -top ~/st-test/build/amd64/coordinator p.prof`（st-db 挂载了 Mac 的文件系统，直接写到 Mac 路径即可）。
- MySQL 慢查询 / 热点语句：st-db 上 `sudo mysql` 查 `performance_schema.events_statements_summary_by_digest`（见 validation-v2-mysql.md §3）。
- 中继统计：中继主机上 `curl -s 127.0.0.1:8093`。
- 运维查询（st-db）：`./coordinator -db $DSN nodes|relays|disputes|audits|check`，`./portal -db $DSN overview|users`。

## 7. 网络特性与坑

1. **CN（腾讯云）**：安全组只开 `~/fw_allow.txt` 列出的端口，sharetoken 可用 **8090–8095/tcp**（UDP 不通，没有 QUIC）；**没有公网 IPv6**；**国际出口丢包严重**（实测到 SG ICMP 丢包 70%、到 US 15%，到 SG 的中继 RTT 0.8–2 s）。CN 买卖跨境时尾延迟高，见验证报告。从 Mac 往 CN 传大文件极慢：**在 CN 上用源码编译**（Go 在 `~/sharetoken-sim/go`，`GOPROXY=https://goproxy.cn`），只传约 340 KB 的源码包（打包时排除 `docs/test_env.txt`；macOS tar 加 `COPYFILE_DISABLE=1`）。
2. **US / SG**：只有 453 MB 内存，只跑中继、客户端和 mock，不要装模型。ufw 未启用。
3. **Mac 的出口**：Mac 到 SG 只有约 11 ms、到 US 约 247 ms，说明 Mac 的流量走了代理或 VPN，所以 Mac 选 relay-sg。orb VM 访问不到 192.168.6.x 局域网（见 §3）。
4. **ZeroTier**：armbian、CN、Mac（192.168.8.123）都在 192.168.8.x 上。客户端默认监听 0.0.0.0 会通告 ZeroTier 地址，这几台之间可能直连而不经中继。要测纯中继路径，就用 `up -listen` 只绑局域网地址。
5. **OrbStack 会把 VM 里监听的端口自动转发到 Mac 的 127.0.0.1**。bob 如果也用默认的 9787，会和 Mac 上的 alice 抢端口：谁后启动谁就 `bind: address already in use` 退出，而且 Mac 上 `curl 127.0.0.1:9787` 可能实际连到的是 bob。所以 bob 的 `config.json` 里 `"http": "127.0.0.1:9788"`，在 orb 里压测要加 `-b http://127.0.0.1:9788`。
6. **zsh**：本地脚本里 `$h:st-test` 会被 zsh 当作修饰符展开，要写成 `${h}:st-test`。
8. st-db + MySQL 上，2 个买家 c=32 时约 73 作业/秒，瓶颈变成卖家名额和网络延迟；协调器每个写事务的提交约 2.6 ms（VM 磁盘 fsync），见 validation-v2-mysql.md。
9. **orb VM 的资源限额**用 `orb create --cpus 4 --memory 8G` 设置，`orb restart` 后才生效；VM 里的 `nproc`、`free` 仍显示宿主机的 14 核 / 16 GB，以 cgroup 为准。Go 1.25+ 按 cgroup 限额设置 GOMAXPROCS。
