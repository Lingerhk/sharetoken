#!/usr/bin/env bash
# End-to-end test of the client's log and crash reports on a real system,
# against the portal of scripts/test/gui_stack.sh: a daemon built with
# -tags diagtest crashes on request (internal/client/diag_testcrash.go), and
# the reports must reach the portal's database, redacted, after a restart.
#
#   scripts/test/gui_stack.sh start DIR
#   scripts/test/diag_e2e.sh DIR local                 # this machine
#   scripts/test/diag_e2e.sh DIR ssh:pve-linux linux   # a Linux machine over SSH
#   scripts/test/diag_e2e.sh DIR ssh:pve-win windows   # a Windows machine (OpenSSH, PowerShell)
#
# A remote machine reaches the stack's portal through a reverse SSH tunnel on
# its own 127.0.0.1:18092, which the portal's certificate is for.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/../.." && pwd)
STACK=${1:?gui_stack directory}; WHERE=${2:?local or ssh:HOST}; OS=${3:-$(go env GOOS)}
eval "$(sed "s/^/export /" "$STACK/env")"
DB=$(sed 's|.*/||' "$STACK/dsn")
TAG=$(echo "$WHERE" | tr -c 'a-z0-9\n' '-' | tr -d '\n')
EMAIL="diag-$TAG-$(date +%s)@example.test"
OUT=$STACK/diag-e2e-$TAG; rm -rf "$OUT"; mkdir -p "$OUT"
pass=0
ok() { pass=$((pass + 1)); echo "  ok  $*"; }
fail() { echo "FAILED: $*" >&2; exit 1; }
sql() { "$ROOT/scripts/test/local-mysql.sh" sql "$DB" "$1"; }

ARCH=$(go env GOARCH); [ "$WHERE" = local ] || ARCH=amd64
EXE=sharetoken-diagtest; [ "$OS" = windows ] && EXE=$EXE.exe
(cd "$ROOT" && GOOS=$OS GOARCH=$ARCH go build -tags diagtest -ldflags "-X sharetoken/internal/version.Version=1.0.6-dev.diagtest" -o "$OUT/$EXE" ./cmd/sharetoken)

# rsh runs a shell command where the client runs; $H is its state directory.
if [ "$WHERE" = local ]; then
  H=$OUT/home; RBIN=$OUT/$EXE
  rsh() { bash -c "$1"; }
else
  HOST=${WHERE#ssh:}; CTL=$(mktemp -u /tmp/stdiag.XXXX)
  ssh -O exit "$HOST" 2>/dev/null || true
  ssh -f -N -o ExitOnForwardFailure=yes -o ControlMaster=yes -o ControlPath="$CTL" -R 18092:127.0.0.1:18092 "$HOST"
  trap 'ssh -o ControlPath="$CTL" -O exit "$HOST" 2>/dev/null || true' EXIT
  if [ "$OS" = windows ]; then
    H='$HOME\st-diag'; RBIN='$HOME\st-diag-bin\'$EXE
    ssh "$HOST" 'Remove-Item -Recurse -Force $HOME\st-diag, $HOME\st-diag-bin -ErrorAction SilentlyContinue; New-Item -ItemType Directory $HOME\st-diag-bin | Out-Null'
    scp -q "$OUT/$EXE" "$HOST:st-diag-bin/$EXE"
    rsh() { ssh "$HOST" "$1"; }
  else
    H='$HOME/st-diag'; RBIN='$HOME/st-diag-bin/'$EXE
    ssh "$HOST" 'rm -rf ~/st-diag ~/st-diag-bin; mkdir -p ~/st-diag-bin'
    scp -q "$OUT/$EXE" "$HOST:st-diag-bin/$EXE"
    rsh() { ssh "$HOST" "$1"; }
  fi
fi

# Environment of every client command there.
if [ "$OS" = windows ]; then
  ENVS='$env:SHARETOKEN_PORTAL="https://127.0.0.1:18092"; $env:SHARETOKEN_REGION="CN"; $env:SHARETOKEN_DIAG_FIRST="1s"; $env:SHARETOKEN_DIAG_EVERY="2s"; '
  st() { rsh "$ENVS & \"$RBIN\" -home \"$H\" $*"; }
  start_daemon() {
    # Hidden, with its output going nowhere: as the logon task starts it.
    # Through WMI: a process an SSH session starts dies with the session.
    rsh "\$c = 'cmd /c \"set SHARETOKEN_PORTAL=https://127.0.0.1:18092&& set SHARETOKEN_REGION=CN&& set SHARETOKEN_DIAG_FIRST=1s&& set SHARETOKEN_DIAG_EVERY=2s&& \"' + \"$RBIN\" + '\" -home \"' + \"$H\" + '\" daemon -http 127.0.0.1:19899 -listen /ip4/127.0.0.1/tcp/0 -nat=false >NUL 2>&1\"'; Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{CommandLine=\$c} | Out-Null"
  }
  alive() { rsh "if (Get-Process sharetoken-diagtest -ErrorAction SilentlyContinue) { 'yes' } else { 'no' }"; }
  key() { rsh "(Get-Content \"$H\\run.json\" | ConvertFrom-Json).key" 2>/dev/null | tr -d '\r'; }
  crash() { rsh "try { Invoke-RestMethod -Method Post -Headers @{Authorization='Bearer $(key)'} -Uri 'http://127.0.0.1:19899/api/v1/diagnostics/crash?kind=$1' | Out-Null } catch { 'request failed (expected for handler)' }"; }
  ls_dir() { rsh "Get-ChildItem -Name \"$H\\logs\\$1\" -ErrorAction SilentlyContinue"; }
  cat_file() { rsh "Get-Content -Raw \"$H\\logs\\$1\""; }
  stop_daemon() { rsh "Stop-Process -Name sharetoken-diagtest -Force -ErrorAction SilentlyContinue" || true; }
else
  ENVS='export SHARETOKEN_PORTAL=https://127.0.0.1:18092 SHARETOKEN_REGION=CN SHARETOKEN_DIAG_FIRST=1s SHARETOKEN_DIAG_EVERY=2s; '
  st() { rsh "$ENVS \"$RBIN\" -home \"$H\" $*"; }
  start_daemon() { rsh "$ENVS nohup \"$RBIN\" -home \"$H\" daemon -http 127.0.0.1:19899 -listen /ip4/127.0.0.1/tcp/0 -nat=false </dev/null >/dev/null 2>&1 &"; }
  alive() { rsh "pgrep -f '[s]haretoken-diagtest -home' >/dev/null && echo yes || echo no"; }
  key() { rsh "sed -n 's/.*\"key\": *\"\\([0-9a-f]*\\)\".*/\\1/p' \"$H/run.json\"" 2>/dev/null; }
  crash() { rsh "curl -s -o /dev/null -w '%{http_code}\n' -X POST -H 'Authorization: Bearer $(key)' 'http://127.0.0.1:19899/api/v1/diagnostics/crash?kind=$1' || true"; }
  ls_dir() { rsh "ls \"$H/logs/$1\" 2>/dev/null || true"; }
  cat_file() { rsh "cat \"$H/logs/$1\""; }
  stop_daemon() { rsh "pkill -f '[s]haretoken-diagtest -home' || true"; }
fi

wait_until() { # wait_until <seconds> <what> <command…>
  local n=$1 what=$2; shift 2
  for _ in $(seq 1 $((n * 2))); do "$@" && return 0; sleep 0.5; done
  fail "timed out: $what"
}
rows() { sql "SELECT COUNT(*) FROM client_reports r JOIN users u ON u.id = r.user_id WHERE u.username = '$EMAIL' AND $1"; }
has_row() { [ "$(rows "$1")" -ge 1 ]; }
dead() { [ "$(alive | tr -d '\r')" = no ]; }
up() { [ -n "$(key)" ] && [ "$(alive | tr -d '\r')" = yes ]; }

echo "== $WHERE ($OS): $EMAIL"
stop_daemon
st login -email "$EMAIL" -send -portal-pin "$PIN" >/dev/null
CODE=$("$ROOT/scripts/test/gui_stack.sh" code "$STACK" "$EMAIL")
st login -email "$EMAIL" -code "$CODE" -portal-pin "$PIN" -device "diag-$TAG" >/dev/null
ok "signed in"
st diagnostics | tee "$OUT/diagnostics.txt"
grep -q 'crash reports: on (the default)' "$OUT/diagnostics.txt" || fail "reports should be on by default outside the EEA"
ok "crash reports on by default (region CN)"

start_daemon
wait_until 30 "daemon start" up
ok "the daemon started with its output going nowhere"
cat_file client.log > "$OUT/client.log"
grep -q 'msg=starting component=diag' "$OUT/client.log" || fail "client.log has no start line"
ok "the daemon writes logs/client.log itself"

# 1. A panic in a handler: recovered, the daemon lives, the report goes at once.
crash handler
sleep 1
[ "$(alive | tr -d '\r')" = yes ] || fail "the daemon died of a recovered panic"
wait_until 30 "handler panic report" has_row "r.kind = 'panic' AND r.message LIKE '%index out of range [7]%'"
ok "a recovered handler panic is reported while the daemon runs"

# 2-4. Crashes: the daemon dies; the next start reports it.
for k in goroutine:5 wrapped:6 fatal:fatal; do
  kind=${k%%:*}; mark=${k#*:}
  crash "$kind"
  wait_until 30 "the daemon to die of $kind" dead
  [ -n "$(ls_dir crash)" ] || fail "no crash file after $kind"
  start_daemon
  wait_until 30 "restart after $kind" up
  if [ "$kind" = fatal ]; then
    wait_until 40 "fatal report" has_row "r.kind = 'crash' AND r.message LIKE 'fatal error%' AND r.stack LIKE '%testcrash%'"
  else
    wait_until 40 "$kind report" has_row "r.kind = 'crash' AND r.message LIKE '%index out of range [$mark]%'"
  fi
  ok "a $kind crash is reported after the restart"
done
[ "$(rows "r.message LIKE '%[6]%'")" = 1 ] || fail "the wrapped panic was reported twice"
ok "the wrapped goroutine's panic and its crash file make one report"
[ "$(sql "SELECT SUM(count) FROM client_reports r JOIN users u ON u.id = r.user_id WHERE u.username = '$EMAIL' AND r.message LIKE '%[6]%'")" = 1 ] || fail "counted twice"

# Redaction: what reached the portal has no address of this machine, user or e-mail.
sql "SELECT CONCAT(message, '\n', stack, '\n', context, '\n', features, '\n', os_version) FROM client_reports r JOIN users u ON u.id = r.user_id WHERE u.username = '$EMAIL'" > "$OUT/stored.txt"
RUSER=$(rsh 'whoami' | tr -d '\r' | sed 's/.*\\//')
for leak in "$EMAIL" "/$RUSER/" "\\\\$RUSER\\\\" "$PIN"; do
  ! grep -qF -- "$leak" "$OUT/stored.txt" || fail "stored reports contain $leak"
done
grep -q 'crashInGoroutine' "$OUT/stored.txt" || fail "the stack lost the function"
ok "the stored reports are redacted (no e-mail, user name or home path) and keep the code location"
sql "SELECT CONCAT_WS(' | ', kind, process, version, os, arch, os_version, count, LEFT(message, 70)) FROM client_reports r JOIN users u ON u.id = r.user_id WHERE u.username = '$EMAIL' ORDER BY r.id" | tee "$OUT/rows.txt"

# Off: a crash is still recorded locally, nothing is sent, the queue stays empty.
st diagnostics off >/dev/null
before=$(rows "1=1")
crash goroutine
wait_until 30 "the daemon to die" dead
start_daemon
wait_until 30 "restart" up
sleep 6
[ "$(rows "1=1")" = "$before" ] || fail "a report was sent with reports off"
[ -z "$(ls_dir reports | tr -d '\r')" ] || fail "a report was queued with reports off"
[ -n "$(ls_dir crash/old | tr -d '\r')" ] || fail "the crash record was not kept"
ok "with reports off nothing is queued or sent; the crash record stays in crash/old"
st diagnostics on >/dev/null

# The CLI reads the log.
st logs -n 3 > "$OUT/logs.txt"
[ "$(wc -l < "$OUT/logs.txt")" -ge 1 ] || fail "sharetoken logs printed nothing"
ok "sharetoken logs prints the log"

# Without SHARETOKEN_REGION: what this system's settings say.
if [ "$OS" = windows ]; then
  rsh "\$env:SHARETOKEN_REGION=\$null; & \"$RBIN\" -home \"$H\" diagnostics" | tee "$OUT/region.txt"
else
  rsh "unset SHARETOKEN_REGION; \"$RBIN\" -home \"$H\" diagnostics" | tee "$OUT/region.txt"
fi
stop_daemon
echo "$WHERE: $pass checks passed"
