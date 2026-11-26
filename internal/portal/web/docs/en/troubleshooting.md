# Troubleshooting

Run these two commands first. They explain most problems:

```bash
{{exe}} version
{{exe}} status
```

The client's log usually has a more specific reason: `{{exe}} logs -n 50`, see [Logs](/docs/config#logs).

## Install {#install}

### Command not found {#not-found}

The directory with the program is not in PATH, or the current terminal has not loaded the new PATH.

::: macos
```bash
ls -l ~/.local/bin/{{exe}}
echo $PATH
source ~/.zshrc
```
:::

::: linux
```bash
ls -l ~/.local/bin/{{exe}}
echo $PATH
source ~/.bashrc
```
:::

::: windows
Open a new PowerShell window, then:

```powershell
Test-Path "$HOME\AppData\Local\Programs\sharetoken\{{exe}}.exe"
$env:Path -split ';' | Select-String sharetoken
```
:::

::: macos
### "Cannot verify the developer" or "is damaged" {#gatekeeper}

The program is not yet notarized by Apple, so Gatekeeper blocks files downloaded with a browser. After you check the [checksum](/docs/install#verify):

```bash
xattr -d com.apple.quarantine ~/.local/bin/{{exe}}
```
:::

::: windows
### SmartScreen or antivirus blocks it {#smartscreen}

The program is not yet code-signed. After you check the [checksum](/docs/install#verify), remove the download flag and run it from a terminal:

```powershell
Unblock-File "$HOME\AppData\Local\Programs\sharetoken\{{exe}}.exe"
```
:::

::: linux
### Permission denied {#permission}

```bash
chmod +x ~/.local/bin/{{exe}}
```

If your home directory is mounted `noexec`, install to `/usr/local/bin` instead. See [Install](/docs/install#path).
:::

## Account {#account}

### The verification code does not arrive {#no-code}

Check your spam folder first. The code can take a few minutes, and it is valid for 10 minutes. Do not resend again and again: wait at least a minute between two, an address gets at most 5 codes an hour, and a code allows 5 wrong tries before you need a new one. If it never arrives, write to this portal's support ({{support}}) from that address: once they have checked it is you, an administrator can give you a one-time sign-in code (valid for 10 minutes) to enter on the sign-in page as usual.

### The verification code is refused {#code-refused}

A mailed code works only where it was asked for. Enter it in the same browser tab, app window or terminal that sent it: a code asked for on your phone does not sign in your computer, and neither does one asked for in another tab. A new code replaces the one before it, so use the latest mail. With `{{exe}} login -send`, run `-code` on the same machine. If the client refuses a fresh code typed where it was asked for, the client is too old for this portal: [update](/docs/update) it first.

### The message is `not logged in` {#not-logged-in}

This machine is not signed in yet, or the state directory is wrong. Run `{{exe}} version` to check the `home` line, then run `{{exe}} login`.

When the sign-in expires (after 60 days by default) or is revoked in the portal, a running client stops using it: `up` exits with `you are logged out or this login was revoked`, and the background service becomes signed out. Run `{{exe}} login` again, then restart the client.

### The message is `throttled` or HTTP 429 {#throttled}

Too many failures in a row, so you are rate limited. Wait a few minutes and try again.

### The message is `signed in, but binding this device failed` {#bind-failed}

There are two common causes:

- **The system clock is wrong.** The message contains `too old or from the future`. Fix the clock and sign in again. The commands are below.
- **This device key is already bound to another account.** Unbind it first on the old account's [devices page](/app/#/devices), or use a [separate state directory](/docs/config#home).

::: macos
```bash
sudo sntp -sS time.apple.com
```
:::

::: linux
```bash
sudo timedatectl set-ntp true
timedatectl status
```
:::

::: windows
Run this in an administrator PowerShell:

```powershell
w32tm /resync
```
:::

### The message is `upgrade_required` {#upgrade-required}

The client version is too old. [Update](/docs/update#upgrade) and try again.

## Running {#running}

### The message is `the client is not running` {#not-running}

`status`, `models`, `relays` and similar commands need the client to be running. Run `{{exe}} up` in another window, or start the [background service](/docs/running#background).

### The message is `cannot reach the portal` {#no-portal}

This machine cannot reach the portal. Check the network, proxy and firewall:

::: macos linux
```bash
curl -sS {{origin}}/healthz
```
:::

::: windows
```powershell
curl.exe -sS {{origin}}/healthz
```
:::

It should print `{"ok":true}`.

### The port is in use {#port-in-use}

The message is `local API on 127.0.0.1:{{port}}: ... address already in use`. Another program is already using port `{{port}}`, usually another running client: another `up`, the background service, or the desktop app.

::: macos
```bash
lsof -nP -iTCP:{{port}} -sTCP:LISTEN
```
:::

::: linux
```bash
ss -ltnp | grep {{port}}
```
:::

::: windows
```powershell
Get-NetTCPConnection -LocalPort {{port}} -State Listen | Select-Object OwningProcess
```
:::

Stop it, or use another port: `{{exe}} up -http 127.0.0.1:9800`.

### It shows `not connected` {#not-connected}

The client is still connecting, or cannot reach a relay. Run `{{exe}} relays -measure` to see the result for each relay. A company network may block port 4001, so ask the administrator to allow outbound TCP 4001 and UDP 4001.

## Calling models {#calling}

### `no_offers` {#no-offers}

No node offers this model right now. Run `{{exe}} models` to check the model ID, and mind the capitalization.

With `max_price_in` or `max_price_out` set in the configuration, the price limits may also have ruled out the nodes there are; when the message has `natively`, `require_native` is on, see [Configuration](/docs/config#buy). When only your own node offers the model, the message starts with `no other user's node`: you cannot buy from yourself.

If your account is paired with sellers, only they count: the message then has `of the sellers you are paired with`. Run `{{exe}} pair` to see them. Wait for one of them to come online, or leave them to buy from the whole network again. See [Buy only from sellers you know](/docs/use-models#pairing).

A seller in private mode is hidden from every buyer that is not paired with it, so a model you saw earlier can disappear from `models`.

### `capacity` {#capacity}

All nodes are full. Retry later, or raise `queue_seconds` in the configuration.

### `insufficient_funds` or `budget` {#funds}

`insufficient_funds` (HTTP 402): the balance is not enough to hold for this request. A request without `max_tokens` holds the whole budget; add a smaller `max_tokens` to the request, or check your balance with `{{exe}} balance`. See [How billing works](/docs/use-models#billing).

`budget` (HTTP 400): the per-request budget does not even pay for the prompt, and the message is `reservation does not cover the prompt; raise the budget`. Raise `budget_micros` in the configuration, or the budget under "Settings > Requests" in the desktop app, see [Configuration](/docs/config#buy).

### The message is `invalid_api_key` (HTTP 401) {#api-key}

The local endpoint wants an API key by default, and the request had none or a wrong one. `{{exe}} apikey` shows the key; enter it in the tool as its OpenAI or Anthropic API key (`Authorization: Bearer <key>` or `x-api-key`). After `{{exe}} apikey rotate` the old key stops working at once.

### The message is `request body too large` (HTTP 413) {#too-large}

A request can be at most 32 MiB. Usually the conversation carries a large file or many images: shrink or remove them, or start a new conversation.

### The `content` of the response is empty {#empty-content}

The reasoning of a thinking model also counts toward `max_tokens`, and the limit was used up. Raise `max_tokens`.

### The request fails after a long time {#stalled}

The node stopped sending anything (not even the progress frames it sends while the model is thinking), or its model produced nothing for 5 minutes, and was judged stuck. When nothing has come back yet, the client retries on another node; when the connection breaks midway, it first tries to resume with the same node. If the node or its network is merely slow, raise `first_byte_seconds` and `idle_seconds` in the configuration. See [Configuration](/docs/config#buy).

### Large requests are slow with direct tunnels on {#tunnel-slow}

Inside a tunnel the connection runs over QUIC by default. On a link that loses packets or jitters, weak Wi-Fi for example, a QUIC connection can stay slow once it has stumbled, and a request of several megabytes then takes many seconds to upload. Switch this computer to TCP: `{{exe}} tailnet on -transport tcp`, or "Transport" under Settings, Network in the desktop app (see [Direct tunnels](/docs/running#tailnet)). `{{exe}} tailnet` shows what each tunnel uses.

## Sharing models {#sharing}

### `share detect` says `cannot list models` {#detect-failed}

The model server is not running, or the address is wrong.

::: macos
```bash
brew services restart ollama
curl -s http://127.0.0.1:11434/v1/models
```
:::

::: linux
```bash
sudo systemctl restart ollama
curl -s http://127.0.0.1:11434/v1/models
```
:::

::: windows
Open Ollama from the Start menu, then:

```powershell
curl.exe -s http://127.0.0.1:11434/v1/models
```
:::

### `status` shows `offer not accepted` {#offer-rejected}

The coordinator rejected the offer, and the reason follows. Common causes are a client version that is too old, or a node paused because an audit did not match.

### No jobs come in {#no-jobs}

Run `{{exe}} models` to see the existing nodes and prices. The coordinator picks nodes by price, reputation, latency and success rate. You can try lowering your price.

If private mode is on, only the buyers paired with you can buy from you. `{{exe}} pair` shows whether it is on and who your buyers are; `{{exe}} pair private off` opens you to everyone again. See [Pair with your buyers](/docs/share#pairing).

## Getting help {#feedback}

Tell us on the portal's [feedback page](/app/#/feedback), and include the information below. Do not send `config.json` or `device.key`.

- The output of `{{exe}} version`
- The output of `{{exe}} status`
- The exact error text, and the job ID (response header `X-Sharetoken-Job`)
- The log around the error: `{{exe}} logs -n 100`. It has no prompts or answers, but it may have your e-mail address and IP addresses: read it before you send it
