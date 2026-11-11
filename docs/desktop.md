# Desktop client (macOS, Linux, Windows)

**English** | [简体中文](zh-CN/desktop.md)

The desktop client covers signing up with an email address, signing in and out, viewing the account and the network, and setting up seller mode (the Sharing view, below).

## Build types

Every client is built as either `dev` or `prod` ([`internal/client/env.go`](../internal/client/env.go), [`gui/env.go`](../gui/env.go)). The two can be installed on one machine and do not affect each other. A plain `go build` names no type and is treated as `dev`, so that a forgotten flag cannot reach production.

| | dev | prod |
|---|---|---|
| Portal | `https://dev.sharetoken.app` | `https://sharetoken.app` |
| Data directory | `~/.sharetoken-dev` | `~/.sharetoken` |
| Local port (spares) | 9887 (9888–9897) | 9787 (9788–9797) |
| CLI and daemon | `sharetoken-dev` | `sharetoken` |
| Window program | `sharetoken-gui-dev` | `sharetoken-gui` |
| macOS app | `ShareToken-dev.app`, `app.sharetoken.desktop.dev` | `ShareToken.app`, `app.sharetoken.desktop` |
| Usage statistics | not sent | sent |
| `SHARETOKEN_PORTAL` | honoured (for test networks) | ignored |

- `sharetoken version` (`sharetoken-dev version`) prints the build type, the portal and the data directory.
- The window only connects to a daemon of its own build type. A dev window and its page title say DEV.
- `SHARETOKEN_HOME` overrides the data directory.
- `config.json` records its format version and the build type that wrote it. A file written by a newer client is refused. A file in an older, unsupported format is deleted together with `device.key` and `network.json`, and the user signs in again; `telemetry.json` is kept because it holds the user's opt-out. A file of the other build type is refused and left alone, so pointing `SHARETOKEN_HOME` at the wrong directory cannot delete another environment's identity.

## Architecture

```
sharetoken-gui (Wails window, gui/)        sharetoken daemon (internal/client/daemon.go)

  ┌────────────────┐  fetch   ┌───────┐  HTTP on   ┌─────────────────────────────────────┐
  │ system WebView │ ───────▶ │ proxy │ ─────────▶ │ /          page (internal/client/ui) │
  └────────────────┘  (same   └───────┘  127.0.0.1 │ /api/v1/…  see the table below       │
                      origin)                      │ /v1/…, other /api → Agent            │
                                                   └─────────────────────────────────────┘
                                                       │ HTTPS               │ libp2p
                                                       ▼                     ▼
                                                     portal        relay → coordinator / sellers
```

- **All logic lives in the daemon.** The page only calls the local API. The window finds or starts a daemon, forwards requests to it, and stops the daemon it started when it quits.
- The window shell is a **separate Go module** (`gui/`). Only it needs a WebView (and cgo on macOS and Linux), so the server programs and the CLI keep their own dependencies and builds.
- The daemon serves the page, so everything can be tested without a window: the API with Go tests, the page with headless Chrome.
- The daemon **runs without a login** (`sharetoken up` does not). After sign-in it starts the Agent in the background. If the network cannot be reached it retries, backing off from 2 seconds to 1 minute; account data stays available because it only needs the portal.
- If a daemon is already running, the window connects to it and leaves it running on exit.
- If the default port is taken by an unrelated program, the window starts its daemon on the first free spare port, and the page shows the actual address. A port set in `config.json` is not replaced; that case is an error. If `sharetoken up` is already running on the same data directory, the window reports an error too, because the same device key would otherwise be online twice.
- The window holds the stdin of the daemon it starts (`-stop-on-stdin-eof`). When the window exits or crashes, the pipe closes and the daemon shuts down in order.

## Sharing view (seller mode)

The top bar of the main page switches between Overview and Sharing (`shareView` in [`internal/client/ui/app.js`](../internal/client/ui/app.js)); the choice is kept in `localStorage.st_view`. The Sharing view only calls the local API, and edits the same `config.json` and `providers.json` as the command line, so changes made on either side show on the other. See [providers.md](providers.md) for the implementation.

| Area | What it does | API |
|---|---|---|
| Sharing state | Start and stop sharing. Tiles: models on sale, jobs done and failed, in progress / concurrency, earnings of this run and time of the last offer, total earned by the account. Warnings from the daemon | `POST /api/v1/share/start`, `/stop`, `GET /api/v1/status`, `/api/v1/account` |
| Relay reservations | While sharing runs: for each relay, whether a reservation is held, its latency and expiry | `share.reservations` in `GET /api/v1/status` |
| Recent jobs sold | Model, state, tokens and earnings of the last 10 jobs sold | `GET /api/v1/jobs?role=sell&limit=10` |
| Provider APIs | Add a provider (API key or account login), turn it on or off, replace its credentials, remove it | `/api/v1/providers`, `/api/v1/providers/oauth/start`, `/finish` |
| Provider models | Pick from the provider's model list (or type a name), set the network ID, input and output price and output limit; test, edit, remove | `/api/v1/providers/{name}/models`, `/test` |
| Local model server | Address and concurrency; detect its models, then add, edit, remove | `PUT /api/v1/share`, `GET /api/v1/share/detect` |

- The data comes from `GET /api/v1/status`, polled every 5 seconds (`share.providers`, `share.serving`, `warnings`). A list is only redrawn when its content changed. Forms live in dialogs that polling does not touch, so input is never lost.
- These endpoints belong to the Agent, so until the client has joined the network the view only says that sharing can be set up once connected.
- Prices are entered in credits per million tokens and stored in micro-credits. The defaults are 0.1 and 0.2, the same as `share add`. When adding a model that is already on the network, the form shows its lowest price.
- An account login takes two steps: the page shows the sign-in address (copy it or click to open it), and the user pastes back the authorization code or the callback address.
- Guards in the page: sharing cannot start without a model; one model ID cannot come from two sources; a new provider whose name is already taken must be renamed (the API would treat it as a credential replacement); when the last sellable model is removed or its provider is turned off, sharing turns off with it.
- One difference from the command line: `share add` also turns sharing on; the page leaves that to the Start button.
- The model table's "lowest price on the network" comes from `GET /api/v1/models` (which includes this machine's own offers). A provider card shows when its login token expires and the names of its extra request headers. The Overview has a Sharing tile that opens the Sharing view, and its network section shows link quality to the coordinator (average latency, jitter, loss).
- The client is in English and Simplified Chinese. Without a saved choice it follows the system language; any other system language gets English (see [i18n.md](i18n.md)).
- A key passes through the page only when it is submitted; the API returns it masked. The UI test checks that no key appears in the page or in `/api/v1/status`.
- Daemon warnings and errors returned by a provider are shown as they are, in English.

## Menu bar and tray

The app shows an icon in the menu bar on macOS and in the notification area on Windows. Linux has no tray: closing the window quits the app and stops the daemon.

| Action | Result |
|---|---|
| Close the window (red button, X, Cmd+W on macOS) | The window is hidden. The app and the daemon keep running; sharing and the local API are not interrupted |
| Left-click the icon on Windows | Opens the window (restores it if minimized) |
| Click the icon on macOS, right-click on Windows | Menu: a status line (not clickable), an update line when a new version exists (opens the window), Open, Quit |
| Quit in the menu, Cmd+Q on macOS | Quits the app; a daemon that this app started stops with it |
| Start the program again | One instance per data directory: the second process brings the window back and exits. dev and prod, and different data directories, do not affect each other |

- The status line is refreshed every 3 seconds from `GET /api/v1/session`, for example `Connected · a@example.com`, `Not signed in`, `The background service is not running`.
- New versions: one system notification per new build ([`gui/notify.go`](../gui/notify.go); clicking it opens the window). Linux sends it too, without a tray. The rules are in [versioning.md](versioning.md), under "Update notices". On Windows, Wails registers a notification activator under HKCU; the uninstaller removes it.
- The menu text follows the page's language: each time the page loads, it reports its language to the shell (`/__shell/lang`, which never reaches the daemon). The text is in [`gui/tray_text.go`](../gui/tray_text.go); a test checks it against the page's language list and the page's wording for the shared entries.
- Windows 11 puts new icons in the `^` overflow area by default; the user can drag the icon onto the taskbar.
- The icons are generated with `go run packaging/gen_tray_icons.go` (run in `gui/`). The macOS icon is a template image coloured by the system; the Windows icon is a colour ICO.
- Implementation: macOS uses AppKit directly (`tray_darwin.m`), because the common Go tray libraries and Wails each define an `AppDelegate`, which clashes at link time. Windows uses `fyne.io/systray` on its own thread.
- **Quitting on Linux:** Wails calls `gtk_main_quit` from a goroutine. GTK is not thread-safe, and the signal is sometimes lost: the window is gone but the process and the daemon stay. `quit_linux.go` uses `OnBeforeClose` to queue the call on the GTK thread.
- **Focus guard on Windows:** on `WM_SETFOCUS` Wails calls WebView2's `MoveFocus` and exits the process with `os.Exit(1)` if that fails, which can happen while the window is minimized or hidden. `tray_windows.go` intercepts the message in that state before Wails' window procedure, and opens the window without Wails' `WindowShow`. Rerun the tray self-test after upgrading Wails.

## Local API of the daemon

Errors have the form `{"error":{"code","message"}}`; the page chooses its text by `code`.

| Endpoint | Description |
|---|---|
| `GET /api/v1/session` | `{env, state, logged_in, portal, user, device, peer, api, error, telemetry, version, api_version, update}`. `state` is `logged_out`, `expired` (the portal rejected the token and the daemon signed out), `starting`, `retrying` (`error` says why) or `running`. When signed out, `portal` is the built-in address (below) and `user` is the last one used, to prefill the form |
| `POST /api/v1/portal/probe` | `{}` (`portal` is optional and defaults to the built-in address) → `{portal, https, trusted, fingerprint, subject, not_after, known, pin_changed, pin_required, registration_open, signup_credit_micros, email_verification, support_email}`. Called before any password is sent |
| `POST /api/v1/register/code` | `{pin, email}`: sends a verification code, when the portal requires one |
| `POST /api/v1/register` | `{pin, email, password, code, device}` → 201 and the session |
| `POST /api/v1/login` | `{pin, account, password, totp, device}` → 200 and the session. `account` is an email address or a user name. An administrator first gets 401 `mfa_required` and resends with `totp` |
| `POST /api/v1/logout` | Stops the Agent, revokes the token (skipped if the portal is unreachable) and removes the token from `config.json`; other settings stay |
| `GET /api/v1/account`, `/api/v1/jobs`, `/api/v1/ledger` | Passed through to the portal; they do not need the Agent. If the portal answers 401, the session becomes `expired` |
| `GET`/`PUT /api/v1/telemetry` | The usage-statistics switch (below) |
| `POST /api/v1/update/check`, `/skip`, `/notified` | Update notices, see [versioning.md](versioning.md) |
| `POST /api/v1/feedback`, `GET /api/v1/feedback/mine` | Send feedback to the portal, and list what this account sent |
| Other `/api/*` and `/v1/*` | Same as `sharetoken up` (see [guide.md](guide.md)). 401 `not_logged_in` when signed out, 503 `not_connected` before the network is joined |

**Login expiry.** The Agent asks the portal once a minute whether its token is still valid (an unreachable portal does not count as a rejection). A device key can still trade with the coordinator after its token is revoked, so the client must stop: the daemon goes to `expired`, and `sharetoken up` exits and asks the user to sign in again.

**The portal address is built in.** It is the build type's portal (`client.BuiltInPortal`). The sign-in page does not ask for an address; it connects when opened, and shows the reason and a Retry button if it cannot. A request that omits `portal` (or leaves it empty) uses the built-in address. A dev build can be pointed at a test network with the environment variable `SHARETOKEN_PORTAL=https://…` (read by both the daemon and the CLI; the CLI has no `-portal` flag). A prod build ignores it, so the environment cannot send passwords elsewhere. An account that is signed in keeps using the portal saved at sign-in. The `TestMain` of the package's Go tests points the variable at the unreachable `127.0.0.1:1`, so tests never touch a real portal.

Common error codes: `bad_portal`, `insecure_portal`, `portal_unreachable`, `not_a_portal`, `pin_required`, `pin_mismatch`, `bad_request`, `exists`, `registration_closed`, `email_required`, `code_cooldown`, `bad_credentials`, `throttled`, `mfa_required`, `bad_code`, `mfa_setup_required`, `already_logged_in`, `bind_failed`, `bad_origin`, `bad_host`.

## The account is an email address

- Self-service sign-up uses an email address. It is normalized (trimmed, lower-cased) and stored as the account name. The maximum length is 191 characters.
- Accounts that an administrator created with a plain user name also work; the sign-in form accepts either.
- By default the portal does not check that the address exists. It does once verification codes are turned on.

### Email verification codes

When the portal sets `accounts.email_verification`, sign-up needs a code sent by email, and sign-up with a plain user name is closed; see [mail.md](mail.md). `/api/v1/portal/probe` returns `email_verification`, and the page shows the code field accordingly. `POST /api/v1/register/code {pin, email}` sends the code, and `POST /api/v1/register` carries it in `code`.

## Security

| Concern | What the client does |
|---|---|
| Portal certificate | The portal is probed before a password is sent. A self-signed certificate must be confirmed by the user against its fingerprint; it is then pinned, and a later change is refused with a warning. Plain HTTP to another machine is refused (`-allow-http` exists for test networks only) |
| Token | Kept only in the daemon and in `config.json` (mode 0600). The API never returns it, and the page cannot read it. The password is only forwarded to the portal; it is not written to disk or to the log |
| Hostile web pages | The `Host` header is checked (against DNS rebinding). `/api/v1/` accepts only requests with no `Origin` or the same origin; `/v1/` allows local origins only, as `sharetoken up` does. JSON endpoints require `Content-Type: application/json` |
| XSS in the page | The CSP forbids inline scripts and external resources; the DOM is built with `textContent` only |
| Switching accounts | A device key belongs to one account. Signing in as another account renames the old key to `device.key.retired-<time>` and generates a new one. Credits belong to the account and are not affected |

**Known and not solved:**

- The local API does not authenticate other processes on the same machine (the same is true of `sharetoken up`). On a multi-user Linux machine another user can call `/v1` and spend your credits, or call `/api/v1/logout`. Fixing this needs a local secret (for example a 0600 file plus a session cookie).
- The token is stored in clear text in `config.json` (0600; on Windows an ACL for the user, SYSTEM and Administrators), as with the CLI. It is not kept in the system keychain.

## Anonymous usage statistics

Statistics go to PostHog and are sent only by `sharetoken daemon`. `sharetoken up` and the server programs send nothing.

- **Built-in key.** The project key is a constant in [`internal/client/telemetry.go`](../internal/client/telemetry.go) (PostHog Cloud, US region); no configuration file is needed. A key of this kind can only write events, not read data. At build time, `TELEMETRY_KEY` replaces it, `TELEMETRY_OFF=1` builds a client that sends nothing and shows no switch, and `TELEMETRY_HOST` selects another instance such as a self-hosted PostHog (only https, or a loopback address). See [`scripts/client_ldflags.sh`](../scripts/client_ldflags.sh).
- **Only prod builds send.** The built-in key is active in prod builds only. `go test`, `go run`, a plain `go build` and dev builds send nothing and show no switch. `scripts/gui_stack.sh` uses a fake key and a fake collector, and `scripts/gui_shell_test.sh` sets `SHARETOKEN_TELEMETRY=0`.
- **User switch.** On by default, with a checkbox on the sign-in page and the main page (`GET`/`PUT /api/v1/telemetry`). The state is kept in `telemetry.json`. Turning it off empties the queue and deletes the install ID. `SHARETOKEN_TELEMETRY=0` or `DO_NOT_TRACK=1` forces it off and hides the switch. A damaged file counts as off.
- **Sending.** `POST <host>/batch/`, best effort: it never blocks the caller, a failed batch is dropped without a retry, and shutdown waits at most 2 seconds.

| Event | Extra fields | When |
|---|---|---|
| `sharetoken_daily_active`, `sharetoken_hourly_active` | `state`, `sharing` | At most once per UTC day or hour; checked every 10 minutes |
| `sharetoken_signed_in` | `method` (login, register) | Sign-in or sign-up succeeded |
| `sharetoken_sign_in_failed` | `method`, `reason` (an allow-listed error code, otherwise `other`) | Sign-in or sign-up failed |
| `sharetoken_signed_out` | `reason` (user, expired) | Sign-out, or the login expired |
| `sharetoken_network_joined` | `duration` (a bucket) | The network was joined |
| `sharetoken_network_join_failed` | none | At most once per sign-in |

Every event also carries `schema_version`, `os`, `arch` and `app_version`. The `distinct_id` is a random install ID (`install:<random>`), unrelated to the account or the device key. The fields are fixed by the struct `telemetryProps`, so an event cannot carry an email address, a device ID, the portal address, an error message, a prompt or model output. Do not add a free-text field.

The PostHog server still sees the IP address a request comes from. Events set `$geoip_disable` and turn person profiles off; to keep no IP at all, also enable "Discard client IP data" in the PostHog project settings.

## Building

```bash
scripts/build-gui.sh dev                 # the build type is required; builds for this machine
scripts/build-gui.sh prod macos arm64    # on a Mac, either architecture
scripts/build-gui.sh prod windows amd64  # from any system
scripts/build-gui-linux.sh prod arm64    # Linux package, from any system with Docker
```

| System | Output in `dist/` (prod names; dev builds insert `-dev`) |
|---|---|
| macOS | `ShareToken.app` (dev: `ShareToken-dev.app`) and `sharetoken-<version>-macos-<arch>.dmg` |
| Linux | `sharetoken-<version>-linux-<arch>/` and `sharetoken-<version>-linux-<arch>.deb` |
| Windows | `sharetoken-<version>-windows-<arch>/` and the installer `sharetoken-<version>-windows-<arch>.exe` |

`dist/SHA256SUMS-<version>` lists the installers.

- The macOS and Linux shells need cgo and are built on the target system (a Linux build cannot be cross-compiled from macOS; `scripts/build-gui-linux.sh` builds it in a Docker container instead). The Windows shell needs no cgo and cross-compiles from anywhere. It needs the WebView2 Runtime to run, and is not code-signed. Its installer is made with `makensis`, or in the Docker image of `scripts/build-gui-linux.sh` when that is not installed.
- Linux build dependencies (Debian/Ubuntu 24.04 names): `build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev`. WebKitGTK is needed at run time. The script adds the `webkit2_41` build tag when it finds WebKitGTK 4.1, and leaves it out on older systems that only have 4.0. The `.deb` installs the programs into `/usr/bin` and adds an entry to the application menu.
- The macOS app carries only an ad-hoc signature. It runs on the machine that built it; distributing it needs a Developer ID signature and notarization, otherwise Gatekeeper blocks it.
- Log: a daemon started by the window writes to `daemon.log` in the data directory (`~/.sharetoken` for prod, `~/.sharetoken-dev` for dev; under `%USERPROFILE%` on Windows).

## Command-line packages

A package without the window is built for all three systems, for servers and for users who do not want the desktop app. The program is the same `sharetoken` (or `sharetoken-dev`) as in the desktop package, under the same name.

```bash
scripts/build-cli.sh dev               # this machine's system
scripts/build-cli.sh prod linux amd64  # macos, linux or windows; amd64 or arm64. No cgo, so any system builds any target
scripts/build-cli.sh prod all          # all six packages
```

The output is `dist/sharetoken-cli-<version>-<os>-<arch>.tar.gz` (`.zip` for Windows; dev builds are named `sharetoken-cli-dev-…`), listed in `dist/SHA256SUMS-<version>`. It holds the program and a `README.txt`. The macOS program carries only the linker's ad-hoc signature and the Windows program is not code-signed, so a downloaded copy triggers the same system warnings as the desktop app.

A whole-session test (register, run, query, buy an answer over the network, stop, sign in, sign out) runs against the local network of `scripts/gui_stack.sh`. It needs a dev build, because a prod build ignores `SHARETOKEN_PORTAL`:

```bash
scripts/gui_stack.sh start DIR && . DIR/env
PORTAL=$PORTAL PIN=$PIN scripts/cli_test.sh <path to sharetoken-dev>
scripts/gui_stack.sh stop DIR
```

The page can also be used without the window: run `sharetoken daemon` and open `http://127.0.0.1:9787/` in a browser (dev: `sharetoken-dev daemon`, port 9887).

## Tests

See [development.md](development.md) for the general build and test setup. `scripts/test.sh` runs `go test` with the local MySQL server that the portal-backed tests need. The tests specific to the desktop client:

| Layer | Command | Covers |
|---|---|---|
| Daemon API | `scripts/test.sh -race ./internal/client` | Sign-up, sign-in and sign-out, success and failure paths; certificate confirmation and pinning; MFA; token revocation; disabled users; switching accounts; concurrent sign-ins; request guards; no secrets in the log |
| Account rules | `scripts/test.sh ./internal/accounts ./internal/portal` | Email validation and normalization; admin API URLs containing `@` and `+`. |
| End-to-end API | `scripts/test.sh -race -run Daemon ./test/e2e` | On a real libp2p topology: sign-up, joining through a relay, buying from a model, accounting, recovery after a restart, sign-out, joining once the network comes back |
| Usage statistics | `scripts/test.sh -race -run Telemetry ./internal/client` | With a fake collector: event format, no personal data, nothing sent without a key, the switch and the environment variables, daily and hourly de-duplication, no effect on the client when the collector is down or hangs |
| Window shell | `cd gui && go test -tags desktop,production .` (add `webkit2_41` on Linux) | Starting, connecting to and stopping the daemon; port conflicts; forwarding |
| Page | `scripts/gui_stack.sh start DIR && node scripts/gui_ui_test.mjs DIR` | Headless Chrome runs every flow of the page against real processes (relay, HTTPS portal, coordinator, seller, daemon). The Sharing view uses a `custom` provider pointed at a mock model server: add, pick a model, price, start, test, reprice, local models, turn off, remove. Account login is checked up to the sign-in address and a bad callback |
| Real WebView | `scripts/gui_shell_test.sh <dev\|prod> <program directory>` | Without showing a window, the system WebView loads the page and POSTs to a real daemon; checks that the daemon stops when the app quits |
| Menu bar and tray | `scripts/gui_tray_test.sh <dev\|prod> <program directory>` (macOS) | The window and the icon show for about 15 seconds. Drives the real icon, menu and window through the system: close, open, minimize and restore, rapid clicks, language following, a second start, the daemon stopping after Quit. On Windows the same self-test runs when the app is started with `SHARETOKEN_GUI_SELFTEST=tray` |
