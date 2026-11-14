#!/usr/bin/env bash
# Builds the packages users download and puts them where a portal offers them (the
# downloads.dir of its config), replacing the directory only once all have arrived:
# the desktop installers (.dmg, .exe, .deb) and the command-line archives, for three
# systems and two architectures each. Runs on a Mac: the .dmg needs hdiutil.
#   scripts/publish-packages.sh <version> <user@host> <directory> [--with-dev-cli] [--owner <user>]
# --with-dev-cli adds the dev command-line packages, for testing against the dev
# network. The portal does not list them, but serves them by name.
# Beside them goes RELEASE-<version>.json: version, commit and the build time all
# packages share. The portal reads it to tell clients about the release
# (GET /api/v1/update); the version alone may not tell 1.0.0 builds apart, and the commit and
# build time are what tell a client a newer build exists.
# release-notes/<lang>.md, when there, go along as RELEASE-<version>.<lang>.md: what
# this build brings, shown by the clients that are offered it (docs/versioning.md).
# SIGNATURES-<version> goes along too: the release key's signature of every package
# (scripts/sign-packages.go), without which clients do not install a package by
# themselves (one-click update). The key is $ST_RELEASE_KEY, by default
# local/keys/release-signing.pem; its public half is built into the clients.
# --owner names the portal's service user (for example st-portal):
# the files are staged in the login user's home and moved into place with sudo, owned by it.
#
# The steps are scripts of their own, which GitHub Actions runs as well
# (.github/workflows/release-client.yml): the build scripts, then
# scripts/release-files.sh (checksums, signatures, RELEASE files), then
# scripts/upload-release.sh (to the server).
set -euo pipefail
[ $# -ge 3 ] || { sed -n 6p "$0" >&2; exit 2; }
V=$1 TARGET=$2 DIR=$3; shift 3
WITH_DEV=; OWNER=
while [ $# -gt 0 ]; do case $1 in --with-dev-cli) WITH_DEV=1;; --owner) OWNER=$2; shift;; *) echo "unknown option $1" >&2; exit 2;; esac; shift; done
cd "$(dirname "$0")/.."
scripts/release-files.sh "$V" --check
# The version does not identify the code before launch, so nothing is reused.
export ST_BUILD_TIME=${ST_BUILD_TIME:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}
rm -rf "dist/SHA256SUMS-$V" "dist/SIGNATURES-$V" "dist/RELEASE-$V.json" dist/RELEASE-"$V".*.md dist/sharetoken-cli-* dist/sharetoken-[0-9]* dist/sharetoken-dev-[0-9]*
scripts/build-cli.sh prod all >/dev/null
for arch in amd64 arm64; do
  scripts/build-gui.sh prod macos "$arch" >/dev/null
  scripts/build-gui.sh prod windows "$arch" >/dev/null
  scripts/build-gui-linux.sh prod "$arch" >/dev/null
done
[ -z "$WITH_DEV" ] || scripts/build-cli.sh dev all >/dev/null
scripts/release-files.sh "$V" ${WITH_DEV:+--with-dev-cli} >/dev/null
scripts/upload-release.sh "$V" "$TARGET" "$DIR" ${OWNER:+--owner "$OWNER"}
