#!/bin/bash
# Build from the working tree and roll out: coordinator (armbian) + every user client.
# Relays and the portal are left alone unless given: deploy.sh [--relays] [--portal]
set -u
. "$(dirname "$0")/env.sh"
cd "$REPO" || exit 1
DO_RELAYS=0; DO_PORTAL=0
for a in "$@"; do case $a in --relays) DO_RELAYS=1;; --portal) DO_PORTAL=1;; esac; done
for c in coordinator sharetoken portal relay; do
  GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $W/build/arm64/$c ./cmd/$c || exit 1
done
for c in sharetoken relay; do
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $W/build/amd64/$c ./cmd/$c || exit 1
  gzip -kf $W/build/amd64/$c
done
go build -o $W/sharetoken ./cmd/sharetoken || exit 1
# CN builds from source (uploads to it are very slow); never ship docs/test_env.txt or secrets
git ls-files -co --exclude-standard | grep -v -e '^docs/test_env.txt' -e 'secrets' -e '^reports/' > $W/files.txt
COPYFILE_DISABLE=1 tar czf $W/src.tgz -T $W/files.txt

# armbian
rsh $ARMBIAN 'mkdir -p ~/st-test/new'
scp $Q $W/build/arm64/coordinator $W/build/arm64/portal s0nnet@$ARMBIAN:st-test/new/ || exit 1
rsh $ARMBIAN "cd ~/st-test && pkill -x coordinator; sleep 1; mv new/coordinator . && setsid nohup $COORD_CMD >> coord.log 2>&1 < /dev/null & sleep 1; echo coordinator restarted"
[ $DO_PORTAL = 1 ] && rsh $ARMBIAN "cd ~/st-test && cp portal portal.prev; pkill -x portal; sleep 1; mv new/portal . && setsid nohup $PORTAL_CMD >> portal.log 2>&1 < /dev/null & sleep 1; echo portal restarted"

client_restart() { # host user
  rsh $1 "cd ~/st-test && pkill -x sharetoken; sleep 1; mv sharetoken.new sharetoken; SHARETOKEN_HOME=\$HOME/st-test/$2 setsid nohup ./sharetoken up >> $2.log 2>&1 < /dev/null & sleep 1; echo $2 restarted"
}
for h in $US $SG; do
  scp $Q $W/build/amd64/sharetoken.gz $W/build/amd64/relay.gz s0nnet@$h:st-test/ && rsh $h 'cd ~/st-test && gunzip -c sharetoken.gz > sharetoken.new && chmod +x sharetoken.new && gunzip -c relay.gz > relay.new && chmod +x relay.new'
done
client_restart $US uma; client_restart $SG sam
scp $Q $W/src.tgz s0nnet@$CN:sharetoken-v2/src-new.tgz && T=600 rsh $CN 'cd ~/sharetoken-v2 && rm -rf src2 && mkdir src2 && tar xzf src-new.tgz -C src2 2>/dev/null && cd src2 && export PATH=$HOME/sharetoken-sim/go/bin:$PATH GOPROXY=https://goproxy.cn GOFLAGS=-mod=mod && CGO_ENABLED=0 go build -trimpath -o ~/st-test/sharetoken.new ./cmd/sharetoken && CGO_ENABLED=0 go build -trimpath -o ~/st-test/relay.new ./cmd/relay' && client_restart $CN carol
if [ $DO_RELAYS = 1 ]; then
  for h in "$US $US6" "$SG $SG6" "$CN"; do set -- $h; rsh $1 'cd ~/st-test && pkill -x relay; sleep 1; mv relay.new relay'; relay_start "$@"; echo "relay $1 restarted"; done
fi
orbsh "cd ~/st-test && pkill -x sharetoken; sleep 1; cp $W/build/amd64/sharetoken . && SHARETOKEN_HOME=\$HOME/st-test/bob setsid nohup ./sharetoken up >> bob.log 2>&1 < /dev/null & sleep 1; echo bob restarted"
orb run -m $ORB bash -c "cat $W/build/amd64/sharetoken.gz | timeout 180 instabox ssh $IBOX -- 'gunzip -c > ~/st-test/sharetoken.new && chmod +x ~/st-test/sharetoken.new'"
iboxsh 'cd ~/st-test && pkill -x sharetoken; sleep 1; mv sharetoken.new sharetoken; (SHARETOKEN_HOME=$HOME/st-test/ivy setsid nohup ./sharetoken up >> ivy.log 2>&1 < /dev/null &); echo ivy restarted'
pkill -x sharetoken; sleep 1; (SHARETOKEN_HOME=$W/alice nohup $W/sharetoken up >> $W/alice.log 2>&1 &); echo alice restarted
