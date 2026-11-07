# sharetoken

> 当前版本：**v1**（2026-11-07）

一个用 Go 写的**开源模型 token 共享网络**：把自己机器上开源权重模型的闲置算力共享出去赚 credit，需要时用 credit 调用别人的节点。同一个账户既能卖也能买。

- 只共享自托管的开源模型（Ollama、vLLM、llama.cpp、SGLang 等），不转发 OpenAI 或 Anthropic，不设模型白名单。
- 节点跑的是不是它声称的模型，由抽检和 TEE 证明来检验，而不是靠名单。

## 买家能得到的保证

| 保证 | 机制 |
|---|---|
| prompt 不经过协调器，中继和网络读不到 | 直连 libp2p 流 + 封装加密（box） |
| 响应没被篡改、截断、重排 | 节点签收据，买家逐块核对哈希 |
| 虚报用量会被退款 | 买家拒签后提交**这一单**的密钥作为证据，协调器用参考模型精确数 token 后自动裁决 |
| 节点没有偷换成小模型 | 协调器随机抽检，在参考模型上重算比对，不符的节点被暂停接单 |
| 节点运营者也看不到 prompt（可选） | AMD SEV-SNP / Intel TDX 机密虚拟机，quote 绑定节点的两把公钥 |
| 账不会丢 | SQLite 复式账本，启动时回放日志对账 |

## 文档

| 文档 | 内容 |
|---|---|
| [docs/design-v1.md](docs/design-v1.md) | 完整设计、威胁对照表、已知边界、下一步 |
| [docs/validation-v1.md](docs/validation-v1.md) | 跨公网三机实测、发现的问题、协调器压测数据 |
| [reports/](reports/) | 前期调研：AI token 共享平台 |

## 快速开始

```bash
go build -o bin/ ./cmd/...

# 1) 协调器（有公网 IP 的机器；-public 同时为 NAT 后的节点提供中继）
#    不要加 -nat=false：它会关掉 AutoNAT，NAT 后的节点就拿不到中继地址
#    -reference 为某个模型配置参考副本：开启对该模型的抽检，争议时精确数 token
bin/coordinator -public -db coordinator.db \
  -reference id=gemma-4-e4b,upstream=gemma4:e4b,url=http://127.0.0.1:11434/v1
#    记下输出里以 /p2p/<id> 结尾的地址，下面记作 $COORD

# 2) 卖家节点（本机跑模型；不需要开放端口）
ollama serve &
bin/node -coordinator "$COORD" \
  -model id=gemma-4-e4b,upstream=gemma4:e4b,in=100000,out=200000   # 价格：micro-credit / 百万 token

# 3) 买家（本地 OpenAI 兼容端点）
bin/buyer -coordinator "$COORD"            # http://127.0.0.1:9787/v1
curl http://127.0.0.1:9787/v1/chat/completions \
  -d '{"model":"gemma-4-e4b","messages":[{"role":"user","content":"hi"}]}'
bin/buyer -coordinator "$COORD" models     # 在线模型、节点数、TEE 节点数、最低价
bin/buyer -coordinator "$COORD" balance    # 余额
```

任何支持 OpenAI 协议的工具，把 base URL 改成 `http://127.0.0.1:9787/v1` 即可。非流式响应在收据验证通过后才返回（响应头 `X-Sharetoken-Verified: true`）；流式响应边到边转发，结尾附一行 `: sharetoken ... verified=true`。

### 买家选项

| 参数 | 作用 |
|---|---|
| `-evidence`（默认开） | 节点收据可证明有错时，向协调器披露**这一单**的密钥和密文以自动退款；关掉则只提交普通争议，等人工仲裁 |
| `-require-tee -tee-policy policy.json` | 只用机密虚拟机里、镜像在白名单内的节点；买家自己验证 quote |
| `-max-price-in` / `-max-price-out` | 价格上限 |

### TEE 节点

在 AMD SEV-SNP 或 Intel TDX 的 Linux 机密虚拟机中，模型服务也跑在同一台虚拟机里：

```bash
bin/node -tee auto -coordinator "$COORD" -upstream http://127.0.0.1:11434/v1 -model ...
# 输出 tee: sev-snp measurement <hex>：把它（来自可复现构建的镜像）写进买家和协调器的策略文件
```

策略文件格式：`{"measurements": {"<hex>": "sharetoken-node v1 / gemma4"}}`。默认离线验证签名链；加 `-tee-online` 时还会检查吊销和 TCB 状态。

### 协调器运维命令

直接读写数据库，可以和运行中的协调器同时执行：

```bash
bin/coordinator deposit <peer-id> <micros>    # 充值
bin/coordinator balance <peer-id>
bin/coordinator disputes                      # 待仲裁和已裁决的争议
bin/coordinator resolve <job-id> pay|refund   # 人工仲裁「无法验证」的争议
bin/coordinator nodes                         # 节点信誉、抽检记录、暂停状态
bin/coordinator audits
bin/coordinator check                         # 回放日志对账
```

## 本机一键演示

```bash
scripts/demo-local.sh    # 需要 Ollama 和本地模型（默认 gemma4:e4b，可用 MODEL_UPSTREAM 指定）
```

全部绑定 127.0.0.1，关闭 NAT 穿透，退出时关闭所有进程。演示内容：

1. 两个节点提供同一模型，其中更便宜的那个会把补全 token 数虚报 100 倍（`scripts/inflating_upstream.py`，仅用于演示）。
2. 买家拒签它的收据并提交证据，协调器用参考模型数清 token 后判节点败诉、全额退款；请求自动切换到诚实节点，返回已验证的回答。
3. 抽检器拿诚实节点和作弊节点分别与参考模型比对，作弊节点连续两次不符后被暂停接单。
4. 停掉协调器后，用运维命令读取 SQLite，账目回放一致。
5. 检查所有日志里都没有 prompt 原文。

## 测试

```bash
go test -race ./...
```

覆盖签名与防篡改、计费整数运算、SQLite 账本（重启、回滚、篡改检测）、结算与证据裁决的全部路径、抽检判定、SEV-SNP（测试用 AMD 证书链）和 TDX（Intel 真实生产 quote）验证，以及基于真实 libp2p 的端到端测试：故障切换、Ticket 重放与盗用、虚报用量按证据退款、抽检抓出换模型和虚报用量的节点、只用 TEE 节点。
