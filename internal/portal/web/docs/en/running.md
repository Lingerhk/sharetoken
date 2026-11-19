# Run the client

`{{exe}} up` is the client's main process. It connects to the network, serves the local API and takes jobs (if sharing is on).

## Run in the foreground {#foreground}

```bash
{{exe}} up
```

After it starts, it prints:

```text
sharetoken client for you@example.com (device 12D3Koo...)
   OpenAI-compatible endpoint: http://127.0.0.1:{{port}}/v1
   coordinator via relay /dns4/relay-sg.sharetoken.app/tcp/4001/p2p/12D3Koo...
   not sharing (sharetoken share add <model> && sharetoken share on)
```

Press `Ctrl+C` to exit cleanly. Run commands such as `status` and `models` in another terminal window.

To change the port or network settings temporarily, without writing to the configuration file:

```bash
{{exe}} up -http 127.0.0.1:9800 -nat=false
```

## Check status {#status}

```bash
{{exe}} status
```

Look at these lines:

| Output | Meaning |
|---|---|
| `coordinator ...: connected (relayed) via relay-sg` | You are connected to the network, and which relay you use |
| `link: 42.1 ms now, avg 40.3 ms ... loss 0%` | Live latency, jitter and packet loss to the current relay |
| `sharing 1 model(s): 3 jobs served, ...` | You are sharing, with jobs taken and earnings |
| `offer not accepted: ...` | The coordinator rejected a model offer, and why |
| `warning: ...` | A problem that needs your attention |

## Relays {#relays}

The client measures speed automatically, picks the fastest relay and switches when a relay fails. You usually do not need to do anything:

```bash
{{exe}} relays
{{exe}} relays -measure
```

`-measure` measures again right away. By default you do not need to open any port: sellers register on a relay, and buyers connect through the relay or by hole punching.

## Direct tunnels {#tailnet}

When buyer and seller are both behind NATs and hole punching fails, requests go through a relay all the way. Direct tunnels are a second way to connect directly: a WireGuard tunnel between the two ends (Tailscale's data plane), which starts through a DERP relay and moves to a direct path between the two within seconds; when it cannot, it stays on DERP, or the request uses the relay as before. It needs no Tailscale account, no system VPN, no administrator rights and no open port. It is off by default, and works when both buyer and seller turn it on, here or in the desktop app (see [Direct tunnels in the app](/docs/desktop#tunnels)):

```bash
{{exe}} tailnet on
{{exe}} tailnet
{{exe}} tailnet off
```

`tailnet` shows through which DERP region this machine is reachable as a seller, and the path of each tunnel (`direct`, or `derp:<region>`). When a request used a tunnel, the response header `X-Sharetoken-Timing` has `tailnet=direct`.

- The first request to a node goes through the relay as usual while the tunnel is set up in the background; later requests use the tunnel. `-wait 3000` makes the first request wait up to 3 seconds for the tunnel before it uses the relay.
- Identity, encryption, billing and verification do not change: the tunnel carries the same connection as before, and DERP, like a relay, sees only ciphertext.
- Inside the tunnel the connection runs over QUIC. `tailnet on -transport tcp` makes this machine's connections to sellers use TCP instead; a seller accepts both. `tailnet` shows which one each tunnel uses. On a steady link QUIC uploads large requests several times faster. On a link that loses packets or jitters, weak Wi-Fi for example, a QUIC connection that stumbles while it speeds up stays slow, and requests of several megabytes take much longer than over TCP: choose TCP there. Ordinary requests of tens of kilobytes show no difference.
- A tunnel connection that carried no request for 10 minutes is closed, and the tunnel 2 minutes after it. The next request sets it up again; while that takes, it goes through the relay.
- The default is tailcat's public DERP relays. A network that runs its own points at its DERP map with `-derp-map <URL or file>`; buyer and seller need the same map: a buyer follows an address only to DERP relays of its own map.

## Run in the background {#background}

To share models or make calls over a long time, run the client in the background. First [sign in](/docs/account#login) in a terminal, because the background service does not ask for a code.

> [!NOTE]
> A sign-in is valid for 60 days. When it expires, run `login` again and restart the service.

::: macos
Use launchd. It starts when you log in to macOS and restarts after an abnormal exit.

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

| Action | Command |
|---|---|
| Check that it is running | `{{exe}} status` |
| View the log | `tail -f ~/Library/Logs/sharetoken.log` |
| Restart | `launchctl kickstart -k gui/$(id -u)/app.sharetoken.client` |
| Stop and disable autostart | `launchctl bootout gui/$(id -u)/app.sharetoken.client` |
:::

::: linux
Use a systemd user service. No root needed.

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

By default it runs only while you are logged in. On a server, to run from boot and keep running after you leave SSH:

```bash
sudo loginctl enable-linger $USER
```

| Action | Command |
|---|---|
| Check that it is running | `{{exe}} status` |
| View the log | `journalctl --user -u sharetoken -f` |
| Restart | `systemctl --user restart sharetoken` |
| Stop and disable autostart | `systemctl --user disable --now sharetoken` |
:::

::: windows
Use Task Scheduler. It starts when you log in to Windows and restarts after an abnormal exit.

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

| Action | Command |
|---|---|
| Check that it is running | `{{exe}} status` |
| View the log | `Get-Content "$HOME\AppData\Local\sharetoken.log" -Wait -Tail 20` |
| Stop | `Stop-ScheduledTask -TaskName ShareToken; Stop-Process -Name {{exe}}` |
| Start | `Start-ScheduledTask -TaskName ShareToken` |
| Disable autostart | `Unregister-ScheduledTask -TaskName ShareToken -Confirm:$false` |
:::

## Sleep and network loss {#sleep}

When the machine sleeps or loses its network, the client's connection to the network drops. It reconnects automatically afterward. While it is disconnected, the models you share do not take jobs.

::: macos
To keep the machine from sleeping for now:

```bash
caffeinate -i {{exe}} up
```
:::

::: linux
To keep the machine from sleeping for now:

```bash
systemd-inhibit --what=sleep {{exe}} up
```
:::

::: windows
To keep taking jobs all the time, turn off automatic sleep in "Settings > System > Power".
:::
