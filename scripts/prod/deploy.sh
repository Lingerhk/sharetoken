#!/bin/bash
# 把门户 + 协调器 + 邮件服务部署（或滚动更新）到生产云主机：Docker MySQL、systemd、nginx + Let's Encrypt、备份。
# 在 Mac 上运行；可重复运行（已有的密钥、密码、证书、数据库都不会被覆盖）。详见 docs/prod-env.md。
# 新主机先以 root 跑一次 scripts/ops/st-vps-bootstrap.sh。
#   scripts/prod/deploy.sh            # 全部
#   scripts/prod/deploy.sh --prepare  # 只上传、建 MySQL 和账号、装好文件，不申请证书、不启动服务（换主机时，切换前用）
#   scripts/prod/deploy.sh --bins     # 只更新二进制和配置并重启（日常发版）
#   scripts/prod/deploy.sh --mail     # 只更新门户和邮件服务，不动协调器
#   scripts/prod/deploy.sh --mail-server  # 只更新邮件服务：门户、协调器都不动
#   scripts/prod/deploy.sh --portal   # 只更新门户和客户端安装包，协调器、邮件服务都不动（改网页、文档时用）
# 服务器上的布局和权限由 scripts/ops/st-host-setup.sh 负责：三个服务各自的系统用户，机密在 /etc/sharetoken/<服务>/，
# 二进制在 /srv/sharetoken/，MySQL root 密码只有 root 可读（/usr/local/sbin/st-mysql-root）。这里只把文件上传到
# 暂存目录 ~/sharetoken-prod 再调用它。
# ST_PREBUILT=1：不编译，直接用 dist/ 里已有的 <程序>-<版本>-linux-amd64（配合 ST_VERSION 指定版本）。
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd); REPO=$(cd "$HERE/../.." && pwd)
HOST=${ST_PROD_HOST:-165.227.3.194}; ARCH=amd64; RUSER=s0nnet; DIR=/home/$RUSER/sharetoken-prod
ETC=/etc/sharetoken; SETUP="sudo bash $DIR/st-host-setup.sh prod $DIR"
EMAIL=${LE_EMAIL:-lingerhk@gmail.com}
MODE=${1:-}
Q="-o LogLevel=ERROR -o ConnectTimeout=10"
rsh() { ssh $Q "$RUSER@$HOST" "$@"; }
W=${ST_TESTENV_WORK:-$HOME/st-test}; KEYS=$W/prod-keys
(umask 077; mkdir -p "$KEYS")
MYSQL_ROOT='sudo /usr/local/sbin/st-mysql-root'
keydir_of() { case $1 in coordinator.key) echo coordinator;; *) echo portal;; esac; }
conf_of() { case $1 in mailserver) echo mail.yaml;; *) echo $1.yaml;; esac; }
newpass() { [ -s "$1" ] || (umask 077; head -c 24 /dev/urandom | base64 | tr -d '/+=' > "$1"); cat "$1"; }
# upload_secret <本地文件> <暂存名> <最终路径>：最终路径已有就不传
upload_secret() { rsh "sudo test -s $3" || { scp $Q "$1" "$RUSER@$HOST:$DIR/$2" >/dev/null; rsh "chmod 600 $DIR/$2"; }; }
cd "$REPO"
V=$(scripts/version.sh); echo "version $V"
case $V in *-dev*) [ -n "${ST_ALLOW_DEV:-}" ] || { echo "$V is not a release: production only runs releases (tag v$(cat VERSION), or set ST_ALLOW_DEV=1)" >&2; exit 2; };; esac

# Builds programs and uploads them with the configs, units and host scripts to the staging directory.
release() {
  local c
  [ -n "${ST_PREBUILT:-}" ] || GOOS=linux GOARCH=$ARCH scripts/build.sh "$@" >/dev/null
  rsh "mkdir -p $DIR/releases/$V"
  for c in "$@"; do scp $Q "dist/$c-$V-linux-$ARCH" "$RUSER@$HOST:$DIR/releases/$V/$c" >/dev/null; done
  scp $Q "$HERE"/*.yaml "$HERE"/sharetoken-*.service "$HERE/nginx-sharetoken.conf" "$REPO/scripts/ops/st-host-setup.sh" \
    "$REPO/scripts/ops/st-mysql-root" "$REPO/scripts/ops/st-mysql-backup" "$RUSER@$HOST:$DIR/" >/dev/null
  # the portal reads relay metrics over SSH (scripts/relay/authorize-metrics.sh prod …)
  if [ -s "$KEYS/relay-metrics.key" ]; then
    scp $Q "$KEYS/relay-metrics.key" "$HERE/relay-metrics.known_hosts" "$RUSER@$HOST:$DIR/" >/dev/null
    rsh "chmod 600 $DIR/relay-metrics.key"
  fi
  resend_key
  rsh "chmod +x $DIR/releases/$V/*; cd $DIR/releases && ls -1t | tail -n +6 | while read -r old; do rm -rf \"\$old\"; done"
  for c in "$@"; do rsh "cd $DIR && releases/$V/$c -config $(conf_of $c) check-config >/dev/null && echo $c config ok"; done
}

# Resend API key (docs/mail.md): ~/st-test/prod-keys/resend.key, re-installed on every deploy so it
# can be rotated. Without it the mail server refuses to start.
resend_key() {
  if [ -s "$KEYS/resend.key" ]; then
    scp $Q "$KEYS/resend.key" "$RUSER@$HOST:$DIR/resend.key" >/dev/null
    rsh "chmod 600 $DIR/resend.key"
  elif ! rsh "sudo test -s $ETC/mail/resend.key"; then
    echo "missing $KEYS/resend.key (docs/mail.md)"; exit 1
  fi
}

# Mail server certificate and key (the portal creates the mail tables, so it goes first).
deploy_mail() {
  [ -s "$KEYS/dkim.key" ] || (umask 077; openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out "$KEYS/dkim.key" 2>/dev/null)
  upload_secret "$KEYS/dkim.key" dkim.key $ETC/mail/dkim.key
  local mp; mp=$(newpass "$KEYS/mail-db.pass")
  { printf "CREATE USER IF NOT EXISTS 'sharetoken_mail'@'%%' IDENTIFIED BY '%s';\nALTER USER 'sharetoken_mail'@'%%' IDENTIFIED BY '%s';\n" "$mp" "$mp"
    for t in mail_outbox mail_inbox mail_status; do printf "GRANT SELECT, INSERT, UPDATE, DELETE ON sharetoken.%s TO 'sharetoken_mail'@'%%';\n" "$t"; done
    printf "GRANT SELECT ON sharetoken.schema_migrations TO 'sharetoken_mail'@'%%';\n"; } | rsh "$MYSQL_ROOT"
  upload_secret "$KEYS/mail-db.pass" mail-db.pass $ETC/mail/db.pass
  # Certificate for STARTTLS. nginx only answers the ACME challenge for this name; the deploy hook
  # (installed by st-host-setup.sh) hands st-mail a copy on every renewal.
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
}

if [ "$MODE" = --portal ]; then
  echo "== build and upload (linux/$ARCH)"
  release portal
  echo "== client packages"
  "$REPO/scripts/publish-packages.sh" "$V" "$RUSER@$HOST" /var/lib/sharetoken/portal/downloads --owner st-portal
  echo "== install and restart portal"
  rsh "$SETUP bins $V portal"
  exit 0
fi

if [ "$MODE" = --mail-server ]; then
  echo "== build and upload (linux/$ARCH)"
  release mailserver
  deploy_mail
  echo "== install and restart mail server"
  rsh "$SETUP bins $V mailserver"
  exit 0
fi

if [ "$MODE" = --mail ]; then
  echo "== build and upload (linux/$ARCH)"
  release portal mailserver
  deploy_mail
  echo "== install and restart portal, then mail server"
  rsh "$SETUP bins $V portal mailserver"
  exit 0
fi

echo "== build and upload (linux/$ARCH)"
release portal coordinator mailserver

if [ "$MODE" = --bins ]; then
  echo "== install and restart"
  rsh "$SETUP bins $V portal coordinator mailserver"
  exit 0
fi

echo "== keys (coordinator peer ID and portal pass key are the network's identity: reuse, never regenerate)"
for k in coordinator.key portal-pass.key; do
  rsh "sudo test -s $ETC/$(keydir_of $k)/$k" \
    || { [ -s "$KEYS/$k" ] || { echo "missing $KEYS/$k (copy the current key there once; see docs/prod-env.md)"; exit 1; }; }
done
[ -s "$KEYS/coordinator.key" ] && upload_secret "$KEYS/coordinator.key" coordinator.key $ETC/coordinator/coordinator.key
[ -s "$KEYS/portal-pass.key" ] && upload_secret "$KEYS/portal-pass.key" portal-pass.key $ETC/portal/portal-pass.key

echo "== MySQL (docker, loopback only; the root password never leaves the host)"
rsh "sudo install -o root -g root -m 700 $DIR/st-mysql-root /usr/local/sbin/st-mysql-root"
rsh "sudo bash -s" <<'REMOTE'
set -euo pipefail
umask 077
install -d -m 700 /root/sharetoken
[ -s /root/sharetoken/mysql-root.pass ] || head -c 24 /dev/urandom | base64 | tr -d '/+=' > /root/sharetoken/mysql-root.pass
if ! docker inspect st-mysql >/dev/null 2>&1; then
  # 1 GB host: the limit and the buffer pool leave room for the three services
  MYSQL_ROOT_PASSWORD=$(cat /root/sharetoken/mysql-root.pass); export MYSQL_ROOT_PASSWORD
  docker run -d --name st-mysql --restart unless-stopped \
    -p 127.0.0.1:3306:3306 -v st-mysql-data:/var/lib/mysql \
    -e MYSQL_ROOT_PASSWORD -e MYSQL_DATABASE=sharetoken \
    --memory 450m \
    mysql:8.0 --transaction-isolation=READ-COMMITTED --skip-name-resolve \
    --character-set-server=utf8mb4 --collation-server=utf8mb4_bin \
    --innodb-buffer-pool-size=128M --max-connections=60 --performance-schema=OFF \
    --mysqlx=OFF --binlog-expire-logs-seconds=86400
  # the image's entrypoint first runs a socket-only temp server: wait for the real one on TCP
  for i in $(seq 90); do /usr/local/sbin/st-mysql-root -e "select 1" >/dev/null 2>&1 && break; sleep 2; done
fi
REMOTE
printf "select version();\n" | rsh "$MYSQL_ROOT"
echo "== MySQL accounts: one per process"
PP=$(newpass "$KEYS/portal-db.pass"); CP=$(newpass "$KEYS/coord-db.pass")
for a in "sharetoken_portal $PP" "sharetoken_coord $CP"; do set -- $a
  printf "CREATE USER IF NOT EXISTS '%s'@'%%' IDENTIFIED BY '%s';\nALTER USER '%s'@'%%' IDENTIFIED BY '%s';\nGRANT ALL PRIVILEGES ON sharetoken.* TO '%s'@'%%';\n" "$1" "$2" "$1" "$2" "$1"; done | rsh "$MYSQL_ROOT"
upload_secret "$KEYS/portal-db.pass" portal-db.pass $ETC/portal/db.pass
upload_secret "$KEYS/coord-db.pass" coord-db.pass $ETC/coordinator/db.pass

if [ "$MODE" = --prepare ]; then
  [ -s "$KEYS/dkim.key" ] && upload_secret "$KEYS/dkim.key" dkim.key $ETC/mail/dkim.key
  [ -s "$KEYS/mail-db.pass" ] && upload_secret "$KEYS/mail-db.pass" mail-db.pass $ETC/mail/db.pass
  echo "== install without starting anything"
  rsh "$SETUP prepare $V"
  exit 0
fi

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
rsh "sudo cp $DIR/nginx-sharetoken.conf /etc/nginx/conf.d/sharetoken.conf && sudo nginx -t && sudo systemctl reload nginx"
rsh "sudo certbot renew --dry-run --no-random-sleep-on-renew --cert-name sharetoken.app 2>&1 | tail -3"

echo "== mail server certificate and account"
deploy_mail

echo "== install: users, directories, secrets, units, cron, certbot hooks; then restart"
rsh "$SETUP install $V"
