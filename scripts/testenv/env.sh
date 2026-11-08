# Shared settings for the distributed test environment (docs/test-env.md).
# Source it: . scripts/testenv/env.sh
REPO=${REPO:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}
W=${ST_TESTENV_WORK:-$HOME/st-test}          # Mac work dir: alice's home, builds, results
Q="-o LogLevel=ERROR -o ConnectTimeout=10"
ARMBIAN=192.168.6.91                         # PRODUCTION portal+coordinator+MySQL (docs/prod-env.md, scripts/prod/deploy.sh)
STDB=st-db                                   # orb VM (4 CPU / 8 GB): load-test only; its portal/coordinator must stay stopped while production runs (same coordinator key)
DB_DSN=mysql://sharetoken@127.0.0.1:3306/sharetoken   # password in ~/st-test/db.env on st-db
US=165.227.115.27;  US6=2604:a880:800:14:0:3:d0f4:b000
SG=201.79.122.100;  SG6=2400:6180:0:d2:0:3:50d5:3000
CN=119.29.250.173
CN2=101.43.2.87; CN2_USER=orcatermuser       # shared Tencent box: QQ holds 127.0.0.1:4001, so the relay uses 4002
# Relay host names (docs/prod-env.md); relays announce /dns4 + /dns6 of these
US_DNS=relay-us.sharetoken.app; SG_DNS=relay-sg.sharetoken.app; CN_DNS=relay-cn.sharetoken.app; CN2_DNS=relay-cn2.sharetoken.app
CONF=$REPO/scripts/testenv/conf               # coordinator.yaml, portal.yaml of st-db (relays: scripts/relay/)
ORB=ada-dev; IBOX=srv-01
PROD_COORD_ID=12D3KooWFAqCYkUS2Kt2KRWQ1mWF82Em4mRbB5nD4V7Fr5PmWfUh   # st-db must never run with this identity
PORTAL_PIN=sha256:0e7c55a96cbe62e3f2f68e1f8ec9c34e8fa77c14e58f0ac7caf4dfc5fd59a717
# Run in ~/st-test on st-db. Settings live in coordinator.yaml / portal.yaml (copied from $CONF);
# the DB password is in db.pass (0600, database.password_file). DBENV still exports it for
# admin commands run with -db $DB_DSN.
DBENV="set -a; . ./db.env; set +a;"
COORD_CMD="./coordinator -config coordinator.yaml"
PORTAL_CMD="./portal -config portal.yaml serve"
# Remote users: host:user
REMOTE_USERS="$US:uma $SG:sam $CN:carol"
mkdir -p "$W/build/arm64" "$W/build/amd64" "$W/results"

login() { [ "$1" = "$CN2" ] && echo "$CN2_USER" || echo s0nnet; }
rsh() { timeout "${T:-60}" ssh $Q -n "$(login "$1")@$1" "$2"; }   # rsh host 'cmd'
orbsh() { orb run -m $ORB bash -c "$1"; }
stdbsh() { timeout "${T:-60}" orb run -m $STDB bash -c "$1"; }
coord_guard()  { # a coordinator on st-db with production's key would fight production's for the relays
  [ "$(stdbsh "cd ~/st-test && $COORD_CMD id" 2>/dev/null)" != "$PROD_COORD_ID" ] || { echo "refused: st-db still holds production's coordinator key (docs/dev-env.md)" >&2; return 1; }
}
coord_start()  { coord_guard || return 1; stdbsh "cd ~/st-test && $DBENV setsid nohup $COORD_CMD >> coord.log 2>&1 < /dev/null & sleep 1"; }
portal_start() { stdbsh "cd ~/st-test && $DBENV setsid nohup $PORTAL_CMD >> portal.log 2>&1 < /dev/null & sleep 1"; }
iboxsh() { printf '%s\n' "$1" | orb run -m $ORB bash -c "timeout ${T:-120} instabox ssh $IBOX -- bash -s"; }  # script on stdin: any quoting works
# Lab relays only: they trust the dev network alone and production never lists them.
# Production's relays are not operated from here (scripts/relay/deploy.sh).
lab_relay_kill()  { rsh "$CN2" 'pkill -x relay'; }
lab_relay_start() { rsh "$CN2" "cd ~/st-test && setsid nohup ./relay -config relay.yaml >> relay.log 2>&1 < /dev/null & sleep 1"; }
push_stdb_conf() { stdbsh "cp $CONF/coordinator.yaml $CONF/portal.yaml ~/st-test/"; }
