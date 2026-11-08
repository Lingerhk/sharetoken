#!/usr/bin/env bash
# Runs the built desktop shell through its self-test: the real webview loads the
# page through the shell's proxy and POSTs to the real daemon, and the shell
# reports the result and quits. No window is shown (hidden on macOS; a virtual
# X display on Linux), and no account or network is involved.
#
#   scripts/gui_shell_test.sh <dev|prod> <dir with sharetoken-<env> and sharetoken-gui-<env>>
set -euo pipefail
ENV=$1
BIN=$(cd "$2" && pwd)
HOME_DIR=$(mktemp -d)
trap 'rm -rf "$HOME_DIR"' EXIT
printf '{"version":1,"env":"%s","http":"127.0.0.1:%s"}\n' "$ENV" "${GUI_TEST_PORT:-19799}" > "$HOME_DIR/config.json"
# The binaries are release builds: keep this test out of the real statistics.
export SHARETOKEN_HOME=$HOME_DIR SHARETOKEN_GUI_SELFTEST=1 SHARETOKEN_TELEMETRY=0
RUN=("$BIN/sharetoken-gui-$ENV")
if [ "$(uname -s)" = Linux ] && [ -z "${DISPLAY:-}" ]; then RUN=(xvfb-run -a "${RUN[@]}"); fi
set +e
OUT=$("${RUN[@]}" 2>&1)
CODE=$?
set -e
echo "$OUT" | grep -E '^selftest:' || { echo "$OUT" | tail -20; echo "no self-test result (exit $CODE)"; exit 1; }
[ "$CODE" = 0 ] || { echo "shell exit code $CODE"; exit 1; }
# The daemon the shell started must be gone with it.
if curl -s -m 2 "http://127.0.0.1:${GUI_TEST_PORT:-19799}/api/v1/session" >/dev/null; then
  echo "the daemon is still running after the shell quit"; exit 1
fi
echo "shell self-test passed; daemon stopped with it"
