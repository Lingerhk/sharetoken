#!/bin/bash
# 把门户 + 协调器部署（或滚动更新）到 armbian：Docker MySQL、systemd、nginx + Let's Encrypt、DDNS cron。
# 在 Mac 上运行；可重复运行（已有的密钥、密码、证书、数据库都不会被覆盖）。详见 docs/prod-env.md。
#   scripts/prod/deploy.sh            # 全部
#   scripts/prod/deploy.sh --bins     # 只更新二进制和配置并重启（日常发版）
#   scripts/prod/deploy.sh --mail     # 只更新门户和邮件服务，不动协调器
#   scripts/prod/deploy.sh --mail-server  # 只更新邮件服务：门户、协调器都不动（门户还没带邮件代码时也能收发）
#   scripts/prod/deploy.sh --portal   # 只更新门户和客户端安装包，协调器、邮件服务都不动（改网页、文档时用）
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd); REPO=$(cd "$HERE/../.." && pwd)
HOST=${ARMBIAN:-192.168.6.91}; RUSER=s0nnet; DIR=/home/$RUSER/sharetoken-prod
EMAIL=${LE_EMAIL:-lingerhk@gmail.com}
BINS_ONLY=0; [ "${1:-}" = "--bins" ] && BINS_ONLY=1
MAIL_ONLY=0; [ "${1:-}" = "--mail" ] && MAIL_ONLY=1
MAIL_SERVER_ONLY=0; [ "${1:-}" = "--mail-server" ] && MAIL_SERVER_ONLY=1
PORTAL_ONLY=0; [ "${1:-}" = "--portal" ] && PORTAL_ONLY=1
Q="-o LogLevel=ERROR -o ConnectTimeout=10"
rsh() { ssh $Q "$RUSER@$HOST" "$@"; }
W=${ST_TESTENV_WORK:-$HOME/st-test}
cd "$REPO"
V=$(scripts/version.sh); echo "version $V"
case $V in *-dev*) [ -n "${ST_ALLOW_DEV:-}" ] || { echo "$V is not a release: production only runs releases (tag v$(cat VERSION), or set ST_ALLOW_DEV=1)" >&2; exit 2; };; esac

# Builds programs and uploads them to releases/<version>/ on the server.
release() {
  GOOS=linux GOARCH=arm64 scripts/build.sh "$@" >/dev/null
  rsh "mkdir -p $DIR/releases/$V"
  local c
  # Through a new name: until launch every release is 1.0.0, so this may be the program that is running.
  # What it replaces stays as <name>.prev in the same directory.
  for c in "$@"; do
    scp $Q "dist/$c-$V-linux-arm64" "$RUSER@$HOST:$DIR/releases/$V/$c.new" >/dev/null
    rsh "cd $DIR/releases/$V && { [ ! -e $c ] || mv -f $c $c.prev; } && mv $c.new $c"
  done
  rsh "chmod +x $DIR/releases/$V/*; cd $DIR/releases && ls -1t | tail -n +6 | while read -r old; do rm -rf \"\$old\"; done"
}
# Points the program's name at this release; the previous link stays in <name>.prev.
activate() {
  local c
  for c in "$@"; do
    rsh "cd $DIR && if [ \"\$(readlink $c)\" = releases/$V/$c ]; then ln -sfn releases/$V/$c.prev $c.prev; elif [ -e $c ]; then cp -P $c $c.prev; fi; ln -sfn releases/$V/$c $c"
  done
}

# Portal and mail server only. The coordinator is left running: trades do not depend on either.
deploy_mail() {
  local keys=$W/prod-keys
  (umask 077; mkdir -p "$keys")
  [ -s "$keys/dkim.key" ] || (umask 077; openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out "$keys/dkim.key" 2>/dev/null)
  scp $Q "$keys/dkim.key" "$RUSER@$HOST:$DIR/dkim.key" >/dev/null; rsh "chmod 600 $DIR/dkim.key"
  # This account can touch nothing but the mail tables. The portal creates them, so it goes first.
  [ -s "$keys/mail-db.pass" ] || (umask 077; head -c 24 /dev/urandom | base64 | tr -d '/+=' > "$keys/mail-db.pass")
  local mp; mp=$(cat "$keys/mail-db.pass")
  printf "CREATE USER IF NOT EXISTS 'sharetoken_mail'@'%%' IDENTIFIED BY '%s';\nALTER USER 'sharetoken_mail'@'%%' IDENTIFIED BY '%s';\nGRANT SELECT, INSERT, UPDATE, DELETE ON sharetoken.mail_outbox TO 'sharetoken_mail'@'%%';\nGRANT SELECT, INSERT, UPDATE, DELETE ON sharetoken.mail_inbox TO 'sharetoken_mail'@'%%';\nGRANT SELECT, INSERT, UPDATE, DELETE ON sharetoken.mail_status TO 'sharetoken_mail'@'%%';\nGRANT SELECT ON sharetoken.schema_migrations TO 'sharetoken_mail'@'%%';\n" "$mp" "$mp" \
    | rsh 'cd ~/sharetoken-prod && docker exec -i st-mysql mysql -h127.0.0.1 -uroot -p"$(cat mysql-root.pass)" 2>/dev/null'
  scp $Q "$keys/mail-db.pass" "$RUSER@$HOST:$DIR/mail-db.pass" >/dev/null; rsh "chmod 600 $DIR/mail-db.pass"

  # Certificate for STARTTLS. nginx only answers the ACME challenge for this name.
  rsh "sudo tee /etc/nginx/conf.d/sharetoken-mail.conf >/dev/null" <<'N'
server {
    listen 80;
    listen [::]:80;
    server_name mail.sharetoken.app;
    location /.well-known/acme-challenge/ { root /var/www/html; }
    location / { return 404; }
}
N
  rsh "sudo nginx -t 2>/dev/null && sudo systemctl reload nginx"
  rsh "sudo test -s /etc/letsencrypt/live/mail.sharetoken.app/fullchain.pem || sudo certbot certonly --webroot -w /var/www/html -d mail.sharetoken.app --email $EMAIL --agree-tos --no-eff-email --non-interactive"
  # The mail server does not run as root: certbot hands it a copy on every renewal.
  rsh "sudo tee /etc/letsencrypt/renewal-hooks/deploy/sharetoken-mail.sh >/dev/null" <<'H'
#!/bin/sh
d=/home/s0nnet/sharetoken-prod/mail-tls
install -d -o s0nnet -g s0nnet -m 700 "$d"
install -o s0nnet -g s0nnet -m 644 /etc/letsencrypt/live/mail.sharetoken.app/fullchain.pem "$d/fullchain.pem"
install -o s0nnet -g s0nnet -m 600 /etc/letsencrypt/live/mail.sharetoken.app/privkey.pem "$d/privkey.pem"
H
  rsh "sudo chmod 755 /etc/letsencrypt/renewal-hooks/deploy/sharetoken-mail.sh && sudo /etc/letsencrypt/renewal-hooks/deploy/sharetoken-mail.sh"

  scp $Q "$HERE/sharetoken-mail.service" "$HERE/mail.yaml" "$RUSER@$HOST:$DIR/" >/dev/null
  rsh "cd $DIR && releases/$V/mailserver -config mail.yaml check-config >/dev/null && sudo cp sharetoken-mail.service /etc/systemd/system/ && sudo systemctl daemon-reload && sudo systemctl enable sharetoken-mail >/dev/null 2>&1; sudo systemctl stop sharetoken-mail 2>/dev/null; true"
  activate mailserver
  rsh "sudo systemctl start sharetoken-mail; sleep 3; systemctl is-active sharetoken-mail"
}

if [ $PORTAL_ONLY = 1 ]; then
  echo "== build and upload (linux/arm64)"
  release portal
  scp $Q "$HERE/portal.yaml" "$RUSER@$HOST:$DIR/portal.yaml.new" >/dev/null
  rsh "cd $DIR && releases/$V/portal -config portal.yaml.new check-config >/dev/null && echo portal config ok"
  echo "== client packages"
  "$REPO/scripts/publish-cli.sh" prod "$V" "$RUSER@$HOST" "$DIR/downloads"
  echo "== restart portal (keeps portal.prev)"
  rsh "cd $DIR && cp portal.yaml portal.yaml.prev && sudo systemctl stop sharetoken-portal && mv portal.yaml.new portal.yaml"
  activate portal
  rsh "sudo systemctl start sharetoken-portal; sleep 3; systemctl is-active sharetoken-portal"
  exit 0
fi

if [ $MAIL_SERVER_ONLY = 1 ]; then
  echo "== build and upload (linux/arm64)"
  release mailserver
  scp $Q "$HERE/mail.yaml" "$RUSER@$HOST:$DIR/" >/dev/null
  scp $Q "$REPO/scripts/godaddy_ddns.sh" "$RUSER@$HOST:$DIR/ddns.sh"
  rsh "cd $DIR && chmod +x ddns.sh && $DIR/ddns.sh"
  echo "== mail tables (the portal normally creates them; the mail account may not, so this uses the portal's account once)"
  rsh "cd $DIR && sed -e 's#sharetoken_mail@#sharetoken@#' -e 's#mail-db.pass#db.pass#' mail.yaml > mail-migrate.yaml && releases/$V/mailserver -config mail-migrate.yaml migrate; rc=\$?; rm -f mail-migrate.yaml; exit \$rc"
  echo "== mail server"
  deploy_mail
  exit 0
fi

if [ $MAIL_ONLY = 1 ]; then
  echo "== build and upload (linux/arm64)"
  release portal mailserver
  scp $Q "$HERE/portal.yaml" "$RUSER@$HOST:$DIR/portal.yaml.new" >/dev/null
  scp $Q "$REPO/scripts/godaddy_ddns.sh" "$RUSER@$HOST:$DIR/ddns.sh"
  rsh "cd $DIR && chmod +x ddns.sh && releases/$V/portal -config portal.yaml.new check-config >/dev/null && echo portal config ok"
  rsh "cd $DIR && $DIR/ddns.sh"
  echo "== restart portal (keeps portal.prev)"
  rsh "cd $DIR && cp portal.yaml portal.yaml.prev && sudo systemctl stop sharetoken-portal && mv portal.yaml.new portal.yaml"
  activate portal
  rsh "sudo systemctl start sharetoken-portal; sleep 3; systemctl is-active sharetoken-portal"
  echo "== mail server"
  deploy_mail
  exit 0
fi

echo "== build and upload (linux/arm64)"
release portal coordinator mailserver
rsh "mkdir -p $DIR/backup"
scp $Q "$HERE/portal.yaml" "$HERE/coordinator.yaml" "$RUSER@$HOST:$DIR/"
scp $Q "$REPO/scripts/godaddy_ddns.sh" "$RUSER@$HOST:$DIR/ddns.sh"
rsh "chmod +x $DIR/ddns.sh; cd $DIR && releases/$V/portal -config portal.yaml check-config >/dev/null && releases/$V/coordinator -config coordinator.yaml check-config >/dev/null && echo configs ok"

if [ $BINS_ONLY = 0 ]; then
echo "== keys (coordinator peer ID and portal pass key are the network's identity: reuse, never regenerate)"
for k in coordinator.key portal-pass.key; do
  if ! rsh "test -s $DIR/$k"; then
    [ -s "$W/prod-keys/$k" ] || { echo "missing $W/prod-keys/$k (copy the current key there once; see docs/prod-env.md)"; exit 1; }
    scp $Q "$W/prod-keys/$k" "$RUSER@$HOST:$DIR/$k" && rsh "chmod 600 $DIR/$k"
  fi
done

echo "== MySQL (docker, loopback only)"
rsh "bash -s" <<REMOTE
set -euo pipefail
cd $DIR
umask 077
[ -s db.pass ] || { head -c 24 /dev/urandom | base64 | tr -d '/+=' > db.pass; }
[ -s mysql-root.pass ] || { head -c 24 /dev/urandom | base64 | tr -d '/+=' > mysql-root.pass; }
if ! docker inspect st-mysql >/dev/null 2>&1; then
  docker run -d --name st-mysql --restart unless-stopped \
    -p 127.0.0.1:3306:3306 -v st-mysql-data:/var/lib/mysql \
    -e MYSQL_ROOT_PASSWORD="\$(cat mysql-root.pass)" -e MYSQL_DATABASE=sharetoken \
    -e MYSQL_USER=sharetoken -e MYSQL_PASSWORD="\$(cat db.pass)" \
    --memory 600m \
    mysql:8.0 --transaction-isolation=READ-COMMITTED --skip-name-resolve \
    --character-set-server=utf8mb4 --collation-server=utf8mb4_bin \
    --innodb-buffer-pool-size=256M --max-connections=100 --performance-schema=OFF \
    --mysqlx=OFF --binlog-expire-logs-seconds=86400
fi
# the image's entrypoint first runs a socket-only temp server: wait for the real one on TCP
for i in \$(seq 90); do docker exec st-mysql mysql -h127.0.0.1 -uroot -p"\$(cat mysql-root.pass)" -e "select 1" >/dev/null 2>&1 && break; sleep 2; done
docker exec st-mysql mysql -h127.0.0.1 -uroot -p"\$(cat mysql-root.pass)" -N -e "select version()" 2>/dev/null
# nightly dump, keep 7
cat > backup.sh <<'B'
#!/bin/bash
cd /home/s0nnet/sharetoken-prod && docker exec st-mysql mysqldump -uroot -p"\$(cat mysql-root.pass)" --single-transaction sharetoken 2>/dev/null | gzip > backup/sharetoken-\$(date +%F).sql.gz && ls -1t backup/*.sql.gz | tail -n +8 | xargs -r rm
B
chmod +x backup.sh
REMOTE

echo "== systemd"
scp $Q "$HERE/sharetoken-portal.service" "$HERE/sharetoken-coordinator.service" "$RUSER@$HOST:$DIR/"
rsh "sudo cp $DIR/sharetoken-*.service /etc/systemd/system/ && sudo systemctl daemon-reload && sudo systemctl enable sharetoken-portal sharetoken-coordinator >/dev/null 2>&1"

echo "== DNS (GoDaddy A records for sharetoken.app + www)"
if ! rsh "test -s ~/.config/sharetoken/ddns.env"; then
  rsh "mkdir -p ~/.config/sharetoken && umask 077 && : > ~/.config/sharetoken/ddns.env"
  grep '^GODADDY_ACCESS_TOKEN=' "$REPO/config.env" | rsh "cat >> ~/.config/sharetoken/ddns.env"
fi
rsh "$DIR/ddns.sh"

echo "== nginx + Let's Encrypt"
if ! rsh "sudo test -s /etc/letsencrypt/live/sharetoken.app/fullchain.pem"; then
  # bootstrap: port 80 only, so certbot can answer the HTTP-01 challenge before any cert exists
  rsh "sudo tee /etc/nginx/conf.d/sharetoken.conf >/dev/null" <<'N'
server {
    listen 80;
    listen [::]:80;
    server_name sharetoken.app www.sharetoken.app;
    location /.well-known/acme-challenge/ { root /var/www/html; }
    location / { return 404; }
}
N
  rsh "sudo nginx -t && sudo systemctl reload nginx"
  rsh "sudo certbot certonly --webroot -w /var/www/html -d sharetoken.app -d www.sharetoken.app --email $EMAIL --agree-tos --no-eff-email --non-interactive"
fi
scp $Q "$HERE/nginx-sharetoken.conf" "$RUSER@$HOST:$DIR/"
rsh "sudo cp $DIR/nginx-sharetoken.conf /etc/nginx/conf.d/sharetoken.conf && sudo nginx -t && sudo systemctl reload nginx"
# the existing certbot.timer renews; the deploy hook (reload-nginx.sh) already reloads nginx
rsh "sudo certbot renew --dry-run --cert-name sharetoken.app 2>&1 | tail -3"

echo "== cron (DDNS every 10 min, MySQL dump nightly)"
rsh "( crontab -l 2>/dev/null | grep -v -e sharetoken-prod/ddns.sh -e sharetoken-prod/backup.sh; \
  echo '*/10 * * * * $DIR/ddns.sh >> $DIR/ddns.log 2>&1'; \
  echo '17 3 * * * $DIR/backup.sh' ) | crontab -"
fi

echo "== restart services"
rsh "sudo systemctl stop sharetoken-coordinator sharetoken-portal"
activate portal coordinator
rsh "sudo systemctl start sharetoken-portal; sleep 3; sudo systemctl start sharetoken-coordinator; sleep 3; systemctl is-active sharetoken-coordinator sharetoken-portal"

echo "== mail server"
deploy_mail
