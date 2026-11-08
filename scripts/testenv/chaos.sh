#!/bin/bash
# Fault-injection run: steady load from 4 buyers + scheduled failures (DUR seconds, default 1080).
# Kills/restarts relays, the coordinator, a seller, the portal and a model server; see docs/test-env.md.
# Output: $W/results/chaos/ (events.log + one request log per buyer); analyse with chaos_report.py.
. "$(dirname "$0")/env.sh"

D=${DUR:-1080}
OUT=$W/results/chaos; rm -rf $OUT; mkdir -p $OUT
EV=$OUT/events.log
ev() { echo "$(date +%s) $*" | tee -a $EV; }
# verify HOST PROCESS LABEL: 15s after a restart, record whether it is running
# (in the background, so the schedule does not shift).
verify() { ( sleep 15; if [ "$1" = "$STDB" ]; then T=20 stdbsh "pgrep -x $2 > /dev/null"; else T=20 rsh $1 "pgrep -x $2 > /dev/null"; fi; if [ $? = 0 ]; then ev "  verify: $3 up"; else ev "  verify: $3 NOT RUNNING"; fi ) & }

ev start
# load
timeout $((D+120)) ssh $Q -n s0nnet@$US "cd ~/st-test && rm -f chaos.log; python3 st_bench.py load -m mock-fast -c 4 -d $D --log chaos.log" > $OUT/uma.json 2> $OUT/uma.slow &
orb run -m ada-dev bash -c "cd ~/st-test && rm -f chaos.log; python3 st_bench.py -b http://127.0.0.1:9788 load -m mock-fast -c 4 -d $D --log chaos.log" > $OUT/bob.json 2> $OUT/bob.slow &
orb run -m ada-dev bash -c "timeout $((D+120)) instabox ssh srv-01 -- 'cd ~/st-test && rm -f chaos.log; python3 st_bench.py load -m mock-fast -c 4 -d $D --stream --log chaos.log'" > $OUT/ivy.json 2> $OUT/ivy.slow &
python3 $REPO/scripts/st_bench.py load -m qwen2.5:0.5b -c 2 -d $D --stream --max-tokens 64 --log $OUT/alice.log > $OUT/alice.json 2> $OUT/alice.slow &

sleep 60;  ev kill relay-sg;        timeout 20 ssh $Q -n s0nnet@$SG 'pkill -x relay'
sleep 90;  ev restart relay-sg;     relay_start $SG $SG6; verify $SG relay relay-sg
sleep 90;  ev kill -9 coordinator;  stdbsh 'pkill -9 -x coordinator'
sleep 10;  ev restart coordinator;  coord_start; verify $STDB coordinator coordinator
sleep 110; ev kill -9 seller-sam;   timeout 20 ssh $Q -n s0nnet@$SG 'pkill -9 -x sharetoken'
sleep 60;  ev restart seller-sam;   timeout 20 ssh $Q -n s0nnet@$SG 'cd ~/st-test && SHARETOKEN_HOME=$HOME/st-test/sam setsid nohup ./sharetoken up >> sam.log 2>&1 < /dev/null & sleep 1'; verify $SG sharetoken seller-sam
sleep 60;  ev restart portal;       stdbsh 'pkill -x portal'; sleep 1; portal_start; verify $STDB portal portal
sleep 120; ev kill carol-upstream;  timeout 20 ssh $Q -n s0nnet@119.29.250.173 'for p in $(pgrep -u s0nnet -x python3); do grep -q mock_upstream /proc/$p/cmdline && kill $p; done'
sleep 60;  ev restart carol-upstream; timeout 20 ssh $Q -n s0nnet@119.29.250.173 'cd ~/st-test && MOCK_WORDS=48 MOCK_DELAY_MS=5 setsid nohup python3 mock_upstream.py 18080 mock-fast >> mock.log 2>&1 < /dev/null & sleep 1'
sleep 60;  ev kill relay-us;        timeout 20 ssh $Q -n s0nnet@$US 'pkill -x relay'
sleep 80;  ev restart relay-us;     relay_start $US $US6; verify $US relay relay-us
wait
ev end
timeout 30 ssh $Q -n s0nnet@$US 'cat ~/st-test/chaos.log' > $OUT/uma.log
orb run -m ada-dev bash -c 'cat ~/st-test/chaos.log' > $OUT/bob.log
orb run -m ada-dev bash -c "timeout 60 instabox ssh srv-01 -- 'cat ~/st-test/chaos.log'" > $OUT/ivy.log
echo done

