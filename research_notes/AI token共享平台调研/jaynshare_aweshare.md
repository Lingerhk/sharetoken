# JaynShare 与 AweShare：「互助式 token 共享」项目调研笔记

> 调研时间：2026-11-06。所有 GitHub / npm / 网站数据均为当日抓取，除非另有标注。
> 方法：克隆两个 GitHub 仓库并阅读 README、文档、源码（JaynShare 为完整 Rust 源码；AweShare 公开仓库仅文档，另下载 npm 包 `aweshare@0.8.3` 的编译后 JS 阅读）；GitHub API / npm registry 元数据；官网与公共 hub 端点探测；通用网页搜索（Reddit / HN / V2EX / linux.do / 小红书等基本无结果）。
>
> **核心结论先行**：两者都是真实存在、**2026 年 8–9 月才出现**的小型 GitHub 项目，而不是成型的商业平台。两者都**没有实现**「贡献多少 → 可提取多少」的互助积分/信用账本：目前都是「受邀小圈子内免费共用」模式，积分/对等机制只停留在路线图或设想阶段。两者在架构上恰好代表两条对立路线：**JaynShare = 中心服务器托管凭证 + TLS 中间人（MITM）代理，支持 Claude Pro/Max 订阅**；**AweShare = 凭证留在贡献者本机 + 出站 WebSocket 反向隧道，不支持 Claude 订阅 OAuth（只支持 API key 和 ChatGPT/Codex 登录）**。

---

## 1. 它们是什么？实体确认与消歧（团队、上线时间、地域、域名、渠道、开源与否）

### Takeaway
JaynShare 指 GitHub 上的 `jaynlabs/jaynshare`：法国两人团队 Jayn Labs 做的 MIT 开源 Rust 自托管代理，官网是 jayn.app/jaynshare，2026-09 发布。AweShare 指 `Wehuman01/aweshare`：作者为澳门大学的 Peng（GitHub `mugpeng`），面向中文用户，以 npm 包和 Docker 镜像分发。它的许可证是专有的（"source-available"，公开仓库只放文档），2026-08 发布，作者还自己运营一个社区 hub `aweshare.wehuman.top`。

### Cited Findings

**JaynShare（实体确认）**
- GitHub 搜索 "jaynshare" 只有一个结果：`jaynlabs/jaynshare`，描述为 "Share your claude/codex subscriptions ! multi-account router"。它有 43 stars、4 forks，用 MIT 许可证，主语言为 Rust，homepage 填的是 `https://jayn.app/jaynshare` — [GitHub repo](https://github.com/jaynlabs/jaynshare)；[GitHub API](https://api.github.com/repos/jaynlabs/jaynshare)
- 仓库创建于 2026-09-14T21:30Z，最后一次 push 在 2026-10-20。Topics 包括 anthropic、claude-code、claude-code-router、multi-account、proxy、rate-limit、self-hosted — [GitHub API](https://api.github.com/repos/jaynlabs/jaynshare)
- 组织 `jaynlabs` 的 GitHub 资料：name "jayn"，location "France"，email `hugo@jayn.app`，blog `https://jayn.app`，创建于 2026-08-11 — [GitHub API users/jaynlabs](https://api.github.com/users/jaynlabs)
- 首个 commit 为 "Jaynshare 1.0.0"（2026-09-14，作者 Hugo Puybareau）。之后几乎所有 commit 和 PR 都出自 Maxence Rossignol（GitHub `maxmaxou2`，提交邮箱后缀为 `@student-cs.fr`，即 CentraleSupélec 学生邮箱）。共 181 个 commit — [commits](https://github.com/jaynlabs/jaynshare/commits/main)；[PRs](https://github.com/jaynlabs/jaynshare/pulls?q=is%3Apr)
- README 原话："This is a project by Jayn Labs started by my friend and I cause we were sick of getting rate limited while the other had plenty leftover quota." 还有 "PLEASE, yes this was GREATLY done with AI." — [README](https://github.com/jaynlabs/jaynshare/blob/main/README.md)
- 致谢 TeamClaude："Shout out to TeamClaude on github. We were more than inspired by their work cause our v1 was mostly a fork." — [README](https://github.com/jaynlabs/jaynshare/blob/main/README.md)。被 fork 的上游是 `KarpelesLab/teamclaude`，描述为 "Multi-account proxy for Claude Code and Codex…rotates on quota"，476 stars — [teamclaude](https://github.com/KarpelesLab/teamclaude)
- v2 是用 Rust 从零重写的版本：v2.0.0 发布于 2026-10-01，最新 v2.4.0 发布于 2026-10-20 — [Releases](https://github.com/jaynlabs/jaynshare/releases)；第三方 man page 也写到 "Version 2 is a from-scratch Rust rewrite" — [linuxcommandlibrary](https://linuxcommandlibrary.com/man/jaynshare)
- 官网 jayn.app/jaynshare 的标语："share your AI subscription! supports: Claude Code, Codex, Claude Desktop…jaynshare puts subscriptions in a group and routes requests to accounts with room left." — [jayn.app/jaynshare](https://jayn.app/jaynshare)
- 域名消歧：`jaynshare.com`、`.ai`、`.io`、`.cn` 在 2026-11-06 的 DNS 查询中均无法解析，官方入口只有 `jayn.app/jaynshare`（本人 curl/dig 实测，无 URL 可引）。npm 上没有 `jaynshare` 包（[registry 返回 Not found](https://registry.npmjs.org/jaynshare)），项目通过 GitHub Releases 分发二进制。
- 社交渠道：README、官网和 GitHub 资料里都没有 Discord、X 或 Telegram 链接，唯一联系方式是官网的 "contact us" — [jayn.app/jaynshare](https://jayn.app/jaynshare)

**AweShare（实体确认）**
- GitHub 搜索 "aweshare" 有两个结果。`Wehuman01/aweshare` 的描述是 "Share local Ollama/vLLM or authorized OpenAI/Anthropic backends through a self-hosted hub — a sharing economy for tokens."，9 stars、0 forks。另一个是 `Maslosoft/AweShare`（"Font awesome based sharer"，2020 年，0 stars，与本主题无关） — [Wehuman01/aweshare](https://github.com/Wehuman01/aweshare)；[Maslosoft/AweShare](https://github.com/Maslosoft/AweShare)
- 仓库创建于 2026-08-18，最后一次 push 在 2026-10-30，license 字段为 NOASSERTION — [GitHub API](https://api.github.com/repos/Wehuman01/aweshare)
- 公开仓库**只有文档**。几乎全部 commit 都是 `release-bot` 提交的 "docs: sync from aweshare-source"，源码在私有仓库 `aweshare-source`。README 写明 CI 会 "mirrors user-facing docs to the public repo" — [commits](https://github.com/Wehuman01/aweshare/commits/main)；[README Development 段](https://github.com/Wehuman01/aweshare/blob/main/README.md)
- 许可证是 "aweshare Proprietary License"：可以免费使用、自托管、自行修改，但 "You may not redistribute…or provide the software…as a product or hosted service to third parties"；条款还写明 "The copyright holder is not obligated to make source code available"；商业授权需另行联系 — [LICENSE](https://github.com/Wehuman01/aweshare/blob/main/LICENSE)
- **自相矛盾之处**：README 自称 "source-available"，npm 包描述却是 "Open-source, local-first AI capability relay"，0827 文章还说 "The code is open source, every line"，而许可证明确是专有的，源码仓库也不公开 — [npm registry](https://registry.npmjs.org/aweshare)；[0827 文章](https://github.com/Wehuman01/aweshare/blob/main/docs/article_media/0827/aweshare-community-hub-beta-10-spots.md)；[LICENSE](https://github.com/Wehuman01/aweshare/blob/main/LICENSE)
- 分发方式：npm 包 `aweshare`（要求 Node ≥ 22）和 Docker 镜像 `ghcr.io/wehuman01/aweshare`。npm 包创建于 2026-08-17，到 2026-10-30 已发布 63 个版本，最新为 0.8.3。npm 维护者是 `mugpeng`（mugpeng@outlook.com），npm 上 license 字段为 "UNLICENSED" — [npm registry](https://registry.npmjs.org/aweshare)；[README](https://github.com/Wehuman01/aweshare/blob/main/README.md)
- 作者：组织 `Wehuman01`（name "wehuman"，bio "Build tools that give people more room to choose."，email `peng@wehuman.top`，创建于 2026-06-26，17 个公开仓库） — [GitHub API users/Wehuman01](https://api.github.com/users/Wehuman01)。个人账号 `mugpeng`（name "Peng"，bio "University of Macau"，location "macao"，68 followers） — [GitHub API users/mugpeng](https://api.github.com/users/mugpeng)
- AweShare 是作者 "awe*" 工具家族的一员，同系列还有 aweskill、aweswitch、awerouter、aweshelf、awewarm（"Subscription window warmer that keeps AI coding-plan windows active"）、awescholar、awefork、awedot 等 — [README Awesome Ecosystem](https://github.com/Wehuman01/aweshare/blob/main/README.md)
- 中文/中国大陆取向很明显。双语 README（含 README_cn.md）；每日额度按 "per Beijing day (UTC+8)" 重置；默认时区 Asia/Shanghai；示例后端包括智谱 GLM coding plan（open.bigmodel.cn）、StepFun、Kimi；示例里出现 "a ladder such as Clash on http://127.0.0.1:7890"；收款用微信支付二维码 — [README](https://github.com/Wehuman01/aweshare/blob/main/README.md)；[社区 hub 文档](https://github.com/Wehuman01/aweshare/blob/main/docs/community-hub/README.md)
- 社交渠道：作者有微信公众号文章系列，首篇为《aweshare：迈入共享token 时代》 — [WeChat](https://mp.weixin.qq.com/s/zFRIuxdLj6F5vPj9P7rXAQ)（抓取时遇到微信验证墙，未能读到正文）。另有 Ko-fi `mugpeng` 赞助页 — [FUNDING.yml](https://github.com/Wehuman01/aweshare/blob/main/.github/FUNDING.yml)
- 域名消歧：`aweshare.com` 在 2026-11-06 302 跳转到 HugeDomains 的域名出售页（`hugedomains.com/domain_profile.cfm?d=aweshare.com`），说明是停放域名，与本项目无关（本人 curl 实测）。`aweshare.ai`、`aweshare.io`、`aweshare.cn` 无法解析。项目唯一在线服务是 `https://aweshare.wehuman.top`，其 `/healthz` 返回 `{"ok":true,"version":"0.8.3","wire":2}` — [hub healthz](https://aweshare.wehuman.top/healthz)
- 时间线：npm 首版 0.2.0 发布于 2026-08-17。0820 文章教用户让 agent 帮忙安装；0822 文章讲 Cloudflare Tunnel 部署 hub；0827 社区 hub 开放内测；0830 支持共享 Codex 登录；0901 发表「去中心化」设计文章；0.8.0 发布于 2026-10-24 — [CHANGELOG](https://github.com/Wehuman01/aweshare/blob/main/docs/CHANGELOG.md)；[article_media 目录](https://github.com/Wehuman01/aweshare/tree/main/docs/article_media)

**容易混淆的其他项目**
- `claude-share`（npm `@0xpv/claude-share`）是另一个通过配对链接共享 Claude Code 订阅的 CLI，与上面两者无关 — [pkgstats](https://www.pkgstats.com/pkg:@0xpv/claude-share)；[Product Hunt](https://www.producthunt.com/p/claude-share/claude-share)

### Inferences
- 两者都是个人或两人级别的副业/开源项目，不是有融资的公司产品。JaynShare 在法国、偏海外；AweShare 的作者在澳门、用户偏中文圈。
- AweShare 的「专有许可 + 私有源码 + 宣传为开源」组合，竞品研究时应看作 **source-available 闭源**。不过 npm 包里的 `dist/*.js` 是未压缩、带注释的 tsc 输出，实际上可以读到实现细节（本次已下载 0.8.3 tarball 验证）。
- 「JaynShare / AweShare」都没有任何大陆平台品牌名、中文商标或 App。如果用户是在某个中文社区看到这两个名字，指的应该就是这两个 GitHub 项目。

### Gaps
- 微信公众号文章正文被验证墙挡住，没能拿到发布日期、阅读量和社群（微信群）信息。
- 没找到 JaynShare 团队的融资或公司注册信息，Jayn Labs 是否为注册实体不明。
- JaynShare 的 v1（TeamClaude fork）没有公开 release，v1 的确切上线日期不明。

---

## 2. 共享机制：贡献什么、如何记账/兑换、有无现金

### Takeaway
JaynShare 共享的是 **Claude Pro/Max OAuth 订阅、Anthropic API key 和 ChatGPT（Codex）订阅**。账号进入池子后，任何受邀成员都可以按「谁还有余量就用谁」的方式自动取用，**没有积分、账本或配额对价**。「贡献多少才能取多少」的 Draw credits 还只是未勾选的 roadmap 项。AweShare 共享的是 **本地 Ollama/vLLM、各类 API key（含国内 coding plan）和 ChatGPT/Codex 登录**，不支持 Claude 订阅 OAuth。它的机制是「凭邀请码入场后，所有已发布的模型都可调用」，由生产者自设并发、每日 token 上限和共享时段。积分（参考科研通）、广告和可迁移信誉都还只是设想。两者都**不涉及现金交易**。

### Cited Findings

**JaynShare**
- 用法："Just replace `claude` by `jaynshare claude` (or `codex` by `jaynshare codex`, or open Claude Desktop with `jaynshare desktop`) and you can draw from your friends' subscriptions when yours is rate limited. PS : It also works if you pool several accounts of your own." — [README](https://github.com/jaynlabs/jaynshare/blob/main/README.md)
- 支持的账号类型："OAuth subscription and API-key accounts"；"Anthropic and OpenAI accounts added by their owners when joining" — [README Highlights](https://github.com/jaynlabs/jaynshare/blob/main/README.md)
- 加入流程："The join…offers to add each of your accounts to the pool, Claude and ChatGPT alike." 之后也可以用 `jaynshare account login` 补充添加。Codex 账号通过 `jaynshare account login --provider codex` 添加 — [README Quick start](https://github.com/jaynlabs/jaynshare/blob/main/README.md)
- 凭证来源有两种。一是导入本机 Claude Code 已保存的凭证："The credential Claude Code holds for this host — its Keychain item on macOS, else `~/.claude/.credentials.json` — copied into the pool and never modified." — [src/pool/managed.rs](https://github.com/jaynlabs/jaynshare/blob/main/src/pool/managed.rs)。二是浏览器 OAuth：由服务器发起 PKCE 流程，客户端在本地回环端口接住 callback 后转发给服务器 — [src/login.rs](https://github.com/jaynlabs/jaynshare/blob/main/src/login.rs)；[src/control/client_accounts.rs](https://github.com/jaynlabs/jaynshare/blob/main/src/control/client_accounts.rs)
- 取用方式：`--account <name>` 指定账号；`--auto` 由池子挑选账号，余量用完时自动迁移会话；`--direct` 不走池子，用自己的登录；单独运行 `jaynshare` 会打开所有池内账号的选择器，并显示每个账号的 5 小时和每周用量 — [README](https://github.com/jaynlabs/jaynshare/blob/main/README.md)
- 账号有归属概念，但没有记账：加入时添加的账号 "owned by that client"，归属只决定谁能对该账号重新登录 — [deploy/README-server.md](https://github.com/jaynlabs/jaynshare/blob/main/deploy/README-server.md)；[security-and-privacy.md](https://github.com/jaynlabs/jaynshare/blob/main/docs/security-and-privacy.md)
- **互助/对等机制尚未实现**。Roadmap 里未勾选的项包括："Owner limits: cap what the pool draws from you, and pause it yourself"、"Draw credits: what you can draw from others' accounts follows what you give the pool" — [README Roadmap](https://github.com/jaynlabs/jaynshare/blob/main/README.md)
- 其余未实现的 roadmap 项：Windows 版 Claude Desktop、Codex 使用 OpenAI API key 账号、"API-key subscriptions from other providers, like OpenCode Go"、Linux 客户端安装器。另外 "Account features (settings, plugins, skills, MCP servers)…the pool refuses them for everyone today" — [README Roadmap](https://github.com/jaynlabs/jaynshare/blob/main/README.md)
- 官网的 "what should we do next?" 栏目列出 "Codex implemented / Cursor ✓ / Claude Desktop implemented"，看起来是功能投票，Cursor 是候选项 — [jayn.app/jaynshare](https://jayn.app/jaynshare)
- 现金：仓库和官网都没有任何付费或结算机制。is_this_safe.md 列出了三种可能的模式（"Company supplies subscriptions / Customers contribute subscriptions / Mixed pool"），并提醒付费服务可能面临 "customer refund demands" 和 "contributor compensation" 的披露义务 — [is_this_safe.md](https://github.com/jaynlabs/jaynshare/blob/main/is_this_safe.md)

**AweShare**
- 定位："share your idle local models and token subscriptions, or use what others share — a sharing economy for tokens." — [README](https://github.com/Wehuman01/aweshare/blob/main/README.md)
- 三个角色：Producer（贡献者）、Consumer（使用者）、Hub operator（守门人）。"a producer shares **capability**, not **keys**" — [0828 三角色文章](https://github.com/Wehuman01/aweshare/blob/main/docs/article_media/0828/aweshare-three-roles.md)
- 可贡献的后端类型（`[[backends]]`）：本地 Ollama/vLLM；`protocol = "anthropic"` 加 API key；OpenAI 兼容接口（如 StepFun）；Responses 兼容接口（如 "a GLM coding-plan key (Codex-ready)"）；以及 `login = "codex"`，即用生产者本机的 `codex login`（ChatGPT Plus/Pro）共享 — [README Producer config](https://github.com/Wehuman01/aweshare/blob/main/README.md)；[0830 Codex 登录文章](https://github.com/Wehuman01/aweshare/blob/main/docs/article_media/0830/aweshare-codex-login-sharing-en.md)
- **不支持 Claude Pro/Max 订阅 OAuth**。编译后代码中，账号登录型后端只有 `login === 'codex'` 一种。Anthropic 适配器只会设置 `headers['x-api-key'] = backend.apiKey`，包里找不到 Claude OAuth 或 `.credentials.json` 相关代码 — [npm tarball aweshare-0.8.3](https://registry.npmjs.org/aweshare/-/aweshare-0.8.3.tgz)（`packages/producer-core/dist/adapters.js`，本人下载后 grep）。README 对 Claude Code 的说明也只限于消费端，即把 `ANTHROPIC_BASE_URL` 指向 hub — [README](https://github.com/Wehuman01/aweshare/blob/main/README.md)
- 入场与权限："Invite-only trust: admission runs through one-time invite codes the operator mints; every admitted consumer may call every offering. **No payments, no marketplace.**" — [README](https://github.com/Wehuman01/aweshare/blob/main/README.md)
- 命名空间：别名形如 `peng/gpt-4o`，"globally unique with one owner — routing is a deterministic lookup"。一个 offering 只对应一个上游模型，hub 会把请求里的 model 改写成 `upstreamModel` — [README](https://github.com/Wehuman01/aweshare/blob/main/README.md)
- 生产者可以自设的护栏："maxConcurrencyPerUser" 默认 1；"maxConcurrentUsers" 默认 3；"dailyTokens" 默认每个北京日 1,000,000（0 表示不限）；`shareWindows` / `blockWindows` / `shareDays` 控制共享时段，例如只在 00:00–06:00 共享；`aweshare producer refresh --add N` 可以临时追加当日额度。"quota expansion is the owner's call — the hub operator…never spends more of its upstream budget" — [README](https://github.com/Wehuman01/aweshare/blob/main/README.md)
- 消费者侧限额（由运营者设置）：`rps`、`burst`、`maxConcurrent`、`tpm`、`maxTotalTokens`（终身 token 预算）、`probeBudget` — [README Consumer limits](https://github.com/Wehuman01/aweshare/blob/main/README.md)
- 账本：只记元数据。"Keep the ledger, never the content. One line per request: who, which model, how many tokens, how long…The ledger is enough to settle fairness" — [0828 文章](https://github.com/Wehuman01/aweshare/blob/main/docs/article_media/0828/aweshare-three-roles.md)。消费者可以用自己的 key 查询 `GET /admin/v1/usage`，只能看到自己的记录 — [README](https://github.com/Wehuman01/aweshare/blob/main/README.md)
- 早期版本有生产者授权（grants）机制，"each producer alone decides who may call their aliases" — [CHANGELOG](https://github.com/Wehuman01/aweshare/blob/main/docs/CHANGELOG.md)。当前 README 的说法则是 "every admitted consumer may call every offering" — [README](https://github.com/Wehuman01/aweshare/blob/main/README.md)
- **积分/互助只是设想**："Sustainability is being explored. Down the road: ad revenue, or rewarding producers with credits the way compute-sharing communities (like 科研通) do — the compute you contribute earns points, and points may become redeemable down the line. All still ideas" — [0827 社区 hub 文章](https://github.com/Wehuman01/aweshare/blob/main/docs/article_media/0827/aweshare-community-hub-beta-10-spots.md)
- 跨 hub 设想："Federated catalogs"、"Hub-to-hub peering"、"Portable reputation…usage history, a history of playing fair…can travel with you"，并注明 "still on paper, nothing shipped" — [0901 去中心化文章](https://github.com/Wehuman01/aweshare/blob/main/docs/article_media/0901/aweshare-decentralization-en.md)
- 社区 hub 的入场方式：给 `peng@wehuman.top` 发邮件申请邀请码（`asi_…`）。内测阶段 "10 consumer spots, first come first served — and no cap on producers" — [0827 文章](https://github.com/Wehuman01/aweshare/blob/main/docs/article_media/0827/aweshare-community-hub-beta-10-spots.md)；[社区 hub 文档](https://github.com/Wehuman01/aweshare/blob/main/docs/community-hub/README.md)

### Inferences
- 用户提出的「先贡献、后提取」的互助信用模式，在这两个项目里都**还没落地**。它们本质上是「熟人小群共用池」，公平靠人际信任加限额维持，而不是靠记账。JaynShare 的 "Draw credits" 和 AweShare 的「科研通式积分」恰好说明，两位作者都认为下一步需要对等记账，但都还没做。这一块是空白点。
- AweShare 的「共享时段」（比如只在夜里共享）和「每日 token 上限」，是给贡献者「我自己白天要用」这种顾虑设计的。互助式产品可以直接借鉴。
- AweShare 不支持 Claude 订阅 OAuth，这是它与「共享 Claude Pro/Max 余量」场景之间最大的缺口。可能原因有两个：作者有意规避 Anthropic 的 OAuth 限制（他在合规段落里把 personal-subscription 列为高风险）；或者 Claude OAuth token 需要特殊的头和 beta 标志才能调用。这一点属于推测。

### Gaps
- 两个项目都没有公开的用户兑换数据（实际有多少账号入池、每个账号被他人取用了多少）。
- AweShare 当前版本的 grants（按生产者授权）是否仍可用、与 "every admitted consumer may call every offering" 怎么共存，文档里没有说清楚。

---

## 3. 商业模式与变现

### Takeaway
两者目前都**不收费、也没有托管 SaaS**。JaynShare 是 MIT 开源，母品牌 jayn.app 的主业是「按节省额收费」的 AI 降本咨询，jaynshare 更像获客或展示用的开源项目。AweShare 靠 Ko-fi 和微信打赏，专有许可证保留了商业授权的口子，作者设想过广告或积分，但都没有实施。单位经济数据均不可得。

### Cited Findings
- jayn.app 首页："Cut your AI bill. Only pay on what you save. jayn is a full time software engineer who audits AI features you already run and finds the cheapest setup for the same quality." 页面还列出若干案例，如 "support bot, same model bill −59%"、"bill −98%"（日期 8 月 21 日至 10 月 23 日） — [jayn.app](https://jayn.app)
- jaynshare 页面的转化入口："interested in reducing AI costs in your company? contact us ↗" — [jayn.app/jaynshare](https://jayn.app/jaynshare)
- JaynShare 是 MIT 许可、完全自托管的："Self-hosted · Linux server · macOS & Windows client"。仓库里没有任何计费代码或托管服务 — [README](https://github.com/jaynlabs/jaynshare/blob/main/README.md)；[LICENSE](https://github.com/jaynlabs/jaynshare/blob/main/LICENSE)
- JaynShare 文档讨论过付费部署的风险分配（"Company supplies subscriptions…buyers' payments and service continuity"；"a paid service may owe refunds or incur other liability"），但没有给出自己的定价 — [is_this_safe.md](https://github.com/jaynlabs/jaynshare/blob/main/is_this_safe.md)
- AweShare 的变现现状："aweshare is free to use and self-host. Sponsors keep it maintained"，渠道是 Ko-fi 和微信收款码 — [README Support](https://github.com/Wehuman01/aweshare/blob/main/README.md)；[FUNDING.yml](https://github.com/Wehuman01/aweshare/blob/main/.github/FUNDING.yml)
- AweShare 许可证禁止 "provide the software…as a product or hosted service to third parties"，"For licensing exceptions (e.g. commercial licensing), contact the copyright holder." — [LICENSE](https://github.com/Wehuman01/aweshare/blob/main/LICENSE)
- 社区 hub 免费、由作者个人运营，不保证可用性："It is run personally by the developer with no uptime guarantees." — [社区 hub 文档](https://github.com/Wehuman01/aweshare/blob/main/docs/community-hub/README.md)
- 未来可能的方向："ad revenue, or rewarding producers with credits…All still ideas — come help define them." — [0827 文章](https://github.com/Wehuman01/aweshare/blob/main/docs/article_media/0827/aweshare-community-hub-beta-10-spots.md)

### Inferences
- JaynShare 的商业逻辑很可能是「开源工具 → 企业 AI 降本咨询线索」，而不是对共享本身收费。
- AweShare 的许可证一方面禁止第三方把它包装成托管服务，另一方面保留商业授权，说明作者至少想给自己留一个将来做托管或平台的选项。
- 两者都没有任何「抽佣」「出售额度」的设计。原因可能是作者们意识到收费会让 ToS 风险从「账号共享」升级成「转售」（JaynShare 的 is_this_safe.md 已经明确提到付费服务的退款和赔偿责任）。

### Gaps
- 没有收入、赞助金额、付费企业客户或单位经济数据。jayn.app 咨询业务与 jaynshare 之间的实际转化情况不明。

---

## 4. 技术架构

### Takeaway
**JaynShare**：一个 Rust 二进制同时充当服务端和客户端。服务端部署在 Tailscale 等私网里，**集中保管所有 OAuth token 和 API key**。客户端用 `jaynshare claude` 启动 Claude Code，注入转发代理和池子的私有 CA，服务器对 api.anthropic.com 和 chatgpt.com **做 TLS 中间人**，读请求体、注入所选账号凭证、按 5 小时和每周用量自动选号与故障转移。**AweShare**：Node.js 写成，hub 是 node:http + ws + SQLite。**生产者本机运行 agent，主动向 hub 建立 WSS 反向隧道**，上游 key 只在本机注入。消费者用标准 OpenAI/Anthropic SDK，把 base URL 指向 hub、按 `namespace/alias` 调用。hub 负责鉴权、路由、计量和故障转移，但**看得到明文请求**。用户描述的那种「凭证不离开贡献者机器、反向隧道流式回传」的架构，正是 AweShare 的做法。

### Cited Findings

**JaynShare：部署与接入**
- 服务端安装命令：`curl -fsSL https://github.com/jaynlabs/jaynshare/releases/latest/download/install.sh | sudo sh`，以 systemd 服务方式运行，监听在 "the host's Tailscale address (or its only private address)"，装完输出邀请命令 `jaynshare join jsi1_…`。"An invite works once and expires after a day." — [README](https://github.com/jaynlabs/jaynshare/blob/main/README.md)
- 客户端：macOS 用 `curl -fsSL …/install.sh | sh -s -- join jsi1_…`，Windows 用 PowerShell 的 `irm …/install.ps1`。"The client updates itself whenever the server does." — [README](https://github.com/jaynlabs/jaynshare/blob/main/README.md)
- 在 Mac 上试用：`curl -fsSLO …/quickstart.sh && bash quickstart.sh`，会在本机起一个服务端 — [README](https://github.com/jaynlabs/jaynshare/blob/main/README.md)
- 要求："A Linux server with systemd"、"A private network between the engineers and the server, such as a tailnet"。"Listeners bind only to loopback or private addresses (10/8, 172.16/12, 192.168/16, 100.64/10, fc00::/7); installation refuses anything else." — [README](https://github.com/jaynlabs/jaynshare/blob/main/README.md)；[security-and-privacy.md](https://github.com/jaynlabs/jaynshare/blob/main/docs/security-and-privacy.md)
- 代码规模：src 下 Rust 约 52.5k 行，GitHub 统计 Rust 约 3.79 MB；配套大量验收测试（tests/acceptance/*.rs）；CI 使用 GitHub Actions；release 有签名 — [GitHub API languages](https://api.github.com/repos/jaynlabs/jaynshare/languages)；[repo tree](https://github.com/jaynlabs/jaynshare)

**JaynShare：MITM 数据面**
- "Jaynshare is a **trusted intermediary**. It is not end-to-end encrypted…`jaynshare claude` launches Claude Code with the pool's forward proxy and the pool's CA. Claude Code's TLS session to `api.anthropic.com` ends at the Jaynshare server, which then opens its own verified TLS connection to Anthropic…The server must read request bodies to pick an account, inject that account's credential, and buffer the request so it can retry it on another account." — [security-and-privacy.md](https://github.com/jaynlabs/jaynshare/blob/main/docs/security-and-privacy.md)
- CA 的注入方式：通过 `NODE_EXTRA_CA_CERTS`（Claude Code）或 `CODEX_CA_CERTIFICATE`（Codex）只交给被启动的那个进程；Claude Desktop 则需要把 CA 装进 macOS 钥匙串。服务端终结 `api.anthropic.com`、`chatgpt.com`、`downloads.claude.ai` 的 TLS，其他 CONNECT 目标透明转发。在 chatgpt.com 上只转发 Codex 自己的路径和用量查询 — [security-and-privacy.md](https://github.com/jaynlabs/jaynshare/blob/main/docs/security-and-privacy.md)
- 凭证存放："OAuth tokens and API keys live in the server's state file, written with mode `0600`…Clients receive neither the credentials nor the configuration." — [security-and-privacy.md](https://github.com/jaynlabs/jaynshare/blob/main/docs/security-and-privacy.md)。服务端负责刷新 OAuth：[src/pool/refresh.rs](https://github.com/jaynlabs/jaynshare/blob/main/src/pool/refresh.rs) 的说明为 "OAuth token-family refreshes shared by every trigger for one account"
- 降低被识别风险的措施：每个池内账号生成一个稳定的 machine ID，每次请求都使用所选账号自己的 ID；剥离 cookie、客户端 IP 头和身份头；拦截 Claude event logging、GrowthBook、Datadog 和 Codex analytics 的遥测。文档也承认 "This does not hide concurrency or guarantee that providers cannot correlate traffic." 另有出口 IP 锁定功能（egress guard）："no attempt goes out while the server's public address is not one of the pinned ones" — [security-and-privacy.md](https://github.com/jaynlabs/jaynshare/blob/main/docs/security-and-privacy.md)；[src/data_plane/egress.rs](https://github.com/jaynlabs/jaynshare/blob/main/src/data_plane/egress.rs)

**JaynShare：调度、5 小时和每周限额、故障转移**
- 选号优先级："pin, preference, operator route preference, the session's binding, the default, the ranking, and the revalidation offer" — [src/pool/selection.rs](https://github.com/jaynlabs/jaynshare/blob/main/src/pool/selection.rs)
- 额度来源：解析 Anthropic 响应头（如 `anthropic-ratelimit-unified-5h-utilization`），按 session（5 小时）和每周（可按模型族细分为 `weekly:<family>`）两个桶计算；再加上定时的 OAuth 用量扫描 — [src/pool/quota.rs](https://github.com/jaynlabs/jaynshare/blob/main/src/pool/quota.rs)；[src/pool/probe.rs](https://github.com/jaynlabs/jaynshare/blob/main/src/pool/probe.rs)
- 切换阈值：默认 `switch_threshold = 0.98`，`failover` 默认开启（源码测试 `minimal_document_takes_every_default`）。用量达到阈值的账号不再接新会话，但已绑定的会话不受影响 — [src/config/mod.rs](https://github.com/jaynlabs/jaynshare/blob/main/src/config/mod.rs)；[src/pool/selection.rs](https://github.com/jaynlabs/jaynshare/blob/main/src/pool/selection.rs)
- 429 和 hold 记账、组织级消费上限 hold、账号切换后的并发 ramp：见 [src/pool/holds.rs](https://github.com/jaynlabs/jaynshare/blob/main/src/pool/holds.rs) 和 [src/pool/ramp.rs](https://github.com/jaynlabs/jaynshare/blob/main/src/pool/ramp.rs)（"A ranking move starts a ramp on the new default — a concurrency cap that grows by a step every interval"）
- 会话粘性：一个会话由 (principal, `x-claude-code-session-id`) 标识，"set at its first attempt, moved only by failover"，作用是保住 prompt cache — [src/pool/sessions.rs](https://github.com/jaynlabs/jaynshare/blob/main/src/pool/sessions.rs)。v2.4.0（2026-10-20）新增 "move an automatic session off an exhausted account" — [Release PR #21](https://github.com/jaynlabs/jaynshare/pull/21)
- **宣传与文档不一致**：官网写 "session swap is smooth, no cache miss, no delay!" — [jayn.app/jaynshare](https://jayn.app/jaynshare)；README 则写 "a moved conversation starts without its prompt cache" — [README](https://github.com/jaynlabs/jaynshare/blob/main/README.md)
- 可观测性：Claude Code 状态栏显示当前服务的账号；`jaynshare status` 用彩色表格展示每个账号的 5 小时和每周用量；审计日志 "metadata only…No body and no credential"；抓包功能（wire capture）默认关闭，开启时所有客户端都能看到 — [README](https://github.com/jaynlabs/jaynshare/blob/main/README.md)；[security-and-privacy.md](https://github.com/jaynlabs/jaynshare/blob/main/docs/security-and-privacy.md)
- 客户端隔离："Cannot use its credential to read client B's prompts…A response is returned only on the connection that made the request" — [security-and-privacy.md](https://github.com/jaynlabs/jaynshare/blob/main/docs/security-and-privacy.md)
- Claude Desktop（macOS）：通过本地 Gateway 加 Jaynshare profile 接入，并禁用 Desktop 自动更新；每次启动先用合成请求做兼容性自检；"Chat and Code go through the pool…Attachments, Cowork, Chrome, connectors, projects, memory and artifacts are untested." — [README](https://github.com/jaynlabs/jaynshare/blob/main/README.md)；[docs/desktop.md](https://github.com/jaynlabs/jaynshare/blob/main/docs/desktop.md)

**AweShare：反向隧道架构**
- 架构图与原话："Producers run a lightweight agent on their own machine…Upstream API keys live only on the producer's device and are injected by the local agent at forwarding time. Consumers point a standard OpenAI/Anthropic SDK at the hub and call models by `namespace/alias`." 图中写着 "aweshare hub (public, 1 node) ◄── WSS reverse tunnel (agent dials out) — no public IP, no port forwarding needed"，hub 职责为 "auth / route / meter" — [README](https://github.com/Wehuman01/aweshare/blob/main/README.md)
- 实现细节："hub server on bare `node:http` + `ws` + SQLite: producer tunnel endpoint `/ws/v1/producer` (upgrade auth, latest-wins, heartbeat sweep), consumer endpoints `/v1/chat/completions`, `/v1/messages`, `/v1/models`…transparent SSE byte relay with tee-based best-effort token extraction…120s head ticket / 300s stream-idle timeouts, cancellation propagation" — [CHANGELOG](https://github.com/Wehuman01/aweshare/blob/main/docs/CHANGELOG.md)。monorepo 结构为 `packages/protocol`、`packages/producer-core`、`apps/hub`、`apps/agent`，有 197 个测试 — [README Development](https://github.com/Wehuman01/aweshare/blob/main/README.md)
- 协议：只做原样中转，不做协议转换。"v1 relays native transparent SSE for OpenAI↔OpenAI (chat completions and Responses), Anthropic↔Anthropic. No cross-protocol conversion, no smart routing, no web console." — [README](https://github.com/Wehuman01/aweshare/blob/main/README.md)
- 消费端接入：Claude Code 设置 `ANTHROPIC_BASE_URL=https://hub.example.com`、`ANTHROPIC_API_KEY=asc_...`，再 `claude --model peng/sonnet`；Codex 配置 `[model_providers.aweshare] base_url = ".../v1"`，`wire_api = "responses"`；OpenAI SDK 用 `baseURL: '.../v1'`；OpenCode 用 `@ai-sdk/openai-compatible` — [README](https://github.com/Wehuman01/aweshare/blob/main/README.md)；[0830 文章](https://github.com/Wehuman01/aweshare/blob/main/docs/article_media/0830/aweshare-codex-login-sharing-en.md)
- 生产端安装：`npm install -g aweshare` → `aweshare producer join --hub … --code asi_…` → 编辑 `~/.aweshare/config.toml` 和 `secrets.json`（0600）→ `aweshare producer doctor` → `aweshare producer start [--background | --install]`（可安装为 launchd 或 systemd 服务）。"when it stops, aliases go offline and consumers get 503" — [README](https://github.com/Wehuman01/aweshare/blob/main/README.md)
- hub 部署：`aweshare hub init && aweshare hub serve`（端口 :8787，前面需自配 Caddy/nginx TLS），或者 `docker run … ghcr.io/wehuman01/aweshare:latest`；hub 自身也可以托管模型（`hub/…` 命名空间） — [README](https://github.com/Wehuman01/aweshare/blob/main/README.md)
- 令牌体系：`asa_`（admin）、`asp_`（producer）、`asc_`（consumer）、`asi_`（一次性邀请码） — [README](https://github.com/Wehuman01/aweshare/blob/main/README.md)
- Codex 登录后端："read from `${CODEX_HOME|~/.codex}/auth.json`, stays in producer memory only…re-read whenever the file changes or a request comes back 401"；上游被固定为 `https://chatgpt.com/backend-api/codex`，其他地址拒绝；会注入 Codex CLI 原生请求头并强制 `store: false`；"Tokens are never refreshed by aweshare"；`loginHome` 参数允许一台机器同时共享多个账号 — [README](https://github.com/Wehuman01/aweshare/blob/main/README.md)
- 故障转移：一是 **Alias backups**，由运营者配置有序备选列表，在生产者离线、后端降级、预算耗尽、共享窗口关闭、并发上限等情况下切换，响应头带 `x-aweshare-fallback`，"no mid-stream swaps"。二是 **Route groups**，由生产者把同一模型的多个账号声明成一个池，按顺序尝试；失败成员冷却 60 秒，连续失败则翻倍，最长 10 分钟 — [README](https://github.com/Wehuman01/aweshare/blob/main/README.md)；[CHANGELOG 0.8.0](https://github.com/Wehuman01/aweshare/blob/main/docs/CHANGELOG.md)
- 上游 5xx 处理：连续 2 次标记为 `unstable`；如果设置了 `degradeOn5xx = true`，连续 3 次即降级并返回 503 `BACKEND_DEGRADED`，之后每 30 秒探测恢复 — [README](https://github.com/Wehuman01/aweshare/blob/main/README.md)
- 防「挂羊头卖狗肉」：hub 会记录上游自报的 `observed_model`，与生产者声明的模型做比对（exact / compatible / mismatch），运营者可以封禁不一致的 alias — [CHANGELOG](https://github.com/Wehuman01/aweshare/blob/main/docs/CHANGELOG.md)
- 计量的局限："Ollama streams report no usage, so they contribute 0…observed-usage thresholds, not hard reservations" — [README](https://github.com/Wehuman01/aweshare/blob/main/README.md)
- 已知限制："Single hub instance + SQLite; no horizontal scaling"；"Corporate proxies may block the WebSocket tunnel" — [README Known limitations](https://github.com/Wehuman01/aweshare/blob/main/README.md)
- 由 agent 代装：可以对 Claude Code 说 "Read …/README.ai.md and follow it to install and configure aweshare"，同时附带一个 aweshare skill — [README](https://github.com/Wehuman01/aweshare/blob/main/README.md)；[resources/skills/aweshare/SKILL.md](https://github.com/Wehuman01/aweshare/blob/main/resources/skills/aweshare/SKILL.md)

### Inferences
- **两种架构的取舍**：
  - JaynShare 把 Claude Code 的原生流量整条劫持下来再换上池内账号的凭证，所以客户端完全无感，可以共享 Claude 订阅、保持会话粘性，也能读取官方的 5 小时和每周额度头。代价是所有人都必须完全信任服务器运营者，因为对方能拿到全部凭证和明文流量。
  - AweShare 让凭证留在贡献者本机，安全边界更好，但要求贡献者机器常年在线，也没有针对订阅 5 小时窗口的感知和调度（只有每日 token 上限和 429/5xx 降级）。
- 互助式产品如果要同时做到「凭证不离开贡献者」和「共享 Claude 订阅」，需要把 AweShare 的反向隧道和 JaynShare 级别的 Claude Code 协议适配结合起来。后者包括 OAuth 头、machine ID 一致性、遥测剥离、按 session 粘性保住 prompt cache、读取 unified rate-limit 头。目前两者都没有做到这一组合。
- JaynShare 统一 machine ID、剥离遥测、锁定出口 IP，客观上都是在降低供应商识别出「多人共享同一账号」的概率。从合规角度看，这些措施本身就是风险信号。

### Gaps
- AweShare 隧道协议的帧格式（wire 2）没有公开规范文档，只能从 dist JS 反推；本次没有逐行分析。
- JaynShare 在真实多用户环境下的故障率、延迟和封号率，都没有公开数据。

---

## 5. 目标用户与 traction

### Takeaway
两者的目标用户都是**互相信任的小圈子**：朋友、小团队、实验室，或者一个人有多个账号。规模都很小。截至 2026-11-06，JaynShare 有 43 stars，各 release 资产累计下载约 630 次；AweShare 有 9 stars，npm 月下载约 2.4k（可能被频繁发版放大），社区 hub 内测只开放 10 个消费者名额。中英文社区（Reddit、HN、V2EX、linux.do、小红书）都没检索到讨论。

### Cited Findings
- JaynShare 的定位："you might hit your limit while someone has barely used his account"，演示中是两人（hugo、maxence）互借额度 — [jayn.app/jaynshare](https://jayn.app/jaynshare)。运营者文档用 "engineers" 指代使用者，并要求 "Do not put people in one pool who should not share those consequences" — [security-and-privacy.md](https://github.com/jaynlabs/jaynshare/blob/main/docs/security-and-privacy.md)
- JaynShare 的数据：43 stars，4 forks（pritomdutta27、Thievinguniverse、raffaelcopy2005、campeasb）；1 个 issue（#17，由联合作者 hugopuybareau 提）；22 个 PR，其中 21 个出自 maxmaxou2，1 个外部 PR 出自 campeasb — [GitHub API](https://api.github.com/repos/jaynlabs/jaynshare)；[issues](https://github.com/jaynlabs/jaynshare/issues?q=is%3Aissue)；[PRs](https://github.com/jaynlabs/jaynshare/pulls?q=is%3Apr)
- JaynShare 各版本 release 资产下载量：v2.4.0 为 230，v2.3.3 为 160，v2.2.2 为 47，v2.1.0 为 45，v2.0.0 为 24，其余为个位数到十几次，合计约 630（含自动更新和 CI） — [Releases](https://github.com/jaynlabs/jaynshare/releases)（通过 GitHub API 汇总 `download_count`）
- AweShare 的定位：熟人群体，原话 "A familiar friend group: one has an idle GPU, one can't max out a subscription, and several have neither." 也覆盖团队、高校实验室和开源社区自建 hub — [0828 文章](https://github.com/Wehuman01/aweshare/blob/main/docs/article_media/0828/aweshare-three-roles.md)；[0901 文章](https://github.com/Wehuman01/aweshare/blob/main/docs/article_media/0901/aweshare-decentralization-en.md)
- AweShare 的数据：9 stars，0 forks，3 个已关闭的 feature issue — [GitHub API](https://api.github.com/repos/Wehuman01/aweshare)；[issues](https://github.com/Wehuman01/aweshare/issues?q=is%3Aissue)
- AweShare npm 下载量：最近一周（10-30 至 11-05）319 次，最近一月（10-07 至 11-05）2,427 次，2026-08-01 至 11-05 累计 11,796 次 — [npm downloads API last-week](https://api.npmjs.org/downloads/point/last-week/aweshare)；[last-month](https://api.npmjs.org/downloads/point/last-month/aweshare)；[range](https://api.npmjs.org/downloads/point/2026-08-01:2026-11-05/aweshare)
- 社区 hub 规模："The hub also applies global admission limits (consumers are capped, currently 10; producers are uncapped)" — [社区 hub 文档](https://github.com/Wehuman01/aweshare/blob/main/docs/community-hub/README.md)。hub 在 2026-11-06 在线，版本 0.8.3 — [healthz](https://aweshare.wehuman.top/healthz)
- 网络声量：WebSearch 检索 "jaynshare" 只找到 linuxcommandlibrary 的 man page — [linuxcommandlibrary](https://linuxcommandlibrary.com/man/jaynshare)。检索 "aweshare" 加 linux.do、V2EX、小红书、"共享token" 均无相关结果（检索于 2026-11-06）。

### Inferences
- npm 下载量会被频繁发版放大：63 个版本，10 月 24 日一天就发了 8 个以上，每次发版都会触发镜像和 CI 拉取。真实的独立安装量可能远低于 1.18 万。
- 两者都还在早期验证阶段，没有形成社区，也没有用户口碑。用户说它们是「互助式共享」的代表，更准确的说法是「互助式共享的早期开源原型」。
- 从 star 数看，被 JaynShare fork 的 TeamClaude（476 stars）这类「多账号轮换代理」的受众，比「对外共享」的受众大一个数量级（推测）。

### Gaps
- 没有拿到真实活跃用户数、入池账号数或社区 hub 的调用量（`/v1/catalog` 需要 consumer key）。
- 没有找到任何用户评价、投诉或媒体报道。微信文章阅读量拿不到。
- GitHub star 的时间分布 API 调用失败，无法判断增长曲线。

---

## 6. 风险：ToS、封号、安全隐私与投诉

### Takeaway
两个项目都**在文档里明确承认违反或可能违反 Anthropic/OpenAI 条款**，并把封号风险推给账号所有者。JaynShare 的额外风险是服务器运营者能看到并篡改全部明文流量、拿到所有凭证，客户端还需要信任一个私有 CA。AweShare 的额外风险是 hub 能看到明文流量，并会保存可回显的 token 明文，而它的社区文档在这一点上与 README 说法不一致。两者都没检索到公开的封号案例或用户投诉。

### Cited Findings
- JaynShare 的警告原文："Pooling Claude subscriptions conflicts with Anthropic's terms and can lead to suspension or termination of every account involved. Same for OpenAI…this project does not claim that it is legal or authorized." — [README](https://github.com/jaynlabs/jaynshare/blob/main/README.md)
- JaynShare 引用的条款："Its Consumer Terms prohibit sharing account credentials or making an account available to someone else, and its Claude Code documentation says that developers may not route requests through Free, Pro, or Max credentials for their users. A proxy, company ownership, or account-owner consent does not override those rules." OpenAI 方面："Terms of Use prohibit sharing account credentials or making an account available to anyone else"（审阅日期 2026-09-17） — [is_this_safe.md](https://github.com/jaynlabs/jaynshare/blob/main/is_this_safe.md)，其引用来源为 [Anthropic Consumer Terms](https://www.anthropic.com/legal/consumer-terms)、[Claude Code legal and compliance](https://code.claude.com/docs/en/legal-and-compliance)、[OpenAI Terms of Use](https://openai.com/policies/terms-of-use/)
- JaynShare 列出的后果："enrolled accounts can lose access, including the owner's personal use outside Jaynshare…a subscription terminated for a violation is not refunded…another participant's activity can…trigger enforcement against the account used" — [is_this_safe.md](https://github.com/jaynlabs/jaynshare/blob/main/is_this_safe.md)
- JaynShare 对运营者的信任要求："the server operator…can read prompts, code context, tool results, attachments, and model responses. They can also alter a request or a response, including injecting malicious instructions or code…No technical control makes an untrusted operator safe while keeping this routing design." 并且 "No independent security audit is claimed." — [security-and-privacy.md](https://github.com/jaynlabs/jaynshare/blob/main/docs/security-and-privacy.md)
- JaynShare 的合规责任转嫁："pooling an account requires your dated decision record…who decided that pooling it is permitted…No command asks for either statement; you carry both obligations by reading this page and installing anyway." — [deploy/README-server.md](https://github.com/jaynlabs/jaynshare/blob/main/deploy/README-server.md)
- JaynShare 的系统层面影响：Claude Desktop 模式需要把池子的 CA 装进 macOS 钥匙串，并会关闭 Desktop 自动更新；Codex 模式下，Codex 进程内的 `curl` 也会信任该 CA — [README](https://github.com/jaynlabs/jaynshare/blob/main/README.md)；[security-and-privacy.md](https://github.com/jaynlabs/jaynshare/blob/main/docs/security-and-privacy.md)
- AweShare 的合规说明："Sharing a personal-subscription key — coding plans included — with third parties likely violates those terms; self-hosted open models have no such issue. **When in doubt, don't share.**" 以及 "Sharing a CLI login (`login = "codex"`) raises the stakes further: the credential is account-wide…higher risk of account suspension or termination" — [README](https://github.com/Wehuman01/aweshare/blob/main/README.md)
- AweShare 的责任划分："The producer bears the consequences of sharing (key revocation, account suspension or termination by the upstream)." — [README](https://github.com/Wehuman01/aweshare/blob/main/README.md)
- AweShare 的隐私边界："consumer prompts and model responses transit the hub in plaintext — this is not end-to-end encryption. The hub persists no request/response content, but the hub operator can technically see it." — [README](https://github.com/Wehuman01/aweshare/blob/main/README.md)
- **AweShare 文档自相矛盾**：
  - README 写："Tokens are stored twice on purpose: a peppered SHA-256 hash drives authentication, and the plaintext is kept so the operator can hand a lost one back…A DB leak therefore exposes every identity" — [README](https://github.com/Wehuman01/aweshare/blob/main/README.md)；CHANGELOG 也记录了引入 `token_plain` 字段 — [CHANGELOG](https://github.com/Wehuman01/aweshare/blob/main/docs/CHANGELOG.md)
  - 社区 hub 文档却写："the hub stores your token only as a hash"、"It cannot be recovered from the hub (only a hash is stored)" — [社区 hub 文档](https://github.com/Wehuman01/aweshare/blob/main/docs/community-hub/README.md)
- AweShare 的滥用控制：一次性邀请码；可撤销、可恢复的暂停（`hub admin invite revoke/restore`）；按消费者限额；生产者可随时下架（`aweshare producer reload`）；model honesty audit — [README](https://github.com/Wehuman01/aweshare/blob/main/README.md)；[社区 hub 文档 FAQ](https://github.com/Wehuman01/aweshare/blob/main/docs/community-hub/README.md)
- 投诉与封号案例：两个仓库的 issue 区都没有封号报告（JaynShare 唯一的 issue 是关于 Codex 更新的问题）；公开网络也没检索到用户投诉 — [jaynshare issues](https://github.com/jaynlabs/jaynshare/issues?q=is%3Aissue)；[aweshare issues](https://github.com/Wehuman01/aweshare/issues?q=is%3Aissue)

### Inferences
- 两位作者都选择「工具自托管、风险自负、写长篇免责声明」来规避平台责任，都不对共享收费，也没有自营一个面向公众、托管 Claude 订阅的 hub。AweShare 的社区 hub 明确更欢迎 Ollama/vLLM 这类无 ToS 问题的后端，对个人订阅的态度是 "skip it if unsure"。这说明一旦平台化、收费或开放给陌生人，法律和封号风险会急剧上升。
- 如果做成「贡献者凭证不离开本机」的架构（AweShare 路线），平台在技术上不托管凭证。但从 Anthropic 和 OpenAI 的条款看，**让他人使用自己的订阅**本身就违规（JaynShare 引用的 "making an account available to someone else"），所以架构选择只能降低凭证泄露风险，不能降低封号风险。
- 互助积分一旦可以兑现，或者跨陌生人流通，就会更接近「转售」，ToS 和监管风险都比熟人共享更高。这是推断，需要法律意见确认。

### Gaps
- 没有找到 Anthropic 或 OpenAI 针对这两个具体工具的执法记录，也没有用户被封号的公开案例。
- 没有独立安全审计或第三方代码审查（JaynShare 明确说未做审计；AweShare 未提及）。
