#!/usr/bin/env bash
# Builds the Linux desktop package in a throwaway Docker container, from any
# system, for either architecture, and copies it to dist/. Only committed files
# go in, so secrets and other ignored files stay out.
#   scripts/build/build-gui-linux.sh <dev|prod> [amd64|arm64]
#   scripts/build/build-gui-linux.sh --image <amd64|arm64>    only make the builder image
set -euo pipefail
cd "$(dirname "$0")/../.."
image() { docker build -q --platform "linux/$1" -t "sharetoken-gui-builder:$1" -f scripts/build/gui-builder.Dockerfile scripts >/dev/null; }
if [ "${1:-}" = --image ]; then image "$2"; exit 0; fi
ENV=${1:-}
case $ENV in
  dev|prod) ;;
  *) sed -n 5,6p "$0" >&2; exit 2 ;;
esac
ARCH=${2:-$(go env GOARCH)}
OUT=dist; VERSION=$(scripts/build/version.sh); COMMIT=$(git rev-parse --short=7 HEAD)
SUFFIX=-$ENV; [ "$ENV" = prod ] && SUFFIX=
DEB=sharetoken$SUFFIX-$VERSION-linux-$ARCH.deb
image "$ARCH"
mkdir -p "$OUT"
# Modules and the build cache stay in named volumes; everything else goes with the container.
git archive HEAD | docker run --rm -i --platform "linux/$ARCH" \
  -e ST_VERSION="$VERSION" -e ST_COMMIT="$COMMIT" -e ST_BUILD_TIME="${ST_BUILD_TIME:-}" \
  -v st-gui-gomod:/root/go/pkg/mod -v "st-gui-gocache-$ARCH:/root/.cache/go-build" \
  "sharetoken-gui-builder:$ARCH" \
  sh -c "mkdir /src && cd /src && tar -x && scripts/build/build-gui.sh $ENV >&2 && cat dist/$DEB" > "$OUT/$DEB.tmp"
mv "$OUT/$DEB.tmp" "$OUT/$DEB"
(cd "$OUT" && { cat "SHA256SUMS-$VERSION" 2>/dev/null; shasum -a 256 "$DEB"; } | sort -u -k2 > "SHA256SUMS-$VERSION.tmp" && mv "SHA256SUMS-$VERSION.tmp" "SHA256SUMS-$VERSION")
echo "$OUT/$DEB"
