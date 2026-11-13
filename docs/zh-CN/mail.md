# 邮件

[English](../mail.md) | **简体中文**

邮件服务发送登录验证码和通知，并接收 `support@` 等少数地址的邮件，管理员在管理后台里查看和回复。**门户离不开它**：没有密码，所有人（包括管理员）都用发到邮箱的验证码登录。没有发件人（`mail.from`）和运行中的邮件服务，谁都登录不了。

代码：[`internal/mailer`](../../internal/mailer)（库）、[`cmd/mailserver`](../../cmd/mailserver)（服务）、[`internal/portal/mail.go`](../../internal/portal/mail.go)（门户接口）。配置样板：[`configs/mail.example.yaml`](../../configs/mail.example.yaml)。

## 结构

```
门户 ── 写 mail_outbox / mail_codes ──▶ MySQL ◀── 投递；写 mail_inbox ── mailserver ◀── 25 端口 ── 互联网
 ▲                                                                        │
 └── 管理后台 /admin/#/mail                                                └── 收件方 MX、smarthost 或 Resend API
```

- 门户和邮件服务**只通过数据库交互**。邮件服务停机期间邮件排队，登录接口照常响应，但验证码要等它恢复后才会送达。
- 邮件服务是唯一直接面对陌生人的进程。让它以单独的系统用户运行，使用单独的 MySQL 账号，且该账号只能访问 `mail_outbox`、`mail_inbox` 和 `mail_status`。这样它碰不到账本、用户和验证码，也读不到门户和协调器的密钥。除了绑定 25 端口的权限，不要给它别的权限。
- 它只接收 `smtp.mailboxes` 列出的地址，其余在 SMTP 对话中拒绝（550）。它从不转发，也不自动回复，所以不是开放中继，也不会产生反向散射。

## 部署步骤

1. 创建受限的 MySQL 账号。门户启动时会创建邮件相关的表；`mailserver -config mail.yaml migrate` 用有建表权限的账号做同样的事。
2. 把 `configs/mail.example.yaml` 复制为 `mail.yaml`，设置 `hostname`（MX 指向的名字）、`domain`、`dkim.selector`、`delivery.mode` 和 `smtp.mailboxes`。
3. 生成 DKIM 密钥，并发布下文的 DNS 记录。
4. 启动服务，并在门户上设置发件地址：`portal.yaml` 里的 `mail.from` 和 `mail.support`，或者系统设置页。

```bash
mailserver -config mail.yaml check-config          # 校验并打印生效的设置
mailserver -config mail.yaml dkim-keygen           # 生成 dkim.key_file，打印它的 TXT 记录
mailserver -config mail.yaml dns                   # 要发布的 DKIM 记录，以及对当前已发布记录的检查
mailserver -config mail.yaml migrate               # 创建邮件相关的表
mailserver -config mail.yaml serve                 # 运行
mailserver -config mail.yaml send-test you@example.net   # 排入一封测试邮件，由运行中的服务投递
```

每种投递方式都需要 DKIM 密钥：没有 `dkim.selector` 和 `dkim.key_file`，服务不会启动。

## DNS

假设一套部署以 `…@example.com` 发信，主机是 `mail.example.com`，DKIM 选择器是 `s1`：

| 记录 | 值 | 用途 |
|---|---|---|
| `MX example.com` | `10 mail.example.com` | 该域的邮件投递到哪里。收信需要。 |
| `A mail.example.com`（以及 `AAAA`） | 邮件服务器的公网地址 | MX 指向的主机。 |
| `TXT example.com` | `v=spf1 a:mail.example.com ~all` | SPF：哪些主机可以代表该域发信。使用 smarthost 或 API 服务商时，改为发布服务商要求的内容（通常是一条 `include:`）。 |
| `TXT s1._domainkey.example.com` | `mailserver dkim-keygen` 打印的内容 | DKIM 公钥。 |
| `TXT _dmarc.example.com` | `v=DMARC1; p=quarantine; rua=mailto:postmaster@example.com` | DMARC 策略。想先看报告再执行的话，从 `p=none` 开始。 |

直投时，发信地址的反向解析（PTR）还应该指向 `mail.example.com`，并且这个名字能正向解析回该地址。做不到这一点，很多收件方会拒收或降级处理。

用 `mailserver -config mail.yaml dns` 检查这些记录：它查询 MX、A、SPF、DKIM（与本机私钥比对）和 DMARC，并逐项打印 `ok` 或 `BAD`。运行中的服务每 10 分钟重复一次检查，结果显示在后台邮件部分的「服务状态」页。检查只报告，不改动任何东西。

## 投递方式

`mail.yaml` 里的 `delivery.mode`：

| 方式 | 邮件如何发出 | 适用情况 |
|---|---|---|
| `direct`（默认） | 服务连接收件域的 MX 的 25 端口，并用自己的 DKIM 密钥为每封邮件签名。 | 主机有固定地址、匹配的 PTR 记录、干净的信誉，并且出站 25 端口可用。 |
| `smarthost` | 所有邮件交给一个中继：`delivery.smarthost.addr`，587 端口（STARTTLS）或 465 端口（TLS），配合 `username` 和 `password_file`。会校验中继的证书。 | 出站 25 端口被封，或者地址没有可用的信誉（家用宽带或动态地址段），而你有一个 SMTP 中继账号。 |
| `resend` | 邮件经 [Resend](https://resend.com) 的 HTTPS API 发出，key 放在 `delivery.resend.api_key_file`。Resend 自己组装邮件并用它的 DKIM 密钥签名，所以域名要在 Resend 验证过，并发布 Resend 给出的 DNS 记录。 | 出站 SMTP 端口全部被封。很多云服务商封 25 端口，有些连 465 和 587 也封；HTTPS 总是通的。 |

说明：

- 选择 `direct` 之前，先确认主机能连上外部邮件服务器的 25 端口。云服务商经常默认封掉它。
- `resend` 方式下，每封邮件以自己的 Message-ID 作为幂等键，所以回复丢失后的重试不会发出第二份。HTTP 400 和 422 视为永久失败。401 和 403（key 或域名验证有问题）、429 以及 5xx 时，邮件留在队列里重试。读不到 key 时服务不会启动。
- 暂时性失败会在 `delivery.retry_for`（默认 48 小时）内重试。已发送的邮件保留 `delivery.keep_for`。
- 后台显示的「对方已接受」不代表用户在收件箱里看到了邮件：服务端看不到邮件被归到了哪里。登录页因此提示用户查看垃圾邮件。

要从外部验证一套部署，向你在某个大型邮件服务商的邮箱发一封测试邮件，查看它的 `Authentication-Results` 头：SPF、DKIM 和 DMARC 都应该通过。

## 登录验证码

新老账户走同一个流程：

1. `POST /api/v1/auth/code {email, invite_code?}` 发送 6 位验证码，并说明这个邮箱是不是新的（`new_account`）。对新邮箱，发信之前先检查注册规则（开放注册、设置页里允许的域名和屏蔽的地址、需要时的邀请码），所以邮件只发给能完成登录的人。对新邮箱的拒绝按 IP 限流，5 分钟 8 次，这样无法拿这些回答快速查出谁有账户。
2. `POST /api/v1/auth/verify {email, code}`（客户端，得到 60 天的 token）或 `POST /api/v1/web/auth/verify`（门户网页，得到 15 天的会话）使用验证码。新邮箱第一次登录时创建账户并发放注册赠送额度。开启了两步验证的账户还需要身份验证器里的动态码（[guide.md](guide.md) 第 4.2 节）。

| 限制 | 值 |
|---|---|
| 有效期 | 10 分钟。验证码过期后，对应的邮件不再投递。 |
| 重发间隔 | 同一邮箱 60 秒一次（`code_cooldown`）。 |
| 每小时 | 同一邮箱 5 次，同一 IP 20 次，全站 500 次（`throttled`）。 |
| 猜测 | 每个验证码 5 次；错误的验证码还按 IP 和按邮箱限流（5 分钟 8 次）。 |

验证码只保存加盐哈希。邮件发出后，主题和正文从数据库中清除；排队期间，管理员在后台也看不到内容。网页、桌面客户端（经 daemon 的 `POST /api/v1/auth/code`）和命令行（`sharetoken login -email`）都使用这个流程。邮件主题是 `Your ShareToken verification code: 123456`。

## 后台里的邮件设置

发件地址、客服地址和「发送测试邮件」在 `/admin/#/settings`，保存后立即生效。**投递方式及其凭据仍在 `mail.yaml` 里**，页面上只读：邮件服务的数据库账号读不到门户的表，而且能改设置的人不应该能把所有外发邮件（包括登录验证码）改道。

其它通知：账户开启或关闭两步验证时会发一封邮件。发通知的代码调用 `mailer.Store.Enqueue`，不等待投递结果。

## 收信安全

- 来信出自陌生人。后台只显示纯文本；HTML 只提取其中的文字，从不作为 HTML 加载。附件和原文以 `application/octet-stream` 下载。
- SPF 和 DKIM 都没有在与 From 域名对齐的前提下通过的邮件，会标为「未验证」并显示警告。发件地址可以伪造：不要因为未验证邮件里的内容去操作账户。
- 限额（默认值，`mail.yaml` 的 `smtp.*`）：单封 10 MiB；每个 IP 每小时 60 封、并发连接 4 个；整个收件箱 2 GiB。超过总量后服务返回 452，对方稍后重试。
- TLS：设置了 `smtp.tls.cert_file` 和 `key_file` 时，服务使用该证书，证书续期后无需重启即可生效；没有设置时使用自签名证书，同样加密。

## 管理后台

`/admin/#/mail` 有收件箱、已归档、退信、发件箱、服务状态和写邮件。邮件可以回复（从客服地址发出）、归档、删除，可以下载原文和附件。发送失败的邮件可以重新发送；验证码邮件不能重发，用户重新获取即可。

## 测试

```bash
scripts/test.sh -race ./internal/mailer ./internal/portal ./internal/client   # 真实 SMTP 往返、DKIM、限流、验证码、管理接口
node scripts/mail_ui_test.mjs                                                 # 无头 Chrome + 真实进程：验证码登录、恶意邮件、后台全流程
```

测试的前置条件见 [development.md](development.md)。
