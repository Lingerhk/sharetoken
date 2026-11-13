#!/usr/bin/env bash
# Runs the built desktop app through its tray self-test: closing the window, the
# menu's Open and Quit, the status line, the language, a second start. The window
# and the menu-bar icon do show for about 15 seconds. No account or network is
# involved. macOS and Linux (on Wayland the minimise steps are skipped; run with
# GDK_BACKEND=x11 for them); on Windows start the app with SHARETOKEN_GUI_SELFTEST=tray.
#
#   scripts/gui_tray_test.sh <dev|prod> <dir with the programs of that build (sharetoken and sharetoken-gui, or sharetoken-dev and sharetoken-gui-dev)>
set -euo pipefail
ENV=$1
BIN=$(cd "$2" && pwd)
PORT=${GUI_TEST_PORT:-19798}
HOME_DIR=$(mktemp -d)
trap 'rm -rf "$HOME_DIR"' EXIT
printf '{"version":1,"env":"%s","http":"127.0.0.1:%s"}\n' "$ENV" "$PORT" > "$HOME_DIR/config.json"
export SHARETOKEN_HOME=$HOME_DIR SHARETOKEN_GUI_SELFTEST=tray
set +e
SUFFIX=-$ENV; [ "$ENV" = prod ] && SUFFIX=
OUT=$("$BIN/sharetoken-gui$SUFFIX" 2>&1)
CODE=$?
set -e
echo "$OUT" | grep -E '^(tray|selftest):' || { echo "$OUT" | tail -20; echo "no self-test result (exit $CODE)"; exit 1; }
[ "$CODE" = 0 ] || { echo "app exit code $CODE"; exit 1; }
echo "$OUT" | grep -q '^selftest: ok' || exit 1
if curl -s -m 2 "http://127.0.0.1:$PORT/api/v1/session" >/dev/null; then
  echo "the daemon is still running after Quit"; exit 1
fi
echo "tray self-test passed; daemon stopped with the app"
