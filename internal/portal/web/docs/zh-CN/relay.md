# 自建中继

中继负责把加密连接转发给无法被直接连到的设备。网络为所有人运行着一批中继。你也可以自己运行一个，叫**私有中继**：这样别人访问你共享的设备时，走的是你的服务器，不再经过网络的中继。

适合这些情况：买家离你的服务器近、离网络的中继远；你希望共享的流量留在自己的机器上；团队内部共享，本来就有服务器。

## 工作方式 {#how}

- 私有中继只为你列出的账号服务，其他账号的设备无法在上面预约。
- 只有共享会用到它。共享的设备在你的中继上占一个位置，买家从那里连进来（能直连时直连）。你的中继正常时，设备不再占用网络的中继；你的中继停了，大约半分钟后设备改用网络的中继，恢复后再切回来。
- 其他一切照旧。登录、账户、撮合和计费从不经过私有中继，也不需要在任何地方登记它：它只存在于你自己的配置里。
- 它看不到 prompt 和回答。经中继的流量在买卖双方之间加密。和任何中继一样，它能看到谁连了谁：也就是连到你设备的买家的地址。
- 平台自己发起的抽检不经过它。私有中继后面的卖家照样被抽检，抽检请求由普通买家的客户端转交。

> [!NOTE]
> 买模型不需要私有中继，也没有任何设置。买家按卖家给出的地址连接。

## 需要准备什么 {#requirements}

一台有公网地址的 Linux 服务器（x86-64 或 ARM64），放行 4001 端口的 TCP 和 UDP（UDP 用于 QUIC）。服务器有 IPv6 时中继也在 IPv6 上监听；没有 IPv6，只有 IPv6 的用户就连不上它。小配置就够：中继只是转发加密数据。

还需要你的账号 ID，在已登录客户端的机器上查看：

```bash
{{exe}} account
```

第一行形如 `user <name> (user:3)`，括号里的就是账号 ID。

## 第 1 步：下载 {#download}

在服务器上操作。`uname -m` 输出 `x86_64` 对应 `amd64`，`aarch64` 对应 `arm64`：

```bash
ARCH=amd64
curl -fLO {{origin}}/downloads/sharetoken-relay-{{version}}-linux-$ARCH.tar.gz
curl -fLO {{origin}}/downloads/SHA256SUMS-{{version}}
sha256sum -c --ignore-missing SHA256SUMS-{{version}}
tar -xzf sharetoken-relay-{{version}}-linux-$ARCH.tar.gz
cd sharetoken-relay-{{version}}-linux-$ARCH
```

包里有程序 `sharetoken-relay`、一个 systemd 单元和一份 README。

## 第 2 步：生成配置 {#init}

列出所有可以使用这个中继的账号。只给自己用：

```bash
./sharetoken-relay init -account user:3 -out relay.yaml
```

团队使用时，重复 `-account`，或用逗号分隔：

```bash
./sharetoken-relay init -account user:3,user:7,user:12 -out relay.yaml
```

云服务器通常在地址转换后面，不知道自己的公网地址。用域名或 IP 告诉中继：

```bash
./sharetoken-relay init -account user:3 -out relay.yaml \
  -announce /dns4/relay.example.com/tcp/4001,/dns4/relay.example.com/udp/4001/quic-v1
```

`init` 会向门户取用来证明账号身份的公钥，和你的账号列表一起写进 `relay.yaml`。文件很短，带注释。运行时中继每 6 小时向门户重新读一次这个公钥，门户换了公钥也能继续工作。以后要加账号，在 `allow_accounts` 下加一行，重启中继。

- **其他门户：** `init` 默认使用 `https://sharetoken.app`。你的客户端登录的是别的门户时，加 `-portal {{origin}}`。
- **检查配置：** 改过 `relay.yaml` 后，可以先用 `./sharetoken-relay -config relay.yaml -check-config` 检查，它会打印生效的设置。
- `init` 不会覆盖已有的文件。

## 第 3 步：启动 {#start}

```bash
./sharetoken-relay -config relay.yaml
```

它会打印自己的地址，一行，以中继的 ID 结尾：

```text
add it to the devices that share through it:
   sharetoken relays add '/dns4/relay.example.com/tcp/4001/p2p/12D3KooW...'
```

配置旁边的 `relay.key` 是中继的身份，请保留：换了 key，地址就变了。

要让它长期运行，安装包里的 systemd 单元：

```bash
sudo mkdir -p /opt/sharetoken-relay
sudo cp sharetoken-relay relay.yaml relay.key /opt/sharetoken-relay/
sudo useradd -r -s /usr/sbin/nologin sharetoken-relay
sudo chown -R sharetoken-relay /opt/sharetoken-relay
sudo cp sharetoken-relay.service /etc/systemd/system/
sudo systemctl enable --now sharetoken-relay
```

用 `journalctl -u sharetoken-relay -f` 看日志，中继每 5 分钟记一行统计。升级时下载新版本的包，用其中的 `sharetoken-relay` 替换 `/opt/sharetoken-relay/sharetoken-relay`，再运行 `sudo systemctl restart sharetoken-relay`；`relay.key` 不变，地址就不变。

## 第 4 步：添加到设备 {#add}

在每台共享的设备上，客户端运行时执行：

```bash
{{exe}} relays add '/dns4/relay.example.com/tcp/4001/p2p/12D3KooW...'
```

然后检查：

```bash
{{exe}} relays
```

这个中继显示为 `private`。设备在上面占到位置后，最后一列是 `sharing`，网络的中继不再显示 `sharing`。

不再使用时：

```bash
{{exe}} relays remove 12D3KooW...
```

## 设置 {#settings}

列表保存在客户端的 [`config.json`](/docs/config#network) 里：

```json
{
  "private_relays": ["/dns4/relay.example.com/tcp/4001/p2p/12D3KooW..."],
  "share": { "keep_official": false }
}
```

| 字段 | 含义 | 默认值 |
|---|---|---|
| `private_relays` | 你自己的中继，最多 8 个 | 空 |
| `share.keep_official` | 私有中继正常时，也在网络的中继上占位置。买家会走先连通的那条路 | `false` |

## 中继的限制和监控 {#limits}

`init` 写出的 `relay.yaml` 按小规模使用设定，可以按需要修改，改完重启中继：

| 字段 | 含义 | `init` 写入的值 |
|---|---|---|
| `access.networks[].max_devices_per_user` | 每个账号同时在中继上占位置的设备数 | `8` |
| `limits.max_reservations` | 中继上同时占位置的设备总数 | `64` |
| `limits.max_reservations_per_ip` | 同一个 IP 地址最多占的位置 | `16` |
| `limits.max_circuits` | 每台设备同时经中继的连接数 | `256` |
| `network.max_conns` | 中继的连接总数 | `1024` |
| `limits.memory` | 所有连接一共可以占用的内存 | 不写：机器内存的四分之三 |

- **每个地址的连接数：** 每个 IPv4 地址（IPv6 按 /56）最多 32 个连接，不能修改。同一个 NAT 后面的设备共用这个数。团队的设备都在同一个出口地址后面时，也要相应调大 `max_reservations_per_ip`。
- **内存：** 机器内存按容器或 cgroup 的上限算（更小时）。没有设置环境变量 `GOMEMLIMIT` 时，中继把自己的内存上限设为机器内存的 85%。
- **统计和指标：** 在 `status.listen` 填一个本机地址，例如 `127.0.0.1:8093`：`/` 返回 JSON 统计，`/metrics` 是 Prometheus 指标（以 `sharetoken_relay_` 开头）。这两个地址没有认证，所以只接受本机回环地址。`status.stats_interval`（默认 `5m`）是日志里记统计的间隔，`0` 表示不记。

## 不工作时 {#trouble}

| 现象 | 原因和处理 |
|---|---|
| `{{exe}} status` 警告私有中继没有列出你的账号 | 这台设备的账号不在 `allow_accounts` 里。把 `{{exe}} account` 显示的 ID 加进去，重启中继 |
| 中继一栏显示错误而不是延迟 | 设备连不上它。检查防火墙（TCP 和 UDP 4001）和公布的地址 |
| 中继能连上，但一直不显示 `sharing` | 这台设备没有打开共享（`{{exe}} share on`），或者中继拒绝了账号（见第一行） |
| 团队里有的设备一直不显示 `sharing`，其他设备正常 | 超过了每个账号的设备数（`max_devices_per_user`）或每个 IP 的位置数（`max_reservations_per_ip`），见[中继的限制和监控](#limits)。调大后重启中继 |
| 买家仍然经网络的中继连进来 | 私有中继中断超过了半分钟，或者打开了 `share.keep_official` |

## 私有中继能看到什么 {#privacy}

运行中继的人能看到哪些设备在上面占了位置、它们的账号 ID 和客户端版本，以及连到这些设备的买家的网络地址、时间和流量。它读不到也改不了请求和回答，也冒充不了卖家：连接两端互相验证身份。

只添加你信得过的中继。添加别人的私有中继时，对方会知道你的账号 ID 和你的买家的地址。
