#!/usr/bin/env bash
# Builds server programs into dist/ with the version in their name and inside them:
#   GOOS=linux GOARCH=arm64 scripts/build/build.sh portal coordinator mailserver
#     -> dist/portal-1.0.0-linux-arm64 ...
# A CPU level above the baseline (GOAMD64=v2..v4, GOARM64=v8.1.. ) is passed on
# to go build and named in the file: GOAMD64=v3 -> dist/portal-1.0.0-linux-amd64v3.
# Such a binary refuses to start on an older CPU, so only build it for a known host.
# Prints one output path per line. The desktop app has scripts/build-gui.sh.
set -euo pipefail
cd "$(dirname "$0")/../.."
[ $# -gt 0 ] || { sed -n 2,5p "$0" >&2; exit 2; }
V=$(scripts/build/version.sh)
OS=${GOOS:-$(go env GOOS)}; ARCH=${GOARCH:-$(go env GOARCH)}
LD="-s -w $(scripts/build/ldflags.sh)"
LEVEL=
case $ARCH in
  amd64) [ "${GOAMD64:-v1}" = v1 ] || LEVEL=$GOAMD64 ;;
  arm64) [ "${GOARM64:-v8.0}" = v8.0 ] || LEVEL=${GOARM64%%,*} ;;
esac
mkdir -p dist
for c in "$@"; do
  out=dist/$c-$V-$OS-$ARCH$LEVEL
  GOOS=$OS GOARCH=$ARCH CGO_ENABLED=0 go build -trimpath -ldflags "$LD" -o "$out" "./cmd/$c"
  grep -aq "$V" "$out" || { echo "$out does not carry version $V" >&2; exit 1; }
  (cd dist && { cat "SHA256SUMS-$V" 2>/dev/null; shasum -a 256 "$(basename "$out")"; } | sort -u -k2 > "SHA256SUMS-$V.tmp" && mv "SHA256SUMS-$V.tmp" "SHA256SUMS-$V")
  echo "$out"
done
