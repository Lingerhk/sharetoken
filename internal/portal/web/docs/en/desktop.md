# Desktop app

The desktop app gives you a graphical interface to sign in, check your account and network status, and set up the models you share.

## Install {#install}

::: downloads desktop
:::

::: macos
1. Open the downloaded `.dmg` and drag ShareToken to the "Applications" folder. Apple Silicon and Intel have different packages. To check yours, open the Apple menu at the top left and click "About This Mac".
2. If the first launch says the developer cannot be verified, open "System Settings > Privacy & Security" and click "Open Anyway" at the bottom of the page. The app is not yet notarized by Apple.
:::

::: linux
The package works on Ubuntu 24.04 or later and Debian 13 or later. Install it with `apt`, which also installs the GTK and WebKitGTK it needs:

```bash
sudo apt install ./sharetoken-{{version}}-linux-<arch>.deb
```

Replace `<arch>` with the `amd64` or `arm64` you downloaded. You can then open ShareToken from the application menu. On other distributions, use the [command-line edition](/docs/install) with the [graphical interface without the app](#browser-ui) below.
:::

::: windows
1. Run the downloaded installer. If SmartScreen blocks it, click "More info" and then "Run anyway". The app is not yet code-signed.
2. Follow the prompts. It installs for the current user only, needs no administrator rights, and adds shortcuts to the Start menu and the desktop.
3. WebView2 Runtime is required. Windows 11 includes it. If Windows 10 does not have it, install it from the Microsoft website.
:::

## Upgrade and uninstall {#upgrade}

When a new version exists, a line appears at the top of the window with one sentence about it. "Details" shows the full release notes and where to download it, and "Check for updates" in Settings asks at any time. To upgrade, download the new version and install it as you did the first time. It replaces the old version. Uninstalling does not delete the state directory `{{home}}`, so your account and device key stay. To remove everything, delete that directory as well.

::: macos
To uninstall, quit the app and move ShareToken from the "Applications" folder to the Trash.
:::

::: linux
To uninstall:

```bash
sudo apt remove sharetoken
```
:::

::: windows
To uninstall, open "Settings > Apps > Installed apps", find ShareToken and click "Uninstall".
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

The window has its navigation on the left: Overview, Sharing, Pairing and Activity, with Feedback and Settings at the bottom. The status bar at the bottom of the window shows the connection, the relay in use and its latency; the latency is green, amber or red by how fast it is. The "?" beside a panel's title explains what the panel is for.

## Share models {#share}

Once you are signed in and connected to the network, click "Sharing" on the left. It has three tabs:

- **Provider APIs**: click "Add provider", choose the provider, and enter an API key or sign in with an account. After saving, choose the models to sell and set each one's model ID on the network and its prices. Each provider can be turned off, given new credentials or deleted, and each model can be tested, repriced or removed.
- **Local model server**: enter the model server's address under "Settings", then click "Add model"; the app lists the models that server offers.
- **Status**: once there is at least one model, click "Start sharing" to take jobs. Sharing turns itself off when there is no model left to sell.
- **Pairing** ("Pairing" on the left, the "My buyers" tab): click "Create pairing code", choose how long it lasts (1, 7 or 30 days) and how many buyers it admits, and give the code to your buyers. The panel lists your codes and your buyers, and lets you revoke a code or remove a buyer. Tick "Private mode" to serve only the buyers paired with you.

Prices are in credit per million tokens. API keys and login tokens are stored only on this computer, and the page shows keys masked. For notes on each provider, see [Share a hosted API](/docs/share#providers).

Under "Settings", the "Requests" tab lets you tick "Only sellers that speak the request's API natively" (Claude Code's requests then go only to sellers whose model server speaks Anthropic Messages, Codex's only to those that speak OpenAI Responses, with no conversion; see [Native and converted](/docs/use-models#native)) and set the budget held for each request (2 credits by default; you pay what a request uses). Both take effect at once.

To buy only from sellers you know, use "My sellers" under "Pairing": click "Enter pairing code" and type the code a seller gave you. See [Buy only from sellers you know](/docs/use-models#pairing).

To share with partners for free, use "Share partners", the first tab under "Pairing". Click "Invite partner", choose how long the code lasts (1, 3 or 7 days) and send it to your partner; they click "Enter invite code". The list shows your partners and the tokens you gave and used in the last 24 hours, and "Unpair" ends a partnership. Under "Sharing settings" you can use partners' nodes only, set a daily limit (tokens per 24 hours, all partners together) and the share of each node's concurrent jobs partners may take (0 pauses sharing). Available models marks the models your partners offer, and Activity shows "Partner (free)" for jobs between partners. See [Share with partners for free](/docs/share#partners).

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

