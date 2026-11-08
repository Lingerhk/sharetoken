#!/bin/bash
# sample.sh SECONDS [INTERVAL]: every INTERVAL seconds (default 60), record RSS (KB), CPU time and
# open file descriptors of every sharetoken process in the environment, as CSV lines:
#   epoch,host,process,rss_kb,cpu_time,fds
# into $W/results/samples.csv. Run it alongside a soak test.
. "$(dirname "$0")/env.sh"
DUR=${1:-1800}; IV=${2:-60}
OUT=$W/results/samples.csv
REMOTE='for n in coordinator portal relay sharetoken mysqld; do for p in $(pgrep -x $n); do echo "$(date +%s),$(hostname),$n,$(ps -o rss= -p $p | tr -d " "),$(ps -o time= -p $p | tr -d " "),$(ls /proc/$p/fd 2>/dev/null | wc -l)"; done; done'
end=$(( $(date +%s) + DUR ))
while [ $(date +%s) -lt $end ]; do
  for h in $US $SG $CN; do T=20 rsh $h "$REMOTE" >> $OUT 2>/dev/null & done
  orbsh "$REMOTE" >> $OUT 2>/dev/null &
  stdbsh "$REMOTE" >> $OUT 2>/dev/null &   # portal + coordinator + MySQL
  iboxsh "$REMOTE" >> $OUT 2>/dev/null &
  for p in $(pgrep -x sharetoken); do echo "$(date +%s),mac,sharetoken,$(ps -o rss= -p $p | tr -d ' '),$(ps -o time= -p $p | tr -d ' '),$(lsof -p $p 2>/dev/null | wc -l | tr -d ' ')"; done >> $OUT
  wait
  sleep $IV
done
