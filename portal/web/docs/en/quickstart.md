# Quick start

From download to your first model call takes about 5 minutes. First choose your system at the top of the page.

## 1. Download and install {#download}

::: macos linux
The commands below detect the CPU architecture and install the program to `~/.local/bin`. No sudo needed.
:::

::: macos
```bash
ARCH=$(uname -m); [ "$ARCH" = x86_64 ] && ARCH=amd64
curl -fLO {{origin}}/downloads/sharetoken-cli-{{env}}-{{version}}-macos-$ARCH.tar.gz
tar -xzf sharetoken-cli-{{env}}-{{version}}-macos-$ARCH.tar.gz
mkdir -p ~/.local/bin
mv sharetoken-cli-{{env}}-{{version}}-macos-$ARCH/{{exe}} ~/.local/bin/
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.zshrc
source ~/.zshrc
```
:::

::: linux
```bash
ARCH=$(uname -m); case $ARCH in x86_64) ARCH=amd64 ;; aarch64) ARCH=arm64 ;; esac
curl -fLO {{origin}}/downloads/sharetoken-cli-{{env}}-{{version}}-linux-$ARCH.tar.gz
tar -xzf sharetoken-cli-{{env}}-{{version}}-linux-$ARCH.tar.gz
mkdir -p ~/.local/bin
mv sharetoken-cli-{{env}}-{{version}}-linux-$ARCH/{{exe}} ~/.local/bin/
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.bashrc
source ~/.bashrc
```
:::

::: windows
Run this in PowerShell. The program goes into your own directory, so you do not need administrator rights.

```powershell
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$name = "sharetoken-cli-{{env}}-{{version}}-windows-$arch"
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

## 2. Register an account {#register}

```bash
{{exe}} register -email you@example.com
```

Enter a password twice when prompted (at least 8 characters), then the verification code sent to your email. When it finishes, this machine is bound to your account.

If you already have an account, sign in instead:

```bash
{{exe}} login -user you@example.com
```

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

Replace `MODEL` below with a model ID from that list:

::: macos linux
```bash
curl http://127.0.0.1:{{port}}/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"MODEL","messages":[{"role":"user","content":"Hello"}],"max_tokens":200}'
```
:::

::: windows
```powershell
$body = @{ model = 'MODEL'; max_tokens = 200; messages = @(@{ role = 'user'; content = 'Hello' }) } | ConvertTo-Json -Depth 5
Invoke-RestMethod http://127.0.0.1:{{port}}/v1/chat/completions -Method Post -ContentType 'application/json' -Body $body | ConvertTo-Json -Depth 8
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
[Call models](/docs/use-models) Connect Python, Node.js and OpenAI-compatible tools.
[Share a model](/docs/share) Share a local model and earn credit.
[Run the client](/docs/running) Keep the client running in the background.
:::
