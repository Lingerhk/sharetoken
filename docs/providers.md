# Seller mode: hosted APIs (providers)

**English** | [简体中文](zh-CN/providers.md)

This is the developer-level description of how a seller shares models that are hosted by a model vendor. The user-facing instructions are in the portal's documentation pages, [`internal/portal/web/docs/en/share.md`](../internal/portal/web/docs/en/share.md) (section `#providers`). This file contains no secrets.

## 1. Overview

Besides a model server on its own machine (Ollama and the like), a seller can share a service it has with a model vendor. The client rewrites the buyer's OpenAI chat-completions request into the vendor's own format, sends it with the seller's API key or OAuth login, and rewrites the answer back into OpenAI streaming chunks. Nothing changes for the buyer, who still sees only the OpenAI format.

- A seller can configure several providers, including several of the same kind (told apart with `-name`), and turn each on or off.
- Local models and provider models can be sold at the same time.
- Credentials are kept apart in `providers.json`, not in `config.json`.
- Every operation exists as a CLI subcommand and as a local HTTP endpoint (used by the desktop app, see [desktop.md](desktop.md)).

> **Note**
> - The buyer's prompt goes to the vendor in clear text, and the usage is billed to the seller's account with that vendor.
> - The OAuth logins use the client IDs and identifying headers of each vendor's own client application.
> - Selling access to a hosted API is subject to that provider's terms of service. Checking that they allow it is the user's responsibility.

## 2. Provider kinds

| Kind | API key | OAuth login | Address with a key | Address with a login | Upstream format |
|---|---|---|---|---|---|
| `anthropic` | yes | yes (Claude subscription) | `api.anthropic.com` | `api.anthropic.com` | Messages API |
| `openai` | yes | yes (ChatGPT / Codex) | `api.openai.com/v1` | `chatgpt.com/backend-api/codex` | key: chat completions; login: Responses |
| `gemini` | yes | yes (Gemini CLI) | `generativelanguage.googleapis.com/v1beta/openai` | `cloudcode-pa.googleapis.com` | key: OpenAI-compatible; login: native generateContent |
| `antigravity` | no | yes | — | `cloudcode-pa.googleapis.com` | native generateContent in the Antigravity envelope |
| `grok` | yes | yes | `api.x.ai/v1` | `cli-chat-proxy.grok.com/v1` | OpenAI-compatible |
| `deepseek` | yes | no | `api.deepseek.com` | — | OpenAI-compatible |
| `custom` | yes (may be empty) | no | given with `-url` | — | OpenAI-compatible |

The kinds are defined in [`internal/provider/kind.go`](../internal/provider/kind.go). A key or token is only ever sent to an HTTPS address or to a machine on the local network (`CheckSecretURL`).

## 3. Code layout

Package [`internal/provider`](../internal/provider):

| File | Contents |
|---|---|
| `store.go` | Reading and writing `providers.json`: `File`, `Entry`, `Token`; `Load`, `Save`, `Update`; `View` (keys masked) |
| `kind.go` | The table of kinds, `CheckSecretURL` |
| `build.go` | `Entry.Upstream` (configuration → `node.Upstream`), `ListModels`, `Check`, `Probe`, `billable` |
| `ops.go` | Operations shared by the CLI and the API: `AddWithKey`, `SaveLogin`, `FinishLoginAndSave`, `SetEnabled`, `Remove`, `SetModel`, `RemoveModel`, `Models`, `Test`, `Views`, `KindViews` |
| `chat.go`, `sse.go` | Parsing the buyer's chat request; sending SSE requests and writing the result as OpenAI chunks |
| `openai.go` | OpenAI-compatible upstream; learning the output limit and retrying |
| `anthropic.go` | chat ↔ Messages API |
| `responses.go` | chat ↔ Responses (the Codex backend) |
| `gemini.go` | chat ↔ native Gemini format; tool schema clean-up |
| `oauth.go` | Two-step login, token renewal, retry on 401 |
| `oauth_anthropic.go`, `oauth_openai.go`, `oauth_google.go`, `oauth_grok.go` | Each vendor's login flow and request envelope |
| `provider_test.go` | Format conversion, streams, the store, limits, hidden reasoning, sign-in addresses, token renewal |

Related code elsewhere:

- [`internal/node/node.go`](../internal/node/node.go): `ModelConfig` carries `MaxOutputTokens` (`max_output`) and a per-model `Upstream` (not serialized). A node needs no global `Upstream`.
- [`internal/client/agent.go`](../internal/client/agent.go): `shareModelsLocked` merges the local models and the models of every enabled provider into one set of offers; `watchedUpstream` warns the seller when credentials are refused.
- [`internal/client/providers.go`](../internal/client/providers.go): the providers API.
- [`internal/client/api.go`](../internal/client/api.go): `ShareStatus` carries `Providers` and `Serving`.
- [`cmd/sharetoken/main.go`](../cmd/sharetoken/main.go): the `provider` subcommand and `share add -provider`.
- [`internal/cli/cli.go`](../internal/cli/cli.go): `ReadSecret` (reads a key without echo).
- [`test/e2e/providers_test.go`](../test/e2e/providers_test.go): two end-to-end tests.

## 4. providers.json

The file is `providers.json` in the client's state directory, mode 0600, written to a temporary file and renamed. `version` is 1; a file with a higher version than the program knows is refused.

```json
{
  "version": 1,
  "providers": [
    {
      "name": "deepseek",
      "type": "deepseek",
      "auth": "apikey",
      "enabled": true,
      "api_key": "<key>",
      "models": [
        {"id": "deepseek-flash", "upstream_model": "deepseek-flash", "price_in": 100000, "price_out": 200000, "max_output": 8192}
      ],
      "added_at": "2030-01-01T12:00:00Z"
    },
    {
      "name": "chatgpt",
      "type": "openai",
      "auth": "oauth",
      "enabled": true,
      "oauth": {
        "access_token": "<token>",
        "refresh_token": "<token>",
        "expires_at": "2030-01-01T13:00:00Z",
        "email": "…",
        "account_id": "…"
      },
      "models": []
    }
  ]
}
```

| Field | Meaning |
|---|---|
| `name` | The user's name for the provider, unique in the file; defaults to the kind |
| `type` | The kind's ID (section 2) |
| `auth` | `apikey` or `oauth` |
| `enabled` | When off, its models are no longer offered; configuration and credentials stay |
| `base_url` | Overrides the kind's own address; required for `custom` |
| `api_key` | The key |
| `headers` | Headers sent with every request, for example `anthropic-workspace-id` |
| `id`, `anthropic_identity` | Stable local identity; the Anthropic device ID and session derivation key are omitted from public views |
| `transport` | `proxy_mode`, `proxy_url`, `tls_profile`, `token_tls_profile`; views never contain proxy passwords |
| `anthropic` | `profile=standard/claude-code`, `sampling=strict/default` |
| `oauth` | `account_uuid` separately identifies the Anthropic user; `scope` and `login_mode` record authorization scope. The token from a login. `account_id` is the account the token acts for (ChatGPT account, Anthropic organization); `project_id` is used by Gemini and Antigravity; `plan` and `extra` depend on the kind |
| `models` | The models this provider sells: network ID, the vendor's model name, prices, `max_output` |

A model entry has the same fields as a local model in `config.json` (`node.ModelConfig`).

Every change goes through `provider.Update`, which re-reads the file, applies the change and writes it back (serialized across CLI and daemon processes with OS file locks), so a token renewed by the running client and a setting changed by a command do not undo each other.

## 5. Command line

```bash
sharetoken provider                               # kinds that can be added, and the configured providers
sharetoken provider add <kind> [-name N] [-url URL] [-header K=V] [-no-check]
sharetoken provider login <kind> [-name N] [-project ID] [-mode full|inference]
sharetoken provider configure <name> [-proxy-mode environment|direct|custom] [-proxy URL]
                         [-profile standard|claude-code] [-sampling strict|default]
                         [-tls-profile native|chrome] [-token-tls-profile native|chrome]
sharetoken provider models <name>                 # models these credentials may use
sharetoken provider test <name> [-model M] [-prompt TEXT]
sharetoken provider on|off <name>
sharetoken provider rm <name>                     # removes the credentials too

sharetoken share add <vendor model> -provider <name> [-id ID] [-in P] [-out P] [-max-output N]
sharetoken share rm <ID>                          # looks among local models and provider models
```

- **Entering a key:** a hidden prompt, standard input, or the environment variable `SHARETOKEN_PROVIDER_KEY`. There is no command-line argument for it, because arguments show up in the process list and the shell history.
- **Check on add:** `provider add` lists the vendor's models once to confirm the key works, and does not save a key that is refused; `-no-check` skips this. For DeepSeek it also reads the account balance.
- **Immediate effect:** while `sharetoken up` is running, these commands change the file and then tell the running client to reload it. No restart is needed.
- **Examples:**

```bash
sharetoken provider add deepseek
sharetoken share add deepseek-flash -provider deepseek
sharetoken provider add custom -name lab -url https://llm.example.com/v1
sharetoken provider add anthropic -header anthropic-workspace-id=<workspace ID>
```

## 6. Local API

The routes are in [`internal/client/providers.go`](../internal/client/providers.go), on the client's local API:

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/v1/providers` | The kinds that can be added (with `needs_url` and `key_optional`, which tell the page which inputs to show) and the configured providers |
| POST | `/api/v1/providers` | Add one with an API key (`provider.AddRequest`) |
| PATCH | `/api/v1/providers/{name}` | Update `enabled`, `transport`, or `anthropic` settings |
| DELETE | `/api/v1/providers/{name}` | Remove it |
| GET | `/api/v1/providers/{name}/models` | `models`, `source`, `verification`, and optional `warning`; fallback IDs do not verify inference access |
| PUT | `/api/v1/providers/{name}/models` | Add a model or change its price (`node.ModelConfig`) |
| DELETE | `/api/v1/providers/{name}/models/{id}` | Remove one model |
| POST | `/api/v1/providers/{name}/test` | `{"model", "prompt"}`: send one request through it |
| POST | `/api/v1/providers/oauth/start` | `{"type", "name", "project", "mode", "transport", "anthropic"}` → the sign-in address |
| POST | `/api/v1/providers/oauth/finish` | `{"id", "code", "name"}` |
| POST | `/api/v1/providers/oauth/session/start` | `{"name", "session_key", "mode", "transport", "anthropic"}` → login ID and organizations |
| POST | `/api/v1/providers/oauth/session/finish` | `{"id", "name", "session_key", "organization"}` → saved login; the cookie is not persisted |

- An answer never contains a key or a token, only the masked form.
- A request with an `Origin` other than `http://<local host>` gets 403 `bad_origin`. Keys enter here, so no web page may call it.
- Status codes: 404 not found, 401 credentials refused, 409 model ID already taken, 502 the vendor returned an error, 400 otherwise.
- The state returned by `GET /api/v1/share` includes `providers` and `serving`.

## 7. Request rewriting

Each provider ends up as a `node.Upstream`: it takes the body of the buyer's chat-completions request and emits OpenAI `chat.completion.chunk` objects.

| Upstream | What is rewritten |
|---|---|
| OpenAI-compatible (deepseek, grok, custom, gemini with a key, openai with a key) | Mostly passed through, with the model name replaced. OpenAI's own API gets `max_completion_tokens` instead of `max_tokens` |
| Anthropic Messages | System messages move to the top level; tool definitions, tool calls and tool results become `tool_use` / `tool_result` blocks; `x-api-key` plus `anthropic-version: 2023-06-01`; stream events are turned back into chunks |
| Anthropic Messages with a login | The default `standard` profile is as above, with a bearer token and the `anthropic-beta: oauth-2025-04-20` header instead of `x-api-key`. The API serves such a token to Claude Code only, so the request's system prompt leads with Claude Code's identity line. The short names `claude-sonnet-4-5`, `claude-opus-4-5` and `claude-haiku-4-5` are sent as their dated IDs. See section 11 for the complete `claude-code` profile |
| Responses (Codex backend) | Messages become `input` items. The backend takes no output limit, so the stream is cut at the buyer's `max_tokens`. A tool named `python` is renamed `python__st` on the way out and restored on the way back |
| Native Gemini | Messages become `contents` / `systemInstruction`, tools become `functionDeclarations`, and `cleanSchema` drops the schema fields Gemini does not accept |
| Antigravity | The native Gemini request wrapped in another envelope (`requestId`, `userAgent`, `requestType`, project ID) with an identity system prompt; the production or the daily host is chosen by plan |

### Output limit

By default the node asks the upstream for up to 32768 output tokens, which some models refuse (`gpt-4o-mini`, for example, stops at 16384 and answers 400). `outputLimits` handles this:

1. A model configured with `max_output` is capped at that value.
2. Otherwise the request goes out as it is. If the vendor refuses it with "the limit is N", N is read from the error message, the request is sent once more, and the model uses N from then on.

Anthropic requires `max_tokens`; `anthropicMaxOutput` supplies the default.

### Hidden reasoning and billing

A buyer checks the output tokens a seller claims: they may not exceed twice the estimate of the visible output plus 64 (`protocol.UsagePlausible`), and a job that claims more is rejected as a whole. A model with hidden reasoning (`gpt-5-nano`, for example) reports far more tokens than the buyer sees.

`billable` wraps every provider and brings the claim down to that bound; the vendor's own number stays in the chunk as `completion_tokens_reported`. The consequence: **the seller is not paid for hidden reasoning tokens**, and prices should allow for that. `provider test` says so when it happens.

## 8. OAuth login

A login takes two steps, so it also works on a machine without a browser:

1. `StartLogin(kind, project)` builds the sign-in address (PKCE and state). The CLI prints the address; the API returns an `id` and the address.
2. The user signs in with any browser and pastes back the authorization code, or the whole callback address. `FinishLogin` parses it (`parseCallback`), exchanges it for a token and saves it.

| Kind | Authorization | Callback | Notes |
|---|---|---|---|
| `anthropic` | `claude.com` | `platform.claude.com/oauth/code/callback` | The page shows a code of the form `code#state` to paste back. The token keeps the account's email and organization. A built-in model list is used when the models cannot be listed |
| `openai` | `auth.openai.com` | `localhost:1455` | The token carries the ChatGPT account ID. A built-in model list is used when the backend's cannot be read |
| `gemini` | Google | `codeassist.google.com/authcode` | The page shows the authorization code. `-project` names the Google Cloud project; otherwise it is found through `loadCodeAssist` / `onboardUser` |
| `antigravity` | Google | `localhost:8085` | The project is found the same way |
| `grok` | `auth.x.ai` | `127.0.0.1:56121` | — |

Where the callback is a localhost address, the browser ends on a page that does not load. Paste the whole address from the address bar.

A started login that is not finished expires after 30 minutes.

Token upkeep (`Tokens` / `TokenSource`):

- A token is renewed when it has less than 5 minutes left, and the new one is written back to `providers.json`.
- A request that gets 401 forces a renewal and is retried once (`withToken`).
- If renewal fails, or the request is refused again, it is handled as refused credentials (below).

### Refused credentials

When the upstream answers 401 or 403 (credentials refused) or 402 (out of balance), `watchedUpstream` raises the warning `source:<name>:credentials` for the seller. It shows in `sharetoken status` and in the API. Until it is fixed, every job through that provider fails. HTTP 429 also raises a status warning and starts the temporary cooldown described in section 11.

## 9. What is verified

| Item | State |
|---|---|
| Format conversion, streams, output limits, hidden reasoning, the store, callback parsing, sign-in addresses of every login kind, token renewal | Covered by unit tests against mock servers ([`internal/provider/provider_test.go`](../internal/provider/provider_test.go)) |
| Several providers each selling through their own API; a refused key warning the seller | Covered by the two end-to-end tests in [`test/e2e/providers_test.go`](../test/e2e/providers_test.go), against mock servers |
| DeepSeek with an API key | Tested against the real service, end to end through the network |
| OpenAI with an API key | Tested against the real service, end to end, including streaming tool calls |
| Anthropic with an API key | Not tested against the real service; the format conversion is tested against a mock only |
| Gemini and Grok with an API key | Not tested against the real service |
| OAuth logins (anthropic, openai, gemini, antigravity, grok) | Not verified against the real services by any test in this repository. The tests cover the sign-in address, token renewal and format conversion; signing in and exchanging the code have to be checked by hand |

In the tests against the real services, jobs reached `final` with `verified=true`, the seller was credited, the providers API leaked no key, and a request with a foreign `Origin` got 403.

Run the automated tests with `scripts/test/test.sh ./...` (see [development.md](development.md)).

## 10. Known limits

- `PUT /api/v1/share` can still set `upstream_key_env`, the name of the environment variable that holds the local model server's key. It is unrelated to providers but belongs to the same credential surface.
- The Codex backend has no dependable model list endpoint. When it cannot be read, a built-in list is used, and new models need a code change. The same holds for the built-in lists of the other logins.
- Two providers cannot sell the same network model ID: the one loaded later is skipped with a warning, and `share add` asks for another ID with `-id`.
- Hidden reasoning tokens are not billed (section 7).


## 11. Anthropic OAuth compatibility profile

The default `standard` profile retains the original OAuth request format. The new `claude-code` profile follows the non-Claude-Code conversion path in sub2api `530c5e3a4`: nine betas, CLI/Stainless headers, request IDs, JSON-encoded metadata, billing attribution and system blocks are generated together. Its CLI baseline is `2.1.321`. The general expansion text is independently written for this adapter and omitted for Fable; it is not a byte-for-byte copy of the full Claude Code prompt.

```bash
sharetoken provider login anthropic -name claude -profile claude-code -token-tls-profile chrome
sharetoken provider login anthropic -name claude -mode inference -profile claude-code
# Hidden cookie prompt; select the organization when there is more than one
sharetoken provider login anthropic -name claude -session-key -profile claude-code

sharetoken provider configure claude -proxy-mode custom -proxy http://127.0.0.1:8080
# For a URL containing a password, set SHARETOKEN_PROVIDER_PROXY and omit -proxy
sharetoken provider configure claude -proxy-mode direct
sharetoken provider configure claude -tls-profile chrome -token-tls-profile chrome
sharetoken provider configure claude -profile standard -tls-profile native -token-tls-profile native
sharetoken provider test claude -model claude-sonnet-4-6
```

The desktop connection settings expose the same options. Partial `transport` updates preserve omitted settings. Direct mode ignores environment proxies; environment mode restores Go's normal proxy rules. Token and inference connections have separate pools and TLS profiles. Browser TLS uses the reference project's `imroc/req/v3 v3.59.0`; selecting it does not select the request profile or add model capabilities.

**Identity and login.** `oauth.account_id` remains the organization UUID; `oauth.account_uuid` is the user UUID. An old login missing the latter needs a refresh or a new sign-in before using the compatibility profile. Refreshes, restarts and signing back into the same account preserve device identity. Duplicate providers for the same Anthropic account are rejected. Cookie login selects an organization explicitly and uses the cookie only for authorization; the cookie is never stored or sent to token or Messages endpoints. Inference-only authorization uses the returned expiry and refresh token, not an assumed unlimited lifetime.

**Models and parameters.** The fallback catalogue includes Fable 5.2 and Mythos 5.2 and is marked `source=fallback`. Listing models verifies model-list access only. HTTP 401/403 are preserved; an inference-only credential without listing permission can still be saved and tested against an explicitly named model. Sonnet 5.5, Haiku 5.5 and Fable/Mythos 5.2 validate explicit sampling settings; `sampling=default` omits temperature/top_p for those models. Forced tool choices on Opus/Sonnet 5.5 and Fable/Mythos 5.2 fail explicitly instead of silently becoming auto. API keys share these parameter rules.

**Sessions.** Buyers may supply a top-level OpenAI `session_id` of at most 256 bytes. It travels inside the encrypted request and reaches the adapter through local context, without being forwarded as a normal OpenAI upstream field. The derived identity is scoped to the seller's provider, authenticated buyer and conversation. Without an explicit ID, the first user text is an approximation: two conversations from one buyer with the same opening can share a session. The actual buyer peer ID is not sent to Anthropic.

**Caching and tool history.** Text/image content blocks and tool entries accept ephemeral `cache_control` with 5m/1h TTL. At most four explicit breakpoints survive normalization, with valid TTL order. Streaming replies include complete signed thinking blocks in `delta.anthropic_thinking`; non-streaming replies assemble them in `message.anthropic_thinking`. Clients must preserve those blocks unchanged in the next assistant message for models requiring signed history. The signature payload is not billed a second time as output. Clients that discard extension fields cannot rely on this feature for tool continuations.

**Input billing.** The compatibility profile moves the original system/developer instructions into messages and injects adapter instructions, changing the instruction role. Billable input is `min(upstream total input, original OpenAI input estimate)`. The seller pays the additional prompt cost, while `prompt_tokens_reported` preserves upstream accounting. Usage totals and receipts use the same billable figures, without increasing marketplace tolerances or output budgets. Verify output-format and agent behavior with the actual model.

**Rate limits and diagnostics.** Provider views report the last upstream HTTP status, request ID and cooldown deadline. HTTP 429 follows Retry-After, defaulting to one minute. Cooling providers are omitted from offers; if all are cooling down, the node withdraws its offer and advertises again after recovery. Tickets based on stale offers receive busy without another upstream call or a buyer fault. Runtime state is local to the daemon and resets at restart.

Mock tests cover TLS verification, cookie organization selection, identity/proxy isolation, concurrent renewal, request construction, caching, signed history and input accounting. Real Anthropic login, tool continuations and quota classification still require account-holder verification. Native buyer `/v1/messages` and a local pool of interchangeable accounts remain separate features.

Completed checks, environment requirements and remaining acceptance scenarios are collected in the [Anthropic OAuth test handoff (Chinese)](zh-CN/anthropic-oauth-testing.md).
