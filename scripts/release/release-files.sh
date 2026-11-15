#!/usr/bin/env bash
# Finishes a release in dist/ from packages that are already built there, by this
# machine (scripts/release/publish-packages.sh) or by GitHub Actions (.github/workflows/
# release-client.yml), and prints the release's files, one per line:
#   scripts/release/release-files.sh <version> [--with-dev-cli] [--unsigned]
#   scripts/release/release-files.sh <version> --check      only check the key and the tree
#
# The packages: the desktop installers (.dmg, .exe, .deb), the macOS .app.zip and
# the command-line archives, for three systems and two architectures each, 14 in
# all; --with-dev-cli adds the six dev command-line archives. Beside them it writes:
#   SHA256SUMS-<version>   afresh, for exactly these packages
#   SIGNATURES-<version>   the release key's signature of each (scripts/release/sign-packages.go).
#                          The key is $ST_RELEASE_KEY, by default local/keys/release-signing.pem,
#                          and must be the one whose public half the clients carry.
#                          --unsigned leaves it out (a trial run; nothing to publish).
#   RELEASE-<version>.json version, commit ($ST_COMMIT, else HEAD) and $ST_BUILD_TIME,
#                          which must be the build time every package was built with.
#   RELEASE-<version>.<lang>.md  the release notes, <lang>.md in $ST_RELEASE_NOTES (by default
#                          local/release-notes/<version>, outside the repository) if there are
#                          any, checked for size and summary length.
set -euo pipefail
cd "$(dirname "$0")/../.."
V=${1:-}
[ -n "$V" ] && [ "${V#-}" = "$V" ] || { sed -n 5,6p "$0" >&2; exit 2; }
shift
WITH_DEV=; UNSIGNED=; CHECK=
while [ $# -gt 0 ]; do
  case $1 in
    --with-dev-cli) WITH_DEV=1 ;;
    --unsigned) UNSIGNED=1 ;;
    --check) CHECK=1 ;;
    *) echo "unknown option $1" >&2; exit 2 ;;
  esac
  shift
done
KEY=${ST_RELEASE_KEY:-local/keys/release-signing.pem}
NOTES=${ST_RELEASE_NOTES:-local/release-notes/$V}

check() {
  if [ -z "$UNSIGNED" ]; then
    [ -r "$KEY" ] || { echo "no release key at $KEY (set ST_RELEASE_KEY)" >&2; exit 2; }
    [ "$(go run scripts/release/sign-packages.go pub "$KEY")" = "$(sed -n 's/^const ReleaseKey = "\(.*\)"$/\1/p' internal/client/upgrade.go)" ] || { echo "$KEY is not the release key the clients carry" >&2; exit 2; }
  fi
  [ "$(scripts/build/version.sh)" = "$V" ] || { echo "this tree is $(scripts/build/version.sh), not $V" >&2; exit 2; }
}
check
[ -z "$CHECK" ] || exit 0
: "${ST_BUILD_TIME:?set ST_BUILD_TIME to the build time the packages were built with}"
COMMIT=${ST_COMMIT:-$(git rev-parse --short=7 HEAD)}

shopt -s nullglob
FILES=()
want() { # how many, description, files...
  local n=$1 what=$2; shift 2
  [ $# = "$n" ] || { echo "expected $n $what for $V in dist/, found $#${1:+: $*}" >&2; exit 1; }
  FILES+=("$@")
}
want 6 "command-line archives" dist/sharetoken-cli-"$V"-*.tar.gz dist/sharetoken-cli-"$V"-*.zip
want 2 "macOS disk images" dist/sharetoken-"$V"-macos-*.dmg
want 2 "zipped macOS apps" dist/sharetoken-"$V"-macos-*.app.zip
want 2 "Windows installers" dist/sharetoken-"$V"-windows-*.exe
want 2 "Linux packages" dist/sharetoken-"$V"-linux-*.deb
[ -z "$WITH_DEV" ] || want 6 "dev command-line archives" dist/sharetoken-cli-dev-"$V"-*.tar.gz dist/sharetoken-cli-dev-"$V"-*.zip

# The first paragraph line of the notes is shown on one line: keep it short.
# Characters are counted as UTF-8 lead bytes, so the locale does not matter.
for f in "$NOTES"/*.md; do
  [ -e "$f" ] || continue # notes are optional: no notes directory means none
  lang=$(basename "$f" .md)
  [ "$(wc -c < "$f")" -le 4096 ] || { echo "$f is larger than 4096 bytes" >&2; exit 2; }
  summary=$(grep -v -E '^[[:space:]]*($|#|[-*] )' "$f" | head -1 || true)
  limit=90; case $lang in zh*|ja|ko) limit=30;; esac
  [ "$(printf %s "$summary" | LC_ALL=C tr -d '\200-\277' | wc -c)" -le "$limit" ] || { echo "$f: the summary line is longer than $limit characters: $summary" >&2; exit 2; }
done

rm -f "dist/SHA256SUMS-$V" "dist/SIGNATURES-$V" "dist/RELEASE-$V.json" dist/RELEASE-"$V".*.md
(cd dist && for f in "${FILES[@]}"; do shasum -a 256 "${f#dist/}"; done | sort -k2 > "SHA256SUMS-$V")
OUT=("${FILES[@]}" "dist/SHA256SUMS-$V")
if [ -z "$UNSIGNED" ]; then
  go run scripts/release/sign-packages.go sign "$KEY" "dist/SIGNATURES-$V" "${FILES[@]}"
  OUT+=("dist/SIGNATURES-$V")
fi
printf '{"version":"%s","commit":"%s","build_time":"%s"}\n' "$V" "$COMMIT" "$ST_BUILD_TIME" > "dist/RELEASE-$V.json"
OUT+=("dist/RELEASE-$V.json")
for f in "$NOTES"/*.md; do
  [ -e "$f" ] || continue
  cp "$f" "dist/RELEASE-$V.$(basename "$f" .md).md"
  OUT+=("dist/RELEASE-$V.$(basename "$f" .md).md")
done
printf '%s\n' "${OUT[@]}"
