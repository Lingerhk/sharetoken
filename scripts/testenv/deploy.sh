#!/bin/bash
# Build from the working tree and roll out: coordinator (orb VM st-db, MySQL) + every user client.
# The portal is left alone unless given: deploy.sh [--portal]
# Relays are not deployed from here: production runs on them (scripts/relay/deploy.sh).
set -u
. "$(dirname "$0")/env.sh"
cd "$REPO" || exit 1
DO_PORTAL=0
for a in "$@"; do case $a in --portal) DO_PORTAL=1;; *) echo "unknown option $a" >&2; exit 2;; esac; done
coord_guard || exit 1
for c in sharetoken; do
  GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w $($REPO/scripts/ldflags.sh)" -o $W/build/arm64/$c ./cmd/$c || exit 1
done
for c in sharetoken coordinator portal; do
  GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w $($REPO/scripts/ldflags.sh)" -o $W/build/amd64/$c ./cmd/$c || exit 1
done
for c in sharetoken; do gzip -kf $W/build/amd64/$c; done
go build -ldflags "$($REPO/scripts/ldflags.sh)" -o $W/sharetoken ./cmd/sharetoken || exit 1
# CN builds from source (uploads to it are very slow); never ship docs/test_env.txt or secrets
git ls-files -co --exclude-standard | while IFS= read -r f; do [ ! -f "$f" ] || echo "$f"; done | grep -v -e '^docs/test_env.txt' -e 'secrets' -e '^reports/' -e 'upstream_proxy.json' -e '\.env$' -e '\.key$' > $W/files.txt
COPYFILE_DISABLE=1 tar czf $W/src.tgz -T $W/files.txt

# st-db (orb VM: the Mac's files are mounted, so copy straight from $W)
push_stdb_conf || exit 1
stdbsh "cd ~/st-test && pkill -x coordinator; sleep 1; cp $W/build/amd64/coordinator . && ./coordinator -config coordinator.yaml check-config > /dev/null && echo coordinator copied" || exit 1
coord_start; echo coordinator restarted
if [ $DO_PORTAL = 1 ]; then
  stdbsh "cd ~/st-test && cp portal portal.prev; pkill -x portal; sleep 1; cp $W/build/amd64/portal ." && portal_start && echo portal restarted
fi

client_restart() { # host user
  rsh $1 "cd ~/st-test && pkill -x sharetoken; sleep 1; mv sharetoken.new sharetoken; SHARETOKEN_HOME=\$HOME/st-test/$2 setsid nohup ./sharetoken up >> $2.log 2>&1 < /dev/null & sleep 1; echo $2 restarted"
}
for h in $US $SG; do
  scp $Q $W/build/amd64/sharetoken.gz s0nnet@$h:st-test/ && rsh $h 'cd ~/st-test && gunzip -c sharetoken.gz > sharetoken.new && chmod +x sharetoken.new'
done
client_restart $US uma; client_restart $SG sam
scp $Q $W/src.tgz s0nnet@$CN:sharetoken-v2/src-new.tgz && T=600 rsh $CN 'cd ~/sharetoken-v2 && rm -rf src2 && mkdir src2 && tar xzf src-new.tgz -C src2 2>/dev/null && cd src2 && export PATH=$HOME/sharetoken-sim/go/bin:$PATH GOPROXY=https://goproxy.cn GOFLAGS=-mod=mod && CGO_ENABLED=0 go build -trimpath -o ~/st-test/sharetoken.new ./cmd/sharetoken' && client_restart $CN carol
orbsh "cd ~/st-test && pkill -x sharetoken; sleep 1; cp $W/build/amd64/sharetoken . && SHARETOKEN_HOME=\$HOME/st-test/bob setsid nohup ./sharetoken up >> bob.log 2>&1 < /dev/null & sleep 1; echo bob restarted"
orb run -m $ORB bash -c "cat $W/build/amd64/sharetoken.gz | timeout 180 instabox ssh $IBOX -- 'gunzip -c > ~/st-test/sharetoken.new && chmod +x ~/st-test/sharetoken.new'"
iboxsh 'cd ~/st-test && pkill -x sharetoken; sleep 1; mv sharetoken.new sharetoken; (SHARETOKEN_HOME=$HOME/st-test/ivy setsid nohup ./sharetoken up >> ivy.log 2>&1 < /dev/null &); echo ivy restarted'
pkill -x sharetoken; sleep 1; (SHARETOKEN_HOME=$W/alice nohup $W/sharetoken up >> $W/alice.log 2>&1 &); echo alice restarted
