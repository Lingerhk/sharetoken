#!/usr/bin/env bash
# Puts a release finished in dist/ (scripts/release-files.sh) where a portal offers
# it (the downloads.dir of its config), replacing the directory only once all files
# have arrived and their checksums match on the server:
#   scripts/upload-release.sh <version> <user@host> <directory> [--owner <user>]
# The files: the packages listed in SHA256SUMS-<version>, that file, SIGNATURES-<version>,
# RELEASE-<version>.json and the release notes RELEASE-<version>.<lang>.md. Every
# package must carry a signature that the public key built into the clients
# accepts, or nothing is sent.
# --owner names the portal's service user (for example st-portal):
# the files are staged in the login user's home and moved into place with sudo, owned by it.
set -euo pipefail
[ $# -ge 3 ] || { sed -n 5p "$0" >&2; exit 2; }
V=$1 TARGET=$2 DIR=$3; shift 3
OWNER=
while [ $# -gt 0 ]; do case $1 in --owner) OWNER=$2; shift;; *) echo "unknown option $1" >&2; exit 2;; esac; shift; done
cd "$(dirname "$0")/.."
for f in "dist/SHA256SUMS-$V" "dist/SIGNATURES-$V" "dist/RELEASE-$V.json"; do
  [ -r "$f" ] || { echo "$f is missing: run scripts/release-files.sh $V first" >&2; exit 2; }
done
PACKAGES=()
while read -r _ name; do PACKAGES+=("dist/$name"); done < "dist/SHA256SUMS-$V"
[ "${#PACKAGES[@]}" -gt 0 ] || { echo "dist/SHA256SUMS-$V lists no packages" >&2; exit 2; }
(cd dist && shasum -a 256 --quiet -c "SHA256SUMS-$V")
go run scripts/sign-packages.go verify "$(sed -n 's/^const ReleaseKey = "\(.*\)"$/\1/p' internal/client/upgrade.go)" "dist/SIGNATURES-$V" "${PACKAGES[@]}"
FILES=("${PACKAGES[@]}" "dist/SHA256SUMS-$V" "dist/SIGNATURES-$V" "dist/RELEASE-$V.json")
for f in dist/RELEASE-"$V".*.md; do [ -e "$f" ] && FILES+=("$f"); done

Q="-o LogLevel=ERROR -o ConnectTimeout=10"
STAGE=$DIR.new
[ -z "$OWNER" ] || STAGE=st-downloads.new   # relative to the login user's home
ssh $Q -n "$TARGET" "mkdir -p '$STAGE' && rm -f '$STAGE'/*"
for f in "${FILES[@]}"; do
  scp $Q "$f" "$TARGET:$STAGE/" >/dev/null
done
# Checks on the server what arrived, then swaps the directory in one step.
if [ -z "$OWNER" ]; then
  ssh $Q -n "$TARGET" "cd '$DIR.new' && sha256sum --quiet -c 'SHA256SUMS-$V' && chmod 644 * && rm -rf '$DIR.old' && { [ ! -e '$DIR' ] || mv '$DIR' '$DIR.old'; } && mv '$DIR.new' '$DIR' && rm -rf '$DIR.old'"
else
  ssh $Q -n "$TARGET" "cd '$STAGE' && sha256sum --quiet -c 'SHA256SUMS-$V' && sudo install -d -o $OWNER -g $OWNER -m 755 '$DIR.new' && sudo install -o $OWNER -g $OWNER -m 644 ./* '$DIR.new/' && sudo rm -rf '$DIR.old' && { [ ! -e '$DIR' ] || sudo mv '$DIR' '$DIR.old'; } && sudo mv '$DIR.new' '$DIR' && sudo rm -rf '$DIR.old' && cd && rm -rf '$STAGE'"
fi
echo "published ${#PACKAGES[@]} packages to $TARGET:$DIR"
