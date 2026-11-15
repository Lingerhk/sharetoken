# Three model APIs

**English** | [简体中文](zh-CN/apis.md)

A buyer's local endpoint serves three APIs, and the network carries each one as it is:

| Client | API | Endpoints |
|---|---|---|
| OpenAI SDKs and tools | chat completions (`chat`) | `POST /v1/chat/completions` |
| Claude Code | Anthropic Messages (`messages`) | `POST /v1/messages`, `POST /v1/messages/count_tokens` |
| Codex | OpenAI Responses (`responses`) | `POST /v1/responses`, `GET /v1/responses` (WebSocket), `POST /v1/responses/compact` |

`GET /v1/models` lists the models with the APIs they are served in natively (`apis`), or Codex's model manifest when called with `?client_version=`.

The design follows sub2api (a gateway that does the same): **pass the request through when the model server speaks its API, convert it only when it does not**. sub2api knows its upstream when a request arrives; a sharetoken buyer does not know which node it will be matched to. So the buyer sends the request in the API it came in, the coordinator prefers nodes that speak it, and the node converts when its model server does not.

```
Claude Code --/v1/messages--+                                       +- model server speaks it: passed through
Codex -------/v1/responses--+- buyer -- ticket.api -- node ---------+
OpenAI clients -/v1/chat/...+  (as sent)   (native first)           +- otherwise: converted (internal/apicompat)
```

## Protocol (infer 1.0.2)

The fields below are signed; the protocol IDs and signing domains did not change (no released client spoke the earlier encoding of 1.0.2 with others).

- **`TicketRequest.API`**, **`TicketRequest.RequireNative`**: the API of the request (empty is `chat`), and whether only nodes that speak it natively may take it.
- **`Ticket.API`**: the node reads the request and writes every response event in it; dispute judges and auditors read it the same way.
- **`ModelPrice.APIs`**: the APIs the node's model server speaks natively for the model (`node.Upstream.APIs`).
- **`ModelPrice` / `Ticket` `PriceCacheRead`, `PriceCacheWrite`**: the prices of prompt tokens read from and written to the model server's prompt cache.
- **`Receipt.CacheReadTokens`, `Receipt.CacheWriteTokens`**: the part of `PromptTokens` billed at those prices.
- **The sealed request** is the request's headers and body: `uvarint(len(header JSON)) || header JSON || body` (`protocol.JoinRequest`). The header holds the client's headers the model server needs (`anthropic-version`, `anthropic-beta`, Claude Code's and Codex's session IDs) and sharetoken's `x-sharetoken-op: compact`. The buyer picks them (`buyer.forwardHeader`), the node filters them again (`node.upstreamHeader`: Anthropic betas by family).
- **Response records** are SSE event payloads (the JSON after `data:`). Messages and Responses payloads carry their event name in `type`, so the buyer restores `event:` lines from it (`apicompat.EventName`).

## Matching

`coordinator.Engine.IssueTicket` keeps only nodes that speak the request's API natively when there is one among the candidates (before price, reputation and session affinity are weighed); with `RequireNative` it skips the others altogether and answers `no_offers` when none is left. `ModelSummary.APIs` is the union of the models' native APIs.

## The node

[`node.UpstreamRequest`](../internal/node/request.go) applies each API's field policy, so a buyer cannot change the model, multiply work or switch on features the seller did not price:

| API | Passed | Dropped | Set by the node |
|---|---|---|---|
| chat | messages, sampling, stop, penalties, seed, response_format, tools, tool_choice, user, parallel_tool_calls, reasoning_effort | everything else (n, logprobs, …) | model, stream, stream_options, max_tokens |
| messages | messages, system, client tools, tool_choice, thinking, temperature, top_p, top_k, stop_sequences, metadata, context_management, output_config | server tools (web_search, web_fetch, code_execution, tool_search_tool, mcp_toolset), container, mcp_servers, service_tier | model, stream, max_tokens |
| responses | input, instructions, client tools (function, custom, namespace, local_shell, tool_search with execution client, …), tool_choice, parallel_tool_calls, reasoning, text, include (only `reasoning.encrypted_content`), prompt_cache_key, temperature, top_p | hosted tools, background, service_tier, metadata; `previous_response_id` and `conversation` are refused | model, stream, store=false, max_output_tokens |

The output limit is `protocol.OutputLimit`: what the reservation covers after a padded prompt estimate, at most `protocol.HardMaxOutput` (32768 for chat, 128000 for the others), at most what the request asks for. The buyer computes the same number.

[`node.plan`](../internal/node/plan.go) then passes the request through when `Upstream.APIs()` contains its API, or converts it with `apicompat.Convert` into the closest API the upstream speaks (`apicompat.Target`: Responses and Messages carry reasoning and tool items chat cannot) and translates the events back.

### Compaction

A compact request (`x-sharetoken-op: compact`, API responses) goes to the model server's `/responses/compact` when it has one (`node.Compactor`, the Codex backend). Otherwise the node sends a normal request whose last user message asks for a summary (`apicompat.CompactPrompt`) and answers with a `compaction` item whose `encrypted_content` is `sharetoken-compaction-v1:` + base64 of the summary. A later request's such item becomes `<conversation_summary>…</conversation_summary>` as a user message for any upstream (the Codex backend included, which cannot read it); an opaque item made by the Codex backend reaches only the Codex backend. Codex with a custom provider usually compacts with a normal request of its own, which needs nothing special.

## Conversion: `internal/apicompat`

Written for sharetoken after sub2api's `apicompat` (LGPL-3.0: its behaviour and test cases were the reference, not its code). One file group per client API, each with request conversion, a streaming translator and the non-streaming aggregator:

| Client API | Files | Converts to |
|---|---|---|
| messages | `messages_*.go` | Responses, chat |
| responses | `responses_*.go` | Messages, chat (and Codex's client tools: custom, namespace, tool_search, local_shell) |
| chat | `chat_*.go` | Responses, Messages (with `anthropic_thinking` passback and `cache_control`) |

What crosses the boundary: tool calls (ids mapped both ways, names made valid and restored, pairing repaired), images and documents, reasoning (Messages thinking ↔ Responses reasoning items; signatures and encrypted reasoning travel inside the other API's opaque fields with a `sharetoken-` prefix, `apicompat.IsConverted`, and are dropped before they reach a model server that cannot read them), stop reasons, usage (cache reads and writes included). Requests are always streamed; a client that wants one JSON answer gets the events aggregated on the buyer's side (`apicompat.Aggregate`).

Tests: table tests per direction, real captured Claude Code and Codex requests (`internal/apicompat/testdata/captured`), and `test/e2e/multiapi_test.go` (every client API against Messages, Responses and chat model servers, compaction, the WebSocket, billing).

## Providers

| Provider | Native APIs |
|---|---|
| `anthropic` (key or login) | messages |
| `openai` key | chat, responses (`/responses`) |
| `openai` login (Codex backend) | responses, compact |
| `deepseek` key | chat, messages (`/anthropic/v1/messages`) |
| `gemini`, `grok`, `custom`, local servers | chat (Gemini and Antigravity logins: chat, converted to Gemini's own format in the provider) |

Passing through keeps each provider's own adaptations: a Claude login's headers and `claude-code` profile (a Claude Code request keeps its system prompt; only the billing line and the account change), cache breakpoint normalization, thinking blocks no Anthropic model signed dropped (and on a signature refusal, the request sent again without thinking); the Codex backend's rules (instructions present, no output limit, `python` renamed, `store=false`) with the stream cut at the job's limit; reasoning items another account made retried without; Gemini's tool calls given the placeholder thought signature when theirs was lost.

## Billing

- **Usage** is read from the events the buyer receives (`protocol.ParseEvent`, `protocol.UsageMeter`): chat's last usage, Messages' `message_start` and `message_delta` merged, Responses' terminal event. Cache reads and writes come from `prompt_tokens_details`, `cache_read_input_tokens` / `cache_creation_input_tokens`, `input_tokens_details` (DeepSeek's `prompt_cache_hit_tokens` and Gemini's `cachedContentTokenCount` too).
- **Cost** (`protocol.Cost`): uncached prompt × input price + cache reads × cache read price + cache writes × cache write price + completion × output price. Holds and output caps assume the higher of the input and cache write prices.
- **What a buyer accepts** (`protocol.UsagePlausible`): prompt ≤ 3 × estimate + 512 (`EstimatePromptTokens` per API, images and files included); cache counts within the prompt; completion ≤ 2 × visible estimate + 64, or, when the response shows hidden reasoning (a thinking or reasoning item, redacted or omitted thinking, reported reasoning tokens), up to the job's output limit. The node signs its usage lowered to these bounds (`protocol.BillableUsage`), so an honest node's receipt is not rejected, and a provider that adds its own instructions caps the billed prompt (`node.Billing.PromptCap`). Dispute judges apply the same rules.
- The jobs table records `api`, `cache_read_tokens` and `cache_write_tokens` (core migration 7); the portal's job lists and CSV exports show them.
