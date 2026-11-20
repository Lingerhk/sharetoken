# Run your own relay

A relay forwards encrypted connections to devices that cannot be reached directly. The network runs relays for everyone. You can also run one yourself, a **private relay**: the devices you share from are then reached through your server instead of the network's relays.

Reasons to run one: your buyers are close to your server and far from the network's relays, you want the traffic of your sharing to stay on your own machine, or you share within a team that already has a server.

## How it works {#how}

- A private relay serves only the accounts you list. Devices of other accounts cannot reserve on it.
- Only sharing uses it. A device that shares holds its place on your relay, and buyers reach it there (or directly, when they can). While your relay works, the device holds no place on the network's relays. If your relay goes down, the device moves to the network's relays after about half a minute and comes back when yours does.
- Everything else stays where it was. Signing in, the account, matching and billing never pass through a private relay, and nobody registers it anywhere: it exists only in your own configuration.
- It sees no prompts and no answers. Traffic through a relay is encrypted between buyer and seller. Like any relay it sees who connects to whom: the addresses of the buyers of your devices.
- The platform's spot checks do not run through it from the platform's side. Sellers behind a private relay are still checked, by requests that ordinary buyers' clients carry.

> [!NOTE]
> Buying needs no private relay and no setting. A buyer connects to whatever addresses the seller offers.

## What you need {#requirements}

A Linux server (x86-64 or ARM64) with a public address and port 4001 open for TCP and UDP. A small one is enough: a relay only copies encrypted bytes.

You also need your account ID, on the machine where the client is signed in:

```bash
{{exe}} account
```

The first line reads `user <name> (user:3)`. The part in parentheses is the account ID.

## Step 1: Download {#download}

On the server. `uname -m` prints `x86_64` for `amd64` and `aarch64` for `arm64`:

```bash
ARCH=amd64
curl -fLO {{origin}}/downloads/sharetoken-relay-{{version}}-linux-$ARCH.tar.gz
curl -fLO {{origin}}/downloads/SHA256SUMS-{{version}}
sha256sum -c --ignore-missing SHA256SUMS-{{version}}
tar -xzf sharetoken-relay-{{version}}-linux-$ARCH.tar.gz
cd sharetoken-relay-{{version}}-linux-$ARCH
```

The package holds the program `sharetoken-relay`, a systemd unit and a README.

## Step 2: Write the configuration {#init}

Name every account whose devices may use the relay. For yourself:

```bash
./sharetoken-relay init -account user:3 -out relay.yaml
```

For a team, repeat `-account` or separate the IDs with commas:

```bash
./sharetoken-relay init -account user:3,user:7,user:12 -out relay.yaml
```

Cloud servers usually sit behind an address translation and do not know their public address. Tell the relay, with a host name or an IP address:

```bash
./sharetoken-relay init -account user:3 -out relay.yaml \
  -announce /dns4/relay.example.com/tcp/4001,/dns4/relay.example.com/udp/4001/quic-v1
```

`init` asks the portal for the key that proves an account, and writes it into `relay.yaml` together with your list. The file is short and commented. To add an account later, add it under `allow_accounts` and restart the relay.

## Step 3: Start it {#start}

```bash
./sharetoken-relay -config relay.yaml
```

It prints its address, one line that ends in the relay's ID:

```text
add it to the devices that share through it:
   sharetoken relays add '/dns4/relay.example.com/tcp/4001/p2p/12D3KooW...'
```

The file `relay.key` next to the configuration is the relay's identity. Keep it: with a new key the address changes.

To keep it running, install the systemd unit from the package:

```bash
sudo mkdir -p /opt/sharetoken-relay
sudo cp sharetoken-relay relay.yaml relay.key /opt/sharetoken-relay/
sudo useradd -r -s /usr/sbin/nologin sharetoken-relay
sudo chown -R sharetoken-relay /opt/sharetoken-relay
sudo cp sharetoken-relay.service /etc/systemd/system/
sudo systemctl enable --now sharetoken-relay
```

`journalctl -u sharetoken-relay -f` follows its log.

## Step 4: Add it to your devices {#add}

On every device that shares, with the client running:

```bash
{{exe}} relays add '/dns4/relay.example.com/tcp/4001/p2p/12D3KooW...'
```

Then check:

```bash
{{exe}} relays
```

The relay is listed as `private`. Once the device has its place there, the last column reads `sharing`, and the network's relays no longer do.

To stop using it:

```bash
{{exe}} relays remove 12D3KooW...
```

## Settings {#settings}

The list is kept in the client's [`config.json`](/docs/config#network):

```json
{
  "private_relays": ["/dns4/relay.example.com/tcp/4001/p2p/12D3KooW..."],
  "share": { "keep_official": false }
}
```

| Field | Meaning | Default |
|---|---|---|
| `private_relays` | Your own relays, at most 8 | Empty |
| `share.keep_official` | Also hold a place on the network's relays while a private relay works. Buyers then use whichever path connects first | `false` |

## When it does not work {#trouble}

| What you see | Cause and fix |
|---|---|
| `{{exe}} status` warns that the private relay does not list your account | The account of this device is not under `allow_accounts`. Add the ID that `{{exe}} account` prints and restart the relay |
| The relay is listed with an error instead of a latency | The device cannot reach it. Check the firewall (TCP and UDP 4001) and the announced address |
| The relay is reachable but never shows `sharing` | Sharing is off on this device (`{{exe}} share on`), or the relay refused the account (see the first row) |
| Buyers still arrive through the network's relays | The private relay was down for more than half a minute, or `share.keep_official` is on |

## What a private relay can and cannot see {#privacy}

Whoever runs the relay sees which devices hold a place on it, their account IDs and client versions, and the network addresses of the buyers that connect to them, with times and byte counts. It cannot read or change requests and answers, and it cannot pretend to be a seller: both ends authenticate each other.

Add only relays you trust with that. When you add someone else's private relay, its owner learns your account ID and the addresses of your buyers.
