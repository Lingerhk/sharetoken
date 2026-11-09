#!/bin/bash
# 部署 dev 环境（docs/dev-env.md）：树莓派上的门户 + 协调器 + 邮件服务；armbian 上的 nginx 入口和 MySQL 独立库。
# 在 Mac 上运行；可重复运行（已有的密钥、数据库、证书都不会被覆盖）。
#   scripts/dev/deploy.sh            # 全部
#   scripts/dev/deploy.sh --bins     # 只更新二进制和配置并重启（含邮件服务）
#   scripts/dev/deploy.sh --portal   # 只更新门户和客户端安装包，协调器、邮件服务都不动
# 服务器上的布局和权限由 scripts/ops/st-host-setup.sh 负责：三个服务各自的系统用户，机密在 /etc/sharetoken-dev/<服务>/，
# 二进制在 /srv/sharetoken-dev/。这里只把文件上传到暂存目录 ~/sharetoken-dev 再调用它。
# 不碰中继（scripts/relay/），也不碰生产服务：armbian 上只增加 nginx 站点、MySQL 库和账号、一把受限的隧道密钥。
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd); REPO=$(cd "$HERE/../.." && pwd)
PI=192.168.6.148; ARMBIAN=192.168.6.91; RUSER=s0nnet; DIR=/home/$RUSER/sharetoken-dev
ETC=/etc/sharetoken-dev; SETUP="sudo bash $DIR/st-host-setup.sh dev $DIR"
DOMAIN=dev.sharetoken.app; EMAIL=${LE_EMAIL:-lingerhk@gmail.com}
BINS_ONLY=0; [ "${1:-}" = "--bins" ] && BINS_ONLY=1
PORTAL_ONLY=0; [ "${1:-}" = "--portal" ] && PORTAL_ONLY=1
Q="-o LogLevel=ERROR -o ConnectTimeout=10"
pi() { ssh $Q -n "$RUSER@$PI" "$@"; }
arm() { ssh $Q -n "$RUSER@$ARMBIAN" "$@"; }
W=${ST_TESTENV_WORK:-$HOME/st-test}; B=$W/build/arm64; KEYS=$W/dev-keys; mkdir -p "$B"
(umask 077; mkdir -p "$KEYS")
# MySQL root on armbian: through the root-only helper once production has been migrated, else the old way.
MYSQL_ROOT='if sudo -n test -x /usr/local/sbin/st-mysql-root; then sudo /usr/local/sbin/st-mysql-root; elif sudo -n test -s /root/sharetoken/mysql-root.pass; then sudo bash -c '"'"'MYSQL_PWD=$(cat /root/sharetoken/mysql-root.pass) exec docker exec -i -e MYSQL_PWD st-mysql mysql -h127.0.0.1 -uroot'"'"'; else cd ~/sharetoken-prod && docker exec -i st-mysql mysql -h127.0.0.1 -uroot -p"$(cat mysql-root.pass)" 2>/dev/null; fi'
conf_of() { case $1 in mailserver) echo mail.yaml;; *) echo $1.yaml;; esac; }
newpass() { [ -s "$1" ] || (umask 077; head -c 24 /dev/urandom | base64 | tr -d '/+=' > "$1"); cat "$1"; }
# upload_secret <本地文件> <暂存名> <最终路径>：最终路径已有就不传
upload_secret() { pi "sudo test -s $3" || { scp $Q "$1" "$RUSER@$PI:$DIR/$2" >/dev/null; pi "chmod 600 $DIR/$2"; }; }

cd "$REPO"
V=$(scripts/version.sh); echo "version $V"
upload_common() { # programs...
  pi "mkdir -p $DIR/releases/$V"
  local c
  for c in "$@"; do scp $Q "dist/$c-$V-linux-arm64" "$RUSER@$PI:$DIR/releases/$V/$c" >/dev/null; done
  scp $Q "$HERE"/*.yaml "$HERE"/sharetoken-dev-*.service "$REPO/scripts/ops/st-host-setup.sh" "$RUSER@$PI:$DIR/" >/dev/null
  pi "chmod +x $DIR/releases/$V/*; cd $DIR/releases && ls -1t | tail -n +6 | while read -r old; do rm -rf \"\$old\"; done"
  for c in "$@"; do pi "cd $DIR && releases/$V/$c -config $(conf_of $c) check-config >/dev/null && echo $c config ok"; done
}

if [ $PORTAL_ONLY = 1 ]; then
  echo "== build and upload (linux/arm64)"
  GOOS=linux GOARCH=arm64 scripts/build.sh portal >/dev/null
  upload_common portal
  echo "== client packages"
  scripts/publish-packages.sh "$V" "$RUSER@$PI" /var/lib/sharetoken-dev/portal/downloads --with-dev-cli --owner st-portal
  echo "== install and restart portal"
  pi "$SETUP bins $V portal"
  exit 0
fi

echo "== build (linux/arm64)"
GOOS=linux GOARCH=arm64 scripts/build.sh portal coordinator mailserver >/dev/null
echo "== upload"
upload_common portal coordinator mailserver

if [ $BINS_ONLY = 1 ]; then
  echo "== install and restart"
  pi "$SETUP bins $V portal coordinator mailserver"
  exit 0
fi

echo "== keys (dev only; the relays' dev network trusts these, so they are never regenerated here)"
for k in coordinator.key portal-pass.key; do
  [ -s "$KEYS/$k" ] || { echo "missing $KEYS/$k (docs/dev-env.md)"; exit 1; }
done
upload_secret "$KEYS/coordinator.key" coordinator.key $ETC/coordinator/coordinator.key
upload_secret "$KEYS/portal-pass.key" portal-pass.key $ETC/portal/portal-pass.key

echo "== MySQL on armbian: its own database; one account per process, only for dev"
PP=$(newpass "$KEYS/portal-db.pass"); CP=$(newpass "$KEYS/coord-db.pass"); MP=$(newpass "$KEYS/mail-db.pass")
{ printf "CREATE DATABASE IF NOT EXISTS sharetoken_dev;\n"
  for a in "sharetoken_dev_portal $PP" "sharetoken_dev_coord $CP"; do set -- $a
    printf "CREATE USER IF NOT EXISTS '%s'@'%%' IDENTIFIED BY '%s';\nALTER USER '%s'@'%%' IDENTIFIED BY '%s';\nGRANT ALL PRIVILEGES ON sharetoken_dev.* TO '%s'@'%%';\n" "$1" "$2" "$1" "$2" "$1"; done
  # the mail account can touch nothing but its tables (the portal creates them)
  printf "CREATE USER IF NOT EXISTS 'sharetoken_mail_dev'@'%%' IDENTIFIED BY '%s';\nALTER USER 'sharetoken_mail_dev'@'%%' IDENTIFIED BY '%s';\n" "$MP" "$MP"
  for t in mail_outbox mail_inbox mail_status; do printf "GRANT SELECT, INSERT, UPDATE, DELETE ON sharetoken_dev.%s TO 'sharetoken_mail_dev'@'%%';\n" "$t"; done
  printf "GRANT SELECT ON sharetoken_dev.schema_migrations TO 'sharetoken_mail_dev'@'%%';\n"
} | ssh $Q "$RUSER@$ARMBIAN" "$MYSQL_ROOT"
upload_secret "$KEYS/portal-db.pass" portal-db.pass $ETC/portal/db.pass
upload_secret "$KEYS/coord-db.pass" coord-db.pass $ETC/coordinator/db.pass
upload_secret "$KEYS/mail-db.pass" mail-db.pass $ETC/mail/db.pass

echo "== SSH tunnel Pi -> armbian MySQL (the key can only forward to 127.0.0.1:3306, and run nothing)"
if pi "sudo test -s $ETC/dbtunnel/dbtunnel.key"; then
  TUNNEL_PUB=$(pi "sudo ssh-keygen -y -f $ETC/dbtunnel/dbtunnel.key")
else
  pi "cd $DIR && { test -s dbtunnel.key || ssh-keygen -q -t ed25519 -N '' -C sharetoken-dev-dbtunnel -f dbtunnel.key; }; chmod 600 dbtunnel.key"
  TUNNEL_PUB=$(pi "cat $DIR/dbtunnel.key.pub")
fi
pi "cd $DIR && ssh-keyscan -t ed25519 192.168.6.91 2>/dev/null > dbtunnel.known_hosts; test -s dbtunnel.known_hosts"
# restrict alone still lets the key run commands: command= is what takes the shell away.
printf 'command="/usr/bin/false",restrict,port-forwarding,permitopen="127.0.0.1:3306" %s\n' "$TUNNEL_PUB" \
  | ssh $Q "$RUSER@$ARMBIAN" 'cat > /tmp/st-dev-tunnel.pub; sed -i "/ sharetoken-dev-dbtunnel\$/d" ~/.ssh/authorized_keys; cat /tmp/st-dev-tunnel.pub >> ~/.ssh/authorized_keys; rm -f /tmp/st-dev-tunnel.pub'

echo "== mail: DKIM key (public half is in DNS, scripts/mail_dns.py)"
[ -s "$KEYS/dkim.key" ] || (umask 077; openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out "$KEYS/dkim.key" 2>/dev/null)
upload_secret "$KEYS/dkim.key" dkim.key $ETC/mail/dkim.key

echo "== public entry on armbian (only adds a site; production's site is untouched)"
if ! arm "sudo test -s /etc/letsencrypt/live/$DOMAIN/fullchain.pem"; then
  ssh $Q "$RUSER@$ARMBIAN" "sudo tee /etc/nginx/conf.d/sharetoken-dev.conf >/dev/null" <<N
server {
    listen 80;
    listen [::]:80;
    server_name $DOMAIN;
    location /.well-known/acme-challenge/ { root /var/www/html; }
    location / { return 404; }
}
N
  arm "sudo nginx -t 2>/dev/null && sudo systemctl reload nginx"
  arm "sudo certbot certonly --webroot -w /var/www/html -d $DOMAIN --email $EMAIL --agree-tos --no-eff-email --non-interactive"
fi
scp $Q "$HERE/nginx-armbian.conf" "$RUSER@$ARMBIAN:/tmp/sharetoken-dev.conf" >/dev/null
arm "sudo cp /tmp/sharetoken-dev.conf /etc/nginx/conf.d/sharetoken-dev.conf && rm /tmp/sharetoken-dev.conf && sudo nginx -t 2>/dev/null && sudo systemctl reload nginx"

echo "== install on the Pi: users, directories, secrets, units; then restart"
pi "$SETUP install $V"
