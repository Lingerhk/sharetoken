# Vendor policy, legal/compliance, economics and users of AI subscription-quota / token-sharing platforms (as of 2026-11-06)

Scope: rules of the game (vendor ToS + enforcement), why the market exists (subscription vs API economics, idle quota), who buys (demand + market-size signals), Chinese and overseas legal context, and participant risks. Platform-specific teardowns (JaynShare, AweShare, Asale, ClawPool, LetsSplit, specific relay stations) and technical architecture are covered by other researchers.

Source-quality legend used below: [OFFICIAL] = vendor/regulator page fetched directly; [NEWS] = mainstream outlet; [LAW] = law-firm analysis; [COMMUNITY] = forum/blog/user report, unverified; [SNIPPET] = only seen via search-result summary, page itself not fetched (403/404) — treat as lower confidence.

---

## Q1. What vendors allow/forbid on account sharing, credential sharing, reselling, and using subscription quota via third-party tools

### Takeaway
Every major vendor forbids exactly what a P2P "share my leftover subscription quota with strangers" product does. Anthropic forbids it most explicitly, in writing, and enforces it: credential sharing, making the account available to others, routing plan credentials on behalf of other users, and (since Jan–Apr 2026) using subscription OAuth outside Claude Code/Claude.ai are all banned. OpenAI is the most open about third-party *tools*: since 2026-09-29 "Sign in with ChatGPT" lets a subscriber spend their own plan in partner apps. But its terms for that feature reportedly ban pooling, transferring, reselling, gifting or sharing plan usage. Google, Cursor and GitHub ban account/credential sharing too.

### Cited Findings

**Anthropic — Consumer Terms (effective Oct 8, 2025) [OFFICIAL]**
- "You may not share your Account login information, Anthropic API key, or Account credentials with anyone else." You also "may not make your Account available to anyone else," and you are responsible for all activity under the account. — [Anthropic Consumer Terms](https://www.anthropic.com/legal/consumer-terms)
- Section 3(7) bans access through bots, scripts or other non-human means "Except when you are accessing our Services via an Anthropic API Key or where we otherwise explicitly permit it." Section 3 also bans using the Services to "resell the Services" and helping "another person to access or use" the Services in those ways. — [Anthropic Consumer Terms](https://www.anthropic.com/legal/consumer-terms)
- Anthropic may "suspend or terminate your access to the Services (including any Subscriptions) at any time without notice to you" if it believes you breached the terms. — [Anthropic Consumer Terms](https://www.anthropic.com/legal/consumer-terms)

**Anthropic — Claude Code "Legal and compliance" page (current, fetched Nov 2026) [OFFICIAL]**
- "Advertised usage limits for Pro, Max, and Ultra plans assume ordinary, individual usage of Claude Code and the Agent SDK." — [code.claude.com legal-and-compliance](https://code.claude.com/docs/en/legal-and-compliance)
- "OAuth authentication is intended exclusively for purchasers of Claude Free, Pro, Max, Ultra, Team, and Enterprise subscription plans and is designed to support ordinary use of Claude Code and other native Anthropic applications." — [same](https://code.claude.com/docs/en/legal-and-compliance)
- "Anthropic does not permit third-party developers to offer Claude.ai login into their own applications, or to route requests through Free, Pro, Max, or Ultra plan credentials on behalf of their users. Moreover, developers may not collect, store, or intermediate Claude.ai credentials or session tokens — sign-in to a Claude account must complete through Anthropic's own flow." — [same](https://code.claude.com/docs/en/legal-and-compliance)
- For platforms that host Claude Code: "Customers may not pay for, resell, or intermediate Claude usage on their end users' behalf. Each end user must authenticate with their own Anthropic API key, Claude subscription plan credentials, or 3P inference provider credential." The Claude Code binary "must not be modified." — [same](https://code.claude.com/docs/en/legal-and-compliance)
- "Anthropic reserves the right to take measures to enforce these restrictions and may do so without prior notice." — [same](https://code.claude.com/docs/en/legal-and-compliance)

**Anthropic — 2025–2026 timeline of tightening [NEWS]**
- **2025-07-28** (effective **2025-08-28**): Anthropic added two weekly limits (an overall cap and an Opus-specific cap) on top of the 5-hour limits for Pro/Max. Reasons given: users running Claude Code 24/7, and some users "violating its usage policy by sharing accounts and reselling access." It estimated the limits would hit "less than 5% of subscribers based on current usage." Overage could be bought "at standard API rates." — [TechCrunch 2025-07-28](https://techcrunch.com/2025/07/28/anthropic-unveils-new-rate-limits-to-curb-claude-code-power-users/); [Engadget](https://www.engadget.com/ai/anthropic-is-rate-limiting-claude-code-blaming-some-users-for-never-turning-it-off-211134730.html)
- In the same announcement Anthropic cited one user who consumed "tens of thousands" of dollars of model usage on a $200 plan. — [Tom's Guide](https://www.tomsguide.com/ai/anthropic-is-putting-a-limit-on-a-claude-ai-feature-because-people-are-using-it-24-7); [Gigazine](https://gigazine.net/gsc_news/en/20250729-anthropic-calude-code-rate-limits)
- **2025-09-05**: Anthropic barred entities more than 50% owned, directly or indirectly, by companies headquartered in unsupported regions (China, Russia, Iran, North Korea), wherever they operate. It said Chinese-linked firms had kept access through overseas subsidiaries. — [The Decoder](https://the-decoder.com/anthropic-bans-companies-majority-controlled-by-china-russia-iran-and-north-korea-from-claude/); [Dawn/AFP](https://www.dawn.com/news/1939885); [MediaNama](https://www.medianama.com/2025/09/223-anthropic-service-policy-update-bans-ai-access-chinese-firms/)
- **January 2026**: Anthropic engineer Thariq Shihipar said Anthropic had taken steps to stop third-party tools from "spoofing the Claude Code harness." His reason: such harnesses generate unusual traffic without Claude Code's telemetry, which makes rate-limit and ban questions hard to debug. — [The Register 2026-02-20](https://www.theregister.com/software/2026/02/20/anthropic-clarifies-ban-on-third-party-tool-access-to-claude/5014546). One secondary source dates the server-side block to 2026-01-09 [SNIPPET] — [paddo.dev](https://paddo.dev/blog/anthropic-walled-garden-crackdown/)
- **2026-02-19/20**: Anthropic revised its docs. They now say that using OAuth tokens from Free/Pro/Max "in any other product, tool, or service" is "not permitted and constitutes a violation of the Consumer Terms of Service." Anthropic called this a clarification of existing policy. Consumer Terms §3.7 had barred unendorsed automated access since at least Feb 2024. OpenCode removed Claude Pro/Max support in a commit citing "anthropic legal requests." Named affected harnesses: OpenCode, Pi (behind OpenClaw), and others. — [The Register](https://www.theregister.com/software/2026/02/20/anthropic-clarifies-ban-on-third-party-tool-access-to-claude/5014546); [Gigazine](https://gigazine.net/gsc_news/en/20260220-anthropic-third-party-block/)
- **2026-04-03/04**: Claude Code head Boris Cherny announced that Claude subscriptions would stop covering usage through third-party tools such as OpenClaw from noon PT (sources say Apr 4 or Apr 5). Affected users could buy discounted "extra usage" bundles or switch to API keys, and Anthropic offered full refunds to anyone who wanted to cancel. Cited reasons: "outsized strain on our systems" and subscriptions "not designed" for how these tools consume capacity. — [TechCrunch 2026-04-04](https://techcrunch.com/2026/04/04/anthropic-says-claude-code-subscribers-will-need-to-pay-extra-for-openclaw-support/); [Gigazine JP](https://gigazine.net/news/20260405-anthropic-claude-code-need-pay-extra-openclaw)
- **April 2026**: Anthropic began "rolling out authentication for certain use cases," i.e. identity verification through Persona with a physical government photo ID and a live selfie. Accounts can still be disabled after verification for repeated violations or "creating accounts from restricted locations." — [KuCoin News (reproduces Anthropic help text), 2026-04-16](https://www.kucoin.com/news/flash/claude-enforces-strict-real-name-verification-users-face-higher-account-risk) [COMMUNITY-level host; Anthropic text quoted]
- The current Anthropic pricing page says the Ultra plan has "No 5-hour limit for verified accounts." This links identity verification to quota. — [claude.com/pricing](https://claude.com/pricing) [OFFICIAL, fetched]
- Unverified: a DEV Community post (now 404) described a **June 15, 2026** change that moved Agent SDK / `claude -p` / SDK-built third-party apps into a separate credit pool. — [dev.to (404)](https://dev.to/devtoolpicks/anthropic-splits-claude-subscriptions-what-changes-for-indie-hackers-on-june-15-5351) [SNIPPET]

**OpenAI [OFFICIAL via snippet / NEWS]**
- Consumer Terms of Use: "You may not share your account credentials or make your account available to anyone else and are responsible for all activities that occur under your account." OpenAI may suspend or terminate accounts that breach the Terms or Usage Policies. The snippet came from a version labeled effective Jan 1, 2026. — [OpenAI Terms of Use](https://openai.com/en-GB/policies/row-terms-of-use/) [SNIPPET; page returned 403]
- Business Terms: customers may not make credentials available to third parties, share individual logins between multiple users, or "resell or lease access to your account or any End User Account." — [OpenAI Business Terms](https://openai.com/policies/business-terms) [SNIPPET]
- **Sign in with ChatGPT (launched 2026-09-29)**: Plus and Pro (possibly also Go) subscribers can spend their plan allowance in participating third-party apps. Launch partners include Amp, Devin, Lovable, OpenClaw, Notion and Vercel (16 partners). Usage draws on the "ChatGPT Work and Codex" allowance, and users can set a weekly cap per app. When the cap is hit, included usage stops; it does not overflow to the app's own billing. — [RuntimeWire 2026-09-29](https://runtimewire.com/article/openai-launches-login-with-chatgpt-and-routes-plan-usage-into-third-party-ai-app); [The New Stack](https://thenewstack.io/sign-in-with-chatgpt/); [OpenAI Help](https://help.openai.com/en/articles/20001542) [help page 403; eligibility (Go/Plus/Pro vs Plus/Pro) conflicts between help pages]
- The **Sign in with ChatGPT Terms** (dated 2026-09-29) reportedly prohibit "pooling, transferring, reselling, gifting, or sharing ChatGPT plan usage or Authentication Tokens," and using one user's subscription to fulfill another user's requests. — [OpenAI Sign in with ChatGPT Terms](https://openai.com/policies/sign-in-with-chatgpt-terms/) [SNIPPET; page 403. Verify the wording before relying on it]
- OpenAI's Thibault Sottiaux publicly endorsed using Codex subscriptions in third-party harnesses (Feb 2026), in contrast to Anthropic. — [The Register](https://www.theregister.com/software/2026/02/20/anthropic-clarifies-ban-on-third-party-tool-access-to-claude/5014546)
- Region: from **2024-07-09** OpenAI took "additional measures" to block API traffic from unsupported regions, including mainland China, Hong Kong and Macau. — [The Register 2024-06-25](https://www.theregister.com/2024/06/25/openai_unsupported_countries/); [Caixin](https://www.caixinglobal.com/2024-06-26/openai-enforces-harsher-api-restrictions-on-unsupported-countries-102209858.html)

**Google (Gemini / Antigravity) [NEWS / COMMUNITY]**
- Around **mid-Feb 2026** (reports range from Feb 12 to Feb 23), Google restricted or suspended Google AI Ultra/Pro and Antigravity accounts that used Antigravity OAuth through OpenClaw to reach Gemini models. A support reply cited the terms against using Antigravity credentials for non-Antigravity products and a "zero-tolerance" policy. Users reported no warning, slow support, and continued billing on "bricked" accounts. Whether Gmail was affected is disputed; one source says only Antigravity access was blocked. — [OECD AI incident 2026-02-23](https://oecd.ai/en/incidents/2026-02-23-ce89); [Google AI dev forum thread](https://discuss.ai.google.dev/t/account-restricted-without-warning-google-ai-ultra-oauth-via-openclaw/122778); [$250/mo Ultra subscriber banned thread](https://discuss.ai.google.dev/t/250-mo-ultra-subscriber-banned-without-warning-the-openclaw-mass-ban-wave-shows-a-systemic-failure-in-googles-developer-support/123015); [Business Today](https://www.businesstoday.in/technology/news/story/google-restricts-antigravity-users-tied-to-suspicious-openclaw-behaviour-517570-2026-02-23); [Gigazine JP](https://gigazine.net/news/20260224-google-gemini-openclaw-accounts-banned/)
- OpenClaw reportedly removed Antigravity as a supported backend afterwards. — [Engineer's Codex](https://engineerscodex.com/google-restricts-ultra-accounts-openclaw) [SNIPPET]
- Unverified report that Gemini CLI / Code Assist will stop serving AI Pro/Ultra/free users and be replaced by a closed-source Antigravity CLI (June 18, 2026). I could not confirm this against an official Google source. — [developmentstoday](https://developmentstoday.com/ai-robotics/google-reshapes-ai-subscription-ladder-new-100-ultra-tier) [SNIPPET]

**Cursor / GitHub Copilot [COMMUNITY / tracker]**
- A Cursor staff member on the official forum said account sharing is against Cursor's ToS. A forum thread on the API/SDK terms treats selling access, e.g. "selling someone access to use 1 hour of your ultra account," as prohibited resale. — [Cursor forum: multiple people one account](https://forum.cursor.com/t/can-multiple-people-use-one-cursor-account/158868); [Cursor forum: API/SDK terms](https://forum.cursor.com/t/api-sdk-terms-of-use-question/159741/4); ToS: [cursor.com/terms-of-service](https://cursor.com/terms-of-service) (exact clause not extracted)
- GitHub ToS (via third-party tracker): no shared login across multiple people; no sharing API tokens to exceed rate limits; one free account per person. — [ConductAtlas: no shared login](https://conductatlas.com/platform/github/github-terms-of-service/no-shared-login-across-multiple-people/); [ConductAtlas: no sharing API tokens to exceed rate limits](https://conductatlas.com/platform/github/github-terms-of-service/no-sharing-api-tokens-to-exceed-rate-limits/)

### Inferences
- Under every vendor's terms, a platform where seller A's subscription serves buyer B's requests is a ToS violation for the seller. Anthropic's "route requests through … plan credentials on behalf of their users" and "collect, store, or intermediate … credentials or session tokens" clauses also reach the platform operator directly, not just the account holder.
- OpenAI's Sign in with ChatGPT is the closest vendor-sanctioned analogue to "spending my quota elsewhere," but it is strictly self-use: per-user, per-app caps, and (reportedly) an explicit ban on pooling, transferring or gifting. Vendors have productized the "use my plan in other tools" demand while keeping "give my plan to someone else" off the table.
- The legitimate way to pool capacity is inside an organization. Anthropic Team plans have pooled API credits (see Q3), Anthropic's enterprise TPM guidance treats rate limits as org-level and shared, and OpenAI Business/Enterprise has workspace credits. A compliant "sharing" product would have to look like team/org billing (members of one customer entity), not a cross-stranger marketplace.
- The direction of travel (weekly caps in 2025 → OAuth lock-in Jan–Feb 2026 → third-party usage moved to paid extra usage Apr 2026 → ID verification Apr 2026 → verification-gated limits on Ultra) is steady tightening at Anthropic. Supply built on Claude subscriptions is the most fragile.

### Gaps
- Could not fetch OpenAI's current consumer ToU or the Sign in with ChatGPT Terms directly (403). The pooling/gifting ban wording is from a search snippet only.
- Exact current Cursor ToS clause text and any GitHub Copilot-specific sharing clause were not extracted.
- No official Google statement on the Feb 2026 ban wave was found; only forum and press reports.
- The June 15, 2026 Anthropic credit-pool split is unverified (source 404).

---

## Q2. Documented enforcement / ban waves and their triggers

### Takeaway
Bans are real, often come without warning, and in many reports without refund. The triggers people report are traffic that doesn't look like the official client (spoofed harness, no telemetry), abnormal consumption velocity, device-fingerprint and IP churn, unsupported regions, and suspicious payment instruments (prepaid/virtual cards). The causes community members attribute are mostly inference; vendors rarely confirm specific triggers.

### Cited Findings
- Anthropic says it has banned accounts that tried to game its pricing, and has blocked harness spoofing (Jan 2026). — [The Register](https://www.theregister.com/software/2026/02/20/anthropic-clarifies-ban-on-third-party-tool-access-to-claude/5014546)
- Google's Feb 2026 wave hit paying AI Ultra users of OpenClaw via Antigravity OAuth, under a "zero-tolerance" policy (see Q1). — [Google AI dev forum](https://discuss.ai.google.dev/t/account-restricted-without-warning-google-ai-ultra-oauth-via-openclaw/122778); [OECD AI incident](https://oecd.ai/en/incidents/2026-02-23-ce89)
- A Linux.do user said a Claude Max (20x) account, subscribed 2026-07-27 and used normally for about a week, was rented out via "CPA mode." It was banned after about 1 hour of rental and about $50 of consumption. The article's author guesses the triggers were short-burst high-frequency consumption, device-fingerprint change and multi-IP churn; this is inference, with no official confirmation. — [80aj.com 2026-08-06 (reposting Linux.do)](https://www.80aj.com/2026/08/06/anthropic-clude-account-ban/) [COMMUNITY]
- March 2026: several Chinese users who paid for Claude with Bybit or Fiat24 virtual cards, or Fiat24 via Apple Pay, reported bans that changing payment method or network did not lift. Anthropic has not confirmed a link. — [80aj.com 2026-03-28](https://www.80aj.com/2026/03/28/anthropic-clause-payments-ban/) [COMMUNITY]
- Bybit virtual debit cards were reported declined when binding to Claude and ChatGPT. The suspected cause is BIN-level risk rules; Stripe is described as strict on anonymous prepaid cards. — [80aj.com 2026-07-31](https://www.80aj.com/2026/07/31/bybit-virtual-card-payment-issue/); [PhotonPay blog](https://www.photonpay.com/hk/blog/article/claude-pro-subscription-failed) [COMMUNITY/vendor marketing]
- On V2EX, users observe that solo personal use rarely gets banned, but unstable proxies, payment-method problems or 拼车 (shared accounts) easily do. — [V2EX t/1176948](https://v2ex.com/t/1176948) [COMMUNITY]
- In 2026, Linux.do users report fresh Claude accounts banned within hours even with domain email, virtual phone number, virtual card and US IP. — summarized at [80aj.com 2026-05-06](https://www.80aj.com/2026/05/06/claude-max-bypass-guide/) [COMMUNITY; not fetched in full]

### Inferences
- A P2P sharing product creates exactly the signals vendors key on. One subscription consumed from many IPs and devices, in bursts, sometimes through non-official clients, looks the same as 转租 (re-renting) to an anti-abuse system. The ~1-hour/$50 ban anecdote suggests Anthropic can detect this within a single 5-hour window.
- Sellers should expect total loss of the subscription with no refund ("without notice" clauses), and buyers should expect their prepaid balance on a platform to become stranded when upstream accounts die.

### Gaps
- No vendor-published statistics on number of accounts banned for sharing/reselling.
- No confirmed OpenAI ban wave specific to ChatGPT/Codex account sharing found in this pass.

---

## Q3. Subscription economics: prices/limits (Nov 2026), extractable API value, utilization ("idle tokens")

### Takeaway
Subscriptions are priced at a steep discount to API list prices for heavy users. Anthropic itself cited a $200-plan user burning "tens of thousands" of dollars of usage, and that arbitrage gap is why both relays and sharing platforms exist. Anthropic enterprise data suggests a typical active developer consumes roughly $150–250/month at API list price, about the same as one Max/Ultra plan. Anthropic's own "<5% of subscribers hit the weekly cap" statement implies most subscribers leave capacity unused. No vendor publishes utilization distributions, and widely repeated "20–30% utilization" figures are unsourced.

### Cited Findings

**Current prices and limits — Anthropic (claude.com/pricing, fetched Nov 2026) [OFFICIAL]**
- Pro: $20/month, or $17/month billed annually ($200 upfront); 1x baseline on both the 5-hour and weekly windows; includes Claude Code. — [claude.com/pricing](https://claude.com/pricing)
- Max: $100/month; 5x Pro on both windows. — [claude.com/pricing](https://claude.com/pricing)
- Ultra: $200/month; "No 5-hour limit for verified accounts"; 10x Pro weekly; includes API credits of $200/month (vs $100 on Max); "Fable" model capped at 50% of weekly limits. — [claude.com/pricing](https://claude.com/pricing)
- Team (2–150 users): Standard $20 (annual) / $25 (monthly) per seat; Premium $100/$125 per seat with 5x standard usage; pooled API credits up to $500/month. — [claude.com/pricing](https://claude.com/pricing)
- Pro/Max/Ultra can buy "usage credits" (extra usage) past plan limits. — [code.claude.com costs](https://code.claude.com/docs/en/costs)
- Note: in 2025 the lineup was Max 5x $100 / Max 20x $200, and Anthropic's 2025 guidance said the $200 plan gave "240 to 480 hours of Sonnet 4 and 24 to 40 hours of Opus 4" per week. — [apidog summary of 2025 announcement](https://apidog.com/blog/weekly-rate-limits-claude-pro-max-guide) [dated 2025]. A July 2026 user report still references "Claude Max (20倍额度)." — [80aj.com](https://www.80aj.com/2026/08/06/anthropic-clude-account-ban/). The Ultra tier's launch date was not found.

**Current prices and limits — OpenAI (learn.chatgpt.com/docs/pricing, fetched Nov 2026) [OFFICIAL]**
- Free $0; Go $8/month; Plus $20/month; Pro "from $100/month" in $100, $200 or $500 tiers (Ultrafast mode on the $500 tier); Business $20/user/month annual or $25 monthly; Enterprise/Edu by contract; or Codex via API key at API pricing. — [ChatGPT/Codex pricing](https://learn.chatgpt.com/docs/pricing)
- Codex local messages per 5 hours (Plus and Standard Business): GPT-6 Astra 5–45; GPT-6.1 Sol 15–160; GPT-6.1 Terra 35–285; GPT-6 Sol 15–150; GPT-6 Luna 350–3,000. Cloud tasks share the allowance. Weekly limits "may apply" but are not quantified. **Pro: no five-hour limit currently.** — [same](https://learn.chatgpt.com/docs/pricing)
- Credits per 1M tokens (input/cached/output): GPT-6 Astra 250/25/1,250; GPT-6.1 Sol 50/2.5/250; GPT-6 Luna 2.5/0.25/12.5. Fast mode uses 2.5x included usage; Ultrafast 8x. Plus/Pro users can buy extra credits when they hit limits. — [same](https://learn.chatgpt.com/docs/pricing)

**Current prices and limits — Google (I/O, 2026-05-19) [OFFICIAL]**
- New Google AI Ultra tier at $100/month with 5x AI Pro limits; top AI Ultra cut from $250 to $200/month with 20x AI Pro. Limits are now "compute-based," refreshing every five hours until a weekly limit is reached. Pro and Ultra can buy pay-as-you-go AI credits for Antigravity. — [Google blog 2026-05-19](https://blog.google/products-and-platforms/products/google-one/google-ai-subscriptions)
- Google AI Pro listed at $19.99/month (aggregator). — [pricepertoken](https://pricepertoken.com/subscriptions/gemini) [SNIPPET]

**Extractable API value / heavy-user arbitrage**
- Anthropic (July 2025): one user consumed "tens of thousands" of dollars of model usage on a $200 plan. — [Tom's Guide](https://www.tomsguide.com/ai/anthropic-is-putting-a-limit-on-a-claude-ai-feature-because-people-are-using-it-24-7)
- The Register frames subscription pricing as a subsidy that enabled "token arbitrage" via third-party harnesses ("all-you-can-eat buffet" analogy). — [The Register](https://www.theregister.com/software/2026/02/20/anthropic-clarifies-ban-on-third-party-tool-access-to-claude/5014546)
- Anthropic enterprise data: Claude Code averages about **$13 per developer per active day** and **$150–250 per developer per month** at API list prices, with **90% of users below $30 per active day**. — [code.claude.com/docs/en/costs](https://code.claude.com/docs/en/costs) [OFFICIAL]
- ccusage is the standard community tool for computing API-equivalent spend from local Claude Code logs. Guides recommend running `ccusage --days 30` to compare against plan price. Its figures are list-price estimates, not billed amounts. — [claudelog pricing guide](https://claudelog.com/claude-code-pricing); [morphllm](https://www.morphllm.com/claude-code-max) [COMMUNITY]

**Utilization / idle quota**
- Anthropic estimated its 2025 weekly limits would affect "less than 5% of subscribers"; a spokesperson said "most users won't notice a difference." — [TechCrunch](https://techcrunch.com/2025/07/28/anthropic-unveils-new-rate-limits-to-curb-claude-code-power-users/)
- Anthropic's team rate-limit guidance cuts recommended TPM per user from 200–300k (1–5 users) to 10–15k (500+ users) "because fewer users tend to use Claude Code concurrently in larger organizations." — [code.claude.com/docs/en/costs](https://code.claude.com/docs/en/costs) [OFFICIAL]
- Vendor/marketing blogs claim most subscribers use under 30%, or 20–30%, of included capacity, but cite only unnamed research. — [ATXP blog](https://atxp.ai/blog/ai-subscriptions-are-kind-of-nuts/); [Admix](https://admix.software/blog/ai-subscription-trap) [COMMUNITY, unsourced]

### Inferences
- **Supply exists**: if fewer than 5% of subscribers hit the weekly cap (Anthropic, 2025), most subscribers' weekly quota expires partly unused, which is the "idle token" pool. The amount per user is unknown.
- **Multiplexing is real**: Anthropic's own concurrency-based TPM guidance shows that pooling many users' allowances supports far more aggregate demand than the sum of peaks. This is the economic logic of a sharing platform, and the logic vendors capture themselves through Team/Enterprise pooling.
- **Arbitrage spread**: a $100–200 plan versus a $150–250/month typical-active-dev API bill (with heavy tails into the thousands or more) means subscription tokens cost a fraction of API list price for heavy users. That spread is the margin relays and sharing platforms monetize, and the reason vendors keep tightening (weekly caps, OAuth lock, extra-usage pricing).
- Vendors now sell their own overage valve (Anthropic usage credits / extra usage; OpenAI Codex credits; Google AI credits) at roughly API rates. A sharing product's buyer value proposition is therefore "overage below the vendor's API-rate overage price," which only works if the shared quota is cheaper than API, i.e. the arbitrage vendors prohibit.

### Gaps
- No vendor-published distribution of subscription utilization (e.g., median % of weekly quota used). No independent survey found.
- Widely quoted community figures could not be tied to a verifiable URL and are excluded as facts: "$2,976 API value for $100 over 21 days on Max 5x," "fully utilized Max ≈ $15,000/yr API-equivalent," and "SemiAnalysis: $200 plan ≈ $8,000/month compute; break-even ~10% utilization."
- Current GitHub Copilot and Cursor plan prices and limits were not researched in this pass.

---

## Q4. Demand side: who needs overage / cheap access, and market-size signals

### Takeaway
The largest and best-documented demand pool is mainland Chinese developers and companies locked out of Claude and OpenAI by region, KYC and payment barriers. They are served by a "中转站" (relay) industry selling Claude/GPT access at roughly 0.03x–0.3x official price. Other demand: heavy Claude Code/Codex users hitting 5-hour and weekly caps, users of third-party agent harnesses (OpenClaw etc.) who lost subscription coverage in 2026, individual developers and small teams wanting multiple models, and content businesses (short drama, MCN, games) needing image and video generation.

### Cited Findings
- **Who uses relays (China)**: individual developers and small teams, especially those needing multiple models at once, who face barriers on "账号、付款、额度、地区访问"; short-drama, MCN and game companies with fast-growing video and image needs; ordinary users drawn in by "0元学AI" / "AI副业" promotions ("一杯奶茶钱兑换数百万Token"). — [中国新闻周刊 via Sina, 2026-06-22](https://news.sina.cn/gn/2026-06-22/detail-iniefqsa0438001.d.html?vt=4) [NEWS]
- **Size and price signals (China)**:
  - A UCSB PhD student's group (刘涵之) evaluated **400+ relay stations**. — [中国新闻周刊/Sina](https://news.sina.cn/gn/2026-06-22/detail-iniefqsa0438001.d.html?vt=4)
  - Price multipliers ("倍率"): ads claim 0.07x; some sellers offer free-quota "日抛号" (disposable accounts) at 0.03x. An operator said anything below one-tenth of official price "基本走的就是黑灰产路线." — [same](https://news.sina.cn/gn/2026-06-22/detail-iniefqsa0438001.d.html?vt=4)
  - Setup cost is ¥2,000–5,000; open-source projects bundle proxying, billing and user management with "一行命令就能部署." — [same](https://news.sina.cn/gn/2026-06-22/detail-iniefqsa0438001.d.html?vt=4)
  - Account pools come from bulk-registered or bought ChatGPT/Claude subscription accounts, real-name phone numbers, and stolen credit cards used to farm free credits. One operator dispatched a single account to "几十人" per day. — [same](https://news.sina.cn/gn/2026-06-22/detail-iniefqsa0438001.d.html?vt=4)
  - Short-video platforms are flooded with "纯血Claude，免费试用" ads. — [same](https://news.sina.cn/gn/2026-06-22/detail-iniefqsa0438001.d.html?vt=4)
- ChinaTalk essay (Zilan Qian, then Oxford China Policy Lab, 2026-05-05), "How to Buy Cheap Claude Tokens in China": transfer stations sell Claude tokens 70–90% below official price through account arbitrage, quota resale and **Claude Max subscription splitting**, with possible model substitution and monetization of user logs; payment by WeChat/Alipay. — secondary coverage: [KuCoin/PANONY](https://www.kucoin.com/news/insight/BTC/69fc85172f8d6c000732c363); [WinBuzzer 2026-07-03](https://winbuzzer.com/2026/07/03/chinas-cheap-claude-tokens-run-through-proxy-markets-xcxwbn/); [IBTimes ("up to 93% cheaper")](https://www.ibtimes.co.uk/grey-market-claude-ai-tokens-china-1805269) [original essay not fetched]
- Financial-media analysis says the minimum sustainable price for a legitimately sourced relay is about 0.7–0.8x official. Anything lower implies problematic account-pool sources or subsidy from "data income." — summarized from [Sina Finance 2026-05-11](https://finance.sina.com.cn/wm/2026-05-11/doc-inhxpatr0578997.shtml); [MarsBit](https://news.marsbit.co/20260507103517088195.html) [SNIPPET]
- Linux.do (the main Chinese dev community for this topic) has active threads on foreign reporting of the "gray industry," e.g. "外媒揭露中国AI中转站的'灰色产业'" (multi-page). — [linux.do t/2145040](https://linux.do/t/topic/2145040); [linux.do t/2147704](https://linux.do/t/topic/2147704?tl=en) [COMMUNITY]
- **Upstream demand drivers (access barriers)**: OpenAI blocked China/HK/Macau API traffic from 2024-07-09 ([The Register](https://www.theregister.com/2024/06/25/openai_unsupported_countries/)). Anthropic barred >50% China-owned entities worldwide from 2025-09-05 ([The Decoder](https://the-decoder.com/anthropic-bans-companies-majority-controlled-by-china-russia-iran-and-north-korea-from-claude/)). Anthropic started ID verification in April 2026 ([KuCoin/Anthropic text](https://www.kucoin.com/news/flash/claude-enforces-strict-real-name-verification-users-face-higher-account-risk)). Domestic Chinese cards are poorly supported and virtual cards get declined ([CSDN guide](https://aicoding.csdn.net/6a61bc6a10ee7a33f291ad87.html); [80aj](https://www.80aj.com/2026/07/31/bybit-virtual-card-payment-issue/)).
- **Cap-driven demand (global)**: weekly limits and 5-hour windows (Anthropic, OpenAI Plus, Google) push heavy users to pay for overage. Anthropic, OpenAI and Google all now sell top-up credits (see Q3). After April 2026, OpenClaw and other third-party-harness users had to pay extra usage or API rates to keep using Claude. — [TechCrunch 2026-04-04](https://techcrunch.com/2026/04/04/anthropic-says-claude-code-subscribers-will-need-to-pay-extra-for-openclaw-support/)
- **Aggregator market signal**: OpenRouter processed 36.1T tokens in the week of June 1–7, 2026 (+13.5% WoW), with Chinese models at 14.19T (+27.49%). OpenRouter raised a $113M Series B at about $1.3B valuation and takes a 5% fee. — [中国新闻周刊/Sina](https://news.sina.cn/gn/2026-06-22/detail-iniefqsa0438001.d.html?vt=4)

### Inferences
- Demand splits into two segments with different legal exposure. (a) **Access-constrained buyers** (China) value any working channel, are price-sensitive, and tolerate gray supply. (b) **Cap-constrained buyers** (global heavy users) mainly want cheaper or more flexible overage than the vendor's extra-usage pricing. Segment (a) is far larger in observable activity (400+ stations, mass short-video ads); segment (b) is now partly served by the vendors' own top-ups and OpenAI's Sign in with ChatGPT.
- Thin-margin economics and ¥2–5k setup cost mean low entry barriers and heavy competition. The price war down to 0.03–0.1x implies a substantial share of the market runs on illegitimate supply (stolen cards, farmed trials) rather than genuine "idle" quota from paying subscribers.

### Gaps
- No credible count of total relay stations in China (only "400+ evaluated"), no revenue estimates, and no Xianyu listing counts or 拼车/车位 price data. Searches found no reliable Xianyu price data.
- No data on how many overseas users want to *borrow* overage from strangers rather than buy vendor credits.
- Student-segment demand was not specifically documented.

---

## Q5. Legal/regulatory context (China + overseas)

### Takeaway
In China, offering generative-AI services to the public, including via a relay or API, falls under the CAC Interim Measures (filing and security assessment). CAC published its first relay enforcement case (Jiangsu, Sept 2026) with an administrative penalty: rectification and a warning. Criminal exposure is real but not yet confirmed by any published judgment. One Shanghai operator self-reported 37 days of criminal detention (May 2026), and lawyers identify reverse-engineering subscription-to-API conversion as the main criminal risk (Criminal Law Art. 285(3)). The Ministry of State Security (MSS) publicly warned against relays in June 2026. Lawyers say there is currently no fully compliant channel to offer Claude/GPT to the mainland public.

### Cited Findings
- **CAC enforcement case**: CAC's typical-cases release (2026-09-15) included a "江苏某科技公司" that ran two websites as an "API中转站" calling multiple LLM APIs to provide chat/QA services and "未按规定开展安全评估." It was ordered to rectify, and the responsible person was "从严处理" and warned, under 《生成式人工智能服务管理暂行办法》等规定. A Tianyuan Law Firm lawyer comments that relays are "not per se illegal" (rectification was allowed). He says responsibility follows whoever actually provides the service to the public, regardless of model ownership or product renaming. Public-facing relays must do security assessment and algorithm filing and disclose the real model called. — [iCourt 2026-09-18](https://www.icourt.cc/prac-document/161924.html) [LAW]
- **Filing statistics**: a July 2026 CAC notice (data to 2026-06-30) reported 988 generative AI services filed (备案) and 598 apps or features registered (登记). Apps calling filed models register with local CAC offices. — [卓建律所 (Zhuojian Law)](https://www.lawzj.cn/news_view.aspx?TypeId=5&Id=1846&Fid=t2%3A5%3A2) [LAW]
- **Applicable rules (per 卓建 analysis)** [LAW] — [lawzj.cn](https://www.lawzj.cn/news_view.aspx?TypeId=5&Id=1846&Fid=t2%3A5%3A2)
  - Interim Measures: Art. 21 handles failure to file; Art. 22 covers providers serving "通过提供可编程接口等方式."
  - Telecommunications Regulations Art. 7: "未取得电信业务经营许可证，任何组织或者个人不得从事电信业务经营活动." This is an administrative regulation that could support "违反国家规定" for 非法经营罪.
  - 国际联网管理暂行规定 Art. 6 bars self-built or other channels for international connection.
- **Criminal theories (per lawyers)** [LAW] — [lawzj.cn](https://www.lawzj.cn/news_view.aspx?TypeId=5&Id=1846&Fid=t2%3A5%3A2); [iCourt](https://www.icourt.cc/prac-document/161014.html); [腾讯新闻 法眼观澜 2026-06-29](https://news.qq.com/rain/a/20260629A07O8W00)
  - **非法经营罪 (illegal business operation)**: contested. A departmental rule (non-filing) is not a "国家规定" under the 155号通知, but the telecom-licence route is arguable.
  - **提供侵入、非法控制计算机信息系统程序、工具罪 (providing tools to intrude into computer systems, Art. 285(3))**: called the main risk. It covers "订阅转API" (subscription-to-API conversion), protocol reverse-engineering, and bypassing signatures or risk controls. Thresholds under the 计算机犯罪解释 Art. 3: 20+ instances or ¥5,000+ illegal gains. Aggregated forwarding with legitimate keys is generally not "intrusive."
  - **帮信罪 (aiding information-network crime, Art. 287-2)**: arises if downstream clients commit fraud.
  - Other exposure: fraud or illegal access to computer data if supply uses stolen cards or keys; personal-information offences if user data is traded.
  - Lawyer 邵诗巍 (Shanghai 曼昆): a mid-size operator (¥500k monthly flow, 10k+ users) that bypasses authentication faces 3–7 years. "Claude、GPT在中国大陆没有合法代理渠道." The Data Security Law and PIPL have no small-scale exemption. — [中国新闻周刊/Sina](https://news.sina.cn/gn/2026-06-22/detail-iniefqsa0438001.d.html?vt=4)
- **Shanghai relay operator case**: an operator ("瓜皮") posted a shutdown notice in May 2026 saying he had been criminally detained by Shanghai police for 37 days for "非法获取API模型," was on bail (取保候审), had returned illicit gains, expected a fine and "将来肯定会被判刑," and could not refund users' top-ups. No official notice or judgment had appeared as of 2026-09-29. — [中国新闻周刊/Sina](https://news.sina.cn/gn/2026-06-22/detail-iniefqsa0438001.d.html?vt=4); [卓建律所](https://www.lawzj.cn/news_view.aspx?TypeId=5&Id=1846&Fid=t2%3A5%3A2)
- **Earlier precedent**: 上海熵云 "套壳ChatGPT" (2022–2023) received an administrative unfair-competition penalty of ¥62,692.7 under the Anti-Unfair Competition Law. This was not a criminal case. — [卓建律所](https://www.lawzj.cn/news_view.aspx?TypeId=5&Id=1846&Fid=t2%3A5%3A2)
- **VPN analogies** (illegal-business convictions for selling circumvention): e.g. (2020)粤1823刑初110号, 35 sales and ¥189.30 profit, 1 year. Sentencing varies widely. — [iCourt](https://www.icourt.cc/prac-document/161014.html) [SNIPPET]. Lawyers also flag bundling proxy/VPN nodes with relay service as a red line (cites 豫12刑终271号). — [lawzj.cn](https://www.lawzj.cn/news_view.aspx?TypeId=5&Id=1846&Fid=t2%3A5%3A2)
- **MSS warning (2026-06-08)**: 《"AI中转站"，风险要防范》 lists data leakage and resale ("数据裸奔"), low-spec models passed off as premium, backdoors that steal accounts and surveil developer devices, and unauthorized data export. It advises using authorized platforms and reporting via 12339. — [The Star 2026-06-10](https://www.thestar.com.my/tech/tech-news/2026/06/10/leaks-and-backdoors-china-warns-of-security-risks-in-relay-services-for-foreign-ai-models); [卓建律所](https://www.lawzj.cn/news_view.aspx?TypeId=5&Id=1846&Fid=t2%3A5%3A2)
- **ICP gate**: upstream channel vendors require resellers to hold ICP filing. An ICP (value-added telecom) licence needs, among 7 criteria, a legally established company with ≥¥1M registered capital (provincial). — [中国新闻周刊/Sina](https://news.sina.cn/gn/2026-06-22/detail-iniefqsa0438001.d.html?vt=4)
- **Payments/forex**: lawyers list "cross-border payment settlement or currency exchange" (跨境结算/换汇) as a red line for relay operators. — [卓建律所](https://www.lawzj.cn/news_view.aspx?TypeId=5&Id=1846&Fid=t2%3A5%3A2). Chinese users pay relays by WeChat/Alipay. — [KuCoin/PANONY on ChinaTalk](https://www.kucoin.com/news/insight/BTC/69fc85172f8d6c000732c363)
- **US side**: a White House memo dated 2026-04-23 reportedly warned about Chinese entities. This is mentioned in the ChinaTalk essay excerpt; content not verified. — [HN mirror (now 404)](https://hackernews-dynamic.seasondane.workers.dev/news/48165492) [SNIPPET]

### Inferences
- A China-facing token-sharing platform would be treated as a provider of generative AI services to the public. Under the "responsibility follows the actual service provider" principle, a P2P or "sharing" framing does not escape filing, security-assessment, ICP and data-export obligations. Since foreign models (Claude/GPT) cannot be filed, the public-facing China use case has no compliant path.
- The specific technique a quota-sharing platform needs, extracting subscription OAuth tokens or turning a consumer subscription into a callable API ("订阅转API"), is exactly what Chinese lawyers flag as the highest criminal-risk activity (Art. 285(3)). It also violates Anthropic's "collect, store, or intermediate … credentials or session tokens" clause.
- Cross-border flows (Chinese buyers paying overseas sellers) add forex and payment-settlement risk. Overseas sellers receiving RMB via intermediaries, or crypto, raise AML and payment-processor-ToS issues.

### Gaps
- No published criminal judgment (裁判文书) against an API relay operator was found. The Shanghai case rests on the operator's own statement plus press reporting.
- Overseas consumer-protection law (EU/US) for buyers of shared quota, and any Western lawsuit against resellers, were not found in this pass.
- No authoritative source found on the personal forex quota or SAFE (国家外汇管理局) treatment of paying foreign individuals for AI quota.
- MSS original text was not fetched; English-language coverage and a law-firm summary were used.

---

## Q6. Risks to participants (sellers, buyers, platform)

### Takeaway
Sellers risk permanent loss of their subscription, without notice or refund, from vendor anti-abuse systems. Buyers risk prompt, code and credential leakage, model substitution, malicious code injection, and stranded prepaid balances. Platform operators carry vendor-contract, administrative and (in China) criminal exposure.

### Cited Findings
- **Seller: account loss.** Anthropic can suspend or terminate "at any time without notice" ([Consumer Terms](https://www.anthropic.com/legal/consumer-terms)) and enforces credential restrictions "without prior notice" ([legal-and-compliance](https://code.claude.com/docs/en/legal-and-compliance)). There is the Max-rental ban after about 1 hour ([80aj](https://www.80aj.com/2026/08/06/anthropic-clude-account-ban/)). Google Ultra users were banned without warning and some kept being billed ([Google AI forum](https://discuss.ai.google.dev/t/250-mo-ultra-subscriber-banned-without-warning-the-openclaw-mass-ban-wave-shows-a-systemic-failure-in-googles-developer-support/123015)).
- **Buyer: data leakage and malicious relays.** The 刘涵之 (UCSB) study of 400+ relays found 9 sites actively injecting malicious code, 1 site draining a researcher's ETH wallet (about $500k loss), over 2.1B tokens processed, 99 real credentials exposed, and 401 agent sessions at risk of loss of control. — [中国新闻周刊/Sina](https://news.sina.cn/gn/2026-06-22/detail-iniefqsa0438001.d.html?vt=4)
- **Buyer: data resale for distillation.** Relays reportedly package full conversation logs, especially coding sessions with code, reasoning and engineering decisions, and sell them to model vendors for training (lawyer 邵诗巍; MSS). — [中国新闻周刊/Sina](https://news.sina.cn/gn/2026-06-22/detail-iniefqsa0438001.d.html?vt=4); [WinBuzzer](https://winbuzzer.com/2026/07/03/chinas-cheap-claude-tokens-run-through-proxy-markets-xcxwbn/)
- **Buyer: model substitution.** MSS says some sites pass off low-spec models as premium ones. 刘涵之 found sites claiming official models whose response format, error messages and metadata did not match the official API. — [中国新闻周刊/Sina](https://news.sina.cn/gn/2026-06-22/detail-iniefqsa0438001.d.html?vt=4); [The Star](https://www.thestar.com.my/tech/tech-news/2026/06/10/leaks-and-backdoors-china-warns-of-security-risks-in-relay-services-for-foreign-ai-models)
- **Buyer: stranded balances.** The Shanghai operator said he could not refund users' top-ups after his arrest. — [中国新闻周刊/Sina](https://news.sina.cn/gn/2026-06-22/detail-iniefqsa0438001.d.html?vt=4)
- **Buyer: compliance-officer warnings.** The Tianyuan lawyer lists fake models, inconsistent content-safety standards, user data passed to multiple parties or abroad, and shared keys as relay compliance issues. — [iCourt](https://www.icourt.cc/prac-document/161924.html). 卓建 advises enterprises not to send personal data, trade secrets or confidential data to relays of unknown origin. — [lawzj.cn](https://www.lawzj.cn/news_view.aspx?TypeId=5&Id=1846&Fid=t2%3A5%3A2)
- **Platform: vendor action against intermediaries.** Anthropic sent legal requests that led OpenCode to drop Claude subscription support (Feb 2026). — [The Register](https://www.theregister.com/software/2026/02/20/anthropic-clarifies-ban-on-third-party-tool-access-to-claude/5014546)

### Inferences
- In a sharing platform, one seller account serves many buyers. A single upstream ban therefore hits many buyers at once, and the platform needs an escrow or refund model for mid-session failures. Payment disputes will cluster around bans, throttling (5-hour or weekly caps hit mid-task) and suspected model substitution, which buyers can't verify without per-response provenance.
- Trust and verification (proof the real model was called, no logging of prompts) is the main buyer pain point that gray relays fail on. It is a differentiation axis, but it can't fix the underlying ToS violation on the supply side.

### Gaps
- No quantitative data on chargeback/dispute rates for relays or sharing platforms.
- No documented case of a vendor suing an individual seller of shared quota.
