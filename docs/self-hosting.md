# Self-hosting with Docker Compose

**English** | [简体中文](zh-CN/self-hosting.md)

`docker-compose.yml` in the repository root starts a complete ShareToken network
on one machine: MySQL 8, the portal, the coordinator and one relay. It is meant
for trying the project out and for small private deployments. To install the
programs by hand, one by one, see [guide.md](guide.md).

## 1. Start it

Requirements: Docker with the Compose plugin. Nothing else: the image builds the
programs from this tree.

```bash
cp .env.example .env
# edit .env: DB_PASSWORD, DB_ROOT_PASSWORD, ADMIN_EMAIL, MAIL_FROM, PUBLIC_HOST
docker compose up -d --build
docker compose ps
```

`PUBLIC_HOST` is the address clients reach this machine at, an IP address or a
DNS name. The relay announces it and the portal's self-signed certificate is
issued for it. The default, `127.0.0.1`, only works for clients on the same
machine.

Ports that must be reachable from clients:

| Port | Program | Setting |
|---|---|---|
| 8092/tcp | portal (HTTPS) | `PORTAL_PORT`, `PORTAL_BIND` |
| 4001/tcp and 4001/udp | relay (TCP and QUIC) | `RELAY_PORT` |

The coordinator and MySQL publish no port.

## 2. What the services do

| Service | Runs | State (volume) |
|---|---|---|
| `mysql` | MySQL 8 with the settings the programs expect (`utf8mb4_bin`, READ-COMMITTED) | `mysql` |
| `init` | Once per `up`, then exits. Writes the three configuration files if they are missing, creates the keys, registers the relay with the portal and creates the administrator. | all three below |
| `portal` | `portal serve` | `portal`: `portal.yaml`, `portal-pass.key`, `portal-tls/` |
| `relay` | `relay` | `relay`: `relay.yaml`, `relay.key` |
| `coordinator` | `coordinator serve` | `coordinator`: `coordinator.yaml`, `coordinator.key` |

The coordinator listens nowhere. It dials the relays registered with the portal
and is reached through them. In this file it shares the relay container's
network, so the relay's public address also leads to the relay from inside.

`init` prints the three public values that tie the network together:

```bash
docker compose logs init
# init: portal pass key ed25519:…      relays accept passes signed with it
# init: coordinator    12D3KooW…       the peer relays let reserve without a pass
# init: relay          12D3KooW…       registered with the portal
```

## 3. Connect a client

The portal starts with a self-signed certificate and prints its fingerprint.
Clients pin it:

```bash
docker compose logs portal | grep sha256:
```

A client built from this tree with plain `go build` is a development build: it
keeps its state in `~/.sharetoken-dev` and takes the portal's address from
`SHARETOKEN_PORTAL`.

```bash
go build -o bin/ ./cmd/sharetoken
export SHARETOKEN_PORTAL=https://<PUBLIC_HOST>:8092
bin/sharetoken login -email alice@example.com -portal-pin sha256:<fingerprint>
bin/sharetoken up                       # http://127.0.0.1:9887/v1 for a development build
bin/sharetoken status
```

To see a trade, start a second user with its own state directory
(`SHARETOKEN_HOME`), share a model from one and call it from the other.
`scripts/dev/mock_upstream.py` is a stand-in model server when no real one is at
hand:

```bash
python3 scripts/dev/mock_upstream.py 18611 mock-model &
bin/sharetoken share upstream http://127.0.0.1:18611/v1
bin/sharetoken share add mock-model
```

The web pages are at `https://<PUBLIC_HOST>:8092/` (site), `/app/` (user app) and
`/admin/` (admin console). The administrator set in `.env` (`ADMIN_EMAIL`) signs
in there with a mailed code and sets up two-step verification on first sign-in.

**Sign-in codes.** There are no passwords: everyone signs in with a six-digit
code mailed to their address, and the first sign-in of an address creates its
account. The portal only queues the mail; this Compose file starts no mail
server ([mail.md](mail.md) sets one up). Until it runs, a code can be read from
the queue, which is fine for a trial on your own machine:

```bash
docker compose exec mysql sh -c 'mysql -usharetoken -p"$MYSQL_PASSWORD" sharetoken -N \
  -e "SELECT to_addr, subject FROM mail_outbox ORDER BY id DESC LIMIT 5"'
```

`MAIL_FROM` must be set for any code to be queued at all.

## 4. Settings

`.env` holds the settings of the Compose file. `init` turns them into the
configuration files **only when a file does not exist yet**, so later changes to
these variables do not rewrite a file. Change the file instead.

| Variable | Default | Meaning |
|---|---|---|
| `DB_PASSWORD`, `DB_ROOT_PASSWORD` | none, required | MySQL passwords |
| `PUBLIC_HOST` | `127.0.0.1` | address clients use |
| `PORTAL_PORT`, `PORTAL_BIND` | `8092`, `0.0.0.0` | where the portal is published |
| `RELAY_PORT` | `4001` | the relay's port, inside and outside the container |
| `PORTAL_TLS_MODE` | `auto` | `auto`: self-signed certificate. `proxy`: a reverse proxy terminates TLS |
| `MAIL_FROM`, `MAIL_SUPPORT` | empty | sender of the portal's mail and the support address. Sign-in codes are mail: without `MAIL_FROM` nobody can sign in |
| `OPEN_REGISTRATION` | `true` | an address without an account gets one at its first sign-in |
| `SIGNUP_CREDIT_MICROS` | `5000000` in `.env.example` | credit of a new user (1 credit = 1,000,000 micro-credits) |
| `ADMIN_EMAIL` | empty | the first administrator's email address; the account is created when it is set |
| `FEE_BPS` | `500` | the platform's cut in basis points |
| `ST_VERSION`, `ST_COMMIT` | empty | version stamped into the programs (default `<VERSION>-dev.docker`) |

To edit a configuration file, copy it out, change it and copy it back:

```bash
docker compose cp portal:/var/lib/sharetoken/portal/portal.yaml .
docker compose cp portal.yaml portal:/var/lib/sharetoken/portal/portal.yaml
docker compose restart portal
```

Every key is described in [configuration.md](configuration.md) and in
`configs/*.example.yaml`.

Admin commands run inside the containers:

```bash
docker compose exec portal portal -config /var/lib/sharetoken/portal/portal.yaml overview
docker compose exec portal portal -config /var/lib/sharetoken/portal/portal.yaml grant alice 1000000 "welcome"
docker compose exec coordinator coordinator -config /var/lib/sharetoken/coordinator/coordinator.yaml nodes
```

## 5. Beyond the trial

**A real certificate.** Put a reverse proxy (Caddy, nginx) with a certificate from
a public CA in front of the portal, set `tls.mode: proxy` in `portal.yaml` (or
`PORTAL_TLS_MODE=proxy` before the first start), publish the portal on loopback
only (`PORTAL_BIND=127.0.0.1`). Clients then need no fingerprint.

**Audits and exact token counts.** Add a reference copy of each model you want
audited to `references` in `coordinator.yaml`: an OpenAI-compatible server the
coordinator can reach. Without one there are no audits, and disputes use a
heuristic token count.

**More relays.** Run `relay` on other machines with a `relay.yaml` that lists the
same portal key and coordinator ID (`docker compose logs init` prints both), then
register each with `portal relay add '<address>' <name> <region>`. The relay
prints the exact command when it starts. Clients choose among relays by
round-trip time, and sellers reserve on more than one.

**Mail.** Every sign-in needs the mail server, which this Compose file does not
start. See [mail.md](mail.md).

**Changing `PUBLIC_HOST` later.** The relay's address is stored in two places.
Edit `network.announce` in `relay.yaml`, remove the relay's registration
(`portal relay ls`, `portal relay rm <id>`) and run `docker compose up -d` again:
`init` registers it under the new address.

## 6. What to back up

- the `mysql` volume (accounts, the ledger, jobs);
- `portal-pass.key`: relays trust its public key, and by default the
  administrators' two-step secrets are encrypted with a key derived from it;
- `portal-tls/` when clients pin the self-signed certificate;
- `coordinator.key`: relays admit the coordinator by its ID;
- `relay.key`: the relay's address contains its ID.

`docker compose down` keeps the volumes. `docker compose down -v` deletes them,
and with them every account and balance.

## 7. Security notes

- The `init` container mounts all three key volumes in order to wire the network
  together. On a deployment split across machines, do those steps by hand so that
  no host holds another program's key ([guide.md](guide.md)).
- MySQL is reachable only inside the Compose network. Keep it that way.
- `OPEN_REGISTRATION=true` with a sign-up credit lets anyone create accounts that
  hold credit; each needs only a mailbox. For anything public, restrict the
  allowed email domains or require invite codes on the settings page, or close
  registration and create users as an administrator.
- [security.md](security.md) lists what the network protects and what it does not.
