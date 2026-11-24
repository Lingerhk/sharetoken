# Account and devices

The account holds your credit. A device is the machine that spends and earns. One account can have several devices.

## Sign in {#login}

There are no passwords. You sign in with a code sent to your email, and the same step creates the account the first time you use an address.

```bash
{{exe}} login -email you@example.com
```

1. A six-digit code is sent to your email. It is valid for 10 minutes.
2. Paste it at the prompt.
3. When you see `signed in as you@example.com`, you are done and this machine is bound to the account.

A new account gets some credit. See the [home page](/) for the amount. If the portal wants an invite code for new accounts, `login` asks for it, or you can add `-invite <code>`. You can also sign in on the [website](/app/) first and then run `login` on each machine. The [desktop app](/docs/desktop) takes the same steps in its window.

If the code does not arrive, check your spam folder first. See [Troubleshooting](/docs/troubleshooting#no-code).

Signing in binds this machine to the account. By default the sign-in lasts 60 days, and on the web portal 15 days. When it expires or is revoked, a running client stops within a minute (the desktop app goes back to its sign-in screen); run `login` again.

Use `-device` to give the device a recognizable name (the default is the host name). It appears in the device list on the portal:

```bash
{{exe}} login -email you@example.com -device office-mac
```

### Sign in from a script {#login-script}

Sending the code and redeeming it can be two separate commands, so a script never waits at a prompt:

```bash
{{exe}} login -email you@example.com -send
{{exe}} login -email you@example.com -code 123456
```

The first command only mails the code. The second signs in with it. Run both on the machine that is being bound.

### Two-factor authentication {#mfa}

You can add a code from an authenticator app (Google Authenticator, Microsoft Authenticator, 1Password and the like) to every sign-in. Turn it on under **Two-factor authentication** on the portal's [account page](/app/#/account): scan the QR code and enter the first code from the app. Turning it off needs a current code from the app.

With it on, `login` asks for the authenticator code after the emailed code. In a script, pass it as `-totp 123456`. Administrators always have it.

If you lose the phone or the app, you cannot sign in, and nobody can read its codes for you. Write to this portal's support ({{support}}) from the account's address: once they have checked it is you, an administrator turns two-factor authentication off. That also revokes all of the account's tokens and web sessions, so run `login` again on each machine. You can turn it on again on the new phone.

> [!WARNING]
> Anyone who can read your mailbox can sign in to the account. Keep your email account safe, or turn on two-factor authentication.

## Sign out {#logout}

```bash
{{exe}} logout
```

This revokes the token for this machine and removes it from the configuration file. The device key and other settings stay, so signing in again gives you the same device.

A running client stops too, so it no longer buys or sells. If it runs as a background service, also stop the service, or it keeps failing to start.

## Check balance and bills {#balance}

```bash
{{exe}} balance
```

```text
user you@example.com (user:...)
  available 4.999900 credits, on hold 0.000000
  bought  3 jobs, 60 prompt + 420 completion tokens, spent 0.000100
  sold    0 jobs, 0 prompt + 0 completion tokens, earned 0.000000 (pending 0)
  devices 1
```

- **available:** The balance you can spend.
- **on hold:** The amount held for requests in progress. Whatever is not used is returned automatically.
- **pending:** The number of sold jobs that are not settled yet.

Recent jobs:

```bash
{{exe}} jobs -n 20
{{exe}} jobs -role buy
{{exe}} jobs -role sell
```

A more detailed bill is on the portal's [Bills page](/app/#/ledger).

## Manage several devices {#devices}

- **Add a device:** Install the client on that machine and run `login`. Binding needs a signature from the device's private key, so you must do it on that machine.
- **View, rename and unbind:** Open the portal's [Devices page](/app/#/devices). After you unbind a device, the coordinator rejects it immediately.
- **Switch accounts:** A device key belongs to one account. Unbind it from the old account first, then run `login` with the new address. Or use a separate state directory. See [State directory](/docs/config#home).

## API token {#tokens}

The client gets a token automatically when it signs in, so you usually do not handle it by hand. The portal's [API Token page](/app/#/tokens) lets you view and revoke all tokens, including browser sessions, and make a new one for a script; a new token is shown only once. When you revoke a machine's token, the client running there stops within a minute.

The local endpoint does **not** use these tokens. It only listens on your machine and takes the client's own API key (`sk-st-…`, see `{{exe}} apikey`). See [Call models](/docs/use-models#endpoint).
