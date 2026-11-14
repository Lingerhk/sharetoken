#!/usr/bin/env bash
# Runs a built command-line client through a whole session against the network
# of scripts/test/gui_stack.sh: sign up with a mailed code, run, look around, buy an
# answer, stop, sign in again, log out. Everything stays in a temporary state
# directory; codes come from the stack's outbox (scripts/test/gui_stack.sh code).
#
#   scripts/test/gui_stack.sh start DIR && . DIR/env
#   STACK=DIR PORTAL=… PIN=… scripts/test/cli_test.sh <sharetoken-dev program>
#
# Only a dev build works here: a prod build ignores $SHARETOKEN_PORTAL.
set -euo pipefail
ST=$(cd "$(dirname "$1")" && pwd)/$(basename "$1")
: "${PORTAL:?}" "${PIN:?}" "${STACK:?the gui_stack.sh directory}"
PORT=${CLI_TEST_PORT:-19796}
API=http://127.0.0.1:$PORT
HOME_DIR=$(mktemp -d)
EMAIL=cli-$RANDOM$RANDOM@example.com
export SHARETOKEN_PORTAL=$PORTAL
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
DB=$(sed 's|.*/||' "$STACK/dsn")
code() { "$ROOT/scripts/test/gui_stack.sh" code "$STACK" "$EMAIL"; }
# A minute between two code requests to one address is the portal's rule; the test skips the wait.
no_cooldown() { "$ROOT/scripts/test/local-mysql.sh" sql "$DB" "UPDATE mail_codes SET created_at = created_at - 7200" > /dev/null; }
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
OUT=$(st login -email "$EMAIL" -portal-pin "$PIN" -send) || fail "login -send"
has "$OUT" "no account yet" || fail "a new address is not announced: $OUT"
[ ! -f "$HOME_DIR/config.json" ] || ! grep -q '"token": *"[^"]' "$HOME_DIR/config.json" || fail "-send signed in"
ok "login -send mails a code and says the address is new"
if [ "$(code)" != 000000 ] && st login -email "$EMAIL" -portal-pin "$PIN" -code 000000 > /dev/null 2>&1; then fail "a wrong code signed in"; fi
ok "a wrong code is refused"
OUT=$(st login -email "$EMAIL" -portal-pin "$PIN" -code "$(code)") || fail "login -code"
has "$OUT" "created" || fail "the new account is not reported: $OUT"
ok "login -code redeems it and creates the account"
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
NOKEY=$(curl -s -o /dev/null -w '%{http_code}' -m 10 "$API/v1/models")
[ "$NOKEY" = 401 ] || fail "the endpoint answered $NOKEY without the API key"
has "$(st apikey)" "required" || fail "apikey does not say the key is required"
ok "the endpoint wants the API key"
REPLY=$(curl -s -m 60 "$API/v1/chat/completions" -H "Authorization: Bearer $(st apikey -q)" -H 'Content-Type: application/json' -d '{"model":"mock-fast","messages":[{"role":"user","content":"hello"}]}')
has "$REPLY" '"choices"' || fail "inference: $REPLY"
ok "the local OpenAI endpoint answers through the network"
st apikey off > /dev/null || fail "apikey off"
[ "$(curl -s -o /dev/null -w '%{http_code}' -m 10 "$API/v1/models")" = 200 ] || fail "apikey off: the running client still wants the key"
OLD=$(st apikey -q); st apikey on > /dev/null; st apikey rotate > /dev/null
[ "$(curl -s -o /dev/null -w '%{http_code}' -m 10 -H "Authorization: Bearer $OLD" "$API/v1/models")" = 401 ] || fail "the old key still works after rotate"
[ "$(curl -s -o /dev/null -w '%{http_code}' -m 10 -H "Authorization: Bearer $(st apikey -q)" "$API/v1/models")" = 200 ] || fail "the new key does not work"
ok "apikey off, on and rotate reach the running client"
sleep 1
has "$(st jobs -role buy -n 5)" mock-fast || fail "the job is not listed"
ok "jobs lists the purchase"
stop_up
if curl -s -m 2 "$API/v1/models" > /dev/null; then fail "up is still running"; fi
ok "up stops when told to"
st logout > /dev/null || fail "logout"
if grep -q '"token": *"[^"]' "$HOME_DIR/config.json"; then fail "logout left the token"; fi
ok "logout removes the token"
no_cooldown
OLD=$(code)
# Interactive: the code is typed (here piped) once the mail is there; the pin is remembered.
( for _ in $(seq 1 100); do NEW=$(code); [ "$NEW" != "$OLD" ] && { echo "$NEW"; exit 0; }; sleep 0.2; done ) \
  | st login -email "$(echo "$EMAIL" | tr a-z A-Z)" > "$HOME_DIR/login.out" 2>&1 || { cat "$HOME_DIR/login.out"; fail "login"; }
has "$(cat "$HOME_DIR/login.out")" "signed in as $EMAIL" || fail "login: $(cat "$HOME_DIR/login.out")"
st balance > /dev/null || fail "balance after login"
ok "login"
st logout > /dev/null
echo "cli test passed"
