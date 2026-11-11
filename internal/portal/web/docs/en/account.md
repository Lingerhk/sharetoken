# Account and devices

The account holds your credit. A device is the machine that spends and earns. One account can have several devices.

## Register {#register}

```bash
{{exe}} register -email you@example.com
```

1. Enter a password of 8 to 256 characters, twice.
2. A verification code is sent to your email. Paste it at the prompt. You can also pass it directly with `-code 123456`.
3. When you see `logged in as you@example.com`, you are done and this machine is bound to the account.

Registration grants some credit. See the [home page](/) for the amount. You can also register on the [website](/app/#register) and then run `login` on each machine.

If the code does not arrive, check your spam folder first. See [Troubleshooting](/docs/troubleshooting#no-code).

## Sign in {#login}

```bash
{{exe}} login -user you@example.com
```

Signing in binds this machine to the account. The sign-in lasts 30 days. When it expires, run `login` again.

Use `-device` to give the device a recognizable name. It appears in the device list on the portal:

```bash
{{exe}} login -user you@example.com -device office-mac
```

### Sign in from a script {#login-script}

The password can be read from an environment variable, so there is no prompt:

::: macos linux
```bash
read -rs SHARETOKEN_PASSWORD && export SHARETOKEN_PASSWORD
{{exe}} login -user you@example.com
unset SHARETOKEN_PASSWORD
```
:::

::: windows
```powershell
$env:SHARETOKEN_PASSWORD = Read-Host 'Password' -MaskInput
{{exe}} login -user you@example.com
Remove-Item Env:SHARETOKEN_PASSWORD
```

`-MaskInput` needs PowerShell 7. On Windows PowerShell 5.1, remove it. The password is then shown in plain text as you type.
:::

> [!WARNING]
> Do not write the password directly in a command line or script file. It would stay in the shell history and on disk.

## Sign out {#logout}

```bash
{{exe}} logout
```

This revokes the token for this machine and removes it from the configuration file. The device key and other settings stay, so signing in again gives you the same device.

When you sign out, also stop the running client (`Ctrl+C` or stop the background service).

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

A more detailed bill is on the portal's [ledger page](/app/#/ledger).

## Manage several devices {#devices}

- **Add a device:** Install the client on that machine and run `login`. Binding needs a signature from the device's private key, so you must do it on that machine.
- **View and unbind:** Open the portal's [devices page](/app/#/devices). After you unbind a device, the coordinator rejects it immediately.
- **Switch accounts:** A device key belongs to one account. Unbind it from the old account first, then run `login` with the new one. Or use a separate state directory. See [State directory](/docs/config#home).

## API token {#tokens}

The client gets a token automatically when it signs in, so you usually do not handle it by hand. The portal's [API tokens page](/app/#/tokens) lets you view and revoke all tokens, including browser sessions.

The local OpenAI endpoint does **not** use a token. It only listens on your machine. See [Call models](/docs/use-models#endpoint).

## Change password {#password}

Change it on the portal's [account page](/app/#/account). You can revoke your other sessions at the same time.
