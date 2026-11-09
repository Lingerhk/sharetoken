# Desktop app

The desktop app gives you a graphical interface to register, sign in, and check your account and network status. Settings for sharing models still use the command line for now.

## Install {#install}

::: downloads desktop
:::

::: macos
1. Open the downloaded `.dmg` and drag ShareToken to the "Applications" folder. Apple Silicon and Intel have different packages. To check yours, open the Apple menu at the top left and click "About This Mac".
2. If the first launch says the developer cannot be verified, open "System Settings > Privacy & Security" and click "Open Anyway" at the bottom of the page. The app is not yet notarized by Apple.
:::

::: linux
The package works on Ubuntu 22.04 or later and Debian 12 or later. Install it with `apt`, which also installs the WebKitGTK it needs:

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

To upgrade, download the new version and install it as you did the first time. It replaces the old version. Uninstalling does not delete the state directory `{{home}}`, so your account and device key stay. To remove everything, delete that directory as well.

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
- There is an icon in the menu bar. Click it to see the status, open the window or quit.
- Closing the window (red button or `Cmd+W`) only hides it. The background service keeps running, so sharing and the local API are not interrupted.
- The app quits only when you quit from the menu or press `Cmd+Q`. If the app started the background service, it stops too.
:::

::: linux
- The Linux edition has no tray icon. Closing the window quits the app, and the background service stops with it.
- To keep sharing models in the background, [run it from the command line](/docs/running#background).
:::

::: windows
- There is an icon in the notification area. Left-click opens the window, and right-click opens the menu (status, open, quit). Windows 11 may put a new icon in the `^` overflow area. You can drag it to the taskbar.
- Closing the window (X) only hides it. The background service keeps running, so sharing and the local API are not interrupted.
- The app quits only when you quit from the menu. If the app started the background service, it stops too.
:::

Starting the program again only brings back the existing window.

## Graphical interface without the app {#browser-ui}

The command-line edition includes the same interface. Run:

```bash
{{exe}} daemon
```

Then open `http://127.0.0.1:{{port}}/` in a browser to register and sign in on the page. The difference from `up` is that `daemon` can run while signed out, and it joins the network only after you sign in.

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

## Usage statistics {#telemetry}

By default, the desktop service (`daemon`) sends anonymous usage statistics, such as whether it is online, whether sign-in succeeded and the system type. They do not include your email, device ID, prompts or model content. The sign-in page and the main screen both have a switch. You can also force it off with the environment variable `SHARETOKEN_TELEMETRY=0` or `DO_NOT_TRACK=1`. `{{exe}} up` does not send statistics.
