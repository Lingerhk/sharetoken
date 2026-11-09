#!/bin/bash
# 在服务器上以 root 运行：把门户、协调器、邮件服务装成各自独立的系统用户，机密只有对应的服务用户能读。
# 由 scripts/{prod,dev}/deploy.sh 上传到暂存目录后调用；可重复运行，已有的机密绝不覆盖。
#
#   st-host-setup.sh <prod|dev> <暂存目录> install <版本>          # 用户、目录、机密迁移、单元、cron，然后装二进制并重启全部
#   st-host-setup.sh <prod|dev> <暂存目录> bins <版本> <程序...>     # 只装这些程序的二进制和配置并重启对应服务
#   st-host-setup.sh <prod|dev> <暂存目录> config <程序...>          # 只装配置（不重启）
#   st-host-setup.sh <prod|dev> <暂存目录> restart <程序...>
#
# 布局（prod；dev 的根目录带 -dev 后缀，单元名带 dev-）：
#   /srv/sharetoken/releases/<版本>/{portal,coordinator,mailserver}   root 所有，只读；/srv/sharetoken/<程序> 是当前版本的软链
#   /etc/sharetoken/portal/{portal.yaml,db.pass,portal-pass.key}     目录 root:st-portal 0750；yaml 0640；机密 st-portal 0600
#   /etc/sharetoken/coordinator/{coordinator.yaml,db.pass,coordinator.key}
#   /etc/sharetoken/mail/{mail.yaml,db.pass,dkim.key,tls/}
#   /etc/sharetoken/ddns/ddns.env（prod）、/etc/sharetoken-dev/dbtunnel/（dev）
#   /var/lib/sharetoken/portal/downloads                               门户唯一可写目录（StateDirectory）
#   /root/sharetoken/mysql-root.pass                                   只有 root；st-mysql-root 用它
#   /root/sharetoken/migrated-<时间>/                                  迁移时从旧目录搬走的机密副本，确认无误后删除
set -euo pipefail
[ "$(id -u)" = 0 ] || { echo "run as root" >&2; exit 2; }
ENV=$1; STAGE=$2; ACTION=$3; shift 3
case $ENV in prod) SFX=""; UNIT=sharetoken-;; dev) SFX=-dev; UNIT=sharetoken-dev-;; *) echo "env must be prod or dev" >&2; exit 2;; esac
SRV=/srv/sharetoken$SFX; ETC=/etc/sharetoken$SFX; LIB=/var/lib/sharetoken$SFX
STAGE_USER=$(stat -c %U "$STAGE")
log() { echo "  $*"; }

unit_of() { case $1 in portal) echo "${UNIT}portal";; coordinator) echo "${UNIT}coordinator";; mailserver) echo "${UNIT}mail";; *) echo "unknown program $1" >&2; exit 2;; esac; }
user_of() { case $1 in portal) echo st-portal;; coordinator) echo st-coord;; mailserver) echo st-mail;; esac; }
conf_of() { case $1 in portal) echo portal.yaml;; coordinator) echo coordinator.yaml;; mailserver) echo mail.yaml;; esac; }
dir_of()  { case $1 in portal) echo portal;; coordinator) echo coordinator;; mailserver) echo mail;; esac; }

ensure_user() { # name home
  getent passwd "$1" >/dev/null || useradd --system --shell /usr/sbin/nologin --home-dir "$2" --no-create-home "$1"
}
# install_secret <源> <目标> <属主>：目标已存在则不动；源搬到 /root 的迁移副本目录
MIGRATED=/root/sharetoken/migrated-$(date +%Y%m%d-%H%M%S)
install_secret() {
  local src=$1 dst=$2 owner=$3
  if [ -e "$dst" ]; then
    chown "$owner:$owner" "$dst"; chmod 600 "$dst"
    [ -e "$src" ] && { mkdir -p -m 700 "$MIGRATED"; mv -f "$src" "$MIGRATED/"; log "moved stale copy $src -> $MIGRATED/"; }
    return 0
  fi
  [ -s "$src" ] || return 0
  install -o "$owner" -g "$owner" -m 600 "$src" "$dst"
  mkdir -p -m 700 "$MIGRATED"; mv -f "$src" "$MIGRATED/"
  log "$dst  (from $src, copy kept in $MIGRATED/)"
}

install_bins() { # version programs...
  local v=$1; shift
  local p
  install -d -o root -g root -m 755 "$SRV" "$SRV/releases" "$SRV/releases/$v"
  for p in "$@"; do
    [ -s "$STAGE/releases/$v/$p" ] || { echo "missing $STAGE/releases/$v/$p" >&2; exit 1; }
    install -o root -g root -m 755 "$STAGE/releases/$v/$p" "$SRV/releases/$v/$p.new"
    [ ! -e "$SRV/releases/$v/$p" ] || mv -f "$SRV/releases/$v/$p" "$SRV/releases/$v/$p.prev"
    mv -f "$SRV/releases/$v/$p.new" "$SRV/releases/$v/$p"
    # 当前链接留作 .prev；同版本重复发布时 .prev 指向这一版里的上一个文件
    if [ "$(readlink "$SRV/$p" 2>/dev/null)" = "releases/$v/$p" ]; then ln -sfn "releases/$v/$p.prev" "$SRV/$p.prev"
    elif [ -e "$SRV/$p" ]; then cp -P "$SRV/$p" "$SRV/$p.prev"; fi
    ln -sfn "releases/$v/$p" "$SRV/$p"
  done
  (cd "$SRV/releases" && ls -1t | tail -n +6 | while read -r old; do rm -rf "$old"; done)
}

install_config() { # programs...
  local p d u
  for p in "$@"; do
    d=$ETC/$(dir_of "$p"); u=$(user_of "$p")
    ensure_user "$u" "$d"
    install -d -o root -g "$u" -m 750 "$d"
    [ -s "$STAGE/$(conf_of "$p")" ] || { echo "missing $STAGE/$(conf_of "$p")" >&2; exit 1; }
    install -o root -g "$u" -m 640 "$STAGE/$(conf_of "$p")" "$d/$(conf_of "$p")"
  done
}

install_units() {
  local f
  for f in "$STAGE"/${UNIT}*.service; do
    [ -e "$f" ] || continue
    install -o root -g root -m 644 "$f" "/etc/systemd/system/$(basename "$f")"
  done
  systemctl daemon-reload
}

install_wrappers() {
  local p u
  for p in portal coordinator mailserver; do
    u=$(user_of "$p")
    cat > "/usr/local/bin/st-$p" <<W
#!/bin/sh
# 以服务用户运行 $p 的运维子命令，例如: st-portal users | overview | relay ls | grant …
# 需要环境变量时: sudo SHARETOKEN_PASSWORD=… st-portal useradd -admin root
if [ "\$(id -u)" != 0 ]; then exec sudo "\$0" "\$@"; fi
exec runuser -u $u -- $SRV/$p -config $ETC/$(dir_of "$p")/$(conf_of "$p") "\$@"
W
    chmod 755 "/usr/local/bin/st-$p"
  done
}

restart_services() { # programs...
  local p units=()
  for p in "$@"; do units+=("$(unit_of "$p")"); done
  systemctl enable "${units[@]}" >/dev/null 2>&1 || true
  systemctl restart "${units[@]}"
  sleep 3
  systemctl is-active "${units[@]}"
}

migrate_secrets() {
  local p
  for p in portal coordinator mailserver; do ensure_user "$(user_of "$p")" "$ETC/$(dir_of "$p")"; install -d -o root -g "$(user_of "$p")" -m 750 "$ETC/$(dir_of "$p")"; done
  # 数据库账号按进程分开；旧的共用 db.pass 只留迁移副本
  install_secret "$STAGE/portal-db.pass" "$ETC/portal/db.pass" st-portal
  install_secret "$STAGE/coord-db.pass" "$ETC/coordinator/db.pass" st-coord
  install_secret "$STAGE/mail-db.pass" "$ETC/mail/db.pass" st-mail
  install_secret "$STAGE/portal-pass.key" "$ETC/portal/portal-pass.key" st-portal
  install_secret "$STAGE/coordinator.key" "$ETC/coordinator/coordinator.key" st-coord
  install_secret "$STAGE/dkim.key" "$ETC/mail/dkim.key" st-mail
  if [ -s "$STAGE/db.pass" ]; then mkdir -p -m 700 "$MIGRATED"; mv -f "$STAGE/db.pass" "$MIGRATED/"; log "old shared db.pass -> $MIGRATED/ (the services now have their own accounts)"; fi
  # 邮件 TLS 证书：certbot 钩子会复制进来；首次迁移把旧副本搬过去
  install -d -o root -g st-mail -m 750 "$ETC/mail/tls"
  if [ -d "$STAGE/mail-tls" ]; then
    [ -s "$STAGE/mail-tls/fullchain.pem" ] && install -o st-mail -g st-mail -m 644 "$STAGE/mail-tls/fullchain.pem" "$ETC/mail/tls/fullchain.pem"
    [ -s "$STAGE/mail-tls/privkey.pem" ] && install -o st-mail -g st-mail -m 600 "$STAGE/mail-tls/privkey.pem" "$ETC/mail/tls/privkey.pem"
    rm -rf "$STAGE/mail-tls"
  fi
  # 门户的下载目录：唯一可写的地方
  install -d -o root -g root -m 755 "$LIB"
  install -d -o st-portal -g st-portal -m 755 "$LIB/portal" "$LIB/portal/downloads"
  if [ -d "$STAGE/downloads" ] && [ -n "$(ls -A "$STAGE/downloads" 2>/dev/null)" ]; then
    cp -a "$STAGE/downloads/." "$LIB/portal/downloads/"; chown -R st-portal:st-portal "$LIB/portal/downloads"; rm -rf "$STAGE/downloads"
    log "downloads -> $LIB/portal/downloads"
  fi
  rm -rf "$STAGE/downloads.old" "$STAGE/downloads.new"
  if [ $ENV = prod ]; then
    # MySQL root 密码：只有 root 用得着
    install -d -m 700 /root/sharetoken
    install_secret "$STAGE/mysql-root.pass" /root/sharetoken/mysql-root.pass root
    # DDNS：自己的用户和 token
    ensure_user st-ddns "$LIB/ddns"
    install -d -o st-ddns -g st-ddns -m 750 "$LIB/ddns"
    install -d -o root -g st-ddns -m 750 "$ETC/ddns"
    local home; home=$(getent passwd "$STAGE_USER" | cut -d: -f6)
    install_secret "$home/.config/sharetoken/ddns.env" "$ETC/ddns/ddns.env" st-ddns
    install -d -o root -g root -m 755 "$SRV"
    [ -s "$STAGE/ddns.sh" ] && install -o root -g root -m 755 "$STAGE/ddns.sh" "$SRV/ddns.sh"
    # 备份归 root
    install -d -m 700 /var/backups/sharetoken
    if [ -d "$STAGE/backup" ]; then mv -f "$STAGE"/backup/*.sql.gz /var/backups/sharetoken/ 2>/dev/null || true; rm -rf "$STAGE/backup"; fi
    chown -R root:root /var/backups/sharetoken; chmod 600 /var/backups/sharetoken/*.sql.gz 2>/dev/null || true
    rm -f "$STAGE/backup.sh"
  else
    ensure_user st-dbtunnel "$LIB/dbtunnel"
    install -d -o st-dbtunnel -g st-dbtunnel -m 700 "$LIB/dbtunnel"
    install -d -o root -g st-dbtunnel -m 750 "$ETC/dbtunnel"
    install_secret "$STAGE/dbtunnel.key" "$ETC/dbtunnel/dbtunnel.key" st-dbtunnel
    [ -s "$STAGE/dbtunnel.known_hosts" ] && install -o root -g st-dbtunnel -m 640 "$STAGE/dbtunnel.known_hosts" "$ETC/dbtunnel/dbtunnel.known_hosts"
    rm -f "$STAGE/dbtunnel.key.pub"
  fi
  # 旧布局留下的东西
  rm -f "$STAGE"/portal "$STAGE"/coordinator "$STAGE"/mailserver "$STAGE"/*.prev "$STAGE"/portal.yaml.prev "$STAGE"/nginx-sharetoken.conf
}

install_host_scripts() {
  [ $ENV = prod ] || return 0
  [ -s "$STAGE/st-mysql-root" ] && install -o root -g root -m 700 "$STAGE/st-mysql-root" /usr/local/sbin/st-mysql-root
  [ -s "$STAGE/st-mysql-backup" ] && install -o root -g root -m 700 "$STAGE/st-mysql-backup" /usr/local/sbin/st-mysql-backup
  cat > /etc/cron.d/sharetoken <<C
# sharetoken: DDNS every 10 min as st-ddns; MySQL dump nightly as root (/var/backups/sharetoken, keeps 7)
*/10 * * * * st-ddns GODADDY_ENV=$ETC/ddns/ddns.env $SRV/ddns.sh >> $LIB/ddns/ddns.log 2>&1
17 3 * * * root /usr/local/sbin/st-mysql-backup
C
  chmod 644 /etc/cron.d/sharetoken
  # 旧的用户 crontab 条目
  local home; home=$(getent passwd "$STAGE_USER" | cut -d: -f6)
  if crontab -u "$STAGE_USER" -l 2>/dev/null | grep -q "sharetoken-prod/\(ddns\|backup\).sh"; then
    crontab -u "$STAGE_USER" -l | grep -v -e "sharetoken-prod/ddns.sh" -e "sharetoken-prod/backup.sh" | crontab -u "$STAGE_USER" -
    log "removed ddns/backup from $STAGE_USER's crontab"
  fi
  rm -f "$STAGE/ddns.log"
  # certbot 把邮件证书交给 st-mail
  cat > /etc/letsencrypt/renewal-hooks/deploy/sharetoken-mail.sh <<H
#!/bin/sh
d=$ETC/mail/tls
install -d -o root -g st-mail -m 750 "\$d"
install -o st-mail -g st-mail -m 644 /etc/letsencrypt/live/mail.sharetoken.app/fullchain.pem "\$d/fullchain.pem"
install -o st-mail -g st-mail -m 600 /etc/letsencrypt/live/mail.sharetoken.app/privkey.pem "\$d/privkey.pem"
H
  chmod 755 /etc/letsencrypt/renewal-hooks/deploy/sharetoken-mail.sh
  [ -s /etc/letsencrypt/live/mail.sharetoken.app/privkey.pem ] && /etc/letsencrypt/renewal-hooks/deploy/sharetoken-mail.sh
  return 0
}

verify() {
  local p u bad=0
  echo "== verify"
  for p in portal coordinator mailserver; do
    u=$(user_of "$p")
    id -nG "$u" | grep -qw docker && { echo "  FAIL: $u is in the docker group"; bad=1; }
  done
  # 每个服务用户只能读自己的机密
  runuser -u st-portal -- cat "$ETC/coordinator/coordinator.key" >/dev/null 2>&1 && { echo "  FAIL: st-portal can read coordinator.key"; bad=1; }
  runuser -u st-coord -- cat "$ETC/portal/portal-pass.key" >/dev/null 2>&1 && { echo "  FAIL: st-coord can read portal-pass.key"; bad=1; }
  runuser -u st-mail -- cat "$ETC/portal/db.pass" >/dev/null 2>&1 && { echo "  FAIL: st-mail can read the portal's db.pass"; bad=1; }
  runuser -u st-portal -- cat "$ETC/portal/db.pass" >/dev/null 2>&1 || { echo "  FAIL: st-portal cannot read its own db.pass"; bad=1; }
  [ $ENV = prod ] && { runuser -u st-portal -- cat /root/sharetoken/mysql-root.pass >/dev/null 2>&1 && { echo "  FAIL: st-portal can read mysql-root.pass"; bad=1; }; }
  for p in portal coordinator mailserver; do
    printf '  %-28s %s\n' "$(unit_of "$p")" "$(systemctl is-active "$(unit_of "$p")") $(systemd-analyze security "$(unit_of "$p")" 2>/dev/null | tail -1)"
  done
  [ $bad = 0 ] && echo "  ok: each service user reads only its own secrets"
  return $bad
}

case $ACTION in
  install)
    V=$1
    echo "== users, directories, secrets"; migrate_secrets
    echo "== host scripts, cron, certbot hook"; install_host_scripts
    echo "== binaries $V"; install_bins "$V" portal coordinator mailserver
    echo "== configs"; install_config portal coordinator mailserver
    echo "== units"; install_units; install_wrappers
    if [ $ENV = dev ]; then systemctl enable "${UNIT}dbtunnel" >/dev/null 2>&1 || true; systemctl restart "${UNIT}dbtunnel"; sleep 2; systemctl is-active "${UNIT}dbtunnel"; fi
    echo "== restart"; systemctl stop "${UNIT}mail" "${UNIT}portal" "${UNIT}coordinator" 2>/dev/null || true
    restart_services coordinator portal mailserver
    verify
    ;;
  bins)
    V=$1; shift
    install_bins "$V" "$@"; install_config "$@"; install_units; install_wrappers
    restart_services "$@"
    ;;
  config) install_config "$@";;
  restart) restart_services "$@";;
  verify) verify;;
  *) echo "unknown action $ACTION" >&2; exit 2;;
esac
