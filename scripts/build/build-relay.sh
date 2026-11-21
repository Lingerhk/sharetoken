#!/usr/bin/env bash
# Builds the relay as a package users download to run a relay of their own (a
# private relay, docs: /docs/relay) into dist/:
#   scripts/build/build-relay.sh                     Linux, both architectures
#   scripts/build/build-relay.sh <amd64|arm64>       one of them
#
# Each makes dist/sharetoken-relay-<version>-linux-<arch>.tar.gz, listed in
# dist/SHA256SUMS-<version>: the program sharetoken-relay (cmd/relay, the same
# program the network's own relays run), a systemd unit and a README. Relays
# run on servers, so there is no macOS or Windows package: `go build ./cmd/relay`
# makes one for anything else.
set -euo pipefail
cd "$(dirname "$0")/../.."
OUT=dist
VERSION=$(scripts/build/version.sh)
LDFLAGS="-s -w $(scripts/build/ldflags.sh)"
mkdir -p "$OUT"

build() { # architecture
  local arch=$1 base dir archive
  base=sharetoken-relay-$VERSION-linux-$arch
  dir=$OUT/$base
  rm -rf "$dir"
  mkdir -p "$dir"
  CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags "$LDFLAGS" -o "$dir/sharetoken-relay" ./cmd/relay
  LC_ALL=C grep -aq "$VERSION" "$dir/sharetoken-relay" || { echo "sharetoken-relay does not carry version $VERSION" >&2; exit 1; }
  sed -e "s/@VERSION@/$VERSION/g" scripts/build/relay-readme.txt > "$dir/README.txt"
  cp scripts/build/sharetoken-relay.service "$dir/"
  archive=$base.tar.gz
  COPYFILE_DISABLE=1 tar --no-xattrs -C "$OUT" -czf "$OUT/$archive" "$base"
  (cd "$OUT" && { cat "SHA256SUMS-$VERSION" 2>/dev/null; shasum -a 256 "$archive"; } | sort -u -k2 > "SHA256SUMS-$VERSION.tmp" && mv "SHA256SUMS-$VERSION.tmp" "SHA256SUMS-$VERSION")
  rm -rf "$dir"
  echo "$OUT/$archive"
}

case "${1:-}" in
  "") build amd64; build arm64 ;;
  amd64|arm64) build "$1" ;;
  *) sed -n 2,5p "$0" >&2; exit 2 ;;
esac
