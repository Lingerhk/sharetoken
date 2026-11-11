# Call models

While the client runs, it serves an OpenAI-compatible endpoint on your machine. Your request is encrypted and sent only to the selected node. The response carries the node's signed receipt, and the client checks it automatically.

## Local endpoint {#endpoint}

| Setting | Value |
|---|---|
| Base URL | `http://127.0.0.1:{{port}}/v1` |
| API key | Not checked. Use any non-empty value |
| Interface | `POST /v1/chat/completions`, `GET /v1/models` |

First make sure `{{exe}} up` is running, or [run it in the background](/docs/running#background).

> [!NOTE]
> The endpoint only listens on your machine, so other machines cannot reach it. But other programs and users on the same machine can call it, which spends your credit. Keep this in mind on shared machines.

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
$r = Invoke-RestMethod http://127.0.0.1:{{port}}/v1/chat/completions -Method Post -ContentType 'application/json; charset=utf-8' -Body ([Text.Encoding]::UTF8.GetBytes($body))
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

client = OpenAI(base_url="http://127.0.0.1:{{port}}/v1", api_key="local")

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

const client = new OpenAI({ baseURL: "http://127.0.0.1:{{port}}/v1", apiKey: "local" });

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
export OPENAI_API_KEY=local
```
:::

::: windows
```powershell
$env:OPENAI_BASE_URL = 'http://127.0.0.1:{{port}}/v1'
$env:OPENAI_API_KEY = 'local'
```
:::

If a tool on a web page calls the endpoint with a non-local `Origin`, the endpoint returns 403. This stops a malicious web page from spending your credit.

## How billing works {#billing}

- **Billed by actual tokens:** The node sets the price, and you pay for the token counts on the receipt.
- **Hold:** When you send a request, an amount is put on hold first, and whatever is not used is returned afterward. If the request has `max_tokens`, only the worst case is held. If not, the whole budget is held (0.2 credit by default).
- **Always set `max_tokens`**, especially for batch or high-concurrency calls. Otherwise a large hold can easily cause `insufficient_funds`.
- **Disconnect midway:** If a streaming request disconnects midway, you are billed only for the part that was delivered.

You can change the budget and price limits in the [configuration file](/docs/config#buy).

## How responses are verified {#verify}

A non-streaming response is handed to you only after its receipt passes verification. A streaming response is forwarded as it arrives, and the verification result is appended at the end. The response headers:

| Header | Meaning |
|---|---|
| `X-Sharetoken-Verified` | Whether the receipt passed verification |
| `X-Sharetoken-Job` | The job ID, for looking up bills and giving feedback |
| `X-Sharetoken-Node` | The node that served this request |
| `X-Sharetoken-Cost-Micros` | The cost of this request, in micro-credit |

If a node overstates usage, the client refuses to sign and hands over the key for that job as evidence. After the ruling, the refund is automatic. The config option `buy.evidence` controls this behavior, and it is on by default.

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

Errors always have the form `{"error":{"code","message"}}`.

| Code | HTTP | Meaning and what to do |
|---|---|---|
| `no_offers` | 503 | No node offers this model. Check the model ID or pick another model. If you are [paired](#pairing), only your paired sellers count |
| `capacity` | 503 | All nodes are full, and there was still no free slot after waiting 30 seconds. Retry after `Retry-After` |
| `insufficient_funds` | 402 | The balance is not enough for the hold. Set a smaller `max_tokens`, or earn some credit |
| `budget` | 400 | The cost of this request may exceed `buy.budget_micros`. Lower `max_tokens` or raise the budget |
| `bad_request` | 400 | The request is invalid. The message gives the reason |
| `upstream_error`, `receipt_rejected` | 502 | The node failed, or the receipt did not pass verification. You can retry |
| `unauthorized`, `forbidden` | 403 | The sign-in expired or the account is disabled. Run `login` again |
| `upgrade_required` | 426 | The client version is too old. See [Update](/docs/update#upgrade) |
| `not_connected` | 503 | The client is not connected to the network yet. Wait a moment or check `status` |

A slow model working on a long prompt may be judged stuck. For timeout settings, see the [configuration file](/docs/config#buy).

The reasoning of a thinking model also counts toward `max_tokens`. If `max_tokens` is too small, `content` may be empty and only `reasoning_content` is present.
