#!/usr/bin/env bash
# Builds server programs into dist/ with the version in their name and inside them:
#   GOOS=linux GOARCH=arm64 scripts/build.sh portal coordinator mailserver
#     -> dist/portal-1.0.0-linux-arm64 ...
# Prints one output path per line. The desktop app has scripts/build-gui.sh.
set -euo pipefail
cd "$(dirname "$0")/.."
[ $# -gt 0 ] || { sed -n 2,5p "$0" >&2; exit 2; }
V=$(scripts/version.sh)
OS=${GOOS:-$(go env GOOS)}; ARCH=${GOARCH:-$(go env GOARCH)}
LD="-s -w $(scripts/ldflags.sh)"
mkdir -p dist
for c in "$@"; do
  out=dist/$c-$V-$OS-$ARCH
  GOOS=$OS GOARCH=$ARCH CGO_ENABLED=0 go build -trimpath -ldflags "$LD" -o "$out" "./cmd/$c"
  grep -aq "$V" "$out" || { echo "$out does not carry version $V" >&2; exit 1; }
  (cd dist && shasum -a 256 "$(basename "$out")" >> "SHA256SUMS-$V.tmp" && sort -u -k2 "SHA256SUMS-$V.tmp" > "SHA256SUMS-$V" && rm "SHA256SUMS-$V.tmp") 
  echo "$out"
done
