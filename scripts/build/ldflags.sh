#!/usr/bin/env bash
# Prints the -X flags that stamp this tree's version into a Go program.
# $ST_BUILD_TIME fixes the build time, so that every package of one release has
# the same (scripts/release/publish-packages.sh), and clients can tell a rebuild is newer.
#   scripts/build/ldflags.sh             the version flags (servers, tools)
#   scripts/build/ldflags.sh dev|prod    the flags of the sharetoken client for that build
#                                  type: stripped (-s -w), with the version and the
#                                  build type (scripts/build/build-gui.sh, build-cli.sh)
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
P=sharetoken/internal/version
COMMIT=${ST_COMMIT:-$(git -C "$HERE" rev-parse --short=7 HEAD 2>/dev/null || echo unknown)}
FLAGS="-X $P.Version=$("$HERE/version.sh") -X $P.Commit=$COMMIT -X $P.BuildTime=${ST_BUILD_TIME:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
case ${1:-} in
  "") echo "$FLAGS" ;;
  dev|prod) echo "-s -w $FLAGS -X sharetoken/internal/client.buildType=sharetoken-build-type=$1" ;;
  *) echo "ldflags.sh: build type must be dev or prod, not $1" >&2; exit 2 ;;
esac
