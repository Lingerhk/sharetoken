# sharetoken v2 使用与部署指南

> 架构说明见 [design-v2.md](design-v2.md)。金额单位：1 credit = 1,000,000 micro-credit；价格是「micro-credit / 百万 token」。

## 1. 普通用户

```bash
# 首次：注册（或 login），本机设备自动绑定到账户
sharetoken register -portal https://portal.example.com -user alice
#   门户用自签名证书时，加上运维公布的指纹：-portal-pin sha256:…（或按提示核对后输入 y）

# 只想用：直接启动
sharetoken up
#   OpenAI-compatible endpoint: http://127.0.0.1:9787/v1

# 想赚 credit：共享本机模型（默认上游是 Ollama http://127.0.0.1:11434/v1）
sharetoken share detect                     # 列出本机模型
sharetoken share add qwen2.5:7b             # 网络上的模型 ID 默认同名；-id 改名，-in/-out 定价
sharetoken up                               # 已在运行时 share 命令会立即生效，无需重启
```

任何 OpenAI 兼容工具把 base URL 设为 `http://127.0.0.1:9787/v1` 即可（API key 随便填）。

| 命令 | 作用 |
|---|---|
| `status` | 是否连上协调器、经哪个中继、当前中继的实时延迟/抖动/丢包（`link:` 行）、各中继 RTT 及测量时间、共享情况、告警 |
| `models` | 网络上的模型、节点数、最低价 |
| `relays [-measure]` | 中继测速结果、测量时间和当前中继的实时链路质量；`-measure` 立即重测 |
| `balance` | 余额、冻结额、买入/卖出的单数和 token 数 |
| `jobs [-role buy\|sell] [-n 20]` | 最近的作业 |
| `share` / `share on\|off\|rm <id>\|upstream <url>` | 共享设置 |
| `logout` | 吊销本机 token |
| `login/register -portal-pin sha256:…` | 信任门户的自签名证书（指纹存入配置）；`-allow-http` 允许明文门户（仅测试网） |
| `config` | 查看配置（token 打码） |

- 状态目录：`~/.sharetoken`（`$SHARETOKEN_HOME` 或 `-home` 覆盖）：`config.json`（0600，含 token）、`device.key`（设备私钥，0600，换机器就是新设备）、`network.json`（中继列表缓存）、`run.json`（运行中客户端的 API 地址）。
- 密码可从 `$SHARETOKEN_PASSWORD` 读取（脚本用）。
- `up -http 127.0.0.1:9800 -listen /ip4/192.168.1.5/tcp/0 -nat=false`：临时覆盖，不写入配置。
- 默认同时监听 IPv4 和 IPv6（`/ip4/0.0.0.0`、`/ip6/::`，TCP + QUIC）；没有 IPv6 的机器自动跳过。
- 默认不需要开任何端口：卖家在中继上预约，买家经中继或打洞连过来。

### config.json 主要字段

```json
{
  "portal": "https://portal.example.com", "portal_pin": "sha256:…",   // 自签名门户才有 pin
  "user": "alice", "token": "st_…", "device_name": "mac",
  "http": "127.0.0.1:9787", "nat": true,
  "relays": [],                                   // 非空时覆盖门户给的中继列表
  "buy":   {"budget_micros": 200000, "max_price_in": 0, "max_price_out": 0, "evidence": true,
            "require_tee": false, "tee_policy": "",
            "queue_seconds": 30, "first_byte_seconds": 120, "idle_seconds": 60},
  "share": {"enabled": true, "upstream": "http://127.0.0.1:11434/v1", "concurrency": 2, "relays": 2,
            "models": [{"id": "qwen2.5:7b", "upstream_model": "qwen2.5:7b", "price_in": 100000, "price_out": 200000}],
            "tee": "off"}                         // off | auto | sev-snp | tdx（机密虚拟机）
}
```

## 2. 运维部署

最小部署：1 台公网机器跑门户 + 协调器（共用 SQLite）+ 一个中继；更多中继放在不同网络位置。

```bash
# 先取两个值：门户签凭证的公钥、协调器的 peer ID（各自首次运行时生成密钥）
portal -db sharetoken.db pass-key             # ed25519:…
coordinator -db sharetoken.db id              # 12D3Koo…

# 中继（每台公网机器一个；云主机用 -announce 声明公网地址，IPv4 和 IPv6 都写）
relay -key relay.key -listen /ip4/0.0.0.0/tcp/8091,/ip6/::/tcp/8091,/ip4/0.0.0.0/udp/8091/quic-v1,/ip6/::/udp/8091/quic-v1 \
      -announce /ip4/203.0.113.7/tcp/8091,/ip6/2001:db8::7/tcp/8091 -status 127.0.0.1:8093 \
      -portal-key ed25519:… -allow-peer 12D3Koo…<协调器>
#   不写 -listen 时默认就是 4001 端口的 v4+v6 TCP/QUIC
#   输出 portal relay add '/ip4/203.0.113.7/tcp/8091/p2p/12D3Koo…,/ip6/2001:db8::7/tcp/8091/p2p/12D3Koo…'
#   ← 原样复制到门户；-status 提供 JSON 统计。机器没有 IPv6 时会提示「IPv6-only users cannot reach this relay」
#   只有出示门户凭证的注册用户设备（以及协调器）才能预约；不加 -portal-key 则为开放中继

# 门户（与协调器同机，同一个数据库文件）；默认 HTTPS
portal -db sharetoken.db serve -listen :8092 -open-registration -signup-credit 5000000
#   没给 -tls-cert/-tls-key 时生成自签名证书（portal-tls/，务必保留）并打印指纹 sha256:…，
#   把它公布给用户；有域名证书时：-tls-cert fullchain.pem -tls-key privkey.pem
#   明文 HTTP 只在监听 127.0.0.1 或加 -insecure-http（仅测试网）时使用
SHARETOKEN_PASSWORD=… portal -db sharetoken.db useradd -admin root
portal -db sharetoken.db relay add '/ip4/203.0.113.7/tcp/8091/p2p/12D3Koo…,/ip6/2001:db8::7/tcp/8091/p2p/12D3Koo…' relay-sh cn-east
#   一个中继一条：同一 peer ID 的多个地址用逗号连接；已登记的中继要改地址就先 relay rm 再 add

# 协调器（不监听端口；自动在所有登记的中继上预约）
coordinator -db sharetoken.db -fee-bps 500 \
  -reference id=qwen2.5:7b,upstream=qwen2.5:7b,url=http://127.0.0.1:11434/v1   # 可选：抽检与精确计数
```

| 运维命令 | 作用 |
|---|---|
| `portal useradd [-admin] <user>` / `passwd <user>` | 建用户 / 改密码 |
| `portal grant <user> <micros> [memo]` | 发放 credit（负数为扣减，不能扣到负余额） |
| `portal users` / `user <user>` / `disable\|enable <user>` | 用户管理；禁用立即生效（token 失效、设备被协调器拒绝） |
| `portal relay add\|ls\|rm\|enable\|disable` | 中继登记（地址可为 v4,v6 逗号连接，按 peer ID 去重）；协调器 15 秒内跟进 |
| `portal overview` | 用户数、流通 credit、平台手续费、冻结额、账本回放结果、各状态作业数、中继可达数 |
| `coordinator disputes\|resolve\|nodes\|audits\|relays\|check\|balance` | 同 v1，另加 `relays` 查看各中继预约状态 |

所有运维命令直接读写数据库，可以和运行中的服务同时执行。

`portal-pass.key`（门户签凭证的私钥）和 `portal-tls/` 都要备份；换 pass key 时，先给所有中继加上新公钥（`-portal-key` 可重复），再换门户的密钥，一小时后去掉旧公钥。

中继准入参数：`-portal-key`（可重复）、`-allow-peer`（可重复）、`-max-devices-per-user 4`；门户：`-pass-key portal-pass.key`、`serve -pass-ttl 1h`。

中继容量参数（默认值已远高于 libp2p 默认）：`-max-reservations 4096`、`-max-reservations-per-ip 64`、`-max-circuits 4096`（单个目标的并发线路数，协调器每个在线用户占一条）、`-max-conns 8192`。

### 2.1 门户网页

`portal serve` 同时提供两套网页，无需额外部署：

| 地址 | 面向 | 功能 |
|---|---|---|
| `https://<host>:8092/` | 用户 | 注册/登录；总览（余额、冻结、买卖汇总）；账单；作业（按买/卖筛选）；API token 的创建（明文只显示一次）与吊销；设备改名/解绑；网络（协调器、中继可达性）；改密码（可同时吊销其他会话） |
| `https://<host>:8092/admin/` | 管理员 | 平台总览（账本回放、流通额、手续费、中继、作业统计、各账户余额）；用户的搜索、新建、发放/扣减 credit、禁用/启用、设/撤管理员、重置密码、吊销 token、解绑设备、账单与作业；中继登记/编辑/启停/删除；全部作业（按状态、用户筛选）；节点信誉；审计记录；争议的人工裁决（付款给节点 / 退款给买家）；全局账本流水 |

- 浏览器会话默认 24 小时（`serve -session-ttl`），存放在 HttpOnly cookie 里，页面脚本读不到；在「API Token」页显示为「web session」并标记「当前会话」，可随时吊销。
- 管理员进入 `/admin/` 用同一个账号；非管理员打开 `/admin/` 只会看到「不是管理员」。
- 非回环地址默认 HTTPS 自签名证书：浏览器会提示证书不受信任，核对 `portal serve` 打印的指纹后继续（或给门户配 CA 证书 / 放在 Caddy、nginx 后面，后者加 `-behind-tls-proxy` 让 cookie 带 Secure）。
- 页面只调用本机 `/api/v1`，没有第三方资源，可以在没有外网的内网部署。
- 争议裁决和协调器 `coordinator resolve` 等价：门户进程直接在共用数据库里结算（手续费率取协调器启动时写入 `meta.fee_bps` 的值）。
- 自动化 UI 测试：`node scripts/portal_ui_test.mjs http://127.0.0.1:8099 ./shots`（需要本机 Chrome；见 validation-v2.md §10）。

## 3. 本地管理 API（给 GUI）

`sharetoken up` 监听 `127.0.0.1:9787`。带非本地 `Origin` 的请求返回 403。

| 方法 路径 | 说明 |
|---|---|
| `GET /api/status` | 完整状态：`peer`、`user`、`api`、`coordinator{connected,relayed,via,link{relay,samples,last_ms,avg_ms,min_ms,max_ms,jitter_ms,loss_pct,last_error,updated_at}}`（`link` 为当前中继最近 30 次 ping 的统计，每 10 秒更新，首次 ping 前不出现）、`measured_at`（上次全量测速时间）、`relays[{addr,name,region,rtt_ms,error,coordinator_ok,current,reserved}]`、`share{…配置, running, stats{jobs_served,jobs_failed,earned_micros,active_jobs,announce_error}, reservations}`、`warnings[]` |
| `GET /api/models` | 网络上的模型汇总 |
| `GET /api/account` | 门户 `/me` 透传：余额、用量、设备 |
| `GET /api/jobs?role=buy\|sell&limit=` | 门户作业列表透传 |
| `GET /api/ledger?limit=&before=` | 门户账单透传 |
| `POST /api/relays/measure` | 立即重测中继并返回结果 |
| `GET /api/share` / `PUT /api/share` | 读 / 改共享设置（body 为 `share` 配置对象，校验后立即生效并保存） |
| `POST /api/share/start` / `stop` | 开关共享 |
| `POST /api/share/reload` | 从 config.json 重新加载共享设置 |
| `GET /api/share/detect[?upstream=]` | 列出本地模型服务的模型 |
| `POST /v1/chat/completions`、`GET /v1/models` | OpenAI 兼容端点（响应头 `X-Sharetoken-Verified/Job/Node/Cost-Micros`） |

`/v1/chat/completions` 的行为要点：

- **冻结额**：请求带 `max_tokens`（或 `max_completion_tokens`）时，只按所匹配节点的价格冻结最坏情况的成本；不带时冻结整个 `budget_micros`。所以批量或高并发调用时最好设置 `max_tokens`。
- **排队**：所有节点都满载时，请求最多等待 `queue_seconds`（默认 30 秒）；仍然没有空位就返回 503 `capacity`，并带 `Retry-After`。
- **卡死保护**：节点超过 `first_byte_seconds`（默认 120 秒）没有给出第一个数据帧，或者两帧之间超过 `idle_seconds`（默认 60 秒）没有数据，就判定为卡死。非流式请求会换一个节点重试；流式请求如果已经开始输出，则以错误事件结束。慢模型处理长 prompt 时请调大这两个值。
- **中途断开**：流式请求中途断开连接（例如用户点「停止」）时，只按已经送达的部分计费。
- **错误码**：`bad_request` 400（模型服务也拒绝的无效请求会带上原因）、`budget` 400、`insufficient_funds` 402、`unauthorized`/`forbidden` 403（例如账户被禁用）、`no_offers` 503（没有节点提供该模型）、`capacity` 503（暂时满载，可重试）、`upstream_error`/`receipt_rejected` 502。
- **思考型模型**（如 gemma4）的推理过程也计入 `max_tokens`；`max_tokens` 太小时 `content` 可能为空，只有 `reasoning_content`。

## 4. 门户 HTTP API

认证：`Authorization: Bearer st_…`；门户网页改用 cookie 会话（见 §4.1）。错误统一为 `{"error":{"code":"…","message":"…"}}`。

| 方法 路径 | 认证 | 说明 |
|---|---|---|
| `GET /api/v1/info` | 无 | `{registration_open, signup_credit_micros, session_hours, passes_enabled}`（登录页用） |
| `POST /api/v1/login` 对管理员 | 无 | 管理员密码正确后不直接发 token：未绑定两步验证 → 401 `mfa_setup_required`（须到网页门户完成绑定）；已绑定 → 401 `mfa_required` + `challenge`，或在请求里一并给 `totp` 一步完成 |
| `POST /api/v1/mfa` | 无 | `{challenge, code, token_name}` → token。challenge 5 分钟有效、错 5 次作废；错码计入登录限流 |
| `POST /api/v1/web/mfa`、`…/mfa/setup`、`…/mfa/confirm` | 页面头 | 浏览器登录的第二步：验码 / 开始绑定（返回 base32 密钥、otpauth 链接、二维码 data URI）/ 用首个验证码确认绑定并建立会话 |
| `POST /api/v1/admin/users/{name}/mfa/reset` | 管理员 | 清除两步验证并吊销其全部 token（丢失验证器时的恢复手段；服务器上也可 `portal mfa-reset <user>`） |
| `GET /api/v1/network` | 无 | `{coordinator, relays:[{addr,name,region,ok,rtt_ms}], pass_key}`，可用中继在前、其中延迟低的在前；`rtt_ms` 是协调器到该中继的往返时间（每 30 秒 ping 一次，未测得时省略） |
| `POST /api/v1/register` | 无 | `{username,password,token_name?}` → `{user, token}`；需 `-open-registration` |
| `POST /api/v1/login` | 无 | 同上；连续失败限流（429） |
| `POST /api/v1/logout` | 是 | 吊销当前 token |
| `GET /api/v1/me` | 是 | `{user, balance{account,available_micros,held_micros}, usage{bought,sold}, devices}` |
| `POST /api/v1/password` | 是 | `{old_password,new_password,revoke_others?}`；连续答错当前密码同样限流 |
| `GET/POST /api/v1/tokens`、`DELETE /api/v1/tokens/{id}` | 是 | API token 管理；`POST {name, ttl_hours}` 只返回一次明文 |
| `GET/POST /api/v1/devices`、`DELETE /api/v1/devices/{peer}` | 是 | 设备；`POST {peer,name,ts,sig}`，sig 为设备对 `accounts.BindMessage` 的签名 |
| `PATCH /api/v1/devices/{peer}` | 是 | `{name}` 改名 |
| `GET /api/v1/ledger?limit=&before=` | 是 | 账单（正数入账、负数出账，含冻结/退款明细） |
| `POST /api/v1/relay-pass` | 是 | `{peer}` → 中继凭证（只给本人已绑定的设备；客户端自动调用） |
| `GET /api/v1/jobs?role=&limit=&before=` | 是 | 作业：角色、模型、状态、token 数、费用、收入 |
| `GET /api/v1/usage` | 是 | 买入/卖出汇总 |
| `GET /api/v1/admin/overview` | 管理员 | 平台汇总与账本回放 |
| `GET/POST /api/v1/admin/users`、`GET /api/v1/admin/users/{name}` | 管理员 | 用户列表、建用户、用户详情（含余额、用量、设备、token） |
| `GET /api/v1/admin/users/search?q=&limit=&offset=` | 管理员 | 按用户名搜索（通配符按字面匹配），每行带余额、冻结、设备数、token 数，返回总数 |
| `POST /api/v1/admin/users/{name}/credit` | 管理员 | `{micros, memo}`，负数扣减 |
| `POST /api/v1/admin/users/{name}/disable\|enable` | 管理员 | 禁用 / 启用（不能对自己） |
| `POST /api/v1/admin/users/{name}/admin` | 管理员 | `{is_admin}`（不能撤销自己） |
| `POST /api/v1/admin/users/{name}/password` | 管理员 | `{new_password, revoke_tokens?}` 重置密码 |
| `GET /api/v1/admin/users/{name}/tokens`、`DELETE …/tokens/{id}`、`POST …/tokens/revoke` | 管理员 | 查看 / 吊销一个 / 吊销全部 token |
| `DELETE /api/v1/admin/users/{name}/devices/{peer}` | 管理员 | 解绑设备 |
| `GET /api/v1/admin/users/{name}/ledger\|jobs` | 管理员 | 该用户的账单、作业 |
| `GET/POST /api/v1/admin/relays`、`PATCH …/{id}`、`POST …/{id}/enable\|disable`、`DELETE …/{id}` | 管理员 | 中继登记、改名/地区、启停、删除 |
| `GET /api/v1/admin/jobs?status=&user=&account=&before=&limit=` | 管理员 | 全部作业（含双方账户、争议原因与裁决） |
| `GET /api/v1/admin/nodes` | 管理员 | 节点信誉记录（附所属用户） |
| `GET /api/v1/admin/audits?limit=` | 管理员 | 审计记录 |
| `GET /api/v1/admin/disputes?limit=` | 管理员 | 待裁决在前的争议列表 |
| `POST /api/v1/admin/disputes/{job}/resolve` | 管理员 | `{decision: "pay"\|"refund"}` 人工裁决 |
| `GET /api/v1/admin/ledger?account=&before=&limit=` | 管理员 | 全局账本流水（附 user:<id> 对应的用户名） |
| `GET /api/v1/admin/accounts` | 管理员 | 所有账本账户的余额与冻结额 |

### 4.1 浏览器会话

**管理员两步验证（强制）**：管理员账号登录只凭密码不会建立会话。第一次登录时门户要求用验证器 App（Google Authenticator、Microsoft Authenticator、1Password 等）扫描二维码或输入密钥，并填入首个 6 位验证码完成绑定；之后每次登录都要填验证码（TOTP，RFC 6238，30 秒一换，同一码只能用一次，前后各容忍一个周期）。token 和会话会记住是否经过两步验证，所有 `/api/v1/admin/*` 只接受这类 token：把普通用户提升为管理员后，其现有会话会被拒（`mfa_required`），须重新登录；本功能上线前签发的管理员 token 同样失效。密钥在数据库里用门户签名私钥派生的密钥 AES-GCM 加密存放。丢失验证器：由另一位管理员在控制台「重置两步验证」，或在服务器上执行 `./portal -db st.db mfa-reset <user>`，下次登录重新绑定。

| 方法 路径 | 说明 |
|---|---|
| `POST /api/v1/web/login`、`POST /api/v1/web/register` | `{username,password}`；成功后在 `st_session` cookie（HttpOnly、SameSite=Strict、HTTPS 下 Secure）里放一个有效期 `-session-ttl` 的 token，响应体不含 token |
| `POST /api/v1/web/logout` | 吊销会话并清 cookie |

携带 cookie 的请求必须同时带请求头 `X-Sharetoken-Web: 1`，且 `Origin`（浏览器附带时）必须就是门户自己，否则 403 `cross_site`；Bearer 请求不受影响。门户不响应 CORS 预检，所以别的网站无法构造这样的请求。

## 5. 本机一键演示

```bash
scripts/demo-local.sh     # 需要 Ollama 和 gemma4:e4b（MODEL_UPSTREAM 可改）
```

两个中继、门户、协调器、三个用户（alice 诚实卖家、carol 虚报用量的卖家、bob 买家），演示注册绑定、选中继、作弊被抓并按证据退款、抽检暂停、中继宕机切换、账户间资金流动与对账、日志无 prompt。
