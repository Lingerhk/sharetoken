#!/usr/bin/env bash
# Prints the -X flags that stamp this tree's version into a Go program.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
P=sharetoken/internal/version
COMMIT=${ST_COMMIT:-$(git -C "$HERE" rev-parse --short=7 HEAD 2>/dev/null || echo unknown)}
echo "-X $P.Version=$("$HERE/version.sh") -X $P.Commit=$COMMIT -X $P.BuildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
