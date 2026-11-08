# 生产环境（prod-env）

维护说明：本文只记录公开信息（域名、IP、端口、记录）。登录凭据、API token、密钥不要写入本文，
统一放在被 `.gitignore` 忽略的文件中（`config.env`、`docs/test-env.secrets.md`）。

最后更新：2026-11-08（晚：门户 + 协调器上线 armbian）

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
| A | @ | armbian 的公网 IPv4（动态，当前 27.125.141.245） | 600 | **DDNS 维护**（`scripts/godaddy_ddns.sh`） |
| A | www | 同上 | 600 | **DDNS 维护**；原来的 CNAME→@ 已被脚本删除（A 与 CNAME 不能并存） |
| A | mail | 同上 | 600 | **DDNS 维护**；邮件服务，其他邮件记录见 [mail.md](mail.md) |
| CNAME | _domainconnect | _domainconnect.gd.domaincontrol.com | 3600 | GoDaddy 默认，未改动 |
| TXT | _dmarc | GoDaddy 默认 DMARC | 3600 | GoDaddy 默认，未改动 |

中继由 prod 和 dev 共用（2026-11-08 起），只用 `scripts/relay/deploy.sh --prod <us|sg|eu|cn>` 逐台发布，见 [dev-env.md](dev-env.md)。指标和监控告警见 [relay-monitor.md](relay-monitor.md)。

## 门户与协调器（armbian）

```
 用户 / 浏览器 ──https://sharetoken.app──> 家庭路由器 WAN :80/:443 ──> armbian 192.168.6.91
                                                nginx（Let's Encrypt，终结 TLS）──> 门户 127.0.0.1:8092
 协调器：无监听端口，只在 4 个中继上预约（出站连接）；MySQL：Docker 容器 st-mysql，127.0.0.1:3306
```

| 项 | 说明 |
|---|---|
| 主机 | armbian（192.168.6.91，aarch64 2 GB）。`ssh s0nnet@192.168.6.91`；目录 `~/sharetoken-prod` |
| 域名 | `https://sharetoken.app`（`www` 301 到主域）。客户端 `-portal https://sharetoken.app`，**不需要 `-portal-pin`**（公共 CA 证书） |
| 门户 | systemd `sharetoken-portal`，`tls.mode: proxy`，只听 127.0.0.1:8092，配置 `scripts/prod/portal.yaml`；`open_registration: false` |
| 协调器 | systemd `sharetoken-coordinator`，配置 `scripts/prod/coordinator.yaml`；peer ID `12D3KooWFAqCYkUS2Kt2KRWQ1mWF82Em4mRbB5nD4V7Fr5PmWfUh`（key 沿用测试期的，中继无需改动）；门户凭证公钥也沿用 |
| MySQL | `docker run mysql:8.0`，容器名 `st-mysql`，卷 `st-mysql-data`，**只绑 127.0.0.1:3306**，`--memory 600m`、buffer pool 256M、`READ-COMMITTED`、`utf8mb4_bin`；`restart unless-stopped`。密码在 `~/sharetoken-prod/db.pass`、`mysql-root.pass`（0600） |
| nginx | 与原有 `sgvpn.ddnsfree.com` 站点共存，配置 `/etc/nginx/conf.d/sharetoken.conf`（源：`scripts/prod/nginx-sharetoken.conf`）；80 只做 ACME 与跳转。nginx 设置 `X-Real-IP`，门户**仅在对端是回环时**采信它做登录失败限流（`portal/portal.go clientIP`） |
| 证书 | Let's Encrypt，`sharetoken.app` + `www.sharetoken.app`，webroot（`/var/www/html`），续期用系统已有的 `certbot.timer`，部署钩子 `reload-nginx.sh` 重载 nginx。首次签发 2026-11-08，到期 2027-02-06，约 60 天后自动续。检查：`sudo certbot certificates`、`sudo certbot renew --dry-run` |
| DDNS | armbian 的 cron 每 10 分钟跑 `~/sharetoken-prod/ddns.sh`（即 `scripts/godaddy_ddns.sh`），日志 `ddns.log`。token 在 `~/.config/sharetoken/ddns.env`（0600）。IP 变了才写 GoDaddy |
| 备份 | cron 每天 03:17 `backup.sh`：`mysqldump` 到 `~/sharetoken-prod/backup/`，保留 7 份 |
| 参考模型 | 协调器的争议裁决要 Mac 上的 `upstream_proxy`（公司采购的 API）：`scripts/testenv/tunnels.sh` 建反向隧道到 armbian 的 127.0.0.1:11434/11436。隧道不在时只影响争议裁决，交易照常 |
| 管理员 | `root`（初始密码在 `docs/test-env.secrets.md`，首次登录后绑 MFA 并改密）。运维命令：`cd ~/sharetoken-prod; ./portal -config portal.yaml users|overview|relay ls|grant …`，`./coordinator -config coordinator.yaml nodes|relays|disputes|check` |

路由器：WAN :80、:443、:25 → 192.168.6.91 同端口（已配置；25 给邮件服务，见 [mail.md](mail.md)）。中继和协调器之间是协调器主动出站，不需要映射 4001。

### DDNS 脚本

```bash
scripts/godaddy_ddns.sh        # 探测公网 IPv4，写 sharetoken.app、www、mail 的 A 记录（TTL 600）
scripts/godaddy_ddns.sh -n     # 只看会改什么
scripts/godaddy_ddns.sh -f     # IP 没变也强制写
```

要点：token 从 `config.env`（或 `~/.config/sharetoken/ddns.env`）的 `GODADDY_ACCESS_TOKEN` 读，输出里不含 token；
IP 探测依次试 ipify / ifconfig.me / icanhazip / checkip.amazonaws.com，并拒绝私网地址；IP 未变不调用写接口；
只管 IPv4（家宽没有可用的公网 IPv6 入站，所以没有 AAAA）。可选变量 `DDNS_DOMAIN`、`DDNS_NAMES`、`DDNS_TTL`、`DDNS_IP`。
IP 变化后最长约 10 分钟（cron）+ 10 分钟（TTL）生效，期间 https://sharetoken.app 暂不可达；已在线客户端不受影响（交易走中继），
只是 1 小时后续领中继凭证会失败，所以这个窗口很短就行。

### 部署 / 更新

```bash
scripts/prod/deploy.sh          # 首次或全量：编译 arm64、Docker MySQL、systemd、DDNS、nginx + 证书、cron，最后重启服务
scripts/prod/deploy.sh --bins   # 日常发版：只更新二进制与 yaml 并重启
```

脚本可重复运行（不会覆盖已有的 key、密码、证书、数据库）。需要先把 `coordinator.key`、`portal-pass.key` 放在 Mac 的 `~/st-test/prod-keys/`（首次从原协调器机器复制）。
st-db 上目前还是生产的协调器 key，`scripts/testenv` 会拒绝在这种状态下启动它；压测要先换成 dev 身份（见 [dev-env.md](dev-env.md)），不再需要停生产。
全新库的初始化（只做一次）：`SHARETOKEN_PASSWORD=… ./portal -config portal.yaml useradd -admin root`，再 `relay add` 登记各中继（地址见 `portal relay ls` 于 st-db 的旧库，或 `scripts/relay/conf/*.yaml`）。

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
- 稳定后把中继记录的 TTL 从 600 调大（`@`/`www` 因为 DDNS 要保持 600）。
- 生产库目前是**全新的**（只迁了 key、管理员和 3 个中继）：测试期的用户、余额和压测流水都留在 st-db 的 MySQL 里，没有带过来。
