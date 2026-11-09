# Install the command-line client

The command-line edition is a single executable. It has no other dependencies and does not need a window system.

## System requirements {#requirements}

::: macos
macOS 12 or later, Apple Silicon or Intel.
:::

::: linux
Any recent 64-bit distribution, x86-64 or ARM64. The program is statically linked and does not depend on the glibc version, so it runs on servers and in containers.
:::

::: windows
Windows 10 or 11, x64 or ARM64. All commands below run in PowerShell.
:::

## Step 1: Check the CPU architecture {#arch}

::: macos
```bash
uname -m
```

`arm64` means Apple Silicon (M series). `x86_64` means Intel.
:::

::: linux
```bash
uname -m
```

`x86_64` means x86-64 (amd64). `aarch64` means ARM64 (arm64).
:::

::: windows
```powershell
$env:PROCESSOR_ARCHITECTURE
```

`AMD64` means x64. `ARM64` means ARM64.
:::

## Step 2: Download {#download}

::: downloads
:::

You can also download from the command line. Replace `ARCH` with `amd64` or `arm64` from the table above:

::: macos
```bash
ARCH=arm64
curl -fLO {{origin}}/downloads/sharetoken-cli-{{env}}-{{version}}-macos-$ARCH.tar.gz
```
:::

::: linux
```bash
ARCH=amd64
curl -fLO {{origin}}/downloads/sharetoken-cli-{{env}}-{{version}}-linux-$ARCH.tar.gz
```
:::

::: windows
```powershell
$arch = 'amd64'
$name = "sharetoken-cli-{{env}}-{{version}}-windows-$arch"
curl.exe -fLO "{{origin}}/downloads/$name.zip"
```
:::

## Step 3: Verify (recommended) {#verify}

Confirm that the downloaded file is not damaged or replaced. The checksums come from this website over HTTPS.

::: macos
```bash
curl -fLO {{origin}}/downloads/SHA256SUMS-{{version}}
shasum -a 256 -c --ignore-missing SHA256SUMS-{{version}}
```

It should print `OK`.
:::

::: linux
```bash
curl -fLO {{origin}}/downloads/SHA256SUMS-{{version}}
sha256sum -c --ignore-missing SHA256SUMS-{{version}}
```

It should print `OK`.
:::

::: windows
```powershell
curl.exe -fLO "{{origin}}/downloads/SHA256SUMS-{{version}}"
$expected = (Select-String -Path "SHA256SUMS-{{version}}" -SimpleMatch "$name.zip").Line.Split(' ')[0]
(Get-FileHash "$name.zip" -Algorithm SHA256).Hash -ieq $expected
```

It should print `True`.
:::

## Step 4: Unpack and add to PATH {#path}

::: macos
```bash
tar -xzf sharetoken-cli-{{env}}-{{version}}-macos-$ARCH.tar.gz
mkdir -p ~/.local/bin
mv sharetoken-cli-{{env}}-{{version}}-macos-$ARCH/{{exe}} ~/.local/bin/
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.zshrc
source ~/.zshrc
```

> [!NOTE]
> Files downloaded with a browser carry a quarantine flag, so the first run may say the developer cannot be verified. The program is not yet notarized by Apple. After you check the checksum, you can remove the flag:
>
> `xattr -d com.apple.quarantine ~/.local/bin/{{exe}}`
>
> Files downloaded with curl do not have this flag.
:::

::: linux
```bash
tar -xzf sharetoken-cli-{{env}}-{{version}}-linux-$ARCH.tar.gz
mkdir -p ~/.local/bin
mv sharetoken-cli-{{env}}-{{version}}-linux-$ARCH/{{exe}} ~/.local/bin/
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.bashrc
source ~/.bashrc
```

If you use zsh, replace `~/.bashrc` with `~/.zshrc`. To install for all users:

```bash
sudo install -m 755 sharetoken-cli-{{env}}-{{version}}-linux-$ARCH/{{exe}} /usr/local/bin/
```
:::

::: windows
```powershell
Expand-Archive "$name.zip" -DestinationPath . -Force
$dir = "$HOME\AppData\Local\Programs\sharetoken"
New-Item -ItemType Directory -Force $dir | Out-Null
Move-Item -Force "$name\{{exe}}.exe" $dir
Unblock-File "$dir\{{exe}}.exe"
[Environment]::SetEnvironmentVariable('Path', [Environment]::GetEnvironmentVariable('Path', 'User') + ";$dir", 'User')
$env:Path += ";$dir"
```

A newly opened PowerShell window picks up the new PATH. The program is not yet code-signed, and `Unblock-File` removes the download flag. If you double-click it in Explorer, SmartScreen may block it, so always run it from a terminal.
:::

## Step 5: Check {#check}

```bash
{{exe}} version
```

The output shows the version, the network (`env {{env}}`), the portal address, the state directory and the local API address (`127.0.0.1:{{port}}`). This command does not connect to the network and does not write any file. Next, [register an account](/docs/account#register).

## Build types {#build-type}

Both the package file name and the program name include the network name. `prod` connects to the production network `https://sharetoken.app`, and `dev` connects to the test network `https://dev.sharetoken.app`. They use different programs, state directories and ports, so you can install both on one machine. Accounts and credit are separate between them.
