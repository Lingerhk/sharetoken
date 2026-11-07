# sharetoken 分布式测试环境

> 用于 v2 及以后版本的多机实测。本文件**不含任何密钥**：测试账户密码见 `docs/test-env.secrets.md`（已加入 `.gitignore`），第三方 API key 不属于本环境（见 §8）。
> 最近一次完整部署：2026-11-07（validation-v2-distributed.md 的测试即在此环境上完成）。

## 1. 拓扑

```
                 ┌────────── relay-us 165.227.115.27:4001 (v4+v6, TCP+QUIC) ──────────┐
 用户 uma (US)  ─┤                                                                     │
 用户 ivy (instabox, AWS, IPv6) ─ relay-us                                            │
 用户 sam (SG)  ─┼────────── relay-sg 201.79.122.100:4001 (v4+v6, TCP+QUIC) ──────────┼── 协调器 (armbian, 家庭内网,
 用户 alice (Mac)、bob (orb VM) ─ relay-sg                                           │    无监听端口，只在 3 个中继上预约)
 用户 carol (CN) ┴────────── relay-cn 119.29.250.173:8091 (仅 v4 TCP) ────────────────┘
                                                                                         门户 https://192.168.6.91:8092 (仅内网)
```

| 主机 | 地址 | 规格 | 角色 | 目录 |
|---|---|---|---|---|
| **armbian** | 192.168.6.91（LAN）、192.168.8.91（ZeroTier） | aarch64 4 核 2 GB，eMMC | 门户 :8092（HTTPS 自签名）+ 协调器（同一个 `st.db`） | `~/st-test` |
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

三个中继已登记在门户（`./portal -db st.db relay ls`），各自的 `relay.key` 在 `~/st-test`；**不要删除 key 文件**，否则 peer ID 变了要重新登记。

## 3. 门户访问路径（门户暂不暴露公网）

| 用户所在 | 门户 URL | 通道 |
|---|---|---|
| Mac | `https://192.168.6.91:8092` | 直连局域网 |
| CN | `https://192.168.8.91:8092` | ZeroTier |
| US、SG | `https://127.0.0.1:8092` | Mac 发起的反向隧道 `ssh -N -R 127.0.0.1:8092:192.168.6.91:8092 s0nnet@<US/SG>`（armbian 没有 US/SG 的 SSH key） |
| orb VM | `https://host.orb.internal:18092` | orb 访问不到家庭局域网；Mac 上 `ssh -N -L 127.0.0.1:18092:127.0.0.1:8092 s0nnet@192.168.6.91`，orb 经 host.orb.internal 访问 Mac 的回环 |
| instabox | `https://127.0.0.1:8092` | instabox 禁止 SSH 远程转发；在 instabox 上 `ssh -N -i ~/.ssh/st_tunnel -L 127.0.0.1:8092:127.0.0.1:8092 s0nnet@165.227.115.27`。该 key 在 US 的 `authorized_keys` 里受限为 `restrict,port-forwarding,permitopen="127.0.0.1:8092",command="/bin/false"`，只能转发到 US 上的门户隧道 |

另外，协调器的参考模型（抽检 / 争议精确计数）经 Mac 反向隧道访问 Mac 的 Ollama：`ssh -N -R 127.0.0.1:21434:127.0.0.1:11434 s0nnet@192.168.6.91`。

**这些隧道都从 Mac 发起，Mac 休眠或重启后需要重建**：`scripts/testenv/tunnels.sh`。门户只用于注册/登录、领中继凭证（1 小时有效）和账户查询，不在交易数据路径上：隧道断开期间已在线的客户端照常交易，约 1 小时后中继凭证过期才会受影响。

## 4. 启动与停止

所有远程常驻进程用 `setsid nohup … < /dev/null &` 启动，本地 ssh 加 `timeout`。**杀进程只用 `pkill -x <进程名>` 或按 PID**：`pkill -f` 会匹配到 ssh 自身的命令行，而 `pkill -x python3` 会误杀同用户的其他 python 服务（CN 是共用机器）。

```bash
# armbian
cd ~/st-test
./portal -db st.db serve -listen :8092 -open-registration -signup-credit 5000000
./coordinator -db st.db -key coordinator.key -fee-bps 500 -debug 127.0.0.1:6060 \
  -reference id=gemma4:e4b,upstream=gemma4:e4b,url=http://127.0.0.1:21434/v1
# 中继（US / SG；CN 用 -listen /ip4/0.0.0.0/tcp/8091 -announce /ip4/119.29.250.173/tcp/8091）
./relay -key relay.key -announce /ip4/<v4>/tcp/4001,/ip4/<v4>/udp/4001/quic-v1,/ip6/<v6>/tcp/4001,/ip6/<v6>/udp/4001/quic-v1 \
  -status 127.0.0.1:8093 -portal-key ed25519:0c89…8dc3 -allow-peer 12D3KooWFAqC…PmWfUh
# 用户客户端（每台用户机器；Mac 上的 home 见 secrets 文件）
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

管理员账户 `root`；用户注册时得 5 credit，压测前用 `./portal -db st.db grant <user> 1000000000 "load test"` 加额度。

## 6. 测试工具

- `scripts/st_bench.py`（只用标准库，兼容 CentOS 7 的 Python 3.6）
  - `func -m <model> [--tools]`：19 项功能用例（单次、流式、include_usage、多轮 + system、max_tokens、中文、长 prompt、采样参数、JSON 模式、工具调用、各类错误码、Origin 拦截、中途断开、并发）
  - `load -m <model> -c <并发> (-n N | -d 秒) [--stream] [--max-tokens K] [--json OUT] [--log 每请求日志] [--slow 秒]`
- `scripts/mock_upstream.py <port> [model…]`：诚实的 OpenAI 兼容假上游（`MOCK_WORDS`、`MOCK_DELAY_MS`、`MOCK_FAIL_PCT`），把网络和平台开销与模型速度分开测。
- 协调器 pprof：armbian 上 `curl -o p.prof 'http://127.0.0.1:6060/debug/pprof/profile?seconds=20'`，拷回后 `go tool pprof -top <arm64 coordinator 二进制> p.prof`。
- 中继统计：中继主机上 `curl -s 127.0.0.1:8093`。
- 运维查询（armbian）：`./coordinator -db st.db nodes|relays|disputes|audits|check`，`./portal -db st.db overview|users`。

## 7. 网络特性与坑

1. **CN（腾讯云）**：安全组只开 `~/fw_allow.txt` 列出的端口，sharetoken 可用 **8090–8095/tcp**（UDP 不通，没有 QUIC）；**没有公网 IPv6**；**国际出口丢包严重**（实测到 SG ICMP 丢包 70%、到 US 15%，到 SG 的中继 RTT 0.8–2 s）。CN 买卖跨境时尾延迟高，见验证报告。从 Mac 往 CN 传大文件极慢：**在 CN 上用源码编译**（Go 在 `~/sharetoken-sim/go`，`GOPROXY=https://goproxy.cn`），只传约 340 KB 的源码包（打包时排除 `docs/test_env.txt`；macOS tar 加 `COPYFILE_DISABLE=1`）。
2. **US / SG**：只有 453 MB 内存，只跑中继、客户端和 mock，不要装模型。ufw 未启用。
3. **Mac 的出口**：Mac 到 SG 只有约 11 ms、到 US 约 247 ms，说明 Mac 的流量走了代理或 VPN，所以 Mac 选 relay-sg。orb VM 访问不到 192.168.6.x 局域网（见 §3）。
4. **ZeroTier**：armbian、CN、Mac 都在 192.168.8.x 上。客户端默认监听 0.0.0.0 会通告 ZeroTier 地址，这几台之间可能直连而不经中继。要测纯中继路径，就用 `up -listen` 只绑局域网地址。
5. **OrbStack 会把 VM 里监听的端口自动转发到 Mac 的 127.0.0.1**。bob 如果也用默认的 9787，会和 Mac 上的 alice 抢端口：谁后启动谁就 `bind: address already in use` 退出，而且 Mac 上 `curl 127.0.0.1:9787` 可能实际连到的是 bob。所以 bob 的 `config.json` 里 `"http": "127.0.0.1:9788"`，在 orb 里压测要加 `-b http://127.0.0.1:9788`。
6. **zsh**：本地脚本里 `$h:st-test` 会被 zsh 当作修饰符展开，要写成 `${h}:st-test`。
7. **armbian 的 Python sqlite3 太旧**，读不了带 STRICT 的库；要查库就把 `st.db*` 拷回 Mac 用 `sqlite3`。
8. 协调器在 armbian（纯 Go SQLite + ARM）上的上限约 **40 作业/秒**（见验证报告 §4）。压测数字受此限制，不代表 x86 服务器上的上限。

## 8. Token 生产方

本网络只共享**自托管的开源权重模型**：测试用 Ollama（gemma4:e4b、qwen2.5:0.5b）和 mock 上游。`docs/test_env.txt` 里列出的 DeepSeek / Anthropic / OpenAI 官方 API key **不接入网络**：把商业 API 的 key 转卖或共享给他人违反这些服务的条款，也不在本项目的范围内（见 `node/upstream.go` 的注释）。这些 key 以明文存在仓库目录里，建议轮换，并只存放在仓库之外。
