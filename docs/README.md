# ShareToken documentation

**English** | [简体中文](zh-CN/README.md)

The end-user guide (installing the app, signing in, sharing a model) is published
with the portal at <https://sharetoken.app/docs/>. The pages here are for people
who run a network or work on the code.

| Document | Read it for |
|---|---|
| [architecture.md](architecture.md) | The four roles, the key design decisions, the database, honest limits |
| [security.md](security.md) | Who is trusted with what, the guarantees and their mechanisms, the limits, reporting a vulnerability |
| [guide.md](guide.md) | Reference: user commands, operator commands, the client's local API, the portal's HTTP API |
| [self-hosting.md](self-hosting.md) | Running a whole network with Docker Compose |
| [configuration.md](configuration.md) | The configuration files of relay, coordinator and portal |
| [providers.md](providers.md) | Selling hosted APIs: provider kinds, API keys and OAuth logins |
| [apis.md](apis.md) | The three model APIs (chat completions, Messages, Responses): passing through, converting, matching, billing |
| [desktop.md](desktop.md) | The desktop app: structure, local API, tray, packaging |
| [mail.md](mail.md) | The mail server: verification codes, delivery modes, DNS |
| [versioning.md](versioning.md) | Release versions, interface versions, upgrade rules, update reminders |
| [i18n.md](i18n.md) | Languages of the site, the portal and the client |
| [development.md](development.md) | Building, testing, packaging, conventions |

Suggested order for a first read: [architecture.md](architecture.md), then
[security.md](security.md), then [self-hosting.md](self-hosting.md) to see it run.

Each page has a Chinese version of the same name under [zh-CN/](zh-CN/README.md).
English is the reference: when the two differ, the English page is right.
