# 用 Docker Compose 自建部署

[English](../self-hosting.md) | **简体中文**

仓库根目录的 `docker-compose.yml` 可以在一台机器上启动完整的 ShareToken 网络：MySQL 8、门户、协调器和一个中继。它适合试用项目和小规模的私有部署。如果要逐个程序手工安装，见 [guide.md](guide.md)。

## 1. 启动

只需要带 Compose 插件的 Docker：镜像会从当前代码构建各个程序。

```bash
cp .env.example .env
# 编辑 .env：DB_PASSWORD、DB_ROOT_PASSWORD、ADMIN_EMAIL、MAIL_FROM、PUBLIC_HOST
docker compose up -d --build
docker compose ps
```

`PUBLIC_HOST` 是客户端访问这台机器用的地址，可以是 IP 地址或域名。中继对外公告的是它，门户的自签名证书也签给它。默认值 `127.0.0.1` 只对本机上的客户端有效。

客户端必须能访问的端口：

| 端口 | 程序 | 设置项 |
|---|---|---|
| 8092/tcp | 门户（HTTPS） | `PORTAL_PORT`、`PORTAL_BIND` |
| 4001/tcp 和 4001/udp | 中继（TCP 和 QUIC） | `RELAY_PORT` |

协调器和 MySQL 不对外发布端口。

## 2. 各个服务

| 服务 | 运行 | 状态（卷） |
|---|---|---|
| `mysql` | MySQL 8，带各程序要求的设置（`utf8mb4_bin`、READ-COMMITTED） | `mysql` |
| `init` | 每次 `up` 运行一次后退出。三个配置文件不存在时写出它们，创建密钥，把中继登记到门户，创建管理员。 | 下面三个卷 |
| `portal` | `portal serve` | `portal`：`portal.yaml`、`portal-pass.key`、`portal-tls/` |
| `relay` | `relay` | `relay`：`relay.yaml`、`relay.key` |
| `coordinator` | `coordinator serve` | `coordinator`：`coordinator.yaml`、`coordinator.key` |

协调器不监听任何地址。它主动连接登记在门户里的中继，别人经中继访问它。在这个 Compose 文件里它和中继容器共用网络，所以从内部用中继的公网地址也能连到中继。

`init` 会打印把整个网络连起来的三个公开值：

```bash
docker compose logs init
# init: portal pass key ed25519:…      中继接受用它签发的凭证
# init: coordinator    12D3KooW…       中继允许这个 peer 不凭凭证预约
# init: relay          12D3KooW…       已登记到门户
```

## 3. 连接客户端

门户启动时使用自签名证书，并打印它的指纹。客户端要固定这个指纹：

```bash
docker compose logs portal | grep sha256:
```

用普通的 `go build` 从本仓库构建出来的客户端是开发版：状态目录是 `~/.sharetoken-dev`，门户地址取自 `SHARETOKEN_PORTAL`。

```bash
go build -o bin/ ./cmd/sharetoken
export SHARETOKEN_PORTAL=https://<PUBLIC_HOST>:8092
bin/sharetoken login -email alice@example.com -portal-pin sha256:<指纹>
bin/sharetoken up                       # 开发版的端点是 http://127.0.0.1:9887/v1
bin/sharetoken status
```

想看到一笔交易，就再启动一个用户，给它单独的状态目录（`SHARETOKEN_HOME`），一个共享模型，另一个调用。手头没有真实的模型服务时，可以用 `scripts/dev/mock_upstream.py` 代替：

```bash
python3 scripts/dev/mock_upstream.py 18611 mock-model &
bin/sharetoken share upstream http://127.0.0.1:18611/v1
bin/sharetoken share add mock-model
```

网页在 `https://<PUBLIC_HOST>:8092/`（官网）、`/app/`（用户页面）和 `/admin/`（管理控制台）。`.env` 里设置的管理员（`ADMIN_EMAIL`）在那里用邮件验证码登录，第一次登录时绑定两步验证。

**登录验证码。** 没有密码：所有人都用发到邮箱的 6 位验证码登录，一个邮箱第一次登录时创建账户。门户只把邮件放进队列；这个 Compose 文件不启动邮件服务（配置见 [mail.md](mail.md)）。在它运行之前，可以从队列里读出验证码，在自己机器上试用时够用：

```bash
docker compose exec mysql sh -c 'mysql -usharetoken -p"$MYSQL_PASSWORD" sharetoken -N \
  -e "SELECT to_addr, subject FROM mail_outbox ORDER BY id DESC LIMIT 5"'
```

必须设置 `MAIL_FROM`，否则验证码根本不会进入队列。

## 4. 设置

`.env` 保存 Compose 文件的设置。`init` **只在配置文件还不存在时**把它们写成配置文件，所以之后再改这些变量不会重写文件，要改就直接改文件。

| 变量 | 默认值 | 含义 |
|---|---|---|
| `DB_PASSWORD`、`DB_ROOT_PASSWORD` | 无，必填 | MySQL 密码 |
| `PUBLIC_HOST` | `127.0.0.1` | 客户端使用的地址 |
| `PORTAL_PORT`、`PORTAL_BIND` | `8092`、`0.0.0.0` | 门户发布在哪里 |
| `RELAY_PORT` | `4001` | 中继的端口，容器内外相同 |
| `PORTAL_TLS_MODE` | `auto` | `auto`：自签名证书。`proxy`：由反向代理终结 TLS |
| `MAIL_FROM`、`MAIL_SUPPORT` | 空 | 门户邮件的发件人和客服地址。登录验证码靠邮件发送：没有 `MAIL_FROM` 谁都登录不了 |
| `OPEN_REGISTRATION` | `true` | 没有账户的邮箱第一次登录时创建账户 |
| `SIGNUP_CREDIT_MICROS` | `.env.example` 里是 `5000000` | 新用户的初始额度（1 credit = 1,000,000 micro-credit） |
| `ADMIN_EMAIL` | 空 | 第一个管理员的邮箱；设置了才会创建这个账户 |
| `FEE_BPS` | `500` | 平台抽成，单位是基点 |
| `ST_VERSION`、`ST_COMMIT` | 空 | 写进程序的版本（默认 `<VERSION>-dev.docker`） |

修改配置文件的做法是拷出来，改好，再拷回去：

```bash
docker compose cp portal:/var/lib/sharetoken/portal/portal.yaml .
docker compose cp portal.yaml portal:/var/lib/sharetoken/portal/portal.yaml
docker compose restart portal
```

每个配置项的说明见 [configuration.md](configuration.md) 和 `configs/*.example.yaml`。

运维命令在容器里执行：

```bash
docker compose exec portal portal -config /var/lib/sharetoken/portal/portal.yaml overview
docker compose exec portal portal -config /var/lib/sharetoken/portal/portal.yaml grant alice 1000000 "welcome"
docker compose exec coordinator coordinator -config /var/lib/sharetoken/coordinator/coordinator.yaml nodes
```

## 5. 试用之后

**正式证书。** 在门户前面放一个带公共 CA 证书的反向代理（Caddy、nginx），在 `portal.yaml` 里设 `tls.mode: proxy`（或者在第一次启动前设 `PORTAL_TLS_MODE=proxy`），让门户只发布在回环地址上（`PORTAL_BIND=127.0.0.1`）。这样客户端就不需要指纹了。

**抽检和精确的 token 计数。** 在 `coordinator.yaml` 的 `references` 里为每个要抽检的模型加一个参考副本，也就是协调器能访问到的 OpenAI 兼容服务。没有参考副本就没有抽检，争议时用估算的 token 数。

**更多中继。** 在其他机器上运行 `relay`，它的 `relay.yaml` 里写同一个门户公钥和协调器 ID（`docker compose logs init` 会打印这两个值），然后用 `portal relay add '<地址>' <名字> <地区>` 逐个登记。中继启动时会打印完整的命令。客户端按往返延迟在中继之间选择，卖家会在不止一个中继上预约。

**邮件。** 每次登录都需要邮件服务，这个 Compose 文件不启动它。见 [mail.md](mail.md)。

**以后修改 `PUBLIC_HOST`。** 中继的地址存在两个地方。修改 `relay.yaml` 里的 `network.announce`，删掉中继的登记（`portal relay ls`、`portal relay rm <id>`），再执行一次 `docker compose up -d`，`init` 会用新地址重新登记。

## 6. 需要备份什么

- `mysql` 卷（账户、账本、作业）；
- `portal-pass.key`：中继信任它的公钥，默认情况下管理员两步验证的密钥也用它派生出的密钥加密；
- `portal-tls/`：客户端固定了自签名证书时；
- `coordinator.key`：中继按协调器的 ID 放行它；
- `relay.key`：中继的地址里包含它的 ID。

`docker compose down` 保留这些卷。`docker compose down -v` 会删除它们，所有账户和余额也随之消失。

## 7. 安全说明

- `init` 容器为了把网络连起来，挂载了全部三个密钥卷。分机部署时请手工完成这些步骤，这样任何一台主机都不会持有另一个程序的密钥（[guide.md](guide.md)）。
- MySQL 只在 Compose 网络内部可达，请保持这样。
- `OPEN_REGISTRATION=true` 加上注册赠送额度，意味着任何人都能创建持有 credit 的账户。每个账户只需要一个邮箱。面向公网时，请在设置页限制允许的邮箱域名或要求邀请码，或者关闭注册、改由管理员创建用户。
- 网络保护什么、不保护什么，见 [security.md](security.md)。
