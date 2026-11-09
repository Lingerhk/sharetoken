# 桌面应用

桌面应用提供图形界面：注册、登录、查看账户和网络状态。共享模型的设置目前仍然用命令行。

> [!NOTE]
> 可下载的版本以[下载区](/#download)为准。标着“即将上线”的版本暂时还不能下载，现在可以先用下面的[不装窗口的图形界面](#browser-ui)。

## 安装 {#install}

::: macos
1. 解压 `sharetoken-{{env}}-<版本>-macos-<架构>.zip`，得到 `sharetoken-{{env}}.app`。
2. 把它拖到“应用程序”文件夹。
3. 第一次打开时，如果提示无法验证开发者：打开“系统设置 > 隐私与安全性”，在页面下方点“仍要打开”。应用还没有 Apple 公证。
:::

::: linux
需要 WebKitGTK。Ubuntu 24.04 上：

```bash
sudo apt install libgtk-3-0t64 libwebkit2gtk-4.1-0
```

解压后运行安装脚本，它把程序装到 `~/.local` 并添加应用菜单项：

```bash
tar -xzf sharetoken-{{env}}-<version>-linux-<arch>.tar.gz
cd sharetoken-{{env}}-<version>-linux-<arch>
./install-linux.sh
```
:::

::: windows
1. 解压 `sharetoken-{{env}}-<版本>-windows-<架构>.zip` 到一个固定的目录。
2. 运行 `sharetoken-gui-{{env}}.exe`。如果 SmartScreen 拦下，点“更多信息”再点“仍要运行”。应用还没有代码签名。
3. 需要 WebView2 Runtime。Windows 11 自带，Windows 10 没有时请从 Microsoft 网站安装。
:::

## 窗口和后台服务 {#behaviour}

应用由两部分组成：窗口，以及真正做事的后台服务（`{{exe}} daemon`）。所有逻辑都在后台服务里，窗口只是显示它。

::: macos
- 菜单栏有一个图标，点击它可以看到状态、打开窗口或退出。
- 关闭窗口（红色按钮或 `Cmd+W`）只是隐藏窗口。后台服务继续运行，共享和本地 API 不中断。
- 从菜单退出或按 `Cmd+Q` 才会退出应用。后台服务如果是应用启动的，会一起停止。
:::

::: linux
- Linux 版没有托盘图标。关闭窗口就是退出应用，后台服务一起停止。
- 想一直在后台共享模型，请用[命令行方式运行](/docs/running#background)。
:::

::: windows
- 通知区有一个图标：左键打开窗口，右键打开菜单（状态、打开、退出）。Windows 11 可能把新图标收进 `^` 溢出区，你可以把它拖到任务栏上。
- 关闭窗口（X）只是隐藏窗口。后台服务继续运行，共享和本地 API 不中断。
- 从菜单退出才会退出应用。后台服务如果是应用启动的，会一起停止。
:::

再次启动程序只会唤回已有的窗口。

## 不装窗口的图形界面 {#browser-ui}

命令行版自带同一套界面。运行：

```bash
{{exe}} daemon
```

然后用浏览器打开 `http://127.0.0.1:{{port}}/`，就可以在页面上注册和登录。和 `up` 的区别是：`daemon` 未登录也能运行，登录后才接入网络。

## 和命令行一起用 {#with-cli}

桌面应用和命令行版共用数据目录 `{{home}}`。应用运行时，`{{exe}} status`、`{{exe}} models`、`{{exe}} share add` 等命令都直接作用于它。同一个数据目录上不要再运行 `{{exe}} up`，否则同一把设备密钥会同时在线两次，客户端会报错退出。

## 使用统计 {#telemetry}

桌面服务（`daemon`）默认发送匿名使用统计，比如是否在线、登录成败和系统类型，不包含邮箱、设备 ID、prompt 或模型内容。登录页和主界面都有开关。也可以设置环境变量 `SHARETOKEN_TELEMETRY=0` 或 `DO_NOT_TRACK=1` 强制关闭。`{{exe}} up` 不发送统计。
