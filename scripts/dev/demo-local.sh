#!/bin/bash
# Live localhost demo of the architecture with a real model.
#
#   relay-a, relay-b   forward encrypted traffic only
#   portal             users, devices, credits (MySQL database shared with the coordinator)
#   coordinator        accounting platform; listens nowhere, reachable only through the relays;
#                      reference copy of the model (audits + exact token counts)
#   alice              user: shares gemma4:e4b from Ollama (honest), and buys
#   carol              user: shares the same model cheaper, but bills 100x the completion tokens
#   bob                user: buys only
#
# It shows: users sign up with a mailed code and bind their devices through the portal; each
# client measures the relays and reaches the coordinator through the best one;
# the cheat is caught and refunded on evidence while the request fails over;
# the auditor checks both sellers; a relay dies and the client switches;
# credits move between user accounts and the books balance; no prompt in any log.
# Everything binds 127.0.0.1; every process is stopped on exit.
set -u
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
BIN="$ROOT/bin"
MODEL_UPSTREAM="${MODEL_UPSTREAM:-gemma4:e4b}"
OLLAMA=http://127.0.0.1:11434/v1
PORTAL=http://127.0.0.1:8092
(cd "$ROOT" && go build -o bin/ ./cmd/...) || exit 1
S="$(mktemp -d)"; echo "work dir: $S"
cd "$S"
PIDS=()
cleanup() { for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null; done; wait 2>/dev/null; }
trap cleanup EXIT
strip() { sed -E 's/^[0-9]{4}\/[0-9]{2}\/[0-9]{2} [0-9:]+ //; s/12D3KooW[A-Za-z0-9]{44}/<peer>/g' | cut -c1-230; }
waitlog() { for _ in $(seq 1 100); do grep -q "$2" "$1" 2>/dev/null && return 0; sleep 0.2; done; echo "timeout waiting for '$2' in $1"; exit 1; }

if ! curl -s -m 1 http://127.0.0.1:11434/api/tags >/dev/null; then
  ollama serve > ollama.log 2>&1 & PIDS+=($!)
  for _ in $(seq 1 40); do curl -s -m 1 http://127.0.0.1:11434/api/tags >/dev/null && break; sleep 0.5; done
  echo "started ollama serve (will be stopped on exit)"
fi
python3 "$ROOT/scripts/dev/inflating_upstream.py" 11999 "$OLLAMA" > cheat-upstream.log 2>&1 & PIDS+=($!)
for _ in $(seq 1 50); do nc -z 127.0.0.1 11999 2>/dev/null && break; sleep 0.2; done

echo; echo "== 0. Relays, portal, coordinator"
export SHARETOKEN_DB_PASSWORD=
DSN=$("$ROOT/scripts/test/local-mysql.sh" new-db st_demo)
COORD=(-key coord.key -db "$DSN")
PASSKEY=$("$BIN/portal" -db "$DSN" pass-key)   # relays trust passes signed with it
COORD_ID=$("$BIN/coordinator" "${COORD[@]}" id)        # relays let the coordinator reserve without one
echo "   portal pass key $PASSKEY"
cat > relay.yaml <<YAML
version: 1
access:
  networks:
    - name: demo
      portal_keys: [$PASSKEY]
      allow_peers: [$COORD_ID]
YAML
"$BIN/relay" -config relay.yaml -key relay-a.key -listen /ip4/127.0.0.1/tcp/4201,/ip6/::1/tcp/4201 -status 127.0.0.1:4211 > relay-a.log 2>&1 & RELAY_A=$!; PIDS+=($RELAY_A)
"$BIN/relay" -config relay.yaml -key relay-b.key -listen /ip4/127.0.0.1/tcp/4202,/ip6/::1/tcp/4202 -status 127.0.0.1:4212 > relay-b.log 2>&1 & RELAY_B=$!; PIDS+=($RELAY_B)
waitlog relay-a.log "portal relay add"; waitlog relay-b.log "portal relay add"
# Dual stack: each relay is registered with its IPv4 and IPv6 address, as it prints them.
RA=$(sed -n "s/.*portal relay add '\([^']*\)'.*/\1/p" relay-a.log)
RB=$(sed -n "s/.*portal relay add '\([^']*\)'.*/\1/p" relay-b.log)

# Sign-in codes are mail; nothing delivers it here, the users read their codes from the outbox.
printf 'version: 1\nmail:\n  from: ShareToken <team@example.test>\n' > portal.yaml
"$BIN/portal" -config portal.yaml -db "$DSN" serve -listen 127.0.0.1:8092 -open-registration -signup-credit 5000000 > portal.log 2>&1 & PIDS+=($!)
waitlog portal.log "listening"
"$BIN/portal" -db "$DSN" useradd -admin root@example.test > /dev/null
"$BIN/portal" -db "$DSN" relay add "$RA" relay-a local > /dev/null
"$BIN/portal" -db "$DSN" relay add "$RB" relay-b local > /dev/null
echo "   relays registered (IPv4 + IPv6): relay-a, relay-b"

"$BIN/coordinator" "${COORD[@]}" -fee-bps 800 \
  -reference "id=gemma-4-e4b,upstream=$MODEL_UPSTREAM,url=$OLLAMA" -audit-interval 5s > coord.log 2>&1 & COORD_PID=$!; PIDS+=($COORD_PID)
waitlog coord.log "via relay"
grep -E "^coordinator|listening|via relay" coord.log | strip | sed 's/^/   /'
for _ in $(seq 1 50); do curl -s $PORTAL/api/v1/network | grep -q '"ok":true.*"ok":true' && break; sleep 0.2; done
echo "   portal /api/v1/network:"; curl -s $PORTAL/api/v1/network | strip | sed 's/^/   /'

echo; echo "== 1. Users sign up with a code mailed to them, and configure sharing"
user() { # name
  export SHARETOKEN_HOME="$S/$1"
  SHARETOKEN_PORTAL=$PORTAL "$BIN/sharetoken" login -email "$1@example.test" -send > /dev/null
  code=$("$ROOT/scripts/test/local-mysql.sh" sql st_demo "SELECT subject FROM mail_outbox WHERE to_addr = '$1@example.test' ORDER BY id DESC LIMIT 1")
  SHARETOKEN_PORTAL=$PORTAL "$BIN/sharetoken" login -email "$1@example.test" -code "${code##* }" -device "$1-box" | tail -2 | head -1 | strip | sed "s/^/   $1: /"
}
st() { SHARETOKEN_HOME="$S/$1" "$BIN/sharetoken" "${@:2}"; }
stup() { SHARETOKEN_HOME="$S/$1" exec "$BIN/sharetoken" up "${@:2}"; } # exec: $! is then the client's PID
user alice; user bob; user carol
st alice share add "$MODEL_UPSTREAM" -id gemma-4-e4b -in 100000 -out 200000 | sed 's/^/   alice: /'
st carol share upstream http://127.0.0.1:11999/v1 > /dev/null
st carol share add "$MODEL_UPSTREAM" -id gemma-4-e4b -in 50000 -out 100000 | sed 's/^/   carol: /'

UP=(-listen /ip4/127.0.0.1/tcp/0 -nat=false)   # alice listens on IPv6 only, the others on IPv4
stup alice -http 127.0.0.1:9787 -listen /ip6/::1/tcp/0 -nat=false > alice.log 2>&1 & PIDS+=($!)
stup carol -http 127.0.0.1:9789 "${UP[@]}" > carol.log 2>&1 & PIDS+=($!)
stup bob -http 127.0.0.1:9788 "${UP[@]}" > bob.log 2>&1 & PIDS+=($!)
waitlog alice.log "endpoint"; waitlog carol.log "endpoint"; waitlog bob.log "endpoint"
for _ in $(seq 1 60); do curl -s -m 2 -H "Authorization: Bearer $(st bob apikey -q)" http://127.0.0.1:9788/v1/models | grep -q '"nodes":2' && break; sleep 0.5; done
echo; echo "== 2. bob's client: relays measured, coordinator reached through the best one"
SHARETOKEN_HOME="$S/bob" "$BIN/sharetoken" status | strip | sed 's/^/   /'
echo; echo "   relay admission (sharers present a portal-signed pass before reserving):"
curl -s 127.0.0.1:4211 | python3 -c 'import json,sys;s=json.load(sys.stdin)["stats"];print("   relay-a: reservations=%d passes accepted=%d rejected=%d denied=%d"%(s["reservations"],s["passes_accepted"],s["passes_rejected"],s["reservations_denied"]))'
echo; echo "   models on the network (two sellers; carol is cheaper):"
SHARETOKEN_HOME="$S/bob" "$BIN/sharetoken" models | sed 's/^/   /'

# The local API wants the keys each client keeps in its directory.
lkey() { python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["key"])' "$S/$1/run.json"; }
akey() { python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["key"])' "$S/$1/apikey.json"; }
lapi() { curl -s -H "Authorization: Bearer $(lkey "$1")" "http://127.0.0.1:$2/api/v1/status"; }
ALICE=$(lapi alice 9787 | python3 -c 'import json,sys;print(json.load(sys.stdin)["peer"])')
CAROL=$(lapi carol 9789 | python3 -c 'import json,sys;print(json.load(sys.stdin)["peer"])')
names() { sed -E "s/$ALICE/<alice>/g; s/$CAROL/<carol>/g"; }

PROMPT="In one short sentence: what is a sealed box in cryptography?"
echo; echo "== 3. bob asks; the cheaper node (carol) is matched first"
curl -s -m 600 -D headers.txt http://127.0.0.1:9788/v1/chat/completions -H "Authorization: Bearer $(akey bob)" -H 'Content-Type: application/json' \
  -d "{\"model\":\"gemma-4-e4b\",\"messages\":[{\"role\":\"user\",\"content\":\"$PROMPT\"}],\"max_tokens\":1000}" | head -c 500; echo
grep -i "^x-sharetoken" headers.txt | names | sed 's/^/   /'
sleep 2
echo "   what happened:"
grep -E "rejected|failing over|decided on evidence" bob.log | strip | sed 's/^/   bob: /'
grep -E "judged on evidence" coord.log | strip | sed 's/^/   coordinator: /'

echo; echo "== 4. Auditor (waiting for carol's 2nd failed audit, up to 4 min)"
mismatches() { grep 'component=auditor' coord.log | grep "$CAROL" | grep -c 'verdict=mismatch'; }
for _ in $(seq 1 120); do [ "$(mismatches)" -ge 2 ] && break; sleep 2; done
grep "component=auditor" coord.log | names | strip | sed 's/^/   /'

echo; echo "== 5. alice buys too: her own node is never matched for her (carol suspended -> no seller)"
curl -s -m 120 http://127.0.0.1:9787/v1/chat/completions -H "Authorization: Bearer $(akey alice)" -H 'Content-Type: application/json' \
  -d '{"model":"gemma-4-e4b","messages":[{"role":"user","content":"Say hi."}],"max_tokens":50}' | head -c 300; echo

echo; echo "== 6. Kill the relay bob uses for the coordinator; bob switches and keeps working"
VIA=$(lapi bob 9788 | python3 -c 'import json,sys;print(json.load(sys.stdin)["coordinator"]["via"])')
if [ "$VIA" = "$RA" ]; then kill $RELAY_A; echo "   killed relay-a"; else kill $RELAY_B; echo "   killed relay-b"; fi
for _ in $(seq 1 60); do
  NOW=$(lapi bob 9788 | python3 -c 'import json,sys;s=json.load(sys.stdin)["coordinator"];print(s["via"] if s["connected"] else "")')
  [ -n "$NOW" ] && [ "$NOW" != "$VIA" ] && break; sleep 0.5; done
SHARETOKEN_HOME="$S/bob" "$BIN/sharetoken" status | grep -E "coordinator|link:|NAME|relay-" | strip | sed 's/^/   /'
curl -s -m 600 -D headers2.txt http://127.0.0.1:9788/v1/chat/completions -H "Authorization: Bearer $(akey bob)" -H 'Content-Type: application/json' \
  -d '{"model":"gemma-4-e4b","stream":true,"messages":[{"role":"user","content":"Count from 1 to 5, comma separated, nothing else."}],"max_tokens":200}' \
  | grep -v '^$' | tail -2 | names | sed 's/^/   /'

sleep 3
echo; echo "== 7. Accounts (portal): credits moved between users"
for u in alice bob carol; do SHARETOKEN_HOME="$S/$u" "$BIN/sharetoken" balance | sed "s/^/   /"; done
"$BIN/portal" -db "$DSN" overview | grep -E 'circulating|platform_fees|books' | sed 's/^/   /'

kill $COORD_PID; wait $COORD_PID 2>/dev/null
echo; echo "== 8. Coordinator stopped; the shared books replay"
"$BIN/coordinator" "${COORD[@]}" check
"$BIN/coordinator" "${COORD[@]}" disputes | names | grep -E '"(node|status|outcome|detail)"' | strip

echo; echo "== 9. Privacy: does any prompt appear in a relay, portal, coordinator or client log?"
if grep -l -e "sealed box" -e "Count from 1 to 5" -e "Say hi" relay-a.log relay-b.log portal.log coord.log alice.log bob.log carol.log; then echo "LEAK"
else echo "   no - prompt text is in none of the logs (evidence was judged in memory)"; fi
