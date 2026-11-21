#!/usr/bin/env bash
# Prints the Go build tags of the sharetoken client (cmd/sharetoken), for
# `go build -tags "$(scripts/build/tags.sh)"`.
#
# The client's tailnet (internal/tailnet) uses tailscale.com as a library and
# needs only its data plane. The tags in client-tags.txt leave out every
# tailscale.com feature it does not use: the list the tailcat library is
# released with, plus ts_omit_ssh (tailcat's SSH services). Besides the size
# (some 3 MB), they keep the engine from asking the default gateway for port
# mappings and from probing for captive portals. A client built without them
# works the same.
#
# After moving to another tailcat version, write the file again:
#   printf '%s,ts_omit_ssh\n' "$(cat "$(go list -m -f '{{.Dir}}' github.com/tailscale/tailcat)/build-tags.txt")" > scripts/build/client-tags.txt
# internal/tailnet's tests fail while the two differ.
set -euo pipefail
cat "$(dirname "$0")/client-tags.txt"
