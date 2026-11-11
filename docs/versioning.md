# Versions and upgrades

**English** | [简体中文](zh-CN/versioning.md)

The release version says which code a program is. The interface versions decide whether two programs can talk to each other. The two are managed separately, and the rules below apply to every change.

## Release version

- **One version:** the whole repository has a single semver, in the `VERSION` file at the root. The client, the desktop shell, the relay, the coordinator, the portal and the mail server share one tag.
- **Format:** a build of tag `v1.2.3` with nothing changed is `1.2.3`. Any other build is `1.2.3-dev.<commit>[.dirty]` ([`scripts/version.sh`](../scripts/version.sh)). A `go build` without the version stamped in is `dev`.
- **Stamping:** [`scripts/ldflags.sh`](../scripts/ldflags.sh) writes the version, commit and build time into `sharetoken/internal/version`. The desktop shell is a separate module and takes `main.version`.
- **Where to see it:** `portal|coordinator|mailserver|relay|sharetoken version`, the first line of each program's log, the portal's `GET /api/v1/version`, and the Versions page of the admin console.

**Before the first public release, the version stays 1.0.0.** A release does not raise the number; it moves the tag `v1.0.0` to the released commit (`git tag -f -a v1.0.0 -m ...`). During this period "1.0.0" does not name one fixed tree. The `commit` does (`GET /api/v1/version`, the admin Versions page), and clients compare the commit and build time to notice a newer build (see "Update notices").

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
| `POST /api/v1/register`, `/login`, `/mfa`, `/devices` | The portal answers 426 with the error code `upgrade_required` |
| `announce`, `ticket_request` | The coordinator answers `upgrade_required`; the local OpenAI endpoint answers 426 |
| `settle`, `started`, `abandon`, `dispute`, `evidence`, `relay-pass`, account queries | **Never blocked:** settlement and disputes involve money |
| Web sign-in and sign-up (`/api/v1/web/*`), `/api/v1/register/code` | Not affected |

The version is reported by the client itself, so this check is for compatibility and is not a security measure. A database with no policy serves every client.

A development build `1.2.3-dev.x` is compared as `1.2.3`. A network for development sets `allow_dev: true`; a production network sets it to `false`.

## Update notices

A client (the desktop app's daemon, and `sharetoken up`) asks the portal at start-up and every hour after: `GET /api/v1/update?kind=desktop|cli&channel=prod|dev&os=…&arch=…`. The portal returns:

- The version policy (`min`, `recommended`, `latest`, `download_url`). When `latest` is empty, the release in the downloads directory is used; when `download_url` is empty, it is `/#download`.
- `release`: the `RELEASE-<version>.json` in the downloads directory (written by [`scripts/publish-packages.sh`](../scripts/publish-packages.sh): version, commit, build time), plus the changelog link `/changelog/#v<version>`.
- `package`: the package of that release for the client's kind, build type, system and architecture, with its size and sha256. A dev client is offered `sharetoken-cli-dev-*`. Dev desktop installers are not published, so a dev desktop app gets none.

A client decides that a newer version exists when its version is below `min` (upgrade required), `recommended` or `latest`, or when the release is **a newer build of the same version**: the commit differs, the build time is later, and a package exists for this client. While the version stays 1.0.0, the second rule is the one that fires. All packages of one release share `ST_BUILD_TIME`, which `ldflags.sh` writes into `version.BuildTime`.

The notice appears in these places:

- At the top of the desktop app's page: the version, a download link (the package for this system, or the download page if there is none), "What's new", and "Skip this version". A required upgrade is shown in red, says that existing jobs still settle, and cannot be skipped.
- In the tray menu: "Update available: x" or "Upgrade required: x"; clicking it opens the window.
- As a system notification, once per new build (through Wails' notification API; macOS asks for permission the first time). The daemon records skipped and notified builds in `update.json` in the data directory, so a restart does not repeat them.
- On the command line: `sharetoken up` writes one WARN line to its log; `sharetoken status` shows it; `sharetoken version --check` asks the portal directly and needs no running client.

Local API: `update` in `GET /api/v1/session`, and in `GET /api/v1/status` when there is a newer version; `POST /api/v1/update/check`, `POST /api/v1/update/skip` and `POST /api/v1/update/notified` (body `{"key"}`; a key that is not the build currently offered gets 409 `not_offered`).

**Not implemented:** downloading and installing are done by the user. The update information is not signed; the client only shows the link and the sha256 and never runs a downloaded file. Automatic upgrades would first need the release information to be signed with an offline key.

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
dist/SHA256SUMS-1.0.0
```

**Publishing packages:** `scripts/publish-packages.sh <version> <user@host> <directory> [--with-dev-cli] [--owner <user>]` runs on a Mac and builds 12 packages (desktop installers and command-line archives, for three systems and two architectures). It uploads them, verifies them on the server, and then replaces the portal's downloads directory (`downloads.dir` in the portal's configuration) as a whole; the portal serves them under `/downloads/`. `--owner` installs the files through sudo as the user the portal runs as. `--with-dev-cli` adds six `sharetoken-cli-dev-*` packages for testing: `GET /api/v1/downloads` does not list them, and they can only be fetched by file name.

Program names carry no version (`sharetoken`, `sharetoken-gui`; dev builds are `sharetoken-dev`, `sharetoken-gui-dev`): the window finds the daemon by a fixed name, and a user's PATH keeps working across upgrades. Server programs built with `scripts/build.sh` do carry the version in their file name.

See [desktop.md](desktop.md) for the client packages, [development.md](development.md) for building and testing, and [self-hosting.md](self-hosting.md) for running the server programs.

## Tests

- `scripts/test.sh`: starts MySQL 8 in Docker and runs all Go tests.
- [`internal/protocol/golden_test.go`](../internal/protocol/golden_test.go): fixed vectors for the signed messages.
- [`internal/mysqldb`](../internal/mysqldb): migration order, resuming after an interruption, concurrent start-up, older programs meeting a breaking migration.
- [`scripts/version_ui_test.mjs`](../scripts/version_ui_test.mjs): the version pages and the update notice, in headless Chrome.
- **Not implemented:** an end-to-end test against the previous release (building the previous tag's client and trading through the current coordinator and portal).
