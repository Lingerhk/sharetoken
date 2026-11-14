#!/usr/bin/env bash
# One-click update of the macOS app, end to end, on a private network
# (scripts/test/gui_stack.sh): builds an old copy A and a new copy B of the dev app,
# publishes B signed with the release key, and lets A install it as the button
# in its window would ($SHARETOKEN_GUI_SELFTEST=update, window hidden). Then:
#   - A swapped its bundle for B's (Wails' updater) and B started and passed the
#     window self-test (it reports to a file);
#   - a package whose signature is not the release key's is refused, and the
#     bundle stays as it was.
#   scripts/test/gui_update_test.sh [work directory]
# The release key is $ST_RELEASE_KEY or local/keys/release-signing.pem. B carries
# LSEnvironment in its Info.plist so that the copy macOS starts after the swap
# uses the test's state directory and portal, not the user's.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/../.." && pwd); cd "$ROOT"
W=${1:-$(mktemp -d)}; mkdir -p "$W"; W=$(cd "$W" && pwd)
KEY=${ST_RELEASE_KEY:-local/keys/release-signing.pem}
ARCH=$(go env GOARCH)
H=$W/home; APPS=$W/Applications; APP="$APPS/ShareToken DEV.app"
GUI="$APP/Contents/MacOS/sharetoken-gui-dev"; EXE="$APP/Contents/MacOS/sharetoken-dev"
fail() { echo "FAIL: $*" >&2; exit 1; }
cleanup() {
  pkill -f "$APPS/" 2>/dev/null || true
  scripts/test/gui_stack.sh stop "$W/stack" >/dev/null 2>&1 || true
}
trap cleanup EXIT

scripts/test/gui_stack.sh start "$W/stack" > /dev/null
# shellcheck disable=SC1091
. "$W/stack/env"
mkdir -p "$H"; chmod 700 "$H"
printf '{"version":1,"env":"dev","portal":"%s","portal_pin":"%s","http":"127.0.0.1:19887"}\n' "$PORTAL" "$PIN" > "$H/config.json"

# with_env APP: the environment macOS gives the app when it starts it (open -n).
with_env() {
  local plist="$1/Contents/Info.plist" pb=/usr/libexec/PlistBuddy
  $pb -c "Delete :LSEnvironment" "$plist" 2>/dev/null || true
  $pb -c "Add :LSEnvironment dict" \
      -c "Add :LSEnvironment:SHARETOKEN_HOME string $H" \
      -c "Add :LSEnvironment:SHARETOKEN_PORTAL string $PORTAL" \
      -c "Add :LSEnvironment:SHARETOKEN_GUI_SELFTEST string 1" \
      -c "Add :LSEnvironment:SHARETOKEN_GUI_SELFTEST_REPORT string $W/after.txt" "$plist"
  codesign --force --deep --sign - "$1" 2>/dev/null
}
build() { # commit build-time → dist/ShareToken-dev.app and its zip
  ST_VERSION=1.0.0 ST_COMMIT=$1 ST_BUILD_TIME=$2 ST_NO_DMG=1 scripts/build/build-gui.sh dev macos "$ARCH" > /dev/null
}

echo "building A (old) and B (new)"
build aaaaaaa 2026-01-01T00:00:00Z
rm -rf "$W/A.app"; ditto dist/ShareToken-dev.app "$W/A.app"; with_env "$W/A.app"
BT=$(date -u +%Y-%m-%dT%H:%M:%SZ)
build bbbbbbb "$BT"
with_env dist/ShareToken-dev.app
ZIP=sharetoken-dev-1.0.0-macos-$ARCH.app.zip
rm -rf "$W/zip"; mkdir "$W/zip"; ditto dist/ShareToken-dev.app "$W/zip/ShareToken DEV.app"
rm -f "$DOWNLOADS/$ZIP"; ditto -c -k --norsrc --noextattr --noacl --keepParent "$W/zip/ShareToken DEV.app" "$DOWNLOADS/$ZIP"
printf '{"version":"1.0.0","commit":"bbbbbbb","build_time":"%s"}\n' "$BT" > "$DOWNLOADS/RELEASE-1.0.0.json"
(cd "$DOWNLOADS" && shasum -a 256 "$ZIP" > SHA256SUMS-1.0.0)

install_a() { rm -rf "$APPS"; mkdir -p "$APPS"; ditto "$W/A.app" "$APP"; }
run_a() { # → A's output in $W/a.log; its status
  rm -f "$W/after.txt"
  SHARETOKEN_HOME=$H SHARETOKEN_PORTAL=$PORTAL SHARETOKEN_GUI_SELFTEST=update SHARETOKEN_GUI_SELFTEST_AFTER=1 \
    timeout 300 "$GUI" > "$W/a.log" 2>&1
}

echo "1. a package signed with another key is refused"
install_a
go run scripts/release/sign-packages.go genkey "$W/other.pem" > /dev/null 2>&1 || true
rm -f "$DOWNLOADS/SIGNATURES-1.0.0"
go run scripts/release/sign-packages.go sign "$W/other.pem" "$DOWNLOADS/SIGNATURES-1.0.0" "$DOWNLOADS/$ZIP"
if run_a; then cat "$W/a.log"; fail "A installed a package signed with another key"; fi
grep -q "not the release key" "$W/a.log" || { cat "$W/a.log"; fail "A failed for another reason"; }
"$EXE" version | grep -q aaaaaaa || fail "the bundle changed"
echo "   refused: $(grep -o 'selftest: failed.*' "$W/a.log")"

echo "2. the signed package is installed, and the new copy starts"
install_a
rm -f "$DOWNLOADS/SIGNATURES-1.0.0"
go run scripts/release/sign-packages.go sign "$KEY" "$DOWNLOADS/SIGNATURES-1.0.0" "$DOWNLOADS/$ZIP"
run_a || { cat "$W/a.log"; fail "A's update failed"; }
cat "$W/a.log" | grep selftest
for _ in $(seq 1 120); do [ -s "$W/after.txt" ] && break; sleep 1; done
[ -s "$W/after.txt" ] || fail "the new copy did not report (helper log: $(ls -t "${TMPDIR:-/tmp}"/wails-update-*.log 2>/dev/null | head -1))"
echo "   after the restart: $(cat "$W/after.txt")"
grep -q "^1.0.0 ok" "$W/after.txt" || fail "the new copy's self-test failed"
"$EXE" version | grep -q bbbbbbb || fail "the bundle is not B: $("$EXE" version | head -1)"
codesign --verify --deep --strict "$APP" || fail "the installed bundle's signature"
[ ! -e "$APP.bak" ] || fail "the backup was left behind"
grep -q "update fetched and checked" "$H/daemon.log" || fail "the daemon did not log the download"
echo "PASS: $(head -1 < <("$EXE" version))"
