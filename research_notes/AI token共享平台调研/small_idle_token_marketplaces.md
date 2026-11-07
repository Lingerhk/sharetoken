# Small "sell your unused AI tokens / idle quota" platforms: AILE, QUOTA, idlicat (闲猫), TokenBank, TokenlyPro, AI Token Exchange, newtokenclub, plus a sweep for other 2026 launches

All pages were accessed on 2026-11-06 unless another date is given. This is a gap-filling pass that reads public pages only. Credential handling and detection or evasion details are out of scope and are not recorded. Figures that sites report about themselves are labelled as unverified.

---

## Q1. What each platform is: operator, launch, market, status (with comparison table and short profiles)

### Takeaway
AILE is the only one of the seven with clear signs of live two-sided activity: about 20.7K settlements in 30 days, USDC payouts and a 10% take rate. QUOTA's gateway is live but its marketplace hasn't launched, and its token-first stats are sample data. TokenBank, AI Token Exchange and SecondTokens are waitlist or private-beta pages. TokenlyPro has a working signup app but shows no traction. idlicat (闲猫) is a Chinese JS page that shows only a title, and newtokenclub is a 0-star GitHub fork with no live service. **TokenBank is the closest match to the user's concept** (deposit idle quota, earn non-cash credits, borrow later).

### Comparison table

| Platform | Market / operator | Launch / status (2026-11-06) | What is shared | Buyer access | Take rate | Payout | Traction (self-reported unless noted) | Acknowledges vendor ToS risk? |
|---|---|---|---|---|---|---|---|---|
| **AILE** (aile.sh) | Overseas, crypto-native. Anonymous, notes signed "Jason" | First note 25 Aug 2026. **Live** | "subscriptions, API keys, or machines" | OpenAI- and Anthropic-compatible API with one key. 482 text models | **10%** of settled usage (15% "nodeless", 0% via x402) | **USDC on Solana**, or opt into $AILE token. Card top-ups for buyers since 2 Nov 2026 | 20,682 settlements in 30 days. 1,142 offers. 152 sharers online. $AILE market cap ~$70K (third party) | **Yes, explicitly.** Sharers warrant they may resell. Provider actions against accounts are the sharer's problem |
| **QUOTA** (quotai.io) | Overseas, crypto-native. Anonymous | Week-numbered roadmap at "Week 3 of 4". Gateway live, marketplace "W5–8". **Pre-launch / early** | API credits, team plans with delegation rights, self-run compute. **Consumer plans excluded** | OpenAI-compatible endpoint. "Up to 60% below list" | Unquantified "cut". Half of margin goes to $QUOTA buyback and burn | **$QUOTA token only**, "not redeemable for cash". Suppliers stake a bond | Stats labelled "Preview · sample data". Stats JSON returns 404 | **Partly.** FAQ says consumer plans "forbid sharing access" |
| **idlicat / 闲猫·闲置Token交易市场** (idlicat.com) | China (Chinese title). Operator unknown | Domain answers HTTP 200 with a 1.6 KB JS shell. **Status unclear** | Unknown. An earlier search snippet mentioned "platform guarantee + escrow" (unverified) | Unknown | Unknown | Unknown | None found | Unknown |
| **TokenBank** (tokenbank.dev) | Overseas (English). Anonymous, hello@tokenbank.dev | "v0.1 · private beta", "est. 2026". **Waitlist** | **Subscription quota** (Claude Code, Claude.ai, agents with a usage API) | Borrow from pooled quota using credits | "Small spread" on credit spending | **Non-cash credits only** (mutual credit) | 3,914 on waitlist. 48.2M tokens pledged | **Weakly.** Will start "with the providers that allow this". No terms page |
| **TokenlyPro** (tokenlypro.com) | Overseas (English). Anonymous | Terms dated 2026-05-20. App signup live. **Live but no visible activity** | Seller "capacity" for Grok, OpenAI, Anthropic and Gemini, listed with a monthly token cap | Buyers "claim allocations". API docs available | "Small flat platform fee per million tokens routed" (amount not stated) | Withdraw above a payout minimum. Method not stated | Product Hunt "Featured" badge only | **Yes, on paper.** Sellers "confirm that you have the right to resell" and must not "evade provider terms of service" |
| **AI Token Exchange (ATE)** (aitokenexchange.net) | Overseas, likely Korea-linked (inference). Anonymous | "Early access" waitlist. **Pre-launch** | **Consumer subscription** web-app capacity, supplied through a seller Chrome extension | Prepaid credits used in ATE's own chat UI. From $1 | **5% seller fee** (sellers keep 95%) | Credited to ATE balance. Cash-out not described | None. Dashboard is a mockup | **No.** Terms and Privacy links are "#" placeholders |
| **newtokenclub/new-api** (GitHub) | China (Chinese README). Anonymous | Repo only, no website or releases. **Concept / code** | Idle ChatGPT Plus / Claude Pro / Gemini Advanced quota, with browser-extension nodes | Pay per task. New-API relay gateway | Not stated | "可随时提现" (withdraw any time). Method not stated | 0 stars, 0 forks | **No** |
| *New:* **SecondTokens (STX)** (secondtokens.vercel.app) | Overseas. Anonymous | "Private beta". Signup button still a placeholder. **Vaporware-stage** | "Unused subscription headroom" in fixed-size lots (OpenClaw, OpenCode, Codex, GPU worker) | Matched lots through a local agent | Not stated | Not stated | None | No |

### Platform profiles

**AILE (aile.sh).** AILE calls itself "the AI sharing ecosystem". Sharers list "subscriptions, API keys, or machines" on a market board. Callers get one key that "speaks the OpenAI and Anthropic APIs" and reach 482 text models, including Claude Sonnet 5.5 and Haiku 5.5, DeepSeek V4.1 Flash and OpenCode Go. An `auto/<model>` option lets AILE pick the provider. Sharers set their own prices, which are shown against list price. The earliest public note is dated 25 Aug 2026 ("What Aile is, and what it isn't yet"). The risk page says "UPDATED JUL 2026", so build work likely started earlier. A 2 Nov 2026 note added card payment for callers through Stripe. The business is crypto-flavoured. The terms set a 10% marketplace fee on settled usage (15% for "nodeless" serves, 0% for x402-paid requests). Sharers are paid in USDC on Solana and can opt to receive the pump.fun-launched $AILE token instead. Callers who pay with $AILE get 2% off. The site shows 20,682 settlements in the 30 days to 6 Nov 2026, 1,142 offers and 152 sharers online. AILE's own terms are the most explicit of the group about vendor risk: "Providers may prohibit key sharing or resale". Sharers must warrant that they are permitted to resell "under every agreement governing it", and "any action a provider takes against an account you connected" is the sharer's responsibility.

**QUOTA (quotai.io).** QUOTA is "The idle inference network" ("The cheapest tokens are the ones already paid for"). It offers an OpenAI-compatible API key for Claude, GPT, Gemini and open models, "priced below list" ("up to 60% below list"). Its preview prices Claude credit at $0.43 per $1. Supply is limited to "API credits, team plans with delegation rights, compute you run yourself". It explicitly excludes consumer plans because they "forbid sharing access". The product is mainly a token launch. The roadmap runs from gateway (W1–2) to points (W3, current) to the $QUOTA launch on StonkFun/Solana (W4) to an "AI Credit Marketplace" (W5–8) to compute futures. Suppliers stake $QUOTA as a bond, can be slashed, and are paid in $QUOTA. Credits are "not redeemable for cash". All the headline numbers (3,418 suppliers, 2.9M requests, ~$48K paid) are labelled "Preview · sample data", and `/api/stats.json` returned 404 when checked. The site lists no operator, terms or privacy pages.

**idlicat / 闲猫·闲置Token交易市场 (idlicat.com).** The domain resolves to a 1,649-byte single-page-app shell. Its only visible content is the title "闲猫·闲置Token交易市场" ("Xianmao: idle-token trading market"). An earlier search snippet, recorded in this project's Asale notes, said the site advertises platform guarantee and fund escrow (平台担保 / 资金托管). That claim could not be re-verified from the page source. Searches for "idlicat" and the Chinese name returned nothing. No operator, ICP filing, pricing or traction was found. The name copies 闲鱼 (Xianyu), the secondhand marketplace that a Jun 2026 Huxiu piece describes as a large gray market for AI accounts, API credits and Codex resets. This suggests the site is pitched as a C2C escrow marketplace (inference).

**TokenBank (tokenbank.dev).** TokenBank is "A marketplace for the Claude Code and AI agent quota you don't burn". Users (1) connect a subscription, (2) deposit unused quota up to a daily ceiling to earn credits, and (3) spend credits to borrow tokens when they run out. There is no cash payout, and "Credits don't expire while your account is active". The platform earns a "small spread" on credit spending. This is a mutual-credit time-shifting model, the closest public analog to the user's product concept. It is a "v0.1 · private beta" with a waitlist opening "in waves". It reports 3,914 people on the waitlist, 48.2M tokens pledged and a ~2.77M-token pool balance, all unverified. The pool list (Claude Sonnet 4.6, Opus 4.7, GPT-5, Gemini 2.5 Pro) uses model names from early-to-mid 2026. That suggests the page hasn't been updated recently (inference). There is no terms page and no security doc ("before launch"). The only ToS hedge is starting "with the providers that allow this".

**TokenlyPro (tokenlypro.com).** TokenlyPro is "The marketplace for AI tokens", and its pitch is to "Move unused capacity to teams that need more" (B2B framing). Sellers connect a provider (Grok, OpenAI, Anthropic, Gemini), "Set price per million tokens by model", and set a monthly token cap per listing. A listing closes once its cap is claimed. Buyers claim allocations and get API access, with docs at /api-docs and an app at app.tokenlypro.com. The fee is a "small flat platform fee per million tokens routed". Seller earnings can be withdrawn above an unstated minimum, and the payout method isn't stated. Its terms are dated 2026-05-20, the earliest dated document in this set. The terms put compliance on users: they must "comply with the acceptable-use policies of the underlying providers", they must not "evade provider terms of service", and sellers "confirm that you have the right to resell access to the associated credentials". No operator, usage numbers or reviews were found beyond a Product Hunt "Featured on" badge.

**AI Token Exchange / ATE (aitokenexchange.net).** ATE calls itself "The world's first P2P marketplace for AI subscription tokens". Buyers top up credits from $1 (estimated tiers: $1 ≈ 200K, $4 ≈ 1M, $14 ≈ 4M tokens) and chat with Claude Sonnet 4.6 and Haiku, GPT-4o and 4o mini, or Gemini Advanced inside ATE's own UI. Conversation context carries across models. Sellers install a Chrome extension and keep the vendor's consumer web app open, so supply comes from **consumer chat subscriptions**. The company keeps a **5% seller fee** ("keep 95% of every transaction"). Earnings go to an ATE balance, and withdrawal isn't described. The site is a waitlist ("early access"). Its "73% of subscribers waste unused AI tokens monthly" statistic has no source, and the Terms and Privacy footer links are "#" placeholders. The site refers to Korea and US time zones, and the English page lives at `/index_en`. Both suggest a Korean team (inference).

**newtokenclub (github.com/newtokenclub/new-api).** This is a Chinese README for an "AI 闲置算力 Token-P2P 交易平台" ("AI idle compute token P2P trading platform"), with the tagline "浏览器插件即节点 · AI Token 变现 · New-API 企业级中转网关". The README says holders of "ChatGPT Plus、Claude Pro、Gemini Advanced" can connect their "每月的闲置 Token 配额" (monthly idle token quota) to the market, set "单价和每日限额" (unit price and daily limit), and withdraw earnings "随时" (any time). Buyers pay "按次" (per task). Script authors get revenue share, and there is a double-entry ledger. It is a secondary development of the open-source New API gateway. The repo has 0 stars and 0 forks, and its 6,262 commits come from upstream. It has no releases and no website, and its clone link points to another account (`lakysir/new-api`). This is a concept repo, not a live service.

### Cited Findings
- AILE homepage: "the AI sharing ecosystem". Sharers offer subscriptions, API keys or machines. 482 text models. OpenAI and Anthropic API compatible. 20,682 settlements in the 30 days to 6 Nov 2026. USDC on Solana. — [aile.sh](https://aile.sh/)
- AILE notes, all by "Jason": 25 Aug 2026 "What Aile is, and what it isn't yet". 17 Sep 2026: three posts on how it works, getting started, and "Who can read your prompts on Aile?". 2 Nov 2026 "Pay for calls with a card" (Stripe). Snapshot: 1,142 offers, 152 sharers online. — [aile.sh/notes](https://aile.sh/notes)
- QUOTA: "The idle inference network". OpenAI-compatible base URL. AI Credit Marketplace and $QUOTA on StonkFun (Solana) listed as "Coming next". — [quotai.io/llms.txt](https://quotai.io/llms.txt); roadmap "Week 3 of 4" — [quotai.io](https://quotai.io/)
- idlicat page title "闲猫·闲置Token交易市场". 1,649-byte HTML shell, HTTP 200 (curl, 2026-11-06). — [idlicat.com](https://idlicat.com/). Earlier escrow claim (search snippet, unverified) — see this project's `asale.md` notes citing [idlicat.com](https://idlicat.com/)
- TokenBank: "A marketplace for the Claude Code and AI agent quota you don't burn". "v0.1 · private beta". "est. 2026". — [tokenbank.dev](https://tokenbank.dev/)
- TokenlyPro: "The marketplace for AI tokens". "Move unused capacity to teams that need more". Terms "Last updated · 2026-05-20". — [tokenlypro.com/sell](https://www.tokenlypro.com/sell); [tokenlypro.com/terms](https://www.tokenlypro.com/terms)
- ATE: "The world's first P2P marketplace for AI subscription tokens". Early access. Korea and US time zones mentioned. — [aitokenexchange.net/index_en](https://aitokenexchange.net/index_en)
- newtokenclub README: "AI 闲置算力 Token-P2P 交易平台". Built on New API. 0 stars, 0 forks. Clone command points to `lakysir/new-api`. — [github.com/newtokenclub/new-api](https://github.com/newtokenclub/new-api)
- Xianyu as an AI gray market (accounts, API credits, Codex resets), Jun 2026 — [Huxiu](https://m.huxiu.com/article/4870122.html)

### Inferences
- Of these, AILE is the only operating two-sided market. The others are pre-launch, waitlist or concept-stage. Several (QUOTA, ATE, SecondTokens) show template or mock signs: sample-data labels, placeholder links, and an unset `NEXT_PUBLIC_GOOGLE_FORM_URL`.
- Two of the seven (AILE, QUOTA) are built around a Solana memecoin-style token (pump.fun / StonkFun). That suggests part of this niche is a "crypto narrative" play, not a pure SaaS marketplace.
- idlicat and newtokenclub are the only China-facing entries, and neither shows a live, verifiable business.

### Gaps
- No legal entity, founder identity or jurisdiction is published for **any** of the seven. AILE's terms (updated Oct 2026) name no entity or governing law.
- The idlicat content can't be rendered without running its JS app. No operator, ICP filing, pricing or traction was found, and the escrow claim is unverified.
- Exact launch dates are unknown for all of them. The available proxies are AILE's first note (25 Aug 2026), TokenlyPro's terms date (20 May 2026) and QUOTA's week-numbered roadmap with no calendar anchor.

---

## Q2. What is sold or shared, and the public user flow (high level)

### Takeaway
Supply falls into three types. (a) **API keys and credits** (AILE, TokenlyPro, QUOTA, which excludes consumer plans). (b) **Consumer or coding subscription quota** (TokenBank, ATE, newtokenclub, SecondTokens, and AILE's "subscriptions"). (c) **Self-hosted compute** (AILE "machines", QUOTA, SecondTokens GPU worker). Buyers mostly get an OpenAI- or Anthropic-compatible endpoint. ATE is the exception: buyers chat in its own web UI.

### Cited Findings
- AILE buyers get a key, pick a model row, and change their base URL. Sharers list idle access on /share and are paid per call. Some listings carry a "Trains on prompts" label, and a dedicated note covers prompt visibility. — [aile.sh](https://aile.sh/); [aile.sh/notes](https://aile.sh/notes)
- QUOTA buyers get an API key ($5 free, no subscription) and change their base URL. Listed compatible tools include Claude Code, Cursor, Cline, Aider and the OpenAI SDK. Suppliers "connect capacity" and stake $QUOTA. The site says "requests reach the provider unchanged" and promises "Zero data retention". — [quotai.io](https://quotai.io/); [quotai.io/llms.txt](https://quotai.io/llms.txt)
- TokenBank flow: connect a subscription, deposit unused quota up to a daily ceiling, earn credits, borrow when you run out. Supported: Claude Code, Claude.ai and "any agent that exposes a usage API". — [tokenbank.dev](https://tokenbank.dev/)
- TokenlyPro seller flow: connect a provider, set price and cap, publish, and earn as buyers claim allocations. Buyers claim allocations from live listings. Listings that exceed the seller's underlying quota "will be paused". — [tokenlypro.com/sell](https://www.tokenlypro.com/sell); [tokenlypro.com/terms](https://www.tokenlypro.com/terms)
- ATE buyers top up, choose a model and chat. Sellers install a Chrome extension, keep the provider web app open, and register. — [aitokenexchange.net/index_en](https://aitokenexchange.net/index_en)
- newtokenclub: providers connect subscription idle quota with a price and daily limit, and the README presents browser extensions as nodes ("浏览器插件即节点"). Clients pay per task. — [github.com/newtokenclub/new-api](https://github.com/newtokenclub/new-api)
- SecondTokens: "Buy extra AI coding capacity when your limit hits. Sell it when yours sits idle." A local agent runs fixed-size lots "without sharing passwords" and returns results "with a full audit trail". — [secondtokens.vercel.app](https://secondtokens.vercel.app)

### Inferences
- The two that rely on consumer web or chat subscriptions through a browser extension (ATE, newtokenclub) carry the highest vendor-ToS exposure. QUOTA designed around that risk by excluding consumer plans.
- TokenBank's "deposit now, borrow later" flow is a direct precedent for the user's concept. Its lack of a terms page or security doc means the hard parts aren't public yet.

### Gaps
- Which vendors AILE's "subscriptions" listings actually cover (Claude Pro/Max? ChatGPT?) isn't stated on the pages read. I did not read /market or /docs.
- TokenlyPro's buyer experience (endpoint format, model list) wasn't read in detail (/api-docs not fetched).

---

## Q3. Business model: pricing vs official, take rate, payouts, mutual credit vs cash

### Takeaway
Take rates are public only for **AILE (10%, 15% for nodeless, 0% via x402)** and **ATE (5% seller fee)**. TokenlyPro charges a flat per-million-token fee, and TokenBank a "spread", but neither gives an amount. Payouts split into **crypto cash** (AILE USDC), **platform tokens** (QUOTA $QUOTA, not cash-redeemable), **internal credits** (TokenBank, ATE) and **unspecified withdrawals** (TokenlyPro, newtokenclub). Only QUOTA puts a number on discounts: "up to 60% below list", with Claude at $0.43 per $1.

### Cited Findings
- AILE: "The marketplace charges 10% of settled usage". Nodeless serves 15%. x402-paid requests 0%. Callers paying from $AILE get a price 2% below the sharer's. Settlement is USDC on Solana, and sharers "may elect to receive their earnings swapped to $AILE". Earnings can be withdrawn at any time. — [aile.sh/legal/terms](https://aile.sh/legal/terms) (updated Oct 2026)
- AILE prices are set per offer and shown against list price. — [aile.sh](https://aile.sh/)
- QUOTA: "up to 60% below list" ($1,200 becomes $480). Claude credit preview $0.43 per $1 (−57%). "The protocol takes a cut". "Half of protocol margin buys back and burns" $QUOTA. Credits "not redeemable for cash". — [quotai.io](https://quotai.io/)
- TokenBank: "small spread" on credit spending. No cash. Credits don't expire while the account is active. — [tokenbank.dev](https://tokenbank.dev/)
- TokenlyPro: "TokenlyPro deducts a small flat platform fee per million tokens routed". Earnings "payable once your balance crosses the payout minimum". — [tokenlypro.com/terms](https://www.tokenlypro.com/terms)
- ATE: 5% seller fee ("keep 95% of every transaction"). Buyer tiers $1, $4 and $14 (estimated, not final). Earnings credited to the ATE balance. — [aitokenexchange.net/index_en](https://aitokenexchange.net/index_en)
- newtokenclub: earnings "可随时提现". Example task cap of 0.05 USD. No fee schedule. — [github.com/newtokenclub/new-api](https://github.com/newtokenclub/new-api)
- SecondTokens: "Simulated rates" of $2.80–$4.10 per 250K tokens. — [secondtokens.vercel.app](https://secondtokens.vercel.app)

### Inferences
- AILE's 10% and ATE's 5% are a rough take-rate benchmark for this niche. They are lower than the 中转站 (relay) spreads documented in `landscape_other_platforms.md`.
- Only TokenBank and ATE run on internal credits with no stated cash-out. That avoids money-transmission and payout complexity, but it reduces seller incentive.

### Gaps
- Withdrawal minimums, KYC, and payout rails other than AILE's USDC are not disclosed anywhere.
- None of the sites state average realised prices compared with official prices, apart from QUOTA's preview figures.

---

## Q4. Traction signals and user complaints

### Takeaway
AILE is the only one with live usage numbers and a third-party market signal: a small token with roughly $70K market cap. Every other figure is a waitlist count, a projection or labelled sample data. **No user complaints, reviews, Reddit/V2EX/linux.do threads or press coverage were found for any of the seven.**

### Cited Findings
- AILE: 20,682 settlements in 30 days (to 6 Nov 2026). 1,142 offers. 152 sharers online. $AILE price $0.0000727 (6 Nov 2026). — [aile.sh](https://aile.sh/); [aile.sh/notes](https://aile.sh/notes)
- $AILE on MadeOnSol: market cap "$70K", liquidity "$21K", 24h volume "$3K", "First seen September 11, 2026". The page doesn't link to aile.sh, so the match is by ticker only. — [madeonsol.com](https://madeonsol.com/token/8d3bQS9vNy3BfqK9dX95JRoaTbWoiuL2hzmSFLDopump). A search snippet from the same page showed "$9K market cap / $24K liquidity", which conflicts. The snapshots likely differ.
- QUOTA: 3,418 suppliers, 41.7M tokens per minute, 2,904,117 requests and 1,208,440 $QUOTA (~$48K) paid, all labelled "Preview · sample data". Month-one "$48K+" payouts are a projection. `/api/stats.json` returned HTTP 404 on 2026-11-06. — [quotai.io](https://quotai.io/); [quotai.io/api/stats.json](https://quotai.io/api/stats.json)
- TokenBank: 3,914 on waitlist. 48.2M tokens pledged. ~2.77M-token pool balance. — [tokenbank.dev](https://tokenbank.dev/)
- TokenlyPro: Product Hunt "Featured on" badge and "All systems normal" status. No numbers. — [tokenlypro.com/sell](https://www.tokenlypro.com/sell)
- ATE: no user counts. Preview earnings are mockups. — [aitokenexchange.net/index_en](https://aitokenexchange.net/index_en)
- newtokenclub: 0 stars, 0 forks, 0 watchers. — [github.com/newtokenclub/new-api](https://github.com/newtokenclub/new-api)
- Searches for reviews of "TokenlyPro", "aile.sh", "quotai" and "idlicat" returned only unrelated crypto and AI-token content. This is a negative result from web searches run on 2026-11-06, so there is no source URL.

### Inferences
- About 20.7K settlements a month means AILE is real but small. If each settlement is one API call, that is roughly 700 calls a day across 152 online sharers. This is inference, because "settlement" may batch calls.
- The lack of any community discussion suggests all seven are still marginal compared with the platforms already covered (Asale, JaynShare, Token Share, and others).

### Gaps
- No independent check of any traction figure. No complaint data. I did not search Discord, Telegram or X, where the crypto-adjacent ones (AILE, QUOTA) would most likely have communities.

---

## Q5. Do their own terms acknowledge vendor ToS risk?

### Takeaway
**AILE** is the most explicit: it states resale may be prohibited, requires sharer warranties, and shifts provider bans onto sharers. **TokenlyPro** requires sellers to confirm resale rights and forbids evading provider terms. **QUOTA** admits in its FAQ that consumer plans forbid sharing, and excludes them. **TokenBank** says only that it will start "with providers that allow this". **ATE, newtokenclub, SecondTokens and idlicat** say nothing (ATE's terms link is a placeholder).

### Cited Findings
- AILE risk page (updated Jul 2026): "Providers may prohibit key sharing or resale." "A provider can revoke a sharer's key at any time, ending the listings backed by it." Token and API-resale legality "differs by jurisdiction". — [aile.sh/legal/risk](https://aile.sh/legal/risk)
- AILE terms (updated Oct 2026): "You may list capacity only if you are permitted to share or resell it under every agreement governing it … including your provider's terms". Sharers are responsible for "any action a provider takes against an account you connected". Listings can be suspended on substantiated provider notices. — [aile.sh/legal/terms](https://aile.sh/legal/terms)
- TokenlyPro terms: users must "comply with the acceptable-use policies of the underlying providers (OpenAI, Anthropic, Grok, Google)". The terms prohibit attempts to "evade provider terms of service". Sellers "confirm that you have the right to resell access to the associated credentials". — [tokenlypro.com/terms](https://www.tokenlypro.com/terms)
- QUOTA FAQ: "It depends on your provider's terms. Consumer plans are usually licensed for personal use and forbid sharing access." — [quotai.io](https://quotai.io/)
- TokenBank FAQ: starting "with the providers that allow this". No terms page. — [tokenbank.dev](https://tokenbank.dev/)
- ATE: no ToS discussion. Terms and Privacy links are "#". — [aitokenexchange.net/index_en](https://aitokenexchange.net/index_en)

### Inferences
- The common legal pattern is to shift vendor-ToS liability onto the supplier through warranties. That does not remove the account-ban risk that `policy_economics_users.md` documents for Anthropic and OpenAI. TokenlyPro's "evade provider terms" ban sits awkwardly with a product built on reselling subscription-derived capacity.

### Gaps
- None of these platforms reported a vendor enforcement action, takedown or ban wave in any source found.

---

### Sweep for other 2026 launches (not in the covered set)
- **SecondTokens (STX)** is the only new entity found. It is a private-beta page and is profiled above. — [secondtokens.vercel.app](https://secondtokens.vercel.app)
- An English search ("sell unused Claude quota marketplace launch 2026") and a Chinese one ("闲置 订阅额度 出售 平台 上线 Claude Codex 2026") turned up only already-covered items (sub2api, the Xianyu gray market) and Anthropic's enterprise "Claude Marketplace" (6 Mar 2026). That is a procurement storefront for partner software, not quota resale. — [SiliconANGLE](https://siliconangle.com/?p=733522); [Huxiu](https://m.huxiu.com/article/4870122.html)
- A Product Hunt hit (post 1225813) turned out to be **iCodex**, a Claude/Codex usage-tracker iOS app. It does not share quota and is not relevant. — [Product Hunt](https://www.producthunt.com/posts/1225813)
- Unchecked lead: Imbue's blog post "unused claude tokens for good" turned up in search. I did not read it, so I can't say whether it is a donation or sharing scheme. — [imbue.com](https://imbue.com/blog/unused-claude-tokens-for-good)
