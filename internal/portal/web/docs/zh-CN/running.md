# 运行客户端

`{{exe}} up` 是客户端的主进程：连接网络，提供本地 API 和模型端点，接单（如果开启了共享），转发已接入的 Claude Code 和 Codex 的请求，并按设置执行[预热](/docs/cli#warmup)。

## 前台运行 {#foreground}

```bash
{{exe}} up
```

启动后会显示：

```text
sharetoken client for you@example.com (device 12D3Koo...)
   OpenAI-compatible endpoint: http://127.0.0.1:{{port}}/v1
   API key required: `sharetoken apikey` shows it
   coordinator via relay /dns4/relay-sg.sharetoken.app/tcp/4001/p2p/12D3Koo...
   not sharing (sharetoken share add <model> && sharetoken share on)
```

按 `Ctrl+C` 正常退出。`status`、`models` 这些命令要在另一个终端窗口里运行。在终端里运行时，日志同时显示在终端上并写入[日志文件](/docs/config#logs)。登录过期或在门户被撤销时，`up` 会停止并提示重新登录。

临时改变端口或网络设置，不写入配置文件：

```bash
{{exe}} up -http 127.0.0.1:9800 -nat=false
```

## 查看状态 {#status}

```bash
{{exe}} status
```

重点看这几行：

| 输出 | 含义 |
|---|---|
| `coordinator ...: connected (relayed) via relay-sg` | 已连上网络，以及经过哪个中继 |
| `link: 42.1 ms now, avg 40.3 ms ... loss 0%` | 当前中继的实时延迟、抖动和丢包 |
| `sharing 1 model(s): 3 jobs served, ...` | 正在共享，接单和收益情况 |
| `offer not accepted: ...` | 模型报价被协调器拒绝，以及原因 |
| `warning: ...` | 需要你注意的问题 |
| `update available: ...` | 有新版本；`upgrade required` 表示这个版本已不能发起新请求，见[升级](/docs/update) |

## 中继 {#relays}

客户端自动测速，选择最快的中继，中继出故障时自动切换。一般不用管：

```bash
{{exe}} relays
{{exe}} relays -measure
```

`-measure` 立即重新测速。默认不需要开任何端口：卖家在中继上预约，买家经中继或打洞连过来。卖家也可以经自己运行的中继共享，见[自建中继](/docs/relay)。

## 直连隧道 {#tailnet}

买家和卖家都在 NAT 后面、打洞又不成功时，请求全程经过中继。直连隧道是另一条直连的路：两端之间建一条 WireGuard 隧道（Tailscale 的数据面），先经 DERP 中继接通，几秒内换到两端之间的直连路径；换不成时留在 DERP 上，或者照旧走中继。不需要 Tailscale 账号，不装系统 VPN，不需要管理员权限，也不用开端口。默认关闭，买卖两端都打开才生效，在这里或在桌面应用里打开都可以（见[应用里的直连隧道](/docs/desktop#tunnels)）：

```bash
{{exe}} tailnet on
{{exe}} tailnet
{{exe}} tailnet off
```

`tailnet on` 和 `tailnet off` 对运行中的客户端立即生效。`tailnet` 显示本机作为卖家经哪个 DERP 区域可达，以及每条隧道走的路径（`direct`，或 `derp:<区域>`）。请求走了隧道时，响应头 `X-Sharetoken-Timing` 里有 `tailnet=direct`。

- 对一个节点的第一个请求照常经中继发出，隧道在后台建立，之后的请求走隧道。`-wait 3000` 让第一个请求最多等 3 秒隧道（最多 10000 毫秒），等不到再走中继。
- 身份、加密、计费和校验都不变：隧道里跑的仍是原来的连接，DERP 和中继一样只看到密文。
- 隧道里的连接跑在 QUIC 上。`tailnet on -transport tcp` 让本机连卖家时改用 TCP；卖家两种都接受。`tailnet` 会显示每条隧道用的是哪一种。网络稳定时，QUIC 上传大请求快几倍。网络丢包或抖动较多时（比如信号不好的 Wi-Fi），一条 QUIC 连接在提速阶段受挫后会一直慢，几 MB 的请求比走 TCP 慢得多：这种网络请选 TCP。几十 KB 的普通请求没有差别。
- 一条隧道连接 10 分钟没有请求就关闭，隧道本身再过 2 分钟关闭。下一个请求会重新建立；建立期间这个请求走中继。
- 默认用 tailcat 的公共 DERP。自己运行 DERP 的网络用 `-derp-map <URL 或文件>` 指向自己的 DERP 表，买卖两端要用同一张表：买家只跟随地址去自己表里的 DERP。
- 机器已经在 Tailscale（或 Headscale）网络里时，卖家打开直连隧道后也会公布它在这个网络里的地址，同一网络里的买家直接连过去。`tailnet` 会显示本机在这个网络里的地址，走这条路的请求在 `X-Sharetoken-Timing` 里是 `tailnet=system`。

## 在后台运行 {#background}

共享模型、长期调用，或者接入了 Claude Code 和 Codex（见[同时使用订阅](/docs/use-models#subscription)）时，让客户端在后台一直运行。先在终端里[登录](/docs/account#login)，后台服务不会询问验证码。

```bash
{{exe}} autostart on
```

这个命令把客户端注册为登录系统时自动启动的服务，并立即启动它：macOS 上是 LaunchAgent `app.sharetoken.client`，Linux 上是 systemd 用户服务 `sharetoken`（不需要 root），Windows 上是登录时运行的计划任务 `ShareToken`。服务运行的是 `{{exe}} daemon`，它和 `up` 做同样的事，区别是登录失效后不退出，可以在桌面应用里重新登录；桌面应用启动时也会直接使用它。

`{{exe}} autostart` 显示是否已设置，`{{exe}} autostart off` 停止服务并取消自启动。装了桌面应用的话，也可以在应用的设置里打开登录时自动启动。

服务的日志和 `up` 一样写在[日志目录](/docs/config#logs)的 `client.log` 里，用 `{{exe}} logs -f` 查看。

> [!NOTE]
> 登录有效期是 60 天。到期后重新运行 `login`，再重启服务。

::: macos
服务异常退出后会自动重启。

| 操作 | 命令 |
|---|---|
| 确认在运行 | `{{exe}} status` |
| 看日志 | `{{exe}} logs -f` |
| 重启 | `launchctl kickstart -k gui/$(id -u)/app.sharetoken.client` |
| 停止并取消自启动 | `{{exe}} autostart off` |
:::

::: linux
服务异常退出后会自动重启。默认只在你登录时运行。服务器上想开机就运行、退出 SSH 也不停：

```bash
sudo loginctl enable-linger $USER
```

| 操作 | 命令 |
|---|---|
| 确认在运行 | `{{exe}} status` |
| 看日志 | `{{exe}} logs -f` |
| 重启 | `systemctl --user restart sharetoken` |
| 停止并取消自启动 | `{{exe}} autostart off` |
:::

::: windows
服务在你登录 Windows 时启动。异常退出后不会自动重启，要等下次登录，或者手动启动。

| 操作 | 命令 |
|---|---|
| 确认在运行 | `{{exe}} status` |
| 看日志 | `{{exe}} logs -f` |
| 停止 | `Stop-ScheduledTask -TaskName ShareToken; Stop-Process -Name {{exe}}` |
| 启动 | `Start-ScheduledTask -TaskName ShareToken` |
| 取消自启动 | `{{exe}} autostart off` |
:::

## 睡眠和断网 {#sleep}

机器睡眠或断网时，客户端和网络的连接会断开，恢复后自动重新连接。这段时间共享的模型不会接单。

::: macos
临时防止机器睡眠：

```bash
caffeinate -i {{exe}} up
```
:::

::: linux
临时防止机器睡眠：

```bash
systemd-inhibit --what=sleep {{exe}} up
```
:::

::: windows
要一直接单，请在“设置 > 系统 > 电源”里关闭自动睡眠。
:::
