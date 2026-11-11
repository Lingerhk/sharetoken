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

- **Settlement:** A job is settled after the buyer confirms it (Ack). A job the buyer does not confirm settles automatically after the 24-hour dispute window. So earnings may show up in `pending` first.
- **Fee:** The platform takes 5% of each job.
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

## Share a hosted API {#providers}

Besides models that run on your machine, you can share a service you have an account for at a model provider: the client forwards buyers' requests to the provider and the answers back. Buyers always use the OpenAI format; the client converts between it and each provider's own. Each such source is called a provider. You can set up several and turn each on or off.

> [!WARNING]
> Requests go to the provider in plain text, and the usage counts against your account there. Check that the provider's terms allow this use, and set your prices above your cost.

| Kind | API key | Login (OAuth) |
|---|---|---|
| `anthropic` | Yes | — |
| `openai` | Yes | Yes (ChatGPT account) |
| `gemini` | Yes | Yes (Google account) |
| `antigravity` | — | Yes (Google account) |
| `grok` | Yes | Yes |
| `deepseek` | Yes | — |
| `custom` | Yes: any OpenAI-compatible API, with its address in `-url` | — |

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
- **Hidden reasoning is not billed:** some models do not return their reasoning, but the provider still counts it as usage. Buyers pay only for the output they receive, so these tokens earn nothing. `{{exe}} provider test` tells you when this happens. Take it into account in your prices.

### Add one with a login {#provider-login}

```bash
{{exe}} provider login openai
```

The client shows a sign-in address. Open it in a browser on any computer and sign in. Then, as the client tells you, paste back the authorization code, or the whole address of the page the browser ends on (that page does not load). The login is renewed automatically.

If a Google account has no project of its own, `gemini` and `antigravity` need a Google Cloud project ID in `-project`.

### Manage providers {#provider-manage}

| Action | Command |
|---|---|
| List the kinds and the providers you set up | `{{exe}} provider` |
| List the models a provider can use | `{{exe}} provider models deepseek` |
| Send one test request | `{{exe}} provider test deepseek -model deepseek-flash` |
| Pause or resume a provider | `{{exe}} provider off deepseek`, `{{exe}} provider on deepseek` |
| Change the key (add it again under the same name; the models stay) | `{{exe}} provider add deepseek` |
| Delete a provider and its credentials | `{{exe}} provider rm deepseek` |

- **Where credentials are kept:** keys and logins are in `providers.json` in the state directory, apart from `config.json`. Only your account can read the file. Neither the commands nor the local API show a whole key.
- **Refused credentials or an empty account:** jobs fail, and `{{exe}} status` shows `warning: deepseek refused its credentials ...` or `... is out of balance ...`. Failed jobs count against your node, so fix it soon.

## Pair with your buyers {#pairing}

Pairing ties buyers to you. Make a pairing code and hand it to the people you want as buyers. Once a buyer joins with the code, that buyer's requests go only to the sellers it is paired with, on all of its devices. One code admits as many buyers as you allow, so a team can share one.

```bash
{{exe}} pair code -label team
```

The command prints the code, for example `ABCD-EFGH-IJKL-MNOP`. It lasts 24 hours unless you pass `-ttl` (30 days at most), and admits any number of buyers unless you pass `-uses`. Each buyer then runs:

```bash
{{exe}} pair join ABCD-EFGH-IJKL-MNOP
```

| Action | Command |
|---|---|
| See your buyers, the sellers you are paired with, and your codes | `{{exe}} pair` |
| Remove one of your buyers | `{{exe}} pair rm 42` |
| Stop a code from admitting more buyers | `{{exe}} pair revoke 7` |
| Serve only your paired buyers (private mode), or anyone again | `{{exe}} pair private on`, `{{exe}} pair private off` |
| As a buyer, end the pairing with a seller | `{{exe}} pair leave 42` |

- **What a paired buyer sees:** only the models of the sellers it is paired with. When none of them offers a model, or all of them are offline, the request fails. It is not sent to another seller.
- **What does not change:** prices, billing, receipts and audits work as for any other job. Pairing alone does not close you to others: buyers that are not paired with anyone can still be matched to you.
- **Private mode:** turn it on and only the buyers paired with you are matched to you and see your models. Everyone else is sent elsewhere. Turn it off to sell to anyone again.
- **A buyer's limit:** one buyer can be paired with at most three sellers at a time.
- **Who sees what:** you see part of each buyer's account name, and they see part of yours.
- **Ending it:** either side can end a pairing at any time. A buyer with no pairings left buys from any seller again.

The desktop app has the same under **Sharing → Pairing** for sellers and **Overview → Paired sellers** for buyers, and so does the **Pairing** page of the web portal.

## Concurrency and performance {#tuning}

The number of requests handled at the same time is 2 by default, set by `share.concurrency` in the configuration file. Adjust it to what your machine can handle, then run this command to apply the change:

```bash
{{exe}} share on
```

## How cheating is prevented {#audit}

The coordinator places random jobs under a new identity and recomputes them on a reference model to compare. If two checks in a row do not match, the node is paused from taking jobs for 24 hours. Make sure the model you share matches its ID, and do not overstate usage: buyers recompute the hash themselves and can get a refund with the evidence.
