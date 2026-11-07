#!/bin/bash
# Copy st_bench.py and mock_upstream.py to every test machine.
. "$(dirname "$0")/env.sh"
for h in $US $SG $CN; do scp $Q $REPO/scripts/st_bench.py $REPO/scripts/mock_upstream.py s0nnet@$h:st-test/; done
orbsh "mkdir -p ~/st-test && cp $REPO/scripts/st_bench.py ~/st-test/"
orb run -m $ORB bash -c "cat $REPO/scripts/st_bench.py | instabox ssh $IBOX -- 'cat > ~/st-test/st_bench.py'"
echo tools pushed
