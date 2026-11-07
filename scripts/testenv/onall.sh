#!/bin/bash
# onall.sh TAG <st_bench.py args>: run the same st_bench command on all 6 buyers at once.
# Results: $W/results/TAG-<user>.jsonl (+ .slow); prints one summary line per buyer.
. "$(dirname "$0")/env.sh"
TAG=$1; shift; ARGS="$*"
python3 $REPO/scripts/st_bench.py $ARGS > $W/results/$TAG-alice.jsonl 2> $W/results/$TAG-alice.slow &
for hu in $REMOTE_USERS; do T=7200 rsh ${hu%%:*} "cd ~/st-test && python3 st_bench.py $ARGS" > $W/results/$TAG-${hu##*:}.jsonl 2> $W/results/$TAG-${hu##*:}.slow & done
orbsh "cd ~/st-test && python3 st_bench.py -b http://127.0.0.1:9788 $ARGS" > $W/results/$TAG-bob.jsonl 2> $W/results/$TAG-bob.slow &
T=7200 iboxsh "cd ~/st-test && python3 st_bench.py $ARGS" > $W/results/$TAG-ivy.jsonl 2> $W/results/$TAG-ivy.slow &
wait
for f in $W/results/$TAG-*.jsonl; do u=$(basename $f .jsonl); printf "%-6s " ${u#$TAG-}; tail -1 $f | python3 -c '
import json,sys
l=sys.stdin.read().strip()
try:
  r=json.loads(l); print("ok %d/%d (%.1f%%) rps=%s p50=%s p90=%s p99=%s ttft50=%s codes=%s" % (r["ok"],r["requests"],r["success_pct"],r["rps"],r["lat_p50"],r["lat_p90"],r["lat_p99"],r["ttft_p50"],r["codes"]))
except Exception: print("RAW", l[:300])'; done
