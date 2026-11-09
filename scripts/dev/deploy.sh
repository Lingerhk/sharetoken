#!/bin/bash
# 部署 dev 环境（docs/dev-env.md）：armbian 上的门户 + 协调器 + 邮件服务 + MySQL + nginx 入口 + DDNS。
# 在 Mac 上运行；可重复运行（已有的密钥、数据库、证书都不会被覆盖）。
#   scripts/dev/deploy.sh            # 全部
#   scripts/dev/deploy.sh --bins     # 只更新二进制和配置并重启（含邮件服务）
#   scripts/dev/deploy.sh --portal   # 只更新门户和客户端安装包，协调器、邮件服务都不动
# 服务器上的布局和权限由 scripts/ops/st-host-setup.sh 负责：三个服务各自的系统用户，机密在 /etc/sharetoken-dev/<服务>/，
# 二进制在 /srv/sharetoken-dev/。这里只把文件上传到暂存目录 ~/sharetoken-dev 再调用它。
# 不碰中继（scripts/relay/），也不碰生产。armbian 上别的站点和容器（sgvpn、homebridge、anylink）不受影响。
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd); REPO=$(cd "$HERE/../.." && pwd)
HOST=${ST_DEV_HOST:-192.168.6.91}; RUSER=s0nnet; DIR=/home/$RUSER/sharetoken-dev
ETC=/etc/sharetoken-dev; SETUP="sudo bash $DIR/st-host-setup.sh dev $DIR"
DOMAIN=dev.sharetoken.app; EMAIL=${LE_EMAIL:-lingerhk@gmail.com}
BINS_ONLY=0; [ "${1:-}" = "--bins" ] && BINS_ONLY=1
PORTAL_ONLY=0; [ "${1:-}" = "--portal" ] && PORTAL_ONLY=1
Q="-o LogLevel=ERROR -o ConnectTimeout=10"
rsh() { ssh $Q -n "$RUSER@$HOST" "$@"; }
W=${ST_TESTENV_WORK:-$HOME/st-test}; KEYS=$W/dev-keys
(umask 077; mkdir -p "$KEYS")
MYSQL_ROOT='sudo /usr/local/sbin/st-mysql-root'
conf_of() { case $1 in mailserver) echo mail.yaml;; *) echo $1.yaml;; esac; }
newpass() { [ -s "$1" ] || (umask 077; head -c 24 /dev/urandom | base64 | tr -d '/+=' > "$1"); cat "$1"; }
# upload_secret <本地文件> <暂存名> <最终路径>：最终路径已有就不传
upload_secret() { rsh "sudo test -s $3" || { scp $Q "$1" "$RUSER@$HOST:$DIR/$2" >/dev/null; rsh "chmod 600 $DIR/$2"; }; }

cd "$REPO"
V=$(scripts/version.sh); echo "version $V"
upload_common() { # programs...
  rsh "mkdir -p $DIR/releases/$V"
  local c
  for c in "$@"; do scp $Q "dist/$c-$V-linux-arm64" "$RUSER@$HOST:$DIR/releases/$V/$c" >/dev/null; done
  scp $Q "$HERE"/*.yaml "$HERE"/sharetoken-dev-*.service "$REPO/scripts/ops/st-host-setup.sh" \
    "$REPO/scripts/ops/st-mysql-root" "$RUSER@$HOST:$DIR/" >/dev/null
  scp $Q "$REPO/scripts/godaddy_ddns.sh" "$RUSER@$HOST:$DIR/ddns.sh" >/dev/null
  rsh "chmod +x $DIR/releases/$V/*; cd $DIR/releases && ls -1t | tail -n +6 | while read -r old; do rm -rf \"\$old\"; done"
  for c in "$@"; do rsh "cd $DIR && releases/$V/$c -config $(conf_of $c) check-config >/dev/null && echo $c config ok"; done
}

if [ $PORTAL_ONLY = 1 ]; then
  echo "== build and upload (linux/arm64)"
  GOOS=linux GOARCH=arm64 scripts/build.sh portal >/dev/null
  upload_common portal
  echo "== client packages"
  scripts/publish-packages.sh "$V" "$RUSER@$HOST" /var/lib/sharetoken-dev/portal/downloads --with-dev-cli --owner st-portal
  echo "== install and restart portal"
  rsh "$SETUP bins $V portal"
  exit 0
fi

echo "== build (linux/arm64)"
GOOS=linux GOARCH=arm64 scripts/build.sh portal coordinator mailserver >/dev/null
echo "== upload"
upload_common portal coordinator mailserver

if [ $BINS_ONLY = 1 ]; then
  echo "== install and restart"
  rsh "$SETUP bins $V portal coordinator mailserver"
  exit 0
fi

echo "== keys (dev only; the relays' dev network trusts these, so they are never regenerated here)"
for k in coordinator.key portal-pass.key; do
  [ -s "$KEYS/$k" ] || { echo "missing $KEYS/$k (docs/dev-env.md)"; exit 1; }
done
upload_secret "$KEYS/coordinator.key" coordinator.key $ETC/coordinator/coordinator.key
upload_secret "$KEYS/portal-pass.key" portal-pass.key $ETC/portal/portal-pass.key

echo "== MySQL on this host: its own database; one account per process"
rsh "sudo install -o root -g root -m 700 $DIR/st-mysql-root /usr/local/sbin/st-mysql-root"
PP=$(newpass "$KEYS/portal-db.pass"); CP=$(newpass "$KEYS/coord-db.pass"); MP=$(newpass "$KEYS/mail-db.pass")
{ printf "CREATE DATABASE IF NOT EXISTS sharetoken_dev;\n"
  for a in "sharetoken_dev_portal $PP" "sharetoken_dev_coord $CP"; do set -- $a
    printf "CREATE USER IF NOT EXISTS '%s'@'%%' IDENTIFIED BY '%s';\nALTER USER '%s'@'%%' IDENTIFIED BY '%s';\nGRANT ALL PRIVILEGES ON sharetoken_dev.* TO '%s'@'%%';\n" "$1" "$2" "$1" "$2" "$1"; done
  # the mail account can touch nothing but its tables (the portal creates them)
  printf "CREATE USER IF NOT EXISTS 'sharetoken_mail_dev'@'%%' IDENTIFIED BY '%s';\nALTER USER 'sharetoken_mail_dev'@'%%' IDENTIFIED BY '%s';\n" "$MP" "$MP"
  for t in mail_outbox mail_inbox mail_status; do printf "GRANT SELECT, INSERT, UPDATE, DELETE ON sharetoken_dev.%s TO 'sharetoken_mail_dev'@'%%';\n" "$t"; done
  printf "GRANT SELECT ON sharetoken_dev.schema_migrations TO 'sharetoken_mail_dev'@'%%';\n"
} | ssh $Q "$RUSER@$HOST" "$MYSQL_ROOT"
upload_secret "$KEYS/portal-db.pass" portal-db.pass $ETC/portal/db.pass
upload_secret "$KEYS/coord-db.pass" coord-db.pass $ETC/coordinator/db.pass
upload_secret "$KEYS/mail-db.pass" mail-db.pass $ETC/mail/db.pass

echo "== mail: DKIM key (public half is in DNS, scripts/mail_dns.py)"
[ -s "$KEYS/dkim.key" ] || (umask 077; openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out "$KEYS/dkim.key" 2>/dev/null)
upload_secret "$KEYS/dkim.key" dkim.key $ETC/mail/dkim.key

echo "== DNS token for DDNS (moved to $ETC/ddns by the host setup)"
if ! rsh "sudo test -s $ETC/ddns/ddns.env"; then
  ssh $Q "$RUSER@$HOST" "mkdir -p ~/.config/sharetoken && umask 077 && cat > ~/.config/sharetoken/ddns.env" < <(grep '^GODADDY_ACCESS_TOKEN=' "$REPO/config.env")
fi

echo "== public entry (only this site; the host's other sites are untouched)"
if ! rsh "sudo test -s /etc/letsencrypt/live/$DOMAIN/fullchain.pem"; then
  ssh $Q "$RUSER@$HOST" "sudo tee /etc/nginx/conf.d/sharetoken-dev.conf >/dev/null" <<N
server {
    listen 80;
    listen [::]:80;
    server_name $DOMAIN;
    location /.well-known/acme-challenge/ { root /var/www/html; }
    location / { return 404; }
}
N
  rsh "sudo nginx -t 2>/dev/null && sudo systemctl reload nginx"
  rsh "sudo certbot certonly --webroot -w /var/www/html -d $DOMAIN --email $EMAIL --agree-tos --no-eff-email --non-interactive"
fi
scp $Q "$HERE/nginx-armbian.conf" "$RUSER@$HOST:/tmp/sharetoken-dev.conf" >/dev/null
rsh "sudo cp /tmp/sharetoken-dev.conf /etc/nginx/conf.d/sharetoken-dev.conf && rm /tmp/sharetoken-dev.conf && sudo nginx -t 2>/dev/null && sudo systemctl reload nginx"

echo "== install: users, directories, secrets, units, cron; then restart"
rsh "$SETUP install $V"
rsh "sudo runuser -u st-ddns -- env GODADDY_ENV=$ETC/ddns/ddns.env DDNS_NAMES=dev /srv/sharetoken-dev/ddns.sh | tail -1"
