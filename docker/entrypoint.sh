#!/bin/sh
# Entry point of the sharetoken image (see Dockerfile, docker-compose.yml).
#
#   st-entrypoint init          one-shot: write the configuration files, create the keys,
#                               register the relay with the portal, create the admin user
#   st-entrypoint portal | coordinator | relay
#   st-entrypoint <program> …   anything else is run as given, e.g. `portal users`
#
# State is under /var/lib/sharetoken/<program>: the configuration file and the
# keys of that program. `init` writes a configuration file only when there is
# none, so a file you have edited is kept.
set -eu
D=/var/lib/sharetoken
PORTAL_YAML=$D/portal/portal.yaml
COORD_YAML=$D/coordinator/coordinator.yaml
RELAY_YAML=$D/relay/relay.yaml

init() {
  host=${PUBLIC_HOST:-127.0.0.1}
  port=${RELAY_PORT:-4001}
  case $host in
    *:*) proto=ip6 ;;
    *[!0-9.]*) proto=dns4 ;;
    *) proto=ip4 ;;
  esac
  dsn=${SHARETOKEN_DB_DSN:-mysql://sharetoken@mysql:3306/sharetoken}

  if [ ! -f "$PORTAL_YAML" ]; then
    cat > "$PORTAL_YAML" <<YAML
# Written by \`st-entrypoint init\` on first start; edit it and restart the portal.
# Every key: configs/portal.example.yaml, docs/configuration.md.
version: 1
database:
  dsn: $dsn
  password_env: SHARETOKEN_DB_PASSWORD
keys:
  pass_key_file: portal-pass.key
http:
  listen: ":8092"
  public_url: "${PORTAL_PUBLIC_URL:-}"
tls:
  mode: ${PORTAL_TLS_MODE:-auto}
  self_signed_dir: portal-tls
  self_signed_hosts: ["$host"]
accounts:
  open_registration: ${OPEN_REGISTRATION:-true}
  signup_credit_micros: ${SIGNUP_CREDIT_MICROS:-0}
YAML
    echo "init: wrote $PORTAL_YAML"
  fi

  if [ ! -f "$COORD_YAML" ]; then
    cat > "$COORD_YAML" <<YAML
# Written by \`st-entrypoint init\` on first start; edit it and restart the coordinator.
# Every key: configs/coordinator.example.yaml, docs/configuration.md.
version: 1
identity:
  key_file: coordinator.key
database:
  dsn: $dsn
  password_env: SHARETOKEN_DB_PASSWORD
market:
  fee_bps: ${FEE_BPS:-500}
# A reference copy of a model turns on audits and exact token counts in disputes:
# references:
#   - id: qwen2.5-7b
#     upstream: qwen2.5:7b
#     url: http://host.docker.internal:11434/v1
YAML
    echo "init: wrote $COORD_YAML"
  fi

  # The two public values a relay needs: whose passes it accepts, and which
  # peer may reserve without one. Both keys are created here if missing.
  pass_key=$(portal -config "$PORTAL_YAML" pass-key)
  coord_id=$(coordinator -config "$COORD_YAML" id)

  if [ ! -f "$RELAY_YAML" ]; then
    cat > "$RELAY_YAML" <<YAML
# Written by \`st-entrypoint init\` on first start; edit it and restart the relay.
# Every key: configs/relay.example.yaml, docs/configuration.md.
version: 1
identity:
  key_file: relay.key
network:
  # The port is the same inside the container and on the host: the coordinator
  # shares this container's network and dials the announced address.
  listen:
    - /ip4/0.0.0.0/tcp/$port
    - /ip4/0.0.0.0/udp/$port/quic-v1
    - /ip6/::/tcp/$port
    - /ip6/::/udp/$port/quic-v1
  announce:
    - /$proto/$host/tcp/$port
    - /$proto/$host/udp/$port/quic-v1
access:
  networks:
    - name: main
      portal_keys: [$pass_key]
      allow_peers: [$coord_id]
YAML
    echo "init: wrote $RELAY_YAML"
  fi

  relay_id=$(relay -config "$RELAY_YAML" -print-id)
  addr="/$proto/$host/tcp/$port/p2p/$relay_id,/$proto/$host/udp/$port/quic-v1/p2p/$relay_id"
  if portal -config "$PORTAL_YAML" relay ls | grep -q "$relay_id"; then
    echo "init: relay $relay_id is registered"
  else
    portal -config "$PORTAL_YAML" relay add "$addr" "${RELAY_NAME:-relay-1}" "${RELAY_REGION:-}" > /dev/null
    echo "init: registered relay $addr"
  fi

  if [ -n "${ADMIN_PASSWORD:-}" ]; then
    admin=${ADMIN_USER:-admin}
    if portal -config "$PORTAL_YAML" user "$admin" > /dev/null 2>&1; then
      echo "init: admin user $admin exists"
    else
      SHARETOKEN_PASSWORD=$ADMIN_PASSWORD portal -config "$PORTAL_YAML" useradd -admin "$admin" > /dev/null
      echo "init: created admin user $admin"
    fi
  fi
  echo "init: portal pass key $pass_key"
  echo "init: coordinator    $coord_id"
  echo "init: relay          $relay_id"
}

case ${1:-portal} in
  init) init ;;
  portal) exec portal -config "$PORTAL_YAML" serve ;;
  coordinator) exec coordinator -config "$COORD_YAML" serve ;;
  relay) exec relay -config "$RELAY_YAML" ;;
  *) exec "$@" ;;
esac
