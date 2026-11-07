# 分布式测试环境脚本

环境说明见 [docs/test-env.md](../../docs/test-env.md)。脚本都在 Mac 上运行，工作目录为 `$ST_TESTENV_WORK`（默认 `~/st-test`），里面放 alice 的客户端 home、构建产物和测试结果。

| 脚本 | 作用 |
|---|---|
| `env.sh` | 主机地址、peer ID、启动命令、辅助函数（`rsh`、`orbsh`、`iboxsh`、`relay_start`）；其他脚本都会 source 它 |
| `tunnels.sh` | 重建 Mac 发起的 SSH 隧道（Ollama 参考模型 → armbian；门户 → orb、US、SG；instabox → US）。Mac 重启或休眠后先跑这个 |
| `deploy.sh [--relays] [--portal]` | 用当前工作区交叉编译，滚动更新协调器和 6 个客户端（CN 在服务器上用源码编译）；默认不动中继和门户 |
| `push-tools.sh` | 把 `st_bench.py`、`mock_upstream.py` 分发到所有机器 |
| `status.sh` | 每个用户一行：协调器连接、所用中继、共享统计；外加三个中继的 JSON 统计 |
| `onall.sh TAG <st_bench 参数>` | 6 个买家同时跑同一条命令，例如 `onall.sh lat load -m mock-fast -c 1 -n 20` |
| `chaos.sh` | 故障注入：4 个买家持续加负载，按时间表杀掉并恢复中继、协调器、卖家、门户、模型服务（`DUR=900`） |
| `chaos_report.py <dir>` | 汇总 `chaos.sh` 的结果：各买家总计、按时间窗的成功率和延迟、每个事件之后的失败请求 |

常用流程：

```bash
scripts/testenv/tunnels.sh
scripts/testenv/deploy.sh                 # 改了代码之后
scripts/testenv/status.sh
scripts/testenv/onall.sh func-qwen func -m qwen2.5:0.5b --tools
scripts/testenv/chaos.sh && python3 scripts/testenv/chaos_report.py ~/st-test/results/chaos
```
