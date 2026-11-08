#!/bin/bash
# 部署 dev 环境（docs/dev-env.md）：树莓派上的门户 + 协调器；armbian 上的 nginx 入口和 MySQL 独立库。
# 在 Mac 上运行；可重复运行（已有的密钥、数据库、证书都不会被覆盖）。
#   scripts/dev/deploy.sh            # 全部
#   scripts/dev/deploy.sh --bins     # 只更新二进制和配置并重启（含邮件服务）
# 不碰中继（scripts/relay/），也不碰生产服务：armbian 上只增加 nginx 站点、MySQL 库和账号、一把受限的隧道密钥。
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd); REPO=$(cd "$HERE/../.." && pwd)
PI=192.168.6.148; ARMBIAN=192.168.6.91; RUSER=s0nnet; DIR=/home/$RUSER/sharetoken-dev
DOMAIN=dev.sharetoken.app; EMAIL=${LE_EMAIL:-lingerhk@gmail.com}
BINS_ONLY=0; [ "${1:-}" = "--bins" ] && BINS_ONLY=1
Q="-o LogLevel=ERROR -o ConnectTimeout=10"
pi() { ssh $Q -n "$RUSER@$PI" "$@"; }
arm() { ssh $Q -n "$RUSER@$ARMBIAN" "$@"; }
W=${ST_TESTENV_WORK:-$HOME/st-test}; B=$W/build/arm64; KEYS=$W/dev-keys; mkdir -p "$B"
(umask 077; mkdir -p "$KEYS")

echo "== build (linux/arm64)"
cd "$REPO"
V=$(scripts/version.sh); echo "version $V"
GOOS=linux GOARCH=arm64 scripts/build.sh portal coordinator mailserver >/dev/null

echo "== upload"
pi "mkdir -p $DIR/backup $DIR/releases/$V"
for c in portal coordinator mailserver; do
  scp $Q "dist/$c-$V-linux-arm64" "$RUSER@$PI:$DIR/releases/$V/$c" >/dev/null
done
scp $Q "$HERE/portal.yaml" "$HERE/coordinator.yaml" "$HERE/mail.yaml" "$RUSER@$PI:$DIR/" >/dev/null
pi "chmod +x $DIR/releases/$V/*; cd $DIR/releases && ls -1t | tail -n +6 | while read -r old; do rm -rf \"\$old\"; done"

if [ $BINS_ONLY = 0 ]; then
  echo "== keys (dev only; the relays' dev network trusts these, so they are never regenerated here)"
  for k in coordinator.key portal-pass.key; do
    [ -s "$KEYS/$k" ] || { echo "missing $KEYS/$k (docs/dev-env.md)"; exit 1; }
    pi "test -s $DIR/$k" || { scp $Q "$KEYS/$k" "$RUSER@$PI:$DIR/$k" >/dev/null; pi "chmod 600 $DIR/$k"; }
  done

  echo "== MySQL on armbian: its own database and account, only for dev"
  [ -s "$KEYS/db.pass" ] || (umask 077; head -c 24 /dev/urandom | base64 | tr -d '/+=' > "$KEYS/db.pass")
  { printf "CREATE DATABASE IF NOT EXISTS sharetoken_dev;\nCREATE USER IF NOT EXISTS 'sharetoken_dev'@'%%' IDENTIFIED BY '%s';\nALTER USER 'sharetoken_dev'@'%%' IDENTIFIED BY '%s';\nGRANT ALL PRIVILEGES ON sharetoken_dev.* TO 'sharetoken_dev'@'%%';\n" "$(cat "$KEYS/db.pass")" "$(cat "$KEYS/db.pass")"; } \
    | ssh $Q "$RUSER@$ARMBIAN" 'cd ~/sharetoken-prod && docker exec -i st-mysql mysql -h127.0.0.1 -uroot -p"$(cat mysql-root.pass)" 2>/dev/null'
  scp $Q "$KEYS/db.pass" "$RUSER@$PI:$DIR/db.pass" >/dev/null; pi "chmod 600 $DIR/db.pass"

  echo "== SSH tunnel Pi -> armbian MySQL (the key can only forward to 127.0.0.1:3306, and run nothing)"
  pi "cd $DIR && { test -s dbtunnel.key || ssh-keygen -q -t ed25519 -N '' -C sharetoken-dev-dbtunnel -f dbtunnel.key; }; chmod 600 dbtunnel.key; ssh-keyscan -t ed25519 192.168.6.91 2>/dev/null > dbtunnel.known_hosts; test -s dbtunnel.known_hosts"
  TUNNEL_PUB=$(pi "cat $DIR/dbtunnel.key.pub")
  # restrict alone still lets the key run commands: command= is what takes the shell away.
  printf 'command="/usr/bin/false",restrict,port-forwarding,permitopen="127.0.0.1:3306" %s\n' "$TUNNEL_PUB" \
    | ssh $Q "$RUSER@$ARMBIAN" 'cat > /tmp/st-dev-tunnel.pub; sed -i "/ sharetoken-dev-dbtunnel\$/d" ~/.ssh/authorized_keys; cat /tmp/st-dev-tunnel.pub >> ~/.ssh/authorized_keys; rm -f /tmp/st-dev-tunnel.pub'

  echo "== systemd"
  scp $Q "$HERE/sharetoken-dev-dbtunnel.service" "$HERE/sharetoken-dev-coordinator.service" "$HERE/sharetoken-dev-portal.service" "$RUSER@$PI:$DIR/" >/dev/null
  pi "sudo cp $DIR/sharetoken-dev-*.service /etc/systemd/system/ && sudo systemctl daemon-reload && sudo systemctl enable sharetoken-dev-dbtunnel sharetoken-dev-coordinator sharetoken-dev-portal >/dev/null 2>&1; sudo systemctl restart sharetoken-dev-dbtunnel; sleep 2; systemctl is-active sharetoken-dev-dbtunnel"

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
fi

echo "== restart dev services"
pi "cd $DIR && releases/$V/coordinator -config coordinator.yaml check-config >/dev/null && releases/$V/portal -config portal.yaml check-config >/dev/null && sudo systemctl stop sharetoken-dev-portal sharetoken-dev-coordinator; [ -e portal ] && cp -P portal portal.prev; [ -e coordinator ] && cp -P coordinator coordinator.prev; ln -sfn releases/$V/portal portal; ln -sfn releases/$V/coordinator coordinator; sudo systemctl start sharetoken-dev-coordinator; sleep 3; sudo systemctl start sharetoken-dev-portal; sleep 3; systemctl is-active sharetoken-dev-coordinator sharetoken-dev-portal"

echo "== mail server (docs/mail.md)"
# The key is kept on the Mac beside the other dev keys; its public half is in DNS (scripts/mail_dns.py).
if [ ! -s "$KEYS/dkim.key" ]; then (umask 077; openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out "$KEYS/dkim.key" 2>/dev/null); fi
scp $Q "$KEYS/dkim.key" "$RUSER@$PI:$DIR/dkim.key" >/dev/null; pi "chmod 600 $DIR/dkim.key"
# The tables exist now that the portal has started. This account can touch nothing but them.
[ -s "$KEYS/mail-db.pass" ] || (umask 077; head -c 24 /dev/urandom | base64 | tr -d '/+=' > "$KEYS/mail-db.pass")
MP=$(cat "$KEYS/mail-db.pass")
printf "CREATE USER IF NOT EXISTS 'sharetoken_mail_dev'@'%%' IDENTIFIED BY '%s';\nALTER USER 'sharetoken_mail_dev'@'%%' IDENTIFIED BY '%s';\nGRANT SELECT, INSERT, UPDATE, DELETE ON sharetoken_dev.mail_outbox TO 'sharetoken_mail_dev'@'%%';\nGRANT SELECT, INSERT, UPDATE, DELETE ON sharetoken_dev.mail_inbox TO 'sharetoken_mail_dev'@'%%';\nGRANT SELECT, INSERT, UPDATE, DELETE ON sharetoken_dev.mail_status TO 'sharetoken_mail_dev'@'%%';\nGRANT SELECT ON sharetoken_dev.schema_migrations TO 'sharetoken_mail_dev'@'%%';\n" "$MP" "$MP" \
  | ssh $Q "$RUSER@$ARMBIAN" 'cd ~/sharetoken-prod && docker exec -i st-mysql mysql -h127.0.0.1 -uroot -p"$(cat mysql-root.pass)" 2>/dev/null'
scp $Q "$KEYS/mail-db.pass" "$RUSER@$PI:$DIR/mail-db.pass" >/dev/null; pi "chmod 600 $DIR/mail-db.pass"
scp $Q "$HERE/sharetoken-dev-mail.service" "$RUSER@$PI:$DIR/" >/dev/null
pi "sudo cp $DIR/sharetoken-dev-mail.service /etc/systemd/system/ && sudo systemctl daemon-reload && sudo systemctl enable sharetoken-dev-mail >/dev/null 2>&1; cd $DIR && releases/$V/mailserver -config mail.yaml check-config >/dev/null && sudo systemctl stop sharetoken-dev-mail; [ -e mailserver ] && cp -P mailserver mailserver.prev; ln -sfn releases/$V/mailserver mailserver; sudo systemctl start sharetoken-dev-mail; sleep 3; systemctl is-active sharetoken-dev-mail"
