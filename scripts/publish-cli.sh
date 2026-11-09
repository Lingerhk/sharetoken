#!/usr/bin/env bash
# Builds the command-line packages and puts them where a portal offers them for
# download (the downloads.dir of its config), keeping the previous version's files
# only until the new ones are complete.
#   scripts/publish-cli.sh <dev|prod> <version> <user@host> <directory>
set -euo pipefail
ENV=$1 V=$2 TARGET=$3 DIR=$4
cd "$(dirname "$0")/.."
[ "$(scripts/version.sh)" = "$V" ] || { echo "this tree is $(scripts/version.sh), not $V" >&2; exit 2; }
Q="-o LogLevel=ERROR -o ConnectTimeout=10"
rm -f "dist/SHA256SUMS-$V"
scripts/build-cli.sh "$ENV" all >/dev/null
ssh $Q -n "$TARGET" "mkdir -p '$DIR.new' && rm -f '$DIR.new'/*"
n=0
for f in dist/sharetoken-cli-"$ENV"-"$V"-*; do
  scp $Q "$f" "$TARGET:$DIR.new/" >/dev/null
  n=$((n+1))
done
[ "$n" = 6 ] || { echo "expected 6 packages for $V, built $n" >&2; exit 1; }
scp $Q "dist/SHA256SUMS-$V" "$TARGET:$DIR.new/" >/dev/null
# Checks on the server what arrived, then swaps the directory in one step.
ssh $Q -n "$TARGET" "cd '$DIR.new' && sha256sum --quiet -c 'SHA256SUMS-$V' && chmod 644 * && rm -rf '$DIR.old' && { [ ! -e '$DIR' ] || mv '$DIR' '$DIR.old'; } && mv '$DIR.new' '$DIR' && rm -rf '$DIR.old'"
echo "published $n packages to $TARGET:$DIR"
