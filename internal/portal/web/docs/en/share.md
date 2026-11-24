# Share a model

Share a model on your machine with the network and earn credit for the tokens actually served. You do not need a public IP or open ports.

> [!WARNING]
> **Running a model requires decrypting the request, so your machine can see the prompts that others send.** Do not log or store them. Share only models that you have the right to share.

## Step 1: Prepare a model server {#server}

By default the client works with Ollama on your machine. For other OpenAI-compatible model servers, see [Use another model server](#other-server). To share a service you have an account for at a model provider, see [Share a hosted API](#providers).

::: macos
```bash
brew install ollama
brew services start ollama
ollama pull gemma4:e4b
```

You can also download the app from [ollama.com](https://ollama.com/download). Once opened, it keeps running in the background.
:::

::: linux
```bash
curl -fsSL https://ollama.com/install.sh | sh
ollama pull gemma4:e4b
```

The install script registers Ollama as a systemd service that starts at boot.
:::

::: windows
```powershell
winget install Ollama.Ollama
ollama pull gemma4:e4b
```

You can also download the installer from [ollama.com](https://ollama.com/download). It runs in the notification area and starts when you log in to Windows.
:::

Check that the client can see the model:

```bash
{{exe}} share detect
```

## Step 2: Add a model {#add}

```bash
{{exe}} share add gemma4:e4b -id gemma-4-e4b
```

| Option | Meaning | Default |
|---|---|---|
| `-id` | The model's ID on the network. Buyers use it to call the model | Same as the local model name |
| `-in` | Price per million input tokens, in micro-credit | `100000` (0.1 credit) |
| `-out` | Price per million output tokens, in micro-credit | `200000` (0.2 credit) |
| `-cache-read` | Price per million input tokens the model server reads from its prompt cache | A tenth of `-in` |
| `-cache-write` | Price per million input tokens the model server writes to its prompt cache | 1.25 × `-in` where the model server speaks the Anthropic Messages API (Anthropic, and DeepSeek with a key), else `-in` |

> [!TIP]
> If other nodes already share the same model, use the same ID so buyers can select you together with them. Run `{{exe}} models` first to see the existing IDs and prices. The coordinator picks nodes by price, reputation, latency and success rate, and price has the highest weight.

Adding a model turns sharing on automatically.

## Step 3: Go online and take jobs {#up}

```bash
{{exe}} up
```

If `up` is already running, `share` commands take effect immediately, with no restart. To take jobs over a long time, [run in the background](/docs/running#background).

Check the status:

```bash
{{exe}} status
```

If you see `sharing 1 model(s): ...`, you are online. If there is an `offer not accepted: ...` line, it gives the reason for the rejection.

## Check earnings {#earnings}

```bash
{{exe}} balance
{{exe}} jobs -role sell -n 20
```

- **Settlement:** A job is final as soon as the buyer confirms it (Ack). A job the buyer does not confirm becomes final after the 24-hour dispute window. The earnings in `balance` count final jobs only; the number of jobs not final yet is shown as `pending`.
- **Fee:** The platform takes 5% of each job; the earnings shown are after the fee. Jobs between [partners](#partners) cost nothing and earn nothing.
- The portal's [jobs](/app/#/jobs) and [ledger](/app/#/ledger) pages have the details.

## Manage sharing {#manage}

| Action | Command |
|---|---|
| View settings | `{{exe}} share` |
| List the local models | `{{exe}} share detect` |
| Change a price (add the same ID again) | `{{exe}} share add gemma4:e4b -id gemma-4-e4b -in 80000 -out 150000` |
| Remove a model | `{{exe}} share rm gemma-4-e4b` |
| Pause taking jobs | `{{exe}} share off` |
| Resume taking jobs | `{{exe}} share on` |

## Use another model server {#other-server}

Any local service with an OpenAI-compatible interface works, such as LM Studio, vLLM or llama.cpp:

```bash
{{exe}} share upstream http://127.0.0.1:1234/v1
{{exe}} share detect
{{exe}} share add my-local-model -id my-model
```

If the local model server needs an API key, put the key in an environment variable and write the variable's name in `share.upstream_key_env` in the [configuration file](/docs/config#share).

- **Chat completions only:** the client uses only `/v1/models` and `/v1/chat/completions` of the local model server. Requests buyers send from Claude Code (Anthropic Messages) or Codex (OpenAI Responses) are converted on your machine.
- **A model server on another machine:** use an `https://` address unless it is this machine or a local network IP. Otherwise prompts travel in plain text, and `{{exe}} status` warns about it.

## Share a hosted API {#providers}

Besides models that run on your machine, you can share a service you have an account for at a model provider: the client forwards buyers' requests to the provider and the answers back, converting between the providers' formats (see "Three APIs" below). Each such source is called a provider. You can set up several and turn each on or off. Local models and providers' models can be shared at the same time.

> [!WARNING]
> Requests go to the provider in plain text, and the usage counts against your account there. Check that the provider's terms allow this use, and set your prices above your cost.

| Kind | API key | Login (OAuth) |
|---|---|---|
| `anthropic` | Yes | Yes (Claude subscription) |
| `openai` | Yes | Yes (ChatGPT account, Codex) |
| `gemini` | Yes | Yes (Google account) |
| `antigravity` | — | Yes (Google account) |
| `grok` | Yes | Yes |
| `deepseek` | Yes | — |
| `custom` | Yes: any OpenAI-compatible API, with its address in `-url`; leave the key empty if the API needs none | — |

### Add one with an API key {#provider-key}

```bash
{{exe}} provider add deepseek
```

The client asks for the API key (it is not shown as you type), asks the provider whether the key works, and saves it:

```text
DeepSeek API key:
saved provider deepseek (DeepSeek, apikey) sk-…9b35
   models: deepseek-flash, deepseek-v4-pro
   balance 327.18 CNY
```

Then add the models to share and go online:

```bash
{{exe}} share add deepseek-flash -provider deepseek -in 100000 -out 200000
{{exe}} up
```

For any other OpenAI-compatible API, use `custom`. To set up several accounts of one kind, give each a name with `-name`:

```bash
{{exe}} provider add custom -name myapi -url https://api.example.com/v1
{{exe}} provider add openai -name openai-team2
```

- **In scripts:** without a terminal, the client reads one line from standard input. You can also put the key in the environment variable `SHARETOKEN_PROVIDER_KEY`. There is no command-line argument for it, so that the key does not stay in your shell history.
- **An Anthropic key that is not tied to a workspace:** add `-header anthropic-workspace-id=WORKSPACE_ID`.
- **A model's output limit:** when the provider refuses a request because of the output limit, the client sends it again within the limit the provider named, and keeps to it afterwards. To set a limit in advance, use `-max-output` in `share add`.
- **Hidden reasoning is billed within the output limit:** some models do not return all of their reasoning (encrypted, summarized or omitted), but the provider counts it as usage. When the answer shows that the model reasoned, buyers pay the reported output tokens up to the job's output limit; past it, and for output nobody can see in an answer without reasoning, they do not. `{{exe}} provider test` shows the output tokens the provider counted and how many of them buyers would pay for at most.
- **Three APIs:** buyers call in chat completions, Anthropic Messages (Claude Code) or OpenAI Responses (Codex). A request your provider speaks natively goes to it unchanged; any other one is converted on your machine. Native requests are matched to you first: an Anthropic provider gets Claude Code's requests, a ChatGPT login Codex's, an OpenAI or DeepSeek key both chat and their own API.

### Cache prices {#cache-prices}

Model servers charge less for input they read from their prompt cache, and Anthropic charges more for input it writes to the cache. A buyer pays your cache prices for those tokens, as the provider reported them: set them with `-cache-read` and `-cache-write` in `share add`, or `price_cache_read` / `price_cache_write` in the configuration file. The defaults (a tenth of the input price for reads; 1.25 times it for writes where the model server speaks the Anthropic Messages API, the input price elsewhere) follow the providers' own ratios.

### Add one with a login {#provider-login}

```bash
{{exe}} provider login openai
```

The client shows a sign-in address. Open it in a browser on any computer and sign in. Then, as the client tells you, paste back the authorization code, or the whole address of the page the browser ends on (that page does not load). The login is renewed automatically. Claude and ChatGPT logins can have their 5-hour windows warmed up at set times each day; see [Warm-up](/docs/cli#warmup).

- **Claude subscription:** `{{exe}} provider login anthropic`. Add `-mode inference` to ask only for permission to call models; such a login may not be able to list models, so name one with `-model` in `provider test`. You can also add `-session-key` and paste the `sessionKey` cookie of claude.ai from your browser (it is not shown as you type, and it is used only to authorize, never saved).
- **ChatGPT:** `provider login openai` signs in with a ChatGPT account and calls models the way Codex does.
- **Google:** if a Google account has no project of its own, `gemini` and `antigravity` need a Google Cloud project ID in `-project`.
- **How long a sign-in address lasts:** finish within 30 minutes of starting, or run `provider login` again.

### Manage providers {#provider-manage}

| Action | Command |
|---|---|
| List the kinds and the providers you set up | `{{exe}} provider` |
| List the models a provider can use | `{{exe}} provider models deepseek` |
| Send one test request | `{{exe}} provider test deepseek -model deepseek-flash` |
| Pause or resume a provider | `{{exe}} provider off deepseek`, `{{exe}} provider on deepseek` |
| Change the key (add it again under the same name; the models stay) | `{{exe}} provider add deepseek` |
| Warm up a Claude or ChatGPT login at set times each day | `{{exe}} provider warmup openai -times 06:00,11:05` |
| Reach the provider through a proxy (`-proxy-mode direct` for none) | `{{exe}} provider configure openai -proxy http://127.0.0.1:8080` |
| Delete a provider and its credentials | `{{exe}} provider rm deepseek` |

- **Where credentials are kept:** keys and logins are in `providers.json` in the state directory, apart from `config.json`. Only your account can read the file. Neither the commands nor the local API show a whole key.
- **Refused credentials or an empty account:** jobs fail, and `{{exe}} status` shows `warning: deepseek refused its credentials ...` or `... is out of balance ...`. Failed jobs count against your node, so fix it soon. When the provider answers HTTP 401, the provider rests for 10 minutes, and its models are left out of your offer meanwhile.
- **Rate limits (HTTP 429):** the provider stops taking jobs until the reset time the provider gives (one minute when it gives none); its models are left out of your offer until then and come back by themselves. `status` shows `... is temporarily rate limited (HTTP 429)`. A 429 about one request only (the request alone is larger than the allowance per minute, or past the model's context) fails that job and rests nothing. The rest is kept only while the client runs; a restart clears it.

## Share with partners for free {#partners}

There are two ways to share. One is buying and selling: buyers pay credits and sellers earn them. Pairing with your buyers (below) is part of it; it only narrows who buys from whom. The other is share partners: two people pair up as partners and each uses the other's nodes **for free**: nothing is held or charged, and there is no platform fee. It suits two people who each have some models or API quota and want to cover for each other.

One makes an invite (it prints a code such as `ABC-DEF` and an [invite link](#invite-links), good for 24 hours unless you pass `-ttl`, 168 hours at most), the other enters the code or the link:

```bash
{{exe}} partner invite
{{exe}} partner join ABC-DEF
```

From then on, when you ask for a model, the coordinator first looks among your partners' online nodes. When one of them can take the request, it is free. When your partners do not offer the model, are offline or busy, or used up what they share, the request goes to the network and is paid as usual.

| Action | Command |
|---|---|
| See your partners, the tokens you gave and used in the last 24 hours, open invites and your settings | `{{exe}} partner` |
| Use partners' nodes only, never buy on the network | `{{exe}} partner set -only on` (`-only off` to undo) |
| The tokens your nodes give all partners per 24 hours | `{{exe}} partner set -limit 200000` (`0`: no limit) |
| The percent of each of your nodes' concurrent jobs partners may take | `{{exe}} partner set -share 50` (`100`: no limit, `0`: pause sharing) |
| Withdraw an open invite | `{{exe}} partner revoke 7` |
| Stop sharing with a partner | `{{exe}} partner rm 42` |
| Approve, or reject, someone waiting for your approval | `{{exe}} partner approve 5`, `{{exe}} partner reject 5` |
| Withdraw your own request that waits for the other's approval | `{{exe}} partner withdraw 5` |

- **The limits cover what you give:** the daily limit counts the last 24 hours, all partners together. A job counts once it ends, so jobs still running when the limit is reached can take it a little past. The share is counted per node and is at least one job: at 4 concurrent jobs and 50%, partners take at most 2 at a time.
- **Past the limits:** your partners buy on the network instead, where they may be matched to your nodes again, paying your price.
- **Partners only:** when no partner node can take a request, it fails and costs nothing. Available models then lists only your partners' (and your own).
- **Reputation:** jobs between partners do not count toward your nodes' record or reputation on the market.
- **How many:** an invite works once, unless you make it for more people (`-uses`, or "Number of people who can join" on the portal, up to 100); each who joins becomes your partner, not the others'. An account can have up to 100 partners at a time.
- **Approval:** make the invite with `-approve` ("Require my approval" in the app and the portal) and whoever uses it waits until you approve. `partner` lists who waits; a request lapses after 7 days.
- **Who sees what:** part of your partner's account name, and the tokens each of you used of the other in the last 24 hours.
- **Ending it:** either side can end it at any time; then neither uses the other's nodes for free any more.
- **Private mode:** the private mode of pairing (below) only concerns paying buyers. It does not affect partners.

In the desktop app it is under **Pairing → Share partners**.

## Pair with your buyers {#pairing}

Pairing ties buyers to you. Make a pairing code and hand it to the people you want as buyers. Once a buyer joins with the code, that buyer's requests go only to the sellers it is paired with, on all of its devices. One code admits as many buyers as you allow, so a team can share one.

```bash
{{exe}} pair code -label team
```

The command prints the code, for example `ABC-DEF`, and its [invite link](#invite-links). It lasts 24 hours unless you pass `-ttl` (7 days at most), and admits any number of buyers unless you pass `-uses`. Each buyer then opens the link, or runs:

```bash
{{exe}} pair join ABC-DEF
```

| Action | Command |
|---|---|
| See your buyers, the sellers you are paired with, and your codes | `{{exe}} pair` |
| Remove one of your buyers | `{{exe}} pair rm 42` |
| Stop a code from admitting more buyers | `{{exe}} pair revoke 7` |
| Serve only your paired buyers (private mode), or anyone again | `{{exe}} pair private on`, `{{exe}} pair private off` |
| As a buyer, end the pairing with a seller | `{{exe}} pair leave 42` |
| Approve, or reject, a buyer waiting for your approval | `{{exe}} pair approve 5`, `{{exe}} pair reject 5` |
| As a buyer, withdraw a request that waits for the seller's approval | `{{exe}} pair withdraw 5` |

- **What a paired buyer sees:** only the models of the sellers it is paired with. When none of them offers a model, or all of them are offline, the request fails. It is not sent to another seller.
- **What does not change:** prices, billing, receipts and audits work as for any other job. Pairing alone does not close you to others: buyers that are not paired with anyone can still be matched to you.
- **Private mode:** turn it on and only the buyers paired with you are matched to you and see your models. Everyone else is sent elsewhere. Turn it off to sell to anyone again.
- **A buyer's limit:** one buyer can be paired with at most three sellers at a time.
- **Approval:** make the code with `-approve` ("Require my approval" in the app and the portal) and each buyer that uses it waits until you approve. `pair` lists who waits; a request lapses after 7 days, and approving it takes a use of the code.
- **Who sees what:** you see part of each buyer's account name, and they see part of yours.
- **Ending it:** either side can end a pairing at any time. A buyer with no pairings left buys from any seller again.

The desktop app has the same under **Pairing → My buyers** for sellers and **Pairing → My sellers** for buyers, and so does the portal's [pairing page](/app/#/pairing).

## Invite links {#invite-links}

Every pairing code and share invite also has a link on the portal's site, `https://sharetoken.app/p#7kQm3xV9` for a pairing code and `https://sharetoken.app/s#…` for a share invite. The command line prints it next to the code; the desktop app and the portal have **Copy link** and **Copy invitation message** (the link, the code and how long they last, ready to paste into a chat).

The link works for someone who has nothing installed yet. Its page says who invites them (their account name, partly hidden) and how long the link lasts. They sign in or sign up there with their email address and click **Accept invite**: the pairing, or the partnership, is made on their account at once. The page then offers the app for their system; once they install it and sign in with the same address, it is ready.

Someone who already has the desktop app can click **Open in ShareToken** on the page instead. The browser asks whether to open the app, and the app shows who invites and asks before it accepts anything. On a phone the page explains that ShareToken runs on computers; inside WeChat and similar chat apps it asks to open the link in a browser, since they block downloads and opening apps.

A link can also be pasted wherever a code goes: in the app, on the portal's pairing page, or on the command line:

```bash
{{exe}} accept https://sharetoken.app/p#7kQm3xV9
```

- **Treat it like the code:** whoever has the link can use it, within the code's limits. Revoke the code and the link stops working too.
- **One portal:** a link belongs to the portal that made it; an app signed in to another portal says so and does not accept it.
- **Guessing:** a code is 6 characters, to be easy to type, and a link 8. Because a code is short, wrong codes and links are limited hard: 5 per 15 minutes and 15 a day per account (3 in all for an account in its first day), and a cap per address. When too many wrong codes come in from the whole site, codes stop working for everyone for up to an hour; the link keeps working, so send the link when you can.

## Concurrency and performance {#tuning}

The number of requests handled at the same time is 2 by default, set by `share.concurrency` in the configuration file. Adjust it to what your machine can handle, then run this command to apply the change:

```bash
{{exe}} share on
```

## How cheating is prevented {#audit}

The platform checks nodes at random. A check is carried by another user's client, or sent from a fresh identity through one of the network's relays, and looks like any buyer's request. A reference copy of the model checks the answers token by token, and the usage is checked too; for models whose reference cannot score tokens (Claude and Gemini, for example) only the usage is checked. A clear mismatch costs reputation, and after two in a row the node is not matched for 24 hours.

Make sure the model you share matches its ID, and do not overstate usage: buyers recompute the hash and check the usage themselves, and can get a refund with the evidence.

**Reputation:** each node's reputation runs from 0 to 1000 and starts at 500. A settled job adds 1, a failed job takes 5, a lost dispute 50; a passed check adds 5, a failed one takes 100. When matching, price weighs 45%, reputation 25%, latency and success rate 15% each.
