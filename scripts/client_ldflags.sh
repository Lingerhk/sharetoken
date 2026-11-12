#!/usr/bin/env bash
# Prints the -ldflags of the sharetoken client program for a build type, dev or
# prod: the version and the build type. It serves
# scripts/build-gui.sh and scripts/build-cli.sh.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
FLAGS="-s -w $("$HERE/ldflags.sh") -X sharetoken/internal/client.buildType=sharetoken-build-type=$1"
echo "$FLAGS"
