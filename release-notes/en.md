# ShareToken 1.0.1

Share partners: share token resources with a partner; the desktop app moves to Wails v3.

## Share partners
- Once paired as share partners, each of you can use the other's token resources; when a partner's nodes are unavailable, requests switch back to the P2P network automatically
- Limit what you give: tokens per 24 hours and a share of each node's concurrency; a share of 0 pauses sharing
- "Partners' nodes only" never buys on the network; under Pairing → Share partners, or `sharetoken partner`

## Sign-in and account
- No more passwords: enter your email and the code sent to it; a new address gets an account
- Two-factor authentication can be turned on

## Desktop app
- New look: now on Wails v3, with a smaller window, a sidebar and a status bar
- The menu bar shows the relay, its latency, sharing and your available credit; tray icon on Linux (needs Ubuntu 24.04 or Debian 13 or later)
- One-click update: click "Update now" to download and install; the package's release signature is checked first. On the command line: `sharetoken upgrade`

## Security
- The local management API needs a key, and the desktop app confirms the background program's identity before connecting
- The local OpenAI-compatible endpoint needs an API key by default; manage it in Settings or with `sharetoken apikey`

## Also
- `sharetoken skill install`: teaches Claude Code and Codex to use the client
- Open source under the MIT license
