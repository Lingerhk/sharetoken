#!/usr/bin/env bash
# Runs the Go tests against the local MySQL (scripts/test/local-mysql.sh).
#   scripts/test/test.sh                      # go test ./...
#   scripts/test/test.sh -race ./coordinator  # arguments go to go test
set -euo pipefail
cd "$(dirname "$0")/../.."
export SHARETOKEN_TEST_MYSQL SHARETOKEN_DB_PASSWORD=
SHARETOKEN_TEST_MYSQL=$(scripts/test/local-mysql.sh server)
[ $# -gt 0 ] || set -- ./...
exec go test "$@"
