# Use with coding agents

Claude Code and Codex can run the client for you. Install the client's skill once, then ask in plain words: "is ShareToken running?", "how many credits do I have left?", "call qwen2.5:7b through ShareToken and summarize this file", "share my local Ollama models".

The skill is a `SKILL.md` file that tells the agent which commands exist, where the local endpoint is, and what it must not do on its own.

## Install the skill {#install}

```bash
{{exe}} skill install
```

The client looks for Claude Code and Codex on this computer and installs the skill for each one it finds:

::: macos linux
| Agent | File |
|---|---|
| Claude Code | `~/.claude/skills/sharetoken/SKILL.md` (under `$CLAUDE_CONFIG_DIR` when it is set) |
| Codex | `~/.codex/skills/sharetoken/SKILL.md` (under `$CODEX_HOME` when it is set) |
:::

::: windows
| Agent | File |
|---|---|
| Claude Code | `%USERPROFILE%\.claude\skills\sharetoken\SKILL.md` (under `%CLAUDE_CONFIG_DIR%` when it is set) |
| Codex | `%USERPROFILE%\.codex\skills\sharetoken\SKILL.md` (under `%CODEX_HOME%` when it is set) |
:::

If an agent has never been used on this computer, its directory does not exist and it is skipped. To install for one anyway, name it:

```bash
{{exe}} skill install -agent claude
{{exe}} skill install -agent codex
{{exe}} skill install -agent all
```

Restart an agent session that is already open; skills are read when a session starts.

## Check {#check}

```bash
{{exe}} skill
```

This lists both agents, whether they were found, and whether the skill is installed and up to date. `{{exe}} skill show` prints the skill itself.

The copy written for you names this client's command, state directory `{{home}}` and endpoint `http://127.0.0.1:{{port}}/v1`. When `{{exe}}` is not on PATH, it names the program's full path instead.

## What the agent may do {#rules}

The skill lets the agent read freely: `status`, `balance`, `jobs`, `models`, `relays`, `share`, `pair`, `config`. It asks the agent to:

- Ask you before it changes anything: signing out, sharing on or off, adding or removing models and providers, pairing.
- Treat credits as money: set `max_tokens` on requests, and ask before large batches or long generations.
- Never put an API key on a command line, and never print your token.
- Ask you for your email address and for the sign-in code from the email. It cannot sign in without you.
- Start the client in the background, or ask you to start it, when it is not running.

An agent follows these rules, but it is not forced to. Keep your agent's own permission prompts on for commands it runs.

## After an update {#update}

Run `{{exe}} skill install` again after [updating the client](/docs/update). It replaces the copy it wrote earlier and leaves an up-to-date copy alone.

## Remove {#remove}

```bash
{{exe}} skill uninstall
```

This removes the skill from both agents. `-agent claude` or `-agent codex` removes it from one.

A `SKILL.md` in the same place that the client did not write, for example one you wrote yourself, is never replaced or removed. `{{exe}} skill install -force` replaces it.
