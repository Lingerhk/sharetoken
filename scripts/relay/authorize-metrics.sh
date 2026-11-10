#!/bin/bash
# 让门户通过 SSH 读中继的 /metrics（管理后台仪表盘，docs/relay-monitor.md「门户读取指标」）。在 Mac 上运行。
#   scripts/relay/authorize-metrics.sh dev us sg eu cn lab-cn2
#   scripts/relay/authorize-metrics.sh --remove dev cn       # 撤销
# 做三件事：
#   1. 没有就生成门户专用密钥 ~/st-test/<env>-keys/relay-metrics.key（ed25519，无口令；deploy.sh 装到门户的 /etc 目录）
#   2. 在每台中继登录用户的 authorized_keys 里加一行，用 restrict + command= 锁死：这把钥匙只能执行一条 curl 读本机指标，
#      不能开 shell、转发端口、分配终端，客户端传什么命令都会被替换。同一注释的旧行先删掉，可重复运行
#   3. 把中继的主机公钥写进 scripts/<env>/relay-metrics.known_hosts（门户只认这些）。公钥用 ssh-keyscan 取，
#      必须和本机 ~/.ssh/known_hosts 里已信任的一致，否则中止
# 不重启中继，不动中继配置。
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd); REPO=$(cd "$HERE/../.." && pwd)
Q="-o LogLevel=ERROR -o ConnectTimeout=10 -o BatchMode=yes"
REMOVE=0; [ "${1:-}" = "--remove" ] && { REMOVE=1; shift; }
ENV=${1:-}; shift || true
case $ENV in dev|prod) ;; *) sed -n 2,4p "$0"; exit 2;; esac
[ $# -gt 0 ] || { sed -n 2,4p "$0"; exit 2; }
W=${ST_TESTENV_WORK:-$HOME/st-test}; KEY=$W/$ENV-keys/relay-metrics.key
CMD='curl -s --max-time 8 http://127.0.0.1:8093/metrics'
TAG=sharetoken-$ENV-portal-metrics
KNOWN=$REPO/scripts/$ENV/relay-metrics.known_hosts

target() { # relay -> user@host
  case $1 in
    us) echo s0nnet@165.227.115.27;; sg) echo s0nnet@201.79.122.100;; eu) echo s0nnet@164.92.157.227;;
    cn) echo s0nnet@119.29.250.173;; lab-cn2) echo orcatermuser@101.43.2.87;;
    *) echo "unknown relay $1 (us sg eu cn lab-cn2)" >&2; exit 2;;
  esac
}
for r in "$@"; do target "$r" >/dev/null; done

if [ $REMOVE = 0 ] && [ ! -s "$KEY" ]; then
  (umask 077; mkdir -p "$(dirname "$KEY")"; ssh-keygen -q -t ed25519 -N '' -C "$TAG" -f "$KEY")
  echo "new key $KEY"
fi
PUB=$( [ $REMOVE = 1 ] || cut -d' ' -f1,2 "$KEY.pub")
LINE="restrict,command=\"$CMD\" $PUB $TAG"

touch "$KNOWN"
for r in "$@"; do
  t=$(target "$r"); host=${t#*@}
  # authorized_keys: drop this environment's old line, then add the current one
  if [ $REMOVE = 1 ]; then
    ssh $Q -n "$t" "f=~/.ssh/authorized_keys; [ -f \$f ] && grep -v ' $TAG\$' \$f > \$f.st-tmp; cat \$f.st-tmp > \$f; rm -f \$f.st-tmp"
    echo "$r: removed $TAG"
    continue
  fi
  ssh $Q "$t" "umask 077; mkdir -p ~/.ssh; f=~/.ssh/authorized_keys; touch \$f; grep -v ' $TAG\$' \$f > \$f.st-tmp || true; cat >> \$f.st-tmp; cat \$f.st-tmp > \$f; rm -f \$f.st-tmp" <<<"$LINE"
  # host key: what the relay offers must be what this Mac already trusts
  scanned=$(ssh-keyscan -t ed25519 "$host" 2>/dev/null | grep -v '^#' | awk '{print $2" "$3}')
  [ -n "$scanned" ] || { echo "$r: ssh-keyscan returned nothing" >&2; exit 1; }
  ssh-keygen -F "$host" -f ~/.ssh/known_hosts 2>/dev/null | grep -qF "$scanned" || { echo "$r: host key of $host is not the one in ~/.ssh/known_hosts; refusing to pin it" >&2; exit 1; }
  grep -v "^$host " "$KNOWN" > "$KNOWN.tmp" || true; echo "$host $scanned" >> "$KNOWN.tmp"; sort -o "$KNOWN" "$KNOWN.tmp"; rm -f "$KNOWN.tmp"
  # the key works, and does nothing but the forced command
  out=$(ssh $Q -i "$KEY" -o IdentitiesOnly=yes -n "$t" 'id; echo SHOULD-NOT-RUN' | grep -c '^sharetoken_relay_' || true)
  shell=$(ssh $Q -i "$KEY" -o IdentitiesOnly=yes -n "$t" 'echo SHOULD-NOT-RUN' | grep -c SHOULD-NOT-RUN || true)
  [ "$out" -gt 0 ] && [ "$shell" = 0 ] || { echo "$r: check failed (metrics lines $out, shell ran $shell)" >&2; exit 1; }
  echo "$r: ok ($out relay metrics, no shell)"
done
