# Call models

While the client runs, it serves an endpoint on your machine that speaks the APIs of OpenAI (chat completions and Responses) and Anthropic (Messages), so OpenAI clients, Claude Code and Codex can all use it. Your request is encrypted and sent only to the selected node. The response carries the node's signed receipt, and the client checks it automatically.

## Local endpoint {#endpoint}

| Setting | Value |
|---|---|
| Base URL | `http://127.0.0.1:{{port}}/v1` |
| API key | This client's key, `sk-st-…`: `{{exe}} apikey -q` prints it, and the desktop app has it next to the endpoint |
| Interfaces | `POST /v1/chat/completions` (OpenAI), `POST /v1/messages` (Anthropic, Claude Code), `POST /v1/responses` (OpenAI Responses, Codex; also as a WebSocket), `GET /v1/models` |

First make sure `{{exe}} up` is running, or [run it in the background](/docs/running#background).

The key keeps other programs and users of your machine from spending your credit through the endpoint. It stays the same until you make a new one, so a tool needs it only once:

::: macos linux
```bash
export OPENAI_API_KEY=$({{exe}} apikey -q)
```
:::

::: windows
```powershell
$env:OPENAI_API_KEY = ({{exe}} apikey -q)
```
:::

| Command | What it does |
|---|---|
| `{{exe}} apikey` | Show the endpoint, the key, and whether it is required |
| `{{exe}} apikey rotate` | Make a new key. The old one stops working at once |
| `{{exe}} apikey off` | Let any request in without the key. Only on a machine nobody else uses |
| `{{exe}} apikey on` | Require the key again |

The desktop app has the same switch and **New key** under **Settings**. A running client follows the change at once. Without the key the endpoint answers 401 `invalid_api_key`; it also accepts the key in an `x-api-key` header.

## List the models {#models}

```bash
{{exe}} models
```

```text
MODEL        NODES  TEE  MIN IN/Mtok  MIN OUT/Mtok
gemma-4-e4b  2      0    100000       200000
```

`NODES` is the number of online nodes, and `TEE` is how many of them are TEE nodes. Prices are in micro-credit per million tokens. The prices above work out to 0.1 credit per million input tokens and 0.2 credit per million output tokens.

## Send a request {#request}

::: macos linux
```bash
curl http://127.0.0.1:{{port}}/v1/chat/completions \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"gemma-4-e4b","messages":[{"role":"user","content":"Hello"}],"max_tokens":200}'
```

Add `-i` to see the response headers.
:::

::: windows
Quoting is easy to get wrong in PowerShell, so `Invoke-RestMethod` is the simplest choice.

```powershell
$body = @{
  model = 'gemma-4-e4b'
  max_tokens = 200
  messages = @(@{ role = 'user'; content = 'Hello' })
} | ConvertTo-Json -Depth 5
$r = Invoke-RestMethod http://127.0.0.1:{{port}}/v1/chat/completions -Method Post -Headers @{ Authorization = "Bearer $env:OPENAI_API_KEY" } -ContentType 'application/json; charset=utf-8' -Body ([Text.Encoding]::UTF8.GetBytes($body))
$r.choices[0].message.content
```

To see the response headers, use `Invoke-WebRequest` instead of `Invoke-RestMethod` and read `.Headers` on the result.
:::

## Use with Python {#python}

::: macos linux
```bash
pip install openai
```
:::

::: windows
```powershell
py -m pip install openai
```
:::

```python
from openai import OpenAI

# api_key comes from $OPENAI_API_KEY (see above)
client = OpenAI(base_url="http://127.0.0.1:{{port}}/v1")

reply = client.chat.completions.create(
    model="gemma-4-e4b",
    messages=[{"role": "user", "content": "Explain quantum tunnelling in one sentence"}],
    max_tokens=500,
)
print(reply.choices[0].message.content)

stream = client.chat.completions.create(
    model="gemma-4-e4b",
    messages=[{"role": "user", "content": "Write a short poem"}],
    max_tokens=500,
    stream=True,
)
for chunk in stream:
    if chunk.choices and chunk.choices[0].delta.content:
        print(chunk.choices[0].delta.content, end="", flush=True)
```

## Use with Node.js {#node}

```bash
npm install openai
```

```javascript
import OpenAI from "openai";

// apiKey comes from $OPENAI_API_KEY (see above)
const client = new OpenAI({ baseURL: "http://127.0.0.1:{{port}}/v1" });

const reply = await client.chat.completions.create({
  model: "gemma-4-e4b",
  messages: [{ role: "user", content: "Hello" }],
  max_tokens: 500,
});
console.log(reply.choices[0].message.content);
```

## Use with other tools {#tools}

Any tool that supports a custom OpenAI base URL works. Many tools read these two environment variables:

::: macos linux
```bash
export OPENAI_BASE_URL=http://127.0.0.1:{{port}}/v1
export OPENAI_API_KEY=$({{exe}} apikey -q)
```
:::

::: windows
```powershell
$env:OPENAI_BASE_URL = 'http://127.0.0.1:{{port}}/v1'
$env:OPENAI_API_KEY = ({{exe}} apikey -q)
```
:::

If a tool on a web page calls the endpoint with a non-local `Origin`, the endpoint returns 403. This stops a malicious web page from spending your credit.

## Use with Claude Code {#claude-code}

Claude Code talks to the endpoint in the Anthropic Messages API. Point it at the client and pick a model from `{{exe}} models`:

::: macos linux
```bash
export ANTHROPIC_BASE_URL=http://127.0.0.1:{{port}}
export ANTHROPIC_AUTH_TOKEN=$({{exe}} apikey -q)
export ANTHROPIC_MODEL=claude-sonnet-4-6          # a model ID from `{{exe}} models`
export ANTHROPIC_SMALL_FAST_MODEL=claude-sonnet-4-6  # for Claude Code's background tasks
claude
```
:::

::: windows
```powershell
$env:ANTHROPIC_BASE_URL = 'http://127.0.0.1:{{port}}'
$env:ANTHROPIC_AUTH_TOKEN = ({{exe}} apikey -q)
$env:ANTHROPIC_MODEL = 'claude-sonnet-4-6'
$env:ANTHROPIC_SMALL_FAST_MODEL = 'claude-sonnet-4-6'
claude
```
:::

Tool use, thinking and prompt caching work as with Anthropic. Any model on the network can be used, not only Claude: a node whose model server does not speak the Messages API converts the request (see [Native and converted](#native)). For a model ID it does not know, Claude Code warns that it assumes a context window of 1M tokens. `/v1/messages/count_tokens` is answered on your machine with an estimate.

> [!TIP]
> Coding agents send long prompts: tens of thousands of tokens with every request. If requests fail with `budget`, the per-request hold does not cover the prompt at the model's price; raise `buy.budget_micros` in the [configuration file](/docs/config#buy) (default `2000000`, 2 credits), or in the desktop app under "Settings > Requests > Budget per request". Only what a request uses is charged.

## Use with Codex {#codex}

Codex talks to the endpoint in the OpenAI Responses API. Add a provider to `~/.codex/config.toml`:

```toml
model = "gpt-5.5"              # a model ID from `{{exe}} models`
model_provider = "sharetoken"

[model_providers.sharetoken]
name = "sharetoken"
base_url = "http://127.0.0.1:{{port}}/v1"
env_key = "SHARETOKEN_API_KEY"
wire_api = "responses"
supports_websockets = true     # optional: one connection for the whole session
```

Codex reads a custom provider's models (context window, reasoning levels, when to compact) only from a catalog file; without one it warns "Model metadata … not found" and guesses. Write the network's models to one and name it at the top of `config.toml`:

```bash
{{exe}} models -codex ~/.codex/sharetoken-models.json
```

```toml
model_catalog_json = "/Users/you/.codex/sharetoken-models.json"   # the path the command printed
```

Run the command again when the models you use change: Codex reads the file when it starts.

Then give Codex the key and start it:

::: macos linux
```bash
export SHARETOKEN_API_KEY=$({{exe}} apikey -q)
codex
```
:::

::: windows
```powershell
$env:SHARETOKEN_API_KEY = ({{exe}} apikey -q)
codex
```
:::

Tool calls (`apply_patch` included), reasoning and context compaction work with any model on the network. Over a WebSocket, Codex sends only what is new in each request; the client keeps the conversation of the connection and sends it whole, since a node keeps nothing between requests. Over HTTP the client also answers `previous_response_id` (for tools such as the OpenAI Agents SDK): it keeps the last responses of each API key in memory for an hour and sends the conversation they make up; an id it does not have is refused with `previous_response_not_found`, and nothing is kept for a request with `store: false`. Codex's `/v1/responses/compact` is supported: a node with a ChatGPT login compacts natively, any other one has its model write a summary. As with Claude Code, raise `buy.budget_micros` if requests fail with `budget`.

To have Claude Code or Codex check the client, call models and manage sharing for you, see [Use with coding agents](/docs/agents).

## Native and converted {#native}

Every node serves all three APIs. A node whose model server speaks the API of your request natively passes it through unchanged; the others convert it, the way gateways such as sub2api do:

| Seller's model server | Native API |
|---|---|
| Anthropic (API key or Claude login) | Messages |
| OpenAI API key | chat completions, Responses |
| ChatGPT (Codex) login | Responses |
| DeepSeek API key | chat completions, Messages |
| Gemini, Grok, other OpenAI-compatible servers | chat completions |

- **Native first:** the network matches a node that speaks your request's API natively whenever one is free, even if a converting one is cheaper. `/v1/models` lists the native APIs of each model in `apis`.
- **Only native:** tick "Only sellers that speak the request's API natively" in the desktop app under "Settings > Requests", set `buy.require_native` in the [configuration file](/docs/config#buy), or send the header `X-Sharetoken-Require-Native: true` with a request. With no such node the request fails with `no_offers` instead of being converted.
- **What a conversion loses:** encrypted reasoning and thinking signatures only work with the model that made them, so they are dropped when a conversation moves to another kind of model; server-side tools (web search, code execution) are not offered by any node.
- **One node only:** the header `X-Sharetoken-Node: <node ID>` sends a request to that node, for trying a seller.

If an answer fails before anything arrived, the client tries other nodes as usual.

## How billing works {#billing}

- **Billed by actual tokens:** The node sets the price, and you pay for the token counts on the receipt.
- **Prompt cache:** input tokens the model server reads from its prompt cache cost the node's cache read price (by default a tenth of its input price), and tokens it writes to the cache its cache write price (by default 1.25 times the input price for Anthropic models, the input price otherwise). The receipt counts both; the job list on the portal marks such jobs with `*`.
- **Reasoning you do not see:** some models reason without showing all of it (encrypted, summarized or omitted thinking). When the answer shows that the model reasoned, you pay the output tokens the model server reported, up to the request's output limit (`max_tokens`, `max_output_tokens`) or what the hold covers. Otherwise output is checked against the text you received.
- **Hold:** When you send a request, an amount is put on hold first, and whatever is not used is returned afterward. If the request has `max_tokens`, only the worst case is held. If not, the whole budget is held (2 credits by default); when your balance is smaller, what there is is held, as long as it covers the prompt and a short answer.
- **Always set `max_tokens`**, especially for batch or high-concurrency calls. Otherwise a large hold can easily cause `insufficient_funds`.
- **Stopping midway:** If your program drops a streaming request midway (for example, the user pressed stop), the node stops generating at once and you are billed only for the part that was delivered.
- **Network breaks:** If the connection breaks because a relay restarted or your network changed, the client reconnects within 60 seconds and the answer continues where it stopped. You do not need to send the request again.

You can change the budget and price limits in the [configuration file](/docs/config#buy).

## How responses are verified {#verify}

A non-streaming response is handed to you only after its receipt passes verification. A streaming response is forwarded as it arrives, and the verification result is appended at the end. The response headers:

| Header | Meaning |
|---|---|
| `X-Sharetoken-Verified` | Whether the receipt passed verification |
| `X-Sharetoken-Job` | The job ID, for looking up bills and giving feedback |
| `X-Sharetoken-Node` | The node that served this request |
| `X-Sharetoken-Cost-Micros` | The cost of this request, in micro-credit |
| `X-Sharetoken-Timing` | Time and traffic: `ticket_ms` (matching), `dial_ms` (connecting to the node), `first_byte_ms` (first byte), `relayed` (whether it went through a relay), `preferred` (whether it went back to the previous node), `frames`, `up_bytes`, `down_bytes` |

A streaming response has no such headers. The same information is in the closing comment line `: sharetoken job=… node=… verified=true …`, together with `resumes` (how many times the stream was picked up again after a break).

If a node overstates usage, the client refuses to sign and hands over the key for that job as evidence. After the ruling, the refund is automatic. The config option `buy.evidence` controls this behavior, and it is on by default.

## Long conversations and large requests {#sessions}

- **Back to the same node:** Later requests of one conversation go to the node that served it last, when it can take them. The connection is already open and the model server's prompt cache is more likely to hit, so the first token comes sooner and the cost may be lower. The client recognizes a conversation by its session: a chat request's `session_id`, Claude Code's session, Codex's `prompt_cache_key`; or else by the model and the first two messages. If that node is busy or offline, another node is used as usual.
- **Request size:** A request can be up to 32 MB (the limit of most cloud Claude APIs), so it can carry images such as screenshots. Requests are compressed before they are sent; a long conversation history usually shrinks to a third to a fifth.

## Use only TEE nodes {#tee}

The operator of an ordinary node can in principle read the prompt. For sensitive content, send it only to TEE nodes. They run in confidential virtual machines, and the operator cannot read their memory. Set this in the [configuration file](/docs/config#buy) and restart the client:

```json
{
  "buy": { "require_tee": true }
}
```

If no node qualifies, the request fails instead of falling back to an ordinary node. The `TEE` column of `models` shows how many TEE nodes exist.

## Buy only from sellers you know {#pairing}

By default your requests go to whichever node the coordinator picks. To send them only to sellers you know, pair with those sellers. A seller gives you a pairing code, and you join with it:

```bash
{{exe}} pair join ABCD-EFGH-IJKL-MNOP
```

From then on this account buys only from the sellers it is paired with, on all of its devices. `models` and `/v1/models` list only their models.

| Action | Command |
|---|---|
| See the sellers you are paired with | `{{exe}} pair` |
| End the pairing with a seller | `{{exe}} pair leave 42` |

- **Limit:** you can be paired with at most three sellers at a time. Leave one to join another.
- **When your sellers are away:** if none of them offers the model, or all of them are offline or full, the request fails with `no_offers` or `capacity`. It is not sent to another seller.
- **Back to the whole network:** leave every seller, and requests go to any node again.
- **What it does not change:** prices, billing and receipt checks stay the same. A paired seller still reads the prompt unless it is a TEE node.

You can do the same in the desktop app under **Overview → Paired sellers**, and on the portal's [pairing page](/app/#/pairing). For the seller's side, see [Pair with your buyers](/docs/share#pairing).

## Errors and retries {#errors}

Errors have the form `{"error":{"code","message"}}`; on `/v1/messages` they have Anthropic's form `{"type":"error","error":{"type","message"}}`, so Claude Code shows them as it shows Anthropic's.

| Code | HTTP | Meaning and what to do |
|---|---|---|
| `no_offers` | 503 | No node offers this model. Check the model ID or pick another model. If you are [paired](#pairing), only your paired sellers count |
| `capacity` | 503 | All nodes are full, and there was still no free slot after waiting 30 seconds. Retry after `Retry-After` |
| `insufficient_funds` | 402 | The balance is not enough for the hold. Set a smaller `max_tokens`, or earn some credit |
| `budget` | 400 | The cost of this request may exceed `buy.budget_micros`. Lower `max_tokens` or raise the budget |
| `bad_request` | 400 | The request is invalid. The message gives the reason. A request over 32 MB gets 413 |
| `upstream_error`, `receipt_rejected` | 502 | The node failed, or the receipt did not pass verification. You can retry |
| `unauthorized`, `forbidden` | 403 | The sign-in expired or the account is disabled. Run `login` again |
| `upgrade_required` | 426 | The client version is too old. See [Update](/docs/update#upgrade) |
| `not_connected` | 503 | The client is not connected to the network yet. Wait a moment or check `status` |

While a model is thinking, the node sends a heartbeat every 10 seconds, so a long think is not judged stuck; only 5 minutes without any output is. For timeout settings, see the [configuration file](/docs/config#buy).

The reasoning of a thinking model also counts toward `max_tokens`. If `max_tokens` is too small, `content` may be empty and only `reasoning_content` is present.
