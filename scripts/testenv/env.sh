# Shared settings for the distributed test environment (docs/test-env.md).
# Source it: . scripts/testenv/env.sh
REPO=${REPO:-$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)}
W=${ST_TESTENV_WORK:-$HOME/st-test}          # Mac work dir: alice's home, builds, results
Q="-o LogLevel=ERROR -o ConnectTimeout=10"
ARMBIAN=192.168.6.91                         # retired 2026-11-08: portal+coordinator moved to the orb VM st-db
STDB=st-db                                   # orb VM (4 CPU / 8 GB): MySQL 8 + portal + coordinator
DB_DSN=mysql://sharetoken@127.0.0.1:3306/sharetoken   # password in ~/st-test/db.env on st-db
US=165.227.115.27;  US6=2604:a880:800:14:0:3:d0f4:b000
SG=201.79.122.100;  SG6=2400:6180:0:d2:0:3:50d5:3000
CN=119.29.250.173
ORB=ada-dev; IBOX=srv-01
PASS_KEY=ed25519:0c8928fec72a6b1502eb83e8104cfdc56d740c54d040e18a7eed05edaaeb8dc3
COORD_ID=12D3KooWFAqCYkUS2Kt2KRWQ1mWF82Em4mRbB5nD4V7Fr5PmWfUh
PORTAL_PIN=sha256:0e7c55a96cbe62e3f2f68e1f8ec9c34e8fa77c14e58f0ac7caf4dfc5fd59a717
# Run in ~/st-test on st-db; DBENV loads SHARETOKEN_DB_PASSWORD.
DBENV="set -a; . ./db.env; set +a;"
COORD_CMD="./coordinator -db $DB_DSN -key coordinator.key -fee-bps 500 -debug 127.0.0.1:6060 -reference id=gemma4:e4b,upstream=gemma4:e4b,url=http://host.orb.internal:11434/v1"
PORTAL_CMD="./portal -db $DB_DSN serve -listen :8092 -open-registration -signup-credit 5000000"
# Remote users: host:user
REMOTE_USERS="$US:uma $SG:sam $CN:carol"
mkdir -p "$W/build/arm64" "$W/build/amd64" "$W/results"

rsh() { timeout "${T:-60}" ssh $Q -n s0nnet@"$1" "$2"; }   # rsh host 'cmd'
orbsh() { orb run -m $ORB bash -c "$1"; }
stdbsh() { timeout "${T:-60}" orb run -m $STDB bash -c "$1"; }
coord_start()  { stdbsh "cd ~/st-test && $DBENV setsid nohup $COORD_CMD >> coord.log 2>&1 < /dev/null & sleep 1"; }
portal_start() { stdbsh "cd ~/st-test && $DBENV setsid nohup $PORTAL_CMD >> portal.log 2>&1 < /dev/null & sleep 1"; }
iboxsh() { printf '%s\n' "$1" | orb run -m $ORB bash -c "timeout ${T:-120} instabox ssh $IBOX -- bash -s"; }  # script on stdin: any quoting works
relay_start() { # host v4 [v6]
  local ann="/ip4/$1/tcp/4001,/ip4/$1/udp/4001/quic-v1" listen=""
  [ -n "${2:-}" ] && ann="$ann,/ip6/$2/tcp/4001,/ip6/$2/udp/4001/quic-v1"
  [ "$1" = "$CN" ] && { ann="/ip4/$CN/tcp/8091"; listen="-listen /ip4/0.0.0.0/tcp/8091"; }
  rsh "$1" "cd ~/st-test && setsid nohup ./relay -key relay.key $listen -announce $ann -status 127.0.0.1:8093 -portal-key $PASS_KEY -allow-peer $COORD_ID >> relay.log 2>&1 < /dev/null & sleep 1"
}
