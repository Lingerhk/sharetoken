# 共享模型

把本机的模型共享到网络，按实际 token 赚 credit。不需要公网 IP，也不需要开端口。

> [!WARNING]
> **运行模型必须解密，你的机器能看到别人发来的 prompt。** 请不要记录或保存这些内容。只共享你本机运行、并且有权共享的模型。

## 第 1 步：准备模型服务 {#server}

默认对接本机的 Ollama。其他 OpenAI 兼容的模型服务见[使用其他模型服务](#other-server)。

::: macos
```bash
brew install ollama
brew services start ollama
ollama pull gemma4:e4b
```

也可以从 [ollama.com](https://ollama.com/download) 下载应用，打开后它会一直在后台运行。
:::

::: linux
```bash
curl -fsSL https://ollama.com/install.sh | sh
ollama pull gemma4:e4b
```

安装脚本会把 Ollama 注册成 systemd 服务，开机自动运行。
:::

::: windows
```powershell
winget install Ollama.Ollama
ollama pull gemma4:e4b
```

也可以从 [ollama.com](https://ollama.com/download) 下载安装程序。它在通知区运行，登录 Windows 时自动启动。
:::

确认客户端能看到模型：

```bash
{{exe}} share detect
```

## 第 2 步：添加模型 {#add}

```bash
{{exe}} share add gemma4:e4b -id gemma-4-e4b
```

| 参数 | 含义 | 默认值 |
|---|---|---|
| `-id` | 模型在网络上的 ID，买家用它调用 | 和本机模型同名 |
| `-in` | 每百万输入 token 的价格，单位 micro-credit | `100000`（0.1 credit） |
| `-out` | 每百万输出 token 的价格，单位 micro-credit | `200000`（0.2 credit） |

> [!TIP]
> 其他节点已经在共享同一个模型时，请使用相同的 ID，买家才能一起选到。先用 `{{exe}} models` 看看现有的 ID 和价格。协调器综合价格、信誉、延迟和成功率选择节点，价格的权重最高。

添加模型会自动打开共享。

## 第 3 步：上线接单 {#up}

```bash
{{exe}} up
```

`up` 已经在运行时，`share` 命令立即生效，不用重启。长期接单请[在后台运行](/docs/running#background)。

确认状态：

```bash
{{exe}} status
```

看到 `sharing 1 model(s): ...` 就说明已经上线。如果有 `offer not accepted: ...`，后面是被拒绝的原因。

## 查看收益 {#earnings}

```bash
{{exe}} balance
{{exe}} jobs -role sell -n 20
```

- **结算：** 买家确认（Ack）后结算；买家没有确认的单子，过了 24 小时争议窗口自动成交。所以收益可能先计入 `pending`。
- **手续费：** 平台从每单收取 5%。
- 门户的[作业](/app/#/jobs)和[账单](/app/#/ledger)页面有明细。

## 管理共享 {#manage}

| 操作 | 命令 |
|---|---|
| 查看设置 | `{{exe}} share` |
| 列出本机模型 | `{{exe}} share detect` |
| 改价格（重新添加同一个 ID） | `{{exe}} share add gemma4:e4b -id gemma-4-e4b -in 80000 -out 150000` |
| 移除模型 | `{{exe}} share rm gemma-4-e4b` |
| 暂停接单 | `{{exe}} share off` |
| 恢复接单 | `{{exe}} share on` |

## 使用其他模型服务 {#other-server}

任何提供 OpenAI 兼容接口的本机服务都可以，比如 LM Studio、vLLM 或 llama.cpp：

```bash
{{exe}} share upstream http://127.0.0.1:1234/v1
{{exe}} share detect
{{exe}} share add my-local-model -id my-model
```

模型服务需要 API key 时，把 key 放在一个环境变量里，再在[配置文件](/docs/config#share)的 `share.upstream_key_env` 里写上变量名。

## 并发和性能 {#tuning}

同时处理的请求数默认是 2，由配置文件里的 `share.concurrency` 控制。按你的机器能力调整，修改后运行下面的命令让它生效：

```bash
{{exe}} share on
```

## 防止作弊的机制 {#audit}

协调器会以新身份随机下单，在参考模型上重算比对。连续两次不符，节点暂停接单 24 小时。请确保你共享的模型和它的 ID 相符，也不要虚报用量：买家会自己重算哈希，并能凭证据退款。
