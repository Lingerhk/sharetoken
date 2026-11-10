# 邮件服务

发送注册验证码和通知，接收 `support@` 等地址的邮件，在管理后台查看和回复。代码：`mailer/`（模块）、`cmd/mailserver`（服务）、`portal/mail.go`（门户接口）。

## 结构

```
门户 ── 写 mail_outbox / mail_codes ──▶ MySQL ◀── 投递、收信写 mail_inbox ── mailserver ◀── 25 端口 ── 互联网
 ▲                                                    │
 └── 管理后台 /admin/#/mail                            └── 直投收件方 MX（或 smarthost、Resend API）
```

- 门户和 mailserver **只通过数据库交互**。mailserver 宕机时邮件排队，注册接口不受影响。
- mailserver 是唯一直接面对陌生人的进程，所以有自己的系统用户 `st-mail` 和单独的 MySQL 账号，只能访问 `mail_outbox`、`mail_inbox`、`mail_status`，碰不到账本、用户和验证码表，也读不到门户和协调器的密钥。systemd 只给它绑定 25 端口的权限（[server-layout.md](server-layout.md)）。
- 只收 `smtp.mailboxes` 列出的地址，其他在 SMTP 对话中拒绝（550）。不转发，不自动回复，所以不是开放中继，也不会产生反向散射。

## 环境

| | prod（云主机） | dev（armbian） |
|---|---|---|
| 发件地址 | `team@sharetoken.app` | `team-dev@sharetoken.app` |
| 反馈地址 | `support@sharetoken.app` | `support-dev@sharetoken.app` |
| DKIM 选择器 | `st1` | `stdev1` |
| 配置 | `scripts/prod/mail.yaml` | `scripts/dev/mail.yaml` |
| 密钥 | Mac `~/st-test/prod-keys/{dkim.key,mail-db.pass,resend.key}`；服务器 `/etc/sharetoken/mail/{dkim.key,db.pass,resend.key}`（st-mail 0600），证书由 certbot 钩子复制到 `/etc/sharetoken/mail/tls/` | Mac `~/st-test/dev-keys/{dkim.key,mail-db.pass}`；armbian `/etc/sharetoken-dev/mail/` |
| 发信 | Resend HTTPS API（`delivery.mode: resend`） | 从家宽 IP 直投 |
| 互联网收信 | 主机 25 端口（ufw 放行） | 只听回环，互联网不可达 |

MX 只有一个，所以外部发给 `team-dev@`、`support-dev@` 的邮件（比如 dev 邮件的退信）进的是 **prod** 的收件箱，后台可按收件地址区分。

部署：

```bash
scripts/dev/deploy.sh --bins          # dev：门户、协调器、邮件服务
scripts/prod/deploy.sh --mail-server  # prod：只更新邮件服务
scripts/prod/deploy.sh --mail         # prod：门户 + 邮件服务，协调器不动
```

## DNS（GoDaddy）

`scripts/mail_dns.py`（`-n` 只看不改）写入，可重复运行：

| 记录 | 值 |
|---|---|
| `MX @` | `10 mail.sharetoken.app` |
| `A mail` | 生产主机 165.227.3.194（静态） |
| `TXT @` | `v=spf1 a:dev.sharetoken.app ~all`（dev 从家宽 IP 直投；prod 的 SPF 由 Resend 的退信域 `send` 承担） |
| `TXT resend._domainkey` | Resend 的 DKIM 公钥（prod 发信由 Resend 签名） |
| `CNAME send`、`CNAME rsend` | `send.forge.rmta.net`、`rsend.forge.rmta.net`（Resend 的退信域和 SPF） |
| `TXT st1._domainkey`、`stdev1._domainkey` | DKIM 公钥 |
| `TXT _dmarc` | GoDaddy 默认（`p=quarantine`），未改动 |

检查：`st-mailserver dns`（服务器上的包装脚本，以 st-mail 运行），或后台“服务状态”页（每 10 分钟自动检查一次）。dev 的家宽 IP 变化后 SPF 最多滞后约 20 分钟，因为用的是 `~all`，且 DKIM 不依赖 IP，DMARC 仍能通过。

## 发信路径

**prod：Resend。** DigitalOcean 封了出站 25、465、587（2026-11-09 实测），所以 prod 配 `delivery.mode: resend`，用官方 Go SDK（`github.com/resend/resend-go/v2`，`mailer/resend.go`）走 HTTPS。Resend 自己组装邮件，并用 `resend._domainkey` 签 DKIM，我们的 `st1` 密钥在 prod 上不参与发信。每封信以我们的 Message-ID 作幂等键，重试时丢了回复也不会发出第二份。HTTP 400、422 视为永久失败；401、403（key 或域名验证出问题）、429、5xx 留在队列里重试。

API key 在 Mac 的 `~/st-test/prod-keys/resend.key`（Resend 后台名为 `sharetoken-app-full`，Full access），`scripts/prod/deploy.sh --mail-server` 每次都会重新安装，换 key 只要改这个文件后再部署一次。没有 key，邮件服务不会启动。Resend 后台的域名 `sharetoken.app` 已于 2026-11-10 验证通过（区域 us-east-1，只开发信，收信仍由我们的 MX 处理）；它的三条记录也由 `scripts/mail_dns.py` 维护。

**dev：家宽直投。** 发信 IP 是 StarHub 家宽动态地址，在 Spamhaus PBL 里，PTR 也没有正向解析（2026-11-08 Port25 实测：SPF、DKIM 通过，iprev 失败）。Gmail 实测（2026-11-09）能收到，但进垃圾邮件。dev 上这样可以接受。

后台的“对方已接受”不代表用户在收件箱里看到了，服务端看不到邮件被归到哪里。注册页因此提示用户查看垃圾邮件。

## 注册验证码

1. `POST /api/v1/register/code {email}`：邮箱已注册时发“已注册”通知而不发验证码，响应完全相同，不泄露谁注册过。
2. `POST /api/v1/register {email, password, code}`：`accounts.email_verification: true` 时必填，同时关闭用户名注册。

| 限制 | 值 |
|---|---|
| 有效期 | 10 分钟；邮件过期后不再投递 |
| 重发间隔 | 同一邮箱 60 秒（`code_cooldown`） |
| 每小时 | 同一邮箱 5 次，同一 IP 20 次，全站 500 次（`throttled`） |
| 猜测 | 每个验证码 5 次，错误次数按 IP 再限流 |

验证码只存加盐哈希。邮件发完后主题和正文从数据库清除，排队期间管理员在后台也看不到。网页、桌面客户端（`POST /api/register/code`）和 CLI（`sharetoken register -email`）都已接入。

## 找回密码

`POST /api/v1/password/forgot {email}` 的响应和耗时都不取决于邮箱是否有账户（实际工作在响应之后做）。有账户才发信，链接是 `<门户公网地址>/app/#reset=<token>`：256 位随机数，只存哈希，放在 URL 片段里所以不进服务器日志，30 分钟有效、只能用一次，新链接使旧链接失效。限制：同一账户 60 秒一次、每小时 5 次，全站每小时 500 次，每 IP 按登录限流规则。

`POST /api/v1/password/reset {token, new_password}` 成功后吊销该账户全部 token 和会话，发通知邮件，并写操作日志。管理员的两步验证不变，只拿到邮箱进不了控制台。用户名不是邮箱的账户收不到链接，由管理员重置。

重置邮件和验证码一样标记为敏感：发完后内容从数据库清除，排队期间后台看不到。需要在系统设置里开启，并有门户公网地址和发件地址。

## 系统设置里的邮件项

发件地址、客服地址和“发送测试邮件”在 `/admin/#/settings`，保存后立即生效。**投递方式（直投或 smarthost）和它的密码仍在 `mail.yaml`**，页面上只读：mailserver 的数据库账号读不到门户的表，而且能改设置的人不应该能把所有外发邮件（包括重置链接）改道。

其他通知：修改密码时发邮件。代码里发通知用 `mailer.Store.Enqueue`，不需要等投递结果。

## 收信安全

- 邮件内容来自陌生人。后台只显示纯文本，HTML 只提取文字，不会作为 HTML 加载。附件和原文一律以 `application/octet-stream` 下载。
- 没有通过 SPF 或 DKIM（且与 From 域名对齐）的邮件标“未验证”并显示警告。回复地址可以被伪造，不要按未验证邮件的内容操作账户。
- 限额：单封 10 MiB，每 IP 每小时 60 封、并发 4，总量 2 GiB。超过总量后返回 452，对方稍后重试。

## 管理后台

`/admin/#/mail`：收件箱、已归档、退信、发件箱、服务状态、写邮件。可以回复（从客服地址发出）、归档、删除、下载原文和附件，失败的邮件可以重新发送。验证码邮件不能重发，用户重新获取即可。

## 测试

```bash
scripts/test.sh -race ./mailer ./portal ./client     # 真实 SMTP 往返、DKIM、限流、验证码、管理接口
node scripts/mail_ui_test.mjs                # 无头 Chrome + 真实进程：注册、恶意邮件、后台全流程
```

外部验证：`st-mailserver send-test check-auth@verifier.port25.com`，报告会回信到发件地址，同时验证了互联网收信。
