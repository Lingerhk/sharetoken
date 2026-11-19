# 运行客户端

`{{exe}} up` 是客户端的主进程：连接网络、提供本地 API、接单（如果开启了共享）。

## 前台运行 {#foreground}

```bash
{{exe}} up
```

启动后会显示：

```text
sharetoken client for you@example.com (device 12D3Koo...)
   OpenAI-compatible endpoint: http://127.0.0.1:{{port}}/v1
   coordinator via relay /dns4/relay-sg.sharetoken.app/tcp/4001/p2p/12D3Koo...
   not sharing (sharetoken share add <model> && sharetoken share on)
```

按 `Ctrl+C` 正常退出。`status`、`models` 这些命令要在另一个终端窗口里运行。

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

## 中继 {#relays}

客户端自动测速，选择最快的中继，中继出故障时自动切换。一般不用管：

```bash
{{exe}} relays
{{exe}} relays -measure
```

`-measure` 立即重新测速。默认不需要开任何端口：卖家在中继上预约，买家经中继或打洞连过来。

## 直连隧道 {#tailnet}

买家和卖家都在 NAT 后面、打洞又不成功时，请求全程经过中继。直连隧道是另一条直连的路：两端之间建一条 WireGuard 隧道（Tailscale 的数据面），先经 DERP 中继接通，几秒内换到两端之间的直连路径；换不成时留在 DERP 上，或者照旧走中继。不需要 Tailscale 账号，不装系统 VPN，不需要管理员权限，也不用开端口。默认关闭，买卖两端都打开才生效：

```bash
{{exe}} tailnet on
{{exe}} tailnet
{{exe}} tailnet off
```

`tailnet` 显示本机作为卖家经哪个 DERP 区域可达，以及每条隧道走的路径（`direct`，或 `derp:<区域>`）。请求走了隧道时，响应头 `X-Sharetoken-Timing` 里有 `tailnet=direct`。

- 对一个节点的第一个请求照常经中继发出，隧道在后台建立，之后的请求走隧道。`-wait 3000` 让第一个请求最多等 3 秒隧道，等不到再走中继。
- 身份、加密、计费和校验都不变：隧道里跑的仍是原来的连接，DERP 和中继一样只看到密文。
- 隧道里的连接跑在 QUIC 上。`tailnet on -transport tcp` 让本机连卖家时改用 TCP；卖家两种都接受。`tailnet` 会显示每条隧道用的是哪一种。
- 默认用 tailcat 的公共 DERP。自己运行 DERP 的网络用 `-derp-map <URL 或文件>` 指向自己的 DERP 表，买卖两端要用同一张表：买家只跟随地址去自己表里的 DERP。

## 在后台运行 {#background}

共享模型或长期调用时，让客户端在后台运行。先在终端里[登录](/docs/account#login)，后台服务不会询问验证码。

> [!NOTE]
> 登录有效期是 60 天。到期后需要重新运行 `login`，再重启服务。

::: macos
用 launchd，登录 macOS 时自动启动，异常退出时自动重启。

```bash
mkdir -p ~/Library/LaunchAgents
cat > ~/Library/LaunchAgents/app.sharetoken.client.plist <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>app.sharetoken.client</string>
  <key>ProgramArguments</key>
  <array><string>$HOME/.local/bin/{{exe}}</string><string>up</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
  <key>ThrottleInterval</key><integer>30</integer>
  <key>StandardOutPath</key><string>$HOME/Library/Logs/sharetoken.log</string>
  <key>StandardErrorPath</key><string>$HOME/Library/Logs/sharetoken.log</string>
</dict>
</plist>
EOF
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/app.sharetoken.client.plist
```

| 操作 | 命令 |
|---|---|
| 确认在运行 | `{{exe}} status` |
| 看日志 | `tail -f ~/Library/Logs/sharetoken.log` |
| 重启 | `launchctl kickstart -k gui/$(id -u)/app.sharetoken.client` |
| 停止并取消自启动 | `launchctl bootout gui/$(id -u)/app.sharetoken.client` |
:::

::: linux
用 systemd 用户服务，不需要 root。

```bash
mkdir -p ~/.config/systemd/user
cat > ~/.config/systemd/user/sharetoken.service <<EOF
[Unit]
Description=ShareToken client
After=network-online.target

[Service]
ExecStart=%h/.local/bin/{{exe}} up
Restart=on-failure
RestartSec=30

[Install]
WantedBy=default.target
EOF
systemctl --user daemon-reload
systemctl --user enable --now sharetoken
```

默认只在你登录时运行。服务器上想开机就运行、退出 SSH 也不停：

```bash
sudo loginctl enable-linger $USER
```

| 操作 | 命令 |
|---|---|
| 确认在运行 | `{{exe}} status` |
| 看日志 | `journalctl --user -u sharetoken -f` |
| 重启 | `systemctl --user restart sharetoken` |
| 停止并取消自启动 | `systemctl --user disable --now sharetoken` |
:::

::: windows
用任务计划程序，在你登录 Windows 时自动启动，异常退出时自动重启。

```powershell
$exe = (Get-Command {{exe}}).Source
$log = "$HOME\AppData\Local\sharetoken.log"
$arg = "-NoProfile -WindowStyle Hidden -Command `"& '$exe' up *>> '$log'`""
$action = New-ScheduledTaskAction -Execute powershell.exe -Argument $arg
$trigger = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
$settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit 0 -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
Register-ScheduledTask -TaskName ShareToken -Action $action -Trigger $trigger -Settings $settings
Start-ScheduledTask -TaskName ShareToken
```

| 操作 | 命令 |
|---|---|
| 确认在运行 | `{{exe}} status` |
| 看日志 | `Get-Content "$HOME\AppData\Local\sharetoken.log" -Wait -Tail 20` |
| 停止 | `Stop-ScheduledTask -TaskName ShareToken; Stop-Process -Name {{exe}}` |
| 启动 | `Start-ScheduledTask -TaskName ShareToken` |
| 取消自启动 | `Unregister-ScheduledTask -TaskName ShareToken -Confirm:$false` |
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
