# Run the client

`{{exe}} up` is the client's main process. It connects to the network, serves the local API and the model endpoint, takes jobs (if sharing is on), passes on the requests of a connected Claude Code or Codex, and runs the [warm-up](/docs/cli#warmup) you set.

## Run in the foreground {#foreground}

```bash
{{exe}} up
```

After it starts, it prints:

```text
sharetoken client for you@example.com (device 12D3Koo...)
   OpenAI-compatible endpoint: http://127.0.0.1:{{port}}/v1
   API key required: `sharetoken apikey` shows it
   coordinator via relay /dns4/relay-sg.sharetoken.app/tcp/4001/p2p/12D3Koo...
   not sharing (sharetoken share add <model> && sharetoken share on)
```

Press `Ctrl+C` to exit cleanly. Run commands such as `status` and `models` in another terminal window. In a terminal the log is shown there as well as written to the [log file](/docs/config#logs). When the sign-in expires or is revoked in the portal, `up` stops and asks you to sign in again.

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
| `update available: ...` | A newer release exists; `upgrade required` means this version can no longer start new requests, see [Upgrade](/docs/update) |

## Relays {#relays}

The client measures speed automatically, picks the fastest relay and switches when a relay fails. You usually do not need to do anything:

```bash
{{exe}} relays
{{exe}} relays -measure
```

`-measure` measures again right away. By default you do not need to open any port: sellers register on a relay, and buyers connect through the relay or by hole punching. A seller can also share through a relay of its own, see [Run your own relay](/docs/relay).

## Direct tunnels {#tailnet}

When buyer and seller are both behind NATs and hole punching fails, requests go through a relay all the way. Direct tunnels are a second way to connect directly: a WireGuard tunnel between the two ends (Tailscale's data plane), which starts through a DERP relay and moves to a direct path between the two within seconds; when it cannot, it stays on DERP, or the request uses the relay as before. It needs no Tailscale account, no system VPN, no administrator rights and no open port. It is off by default, and works when both buyer and seller turn it on, here or in the desktop app (see [Direct tunnels in the app](/docs/desktop#tunnels)):

```bash
{{exe}} tailnet on
{{exe}} tailnet
{{exe}} tailnet off
```

`tailnet on` and `tailnet off` take effect at once in a running client. `tailnet` shows through which DERP region this machine is reachable as a seller, and the path of each tunnel (`direct`, or `derp:<region>`). When a request used a tunnel, the response header `X-Sharetoken-Timing` has `tailnet=direct`.

- The first request to a node goes through the relay as usual while the tunnel is set up in the background; later requests use the tunnel. `-wait 3000` makes the first request wait up to 3 seconds for the tunnel (at most 10000 milliseconds) before it uses the relay.
- Identity, encryption, billing and verification do not change: the tunnel carries the same connection as before, and DERP, like a relay, sees only ciphertext.
- Inside the tunnel the connection runs over QUIC. `tailnet on -transport tcp` makes this machine's connections to sellers use TCP instead; a seller accepts both. `tailnet` shows which one each tunnel uses. On a steady link QUIC uploads large requests several times faster. On a link that loses packets or jitters, weak Wi-Fi for example, a QUIC connection that stumbles while it speeds up stays slow, and requests of several megabytes take much longer than over TCP: choose TCP there. Ordinary requests of tens of kilobytes show no difference.
- A tunnel connection that carried no request for 10 minutes is closed, and the tunnel 2 minutes after it. The next request sets it up again; while that takes, it goes through the relay.
- The default is tailcat's public DERP relays. A network that runs its own points at its DERP map with `-derp-map <URL or file>`; buyer and seller need the same map: a buyer follows an address only to DERP relays of its own map.
- When the machine is already on a Tailscale (or Headscale) network, a seller with direct tunnels on also announces its address on that network, and buyers on the same network connect to it directly. `tailnet` shows that address, and a request that went this way has `tailnet=system` in `X-Sharetoken-Timing`.

## Run in the background {#background}

To share models, make calls over a long time, or keep Claude Code and Codex connected (see [Keep your subscription](/docs/use-models#subscription)), keep the client running in the background. First [sign in](/docs/account#login) in a terminal, because the background service does not ask for a code.

```bash
{{exe}} autostart on
```

This registers the client as a service that starts when you log in to the system, and starts it now: a LaunchAgent `app.sharetoken.client` on macOS, a systemd user service `sharetoken` on Linux (no root needed), and a scheduled task `ShareToken` that runs at logon on Windows. The service runs `{{exe}} daemon`, which does the same as `up` but does not exit when the sign-in ends, so you can sign in again in the desktop app; the desktop app also uses it when it starts.

`{{exe}} autostart` shows whether it is set up, and `{{exe}} autostart off` stops the service and removes it. With the desktop app installed you can instead turn on starting at login in the app's settings.

Like `up`, the service writes its log to `client.log` in the [log directory](/docs/config#logs). Follow it with `{{exe}} logs -f`.

> [!NOTE]
> A sign-in is valid for 60 days. When it expires, run `login` again and restart the service.

::: macos
The service is restarted after an abnormal exit.

| Action | Command |
|---|---|
| Check that it is running | `{{exe}} status` |
| View the log | `{{exe}} logs -f` |
| Restart | `launchctl kickstart -k gui/$(id -u)/app.sharetoken.client` |
| Stop and disable autostart | `{{exe}} autostart off` |
:::

::: linux
The service is restarted after an abnormal exit. By default it runs only while you are logged in. On a server, to run from boot and keep running after you leave SSH:

```bash
sudo loginctl enable-linger $USER
```

| Action | Command |
|---|---|
| Check that it is running | `{{exe}} status` |
| View the log | `{{exe}} logs -f` |
| Restart | `systemctl --user restart sharetoken` |
| Stop and disable autostart | `{{exe}} autostart off` |
:::

::: windows
The service starts when you log in to Windows. It is not restarted after an abnormal exit: it starts again at your next logon, or when you start it.

| Action | Command |
|---|---|
| Check that it is running | `{{exe}} status` |
| View the log | `{{exe}} logs -f` |
| Stop | `Stop-ScheduledTask -TaskName ShareToken; Stop-Process -Name {{exe}}` |
| Start | `Start-ScheduledTask -TaskName ShareToken` |
| Disable autostart | `{{exe}} autostart off` |
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
