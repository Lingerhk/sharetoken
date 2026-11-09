# Desktop app

The desktop app gives you a graphical interface to register, sign in, and check your account and network status. Settings for sharing models still use the command line for now.

> [!NOTE]
> The [download section](/#download) is the source of truth for which versions you can download. A version marked "coming soon" cannot be downloaded yet. In the meantime, use the [graphical interface without the app](#browser-ui) below.

## Install {#install}

::: macos
1. Unzip `sharetoken-{{env}}-<version>-macos-<arch>.zip` to get `sharetoken-{{env}}.app`.
2. Drag it to the "Applications" folder.
3. If the first launch says the developer cannot be verified, open "System Settings > Privacy & Security" and click "Open Anyway" at the bottom of the page. The app is not yet notarized by Apple.
:::

::: linux
Requires WebKitGTK. On Ubuntu 24.04:

```bash
sudo apt install libgtk-3-0t64 libwebkit2gtk-4.1-0
```

After unpacking, run the install script. It installs the program to `~/.local` and adds an application menu entry:

```bash
tar -xzf sharetoken-{{env}}-<version>-linux-<arch>.tar.gz
cd sharetoken-{{env}}-<version>-linux-<arch>
./install-linux.sh
```
:::

::: windows
1. Unzip `sharetoken-{{env}}-<version>-windows-<arch>.zip` to a permanent directory.
2. Run `sharetoken-gui-{{env}}.exe`. If SmartScreen blocks it, click "More info" and then "Run anyway". The app is not yet code-signed.
3. WebView2 Runtime is required. Windows 11 includes it. If Windows 10 does not have it, install it from the Microsoft website.
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

## Usage statistics {#telemetry}

By default, the desktop service (`daemon`) sends anonymous usage statistics, such as whether it is online, whether sign-in succeeded and the system type. They do not include your email, device ID, prompts or model content. The sign-in page and the main screen both have a switch. You can also force it off with the environment variable `SHARETOKEN_TELEMETRY=0` or `DO_NOT_TRACK=1`. `{{exe}} up` does not send statistics.
