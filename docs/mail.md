# Mail

**English** | [简体中文](zh-CN/mail.md)

The mail server sends sign-in codes and notices, and receives mail for a few addresses such as `support@`, which administrators read and answer in the admin console. **A portal needs it**: there are no passwords, and everyone, administrators included, signs in with a code mailed to their address. Without a sender (`mail.from`) and a running mail server nobody can sign in.

Code: [`internal/mailer`](../internal/mailer) (the library), [`cmd/mailserver`](../cmd/mailserver) (the server), [`internal/portal/mail.go`](../internal/portal/mail.go) (the portal's endpoints). Example configuration: [`configs/mail.example.yaml`](../configs/mail.example.yaml).

## Structure

```
portal ── writes mail_outbox / mail_codes ──▶ MySQL ◀── delivers; writes mail_inbox ── mailserver ◀── port 25 ── internet
  ▲                                                                                        │
  └── admin console /admin/#/mail                                                          └── recipient's MX, a smarthost, or the Resend API
```

- The portal and the mail server talk **only through the database**. While the mail server is down, mail queues up and the sign-in endpoints keep answering, but no code arrives until it is back.
- The mail server is the one process strangers talk to directly. Run it as its own system user, with a MySQL account of its own that may only use `mail_outbox`, `mail_inbox` and `mail_status`. It then cannot reach the ledger, the users or the verification codes, and it cannot read the keys of the portal or the coordinator. Give it nothing beyond the right to bind port 25.
- It accepts mail only for the addresses listed in `smtp.mailboxes` and refuses everything else during the SMTP dialogue (550). It never forwards and never answers automatically, so it is not an open relay and produces no backscatter.

## Setting it up

1. Create the restricted MySQL account. The portal creates the mail tables when it starts; `mailserver -config mail.yaml migrate` does the same with an account that may create tables.
2. Copy `configs/mail.example.yaml` to `mail.yaml` and set `hostname` (the name MX points to), `domain`, `dkim.selector`, `delivery.mode` and `smtp.mailboxes`.
3. Create the DKIM key and publish the DNS records below.
4. Start the server, and set the sender addresses on the portal: `mail.from` and `mail.support` in `portal.yaml`, or the settings page.

```bash
mailserver -config mail.yaml check-config          # validate, print the effective settings
mailserver -config mail.yaml dkim-keygen           # create dkim.key_file, print its TXT record
mailserver -config mail.yaml dns                   # the DKIM record to publish, and a check of what is published now
mailserver -config mail.yaml migrate               # create the mail tables
mailserver -config mail.yaml serve                 # run
mailserver -config mail.yaml send-test you@example.net   # queue a test message; the running server delivers it
```

A DKIM key is required in every delivery mode: without `dkim.selector` and `dkim.key_file` the server does not start.

## DNS

For a deployment that sends as `…@example.com` from the host `mail.example.com`, with the DKIM selector `s1`:

| Record | Value | Purpose |
|---|---|---|
| `MX example.com` | `10 mail.example.com` | Where mail for the domain is delivered. Needed to receive. |
| `A mail.example.com` (and `AAAA`) | The mail server's public address | The host MX points to. |
| `TXT example.com` | `v=spf1 a:mail.example.com ~all` | SPF: which hosts may send for the domain. With a smarthost or an API provider, use what the provider tells you to publish instead (usually an `include:`). |
| `TXT s1._domainkey.example.com` | Printed by `mailserver dkim-keygen` | The DKIM public key. |
| `TXT _dmarc.example.com` | `v=DMARC1; p=quarantine; rua=mailto:postmaster@example.com` | DMARC policy. Start with `p=none` if you want reports before enforcing. |

For direct delivery, the reverse DNS (PTR) of the sending address should also resolve to `mail.example.com`, and that name back to the address. Many receivers reject or downgrade mail when it does not.

Check the records with `mailserver -config mail.yaml dns`: it looks up MX, A, SPF, DKIM (compared with the local private key) and DMARC, and prints `ok` or `BAD` for each. The running server repeats the check every 10 minutes and shows it on the "service status" tab of the console's mail section. The check reports only; it changes nothing.

## Delivery modes

`delivery.mode` in `mail.yaml`:

| Mode | How mail leaves | When to use it |
|---|---|---|
| `direct` (default) | The server connects to the recipient domain's MX on port 25 and signs each message with its own DKIM key. | A host with a static address, a matching PTR record, a clean reputation and open outbound port 25. |
| `smarthost` | Everything is handed to one relay: `delivery.smarthost.addr` on port 587 (STARTTLS) or 465 (TLS), with `username` and `password_file`. The relay's certificate is verified. | Outbound port 25 is blocked, or the address has no usable reputation (residential or dynamic ranges), and you have an SMTP relay account. |
| `resend` | Messages go to the [Resend](https://resend.com) HTTPS API with the key in `delivery.resend.api_key_file`. Resend builds the message and signs it with its own DKIM key, so the domain must be verified with Resend and Resend's DNS records published. | Outbound SMTP ports are blocked altogether. Many cloud providers block 25, and some also 465 and 587; HTTPS always gets out. |

Notes:

- Before choosing `direct`, test that the host can open a connection to port 25 of an outside mail server. Cloud providers often block it by default.
- In `resend` mode each message carries its Message-ID as the idempotency key, so a retry after a lost answer does not produce a second copy. HTTP 400 and 422 are permanent failures. 401 and 403 (a problem with the key or the domain verification), 429 and 5xx leave the message queued for retry. Without a readable key the server does not start.
- A temporary failure is retried for `delivery.retry_for` (48 hours by default). Sent mail is kept for `delivery.keep_for`.
- "Accepted by the recipient's server" in the console does not mean the user saw the message in the inbox: the server cannot see where it was filed. The sign-in page therefore tells users to look in the spam folder.

To verify a deployment from outside, send a test message to a mailbox you control at a large provider and read its `Authentication-Results` header: SPF, DKIM and DMARC should all pass.

## Sign-in codes

One flow for new and existing accounts:

1. `POST /api/v1/auth/code {email, invite_code?}` mails a six-digit code and says whether the address is new (`new_account`). For a new address the registration rules are checked before any mail goes out (open registration, the allowed domains and the blocked addresses on the settings page, an invite code when one is required), so mail only reaches people who can finish. Refusals of a new address are limited to 8 per IP address in 5 minutes, so the answers are no fast directory of who has an account.
2. `POST /api/v1/auth/verify {email, code}` (clients, a token for 60 days) or `POST /api/v1/web/auth/verify` (the portal's pages, a session for 15 days) redeems it. The first sign-in of a new address creates its account and grants the sign-up credit. An account with two-factor authentication then needs its authenticator code as well ([guide.md](guide.md), section 4.2).

| Limit | Value |
|---|---|
| Lifetime | 10 minutes. An expired code's message is no longer delivered. |
| Asking again | Once per 60 seconds for one address (`code_cooldown`). |
| Per hour | 5 for one address, 20 for one IP address, 500 in total (`throttled`). |
| Guessing | 5 attempts per code; wrong codes are also limited per IP address and per address (8 in 5 minutes). |

Only a salted hash of a code is stored. Once the message has been sent, its subject and body are erased from the database, and while it is queued administrators cannot read it in the console. The web pages, the desktop client (through its daemon's `POST /api/v1/auth/code`) and the command line (`sharetoken login -email`) all use this flow. The message's subject is `Your ShareToken verification code: 123456`.

## Mail settings in the console

The sender address, the support address and "send a test message" are at `/admin/#/settings` and apply as soon as they are saved. **The delivery mode and its credentials stay in `mail.yaml`** and are read-only on the page: the mail server's database account cannot read the portal's tables, and someone who can change settings should not be able to reroute all outgoing mail, sign-in codes included.

Other notices: a message is sent when two-factor authentication is turned on or off for an account. Code that sends a notice calls `mailer.Store.Enqueue` and does not wait for delivery.

## Receiving safely

- Incoming mail comes from strangers. The console shows plain text only; from HTML only the text is extracted, and it is never loaded as HTML. Attachments and the raw message are downloaded as `application/octet-stream`.
- A message that passes neither SPF nor DKIM in alignment with its From domain is marked "unverified" with a warning. Sender addresses can be forged: do not act on an account because of what an unverified message says.
- Limits (defaults, `smtp.*` in `mail.yaml`): 10 MiB per message; 60 messages an hour and 4 concurrent connections per IP address; 2 GiB for the whole inbox. Above the total the server answers 452 and the sender retries later.
- TLS: with `smtp.tls.cert_file` and `key_file` the server offers that certificate and picks up renewals without a restart; without them it uses a self-signed one, which still encrypts.

## Admin console

`/admin/#/mail` has the inbox, the archive, bounces, the outbox, the service status and a compose form. Messages can be answered (from the support address), archived, deleted, and downloaded raw or by attachment. Failed outgoing mail can be sent again; verification-code mail cannot, the user asks for a new code.

## Tests

```bash
scripts/test.sh -race ./internal/mailer ./internal/portal ./internal/client   # real SMTP round trips, DKIM, rate limits, codes, the admin endpoints
node scripts/mail_ui_test.mjs                                                 # headless Chrome against real processes: sign-in by code, hostile mail, the whole console
```

See [development.md](development.md) for what the tests need.
