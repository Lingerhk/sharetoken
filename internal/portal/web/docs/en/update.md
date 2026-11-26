# Update and uninstall

## Check the version {#version}

```bash
{{exe}} version
```

A running client checks for new versions when it starts and every hour after that: the desktop app shows them at the top of its window, in the tray menu and as a system notification, and the command-line client writes them to its log. `{{exe}} status` shows them too. To check by hand:

```bash
{{exe}} version --check
```

See the [download section](/#download) for the latest version. If the client is older than the minimum version the network requires, sign-in and model calls return `upgrade_required` (HTTP 426). Settlement and account queries are not affected, and your existing credit is not affected.

## One-click update {#one-click}

The desktop app installs a new version by itself: click **Update now** in the line at the top of its window. It downloads the package, checks that it carries the ShareToken release signature, lets jobs in progress finish, installs it and restarts. On Windows the installer runs silently in the background; on Linux the system asks for an administrator password, as for any package.

Only an app installed from its package can update itself this way: on macOS it must be in the Applications folder (not opened from the disk image), on Windows installed with the installer, and on Linux installed from the `.deb`, with `pkexec` on the system. Otherwise the line offers only "Details", with the download to [update by hand](#upgrade). "Skip this version" stops offering a version you do not want; a required upgrade cannot be skipped.

The command-line client does the same with:

```bash
{{exe}} upgrade
```

It asks before installing (`-y` does not ask) and installs the package the way this copy was installed: the program file of the command-line archive, the macOS app (quit it first, or update from the app), the Windows installer (it runs in the background; open the app again when it is done), or the `.deb` with `sudo dpkg -i`. A package without a valid release signature is never installed. A running `{{exe}} up` or `{{exe}} daemon` keeps the old version until you restart it.

## Update by hand {#upgrade}

An update just replaces the program file. Your account, device key and configuration are in the state directory and are not affected, so you do not need to sign in again. For the desktop app, download the new package and [install it as the first time](/docs/desktop#install); it replaces the old version. For the command-line edition:

1. Stop the running client (`Ctrl+C`, or stop the [background service](/docs/running#background)).
2. Download the new version and replace the program. The commands are below.
3. Check the version with `{{exe}} version`, then start the client again.
4. If you [installed the skill for coding agents](/docs/agents), run `{{exe}} skill install` again.

::: macos
```bash
ARCH=$(uname -m); [ "$ARCH" = x86_64 ] && ARCH=amd64
curl -fLO {{origin}}/downloads/sharetoken-cli-{{version}}-macos-$ARCH.tar.gz
tar -xzf sharetoken-cli-{{version}}-macos-$ARCH.tar.gz
mv sharetoken-cli-{{version}}-macos-$ARCH/{{exe}} ~/.local/bin/
```
:::

::: linux
```bash
ARCH=$(uname -m); case $ARCH in x86_64) ARCH=amd64 ;; aarch64) ARCH=arm64 ;; esac
curl -fLO {{origin}}/downloads/sharetoken-cli-{{version}}-linux-$ARCH.tar.gz
tar -xzf sharetoken-cli-{{version}}-linux-$ARCH.tar.gz
mv sharetoken-cli-{{version}}-linux-$ARCH/{{exe}} ~/.local/bin/
```
:::

::: windows
```powershell
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$name = "sharetoken-cli-{{version}}-windows-$arch"
curl.exe -fLO "{{origin}}/downloads/$name.zip"
Expand-Archive "$name.zip" -DestinationPath . -Force
Move-Item -Force "$name\{{exe}}.exe" "$HOME\AppData\Local\Programs\sharetoken\"
Unblock-File "$HOME\AppData\Local\Programs\sharetoken\{{exe}}.exe"
```
:::

## Uninstall {#uninstall}

### Step 1: Sign out {#uninstall-logout}

This revokes the token on this machine:

```bash
{{exe}} logout
```

If you will not use this machine any more, unbind it on the portal's [devices page](/app/#/devices).

If you installed the [skill for coding agents](/docs/agents), remove it while the program is still there:

```bash
{{exe}} skill uninstall
```

If you connected Claude Code or Codex with `{{exe}} connect`, put back their own settings first (the backups are in the state directory, which step 3 deletes), and turn off the Codex Desktop unlock (which makes the system stop trusting its localhost certificate). A command for something you never set up does nothing:

```bash
{{exe}} codex-unlock off
{{exe}} connect claude -off
{{exe}} connect codex -off
```

### Step 2: Remove the program and background service {#uninstall-program}

This removes the command-line edition and its background service. `{{exe}} autostart off` removes the service set up with `{{exe}} autostart on` as in [Run in the background](/docs/running#background), and does nothing when there is none. Uninstall the desktop app [its own way](/docs/desktop#upgrade); that removes its command-line program too.

::: macos
```bash
{{exe}} autostart off
rm -f ~/.local/bin/{{exe}}
```
:::

::: linux
```bash
{{exe}} autostart off
rm -f ~/.local/bin/{{exe}}
```
:::

::: windows
```powershell
{{exe}} autostart off
Stop-Process -Name {{exe}} -ErrorAction SilentlyContinue
Remove-Item "$HOME\AppData\Local\Programs\sharetoken\{{exe}}.exe"
```
:::

### Step 3: Remove the state directory (optional) {#uninstall-data}

> [!WARNING]
> This deletes the device key and configuration. Your credit is recorded on the account and is not lost. After you reinstall and sign in with the same account, this machine becomes a new device.

::: macos linux
```bash
rm -rf {{home}}
```
:::

::: windows
```powershell
Remove-Item -Recurse -Force "{{homewin}}"
```
:::

The logs are in the state directory's `logs` by default and go with it. If you moved them elsewhere with `log.dir` in the configuration, delete that directory too. If you once set up a service by hand following an older version of this guide, `~/Library/Logs/sharetoken.log` (macOS) or `$HOME\AppData\Local\sharetoken.log` (Windows) may be left; delete it too.
