# 账户和设备

账户记录你的 credit，设备是花钱和赚钱的那台机器。一个账户可以绑定多台设备。

## 注册 {#register}

```bash
{{exe}} register -email you@example.com
```

1. 输入密码，长度 8 到 256 个字符，需要输入两次。
2. 邮箱会收到验证码，粘贴到提示处。验证码也可以直接用 `-code 123456` 给出。
3. 看到 `logged in as you@example.com` 就完成了，这台机器已绑定到账户。

注册会赠送一笔 credit，数额见[首页](/)。也可以在[网页](/app/#register)上注册，之后在每台机器上运行 `login`。

收不到验证码时先看垃圾邮件文件夹，详见[故障排查](/docs/troubleshooting#no-code)。

## 登录 {#login}

```bash
{{exe}} login -user you@example.com
```

登录会把这台机器绑定到账户。登录状态保存 30 天，到期后再运行一次 `login`。

可以用 `-device` 给设备起个容易认的名字，它显示在门户的设备列表里：

```bash
{{exe}} login -user you@example.com -device office-mac
```

### 在脚本里登录 {#login-script}

密码可以从环境变量读取，这样不会出现提示：

::: macos linux
```bash
read -rs SHARETOKEN_PASSWORD && export SHARETOKEN_PASSWORD
{{exe}} login -user you@example.com
unset SHARETOKEN_PASSWORD
```
:::

::: windows
```powershell
$env:SHARETOKEN_PASSWORD = Read-Host 'Password' -MaskInput
{{exe}} login -user you@example.com
Remove-Item Env:SHARETOKEN_PASSWORD
```

`-MaskInput` 需要 PowerShell 7。Windows PowerShell 5.1 请去掉它，输入时会显示明文。
:::

> [!WARNING]
> 不要把密码直接写进命令行或脚本文件，它会留在 shell 历史和磁盘上。

## 退出登录 {#logout}

```bash
{{exe}} logout
```

这会吊销这台机器的 token，并从配置文件里删除它。设备密钥和其他设置保留，再次登录后还是同一台设备。

退出登录时请一并停掉正在运行的客户端（`Ctrl+C` 或停止后台服务）。

## 查看余额和账单 {#balance}

```bash
{{exe}} balance
```

```text
user you@example.com (user:...)
  available 4.999900 credits, on hold 0.000000
  bought  3 jobs, 60 prompt + 420 completion tokens, spent 0.000100
  sold    0 jobs, 0 prompt + 0 completion tokens, earned 0.000000 (pending 0)
  devices 1
```

- **available：** 可用余额。
- **on hold：** 正在进行的请求冻结的金额，用剩的部分自动退回。
- **pending：** 卖出的作业里还没结算的单数。

最近的作业：

```bash
{{exe}} jobs -n 20
{{exe}} jobs -role buy
{{exe}} jobs -role sell
```

更详细的账单在门户的[账单页面](/app/#/ledger)。

## 管理多台设备 {#devices}

- **加一台设备：** 在那台机器上安装客户端，运行 `login`。绑定需要设备私钥签名，所以必须在那台机器上操作。
- **查看和解绑：** 打开门户的[设备页面](/app/#/devices)。解绑后协调器会立即拒绝这台设备。
- **换账户：** 一把设备密钥只属于一个账户。先在旧账户里解绑，再用新账户 `login`；或者用单独的数据目录，见[数据目录](/docs/config#home)。

## API token {#tokens}

客户端登录时自动获得 token，一般不需要手动处理。门户的[API Token 页面](/app/#/tokens)可以查看和吊销所有 token，包括浏览器会话。

本地 OpenAI 端点**不使用** token，它只监听本机。详见[调用模型](/docs/use-models#endpoint)。

## 修改密码 {#password}

在门户的[账户页面](/app/#/account)修改，可以同时吊销其他会话。
