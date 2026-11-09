#!/bin/bash
# (Re)create the SSH tunnels the environment needs; all start on the Mac.
. "$(dirname "$0")/env.sh"
start() { # name ssh-args...
  local name=$1; shift
  (nohup ssh -N -o ServerAliveInterval=15 -o ServerAliveCountMax=3 -o ExitOnForwardFailure=yes "$@" > "$W/tunnel-$name.log" 2>&1 &)
  echo "$name: started"
}
# The portal runs in the orb VM st-db; OrbStack forwards its :8092 to the
# Mac's 127.0.0.1:8092 (and the Mac's LAN/ZeroTier addresses: CN uses
# https://192.168.8.123:8092, bob https://host.orb.internal:8092). The
# coordinator reaches the Mac's Ollama directly (host.orb.internal:11434).
for h in $US $SG; do
  pgrep -f "127.0.0.1:8092:127.0.0.1:8092 s0nnet@$h" >/dev/null || start portal-$h -R 127.0.0.1:8092:127.0.0.1:8092 s0nnet@$h
done
# Production coordinator (docs/prod-env.md) reaches the Mac's upstream_proxy (company-procured APIs,
# used only to count tokens exactly in disputes) through a reverse tunnel onto its loopback ports.
pgrep -f "127.0.0.1:11434:127.0.0.1:11434 s0nnet@$PROD" >/dev/null || start refs-prod -R 127.0.0.1:11434:127.0.0.1:11434 -R 127.0.0.1:11436:127.0.0.1:11436 s0nnet@$PROD
# instabox -> US (restricted key; see docs/test-env.md §3)
iboxsh 'pgrep -f st_tunnel >/dev/null || (setsid nohup ssh -N -i ~/.ssh/st_tunnel -o ServerAliveInterval=15 -o ExitOnForwardFailure=yes -L 127.0.0.1:8092:127.0.0.1:8092 s0nnet@'$US' > ~/st-test/tunnel.log 2>&1 < /dev/null &); echo instabox tunnel ok'
pgrep -fl "ssh -N" | sed 's/^/  /'
