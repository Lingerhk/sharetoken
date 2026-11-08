#!/bin/bash
# 部署中继监控（docs/relay-monitor.md）。在 Mac 上运行，不重启中继。中继要先更新到带 /metrics 的版本（deploy.sh）。
#   scripts/relay/deploy-monitor.sh lab-cn2                  # 实验中继
#   scripts/relay/deploy-monitor.sh --prod us sg eu cn       # 生产中继必须带 --prod
#   scripts/relay/deploy-monitor.sh --test-push lab-cn2      # 部署后发一条测试通知
#   scripts/relay/deploy-monitor.sh --remove --prod us       # 删除 cron（文件留着）
# Pushover 账号放在 ~/st-test/pushover.env（PUSHOVER_TOKEN=…、PUSHOVER_USER=…），不进仓库；有这个文件才下发。
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
W=${ST_TESTENV_WORK:-$HOME/st-test}
Q="-o LogLevel=ERROR -o ConnectTimeout=10"
MARK=sharetoken-relay-monitor
PROD=0 REMOVE=0 TESTPUSH=0
while [ $# -gt 0 ]; do
  case $1 in
    --prod) PROD=1;; --remove) REMOVE=1;; --test-push) TESTPUSH=1;;
    --*) echo "unknown flag $1" >&2; exit 2;;
    *) break;;
  esac
  shift
done
[ $# -gt 0 ] || { sed -n 2,7p "$0"; exit 2; }
for r in "$@"; do
  case $r in
    us|sg|eu|cn) [ $PROD = 1 ] || { echo "$r carries production: pass --prod" >&2; exit 2; };;
    lab-cn2) ;;
    *) echo "unknown relay $r (us sg eu cn lab-cn2)" >&2; exit 2;;
  esac
done

for r in "$@"; do
  case $r in
    us) h=s0nnet@165.227.115.27;;
    sg) h=s0nnet@201.79.122.100;;
    eu) h=s0nnet@164.92.157.227;;
    cn) h=s0nnet@119.29.250.173;;
    lab-cn2) h=orcatermuser@101.43.2.87;;
  esac
  echo "== $r"
  if [ $REMOVE = 1 ]; then
    ssh $Q -n "$h" "crontab -l 2>/dev/null | grep -vF '$MARK' | crontab -"
    echo "  cron removed"
    continue
  fi
  # 中继还是旧版时，装上就会一直告警
  ssh $Q -n "$h" "curl -s -m 5 127.0.0.1:8093/metrics | grep -q '^sharetoken_relay_service_enabled'" \
    || { echo "$r has no /metrics yet: deploy the relay first (scripts/relay/deploy.sh)" >&2; exit 1; }
  scp $Q "$HERE/monitor.py" "$h:st-test/monitor.py"
  scp $Q "$HERE/conf/$r.monitor.ini" "$h:st-test/monitor.ini"
  if [ -f "$W/pushover.env" ]; then
    ssh $Q -n "$h" 'umask 077; : > ~/st-test/pushover.env; chmod 600 ~/st-test/pushover.env'
    scp $Q "$W/pushover.env" "$h:st-test/pushover.env"
  fi
  # 只增删带标记的那一行，机器上别的 cron 不动
  ssh $Q -n "$h" "{ crontab -l 2>/dev/null | grep -vF '$MARK'; echo '* * * * * cd \$HOME/st-test && python3 monitor.py -c monitor.ini >> monitor.log 2>&1 # $MARK'; } | crontab -"
  ssh $Q -n "$h" 'cd ~/st-test && python3 monitor.py -c monitor.ini --check' || echo "  (some checks are failing)"
  [ $TESTPUSH = 0 ] || ssh $Q -n "$h" 'cd ~/st-test && python3 monitor.py -c monitor.ini --test'
done
