ShareToken relay @VERSION@

A relay forwards encrypted connections to devices that cannot be reached
directly. This package is for running one of your own (a private relay): your
sharing devices are then reached through your server instead of the network's
relays. It sees no prompts or answers, only who connects to whom.

1. Write its configuration, naming the accounts whose devices may use it
   (`sharetoken account` shows yours, for example user:3):

     ./sharetoken-relay init -account user:3 -out relay.yaml

   On a cloud host add the public address, for example
     -announce /dns4/relay.example.com/tcp/4001,/dns4/relay.example.com/udp/4001/quic-v1

2. Open port 4001 (TCP and UDP) and start it:

     ./sharetoken-relay -config relay.yaml

   It prints its address. sharetoken-relay.service runs it under systemd:
   put this directory at /opt/sharetoken-relay, then
     sudo useradd -r -s /usr/sbin/nologin sharetoken-relay
     sudo chown -R sharetoken-relay /opt/sharetoken-relay
     sudo cp sharetoken-relay.service /etc/systemd/system/
     sudo systemctl enable --now sharetoken-relay

3. On each device that shares, add the address the relay printed:

     sharetoken relays add '<address>'

The guide has the details: https://sharetoken.app/docs/relay
