# 生产环境（prod-env）

维护说明：本文只记录公开信息（域名、IP、端口、记录）。登录凭据、API token、密钥不要写入本文，
统一放在被 `.gitignore` 忽略的文件中（`config.env`、`docs/test-env.secrets.md`）。

最后更新：2026-11-08

## 域名

- 域名：`sharetoken.app`，注册商与 DNS 托管均为 GoDaddy。
- 权威 NS：`ns51.domaincontrol.com`、`ns52.domaincontrol.com`。
- 管理凭据：`config.env` 中的 `GODADDY_ACCESS_TOKEN`（不入库）。

## 中继节点（默认端口 4001，TCP + QUIC）

| 节点 | 主机名 | IPv4 | IPv6 | TTL |
|---|---|---|---|---|
| US | relay-us.sharetoken.app | 165.227.115.27 | 2604:a880:800:14:0:3:d0f4:b000 | 600 |
| SG | relay-sg.sharetoken.app | 201.79.122.100 | 2400:6180:0:d2:0:3:50d5:3000 | 600 |
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
| A | relay-cn | 119.29.250.173 | 600 | 本次新增 |
| A | relay-cn2 | 101.43.2.87 | 600 | 本次新增 |
| A | @ | Parked | 600 | GoDaddy 默认，未改动 |
| CNAME | www | @ | 3600 | GoDaddy 默认，未改动 |
| CNAME | _domainconnect | _domainconnect.gd.domaincontrol.com | 3600 | GoDaddy 默认，未改动 |
| TXT | _dmarc | GoDaddy 默认 DMARC | 3600 | GoDaddy 默认，未改动 |

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
- 中继公布地址从 `/ip4/<ip>/...` 改为 `/dns4/relay-xx.sharetoken.app/...`（及 `/dns6/`），
  并同步 `scripts/testenv/env.sh`、`docs/test-env.md`。
- 稳定后把 TTL 从 600 调大。
