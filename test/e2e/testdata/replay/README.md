# Replayed model-server streams

Streams in the wire format of model servers sharetoken has no account at in
its tests: the ChatGPT Codex backend (`/backend-api/codex/responses`, a
ChatGPT login) and Google's Code Assist backend
(`/v1internal:streamGenerateContent?alt=sse`, a Gemini CLI login). They follow
the shapes those servers send (event order, field names, the events and fields
clients do not use, such as `codex.rate_limits` and `trafficType`), with
placeholders the replay server (`replay_test.go`) fills in for each request:

| placeholder | value |
|---|---|
| `{{ID}}` | a fresh ID |
| `{{NOW}}` | the time in seconds |
| `{{MODEL}}` | the model the request named |
| `{{TOOL}}` | a tool the request offers (a shell or file-reading tool) |
| `{{CALL}}` | the call ID |
| `{{ARGS}}` | the call's arguments as a JSON string's contents |
| `{{ARGS_A}}`, `{{ARGS_B}}` | the same, in two pieces |
| `{{ARGS_OBJECT}}` | the arguments as a JSON object |
| `{{TEXT_A}}`, `{{TEXT_B}}`, `{{TEXT}}` | the answer (it quotes the tool's output) |

`*_tool.sse` answers a request that offers tools and has no tool result yet;
`*_text.sse` answers the rest.
