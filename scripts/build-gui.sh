#!/usr/bin/env bash
# Builds the desktop app into dist/, for one build type, which must be named:
#   scripts/build-gui.sh <dev|prod>                        for this machine
#   scripts/build-gui.sh <dev|prod> windows [amd64|arm64]  from any system, no cgo
#
#   macOS:   dist/sharetoken-<env>.app (not signed or notarized), and a zip of it
#   Linux:   dist/sharetoken-<env>-linux-<arch>/ with a launcher entry, and a tar.gz of it
#   Windows: dist/sharetoken-<env>-windows-<arch>/ (not code-signed), and a zip of it
#
# Programs: sharetoken-<env> (CLI and daemon), sharetoken-gui-<env> (window).
#
# The build type decides the portal, the state directory and the local port
# (client/env.go, gui/env.go), so a dev and a prod app can live on one machine:
#   dev:  https://dev.sharetoken.app   ~/.sharetoken-dev   127.0.0.1:9887
#   prod: https://sharetoken.app       ~/.sharetoken       127.0.0.1:9787
#
# Linux build dependencies (Debian/Ubuntu 24.04 names):
#   sudo apt install build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev
#
# Usage statistics go to the built-in PostHog project from prod builds only
# (client/telemetry.go). TELEMETRY_KEY=… replaces the key, TELEMETRY_OFF=1 builds
# an app that sends nothing, and TELEMETRY_HOST=https://… selects another instance.
set -euo pipefail
cd "$(dirname "$0")/.."
ENV=${1:-}
case $ENV in
  dev|prod) shift ;;
  *) echo "usage: scripts/build-gui.sh <dev|prod> [windows [amd64|arm64]]" >&2; exit 2 ;;
esac
if [ "$ENV" = prod ]; then NAME=sharetoken; BUNDLE_ID=app.sharetoken.desktop
else NAME="sharetoken DEV"; BUNDLE_ID=app.sharetoken.desktop.dev; fi
OUT=dist
VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
STAMP=sharetoken-build-type=$ENV
LDFLAGS="-s -w -X sharetoken/client.Version=$VERSION -X sharetoken/client.buildType=$STAMP"
[ -n "${TELEMETRY_OFF:-}" ] && LDFLAGS="$LDFLAGS -X sharetoken/client.TelemetryKey="
[ -n "${TELEMETRY_KEY:-}" ] && LDFLAGS="$LDFLAGS -X sharetoken/client.TelemetryKey=$TELEMETRY_KEY"
[ -n "${TELEMETRY_HOST:-}" ] && LDFLAGS="$LDFLAGS -X sharetoken/client.TelemetryHost=$TELEMETRY_HOST"
SHELL_LDFLAGS="-s -w -X main.buildType=$STAMP"

fill() { # template: the packaging files name the build type with @ENV@ and so on
  sed -e "s/@ENV@/$ENV/g" -e "s/@NAME@/$NAME/g" -e "s/@BUNDLE_ID@/$BUNDLE_ID/g" -e "s/@VERSION@/$VERSION/g" "$1"
}
build_shell() { # output path, extra tags
  (cd gui && CGO_ENABLED=1 go build -trimpath -tags "desktop,production$2" -ldflags "$SHELL_LDFLAGS" -o "../$1" .)
}
# -X says nothing when it misses its variable, and a wrong build type here sends
# users to the wrong network: look for the stamp in what was built. Works on
# binaries of any system. (go version -m cannot tell: -trimpath hides -ldflags.)
verify() { # programs
  local f
  for f in "$@"; do
    LC_ALL=C grep -aq "$STAMP" "$f" || { echo "$f is not a $ENV build" >&2; exit 1; }
  done
}

case "${1:-$(uname -s)}" in
windows)
  # Wails needs no cgo on Windows, so this cross-compiles. The .syso carries the
  # icon and the manifest (DPI awareness, common controls v6 for its dialogs);
  # go build links it by its name, and it must not outlive this build.
  ARCH=${2:-amd64}
  BASE=sharetoken-$ENV-windows-$ARCH
  DIR=$OUT/$BASE
  rm -rf "$DIR"
  mkdir -p "$DIR"
  CGO_ENABLED=0 GOOS=windows GOARCH=$ARCH go build -trimpath -ldflags "$LDFLAGS" -o "$DIR/sharetoken-$ENV.exe" ./cmd/sharetoken
  trap 'rm -f gui/rsrc_windows_*.syso' EXIT
  go run github.com/tc-hib/go-winres@v0.3.3 simply --arch "$ARCH" --manifest gui --icon gui/packaging/icon.png --out gui/rsrc
  (cd gui && CGO_ENABLED=0 GOOS=windows GOARCH=$ARCH go build -trimpath -tags desktop,production -ldflags "$SHELL_LDFLAGS -H windowsgui" -o "../$DIR/sharetoken-gui-$ENV.exe" .)
  verify "$DIR/sharetoken-$ENV.exe" "$DIR/sharetoken-gui-$ENV.exe"
  (cd "$OUT" && rm -f "$BASE.zip" && zip -qr "$BASE.zip" "$BASE")
  echo "built $DIR"
  ;;
Darwin)
  APP=$OUT/sharetoken-$ENV.app
  rm -rf "$APP"
  mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
  CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "$APP/Contents/MacOS/sharetoken-$ENV" ./cmd/sharetoken
  build_shell "$APP/Contents/MacOS/sharetoken-gui-$ENV" ""
  verify "$APP/Contents/MacOS/sharetoken-$ENV" "$APP/Contents/MacOS/sharetoken-gui-$ENV"
  fill gui/packaging/Info.plist > "$APP/Contents/Info.plist"
  ICONSET=$(mktemp -d)/app.iconset
  mkdir -p "$ICONSET"
  for s in 16 32 128 256 512; do
    sips -z $s $s gui/packaging/icon.png --out "$ICONSET/icon_${s}x${s}.png" >/dev/null
    sips -z $((s*2)) $((s*2)) gui/packaging/icon.png --out "$ICONSET/icon_${s}x${s}@2x.png" >/dev/null
  done
  iconutil -c icns "$ICONSET" -o "$APP/Contents/Resources/sharetoken.icns"
  # Ad-hoc signature: Apple silicon refuses to run unsigned code. Downloaded
  # copies still need a Developer ID signature and notarization to open without
  # a Gatekeeper warning.
  codesign --force --deep --sign - "$APP" 2>/dev/null
  (cd "$OUT" && rm -f "sharetoken-$ENV-macos-$(uname -m).zip" && ditto -c -k --keepParent "sharetoken-$ENV.app" "sharetoken-$ENV-macos-$(uname -m).zip")
  echo "built $APP"
  ;;
Linux)
  # webkit2gtk 4.1 (Ubuntu 24.04+, Debian 12+); older systems only have 4.0.
  TAG=""
  pkg-config --exists webkit2gtk-4.1 && TAG=",webkit2_41"
  BASE=sharetoken-$ENV-linux-$(uname -m)
  DIR=$OUT/$BASE
  rm -rf "$DIR"
  mkdir -p "$DIR"
  CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "$DIR/sharetoken-$ENV" ./cmd/sharetoken
  build_shell "$DIR/sharetoken-gui-$ENV" "$TAG"
  verify "$DIR/sharetoken-$ENV" "$DIR/sharetoken-gui-$ENV"
  cp gui/packaging/icon.png "$DIR/"
  fill gui/packaging/sharetoken.desktop > "$DIR/sharetoken-$ENV.desktop"
  fill gui/packaging/install-linux.sh > "$DIR/install-linux.sh"
  chmod +x "$DIR/install-linux.sh"
  tar -C "$OUT" -czf "$DIR.tar.gz" "$BASE"
  echo "built $DIR"
  ;;
*)
  echo "unsupported system: $(uname -s)" >&2; exit 1 ;;
esac
