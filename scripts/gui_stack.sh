#!/usr/bin/env bash
# Runs a complete private network on this machine for testing the desktop app,
# on ports that do not clash with a test environment: a relay, an HTTPS portal
# with a self-signed certificate, a coordinator, a seller with a mock model,
# and the desktop daemon. Everything lives in a temp directory.
#
#   scripts/gui_stack.sh start DIR     # prints the variables the tests need to DIR/env
#   scripts/gui_stack.sh stop  DIR
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
CMD=${1:?start or stop}; DIR=${2:?directory}
# $GUI_STACK_OFFSET shifts the ports and names the database apart, so two of these
# can run at once; the UI tests that assume the default ports need offset 0.
OFF=${GUI_STACK_OFFSET:-0}
PORTAL_PORT=$((18092+OFF)); RELAY_PORT=$((14201+OFF)); MOCK_PORT=$((18434+OFF)); SELLER_API=$((19788+OFF)); DAEMON_API=$((19787+OFF))

if [ "$CMD" = stop ]; then
  [ -f "$DIR/pids" ] && while read -r p; do kill "$p" 2>/dev/null || true; done < "$DIR/pids"
  sleep 1
  exit 0
fi

mkdir -p "$DIR"; DIR=$(cd "$DIR" && pwd); cd "$DIR"; : > pids
BIN=$DIR/bin; mkdir -p "$BIN"
(cd "$ROOT" && go build -o "$BIN/" ./cmd/...)
# The desktop daemon reports to the UI test's fake collector, never to a real one.
(cd "$ROOT" && go build -ldflags "-X sharetoken/client.TelemetryKey=phc_test_key -X sharetoken/client.TelemetryHost=http://127.0.0.1:18999" -o "$BIN/sharetoken-telemetry" ./cmd/sharetoken)
run() { local log=$1; shift; "$@" > "$log" 2>&1 & echo $! >> pids; }
wait_for() { for _ in $(seq 1 150); do grep -q "$2" "$1" 2>/dev/null && return 0; sleep 0.2; done; echo "timed out waiting for $2 in $1" >&2; tail -5 "$1" >&2; exit 1; }

export SHARETOKEN_DB_PASSWORD=
DSN=$("$ROOT/scripts/local-mysql.sh" new-db "st_gui_stack${OFF#0}")
echo "$DSN" > dsn
COORD=(-key coord.key -db "$DSN")
PASSKEY=$("$BIN/portal" -db "$DSN" pass-key)
COORD_ID=$("$BIN/coordinator" "${COORD[@]}" id)
cat > relay.yaml <<YAML
version: 1
access:
  networks:
    - name: test
      portal_keys: [$PASSKEY]
      allow_peers: [$COORD_ID]
YAML
run relay.log "$BIN/relay" -config relay.yaml -key relay.key -listen /ip4/127.0.0.1/tcp/$RELAY_PORT -status 127.0.0.1:14211
wait_for relay.log "portal relay add"
RA=$(sed -n "s/.*portal relay add '\([^']*\)'.*/\1/p" relay.log)

cat > portal.yaml <<YAML
version: 1
tls:
  mode: self-signed
  self_signed_dir: $DIR/tls
  self_signed_hosts: [127.0.0.1]
YAML
mkdir -p tls
"$BIN/portal" -config portal.yaml -db "$DSN" serve -listen 127.0.0.1:$PORTAL_PORT -open-registration -signup-credit 5000000 > portal.log 2>&1 &
PORTAL_PID=$!; echo $PORTAL_PID >> pids
wait_for portal.log "listening"
"$BIN/portal" -db "$DSN" relay add "$RA" relay-local test > /dev/null
PIN=$(openssl s_client -connect 127.0.0.1:$PORTAL_PORT </dev/null 2>/dev/null | openssl x509 -noout -fingerprint -sha256 | sed 's/.*=//; s/://g' | tr 'A-F' 'a-f')
PIN=sha256:$PIN

run coord.log "$BIN/coordinator" "${COORD[@]}" -fee-bps 800
wait_for coord.log "via relay"
PORTAL=https://127.0.0.1:$PORTAL_PORT
for _ in $(seq 1 100); do curl -sk $PORTAL/api/v1/network | grep -q '"ok":true' && break; sleep 0.2; done

run mock.log python3 "$ROOT/scripts/mock_upstream.py" $MOCK_PORT
for _ in $(seq 1 50); do curl -s http://127.0.0.1:$MOCK_PORT/v1/models > /dev/null && break; sleep 0.2; done

ST="$BIN/sharetoken -home $DIR/seller"
SHARETOKEN_PORTAL=$PORTAL SHARETOKEN_PASSWORD=seller-password $ST register -user seller -device seller-box -portal-pin "$PIN" > /dev/null
$ST share upstream http://127.0.0.1:$MOCK_PORT/v1 > /dev/null
$ST share add mock-fast -id mock-fast -in 100000 -out 200000 > /dev/null
$ST share on > /dev/null
run seller.log $ST up -http 127.0.0.1:$SELLER_API -listen /ip4/127.0.0.1/tcp/0 -nat=false
for _ in $(seq 1 100); do curl -s http://127.0.0.1:$SELLER_API/api/v1/status | grep -q '"connected": true' && break; sleep 0.2; done

run daemon.log env SHARETOKEN_PORTAL="$PORTAL" "$BIN/sharetoken-telemetry" -home "$DIR/desktop" daemon -http 127.0.0.1:$DAEMON_API -listen /ip4/127.0.0.1/tcp/0 -nat=false
for _ in $(seq 1 50); do curl -s http://127.0.0.1:$DAEMON_API/api/v1/session > /dev/null && break; sleep 0.2; done

cat > env <<ENV
PORTAL=$PORTAL
PIN=$PIN
DAEMON=http://127.0.0.1:$DAEMON_API
PORTAL_PID=$PORTAL_PID
DESKTOP_HOME=$DIR/desktop
ENV
cat env
