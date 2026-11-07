# ClawPool (clawpool.ai): business profile

Research date: 2026-11-06. Scope is business and product information only. Technical internals (credential storage, proxy mechanics, detection-avoidance) are out of scope and left out on purpose.

Method note: clawpool.ai is a client-rendered single-page app. Plain fetches return only the `<title>` and meta tags. Page text was read through a server-side text renderer (r.jina.ai) on 2026-11-06, and no site code was run locally. Citations point to the original clawpool.ai URLs. The Wayback Machine was offline during research, so earlier versions of the pricing and FAQ pages could not be seen.

**Headline: ClawPool has shut down.** As of 2026-11-06, the homepage, /pricing, /faq and /how-it-works all show a shutdown notice. Its stated reason is that it could not keep enough Claude Max accounts in the pool. API keys no longer work, new subscriptions are closed, and subscribers are getting pro-rata Stripe refunds.

---

## Q1. Identity: operator, launch date, country/market, domain, channels

### Takeaway
ClawPool was a small independent project. It went public on Hacker News on 2026-02-11, run by a pseudonymous founder ("pablojamjam" on HN, "peter-jammable" on GitHub). No company name or real names are published. Its terms choose Delaware (US) law, while one third-party directory lists it as founded in 2025 in Vienna, Austria. It marketed to English-speaking developers, and no social channels beyond HN and GitHub were found.

### Cited Findings
- **Domain and brand:** clawpool.ai. Page title "ClawPool — Pool a login for crazy cheap AI coding". Support email support@clawpool.ai, legal email legal@clawpool.ai. — [clawpool.ai](https://clawpool.ai); [clawpool.ai/terms](https://clawpool.ai/terms)
- **Self-description in the footer:** "ClawPool · Cheaper AI Coding, powered by the dev community · Independent project, not affiliated with Anthropic." — [clawpool.ai/status](https://clawpool.ai/status)
- **Public launch:** the Show HN post "Show HN: ClawPool – Pool Claude tokens to make $$$ or crazy cheap Claude Code", posted 2026-02-11 12:27:37 UTC. It got 21 points and 7 comments. — [HN item 46974109](https://news.ycombinator.com/item?id=46974109); timestamp from [HN Algolia API](https://hn.algolia.com/api/v1/items/46974109)
- **Submitter:** HN user `pablojamjam`. It is the only submission on that account, and the account posted the founder's first comment 9 seconds after the story. — [HN submissions of pablojamjam](https://news.ycombinator.com/submitted?id=pablojamjam). (One automated summary of the Algolia API named "rennokki" as author. rennokki actually posted a critical comment, and the HN submissions page confirms pablojamjam as submitter.)
- **Pre-launch operation:** in the launch thread the founder wrote "I've had this running for weeks with no issues" and "there's a waitlist". — [HN item 46974109](https://news.ycombinator.com/item?id=46974109)
- **Open-source repo:** GitHub `peter-jammable/clawpool-proxy-function`, described as "The deployed proxy function running on proxy.clawpool.ai". It was created 2026-02-10 and last pushed 2026-05-13, with 3 stars, 1 fork, no license and JavaScript code. — [GitHub repo](https://github.com/peter-jammable/clawpool-proxy-function); metadata from [GitHub API](https://api.github.com/repos/peter-jammable/clawpool-proxy-function)
- **GitHub owner profile:** `peter-jammable` has no name, company, location or bio. The account was created 2024-07-04 and has 3 public repos. — [GitHub API user](https://api.github.com/users/peter-jammable)
- **Directory listing:** EveryDev.ai describes ClawPool as "founded in 2025 in Vienna, Austria", listed in February 2026, with pricing "from $8 per month". This comes from a search-result summary only, because the page returned HTTP 429 and was not read directly. — [EveryDev.ai](https://www.everydev.ai/tools/clawpool)
- **Governing law:** "the laws of the State of Delaware, United States", with disputes heard in Delaware courts. — [clawpool.ai/terms §12](https://clawpool.ai/terms)
- **Template origin of the legal pages:** the terms and privacy policy are "adapted from the Basecamp open-source policies (CC BY 4.0)". — [clawpool.ai/terms](https://clawpool.ai/terms); [clawpool.ai/privacy](https://clawpool.ai/privacy)
- **Contact inconsistencies:** the terms and privacy policy send users to "Open an issue on our GitHub repository", but the link goes to `github.com/anthropics/claude-code/issues`, which is Anthropic's own repo. The privacy contact is privacy@clawpool.**com**, while every other address uses .ai. — [clawpool.ai/terms §13](https://clawpool.ai/terms); [clawpool.ai/privacy §9](https://clawpool.ai/privacy)
- **Third-party services named:** Google sign-in, Stripe payments, Stripe Connect payouts, and Cloudflare hosting. — [clawpool.ai/terms §7](https://clawpool.ai/terms); [clawpool.ai/privacy §3](https://clawpool.ai/privacy)

### Inferences
- This looks like a solo or very small side project, not a funded company. Signs: pseudonymous accounts, an adapted boilerplate legal template, a contact link pointing at the wrong repo, a mismatched email domain, and no company name.
- "Vienna, Austria" (EveryDev) and Delaware governing law are not necessarily in conflict. Non-US founders often choose Delaware law or use a Delaware entity. No Delaware entity was found, though.
- The site was English-only. The status page shows an "English" language selector, but no other language was seen. The target market looks global and English-speaking, not Chinese-speaking.

### Gaps
- No legal entity name, founder real name, team size or funding information was found.
- No X/Twitter, Discord, Reddit, Product Hunt, linux.do, V2EX, Zhihu, Xiaohongshu or WeChat presence was found under the clawpool.ai brand. Searches for "clawpool" with Claude and "拼车", and for clawpool with reddit/twitter/producthunt, returned nothing relevant.
- Exact go-live date is unknown. The repo was created 2026-02-10, and the founder said it had been running "for weeks" before 2026-02-11.

**Name disambiguation (other "ClawPool"s to ignore):** "虾池ClawPool - AI Agent交易市场" (an AI-agent marketplace promo seen in a GitHub Actions run) — [GitHub Actions](https://github.com/langchain-ai/deepagents/actions/runs/23072196481); npm `@dhf-claude/clawpool` and `@dhfpub/clawpool-claude` (chat-channel plugins for "ClawPool.dhf.pub") — [Socket](https://socket.dev/npm/package/@dhf-claude/clawpool), [pkgstats](https://www.pkgstats.com/pkg:@dhfpub/clawpool-claude); npm `@linkclaw/clawpool` (an OpenClaw Docker CLI) — [pkgstats](https://www.pkgstats.com/pkg:@linkclaw/clawpool). None of these showed any link to clawpool.ai.

---

## Q2. Product as publicly described: what contributors give, what buyers get, vendors and models, user journey

### Takeaway
ClawPool was a Claude-only, two-sided pool. Claude subscription holders (marketed at Claude Max) "pooled" their subscription login token, and buyers paid a small flat monthly fee for Claude Code access through ClawPool's endpoint. No OpenAI, GPT, Gemini or other vendors were listed.

### Cited Findings
- **Official service description:** "ClawPool is a two-sided marketplace that proxies requests to the LLM provider API through OAuth tokens contributed by providers ("Providers"). Consumers ("Consumers") purchase token allocations to access AI models without subscription throttles. ClawPool does not call the LLM provider API with its own credentials — all requests are routed through Provider tokens." — [clawpool.ai/terms §1](https://clawpool.ai/terms)
- **Founder pitch:** "To actually use Claude Code you need Max at $200/mo, and then most of that capacity sits idle anyway. So ClawPool lets subscribers pool their OAuth tokens and earn up to $120/mo from the spare capacity. Everyone else gets Opus, Sonnet, all models for $8/mo." — [HN item 46974109](https://news.ycombinator.com/item?id=46974109) (text via [Algolia API](https://hn.algolia.com/api/v1/items/46974109))
- **Homepage marketing (OG image):** headline "Pool a login for crazy cheap Claude". Subhead "Opus, Sonnet, all models — up to 80% off from just $8/month. Auto refresh keeps you going. Cancel anytime." and "We pool idle subscriptions so you get Opus-level AI for code, research, writing, whatever you need." Two entry buttons: "I want cheap Claude Code" and "I want to earn from my tokens". CTA: "Pool a Login — $8/mo". Nav: How it Works, Pricing, Provide, FAQ, Status, Sign In, Sign Up. — [clawpool.ai og-image](https://clawpool.ai/og-image.png)
- **Meta description:** "All models including Opus, $8/mo flat. Auto refresh when you run out. Cancel anytime." — [clawpool.ai](https://clawpool.ai) (HTML meta tags)
- **Vendor and models:** Anthropic Claude only (Opus, Sonnet, "all models"). The legal pages say "LLM provider" generically but link Anthropic's privacy policy as that provider. — [clawpool.ai/privacy §3](https://clawpool.ai/privacy); [HN item 46974109](https://news.ycombinator.com/item?id=46974109)
- **Buyer journey (high level):**
  - Sign in with Google (18+ only). — [clawpool.ai/terms §2](https://clawpool.ai/terms)
  - Subscribe through Stripe. — [clawpool.ai/terms §3](https://clawpool.ai/terms)
  - Receive a "pool key". Point Claude Code at ClawPool's endpoint with two standard environment variables (a base URL and an auth token), then run `claude`. — [HN item 46974109](https://news.ycombinator.com/item?id=46974109); [GitHub README](https://github.com/peter-jammable/clawpool-proxy-function)
  - The endpoint was Anthropic-API-compatible for Claude Code, not an OpenAI-compatible endpoint.
- **Contributor ("Provider") journey (high level):**
  - The "Pool Your Token & Earn" page reads: "Pool your Claude token and earn from your idle capacity. Your capacity is protected by design. You control when leasing stops. Set your session utilization limit. Leasing pauses when total usage reaches it." The UI example shows "30% headroom 70% pause point". — [clawpool.ai/provide](https://clawpool.ai/provide)
  - Providers choose a "leasing cutoff (from 10% up to 95% session utilization)" and "can change their leasing cutoff or remove their token at any time". — [clawpool.ai/terms §4.2](https://clawpool.ai/terms)
  - No desktop app or browser extension is mentioned. Contribution happened through the web dashboard after Google sign-in. (This last point is an inference from the terms and site navigation.)
- **Data claims:** "Content you send through the Service (prompts, code, etc.) remains yours — ClawPool does not store, log, or claim ownership of your content. Requests are proxied in real time with nothing retained." — [clawpool.ai/terms §6](https://clawpool.ai/terms)
- **Open source:** "ClawPool is open-source software." Only the proxy function repo was found, and it has no license file. — [clawpool.ai/terms §6](https://clawpool.ai/terms); [GitHub repo](https://github.com/peter-jammable/clawpool-proxy-function)

### Inferences
- The product sat in the "subscription seat pooling and relay" category, not "API key resale". Supply was consumer and Max subscription logins, not pay-as-you-go API keys.
- The "auto refresh" or "continuous access" feature, where you pay again when the allocation runs out, was the buyer-side upsell.

### Gaps
- Pre-shutdown content of /pricing, /faq and /how-it-works could not be read. Those routes now show only the shutdown notice, and the Wayback Machine was offline.
- Whether non-Max Claude subscriptions (e.g., Pro) were accepted as supply is not stated. Marketing and the shutdown notice refer to "Claude Max accounts".

---

## Q3. Business model and pricing

### Takeaway
Buyers paid a flat monthly subscription: $8/mo in launch and homepage marketing, and $10/month for 4M tokens in the current terms. Each $10 "continuous access" renewal reset the allocation. Providers earned a fixed rate per million tokens served, paid out through Stripe Connect. The provider rate and the platform's take rate were never captured publicly.

### Cited Findings
- **Consumer plan in the terms:** "Claude Code — $10/month, 4,000,000 tokens/month, continuous access available at $10 per renewal." Prices in USD, billed through Stripe at the start of each cycle. — [clawpool.ai/terms §3.1](https://clawpool.ai/terms)
- **Continuous access:** "When your token allocation is exhausted, continuous access renews your plan early for $10 and resets your allocation to 4,000,000 tokens… Renewals are one-time charges, not additional subscriptions." — [clawpool.ai/terms §3.2](https://clawpool.ai/terms)
- **Marketing price:** "$8/mo flat", "up to 80% off from just $8/month". — [clawpool.ai meta](https://clawpool.ai); [og-image](https://clawpool.ai/og-image.png); [HN item 46974109](https://news.ycombinator.com/item?id=46974109) (Feb 2026). The $8 and $10 figures conflict. The $10 terms figure is probably the later price, but no date is visible.
- **Reference vendor prices (from the HN thread, not official pages):** founder: "Max at $200/mo"; commenter brookler: "you can have a Max at $100/month". — [HN item 46974109](https://news.ycombinator.com/item?id=46974109)
- **Refund policy (normal operation):** full refund only if no tokens were used in the period. "Once usage begins, payments for that period are final… Refreshes are non-refundable." — [clawpool.ai/terms §3.3](https://clawpool.ai/terms)
- **Provider earnings:** "Providers earn a fixed rate per million tokens served through their token. The current rate is always published on the Provider page and on the provider dashboard… Earnings settle immediately — there is no pending period. Payouts are processed through Stripe Connect. ClawPool may adjust the rate with a minimum of 14 calendar days' written notice." — [clawpool.ai/terms §4.3](https://clawpool.ai/terms)
- **Earnings ceiling claim:** "earn up to $120/mo from the spare capacity" (founder, Feb 2026). — [HN item 46974109](https://news.ycombinator.com/item?id=46974109)
- **Minimum payout:** the terms refer to a "minimum payout threshold" (earnings below it are forfeited on termination) but never give the amount. — [clawpool.ai/terms §8.3–8.4](https://clawpool.ai/terms)
- **Other fees:** none besides the subscription and $10 renewals were found in the terms.

### Inferences
- The implied consumer price is $10 / 4M tokens = **$2.50 per million tokens**, blended across input and output (simple arithmetic on the §3.1 numbers). The terms do not say how cached or input tokens count toward the 4M.
- The take rate equals the consumer price per million tokens minus the provider rate per million tokens. The provider rate was only on the now-dead Provider page and dashboard, so the take rate cannot be calculated.
- Under the "up to $120/mo" claim, one Max seat ($100–$200/mo to the subscriber) could recover roughly 60–120% of its cost. That depends on demand that the shutdown notice suggests never appeared at scale.

### Gaps
- Provider rate per million tokens, platform commission, minimum payout amount and payout schedule were not found.
- No official Anthropic price comparison was fetched for this note. The "up to 80% off" claim is ClawPool's own and unverified.

---

## Q4. Target users and traction

### Takeaway
ClawPool targeted Claude Code users who want cheap Opus/Sonnet access on the buy side, and Claude Max subscribers with idle capacity on the supply side. Traction evidence is minimal: one modest Show HN, a 3-star repo, no published user or volume numbers, and a status page that now shows zero active tokens and zero requests.

### Cited Findings
- **Audience:** buyers via "I want cheap Claude Code"; sellers via "I want to earn from my tokens"; use cases "code, research, writing, whatever you need". — [og-image](https://clawpool.ai/og-image.png)
- **HN reception:** 21 points, 7 comments (2026-02-11). — [HN item 46974109](https://news.ycombinator.com/item?id=46974109)
- **Community criticism (HN, Feb 2026):**
  - gleipnircode: "Their ToS explicitly prohibit reselling. Sellers risk permanent bans, buyers risk losing access overnight… Clever hack, but the runway seems very short."
  - brookler: "I can see Anthropic coming down on this."
  - mdrzn: "The correct response would be 'Yes, it breaks Anthropic TOS'."
  - rennokki: "Doing this will enforce Claude to raise prices or decrease limits."
  - soraminazuki pushed back on "one-sided ToS" in general.
  - Source: [HN item 46974109](https://news.ycombinator.com/item?id=46974109)
- **GitHub:** 3 stars, 1 fork, 1 open issue. — [GitHub API](https://api.github.com/repos/peter-jammable/clawpool-proxy-function)
- **Live status page (2026-11-06):** "Service disruption", "0 active tokens", "0 requests" in every hourly bucket of the 90-hour window (≈Nov 2 8 PM – Nov 6 1 PM), "90-hour uptime 98.89%". — [clawpool.ai/status](https://clawpool.ai/status)
- **Directory listing:** EveryDev.ai (search summary only). — [EveryDev.ai](https://www.everydev.ai/tools/clawpool)

### Inferences
- The shutdown notice blames supply ("not able to keep enough Claude Max accounts in the pool"). The marketplace failed on the supply side, so demand at $8–10/mo apparently outran contributed capacity.
- No press coverage was found, so traction was probably small, at most hundreds of users. That is speculative because there is no published data.

### Gaps
- No published user counts, token volumes, provider counts, revenue, reviews on review sites, or social-media follower numbers were found.
- No user complaints were found outside HN, including about refunds or the shutdown.

---

## Q5. Status as of November 2026, vendor policy, ToS statements

### Takeaway
**Shut down.** As of 2026-11-06 ClawPool shows a shutdown notice. API keys are disabled, new sign-ups are closed, and pro-rata refunds are going out through Stripe. Its stated reason is pool supply, not a vendor action. Its own terms put ToS compliance on contributors.

### Cited Findings
- **Shutdown notice (homepage, /pricing, /faq, /how-it-works):** "We weren't able to keep enough Claude Max accounts in the pool to keep service reliable, so we're shutting it down. Refunds: every active subscriber is getting a pro-rata refund via Stripe to the original payment method. The refund covers whichever portion of your current period you used less of — time elapsed or tokens consumed. You should see it in 5–10 business days. Existing API keys have stopped working. New subscriptions are no longer being accepted. Questions: support@clawpool.ai" — [clawpool.ai](https://clawpool.ai); [clawpool.ai/pricing](https://clawpool.ai/pricing); [clawpool.ai/faq](https://clawpool.ai/faq)
- **Domain still up:** clawpool.ai and proxy.clawpool.ai both returned HTTP 200 through Cloudflare on 2026-11-06. — [clawpool.ai](https://clawpool.ai)
- **Status page:** "Service disruption · 0 active tokens", with zero requests since at least ≈2026-11-02. — [clawpool.ai/status](https://clawpool.ai/status)
- **Last code activity:** repo last pushed 2026-05-13. — [GitHub API](https://api.github.com/repos/peter-jammable/clawpool-proxy-function)
- **Discontinuation promises in the terms:** "at least 30 days' notice via email", pro-rata refunds, a final payout to providers above the minimum within 30 days, and data kept 90 days. — [clawpool.ai/terms §8.3](https://clawpool.ai/terms)
- **ToS and risk language in ClawPool's own documents:**
  - "Independent project, not affiliated with Anthropic." — [clawpool.ai/status footer](https://clawpool.ai/status)
  - Providers "represent that you have a valid subscription and that contributing the token does not violate the applicable terms of service." This puts compliance responsibility on contributors. — [clawpool.ai/terms §4.1](https://clawpool.ai/terms)
  - The acceptable-use policy bans *reselling ClawPool access* and using the service "in a way that violates the LLM provider's Acceptable Use Policy". — [clawpool.ai/terms §5](https://clawpool.ai/terms)
  - "As is" warranty disclaimer with no uptime or token-availability guarantee. Liability is capped at the amount paid in the last 12 months. — [clawpool.ai/terms §9–10](https://clawpool.ai/terms)
  - No explicit warning in the terms about provider account bans was found.
- **Founder on ToS (Feb 2026):** "I'm expecting some blowback on ToS despite the account protection — I'm all ears." — [HN item 46974109](https://news.ycombinator.com/item?id=46974109)
- **Vendor-policy context (secondary sources, not ClawPool-specific):** secondary reports say Anthropic stopped Claude Pro/Max subscription limits from covering third-party agent tools such as OpenClaw, effective 2026-04-04. — [Decodo blog](https://decodo.com/blog/anthropic-openclaw-claude-subscription-ban-guide); [Slickdeals thread](https://slickdeals.net/f/19383954-ymmv-anthropic-claude-100-extra-usage-claim-in-account). ClawPool's notice does not mention this or any Anthropic action.

### Inferences
- The exact shutdown date is unknown. Evidence points to a shutdown in **roughly mid-May to early November 2026**: zero traffic since at least ≈Nov 2, and no code pushes after 2026-05-13. This is a weak bracket.
- Several factors plausibly squeezed supply: Anthropic's stated crackdown on account sharing and reselling (cited by HN commenters), weekly rate limits, and the April 2026 third-party-tool restrictions. Contributors would have had stronger reasons to pull out because of ban risk. This is an inference and is not confirmed by ClawPool's notice.
- Whether the promised 30-day email notice was given could not be checked.

### Gaps
- No public statement from Anthropic about ClawPool specifically was found.
- No press, blog or social post about the shutdown was found. The search "ClawPool shutting down Claude Max pool refund" returned only general OpenClaw/Anthropic news.

---

## Q6. Relationship or comparison to Asale, LetsSplit, JaynShare, AweShare, Token Share (tokenshare.app)

### Takeaway
No relationship, partnership, cross-mention or public comparison between ClawPool and any of these five platforms was found. ClawPool differs from them in being Claude-only, in its Western/English-language HN launch, and in now being shut down.

### Cited Findings
- None of the clawpool.ai pages read (terms, privacy, provide, status, shutdown notice) mention Asale, LetsSplit, JaynShare, AweShare or Token Share. — [clawpool.ai/terms](https://clawpool.ai/terms); [clawpool.ai/privacy](https://clawpool.ai/privacy); [clawpool.ai/provide](https://clawpool.ai/provide)
- The HN thread does not mention any of them. — [HN item 46974109](https://news.ycombinator.com/item?id=46974109)
- Searches pairing "clawpool" with Chinese community terms (拼车, linux.do, V2EX) found no ClawPool discussion on Chinese forums, and so no comparisons with the China-focused platforms in that list. — search 2026-11-06 (no relevant URLs returned)

### Inferences
- **Category position:** ClawPool was a pooled subscription-login relay. A platform sits in the request path, meters tokens, takes buyer payments through Stripe and pays contributors per million tokens through Stripe Connect.
  - This makes it structurally closer to relay-style "idle quota" platforms than to LetsSplit-style seat-splitting boards, which stay out of the money and request flow.
  - Closeness to the other named platforms should be checked against their own research notes.
- **Distinguishing traits for the comparison table:**
  - Single vendor (Anthropic Claude only).
  - Flat-subscription consumer pricing ($8–10/mo for 4M tokens) rather than per-token pay-as-you-go.
  - Contributor-set usage cutoff.
  - Western payment rails (Stripe/Stripe Connect, Google sign-in).
  - Delaware governing law.
  - **Supply-side failure leading to shutdown in 2026** is the most useful data point for the market study.

### Gaps
- No third-party comparison articles or listicles that include ClawPool were found.
