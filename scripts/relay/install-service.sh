#!/bin/bash
# 把中继改由 systemd 管理（docs/dev-env.md）。在 Mac 上运行，会重启一次中继，所以生产中继要带 --prod，逐台来。
#   scripts/relay/install-service.sh lab-cn2
#   scripts/relay/install-service.sh --prod sg
# 新中继先用 deploy.sh 发布，再运行这个脚本。之后 deploy.sh 用 systemctl restart 重启它。
# lab-stdb 不用它：st-db 平时停机，中继是压测时手动启动的。
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
Q="-o LogLevel=ERROR -o ConnectTimeout=10"
UNIT=sharetoken-relay
PROD=0; [ "${1:-}" = "--prod" ] && { PROD=1; shift; }
[ $# -gt 0 ] || { sed -n 2,6p "$0"; exit 2; }
for r in "$@"; do
  case $r in
    us|sg|eu|cn) [ $PROD = 1 ] || { echo "$r carries production: pass --prod" >&2; exit 2; };;
    lab-cn2) ;;
    *) echo "unknown relay $r (us sg eu cn lab-cn2)" >&2; exit 2;;
  esac
done

for r in "$@"; do
  echo "== $r"
  U=s0nnet; admin=; sudo="sudo -n"
  case $r in
    us) ip=165.227.115.27;;
    sg) ip=201.79.122.100;;
    # 这台的 s0nnet 没有 sudo：用 root 安装，再只放行中继的 systemctl 命令
    eu) ip=164.92.157.227; admin=root; sudo=;;
    cn) ip=119.29.250.173;;
    lab-cn2) ip=101.43.2.87; U=orcatermuser;;
  esac
  sed -e "s|@USER@|$U|g" -e "s|@DIR@|/home/$U/st-test|g" "$HERE/$UNIT.service" \
    | ssh $Q "${admin:-$U}@$ip" "$sudo tee /etc/systemd/system/$UNIT.service >/dev/null"
  ssh $Q "${admin:-$U}@$ip" "U=$U UNIT=$UNIT S='$sudo' bash -s" <<'R'
set -e
if [ -z "$S" ]; then
  f=/etc/sudoers.d/sharetoken-relay
  printf '%s ALL=(root) NOPASSWD: /usr/bin/systemctl start %s, /usr/bin/systemctl stop %s, /usr/bin/systemctl restart %s\n' "$U" "$UNIT" "$UNIT" "$UNIT" > "$f.tmp"
  chmod 440 "$f.tmp"; visudo -cf "$f.tmp" >/dev/null; mv "$f.tmp" "$f"
fi
$S systemctl daemon-reload
$S systemctl enable "$UNIT" 2>&1 | grep -v '^Created symlink' || true
if systemctl is-active -q "$UNIT"; then
  echo "  already under systemd; unit file refreshed, relay not restarted"
else
  RDIR=/home/$U/st-test
  for p in $(pgrep -x relay); do [ "$(readlink /proc/$p/cwd)" = "$RDIR" ] && $S kill "$p"; done
  for i in 1 2 3 4 5 6 7 8 9 10; do
    alive=0; for p in $(pgrep -x relay); do [ "$(readlink /proc/$p/cwd)" = "$RDIR" ] && alive=1; done
    [ $alive = 0 ] && break; sleep 1
  done
  $S systemctl start "$UNIT"
fi
sleep 4
echo "  $(systemctl is-enabled $UNIT) $(systemctl is-active $UNIT) $(systemctl show -p MainPID $UNIT)"
curl -s -m 3 127.0.0.1:8093/metrics | grep -E '^sharetoken_relay_(service_enabled|network_coordinators_reserved)' | sed 's/^/  /'
R
done
