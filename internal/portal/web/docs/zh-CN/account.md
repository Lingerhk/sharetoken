# 账户和设备

账户记录你的 credit，设备是花钱和赚钱的那台机器。一个账户可以绑定多台设备。

## 登录 {#login}

没有密码。用发到邮箱的验证码登录；一个邮箱第一次登录时，同一步就会创建账户。

```bash
{{exe}} login -email you@example.com
```

1. 邮箱会收到 6 位验证码，10 分钟内有效。
2. 把它粘贴到提示处。
3. 看到 `logged in as you@example.com` 就完成了，这台机器已绑定到账户。

新账户会获赠一笔 credit，数额见[首页](/)。如果门户要求邀请码，加上 `-invite <邀请码>`。也可以先在[网页](/app/)上登录，之后在每台机器上运行 `login`。

收不到验证码时先看垃圾邮件文件夹，详见[故障排查](/docs/troubleshooting#no-code)。

登录会把这台机器绑定到账户。登录状态保存 60 天，到期后再运行一次 `login`。网页门户的登录保持 15 天。

可以用 `-device` 给设备起个容易认的名字，它显示在门户的设备列表里：

```bash
{{exe}} login -email you@example.com -device office-mac
```

### 在脚本里登录 {#login-script}

发送验证码和使用验证码可以分成两条命令，脚本不会停在提示处：

```bash
{{exe}} login -email you@example.com -send
{{exe}} login -email you@example.com -code 123456
```

第一条只发送验证码，第二条用它登录。两条都要在要绑定的那台机器上运行。

### 两步验证 {#mfa}

可以给每次登录再加一道身份验证器 App（Google Authenticator、Microsoft Authenticator、1Password 等）里的动态码。在门户[账户页面](/app/#/account)的**两步验证**里开启：扫描二维码，输入 App 里的第一个动态码。关闭时也需要输入当前的动态码。

开启后，`login` 在邮箱验证码之后还会询问动态码；脚本里用 `-totp 123456` 给出。管理员必须开启。

手机或 App 丢了就无法登录，别人也替你看不到动态码。请用账户的邮箱写信给本门户的客服（{{support}}）：核实是你本人之后，管理员会关闭你的两步验证，换了新手机后可以重新开启。

> [!WARNING]
> 能读你邮箱的人就能登录这个账户。请保管好邮箱账户，或者开启两步验证。

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
- **换账户：** 一把设备密钥只属于一个账户。先在旧账户里解绑，再用新邮箱 `login`；或者用单独的数据目录，见[数据目录](/docs/config#home)。

## API token {#tokens}

客户端登录时自动获得 token，一般不需要手动处理。门户的[API Token 页面](/app/#/tokens)可以查看和吊销所有 token，包括浏览器会话。

本地 OpenAI 端点**不使用** token，它只监听本机。详见[调用模型](/docs/use-models#endpoint)。
