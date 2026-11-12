# Development

**English** | [简体中文](zh-CN/development.md)

How to build, test and package the project. For what the code does, start with
[architecture.md](architecture.md).

## 1. Layout

```
cmd/         one directory per program: sharetoken, relay, coordinator, portal, mailserver
internal/    all library code; nothing here is a public Go API
gui/         the desktop shell (Wails), a separate Go module with its own go.mod
test/        end-to-end tests over real libp2p hosts (test/e2e, test/flow)
configs/     example configuration files; the tests load them
docker/      entry point of the container image
scripts/     build, packaging, test and demo scripts
docs/        documentation: English here, 简体中文 in docs/zh-CN
```

The packages under `internal/`, from the wire up:

| Layer | Packages |
|---|---|
| Wire and cryptography | `protocol`, `canon`, `box`, `receipt`, `attest` (`sevsnp`, `tdx`, `hw`), `relaypass` |
| Verification | `evidence`, `tokens`, `audit` |
| Network | `p2p`, `relay` |
| User side | `client` (daemon, local API, embedded UI in `client/ui`), `buyer`, `node`, `provider` |
| Platform | `coordinator`, `ledger`, `accounts`, `portal` (HTTP API and the web pages in `portal/web`), `mailer` |
| Support | `config`, `mysqldb`, `secfile`, `totp`, `version`, `cli` |

Two rules keep this readable. The wire packages import nothing from the layers
above them. And the strings that separate signature domains
(`sharetoken/receipt/v1` and the like) are part of the protocol: they are not
import paths and do not change when a package moves.
`internal/protocol/golden_test.go` fails if a signed encoding changes.

## 2. Build

Go 1.26 or newer. The server programs and the command-line client need no cgo.

```bash
go build -o bin/ ./cmd/...
```

**Build types.** The client is built as `dev` or `prod`, and a plain `go build`
is `dev`, so that a build from a working tree never talks to the production
network by accident:

| | dev | prod |
|---|---|---|
| Portal | the project's test network, or `$SHARETOKEN_PORTAL` | `https://sharetoken.app` |
| State directory | `~/.sharetoken-dev` | `~/.sharetoken` |
| Local endpoint | `127.0.0.1:9887` | `127.0.0.1:9787` |

For your own network, use a dev build and set `SHARETOKEN_PORTAL`
([self-hosting.md](self-hosting.md)).

**Versions.** `VERSION` holds the release number. `scripts/version.sh` prints it
on the release tag and `<version>-dev.<commit>` anywhere else, and
`scripts/ldflags.sh` stamps it into a program. The rules are in
[versioning.md](versioning.md).

| Script | Output |
|---|---|
| `scripts/build.sh portal coordinator relay mailserver` | server programs in `dist/`, named with version, system and architecture. `GOOS` and `GOARCH` cross-compile |
| `scripts/build-cli.sh <dev\|prod> [system] [arch]` | command-line client archives |
| `scripts/build-gui.sh <dev\|prod> [macos\|windows] [arch]` | the desktop app and its installer (`.dmg`, `.exe`) |
| `scripts/build-gui-linux.sh <dev\|prod> [arch]` | the Linux desktop package (`.deb`), built in Docker (`scripts/gui-builder.Dockerfile`) |
| `scripts/publish-packages.sh <version> <user@host> <dir>` | builds every package of a release and uploads them to a portal's downloads directory |
| `docker build -t sharetoken .` | the server image used by `docker-compose.yml` |

The desktop app is described in [desktop.md](desktop.md).

## 3. Test

The code runs on MySQL 8 only, and so do the tests. `scripts/test.sh` starts a
MySQL container (`scripts/local-mysql.sh`: loopback only, data in memory, kept
running between runs) and then runs `go test`:

```bash
scripts/test.sh                        # go test ./...
scripts/test.sh -race ./...
scripts/test.sh ./internal/coordinator
```

To use a server you already have, set `SHARETOKEN_TEST_MYSQL=mysql://user@host:3306/`
and run `go test` directly. Every test creates a database of its own.

What the Go tests cover: signatures and tampering, integer billing arithmetic,
the ledger (restart, rollback, tamper detection), every settlement and dispute
path, audit verdicts, SEV-SNP and TDX quote verification, the portal API and its
web security model, the mail server over real SMTP, and, in `test/e2e`, whole
networks of real libp2p hosts: failover, ticket replay and theft, refunds on
evidence, audits catching a swapped model, relays going down.

The desktop module has its own tests:

```bash
(cd gui && go test ./...)
```

**Browser tests.** The web pages and the desktop app's page are driven in a
headless Chrome over the DevTools protocol. They need Node 22 and Chrome, and no
packages. `scripts/gui_stack.sh` starts a private network for them in a
temporary directory:

```bash
scripts/gui_stack.sh start /tmp/st && . /tmp/st/env
node scripts/gui_ui_test.mjs /tmp/st /tmp/st/shots       # desktop app page
scripts/gui_stack.sh stop /tmp/st
```

The others (`portal_ui_test.mjs`, `settings_ui_test.mjs`, `theme_ui_test.mjs`,
`i18n_ui_test.mjs`, `docs_ui_test.mjs`, `mail_ui_test.mjs`,
`version_ui_test.mjs`) say at the top what they expect and how to run them.

**Other tools.**

| Script | Use |
|---|---|
| `scripts/demo-local.sh` | a whole network on 127.0.0.1 with a real model from Ollama, including a cheating seller |
| `scripts/mock_upstream.py` | a fast, honest stand-in for a model server |
| `scripts/inflating_upstream.py` | a dishonest one that inflates usage, for the demo |
| `scripts/st_bench.py` | functional checks and load generation against a running client |
| `scripts/upstream_proxy.py` | a local proxy in front of several model servers. Its configuration holds API keys and is not committed |
| `scripts/cli_test.sh`, `gui_shell_test.sh`, `gui_tray_test.sh` | tests of built packages |

## 4. Conventions

- **Secrets stay out of the repository.** `.gitignore` excludes keys, password
  files, `.env` files and the `local/` directory, which is where configuration
  and deployment scripts of real environments belong.
- **Configuration files carry no secrets either**: they name an environment
  variable or a file ([configuration.md](configuration.md)).
- **MySQL only, with numbered migrations** (`internal/mysqldb`). A schema change
  is a new migration and never an edit of an old one.
- **Signed encodings are frozen.** A change needs a new signing domain
  ([versioning.md](versioning.md)).
- **The web pages have no build step and no dependencies**: plain HTML, CSS and
  JavaScript embedded in the programs, under a Content-Security-Policy that
  allows no inline code.
- **Documentation is in English first.** Each page in `docs/` has a Chinese
  counterpart of the same name in `docs/zh-CN/`. Change both together.
- Run `gofmt` and `go vet ./...` before sending a change.
