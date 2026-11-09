#!/usr/bin/env bash
# Builds the desktop app and its installer into dist/, for one build type, which
# must be named:
#   scripts/build-gui.sh <dev|prod>                        for this machine
#   scripts/build-gui.sh <dev|prod> macos [amd64|arm64]    on a Mac, either architecture
#   scripts/build-gui.sh <dev|prod> windows [amd64|arm64]  from any system, no cgo
#   scripts/build-gui-linux.sh <dev|prod> [amd64|arm64]    Linux, from any system with Docker
#
#   macOS:   dist/ShareToken.app (not signed or notarized), and sharetoken-<version>-macos-<arch>.dmg
#   Linux:   dist/sharetoken-<version>-linux-<arch>/, and sharetoken-<version>-linux-<arch>.deb
#   Windows: dist/sharetoken-<version>-windows-<arch>/ (not code-signed), and sharetoken-<version>-windows-<arch>.exe,
#            made with makensis, or in the Docker image of scripts/build-gui-linux.sh if it is not installed
#   dist/SHA256SUMS-<version> lists the installers.
#
# Programs: sharetoken (CLI and daemon), sharetoken-gui (window). Their names carry
# no version: the window finds the daemon by name, and a user's PATH keeps working
# across upgrades. The version is inside them and in the installer's name. A dev
# build says so in all its names: sharetoken-dev, sharetoken-gui-dev, sharetoken-dev-<version>-…
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
if [ "$ENV" = prod ]; then NAME=ShareToken; BUNDLE_ID=app.sharetoken.desktop; SUFFIX=
else NAME="ShareToken DEV"; BUNDLE_ID=app.sharetoken.desktop.dev; SUFFIX=-$ENV; fi
EXE=sharetoken$SUFFIX; GUI=sharetoken-gui$SUFFIX
OUT=dist
VERSION=$(scripts/version.sh)
NUMERIC=${VERSION%%-*}  # macOS and Windows metadata take only numbers
STAMP=sharetoken-build-type=$ENV
LDFLAGS=$(scripts/client_ldflags.sh "$ENV")
SHELL_LDFLAGS="-s -w -X main.buildType=$STAMP -X main.version=$VERSION"

fill() { # template: the packaging files name the build type with @ENV@ and so on
  sed -e "s/@EXE@/$EXE/g" -e "s/@GUI@/$GUI/g" -e "s/@NAME@/$NAME/g" -e "s/@BUNDLE_ID@/$BUNDLE_ID/g" -e "s/@VERSION@/$NUMERIC/g" "$1"
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
    LC_ALL=C grep -aq "$VERSION" "$f" || { echo "$f does not carry version $VERSION" >&2; exit 1; }
  done
}

sums() { # installer: added to the release's checksum file
  (cd "$OUT" && { cat "SHA256SUMS-$VERSION" 2>/dev/null; shasum -a 256 "$1"; } | sort -u -k2 > "SHA256SUMS-$VERSION.tmp" && mv "SHA256SUMS-$VERSION.tmp" "SHA256SUMS-$VERSION")
}

build_macos() { # architecture
  # Cgo builds for the other architecture too: Go passes -arch to clang.
  local arch=$1 app="$OUT/ShareToken$SUFFIX.app" dmg stage icons
  export GOARCH=$arch
  rm -rf "$app"
  mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
  CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "$app/Contents/MacOS/$EXE" ./cmd/sharetoken
  build_shell "$app/Contents/MacOS/$GUI" ""
  verify "$app/Contents/MacOS/$EXE" "$app/Contents/MacOS/$GUI"
  fill gui/packaging/Info.plist > "$app/Contents/Info.plist"
  icons=$(mktemp -d)/app.iconset
  mkdir -p "$icons"
  for s in 16 32 128 256 512; do
    sips -z $s $s gui/packaging/icon.png --out "$icons/icon_${s}x${s}.png" >/dev/null
    sips -z $((s*2)) $((s*2)) gui/packaging/icon.png --out "$icons/icon_${s}x${s}@2x.png" >/dev/null
  done
  iconutil -c icns "$icons" -o "$app/Contents/Resources/sharetoken.icns"
  # Ad-hoc signature: Apple silicon refuses to run unsigned code. Downloaded
  # copies still need a Developer ID signature and notarization to open without
  # a Gatekeeper warning.
  codesign --force --deep --sign - "$app" 2>/dev/null
  # The disk image opens on the app and a link to drag it onto.
  dmg=sharetoken$SUFFIX-$VERSION-macos-$arch.dmg
  stage=$(mktemp -d)
  ditto "$app" "$stage/$NAME.app"
  ln -s /Applications "$stage/Applications"
  rm -f "$OUT/$dmg"
  hdiutil create -quiet -volname "$NAME" -srcfolder "$stage" -fs HFS+ -format UDZO "$OUT/$dmg"
  rm -rf "$stage"
  sums "$dmg"
  echo "built $OUT/$dmg"
}

nsis() { # makensis arguments; paths in them are relative to dist/
  if command -v makensis >/dev/null; then
    local nsi=$PWD/gui/packaging/installer.nsi
    (cd "$OUT" && makensis -NOCD "$@" "$nsi")
  else
    scripts/build-gui-linux.sh --image amd64
    docker run --rm --platform linux/amd64 -v "$PWD/$OUT:/out" -v "$PWD/gui/packaging:/packaging:ro" -w /out sharetoken-gui-builder:amd64 makensis -NOCD "$@" /packaging/installer.nsi
  fi
}

case "${1:-$(uname -s)}" in
windows)
  # Wails needs no cgo on Windows, so this cross-compiles. The .syso carries the
  # icon and the manifest (DPI awareness, common controls v6 for its dialogs);
  # go build links it by its name, and it must not outlive this build.
  ARCH=${2:-amd64}
  BASE=sharetoken$SUFFIX-$VERSION-windows-$ARCH
  DIR=$OUT/$BASE
  rm -rf "$DIR"
  mkdir -p "$DIR"
  CGO_ENABLED=0 GOOS=windows GOARCH=$ARCH go build -trimpath -ldflags "$LDFLAGS" -o "$DIR/$EXE.exe" ./cmd/sharetoken
  trap 'rm -f gui/rsrc_windows_*.syso' EXIT
  go run github.com/tc-hib/go-winres@v0.3.3 simply --arch "$ARCH" --manifest gui --icon gui/packaging/icon.png --out gui/rsrc
  (cd gui && CGO_ENABLED=0 GOOS=windows GOARCH=$ARCH go build -trimpath -tags desktop,production -ldflags "$SHELL_LDFLAGS -H windowsgui" -o "../$DIR/$GUI.exe" .)
  verify "$DIR/$EXE.exe" "$DIR/$GUI.exe"
  cp gui/packaging/icon.ico gui/packaging/user-path.ps1 "$DIR/"
  nsis -V2 "-DNAME=$NAME" "-DEXE=$EXE" "-DGUI=$GUI" "-DVERSION=$NUMERIC" "-DSRC=$BASE" "-DOUTFILE=$BASE.exe" "-DSUFFIX=$SUFFIX"
  sums "$BASE.exe"
  echo "built $OUT/$BASE.exe"
  ;;
macos)
  build_macos "${2:-$(go env GOARCH)}"
  ;;
Darwin)
  build_macos "$(go env GOARCH)"
  ;;
Linux)
  # webkit2gtk 4.1 (Ubuntu 22.04+, Debian 12+); older systems only have 4.0.
  TAG=""
  pkg-config --exists webkit2gtk-4.1 && TAG=",webkit2_41"
  ARCH=$(go env GOARCH)
  BASE=sharetoken$SUFFIX-$VERSION-linux-$ARCH
  DIR=$OUT/$BASE
  rm -rf "$DIR"
  mkdir -p "$DIR"
  CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "$DIR/$EXE" ./cmd/sharetoken
  build_shell "$DIR/$GUI" "$TAG"
  verify "$DIR/$EXE" "$DIR/$GUI"
  # The package: programs on every user's PATH, an entry in the application menu.
  ROOT=$(mktemp -d)
  install -d "$ROOT/DEBIAN" "$ROOT/usr/bin" "$ROOT/usr/share/applications" "$ROOT/usr/share/icons/hicolor/512x512/apps"
  install -m 755 "$DIR/$EXE" "$DIR/$GUI" "$ROOT/usr/bin/"
  install -m 644 gui/packaging/icon.png "$ROOT/usr/share/icons/hicolor/512x512/apps/$EXE.png"
  fill gui/packaging/sharetoken.desktop > "$ROOT/usr/share/applications/$EXE.desktop"
  chmod 644 "$ROOT/usr/share/applications/$EXE.desktop"
  { fill gui/packaging/deb-control
    echo "Architecture: $ARCH"
    echo "Installed-Size: $(du -sk "$ROOT/usr" | cut -f1)"
  } > "$ROOT/DEBIAN/control"
  install -m 755 gui/packaging/deb-postchange "$ROOT/DEBIAN/postinst"
  install -m 755 gui/packaging/deb-postchange "$ROOT/DEBIAN/postrm"
  chmod -R go-w "$ROOT"
  dpkg-deb --root-owner-group -Zxz --build "$ROOT" "$DIR.deb" >/dev/null
  rm -rf "$ROOT"
  sums "$BASE.deb"
  echo "built $DIR.deb"
  ;;
*)
  echo "unsupported system: $(uname -s)" >&2; exit 1 ;;
esac
