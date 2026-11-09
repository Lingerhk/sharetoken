# 升级和卸载

## 查看版本 {#version}

```bash
{{exe}} version
```

最新版本见[下载区](/#download)。客户端低于网络要求的最低版本时，登录和调用模型会返回 `upgrade_required`（HTTP 426），但结算和账户查询不受影响，已有的 credit 不受影响。

## 升级 {#upgrade}

升级就是替换程序文件。账户、设备密钥和配置都在数据目录里，不受影响，不需要重新登录。

1. 停止正在运行的客户端（`Ctrl+C`，或者停掉[后台服务](/docs/running#background)）。
2. 下载新版本并替换程序，命令见下面。
3. 用 `{{exe}} version` 确认版本，然后重新启动客户端。

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

## 卸载 {#uninstall}

### 第 1 步：退出登录 {#uninstall-logout}

这会吊销这台机器上的 token：

```bash
{{exe}} logout
```

如果这台机器不再使用，到门户的[设备页面](/app/#/devices)解绑它。

### 第 2 步：删除程序和后台服务 {#uninstall-program}

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

### 第 3 步：删除数据目录（可选） {#uninstall-data}

> [!WARNING]
> 这会删除设备密钥和配置。credit 记在账户上，不会丢失；重新安装后用同一个账户登录即可，这台机器会成为一台新设备。

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
