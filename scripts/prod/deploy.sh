#!/bin/bash
# 把门户 + 协调器部署（或滚动更新）到 armbian：Docker MySQL、systemd、nginx + Let's Encrypt、DDNS cron。
# 在 Mac 上运行；可重复运行（已有的密钥、密码、证书、数据库都不会被覆盖）。详见 docs/prod-env.md。
#   scripts/prod/deploy.sh            # 全部
#   scripts/prod/deploy.sh --bins     # 只更新二进制和配置并重启（日常发版）
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd); REPO=$(cd "$HERE/../.." && pwd)
HOST=${ARMBIAN:-192.168.6.91}; RUSER=s0nnet; DIR=/home/$RUSER/sharetoken-prod
EMAIL=${LE_EMAIL:-lingerhk@gmail.com}
BINS_ONLY=0; [ "${1:-}" = "--bins" ] && BINS_ONLY=1
Q="-o LogLevel=ERROR -o ConnectTimeout=10"
rsh() { ssh $Q "$RUSER@$HOST" "$@"; }
W=${ST_TESTENV_WORK:-$HOME/st-test}; B=$W/build/arm64; mkdir -p "$B"

echo "== build (linux/arm64)"
cd "$REPO"
for c in portal coordinator; do
  GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "$B/$c" ./cmd/$c
done

echo "== upload"
rsh "mkdir -p $DIR/backup"
scp $Q "$B/portal" "$B/coordinator" "$RUSER@$HOST:$DIR/" >/dev/null
scp $Q "$HERE/portal.yaml" "$HERE/coordinator.yaml" "$RUSER@$HOST:$DIR/"
scp $Q "$REPO/scripts/godaddy_ddns.sh" "$RUSER@$HOST:$DIR/ddns.sh"
rsh "chmod +x $DIR/ddns.sh $DIR/portal $DIR/coordinator; cd $DIR && ./portal -config portal.yaml check-config >/dev/null && ./coordinator -config coordinator.yaml check-config >/dev/null && echo configs ok" || true

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
rsh "sudo systemctl restart sharetoken-coordinator sharetoken-portal; sleep 3; systemctl is-active sharetoken-coordinator sharetoken-portal"
