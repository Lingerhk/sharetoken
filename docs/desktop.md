# Desktop client (macOS, Linux, Windows)

**English** | [简体中文](zh-CN/desktop.md)

The desktop client covers signing in by emailed code (which signs up a new address), signing out, viewing the account and the network, and setting up seller mode (the Sharing view, below).

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
| `SHARETOKEN_PORTAL` | honoured (for test networks) | ignored |

- `sharetoken version` (`sharetoken-dev version`) prints the build type, the portal and the data directory.
- The window only connects to a daemon of its own build type. A dev window and its page title say DEV.
- `SHARETOKEN_HOME` overrides the data directory.
- `config.json` records its format version and the build type that wrote it. A file written by a newer client is refused. A file in an older, unsupported format is deleted together with `device.key` and `network.json`, and the user signs in again. A file of the other build type is refused and left alone, so pointing `SHARETOKEN_HOME` at the wrong directory cannot delete another environment's identity.

## Architecture

```
sharetoken-gui (Wails v3 window, gui/)      sharetoken daemon (internal/client/daemon.go)

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
- The proxy adds one script tag to the page's head, for `/__shell/boot.js`, which the shell serves itself (`shellBootJS` in `gui/main.go`). It marks the system (`os-<system>` on `<html>`), loads Wails' page runtime (`/wails/runtime.js`, answered by Wails' asset server: window dragging on macOS, and Wails runs script in the page only once it is there), and reports the page's language to the shell. Both are of the page's own origin, so the page's CSP lets them run. Requests under `/__shell/` never reach the daemon.

## Window layout

The window is small (880×600 by default, 760×520 at least), and the page is laid out as an application, not as a web page ([`internal/client/ui/app.css`](../internal/client/ui/app.css)):

- **The window itself never scrolls.** An icon sidebar on the left (Overview, Sharing, Pairing, Activity; Feedback and Settings at the bottom), the current view in the middle, a status bar at the bottom. Long lists scroll inside their own panel; when a short window cannot hold a view, the view scrolls, not the window.
- **Views**: Overview (the balance and usage tiles, the local endpoint, the network and the available models side by side), Sharing (tabs: Status, Provider APIs, Local models), Pairing (tabs: My sellers, My buyers), Activity (tabs: All, Sold), Settings (the account and Log out, the language, the version and Check for updates, feedback, the devices). Every view stays in the page: polling keeps the hidden ones current, and switching shows them at once.
- **Shown when asked for**: what a panel is for is behind the "?" beside its title; feedback is a dialog; an update is one line, with its details in a dialog.
- **Status bar**: the connection, the relay in use and its latency, the account, the version.
- **Latency colours** are the portal's: green under 80 ms, amber under 250 ms, red otherwise (`rtt` in `app.js`). The relay table, the link quality, the relay reservations and the status bar use them.
- Text is not selectable by default; only what is worth copying is (addresses, code, tables, notices).
- **macOS** hides the title bar (`application.MacTitleBarHiddenInset`), so the page reaches the top of the window. The shell's boot script adds the class `os-<system>` to `<html>`; under `os-darwin` the sidebar leaves room for the window's buttons, and the sidebar and the view's title bar drag the window (`--wails-draggable`, through Wails' page runtime), their buttons excepted. Windows and Linux keep the system's title bar.

## Sharing view (seller mode)

Sharing in the sidebar opens the Sharing view (`shareView` in [`internal/client/ui/app.js`](../internal/client/ui/app.js)); the current view is kept in `localStorage.st_view`. The Sharing view only calls the local API, and edits the same `config.json` and `providers.json` as the command line, so changes made on either side show on the other. See [providers.md](providers.md) for the implementation.

| Area | What it does | API |
|---|---|---|
| Sharing state | Start and stop sharing. Tiles: models on sale, jobs done and failed, in progress / concurrency, earnings of this run and time of the last offer, total earned by the account. Warnings from the daemon | `POST /api/v1/share/start`, `/stop`, `GET /api/v1/status`, `/api/v1/account` |
| Relay reservations | While sharing runs: for each relay, whether a reservation is held, its latency and expiry | `share.reservations` in `GET /api/v1/status` |
| Recent jobs sold | Model, state, tokens and earnings of the last 10 jobs sold | `GET /api/v1/jobs?role=sell&limit=10` |
| Provider APIs | Add a provider (API key or account login), turn it on or off, replace its credentials, remove it | `/api/v1/providers`, `/api/v1/providers/oauth/start`, `/finish` |
| Provider models | Pick from the provider's model list (or type a name), set the network ID, input and output price and output limit; test, edit, remove | `/api/v1/providers/{name}/models`, `/test` |
| Local model server | Address and concurrency; detect its models, then add, edit, remove | `PUT /api/v1/share`, `GET /api/v1/share/detect` |
| Pairing | Private mode (serve only paired buyers). Create a pairing code (note; lifetime of 1, 7 or 30 days; number of buyers), copy it, revoke it; the buyers paired with this account, and removing one | `GET /api/v1/pairing`, `POST /api/v1/pairing/codes`, `DELETE /api/v1/pairing/codes/{id}`, `DELETE /api/v1/pairing/buyers/{id}`, `PUT /api/v1/pairing/private` |

- The data comes from `GET /api/v1/status`, polled every 5 seconds (`share.providers`, `share.serving`, `warnings`). A list is only redrawn when its content changed. Forms live in dialogs that polling does not touch, so input is never lost.
- These endpoints belong to the Agent, so until the client has joined the network the view only says that sharing can be set up once connected.
- Prices are entered in credits per million tokens and stored in micro-credits. The defaults are 0.1 and 0.2, the same as `share add`. When adding a model that is already on the network, the form shows its lowest price.
- An account login takes two steps: the page shows the sign-in address (copy it or click to open it), and the user pastes back the authorization code or the callback address.
- Guards in the page: sharing cannot start without a model; one model ID cannot come from two sources; a new provider whose name is already taken must be renamed (the API would treat it as a credential replacement); when the last sellable model is removed or its provider is turned off, sharing turns off with it.
- One difference from the command line: `share add` also turns sharing on; the page leaves that to the Start button.
- The model table's "lowest price on the network" comes from `GET /api/v1/models` (which includes this machine's own offers). A provider card shows when its login token expires and the names of its extra request headers. The Overview has a Sharing tile that opens the Sharing view, and its network section shows link quality to the coordinator (average latency, jitter, loss).
- Pairing (`pairingView` in `app.js`) has a view of its own, with two tabs. My sellers: enter a seller's code, see the sellers this account is paired with, leave one (`POST /api/v1/pairing/redeem`, `DELETE /api/v1/pairing/sellers/{id}`); while the account is paired it says that it buys only from those sellers, and Available models lists only theirs. My buyers: private mode, the pairing codes and the paired buyers. Pairings live at the portal, so the view works before the network is joined; it is read again every 15 seconds.
- The client is in English and Simplified Chinese. Without a saved choice it follows the system language; any other system language gets English (see [i18n.md](i18n.md)).
- A key passes through the page only when it is submitted; the API returns it masked. The UI test checks that no key appears in the page or in `/api/v1/status`.
- Daemon warnings and errors returned by a provider are shown as they are, in English.

## Menu bar and tray

The app shows an icon in the menu bar on macOS, in the notification area on Windows, and as a status notifier item on Linux (KDE, Ubuntu's GNOME with its AppIndicator extension, and other desktops that implement it). A Linux desktop without one, such as a plain GNOME, gets no icon: there, closing the window quits the app and stops the daemon, because nothing could open the window again.

| Action | Result |
|---|---|
| Close the window (red button, X, Cmd+W on macOS) | The window is hidden. The app and the daemon keep running; sharing and the local API are not interrupted |
| Left-click the icon on Windows | Opens the window (restores it if minimized) |
| Click the icon on macOS and Linux, right-click on Windows | Menu: a status line, information lines (the relay in use and its latency, the models on sale while sharing, the available credit; none clickable), an update line when a new version exists (opens the window), Open, Quit |
| Quit in the menu, Cmd+Q on macOS | Quits the app; a daemon that this app started stops with it |
| Start the program again | One instance per data directory: the second process brings the window back and exits. dev and prod, and different data directories, do not affect each other |

- The status line is refreshed every 3 seconds from `GET /api/v1/session`, for example `Connected · a@example.com`, `Not signed in`, `The background service is not running`.
- The information lines show only while connected, and are also updated every 3 seconds, from `GET /api/v1/status`: `Relay: relay-sg · 42 ms` (the latency is the mean of the live pings of the relay in use, or else its last measurement) and `Sharing · models on sale: 2`. `Available credit: 4.99` is fetched once a minute from `GET /api/v1/account`, because that asks the portal. The tooltip of the icon carries the relay and its latency too.
- The system notification about a new release carries the one-line summary of its release notes (see "Release notes" in [versioning.md](versioning.md)).
- New versions: one system notification per new build ([`gui/notify.go`](../gui/notify.go); clicking it opens the window). The rules are in [versioning.md](versioning.md), under "Update notices". On Windows, Wails registers a notification activator under HKCU, under the app's name, each time the app starts; the uninstaller removes it.
- The menu text follows the page's language: the page reports its language to the shell when it loads and whenever it changes (`/__shell/lang`, which never reaches the daemon). The text is in [`gui/tray_text.go`](../gui/tray_text.go); a test checks it against the page's language list and the page's wording for the shared entries.
- Windows 11 puts new icons in the `^` overflow area by default; the user can drag the icon onto the taskbar.
- The icons are generated with `go run packaging/gen_tray_icons.go` (run in `gui/`). The macOS icon is a template image coloured by the system; the Windows icon is a colour ICO; Linux uses `packaging/tray.png`, the Windows drawing at 64×64.
- Implementation: Wails v3's system tray on all three systems ([`gui/tray_native.go`](../gui/tray_native.go); icons and per-system details in `tray_platform_<system>.go`). Menu labels are escaped where the system reads a character as a mnemonic mark (`&` on Windows, `_` on Linux).
- The single-instance lock is Wails'. Its ID is `app.sharetoken.desktop.<build type>.h<hash of the data directory>`; on Linux it is a D-Bus name, whose elements may not start with a digit.
- Wails v3 handles a refused WebView2 `MoveFocus` itself (v2 ended the process); the tray self-test's rapid minimise and Open steps cover it. Rerun the tray self-test after upgrading Wails.
- `App.Show` (used on macOS to bring back an app hidden with Command-H) is called on the main thread with `application.InvokeSync`: unlike the window's methods it does not move there itself, and AppKit ends the process otherwise.
- `app.Run()` does not return on macOS: the app terminates the process. Stopping the daemon and the self-tests' reports therefore run from Wails' `OnShutdown` and `PostShutdown` hooks.

## Local API of the daemon

Errors have the form `{"error":{"code","message"}}`; the page chooses its text by `code`.

| Endpoint | Description |
|---|---|
| `GET /api/v1/session` | `{env, state, logged_in, portal, user, device, peer, api, error, version, api_version, update}`. `state` is `logged_out`, `expired` (the portal rejected the token and the daemon signed out), `starting`, `retrying` (`error` says why) or `running`. When signed out, `portal` is the built-in address (below) and `user` is the last one used, to prefill the form |
| `POST /api/v1/portal/probe` | `{}` (`portal` is optional and defaults to the built-in address) → `{portal, https, trusted, fingerprint, subject, not_after, known, pin_changed, pin_required, registration_open, invite_required, signup_credit_micros, support_email}`. Called before anything is sent to the portal |
| `POST /api/v1/auth/code` | `{pin, email, invite_code}`: the portal mails a sign-in code. The answer says whether the address is new; `invite_required` asks for an invite code first |
| `POST /api/v1/auth/verify` | `{pin, email, code, invite_code, device}` → the session. An account with two-factor authentication gets 401 `mfa_required`: the daemon keeps the portal's challenge and the page asks for the authenticator code |
| `POST /api/v1/auth/mfa` | `{code}`: finishes a sign-in waiting for two-factor authentication → the session. 401 `challenge_expired` after 5 minutes or 5 wrong codes: start again with a new emailed code |
| `POST /api/v1/logout` | Stops the Agent, revokes the token (skipped if the portal is unreachable) and removes the token from `config.json`; other settings stay |
| `GET /api/v1/account`, `/api/v1/jobs`, `/api/v1/ledger` | Passed through to the portal; they do not need the Agent. If the portal answers 401, the session becomes `expired` |
| `POST /api/v1/update/check`, `/skip`, `/notified` | Update notices, see [versioning.md](versioning.md) |
| `POST /api/v1/feedback`, `GET /api/v1/feedback/mine` | Send feedback to the portal, and list what this account sent |
| `/api/v1/pairing…` | Pairing codes and pairings, passed on to the portal ([guide.md](guide.md), section 3) |
| Other `/api/*` and `/v1/*` | Same as `sharetoken up` (see [guide.md](guide.md)). 401 `not_logged_in` when signed out, 503 `not_connected` before the network is joined |

**Login expiry.** The Agent asks the portal once a minute whether its token is still valid (an unreachable portal does not count as a rejection). A device key can still trade with the coordinator after its token is revoked, so the client must stop: the daemon goes to `expired`, and `sharetoken up` exits and asks the user to sign in again.

**The portal address is built in.** It is the build type's portal (`client.BuiltInPortal`). The sign-in page does not ask for an address; it connects when opened, and shows the reason and a Retry button if it cannot. A request that omits `portal` (or leaves it empty) uses the built-in address. A dev build can be pointed at a test network with the environment variable `SHARETOKEN_PORTAL=https://…` (read by both the daemon and the CLI; the CLI has no `-portal` flag). A prod build ignores it, so the environment cannot send sign-in codes elsewhere. An account that is signed in keeps using the portal saved at sign-in. The `TestMain` of the package's Go tests points the variable at the unreachable `127.0.0.1:1`, so tests never touch a real portal.

Common error codes: `bad_portal`, `insecure_portal`, `portal_unreachable`, `not_a_portal`, `pin_required`, `pin_mismatch`, `bad_request`, `bad_email`, `registration_closed`, `email_not_allowed`, `invite_required`, `bad_invite`, `account_disabled`, `mail_unavailable`, `code_cooldown`, `throttled`, `bad_code`, `mfa_required`, `challenge_expired`, `mfa_setup_required`, `already_logged_in`, `bind_failed`, `bad_origin`, `bad_host`.

## The account is an email address

- There are no passwords. The sign-in page has one form for new and existing accounts: the email address, "Send code", the six-digit code (and an invite code when the portal asks for one). The first sign-in of an address creates its account; see [mail.md](mail.md) for the codes.
- The address is normalized (trimmed, lower-cased) and stored as the account name. The maximum length is 191 characters.
- An account named without an address (older ones) cannot sign in until an operator renames it (`portal rename <user> <email>`).
- With two-factor authentication on (the user turns it on on the portal's account page; administrators always have it), the page asks for the authenticator code after the emailed one.
- A client's sign-in lasts 60 days (`sessions.login_ttl` of the portal).

## Security

| Concern | What the client does |
|---|---|
| Portal certificate | The portal is probed before anything is sent to it. A self-signed certificate must be confirmed by the user against its fingerprint; it is then pinned, and a later change is refused with a warning. Plain HTTP to another machine is refused (`-allow-http` exists for test networks only) |
| Token | Kept only in the daemon and in `config.json` (mode 0600). The API never returns it, and the page cannot read it. Sign-in codes are only forwarded to the portal; they are not written to disk or to the log |
| Hostile web pages | The `Host` header is checked (against DNS rebinding). `/api/v1/` accepts only requests with no `Origin` or the same origin; `/v1/` allows local origins only, as `sharetoken up` does. JSON endpoints require `Content-Type: application/json` |
| Other programs and users on the machine | Loopback is open to all of them. The daemon makes a key at each start and writes it to `run.json` (mode 0600, in the 0700 state directory); everything but the page's files, the OpenAI endpoint `/v1/` and `GET /api/v1/hello` wants it as `Authorization: Bearer <key>`, else 401 `local_auth`. The window's proxy, the tray and the CLI read it from there (`internal/client/localauth.go`) |
| A program holding the daemon's port | The window uses a daemon only after it proves it holds the key in `run.json`: `GET /api/v1/hello?nonce=…` answers with an HMAC-SHA256 under the key of its address and the nonce. The key itself never goes to an address that has not proved it, so the page, sign-in codes and provider keys go only to this user's daemon. With no `run.json`, whatever answers at the address is passed over for a spare port |
| XSS in the page | The CSP forbids inline scripts and external resources; the DOM is built with `textContent` only |
| Switching accounts | A device key belongs to one account. Signing in as another account renames the old key to `device.key.retired-<time>` and generates a new one. Credits belong to the account and are not affected |

**Known and not solved:**

- With the API key turned off (Settings, or `sharetoken apikey off`), another program or user of the machine can spend credits through `/v1/`. It is on by default.
- The token is stored in clear text in `config.json` (0600; on Windows an ACL for the user, SYSTEM and Administrators), as with the CLI. It is not kept in the system keychain.

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

- The macOS and Linux shells need cgo and are built on the target system (a Linux build cannot be cross-compiled from macOS; `scripts/build-gui-linux.sh` builds it in a Docker container instead). The Windows shell needs no cgo and cross-compiles from anywhere. It needs the WebView2 Runtime to run, and is not code-signed. Its icon and manifest come from `gui/packaging/winres.json` (go-winres): Wails takes the window's icon from icon group 32512 and the notifications' from 3. Its installer is made with `makensis`, or in the Docker image of `scripts/build-gui-linux.sh` when that is not installed.
- Linux build dependencies (Debian/Ubuntu 24.04 names): `build-essential pkg-config libgtk-4-dev libwebkitgtk-6.0-dev`. Wails v3 uses GTK 4 and WebKitGTK 6.0 and needs GTK 4.10 or later, so the Linux app runs on Ubuntu 24.04 and later and Debian 13 and later, not on Ubuntu 22.04 (GTK 4.6) or Debian 12 (4.8). The Docker builder is Ubuntu 24.04 (`scripts/gui-builder.Dockerfile`). The `.deb` (`Depends: libgtk-4-1 (>= 4.10), libwebkitgtk-6.0-4`) installs the programs into `/usr/bin` and adds an entry to the application menu.
- **AppArmor:** WebKitGTK 6.0 always sandboxes its web processes with bubblewrap, which needs an unprivileged user namespace. Ubuntu 23.10 and later allow one only to programs whose AppArmor profile says so; without one the window aborts at start (`bwrap: setting up uid map: Permission denied`). The `.deb` installs `/etc/apparmor.d/sharetoken-gui` (`gui/packaging/apparmor-profile`: unconfined, `userns`, like Ubuntu's profile for its own browser) and loads it in `postinst`; `postrm` unloads it. A shell built and run outside the package needs a profile for its own path, or `WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS=1` for a test.
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

A whole-session test (sign in by code, run, query, buy an answer over the network, stop, sign in, sign out) runs against the local network of `scripts/gui_stack.sh`. It needs a dev build, because a prod build ignores `SHARETOKEN_PORTAL`:

```bash
scripts/gui_stack.sh start DIR && . DIR/env
PORTAL=$PORTAL PIN=$PIN scripts/cli_test.sh <path to sharetoken-dev>
scripts/gui_stack.sh stop DIR
```

The page can also be used without the window: run `sharetoken daemon` and open the address it prints, `http://127.0.0.1:9787/#key=…`, in a browser (dev: `sharetoken-dev daemon`, port 9887). The page keeps the key for the tab (`sessionStorage`) and takes it out of the address; the key changes at each start of the daemon.

## Tests

See [development.md](development.md) for the general build and test setup. `scripts/test.sh` runs `go test` with the local MySQL server that the portal-backed tests need. The tests specific to the desktop client:

| Layer | Command | Covers |
|---|---|---|
| Daemon API | `scripts/test.sh -race ./internal/client` | Sign-in by code (new and existing accounts) and sign-out, success and failure paths; certificate confirmation and pinning; MFA; token revocation; disabled users; switching accounts; concurrent sign-ins; request guards; no secrets in the log |
| Account rules | `scripts/test.sh ./internal/accounts ./internal/portal` | Email validation and normalization; admin API URLs containing `@` and `+`. |
| End-to-end API | `scripts/test.sh -race -run Daemon ./test/e2e` | On a real libp2p topology: sign-in, joining through a relay, buying from a model, accounting, recovery after a restart, sign-out, joining once the network comes back |
| Window shell | `cd gui && go test .` | Starting, connecting to and stopping the daemon; port conflicts; forwarding |
| Page | `scripts/gui_stack.sh start DIR && node scripts/gui_ui_test.mjs DIR` | Headless Chrome runs every flow of the page against real processes (relay, HTTPS portal, coordinator, seller, daemon). The Sharing view uses a `custom` provider pointed at a mock model server: add, pick a model, price, start, test, reprice, local models, turn off, remove. Account login is checked up to the sign-in address and a bad callback. Pairing: wrong and own codes, pairing with a seller that sells nothing (no models, requests refused) and with one that sells, creating and revoking a code, a buyer joining and being removed, private mode from the page and its effect on a buyer that is not paired |
| Real WebView | `scripts/gui_shell_test.sh <dev\|prod> <program directory>` | Without showing a window, the system WebView loads the page and POSTs to a real daemon; checks that the daemon stops when the app quits |
| Menu bar and tray | `scripts/gui_tray_test.sh <dev\|prod> <program directory>` (macOS, Linux) | The window and the icon show for about 15 seconds. Checks with the system that the icon is shown, then drives the menu items' actions and the real window: close, Open, minimise and restore, rapid minimise and Open, language following, a click on the icon (Windows, through the shell's message), Command-W (macOS), a second start, the daemon stopping after Quit. Wayland does not tell an app that its window is minimised, so on a Wayland session the minimise steps are skipped; `GDK_BACKEND=x11` runs them. On Windows the same self-test runs when the app is started with `SHARETOKEN_GUI_SELFTEST=tray` |
