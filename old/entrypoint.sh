#!/bin/sh
set -eu

mkdir -p /run/nginx
mkdir -p /srv/kanly
chmod 0777 /srv/kanly || true

if [ -S /var/run/docker.sock ]; then
  DOCKER_GID=$(stat -c '%g' /var/run/docker.sock)
  if ! getent group dockerhost >/dev/null 2>&1; then
    groupadd -g "$DOCKER_GID" dockerhost 2>/dev/null || true
  fi
  usermod -aG dockerhost www-data 2>/dev/null || true
fi

php-fpm -D
exec nginx -g 'daemon off;'
