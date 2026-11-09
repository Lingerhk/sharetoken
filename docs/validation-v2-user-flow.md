# 用户流程验证（2026-11-09，dev 环境）

两个真实用户，走公网中继：Mac（`lingerhk@gmail.com`，卖家，macOS 应用注册，`scripts/upstream_proxy.py` 提供 `deepseek-flash`）；instabox（`support-dev@sharetoken.app`，买家，Linux 命令行包）。服务端和客户端都是 `1.0.0-dev.e1dfee8.dirty`。

## 修复的问题

| 级别 | 问题 | 修复 |
|---|---|---|
| 高 | 买家取消流式请求后，整笔预留冻结 24 小时（无 `max_tokens` 时每次 0.2 credit，实际成本约 10 micro） | 转 `pending` 时只留成本，其余立即退还（`ledger.ShrinkHold`）；至少留 1 micro，因为 `ledger_holds` 要求金额大于 0 |
| 高 | 退出或吊销 token 后，运行中的客户端仍能买卖 | Agent 每分钟确认登录，`logout` 立即触发；`up` 退出，daemon 转 `expired` |
| 中 | `share off` 后报价要到期才消失，买家收到 502 并产生闲置冻结 | 新消息 `withdraw`，报价立即下架，下架前发出的公告不能恢复它 |
| 中 | `up` 没有 Host 校验，DNS rebinding 能读账户信息 | 监听回环地址时启用 `guardHost` |
| 低 | 网页提示的 `sharetoken login -portal …` 参数不存在 | 去掉 `-portal` |
| 低 | 纯买家设备的“最近活动”永远是“从未” | 领取 ticket 时也更新 |
| 低 | 网页把小额显示成 0 | 小于 100 时最多显示 6 位小数 |
| 低 | macOS 打的 tar 包含 `._*` 和扩展属性 | `COPYFILE_DISABLE=1 tar --no-xattrs` |
| 低 | CLI 注册提示 `Username:`；`logout` 没有输出；只能买到自己模型时提示不清；上游代理在对端断开时打印堆栈 | 分别修正 |

## 最终账目

买家 4.594407 可用 + 0.400101 冻结 + 0.005492 已花 = 5.000000。卖家 5.000000 + 0.005229 收入 = 5.005229。0.005492 = 0.005229 + 手续费 0.000263。其中 0.4 credit 冻结来自修复前取消的两个请求，争议窗口结束（约 2026-11-10 11:50）后退还。

## 测试

`scripts/test.sh ./...`、`scripts/functest.py dev`（52 过 1 失败：中继还是 1.0.0，而 dev 门户是 `-dev` 构建，属预期）、`scripts/cli_test.sh`、`scripts/gui_ui_test.mjs`（67 项）。

`gui_stack.sh` 的遥测地址固定为 `127.0.0.1:18999`，多个 stack 同时运行时 `gui_ui_test` 的“one anonymous install ID”会误报。

## 没有覆盖

prod；带证据的争议裁决；余额不足（只有单元测试）；修复后的真实窗口和菜单栏（修复后用 daemon + 无头 Chrome 测同一套页面）；管理后台；Windows。
