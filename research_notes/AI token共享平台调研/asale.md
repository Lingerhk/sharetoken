# Asale (asale.ai)：闲置 AI 订阅额度的 Token 共享市场

> 调研日期：2026-11-06。除特别标注外，所有官网数据和文档内容均于 2026-11-06 抓取。市场数据为当日实时快照，会随时间变化。
> 标注说明：**[事实]** 有 URL 支撑；**[推断]** 是我基于证据的判断；**[自查]** 是我自己做的 DNS/WHOIS/GitHub API 查询（命令见各处说明），不是第三方来源。

---

## 1. Asale 是什么？如何消歧？团队、上线时间、地点、融资、域名、社交账号

### Takeaway
Asale（asale.ai，GitHub 组织 `asale-ai`）是一个双边 P2P“token 共享网络”。卖家在自己电脑上运行开源桌面客户端，把 Claude/ChatGPT-Codex/Gemini/Kimi/Grok 订阅的闲置额度挂到市场上；买家按 token 付费，结算币种为 USDT。运营主体自称“懒人书科技（Lazy Book Technology）”，2026 年 7 月开始做这个项目。种种线索都指向中国团队（域名注册地北京、提交记录里有 QQ 邮箱、24 小时消费量过半来自中国）。没有找到创始人姓名、融资信息，也没有 X/Discord/Telegram/微信群等社交账号。

### Cited Findings
- **定位**：官网标题 “Asale — a market for real users' idle subscription quota”，中文站标题 “Asale — 真人闲置订阅额度的 Token 市场”。没有单独的中文品牌名，中文站也直接用 “Asale”。— [asale.ai/en](https://asale.ai/en)；[asale.ai/zh](https://asale.ai/zh)
- 官网原话：“Asale is a network that moves unused AI subscription capacity to the people who need it… Asale runs no models: every request is served by a real user's upstream account.” — [asale.ai/en](https://asale.ai/en)
- README 原话：“Asale is a token sharing network… The platform handles matching, forwarding and billing.” 仓库描述：“Desktop client for the Asale token-sharing network: sell idle Claude, ChatGPT/Codex, Gemini, Kimi and Grok subscription quota for USDT, or buy capacity the moment you hit a rate limit.” — [GitHub asale-ai/asale](https://github.com/asale-ai/asale)
- **运营主体**：“Asale 由懒人书科技（Lazy Book Technology）开发并运营”，D-U-N-S 编号 699380571，“从什么时候开始：2026 年 7 月”，联系邮箱 support@asale.ai。— [关于 Asale（中文）](https://asale.ai/zh/docs/about)；[About Asale (EN)](https://asale.ai/en/docs/about)
- **服务条款**没有写法律实体全称、注册地、适用法律和争议解决方式，只写了 “Asale” 和 support@asale.ai。要求用户年满 18 岁，“One account per person”，只需验证邮箱（没有 KYC）。最后更新于 2026-08-21。— [Terms of service](https://asale.ai/en/docs/terms)
- **GitHub 时间线**：组织 `asale-ai` 创建于 2026-07-24，仓库 `asale-ai/asale` 创建于 2026-07-26。最早的 release 是 v0.1.3（2026-07-28），最新是 v1.2.4（2026-10-30），三个多月发了约 100 个 release。截至 2026-11-06 有 21 star、4 fork、Apache-2.0 许可，主要语言 Rust。— [GitHub API: repos/asale-ai/asale](https://api.github.com/repos/asale-ai/asale)；[Releases](https://github.com/asale-ai/asale/releases)；[v0.1.3](https://github.com/asale-ai/asale/releases/tag/v0.1.3)；[v1.2.4](https://github.com/asale-ai/asale/releases/tag/v1.2.4)
- **贡献者**：只有两个，`ghx13153395484`（54 次提交，账号建于 2025-06-27，资料为空）和 `asale-user`（13 次提交，账号建于 2026-07-24，name 填的是 “asale”）。提交作者邮箱包括 `3437900233@qq.com` 和 `lz@email.com`。— [GitHub API: contributors](https://api.github.com/repos/asale-ai/asale/contributors)；[GitHub API: commits](https://api.github.com/repos/asale-ai/asale/commits)
- **安全联系**：security@asale.ai，另有 GitHub 私有漏洞报告渠道和备用邮箱 asale.user@gmail.com。security.txt 写的是 “Preferred-Languages: en, zh, ja”。— [SECURITY.md](https://github.com/asale-ai/asale/blob/main/SECURITY.md)；[security.txt](https://asale.ai/.well-known/security.txt)
- **社区渠道**：About 页只列了 GitHub Discussions 和邮箱。GitHub Discussions 一共 1 帖（官方欢迎帖，0 回复）。官网、README 和 About 页都没有 X/Discord/Telegram/微信群链接。— [About](https://asale.ai/en/docs/about)；[Discussions](https://github.com/asale-ai/asale/discussions)
- **同组织的其他开源项目**：`llm-verify`（检测 LLM 端点的模型真伪、计费膨胀、中转来源）、`token-meter`（Rust 跨厂商 token 计量）、`agent-firewall`、`repolish`、`seo-geo-skill`、`anything-to-skill`。组织简介：“Open-source AI agent tooling — skills, SEO/GEO audits, and infra for Claude, ChatGPT, Gemini and more.” — [github.com/asale-ai](https://github.com/asale-ai)
- **周边产品**：官网还链接了 Studio（studio.asale.ai，用余额按次调用模型做聊天/翻译/画图）、AI Visibility（aeo.asale.ai，GEO 品牌监测）和 Swarm（swarm.asale.ai，把方案交给一群 AI agent 评审）。— [asale.ai/en](https://asale.ai/en)
- **[自查] 域名与托管**：WHOIS 显示 asale.ai 由 GoDaddy 注册，创建于 2025-01-16，Registrant State/Province 为 Beijing，Country 为 CN。asale.ai、gw.asale.ai、api.asale.ai 都解析到 8.216.38.157。网站由 nginx/1.30.0 + Next.js 提供。（查询方式：`whois asale.ai`、`dig`、`curl -I`，2026-11-06。）
- **可能的关联方 dLazy**：GitHub 组织 `dlazy-ai`（创建于 2026-08-30）fork 了 asale-ai/asale，fork 描述为 “Share the tokens you don't use Turn idle quota into income”。该组织的其他仓库都是 dLazy AI 的图像、视频、音频生成 CLI 和 skill（如 `@dlazy/cli`，“69 image, video, audio … models”）。dlazy.com 的标题是 “dLazy AI — All-in-One AI Creative Workspace for Video & Image”。— [github.com/dlazy-ai](https://github.com/dlazy-ai)；[dlazy-ai/asale (fork)](https://github.com/dlazy-ai/asale)；[dlazy.com](https://dlazy.com)

### Inferences
- **[推断] 团队在中国（大概率北京）**：证据有几条。域名注册地是北京/CN；提交邮箱是 QQ 邮箱；贡献者账号名像中国 11 位手机号（`13153395484`）；前端以中文为首要语言（zh / zh-TW / en / ja）；中国用户占 24 小时消费额的 50.8%。8.216.x.x 段看起来属于阿里云国际节点，不过我没有核实 IP 归属。
- **[推断] 懒人书科技可能和 dLazy（“懒人”）是同一家或关联公司**：名字相同，`dlazy-ai` fork 了 Asale，并用和官方相近的文案推广。市场里还有图像（59）、视频（31）、语音（22）等非文本模型，这和 dLazy 的多模态聚合产品形态吻合。目前只是间接证据，没有官方说明两者关系。
- **[推断] 团队规模很小**：主仓库只有 2 名人类贡献者，0 个外部 PR，社区几乎为零，看起来是 1–3 人的早期项目。迭代速度很快（约 100 个版本 / 3 个月），文档写得细致、措辞坦率。
- **消歧**：用 “Asale AI token / asale.ai / 闲置 token” 等多种写法搜索，结果都指向 asale.ai 和 asale-ai GitHub 组织，没有发现另一个同名、同样做 AI token 交易的实体。要注意别和名字相近的 **Aile（aile.sh，“Rent premium AI models. Lend idle API keys.”）** 混淆，它是另一家同类平台。没查到 asale.io / asale.xyz 与该产品有关。

### Gaps
- 没有找到创始人或团队成员的真实姓名、融资情况、媒体报道或创始人访谈。
- “懒人书科技”的工商全称和注册地址查不到。D-U-N-S 699380571 需要在 D&B 目录手动核实，本次没有核实。
- 没有找到 X/Twitter、Discord、Telegram、微信群、即刻、小红书等官方账号。
- 没有在 Product Hunt、Hacker News、Reddit、linux.do、知乎、掘金上找到关于 Asale 的讨论（搜索结果为零，但不能排除索引覆盖不全）。

---

## 2. 卖什么？支持哪些厂商、订阅和模型？

### Takeaway
卖的主要是 **OAuth 订阅额度**（Claude Pro/Max/Team/Enterprise、ChatGPT/Codex Plus/Pro、Gemini/Antigravity、Kimi Code、Grok CLI），也接受 Moonshot/xAI 平台 API key 和 “custom endpoint”（按余额计费的 key）。买方侧有一键改配置的工具是 Claude Code、Codex、Gemini CLI（以及 Antigravity CLI），文档还列了 OpenClaw、Hermes、opencode。市场上一共 449 个模型，其中 384 个有卖家在线，覆盖文本、图像、视频、语音、转写和嵌入。

### Cited Findings
- **卖方接入方式**（原表）：浏览器 OAuth 登录（Claude、ChatGPT/Codex、Gemini）；设备码授权（Kimi Code、Grok CLI，“you confirm a short code in a browser, which need not be on this machine”）；API key（“Moonshot and xAI platform keys, billed against a balance rather than a plan”）；从本机已登录的 CLI 导入（例如 `.codex/auth.json`）。— [Selling tokens](https://asale.ai/en/docs/sell-tokens)；[README](https://github.com/asale-ai/asale/blob/main/README.md)
- v0.1.3 release notes（2026-07-28）：“All five subscriptions can now be sold… Each vendor's metered platform API key can be connected separately.” — [v0.1.3](https://github.com/asale-ai/asale/releases/tag/v0.1.3)
- **Claude 卖方要求**：付费 Claude 订阅（Pro、Max 5×、Max 20×、Team/Enterprise 席位），免费账号 “has nothing to sell”。Team/Enterprise 席位作为单独的 vendor `claude_work` 接入。— [Selling a Claude subscription](https://asale.ai/en/docs/providers/claude)
- **Antigravity**（Google）是独立的 OAuth 凭据族 `antigravity`。文档写着 “The public client ID defaults to the one used by CLIProxyAPI”，协议实现参考了 `../CLIProxyAPI/internal/auth/antigravity/` 等路径。上架范围限于 “discovered, priced text models from Google's and Anthropic's catalog entries”。文档也承认 “No paid-account or live CLI test has been completed.” — [docs/ANTIGRAVITY.md](https://github.com/asale-ai/asale/blob/main/docs/ANTIGRAVITY.md)
- **买方工具**：FAQ 写 “Claude Code, Codex / ChatGPT, and Gemini CLI on the buy side. On the sell side, those plus Kimi and Grok via API key, and any custom endpoint.” 文档导航里另有 “Buying with OpenClaw / Hermes / opencode” 三页。— [FAQ](https://asale.ai/en/docs/faq)；[About（文档导航）](https://asale.ai/en/docs/about)
- **市场规模**（2026-11-06 快照）：“Shared models 449, 384 with a seller online”；按输出类型分为 Text 282、Image 59、Video 31、Speech 22、Transcription 20、Embeddings 35。挂牌示例：claude-haiku-4-5、claude-opus-5、claude-opus-5-5、claude-sonnet-5、claude-sonnet-5-5、claude-fable-5 / 5-1、gpt-6-astra、gpt-6.1-sol、gpt-5.6-luna / terra、gemini-4-argon、gemini-3.8-flash、grok-build-0.1、kimi-k3、kimi-k3.1-pro 等。— [Model Market](https://asale.ai/en/market)
- 网关只认平台自己的模型名（“Model names are the platform's, not a vendor's SKU list”），可通过 `GET https://gw.asale.ai/v1/models` 查询。没有任何人定过价的模型会被网关直接拒绝。— [API overview](https://asale.ai/en/docs/api/overview)；[How models are priced](https://asale.ai/en/docs/pricing)
- **套餐容量估算**（Asale 申报给市场的 5 小时窗口 token 数）：Claude Max 20× 为 4.4M，Max 5× 为 2.2M，Team/Enterprise 为 1.5M，Pro 为 1.1M；ChatGPT/Codex Pro 为 2.0M，Plus 为 1.0M；Gemini Ultra 为 3.0M，Pro/Advanced 为 1.5M；Kimi、Grok 任意套餐均为 0.5M。全天折算按 ×4.8。原话：“Asale never asks a provider ‘how much quota is left’ — none of them will say. It estimates instead.” — [Plans & capacity](https://asale.ai/en/docs/plans)

### Inferences
- **[推断]** 交易的核心标的是**订阅 OAuth 会话的配额**，不是 API credits。API key 只是补充，主要给 Moonshot/xAI 和 custom endpoint 用。Cursor 不在支持列表里，没有找到任何提及。
- **[推断]** 市场上的图像、视频、语音等非文本模型，不可能来自 Claude/Codex 这类 CLI 订阅，更可能来自 “custom endpoint”/API key 类供给或平台自营渠道。这一点和 “Asale runs no models” 的说法存在需要澄清的张力，文档没有解释这些模型的来源。

### Gaps
- 文档没有说明图像、视频、嵌入类模型的供给来源和验证方式。
- Cursor、Copilot、Perplexity 等订阅不在支持列表中，也没有路线图。

---

## 3. 商业模式：市场、资金池还是中转？定价、抽成、结算、出入金规则

### Takeaway
Asale 是**带自动撮合的双边市场**，属于 P2P relay。它不是资金池：平台不持有额度，买家也不能挑卖家，撮合器按价格、信誉、延迟、成功率打分派单。价格由算法按供需每分钟调整，取值为官方标价的 10%–100%；卖家只能设置“价格地板”。平台对每笔成交请求**抽成 8%**，没有其他费用。全部用 **USDT**（Solana SPL / TRON TRC20）结算，另有一条信用卡充值通道。资金是**平台托管、不隔离、没有储备证明**。

### Cited Findings
- **抽成**：“The platform keeps 8% of each settled request. Nothing else: no monthly fee, no minimum, no listing fee, no withdrawal subscription.” — [How models are priced](https://asale.ai/en/docs/pricing)；条款原文 “The platform takes 8% of each settled request.” — [Terms](https://asale.ai/en/docs/terms)
- **定价公式**：`market price = list price × ratio, ratio ∈ [0.10, 1.00]`，每个模型一个 ratio，同时作用于 input/output/cache_read/cache_write 四类 token；`u = calls in the last minute ÷ (online lanes × 10)`，`target = 0.10 + 0.90 × u`；ratio 每分钟向 target 移动 30%；从未成交的模型起始价为标价的 80%；某个价格档满了（u ≥ 1）就直接跳到下一个卖家的保留价。— [How models are priced](https://asale.ai/en/docs/pricing)
- **算例**（官方）：claude-sonnet-5 标价为输入 $2/M、输出 $10/M，假设 ratio 为 35%。一次请求 12,000 输入 + 900 输出，计 11,550 µUSDT（约 $0.012，按标价是 $0.033）。平台费 924，卖家得 10,626。预冻结按 max_tokens 计算，为 28,672 µUSDT，多冻结的部分在结算时退还。— [How models are priced](https://asale.ai/en/docs/pricing)
- **卖家控制项**：每个账号单独设置开关、每日上限（按 token 或按套餐日容量的百分比，按 UTC 日重置）、价格地板（官方标价的百分比，默认不设）、并发数（默认 10）。— [Selling tokens](https://asale.ai/en/docs/sell-tokens)；[Claude provider guide](https://asale.ai/en/docs/providers/claude)
- **撮合权重**：价格 0.45、信誉 0.25、往返延迟 0.15、成功率 0.15。卖家要被考虑，信誉至少 600，并且要有空闲并发槽。“Buyers do not choose you; the matcher does.” — [How it works](https://asale.ai/en/docs/how-it-works)；[Selling tokens](https://asale.ai/en/docs/sell-tokens)
- **钱包三栏**：Available、Frozen（在途请求的预冻结，被放弃的冻结 30 分钟后自动释放）、Payable（卖出收入）。卖出收入记入 Payable 后，**24 小时后**才转为可花余额，这段时间留作争议窗口。买家可在任务结束后 72 小时内对已结算任务发起争议，争议成立则用反向分录退款。— [Wallet & settlement](https://asale.ai/en/docs/wallet)；[Funds & safeguards](https://asale.ai/en/docs/funds)
- **充值**：USDT (TRON) 最低 10 USDT、手续费 3 USDT（归集 gas 约 2.24 USDT）；USDT (Solana) 最低 2 USDT、免费、32 确认后到账；信用卡最低 5 USDT、手续费 5% + 0.4 USDT，通过 **Dodo Payments** 作为 merchant of record，只能充值不能提现。— [Funds & safeguards](https://asale.ai/en/docs/funds)
  - 冲突：Wallet 页说 “A suggested minimum is shown, but it is not enforced: smaller deposits are credited too”，和 Funds 页列出的最低充值额不一致。— [Wallet](https://asale.ai/en/docs/wallet) vs [Funds](https://asale.ai/en/docs/funds)
- **提现**：TRON 最低 20 USDT，手续费按链上实时报价 5–10 USDT；Solana 最低 10 USDT，手续费 1 USDT；单笔上限 500 USDT；每小时最多 4 笔；滚动 24 小时最多 20,000 USDT。部分账号只能提到白名单地址。— [Funds & safeguards](https://asale.ai/en/docs/funds)
  - 冲突：Funds 页称 “There is no manual approval queue… A normal withdrawal never touches a human”，条款则写 “A risk review runs before payout… usually within one business day”。— [Funds](https://asale.ai/en/docs/funds) vs [Terms](https://asale.ai/en/docs/terms)
  - 冲突：README 写 “Settlement runs once a minute, in USDT (TRC20)… Deposits and withdrawals run over TRON”（只提 TRON），网站文档则写 Solana 和 TRON 都支持，并且 “Solana is offered first”。— [README](https://github.com/asale-ai/asale/blob/main/README.md) vs [Wallet](https://asale.ai/en/docs/wallet)
- **托管性质**：“This is not segregated custody. Your balance is a row in the platform's ledger… on-chain USDT is consolidated into platform wallets… There is no third-party custodian and no proof of reserves.” 每小时做一次复式记账全量对账，对不上就冻结该钱包。“There are no published payout-time statistics yet.” — [Funds & safeguards](https://asale.ai/en/docs/funds)
- **不计费情形**：请求报错、usage 为 0、买家中途断开、卖家在发出首字节前掉线，这几种情况都不计费。条款写的是 “Interrupted requests are billed for tokens actually produced”。— [Buying tokens](https://asale.ai/en/docs/buy-tokens)；[Terms](https://asale.ai/en/docs/terms)
  - 冲突：Buy/FAQ 页说买家中途断开 “not billed”，条款说中断请求按实际产出计费。
- **账户终止时余额的处理**：已确认的可提现余额仍然可以提走，除非属于违约所得；平台要拿出具体 task ID 才能扣，只扣对应部分，用户有 30 天申诉期。— [Funds & safeguards](https://asale.ai/en/docs/funds)

### Inferences
- **[推断] 收入模型**是纯交易抽成（8% take rate），没有订阅费、上架费或提现利差。TRON 充值的 3 USDT 和提现手续费号称按成本收取。另一个潜在收入是沉淀资金（余额不计息、资金不隔离）。
- **[推断] 定价机制更像“算法做市 + 卖家保留价”**，不是挂单簿：卖家没法主动报价，只能设地板，买家没法选卖家。这种设计让市场在供给过剩时自动压到地板价。从市场快照看，大量模型的价格只有标价的 5%（见下文“冲突”）。

### Gaps
- 官方算例和条款都没有说明卖家实际到手的有效单价，例如按 Claude Max 20× 满额卖出一个月能赚多少，也没有给出任何收益案例。
- 没有提现到账时间的统计（官方承认尚未发布）。

---

## 4. 技术架构：卖家如何贡献额度、买家如何消费、计量、路由、安全

### Takeaway
架构分三段：**买家本机代理 → 中心网关（gw.asale.ai）→ 卖家本机客户端（通过 WebSocket 长连接接单）→ 上游厂商**。卖家的 OAuth 凭据**只存在卖家本机**（系统钥匙串 + `~/.asale/auths`），由卖家客户端在调用时注入，再从卖家自己的 IP 发往上游。网关对每次派单做 Ed25519 签名（包含 task_id、模型、预算、过期时间），卖家本地验签并按预算截断。买家可以通过本地代理（:9787）间接接入，也可以直接调用 OpenAI、Anthropic、Gemini 三种兼容协议的网关。代价是**卖家机器能看到买家的明文 prompt**，没有端到端加密。客户端开源（Rust + Tauri 2，Apache-2.0），服务端闭源。

### Cited Findings
- **请求路径四跳**：
  1. CLI 调用本机代理（127.0.0.1），工具配置已被改写为指向代理；
  2. 代理转发到网关，“Only the auth header is swapped, for your Asale API key. The body is untouched”；
  3. 网关给请求定价，从买家余额冻结预估费用，并挑选最佳 lane；
  4. 卖家客户端调用上游，“The seller's own subscription token is injected locally and never leaves their machine.”

  — [How it works](https://asale.ai/en/docs/how-it-works)
- **代码结构**：
  - `protocol/`：“the wsrelay wire protocol, the single definition shared by server and client”；
  - `core/ws.rs`：signed handshake、supply.declare、heartbeat、job dispatch；
  - `core/executor.rs`：注入本地订阅凭据、回传流、解析 usage、执行预算；
  - `core/security.rs`：设备 Ed25519 身份；
  - `store.rs`：SQLite，只存钥匙串引用；
  - `daemon/`（`asaled`）：全部业务逻辑，本地 HTTP/JSON-RPC 监听 :9700；
  - `daemon/proxy.rs`：本地消费代理 :9787；
  - `publisher.rs`：卖方会话、限额、自动停止；
  - `tool_config.rs`：改写和恢复 CLI 配置；
  - `src-tauri/`：Tauri 2 外壳；
  - `src/`：Vite + React 18 + i18next（zh / zh-TW / en / ja）。

  — [docs/DEVELOPMENT.md](https://github.com/asale-ai/asale/blob/main/docs/DEVELOPMENT.md)
- 编译时注入网关公钥 `ASALE_QUOTA_PUBKEY`，没有它 “the client cannot verify the gateway's authorization, and selling stays stuck at ‘coming online’ forever.” 服务端规格文档（`asale-client-spec.md`、`token-trading.md`、`deploy/README.md`）放在不公开的“main repository”里。— [docs/DEVELOPMENT.md](https://github.com/asale-ai/asale/blob/main/docs/DEVELOPMENT.md)
- **安装**：macOS/Linux 用 `curl -fsSL https://asale.ai/dl/install.sh | sh`，Windows 用 `irm https://asale.ai/dl/install.ps1 | iex`。另有 .dmg（Developer ID 签名 + Apple 公证）、.exe/.msi（Azure Artifact Signing 做 Authenticode 签名）、.AppImage/.deb、无桌面服务器用的 `asale-cli-*.tar.gz`。无头服务器模式依次运行 `asale start`、`asale expose on`、`asale url`（URL 里带的 token “is the entire authorization”）。自动更新每 10 分钟轮询一次 `https://asale.ai/dl/manifest.json`，然后重新执行安装脚本，没有签名增量更新器。— [README](https://github.com/asale-ai/asale/blob/main/README.md)；[docs/DEVELOPMENT.md](https://github.com/asale-ai/asale/blob/main/docs/DEVELOPMENT.md)；[manifest.json](https://asale.ai/dl/manifest.json)（2026-11-06 显示 version 1.2.4）
- **卖家凭据**：“Credentials live in an encrypted store on your machine, under ~/.asale/auths/… injected into an upstream request at the moment of the call and never leave the device — not to the buyer, not to Asale.” 每个账号每分钟重新申报一次可用量（剩余窗口、每日上限、并发）。— [Selling tokens](https://asale.ai/en/docs/sell-tokens)
- **出口网络**：卖方请求从卖家自己的机器发往上游。部分地区直连会返回 403，客户端支持上游代理（auto / direct / manual，http、https、socks5）。— [Claude provider guide](https://asale.ai/en/docs/providers/claude)
- **买家侧，一键改配置**：Buy 开关会把工具的 `base_url` 指向 127.0.0.1 代理，key 换成 Asale key，原文件先逐字节备份，关闭开关后恢复。Claude Code 只改 `~/.claude/settings.json` 里的 `env.ANTHROPIC_BASE_URL` 和 `env.ANTHROPIC_AUTH_TOKEN`。Codex 会把模型写入 `~/.codex/config.toml`。未勾选的模型在本地直接拒绝（返回 403，不产生费用）。开关打开期间，该工具的全部流量都走 Asale，“It does not fall back to a local subscription.” — [Buying tokens](https://asale.ai/en/docs/buy-tokens)；[Buying with Claude Code](https://asale.ai/en/docs/tools/claude-code)
- **买家侧，直连 API**：
  - OpenAI 兼容：base URL `https://gw.asale.ai/v1`（`/v1/chat/completions`，以及 responses）；
  - Anthropic 兼容：base URL `https://gw.asale.ai`（`/v1/messages`、`/v1/messages/count_tokens`，后者只是启发式估算）；
  - Gemini 兼容：`/v1beta/models/{model}:generateContent`；
  - key 格式为 `sk-asale-...`，可以放在 Bearer、`x-api-key` 或 `x-goog-api-key` 里；每个 key 有独立的 RPM/TPM 上限和可选预算；请求体上限 16 MiB；SSE 流式输出“no re-framing”；
  - 钱包余额 API：`GET https://api.asale.ai/api/v1/me/wallet/balance`（单位 micro_usdt，返回字段包括 `max_ratio_pct` 等）。

  — [API overview](https://asale.ai/en/docs/api/overview)；[Anthropic-compatible API](https://asale.ai/en/docs/api/anthropic)
- **跨协议转换**：网关会在 Anthropic、OpenAI、Gemini 三种格式之间翻译。thinking 签名 “is only handed back when the request was served over the same wire format; a publisher serving it through another vendor produces no signature rather than a forged one”。— [Anthropic-compatible API](https://asale.ai/en/docs/api/anthropic)；[Security](https://asale.ai/en/docs/security)
- **路由与容错**：如果首字节前出错，静默换下一个候选，最多再重试 2 次；流已经开始后无法重放，错误透传给买家且不计费。会话会粘在同一卖家 10 分钟，以保住 prompt cache 命中。— [How it works](https://asale.ai/en/docs/how-it-works)；[Savings](https://asale.ai/en/docs/savings)
- **限流处理**：上游返回 429 时，整个账号冷却到上游给出的重置时间，不在同一账号上重试，失败的请求换到另一台设备；出错的设备冷却 60 秒，持续失败会被下架。早期曾有本地配额估算器在“撞墙前”停售，后来删掉了。— [Upstream account risk](https://asale.ai/en/docs/upstream-risk)；[Selling tokens](https://asale.ai/en/docs/sell-tokens)
- **计量**：以上游返回的 usage 为准。网关自己独立估算一份（按字符类加权，拉丁文约 4 字符/token，CJK 约 1.5），偏差超过 25% 记入卖家信誉。输出量按签名预算封顶；prompt 量封顶为网关估算的 3 倍（或估算 + 1000 token，取较大者），超过就四类 token 等比缩放。结算在单个数据库事务里完成，写三条复式分录，用唯一结算键防止重复扣费。— [Metering & settlement](https://asale.ai/en/docs/metering)
- **防作弊（买卖双方）**：派单签名覆盖 `task_id | model | budget | expiry`，1 小时过期；任务通道绑定设备；买家 key 在服务端只存 SHA-256；设备握手签名、限时、一次性使用。— [Security](https://asale.ai/en/docs/security)
- **模型真伪验证**：
  - 每条 lane 指一个（设备、厂商、模型）三元组；
  - 上架前平台从“前门”试买约 10 次（文档原话 “a dozen times”），双方都按 0 结算，但会消耗卖家的额度；
  - 评分 ≥ 75 且没有触发硬门槛就算通过；
  - 上架后抽检 1% 流量（处于 watch 状态则 10%），按市价付费，样本分散在约 1 小时内发出，最长 72 小时必须复检一次，通过结论有效期 7 天；
  - 题目由服务器种子按次生成，避免卖家背题；
  - 同一模型有 8 条以上 lane 时做同侪画像，偏离超过 3σ 就深度复查；
  - 只有“硬证据 + 置信度 ≥ 0.6”才下架；
  - 官方承认 “There is no proof of which binary runs on the other end.”

  — [Price and authenticity](https://asale.ai/en/docs/real-and-cheap)；[Verification](https://asale.ai/en/docs/verification)；[llm-verify](https://github.com/asale-ai/llm-verify)
- **隐私**：
  - 平台不存请求体（“No message column… relayed in memory… No request or response bodies in logs”）；
  - 客户端只上报随机 UUID、30 秒一次的心跳、挂出的额度和每单 token 数；
  - 网站加载 Google tag，桌面客户端不带任何分析；
  - 关键限制：“Your request is relayed by another user's client, which reconstructs and sends it upstream. At that moment the text is visible to that machine. This is not end-to-end encrypted.” SECURITY.md 称之为 “Known, and by design”。

  — [Security](https://asale.ai/en/docs/security)；[Privacy](https://asale.ai/en/docs/privacy)；[SECURITY.md](https://github.com/asale-ai/asale/blob/main/SECURITY.md)
- **[自查]** `gw.asale.ai` 返回的 CORS 暴露头包括 `x-asale-notice`、`x-asale-notice-min-version`、`x-asale-task`，网关本身是 nginx 1.30.0。（`curl -I https://gw.asale.ai/v1/models`，2026-11-06）

### Inferences
- **[推断] 这是 “key 不离机、会话 P2P 出口”的架构**，和“把 API key 上传给平台”的中转站（例如 new-api/one-api 类）有本质区别。上游看到的是卖家自己的 IP 和 OAuth 客户端，这降低了凭据泄露风险，但 prompt 会暴露给卖家。
- **[推断] 技术上可能借鉴或复用了 CLIProxyAPI**（一个把 Claude Code、Codex、Gemini CLI 的 OAuth 会话包装成 API 的开源项目）的协议实现：Antigravity 文档直接引用 `../CLIProxyAPI/...` 源码路径，并默认使用 CLIProxyAPI 的 OAuth client ID。
- **[推断] 对 ShareToken 类产品的设计启示**：
  - 签名预算的派单（卖家本地强制执行上限）、预冻结 + 多退、四类 token 分别计价、10 分钟会话粘滞保 cache、首字节前透明重试、平台“前门”抽检验真，这些机制都值得参考；
  - 它的空白在 prompt 隐私，没有 TEE 或端到端加密。

### Gaps
- 服务端（网关、撮合、结算）闭源，规格文档不公开，无法验证服务端实现与文档是否一致。
- 没有第三方安全审计报告，也没有公开披露的安全事件。
- 卖家客户端是否会对买家请求做内容过滤或日志记录，取决于卖家运行的是不是官方二进制；协议层面无法阻止恶意卖家修改开源客户端来记录 prompt。这是由设计推出的结论，文档承认可见性，但没有讨论恶意卖家留存数据的问题。

---

## 5. 目标用户与业务数据

### Takeaway
目标用户分三类：**卖家**是用不满额度的 Claude Max / ChatGPT Pro 等高价订阅持有者；**买家**是撞到限额的重度 CLI agent 用户，以及没有订阅、想按量便宜调用的开发者；双向用户是“夜里卖、白天买”。截至 2026-11-06，业务体量很小：24 小时成交额约 330 USDT、约 1.5 万次任务，**中国买家占 50.8%**。GitHub 只有 21 star，社区讨论基本为零。

### Cited Findings
- 官方用户画像：“Most users sell overnight and buy during the day.” 计划对照表写道：Max 20× 用户 “The best supply on the network. Cap at 50–70% and forget it”；Pro 用户 “Sell overnight, buy during the day”；没有订阅的用户 “Buy only”。— [Introduction](https://asale.ai/en/docs/introduction)；[Plans & capacity](https://asale.ai/en/docs/plans)
- **市场快照（2026-11-06，过去 24 小时）**：Volume **329.760051 USDT**；Tasks **15,247**；Regions **14**；“Avg paid, share of list (24h) **7%** — Weighted by **11,240** settled turns”；“Available capacity 56100.0M”。— [Model Market](https://asale.ai/en/market)
- **按地区（24 小时消费额）**：中国 50.8%（167.65 USDT / 6,728 tasks，热门模型 gpt-5.6-sol）；日本 9.9%（1,547 tasks）；美国 9.9%（2,250 tasks，热门模型 claude-sonnet-5）；德国 7.6%；韩国 5.8%；英国 4.2%；加拿大 3.9%；新加坡 3.1%；香港 2.8%；荷兰 1.0%；澳大利亚 0.9%；尼日利亚、马来西亚、Unknown 各约 0%。— [Global Distribution](https://asale.ai/en/distribution)
- **价格示例（2026-11-06）**：
  - claude-opus-5-5：输出 1.00 / 输入 0.20 USDT 每百万 token（标价的 5%）；
  - claude-sonnet-5：0.50 / 0.10（5%）；
  - claude-haiku-4-5：0.25 / 0.05（5%）；
  - gpt-6-astra：2.50 / 0.50（5%）；
  - claude-fable-5-1：5.00 / 1.00（10%）；
  - kimi-k3：1.60 / 0.019（10%）。

  — [Model Market](https://asale.ai/en/market)；首页 “In the last 24 hours Buyers paid 7% of list price” — [asale.ai/en](https://asale.ai/en)
- **更早的快照**：2026-11 之前搜索引擎索引的首页摘要显示 “claude-opus-5 at 60% of list price and kimi-k3 at 100%”，客户端版本 v0.2.5。说明早期价格高得多。这一条来自搜索摘要，日期不确定，我没有直接核实。— [asale.ai/en（搜索引擎摘要）](https://asale.ai/en)
- **冲突**：定价文档写 `ratio ∈ [0.10, 1.00]`、“as low as 10% of vendor list”，但 2026-11-06 的市场页和首页大量模型显示 “5% of list”（“Cheapest on sale right now: 5% of list”），低于文档写明的下限。— [How models are priced](https://asale.ai/en/docs/pricing) vs [Model Market](https://asale.ai/en/market)
- **GitHub 社区**：21 star、4 fork、2 个 issue（都已关闭）、0 个 PR、1 个 Discussion（官方欢迎帖，0 回复）。— [GitHub asale-ai/asale](https://github.com/asale-ai/asale)；[Discussions](https://github.com/asale-ai/asale/discussions)
- V2EX 上有一个相关话题讨论 “token 订阅制能不能像 p2p 共享带宽一样流动起来”，但我没有核实该帖是否提到 Asale。— [V2EX t/1221783](https://www.v2ex.com/t/1221783)

### Inferences
- **[推断] 平台收入估算**：按快照的 24 小时成交额 329.76 USDT × 8%，平台日抽成约 26 USDT，折合月 GMV 约 1 万 USDT、月平台收入约 800 USDT。这是单日快照外推，误差很大。
- **[推断] 单价被压到地板**：均价 7%，大量 5%，再加上 “Available capacity 56,100M” 这类整齐的大数，说明**供给远大于需求**。卖家实际到手很低，例如以 5% 卖 Opus 5.5 输出，扣 8% 后约 $0.92/M。要么市场靠价格吸引需求，要么供给数据只是申报值、不是真实可用量。整齐的 “1300.0M / 1000.0M / 700.0M” 也像是估算或申报，不像实测。
- **[推断] 中国买家占一半**，符合“国内用户难以直接订阅或访问海外模型”的需求结构。gpt-5.6-sol 是中国、日本、德国、韩国、加拿大的头号消费模型。
- **[推断]** 平均每任务约 0.02 USDT（329.76 / 15,247），说明以小额 agent 调用为主。

### Gaps
- 没有注册用户数、卖家数、在线设备数的公开数据，也没有历史 GMV 曲线。
- 没有找到 X、Discord、Telegram、微信群、小红书、知乎、linux.do 等平台上的社区规模和用户口碑。
- “Available capacity” 和每模型 supply 数字的口径没有说明（是申报值还是可用量）。

---

## 6. 风险：上游 ToS 合规、封号、安全事件、用户投诉、诈骗

### Takeaway
官方**明确承认**出售订阅额度“是对上游条款的明确违反”，不是灰色地带。官方无法保证卖家不被封号，不赔偿被封的订阅，也不公布封号率。官方还透露 Anthropic 已经封堵过“订阅额度服务第三方应用”的路径。其他风险包括：买家 prompt 对卖家可见；资金不隔离、无储备证明、全部用 USDT；无 KYC；运营主体信息模糊。公开投诉目前只有 1 起充值延迟到账（已解决），没有发现公开的诈骗或安全事件报道。

### Cited Findings
- **ToS 明确违反**：“Nearly every model vendor's terms carry the same clause: the subscription is issued to you, and you may not transfer, rent or otherwise make account access available to others. Sharing unused capacity, however cleanly engineered, lands on that clause. This is not ‘grey, but nobody enforces it’. It is a clear breach of terms whose enforcement varies by vendor — throttling, suspended API access, or a disabled account. We are not going to describe this as compliant.” — [Upstream account risk](https://asale.ai/en/docs/upstream-risk)
- **Anthropic 已收紧**：“Some vendors have already tightened. Anthropic has closed the path that let subscription capacity serve third-party applications; hitting that wall shows up as the whole account going dark across every model for a while.” — [Upstream account risk](https://asale.ai/en/docs/upstream-risk)
- **跨地域风险信号**：“Cross-region matching is allowed by default… one account appearing in several regions in a short window, which is itself a risk signal.” 支持按模型、按地区配置路由策略，但默认不限制。— [Upstream account risk](https://asale.ai/en/docs/upstream-risk)
- **不保证、不赔付**：“We cannot guarantee you will not be banned… We do not publish a ban rate… What we cannot do: negotiate with the vendor on your behalf, or reimburse a suspended subscription. This is not an insurance product.” 官方建议：“If this account is load-bearing for your work… do not sell on it.” — [Upstream account risk](https://asale.ai/en/docs/upstream-risk)
- **条款层面**：卖家只能卖自己合法持有的额度，“Capacity from shared or someone else's account is excluded”；违规来源的额度 “whatever follows with that vendor is yours to carry”；禁止刷单（wash trading）、伪造计量、使用别处获得的凭据；以次充好会被下架并冻结收入。— [Terms](https://asale.ai/en/docs/terms)
- **隐私风险**：“Do not send confidential material.” 条款要求买家不要发送源码里的密钥、敏感个人数据、商业秘密或受监管数据。— [Terms](https://asale.ai/en/docs/terms)；[Security](https://asale.ai/en/docs/security)
- **资金风险**：没有隔离托管，没有第三方托管人，没有储备证明；大额提现可能抽干热钱包，后续提现要排队几十分钟；链上转错地址无法追回。— [Funds & safeguards](https://asale.ai/en/docs/funds)；[Terms](https://asale.ai/en/docs/terms)
- **用户投诉（GitHub）**：
  - Issue #2（2026-10-16）：Solana USDT 充值在最终确认 90 分钟后仍未到账，联系表单也不给工单号。官方于 2026-10-22 回复称是 “a delay in our deposit detection process”，并已优化，随后关闭。— [Issue #2](https://github.com/asale-ai/asale/issues/2)
  - Issue #3（2026-10-29）：Antigravity 账号 20 个可服务模型中有 18 个因为 ID 后缀不匹配而“silently unsellable”，2026-10-30 在 v1.2.4 修复。— [Issue #3](https://github.com/asale-ai/asale/issues/3)；[v1.2.4](https://github.com/asale-ai/asale/releases/tag/v1.2.4)
- **安全披露**：没有赏金计划（“There is no paid bounty programme”），承诺 2 个工作日内确认、7 天内评估。SECURITY.md 写的是 3 个工作日确认、30 天内修复。两处承诺不一致。— [Security](https://asale.ai/en/docs/security)；[SECURITY.md](https://github.com/asale-ai/asale/blob/main/SECURITY.md)
- **安装方式风险**：一键安装是 `curl | sh`，自动更新是“重新执行网站下发的安装脚本”，没有签名增量更新，需要提权安装到 root 拥有的目录。— [docs/DEVELOPMENT.md](https://github.com/asale-ai/asale/blob/main/docs/DEVELOPMENT.md)

### Inferences
- **[推断] 卖家面临的最大风险是被上游封号**（Anthropic 尤其严格）。买家面临的最大风险有两个：prompt 泄露给陌生卖家，以及资金托管在一个主体信息不透明、只收 USDT 的平台。
- **[推断] 合规与支付**：只用 USDT（TRON/Solana）、只需邮箱注册，这降低了中国用户的使用门槛，但也带来反洗钱和监管风险。在中国大陆运营加密货币结算业务本身有法律风险。条款没有写适用法律。
- **[推断] 运营主体透明度**：只给了 D-U-N-S 编号和中英文商号，没有工商全称和地址，用户维权路径不清楚。

### Gaps
- 没有找到卖家因使用 Asale 被 OpenAI 或 Anthropic 封号的公开案例，也没有官方封号率（官方明确拒绝公布）。
- 没有找到媒体报道、诈骗指控或安全事件。
- 没有找到 OpenAI、Anthropic、Google 针对 Asale 的公开回应。

---

## 7. 与 ClawPool / LetsSplit 及同类平台的关系

### Takeaway
没有发现 Asale 与 ClawPool 或 LetsSplit 之间的任何直接关系、合作或公开对比。搜索中出现了若干同类“闲置 token 交易”平台，可作为竞品参考（本次未深入研究）。

### Cited Findings
- 用 “Asale ClawPool LetsSplit idle tokens” 组合搜索，没有返回相关结果（返回的都是无关的 DeFi “Idle Finance”）。— 搜索于 2026-11-06，无可引用 URL
- 搜索中出现的同类平台（未核实，仅供参考）：
  - **Aile**（aile.sh），“Rent premium AI models. Lend idle API keys.” — [aile.sh](https://aile.sh/)
  - **QUOTA**（quotai.io），“The idle inference network.” — [quotai.io](https://quotai.io/)
  - **闲猫·闲置 Token 交易市场**（idlicat.com），自称有平台担保和资金托管。 — [idlicat.com](https://idlicat.com/)
  - **TokenlyPro**，“Sell Unused AI Tokens.” — [tokenlypro.com/sell](https://www.tokenlypro.com/sell)
  - **AI Token Exchange**。 — [aitokenexchange.net](https://aitokenexchange.net/index_en)
  - GitHub 开源项目 **newtokenclub/new-api**，搜索摘要称它用浏览器插件作为节点做 P2P（未核实）。 — [GitHub newtokenclub/new-api](https://github.com/newtokenclub/new-api)

### Inferences
- **[推断] Asale 的差异化**有四点：
  - 开源客户端，凭据不离机；
  - 撮合器自动派单 + 算法定价，不是挂单；
  - 平台主动“前门”抽检验真（llm-verify）；
  - 文档对 ToS 违约、prompt 可见、资金不隔离等风险披露得非常坦率。

  如果 ClawPool / LetsSplit 是“拼车/合租”或“API key 托管中转”模式，Asale 和它们的主要区别会在凭据位置（本机 vs 平台）和隐私边界上。这一点需要由负责那两家的研究者核对。

### Gaps
- 没有找到任何第三方横向评测或对比文章。
