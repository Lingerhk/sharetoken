#!/usr/bin/env bash
# Prints the -ldflags of the sharetoken client program for a build type, dev or
# prod: the version, the build type, and where usage statistics go. It serves
# scripts/build-gui.sh and scripts/build-cli.sh. Statistics are sent by the
# `daemon` command of prod builds only (internal/client/telemetry.go). TELEMETRY_KEY=…
# replaces the key, TELEMETRY_OFF=1 builds a program that sends nothing, and
# TELEMETRY_HOST=https://… selects another instance.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
FLAGS="-s -w $("$HERE/ldflags.sh") -X sharetoken/internal/client.buildType=sharetoken-build-type=$1"
[ -n "${TELEMETRY_OFF:-}" ] && FLAGS="$FLAGS -X sharetoken/internal/client.TelemetryKey="
[ -n "${TELEMETRY_KEY:-}" ] && FLAGS="$FLAGS -X sharetoken/internal/client.TelemetryKey=$TELEMETRY_KEY"
[ -n "${TELEMETRY_HOST:-}" ] && FLAGS="$FLAGS -X sharetoken/internal/client.TelemetryHost=$TELEMETRY_HOST"
echo "$FLAGS"
