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
| Private relay (run by a user, §2.8) | Forwarding bytes to its owners' nodes | Anything else, and less than the network's relays: no traffic with the coordinator passes through it |
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
  cuts it into frames at most every 50 ms, so tokens that arrive together share
  a frame, and pads each frame to 32-byte steps (256-byte steps above 1 KiB). A
  relay therefore cannot read the length of single tokens off the frame sizes,
  the way published attacks on streamed answers do. Compression alone would
  make that worse (a compressed size reflects how repetitive the text is),
  which is why padding comes with it. What remains visible: the time at which
  frames are sent, and so the rhythm of the answer at 50 ms resolution, and its
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

- **Audits** (`internal/audit`). At random times the coordinator buys a few
  answers (five by default) from a seller through the ordinary buyer path, in
  any of the three APIs, with open-ended questions (explanations, advice,
  rewrites; questions with one right answer tell nothing about the model) and
  greedy decoding. Its own reference copy of the model then reads each answer
  token by token: with teacher forcing it reports, at every position, whether
  the seller's token is its own first choice (a self-hosted vLLM does this in
  one call per answer, DeepSeek's prefix completion in one call per token);
  where the reference only reports log-probabilities for its own output
  (OpenAI), the seller's answer must leave the reference's greedy answer, if
  at all, at a near tie. The verdict is on the mean over all questions, and the
  threshold is calibrated per reference: the reference scores its own answers,
  which is what an honest seller returns, every day. The bill is checked
  against the reference's token counts. A clear mismatch costs reputation, and
  consecutive mismatches suspend the seller from matching; results in between
  are inconclusive and cost nothing. A new reference starts in shadow mode
  (verdicts recorded only), and so does one whose honest scores drifted (the
  provider changed the model behind the name). Models whose reference reports
  no log-probabilities (Claude, Gemini) are checked for the bill only.
  The questions are carried to the seller by users' clients
  (`internal/coordinator/errand.go`): a client asks the coordinator now and
  then whether there is one to carry, and runs it as a job of its own, from
  its own device and over the connection it uses for its own purchases, so
  the seller meets a buyer it may already know. Each question of an audit
  goes through a different client where there are several. The coordinator
  seals the request with a key it keeps: the client reads neither question
  nor answer, and cannot change them, because the seller's receipt signs the
  hashes of both and the coordinator checks what comes back against it. The
  ticket is paid by the platform, not by the client. Only a client the market
  could have matched to that seller for money carries: not the seller's own
  devices, not its share partners, not a stranger to a private seller. A
  user can turn carrying off, in the client's configuration file only
  (`buy.spot_checks`). When no client is there to carry a question, the auditor asks
  from a fresh identity of its own, and then only through circuits of the
  coordinator's own relays: never directly, which would show sellers the
  coordinator's address, and never through any other relay
  (`audit.delivery`).
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
  several stale tickets gets no new ones. Whatever their age, an account
  holds at most 32 tickets that have not started, and 8 for one node; further
  requests wait for one to start or to be released.
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

- A code is 30 random bits (6 base32 characters, easy to type), expires (one
  day by default, 7 days at most) and can be limited to a number of buyers and
  revoked. Its invite link carries a separate token of about 47.6 bits (8 base62
  characters) after the `#`, so it reaches no server log or Referer. Only a
  token gets a preview without an account (who invites, masked; the label; the
  expiry): a code is short enough to guess, a token is not.
- Because a code is short, wrong codes and links are limited hard: an account
  may enter 5 wrong ones per 15 minutes and 15 a day (an account younger than a
  day only 3), an address 10 per 15 minutes and 50 a day. Guessers with many
  accounts and addresses are stopped over everyone: 100 wrong codes in an hour
  from the whole site stop codes for everyone for an hour (invite links still
  work), and 2000 wrong links stop the public preview of links for an hour.
  Each pause sends the administrators a Pushover alert
  (`alerts.pushover_file`; `internal/portal/invites.go`). So at most about
  2400 codes can be tried a day, whoever tries: with 10 000 live codes, one is
  hit about every 45 days, and that only reaches the buyer side of a pairing
  or a share partnership the owner sees and can end (or a request, when the
  owner approves each use).
- A seller may ask to approve each use of a code: a leaked code then only files
  requests, which the seller sees and rejects.
- Any web page can open `sharetoken://`, so the desktop app never accepts an
  invite link by itself: it shows who invites and accepts only on a click. A
  link from another portal is shown as such and cannot be accepted.
- Each side sees the other's account name masked. A code that leaks lets a
  stranger pair with the seller. With an open seller that gives the stranger
  nothing beyond what an unpaired buyer already has; with a private seller it
  lets the stranger buy. The seller sees every pairing and can remove it, and
  can limit a code to a number of buyers.
- Pairing restricts the buyer. A seller still serves buyers that are not paired
  with it, unless it turns on private mode: then the coordinator matches only
  its paired buyers to it and hides its models from everyone else.
- A buyer can be paired with three sellers at most.
- Private sellers are audited like any other: the clients of their paired
  buyers carry the questions, and when none is there the coordinator's audit
  identities are matched to them. A private seller with few buyers can guess
  that an unknown peer is an audit, so audits that are not carried prove less
  for such a seller.
- Administrators see every pairing with both account names in full.

### 2.7 Share partners

Two users can pair up as share partners and use each other's nodes for free
(`internal/accounts/sharing.go`). Like pairing, this is a matching and billing
rule the coordinator applies on every request: it tries the partners' nodes
first and marks such a ticket `Shared`, a signed field neither peer can change,
so the job moves no credits. Nothing else about the job changes: the partner's
node sees the prompt as any seller would, and receipts, hashes and audits work
as usual.

- An invite is a secret like a pairing code: 30 random bits with a 47.6-bit
  link token, one day by default and 7 days at most, admits one person (or up to 100 when made for more), can ask for
  the inviter's approval, and can be withdrawn. Wrong codes and links are
  counted as for pairing codes. A
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

### 2.8 Private relays

A user can run a relay of their own (`relay init`, the same program as the
network's relays) and have their sharing nodes reached through it
(`private_relays` in the client's configuration). Three rules keep such a relay
from learning or deciding more than a seller already does:

- **It admits only its owners.** The relay lists accounts
  (`access.networks[].allow_accounts`). The portal signs the account into
  every relay pass, so the relay admits those accounts' devices offline, a new
  device needs no change on the relay, and a disabled user's passes run out
  within their lifetime. It carries no coordinator (no `allow_peers`).
- **Traffic with the coordinator never uses it.** The client keeps private
  relays in a list of their own that the choice of the path to the
  coordinator never reads (`internal/client/private_relays.go`), and the
  coordinator reserves only on the relays registered with the portal. Whoever
  runs a private relay therefore never sees the coordinator's address, nor
  which users are online: only the nodes that reserved on it and the buyers
  that connect to them.
- **The platform's own audit connections never use it.** The auditor's
  identity dials only circuits of registered relays (§2.4). Nodes behind a
  private relay are audited all the same, by the jobs that buyers' clients
  carry: a carrier dials whatever addresses the node offers, as it does for
  its own requests.

Nothing registers a private relay: the portal and the coordinator do not know
it exists, apart from its circuit addresses in the node's offers. While one
of its private relays holds a reservation, a node holds none on the network's
relays (unless `share.keep_official`); when none does for half a minute, it
falls back to them.

### 2.9 Direct tunnels

With `sharetoken tailnet on` a buyer and a seller that both turned it on may
be connected by a WireGuard tunnel (Tailscale's data plane, as the library
tailcat) in place of a relay ([architecture.md](architecture.md) §2.17).

- **Nothing rests on the tunnel.** Inside it runs the same libp2p connection
  as anywhere, over QUIC or TCP: the peer proves its identity in the handshake, and prompts,
  tickets and receipts are protected as before. The tunnel's keys are in the
  node's offer, which every buyer of the node gets: they keep nobody out,
  and are not meant to. Whoever has the address reaches one thing, the node's
  libp2p listener inside the tunnel, the same one its TCP and QUIC ports
  offer; the tunnel leads to no other service and to no network of the
  machine.
- **A DERP relay sees what a relay sees**: the two addresses, how much they
  exchange and when; not the content. The default DERP relays are tailcat's
  public ones, run by Tailscale and not by this network: a deployment that
  minds sets `tailnet.derp_map` to relays of its own.
- **A seller cannot choose where a buyer connects.** The buyer looks the
  address's DERP region up in its own map and refuses a region it does not
  have.
- **Directly connected peers see each other's IP address**, as they do after
  a successful hole punch. A user who wants only relays to see theirs leaves
  the tailnet off (and sets `nat` to false).

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
| Filling the coordinator's memory, or buyers' dial queues, with offers | An offer lists at most 128 models and 64 addresses, and only addresses of TCP, QUIC, a relay circuit or a tunnel; a buyer dials no other kind. The frame that carries an offer is read up to 256 KiB. An account shares from at most 16 nodes at a time and binds at most 100 devices. Peers that are no user's device get no model list and cannot fill the coordinator's tables. | `internal/protocol/limits.go`, `internal/coordinator`, `internal/accounts` |
| A seller pricing itself high to collect the platform's audit payments | An audit job pays at most three times the model's median price (the median over accounts of each one's cheapest offer); a node priced above that is not audited. All audit jobs of a day together reserve at most `audit.daily_budget_micros`. | `internal/coordinator/auditcap.go` |
| One peer holding a relay's circuits | A peer holds at most 32 circuits at a time and all peers of one address 128, so that the slots towards the coordinator cannot be taken from one place. | `internal/relay/circuits.go` |
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
  audit from a real job could behave only when audited. A carried question
  comes from a real buyer's device over that buyer's usual path, direct or
  relayed, but it is still a short chat with greedy decoding, and a seller
  could treat every such request honestly and cheat on long ones. A client
  that carries learns that the seller was audited at that moment (not the
  question, nor the verdict); a seller that also runs buyers' clients could
  use that, and a client can refuse or lose what it carries, which costs the
  platform the job but never the seller its record. The seller sees the
  carrying device's peer ID and address, as for any job that device buys.
  A question that no client could carry comes from a fresh identity through
  an official relay, which a seller can tell from its regular buyers. A token-level check
  shows that answers behave like the reference model's; it is not a
  cryptographic proof. Only models whose reference reports log-probabilities
  can be checked: open-weight models on a self-hosted server, DeepSeek, and
  OpenAI's non-reasoning models. For the others nothing proves which model
  produced the text.
- **Without a reference, usage checks are loose.** The fallback token counter is
  a heuristic and catches only gross over-billing.
- **Attestation is only as good as the image.** The allow-listed measurement must
  belong to a reproducible build that keeps keys in memory and serves a pinned
  model. A CPU TEE does not cover GPU memory. Side channels and vendor firmware
  bugs are out of scope.
- **Relayed connections are not admission-controlled.** Anyone can open a circuit
  to a reserved peer. The peer authenticates and rejects strangers, but the
  relay's bandwidth is used. A relay limits how many circuits one peer and one
  address hold at a time, not who they are; someone with many addresses is
  bounded only by the aggregate limits. A circuit has no idle timeout: it
  lasts until one end closes it.
- **The audit price cap follows the market.** Where one seller is the only one
  offering a model, or a majority of accounts collude, the median is theirs;
  the daily budget is then the bound. A node that prices itself above the cap
  is not audited at all, and buyers see its price.
- **A seller's own relay sees its buyers' addresses.** A buyer that reaches a
  node through the seller's private relay shows that relay its network
  address, the times and the byte counts: what a direct connection shows the
  seller anyway, now also when the buyer could not connect directly. It sees
  no content.
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
