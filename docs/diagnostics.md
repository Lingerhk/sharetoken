# Logs and crash reports

How the client logs, records crashes, and reports them to the portal. User docs: `/docs/config#logs`, `/docs/cli#logs`.

## Log files

`sharetoken up` and `sharetoken daemon` call `client.StartDiag` (internal/client/diag.go), which opens the log through `diag.Setup` (internal/diag/log.go):

- Directory: `$SHARETOKEN_LOG_DIR`, else `-log-dir` (global flag, before the command), else `config.json` `log.dir`, else `<home>/logs`. Read once at start; a change needs a restart.
- `client.log`, slog text handler, level `log.level` (default info). Rotated in-process by size (`log.max_size_mb`, default 10) into `client.log.1 … .N` (`log.max_files`, default 5). If a rename fails (Windows, a reader holding the file), the file is copied to `.1` and truncated.
- Also written to stderr when stderr is a terminal (`up` in a shell). Otherwise only to the file, so a daemon started by the desktop app, launchd, systemd or the Windows logon task logs the same way (before 1.0.6 a daemon started by the Windows logon task logged nowhere).
- The desktop shell (gui/logfile.go, own module: it duplicates the few lines it needs) writes `gui.log`, rotated at start, and redirects the daemon it starts to `daemon.stderr` (truncated per start): only what the daemon prints before its log is open, Go's own crash output, and `error:` lines from `main`. The macOS LaunchAgent written by `sharetoken autostart on` points StandardOut/ErrorPath at the same file.
- At daemon start the logs of older releases (`<home>/daemon.log`, `~/Library/Logs/<name>.log`) are moved into the directory as `daemon.log.old` / `launchd.log.old`.
- CLI: `sharetoken logs [-n N] [-f] [-gui]`, `sharetoken logs path`. Desktop: Settings → General → Diagnostics → "Open log folder" (`POST /api/v1/logs/open`; `$SHARETOKEN_OPEN_DRY_RUN` makes it only check the directory, for tests).

## Crashes

- `diag.StartCrashCapture` opens `crash/crash-<process>-<UTC time>-<pid>.txt` and calls `runtime/debug.SetCrashOutput`: an unrecovered panic in any goroutine, or a fatal runtime error, is written there as well as to stderr. A clean exit removes the empty file (`Finish`).
- Fatal errors (`throw`, e.g. concurrent map writes) do not write their `fatal error: …` line to the crash output, only the traceback. The collector looks for the line in `daemon.stderr` when that changed within 10 s of the crash; otherwise the message is `fatal error: <first runtime frame>` (e.g. `internal/runtime/maps.fatal`).
- At the next start of `up`/`daemon`, `diag.Collector` handles every crash file of another process (the GUI's too): a non-empty one becomes a report (when reports are on) with the last 100 lines of that process's log written before the crash, then moves to `crash/old` (5 kept). Empty files of dead processes are removed; files of live processes are left alone.
- Recovered panics: `diag.HTTPErrorLog` is the `ErrorLog` of both local HTTP servers, so a handler panic that net/http recovers becomes a `panic` report at once. Long-running goroutines of the daemon and agent run through `diag.Wrap`/`diag.Go`, which record the panic immediately (kind `crash`, message `… (in <name>)`) and re-panic; the crash file of the same panic is then deduplicated in the queue (same fingerprint, process, within 10 minutes).
- The fingerprint (16 hex) is sha256 of `function@file:line` of the frame after the last `panic(` frame of the first goroutine (or the first non-runtime frame), else of the message with numbers removed.

## Reports

`diag.Report`: id, kind (`crash`|`panic`), process (`daemon`|`cli`|`gui`), fingerprint, version, env, os, arch, os_version, occurred_at, message (≤1 KiB), stack (≤32 KiB), context (≤100 lines × 512 B), features (on/off of share, tailnet, private relay, keep_official, logged_in, tee, transport).

- Redacted (internal/redact) before it is written to `reports/`: what is queued is what is sent, and the desktop app shows the queue verbatim ("View pending reports", `GET /api/v1/diagnostics/reports`; CLI `sharetoken diagnostics show`).
- `redact.Line` parses slog text lines: allow-listed keys keep values, `user`/`email`/`device`/`home`/`file`/`code`/… are dropped, peer keys become `self:<last 6>` or `peer#<hmac6>` (salt per report), address keys keep only loopback/LAN/public class, `err`/`msg`/`reason` go through `redact.String` and are cut to 300 bytes, unknown keys are dropped.
- `redact.String`: e-mails, `Bearer`/`Basic` values, `sk-…`, `st_…` tokens, JWTs, `token=`/`api_key=`/… values, hex ≥32 and base64 ≥48 runs, URLs (scheme + host class + port; a few public API hosts kept), multiaddrs, IPv4/IPv6 (loopback kept), home directories, OS user and host names, pairing codes.
- Queue: `logs/reports/<id>.json`, at most 50 files / 2 MiB, oldest dropped.
- Sending (`diag.Uploader`): first round 30 s after start, then every 10 min, and at once after a sign-in or turning reports on. `POST /api/v1/client-reports` with the user's token. 2xx and 400/413/422 remove the report; anything else keeps it and ends the round. Logged out: nothing is sent. `$SHARETOKEN_DIAG_FIRST` / `$SHARETOKEN_DIAG_EVERY` shorten the timing in tests.

## The switch

`config.json` `diagnostics.upload` (`*bool`). Unset: `diag.DefaultUpload()`, which is false in the European Economic Area (EU + IS, LI, NO): country from macOS `AppleLocale`, Windows `GetUserDefaultGeoName` (then the locale), else `LC_ALL`/`LC_MESSAGES`/`LANG`; or time zone (`$TZ`, `/etc/localtime`) in an EEA zone. `$SHARETOKEN_REGION` (two letters) overrides. Read at each report and each upload round, so the switch takes effect at once; turning it off empties the queue. API: `GET/PUT /api/v1/diagnostics`; CLI: `sharetoken diagnostics [on|off|show]`.

## Portal

- Migration portal 4: `client_reports`, one row per (user, fingerprint, version, process) with `count`; a resent report id is not counted twice; a `fixed` problem reported again goes back to `new`.
- `POST /api/v1/client-reports` (`clientOnly` + `auth`): validates (`Report.Check`), redacts again, limits 20/hour per user and 60/hour per IP (in memory), body limit 160 KiB.
- Admin: `GET /api/v1/admin/client-reports` (grouped by fingerprint; `status=new|known|fixed`, `q=` fingerprint/message/version, CSV export), `GET …/{fp}` (rows per user/version, latest stack and context), `POST …/{fp}/status` (`client_report.status` in the audit log). Console page 客户端异常, dashboard card 客户端异常（7 天）.
- Retention: rows not reported for 90 days are deleted every 6 hours (`Server.Run`).

## Tests

- `go test ./internal/redact` (+ `-fuzz FuzzNoLeak`), `./internal/diag` (real crashing subprocesses: panic, wrapped goroutine, fatal error, with and without daemon.stderr; HTTP handler panic; queue; uploader; rotation; region), `./internal/client -run 'Diag|StartDiag'` (daemon API, sign-in → portal), `./internal/portal -run ClientReport`.
- `scripts/test/gui_ui_test.mjs` (settings row) and `scripts/test/portal_ui_test.mjs` (admin page, detail, fixed, dashboard).
