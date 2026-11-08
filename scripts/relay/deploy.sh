#!/bin/bash
# 发布中继（docs/dev-env.md）。在 Mac 上运行，一次发布明确点名的中继，失败就停，不会继续下一台。
#   scripts/relay/deploy.sh lab-cn2 lab-stdb          # 实验中继：只有 dev 用
#   scripts/relay/deploy.sh --prod sg                 # 生产在用的中继（us sg eu cn）：必须带 --prod，逐台来
# 每台先用新二进制校验新配置，通过才替换并重启；旧版留在 relay.prev、relay.yaml.prev。
# 装了 systemd 服务（install-service.sh）的用 systemctl 重启，lab-stdb 和还没装的新中继用 nohup。
# 重启会断开该中继上的连接，在途请求会失败；中继的改动先在实验中继上验证。
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd); REPO=$(cd "$HERE/../.." && pwd)
W=${ST_TESTENV_WORK:-$HOME/st-test}; B=$W/build/amd64; mkdir -p "$B"
Q="-o LogLevel=ERROR -o ConnectTimeout=10"
CN=119.29.250.173; CN2=101.43.2.87; SERVE_PORT=8095
PROD=0; [ "${1:-}" = "--prod" ] && { PROD=1; shift; }
[ $# -gt 0 ] || { sed -n 2,7p "$0"; exit 2; }
for r in "$@"; do
  case $r in
    us|sg|eu|cn) [ $PROD = 1 ] || { echo "$r carries production: pass --prod" >&2; exit 2; };;
    lab-cn2|lab-stdb) ;;
    *) echo "unknown relay $r (us sg eu cn lab-cn2 lab-stdb)" >&2; exit 2;;
  esac
done
cd "$REPO"

build_amd64() { [ -n "${BUILT:-}" ] || { GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "$B/relay" ./cmd/relay; gzip -kf "$B/relay"; BUILT=1; }; }
# CN and CN2 have very slow uploads from here: build on CN, from source without secrets.
build_on_cn() {
  [ -z "${CN_BUILT:-}" ] || return 0
  git ls-files -co --exclude-standard | while IFS= read -r f; do [ ! -f "$f" ] || echo "$f"; done | grep -v -e '^docs/test_env.txt' -e 'secrets' -e '^reports/' -e 'upstream_proxy.json' -e '\.env$' -e '\.key$' > "$W/files.txt"
  COPYFILE_DISABLE=1 tar czf "$W/src.tgz" -T "$W/files.txt"
  scp $Q "$W/src.tgz" "s0nnet@$CN:sharetoken-v2/src-new.tgz"
  timeout 600 ssh $Q -n "s0nnet@$CN" 'cd ~/sharetoken-v2 && rm -rf src2 && mkdir src2 && tar xzf src-new.tgz -C src2 2>/dev/null && cd src2 && export PATH=$HOME/sharetoken-sim/go/bin:$PATH GOPROXY=https://goproxy.cn GOFLAGS=-mod=mod && CGO_ENABLED=0 go build -trimpath -o ~/st-test/relay.new ./cmd/relay'
  CN_BUILT=1
}
# CN2 fetches CN's build over the domestic link; the HTTP server holds only that binary and lives for this copy.
copy_cn_to_cn2() {
  local sum; sum=$(ssh $Q -n "s0nnet@$CN" 'sha256sum ~/st-test/relay.new | cut -d" " -f1')
  ssh $Q -n "s0nnet@$CN" "rm -rf ~/st-test/serve && mkdir ~/st-test/serve && cp ~/st-test/relay.new ~/st-test/serve/relay.new && cd ~/st-test/serve && (timeout 300 python3 -m http.server $SERVE_PORT >/dev/null 2>&1 &) ; sleep 1"
  timeout 280 ssh $Q -n "orcatermuser@$CN2" "cd ~/st-test && curl -sS --max-time 270 -o relay.new http://$CN:$SERVE_PORT/relay.new && chmod +x relay.new && echo '$sum  relay.new' | sha256sum -c --quiet" || { ssh $Q -n "s0nnet@$CN" "rm -rf ~/st-test/serve; pkill -f 'http[.]server $SERVE_PORT'" || true; return 1; }
  ssh $Q -n "s0nnet@$CN" "rm -rf ~/st-test/serve; pkill -f 'http[.]server $SERVE_PORT'" || true
}

SWAP='cd "$RDIR" && ./relay.new -config relay.yaml.new -check-config >/dev/null && { [ -f relay.yaml ] && cp relay.yaml relay.yaml.prev || true; [ -f relay ] && cp relay relay.prev || true; if [ -f /etc/systemd/system/sharetoken-relay.service ]; then mv relay.new relay; mv relay.yaml.new relay.yaml; sudo -n systemctl restart sharetoken-relay; else for p in $(pgrep -x relay); do [ "$(readlink /proc/$p/cwd)" = "$RDIR" ] && kill $p; done; sleep 2; mv relay.new relay; mv relay.yaml.new relay.yaml; setsid nohup ./relay -config relay.yaml >> relay.log 2>&1 < /dev/null & fi; } ; sleep 4; curl -s -m 3 "127.0.0.1:$STATUS" | python3 -c "import json,sys; d=json.load(sys.stdin); print(\"  up: peers=%s networks=%s\" % (d[\"peers\"], [(n[\"name\"], n[\"reservations\"]) for n in d[\"stats\"][\"networks\"]]))"'

for r in "$@"; do
  echo "== $r"
  conf=$HERE/conf/$r.yaml
  case $r in
    us|sg|eu)
      case $r in us) h=165.227.115.27;; sg) h=201.79.122.100;; eu) h=164.92.157.227;; esac
      build_amd64
      scp $Q "$B/relay.gz" "s0nnet@$h:st-test/relay.gz"
      scp $Q "$conf" "s0nnet@$h:st-test/relay.yaml.new"
      ssh $Q -n "s0nnet@$h" "cd ~/st-test && gunzip -c relay.gz > relay.new && chmod +x relay.new; export RDIR=\$HOME/st-test STATUS=8093; $SWAP"
      ;;
    cn)
      build_on_cn
      scp $Q "$conf" "s0nnet@$CN:st-test/relay.yaml.new"
      ssh $Q -n "s0nnet@$CN" "export RDIR=\$HOME/st-test STATUS=8093; $SWAP"
      ;;
    lab-cn2)
      build_on_cn; copy_cn_to_cn2
      scp $Q "$conf" "orcatermuser@$CN2:st-test/relay.yaml.new"
      ssh $Q -n "orcatermuser@$CN2" "export RDIR=\$HOME/st-test STATUS=8093; $SWAP"
      ;;
    lab-stdb)
      build_amd64
      orb run -m st-db bash -c "mkdir -p ~/st-test/relay-lab && cp '$B/relay' ~/st-test/relay-lab/relay.new && chmod +x ~/st-test/relay-lab/relay.new && cp '$conf' ~/st-test/relay-lab/relay.yaml.new; export RDIR=\$HOME/st-test/relay-lab STATUS=8094; $SWAP" </dev/null
      ;;
  esac
done
