#!/usr/bin/env bash
# Builds the packages users download and puts them where a portal offers them (the
# downloads.dir of its config), replacing the directory only once all have arrived:
# the desktop installers (.dmg, .exe, .deb) and the command-line archives, for three
# systems and two architectures each. Runs on a Mac: the .dmg needs hdiutil.
#   scripts/publish-packages.sh <version> <user@host> <directory> [--with-dev-cli]
# --with-dev-cli adds the dev command-line packages, for testing against the dev
# network. The portal does not list them, but serves them by name.
set -euo pipefail
V=$1 TARGET=$2 DIR=$3 WITH_DEV=${4:-}
cd "$(dirname "$0")/.."
[ "$(scripts/version.sh)" = "$V" ] || { echo "this tree is $(scripts/version.sh), not $V" >&2; exit 2; }
Q="-o LogLevel=ERROR -o ConnectTimeout=10"
# The version does not identify the code before launch, so nothing is reused.
rm -f "dist/SHA256SUMS-$V" dist/sharetoken-cli-* dist/sharetoken-[0-9]* dist/sharetoken-dev-[0-9]*
scripts/build-cli.sh prod all >/dev/null
for arch in amd64 arm64; do
  scripts/build-gui.sh prod macos "$arch" >/dev/null
  scripts/build-gui.sh prod windows "$arch" >/dev/null
  scripts/build-gui-linux.sh prod "$arch" >/dev/null
done
FILES=(dist/sharetoken-cli-"$V"-* dist/sharetoken-"$V"-*.dmg dist/sharetoken-"$V"-*.exe dist/sharetoken-"$V"-*.deb)
[ "${#FILES[@]}" = 12 ] || { echo "expected 12 packages for $V, built ${#FILES[@]}" >&2; exit 1; }
if [ "$WITH_DEV" = --with-dev-cli ]; then
  scripts/build-cli.sh dev all >/dev/null
  FILES+=(dist/sharetoken-cli-dev-"$V"-*)
  [ "${#FILES[@]}" = 18 ] || { echo "expected 18 packages for $V, built ${#FILES[@]}" >&2; exit 1; }
fi
ssh $Q -n "$TARGET" "mkdir -p '$DIR.new' && rm -f '$DIR.new'/*"
for f in "${FILES[@]}" "dist/SHA256SUMS-$V"; do
  scp $Q "$f" "$TARGET:$DIR.new/" >/dev/null
done
# Checks on the server what arrived, then swaps the directory in one step.
ssh $Q -n "$TARGET" "cd '$DIR.new' && sha256sum --quiet -c 'SHA256SUMS-$V' && chmod 644 * && rm -rf '$DIR.old' && { [ ! -e '$DIR' ] || mv '$DIR' '$DIR.old'; } && mv '$DIR.new' '$DIR' && rm -rf '$DIR.old'"
echo "published ${#FILES[@]} packages to $TARGET:$DIR"
