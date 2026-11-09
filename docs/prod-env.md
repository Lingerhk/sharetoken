# 生产环境（prod-env）

维护说明：本文只记录公开信息（域名、IP、端口、记录）。登录凭据、API token、密钥不要写入本文，
统一放在被 `.gitignore` 忽略的文件中（`config.env`、`docs/test-env.secrets.md`）。

最后更新：2026-11-09（发布 1.0.0；生产从 armbian 迁到云主机）

## 域名

- 域名：`sharetoken.app`，注册商与 DNS 托管均为 GoDaddy。
- 权威 NS：`ns51.domaincontrol.com`、`ns52.domaincontrol.com`。
- 管理凭据：`config.env` 中的 `GODADDY_ACCESS_TOKEN`（不入库）。

## 中继节点（默认端口 4001，TCP + QUIC）

| 节点 | 主机名 | IPv4 | IPv6 | TTL |
|---|---|---|---|---|
| US | relay-us.sharetoken.app | 165.227.115.27 | 2604:a880:800:14:0:3:d0f4:b000 | 600 |
| SG | relay-sg.sharetoken.app | 201.79.122.100 | 2400:6180:0:d2:0:3:50d5:3000 | 600 |
| EU | relay-eu.sharetoken.app | 164.92.157.227 | 2a03:b0c0:2:f0:0:2:4191:8001 | 600 |
| CN | relay-cn.sharetoken.app | 119.29.250.173 | 无（暂未开通） | 600 |
| CN 2 | relay-cn2.sharetoken.app | 101.43.2.87 | 无（暂未开通） | 600 |

说明：
- CN、CN 2 为腾讯云 ECS，2026-11-08 实测网卡上只有 `fe80::` 链路本地地址，没有公网 IPv6。
  因此这两个主机名暂不配置 AAAA 记录，避免双栈客户端先超时再回退。
- 开通 IPv6 后需要补 AAAA 记录，并同步更新本表。

## 当前 DNS 记录（sharetoken.app）

| 类型 | 名称 | 值 | TTL | 备注 |
|---|---|---|---|---|
| A | relay-us | 165.227.115.27 | 600 | 本次新增 |
| AAAA | relay-us | 2604:a880:800:14:0:3:d0f4:b000 | 600 | 本次新增 |
| A | relay-sg | 201.79.122.100 | 600 | 本次新增 |
| AAAA | relay-sg | 2400:6180:0:d2:0:3:50d5:3000 | 600 | 本次新增 |
| A | relay-eu | 164.92.157.227 | 600 | 2026-11-09 新增 |
| AAAA | relay-eu | 2a03:b0c0:2:f0:0:2:4191:8001 | 600 | 2026-11-09 新增 |
| A | relay-cn | 119.29.250.173 | 600 | 本次新增 |
| A | relay-cn2 | 101.43.2.87 | 600 | 本次新增 |
| A | @ | 165.227.3.194 | 600 | 生产主机（静态） |
| A | www | 165.227.3.194 | 600 | 生产主机（静态） |
| A | mail | 165.227.3.194 | 600 | 生产主机（静态）；其他邮件记录见 [mail.md](mail.md) |
| A | dev | 家宽公网 IPv4（动态） | 600 | **DDNS 维护**，armbian 上的 cron（[dev-env.md](dev-env.md)） |
| CNAME | _domainconnect | _domainconnect.gd.domaincontrol.com | 3600 | GoDaddy 默认，未改动 |
| TXT | _dmarc | GoDaddy 默认 DMARC | 3600 | GoDaddy 默认，未改动 |

中继由 prod 和 dev 共用（2026-11-08 起），只用 `scripts/relay/deploy.sh --prod <us|sg|eu|cn>` 逐台发布，见 [dev-env.md](dev-env.md)。指标和监控告警见 [relay-monitor.md](relay-monitor.md)。

## 门户与协调器（云主机）

2026-11-09 从 armbian（家宽）迁到这台 DigitalOcean 主机，迁移时库和安装包原样带过来了。

```
 用户 / 浏览器 ──https://sharetoken.app──> 165.227.3.194 nginx（Let's Encrypt，终结 TLS）──> 门户 127.0.0.1:8092
 协调器：无监听端口，只在 4 个中继上预约（出站连接）；MySQL：Docker 容器 st-mysql，127.0.0.1:3306
 邮件：25 端口直接收信；发信经 Resend 的 HTTPS API（docs/mail.md）
```

| 项 | 说明 |
|---|---|
| 主机 | DigitalOcean sfo2，`st-portal-us-01`，165.227.3.194，x86_64，1 vCPU、1 GB 内存、2 GB swap，Ubuntu 26.04。`ssh s0nnet@165.227.3.194`（只有密钥登录，root 也只能用密钥）。新主机先以 root 跑一次 `scripts/ops/st-vps-bootstrap.sh`。布局、用户、权限、运维命令见 [server-layout.md](server-layout.md)：二进制 `/srv/sharetoken/`，配置和机密 `/etc/sharetoken/<服务>/`，下载区 `/var/lib/sharetoken/portal/downloads`；`~/sharetoken-prod` 只是部署暂存目录 |
| 防火墙 | ufw 只放行 22、80、443、25/tcp；MySQL 和门户只听回环 |
| 域名 | `https://sharetoken.app`（`www` 301 到主域）。客户端 `-portal https://sharetoken.app`，**不需要 `-portal-pin`**（公共 CA 证书） |
| 门户 | systemd `sharetoken-portal`（用户 `st-portal`），`tls.mode: proxy`，只听 127.0.0.1:8092，配置 `scripts/prod/portal.yaml`；`open_registration: true`（邮箱验证码） |
| 协调器 | systemd `sharetoken-coordinator`，用户 `st-coord`，配置 `scripts/prod/coordinator.yaml`；peer ID `12D3KooWFAqCYkUS2Kt2KRWQ1mWF82Em4mRbB5nD4V7Fr5PmWfUh`（key 沿用，中继无需改动）；门户凭证公钥也沿用。**同一把 key 不能同时在两处运行** |
| MySQL | `docker run mysql:8.0`，容器名 `st-mysql`，卷 `st-mysql-data`，**只绑 127.0.0.1:3306**，`--memory 450m`、buffer pool 128M、`READ-COMMITTED`、`utf8mb4_bin`；`restart unless-stopped`。账号按进程分开（`sharetoken_portal`、`sharetoken_coord`、`sharetoken_mail`），root 操作用 `sudo st-mysql-root`，见 server-layout.md §4。部署账号不在 docker 组 |
| nginx | 配置 `/etc/nginx/conf.d/sharetoken.conf`（源：`scripts/prod/nginx-sharetoken.conf`）；80 只做 ACME 与跳转。nginx 设置 `X-Real-IP`，门户**仅在对端是回环时**采信它做登录失败限流（`portal/portal.go clientIP`） |
| 证书 | Let's Encrypt，`sharetoken.app` + `www.sharetoken.app`、`mail.sharetoken.app`，webroot（`/var/www/html`），续期用 `certbot.timer`，部署钩子重载 nginx 并把邮件证书复制给 st-mail。检查：`sudo certbot certificates`、`sudo certbot renew --dry-run` |
| DNS | `@`、`www`、`mail` 是静态 A 记录。**主机上没有 GoDaddy token，也不跑 DDNS**（公网主机被攻破不应等于全部 DNS 被改）。没有配 AAAA |
| 备份 | `/etc/cron.d/sharetoken` 每天 03:17 以 root 跑 `/usr/local/sbin/st-mysql-backup`：`mysqldump` 到 `/var/backups/sharetoken/`（root 0700），保留 7 份。**只在这台主机上，主机没了备份也没了**；异地备份待办 |
| 参考模型 | 协调器的争议裁决要 Mac 上的 `upstream_proxy`（公司采购的 API）：`scripts/testenv/tunnels.sh` 建反向隧道到这台主机的 127.0.0.1:11434/11436。隧道不在时只影响争议裁决，交易照常 |
| 版本 | 当前 1.0.0。程序是指向 `releases/<版本>/` 的软链，上一版留在 `<程序>.prev`，客户端版本策略 `min 1.0.0`、`allow_dev=false`，见 [versioning.md](versioning.md) |
| 管理员 | `root`，初始密码在 Mac 的 `~/st-test/prod-keys/admin.pass` 和 `docs/test-env.secrets.md`。运维命令：`st-portal users|overview|relay ls|grant …`，`st-coordinator nodes|relays|disputes|check`，`st-mailserver dns|send-test …`（server-layout.md §3） |

内存只有 1 GB，所以各服务都设了 `GOMEMLIMIT`（门户 150 MiB、协调器 256 MiB、邮件 64 MiB）。这够现在的流量，真有负载了要先升配。

### 部署 / 更新

```bash
scripts/prod/deploy.sh          # 首次或全量：编译 amd64、Docker MySQL、systemd、nginx + 证书、cron，最后重启服务
scripts/prod/deploy.sh --bins   # 日常发版：构建带版本的二进制，放到 releases/<版本>/，切换软链并重启
scripts/prod/deploy.sh --prepare # 换主机时：装好一切但不申请证书、不启动服务
```

部署脚本只把文件上传到暂存目录 `~/sharetoken-prod`，然后在服务器上调用 `st-host-setup.sh`（流程见 server-layout.md §6）。

脚本可重复运行（不会覆盖已有的 key、密码、证书、数据库）。需要先把 `coordinator.key`、`portal-pass.key` 放在 Mac 的 `~/st-test/prod-keys/`。
st-db 上目前还是生产的协调器 key，`scripts/testenv` 会拒绝在这种状态下启动它；压测要先换成 dev 身份（见 [dev-env.md](dev-env.md)），不再需要停生产。
生产只部署正式版本：脚本拒绝 `-dev` 构建，发版前先打 tag（见 [versioning.md](versioning.md)）。回滚：`cd /srv/sharetoken && sudo systemctl stop sharetoken-portal && sudo mv portal.prev portal && sudo systemctl start sharetoken-portal`，协调器和邮件服务相同。

全新库的初始化（只做一次）：`sudo SHARETOKEN_PASSWORD=… st-portal useradd -admin root`，再 `st-portal relay add` 登记各中继（地址在 `scripts/relay/conf/*.yaml`，peer ID 见 `curl 127.0.0.1:8093`），最后设版本策略：`st-portal version-policy set -min 1.0.0 -recommended 1.0.0 -latest 1.0.0 -download-url https://sharetoken.app/#download -allow-dev=false`。

部署后的功能测试：`scripts/functest.py prod`（真实客户端 + mock 模型，几个请求，不是压测；会留下 `functest-*` 账号）。

## 变更方法（GoDaddy API）

```bash
set -a; . ./config.env; set +a
# 查询
curl -s -H "Authorization: Bearer $GODADDY_ACCESS_TOKEN" \
  https://api.godaddy.com/v1/domains/sharetoken.app/records
# 新增或替换某个名称下的某类记录（PUT 会替换该 type+name 的全部记录）
curl -X PUT -H "Authorization: Bearer $GODADDY_ACCESS_TOKEN" -H 'Content-Type: application/json' \
  -d '[{"data":"1.2.3.4","ttl":600}]' \
  https://api.godaddy.com/v1/domains/sharetoken.app/records/A/relay-xx
```

验证（直接问权威服务器，避免缓存）：

```bash
dig +short A    relay-us.sharetoken.app @ns51.domaincontrol.com
dig +short AAAA relay-us.sharetoken.app @ns51.domaincontrol.com
```

## 待办

- 腾讯云 CN、CN 2 开通公网 IPv6，然后补 AAAA 记录。
- ~~中继公布地址改为域名~~：2026-11-08 已完成。US、SG 宣告 `/dns4` + `/dns6`，CN 宣告 `/dns4/relay-cn.sharetoken.app/tcp/8091`，
  portal 中登记的地址也已改为域名（配置见 `scripts/relay/conf/*.yaml`）。
- relay-cn2（101.43.2.87）：DNS 已配，**中继尚未上线**。2026-11-08 实测：
  - SSH 用户是 `orcatermuser`（不是 s0nnet），有 sudo；Ubuntu 22.04，2 核 3.4 GB，4 GB swap。
  - 共用机器：nginx 占 80/443，**QQ 占 127.0.0.1:4001**，所以中继用 **4002**（现作实验中继，配置 `scripts/relay/conf/lab-cn2.yaml`，见 [dev-env.md](dev-env.md)）。
  - 和 CN 一样**没有公网 IPv6**，网卡上只有 fe80::。
  - 腾讯云安全组只放行了 22、80、443/tcp。需要放行 **4002/tcp 和 4002/udp**（QUIC），中继才能对外服务。
  - 从 Mac 上传很慢（约 6 KB/s），二进制要在机器上从源码编译（Go 用阿里云镜像，GOPROXY=goproxy.cn）。
- 稳定后把中继和 `@`、`www`、`mail` 记录的 TTL 从 600 调大（`dev` 因为 DDNS 要保持 600）。
- 数据库备份只在生产主机本地，需要异地备份。
- 生产库 2026-11-09 清空重建。迁移前的最后一份导出在 armbian 的 `/var/backups/sharetoken/sharetoken-final-20261109-225855.sql.gz`（root 可读）。

## 文档和安装包

门户提供 `/docs/`（用户指南，内容编译在程序里）和 `/downloads/`（客户端安装包，放在服务器的 `downloads/` 目录）。两者都经 nginx 的 `location /` 转发，nginx 不用改。只改网页、文档或发布命令行包时用 `scripts/prod/deploy.sh --portal`，协调器和邮件服务不动。
