# Desktop app

The desktop app gives you a graphical interface to sign in, check your account and network status, set up the models you share, and connect Claude Code and Codex. Its package also contains the [command-line edition](#with-cli).

## Install {#install}

::: downloads desktop
:::

::: macos
1. It needs macOS 13 or later. Open the downloaded `.dmg`, drag ShareToken to the "Applications" folder, and open it from there from now on ([one-click updates](/docs/update#one-click) need that). Apple Silicon and Intel have different packages. To check yours, open the Apple menu at the top left and click "About This Mac".
2. If the first launch says the developer cannot be verified, open "System Settings > Privacy & Security" and click "Open Anyway" at the bottom of the page. The app is not yet notarized by Apple.
:::

::: linux
The package works on Ubuntu 24.04 or later and Debian 13 or later (it needs GTK 4.10 or later and WebKitGTK 6.0), with one for x86-64 and one for ARM64. Install it with `apt`, which also installs the GTK and WebKitGTK it needs:

```bash
sudo apt install ./sharetoken-{{version}}-linux-<arch>.deb
```

Replace `<arch>` with the `amd64` or `arm64` you downloaded. You can then open ShareToken from the application menu. The package also brings an AppArmor profile (`/etc/apparmor.d/sharetoken-gui`): Ubuntu needs it to let the window's web pages run in a sandbox. On other distributions, use the [command-line edition](/docs/install) with the [graphical interface without the app](#browser-ui) below.
:::

::: windows
1. Run the downloaded installer (Windows 10 or 11; x64 and ARM64 have different installers). If SmartScreen blocks it, click "More info" and then "Run anyway". The app is not yet code-signed.
2. Follow the prompts. It installs for the current user only, needs no administrator rights, and adds shortcuts to the Start menu and the desktop.
3. WebView2 Runtime is required. Windows 11 includes it. If Windows 10 does not have it, install it from the Microsoft website.
:::

## Upgrade and uninstall {#upgrade}

When a new version exists, a line appears at the top of the window with one sentence about it; the tray menu gets an item for it too, and a system notification appears once. "Update now" makes the app download the new version, check its release signature, install it and restart; see [One-click update](/docs/update#one-click). "Details" shows the full release notes and where to download it, and "Skip this version" stops offering a version you do not want (a required upgrade cannot be skipped). "Check for updates" on the "General" tab of Settings asks at any time. When the line has no "Update now", download the new version and install it as you did the first time. It replaces the old version.

Before you uninstall, if you connected Claude Code or Codex under "Settings > Tools", click "Disconnect" to put back their own settings, and turn off "Codex Desktop unlock" and "Start ShareToken when you log in". Uninstalling does not undo these, and the tools would keep trying to reach a client that is gone. Uninstalling does not delete the state directory `{{home}}` either, so your account, device key, configuration and logs stay. To remove everything, delete that directory as well.

::: macos
To uninstall, quit the app and move ShareToken from the "Applications" folder to the Trash.
:::

::: linux
To uninstall:

```bash
sudo apt purge sharetoken
```

`purge` removes the AppArmor profile as well; `remove` leaves it.
:::

::: windows
To uninstall, open "Settings > Apps > Installed apps", find ShareToken and click "Uninstall". It removes the shortcuts, its PATH entry and the registration for `sharetoken://` links.
:::

## Window and background service {#behaviour}

The app has two parts: the window, and the background service that does the real work (`{{exe}} daemon`). All logic lives in the background service. The window only displays it.

::: macos
- There is an icon in the menu bar. Click it to see the connection, the relay in use and its latency, whether you are sharing and your available credit, or to open the window or quit.
- Closing the window (red button or `Cmd+W`) only hides it. The background service keeps running, so sharing and the local API are not interrupted.
- The app quits only when you quit from the menu or press `Cmd+Q`. If the app started the background service, it stops too.
:::

::: linux
- There is an icon in the top bar (on Ubuntu, and on other desktops that show app indicators). Click it to see the connection, the relay in use and its latency, whether you are sharing and your available credit, or to open the window or quit. Closing the window only hides it; the background service keeps running.
- On a desktop without app indicators, such as GNOME without an indicator extension, there is no icon: closing the window quits the app, and the background service stops with it. To keep sharing models in the background there, [run it from the command line](/docs/running#background).
:::

::: windows
- There is an icon in the notification area. Left-click opens the window, and right-click opens the menu (the connection, the relay in use and its latency, sharing, your available credit, open, quit). Windows 11 may put a new icon in the `^` overflow area. You can drag it to the taskbar.
- Closing the window (X) only hides it. The background service keeps running, so sharing and the local API are not interrupted.
- The app quits only when you quit from the menu. If the app started the background service, it stops too.
:::

Starting the program again only brings back the existing window.

With "Start ShareToken when you log in (in the tray, without opening the window)" ticked under "Settings > Tools", the app starts in the tray each time you log in, without opening its window, and sharing and the local API work as usual. On a Linux desktop without a tray, the window opens. Without it, the background service stops after a restart, a logout or Quit from the tray, and connected Claude Code and Codex cannot use even their own official models until ShareToken is opened again; see [Keep your Claude or ChatGPT subscription](/docs/use-models#subscription).

The window has its navigation on the left: Overview, Sharing, Pairing and Activity, with Feedback and Settings at the bottom. The Overview shows your credit, the address of the local model endpoint (with its API key to copy when one is required), the network and the available models; Activity has two tabs, "All" and "Sold". The status bar at the bottom of the window shows the connection, the relay in use and its latency, and what the direct tunnels are doing, with your account and the version on the right; a latency is green, amber or red by how fast it is. The "?" beside a panel's title explains what the panel is for.

Settings has these tabs:

- **General**: your account and "Log out", whether the local model endpoint requires an API key (and copying the key or making a new one), the language, the version and "Check for updates", feedback, and diagnostics: the "Send crash reports" switch and "View pending reports". See [Logs and crash reports](/docs/config#logs).
- **Requests**: how models are bought; see the end of [Share models](#share) below.
- **Network**: [direct tunnels](#tunnels).
- **My devices**: the devices bound to this account.
- **Warm-up**: see [Warm-up](#warmup).
- **Tools**: connect Claude Code and Codex to the local endpoint, keeping their own official login if you like, and start the app when you log in. See [Keep your Claude or ChatGPT subscription](/docs/use-models#subscription).

## Share models {#share}

Once you are signed in and connected to the network, click "Sharing" on the left. It has three tabs:

- **Status**: once there is at least one model, click "Start sharing" to take jobs. Sharing turns itself off when there is no model left to sell. While you share, it shows the models on sale, the jobs and earnings, and the relays buyers reach you through.
- **Provider APIs**: click "Add provider", choose the provider, and enter an API key or sign in with an account. After saving, choose the models to sell and set each one's model ID on the network and its prices. Each provider can be disabled, given new credentials, changed in "Connection settings" (upstream proxy and TLS) or deleted, and each model can be tested, edited or removed. A Claude or ChatGPT account added by signing in also shows its plan's usage and its warm-up.
- **Local models**: click "Settings" on this panel and enter the model server's address (Ollama's default is `http://127.0.0.1:11434/v1`) and how many requests it handles at once, then click "Add model"; the app lists the models that server offers.

Prices are in credit per million tokens. API keys and login tokens are stored only on this computer, and the page shows keys masked. For notes on each provider, see [Share a hosted API](/docs/share#providers). The jobs you sold are under the "Sold" tab of Activity.

To serve only buyers you know, use the "My buyers" tab of "Pairing" on the left: click "Create pairing code", choose how long it lasts (1 or 7 days), how many buyers it admits and whether each needs your approval, and give the code or its link to your buyers ("Copy link", "Copy invitation message"). The tab lists your codes and your buyers, with anyone waiting for your approval ("Approve", "Reject"), and lets you revoke a code or remove a buyer. Tick "Private mode" to serve only the buyers paired with you.

Under "Settings", the "Requests" tab lets you tick "Only sellers that speak the request's API natively" (Claude Code's requests then go only to sellers whose model server speaks Anthropic Messages, Codex's only to those that speak OpenAI Responses, with no conversion; see [Native and converted](/docs/use-models#native)) and set the budget held for each request (2 credits by default; you pay what a request uses). Both take effect at once.

To buy only from sellers you know, use "My sellers" under "Pairing": click "Enter pairing code" and type the code, or paste the link, a seller gave you. See [Buy only from sellers you know](/docs/use-models#pairing).

To share with partners for free, use "Share partners", the first tab under "Pairing". Click "Invite partner", choose how long the code lasts (1, 3 or 7 days) and whether you approve whoever uses it, and send the code or its link to your partner; it can be used once. They click "Enter invite code" and type the code or paste the link. The list shows your partners and the tokens you gave and used in the last 24 hours, and "Unpair" ends a partnership. Under "Sharing settings" you can use partners' nodes only, set a daily limit (tokens per 24 hours, all partners together) and the share of each node's concurrent jobs partners may take (0 pauses sharing). Available models marks the models your partners offer, and Activity shows "Partner (free)" for jobs between partners. See [Share with partners for free](/docs/share#partners).

An [invite link](/docs/share#invite-links) clicked in the browser can open the app ("Open in ShareToken" on the link's page; the installers register the app for `sharetoken://` links). The app comes to the front, shows who invites you and asks before it accepts; when you are signed out it asks after you sign in.

## Direct tunnels {#tunnels}

Under "Settings", the "Network" tab has the switch for [direct tunnels](/docs/running#tailnet): an encrypted tunnel between you and the other side of a request, in place of the relays. It works when both sides have it on; otherwise, and whenever a tunnel cannot be set up, requests go through the relays as before. Ticking "Direct tunnels" applies at once and shows the settings:

| Setting | What it does |
|---|---|
| Transport | What a connection inside a tunnel runs on, for the connections this computer opens (when you use a model): QUIC, the default, or TCP. When you share a model, both are accepted. QUIC is faster for large requests on a steady network; choose TCP on a link that loses packets or jitters, such as weak Wi-Fi, if you send requests of several megabytes |
| Wait for a tunnel | How many milliseconds the first request to a seller may wait for its tunnel (0 to 10000). 0, the default, does not wait: that request goes through the relay, and the tunnel takes over the next ones |
| DERP relays | The relays a tunnel starts through and stays on when no direct path is found. Empty uses the public ones; a network that runs its own gives the URL or the file of its DERP map. Buyer and seller need the same |

"This device" says whether buyers can reach this computer through a tunnel. The "Tunnels" panel lists each tunnel: its direction (to a seller, from a buyer), the peer, the path ("Direct", or "Relayed" with the DERP region), the transport, the latency, the number of connections, and the traffic.

The status bar shows the tunnels in short: "Off"; "On, no tunnel yet"; or the transport, the path, the number of tunnels and their latency, for example "QUIC Direct × 2". A click on it opens these settings. The Overview's network panel says the same.

## Warm-up {#warmup}

A Claude or ChatGPT (Codex) subscription's 5-hour window starts at the first request after it resets. The "Warm-up" tab under "Settings" starts windows early, so they reset sooner while you work. It lists the accounts there are to warm up: the Claude Code and Codex on this computer, and the Claude and ChatGPT logins you added under "Sharing". Each row shows its times of day, whether it warms up on reset, its usage windows now and the last warm-up's result. Click "Settings" on a row to change it; with "Turn the warm-up on" ticked:

| Setting | What it does |
|---|---|
| Times of day | This computer's local time; several, separated by commas. At each, one tiny request is sent if the 5-hour window isn't running; a time missed by more than an hour (the computer asleep, say) is skipped |
| Warm up on reset | The weekly window, or the 5-hour and weekly windows, start again as soon as they reset. Nothing is sent while a window is used up or extra usage is in use |
| Model | Empty uses the cheapest model |
| Program path | Only for the Claude Code and Codex on this computer: empty finds the `claude` or `codex` command automatically. Otherwise the full path of the program, whose file must be named `claude` or `codex` |

"Warm up now" on a row sends one at once; "Pause every automatic warm-up" stops the scheduled ones. Warm-ups happen while the client runs, do not go through the ShareToken network, and cost no credit. For the command line, see [Warm-up](/docs/cli#warmup).

## Graphical interface without the app {#browser-ui}

The command-line edition includes the same interface. Run:

```bash
{{exe}} daemon
```

Then open the address it prints, `http://127.0.0.1:{{port}}/#key=…`, in a browser to sign in on the page. The `#key=` part is what lets the page use the client; it changes each time `daemon` starts. The difference from `up` is that `daemon` can run while signed out, and it joins the network only after you sign in.

## Use with the command line {#with-cli}

The desktop app and the command-line edition share the state directory `{{home}}`. While the app is running, commands such as `{{exe}} status`, `{{exe}} models` and `{{exe}} share add` act on it directly. Do not run `{{exe}} up` on the same state directory. The same device key would be online twice, and the client would exit with an error.

::: macos
`{{exe}}` is inside the app. To use it by this name in a terminal, add it to PATH first:

```bash
echo 'export PATH="/Applications/ShareToken.app/Contents/MacOS:$PATH"' >> ~/.zshrc
```

Then open a new terminal.
:::

::: linux
The package has already placed `{{exe}}` in `/usr/bin`, so you can use it directly.
:::

::: windows
The installer has already added `{{exe}}` to the current user's PATH. Open a new terminal to use it.
:::
