#!/usr/bin/env bash
# A MySQL 8 in Docker for tests and local stacks: the code only runs on MySQL.
#   scripts/local-mysql.sh server         # starts it if needed, prints mysql://root@127.0.0.1:33971/
#   scripts/local-mysql.sh new-db <name>  # also (re)creates an empty database, prints its DSN
# The container (st-test-mysql, loopback only, no password, data in tmpfs) stays
# up between runs. Remove it with: docker rm -f st-test-mysql
set -euo pipefail
PORT=${ST_TEST_MYSQL_PORT:-33971}
up() {
  if [ "$(docker inspect -f '{{.State.Running}}' st-test-mysql 2>/dev/null)" != true ]; then
    docker rm -f st-test-mysql >/dev/null 2>&1 || true
    docker run -d --name st-test-mysql -e MYSQL_ALLOW_EMPTY_PASSWORD=yes -p "127.0.0.1:$PORT:3306" \
      --tmpfs /var/lib/mysql mysql:8.0 --skip-name-resolve --max-connections=2000 \
      --character-set-server=utf8mb4 --collation-server=utf8mb4_bin --performance-schema=OFF --mysqlx=OFF >/dev/null
  fi
  # The image's entrypoint first runs a socket-only temporary server: wait for TCP.
  for _ in $(seq 120); do
    docker exec st-test-mysql mysql -h127.0.0.1 -uroot -e 'select 1' >/dev/null 2>&1 && return 0
    perl -e 'select(undef,undef,undef,0.5)'
  done
  echo "MySQL did not start" >&2; exit 1
}
case ${1:-} in
  server) up; echo "mysql://root@127.0.0.1:$PORT/" ;;
  new-db)
    name=${2:?database name}
    up
    docker exec st-test-mysql mysql -h127.0.0.1 -uroot -e "DROP DATABASE IF EXISTS \`$name\`; CREATE DATABASE \`$name\` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"
    echo "mysql://root@127.0.0.1:$PORT/$name" ;;
  *) sed -n 2,5p "$0" >&2; exit 2 ;;
esac
