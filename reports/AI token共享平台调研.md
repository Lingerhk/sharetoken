# 闲置 AI 额度共享平台踩在厂商红线上

截至 2026 年 11 月 6 日，真正在运转、让陌生人之间买卖闲置订阅额度的双边市场只有少数几家：**Asale**（asale.ai，卖家客户端开源，USDT 结算，每笔抽成 8%）、**AILE**（aile.sh，抽成 10%，USDC 结算，自报 30 天约 2.07 万笔结算）和 **Token Share**（tokenshare.app，卖方付 5%，只有 macOS 客户端）。最接近"先存额度、超额时再借"这一互助设想的是 **TokenBank**，但它还停留在候补名单阶段。用户原来的几个前提需要修正。**LetsSplit 不是 token 共享平台**，它是印度个人开发者做的订阅拼车撮合板。**ClawPool 已经关停**，原因是凑不够 Claude Max 账号，订阅者已按比例退款。**JaynShare 和 AweShare 都没有互助记账**，它们是熟人小圈子的自托管共用工具，"贡献多少才能取多少"只写在路线图或设想里。所谓"key 不离开本机就能分享"，实际做法是把卖家电脑变成执行节点，靠主动向外建立的长连接接单：平台下发带签名的任务，卖家客户端在本地注入自己的凭据，从自己的网络出口调用厂商，再把流式结果传回，平台按上游返回的 usage 记账。这样平台确实拿不到卖家凭据，但买家的明文 prompt 会被陌生卖家看到，合规性质也没有任何变化。Anthropic 明文禁止"代表他人通过 Pro/Max 凭据路由请求"，Asale 自己也承认出售额度是"明确违约"。需求是真的：Asale 过去 24 小时的消费额有一半来自中国买家。但供给建立在随时可能被封的个人订阅上，成交价已经跌到官方标价的 5%–7%。整个赛道规模很小，运营主体大多匿名。

## 先纠正三个前提：五个名字里只有 Asale 是真正在运转的额度市场

按 2026-11-06 的公开信息，用户把五个平台分成"互助共享"和"闲置 token 变现"两类，这个分法有三处不成立。

第一，**LetsSplit 不是 token 共享平台**。名字对得上、又和 AI 订阅有点关系的只有 letssplit.in。它是印度浦那个人开发者 Sudhanshu Burile 在 2026 年 6–7 月做的订阅拼车撮合板，主要分摊 Netflix、Spotify 这类家庭套餐的空位。ChatGPT、Claude 只以 logo 形式出现在首页 ([LetsSplit 首页](https://www.letssplit.in/))。它的开源代码里，服务种子列表一个 AI 厂商都没有。数据模型只有席位数、总价、每席单价和 UPI 收款号，没有任何凭据、API key、token 或用量字段 ([SubSplit db/schema.ts](https://github.com/sudhanshu-b/SubSplit/blob/main/db/schema.ts))。平台不经手资金，成员直接用 UPI 付钱给车主，测试期不收佣金 ([LetsSplit FAQ](https://www.letssplit.in/faq))。真正做"出售闲置 AI 额度"、名字又容易混淆的是 TokenBank、TokenlyPro 和 AI Token Exchange，下一节会讲。

第二，**ClawPool 已经关停**。clawpool.ai 的首页、定价页和 FAQ 都换成了停服公告，原文是"没能在池子里留住足够多的 Claude Max 账号来保证服务可靠"。现有 API key 已经失效，不再接受新订阅，活跃订阅者通过 Stripe 按比例退款 ([ClawPool 停服公告](https://clawpool.ai))。状态页显示 "0 active tokens"，至少从 11 月 2 日起每小时的请求数都是零 ([ClawPool 状态页](https://clawpool.ai/status))。确切停服日期不清楚，代码仓库最后一次推送是 2026-05-13 ([GitHub API](https://api.github.com/repos/peter-jammable/clawpool-proxy-function))。

第三，**JaynShare 和 AweShare 都没有"先贡献、后提取"的互助账本**。JaynShare 的池子里，受邀成员谁有余量就自动用谁的。"Draw credits：从别人账号取用多少，取决于你给池子贡献多少"在 README 路线图里还是一个没勾选的条目 ([JaynShare README](https://github.com/jaynlabs/jaynshare/blob/main/README.md))。AweShare 凭一次性邀请码入场，入场后"每个被接纳的消费者都能调用每个已发布的模型"，README 原话是 "No payments, no marketplace" ([AweShare README](https://github.com/Wehuman01/aweshare/blob/main/README.md))。作者设想过参照科研通给贡献者发积分，但同一篇文章写着 "All still ideas" ([AweShare 社区 hub 内测文章](https://github.com/Wehuman01/aweshare/blob/main/docs/article_media/0827/aweshare-community-hub-beta-10-spots.md))。两者本质上都是熟人圈子共用一个池子，公平靠人情和限额来维持，而不是靠记账。还有一点很容易被忽略：**AweShare 不支持共享 Claude Pro/Max 订阅**。它的账号登录型后端只接 ChatGPT/Codex，Anthropic 那一侧只接 API key。两个项目都不收费。JaynShare 的母品牌 jayn.app 主业是按节省金额收费的 AI 降本咨询，JaynShare 更像是给咨询业务引流的开源作品 ([jayn.app](https://jayn.app))。AweShare 靠 Ko-fi 和微信打赏维持，它的专有许可证禁止第三方把它做成托管服务，商业授权需要另谈 ([AweShare LICENSE](https://github.com/Wehuman01/aweshare/blob/main/LICENSE))。

| 平台 | 实际定位 | 共享或交易的对象 | 资金流 | 2026-11-06 状态 | 规模信号 |
|---|---|---|---|---|---|
| JaynShare | 法国两人团队的 MIT 开源自托管多账号路由器 | Claude Pro/Max、ChatGPT/Codex 订阅和 API key，集中存放在自建服务器上 | 无 | 活跃，v2.4.0（2026-10-20） | 43 star，各版本累计下载约 630 次（[GitHub](https://github.com/jaynlabs/jaynshare/releases)） |
| AweShare | 澳门大学作者开发、源码不公开的专有许可共享 hub | 本地开源模型、各类 API key、ChatGPT/Codex 登录；不含 Claude 订阅 | 无，靠打赏 | 活跃，0.8.3；社区 hub 只开放 10 个消费者名额 | 9 star，npm 月下载约 2,400 次（[npm](https://api.npmjs.org/downloads/point/last-month/aweshare)） |
| Asale | 撮合式 P2P 额度市场 | Claude、ChatGPT/Codex、Gemini、Kimi、Grok 订阅额度，以及部分 API key | USDT，平台抽 8% | 活跃，v1.2.4 | 24 小时成交约 330 USDT、约 1.5 万次任务 |
| ClawPool | 只做 Claude 的订阅登录拼池中转 | Claude Max 登录令牌 | Stripe 包月 $8–10 | **已关停** | HN 21 分，状态页请求数为 0 |
| LetsSplit | 印度订阅拼车撮合板 | 订阅套餐席位，在站外交接 | 站外 UPI 直付，不抽成 | 测试期 | Product Hunt 约 11 票（[PH](https://www.producthunt.com/products/letssplit)） |

## 其他平台：真正的双边额度市场不到五家，灰色中转站近千家

把范围扩大到所有和"共享或变现 AI 额度"有关的平台，可以看到一个金字塔结构。塔尖是少数几家真正让陌生人双向买卖闲置订阅额度的 P2P 市场。中间是一批还停在候补名单或概念阶段的同类项目。塔基是数以百计的中转站和拼车生意，它们由运营者自建账号池，单向卖给用户。

塔尖除了 Asale，还有两家在运营。**AILE** 自称 "the AI sharing ecosystem"，卖家可以挂订阅、API key 或机器，买家拿一个 key 就能用 OpenAI 和 Anthropic 两种协议调用 482 个文本模型。截至 11 月 6 日的 30 天内，它自报 **20,682 笔结算**、1,142 个报价、152 个在线卖家 ([AILE 首页](https://aile.sh/))。AILE 对成交用量收 **10%** 的市场费，"nodeless" 服务收 15%，通过 x402 支付的请求不收费。卖家收入以 Solana 链上的 USDC 结算，也可以选择换成在 pump.fun 发行的 $AILE 代币 ([AILE 条款](https://aile.sh/legal/terms))；11 月 2 日起买家可以刷卡充值 ([AILE notes](https://aile.sh/notes))。**Token Share**（Token Share, Inc.）只做 Claude Code。卖家运行一个 macOS 应用，通过端到端加密的 WebSocket 中继与之配对的买家的 Claude Code 请求，消耗的是卖家自己套餐的周额度，"凭据从不离开你的机器"。它只向卖方收 **5%** 的费用，买方不付费。官网示例中买家每个 session 付 $0.14，而 Anthropic 的 Extra usage 是 $0.42。每笔订单都有托管，争议须在 24 小时内提出，卖家掉线后约 2 秒内切换到同档位的另一个卖家，结算走 Stripe Connect 或 Base 链上的 USDC。它没有公开任何用户数据或团队信息 ([tokenshare.app](https://tokenshare.app/))。

用户设想的"先把余量存进去，超额时再借出来"的互助模式，在公开平台里只有 **TokenBank** 完整写出来了。它的流程是：接入订阅，在每日上限内存入未用完的额度来换积分，自己额度用完时用积分借 token。**积分不能提现**，账户活跃期间不过期，平台从积分消费里赚"一小部分价差"。不过它还是 "v0.1 · private beta"，自报候补名单 3,914 人、承诺存入 4,820 万 token，没有服务条款，也没有安全文档 ([tokenbank.dev](https://tokenbank.dev/))。在合规的开源模型领域，这种模式早有成熟先例：AI Horde 的志愿者按完成的任务赚 kudos，kudos 只能换排队优先级，"不能买卖"，也不过期 ([AI Horde README](https://github.com/Haidra-Org/AI-Horde))。

中间层的项目都缺少真实交易的证据。TokenlyPro 的条款日期是 2026-05-20，面向 B2B，按每百万 token 收固定费用，但看不到任何交易量 ([TokenlyPro 条款](https://www.tokenlypro.com/terms))。QUOTA 主动把个人消费套餐排除在外，理由是这类套餐"禁止共享访问"。它只接 API credits、带委派权限的团队套餐和自营算力，收益用不能兑现的 $QUOTA 代币支付，页面上的数据标着 "sample data" ([quotai.io](https://quotai.io/))。AI Token Exchange 让卖家装 Chrome 扩展，开着厂商的消费级网页应用来供货，向卖方收 5%。它目前只有候补名单，条款和隐私政策的链接都是占位符 "#" ([AI Token Exchange](https://aitokenexchange.net/index_en))。中文圈有几家：Token出租（token-share.app/zh）宣称价格只有官方 API 的 5%–15% ([云转迷文章](https://www.yunzhuanmi.com/token-chu-zu-diao-gong-xiang/))；"自足"在 V2EX 招募 Claude 额度提供者，帖子 714 次浏览、0 回复 ([V2EX](https://www.v2ex.com/t/1247430))；"闲猫·闲置Token交易市场"（idlicat.com）只有一个 JS 空壳页面 ([idlicat.com](https://idlicat.com/))；newtokenclub 是一个 0 star 的概念仓库 ([GitHub](https://github.com/newtokenclub/new-api))。

塔基规模大得多。监测站 aiapirank 从 **986 个中转站**里筛出 100 家提供 Claude Code 服务 ([aiapirank](https://aiapirank.github.io/vendor/claude-code/))，另一个榜单横向比较了 479 家 Claude 中转站 ([zhongzhuanzhanrank](https://zhongzhuanzhanrank.github.io/claude-zhongzhuanzhan/))。2026 年 5 月的一次评测显示，Sonnet 4.6 在各中转站的价格是每百万 token ¥2.4–10.5，官方价 ¥15；Opus 4.7 是 ¥7.5–28，官方价 ¥75。NekoCode 等站的货源明确写着来自"MAX号池" ([codingtoken.org](https://codingtoken.org/blog/relay-station-review-2026/))。DuckCoding 的"Claude Code 专用"分组是"自建 $200 Max 订阅号池" ([aitshirtdesign](https://aitshirtdesign.com/claudecode/duckcoding))。拼车商家把一个 Max 20x 账号拆成 5 个席位，每席 ¥499，以中转 API key 的形式交付 ([easyclaude](https://easyclaude.com/post/claude-code-max-pinche))。这些生意的底层大多是开源账号池软件：claude-relay-service 有 12.8k star，README 直接把它定位为"拼车共享"，同时提醒"可能违反 Anthropic 的服务条款" ([claude-relay-service](https://github.com/Wei-Shaw/claude-relay-service))；它的后继者 sub2api 加上了多租户分发和计费。这一层和用户设想的关键区别在于，用户在这里**只买不卖**。账号池归运营者，不是"每个人既是供给方也是需求方"的互助网络。

| 类别 | 代表 | 供给来源 | 结算方式 | 与"共享余量、超额时借用"的贴合度 |
|---|---|---|---|---|
| 运营中的 P2P 闲置额度市场 | Asale、AILE、Token Share | 个人订阅额度、API key（AILE 还包括机器） | 稳定币或现金，抽成 5%–10% | 高，但属于买卖，不是互助 |
| 互助积分型 | TokenBank | 订阅额度 | 积分不可提现，平台赚价差 | 最高，但只有候补名单 |
| 熟人共用工具 | JaynShare、AweShare、claude-share | 成员自己的订阅或 key | 无 | 中等，没有账本 |
| 早期或概念平台 | TokenlyPro、QUOTA、AI Token Exchange、Token出租、自足、闲猫、newtokenclub、SecondTokens | 来源混杂 | 平台币、积分或未说明 | 低到中等，大多没有真实交易证据 |
| 中转站与拼车 | 近千家中转站、Max 拼车卡、闲鱼拼车，底层是 CRS、sub2api | 运营者自建账号池、官方 key、AWS | 人民币按量计费或包月 | 低，单向售卖 |
| 合规或相邻的替代方案 | Sign in with ChatGPT、Claude Team 共享额度、AI Credits、AI Horde | 自己的订阅、组织额度、API credits、开源模型算力 | 官方计费或积分 | 低到中等，但合规 |

最后一类说明厂商正在自己吸收这部分需求。OpenAI 在 2026-09-29 上线 "Sign in with ChatGPT"，订阅用户可以在合作应用里花自己套餐的额度，并能给每个应用设每周上限 ([The New Stack](https://thenewstack.io/sign-in-with-chatgpt/))。Claude Team 套餐自带最高每月 $500 的共享 API 额度 ([Claude 定价页](https://claude.com/pricing))。API credits 的转售有经纪平台在做，比如 AI Credits，卖家能收回面值的 40%–70% ([getaiperks](https://www.getaiperks.com/en/ai/sell-ai-credits))。

## Asale、ClawPool、LetsSplit 是三种截然不同的生意

### Asale：算法做市加 8% 抽成，成交价已跌到标价的 5%

Asale 自称"真人闲置订阅额度的 Token 市场"，并强调 "Asale runs no models"，每个请求都由某个真实用户的上游账号来服务 ([Asale 首页](https://asale.ai/en))。运营主体自称"懒人书科技（Lazy Book Technology）"，给出了一个 D-U-N-S 编号，项目从 2026 年 7 月开始 ([关于 Asale](https://asale.ai/zh/docs/about))。服务条款里没有写法律实体全称、注册地和适用法律，注册只需验证邮箱，没有 KYC ([Asale 条款](https://asale.ai/en/docs/terms))。GitHub 组织创建于 2026-07-24，三个多月发了约 100 个版本，主仓库截至调研日有 21 star，只有两名贡献者 ([GitHub API](https://api.github.com/repos/asale-ai/asale))。提交记录里有 QQ 邮箱 ([commits](https://api.github.com/repos/asale-ai/asale/commits))，前端把中文放在第一语言位，两点都指向一个中国小团队。另一个 GitHub 组织 dlazy-ai fork 了 Asale，并用相近的文案推广，"懒人书"和"dLazy"可能是关联方 ([dlazy-ai/asale](https://github.com/dlazy-ai/asale))，不过这只是间接证据。

它的商业模式是**算法做市加交易抽成**。买家不能选卖家，由撮合器按价格（权重 0.45）、信誉（0.25）、往返延迟（0.15）和成功率（0.15）打分派单，卖家信誉至少要有 600 分才会被考虑 ([How it works](https://asale.ai/en/docs/how-it-works))。卖家不能报价，只能设一个"价格地板"。市场价等于官方标价乘以一个比例，文档写这个比例在 10%–100% 之间，每分钟按"上一分钟调用量 ÷（在线通道数 × 10）"算出的利用率向目标值移动 30%。**平台从每笔成交请求里抽 8%**，没有月费、上架费或最低消费 ([定价文档](https://asale.ai/en/docs/pricing))。资金只走 USDT（TRON 和 Solana），另有一条信用卡充值通道，由 Dodo Payments 作为 merchant of record，费率 5% + 0.4 USDT，只能充值不能提现。文档坦白说**资金不隔离，也没有储备证明**，"你的余额只是平台账本里的一行" ([资金与保障](https://asale.ai/en/docs/funds))。卖出收入先记为待结算，24 小时后才能花用；买家可以在 72 小时内对已结算的任务发起争议 ([钱包与结算](https://asale.ai/en/docs/wallet))。几处文档互相矛盾，比如"提现没有人工审核"和条款里的"付款前进行风险审核"。这是一个迭代很快的早期项目的典型特征。

它的用户画像很清楚。卖家主要是用不满额度的 Claude Max、ChatGPT Pro 订阅者，官方建议 Max 20× 用户"把上限设在 50–70% 然后忘掉它"。Pro 用户被建议"夜里卖、白天买"。没有订阅的人只做买家 ([套餐与容量](https://asale.ai/en/docs/plans))。实际成交规模很小。11 月 6 日的快照显示，过去 24 小时**成交 329.76 USDT、15,247 次任务，覆盖 14 个地区**，按 11,240 个已结算轮次加权，买家平均只付了**标价的 7%** ([模型市场](https://asale.ai/en/market))。**中国买家贡献了 50.8% 的消费额**（167.65 USDT），日本和美国各约 9.9% ([地区分布](https://asale.ai/en/distribution))。同一天，claude-opus-5-5 的挂牌价是输出每百万 token 1.00 USDT、输入 0.20 USDT，相当于标价的 5%，**比文档写明的 10% 下限还低**，文档没有解释这一点。用单日快照粗略外推：月成交额约 1 万 USDT，平台月收入约 800 USDT；卖家合计月收入约 9,100 USDT，只够付四十几份 $200 档订阅的月费。平均每次任务约 0.02 USDT，说明交易以零碎的 agent 调用为主。市场页上"可用容量 56,100M"这类整齐的大数，来自 Asale 对各套餐 5 小时窗口的估算，官方原话是"Asale 从不问厂商还剩多少额度，没有一家会告诉你，我们只能估算" ([套餐与容量](https://asale.ai/en/docs/plans))。所以"供给远大于需求"是**申报口径**下的结论，不等于真实可用的余量。

### ClawPool：每月 10 美元的 Claude 拼池，死于凑不齐卖家

ClawPool 于 2026-02-11 在 Hacker News 发布，发帖人用的是匿名账号，帖子拿到 21 分、7 条评论 ([Show HN](https://news.ycombinator.com/item?id=46974109))。它的条款把自己定义为双边市场："通过提供方贡献的 OAuth token 把请求代理到 LLM 厂商 API……ClawPool 不用自己的凭据调用厂商 API" ([ClawPool 条款 §1](https://clawpool.ai/terms))。它只做 Claude。创始人的说法是："要真正用好 Claude Code 就得买 $200 的 Max，而大部分额度都闲着。所以 ClawPool 让订阅者把 OAuth token 放进池子，每月最多赚 $120，其他人每月 $8 就能用上 Opus、Sonnet 和全部模型" ([Show HN](https://news.ycombinator.com/item?id=46974109))。

它的收入模式是**买方包月、卖方按量计酬，平台赚中间的差价**。条款里的消费者套餐是每月 $10、400 万 token，额度用完可以再付 $10 提前续期，折合**每百万 token 约 $2.50**（输入输出混合计）。提供方按"每百万 token 固定费率"拿钱，通过 Stripe Connect 即时结算，可以把出租上限设在 session 用量的 10%–95% 之间 ([ClawPool 条款 §3–4](https://clawpool.ai/terms))。提供方费率只登在已经下线的页面上，所以平台的实际抽成算不出来。条款把合规责任推给提供方，要求他们声明"贡献 token 不违反适用的服务条款" ([条款 §4.1](https://clawpool.ai/terms))。HN 评论区当时就警告"ToS 明确禁止转售，卖家有永久封号风险，买家可能一夜之间失去访问" ([Show HN](https://news.ycombinator.com/item?id=46974109))。

ClawPool 失败在供给侧，这个教训有参考价值。买方价格固定，卖方又按量计酬，一旦提供方撤出，平台没有价格杠杆能把供给拉回来。停服公告没有提到任何厂商行动。但它所在的 2026 年上半年，正好是 Anthropic 一步步禁止第三方工具使用订阅额度的时期（见最后一节），提供方因为担心封号而退出，是很合理的推断。

### LetsSplit：只做撮合，不碰钱，也不碰凭据

LetsSplit 是个人作品集级别的项目。代码仓库创建于 2026-06-06，6 周里 33 次提交，0 star ([GitHub](https://github.com/sudhanshu-b/SubSplit))；域名注册于 2026-06-14 ([WHOIS](https://who.is/whois/letssplit.in))。技术栈是标准的 CRUD 组合：Next.js、Supabase Postgres、Drizzle ORM、Better Auth，部署在 Vercel ([About](https://www.letssplit.in/about))。功能只有挂单、加入申请、聊天、付款凭证记录、评价和管理员封禁，没有代理端点，没有本地客户端，没有凭据保险库，也不做用量计量。商业上，它测试期免费、不抽成、不经手资金，"不处理、不持有、不担保任何付款"，退款和交付纠纷都由用户和车主自己解决 ([LetsSplit FAQ](https://www.letssplit.in/faq))。唯一的信任机制是一个贝叶斯信誉分：先验 3.5，相当于 5 条虚拟评价的权重，再叠加验证和付款记录的调整 ([trust-score.ts](https://github.com/sudhanshu-b/SubSplit/blob/main/lib/trust-score.ts))。首页上的"247 listings available"是写死在代码里的文字，不是实时数据 ([landing-page.tsx](https://github.com/sudhanshu-b/SubSplit/blob/main/components/landing-page.tsx))。目标用户是想省订阅费的印度学生、开发者和家庭。首页有一条推荐语说"Claude Code 的订阅费越来越多"，这能侧面说明印度开发者想要更便宜的 Claude。但 ChatGPT Plus/Pro 和 Claude Pro/Max 都没有家庭套餐，在 LetsSplit 上"拼"这些 AI 订阅，只能靠共用登录，而这直接违反 Anthropic 消费者条款里"不得让他人使用你的账户"的规定 ([Anthropic 消费者条款](https://www.anthropic.com/legal/consumer-terms))。

| 维度 | Asale | ClawPool | LetsSplit | AILE（参照） | Token Share（参照） |
|---|---|---|---|---|---|
| 交易标的 | 多家厂商的订阅额度，加部分 API key | Claude Max 登录令牌 | 订阅席位 | 订阅、API key、机器 | Claude Code 周额度 |
| 买方计价 | 按 token，算法浮动（实测为标价的 5%–10%） | 包月 $10/400 万 token（约 $2.5/M） | 车主定价，按席位分摊 | 卖家自定价 | 按 session 浮动（示例 $0.14） |
| 平台收入 | 每笔成交抽 8% | 买方订阅费与卖方费率的差价（未公开） | 无 | 10%（nodeless 15%） | 向卖方收 5% |
| 结算通道 | USDT（TRON/Solana），另可信用卡充值 | Stripe / Stripe Connect | 站外 UPI | USDC（Solana）、$AILE、信用卡 | Stripe Connect 或 USDC（Base） |
| 资金托管 | 平台账本，不隔离，无储备证明 | Stripe | 无 | 未详述 | 每单托管 |
| 卖方凭据存放位置 | 卖家本机 | 平台服务器 | 不经过平台 | 未公开 | 卖家本机 |
| 主要用户 | 撞到限额的 CLI agent 用户，中国买家过半 | 英语区 Claude Code 用户 | 印度学生、开发者、家庭 | 偏加密原生的开发者（推断） | Claude Code 重度用户 |
| 状态 | 运营中 | 已关停 | 测试期 | 运营中 | 运营中，只有 macOS 版 |

## "key 不离开本机"的实现：把卖家电脑变成主动出站接单的执行节点

### 中心托管和本地节点是两条对立路线

这个赛道的技术路线可以分成两种。第一种是**中心网关托管凭据**，中转站生态的主流开源网关 new-api（one-api 的衍生版）就是典型。管理员把上游 key 作为"渠道"录入，网关对外提供 OpenAI、Anthropic、Gemini 兼容的端点，按优先级和权重路由，失败重试，并按上游 usage 记账 ([new-api](https://github.com/QuantumNous/new-api))。LiteLLM 也是同一思路，它按 deployment 做负载均衡，对 429 自动冷却，每个请求写一行消费记录 ([LiteLLM 路由文档](https://docs.litellm.ai/docs/routing))。ClawPool 属于这一类：提供方把登录令牌交给平台，平台的代理函数代为调用 ([ClawPool 条款 §1](https://clawpool.ai/terms))。

JaynShare 是这一类的自托管版本，做得更深。它的服务器部署在 Tailscale 这样的私网里，**集中保存所有成员的 OAuth token 和 API key**，客户端拿不到凭据。成员通过 JaynShare 启动 Claude Code 时，流量被导向池子的转发代理，客户端信任池子自己的私有 CA。服务器作为"受信中间人"终止发往 Anthropic 的 TLS，读取请求体来挑选账号，注入该账号的凭据，并把请求缓存下来，以便换账号重试 ([JaynShare 安全与隐私文档](https://github.com/jaynlabs/jaynshare/blob/main/docs/security-and-privacy.md))。选号依据是厂商响应里返回的 5 小时和每周用量，用到 98% 就不再接新会话，已绑定的会话保持粘性，以保住 prompt cache ([quota.rs](https://github.com/jaynlabs/jaynshare/blob/main/src/pool/quota.rs)；[selection.rs](https://github.com/jaynlabs/jaynshare/blob/main/src/pool/selection.rs))。文档直说服务器运营者能读到全部 prompt、代码和回复，还能篡改请求和响应，"在保持这种路由设计的前提下，没有任何技术手段能让一个不可信的运营者变得安全" ([JaynShare 安全与隐私文档](https://github.com/jaynlabs/jaynshare/blob/main/docs/security-and-privacy.md))。它还内置了一些让多人共用更难被厂商识别的设计，从合规角度看，这本身就是风险信号。

第二种是**卖家本地节点**，也就是"key 不离开本机"的路线，Asale、AweShare 和 Token Share 都走这条路。这种设计在志愿算力网络里有成熟先例。AI Horde 的 worker "从不监听端口，总是主动向 API 拉活"，失败任务超时后会被重新分配 ([Horde SDK 文档](https://horde-sdk.readthedocs.io/en/latest/haidra-assets/docs/workers))。Cloudflare Tunnel 的 cloudflared 也只建立出站连接，源站不需要公网 IP ([Cloudflare Tunnel](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/))。核心思路是：卖家节点主动向中心协调器建立一条持久的出站连接（WebSocket 或长轮询），在上面注册能力、发送心跳、接收任务，所以不需要公网 IP，也不需要做端口映射。上游凭据只存在节点的内存和系统钥匙串里。

### Asale 的四跳链路与组件

Asale 是公开文档最完整的本地节点实现。官方把一次请求拆成四跳 ([How it works](https://asale.ai/en/docs/how-it-works))：

```
买家的 Claude Code / Codex / Gemini CLI
   │ ① 工具配置里的 base_url 被改为指向本机代理（127.0.0.1:9787），原配置逐字节备份
   ▼
买家本机代理 ── ② 只把鉴权头换成 Asale key，请求体不动 ──► 中心网关 gw.asale.ai
                                                         │ ③ 定价 → 从买家余额预冻结 → 撮合器选出 lane
                                                         │    下发 Ed25519 签名任务 {task_id | model | budget | expiry}
                                                         ▼
                                  卖家桌面客户端（通过出站 WebSocket 长连接常驻在线，每分钟重新申报可用量）
                                                         │ ④ 本地验签、按预算截断；在本机注入卖家自己的订阅凭据
                                                         ▼
                                              厂商 API（请求从卖家自己的网络出口发出）
   ◄──────── SSE 流原路返回：厂商 → 卖家客户端 → 网关 → 买家 ────────
   网关解析 usage → 单个数据库事务完成结算（买家扣款 / 卖家入账 92% / 平台 8%）→ 卖家收入 24 小时后可用
```

卖家客户端开源（Apache-2.0），用 Rust 写成，桌面外壳是 Tauri 2，前端是 React。代码按职责分层。`protocol/` 定义 wsrelay 线协议，服务端和客户端共用这一份定义。`core/ws.rs` 负责签名握手、供给申报、心跳和任务派发。`core/executor.rs` 注入本地凭据、回传数据流、解析 usage、执行预算上限。`core/security.rs` 管理设备的 Ed25519 身份。本地 SQLite 只保存钥匙串里条目的引用，不存凭据本身 ([DEVELOPMENT.md](https://github.com/asale-ai/asale/blob/main/docs/DEVELOPMENT.md))。网关公钥在编译时写进客户端，没有它客户端无法验证网关的派单授权，"卖出状态会永远卡在'正在上线'"。网关、撮合和结算的服务端代码都闭源，规格文档放在不公开的主仓库里 ([DEVELOPMENT.md](https://github.com/asale-ai/asale/blob/main/docs/DEVELOPMENT.md))。卖家在客户端里通过厂商的浏览器授权或设备码授权接入订阅，凭据"在调用的那一刻注入上游请求，从不离开设备，既不给买家，也不给 Asale" ([卖出文档](https://asale.ai/en/docs/sell-tokens))。

买家有两种接入方式。一种是在客户端里打开"买入"开关，工具配置被一键改写，指向本地代理。另一种是直接用 `sk-asale-` 开头的 key 调用网关，网关同时提供 OpenAI、Anthropic、Gemini 三种兼容协议，每个 key 有独立的 RPM/TPM 上限和可选预算 ([API 概览](https://asale.ai/en/docs/api/overview))。网关会在三种协议之间转换。thinking 签名只有在请求和服务卖家用的是同一种协议时才会返回，跨协议时宁可不给签名，也不伪造 ([Anthropic 兼容 API](https://asale.ai/en/docs/api/anthropic))。容错规则如下。首字节发出之前出错，网关静默换下一个候选，最多再重试 2 次。流开始之后就没法重放了，错误直接透传给买家，这次请求不计费。同一会话会在 10 分钟内粘在同一个卖家身上，以保住 prompt cache ([How it works](https://asale.ai/en/docs/how-it-works))。上游返回 429 时，整个账号冷却到上游给出的重置时间，失败的请求转给另一台设备 ([上游账号风险](https://asale.ai/en/docs/upstream-risk))。

AweShare 的结构类似，但更轻。生产者在本机运行 agent，主动向 hub 的 `/ws/v1/producer` 建立 WSS 反向隧道。hub 用原生 node:http、ws 和 SQLite 实现，对消费者提供 `/v1/chat/completions` 和 `/v1/messages`，"透明转发 SSE 字节流，并用 tee 尽力从流里提取 token 数"，同时设有 120 秒首包超时和 300 秒流空闲超时 ([AweShare CHANGELOG](https://github.com/Wehuman01/aweshare/blob/main/docs/CHANGELOG.md))。它不做协议转换。消费者按 `namespace/alias` 调用，hub 查表路由。生产者可以设并发上限、每个北京日的 token 上限（默认 100 万），以及共享时段，比如只在 00:00–06:00 共享。目前只支持单个 hub 实例，不能水平扩展 ([AweShare README](https://github.com/Wehuman01/aweshare/blob/main/README.md))。Token Share 多做了一层：在买家和卖家之间用端到端加密的 WebSocket，发生争议时依据中继日志判定，而 prompt 不进日志 ([tokenshare.app](https://tokenshare.app/))。

### 平台必须"看得见"用量，计量、结算和验真才成立

本地节点模式最难的部分在结算，不在转发。只有卖家能直接看到厂商的响应，卖家就有动机虚报用量，或者用便宜模型冒充贵模型。Asale 的对策是让网关始终能解析数据流。计费以上游返回的 usage 为准，同时网关自己独立估算一份（拉丁文字约 4 个字符算 1 个 token，中日韩文字约 1.5 个字符算 1 个 token），偏差超过 25% 就扣卖家信誉。输出量不能超过签名任务里的预算。prompt 量的上限是网关估算值的 3 倍，或估算值加 1000 token，取较大者。结算在单个数据库事务里写三条复式分录，用唯一的结算键防止重复扣费 ([计量与结算](https://asale.ai/en/docs/metering))。预冻结金额按 max_tokens 计算，结算时退还多冻结的部分 ([定价文档](https://asale.ai/en/docs/pricing))。派单签名覆盖 task_id、模型、预算和过期时间，1 小时后失效。任务通道绑定到具体设备，买家 key 在服务端只存 SHA-256 哈希 ([安全文档](https://asale.ai/en/docs/security))。

验真用的是抽检加信誉。每个"设备、厂商、模型"组合是一条 lane。上架前，平台从买家入口试买大约十几次，评分达到 75 分才通过。上架后抽检 1% 的流量，处于观察状态的 lane 抽检 10%，题目由服务器按次生成。同一模型有 8 条以上 lane 时，平台会给它们做同侪画像，偏离超过 3σ 就深度复查。官方承认"无法证明对端运行的是哪个二进制" ([验真文档](https://asale.ai/en/docs/verification))。验真工具已经开源为 llm-verify ([llm-verify](https://github.com/asale-ai/llm-verify))。这套做法本质上和 Hyperbolic 的 Proof of Sampling 一样，都是"随机抽样加惩罚"，只要被抽中的概率够高，诚实就是占优策略 ([PoSP 论文](https://arxiv.org/html/2405.00295v2))。闭源厂商 API 没法用 TOPLOC 那种基于模型激活值重算的验证，因为重算需要模型权重 ([TOPLOC 论文](https://arxiv.org/html/2501.16007v2))。所以在这个场景里，抽检和信誉几乎是唯一的手段。AweShare 的做法更简单：记录上游自报的模型名，和生产者声明的模型对比，不一致的 alias 由运营者封禁 ([AweShare CHANGELOG](https://github.com/Wehuman01/aweshare/blob/main/docs/CHANGELOG.md))。

这里有一个结构性矛盾：**隐私和计量完整性互相拉扯**。如果像 Token Share 宣称的那样，在买家和卖家之间做端到端加密，平台就看不到明文流，只能相信卖家上报的用量或中继日志。如果像 Asale 那样让网关解析数据流，平台就能看到 prompt。Asale 的选择是"不存储请求体，只在内存里中转，日志里不记录请求和响应内容" ([安全文档](https://asale.ai/en/docs/security))。

### key 不离机保护的是卖家，买家的 prompt 反而暴露给更多人

| 角色 | 卖家凭据 | 买家 prompt 和回复 | token 用量 |
|---|---|---|---|
| 中心中转站运营者（new-api 类中转站、ClawPool、JaynShare 服务器） | 能看到 | 能看到明文 | 能看到 |
| 本地节点模式的平台（Asale 网关、AweShare hub） | 看不到，前提是客户端诚实 | 能看到，除非买家到节点之间加密 | 能看到；加密后只能相信节点上报 |
| 卖家节点 | 自己的凭据 | **始终能看到明文** | 能看到 |
| 上游厂商 | 能看到 | 能看到 | 能看到 |
| TEE 网关运营者（如 Phala） | 看不到，密封在 enclave 里 | 看不到，前提是经过证明的代码可信 | 通过签名收据获得 |

这张表说明，"key 不离开本机"只换了**凭据**放在哪里，**请求内容**放在哪里并没有变。Asale 的安全文档原话是："你的请求由另一个用户的客户端中转，它会重建请求并发往上游，在那一刻文本对那台机器可见。这不是端到端加密"。SECURITY.md 把这一点归为"已知、设计如此" ([安全文档](https://asale.ai/en/docs/security)；[SECURITY.md](https://github.com/asale-ai/asale/blob/main/SECURITY.md))。对买家来说，这比中心中转站更糟。用中转站，买家只需要信任一个可识别的运营者；用本地节点模式，买家要信任这个运营者，再加上一群匿名卖家。端到端加密最远也只能加到卖家节点，因为卖家必须解密后才能用普通 TLS 调用厂商，而卖家恰恰是整条链路上最不可信的一方。更要紧的是**完整性**。agent 会执行模型返回的工具调用，能篡改响应的中间人就能注入命令。JaynShare 文档把这一点写得很明白。一项对 400 多家中转站的研究发现，**9 家在主动注入恶意代码，1 家掏空了研究者的 ETH 钱包，造成约 50 万美元损失** ([中国新闻周刊/新浪](https://news.sina.cn/gn/2026-06-22/detail-iniefqsa0438001.d.html?vt=4))。Asale 的验真机制检查的是"模型对不对"，不检查"每条响应有没有被改过"。协议层面也阻止不了恶意卖家修改开源客户端来记录 prompt。

即使对卖家，"key 不离机"也只是软件层面的承诺。只有当客户端开源、并且能做可复现构建时，卖家才能自己核实。Asale 客户端是开源的，但自动更新机制是每 10 分钟拉一次网站上的版本清单，然后重新执行网站下发的安装脚本，没有签名的增量更新器 ([DEVELOPMENT.md](https://github.com/asale-ai/asale/blob/main/docs/DEVELOPMENT.md))。所以信任最终还是落在官方的发布渠道上。AweShare 的生产者 agent 采用专有许可，源码不公开，生产者更没办法核实。能同时解决"运营者拿不到凭据"和"运营者看不到 prompt"的，目前是可信执行环境（TEE）方案。AWS Nitro Enclaves 可以设定只有 enclave 度量值匹配时，KMS 才放行解密 ([AWS Nitro Enclaves](https://docs.aws.amazon.com/enclaves/latest/user/nitro-enclave.html))。Phala 的证明网关给每个响应附一张收据，把请求和响应的哈希绑定到经过证明的工作负载上 ([Phala 文档](https://docs.phala.com/confidential-ai-inference/confidential-ai-api))。但这次调研没有找到任何额度共享平台在节点上用 TEE。而且 TEE 只解决运营者信任问题，出口仍然是同一个订阅凭据，厂商条款问题一点没变。

## 需求真实，但每一条供给都违反厂商条款

### 两类买家：被地域挡在门外的中国开发者，和撞上限额的重度用户

需求明显分成两块。第一块是**被地域、实名和支付门槛挡住的中国开发者和企业**。OpenAI 从 2024-07-09 起屏蔽中国大陆、香港和澳门的 API 流量 ([The Register](https://www.theregister.com/2024/06/25/openai_unsupported_countries/))，Anthropic 从 2025-09-05 起禁止中资控股超过 50% 的实体使用其服务 ([The Decoder](https://the-decoder.com/anthropic-bans-companies-majority-controlled-by-china-russia-iran-and-north-korea-from-claude/))。《中国新闻周刊》的报道显示，中转站广告宣称的价格低到官方的 0.07 倍，"日抛号"更低到 0.03 倍。一位从业者说，低于官方价十分之一的，"基本走的就是黑灰产路线" ([中国新闻周刊/新浪](https://news.sina.cn/gn/2026-06-22/detail-iniefqsa0438001.d.html?vt=4))。Asale 消费额中 50.8% 来自中国，正是这块需求的反映。第二块是**全球范围内撞上 5 小时和每周限额的重度 agent 用户**。Anthropic 的企业数据显示，Claude Code 按 API 标价折算，平均每个开发者每个活跃日花约 $13，每月约 $150–250，90% 的用户每个活跃日低于 $30 ([Claude Code 成本文档](https://code.claude.com/docs/en/costs))。2025 年 Anthropic 推出周限额时估计，受影响的订阅者不到 5% ([TechCrunch](https://techcrunch.com/2025/07/28/anthropic-unveils-new-rate-limits-to-curb-claude-code-power-users/))。反过来说，大多数订阅者每周都有额度没用完就过期了。这就是"闲置 token"的来源，不过没有任何厂商公布利用率分布。

这块生意的利润来自订阅价和 API 价之间的差价。Anthropic 自己举过例子，有用户在 $200 的套餐上消耗了价值"数万美元"的模型用量 ([Tom's Guide](https://www.tomsguide.com/ai/anthropic-is-putting-a-limit-on-a-claude-ai-feature-because-people-are-using-it-24-7))。麻烦在于，厂商现在都自己卖超额额度了。Anthropic 卖 usage credits，OpenAI 卖 Codex credits ([ChatGPT/Codex 定价](https://learn.chatgpt.com/docs/pricing))，Google 卖 AI credits ([Google 博客](https://blog.google/products-and-platforms/products/google-one/google-ai-subscriptions))，价格大致都按 API 计。共享平台能给买家的价值只剩一条：比厂商的超额价更便宜。而这正是厂商明令禁止的那种套利。

### 厂商条款和执法在 2026 年持续收紧

Anthropic 的条款写得最明确。消费者条款规定："你不得与任何人共享账户登录信息、API key 或账户凭据，也不得让任何其他人使用你的账户" ([Anthropic 消费者条款](https://www.anthropic.com/legal/consumer-terms))。Claude Code 的法律合规页更进一步，直接点到平台运营者："Anthropic 不允许第三方开发者……代表其用户通过 Free、Pro、Max 或 Ultra 套餐凭据路由请求。此外，开发者不得收集、存储或中转 Claude.ai 的凭据或会话令牌"，Anthropic 还保留"不事先通知即采取措施"的权利 ([Claude Code 法律与合规](https://code.claude.com/docs/en/legal-and-compliance))。照字面理解，这一条同时覆盖了中心托管和本地节点两种架构。key 放在哪里只影响凭据由谁保管，改变不了"一个人的订阅在服务陌生第三方"这个事实。

执法时间线是一路收紧的。2025 年 7 月，Anthropic 加上周限额，理由之一就是有人"共享账户和转售访问" ([TechCrunch](https://techcrunch.com/2025/07/28/anthropic-unveils-new-rate-limits-to-curb-claude-code-power-users/))。2026 年 1 月，它封堵了冒充 Claude Code 的第三方客户端。2 月，文档明确写出"在任何其他产品、工具或服务中使用 Free/Pro/Max 的 OAuth token"违反消费者条款，OpenCode 随后以"Anthropic 法律要求"为由移除了对 Claude 订阅的支持 ([The Register](https://www.theregister.com/software/2026/02/20/anthropic-clarifies-ban-on-third-party-tool-access-to-claude/5014546))。4 月 4 日起，订阅额度不再覆盖 OpenClaw 等第三方工具，用户只能买额外用量或改用 API ([TechCrunch](https://techcrunch.com/2026/04/04/anthropic-says-claude-code-subscribers-will-need-to-pay-extra-for-openclaw-support/))。同月开始对部分用户做证件加自拍的身份验证 ([KuCoin 转载的 Anthropic 帮助文本](https://www.kucoin.com/news/flash/claude-enforces-strict-real-name-verification-users-face-higher-account-risk))。现在的 Ultra 套餐写明"已验证账户无 5 小时限制" ([Claude 定价页](https://claude.com/pricing))，把实名和额度直接挂钩。Google 在 2026 年 2 月对通过 OpenClaw 使用 Antigravity OAuth 的 AI Ultra 付费用户做了一轮无预警封禁 ([OECD AI 事件库](https://oecd.ai/en/incidents/2026-02-23-ce89))。OpenAI 对"在第三方工具里自用"最宽容，但据二手报道，Codex 负责人说过，用 sub2api 把订阅转成 API 再分给多人用，会被反欺诈系统标记 ([X 转述](https://x.com/0xLogicrw/status/2090701984412373157))；Sign in with ChatGPT 的条款据称也明确禁止"汇集、转让、转售、赠送或共享套餐用量" ([OpenAI 条款，仅见搜索摘要](https://openai.com/policies/sign-in-with-chatgpt-terms/))。社区里有人报告，一个 Max 20x 账号出租后约 1 小时、消耗约 $50 就被封了 ([80aj 转载 Linux.do](https://www.80aj.com/2026/08/06/anthropic-clude-account-ban/))。这只是个案，没有官方确认。

平台们自己也不否认违约。Asale 的文档写道："这不是'灰色但没人管'，而是明确违反条款，只是各厂商执行力度不同……我们不会把它描述为合规"。它还提到 Anthropic "已经堵上了让订阅额度服务第三方应用的路径"，并表示不赔偿被封的订阅，"这不是保险产品" ([Asale 上游账号风险](https://asale.ai/en/docs/upstream-risk))。JaynShare 的 README 写着共用 Claude 订阅"可能导致所有相关账户被暂停或终止" ([JaynShare README](https://github.com/jaynlabs/jaynshare/blob/main/README.md))。AILE、TokenlyPro 和 ClawPool 的做法一样，都靠条款要求卖家保证"自己有权转售"，把封号后果推给卖家 ([AILE 条款](https://aile.sh/legal/terms))。这种安排里，一个卖家账号被封，会同时影响一批买家，而买家充进去的余额由一个运营主体不透明、只收 USDT 的平台保管。

### 在中国运营还要叠加行政和刑事风险

面向中国公众的共享平台，会被当作生成式 AI 服务的提供者。2026 年 9 月，网信办公布的典型案例里有一家江苏公司，运营"API 中转站"但没有做安全评估，被责令整改，负责人被约谈警告。律师的解读是，中转站本身不必然违法，但责任跟着实际向公众提供服务的那一方走，改名换包装都没用 ([iCourt](https://www.icourt.cc/prac-document/161924.html))。律师们普遍认为风险最高的是"订阅转 API"、逆向协议、绕过签名或风控，这些行为可能构成刑法第 285 条第 3 款规定的"提供侵入、非法控制计算机信息系统程序、工具罪"，入罪门槛是 20 次以上或违法所得 5,000 元 ([卓建律所](https://www.lawzj.cn/news_view.aspx?TypeId=5&Id=1846&Fid=t2%3A5%3A2))。上海一名中转站运营者自述被刑事拘留 37 天，取保候审后无法退还用户的充值 ([中国新闻周刊/新浪](https://news.sina.cn/gn/2026-06-22/detail-iniefqsa0438001.d.html?vt=4))。国家安全部在 2026 年 6 月公开提示中转站存在数据泄露、以次充好和后门窃取账号等风险 ([The Star](https://www.thestar.com.my/tech/tech-news/2026/06/10/leaks-and-backdoors-china-warns-of-security-risks-in-relay-services-for-foreign-ai-models))。还有律师直言"Claude、GPT 在中国大陆没有合法代理渠道" ([中国新闻周刊/新浪](https://news.sina.cn/gn/2026-06-22/detail-iniefqsa0438001.d.html?vt=4))。Asale 只用 USDT 结算、注册只需邮箱，确实降低了中国用户的使用门槛，但也带来了加密资产结算和反洗钱方面的风险，而且它的条款里没有写适用法律。

## 结论

这次调研把用户的问题拆开后，两种失败模式最值得注意。ClawPool 买方价格固定，死于**供给不足**：卖家撤出后，平台没有价格手段能把他们拉回来。Asale 靠算法定价，却把成交价压到了标价的 5%，申报的"可用容量"远大于需求。两者合起来说明，**闲置订阅额度在纸面上很多，实际上很脆弱**。卖家能随时挂出额度，也会在封号风险出现时一起撤走，而愿意掏钱的边际买家对价格极度敏感，主要是被挡在门外的中国开发者。用户设想的"先存后借"互助账本至今没有可用的产品，TokenBank 只有候补名单，JaynShare 和 AweShare 只在路线图里提过。它的优点是不涉及现金：积分不能兑现，就消除了虚报用量的动机，也避开了托管和资金转移的义务。但它解决不了条款问题。一旦积分能在陌生人之间流通，它在法律上就更接近"转售"。

对打算做这类产品的团队来说，"key 不离开本机"已经不是差异点，Asale、AweShare 和 Token Share 都实现了。真正的空白在**买家一侧**：怎么证明每条响应确实来自真实的上游模型、没有被节点篡改，怎么让 prompt 不暴露给匿名卖家。目前没有平台在节点上用 TEE 或可验证收据。更根本的判断是，只要供给来自个人消费订阅，任何架构都改变不了违约性质，而厂商正在把这部分需求收回自己手里：超额 credits、Sign in with ChatGPT、Team 共享额度，以及和实名挂钩的额度。合规的"共享"只剩三条路：组织内部的额度池或 BYOK（每个用户自带 key），厂商明确授权的转售，以及共享开源模型的算力（AI Horde 的 kudos 模式）。任何面向陌生人的订阅额度市场，都应该预期会在厂商的下一轮收紧中失去供给。ClawPool 已经是先例。
