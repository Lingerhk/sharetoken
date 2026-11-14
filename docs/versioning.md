# Versions and upgrades

**English** | [简体中文](zh-CN/versioning.md)

The release version says which code a program is. The interface versions decide whether two programs can talk to each other. The two are managed separately, and the rules below apply to every change.

## Release version

- **One version:** the whole repository has a single semver, in the `VERSION` file at the root. The client, the desktop shell, the relay, the coordinator, the portal and the mail server share one tag.
- **Format:** a build of tag `v1.2.3` with nothing changed is `1.2.3`. Any other build is `1.2.3-dev.<commit>[.dirty]` ([`scripts/version.sh`](../scripts/version.sh)). A `go build` without the version stamped in is `dev`.
- **Stamping:** [`scripts/ldflags.sh`](../scripts/ldflags.sh) writes the version, commit and build time into `sharetoken/internal/version`. The desktop shell is a separate module and takes `main.version`.
- **Where to see it:** `portal|coordinator|mailserver|relay|sharetoken version`, the first line of each program's log, the portal's `GET /api/v1/version`, and the Versions page of the admin console.

**Releases raise the number since 1.0.1** (2026-11-14). Before that, every release was 1.0.0 and moved the tag `v1.0.0`, so the 1.0.0 builds differ: the `commit` names them (`GET /api/v1/version`, the admin Versions page), and clients compare the commit and build time to notice a newer build of the same version (see "Update notices"). Protocol IDs and signing domains keep their 1.0.0 / v1 names until the first public release; formats still change in place before then.

To cut a release: change `VERSION`; add the release and its features at the top of the changelog page ([`internal/portal/web/changelog.html`](../internal/portal/web/changelog.html), served at `/changelog/`) with its translations; commit; `git tag v1.2.3`; then build.

## Interface versions

| Interface | Where the version is | Current |
|---|---|---|
| Portal HTTP API | path | `/api/v1` |
| Client local API | path | `/api/v1` (the OpenAI endpoint stays `/v1`) |
| Control plane (client ↔ coordinator) | libp2p protocol ID | `/sharetoken/ctrl/1.0.0` |
| Data plane (buyer ↔ node) | libp2p protocol ID, plus an `infer_version` agreed per job | `/sharetoken/infer/1.0.0`, version 1 |
| Relay pass | libp2p protocol ID, plus the signing domain | `/sharetoken/relay-pass/1.0.0`, `sharetoken/relay-pass/v1` (the signature covers the whole body, so a new field needs no new version) |
| Signed messages | signing domain | `sharetoken/offer/v1`, `sharetoken/ticket/v1`, `sharetoken/receipt/v1`, `sharetoken/ack/v1` |
| Database | `schema_migrations`, numbered per component | `core` 3, `mail` 1, `portal` 2 |
| Server configuration | `version` in the YAML file | 1 |
| Client configuration | `version` in `config.json` | 1 |

**How a client reports its version:**

- To the portal: every request carries `User-Agent: sharetoken-client/<version> (<env>; <os>/<arch>)` and `X-ST-Client-Version`. Every response carries `X-ST-Server-Version`.
- To the coordinator and to nodes: the frame that opens a request carries `ver` (`protocol.SendRequest`).
- Every libp2p host: the `UserAgent` in identify is `sharetoken-<program>/<version> (<os>/<arch>)`. The coordinator records relay versions from it.
- An Offer carries the signed fields `client_version` and `infer_versions`, and a Ticket the signed `infer_version`. The coordinator only matches a buyer and a seller that share an infer version.

## Version policy

The client version policy is stored in the shared database under `meta.version_policy`. The portal and the coordinator read the same record (the coordinator caches it for up to 30 seconds), so changing it needs no deployment. Change it on the admin console's Versions page, or with:

```bash
portal -config portal.yaml version-policy                      # show it
portal -config portal.yaml version-policy set -min 1.0.0 -recommended 1.0.0 -latest 1.0.0 \
  -download-url https://sharetoken.app/#download -allow-dev=false
```

| Operation | Client below the minimum, a development build while `allow_dev` is off, or no version given |
|---|---|
| `POST /api/v1/auth/verify`, `/mfa`, `/devices` | The portal answers 426 with the error code `upgrade_required` |
| `announce`, `ticket_request` | The coordinator answers `upgrade_required`; the local OpenAI endpoint answers 426 |
| `settle`, `started`, `abandon`, `dispute`, `evidence`, `relay-pass`, account queries | **Never blocked:** settlement and disputes involve money |
| Web sign-in (`/api/v1/web/*`), `/api/v1/auth/code` | Not affected |

The version is reported by the client itself, so this check is for compatibility and is not a security measure. A database with no policy serves every client.

A development build `1.2.3-dev.x` is compared as `1.2.3`. A network for development sets `allow_dev: true`; a production network sets it to `false`.

## Update notices

A client (the desktop app's daemon, and `sharetoken up`) asks the portal at start-up and every hour after: `GET /api/v1/update?kind=desktop|cli&channel=prod|dev&os=…&arch=…`. The portal returns:

- The version policy (`min`, `recommended`, `latest`, `download_url`). When `latest` is empty, the release in the downloads directory is used; when `download_url` is empty, it is `/#download`.
- `release`: the `RELEASE-<version>.json` in the downloads directory (written by [`scripts/release-files.sh`](../scripts/release-files.sh) when a release is published: version, commit, build time), plus the changelog link `/changelog/#v<version>`.
- `package`: the package of that release for the client's kind, build type, system and architecture, with its size and sha256. A dev client is offered `sharetoken-cli-dev-*`. Dev desktop installers are not published, so a dev desktop app gets none.

A client decides that a newer version exists when its version is below `min` (upgrade required), `recommended` or `latest`, or when the release is **a newer build of the same version**: the commit differs, the build time is later, and a package exists for this client. Before 1.0.1 every release was 1.0.0, so the second rule was the one that fired; it still catches a release rebuilt under the same number. All packages of one release share `ST_BUILD_TIME`, which `ldflags.sh` writes into `version.BuildTime`.

The notice appears in these places:

- One line at the top of the desktop app's page: the version, the summary of the release notes, "Details" and "Skip this version". "Details" opens a dialog with the release notes in full, a download link (the package for this system, or the download page if there is none) and "What's new". A required upgrade is shown in red, says that existing jobs still settle, and cannot be skipped. "Check for updates" in Settings asks the portal at once and opens this dialog when there is a new version, a skipped one included.
- In the tray menu: "Update available: x" or "Upgrade required: x"; clicking it opens the window.
- As a system notification, once per new build (through Wails' notification API; macOS asks for permission the first time). The daemon records skipped and notified builds in `update.json` in the data directory, so a restart does not repeat them.
- On the command line: `sharetoken up` writes one WARN line to its log; `sharetoken status` shows it; `sharetoken version --check` asks the portal directly and needs no running client.

Local API: `update` in `GET /api/v1/session`, and in `GET /api/v1/status` when there is a newer version; `POST /api/v1/update/check`, `POST /api/v1/update/skip` and `POST /api/v1/update/notified` (body `{"key"}`; a key that is not the build currently offered gets 409 `not_offered`).

### One-click update

A client can download and install a new build by itself, provided the package carries the **release key's** signature.

- **Signature:** the release key is an Ed25519 key whose private half is kept offline (by default `local/keys/release-signing.pem`, or `$ST_RELEASE_KEY`), or, for releases made on GitHub Actions, in a secret of the `release` environment (see "Publishing packages"); its public half is built into the clients (`ReleaseKey` in [`internal/client/upgrade.go`](../internal/client/upgrade.go), and the same in [`gui/update.go`](../gui/update.go); a test keeps them equal). It signs the file's SHA-256 (Ed25519 over the 32-byte digest), which is what Wails' updater checks with its `ed25519` algorithm. [`scripts/sign-packages.go`](../scripts/sign-packages.go) makes the key and the signatures; `scripts/release-files.sh` checks the key against the clients' and signs every package into `SIGNATURES-<version>` (the layout of `SHA256SUMS`: `<base64 signature>  <file>`).
- **Portal:** `GET /api/v1/update` also returns `install`, the file this client installs by itself, only when it is signed: for the macOS app `sharetoken-<version>-macos-<arch>.app.zip` (the .app zipped; not listed on the download page), the installer on Windows, the `.deb` on Linux, the archive for the command line. `package` and `install` carry `signature`.
- **Download:** the daemon's `POST /api/v1/update/download` (`{"key"}`) fetches `install` into `update/` in the state directory, hashing as it goes, and keeps the file only if checksum and signature match; `session.update.download` reports it (`downloading`, `ready`, `failed`).
- **Install, desktop app:** "Update now" appears only in the desktop window: its shell marks the page with `data-shell-update="1"`, and the page calls the shell's `/__shell/update` (POST `{key}` to start, GET to follow). The shell has the daemon fetch the file, checks the signature itself, stops the daemon (jobs in flight finish), then:
  - macOS: Wails' updater (`pkg/updater`) checks digest and signature again, unzips, swaps the whole .app once the app has quit, verifies its code signature and opens it again; on failure it restores the old bundle. The app must be in a directory the user can write, not run from the disk image or from App Translocation.
  - Windows: the installer runs silently (`/S /RELAUNCH`) through `cmd /c start`, so that it is not a child of the app (it ends the app with `taskkill /T`) and leaves the app's job; `/RELAUNCH` makes it open the app again.
  - Linux: `pkexec dpkg -i` (the desktop asks for an administrator password; cancelling installs nothing); a small script starts the new window once this one has left. Only a copy installed from the `.deb` qualifies.
- **Install, command line:** `sharetoken upgrade [-y]` picks the package by how this copy was installed (`client.DetectInstallation`): the archive's program file is replaced (on Windows the running program is renamed `.old` and deleted at the next start); inside the macOS app the bundle is swapped (the app must not be running); a Windows desktop install runs the installer silently; a `.deb` goes through `sudo dpkg -i` (`pkexec` outside a terminal). `dpkg -i` and not `apt-get install`, so that a newer build of the same version installs.

**Not yet:** automatic installs and staged rollouts; downgrade protection (the signature covers the file, not its version: whoever can alter the portal's answer can offer an older signed package); a release key held in hardware (it is a file, or a GitHub secret for releases made there: see "Publishing packages").

### Release notes

A release can carry a text of its own that tells users what this build brings. The sources are `release-notes/<language>.md` in the repository (`en.md`, `zh-CN.md`: the client's two languages), written for the release that wants them; the directory is not kept between releases, and a release without it has no notes (clients then show only the version). Publishing a release puts them beside the packages as `RELEASE-<version>.<language>.md`; the portal puts what it reads into `release.notes` (by language, each with a `summary` and a `body`), and the client hands it to the page unchanged. The files in the server's downloads directory can also be edited in place, without publishing again.

The text is in a very small markup that clients draw themselves; it is never parsed as HTML:

| Written | Shown as |
|---|---|
| `# text` | A title |
| `## text` | A subtitle |
| `- text` | A list item |
| Any other line | A paragraph |
| `**text**`, `` `text` `` inside a line | Bold, code |

The **summary** is the first paragraph line (the first line that is neither a heading nor a list item). It is shown where there is room for one line: the update line at the top of the page, the system notification, `sharetoken status`. Keep it short: `scripts/release-files.sh` refuses more than 30 characters in Chinese, Japanese and Korean and more than 90 in other languages. The portal keeps 120 characters at most. A file may be 4096 bytes at most; a larger one leaves that language without notes, and the release is still offered. The page takes the notes in its own language, or else in English.

## Upgrade rules

Every change follows these rules:

1. **Compatible changes:** adding an optional field, a new frame type or a new endpoint leaves the interface version as it is. JSON decoding always ignores unknown fields (the portal's `readJSON`, `protocol.Envelope`).
2. **Breaking changes take three steps:** receivers first accept both the old and the new version; senders then switch to the new one; the old one is removed once the admin console shows that nobody uses it.
3. **Stream protocols:** the old and the new handler exist side by side, and the client negotiates with `NewStream(new, old)`. A new per-job protocol version is added to `SupportedInferVersions`.
4. **Signed messages:** a program that relays a message drops the fields it does not know, so **a new signed field needs a new signing domain** (`sharetoken/offer/v2`), and verifiers first accept both domains. [`internal/protocol/golden_test.go`](../internal/protocol/golden_test.go) pins the current encodings; when it fails, the signed content has changed. Verification of old receipt versions is kept for good, because a dispute may need it.
5. **Database:** add a numbered migration and never edit one that has shipped ([`internal/mysqldb/migrate.go`](../internal/mysqldb/migrate.go)). One release only adds; removals wait for the next release, so a rollback does not have to touch data. When older programs really cannot work with a change, set `MinReader`, and they refuse to start.
6. **Configuration:** a new setting has a default. An incompatible change raises `version`, and programs first accept both versions.
7. **Default upgrade order:** database (the portal or the coordinator migrates it at start-up) → coordinator and portal → relays → clients. When the relay pass format changes, relays are upgraded before the portal.
8. **Retiring old clients:** look at the client distribution in the admin console, and raise `min_client` only after that.

## Build outputs

```
dist/sharetoken-1.0.0-macos-arm64.dmg             scripts/build-gui.sh prod macos arm64
dist/sharetoken-1.0.0-windows-amd64.exe           scripts/build-gui.sh prod windows amd64
dist/sharetoken-1.0.0-linux-arm64.deb             scripts/build-gui-linux.sh prod arm64 (in Docker, from any system)
dist/sharetoken-cli-1.0.0-linux-amd64.tar.gz      scripts/build-cli.sh prod linux amd64 ("all" builds three systems, two architectures)
dist/sharetoken-cli-1.0.0-windows-amd64.zip       scripts/build-cli.sh prod windows amd64
dist/relay-1.0.0-linux-amd64                      GOOS=linux GOARCH=amd64 scripts/build.sh relay
dist/sharetoken-1.0.0-macos-arm64.app.zip         the same: the .app zipped, for one-click updates
dist/SHA256SUMS-1.0.0
dist/SIGNATURES-1.0.0                             scripts/release-files.sh, with the release key
```

**Publishing packages.** A release is 14 packages (desktop installers, the two macOS .app.zip files and command-line archives, for three systems and two architectures), signed with the release key and put into the portal's downloads directory (`downloads.dir` in the portal's configuration), which is replaced as a whole; the portal serves them under `/downloads/`. There are two ways to make one, and both run the same scripts:

- **From a Mac:** `scripts/publish-packages.sh <version> <user@host> <directory> [--with-dev-cli] [--owner <user>]` builds the packages, signs them, uploads them, verifies them on the server and swaps the directory. `--owner` installs the files through sudo as the user the portal runs as. `--with-dev-cli` adds six `sharetoken-cli-dev-*` packages for testing: `GET /api/v1/downloads` does not list them, and they can only be fetched by file name.
- **From GitHub Actions:** the workflow "Client release" ([`.github/workflows/release-client.yml`](../.github/workflows/release-client.yml)). Pushing the tag `v<VERSION>`, or moving it (`git tag -f -a v1.0.1 -m ... && git push -f origin v1.0.1`), builds the packages on GitHub's runners: the macOS app for both architectures on an Apple silicon runner, the Windows app cross-compiled and packed with makensis on Linux, the `.deb` in the Docker image of `build-gui-linux.sh` on a Linux runner of each architecture, and the command line on Linux. Every package gets the Go of `scripts/gui-builder.Dockerfile` and the same build time. The job "Sign and publish on GitHub" then signs them and creates the GitHub release with every file (for a moved tag it replaces the files of the existing release), and "Publish to the portal" swaps the downloads directory. "Run workflow" on a release tag does the same and asks whether to publish to the portal; on a branch it is a trial: everything is built and checked, nothing is signed or published, and the packages stay with the run for a week. A tag that does not match `VERSION` fails the run.

The steps are scripts of their own: the build scripts; [`scripts/release-files.sh`](../scripts/release-files.sh) `<version> [--with-dev-cli] [--unsigned]`, which checks that every package is in `dist/`, checks the release notes, and writes `SHA256SUMS-<version>`, `SIGNATURES-<version>`, `RELEASE-<version>.json` and `RELEASE-<version>.<language>.md`; and [`scripts/upload-release.sh`](../scripts/upload-release.sh) `<version> <user@host> <directory> [--owner <user>]`, which sends nothing unless every checksum matches and every signature verifies against the key built into the clients.

The workflow needs these settings on GitHub (Settings, Environments; and Settings, Secrets and variables, Actions):

| Where | Name | Value |
|---|---|---|
| Environment `release` | secret `RELEASE_SIGNING_KEY` | the release key's PEM file (`local/keys/release-signing.pem`) |
| Environment `portal` | secret `PORTAL_SSH_KEY` | a private SSH key of the account that installs the files on the portal's server |
| | variable `PORTAL_SSH_TARGET` | `user@host` |
| | variable `PORTAL_DOWNLOADS_DIR` | the portal's `downloads.dir` |
| | variable `PORTAL_SSH_KNOWN_HOSTS` | the server's `known_hosts` line (for example from `ssh-keyscan -t ed25519 <host>`, compared by hand with the server's key); the host key is checked strictly |
| | variable `PORTAL_OWNER` (optional) | the portal's service user, as `--owner` |
| Repository | variable `RELEASE_PUBLISH_PORTAL` | `true`: a pushed tag also publishes to the portal; otherwise only "Run workflow" does |
| | variable `RELEASE_WITH_DEV_CLI` | `true`: every run adds the dev command-line archives |

**The release key on GitHub.** Kept as that secret, the release key is no longer only offline: whoever gets the `release` job to run can sign a package that every client installs by itself. Give the environments `release` and `portal` required reviewers and let them deploy from tags `v*` only, and add a tag ruleset so that only maintainers can create or move `v*` tags. The job writes the key to a temporary file only while signing. Without the secret a release run stops before signing, and releases can still be made from a Mac as before.

Program names carry no version (`sharetoken`, `sharetoken-gui`; dev builds are `sharetoken-dev`, `sharetoken-gui-dev`): the window finds the daemon by a fixed name, and a user's PATH keeps working across upgrades. Server programs built with `scripts/build.sh` do carry the version in their file name.

See [desktop.md](desktop.md) for the client packages, [development.md](development.md) for building and testing, and [self-hosting.md](self-hosting.md) for running the server programs.

## Tests

- `scripts/test.sh`: starts MySQL 8 in Docker and runs all Go tests.
- [`internal/protocol/golden_test.go`](../internal/protocol/golden_test.go): fixed vectors for the signed messages.
- [`internal/mysqldb`](../internal/mysqldb): migration order, resuming after an interruption, concurrent start-up, older programs meeting a breaking migration.
- [`scripts/version_ui_test.mjs`](../scripts/version_ui_test.mjs): the version pages and the update notice, in headless Chrome.
- [`scripts/gui_update_test.sh`](../scripts/gui_update_test.sh): the macOS one-click update end to end. On a private network it builds an old copy A and a new copy B, publishes B signed, lets A install it (`SHARETOKEN_GUI_SELFTEST=update`, window hidden) and checks that B is in place and starts; a package signed with another key is refused. Windows and Linux follow the same steps by hand on the test VMs (install A, publish B, run `sharetoken-gui-dev` with the same variables; on the Linux VM a temporary polkit rule lets `pkexec` run without a password).
- **Not implemented:** an end-to-end test against the previous release (building the previous tag's client and trading through the current coordinator and portal).
