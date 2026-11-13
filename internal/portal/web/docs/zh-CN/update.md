# 升级和卸载

## 查看版本 {#version}

```bash
{{exe}} version
```

客户端会自己检查新版本：桌面应用在页面顶部、托盘菜单和系统通知里提示，命令行客户端在日志里提示，`{{exe}} status` 也会显示。也可以手动检查：

```bash
{{exe}} version --check
```

最新版本见[下载区](/#download)。客户端低于网络要求的最低版本时，登录和调用模型会返回 `upgrade_required`（HTTP 426），但结算和账户查询不受影响，已有的 credit 不受影响。

## 一键升级 {#one-click}

桌面应用可以自己安装新版本：点窗口顶部提示条上的 **立即更新**。它会下载安装包，校验 ShareToken 发布签名，等进行中的作业结束，然后安装并重启。macOS 上应用要放在"应用程序"文件夹里（不能直接从磁盘映像里打开）；Linux 上系统会像安装其他软件包一样要求输入管理员密码。

命令行客户端用这个命令做同样的事：

```bash
{{exe}} upgrade
```

安装前会先确认（加 `-y` 不确认），并按这份程序的安装方式安装：命令行压缩包里的程序文件、macOS 应用（先退出应用，或在应用里升级）、Windows 安装程序，或者用 `sudo dpkg -i` 安装 `.deb`。没有有效发布签名的安装包一律不装。正在运行的 `{{exe}} up` 重启之后才换成新版本。

## 手动升级 {#upgrade}

升级就是替换程序文件。账户、设备密钥和配置都在数据目录里，不受影响，不需要重新登录。

1. 停止正在运行的客户端（`Ctrl+C`，或者停掉[后台服务](/docs/running#background)）。
2. 下载新版本并替换程序，命令见下面。
3. 用 `{{exe}} version` 确认版本，然后重新启动客户端。
4. 如果[给编程助手装过 skill](/docs/agents)，再运行一次 `{{exe}} skill install`。

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

## 卸载 {#uninstall}

### 第 1 步：退出登录 {#uninstall-logout}

这会吊销这台机器上的 token：

```bash
{{exe}} logout
```

如果这台机器不再使用，到门户的[设备页面](/app/#/devices)解绑它。

如果[给编程助手装过 skill](/docs/agents)，趁程序还在先卸载它：

```bash
{{exe}} skill uninstall
```

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
