#!/usr/bin/env bash
# Builds the command-line client into dist/, for one build type, which must be
# named. It needs no cgo and no window system, so any system builds them all:
#   scripts/build/build-cli.sh <dev|prod>                      this machine's system
#   scripts/build/build-cli.sh <dev|prod> <macos|linux|windows> [amd64|arm64]
#   scripts/build/build-cli.sh <dev|prod> all                  three systems, two architectures
#
# Each makes dist/sharetoken-cli-<version>-<os>-<arch>.tar.gz (.zip for Windows),
# listed in dist/SHA256SUMS-<version>. It holds the program sharetoken, the same
# program as in the desktop app's package and under the same name, so a user's
# scripts and PATH work with either, and a README. Only dev builds carry their
# type in these names (sharetoken-cli-dev-…, sharetoken-dev), so they can live
# next to the real thing.
#
# macOS programs carry only
# the linker's ad-hoc signature: a downloaded one needs a Developer ID signature
# and notarization to run without a Gatekeeper warning, and Windows ones are not
# code-signed.
set -euo pipefail
cd "$(dirname "$0")/../.."
ENV=${1:-}
case $ENV in
  dev|prod) shift ;;
  *) sed -n 2,6p "$0" >&2; exit 2 ;;
esac
OUT=dist
VERSION=$(scripts/build/version.sh)
STAMP=sharetoken-build-type=$ENV
SUFFIX=-$ENV; [ "$ENV" = prod ] && SUFFIX=
LABEL=" ($ENV build)"; [ "$ENV" = prod ] && LABEL=
LDFLAGS=$(scripts/build/ldflags.sh "$ENV")
TAGS=$(scripts/build/tags.sh)
mkdir -p "$OUT"

build() { # go's system name, architecture
  local goos=$1 arch=$2 os=$1 exe=sharetoken$SUFFIX base dir archive
  if [ "$goos" = darwin ]; then os=macos; fi
  if [ "$goos" = windows ]; then exe=$exe.exe; fi
  base=sharetoken-cli$SUFFIX-$VERSION-$os-$arch
  dir=$OUT/$base
  rm -rf "$dir"
  mkdir -p "$dir"
  CGO_ENABLED=0 GOOS=$goos GOARCH=$arch go build -trimpath -tags "$TAGS" -ldflags "$LDFLAGS" -o "$dir/$exe" ./cmd/sharetoken
  # -X says nothing when it misses its variable; see scripts/build-gui.sh.
  LC_ALL=C grep -aq "$STAMP" "$dir/$exe" || { echo "$exe is not a $ENV build" >&2; exit 1; }
  LC_ALL=C grep -aq "$VERSION" "$dir/$exe" || { echo "$exe does not carry version $VERSION" >&2; exit 1; }
  sed -e "s/@EXE@/$exe/g" -e "s/@VERSION@/$VERSION/g" -e "s/@LABEL@/$LABEL/g" scripts/build/cli-readme.txt > "$dir/README.txt"
  if [ "$goos" = windows ]; then
    archive=$base.zip
    (cd "$OUT" && rm -f "$archive" && zip -qr "$archive" "$base")
  else
    archive=$base.tar.gz
    # Without these, macOS adds ._* files and extended attributes that other systems' tar complains about.
    COPYFILE_DISABLE=1 tar --no-xattrs -C "$OUT" -czf "$OUT/$archive" "$base"
  fi
  (cd "$OUT" && { cat "SHA256SUMS-$VERSION" 2>/dev/null; shasum -a 256 "$archive"; } | sort -u -k2 > "SHA256SUMS-$VERSION.tmp" && mv "SHA256SUMS-$VERSION.tmp" "SHA256SUMS-$VERSION")
  rm -rf "$dir"
  echo "$OUT/$archive"
}

goos_of() {
  case $1 in
    macos) echo darwin ;;
    linux|windows) echo "$1" ;;
    *) echo "unknown system: $1 (macos, linux or windows)" >&2; exit 2 ;;
  esac
}

case "${1:-}" in
  "") build "$(go env GOOS)" "$(go env GOARCH)" ;;
  all)
    for os in darwin linux windows; do
      for arch in amd64 arm64; do build "$os" "$arch"; done
    done
    ;;
  *) build "$(goos_of "$1")" "${2:-$(go env GOARCH)}" ;;
esac
