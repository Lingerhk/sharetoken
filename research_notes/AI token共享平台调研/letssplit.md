# LetsSplit (letssplit.in): what it is, and whether it is an "idle AI token monetization" platform

> Research date: 2026-11-06. All facts below link to a source. "Observed" means the researcher checked it directly in this session (curl, GitHub API, WHOIS) on 2026-11-06.
> **Bottom line up front:** The only active "LetsSplit" with any AI-subscription angle is **letssplit.in**. It is a one-person Indian side project, launched around July 2026, for **subscription carpooling / family-plan seat splitting** (Netflix, Spotify, etc.). ChatGPT, Claude, Gemini, Perplexity and Copilot are only listed as service logos. It is **not** a token-sharing, quota-relay or API-credit marketplace. It has no proxy, no metering, no client and no payment processing, and it does not touch credentials. I found no evidence of any other LetsSplit that monetizes idle AI tokens.

---

## Q1. What exactly is LetsSplit? Identity, disambiguation, team, launch, domain, socials

### Takeaway
"LetsSplit" (letssplit.in, internal and repo name "SubSplit") is a free beta web app built by one developer, **Sudhanshu Burile** (Pune, India). It is a listing board where people split the cost of a subscription: a "host" with spare seats on a plan they pay for is matched with "members" who pay the host directly over UPI. The domain was registered in June 2026, and the product launched publicly on Peerlist and Product Hunt in about July 2026. It has no company, no funding and no AI-token functionality.

### Cited Findings
**Identity and positioning**
- Homepage hero: "Split subscriptions. Save real money. Share Netflix, Spotify, YouTube & more with people you trust." — [letssplit.in](https://www.letssplit.in/)
- About page: "SubSplit is a marketplace for sharing family-plan subscriptions — connecting people who have spare slots with people who need them. No sketchy group chats. No trust issues." Mission: "Make premium software affordable by letting people share legitimately, safely, and transparently." — [letssplit.in/about](https://www.letssplit.in/about)
- The FAQ answer to "What is LetsSplit?" (served in the FAQ page's JS bundle): "LetsSplit helps you split the cost of subscriptions you're already paying for — Netflix, Spotify, YouTube Premium, and more — with people you trust. Hosts list a spare seat on a plan they own; members request to join and pay their share directly to the host." — [letssplit.in/faq](https://www.letssplit.in/faq)
- Branding is inconsistent: the site footer and title say "LetsSplit", but the About page and the homepage "How it works" step 4 say "SubSplit". The GitHub repo is named `SubSplit` and its README heading is "# LetsSplit". — [letssplit.in](https://www.letssplit.in/); [GitHub sudhanshu-b/SubSplit](https://github.com/sudhanshu-b/SubSplit)

**Team, company and funding**
- About page "Builder" section: "Sudhanshu Burile, Full Stack Developer, Pune, India · CS TECH AI. ~2 years building and shipping production-grade applications across React, Next.js, Node.js, and Java Spring Boot. **SubSplit is a personal project** — built from scratch to solve a real problem and shipped to production end-to-end." Contact: burilesudhanshu@gmail.com. — [letssplit.in/about](https://www.letssplit.in/about)
- The founder's GitHub profile lists name "Sudhanshu Burile", company "CS Tech AI", location "Pune", bio "Dev @ CS TECH AI", 1 follower (observed via GitHub API). — [github.com/sudhanshu-b](https://github.com/sudhanshu-b)
- Terms of Use (last updated July 12, 2026) name no legal entity or address, only "LetsSplit" and support@letssplit.in, and are "governed by the laws of India", with "exclusive jurisdiction" in "Courts located in India". — [letssplit.in/terms](https://www.letssplit.in/terms)

**Launch timeline and domain**
- GitHub repo `sudhanshu-b/SubSplit`: created 2026-06-06, last push 2026-07-20, 33 commits, 0 stars, TypeScript, Vercel homepage `sub-split-six.vercel.app`. The last commits on 2026-07-20 are "Final deployment" and "peerlist launch added" (observed via GitHub API). — [GitHub sudhanshu-b/SubSplit](https://github.com/sudhanshu-b/SubSplit)
- WHOIS for letssplit.in: Creation Date 2026-06-14, Registrar GoDaddy, Registry Expiry 2027-06-14 (observed via `whois`, 2026-11-06). — [who.is/whois/letssplit.in](https://who.is/whois/letssplit.in)
- Product Hunt shows "Launched 4mo ago" / "Launched in 2026", tagline "Share subscription costs with trusted people", 11 points, 6 comments, pricing "Free", categories Payments, Software Engineering and Streaming Services. Sudhanshu Burile is listed on the launch team. — [Product Hunt: LetsSplit](https://www.producthunt.com/products/letssplit)
- hunted.space records 10 upvotes, 6 comments and #100 on the daily leaderboard, hunted by Sudhanshu Burile. — [hunted.space/product/letssplit](https://hunted.space/product/letssplit)
- Maker's PH post: "This is the first public version, so I'm looking for honest feedback more than anything else." — [Product Hunt: LetsSplit](https://www.producthunt.com/products/letssplit)
- Peerlist project page (categories "Productivity • Finance • SaaS"). It returned HTTP 403 to automated fetch. The search snippet describes "a marketplace for shared subscription plans… hosts can list eligible plans and members can discover, request to join, chat, track payments" and lists ChatGPT, Netflix, YouTube, Claude and Canva. — [Peerlist: LetsSplit](https://peerlist.io/burilesudhanshu/project/letssplit)
- A WebCatalog entry describes it as a subscription-sharing platform for "streaming services, productivity apps, AI tools, cloud storage, and other digital subscriptions" (from the search snippet). — [webcatalog.io/en/apps/letssplit](https://webcatalog.io/en/apps/letssplit)

**Social accounts**
- The About page links only the founder's personal accounts: [GitHub sudhanshu-b](https://github.com/sudhanshu-b), [LinkedIn /in/sudhanshub](https://linkedin.com/in/sudhanshub) and [Peerlist burilesudhanshu](https://peerlist.io/burilesudhanshu). No X, Discord, Telegram or Reddit account for the product was found on the site. — [letssplit.in/about](https://www.letssplit.in/about)

**Disambiguation: other "LetsSplit" / "Let's Split" entities that are NOT this product**
- **"Let's Split" mechanical keyboard.** A well-known open-source split ergonomic keyboard (QMK firmware). Many GitHub repos use the name, e.g. `CampAsAChamp/LetsSplitWindowsGuide` (31★) and `tsukaworks/letssplitcase`. — [GitHub search "letssplit"](https://github.com/search?q=letssplit&type=repositories); [CampAsAChamp/LetsSplitWindowsGuide](https://github.com/CampAsAChamp/LetsSplitWindowsGuide)
- **Bill or expense-splitting hobby apps.** Examples: `Bendername/LetsSplit` ("Easily splitting bills", 2016), `manangoel99/LetsSplit` ("Web App Version of Splitwise made in flask"), `eeshadutta/LetsSplit` ("A Group expense Manager built in Flask") and `godzaryan/LetsSplit` (a Next.js boilerplate at lets-split-three.vercel.app, 2026). — [GitHub search "letssplit"](https://github.com/search?q=letssplit&type=repositories)
- **Other domains.** `letssplit.com` serves a 114-byte page with a JS redirect to `/lander`, which looks like a parked domain with no product. `letssplit.app` returned Cloudflare 403 / "error code: 1050". `letssplit.ai`, `letssplit.io`, `letssplit.co` and `lets-split.ai` did not respond (all observed via curl, 2026-11-06). — [letssplit.com](https://letssplit.com)
- **Similarly named Indian subscription-sharing startup SplitSub** (2021, YourStory). It is a different company. — [YourStory 2021: SplitSub](https://yourstory.com/2021/03/fintech-startup-splitsub-digital-subscription-sharing/amp)
- **Lightsplit** is an expense-splitting app with a Claude connector. It is unrelated. — [lightsplit.com blog](https://lightsplit.com/blog/split-expenses-with-claude-custom-connector)
- **Searches that found no AI-token LetsSplit:** English searches for "LetsSplit AI tokens idle quota", "LetsSplit AI token monetize unused", "letssplit Claude Max share quota tokens platform", "letssplit.ai OR letsplit.ai" and "ClawPool Asale LetsSplit idle tokens", plus the Chinese search "LetsSplit 闲置 token 共享 Claude ChatGPT 拼车". None returned an AI-token or quota-relay product called LetsSplit. Results were either letssplit.in or unrelated token marketplaces such as [TokenBank](https://tokenbank.dev/), [TokenlyPro](https://www.tokenlypro.com/sell) and [AI Token Exchange](https://aitokenexchange.net/index_en).

### Inferences
- **How I disambiguated.** letssplit.in is the only LetsSplit that names ChatGPT and Claude, has an "AI Tools" category and has a testimonial about "Claude code" costs, so it is almost certainly the product the user saw. It is a **subscription-cost carpool board**, not a token-sharing platform. The user's grouping of it with Asale and ClawPool as an "idle token monetization" platform does not match what the product does.
- This is a personal portfolio-grade project by a ~2-year developer: one GitHub follower, 0 stars, 33 commits over about 6 weeks, about 10 PH upvotes, no company and no funding. It should be treated as a weak signal of demand, not as a competitor with traction.
- It is possible the user meant a different product whose name is easy to confuse with this one. TokenBank (Claude Code quota lending), TokenlyPro (sell unused AI tokens) and AI Token Exchange (P2P subscription credits, 5% seller fee per the search snippet) actually match "idle token monetization". I did not research them.

### Gaps
- No founding date, team size beyond the solo builder, funding or legal entity was found. No press coverage or founder interview was found.
- The Peerlist page returned 403, so its upvote count and launch date are unverified.
- I could not confirm the exact Product Hunt launch date. "4mo ago" as of Nov 2026 and the 2026-07-20 "peerlist launch added" commit both point to about mid/late July 2026.

---

## Q2. What is shared or split, and which AI vendors and models?

### Takeaway
What gets split is **a seat on a subscription plan the host already owns** (family-plan slots, or a shared sign-in). There are no tokens, API credits, API keys, OAuth sessions or relayed quota. ChatGPT, Claude, Gemini, Perplexity and GitHub Copilot appear only as logos and in an "AI Tools" category. The site does not explain how any AI plan would actually be shared.

### Cited Findings
- **Services shown** ("Works with the services you love"): Netflix, Spotify, YouTube Premium, Notion, GitHub Copilot, iCloud+, Perplexity, ChatGPT, Claude, Gemini, Apple TV+, 1Password, Dropbox and Duolingo. The browse mock-up has category filters "All / Streaming / Productivity / AI Tools". — [letssplit.in](https://www.letssplit.in/)
- **Access mechanism:** "Access is shared directly by the host through the service's normal sharing features **or a shared sign-in**, and SubSplit never sees, stores, or brokers login credentials." — [letssplit.in](https://www.letssplit.in/)
- **AI-related testimonial:** "Subscription costs for Claude code was adding up quickly. LetsSplit helped me reduce my monthly expenses…" (Advait Deshpande, Software Developer, Pune). Another: "Between AWS, GitHub, AI tools, and streaming subscriptions… LetsSplit helped me find trusted people to share eligible plans." — [letssplit.in](https://www.letssplit.in/)
- **The code-level service catalogue has no AI vendors.** The seed list is Spotify Family, YouTube Premium, Netflix, Apple One, Microsoft 365 Family, Amazon Prime, Disney+ and Max, all family or bundle plans. — [SubSplit db/seed.ts](https://github.com/sudhanshu-b/SubSplit/blob/main/db/seed.ts)
- The data model is purely "seats": a `subscription` listing has `totalSeats`, `priceTotal` and a generated `pricePerSeat = price_total / total_seats`, plus `currency` (default "USD"), `region`, `durationDays`, `activeFrom/activeTill` and `upiId`. There are no fields for credentials, API keys, tokens, quota or usage. — [SubSplit db/schema.ts](https://github.com/sudhanshu-b/SubSplit/blob/main/db/schema.ts)
- **Listing lifecycle supports group-buy:** `subscription_status` = `recruiting` ("accepting members") → `ready_to_purchase` ("minimum members reached, host preparing to buy") → `active` ("subscription purchased, sharing live") → `completed` / `cancelled`. — [SubSplit db/schema.ts](https://github.com/sudhanshu-b/SubSplit/blob/main/db/schema.ts)
- PH and Peerlist descriptions name ChatGPT, Claude and Canva among the "shared subscription plans". — [Product Hunt: LetsSplit](https://www.producthunt.com/products/letssplit); [Peerlist: LetsSplit](https://peerlist.io/burilesudhanshu/project/letssplit)

### Inferences
- **Neither ChatGPT Plus/Pro nor Claude Pro/Max has a consumer family plan.** For those plans, the only way to "split" on LetsSplit would be a shared sign-in, which is account sharing (see Q6). The only legitimate multi-person routes are ChatGPT Business/Team or Claude Team seats, where each member gets their own seat. The `ready_to_purchase` state would fit a host recruiting members to meet a Team plan's minimum seat count before buying. This is inferred from the schema; the site never describes it.
- Nothing on the site or in the code touches the user's product concept: shared leftover 5-hour or weekly quota, overage borrowing, or relayed API calls.

### Gaps
- Live listings are behind a login (see Q4). I could not check whether any real ChatGPT or Claude listings exist, or what access method they state.

---

## Q3. Business model: pricing, take rate, payouts, fees, refunds, guarantees

### Takeaway
It is a pure **matchmaking and coordination board with no revenue model yet**. It is "free to use during beta", takes no platform fee and processes no money. Members pay hosts directly over UPI and upload a transaction reference or screenshot as proof. There is no escrow, refund policy or guarantee. Disputes are left to the users, backed by a review-based trust score and a report button.

### Cited Findings
- FAQ "Is LetsSplit free to use?": "Yes — LetsSplit is free to use **during beta**. There's no platform fee on top of the amount you agree to pay the host for your seat." — [letssplit.in/faq](https://www.letssplit.in/faq)
- FAQ "How do payments work?": "Payment happens directly between you and the host, outside the platform — typically via UPI. The host shares a UPI ID, you pay your share, and either you or the host records the transaction reference (with an optional screenshot as proof) so everyone in the group can see who's paid." — [letssplit.in/faq](https://www.letssplit.in/faq)
- FAQ "Does LetsSplit process or hold any payments?": "No. LetsSplit doesn't process, hold, or guarantee any payment — it only helps you track who's paid within a group." — [letssplit.in/faq](https://www.letssplit.in/faq)
- FAQ "What if a host doesn't provide access after I've paid?": "That's a dispute between you and the host — see the host's reviews and trust score before joining… LetsSplit isn't a party to that payment and can't guarantee or reverse it, but you can report the host so we can review their account." — [letssplit.in/faq](https://www.letssplit.in/faq)
- FAQ "Can I get a refund?": "Refunds are between you and the host… LetsSplit doesn't process payments, so we can't issue refunds ourselves." — [letssplit.in/faq](https://www.letssplit.in/faq)
- Terms: LetsSplit is "a coordination tool". It "is not a party to the subscription contract between any user and any third-party provider" and "does not hold, escrow, or guarantee any payment made between users". It "does not guarantee that a host will honor a listing, keep a subscription active, or refund a member". Prohibited conduct includes "Collect payment for a subscription you have no intention of providing access to." — [letssplit.in/terms](https://www.letssplit.in/terms)
- **Payment terms in code:** `payment_terms` enum = `upfront` | `split_30`. `membership_payment.installmentNumber` is "1 or 2", with `transactionRef` and `proofImageUrl` ("stores a base64 data URL (MVP — move to object storage later)"). Payment status enum: pending / succeeded / failed / refunded. — [SubSplit db/schema.ts](https://github.com/sudhanshu-b/SubSplit/blob/main/db/schema.ts)
- **Example prices** (landing-page mock-up UI, not verified live listings): Netflix Standard with Ads ₹149/mo (2/4 slots), Spotify Duo Premium ₹119/mo (1/2 slots), YouTube Premium Family ₹89/mo (3/5 slots). CTA: "Save up to ₹2,000/month… completely free." — [letssplit.in](https://www.letssplit.in/)
- **Trust score algorithm:** a Bayesian average of reviews with prior 3.5 and weight 5 phantom reviews, plus behavioural adjustments (email/phone verification, host lifecycle, payment history), clamped to 0–5. Users with no reviews get null ("New Host"). — [SubSplit lib/trust-score.ts](https://github.com/sudhanshu-b/SubSplit/blob/main/lib/trust-score.ts); FAQ: "built from reviews… plus signals like completed plans and on-time payments… a helpful signal, not a guarantee." — [letssplit.in/faq](https://www.letssplit.in/faq)

### Inferences
- **Take rate = 0%. Platform revenue = none today.** "During beta" hints that a fee may come later, but nothing is announced.
- Compared with token-relay platforms such as Asale and ClawPool, which sit in the money flow and metering path, LetsSplit carries no financial or operational risk but also captures no value. Its "guarantee" is only social: reviews, trust score and reports.
- `split_30` likely means paying in two instalments (for example 30 days apart). This is inferred from the enum name plus "installment 1 or 2" and is not documented.

### Gaps
- No pricing data for real AI-tool listings (for example the price per member of a ChatGPT Team or Claude seat). Live listings require login.
- No stated future monetization plan.

---

## Q4. Technical architecture

### Takeaway
LetsSplit is a standard CRUD web app (Next.js + Supabase Postgres + Drizzle + Better Auth on Vercel) with listings, join requests, chat, payment-proof tracking, reviews and admin moderation. There is **no relay, proxy endpoint, local client, credential vault, team-invite automation, metering or usage tracking**. Credentials never pass through the platform, because access is handed over off-platform by the host.

### Cited Findings
- Stated stack: "Next.js, TypeScript, PostgreSQL, Supabase, Drizzle ORM, Tailwind CSS, Framer Motion, Better Auth". — [letssplit.in/about](https://www.letssplit.in/about)
- Source is public on GitHub (no licence file observed), deployed on Vercel (`sub-split-six.vercel.app`). — [GitHub sudhanshu-b/SubSplit](https://github.com/sudhanshu-b/SubSplit)
- API routes (observed via GitHub tree): `/api/listings`, `/api/listings/[id]/join`, `/requests/[memberId]`, `/members/[memberId]/remind`, `/reviews`, `/api/memberships/[id]/payments`, `/leave`, `/api/messages`, `/api/browse`, `/api/profile/phone`, `/api/admin/users/[id]/suspend`, `/api/admin/testimonials` and `/api/feedback`. DB tables: app_user, service, subscription, membership, membership_payment, review, conversation, message, report, testimonial, feedback, plus Better Auth session/account/verification tables. — [GitHub sudhanshu-b/SubSplit](https://github.com/sudhanshu-b/SubSplit); [db/schema.ts](https://github.com/sudhanshu-b/SubSplit/blob/main/db/schema.ts)
- Credentials stance: the platform "never sees, stores, or brokers login credentials". — [letssplit.in](https://www.letssplit.in/)
- `GET https://www.letssplit.in/api/browse` returns `{"error":"Unauthorized"}` without a session, so listings are login-gated (observed 2026-11-06). — [letssplit.in/api/browse](https://www.letssplit.in/api/browse)
- The "Changed pooling" commit (2026-07-14) touched `db/index.ts`, the admin page and the navbar. It is about **database connection pooling**, not token or quota pooling (observed in the commit diff). — [GitHub sudhanshu-b/SubSplit commits](https://github.com/sudhanshu-b/SubSplit/commits/main)
- Google Analytics is embedded ("gtag code updated", "Landing page and google anlytics added"). — [GitHub sudhanshu-b/SubSplit commits](https://github.com/sudhanshu-b/SubSplit/commits/main)

### Inferences
- **For the user's question list:** OpenAI- or Anthropic-compatible endpoints, Claude Code or Codex CLI integration, per-user quota isolation, 5-hour or weekly limit tracking, prompt privacy and abuse controls are all **not applicable**, because none exist. The only abuse controls are email/phone verification, host approval of joiners, reviews, reports and admin "suspend user".
- Architecturally, LetsSplit resembles a Together Price, Spliiit or GamsGo-style "carpool coordination" site, not an Asale or ClawPool-style "quota relay".

### Gaps
- No docs, install steps, mobile app, Chrome extension or SDK exist or were found.

---

## Q5. Target users and traction

### Takeaway
The audience is Indian (₹ prices, UPI payments, Pune testimonials) students, developers and families who want to cut subscription costs. There is **no verifiable traction**. The "247 listings available" on the homepage is a hard-coded mock-up string, and the public signals are about 10–11 Product Hunt upvotes, 6 comments and a 0-star repo.

### Cited Findings
- Target framing: "Whether you're a student, a developer, or a family — you shouldn't have to pay full price alone." Trust: "Email-verified accounts, in-app messaging, and UPI-based payments." — [letssplit.in/about](https://www.letssplit.in/about)
- "247 listings available" is hard-coded in the landing page mock-up (`components/landing-page.tsx`, line ~359: `<p …>247 listings available</p>`), so it is not a live count. — [SubSplit components/landing-page.tsx](https://github.com/sudhanshu-b/SubSplit/blob/main/components/landing-page.tsx)
- Homepage testimonials come from people in Pune and other developers ("Abhishek Pune, Geometry Dev, Pune", "Advait Deshpande, Software Developer, Pune", "Om Suple, DevOps Engineer"). Testimonials are admin-managed via `/api/admin/testimonials`. — [letssplit.in](https://www.letssplit.in/); [GitHub tree](https://github.com/sudhanshu-b/SubSplit)
- Product Hunt: 11 points, 6 comments. Commenters asked how connection or access problems are handled and requested renewal reminders, regional filtering and verified payment history. — [Product Hunt: LetsSplit](https://www.producthunt.com/products/letssplit). hunted.space: 10 upvotes, #100 daily. — [hunted.space](https://hunted.space/product/letssplit)
- No product presence was found on Hacker News, Reddit, V2EX, linux.do, X, Discord or Telegram in the searches run (see the Q1 search list). — [letssplit.in/about](https://www.letssplit.in/about) (only personal socials linked)

### Inferences
- Real usage is probably minimal (friends and early testers). Given the hard-coded stats and Pune-centric testimonials, the homepage numbers should not be quoted as traction.
- The Claude Code testimonial suggests Indian developers do want cheaper Claude access. That is anecdotal support for demand, but LetsSplit does not solve it in a ToS-compliant way.

### Gaps
- No user counts, listing counts, GMV or retention data are available. The browse API requires authentication.

---

## Q6. Risks: ToS compliance, bans, complaints, scams

### Takeaway
For AI tools specifically, LetsSplit's "shared sign-in" option conflicts directly with Anthropic's consumer terms, and very likely with OpenAI's. LetsSplit pushes all compliance, ban, non-delivery and refund risk onto users. Because there is no escrow, the scam surface (paying a host who never delivers) is borne entirely by members.

### Cited Findings
- Anthropic Consumer Terms (effective Oct 8, 2025): "You may not share your Account login information, Anthropic API key, or Account credentials with anyone else." "You also may not make your Account available to anyone else." Section 3 also bars users who would "…resell the Services." — [Anthropic Consumer Terms](https://www.anthropic.com/legal/consumer-terms)
- A secondary source says one Claude Max plan is one seat for one person, and that two people on one login share one allowance and one chat history. — [SSD Nodes: What one Claude Max plan covers](https://www.ssdnodes.com/learn/what-one-claude-max-plan-covers)
- A secondary 2026 guide says ChatGPT Plus, Claude Pro and Perplexity Pro are single-user, and that sharing logins violates their terms and can trigger suspension. — [krater.ai: Can you share an AI subscription?](https://krater.ai/blog/share-ai-subscription-multiple-users)
- LetsSplit FAQ: "we don't review listings against any third-party service's terms of service, and listing something on LetsSplit isn't a representation that it's compliant." "It's your responsibility to check the provider's own terms before listing or joining a plan." — [letssplit.in/faq](https://www.letssplit.in/faq)
- Terms: consequences of sharing against a provider's rules "are between you and that provider". LetsSplit "is not responsible for, and will not compensate you for, any such outcome". Users must indemnify LetsSplit for claims arising from third-party-terms violations, and losses of account access or of money paid to other users are excluded from liability. — [letssplit.in/terms](https://www.letssplit.in/terms)
- No public complaints, scam reports or ban reports specific to LetsSplit were found in any search (see the Q1 search list).

### Inferences
- For a token-sharing product, LetsSplit is a cautionary example of "we only coordinate" positioning. It reduces platform liability, but it gives buyers no protection and does nothing to keep the underlying account sharing within the terms.
- The lack of complaints most likely reflects low usage, not safety.

### Gaps
- I could not re-verify OpenAI's Terms of Use account-sharing clause in this session, because openai.com returned 403 to automated fetches. The clause should be checked at [openai.com/policies/row-terms-of-use](https://openai.com/policies/row-terms-of-use/). Its wording reportedly bars sharing account credentials or making the account available to others; this is unverified here.
- No data was found on whether any LetsSplit user has actually been banned by OpenAI or Anthropic.

---

## Q7. Relationship to Asale and ClawPool (the "idle token monetization" peers)

### Takeaway
No relationship, mention or comparison was found between LetsSplit and Asale or ClawPool. They belong to different categories. Asale and ClawPool are **quota-relay or token-pooling networks** that sit in the request path and meter usage. LetsSplit is an **off-platform seat-splitting board**.

### Cited Findings
- Search for "ClawPool Asale LetsSplit idle tokens" returned Asale and ClawPool pages but nothing about LetsSplit. — [Show HN: ClawPool](https://news.ycombinator.com/item?id=46974109); [Asale](https://asale.ai/en)
- ClawPool (Show HN, per search snippet): subscribers pool OAuth tokens and earn up to $120/mo from spare capacity. Its proxy bills after the response streams. HN commenters warned about Anthropic bans and resale prohibitions. — [Show HN: ClawPool](https://news.ycombinator.com/item?id=46974109); [clawpool-proxy-function](https://github.com/peter-jammable/clawpool-proxy-function)
- Asale (per search snippet): "a market for real users' idle subscription quota". A desktop client lets users sell idle Claude, ChatGPT/Codex, Gemini, Kimi and Grok subscription quota for USDT or buy capacity on hitting rate limits. — [asale.ai/en](https://asale.ai/en); [asale-ai/asale releases](https://github.com/asale-ai/asale/releases/tag/v1.2.3)

### Inferences
- **For the user's competitive map,** LetsSplit belongs in an adjacent "subscription carpool / 拼车 coordination" bucket (with Together Price, Spliiit, GamsGo and the Chinese ChatGPT/Claude 合租 sites), not in the "idle token monetization" bucket. It is not a direct competitor to a share-leftover-quota or borrow-overage product. Its only relevance is evidence of demand from Indian developers for cheaper Claude and ChatGPT access, and a minimal trust and reputation design (Bayesian trust score, payment-proof tracking) that a token-sharing product could borrow.

### Gaps
- Asale and ClawPool details above come from search snippets only and are not verified here. Other researchers are covering them.
