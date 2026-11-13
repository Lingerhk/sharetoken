# 命令参考

所有命令的格式：

```bash
{{exe}} [-home DIR] <command> [flags]
```

`-home` 要写在命令**前面**，用来指定另一个数据目录。不带参数运行可以看到简短的帮助。

## 账户 {#account}

### login {#login}

用发到邮箱的验证码登录，并绑定这台设备。没有账户的邮箱会自动创建账户。

```bash
{{exe}} login -email you@example.com
```

| 参数 | 含义 |
|---|---|
| `-email ADDR` | 你的邮箱，不给时询问 |
| `-send` | 只发送验证码就退出，之后用 `-code` 登录 |
| `-code CODE` | 已经收到的验证码。不给时会发送一封并询问 |
| `-invite CODE` | 门户要求邀请码时，新账户的邀请码 |
| `-totp CODE` | 开启了两步验证时，身份验证器里的动态码。不给时询问 |
| `-device NAME` | 设备名称，默认是主机名 |

### logout {#logout}

吊销这台机器的 token，并从配置文件删除。

### balance {#balance}

显示余额、冻结额、买入和卖出的统计，以及设备数量。`account` 是它的别名。

### jobs {#jobs}

```bash
{{exe}} jobs -role sell -n 50
```

| 参数 | 含义 |
|---|---|
| `-role buy\|sell` | 只看买入或卖出，默认都看 |
| `-n N` | 显示多少条，默认 20 |

## 运行 {#run}

### up {#up}

运行客户端。`run` 是它的别名。

| 参数 | 含义 |
|---|---|
| `-http ADDR` | 本地 API 地址，默认 `127.0.0.1:{{port}}` |
| `-listen MADDRS` | libp2p 监听地址，逗号分隔 |
| `-nat=false` | 关闭 UPnP、NAT-PMP 和打洞 |

这些参数都只对这次运行有效，不写入配置文件。

### daemon {#daemon}

运行桌面应用背后的服务，提供[网页界面](/docs/desktop#browser-ui)。参数和 `up` 相同。和 `up` 不同的是，它未登录时也能运行，注册和登录通过它的页面完成。

### status {#status}

显示连接、中继和共享的状态。需要客户端正在运行。

### models {#models}

列出网络上的模型、节点数、TEE 节点数和最低价。需要客户端正在运行。

### relays {#relays}

显示各中继的延迟。`-measure` 立即重新测速。需要客户端正在运行。

## 共享 {#share}

| 命令 | 作用 |
|---|---|
| `share` | 显示共享设置 |
| `share detect [-upstream URL]` | 列出本机模型服务的模型 |
| `share add <model> [-id ID] [-in P] [-out P]` | 共享一个模型，同时打开共享 |
| `share rm <id>` | 移除一个模型 |
| `share on`、`share off` | 打开或暂停共享 |
| `share upstream <url>` | 设置本机模型服务，默认 `http://127.0.0.1:11434/v1` |

`<model>` 必须紧跟在 `add` 后面，参数写在它之后。详见[共享模型](/docs/share)。

## 配对 {#pairing}

卖家生成配对码并分发出去。买家用它配对之后，只向已配对的卖家购买。一个配对码可以让多个买家配对。

| 命令 | 作用 |
|---|---|
| `pair` | 显示你和谁配对，以及你的配对码 |
| `pair code [-label L] [-ttl 24h] [-uses N]` | 卖家：生成一个给买家用的配对码。默认 24 小时有效（最长 30 天），不限买家数量 |
| `pair join <code>` | 买家：和生成这个配对码的卖家配对 |
| `pair leave <seller id>` | 买家：解除和某个卖家的配对 |
| `pair rm <buyer id>` | 卖家：移除一个买家 |
| `pair revoke <code id>` | 卖家：让配对码不再接纳新买家，已配对的买家不受影响 |
| `pair private on\|off` | 卖家：只为和你配对的买家服务，或者恢复为向所有人出售 |

```bash
{{exe}} pair code -label team -ttl 72h
{{exe}} pair join ABCD-EFGH-IJKL-MNOP
```

命令里的 id 就是 `pair` 列出的编号。详见[和买家配对](/docs/share#pairing)。

## 编程助手 {#agents}

`skill install` 让本机的 Claude Code 和 Codex 学会使用客户端：查看状态和余额、通过本地接口调用模型、共享模型。它在 `~/.claude` 和 `~/.codex` 下写入 `skills/sharetoken/SKILL.md`（Windows 上在 `%USERPROFILE%` 下；设置了 `CLAUDE_CONFIG_DIR`、`CODEX_HOME` 时以它们为准）。这个 skill 要求助手在修改设置或大量花费 credit 之前先征得你的同意。

| 命令 | 作用 |
|---|---|
| `skill` | 找到了哪些助手，各自是否已装好 skill |
| `skill install [-agent claude\|codex\|all]` | 安装，默认装给找到的每个助手。客户端更新后再运行一次 |
| `skill uninstall [-agent …]` | 卸载 |
| `skill show` | 打印 skill 内容 |

```bash
{{exe}} skill install
{{exe}} skill install -agent codex
```

同名但不是客户端写的文件不会被覆盖或删除，除非加 `-force`。已打开的助手会话重启后才会加载。详见[在编程助手中使用](/docs/agents)。

## 其他 {#other}

| 命令 | 作用 |
|---|---|
| `config` | 显示配置文件路径和内容，token 只显示开头 |
| `version` | 显示版本、网络、门户、数据目录和本地 API 地址。不写任何文件 |

## 环境变量 {#env}

| 变量 | 作用 |
|---|---|
| `SHARETOKEN_HOME` | 数据目录，和 `-home` 相同 |
