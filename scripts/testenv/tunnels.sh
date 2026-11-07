#!/bin/bash
# (Re)create the SSH tunnels the environment needs; all start on the Mac.
. "$(dirname "$0")/env.sh"
start() { # name ssh-args...
  local name=$1; shift
  (nohup ssh -N -o ServerAliveInterval=15 -o ServerAliveCountMax=3 -o ExitOnForwardFailure=yes "$@" > "$W/tunnel-$name.log" 2>&1 &)
  echo "$name: started"
}
pgrep -f "127.0.0.1:21434:127.0.0.1:11434" >/dev/null || start ollama-ref -R 127.0.0.1:21434:127.0.0.1:11434 s0nnet@$ARMBIAN
pgrep -f "127.0.0.1:18092:127.0.0.1:8092"  >/dev/null || start portal-orb -L 127.0.0.1:18092:127.0.0.1:8092 s0nnet@$ARMBIAN
for h in $US $SG; do
  pgrep -f "127.0.0.1:8092:$ARMBIAN:8092 s0nnet@$h" >/dev/null || start portal-$h -R 127.0.0.1:8092:$ARMBIAN:8092 s0nnet@$h
done
# instabox -> US (restricted key; see docs/test-env.md §3)
iboxsh 'pgrep -f st_tunnel >/dev/null || (setsid nohup ssh -N -i ~/.ssh/st_tunnel -o ServerAliveInterval=15 -o ExitOnForwardFailure=yes -L 127.0.0.1:8092:127.0.0.1:8092 s0nnet@'$US' > ~/st-test/tunnel.log 2>&1 < /dev/null &); echo instabox tunnel ok'
pgrep -fl "ssh -N" | sed 's/^/  /'
