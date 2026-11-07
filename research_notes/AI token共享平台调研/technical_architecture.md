# Technical Architecture of LLM API Capacity-Sharing Platforms (Central Gateway vs. Contributor-Run Local Node)

Scope: system design, data flow, metering, trust and privacy, and the vendor terms that limit which designs are viable. Credential extraction, client impersonation, fingerprint mimicry and detection avoidance are out of scope and not covered. Research date: 2026-11-06. Sources were fetched on that date unless a date is given. "Inference" means my own synthesis and is not stated by any source.

---

## Q1. Pattern A: How central LLM API gateways and aggregators are built (new-api / one-api, LiteLLM proxy, OpenRouter)

### Takeaway
A central gateway holds a pool of upstream credentials, which new-api calls "channels" (渠道) and LiteLLM calls "deployments". It exposes one or more vendor-compatible endpoints (OpenAI, Anthropic Messages, Gemini), routes requests by priority, weight, latency or load, and puts failing upstreams into cooldown or fallback. Spend per virtual key, user or team goes into a database ledger priced from upstream `usage`, and Redis shares rate-limit and routing state across instances. The platform operator has custody of every upstream key and can see every prompt and response.

### Cited Findings
**new-api (QuantumNous/new-api), the main open-source gateway in the Chinese "中转站" (relay station) ecosystem**
- It is "developed based on One API (MIT License)", so it is a derivative of songquanpeng/one-api and not a separate rewrite. It is licensed AGPL-3.0 with added Section 7 terms: modified versions with a UI must keep a visible link to the original project and the attribution. A separate commercial license is offered. — [GitHub QuantumNous/new-api](https://github.com/QuantumNous/new-api)
- Channel model: an admin adds a channel with "your upstream API key, available models, and group assignment" and then runs a channel test. A channel can hold "multiple upstream keys". The README does not describe how keys are selected or rotated. — [new-api README](https://github.com/QuantumNous/new-api)
- Endpoints: OpenAI-style `POST /v1/chat/completions` and `POST /v1/responses`, Anthropic `POST /v1/messages`, Gemini `POST /v1beta/models/{model}:generateContent` with streaming variants, plus embeddings, rerank, images, audio, realtime WebSocket and task-plugin routes. "RelayKit" converts requests, responses and streams among the four text protocols, with a warning that protocol-specific tools and fields may not map exactly. — [new-api README](https://github.com/QuantumNous/new-api)
- Routing: model mappings, channel priorities and weights, retries, and channel affinity. — [new-api README](https://github.com/QuantumNous/new-api)
- Billing: quotas and subscriptions, cache accounting, and "expression-based pricing for different usage tiers". Users need quota or a valid subscription before they can call. — [new-api README](https://github.com/QuantumNous/new-api)
- Rate limits can be shared through Redis across nodes. Without shared Redis, each node counts limits separately. Usage logs record requests, costs and cache accounting, and they can go to a separate database such as ClickHouse. Authentication includes OAuth/OIDC, passkeys and 2FA. — [new-api README](https://github.com/QuantumNous/new-api)

**LiteLLM proxy (BerriAI)**
- Router: deployments that share a `model_name` form a load-balanced group. Strategies are `simple-shuffle` (the default and the one recommended for production; it uses rpm/tpm or weight), `usage-based-routing` (lowest recent TPM; "not recommended for production due to performance impacts"), `least-busy` (fewest in-flight calls) and `latency-based-routing`. — [LiteLLM Routing docs](https://docs.litellm.ai/docs/routing)
- Each deployment has its own `tpm`/`rpm`. `max_parallel_requests` returns an immediate 429 when exceeded instead of queuing. — [LiteLLM Routing](https://docs.litellm.ai/docs/routing)
- Failure handling: cooldown is per deployment. By default a deployment cools down on a 429, on more than 50% failures in the current minute, or on a 401, 404 or 408, for 5 seconds. `allowed_fails` and `cooldown_time` switch this to a windowed failure count. `num_retries` uses exponential backoff for rate-limit errors. `order` sets priority tiers, then `fallbacks`, and `context_window_fallbacks` handles context-length errors. — [LiteLLM Routing](https://docs.litellm.ai/docs/routing)
- Redis tracks TPM/RPM and session-affinity pins across proxy instances. Without it, each instance keeps its own state. — [LiteLLM Routing](https://docs.litellm.ai/docs/routing)
- Ledger: each request writes a row to `LiteLLM_SpendLogs` with a hashed API key, the owning user, the team, tags and `end_user`. Other tables are `LiteLLM_VerificationToken`, `LiteLLM_UserTable` and `LiteLLM_TeamTable`. Cost comes from LiteLLM's model cost map and is returned in the `x-litellm-response-cost` header. Virtual keys come from `/key/generate` and carry `max_budget`, `budget_duration`, `tpm_limit`, `rpm_limit` and `max_parallel_requests`. — [LiteLLM cost tracking](https://docs.litellm.ai/docs/proxy/cost_tracking)
- A caveat in LiteLLM's own docs: the request's `user` field is recorded as `end_user`, so "users could 'avoid' having their spend tracked" if they set it themselves. Spend should be tied to the key's `user_id` instead. — [LiteLLM cost tracking](https://docs.litellm.ai/docs/proxy/cost_tracking)

**OpenRouter (hosted aggregator; public docs only)**
- Default provider selection uses "measured price, throughput, latency, and reliability", plus time to first token, rate limits and tool-call quality. Traffic goes mostly to the strongest providers, with a small share sent to weaker ones so they can recover. A failed request retries on the remaining providers unless `allow_fallbacks: false`. `order` sets an explicit priority list. — [OpenRouter Provider Routing](https://openrouter.ai/docs/features/provider-routing)
- Policy filters: `data_collection: "deny"`, `zdr: true` (Zero Data Retention endpoints only), `quantizations` (e.g. int4/fp8/bf16), and `max_price` ceilings per prompt or completion token. — [OpenRouter Provider Routing](https://openrouter.ai/docs/features/provider-routing)
- BYOK: provider keys are "securely encrypted and used for all requests routed through the specified provider". The fee is "5% of what the same model/provider would cost normally on OpenRouter", with a $25,000/month list-price allowance on pay-as-you-go. Prioritized BYOK keys are tried first, then OpenRouter's shared endpoints unless "Shared capacity fallback" is disabled. — [OpenRouter BYOK](https://openrouter.ai/docs/use-cases/byok)
- Usage: token counts use "the model's native tokenizer". `cost` is the amount charged, and `cost_details.upstream_inference_cost` is populated only for BYOK requests. Responses include `cached_tokens`, `cache_write_tokens` and `reasoning_tokens`. In streams, usage arrives in the terminal event. Usage can be looked up later through `/generation?id=`. `stream_options.include_usage` is deprecated on OpenRouter because usage is always included. — [OpenRouter Usage Accounting](https://openrouter.ai/docs/use-cases/usage-accounting)

**Upstream usage fields that gateways meter from (Anthropic example)**
- The Messages SSE stream sends `message_start`, then `content_block_start/delta/stop`, then one or more `message_delta`, then `message_stop`, with `ping` events possible anywhere. `message_start.message.usage` carries `input_tokens` and the cache fields. The docs say: "The token counts shown in the `usage` field of the `message_delta` event are *cumulative*." Errors such as `overloaded_error`, which corresponds to HTTP 529 in non-streaming calls, can arrive inside the stream. — [Anthropic Streaming docs](https://platform.claude.com/docs/en/build-with-claude/streaming)

### Inferences
- Generic Pattern A data flow:
```
 Consumer (SDK/IDE) --HTTPS, platform virtual key--> [ Gateway: auth -> quota pre-check -> router ]
                                                         |  picks channel/deployment (priority/weight/
                                                         |  latency/least-busy; skips cooled-down keys)
                                                         v
                                              Upstream vendor API (platform-held key)
                                                         |
 Consumer <---- SSE relayed chunk-by-chunk ------------- +   gateway tees stream, parses final usage
                                                             -> ledger row (user, key, model, in/out/cache
                                                                tokens, cost) -> deduct quota
```
- Keys and ledger are both central, so a gateway can fail over transparently before the first byte, enforce budgets atomically through Redis and the DB, and reconcile against the upstream invoice. The cost is that the operator has custody of all upstream credentials and sees every prompt in plaintext. That custody is exactly what the "key never leaves your computer" pitch is set against.
- After the first SSE byte has gone to the client, a gateway can no longer retry silently. A mid-stream failure, such as an in-stream `overloaded_error`, reaches the consumer. This applies to both patterns, but Pattern B nodes drop more often.

### Gaps
- new-api's README does not document channel key-selection logic, model-ratio and group-ratio formulas, or whether quota is "pre-consumed" and then reconciled. Those would need the docs site or source code, which I did not read.
- I did not fetch the OpenAI streaming docs. OpenAI's `stream_options: {include_usage: true}` behavior, which sends an extra final usage chunk, comes from my prior knowledge and is not verified here. openai.com returned HTTP 403 to fetches.
- I did not fetch the one-api README separately.

---

## Q2. Pattern B: Contributor-run local node ("key stays local"), with documented analogues

### Takeaway
No major open-source project documents a "relay a third-party closed API through volunteers' local keys" network, so this pattern has to be reconstructed from analogues. The closest documented analogues are volunteer and distributed compute networks where workers **only make outbound connections** and **pull jobs** from a central coordinator: AI Horde, which states "Workers never listen on a port". There are also tunnel products (Cloudflare Tunnel's outbound-only `cloudflared`), token-gated P2P meshes (LocalAI federation on libp2p/EdgeVPN; Petals swarms), and server/worker clusters (GPUStack). They share a common set of mechanisms: token-based node registration, an outbound persistent or polling channel, a central registry and scheduler, timeout-and-reassign for failures, and per-node credit or reputation.

### Cited Findings
**AI Horde (Haidra-Org): the most complete documented volunteer-worker plus central-coordinator system**
- The server "queues requests, matches them to volunteer workers, collects the results and keeps the kudos accounts". "Workers can join and leave at any time." — [AI-Horde README](https://github.com/Haidra-Org/AI-Horde)
- Pull model: "Workers never listen on a port for jobs - they always reach out to the API for work." Workers pop jobs when their local queue has room. For long jobs they pop the next one with about 30 seconds left, because loading assets takes about 3 to 20 seconds. — [Horde SDK docs: Workers](https://horde-sdk.readthedocs.io/en/latest/haidra-assets/docs/workers)
- The job pop endpoint is `POST /v2/generate/pop`. Jobs are matched to workers by capability and kudos priority, and each job carries a unique generation UUID. — [Horde SDK docs: Workers](https://horde-sdk.readthedocs.io/en/latest/haidra-assets/docs/workers), as summarized from search results
- Failure handling: a worker submits a "faulted" payload on an unrecoverable error so the API can reassign quickly, and "The API eventually times out abandoned jobs and reassigns them if possible." — [Horde SDK docs: Workers](https://horde-sdk.readthedocs.io/en/latest/haidra-assets/docs/workers)
- Registration and auth: workers run "with your API key", tied to an OAuth2 account (recommended), a pseudonymous account, or anonymous use (`0000000000`). Text workers run "a wide variety of Large Language Models". — [AI-Horde README](https://github.com/Haidra-Org/AI-Horde); [Horde SDK docs: Workers](https://horde-sdk.readthedocs.io/en/latest/haidra-assets/docs/workers)
- Trust tiers: an account becomes "trusted" after running a worker for about a week and earning substantial kudos. Trusted accounts can run workers behind a VPN and stop being subject to suspicion checks. A dbzer0 blog post (undated in the results) mentions per-IP worker limits and caps on how many workers untrusted users can onboard. — [Horde SDK definitions](https://horde-sdk.readthedocs.io/en/latest/haidra-assets/docs/definitions); [dbzer0 blog](https://dbzer0.com/blog/tag/aihorde/page/3/)

**Cloudflare Tunnel: the generic "outbound-only connector" building block**
- `cloudflared` runs on the origin and dials out to Cloudflare. The firewall only has to allow that outbound traffic, and after the tunnel is up, traffic flows both ways. All inbound traffic can be blocked, and no public IP is needed. A tunnel is a persistent logical object identified by a UUID, with one or more `cloudflared` connector processes for redundancy, each connecting to its nearest data center. Origin traffic is authenticated with the tunnel's connector credentials. — [Cloudflare Tunnel docs](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/)

**LocalAI P2P / federation: token-gated mesh with least-connections load balancing**
- Nodes join with a shared token over libp2p (go-libp2p), and EdgeVPN is used as a library to build the network and a shared ledger. There is no "single master server". Discovery uses a DHT by default, or mDNS only when `LOCALAI_P2P_DISABLE_DHT` is set. Bootstrap peers and announce addresses are configurable for remapped NAT. — [LocalAI distributed inference](https://localai.io/docs/features/distribute/)
- Federated mode sends each new TCP connection to the online node with the fewest active connections, with random tie-breaks. Capacity is reserved before forwarding and released afterwards, including on errors. Keep-alive connections stick to one node. Counts are per federated server, not cluster-wide. — [LocalAI distributed inference](https://localai.io/docs/features/distribute/)
- Security: anyone with the token can join, and publishing a swarm to the explorer "publishes its token, so anyone who sees the list can use the swarm's workers." — [LocalAI distributed inference](https://localai.io/docs/features/distribute/)

**Petals: a public volunteer swarm serving model layers**
- Servers host blocks of a model through `python -m petals.cli.run_server`. Clients "load a small part of the model, then join a network of people serving the other parts." The README warns: "Your data will be processed with the help of other people in the public swarm," and recommends a private swarm for sensitive data. "Hosting a server does not allow others to run custom code on your computer." There are no credits, only a thank-you listing on the swarm monitor. — [Petals README](https://github.com/bigscience-workshop/petals)

**GPUStack: a server/worker cluster with a scheduler**
- The server holds the API server (authn/authz), scheduler ("assigning model instances to workers") and controllers. Workers run a runtime that detects GPUs and a Serving Manager. An AI Gateway built on Higress routes requests to model instances. The default database is embedded PostgreSQL. — [GPUStack architecture](https://docs.gpustack.ai/latest/architecture/)

**exo: local-LAN peer cluster (a contrast case)**
- Devices "automatically discover each other" on the same LAN or over Thunderbolt. Nodes can run with `--no-worker` as "coordinator-only nodes". The cluster exposes OpenAI Chat Completions, Claude Messages, OpenAI Responses and Ollama APIs on `localhost:52415`. The README does not describe clusters spanning the public internet. — [exo README](https://github.com/exo-explore/exo)

### Inferences
- Reconstructed Pattern B design, assembled from the analogues above. It is not taken from any one platform's documentation.
```
  Contributor machine                         Coordinator (cloud)                        Consumer
 +------------------------+   outbound WSS / gRPC stream / long-poll   +-------------------------+
 | node agent (CLI/app)   | ---------------------------------------->  | node registry (Redis):  |
 |  - node token (login)  |   1. REGISTER {node_id, models, max_conc,  |  node_id, caps, load,   |
 |  - upstream API key    |      version}   2. HEARTBEAT every N s     |  last_seen, reputation  |
 |    (local keychain)    |                                            | OpenAI/Anthropic-compat | <-- HTTPS + platform key
 |                        | <---- 3. JOB {job_id, request body} -----  | endpoint, auth, credit  |
 |  4. calls upstream     |                                            | pre-reservation,        |
 |     with LOCAL key ----+--> Vendor API                              | scheduler (least-conn / |
 |  5. streams SSE chunks |                                            | weighted / reputation)  |
 |     back over channel  | ---- 6. CHUNK.. CHUNK.. DONE{usage} ---->  | relays SSE to consumer  | --> SSE to consumer
 +------------------------+                                            | 7. settle ledger        |
                                                                       +-------------------------+
```
- Push vs. pull. With a persistent bidirectional channel (WebSocket, gRPC bidi stream, or a tunnel like `cloudflared`), the coordinator pushes jobs and gets low latency for streaming. With pull or long-poll, as in AI Horde, the design is simpler and stateless but adds a polling delay. Either way the node needs no inbound port, so NAT traversal reduces to "dial out". This is why every analogue above uses outbound connections or relays.
- The heartbeat drives scheduling. The coordinator treats a node as live only while its channel is open and heartbeats are fresh. It routes by declared capacity, current in-flight count (the LocalAI least-connections approach) and reputation. Upstream rate-limit or quota errors reported by a node should put that node into cooldown, the same way LiteLLM cools down a deployment.
- Failure handling follows AI Horde's timeout-and-reassign approach. Reassignment is only safe **before** the first token has reached the consumer. A mid-stream node disconnect has to end in an error, or in a degraded "continue" that is not transparent.
- What "key never leaves the computer" means in this design: the coordinator never receives the upstream credential. The key exists only in the node's memory and keychain. This is a property of the node software and cannot be verified by the contributor unless the software is open-source and reproducibly built. A closed-source agent could still send the key out.
- The pattern is a natural fit for open-weight models, where nodes run inference locally and no third-party terms apply. Applied to closed vendor APIs, it collides with the vendor terms in Q5.

### Gaps
- I found no public, documented open-source project that implements "relay consumer requests through volunteers' own closed-API keys". The design above is reconstructed by analogy.
- I could not verify the internals of Petals (hivemind DHT, libp2p relays for NAT traversal, fault-tolerant re-routing) from the README alone. Those are in arXiv 2209.01188 and 2312.08361, which I did not fetch.
- GitHub Actions self-hosted runners are another outbound long-poll analogue. The page I fetched did not contain the communication details, so they are unverified here.
- I did not research Kalavai, io.net or Hyperbolic's node-onboarding docs (registration, heartbeat intervals) in this session.
- AI Horde docs do not state outright that workers receive prompts in plaintext. It follows necessarily from workers running the generation, so I list it as an inference in Q3.

---

## Q3. Trust and privacy trade-offs: who sees what; E2E encryption; confidential computing

### Takeaway
Pattern B moves **credential custody** from the platform to the contributor, but it does not hide **request content**. The node operator, an anonymous contributor, sees every consumer prompt and response in plaintext, and so does the upstream vendor. The coordinator also sees them unless consumer-to-node encryption is added. Adding that encryption breaks the coordinator's ability to meter or audit content independently. Confidential computing offers a third option: a central gateway whose keys and plaintext sit inside an attested TEE that the operator cannot read (AWS Nitro Enclaves with KMS; Intel TDX plus NVIDIA H100 CC; Phala's attested gateway with signed receipts). TEEs address operator access. They do not change vendor terms.

### Cited Findings
- Petals' own warning about volunteer networks: "Your data will be processed with the help of other people in the public swarm"; use a private swarm for sensitive data. — [Petals README](https://github.com/bigscience-workshop/petals)
- In LocalAI, anyone holding the swarm token can join and use workers. The docs do not describe the encryption or access control behind the "secure and private" claim. — [LocalAI distributed docs](https://localai.io/docs/features/distribute/)
- AI Horde says "The Horde has no ads and does not sell your data". For OAuth2 accounts it stores only the account's unique ID. It does not state what workers can see. — [AI-Horde README](https://github.com/Haidra-Org/AI-Horde)
- OpenRouter (central) stores BYOK keys "securely encrypted". As the gateway it necessarily handles prompts, and it offers `zdr` and `data_collection: deny` routing to limit downstream retention. — [OpenRouter BYOK](https://openrouter.ai/docs/use-cases/byok); [Provider Routing](https://openrouter.ai/docs/features/provider-routing)
- **AWS Nitro Enclaves**: an enclave is a "separate, hardened, and highly-constrained" VM with "no persistent storage, interactive access, or external networking". Users "cannot SSH into an enclave", and its data "cannot be accessed by the processes, applications, or users (root or admin) of the parent instance". The enclave talks only to its parent over a local socket. AWS KMS key policies can make decryption succeed "only when the measurements of the enclave match the KMS key policy". — [AWS Nitro Enclaves docs](https://docs.aws.amazon.com/enclaves/latest/user/nitro-enclave.html)
- **NVIDIA H100 confidential computing** (blog dated 2023-08-03): works with AMD SEV-SNP or Intel TDX confidential VMs. The driver runs inside the CPU TEE and moves data through an "encrypted bounce buffer". Command buffers and kernels are encrypted and signed over PCIe. The GPU is attested with a device-unique ECC-384 identity key fused at manufacture, verified through NVIDIA's Remote Attestation Service (NRAS). This protects against an operator with access to "the hypervisor, or even the system itself". CPU-GPU bandwidth drops to about 4 GB/s, while GPU compute and HBM bandwidth are unchanged. — [NVIDIA Technical Blog](https://developer.nvidia.com/blog/confidential-computing-on-h100-gpus-for-secure-and-trustworthy-ai/)
- **Phala Confidential AI API**: an OpenAI-compatible endpoint (`https://inference.phala.com/v1`) behind an "Attested Confidential Inference (ACI) gateway". `GET /v1/aci/attestation` returns the gateway workload identity, the TEE quote, source provenance and the public keyset. Every response carries an `x-receipt-id`. The receipt "binds request and response hashes to the attested workload" and records whether the upstream provider was verified and channel-bound before the prompt was forwarded. — [Phala docs](https://docs.phala.com/confidential-ai-inference/confidential-ai-api)
- Other TEE inference projects found through search, not verified in depth: NEAR AI Confidential Inference Cloud (NVIDIA CC inside Intel TDX, OpenAI-compatible, claims open and reproducible images); OpenPcc (arXiv 2606.11145, a research prototype on TDX plus H100 with composite attestation). — [NEAR AI](https://cloud.near.ai/); [OpenPcc arXiv](https://arxiv.org/html/2606.11145v1)

### Inferences
- Visibility matrix:
```
                              | upstream key | consumer prompt/response | usage/tokens
 Pattern A gateway operator   |    YES       |   YES (plaintext)        |  YES (parses stream)
 Pattern B coordinator        |    NO*       |   YES unless E2E to node |  YES, or node-reported if E2E
 Pattern B node operator      |    YES (own) |   YES (plaintext, always)|  YES
 Upstream vendor              |    YES       |   YES                    |  YES
 TEE gateway operator         |    NO (sealed in enclave; KMS release by measurement) | NO (if attested code honest) | via signed receipts
 * only if the node software is honest; unverifiable if closed-source
```
- End-to-end encryption is only partly feasible. The consumer can encrypt the request to the node's public key so the coordinator relays ciphertext, but the node has to decrypt it to call the vendor over ordinary TLS. Encryption therefore ends at the node, which is the least-trusted party: an anonymous contributor who can log, modify or replay prompts. E2E protects consumers from the platform, not from the node. There is a further trade-off: if the coordinator cannot read the stream, it has to accept **node-reported** usage, which weakens metering (see Q4).
- For consumers, Pattern B is worse on privacy than Pattern A. In Pattern A they trust one identifiable operator. In Pattern B they trust that operator plus many unknown node operators. "Key never leaves" protects the contributor, not the consumer.
- The TEE approach ("Pattern C") is technically the strongest answer to "hold secrets centrally without operator access". Keys are sealed to an enclave measurement, and prompts are decrypted only inside attested code, with signed receipts. Even so, the egress to the vendor API carries the same credential and the same contractual relationship, so it solves an operator-trust problem and not a terms-of-service problem.

### Gaps
- I found no public documentation of a token-sharing platform that implements consumer-to-node E2E encryption or TEE-protected node agents.
- I did not fetch Intel TDX primary docs. TDX facts above come only through the NVIDIA blog and Phala/NEAR summaries.
- The details of Phala's receipt signature algorithm and key generation sit on a "Verify a Response" page I did not fetch.

---

## Q4. Metering and settlement in multi-party networks: verifying service, counting tokens, credit ledgers, escrow and disputes

### Takeaway
Token counting is straightforward when the coordinator can parse the upstream `usage` fields in the stream. It becomes a trust problem when the node is the only party that sees the vendor response, because a node can inflate usage or substitute a cheaper model. Documented verification techniques, mostly for open-weight inference, are: probabilistic spot-checks with stake (Hyperbolic PoSP), activation hashing that permits recomputation (Prime Intellect TOPLOC), TEE-signed receipts (Phala), and reputation or trust tiers (AI Horde). For closed vendor APIs, recomputation-based checks are not possible. Ledgers range from AI Horde's non-transferable, non-purchasable "kudos" (a mutual-aid model) to cash marketplaces, which add escrow and dispute requirements. I found little public documentation of escrow and disputes.

### Cited Findings
**Token counting sources**
- Anthropic: input tokens in `message_start.usage`; cumulative output tokens in `message_delta.usage`. — [Anthropic Streaming](https://platform.claude.com/docs/en/build-with-claude/streaming)
- OpenRouter: native-tokenizer counts; `cost`; `upstream_inference_cost` for BYOK only; usage in the terminal stream event; `/generation` lookup by id. — [OpenRouter Usage Accounting](https://openrouter.ai/docs/use-cases/usage-accounting)
- LiteLLM: a per-request ledger in `LiteLLM_SpendLogs` with `prompt_tokens`, `completion_tokens` and `total_tokens`, priced from the model cost map. — [LiteLLM cost tracking](https://docs.litellm.ai/docs/proxy/cost_tracking)

**Verification of work**
- Hyperbolic **Proof of Sampling (PoSP)** and **spML**: a Nash-equilibrium-based protocol that verifies only a random sample of outputs. When the challenge probability is above a threshold set by the stake and reward parameters, honest behavior is the dominant strategy. Sampling rates depend on node reputation and stake, and new nodes are sampled more. Developed with UC Berkeley and Columbia researchers (arXiv 2405.00295, first posted May 2024). — [arXiv 2405.00295](https://arxiv.org/html/2405.00295v2); [Hyperbolic blog](https://www.hyperbolic.xyz/blog/deep-dive-into-hyperbolic-proof-of-sampling). Cost-saving figures in the blog are vendor claims.
- Prime Intellect **TOPLOC** (arXiv 2501.16007, January 2025): locality-sensitive hashing of top-k intermediate activations, encoded as a polynomial congruence. A verifier recomputes and compares, which detects unauthorized changes to model, prompt or precision. The paper reports 258 bytes per 32 tokens for Llama-3.1-8B and validation up to 100x faster than inference. It is integrated with vLLM and SGLang. The figures are the authors' own. — [arXiv 2501.16007v2](https://arxiv.org/html/2501.16007v2); [PyPI toploc](https://pypi.org/project/toploc/0.0.1/)
- **TEE signed receipts**: Phala receipts bind request and response hashes to an attested workload. — [Phala docs](https://docs.phala.com/confidential-ai-inference/confidential-ai-api)
- **Reputation and trust**: AI Horde's trusted status takes about a week of operation plus substantial kudos, and untrusted accounts face suspicion checks and per-IP worker limits. — [Horde SDK definitions](https://horde-sdk.readthedocs.io/en/latest/haidra-assets/docs/definitions); [dbzer0 blog](https://dbzer0.com/blog/tag/aihorde/page/3/)

**Ledgers**
- AI Horde kudos: earned per completed job (scaled by complexity and resources) plus a "nominal amount" for being online, "awarded in 10-minute increments". Kudos are spent for queue priority. "Kudos cannot be bought or sold" and do not expire. The ledger is "event-sourced ... with reservations" and records charges and payments. Anonymous users get the lowest priority, but "everyone gets served". — [AI-Horde README](https://github.com/Haidra-Org/AI-Horde); [Horde SDK Workers](https://horde-sdk.readthedocs.io/en/latest/haidra-assets/docs/workers)
- LiteLLM budgets: `max_budget` and `budget_duration`, with automatic resets on virtual keys; `/global/spend/reset` zeroes spend while keeping the logs. — [LiteLLM cost tracking](https://docs.litellm.ai/docs/proxy/cost_tracking)

### Inferences
- Recommended settlement flow for Pattern B, a design inference that combines AI Horde reservations with gateway-style ledgers:
```
 request in -> RESERVE est. max cost from consumer balance (hold)
            -> dispatch to node -> stream relayed; coordinator parses usage (if it can see plaintext)
            -> on DONE: COMMIT actual cost (debit consumer), CREDIT node (minus platform fee), RELEASE remainder
            -> on fault before first byte: RELEASE + reassign;  mid-stream fault: COMMIT partial or refund per policy
 all entries append-only (event-sourced) -> periodic reconciliation / dispute window before credits become withdrawable
```
- Closed vendor APIs make verification structurally harder than open-weight inference. TOPLOC-style recomputation needs the weights, outputs are non-deterministic, and to my knowledge vendors do not sign their responses (unverified). That leaves spot-checks against a trusted reference account, redundant execution of sampled requests on two nodes, reputation and stake, and TEE-attested node agents. All of them are probabilistic, and sampling doubles the cost of each sampled request.
- If E2E to the node is used (Q3), the coordinator cannot parse usage and must trust node-reported counts. Privacy and metering integrity pull against each other.
- A mutual-aid credit model (non-transferable, no cash-out, as with kudos) limits fraud incentives and avoids payment, escrow and money-transmission obligations. A cash-payout model creates incentives to inflate usage or substitute models, and it needs escrow holds, withdrawal delays and dispute handling.

### Gaps
- I found no public primary documentation describing escrow or dispute workflows for decentralized LLM inference marketplaces (io.net, Hyperbolic marketplace and others were not fetched).
- I found no public documentation of how any token-sharing platform verifies that a node really called the claimed closed model.
- Whether OpenAI or Anthropic offer cryptographically signed responses is unverified. I found no source either way in this session.

---

## Q5. Vendor terms (public text only): credential sharing and reselling

### Takeaway
OpenAI's and Anthropic's published terms both prohibit sharing account credentials or API keys with others, making an account available to others, and reselling access without approval. Anthropic's Claude Code legal page explicitly bars third parties from routing requests through subscription credentials on behalf of their users, and from intermediating usage. Under these terms, the vendor-endorsed sharing patterns are BYOK (each end user brings their own key or subscription), authorized commercial arrangements, and open-weight models on contributed hardware. "The key never leaves the contributor's computer" changes where the key is stored. It does not change whose account serves third parties.

### Cited Findings
**OpenAI**
- Consumer Terms of Use (December 11, 2024 version, read via a third-party archive because openai.com returned 403): "You may not share your account credentials or make your account available to anyone else and are responsible for all activities that occur under your account." A third-party tracker reports a January 1, 2026 update that I could not confirm. — [Wind River archive of OpenAI ToU 2024-12-11](https://open.windriver.com/info/uni-license-list/licenses/openai-tou-20241211.html); [openai.com/policies/row-terms-of-use](https://openai.com/policies/row-terms-of-use/) (403 on fetch)
- Business Terms (API, Enterprise and Team), quoted through a third-party provision tracker and not verified on openai.com: "You may not make account access credentials available to third parties, share individual login credentials between multiple users on an account, or resell or lease access to your account or any End User Account." — [ConductAtlas: OpenAI Business Terms provision](https://conductatlas.com/platform/openai/openai-business-terms/provision/CA-P-059963/prohibition-on-sharing-account-credentials-between-users/); [openai.com/policies/business-terms](https://openai.com/policies/business-terms)
- Help Center, "Best Practices for API Key Safety": "OpenAI does not support the sharing of API keys." "Your API key is intended to be used by you. The sharing of API keys is against the Terms of Use." The article also tells account holders to give each team member their own key through the Members page. The cached copies differ in wording and date. — [OpenAI Help 5112595](https://help.openai.com/en/articles/5112595-best-practices-for-api-key-safety)

**Anthropic**
- Consumer Terms (effective October 8, 2025), Section 2: "You may not share your Account login information, Anthropic API key, or Account credentials with anyone else. You also may not make your Account available to anyone else." "You are responsible for all activity occurring under your Account". Section 3 prohibits developing competing products "or resell the Services", and prohibits automated or non-human access except "via an Anthropic API Key or where we otherwise explicitly permit it". — [Anthropic Consumer Terms](https://www.anthropic.com/legal/consumer-terms)
- Commercial Terms (effective June 17, 2025): A.1 allows using the Services "to power products and services Customer makes available to its own customers and end users". D.4 says Customer must not "access the Services to build a competing product or service, including to train competing AI models or resell the Services except as expressly approved by Anthropic", and (c) must not "support any third party's attempt" at restricted conduct. D.5 says "Customer is responsible for all activity under its account." I found no explicit API-key-sharing clause in the Commercial Terms. — [Anthropic Commercial Terms](https://www.anthropic.com/legal/commercial-terms)
- Claude Code "Legal and compliance" page (current as of November 2026):
  - "Anthropic does not permit third-party developers to offer Claude.ai login into their own applications, or to route requests through Free, Pro, Max, or Ultra plan credentials on behalf of their users. Moreover, developers may not collect, store, or intermediate Claude.ai credentials or session tokens."
  - "Customers may not pay for, resell, or intermediate Claude usage on their end users' behalf. Each end user must authenticate with their own Anthropic API key, Claude subscription plan credentials, or 3P inference provider credential."
  - Customers may still provision their own API keys "for use by the customer's own authorized users — provided the resulting usage is billed to the key owner ... and is not resold or intermediated".
  - "Advertised usage limits for Pro, Max, and Ultra plans assume ordinary, individual usage."
  - "Anthropic reserves the right to take measures to enforce these restrictions and may do so without prior notice."
  - Source: [Claude Code Legal and compliance](https://code.claude.com/docs/en/legal-and-compliance)

### Inferences
- Read literally, these terms cover Pattern A when the platform pools individuals' keys or subscriptions, and they cover Pattern B for the same reason. In both, one account holder's credential serves unrelated third parties, and both vendors say the account holder is "responsible for all activity". Keeping the key local is a credential-custody feature and has no bearing on compliance.
- Architectures consistent with the published text:
  1. BYOK gateways, where each consumer brings their own key and the gateway only routes (OpenRouter BYOK; Claude Code's "each end user must authenticate with their own" credential).
  2. An organization's own keys used internally by its authorized users and billed to that organization.
  3. Resale or aggregation under an express vendor agreement (Anthropic Commercial Terms D.4 "except as expressly approved").
  4. Volunteer or contributed-compute networks serving **open-weight models**: AI Horde, Petals, LocalAI federation.
- Whether hosted aggregators such as OpenRouter hold specific reseller agreements is not stated in the docs I read (see Gaps).

### Gaps
- openai.com policy pages returned HTTP 403, so the OpenAI text is taken from a third-party archive (Wind River), a provision tracker (ConductAtlas) and Help Center snapshots. The current live wording and effective dates are unconfirmed.
- I did not fetch the Anthropic Usage Policy (AUP) or the OpenAI Usage Policies. Credential and resale constraints mainly sit in the terms of service quoted above.
- I found no public statement of the commercial agreements under which aggregators like OpenRouter resell OpenAI or Anthropic models.
