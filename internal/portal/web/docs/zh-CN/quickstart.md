# 快速开始

从下载到第一次调用模型，大约 5 分钟。请先在页面上方选择你的系统。

> [!TIP]
> 这里用的是命令行版。想用图形界面的话，请安装[桌面应用](/docs/desktop)。

## 1. 下载并安装 {#download}

::: macos linux
下面的命令自动判断 CPU 架构，把程序装到 `~/.local/bin`，不需要 sudo。
:::

::: macos
```bash
ARCH=$(uname -m); [ "$ARCH" = x86_64 ] && ARCH=amd64
curl -fLO {{origin}}/downloads/sharetoken-cli-{{version}}-macos-$ARCH.tar.gz
tar -xzf sharetoken-cli-{{version}}-macos-$ARCH.tar.gz
mkdir -p ~/.local/bin
mv sharetoken-cli-{{version}}-macos-$ARCH/{{exe}} ~/.local/bin/
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.zshrc
source ~/.zshrc
```
:::

::: linux
```bash
ARCH=$(uname -m); case $ARCH in x86_64) ARCH=amd64 ;; aarch64) ARCH=arm64 ;; esac
curl -fLO {{origin}}/downloads/sharetoken-cli-{{version}}-linux-$ARCH.tar.gz
tar -xzf sharetoken-cli-{{version}}-linux-$ARCH.tar.gz
mkdir -p ~/.local/bin
mv sharetoken-cli-{{version}}-linux-$ARCH/{{exe}} ~/.local/bin/
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.bashrc
source ~/.bashrc
```
:::

::: windows
在 PowerShell 里运行。程序装到你自己的目录，不需要管理员权限。

```powershell
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$name = "sharetoken-cli-{{version}}-windows-$arch"
curl.exe -fLO "{{origin}}/downloads/$name.zip"
Expand-Archive "$name.zip" -DestinationPath . -Force
$dir = "$HOME\AppData\Local\Programs\sharetoken"
New-Item -ItemType Directory -Force $dir | Out-Null
Move-Item -Force "$name\{{exe}}.exe" $dir
Unblock-File "$dir\{{exe}}.exe"
[Environment]::SetEnvironmentVariable('Path', [Environment]::GetEnvironmentVariable('Path', 'User') + ";$dir", 'User')
$env:Path += ";$dir"
```
:::

确认安装成功：

```bash
{{exe}} version
```

能看到版本号、门户地址和本地 API 地址就对了。想手动选择安装包或校验文件，见[安装命令行版](/docs/install)。

## 2. 登录 {#register}

```bash
{{exe}} login -email you@example.com
```

没有密码：输入邮箱收到的 6 位验证码即可。一个邮箱第一次登录时会自动创建账户。完成后这台机器就绑定到了你的账户。

## 3. 启动客户端 {#up}

```bash
{{exe}} up
```

看到下面这一行就说明已经连上网络。这个窗口要保持运行，按 `Ctrl+C` 停止。

```text
   OpenAI-compatible endpoint: http://127.0.0.1:{{port}}/v1
```

## 4. 调用模型 {#call}

打开**另一个**终端窗口，先看网络上有哪些模型：

```bash
{{exe}} models
```

把下面的 `MODEL` 换成上一步列出的模型 ID：

::: macos linux
```bash
curl http://127.0.0.1:{{port}}/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"MODEL","messages":[{"role":"user","content":"Hello"}],"max_tokens":200}'
```
:::

::: windows
```powershell
$body = @{ model = 'MODEL'; max_tokens = 200; messages = @(@{ role = 'user'; content = 'Hello' }) } | ConvertTo-Json -Depth 5
Invoke-RestMethod http://127.0.0.1:{{port}}/v1/chat/completions -Method Post -ContentType 'application/json' -Body $body | ConvertTo-Json -Depth 8
```
:::

最后看看花了多少：

```bash
{{exe}} balance
```

> [!TIP]
> `models` 没有列出模型或调用返回 `no_offers`，说明此刻没有节点在线提供模型。你也可以把自己的模型共享出去，见[共享模型](/docs/share)。

## 下一步 {#next}

::: cards
[调用模型](/docs/use-models) 接入 Python、Node.js 和各种 OpenAI 兼容工具。
[共享模型](/docs/share) 把本机模型共享出去赚 credit。
[运行客户端](/docs/running) 让客户端在后台一直运行。
[在编程助手中使用](/docs/agents) 让 Claude Code 或 Codex 替你操作客户端。
:::
