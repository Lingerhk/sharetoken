# Landscape of other AI quota / token sharing, pooling, resale and relay platforms (excluding JaynShare, AweShare, Asale, ClawPool, LetsSplit)

Research date: 2026-11-06. All facts below carry the source URL they came from. Most sources are vendor marketing, forum posts, or secondary blogs. They are flagged where that matters. "Unverified" means I could not confirm the claim against a primary source in this session.

---

## Q1. What platforms exist? Categorized landscape map (breadth + depth on the most relevant)

### Takeaway
The space has seven distinct categories. The newest one, and the closest to the "share leftover quota, borrow later" idea, is P2P idle-quota marketplaces: **Token Share** (tokenshare.app, a local client relay with a 5% seller fee), **Token出租** (token-share.app/zh, a CN shared compute pool), and **自足** (recruiting on V2EX). The largest category by count is the Chinese 中转站 (relay station) ecosystem. Trackers list roughly 980+ relay sites, with about 100 to 480 of them serving Claude. Open-source pooling software (claude-relay-service, 12.8k stars; sub2api; CLIProxyAPI) is the common substrate under both 拼车 (carpool) and 中转站.

### Cited Findings

#### A. Master table (24 entries)

| # | Name | URL | Category | What is shared | Mechanism | Pricing / take rate | Target users | Traction signal | Status (as of date) | Sources |
|---|---|---|---|---|---|---|---|---|---|---|
| 1 | **Token Share** (Token Share, Inc.) | https://tokenshare.app/ | P2P idle-quota marketplace (global) | Seller's unused weekly Claude Code allowance (Pro/Max) | **Local client.** Seller runs a macOS app (v0.6.2) that relays a paired buyer's Claude Code requests over an E2E-encrypted WebSocket. Requests count against the seller's own plan. "Credentials never leave your machine." Failover to another seller in the same tier in ~2s. | Flat **5% fee on the seller side**. Buyers pay no fee. Illustrative price $0.14/session vs $0.42 for Anthropic Extra usage ("3× cheaper"). Seller example: Max plan, 200 sessions/wk × $0.18 = $36 gross, $34.20 net. Payouts via Stripe Connect or USDC on Base. Escrow on every order. Disputes within 24h. | Claude Code users who hit weekly limits (buyers); users with idle allowance (sellers) | None published. Launch date not stated. | Active (macOS live; Windows/Linux "coming soon/beta", which the site contradicts) | [tokenshare.app](https://tokenshare.app/) |
| 2 | **Token出租** | https://token-share.app/zh | P2P / pooled idle-quota (CN) | Idle ChatGPT / Claude / Grok subscription quota | Suppliers "接入闲置账号" into a "共享算力池". Buyers call an OpenAI-compatible API. Technical method not described (likely credential/OAuth upload. Inference, unverified). | Buyer price ~**5–15% of official API**. GPT 5.6 Sol and Claude Opus 5 / Sonnet 5 at ~1/10 of official. Supplier revenue share stated but no % given. | CN developers; idle-subscription holders | None found | Promoted Oct 10, 2026 | [yunzhuanmi.com article (2026-10-10)](https://www.yunzhuanmi.com/token-chu-zu-diao-gong-xiang/) |
| 3 | **自足** | No URL in post | P2P idle-quota pool (CN) | Idle Claude subscription quota ("优先招募 claude 提供者") | Not stated | Not stated. Pitch is "reduce waste, recoup part of subscription cost". | Overseas holders of Claude subscriptions | V2EX promo post, 714 views, 0 replies | Recruiting (post dated Oct 9, year not shown. Likely 2026 by thread ID, an inference) | [V2EX t/1247430](https://www.v2ex.com/t/1247430) |
| 4 | **claude-relay-service (CRS)** → **CRS 2.0 / sub2api** | https://github.com/Wei-Shaw/claude-relay-service ; site pincc.ai | Open-source 拼车 / relay software | Pools multiple Claude, OpenAI/Codex, Gemini, Droid subscription accounts behind one endpoint. Per-user API keys and cost tracking. | **Self-hosted relay with OAuth credential pool.** Operator logs in subscription accounts; users get relay API keys. | Free (MIT). README frames it as splitting a Max subscription among friends ("拼车共享"). | Small groups (3–5 friends); also used by commercial 中转站 operators | **12.8k stars, 1.9k forks, 1,937 commits** | Active. Critical admin auth bypass in ≤v1.1.248; README tells users to migrate to sub2api. README warns it "可能违反Anthropic的服务条款". | [GitHub README](https://github.com/Wei-Shaw/claude-relay-service) |
| 5 | **sub2api** | (CRS 2.0) | Open-source subscription→API distribution gateway | Codex/ChatGPT, Claude subscription quota | Multi-account pool, API-key distribution, billing, multi-user sharing | Free software | Small teams doing "订阅共享或分发" | OpenAI's Codex lead said many users hit by "quota shrink" were using sub2api (secondary report) | Active, but flagged by OpenAI anti-fraud (see Q2) | [jiangmingtao.com guide](https://www.jiangmingtao.com/articles/10691-sub2api-open-source-guide); [gitcode comparison](https://gitcode.csdn.net/69d9cf0b54b52172bc68bcd3.html); [X post](https://x.com/0xLogicrw/status/2090701984412373157) |
| 6 | **CLIProxyAPI** | https://github.com/router-for-me/CLIProxyAPI | Open-source personal subscription→API proxy | Wraps Antigravity, ChatGPT Codex, Claude Code, Grok Build, etc. as OpenAI/Gemini/Claude-compatible APIs. Supports multi-account round-robin for Codex. | Local proxy using the user's own OAuth logins | Free | Individuals | Reportedly once recommended publicly by OpenAI Codex lead Tibo Sottiaux for putting GPT-5.6 into Claude Code (Jul 2026) | Active | [GitHub](https://github.com/router-for-me/CLIProxyAPI); [Sina 2026-07-13](https://finance.sina.com.cn/wm/2026-07-13/doc-inihsuhp7471824.shtml); [X post](https://x.com/0xLogicrw/status/2090701984412373157) |
| 7 | **Claude Code Max 拼车 card services** (example: easyclaude.com) | https://easyclaude.com/post/claude-code-max-pinche | Commercial 拼车 (CN) | Seats on a shared Max 20x account | **Relay API key, not shared login** ("不是共享 Session，而是每个用户分到独立 API Key"). Uses ANTHROPIC_BASE_URL / ANTHROPIC_AUTH_TOKEN. | 小月卡 ¥499/mo (5 seats, $20/day), 大月卡 ¥949 (3 seats, $45/day), 超级卡 ¥1,149 (2 seats, $55/day), 独享卡 ¥2,199 (exclusive). Official Max 20x is $200 (~¥1,450). | CN Claude Code users | Vendor content only | Active (Jan 19, 2026 article) | [easyclaude.com (2026-01-19)](https://easyclaude.com/post/claude-code-max-pinche) |
| 8 | **Xianyu (闲鱼) Max 拼车** (informal) | — | Informal C2C 拼车 | Seats on a Max account | Shared account / relay | Common quotes **¥350–450/month** per Max 拼车 seat | CN individuals | — | Ongoing. Source notes the account isn't yours, chat history sits with the seller, and many riders trigger risk control. | [Google Sites guide (vendor-written)](https://sites.google.com/view/guoneiaidingyuequangonglue/claude-pro-%E4%BB%A3%E5%85%85%E6%80%8E%E4%B9%88%E9%80%89%E4%BB%B7%E6%A0%BC%E8%B4%A6%E5%8F%B7%E5%BD%92%E5%B1%9E%E5%92%8C%E5%B0%81%E5%8F%B7%E9%A3%8E%E9%99%A9%E4%B8%80%E6%AC%A1%E8%AF%B4%E6%B8%85) (via search summary) |
| 9 | **PackyCode / PackyAPI** | https://www.packyapi.com | Commercial 中转站 (CN) | Claude (and others) via API | Pay-as-you-go relay (按量计费) | Sonnet 4.6 "basic" ¥2.4/M tokens; Opus 4.7 ¥12/M (official ¥15 / ¥75). $1 sign-up credit. Min top-up ¥50. | CN Claude Code users | 97.8% success in a 100-request test (May 2026); 98.5% uptime on aiapirank (Nov 2026). Recommended for large top-ups after "about a year of community discussion". | Active. Basic tier "略有降质" (slight quality degradation). | [codingtoken.org (2026-05-22)](https://codingtoken.org/blog/relay-station-review-2026/); [aiapirank (2026-11-06)](https://aiapirank.github.io/vendor/claude-code/); [aitshirtdesign (2026-08)](https://aitshirtdesign.com/claudecode/duckcoding); [apizdm](https://www.apizdm.com/coding-relay-head-to-head-comparison-20260612/) |
| 10 | **RightCode** | https://www.right.codes | Commercial 中转站 | Claude via API | Relay | Sonnet 4.6 ¥4.3/M vs official ¥15. Opus 4.7 ¥9.5/M vs ¥75. Min top-up ¥1. | CN devs | 99.2% success (100 requests, May 2026) | Active (May 2026) | [codingtoken.org](https://codingtoken.org/blog/relay-station-review-2026/) |
| 11 | **Poixe AI** | https://poixe.com | Commercial 中转站 | Claude via API | Relay | Sonnet 4.6 ¥10.5 (VIP ¥8.4). Opus 4.7 ¥28 (VIP ¥22.4). "几乎不掺水". | CN devs | 99.8% success (May 2026) | Active | [codingtoken.org](https://codingtoken.org/blog/relay-station-review-2026/) |
| 12 | **NekoCode** | https://nekocode.ai | Commercial 中转站 | Claude from a "MAX号池" (pool of Max subscription accounts) | Subscription-pool relay | Sonnet 4.6 ¥4.5, Opus 4.7 ¥9 per M tokens | CN devs | 98.5% success (May 2026) | Active | [codingtoken.org](https://codingtoken.org/blog/relay-station-review-2026/) |
| 13 | **DuckCoding** | https://www.duckcoding.ai (old: duckcoding.com) | Commercial 中转站 | Claude, Codex, Gemini, Grok, DeepSeek. 21 groups, ~167 models. | Relay. The "Claude Code专用" group is a **self-built $200 Max subscription pool**. Other groups use official keys or AWS. | ¥1 = $1 credit. Discount vs official ≈ multiplier ÷ 7.1. Multipliers from 0.1× to 5.3× (官key 5.3×, AWS 3.6×, Max pool 1.5×). Min top-up ¥10. Refunds pro-rated minus 5%. | CN devs (despite a "no mainland China" notice) | AWS group ~34–35% uptime vs 100% on official-key groups (Aug 2026) | **Active, "预警" (warning) rating.** Not a rug-pull; the domain moved. | [aitshirtdesign.com (updated 2026-08-31)](https://aitshirtdesign.com/claudecode/duckcoding) |
| 14 | **88code** | (not verified) | Commercial 中转站 | Claude Code | Relay | — | CN devs | — | **"半跑路" (semi-rug-pull) per linux.do users.** Refund group turmoil ("2.9晚上退款群"). Users kicked; refunds pending. Some channels alive but "比市价贵几倍". Possible split from linux.do community. User claims, unverified. | [linux.do t/1584743](https://linux.do/t/topic/1584743); [linux.do t/1644506](https://linux.do/t/topic/1644506) |
| 15 | **AnyRouter** | (not verified) | 公益站 / free-credit relay | Claude Code credits | Relay | Free/sign-up credits (characterization from community; pricing unverified) | CN devs (linux.do community) | Multiple ban threads | Users report accounts banned (403s, "2个号前后被封禁"). Cause unclear (random, concurrency, or redistribution). | [linux.do t/1737718](https://linux.do/t/topic/1737718); [linux.do t/1555848](https://linux.do/t/topic/1555848) |
| 16 | **DawCode, 9527code, ToTokens, 88API, 云渡, 灵算, OmniaKey, 8sToken, SevnX, 吉吉不降智AI, Apiporter, etc.** | via https://aiapirank.github.io/vendor/claude-code/ | Long tail of 中转站 | Claude Code API | Relay | DawCode: Sonnet ¥4.5, Opus ¥7.5 "基本版" | CN devs | Uptime ranges from 60.9% (Apiporter) to 100% (云渡, 灵算, ToolCode) | Active as of 2026-11-06 | [aiapirank (2026-11-06)](https://aiapirank.github.io/vendor/claude-code/); [codingtoken.org](https://codingtoken.org/blog/relay-station-review-2026/) |
| 17 | **Relay-station rankings / directories** (aiapirank, 中转站排行 zhongzhuanzhanrank, ai-coding-welfare, relay-pulse) | https://aiapirank.github.io/ ; https://zhongzhuanzhanrank.github.io/ ; https://github.com/panxunying/ai-coding-welfare ; https://github.com/prehisle/relay-pulse | Meta-layer (discovery/monitoring) | — | Probes uptime and latency; lists free "公益站" | Free (often affiliate-linked) | Buyers of relay access | aiapirank: 100 Claude vendors filtered from **986** relay sites, synced twice daily. zhongzhuanzhanrank: **479** Claude relays compared. | Active Nov 2026 | [aiapirank](https://aiapirank.github.io/vendor/claude-code/); [zhongzhuanzhanrank](https://zhongzhuanzhanrank.github.io/claude-zhongzhuanzhan/); [ai-coding-welfare](https://github.com/panxunying/ai-coding-welfare) |
| 18 | **Claude/ChatGPT 代充 (top-up) shops** (aichongzhi.org, 买号管家, claudemax.shop, V2EX sellers) | https://aichongzhi.org/claude/ ; https://maihaoguanjia.com/blog/claude-daichong/ ; https://claudemax.shop/ | Top-up / account services (CN) | Pays for **the buyer's own** account. Some sell finished accounts (成品号). | Card-key (卡密) or **cookie submission** (claudemax.shop asks for the claude.ai cookie) | Pro ¥178–249; Max 5x ¥880–1,760; Max 20x ¥1,680–2,980 (official $20/$100/$200). V2EX seller: Plus ¥130, Pro 5x ¥680. | CN users without foreign cards | Many GitHub "guide" repos acting as SEO (e.g., claude-daichong) | Active | [search summary of aichongzhi / maihaoguanjia / claudemax](https://claudemax.shop/blog/claude-price-2026-10); [V2EX t/1247562](https://www.v2ex.com/t/1247562); [GitHub claude-daichong](https://github.com/haiwaihuyonghui-rgb/claude-daichong) |
| 19 | **GamsGo** | https://www.gamsgo.com | Subscription-splitting / shared-seat marketplace (global) | Shared ChatGPT Plus seats; Claude group-buys reportedly split Team plans | **Shared login/seat**, plus a "ChatGPT recharge" service for your own account | Shared Plus ~**$5–7/mo** (€5.51 FR; ~$5.77 KR). Own-account Plus recharge $17.99/mo. Claude shared seat est. $6–10; Team split $8–12/person. | Price-sensitive consumers (EU, KR, SEA) | Multilingual site (fr/ko/vi/tr/nl) | Active. Prices come from competitor/affiliate blogs. | [perspectiveai.xyz (competitor)](https://perspectiveai.xyz/gamsgo-alternative-2026/); [gamsgo.com/ko](https://www.gamsgo.com/ko/details/chatgpt-recharge/partner/3jFHsk); [moneyroan.com](https://moneyroan.com/chatgpt-plus-cheap-2026/); [alucare.fr](https://www.alucare.fr/en/payer-chatgpt-moins-cher/) |
| 20 | **Sharesub / Spliiit** (EU) | https://www.sharesub.com ; spliiit.com | Subscription-splitting marketplace | Gemini, Quillbot, etc. **Not ChatGPT or Claude.** | Co-subscription | Sharesub claims up to 70% savings | EU consumers | — | Sharesub: ChatGPT/Claude "noch nicht zum Teilen freigegeben". No evidence Spliiit lists them. | [Sharesub AI category](https://www.sharesub.com/de/abonnements/kuenstliche-intelligenz); [Test-Achats on Spliiit](https://www.test-achats.be/hightech/internet/news/spliiit-partage-abonnement) |
| 21 | **AI Credits** (aicredits.co); also AICreditMart | https://www.aicredits.co/en | API credit resale marketplace | Unused OpenAI, Anthropic, Gemini, AWS, Azure, GCP **API/startup credits** | Broker. Seller sends balance screenshots and optional read-only access; marketplace handles KYC, tax, invoicing; **credentials transferred**. 24–48h. | Sellers recover **40–70% of face value** (OpenAI/Anthropic 50–70%; Gemini/AWS/Azure/GCP 40–60%). Buyer UI shows a 40–80% off slider. Fees not stated. | Startups with expiring credits; cost-sensitive teams | Claims "200+ verified buyers", "$20M+ traded" (marketing, unverified) | Active (Mar 2026) | [getaiperks.com (2026-03-19, affiliate)](https://www.getaiperks.com/en/ai/sell-ai-credits); [explainx.ai](https://explainx.ai/blog/ai-credit-resale-market-cheap-claude-gpt-tokens-safety-2026) |
| 22 | **CheapCredits, Tokvana, Neokens** | (not verified) | Bulk-discount API routers (gray market) | Resold multi-vendor API access | Proxy/router | CheapCredits: flat **40% off** list. Offers a GDPR Art. 28 DPA that the source says conflicts with its proxy architecture. | Developers, startups | — | Active (Aug–Oct 2026) | [explainx.ai](https://explainx.ai/blog/ai-credit-resale-market-cheap-claude-gpt-tokens-safety-2026) |
| 23 | **Account marketplaces (Z2U, PlayerUp) and Telegram stolen-session markets** | https://www.z2u.com/claude-ai/accounts-5-25307 ; https://www.playerup.com/accounts/claudeaiaccount/ | Account resale / black market | Claude Pro / ChatGPT accounts; **stolen session tokens** for Claude, ChatGPT, Gemini, Cursor (Okta report) | Account handover; session-cookie hijack (bypasses password and MFA) | n/a | — | Gizmodo: "black market just for stolen ChatGPT and Claude logins… open 24/7" | Active | [Z2U](https://www.z2u.com/claude-ai/accounts-5-25307); [PlayerUp](https://www.playerup.com/accounts/claudeaiaccount/); [Gizmodo](https://gizmodo.com/theres-a-new-black-market-just-for-stolen-chatgpt-and-claude-logins-its-open-24-7-2000809840) (page returned 403; details from search snippet only) |
| 24 | **Company leftover-quota resale posts** (Juejin, linux.do) | https://juejin.cn/post/7650532520375713834 | Ad-hoc B2B/C2C resale | A company's idle Claude quota after a project was shelved | Unknown | Unknown | CN devs | linux.do thread: "闲置的Claude订阅可以用来干啥呢" | Ongoing | [Juejin](https://juejin.cn/post/7650532520375713834); [linux.do t/3052475](https://linux.do/t/topic/3052475) |

#### B. Contrast group: legitimate or adjacent offerings often confused with "sharing"
- **Domestic Coding Plans (cheap overage substitutes):** GLM Coding Plan Lite is $18/mo (5h quota 2,000 credits; weekly 10,000), Pro $80, Max $168. Kimi Code is bundled in Kimi membership (from $19). Alibaba Coding Plan Pro is $50/mo (6,000 requests per 5h). MiniMax M Plan is $220/yr. Z.ai and MiniMax list Claude Code, Codex, and OpenCode as supported clients. Data as of Oct 2026, from a secondary comparison. One report says the GLM plan price doubled ("护照税" controversy). — [morphllm (Oct 2026)](https://www.morphllm.com/coding-plan); [remio.ai](https://www.remio.ai/post/the-glm-coding-plan-went-viral-in-north-america-then-the-price-doubled)
- **Anthropic's own "sharing" features:** Anthropic Extra usage is priced at ~$0.42/session in Token Share's illustration ([tokenshare.app](https://tokenshare.app/)). As of Oct 16, 2026, Max reportedly bundles $100 (5x) or $200 (20x) monthly API credits, and Team plans get up to $500/mo in pooled credits (secondary, unverified against Anthropic's page) ([explainx.ai](https://explainx.ai/blog/ai-credit-resale-market-cheap-claude-gpt-tokens-safety-2026)). Anthropic's enterprise **Claude Marketplace** lets customers redirect unused commitments to third-party software; it is in limited preview and does not resell consumer quota ([TechRadar](https://www.techradar.com/pro/anthropics-claude-marketplace-allows-customers-to-buy-third-party-cloud-services)).
- **OpenRouter / SiliconFlow (legit aggregators):** These resell official API capacity under provider agreements, with no subscription pooling. I did not re-verify their 2026 fee schedules in this session. See Gaps.

#### C. Decentralized / crypto "compute sharing" networks
These networks share **GPU compute for open-weight models**, not GPT/Claude subscription quota:
- **FAR Labs / FAR AI:** launched Feb 2026. Pools idle gaming-PC and workstation GPUs for inference; node operators are rewarded by usage. — [HackerNoon](https://hackernoon.com/lite/far-labs-launches-distributed-compute-network-for-ai-inference)
- **BTTInferGrid (BitTorrent):** announced June 17, 2026. Aggregates idle GPUs for inference. — [KuCoin news](https://www.kucoin.com/news/flash/bittorrent-launches-bttinfergrid-a-decentralized-ai-inference-computing-network)
- **Inference.net:** "decentralized, verified network of LLM inference providers". — [inference.net blog](https://inference.net/blog/arbitraging-down-llm-inference-to-the-cost-of-electricity)
- **Jatevo:** contributors earn tokens per request served. Waitlist only. Warns that operators can potentially view prompts. — [jatevo.ai](https://jatevo.ai/decentralized)
- **Mesh / Meshtrain:** open-source P2P GPU pooling that pays "MeshCoin". — [Crypto Briefing](https://cryptobriefing.com/mesh-idle-nvidia-gpus-ai-compute-network/)
- **Gonka AI:** decentralized inference and training. The source article is promotional. — [dev.to](https://dev.to/operationalneuralnetwork/the-10b-gpu-revolution-how-web3-is-breaking-ais-compute-monopoly-53pg)
- Chutes and Gaia did not show up in my searches. See Gaps.

#### D. Depth on the most relevant platforms (summary)
1. **Token Share (tokenshare.app):** the closest analog to the user's concept. The architecture is a **seller-side local relay** that never exports OAuth credentials, plus escrow, supply/demand-floating per-session pricing, a 5% seller fee, Stripe or USDC payouts, 24h disputes judged on relay logs (prompts not logged), and failover within ~2s. Weaknesses: no traction data, no named team, macOS only, and the page says users "are responsible for complying with the terms of the underlying services". — [tokenshare.app](https://tokenshare.app/)
2. **Token出租 (token-share.app/zh):** a CN pooled model. Suppliers "connect idle accounts" to a shared pool; buyers pay ~5–15% of official API price through an OpenAI-compatible API. It covers GPT, Claude, Gemini, and Grok. The domain resembles Token Share's, but I found no evidence that they are related. — [yunzhuanmi (2026-10-10)](https://www.yunzhuanmi.com/token-chu-zu-diao-gong-xiang/)
3. **CRS / sub2api / CLIProxyAPI:** the open-source substrate. CRS has 12.8k stars. Its successor sub2api adds multi-tenant key distribution and billing. This is the tooling OpenAI's anti-fraud now flags. — [GitHub CRS](https://github.com/Wei-Shaw/claude-relay-service); [X post](https://x.com/0xLogicrw/status/2090701984412373157)
4. **Max 拼车 card services:** sell a fixed daily USD quota per seat on a shared Max 20x account, delivered as relay API keys (¥499/5-seat to ¥2,199 exclusive). — [easyclaude](https://easyclaude.com/post/claude-code-max-pinche)
5. **Commercial 中转站 (PackyCode, RightCode, Poixe, NekoCode, DuckCoding, 88code):** price-per-million-token or multiplier pricing. Supply mixes official keys, AWS Bedrock, and **Max subscription pools ("MAX号池", "自建$200 Max订阅号池")**. — [codingtoken.org](https://codingtoken.org/blog/relay-station-review-2026/); [aitshirtdesign](https://aitshirtdesign.com/claudecode/duckcoding)
6. **AI Credits:** the clean, brokered version of API-credit resale, recovering 40–70% of face value. — [getaiperks](https://www.getaiperks.com/en/ai/sell-ai-credits)
7. **GamsGo:** the dominant global shared-seat reseller for ChatGPT Plus (~$5–7/mo). — [perspectiveai.xyz](https://perspectiveai.xyz/gamsgo-alternative-2026/)

### Inferences
- The user's concept (share leftovers, borrow later) maps most closely to Token Share's **local-client relay** design, not to credential-upload pools. The local-client model is the only one where the supplier never hands over OAuth tokens. It is still a ToS violation on Anthropic's side, because it routes other people's requests through a subscription (see Q2).
- Most CN "subscription pools" are commercial 中转站 that buy or farm Max accounts and resell capacity. They are not true mutual-aid pools where users are both suppliers and consumers. I found very few genuine two-sided (supply-and-borrow) platforms. Token Share, Token出租, and 自足 are the only ones, and none publishes traction.
- No "credit exchange" model (contribute quota now, earn non-cash credits redeemable later) turned up outside the five platforms excluded from this brief. That model may be a differentiator, or it may simply be covered by the excluded platforms.

### Gaps
- Token Share and Token出租: launch dates, user counts, GMV, team identity, and whether the two are related. Neither site publishes this.
- 自足: URL, mechanism, and pricing are unknown (one promo post only).
- Current pricing for AnyRouter and 88code, and whether 88code is fully shut down. Only linux.do user claims are available.
- OpenRouter and SiliconFlow 2026 fee schedules were not verified this session. From training knowledge (mid-2025, unverified now), OpenRouter charges a small fee on credit purchases and passes through provider prices.
- I found no reliable sources on Chutes (Bittensor) or Gaia in 2026. Spliiit's historical listing of ChatGPT is also unverified.
- I found no data on AWS Bedrock credit farming or free-trial farming as specific supply sources. DuckCoding's "Claude Code-AWS" group shows AWS-sourced supply exists, but not how it is obtained.

---

## Q2. Which platforms are most active as of late 2026, and which were shut down, banned, or hit by vendor enforcement?

### Takeaway
The 中转站 market is very large and still active in Nov 2026: 986 tracked relay sites, ~100 to 479 serving Claude. Enforcement has escalated on three fronts. (1) Anthropic blocked third-party OAuth use (Jan 2026) and codified it in terms (Feb 19–20, 2026), reportedly extended to all third-party harnesses on Apr 4, 2026. (2) From late June 2026, Anthropic ran a China-focused crackdown targeting relays, VPN users, and overseas shells (FT, Jul 3, 2026). (3) OpenAI's anti-fraud flags sub2api-style multi-user distribution of Codex subscriptions (~Aug 2026). Casualties are mostly at the individual-relay level (88code "semi-rug-pull", AnyRouter account bans). I found no report of a named large relay being taken down by a vendor.

### Cited Findings
**Activity signals (Nov 2026)**
- aiapirank filters 100 Claude Code vendors out of 986 relay sites, with data updated 2026-11-06 and synced twice daily. — [aiapirank](https://aiapirank.github.io/vendor/claude-code/)
- Another ranking compares "479 家" Claude relays. A separate list counts 97 of 983 relays supporting Claude Code. — [zhongzhuanzhanrank](https://zhongzhuanzhanrank.github.io/claude-zhongzhuanzhan/); [aiapirank (search snippet)](https://aiapirank.github.io/vendor/claude-code/)
- A "Claude Code 中转站推荐 (2026年11月)" guide screens relays on thinking-signature and tool-call pass rates. Some relays forward only chat endpoints and cannot run Claude Code. — [veridrop.app](https://veridrop.app/guide/claude-code-zhongzhuanzhan)
- CRS has 12.8k stars and 1.9k forks. — [GitHub](https://github.com/Wei-Shaw/claude-relay-service)

**Anthropic: third-party harness / OAuth enforcement**
- January 2026: Anthropic took steps to stop third-party tools "spoofing the Claude Code harness" and banned accounts that tried to game pricing. — [The Register, 2026-02-20](https://www.theregister.com/software/2026/02/20/anthropic-clarifies-ban-on-third-party-tool-access-to-claude/5014546)
- The HN thread "Anthropic blocks third-party use of Claude Code subscriptions" (Jan 2026). Secondary sources date the server-side block to Jan 9, 2026, affecting OpenCode, Cline, and RooCode. — [HN 46549823](https://news.ycombinator.com/item?id=46549823); [search summary of paddo.dev etc.](https://paddo.dev/blog/anthropic-walled-garden-crackdown/)
- Week of Feb 20, 2026: the terms now say "Using OAuth tokens obtained through Claude Free, Pro, or Max accounts in any other product, tool, or service" is "not permitted and constitutes a violation of the Consumer Terms of Service". Section 3.7 already barred helping "another person to access or use" the Services in prohibited ways. OpenCode removed Claude Pro/Max support citing "anthropic legal requests". Anthropic's Thariq Shihipar: "Third-party harnesses using Claude subscriptions… are prohibited by our Terms of Service." — [The Register](https://www.theregister.com/software/2026/02/20/anthropic-clarifies-ban-on-third-party-tool-access-to-claude/5014546)
- April 4, 2026: enforcement reportedly extended to all third-party harnesses (OpenClaw, NanoClaw), moving users to pay-as-you-go. Secondary source. — [natural20.com](https://natural20.com/coverage/anthropic-banned-openclaw-oauth-claude-code-third-party)
- October 2026: a blog claims Anthropic rewrote the auth section and removed the OAuth/Agent SDK line, while still barring third-party apps from routing requests through Free/Pro/Max plans. **Unverified, possibly conflicting.** — [autonomee.ai](https://autonomee.ai/blog/claude-code-terms-of-service-explained/)

**Anthropic: China-focused enforcement (directly hits 中转站)**
- Sept 2025: Anthropic publicly extended sales restrictions to overseas subsidiaries of Chinese-controlled companies. — [huxiu](https://m.huxiu.com/article/4872575.html)
- April 2026: Anthropic introduced Persona ID and face verification for some high-risk users. — [huxiu](https://m.huxiu.com/article/4872575.html)
- From late June 2026: Claude accounts of China users on VPNs, API relays, or overseas shells were banned without warning. FT (Jul 3, 2026) reported Anthropic targeting overseas subsidiaries, cloud resale, VPNs, and third-party "中转站", with Claude Code as a focus. — [Net.Coffee](https://ip.net.coffee/claude/news/20260703e.html); [huxiu](https://m.huxiu.com/article/4872575.html); [163.com](https://www.163.com/dy/article/L0TS78U60511D6RL.html)
- Claude Code reportedly contained a hidden user-identification mechanism (timezone, proxy). Anthropic reportedly called it an "experimental" anti-abuse measure to be removed. — [QQ News](https://news.qq.com/rain/a/20260703A085L800); [ifeng](https://news.ifeng.com/c/8uSaxT3YvKA)
- Alibaba reportedly banned Claude internally on Jul 3, 2026. ByteDance's Trae emailed users that Claude access was being removed. — [huxiu](https://m.huxiu.com/article/4872575.html); [Net.Coffee](https://ip.net.coffee/claude/news/20260703e.html)
- Net.Coffee names no specific relays as affected and reports no price or availability effect. — [Net.Coffee](https://ip.net.coffee/claude/news/20260703e.html)

**OpenAI enforcement**
- Codex lead Tibo Sottiaux said many users complaining of quota shrink were using sub2api. Turning a subscription into API traffic and distributing or sharing it among multiple people gets flagged by OpenAI's anti-fraud system. He had earlier endorsed CLIProxyAPI for personal use (Codex OAuth into Claude Code). Secondary report on X (~Aug 2026). — [X @0xLogicrw](https://x.com/0xLogicrw/status/2090701984412373157); [Sina 2026-07-13](https://finance.sina.com.cn/wm/2026-07-13/doc-inihsuhp7471824.shtml)
- OpenAI's Thibault Sottiaux publicly endorsed using Codex subscriptions in third-party harnesses, which contrasts with Anthropic's stance. — [The Register](https://www.theregister.com/software/2026/02/20/anthropic-clarifies-ban-on-third-party-tool-access-to-claude/5014546)

**Platform-level casualties and status**
- 88code: "半跑路" (semi-rug-pull). Refund-group turmoil, users kicked, refunds pending, some channels alive at "几倍" market price. — [linux.do t/1584743](https://linux.do/t/topic/1584743); [linux.do t/1644506](https://linux.do/t/topic/1644506)
- AnyRouter: user accounts banned (403s). Cause disputed. — [linux.do t/1737718](https://linux.do/t/topic/1737718); [linux.do t/1555848](https://linux.do/t/topic/1555848)
- DuckCoding: not a rug-pull as of Aug 2026; it changed domain. On 2026-06-13 it posted a "no mainland China service, no refunds for VPN circumvention" notice while still accepting WeChat and Alipay. — [aitshirtdesign](https://aitshirtdesign.com/claudecode/duckcoding)
- CRS ≤v1.1.248 had a critical admin authentication bypass. — [GitHub](https://github.com/Wei-Shaw/claude-relay-service)
- Sharesub does not offer ChatGPT or Claude sharing. — [Sharesub](https://www.sharesub.com/de/abonnements/kuenstliche-intelligenz)
- linux.do thread: "Claude Pro 存活18天" (a Pro account survived 18 days). — [linux.do t/3010787](https://linux.do/t/topic/3010787)

### Inferences
- Enforcement targets the **subscription-pool supply side**: OAuth use outside first-party clients, multi-user distribution patterns, and China-linked usage chains. Any token-sharing product that routes other people's requests through a consumer subscription is exposed. That includes local-client designs like Token Share, because Anthropic's terms bar helping another person access the Services and limit OAuth to Claude Code / Claude.ai.
- OpenAI appears more tolerant of personal third-party harness use but has drawn a line at multi-user distribution. Its tolerance for a P2P sharing product is likely low.
- Relays have adapted by diversifying supply (official keys at higher multipliers, AWS, Max pools) and pricing groups by risk. DuckCoding's 0.1×–5.3× group spread is an example.

### Gaps
- No primary Anthropic statement naming relay stations or quantifying bans was found. The China crackdown details come via Chinese media relaying FT.
- The April 4, 2026 extension and October 2026 terms rewrite are secondary claims. I did not check Anthropic's legal page directly.
- No reliable account exists of any named large relay being shut down by vendor action.

---

## Q3. What business models recur, and what are typical price points relative to official?

### Takeaway
Six recurring models:
1. **Spread on discounted quota (中转站):** ~20–60% of official per-token price for subscription-pool supply; ≥official for official-key supply.
2. **Seat/time-slice 拼车:** ¥350–1,149 per seat-month on a $200 Max 20x.
3. **Shared login seats:** ~25–35% of list (GamsGo $5–7 vs $20).
4. **P2P marketplace commission:** 5% seller fee at Token Share; buyers pay ~1/3 of Anthropic Extra usage.
5. **Credit resale brokerage:** 40–70% recovery for sellers; 30–98% off for buyers.
6. **代充 top-up:** a markup over official, not a discount.

Free "公益站" sign-up credits act as a customer-acquisition funnel.

### Cited Findings
- **Per-token relay pricing (May 2026, ¥ per M tokens):**
  - Sonnet 4.6: official ¥15. RightCode ¥4.3, NekoCode ¥4.5, DawCode ¥4.5, PackyAPI basic ¥2.4, Poixe ¥10.5 (VIP ¥8.4).
  - Opus 4.7: official ¥75. RightCode ¥9.5, NekoCode ¥9, DawCode ¥7.5, PackyAPI ¥12, Poixe ¥28.
  - The source mislabels RightCode's ¥4.3 as "95折" (it is ~29% of official).
  - — [codingtoken.org (2026-05-22)](https://codingtoken.org/blog/relay-station-review-2026/)
- **Multiplier pricing:** DuckCoding sells ¥1 = $1 credit. Discount vs official ≈ group multiplier ÷ 7.1. Max-pool group at 1.5× (≈21% of official). Official-key group at 5.3× (≈75%). AWS group at 3.6×. Grok Build at 0.1×. Refunds pro-rated minus 5%. — [aitshirtdesign](https://aitshirtdesign.com/claudecode/duckcoding)
- **Pooled idle quota:** Token出租 prices ~5–15% of official API, with Opus 5 / Sonnet 5 / GPT 5.6 Sol at ~1/10. — [yunzhuanmi](https://www.yunzhuanmi.com/token-chu-zu-diao-gong-xiang/)
- **拼车 seats:**
  - easyclaude Max 20x: ¥499 (5 seats, $20/day each), ¥949 (3 seats, $45/day), ¥1,149 (2 seats, $55/day), ¥2,199 (exclusive). Official is ~¥1,450. Prices under ~¥300 for the small tier are "多半超卖" (mostly oversold). — [easyclaude](https://easyclaude.com/post/claude-code-max-pinche)
  - Xianyu Max 拼车: ¥350–450/month. — [Google Sites guide](https://sites.google.com/view/guoneiaidingyuequangonglue/claude-pro-%E4%BB%A3%E5%85%85%E6%80%8E%E4%B9%88%E9%80%89%E4%BB%B7%E6%A0%BC%E8%B4%A6%E5%8F%B7%E5%BD%92%E5%B1%9E%E5%92%8C%E5%B0%81%E5%8F%B7%E9%A3%8E%E9%99%A9%E4%B8%80%E6%AC%A1%E8%AF%B4%E6%B8%85)
- **P2P commission:** Token Share takes 5% from sellers and nothing from buyers. Illustrative buyer price $0.14/session vs $0.42 Anthropic Extra usage. No listing or monthly fees. — [tokenshare.app](https://tokenshare.app/)
- **Shared seats:** GamsGo shared Plus ~$5–7/mo vs $20. Claude shared seat est. $6–10. — [perspectiveai.xyz](https://perspectiveai.xyz/gamsgo-alternative-2026/)
- **Credit resale:**
  - AI Credits sellers recover 40–70% of face value (OpenAI/Anthropic 50–70%). — [getaiperks](https://www.getaiperks.com/en/ai/sell-ai-credits)
  - Gray-market discounts are 30–98% off list. "Tens of millions" of dollars in credits circulate. ~10–20% of what one researcher saw was fraud-funded. CheapCredits is flat 40% off. — [explainx.ai, citing Matt Lenhard (Aug 2026)](https://explainx.ai/blog/ai-credit-resale-market-cheap-claude-gpt-tokens-safety-2026)
- **代充 top-ups:** Pro ¥178–249, Max 5x ¥880–1,760, Max 20x ¥1,680–2,980 (official $20 / $100 / $200). V2EX seller: Plus ¥130, Pro 5x ¥680. — [claudemax.shop](https://claudemax.shop/blog/claude-price-2026-10); [V2EX t/1247562](https://www.v2ex.com/t/1247562)
- **Funnel incentives:** PackyAPI gives $1 at sign-up. NekoCode gives ¥15 that counts as ¥30. DawCode gives ¥4 plus check-in credit. A GitHub list aggregates free 公益站 for Claude Code and Codex. — [codingtoken.org](https://codingtoken.org/blog/relay-station-review-2026/); [ai-coding-welfare](https://github.com/panxunying/ai-coding-welfare)
- **Arbitrage root:** the gap between flat subscriptions and metered API pricing makes resale profitable "with no fraud required". — [explainx.ai](https://explainx.ai/blog/ai-credit-resale-market-cheap-claude-gpt-tokens-safety-2026)

### Inferences
- The "x折" for subscription-pool Claude supply clusters around **2–3折** (20–30% of official API price). Official-key supply sells at **7–8折**. Pooled idle-quota P2P (Token出租) claims **~1折**. A new sharing product would have to land between P2P prices (~10%) and stable 中转站 prices (~20–30%).
- On a $200 Max 20x, a 5-seat 拼车 at ¥499 each nets the operator ~¥2,495/mo against ~¥1,450 cost, roughly a 70% gross margin before bans. This explains the oversupply of operators. (Calculation based on the easyclaude figures.)
- Credit resale (40–70% recovery) is the only model with a plausibly clean title transfer. It covers API credits only, not subscription quota.

### Gaps
- No public take-rate data for most 中转站 (cost of supply vs sale price) or for Token出租's supplier share.
- No reliable statistics on 拼车 market size or GMV.

---

## Q4. What do users complain about (instability, model substitution / 掺水, privacy, runaway billing, bans)?

### Takeaway
The recurring complaints:
- **Instability and uptime gaps:** 60.9% to 100% uptime across relays; one AWS group ran at 34–35%.
- **Quality degradation / model substitution:** "降质", "掺水", "降智". A Kimi distillation was sold as Claude.
- **Privacy and integrity:** relays terminate TLS, can log prompts and code, and can tamper with agent tool calls.
- **Rug-pulls and refund disputes:** 88code.
- **Account bans:** AnyRouter, China crackdown, OpenAI anti-fraud.
- **Overselling and fraud-funded supply:** stolen cards (10–20%), stolen session tokens.

I found little evidence of "runaway billing" complaints specifically.

### Cited Findings
- **Instability:**
  - Relays "经常断线或降质" (often drop connections or degrade quality). — [codingtoken.org](https://codingtoken.org/blog/relay-station-review-2026/)
  - Apiporter 60.9% uptime; SudoCode 86.5%. — [aiapirank](https://aiapirank.github.io/vendor/claude-code/)
  - DuckCoding's AWS group at ~34–35% uptime. Its status page read "Partially Degraded Service". — [aitshirtdesign](https://aitshirtdesign.com/claudecode/duckcoding)
- **Substitution / 降智:**
  - A resold "Claude" turned out to be a Kimi-based distillation. — [explainx.ai](https://explainx.ai/blog/ai-credit-resale-market-cheap-claude-gpt-tokens-safety-2026)
  - PackyAPI basic tier is "略有降质"; Poixe is described as "几乎不掺水". — [codingtoken.org](https://codingtoken.org/blog/relay-station-review-2026/)
  - A vendor named "吉吉不降智AI" appears on the ranking. — [aiapirank](https://aiapirank.github.io/vendor/claude-code/)
  - Rankings test thinking-signature and tool-call support because some relays break Claude Code. — [veridrop.app](https://veridrop.app/guide/claude-code-zhongzhuanzhan)
- **Privacy and security:**
  - Relays decrypt traffic and can log it. A malicious relay could alter bash commands or file writes. A broker was "probably picking from a pool of keys". — [explainx.ai](https://explainx.ai/blog/ai-credit-resale-market-cheap-claude-gpt-tokens-safety-2026)
  - FT-relayed coverage says relays risk leaking prompts, code, and R&D information. — [Net.Coffee](https://ip.net.coffee/claude/news/20260703e.html)
  - claudemax.shop asks buyers to submit their claude.ai cookie. — [claudemax.shop](https://claudemax.shop/blog/claude-price-2026-10)
  - Jatevo warns GPU operators can view prompts. — [jatevo.ai](https://jatevo.ai/decentralized)
  - Stolen AI session tokens are sold on Telegram and bypass MFA (Okta, via Gizmodo; snippet only). — [Gizmodo](https://gizmodo.com/theres-a-new-black-market-just-for-stolen-chatgpt-and-claude-logins-its-open-24-7-2000809840)
  - CRS admin authentication bypass. — [GitHub](https://github.com/Wei-Shaw/claude-relay-service)
- **Shared-account issues:** in 拼车, the account isn't yours, chat history sits with others, and many riders trigger risk control. — [Google Sites guide](https://sites.google.com/view/guoneiaidingyuequangonglue/claude-pro-%E4%BB%A3%E5%85%85%E6%80%8E%E4%B9%88%E9%80%89%E4%BB%B7%E6%A0%BC%E8%B4%A6%E5%8F%B7%E5%BD%92%E5%B1%9E%E5%92%8C%E5%B0%81%E5%8F%B7%E9%A3%8E%E9%99%A9%E4%B8%80%E6%AC%A1%E8%AF%B4%E6%B8%85)
  - Quotas are shared among members and you don't control the account (GamsGo-type seats). — [subpickly](https://subpickly.vercel.app/en/chatgpt/price); [alucare.fr](https://www.alucare.fr/en/payer-chatgpt-moins-cher/)
  - A linux.do user on sharing idle quota: "分享出去更是不敢，大概率被封号". — [linux.do t/3052475 (via search snippet)](https://linux.do/t/topic/3052475)
- **Rug-pull and refund:**
  - 88code refund group: users kicked, refunds unpaid. — [linux.do t/1584743](https://linux.do/t/topic/1584743)
  - DuckCoding's mainland-China clause leaves termination and no-refund decisions to the operator. Its testimonials appear self-published. It has no ICP filing or operator identity. — [aitshirtdesign](https://aitshirtdesign.com/claudecode/duckcoding)
- **Bans:**
  - AnyRouter 403s and bans. — [linux.do t/1737718](https://linux.do/t/topic/1737718)
  - China-wide no-warning bans since late June 2026. — [huxiu](https://m.huxiu.com/article/4872575.html)
  - OpenAI anti-fraud flags sub2api sharing ("额度缩水"). — [X post](https://x.com/0xLogicrw/status/2090701984412373157)
  - Accounts opened with stolen cards may be banned "随时封". — [easyclaude](https://easyclaude.com/post/claude-code-max-pinche)
- **Overselling and fraud:**
  - Small-tier 拼车 under ¥300 is "多半超卖". — [easyclaude](https://easyclaude.com/post/claude-code-max-pinche)
  - 10–20% of the gray market observed was fraud-funded. — [explainx.ai](https://explainx.ai/blog/ai-credit-resale-market-cheap-claude-gpt-tokens-safety-2026)

### Inferences
- Trust is the core unmet need. The heavy use of third-party uptime and "不降智" probes (aiapirank, relay-pulse, veridrop) shows buyers can't verify what model they get. A sharing product that provably routes to the genuine first-party model (Token Share's local-client design does this by construction) and offers escrow/refunds addresses the top three complaints.
- Supplier-side fear of bans is the main barrier to recruiting sharers. The linux.do sentiment and the low engagement on 自足's V2EX recruitment both point to this. Ban compensation or insurance may matter more than payout rate.

### Gaps
- No systematic complaint data (counts, surveys). Evidence is anecdotal forum threads and vendor or competitor blogs.
- No specific "runaway billing" incident tied to a relay or sharing platform was found.
- Reddit, Xiaohongshu, and Jike were not directly searched in this pass. Western-side user sentiment on Token Share is unknown.
