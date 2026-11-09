# Windows 调试环境备忘

调试桌面客户端用的 Windows 虚拟机，以及在上面构建、测试的方法。客户端本身的结构见 [gui.md](gui.md)。

## 虚拟机

| 项 | 值 |
|---|---|
| 位置 | VMware Fusion，`~/Virtual Machines.localized/Windows11-arm64.vmwarevm` |
| 系统 | Windows 11 Enterprise ARM64，build 22631（23H2）。注册表 `ProductName` 显示 “Windows 10”，这是 Windows 11 的已知现象，以 build 号为准 |
| 配置 | 2 vCPU、4 GB 内存、NAT（vmnet8），系统盘剩余约 40 GB |
| 账号 | `adadmin`（本地管理员） |
| 已有 | Go 1.25（低于项目要求，不用它构建）、WebView2 Runtime |
| 没有 | git、gcc、node、wails，当前流程都不需要 |

- 虚拟机启用了 vTPM，**整机加密**。`vmrun` 没有密码打不开，开机和解锁要在 Fusion 里手动操作。
- 系统版本 23H2 的企业版 2026-11-10 起不再有安全更新，只用于调试。
- ISO（`VMWIsoImages/Windows11_22631.2861_Enterprise_zh-cn_arm64.iso`）不是微软原版，注入过 VMware 驱动（`$WinPEDriver$`）。不要用这台机器的结果代替干净系统上的验证。

## 连接

```bash
ssh st-win      # adadmin@172.16.224.129，默认 shell 是 PowerShell
```

`~/.ssh/config` 里的配置：

```
Host st-win
    HostName 172.16.224.129
    User adadmin
    IdentityFile ~/.ssh/sharetoken_win_ed25519
    IdentitiesOnly yes
```

- 只允许密钥登录。公钥在虚拟机的 `C:\ProgramData\ssh\administrators_authorized_keys`，这个文件只能有 SYSTEM 和 Administrators 的权限，否则 sshd 会忽略它。
- 防火墙只放行 `172.16.224.0/24` 的 22 端口（规则名 `sshd-vmnet8`），局域网其他机器连不进来。
- **IP 是 DHCP 分配的，可能变。** 连不上时在虚拟机里运行 `ipconfig`，再改 `HostName`。
- sshd 是 Win32-OpenSSH 的 ARM64 MSI，从 GitHub 安装。

## 构建

在 Mac 上交叉编译（Windows 版窗口壳不需要 cgo），再传到虚拟机：

```bash
scripts/build-gui.sh dev windows arm64     # 产物 dist/sharetoken-dev-<版本>-windows-arm64/
scp dist/sharetoken-dev-*-windows-arm64/*.exe st-win:C:/Users/adadmin/st/app/
```

调试用 dev 构建，它不发送统计。调试 prod 构建时一定带 `TELEMETRY_OFF=1`，否则会向真实统计项目发送事件。

## 跑单元测试

```bash
GOOS=windows GOARCH=arm64 CGO_ENABLED=0 go test -c -o client.test.exe ./client
(cd gui && GOOS=windows GOARCH=arm64 CGO_ENABLED=0 go test -c -tags desktop,production -o gui.test.exe .)
scp client.test.exe st-win:C:/Users/adadmin/st/test/
ssh st-win "& 'C:\Users\adadmin\st\test\client.test.exe' '-test.timeout=280s'"
```

- PowerShell 会把未加引号的 `-test.timeout` 拆成两个参数，**`-test.*` 参数必须加引号**，否则测试不会启动，只以退出码 2 结束。
- 不要设 `SHARETOKEN_TELEMETRY=0`，使用统计的测试会失败。这些测试用假收集器，不会发送真实数据。
- `attest/sevsnp` 的测试不能编译：第三方 `go-sev-guest` 的 mock 只支持 Unix。它只影响服务端的测试，与客户端无关。

## 显示窗口

SSH 会话在 session 0，看不到桌面，也打不开窗口。要运行窗口程序或截图，必须在已登录的桌面会话（session 1）里启动，用一次性计划任务：

```powershell
$a = New-ScheduledTaskAction -Execute 'conhost.exe' -Argument '--headless cmd /c C:\Users\adadmin\st\start.cmd'
$p = New-ScheduledTaskPrincipal -UserId adadmin -LogonType Interactive -RunLevel Limited
Register-ScheduledTask -TaskName st-gui -Action $a -Principal $p -Force | Out-Null
Start-ScheduledTask -TaskName st-gui
```

- 前提是 `adadmin` 已登录桌面且没有锁屏。
- `conhost --headless` 用来避免 `cmd` 窗口盖住应用；截图、`CloseMainWindow()` 也要用这种方式运行。
- `start.cmd` 里设置 `SHARETOKEN_HOME`，并用 `start "" sharetoken-gui-dev.exe` 启动。
- 窗口壳链接了 `-H windowsgui`，只有把输出重定向到文件才能拿到 stdout。自检的结果就是这样读取的：`SHARETOKEN_GUI_SELFTEST=1` 时运行 `sharetoken-gui-dev.exe > out.txt`。
- 脚本 `scripts/gui_shell_test.sh` 是 bash 写的，不能在虚拟机上运行，要按它的步骤手工做。
- 托盘自检：`SHARETOKEN_GUI_SELFTEST=tray`，并用独立的 `SHARETOKEN_HOME`（`config.json` 写私有端口），运行 `sharetoken-gui-dev.exe > out.txt 2>&1`。通过时输出 `selftest: ok`，退出码 0。窗口会显示。
- 用 PowerShell 操作真实控件时注意：传给 `FindWindow` 的 `$null` 会变成空字符串，要写 `[NullString]::Value`。托盘按钮的 UIA 名称前有一个空格，任务栏按钮是 `sharetoken-gui-dev - 1 个运行窗口`。托盘菜单是 Win32 菜单（类名 `#32768`），UIA 查不到菜单项名称。不要盲点坐标：溢出区没展开时，右键会落到桌面菜单上。
- `Start-Process` 启动的程序会继承脚本的输出句柄，脚本的输出文件要每次换名。

## 时钟

虚拟机暂停恢复后时钟可能不同步，慢得多时登录会失败：`bind_failed`、`device signature is too old or from the future`。在 Mac 上执行 `ssh st-win "Set-Date -Date ([datetime]'$(date -u +%FT%TZ)')"` 校正。

## 用完暂停

测试结束后暂停虚拟机：Fusion 菜单“虚拟机 › Suspend”。启动用 `open` 打开 `.vmwarevm` 目录，由 Fusion 启动。这台机器加密，`vmrun` 的 start、suspend 都要密码，用不了。

## 端到端测试

不用改配置：在 Mac 上启动本地测试网络，把门户和中继的端口转发到虚拟机的回环地址，地址和证书就和本机一样。

```bash
scripts/gui_stack.sh start /tmp/st-stack     # 门户 18092、中继 14201；PORTAL 和 PIN 写在 /tmp/st-stack/env
ssh -N -R 127.0.0.1:18092:127.0.0.1:18092 -R 127.0.0.1:14201:127.0.0.1:14201 st-win
```

然后在虚拟机里启动程序，portal 填 `https://127.0.0.1:18092`。结束后运行 `scripts/gui_stack.sh stop /tmp/st-stack`。

## 平台差异

| 问题 | 做法 |
|---|---|
| 文件权限 | Go 在 Windows 上总是报告 0666，权限位没有意义。`internal/secfile` 改用 ACL：`device.key`、`config.json` 只给本人、SYSTEM、Administrators，不继承目录权限。读取密钥时发现其他账户有权限就拒绝，并给出 `icacls` 修复命令 |
| 停止 daemon | Windows 不能发 SIGTERM。窗口壳持有 daemon 的 stdin，关闭后 daemon 正常退出（`-stop-on-stdin-eof`），各平台相同。窗口壳崩溃时管道关闭，daemon 也会退出 |
| 控制台窗口 | daemon 是控制台程序，用 `CREATE_NO_WINDOW` 启动，避免弹黑框 |
| 程序名 | `findBinary` 找 `sharetoken.exe`，不检查可执行位 |
| 菜单 | Wails 在 Windows 上把菜单角色画成空白菜单，所以不设窗口菜单。复制粘贴由 WebView2 处理。托盘菜单是另一回事，见 [gui.md](gui.md) |
| 图标和 manifest | `go-winres` 在构建时生成 `gui/rsrc_windows_*.syso`，构建后删除。没有它就没有图标和 DPI 感知 |
| 页面来源 | WebView2 的 origin 是 `http://wails.localhost`（macOS 是 `wails://wails`）。代理会去掉 Origin，daemon 不受影响 |

## 已知问题

- **首次登录弹防火墙提示。** daemon 默认在所有网卡上监听 libp2p，Windows 会询问是否放行，发行者显示“未知”。点取消后系统生成入站阻止规则，买方走中继不受影响，已实测推理成功；直连和卖方的入站连接会受影响，这部分没有测过。是否改变默认监听还没有决定。
- **没有代码签名。** 下载后的程序可能被 SmartScreen 拦截。
- **Windows 上还没验证：** 卖方（共享模型）；x64 实机（只确认能编译）；没有预装 WebView2 的系统；无头 Chrome 界面测试（`gui_ui_test.mjs`）。
