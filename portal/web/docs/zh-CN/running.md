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

## 在后台运行 {#background}

共享模型或长期调用时，让客户端在后台运行。先在终端里[登录](/docs/account#login)，后台服务不会询问密码。

> [!NOTE]
> 登录有效期是 30 天。到期后需要重新运行 `login`，再重启服务。

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
