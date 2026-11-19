#!/usr/bin/env sh
# Initial replica bootstrap ONLY; mount this script read-only and /data writable.
# Role lives in /data/tokenkey-redis.conf, never in redis-server command flags.
# Runtime REPLICAOF + CONFIG REWRITE then survives container/host restarts.
set -eu
umask 077
config=/data/tokenkey-redis.conf
[ -s /run/requirepass ] && [ -s /run/masterauth ] || {
  echo 'REFUSED: nonempty Redis credential files required' >&2; exit 64
}
requirepass="$(cat /run/requirepass)"
masterauth="$(cat /run/masterauth)"
[ -n "$requirepass" ] && [ -n "$masterauth" ] || exit 64
for arg in "$@"; do
  case "$arg" in *replicaof*|*slaveof*|--include|--include=*) echo 'REFUSED: replication flags override persistent role' >&2; exit 64 ;; esac
done
if [ ! -f "$config" ]; then
  # Never silently turn an existing data set back into a replica if its config
  # disappears. Bootstrap is allowed only on a new, empty data directory.
  if [ -n "$(ls -A /data)" ]; then
    echo 'REFUSED: existing Redis data without persistent role configuration' >&2; exit 64
  fi
  host="${REDIS_INITIAL_MASTER_HOST:?initial replica master required}"
  port="${REDIS_INITIAL_MASTER_PORT:?initial replica port required}"
  case "$host" in *[!0-9.]*|'') echo 'REFUSED: master must be a private IPv4 literal' >&2; exit 64 ;; esac
  case "$host" in 10.*) ;; *) echo 'REFUSED: initial master must use private 10.x network' >&2; exit 64 ;; esac
  case "$port" in *[!0-9]*|'') exit 64 ;; esac
  [ "$port" -ge 1 ] && [ "$port" -le 65535 ] || exit 64
  printf 'replicaof %s %s\nreplica-read-only yes\n' "$host" "$port" > "$config.tmp"
  mv "$config.tmp" "$config"
fi
# CONFIG REWRITE includes auth values. Keep the file private on the persistent
# data volume; do not print the file or put passwords in docker inspect Env.
chmod 600 "$config"
exec redis-server "$config" "$@" \
  --requirepass "$requirepass" --masterauth "$masterauth"
