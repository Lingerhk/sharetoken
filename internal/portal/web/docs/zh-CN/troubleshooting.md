# 故障排查

先运行这两个命令，大多数问题都能看出原因：

```bash
{{exe}} version
{{exe}} status
```

## 安装 {#install}

### 找不到命令 {#not-found}

程序所在的目录不在 PATH 里，或者当前终端还没有加载新的 PATH。

::: macos
```bash
ls -l ~/.local/bin/{{exe}}
echo $PATH
source ~/.zshrc
```
:::

::: linux
```bash
ls -l ~/.local/bin/{{exe}}
echo $PATH
source ~/.bashrc
```
:::

::: windows
打开一个新的 PowerShell 窗口，然后：

```powershell
Test-Path "$HOME\AppData\Local\Programs\sharetoken\{{exe}}.exe"
$env:Path -split ';' | Select-String sharetoken
```
:::

::: macos
### 提示“无法验证开发者”或“已损坏” {#gatekeeper}

程序还没有 Apple 公证，浏览器下载的文件会被 Gatekeeper 拦下。核对[校验值](/docs/install#verify)后：

```bash
xattr -d com.apple.quarantine ~/.local/bin/{{exe}}
```
:::

::: windows
### SmartScreen 或杀毒软件拦截 {#smartscreen}

程序还没有代码签名。核对[校验值](/docs/install#verify)后去掉下载标记，并在终端里运行：

```powershell
Unblock-File "$HOME\AppData\Local\Programs\sharetoken\{{exe}}.exe"
```
:::

::: linux
### 提示 Permission denied {#permission}

```bash
chmod +x ~/.local/bin/{{exe}}
```

如果 home 目录挂载了 `noexec`，请改装到 `/usr/local/bin`，见[安装](/docs/install#path)。
:::

## 账户 {#account}

### 收不到验证码 {#no-code}

先看垃圾邮件文件夹。验证码可能要等几分钟，不要连续重发，发送有频率限制。多次失败请通过[反馈](#feedback)联系我们。

### 提示 `not logged in` {#not-logged-in}

这台机器还没登录，或者数据目录不对。运行 `{{exe}} version` 确认 `home` 一行，然后运行 `{{exe}} login`。

### 提示 `throttled` 或 HTTP 429 {#throttled}

连续失败次数太多，已被限流。等几分钟再试。

### 提示 `logged in, but binding this device failed` {#bind-failed}

有两种常见原因：

- **系统时钟不准。** 消息里有 `too old or from the future`。校准时钟后再登录，命令见下面。
- **这把设备密钥已绑定到另一个账户。** 先在旧账户的[设备页面](/app/#/devices)解绑，或者用[单独的数据目录](/docs/config#home)。

::: macos
```bash
sudo sntp -sS time.apple.com
```
:::

::: linux
```bash
sudo timedatectl set-ntp true
timedatectl status
```
:::

::: windows
在管理员 PowerShell 里运行：

```powershell
w32tm /resync
```
:::

### 提示 `upgrade_required` {#upgrade-required}

客户端版本太旧，[升级](/docs/update#upgrade)后再试。

## 运行 {#running}

### 提示 `the client is not running` {#not-running}

`status`、`models`、`relays` 需要客户端在运行。在另一个窗口运行 `{{exe}} up`，或者启动[后台服务](/docs/running#background)。

### 提示 `cannot reach the portal` {#no-portal}

这台机器连不上门户。检查网络、代理和防火墙：

::: macos linux
```bash
curl -sS {{origin}}/healthz
```
:::

::: windows
```powershell
curl.exe -sS {{origin}}/healthz
```
:::

应该输出 `{"ok":true}`。

### 提示端口被占用 {#port-in-use}

已经有别的程序在用端口 `{{port}}`，通常是另一个正在运行的客户端。

::: macos
```bash
lsof -nP -iTCP:{{port}} -sTCP:LISTEN
```
:::

::: linux
```bash
ss -ltnp | grep {{port}}
```
:::

::: windows
```powershell
Get-NetTCPConnection -LocalPort {{port}} -State Listen | Select-Object OwningProcess
```
:::

停掉它，或者换个端口：`{{exe}} up -http 127.0.0.1:9800`。

### 显示 `not connected` {#not-connected}

客户端还在连接，或者连不上中继。运行 `{{exe}} relays -measure` 看每个中继的结果。公司网络可能拦截 4001 端口，请让管理员放行出站的 TCP 4001 和 UDP 4001。

## 调用模型 {#calling}

### `no_offers` {#no-offers}

此刻没有节点提供这个模型。运行 `{{exe}} models` 确认模型 ID，注意大小写。

如果你的账户已经和卖家配对，就只有这些卖家算数，这时错误消息以 `of the sellers you are paired with` 结尾。运行 `{{exe}} pair` 查看他们。等其中一个上线，或者解除配对，恢复为向整个网络购买。见[只向认识的卖家购买](/docs/use-models#pairing)。

开启了私有模式的卖家对所有没有和它配对的买家隐藏，所以之前看到过的模型可能从 `models` 里消失。

### `capacity` {#capacity}

节点都满载。稍后重试，或者在配置里调大 `queue_seconds`。

### `insufficient_funds` 或 `budget` {#funds}

余额不足以冻结这次请求。给请求加上较小的 `max_tokens`，或者用 `{{exe}} balance` 查看余额。见[如何计费](/docs/use-models#billing)。

### 响应的 `content` 是空的 {#empty-content}

思考型模型的推理过程也计入 `max_tokens`，额度被用完了。调大 `max_tokens`。

### 请求很久后失败 {#stalled}

节点太慢被判定为卡死。调大配置里的 `first_byte_seconds` 和 `idle_seconds`，见[配置](/docs/config#buy)。

## 共享模型 {#sharing}

### `share detect` 报 `cannot list models` {#detect-failed}

模型服务没有运行，或者地址不对。

::: macos
```bash
brew services restart ollama
curl -s http://127.0.0.1:11434/v1/models
```
:::

::: linux
```bash
sudo systemctl restart ollama
curl -s http://127.0.0.1:11434/v1/models
```
:::

::: windows
从开始菜单打开 Ollama，然后：

```powershell
curl.exe -s http://127.0.0.1:11434/v1/models
```
:::

### `status` 里有 `offer not accepted` {#offer-rejected}

协调器拒绝了报价，原因就写在后面。常见的是客户端版本过低，或者节点因为抽检不符被暂停。

### 一直没有订单 {#no-jobs}

运行 `{{exe}} models` 看看现有的节点和价格。协调器综合价格、信誉、延迟和成功率选择节点，可以试试调低价格。

如果开启了私有模式，只有和你配对的买家能向你购买。`{{exe}} pair` 显示它是否开启以及你有哪些买家；`{{exe}} pair private off` 恢复为向所有人出售。见[和买家配对](/docs/share#pairing)。

## 需要帮助 {#feedback}

在门户的[反馈页面](/app/#/feedback)告诉我们，附上下面的信息。不要发送 `config.json` 或 `device.key`。

- `{{exe}} version` 的输出
- `{{exe}} status` 的输出
- 报错的原文，以及作业 ID（响应头 `X-Sharetoken-Job`）
