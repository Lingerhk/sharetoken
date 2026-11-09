# Linux 调试环境备忘

调试 Linux 桌面客户端和命令行版用的 Ubuntu 虚拟机。客户端本身见 [gui.md](gui.md)。

## 虚拟机

| 项 | 值 |
|---|---|
| 位置 | VMware Fusion，`~/Virtual Machines.localized/Ubuntu24-arm64.vmwarevm` |
| 系统 | Ubuntu 24.04.5 Desktop arm64（Wayland），2 vCPU、4 GB 内存、40 GB 磁盘、NAT |
| 镜像 | `~/Virtual Machines.localized/VMWIsoImages/ubuntu-24.04/`，已按官方 SHA256 校验 |
| 账号 | `stuser`，桌面自动登录，sudo 免密码，已关闭自动锁屏 |
| 已装 | 构建依赖（见 [gui.md](gui.md)）、Go（`/usr/local/go`）、`ydotool`、`gnome-screenshot` |

```bash
ssh st-ubuntu     # 密钥 ~/.ssh/sharetoken_ubuntu_ed25519；IP 由 DHCP 分配，变了就改 ~/.ssh/config
VMRUN="/Applications/VMware Fusion.app/Contents/Library/vmrun"
VMX="$HOME/Virtual Machines.localized/Ubuntu24-arm64.vmwarevm/Ubuntu24-arm64.vmx"
"$VMRUN" -T fusion start "$VMX" gui
"$VMRUN" -T fusion suspend "$VMX"   # 用完暂停；这台没有加密，不需要密码
```

## 传代码和构建

虚拟机里没有 git。只传被跟踪和未忽略的文件，防止密钥文件进虚拟机；`COPYFILE_DISABLE` 防止 macOS 打入 `._*` 文件：

```bash
git ls-files -co --exclude-standard | grep -v '^research_notes/' | COPYFILE_DISABLE=1 tar --no-xattrs -czf /tmp/src.tgz -T -
scp /tmp/src.tgz st-ubuntu:/tmp/
# 虚拟机里：解压后设置 ST_VERSION（没有 .git 时 scripts/version.sh 不会输出）
export PATH=/usr/local/go/bin:$PATH ST_COMMIT=<提交> ST_VERSION=<版本>
scripts/build-gui.sh dev && scripts/build-cli.sh dev
(cd gui && go test -race -tags desktop,production,webkit2_41 .)
```

## 窗口测试

SSH 里要指向桌面会话，才能在真实桌面上打开窗口：

```bash
export XDG_RUNTIME_DIR=/run/user/1000 DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus WAYLAND_DISPLAY=wayland-0 DISPLAY=:0
scripts/gui_shell_test.sh dev <程序目录>
gnome-screenshot -f /tmp/g.png
```

- 输入用 `sudo ydotool key --delay 1500 --key-delay 60 alt+F4`：工具每次新建虚拟设备，延迟太短时事件会丢。表单输入不可靠，只用它做关窗这类单次按键。
- 不要用 Mac 上的 `open -a` 或全屏截图：Mac 桌面是用户在用的，截 VM 用 `gnome-screenshot`。

## 端到端

```bash
GUI_STACK_OFFSET=1000 scripts/gui_stack.sh start DIR && . DIR/env   # 偏移量让它和别的会话的测试网络互不影响
ssh -N -R 127.0.0.1:19092:127.0.0.1:19092 -R 127.0.0.1:15201:127.0.0.1:15201 st-ubuntu
# 虚拟机里：
PORTAL=https://127.0.0.1:19092 PIN=<PIN> scripts/cli_test.sh <sharetoken-dev 程序>
```

默认偏移量下数据库名 `st_gui_stack` 和端口都固定，两个 `gui_stack.sh` 同时运行时后者会重建数据库，前者的账户全部失效。
