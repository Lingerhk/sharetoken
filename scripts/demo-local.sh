#!/bin/bash
# Live localhost demo of the open-weight token-sharing network, with a real model.
#
#   coordinator  SQLite ledger, reference copy of the model (audits + exact token counts)
#   honest node  serves gemma4:e4b from Ollama
#   cheat node   same model, cheaper, but bills 100x the completion tokens it used
#   buyer        local OpenAI-compatible proxy
#
# It shows: the cheat is caught and refunded on evidence while the request fails
# over to the honest node; the auditor checks both nodes against the reference;
# the books survive a coordinator shutdown; no prompt appears in any log.
# Everything binds 127.0.0.1, NAT traversal is off, every process is stopped on exit.
set -u
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="$ROOT/bin"
MODEL_UPSTREAM="${MODEL_UPSTREAM:-gemma4:e4b}"
OLLAMA=http://127.0.0.1:11434/v1
(cd "$ROOT" && go build -o bin/ ./cmd/...) || exit 1
S="$(mktemp -d)"; echo "work dir: $S"
cd "$S"
PIDS=()
cleanup() { for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null; done; wait 2>/dev/null; }
trap cleanup EXIT
strip() { sed -E 's/^[0-9\/: ]+//; s/12D3KooW[A-Za-z0-9]{44}/<peer>/g' | cut -c1-230; }

if ! curl -s -m 1 http://127.0.0.1:11434/api/tags >/dev/null; then
  ollama serve > ollama.log 2>&1 & PIDS+=($!)
  for _ in $(seq 1 40); do curl -s -m 1 http://127.0.0.1:11434/api/tags >/dev/null && break; sleep 0.5; done
  echo "started ollama serve (will be stopped on exit)"
fi
python3 "$ROOT/scripts/inflating_upstream.py" 11999 "$OLLAMA" & PIDS+=($!)

COORD_FLAGS=(-key coord.key -db coord.db)
"$BIN/coordinator" "${COORD_FLAGS[@]}" -listen /ip4/127.0.0.1/tcp/4101 -nat=false -fee-bps 800 -dev-credit 5000000 \
  -reference "id=gemma-4-e4b,upstream=$MODEL_UPSTREAM,url=$OLLAMA" -audit-interval 5s > coord.log 2>&1 & COORD_PID=$!; PIDS+=($COORD_PID)
for _ in $(seq 1 50); do grep -q "/p2p/" coord.log && break; sleep 0.2; done
COORD=$(grep -o '/ip4/127.0.0.1/tcp/4101/p2p/[A-Za-z0-9]*' coord.log | head -1)
echo "coordinator: $COORD"

"$BIN/node" -key honest.key -listen /ip4/127.0.0.1/tcp/4102 -nat=false -relay=false -coordinator "$COORD" \
  -model id=gemma-4-e4b,upstream=$MODEL_UPSTREAM,in=100000,out=200000 > honest.log 2>&1 & PIDS+=($!)
"$BIN/node" -key cheat.key -listen /ip4/127.0.0.1/tcp/4103 -nat=false -relay=false -coordinator "$COORD" \
  -upstream http://127.0.0.1:11999/v1 -model id=gemma-4-e4b,upstream=$MODEL_UPSTREAM,in=50000,out=100000 > cheat.log 2>&1 & CHEAT_PID=$!; PIDS+=($CHEAT_PID)
"$BIN/buyer" -key buyer.key -listen /ip4/127.0.0.1/tcp/0 -nat=false -coordinator "$COORD" -http 127.0.0.1:9787 > buyer.log 2>&1 & PIDS+=($!)
for _ in $(seq 1 50); do grep -q '^node ' honest.log && grep -q '^node ' cheat.log && break; sleep 0.2; done
HONEST=$(grep -o '^node [A-Za-z0-9]*' honest.log | cut -d' ' -f2)
CHEAT=$(grep -o '^node [A-Za-z0-9]*' cheat.log | cut -d' ' -f2)

for _ in $(seq 1 60); do curl -s -m 2 http://127.0.0.1:9787/v1/models | grep -q '"nodes":2' && break; sleep 0.5; done
echo; echo "== GET /v1/models (two nodes offer gemma-4-e4b; the cheat is cheaper)"; curl -s http://127.0.0.1:9787/v1/models; echo

PROMPT="In one short sentence: what is a sealed box in cryptography?"
echo; echo "== 1. Non-stream request: the cheaper node is matched first"
curl -s -m 600 -D headers.txt http://127.0.0.1:9787/v1/chat/completions -H 'Content-Type: application/json' \
  -d "{\"model\":\"gemma-4-e4b\",\"messages\":[{\"role\":\"user\",\"content\":\"$PROMPT\"}],\"max_tokens\":1000}" | head -c 600; echo
grep -i "^x-sharetoken" headers.txt | sed -E "s/$HONEST/<honest node>/; s/$CHEAT/<cheat node>/"
sleep 2
echo "-- what happened (buyer, coordinator):"
grep -E "rejected|failing over|decided on evidence" buyer.log | strip | sed 's/^/   /'
grep -E "judged on evidence" coord.log | strip | sed 's/^/   /'

echo; echo "== 2. The auditor checks random nodes against the reference model (waiting for the cheat's 2nd failed audit, up to 4 min)"
mismatches() { grep 'component=auditor' coord.log | grep "$CHEAT" | grep -c 'verdict=mismatch'; }
for _ in $(seq 1 120); do [ "$(mismatches)" -ge 2 ] && break; sleep 2; done
grep "component=auditor" coord.log | sed -E "s/$HONEST/<honest>/; s/$CHEAT/<cheat>/" | strip | sed 's/^/   /'
if [ "$(mismatches)" -ge 2 ]; then echo "   -> two failed audits in a row: the cheat is suspended from matching"
else kill $CHEAT_PID 2>/dev/null; echo "   -> cheat not audited twice yet; stopped it for the rest of the demo"; fi

echo; echo "== 3. Stream request (only the honest node is matched now), last lines"
curl -s -N -m 600 http://127.0.0.1:9787/v1/chat/completions -H 'Content-Type: application/json' \
  -d '{"model":"gemma-4-e4b","stream":true,"messages":[{"role":"user","content":"Count from 1 to 5, comma separated, nothing else."}],"max_tokens":200}' \
  | grep -v '^$' | tail -3 | sed -E "s/$HONEST/<honest node>/"

sleep 2
echo; echo "== 4. Buyer balance: 5,000,000 dev credit minus the two honest jobs; the cheated job was refunded"
"$BIN/buyer" -key buyer.key -listen /ip4/127.0.0.1/tcp/0 -nat=false -coordinator "$COORD" balance | sed -E 's/12D3KooW[A-Za-z0-9]{44}/<buyer>/'
grep -h "job settled" honest.log | strip | sed 's/^/   honest node: /'


kill $COORD_PID; wait $COORD_PID 2>/dev/null
echo; echo "== 5. Coordinator stopped; its SQLite books are intact (admin commands read the database)"
"$BIN/coordinator" "${COORD_FLAGS[@]}" check
"$BIN/coordinator" "${COORD_FLAGS[@]}" nodes | sed -E "s/$HONEST/<honest>/; s/$CHEAT/<cheat>/"
"$BIN/coordinator" "${COORD_FLAGS[@]}" disputes | sed -E "s/$HONEST/<honest>/; s/$CHEAT/<cheat>/; s/12D3KooW[A-Za-z0-9]{44}/<peer>/g" | grep -E '"(node|status|outcome|detail)"'

echo; echo "== 6. Privacy: does either prompt appear in any marketplace log?"
if grep -l -e "sealed box" -e "Count from 1 to 5" coord.log honest.log cheat.log buyer.log; then echo "LEAK"
else echo "no - prompt text is in none of the coordinator/node/buyer logs (evidence was judged in memory)"; fi
