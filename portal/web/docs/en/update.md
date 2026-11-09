# Update and uninstall

## Check the version {#version}

```bash
{{exe}} version
```

See the [download section](/#download) for the latest version. If the client is older than the minimum version the network requires, sign-in and model calls return `upgrade_required` (HTTP 426). Settlement and account queries are not affected, and your existing credit is not affected.

## Update {#upgrade}

An update just replaces the program file. Your account, device key and configuration are in the state directory and are not affected, so you do not need to sign in again.

1. Stop the running client (`Ctrl+C`, or stop the [background service](/docs/running#background)).
2. Download the new version and replace the program. The commands are below.
3. Check the version with `{{exe}} version`, then start the client again.

::: macos
```bash
ARCH=$(uname -m); [ "$ARCH" = x86_64 ] && ARCH=amd64
curl -fLO {{origin}}/downloads/sharetoken-cli-{{env}}-{{version}}-macos-$ARCH.tar.gz
tar -xzf sharetoken-cli-{{env}}-{{version}}-macos-$ARCH.tar.gz
mv sharetoken-cli-{{env}}-{{version}}-macos-$ARCH/{{exe}} ~/.local/bin/
```
:::

::: linux
```bash
ARCH=$(uname -m); case $ARCH in x86_64) ARCH=amd64 ;; aarch64) ARCH=arm64 ;; esac
curl -fLO {{origin}}/downloads/sharetoken-cli-{{env}}-{{version}}-linux-$ARCH.tar.gz
tar -xzf sharetoken-cli-{{env}}-{{version}}-linux-$ARCH.tar.gz
mv sharetoken-cli-{{env}}-{{version}}-linux-$ARCH/{{exe}} ~/.local/bin/
```
:::

::: windows
```powershell
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$name = "sharetoken-cli-{{env}}-{{version}}-windows-$arch"
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

### Step 2: Remove the program and background service {#uninstall-program}

::: macos
```bash
launchctl bootout gui/$(id -u)/app.sharetoken.client 2>/dev/null
rm -f ~/Library/LaunchAgents/app.sharetoken.client.plist
rm -f ~/.local/bin/{{exe}}
```
:::

::: linux
```bash
systemctl --user disable --now sharetoken 2>/dev/null
rm -f ~/.config/systemd/user/sharetoken.service
rm -f ~/.local/bin/{{exe}}
```
:::

::: windows
```powershell
Unregister-ScheduledTask -TaskName ShareToken -Confirm:$false -ErrorAction SilentlyContinue
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
