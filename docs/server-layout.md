# 服务器布局与权限（门户 / 协调器 / 邮件服务）

> 2026-11-09 起 prod（armbian）和 dev（树莓派）都按这套布局运行，由 `scripts/ops/st-host-setup.sh` 安装。主机、域名、证书等各环境自己的事实见 [prod-env.md](prod-env.md) 和 [dev-env.md](dev-env.md)；为什么这样做见 [security-audit.md](security-audit.md) 第 6 条。

## 1. 原则

- **一个进程一个系统用户**，只读得到自己的配置和机密。门户被攻破拿不到协调器的网络身份、DKIM 私钥、别的数据库账号和 DNS token。
- **机密不在任何登录账号的家目录里**。部署账号（`s0nnet`）只往暂存目录上传文件；装到位、改属主、重启都由 root 的安装脚本做。
- **二进制归 root、只读**；服务进程除下载区外不能写任何目录。
- **数据库账号按进程分开**，MySQL root 密码只有 root 能读，且不经过任何进程的命令行。

## 2. 布局

dev 的根目录带 `-dev` 后缀（`/srv/sharetoken-dev`、`/etc/sharetoken-dev`、`/var/lib/sharetoken-dev`），单元名带 `dev-`，其余相同。

| 路径 | 属主 / 权限 | 内容 |
|---|---|---|
| `/srv/sharetoken/releases/<版本>/{portal,coordinator,mailserver}` | root 0755 | 二进制。最多保留 5 个版本 |
| `/srv/sharetoken/{portal,coordinator,mailserver}` | root 软链 | 当前版本；上一版在 `<程序>.prev`，回滚就是把它换回来 |
| `/srv/sharetoken/ddns.sh` | root 0755 | prod：`scripts/godaddy_ddns.sh` |
| `/etc/sharetoken/portal/` | root:st-portal 0750 | `portal.yaml`（0640）、`db.pass`、`portal-pass.key`（st-portal 0600） |
| `/etc/sharetoken/coordinator/` | root:st-coord 0750 | `coordinator.yaml`、`db.pass`、`coordinator.key` |
| `/etc/sharetoken/mail/` | root:st-mail 0750 | `mail.yaml`、`db.pass`、`dkim.key`、`tls/{fullchain,privkey}.pem`（certbot 钩子复制进来） |
| `/etc/sharetoken/ddns/ddns.env` | st-ddns 0600 | prod：GoDaddy token |
| `/etc/sharetoken-dev/dbtunnel/` | root:st-dbtunnel 0750 | dev：到 armbian MySQL 的隧道私钥和 known_hosts |
| `/var/lib/sharetoken/portal/downloads/` | st-portal 0755 | 门户唯一可写的目录（systemd `StateDirectory`）：客户端安装包 |
| `/var/lib/sharetoken/ddns/ddns.log` | st-ddns | prod：DDNS 日志 |
| `/root/sharetoken/mysql-root.pass` | root 0600 | prod：MySQL root 密码 |
| `/root/sharetoken/migrated-<时间>/` | root 0700 | 迁移时从旧目录搬走的机密副本，确认稳定后删除 |
| `/var/backups/sharetoken/` | root 0700 | prod：每夜 `mysqldump`，保留 7 份 |
| `/etc/cron.d/sharetoken` | root | prod：DDNS 每 10 分钟（st-ddns）、备份每天 03:17（root） |
| `/usr/local/sbin/st-mysql-root`、`st-mysql-backup` | root 0700 | prod：见 §4 |
| `/usr/local/bin/st-portal`、`st-coordinator`、`st-mailserver` | root 0755 | 运维命令包装，见 §3 |
| `~/sharetoken-prod`、`~/sharetoken-dev` | 部署账号 | **暂存目录**：上传的二进制、yaml、单元、脚本。不放机密 |

密钥文件必须是服务用户所有且 0600：`p2p.LoadOrCreateKeys` 拒绝组或他人可读的 key，`config.Secret` 对宽权限的密码文件告警。yaml 里的所有路径都写绝对路径。

系统用户都是 `useradd --system --shell /usr/sbin/nologin`，不在 `sudo`、`docker` 组：`st-portal`、`st-coord`、`st-mail`，prod 另有 `st-ddns`，dev 另有 `st-dbtunnel`。

## 3. 运维命令

二进制读的是 `/etc/sharetoken/<服务>/` 下的配置和机密，所以运维子命令要以服务用户跑。包装脚本替你做这件事（非 root 调用时先 `sudo` 自己，再 `runuser -u <服务用户>`）：

```bash
st-portal users | overview | relay ls | grant alice 1000000 "test" | version-policy set …
st-coordinator nodes | relays | disputes | check | balance
st-mailserver dns | send-test check-auth@verifier.port25.com
sudo SHARETOKEN_PASSWORD=… st-portal useradd -admin root     # 需要环境变量时这样传
```

armbian 的 sudoers 只允许切到 root（`(root) NOPASSWD: ALL`），`sudo -u st-portal …` 会被拒绝，所以一律用包装脚本或 `sudo runuser -u st-portal -- …`。`scripts/functest.py` 也走包装脚本。

## 4. MySQL

| 账号 | 用途 | 密码在 |
|---|---|---|
| `sharetoken_portal` / dev `sharetoken_dev_portal` | 门户，`ALL ON <库>.*` | 服务器 `/etc/sharetoken/portal/db.pass`；Mac `~/st-test/{prod,dev}-keys/portal-db.pass` |
| `sharetoken_coord` / dev `sharetoken_dev_coord` | 协调器，`ALL ON <库>.*` | `…/coordinator/db.pass`；Mac `coord-db.pass` |
| `sharetoken_mail` / dev `sharetoken_mail_dev` | 邮件服务，只有 `mail_outbox`、`mail_inbox`、`mail_status` 的 DML 和 `schema_migrations` 的 SELECT | `…/mail/db.pass`；Mac `mail-db.pass` |
| `sharetoken`、`sharetoken_dev` | 2026-11-09 前的共用账号，**已不使用**，可以 `DROP USER` | — |

root 操作一律经 `sudo st-mysql-root`：它读 `/root/sharetoken/mysql-root.pass`，用 `docker exec -e MYSQL_PWD`（只传变量名）把密码交给容器里的 mysql 客户端，密码不出现在宿主机或容器的任何进程参数里。

```bash
sudo st-mysql-root -e "select version()"
printf "GRANT …;\n" | sudo st-mysql-root
sudo st-mysql-backup          # 手动备份一次到 /var/backups/sharetoken/
```

dev 的库在同一个 MySQL 实例里（`sharetoken_dev`），所以 dev 部署脚本建账号时也是 ssh 到 armbian 调 `st-mysql-root`。

## 5. systemd 单元

三个服务单元（`scripts/{prod,dev}/*.service`）共用一组加固：`NoNewPrivileges`、`ProtectSystem=strict`、`ProtectHome=yes`、`PrivateTmp`、`PrivateDevices`、`ProtectKernel{Tunables,Modules,Logs}`、`ProtectControlGroups`、`ProtectClock`、`ProtectHostname`、`RestrictNamespaces`、`RestrictRealtime`、`RestrictSUIDSGID`、`LockPersonality`、`MemoryDenyWriteExecute`、`SystemCallArchitectures=native`、`SystemCallFilter=@system-service`（被拦的调用返回 `EPERM` 而不是杀进程）、`UMask=0077`。

| 单元 | 用户 | 特别之处 |
|---|---|---|
| `sharetoken-portal` | st-portal | `StateDirectory=sharetoken/portal`；`CapabilityBoundingSet=`（空）；dev 另有 `IPAddressAllow`，只让 armbian 和本机访问 8092 |
| `sharetoken-coordinator` | st-coord | 不写磁盘；`RestrictAddressFamilies` 多一个 `AF_NETLINK`（libp2p 枚举网卡） |
| `sharetoken-mail` | st-mail | `AmbientCapabilities=CAP_NET_BIND_SERVICE` 绑 25 端口，再无其它 capability |
| `sharetoken-dev-dbtunnel` | st-dbtunnel | dev：`ssh -N` 到 armbian 的 3306，密钥在 `authorized_keys` 里限制为 `command="/usr/bin/false",restrict,port-forwarding,permitopen="127.0.0.1:3306"` |

`systemd-analyze security <单元>` 约 1.7（OK）。

## 6. 部署流程

`scripts/prod/deploy.sh` 和 `scripts/dev/deploy.sh` 在 Mac 上运行：编译、把二进制放进暂存目录的 `releases/<版本>/`、上传 yaml / 单元 / 安装脚本，需要时生成或上传机密（只在服务器上还没有时），然后：

```bash
sudo bash ~/sharetoken-prod/st-host-setup.sh prod ~/sharetoken-prod install <版本>            # 首次或全量
sudo bash ~/sharetoken-prod/st-host-setup.sh prod ~/sharetoken-prod bins <版本> portal mailserver   # 只装这些程序并重启
sudo bash … verify                                                                           # 自检
```

`install` 建用户和目录、把暂存目录里的机密装到位（已有的绝不覆盖；搬走的原件留在 `/root/sharetoken/migrated-<时间>/`）、装二进制、配置、单元、包装脚本、cron、certbot 钩子，重启后自检：每个服务用户只能读自己的机密，没有一个在 docker 组。`scripts/prod/deploy.sh --migrate` 不编译，沿用服务器上正在运行的版本，只做布局迁移；可重复运行。

安装包：`scripts/publish-packages.sh <版本> <user@host> /var/lib/sharetoken/portal/downloads --owner st-portal`（dev 加 `--with-dev-cli`）先传到部署账号家目录的 `st-downloads.new`，校验后经 sudo 以 st-portal 属主装进下载区。

回滚一个程序：`cd /srv/sharetoken && sudo systemctl stop sharetoken-portal && sudo mv portal.prev portal && sudo systemctl start sharetoken-portal`。

## 7. 有意没做的

- **部署账号 `s0nnet` 仍有免密 sudo，armbian 上仍在 docker 组。** 它是这两台个人机器上的管理账号，收紧需要另一个管理员账号；服务已不以它运行，门户被攻破不再等于拿到它。要做的话：另建管理员，给 `s0nnet` 一份只含 `systemctl start|stop|restart sharetoken-*`、`install` 到 `/srv/sharetoken` 和 `/etc/sharetoken`、`nginx -t`/`reload`、`certbot`、`st-mysql-root` 的 sudoers 白名单，再 `gpasswd -d s0nnet docker`。
- GoDaddy token 无法限定作用域，泄露即可改全部 DNS。要彻底降低影响面得把 DNS 迁到支持按 zone 授权的服务商。
- 旧的共用数据库账号 `sharetoken`、`sharetoken_dev` 还没删。
