# Quick start

This page takes you step by step from the download to your first model call. First choose your system at the top of the page.

> [!TIP]
> This page uses the command-line edition. For a graphical interface, install the [desktop app](/docs/desktop).

## 1. Download and install {#download}

::: macos linux
The commands below detect the CPU architecture and install the program to `~/.local/bin`. No sudo needed.
:::

::: macos
```bash
ARCH=$(uname -m); [ "$ARCH" = x86_64 ] && ARCH=amd64
curl -fLO {{origin}}/downloads/sharetoken-cli-{{version}}-macos-$ARCH.tar.gz
tar -xzf sharetoken-cli-{{version}}-macos-$ARCH.tar.gz
mkdir -p ~/.local/bin
mv sharetoken-cli-{{version}}-macos-$ARCH/{{exe}} ~/.local/bin/
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.zshrc
source ~/.zshrc
```
:::

::: linux
```bash
ARCH=$(uname -m); case $ARCH in x86_64) ARCH=amd64 ;; aarch64) ARCH=arm64 ;; esac
curl -fLO {{origin}}/downloads/sharetoken-cli-{{version}}-linux-$ARCH.tar.gz
tar -xzf sharetoken-cli-{{version}}-linux-$ARCH.tar.gz
mkdir -p ~/.local/bin
mv sharetoken-cli-{{version}}-linux-$ARCH/{{exe}} ~/.local/bin/
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.bashrc
source ~/.bashrc
```
:::

::: windows
Run this in PowerShell. The program goes into your own directory, so you do not need administrator rights.

```powershell
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$name = "sharetoken-cli-{{version}}-windows-$arch"
curl.exe -fLO "{{origin}}/downloads/$name.zip"
Expand-Archive "$name.zip" -DestinationPath . -Force
$dir = "$HOME\AppData\Local\Programs\sharetoken"
New-Item -ItemType Directory -Force $dir | Out-Null
Move-Item -Force "$name\{{exe}}.exe" $dir
Unblock-File "$dir\{{exe}}.exe"
[Environment]::SetEnvironmentVariable('Path', [Environment]::GetEnvironmentVariable('Path', 'User') + ";$dir", 'User')
$env:Path += ";$dir"
```
:::

Check that the install worked:

```bash
{{exe}} version
```

If you see the version, the portal address and the local API address, it worked. To pick a package or checksum file by hand, see [Install the command-line client](/docs/install).

## 2. Sign in {#register}

```bash
{{exe}} login -email you@example.com
```

There is no password: enter the six-digit code sent to your email. The first sign-in with an address creates the account. When it finishes, this machine is bound to your account. For two-factor authentication, invite codes and the like, see [Account and devices](/docs/account#login).

## 3. Start the client {#up}

```bash
{{exe}} up
```

When you see the line below, you are connected to the network. Keep this window running. Press `Ctrl+C` to stop.

```text
   OpenAI-compatible endpoint: http://127.0.0.1:{{port}}/v1
```

## 4. Call a model {#call}

Open **another** terminal window and list the models on the network:

```bash
{{exe}} models
```

The local endpoint wants an API key by default. The key starts with `sk-st-` and stays on your machine; `{{exe}} apikey -q` prints it. First put it in an environment variable:

::: macos linux
```bash
export OPENAI_API_KEY=$({{exe}} apikey -q)
```
:::

::: windows
```powershell
$env:OPENAI_API_KEY = ({{exe}} apikey -q)
```
:::

Then replace `MODEL` below with a model ID that `models` listed:

::: macos linux
```bash
curl http://127.0.0.1:{{port}}/v1/chat/completions \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"MODEL","messages":[{"role":"user","content":"Hello"}],"max_tokens":200}'
```
:::

::: windows
```powershell
$body = @{ model = 'MODEL'; max_tokens = 200; messages = @(@{ role = 'user'; content = 'Hello' }) } | ConvertTo-Json -Depth 5
Invoke-RestMethod http://127.0.0.1:{{port}}/v1/chat/completions -Method Post -Headers @{ Authorization = "Bearer $env:OPENAI_API_KEY" } -ContentType 'application/json' -Body $body | ConvertTo-Json -Depth 8
```
:::

Finally, see how much it cost:

```bash
{{exe}} balance
```

> [!TIP]
> If `models` lists nothing or the call returns `no_offers`, no node is online offering models right now. You can also share your own models. See [Share a model](/docs/share).

## Next steps {#next}

::: cards
[Call models](/docs/use-models) Connect Python, Node.js, Claude Code, Codex and OpenAI-compatible tools.
[Keep your Claude or ChatGPT subscription](/docs/use-models#subscription) Claude Code and Codex keep their own login and add the network's models.
[Share a model](/docs/share) Share a local model, a vendor API key or a login and earn credit.
[Run the client](/docs/running) Keep the client running in the background.
[Use with coding agents](/docs/agents) Let Claude Code or Codex run the client for you.
:::
