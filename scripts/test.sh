#!/usr/bin/env bash
# Runs the Go tests against the local MySQL (scripts/local-mysql.sh).
#   scripts/test.sh                      # go test ./...
#   scripts/test.sh -race ./coordinator  # arguments go to go test
set -euo pipefail
cd "$(dirname "$0")/.."
export SHARETOKEN_TEST_MYSQL SHARETOKEN_DB_PASSWORD=
SHARETOKEN_TEST_MYSQL=$(scripts/local-mysql.sh server)
[ $# -gt 0 ] || set -- ./...
exec go test "$@"
