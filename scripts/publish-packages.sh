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
# (GET /api/v1/update); before launch the version stays 1.0.0, and the commit and
# build time are what tell a client a newer build exists.
# --owner names the portal's service user (st-portal, see scripts/ops/st-host-setup.sh):
# the files are staged in the login user's home and moved into place with sudo, owned by it.
set -euo pipefail
V=$1 TARGET=$2 DIR=$3; shift 3
WITH_DEV=; OWNER=
while [ $# -gt 0 ]; do case $1 in --with-dev-cli) WITH_DEV=1;; --owner) OWNER=$2; shift;; *) echo "unknown option $1" >&2; exit 2;; esac; shift; done
cd "$(dirname "$0")/.."
[ "$(scripts/version.sh)" = "$V" ] || { echo "this tree is $(scripts/version.sh), not $V" >&2; exit 2; }
Q="-o LogLevel=ERROR -o ConnectTimeout=10"
# The version does not identify the code before launch, so nothing is reused.
COMMIT=$(git rev-parse --short=7 HEAD)
export ST_BUILD_TIME=${ST_BUILD_TIME:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}
rm -rf "dist/SHA256SUMS-$V" "dist/RELEASE-$V.json" dist/sharetoken-cli-* dist/sharetoken-[0-9]* dist/sharetoken-dev-[0-9]*
scripts/build-cli.sh prod all >/dev/null
for arch in amd64 arm64; do
  scripts/build-gui.sh prod macos "$arch" >/dev/null
  scripts/build-gui.sh prod windows "$arch" >/dev/null
  scripts/build-gui-linux.sh prod "$arch" >/dev/null
done
FILES=(dist/sharetoken-cli-"$V"-* dist/sharetoken-"$V"-*.dmg dist/sharetoken-"$V"-*.exe dist/sharetoken-"$V"-*.deb)
[ "${#FILES[@]}" = 12 ] || { echo "expected 12 packages for $V, built ${#FILES[@]}" >&2; exit 1; }
if [ -n "$WITH_DEV" ]; then
  scripts/build-cli.sh dev all >/dev/null
  FILES+=(dist/sharetoken-cli-dev-"$V"-*)
  [ "${#FILES[@]}" = 18 ] || { echo "expected 18 packages for $V, built ${#FILES[@]}" >&2; exit 1; }
fi
printf '{"version":"%s","commit":"%s","build_time":"%s"}\n' "$V" "$COMMIT" "$ST_BUILD_TIME" > "dist/RELEASE-$V.json"
STAGE=$DIR.new
[ -z "$OWNER" ] || STAGE=st-downloads.new   # relative to the login user's home
ssh $Q -n "$TARGET" "mkdir -p '$STAGE' && rm -f '$STAGE'/*"
for f in "${FILES[@]}" "dist/SHA256SUMS-$V" "dist/RELEASE-$V.json"; do
  scp $Q "$f" "$TARGET:$STAGE/" >/dev/null
done
# Checks on the server what arrived, then swaps the directory in one step.
if [ -z "$OWNER" ]; then
  ssh $Q -n "$TARGET" "cd '$DIR.new' && sha256sum --quiet -c 'SHA256SUMS-$V' && chmod 644 * && rm -rf '$DIR.old' && { [ ! -e '$DIR' ] || mv '$DIR' '$DIR.old'; } && mv '$DIR.new' '$DIR' && rm -rf '$DIR.old'"
else
  ssh $Q -n "$TARGET" "cd '$STAGE' && sha256sum --quiet -c 'SHA256SUMS-$V' && sudo install -d -o $OWNER -g $OWNER -m 755 '$DIR.new' && sudo install -o $OWNER -g $OWNER -m 644 ./* '$DIR.new/' && sudo rm -rf '$DIR.old' && { [ ! -e '$DIR' ] || sudo mv '$DIR' '$DIR.old'; } && sudo mv '$DIR.new' '$DIR' && sudo rm -rf '$DIR.old' && cd && rm -rf '$STAGE'"
fi
echo "published ${#FILES[@]} packages to $TARGET:$DIR"
