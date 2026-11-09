#!/usr/bin/env bash
# Runs a built command-line client through a whole session against the network
# of scripts/gui_stack.sh: register, run, look around, buy an answer, stop, log
# in again, log out. Everything stays in a temporary state directory.
#
#   scripts/gui_stack.sh start DIR && . DIR/env
#   PORTAL=… PIN=… scripts/cli_test.sh <sharetoken-dev program>
#
# Only a dev build works here: a prod build ignores $SHARETOKEN_PORTAL.
set -euo pipefail
ST=$(cd "$(dirname "$1")" && pwd)/$(basename "$1")
: "${PORTAL:?}" "${PIN:?}"
PORT=${CLI_TEST_PORT:-19796}
API=http://127.0.0.1:$PORT
HOME_DIR=$(mktemp -d)
EMAIL=cli-$RANDOM$RANDOM@example.com
export SHARETOKEN_PORTAL=$PORTAL SHARETOKEN_PASSWORD=cli-test-password-1 SHARETOKEN_TELEMETRY=0
UP=
stop_up() { if [ -n "$UP" ]; then kill "$UP" 2>/dev/null || true; wait "$UP" 2>/dev/null || true; UP=; fi; }
trap 'stop_up; rm -rf "$HOME_DIR"' EXIT
st() { "$ST" -home "$HOME_DIR" "$@"; }
ok() { echo "  ok  $1"; }
fail() { echo "FAILED: $1"; [ -f "$HOME_DIR/up.log" ] && tail -5 "$HOME_DIR/up.log"; exit 1; }
start_up() {
  "$ST" -home "$HOME_DIR" up -http "127.0.0.1:$PORT" -listen /ip4/127.0.0.1/tcp/0 > "$HOME_DIR/up.log" 2>&1 &
  UP=$!
  for _ in $(seq 1 100); do
    case $(st status 2>/dev/null) in *": connected"*) return 0 ;; esac
    kill -0 "$UP" 2>/dev/null || fail "up exited"
    sleep 0.3
  done
  fail "up did not connect"
}

has() { case $1 in *"$2"*) return 0 ;; *) return 1 ;; esac; } # no grep -q in a pipe: pipefail sees its SIGPIPE
has "$(st version)" "env dev" || fail "not a dev build"
ok "version says what it was built for, and touched nothing: $(ls "$HOME_DIR" | wc -l | tr -d ' ') files"
st balance >/dev/null 2>&1 && fail "balance worked before logging in"
ok "commands that need an account refuse before login"
st register -email "$EMAIL" -portal-pin "$PIN" > /dev/null || fail "register"
ok "register"
grep -q "$EMAIL" "$HOME_DIR/config.json" || fail "the account is not in the config"
[ "$(uname -s)" = Linux ] && { [ "$(stat -c %a "$HOME_DIR/config.json")" = 600 ] || fail "config.json is not private"; ok "config.json is 0600"; }
BAL=$(st balance) || fail "balance"
[ -n "$BAL" ] || fail "balance printed nothing"
ok "balance"
start_up
ok "up joins the network"
for _ in $(seq 1 60); do
  if has "$(st models 2>/dev/null)" mock-fast; then break; fi
  sleep 0.5
done
has "$(st models)" mock-fast || fail "the seller's model is not listed"
ok "models lists the seller's model"
st relays >/dev/null || fail "relays"
ok "relays"
REPLY=$(curl -s -m 60 "$API/v1/chat/completions" -H 'Content-Type: application/json' -d '{"model":"mock-fast","messages":[{"role":"user","content":"hello"}]}')
has "$REPLY" '"choices"' || fail "inference: $REPLY"
ok "the local OpenAI endpoint answers through the network"
sleep 1
has "$(st jobs -role buy -n 5)" mock-fast || fail "the job is not listed"
ok "jobs lists the purchase"
stop_up
if curl -s -m 2 "$API/v1/models" > /dev/null; then fail "up is still running"; fi
ok "up stops when told to"
st logout > /dev/null || fail "logout"
if grep -q '"token": *"[^"]' "$HOME_DIR/config.json"; then fail "logout left the token"; fi
ok "logout removes the token"
st login -user "$EMAIL" -portal-pin "$PIN" > /dev/null || fail "login"
st balance > /dev/null || fail "balance after login"
ok "login"
st logout > /dev/null
echo "cli test passed"
