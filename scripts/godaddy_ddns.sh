#!/usr/bin/env bash
# GoDaddy 动态解析：把本机公网 IPv4 写到 sharetoken.app、www 和 mail 的 A 记录。
# 每跑一次对一次；IP 没变就不调用写接口，所以可以放心放进 cron。
#
#   scripts/godaddy_ddns.sh            # 更新
#   scripts/godaddy_ddns.sh -n         # 只显示将要做什么，不改 DNS
#   scripts/godaddy_ddns.sh -f         # 即使 IP 未变也强制写入
#
# 凭据：GODADDY_ACCESS_TOKEN（GoDaddy 的 "key:secret"，见 docs/prod-env.md），按顺序从
#   环境变量、$GODADDY_ENV、脚本旁边 ../config.env、~/.config/sharetoken/ddns.env 读取。
# 可选环境变量：DDNS_DOMAIN（sharetoken.app）、DDNS_NAMES（"@ www mail"）、DDNS_TTL（600）、
#   DDNS_IP（手动指定 IP，跳过探测）。
#
# www 原来是 CNAME→@，GoDaddy 不允许同名 CNAME 与 A 并存，所以首次运行会先删掉这条 CNAME。
# 失败时退出码非 0，输出里不会出现 token。
set -euo pipefail

DOMAIN=${DDNS_DOMAIN:-sharetoken.app}
NAMES=${DDNS_NAMES:-"@ www mail"}
TTL=${DDNS_TTL:-600}
API=${GODADDY_API:-https://api.godaddy.com/v1}
DRY=0; FORCE=0
while getopts "nf" o; do case $o in n) DRY=1;; f) FORCE=1;; *) exit 2;; esac; done

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
for f in "${GODADDY_ENV:-}" "$here/../config.env" "$HOME/.config/sharetoken/ddns.env"; do
  [ -n "$f" ] && [ -r "$f" ] && [ -z "${GODADDY_ACCESS_TOKEN:-}" ] && { set -a; . "$f"; set +a; }
done
: "${GODADDY_ACCESS_TOKEN:?GODADDY_ACCESS_TOKEN not set (config.env)}"

log() { printf '%s %s\n' "$(date '+%F %T')" "$*"; }
# GoDaddy "key:secret" uses sso-key; a bare OAuth-style token uses Bearer.
case $GODADDY_ACCESS_TOKEN in
  *:*) AUTH="sso-key $GODADDY_ACCESS_TOKEN";;
  *)   AUTH="Bearer $GODADDY_ACCESS_TOKEN";;
esac
gd() {
  local m=$1 p=$2 d=${3:-}
  curl -sS --max-time 20 -X "$m" -H "Authorization: $AUTH" -H 'Content-Type: application/json' \
    ${d:+-d "$d"} -w '\n%{http_code}' "$API/domains/$DOMAIN$p"
}

is_v4() { [[ $1 =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}$ ]] && ! [[ $1 =~ ^(10\.|127\.|192\.168\.|169\.254\.|172\.(1[6-9]|2[0-9]|3[01])\.) ]]; }

detect_ip() {
  local u ip
  for u in https://api.ipify.org https://ifconfig.me/ip https://ipv4.icanhazip.com https://checkip.amazonaws.com; do
    ip=$(curl -4 -sS --max-time 8 "$u" 2>/dev/null | tr -d '[:space:]') || continue
    is_v4 "$ip" && { echo "$ip"; return 0; }
  done
  return 1
}

IP=${DDNS_IP:-$(detect_ip)} || { log "ERROR: cannot determine public IPv4"; exit 1; }
is_v4 "$IP" || { log "ERROR: '$IP' is not a public IPv4"; exit 1; }

rc=0
for n in $NAMES; do
  fqdn=$([ "$n" = "@" ] && echo "$DOMAIN" || echo "$n.$DOMAIN")
  out=$(gd GET "/records/A/$n"); code=${out##*$'\n'}; body=${out%$'\n'*}
  [ "$code" = 200 ] || { log "ERROR: GET A $fqdn -> HTTP $code: $body"; rc=1; continue; }
  cur=$(printf '%s' "$body" | grep -o '"data":"[^"]*"' | head -1 | cut -d'"' -f4 || true)
  if [ "$cur" = "$IP" ] && [ $FORCE = 0 ]; then log "$fqdn already $IP"; continue; fi

  if [ $DRY = 1 ]; then log "[dry-run] $fqdn: ${cur:-<none>} -> $IP"; continue; fi

  # A 记录与同名 CNAME 互斥：有 CNAME 就先删（只对 @ 以外的名字有意义）
  if [ "$n" != "@" ]; then
    out=$(gd GET "/records/CNAME/$n"); code=${out##*$'\n'}; body=${out%$'\n'*}
    if [ "$code" = 200 ] && [ "$body" != "[]" ]; then
      out=$(gd DELETE "/records/CNAME/$n"); code=${out##*$'\n'}
      [ "$code" = 204 ] || { log "ERROR: delete CNAME $fqdn -> HTTP $code: ${out%$'\n'*}"; rc=1; continue; }
      log "$fqdn: removed CNAME (replaced by A)"
    fi
  fi
  out=$(gd PUT "/records/A/$n" "[{\"data\":\"$IP\",\"ttl\":$TTL}]"); code=${out##*$'\n'}
  if [ "$code" = 200 ]; then log "$fqdn: ${cur:-<none>} -> $IP (ttl $TTL)"
  else log "ERROR: PUT A $fqdn -> HTTP $code: ${out%$'\n'*}"; rc=1; fi
done
exit $rc
