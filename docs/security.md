# Security

**English** | [简体中文](zh-CN/security.md)

ShareToken lets people buy inference from machines they do not control, pay for
it with credit held by a platform, and reach each other through relays run by
third parties. This document states who is trusted with what, what each party
can and cannot do, and where the protection stops. The design behind it is in
[architecture.md](architecture.md).

## 1. Parties and what they are trusted with

| Party | Trusted with | Not trusted with |
|---|---|---|
| Buyer's client | Its own prompts and credit | Paying honestly: it may refuse a valid receipt |
| Seller's client | The plaintext of the jobs it serves (unless it runs in a TEE) | Running the advertised model, reporting usage honestly, returning what the model produced |
| Relay | Forwarding bytes | Anything else. It sees who talks to whom and how much, never content |
| Coordinator | The ledger, matching, dispute decisions | Prompts and answers: they do not pass through it. It sees one job's content only when the buyer discloses it as evidence |
| Portal | Accounts (email addresses; no passwords), sign-in codes (salted hashes), tokens (hashed), two-factor secrets (encrypted), device bindings, credit grants | Prompts and answers, device private keys |
| Network | Nothing | |

Every party is a libp2p Ed25519 identity. Offers are signed by the seller,
tickets by the coordinator, receipts by the seller and acknowledgements by the
buyer. Each signature covers a canonical, length-prefixed encoding with its own
domain string (`internal/canon`, `internal/protocol`), so a signature made for
one kind of message cannot be replayed as another.

## 2. What a buyer can rely on

### 2.1 Confidentiality on the way

The prompt goes from the buyer to the seller and nowhere else. It is protected
by two layers:

- **Transport.** Every libp2p connection uses Noise, end to end between the two
  peers, including when the connection is carried by a relay. The relay
  forwards ciphertext.
- **Sealed box** (`internal/box`). The buyer seals the request from a fresh
  per-job X25519 key to the seller's box key: X25519 ECDH, HKDF-SHA256,
  AES-256-GCM. The seller seals the response as a numbered stream under one key
  that both derive from the same agreement, bound to the job, the direction and
  both public keys; the frame number is the nonce. The additional data binds
  every ciphertext to its job, and every response frame to its sequence number,
  so ciphertexts cannot be moved to another job or reordered.

  The buyer can derive the response key too, so it could produce frames that
  open. That gives it nothing: what the seller delivered is fixed by the
  receipt, which signs a hash of the exact ciphertexts (2.2); before the stream
  key, anyone could seal a chunk to the buyer's public key in the same way.

- **Lengths and timing** (`protocol/codec.go`). Before sealing, the request is
  compressed and padded to a size bucket (steps of a quarter), so a relay sees
  its size only within 25%. The response is one compressed stream; the seller
  cuts it into frames at most every 20 ms, so tokens that arrive together share
  a frame, and pads each frame to 32-byte steps (256-byte steps above 1 KiB). A
  relay therefore cannot read the length of single tokens off the frame sizes,
  the way published attacks on streamed answers do. Compression alone would
  make that worse (a compressed size reflects how repetitive the text is),
  which is why padding comes with it. What remains visible: the time at which
  frames are sent, and so the rhythm of the answer at 20 ms resolution, and its
  total size.

The second layer exists because of what the first cannot do: the box key can
live inside a TEE while the host's network stack terminates Noise, and receipts
are computed over the sealed bytes.

The control path (tickets, receipts, disputes) also runs over Noise end to end
through the relay, so relays do not see it either.

### 2.2 Integrity of the answer

The seller signs a receipt (`internal/receipt`) that says: for the request whose
sealed bytes hash to this value, I returned the response whose sealed frames hash
to that value, using this model and this many tokens. The buyer's client
recomputes both hashes from the bytes it actually sent and received and verifies
every frame's authentication tag and order as it arrives. A seller cannot sign
one transcript and deliver another, and nobody in between can alter, drop or
reorder frames without the check failing.

Non-streaming responses are returned to the caller only after the receipt has
been verified (`X-Sharetoken-Verified: true`). Streaming responses are forwarded
as they arrive and end with a comment line stating the result.

### 2.3 Honest billing

A receipt also carries token counts, and the buyer's client estimates them from
the transcript. When they are implausible it rejects the receipt and opens a
dispute with evidence (`internal/evidence`): the private key of that one job, the
sealed request and the sealed response frames. Because the request was sealed
from a per-job key and the seller sealed every frame under a key derived from
that same key, the coordinator can check without trusting the buyer:

- the request and the frames are exactly what the seller signed for;
- the disclosed key is the one the request committed to;
- whether every frame opens under the derived key and decodes;
- how many tokens the transcript really contains, counted with a reference
  tokenizer when the coordinator has a reference copy of the model
  (`internal/tokens`).

Only that job is revealed: every job has its own key. The coordinator holds the
plaintext in memory while it decides and records only the outcome. A claim about
bytes the seller never signed cannot be decided this way and goes to an
administrator.

### 2.4 The advertised model

A receipt proves what was returned. It does not prove which model produced it.
Two mechanisms address that:

- **Audits** (`internal/audit`). At random times the coordinator buys one
  completion from a seller under a fresh identity, through the ordinary buyer
  path, with a randomized prompt and greedy decoding. It sends the byte-identical
  request to its own reference copy of the model and compares the output and the
  usage. A clear mismatch costs reputation, and consecutive mismatches suspend
  the seller from matching. Results in between are inconclusive and cost nothing,
  so numeric differences across hardware do not punish honest sellers.
- **Hardware attestation** (`internal/attest`). A seller in an AMD SEV-SNP or
  Intel TDX confidential VM publishes a quote, signed by the CPU, over the
  guest's launch measurement and a hash of both of its keys. A buyer that
  verifies the quote and finds the measurement on its allow-list knows that the
  prompt is decrypted only inside that image and that the receipt was signed by
  it. Both keys are bound: binding only the signing key would let a host keep the
  box key outside the VM.

### 2.5 The seller's side

- **A seller is paid for work it did.** The buyer's credit is placed on hold when
  the ticket is issued. A buyer that refuses a valid receipt has to dispute it,
  and the evidence then shows the receipt was right.
- **A ticket cannot be replayed or used by someone else.** It names the buyer,
  the seller, the job and an expiry, and the seller serves each job once.
- **A buyer that takes tickets and never connects** ties up a seller's slot for a
  bounded time: an unstarted job is released shortly after its ticket expires,
  with the hold returned and no penalty to the seller, and an account holding
  several stale tickets gets no new ones.
- **A seller that fails before delivering anything** abandons the job, which
  returns the buyer's hold at once.
- **A user cannot trade with themselves** to farm reputation: matching excludes
  every device of the buyer's own account.

### 2.6 Choosing your sellers

A buyer that does not want its prompts to reach arbitrary sellers can pair with
the ones it knows (`internal/accounts/pairing.go`). A seller hands out a pairing
code, the buyer redeems it, and from then on the coordinator matches that
buyer's requests only to the sellers it is paired with and lists only their
models. The rule is applied by the coordinator on every request, for every
device of the account, also when the request names a node, so a modified client
cannot step around it.

- A code is 80 random bits, expires (one day by default, 30 days at most) and
  can be limited to a number of buyers and revoked. Wrong codes are throttled
  per account and per address, like failed logins.
- Each side sees the other's account name masked. A code that leaks lets a
  stranger pair with the seller. With an open seller that gives the stranger
  nothing beyond what an unpaired buyer already has; with a private seller it
  lets the stranger buy. The seller sees every pairing and can remove it, and
  can limit a code to a number of buyers.
- Pairing restricts the buyer. A seller still serves buyers that are not paired
  with it, unless it turns on private mode: then the coordinator matches only
  its paired buyers to it and hides its models from everyone else.
- A buyer can be paired with three sellers at most.
- Private sellers are audited like any other: the coordinator's audit
  identities are matched to them. A private seller with few buyers can guess
  that an unknown peer is an audit, so audits of such a seller prove less.
- Administrators see every pairing with both account names in full.

### 2.7 Share partners

Two users can pair up as share partners and use each other's nodes for free
(`internal/accounts/sharing.go`). Like pairing, this is a matching and billing
rule the coordinator applies on every request: it tries the partners' nodes
first and marks such a ticket `Shared`, a signed field neither peer can change,
so the job moves no credits. Nothing else about the job changes: the partner's
node sees the prompt as any seller would, and receipts, hashes and audits work
as usual.

- An invite is a secret like a pairing code: 80 random bits, one day by
  default and 7 days at most, admits one person, and can be withdrawn. Wrong
  codes are throttled per account and per address, like failed logins. A
  leaked invite lets a stranger use the inviter's nodes for free within its
  limits; the inviter sees the partner and can end it.
- Each side sees the other's account name masked, and the tokens each used of
  the other in the last 24 hours. Administrators see every share group with
  names in full.
- What a user gives is bounded by its own settings: tokens per rolling 24 hours
  (counted from settled jobs, so jobs in flight can pass it a little) and a
  share of each node's concurrent slots (0 pauses). A user may have five
  partners at most.
- Partners are matched whatever the user's pairings, and a private seller still
  serves its partners: private mode concerns paying buyers only.

## 3. Accounts, devices and the platform

| Threat | Defence | Code |
|---|---|---|
| Binding someone else's device to your account, to collect its earnings | A binding needs the device's signature over the user name, the peer ID and a timestamp within five minutes. A bound device cannot be bound elsewhere. | `internal/accounts` |
| Stolen API token | Tokens can be revoked at any time, login tokens expire, and only their SHA-256 is stored. | `internal/accounts`, `internal/portal` |
| Guessing a sign-in code | There are no passwords: a sign-in needs a six-digit code mailed to the account's address. A code lives 10 minutes and allows 5 tries; an address gets at most 5 codes an hour (20 per IP, 500 in total); wrong codes are also throttled per IP and per address. Only a salted hash of a code is stored, and the message is erased from the outbox once sent. | `internal/mailer/codes.go`, `internal/portal/auth.go` |
| Someone else's mailbox | The mailbox is the key to the account: whoever reads it can sign in. Users may add TOTP two-factor authentication on the account page, and a notice is mailed when it is turned on or off. A portal without a working mail sender lets nobody sign in. | `internal/portal/mfa.go` |
| A disabled user keeps trading | The coordinator checks the user's state on every request, tokens stop working at once, and relay passes run out within their lifetime (one hour by default). | `internal/accounts`, `internal/relaypass` |
| An administrator's mailbox read by someone else | Administrators must use TOTP two-step verification and cannot turn it off. A token issued without it cannot reach the admin API. Wrong codes are throttled, a challenge is void after five mistakes, and a code cannot be replayed. TOTP secrets are stored encrypted (AES-GCM). | `internal/portal/mfa.go`, `internal/totp` |
| Session theft, cross-site request forgery, script injection in the web pages | Sessions are HttpOnly, SameSite=Strict cookies that only count together with a custom header, and the Origin must be the portal's own. A strict Content-Security-Policy allows no inline code and no third-party resources. The pages build the DOM from text and never from HTML strings. | `internal/portal/web.go` |
| Eavesdropping on the portal connection | HTTPS by default. A self-signed certificate is pinned by fingerprint in the client, and the client refuses to send sign-in codes over plain HTTP to another machine. | `internal/portal/tls.go`, `internal/client` |
| A web page calls the local client API to spend the user's credit | The local API binds to loopback and refuses any request whose Origin is not local. | `internal/client` |
| Bypassing the relays to reach the coordinator | The coordinator listens on no address. | `cmd/coordinator` |
| Strangers filling a relay's reservation slots | A reservation needs a short-lived pass signed by the portal and bound to the device's peer ID, with a limit on devices per user. Relays verify it offline. | `internal/relaypass`, `internal/relay` |
| Exhausting memory with huge frames or floods of streams | Frames grow with the data that arrives, never from the declared length. Stream handlers reserve memory from libp2p's resource manager first, with limits per stream, per peer and in total. | `internal/protocol`, `internal/p2p` |
| A relay goes down | Clients switch relays automatically. Sellers reserve on more than one, and the coordinator reserves on all of them. | `internal/client`, `internal/p2p` |
| Tampering with the ledger | Double-entry: every movement is a balanced transfer, journaled, and the journal is replayed against the balances on startup. All writes of the portal and the coordinator are serialized by one database lock. | `internal/ledger`, `internal/mysqldb` |
| Mail from strangers | The mail server accepts mail only for listed mailboxes, forwards nothing, shows only plain text in the console and serves attachments as downloads. It needs no access to the ledger or the user tables. | `internal/mailer` |

Files that hold secrets on a user's machine (the device key, the configuration
with the login token, provider credentials) are created so that only their user
can read them: mode 0600 on Unix, an ACL on Windows (`internal/secfile`).

## 4. Limits

Stated plainly, because a reader deciding whether to rely on the network needs
them more than the list above.

- **Without a TEE the seller reads the prompt.** A model cannot run on
  ciphertext. Encryption moves the trust boundary to the seller's machine, and
  only attestation removes the operator from it. Buyers who need this set
  `require_tee`.
- **Hosted-API sellers pass the prompt on.** A seller that resells a hosted API
  sends the request to that provider under its own account. The buyer's data is
  then also subject to that provider's policy, and the seller is responsible for
  that provider's terms of service.
- **Audits are probabilistic and need a reference.** A seller that could tell an
  audit from a real job could behave only when audited. Audit prompts are
  randomized and come from a fresh identity each time, but an operator who needs
  stronger cover should give the auditor its own network egress. Only models
  with a reference copy can be audited, which in practice means open-weight
  models. For closed models nothing proves which model produced the text.
- **Without a reference, usage checks are loose.** The fallback token counter is
  a heuristic and catches only gross over-billing.
- **Attestation is only as good as the image.** The allow-listed measurement must
  belong to a reproducible build that keeps keys in memory and serves a pinned
  model. A CPU TEE does not cover GPU memory. Side channels and vendor firmware
  bugs are out of scope.
- **Relayed connections are not admission-controlled.** Anyone can open a circuit
  to a reserved peer. The peer authenticates and rejects strangers, but the
  relay's bandwidth is used. Only aggregate limits bound it.
- **Pairing is a matching rule, not encryption.** It decides which sellers a
  buyer's requests are sent to. What a paired seller can see of a prompt is
  unchanged, and the platform, which keeps the pairings, could alter them.
- **Share partners are a matching and billing rule too.** The platform keeps
  the partnerships and the shared flag, and could alter them or charge for a
  job it marked free.
- **Relays see metadata.** They see who connects to whom, when and how much, not
  what.
- **The platform is trusted with the accounting.** One coordinator and one portal
  hold the ledger. They cannot read prompts, but a dishonest operator could
  misstate balances. Credit is granted by administrators and there is no on-chain
  or external settlement.
- **A self-signed portal certificate depends on the first check.** Users must
  compare the fingerprint with the one the operator publishes. A certificate from
  a public CA is better where a domain name exists.

## 5. Notes for operators

- Run each server program as its own system user, with its key files readable by
  that user only. The portal's pass key, the coordinator's key and the mail
  server's DKIM key have no reason to be readable by one another.
- Give the mail server a database account limited to its own tables.
- Keep the database reachable only from the portal's and the coordinator's
  machines.
- Back up the portal's pass key, its self-signed certificate directory if you use
  one, the coordinator's key, each relay's key and the database. Losing a key
  changes an identity that others have pinned.
- Configuration files never hold secrets: they name an environment variable or a
  file ([configuration.md](configuration.md)).
- The relay's status and metrics endpoint has no authentication and only binds to
  loopback. Read it over SSH or through an authenticated proxy.
- `dev.open_accounts` in the coordinator's configuration is for test networks
  only: it lets unregistered peers trade.

## Reporting a vulnerability

Please report security problems privately, through the repository's private
vulnerability reporting ("Report a vulnerability" under the Security tab) or by
mail to the support address shown on <https://sharetoken.app>. Do not open a
public issue. Include what you found, how to reproduce it and what an attacker
gains. You will get an answer, and credit in the fix if you want it.
