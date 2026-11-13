# 安装命令行版

命令行版是一个独立的可执行文件，没有其他依赖，也不需要窗口系统。

## 系统要求 {#requirements}

::: macos
macOS 13 或更新版本，Apple Silicon 或 Intel。
:::

::: linux
任何较新的 64 位发行版，x86-64 或 ARM64。程序是静态链接的，不依赖 glibc 版本，服务器和容器都能运行。
:::

::: windows
Windows 10 或 11，x64 或 ARM64。下面的命令都在 PowerShell 里运行。
:::

## 第 1 步：确认 CPU 架构 {#arch}

::: macos
```bash
uname -m
```

`arm64` 对应 Apple Silicon（M 系列），`x86_64` 对应 Intel。
:::

::: linux
```bash
uname -m
```

`x86_64` 对应 x86-64（amd64），`aarch64` 对应 ARM64（arm64）。
:::

::: windows
```powershell
$env:PROCESSOR_ARCHITECTURE
```

`AMD64` 对应 x64，`ARM64` 对应 ARM64。
:::

## 第 2 步：下载 {#download}

::: downloads
:::

也可以用命令行下载。把 `ARCH` 换成上表里的 `amd64` 或 `arm64`：

::: macos
```bash
ARCH=arm64
curl -fLO {{origin}}/downloads/sharetoken-cli-{{version}}-macos-$ARCH.tar.gz
```
:::

::: linux
```bash
ARCH=amd64
curl -fLO {{origin}}/downloads/sharetoken-cli-{{version}}-linux-$ARCH.tar.gz
```
:::

::: windows
```powershell
$arch = 'amd64'
$name = "sharetoken-cli-{{version}}-windows-$arch"
curl.exe -fLO "{{origin}}/downloads/$name.zip"
```
:::

## 第 3 步：校验（推荐） {#verify}

确认下载的文件没有损坏或被替换。校验值的来源是这个网站的 HTTPS。

::: macos
```bash
curl -fLO {{origin}}/downloads/SHA256SUMS-{{version}}
shasum -a 256 -c --ignore-missing SHA256SUMS-{{version}}
```

应当输出 `OK`。
:::

::: linux
```bash
curl -fLO {{origin}}/downloads/SHA256SUMS-{{version}}
sha256sum -c --ignore-missing SHA256SUMS-{{version}}
```

应当输出 `OK`。
:::

::: windows
```powershell
curl.exe -fLO "{{origin}}/downloads/SHA256SUMS-{{version}}"
$expected = (Select-String -Path "SHA256SUMS-{{version}}" -SimpleMatch "$name.zip").Line.Split(' ')[0]
(Get-FileHash "$name.zip" -Algorithm SHA256).Hash -ieq $expected
```

应当输出 `True`。
:::

## 第 4 步：解压并放到 PATH {#path}

::: macos
```bash
tar -xzf sharetoken-cli-{{version}}-macos-$ARCH.tar.gz
mkdir -p ~/.local/bin
mv sharetoken-cli-{{version}}-macos-$ARCH/{{exe}} ~/.local/bin/
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.zshrc
source ~/.zshrc
```

> [!NOTE]
> 用浏览器下载的文件带隔离标记，第一次运行可能提示“无法验证开发者”。程序还没有 Apple 公证，核对校验值后可以去掉标记：
>
> `xattr -d com.apple.quarantine ~/.local/bin/{{exe}}`
>
> 用 curl 下载的文件没有这个标记。
:::

::: linux
```bash
tar -xzf sharetoken-cli-{{version}}-linux-$ARCH.tar.gz
mkdir -p ~/.local/bin
mv sharetoken-cli-{{version}}-linux-$ARCH/{{exe}} ~/.local/bin/
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.bashrc
source ~/.bashrc
```

zsh 用户把 `~/.bashrc` 换成 `~/.zshrc`。想给所有用户安装：

```bash
sudo install -m 755 sharetoken-cli-{{version}}-linux-$ARCH/{{exe}} /usr/local/bin/
```
:::

::: windows
```powershell
Expand-Archive "$name.zip" -DestinationPath . -Force
$dir = "$HOME\AppData\Local\Programs\sharetoken"
New-Item -ItemType Directory -Force $dir | Out-Null
Move-Item -Force "$name\{{exe}}.exe" $dir
Unblock-File "$dir\{{exe}}.exe"
[Environment]::SetEnvironmentVariable('Path', [Environment]::GetEnvironmentVariable('Path', 'User') + ";$dir", 'User')
$env:Path += ";$dir"
```

新打开的 PowerShell 窗口会自动带上新的 PATH。程序还没有代码签名，`Unblock-File` 去掉下载标记。如果用资源管理器双击运行，SmartScreen 可能拦下，请始终在终端里运行它。
:::

## 第 5 步：确认 {#check}

```bash
{{exe}} version
```

输出里有版本号、网络（`env prod`）、门户地址、数据目录和本地 API 地址（`127.0.0.1:{{port}}`）。这个命令不会连网，也不会写任何文件。接下来去[登录](/docs/account#login)。
