# 中继监控

每台中继在 `127.0.0.1` 上暴露 Prometheus 指标，同机的 cron 每分钟跑一次 `scripts/relay/monitor.py`，异常时用 Pushover 通知。

## 指标

和 JSON 统计共用 `status.listen`（生产配置是 `127.0.0.1:8093`）：`/` 是 JSON，`/metrics` 是 Prometheus 文本格式。这两个接口没有认证，所以配置校验只接受回环地址，写别的地址中继启动不了。

```
curl -s 127.0.0.1:8093/metrics | grep ^sharetoken_relay
```

| 指标（前缀 `sharetoken_relay_`） | 含义 |
|---|---|
| `info{peer_id}` | 恒为 1，标签是中继的 peer ID |
| `service_enabled` | 中继服务是否在运行 |
| `network_coordinators_reserved{network}` / `_expected{network}` | 持有有效预约的协调器数 / 配置的 `allow_peers` 数 |
| `network_reservations{network}`、`network_max_reservations{network}` | 该网络的预约数和上限（0 表示只受全局限制） |
| `network_passes_accepted_total{network}`、`network_reservations_denied_total{network}` | 该网络的通行证和超限拒绝 |
| `peers`、`connections`、`max_connections` | 对端数、连接数、连接上限 |
| `reservations`、`max_reservations`、`circuits` | 当前预约、预约上限、当前电路 |
| `reservations_granted_total`、`reservations_denied_total` | 预约的批准和拒绝 |
| `circuits_opened_total`、`circuits_denied_total`、`relayed_bytes_total` | 电路和转发字节 |
| `passes_accepted_total`、`passes_rejected_total` | 通行证 |

另外还有 libp2p 自带的 `libp2p_*`，以及 `go_*`、`process_*`。

## 监控项

连续失败才告警：默认 3 轮，TCP 探测 5 轮（每轮 1 分钟）。未恢复的每 60 分钟重发一次，恢复时通知一次。

| 检查 | 级别 | 条件 |
|---|---|---|
| `metrics` | 严重 | `/metrics` 无响应或没有中继指标（进程挂了、卡死、版本过旧） |
| `service` | 严重 | 中继服务未运行 |
| `coordinator/<网络>` | 严重（`warn_networks` 里的网络是警告，默认 dev） | 协调器没有预约，该网络的用户经这台中继连不上协调器 |
| `tcp/<地址>` | 严重 | 本机对外端口连不上。走公网域名，所以 DNS 错了也能发现；只测 TCP，不测 QUIC |
| `watch/<地址>` | 严重（`watch_severity` 可改） | 对端中继连不上 |
| `capacity/*` | 警告 | 预约、连接、文件描述符、网络预约达到上限的 80% |
| `rate/*` | 警告 | 每分钟预约被拒 ≥60、电路被拒 ≥60、通行证被拒 ≥30 |
| `cpu`、`memory`、`disk` | 警告 | 中继进程 CPU ≥90%、可用内存 <10%、磁盘已用 ≥90% |

严重用 Pushover 优先级 1，警告和恢复用 0。进程重启的通知用 -1，不响铃。一切正常时不发任何通知，所以没收到通知不能说明监控在工作：cron 或 Pushover 坏了不会有提示，要确认时登录机器跑 `monitor.py -c monitor.ini --check`，或看 `monitor.log`。

相互守望：机器彻底宕机时它发不出告警，所以 US→SG→EU→US 环形互盯，CN 和 lab-cn2 互盯（跨境丢包太重，不和境外互盯）。

监控只通知，不重启中继。进程崩溃和机器重启由 systemd 负责（`sharetoken-relay` 服务，5 秒后拉起，一直重试），所以短暂崩溃可能只收到一条静音的重启通知；一直起不来才会告警。

## 部署

```
scripts/relay/deploy.sh lab-cn2                   # 先更新中继，/metrics 随新版本上线（build_info 是中继的版本，和已部署的软链不一致时告警）
scripts/relay/deploy-monitor.sh lab-cn2           # 装监控：不重启中继
scripts/relay/deploy-monitor.sh --prod us sg eu cn   # 生产中继要带 --prod
```

配置在 `scripts/relay/conf/<中继>.monitor.ini`，可调项和默认值见 `monitor.py` 的 `DEFAULTS`。服务器上的文件都在 `~/st-test/`：`monitor.py`、`monitor.ini`、`pushover.env`、`monitor.state`、`monitor.log`（超过 1 MB 轮转一份）。cron 只有一行，带 `sharetoken-relay-monitor` 标记。

## Pushover 账号

把账号写到 Mac 的 `~/st-test/pushover.env`（`*.env` 已被 gitignore），再运行一次部署：

```
PUSHOVER_TOKEN=<应用的 API token>
PUSHOVER_USER=<用户 key>
```

```
chmod 600 ~/st-test/pushover.env
scripts/relay/deploy-monitor.sh --test-push lab-cn2
scripts/relay/deploy-monitor.sh --test-push --prod us sg eu cn
```

文件以 0600 下发。没有账号时监控照常运行，告警只写日志。发送失败会在下一轮重试。

## 排查

```
cd ~/st-test
python3 monitor.py -c monitor.ini --check   # 打印各项当前结果，不发通知、不写状态
tail monitor.log                            # 发过什么、发送失败的原因
systemctl status sharetoken-relay           # 中继服务状态
journalctl -u sharetoken-relay -n 50        # 中继日志（需要 sudo 或 systemd-journal 组）
```

测试：`python3 scripts/relay/monitor_test.py`。脚本只用标准库，要兼容 CN 上的 Python 3.6 和 cron 的 `LANG=C`。
