# Warm-up: starting usage windows early

**English** | [简体中文](zh-CN/warmup.md)

A Claude subscription's 5-hour window, and a ChatGPT (Codex) one's, starts at the
account's first request after it resets, not at a fixed time. Work that starts at
9:00 gets the windows 9–14 and 14–19; one tiny request at 6:00 gets 6–11, 11–16 and
16–21: the first window, mostly unused, resets at 11:00 instead of 14:00. The same
holds for the weekly window: one that nobody uses for a day after it resets ends a
day later too.

The warm-up sends that one tiny request when the user wants a window to start. It
is modeled on magpie's daily warm-up. Package: `internal/warmup`; the seller side
is in `internal/provider/warm.go` and `limits.go`.

## 1. What is warmed up

| Account | How the request goes | Settings kept in |
|---|---|---|
| The Claude Code of this machine (`local:claude`) | the `claude` CLI, as the user signed it in | `warmup.json` |
| The Codex of this machine (`local:codex`) | the `codex` CLI, through its ChatGPT sign-in | `warmup.json` |
| A seller's Claude or ChatGPT login (`provider:<id>`) | the provider's own upstream, as its jobs go | the provider's `warmup` in `providers.json` |

None of it goes through the sharetoken network: no relay, coordinator, portal,
buyer, ledger or node. A provider's warm-up is no job: it takes no concurrency slot
and earns or costs no credits. Only OAuth logins of type `anthropic` and `openai`
have windows to warm up; API-key providers have none.

## 2. When a warm-up is sent

Each account has:

- **Times of day** (`times`, local time, at most 6, e.g. `06:00`, `11:05`). At each,
  if the 5-hour window isn't running, one request is sent. Each time of day is had
  once a day: the last one handled is kept, so a restart sends nothing twice. A
  machine asleep at the time sends it on waking within an hour; later it is skipped,
  as a window started hours late is not the one wanted. A window that is running at
  the time but runs out within that hour is started after it ends.
- **On reset** (`on_reset`): `week` starts the weekly window as soon as it resets,
  `all` the 5-hour window too. A 5-hour window that, started now, would still run
  at the next time of day waits for that time instead (the window ending at 2:00
  is not restarted to run past 6:00); so does the weekly window while the 5-hour
  one is idle, as the one request starts both. A window never read is started once,
  then again a whole window after the last warm-up. A window a warm-up left not
  started, as far as is read, waits 15 minutes for the next try, doubling up to 4
  hours.

Nothing is sent:

- while a window still running is used up, or the account is on extra usage or
  credits, or the vendor refused it for its allowance: the request would be billed
  past the plan, or fail (`used_up`);
- for a provider that answered a request in the last 10 minutes: its window is
  running (`running`);
- for a provider that is off (`disabled`), or resting after a 429;
- for an agent whose program isn't found (`not_installed`);
- while all warm-ups are paused (`paused`; *Warm up now* still works).

A failed warm-up is tried again 5 minutes later, three times per time of day.

The service looks every minute, by the wall clock, so a machine that slept notices
the time on waking. The client must be running: the desktop app (`sharetoken
daemon`, logged in or not) or `sharetoken up`. Neither needs the portal.

## 3. Reading the windows without spending them

| Account | Read from |
|---|---|
| Claude Code | `claude -p /usage`, which Claude Code answers itself without a model: `Current session: 34% used · resets Nov 20 at 11:50am (Asia/Singapore)` |
| Codex | `GET https://chatgpt.com/backend-api/wham/usage` with the token in `$CODEX_HOME/auth.json` (`~/.codex`), read only: the token is never renewed here. A sign-in kept in the keychain, or an expired token, reads as unknown |
| A provider | the headers of every answer through it, a job's or a warm-up's: `anthropic-ratelimit-unified-5h-utilization` / `-reset` (and `7d`, `-status`, `-representative-claim`), `x-codex-primary-*` / `x-codex-secondary-*` (`used-percent`, `window-minutes`, `reset-at` or `reset-after-seconds`). No extra request |

A window counts as not started when nothing of it is used and its reset is unknown
or a whole window away (less 10 minutes): the ChatGPT backend reports an idle
window as resetting a full window from now. When nothing can be read, the times of
day still send their request: a request into a running window costs a few tokens
and changes nothing.

A local agent's windows are read before a time of day's request, every 30 minutes
while it starts windows on reset (and as soon as one was to reset), and after each
warm-up. A provider's come for free with its traffic.

## 4. The request

- **Claude Code**: in a new empty directory, with `hi` on standard input:
  `claude -p --output-format json --model haiku --tools "" --strict-mcp-config --setting-sources "" --no-session-persistence`.
  No tools, MCP servers, user settings or hooks; no session is kept.
- **Codex**: `codex exec --skip-git-repo-check --ephemeral -s read-only -c model_provider="openai" -c model_reasoning_effort="low" [-m MODEL] hi`,
  in a new empty directory.
- Both run without `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_BASE_URL`,
  `OPENAI_API_KEY`, `OPENAI_BASE_URL` and the like, which would send the request to an
  API key or another endpoint (sharetoken's own among them) instead of the
  subscription. The program's directory and the usual install directories go on the
  `PATH`, as the desktop app starts with a short one and an npm install is a node
  script. A command that does not answer within 2 minutes is ended with its process
  tree (on Windows without a console window).
- **A provider**: one streamed chat request, `max_tokens` 16, `hi`, through
  `Entry.Upstream`: the same request profile, proxy, TLS and token renewal as a
  job. The model is the one set, else the family's small one (Haiku; a `mini`
  Codex model), else what the provider sells; one refused as unknown (404, or a 400
  about the model) moves on to the next.

The agents' programs are found as set (`binary`), else on the `PATH`, else in the
usual install directories (`~/.claude/local`, `~/.local/bin`, Homebrew,
`%APPDATA%\npm` …). A wrapper of a terminal app (a `…shims/` directory) is used
only when nothing else is found.

## 5. Settings and state

`warmup.json` (0600), changed by the CLI or the local API; the running client reads
it at each look:

```json
{
  "paused": false,
  "claude": {"enabled": true, "times": ["06:00", "11:05"], "on_reset": "all", "model": "", "binary": ""},
  "codex":  {"enabled": false}
}
```

A provider's, in `providers.json` (a new login of the same name keeps it):

```json
{"name": "max", "type": "anthropic", "auth": "oauth", "warmup": {"times": ["06:00"], "on_reset": "week", "model": ""}}
```

`warmup-state.json` (0600) keeps, by account, the last warm-up, its result and
error, the time of day last handled, failures, the backoff of warm-ups on reset, and
the windows as last read.

## 6. Command line

```
sharetoken warmup                                   # every account, its times, the last result, its windows
sharetoken warmup set claude|codex [-times 06:00,11:05] [-on-reset off|week|all] [-model M] [-binary PATH] [-off]
sharetoken provider warmup NAME [-times 06:00] [-on-reset off|week|all] [-model M] [-off]
sharetoken warmup run [claude|codex|provider:NAME]  # now; default: every one that is set
sharetoken warmup pause | resume
```

`-times none` clears the times; `-model default` the model; `-binary auto` the path.
Settings can be changed with the client stopped; `run` needs it running.

## 7. Local API

Served by the desktop daemon (logged in or out) and by `sharetoken up`, behind the
local API key:

| Method and path | Does |
|---|---|
| `GET /api/v1/warmup` | `{paused, zone, settings, targets: [...]}`; each target has its settings, `skip`, `binary`/`version`, `next`, and its state (`last`, `last_try`, `result`, `error`, `last_model`, `usage`) |
| `PUT /api/v1/warmup` | `{"paused": …, "claude": {…}, "codex": {…}}`: what is given is replaced |
| `PUT /api/v1/warmup/providers/{name}` | the provider's `{times, on_reset, model}`; `{}` turns it off |
| `POST /api/v1/warmup/run` | `{"target": "local:claude" \| "codex" \| "provider:NAME"}`: warm up now; 502 with the target when it failed |

`GET /api/v1/providers` also tells, for a Claude or ChatGPT login,
`warm_capable`, its `warmup` and its `usage` (the windows its answers told).

The desktop app shows it under Settings → Warm-up, and each provider card in Share →
Providers shows its windows and warm-up.

## 8. Verified, and limits

- Unit tests: `go test ./internal/warmup ./internal/provider` (times of day, slack,
  restart, running and ending windows, used up, recent job, failures, pause,
  on-reset with its wait for the time of day, `/usage` and `/wham/usage` parsing,
  usage headers, a provider warm-up through the Codex upstream on a mock server).
- UI: `scripts/test/gui_ui_test.mjs` sets up a warm-up in the page and warms up a
  stand-in `claude`.
- By hand, on macOS and on the PVE VMs (Ubuntu 24.04, Windows 11), with Claude Code
  2.1.329 and Codex 0.171.0: warm up now, their windows read after; a time of day
  reached with the 5-hour window running recorded as `running` with nothing sent;
  no second send after a restart; nothing sent while paused. The client ran with a
  bare `PATH` (`/usr/bin:/bin`; on Windows without the npm, nodejs and
  `.local\bin` directories), no `USER`, and bogus `ANTHROPIC_*` / `OPENAI_*`
  variables. On Windows the native `claude.exe` and npm's `claude.cmd` and `codex.cmd`
  both work; an empty argument (`--tools ""`) reaches the program through `cmd.exe`.
  `/usage` prints `Nov 20 at 11:50am` on macOS and `Nov 20, 10:30pm` on Linux and
  Windows; both are read.
- Not verified against a real seller subscription yet; the response headers of
  both vendors are read as Claude Code and Codex read them.
- The machine must be awake and the client running. To warm up while the machine
  sleeps, wake it on a schedule (`pmset repeat wake …` on macOS, a Task Scheduler
  task with *Wake the computer* on Windows); the client changes no system settings.
- A local agent is warmed up as whichever account its CLI is signed in to. An
  account used both here and by a seller is warmed up by each; a second `hi` in a
  running window does no harm.
- `/usage`'s wording changes with Claude Code's versions; a reading it cannot parse
  counts as unknown, and the times of day send their request anyway.
