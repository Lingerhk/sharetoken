#!/usr/bin/env bash
# Prints the version of this tree: VERSION on its release tag with nothing
# changed, otherwise VERSION-dev.<commit>[.dirty]. $ST_VERSION overrides it, for
# building from a copy without .git.
set -euo pipefail
cd "$(dirname "$0")/.."
[ -z "${ST_VERSION:-}" ] || { echo "$ST_VERSION"; exit 0; }
V=$(tr -d ' \n' < VERSION)
DIRTY=$(git status --porcelain --untracked-files=no 2>/dev/null | head -1)
if [ -z "$DIRTY" ] && [ "$(git describe --exact-match --tags HEAD 2>/dev/null)" = "v$V" ]; then
  echo "$V"
else
  echo "$V-dev.$(git rev-parse --short=7 HEAD 2>/dev/null || echo nogit)${DIRTY:+.dirty}"
fi
