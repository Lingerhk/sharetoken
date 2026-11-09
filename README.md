# sharetoken

> 当前版本：**v2**（2026-11-07，分支 `arch-v2`）——四角色架构：用户 / 中继 / 协调器 / 门户。

一个用 Go 写的**开源模型 token 共享网络**：把自己机器上开源权重模型的闲置算力共享出去赚 credit，需要时用 credit 调用别人的节点。同一个账户既能卖也能买。

- 节点跑的是不是它声称的模型，由抽检和 TEE 证明来检验，而不是靠名单。

## 架构

| 角色 | 程序 | 职责 |
|---|---|---|
| 用户 | `sharetoken` | 既是买家也是卖家；本地 OpenAI 端点 + 管理 API（给 GUI）；自动按 RTT 选最优中继。桌面客户端见 [docs/gui.md](docs/gui.md) |
| 中继 | `relay` | 只转发加密流量，有多个；看不到明文、不记账；只给出示门户短期凭证的注册设备预约 |
| 协调器 | `coordinator` | 算账平台：撮合、结算、争议、抽检；不监听端口，只经中继访问 |
| 门户 | `portal` | 用户管理：注册登录、API token、设备绑定、credit、账单与用量、中继登记；自带官网 `/`、用户网页 `/app/`（`/login` 跳转到这里）和管理控制台 `/admin/`（管理员强制 TOTP 两步验证）；与协调器共用数据库 |

## 买家能得到的保证

| 保证 | 机制 |
|---|---|
| prompt 不经过协调器，中继和网络读不到 | 直连 libp2p 流 + 封装加密（box） |
| 响应没被篡改、截断、重排 | 节点签收据，买家逐块核对哈希 |
| 虚报用量会被退款 | 买家拒签后提交**这一单**的密钥作为证据，协调器用参考模型精确数 token 后自动裁决 |
| 节点没有偷换成小模型 | 协调器随机抽检，在参考模型上重算比对，不符的节点被暂停接单 |
| 节点运营者也看不到 prompt（可选） | AMD SEV-SNP / Intel TDX 机密虚拟机，quote 绑定节点的两把公钥 |
| 账不会丢 | MySQL 复式账本，启动时回放日志对账 |

## 文档

| 文档 | 内容 |
|---|---|
| [docs/design-v2.md](docs/design-v2.md) | v2 架构：四个角色、用户账户、经中继访问协调器、中继选择、共用 MySQL 数据库 |
| [docs/guide-v2.md](docs/guide-v2.md) | 用户命令、运维部署、本地管理 API（GUI）、门户 HTTP API |
| [docs/versioning.md](docs/versioning.md) | 版本号、接口版本、升级规则和版本策略 |
| [docs/gui.md](docs/gui.md) | 桌面客户端（macOS / Linux / Windows）：结构、本地 API、邮箱账号、构建和测试 |
| [docs/windows-env.md](docs/windows-env.md) | Windows 调试虚拟机：连接、构建、测试方法和平台差异 |
| [docs/validation-v2.md](docs/validation-v2.md) | 自动化测试、本机演示、三机跨公网实测 |
| [docs/validation-v2-distributed.md](docs/validation-v2-distributed.md) | 7 机跨三大洲实测：功能、性能、故障注入、长稳 |
| [docs/validation-v2-mysql.md](docs/validation-v2-mysql.md) | 门户和协调器迁到 MySQL 后的复测 |
| [docs/test-env.md](docs/test-env.md) | 分布式测试环境（主机、访问路径、脚本） |
| [docs/prod-env.md](docs/prod-env.md) / [docs/dev-env.md](docs/dev-env.md) | 生产与 dev 环境：主机、域名、DNS、证书、部署命令 |
| [docs/server-layout.md](docs/server-layout.md) | 服务器布局与权限：每个服务一个系统用户、机密目录、运维命令、MySQL 账号、systemd 加固、部署流程 |
| [docs/security-audit.md](docs/security-audit.md) | 2026-11-09 代码级安全审计：发现、严重度、修复状态与建议顺序 |
| [docs/mail.md](docs/mail.md) | 邮件服务：注册验证码、收发、DNS、投递与安全 |
| [reports/](reports/) | 前期调研：AI token 共享平台 |

## 快速开始

```bash
go build -o bin/ ./cmd/...

# 运维：门户 + 协调器（共用 MySQL 8，先建好空库）、中继（公网机器）
export SHARETOKEN_DB_PASSWORD=…
DSN=mysql://sharetoken@127.0.0.1:3306/sharetoken
bin/portal -db $DSN pass-key          # 门户签发中继凭证的公钥 ed25519:…
bin/coordinator -db $DSN id           # 协调器 peer ID
bin/relay -config relay.yaml                   # 门户公钥和协调器 ID 写在 access.networks（configs/relay.example.yaml）；打印 portal relay add '…' 命令
bin/portal -db $DSN serve -listen :8092 -open-registration -signup-credit 5000000
#   默认 HTTPS（自签名证书，打印指纹，用户登录时用 -portal-pin 确认）
bin/portal -db $DSN relay add '/ip4/<公网IPv4>/tcp/8091/p2p/<id>,/ip6/<公网IPv6>/tcp/8091/p2p/<id>' relay-1
bin/coordinator -db $DSN \
  -reference id=gemma-4-e4b,upstream=gemma4:e4b,url=http://127.0.0.1:11434/v1   # 可选：抽检 + 精确计数

# 用户
bin/sharetoken register -user alice        # 门户地址内置为 https://sharetoken.app；测试网用环境变量 SHARETOKEN_PORTAL=https://<门户>:8092，自签名证书再加 -portal-pin sha256:…
bin/sharetoken share add gemma4:e4b -id gemma-4-e4b      # 可选：共享本机 Ollama 模型赚 credit
bin/sharetoken up                                        # http://127.0.0.1:9787/v1
bin/sharetoken status | models | balance | jobs
```

任何支持 OpenAI 协议的工具，把 base URL 改成 `http://127.0.0.1:9787/v1` 即可。非流式响应在收据验证通过后才返回（响应头 `X-Sharetoken-Verified: true`）；流式响应边到边转发，结尾附一行 `: sharetoken ... verified=true`。详细参数见 [guide-v2.md](docs/guide-v2.md)。

## 本机一键演示

```bash
scripts/demo-local.sh    # 需要 Ollama 和本地模型（默认 gemma4:e4b，可用 MODEL_UPSTREAM 指定）
```

两个中继、门户、协调器和三个用户全部跑在 127.0.0.1：注册并绑定设备、按 RTT 选中继、虚报用量的卖家被抓并按证据退款、请求自动切换到诚实卖家、抽检暂停作弊者、中继宕机后客户端自动切换、credit 在用户账户间流动且账本回放一致、所有日志里都没有 prompt。

## 测试

```bash
scripts/test.sh -race ./...   # 在 Docker 里起 MySQL 8 再跑 go test
```

覆盖签名与防篡改、计费整数运算、MySQL 账本（重启、回滚、篡改检测）、结算与证据裁决的全部路径、抽检判定、SEV-SNP（测试用 AMD 证书链）和 TDX（Intel 真实生产 quote）验证，以及基于真实 libp2p 的端到端测试：故障切换、Ticket 重放与盗用、虚报用量按证据退款、抽检抓出换模型和虚报用量的节点、只用 TEE 节点；v2 的完整拓扑测试（多中继、只经中继可达的协调器、门户共用数据库、用户互为买卖、中继宕机切换）、门户 API、设备绑定防伪。
