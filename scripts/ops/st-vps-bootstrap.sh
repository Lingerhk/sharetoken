#!/bin/bash
# 新建云主机的第一次准备，以 root 运行一次，可重复运行：部署账号、软件包、swap、防火墙、sshd。
# 之后的事都由 scripts/prod/deploy.sh 以部署账号完成（docs/server-layout.md）。
#   ssh root@<ip> 'bash -s' < scripts/ops/st-vps-bootstrap.sh
# 部署账号的 SSH 公钥来自 root 的 authorized_keys 里的 ssh-ed25519 条目。
set -euo pipefail
[ "$(id -u)" = 0 ] || { echo "run as root" >&2; exit 2; }
ADMIN=${ADMIN:-s0nnet}
export DEBIAN_FRONTEND=noninteractive

echo "== deploy account $ADMIN (key login, passwordless sudo, not in the docker group)"
id "$ADMIN" >/dev/null 2>&1 || useradd --create-home --shell /bin/bash "$ADMIN"
install -d -o "$ADMIN" -g "$ADMIN" -m 700 "/home/$ADMIN/.ssh"
grep '^ssh-ed25519 ' /root/.ssh/authorized_keys > "/home/$ADMIN/.ssh/authorized_keys"
[ -s "/home/$ADMIN/.ssh/authorized_keys" ] || { echo "root has no ssh-ed25519 key to copy" >&2; exit 1; }
chown "$ADMIN:$ADMIN" "/home/$ADMIN/.ssh/authorized_keys"; chmod 600 "/home/$ADMIN/.ssh/authorized_keys"
echo "$ADMIN ALL=(root) NOPASSWD: ALL" > "/etc/sudoers.d/90-$ADMIN"; chmod 440 "/etc/sudoers.d/90-$ADMIN"
visudo -cq

echo "== sshd: keys only"
cat > /etc/ssh/sshd_config.d/10-sharetoken.conf <<'C'
PasswordAuthentication no
KbdInteractiveAuthentication no
PermitRootLogin prohibit-password
C
sshd -t
systemctl reload ssh

echo "== swap (the host has 1 GB of memory)"
if ! swapon --show=NAME --noheadings | grep -q .; then
  [ -e /swapfile ] || { fallocate -l 2G /swapfile; chmod 600 /swapfile; mkswap /swapfile >/dev/null; }
  swapon /swapfile
  grep -q '^/swapfile ' /etc/fstab || echo '/swapfile none swap sw 0 0' >> /etc/fstab
fi
echo 'vm.swappiness=10' > /etc/sysctl.d/90-sharetoken.conf; sysctl -q --system

echo "== packages"
apt-get update -qq
apt-get install -y -qq docker.io nginx certbot ufw unattended-upgrades >/dev/null
systemctl enable --now docker nginx >/dev/null 2>&1
rm -f /etc/nginx/sites-enabled/default
nginx -t 2>/dev/null && systemctl reload nginx

echo "== firewall: ssh, http, https, smtp"
ufw default deny incoming >/dev/null; ufw default allow outgoing >/dev/null
for p in 22 80 443 25; do ufw allow "$p/tcp" >/dev/null; done
ufw --force enable >/dev/null
ufw status | sed -n 1,12p
