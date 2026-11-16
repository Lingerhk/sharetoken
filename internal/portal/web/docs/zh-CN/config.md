# 配置和文件

大多数人不需要改配置文件。这一页说明数据放在哪里，以及有哪些可调的设置。

## 数据目录 {#home}

::: macos linux
默认是 `{{home}}`。
:::

::: windows
默认是 `{{homewin}}`，通常就是 `C:\Users\<你的用户名>\.sharetoken`。
:::

想用另一个目录，比如同一台机器上跑两个账户：

::: macos linux
```bash
{{exe}} -home ~/sharetoken-second login -email other@example.com
{{exe}} -home ~/sharetoken-second up -http 127.0.0.1:9800
{{exe}} -home ~/sharetoken-second status
```
:::

::: windows
```powershell
{{exe}} -home "$HOME\sharetoken-second" login -email other@example.com
{{exe}} -home "$HOME\sharetoken-second" up -http 127.0.0.1:9800
{{exe}} -home "$HOME\sharetoken-second" status
```
:::

也可以设置环境变量 `SHARETOKEN_HOME`。

## 目录里的文件 {#files}

| 文件 | 内容 |
|---|---|
| `config.json` | 设置和登录 token。只有你自己能读 |
| `device.key` | 设备私钥。只有你自己能读，换一台机器就是一台新设备 |
| `network.json` | 中继列表的缓存，删除后会自动重新获取 |
| `run.json` | 运行中客户端的 API 地址和管理接口的 key |
| `apikey.json` | OpenAI 端点的 API key 以及是否需要它（`{{exe}} apikey`） |
| `daemon.log` | 桌面应用启动的服务的日志 |

> [!WARNING]
> `config.json` 和 `device.key` 相当于这台设备的登录凭据。不要分享、提交到代码仓库，也不要放进别人能读的备份。丢失了也不会损失 credit，重新登录即可。设备密钥变了之后，它就是一台新设备，旧设备可以在门户解绑。

## 查看和修改配置 {#edit}

```bash
{{exe}} config
```

这个命令显示文件路径和内容。修改时用文本编辑器打开 `config.json`，然后**重启客户端**才会生效。共享设置例外，见[共享设置](#share)。

文件是严格的 JSON，不能有注释。下面的示例只列出要改的字段。

## 调用模型的设置 {#buy}

```json
{
  "buy": {
    "budget_micros": 2000000,
    "max_price_in": 0,
    "max_price_out": 0,
    "evidence": true,
    "require_tee": false,
    "require_native": false,
    "spot_checks": true,
    "queue_seconds": 30,
    "first_byte_seconds": 30,
    "idle_seconds": 30
  }
}
```

| 字段 | 含义 | 默认值 |
|---|---|---|
| `budget_micros` | 每个请求最多冻结多少，单位 micro-credit，用剩的退回 | `2000000`（2 credit） |
| `max_price_in` | 每百万输入 token 最多愿意付多少，`0` 表示不限 | `0` |
| `max_price_out` | 每百万输出 token 最多愿意付多少，`0` 表示不限 | `0` |
| `evidence` | 节点虚报用量时，交出这一单的密钥以自动退款 | `true` |
| `require_tee` | 只把请求发给 TEE 节点 | `false` |
| `require_native` | 只把请求发给模型服务器原生支持请求所用接口的节点，不做转换（见[原生和转换](/docs/use-models#native)）。单个请求也可以用请求头 `X-Sharetoken-Require-Native: true` 要求 | `false` |
| `spot_checks` | 协助平台抽检卖家。平台会不定期抽查卖家是否真的在用它标称的模型；开启后，客户端偶尔替平台把一个很短的抽检请求转交给卖家，就像发你自己的请求一样，卖家因此分不清哪次是抽检。请求和回答都是加密的（本机看不到内容），也不花 credit。卖家会像对待你发的任何请求一样看到本设备的地址。默认开启，只能在这个文件里关闭，桌面端没有这个开关 | `true` |
| `queue_seconds` | 节点全部满载时最多等多久，负数表示立即失败 | `30` |
| `first_byte_seconds` | 等节点第一帧的时限，超时换节点。模型思考期间节点每 10 秒发一个进度帧，所以慢模型也不需要调大 | `30` |
| `idle_seconds` | 两帧之间的时限，超时判定为卡死。只收到进度帧、连续 5 分钟没有输出，也判定为卡死 | `30` |

## 共享设置 {#share}

```json
{
  "share": {
    "enabled": true,
    "upstream": "http://127.0.0.1:11434/v1",
    "upstream_key_env": "",
    "concurrency": 2,
    "relays": 2,
    "models": [
      { "id": "gemma-4-e4b", "upstream_model": "gemma4:e4b", "price_in": 100000, "price_out": 200000 }
    ],
    "tee": "off"
  }
}
```

| 字段 | 含义 | 默认值 |
|---|---|---|
| `enabled` | 是否接单 | `false` |
| `upstream` | 本机模型服务的 OpenAI 兼容地址 | Ollama：`http://127.0.0.1:11434/v1` |
| `upstream_key_env` | 保存本机模型服务 API key 的环境变量名 | 空 |
| `concurrency` | 同时处理的请求数 | `2` |
| `relays` | 在网络的几个中继上预约 | `2` |
| `keep_official` | 使用[私有中继](/docs/relay)时：它正常时也保留网络中继上的预约 | `false` |
| `models` | 共享的模型。一般用 `share add` 管理。每个模型还可以设置 `price_cache_read` 和 `price_cache_write`，即从 prompt 缓存读出和写入缓存的输入 token 的价格，见[缓存价格](/docs/share#cache-prices) | 空 |
| `tee` | 机密虚拟机类型：`off`、`auto`、`sev-snp`、`tdx` | `off` |

共享的托管 API（provider）不在这个文件里：它们连同 API key 和登录凭据保存在同一目录的 `providers.json`，用 [`provider` 命令](/docs/share#providers)管理，不建议手动编辑。

用 `share` 子命令修改时，运行中的客户端立即生效。手动编辑文件后，再运行一次 `{{exe}} share on` 让它重新加载。

## 网络设置 {#network}

| 字段 | 含义 | 默认值 |
|---|---|---|
| `http` | 本地 API 和 OpenAI 端点的地址。请保持在本机地址上 | `127.0.0.1:{{port}}` |
| `listen` | libp2p 监听地址 | 所有网卡，IPv4 和 IPv6，TCP 和 QUIC |
| `nat` | UPnP、NAT-PMP 和打洞 | `true` |
| `relays` | 非空时覆盖门户提供的中继列表 | 空 |
| `private_relays` | 你自己的中继，共享经由它们，见[自建中继](/docs/relay)。用 `relays add` 管理 | 空 |
