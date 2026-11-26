# 账户和设备

账户记录你的 credit，设备是花钱和赚钱的那台机器。一个账户可以绑定多台设备。

## 登录 {#login}

没有密码。用发到邮箱的验证码登录；一个邮箱第一次登录时，同一步就会创建账户。

```bash
{{exe}} login -email you@example.com
```

1. 邮箱会收到 6 位验证码，10 分钟内有效，而且只能在申请它的地方使用：这条命令，或发出申请的那个浏览器标签页、应用窗口。
2. 把它粘贴到提示处。
3. 看到 `signed in as you@example.com` 就完成了，这台机器已绑定到账户。

新账户会获赠一笔 credit，数额见[首页](/)。如果门户要求新账户提供邀请码，`login` 会提示你输入，也可以直接加上 `-invite <邀请码>`。也可以先在[网页](/app/)上登录，之后在每台机器上运行 `login`。[桌面应用](/docs/desktop)在窗口里走同样的步骤。

收不到验证码时先看垃圾邮件文件夹，详见[故障排查](/docs/troubleshooting#no-code)。

登录会把这台机器绑定到账户。登录状态默认保存 60 天，网页门户的登录默认保持 15 天。登录到期或被吊销后，正在运行的客户端会在一分钟内自动停止（桌面应用回到登录界面），再运行一次 `login` 即可。

可以用 `-device` 给设备起个容易认的名字（默认是主机名），它显示在门户的设备列表里：

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

手机或 App 丢了就无法登录，别人也替你看不到动态码。请用账户的邮箱写信给本门户的客服（{{support}}）：核实是你本人之后，管理员会关闭你的两步验证，同时吊销这个账户所有的 token 和网页会话，所以每台机器都要重新 `login`。换了新手机后可以重新开启。

> [!WARNING]
> 能读你邮箱的人就能登录这个账户。请保管好邮箱账户，或者开启两步验证。

## 退出登录 {#logout}

```bash
{{exe}} logout
```

这会吊销这台机器的 token，并从配置文件里删除它。设备密钥和其他设置保留，再次登录后还是同一台设备。

正在运行的客户端也会随之停止，不再买卖。用后台服务运行的客户端，请同时停掉服务，否则它会反复启动失败。

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
- **查看、重命名和解绑：** 打开门户的[设备页面](/app/#/devices)。解绑后协调器会立即拒绝这台设备。
- **换账户：** 一把设备密钥只属于一个账户。先在旧账户里解绑，再用新邮箱 `login`；或者用单独的数据目录，见[数据目录](/docs/config#home)。

## API token {#tokens}

客户端登录时自动获得 token，一般不需要手动处理。门户的[API Token 页面](/app/#/tokens)可以查看和吊销所有 token，包括浏览器会话；也可以给脚本新建一个，新 token 只显示一次。吊销某台机器的 token 后，那里正在运行的客户端会在一分钟内停止。

本地端点**不使用**这些 token。它只监听本机，用的是客户端自己的 API key（`sk-st-…`，`{{exe}} apikey` 查看）。详见[调用模型](/docs/use-models#endpoint)。
