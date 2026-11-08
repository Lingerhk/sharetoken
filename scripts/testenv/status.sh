#!/bin/bash
# One line per test user: coordinator connection, relay in use, sharing stats.
. "$(dirname "$0")/env.sh"
line() { grep -E "^coordinator|^sharing" | tr '\n' ' ' | sed 's/  */ /g'; echo; }
printf "%-12s" "alice(mac)"; SHARETOKEN_HOME=$W/alice $W/sharetoken status 2>&1 | line
for hu in $REMOTE_USERS; do printf "%-12s" "${hu##*:}"; T=20 rsh ${hu%%:*} "SHARETOKEN_HOME=\$HOME/st-test/${hu##*:} ~/st-test/sharetoken status" 2>&1 | line; done
printf "%-12s" "bob(orb)";  orbsh 'SHARETOKEN_HOME=$HOME/st-test/bob ~/st-test/sharetoken status' 2>&1 | line
printf "%-12s" "ivy(ibox)"; iboxsh 'SHARETOKEN_HOME=$HOME/st-test/ivy ~/st-test/sharetoken status' 2>&1 | line
for h in $US $SG $CN $CN2; do printf "relay %-15s " $h; T=15 rsh $h 'curl -s 127.0.0.1:8093' | head -c 300; echo; done
