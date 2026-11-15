# Captured client requests

Requests the real clients sent to a capture server standing in for the model
server (2026-11-15): Claude Code 2.1.325 (`ANTHROPIC_BASE_URL`) and Codex
0.144.6 (a custom provider with `wire_api = "responses"`), each asked to read
a file. Each file is `{method, path, headers, body}`, `body` the request as a
JSON string.

| File | What |
|---|---|
| `claude-code-2.1.325-turn1.json` | The first request: system blocks with the billing line and `cache_control`, a mid-conversation `system` message, tools (one with `defer_loading`), adaptive thinking, `output_config.effort`, `context_management` |
| `claude-code-2.1.325-turn2-tool-result.json` | The next one: the assistant's signed thinking block and `tool_use`, the `tool_result` |
| `codex-0.144.6-turn1.json` | Codex's first request: instructions, developer messages, function tools, the `apply_patch` custom tool with its grammar, `tool_search`, `web_search`, `prompt_cache_key`, `client_metadata` |
| `codex-0.144.6-compaction-turn.json` | Codex compacting its context with a normal request (`request_kind: compaction` in its turn metadata) |
| `codex-0.144.6-after-compaction.json` | The request after it, carrying the handoff summary |

Sanitized: IDs (device, installation, sessions) replaced, paths replaced with
`/home/user/...`, credentials and transport headers dropped, and long system
prompts, instructions and tool descriptions cut to their first 160 characters.
Their structure (blocks, roles, tool names, schemas, grammars,
`cache_control`) is as the clients sent it.
