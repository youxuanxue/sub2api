#!/usr/bin/env bash
# install-dataplane-client.sh — after the data-plane split, give host-side probes
# a psql / redis-cli that already point at the data machine.
#
# Design: docs/approved/design-data-plane-split.md. After the cutover the app host
# runs no Postgres or Redis container, so every `docker exec "$TK_PG" psql ...`
# probe would fail to resolve. This script starts two idle client containers:
#
#   tokenkey-postgres-client  PG image; PGHOST/PGPORT/PGUSER/PGPASSWORD/PGDATABASE
#                             (and POSTGRES_PASSWORD) taken from the live app color
#   tokenkey-redis-client     Redis image; a redis-cli wrapper that defaults -h/-p to
#                             the data machine and reads the password from a file
#
# ops/lib/resolve-app-container.sh resolves SQL / redis-cli callers over server and
# client names with the single-running rule, so probes need no change. Run this
# only AFTER the old local Postgres and Redis are stopped: while they still run,
# client + server is ambiguous and every probe fails closed.
#
# Default is a read-only plan. Writing needs both:
#   MODE=apply  TK_DATAPLANE_CLIENT_CONFIRM=install-dataplane-client-<DATABASE_HOST>
#
# Usage (via run-probe, which ships ops/lib next to the script):
#   bash ops/observability/run-probe.sh --target prod \
#     --script ops/stage0/install-dataplane-client.sh
#   ... --env MODE=apply --env TK_DATAPLANE_CLIENT_CONFIRM=install-dataplane-client-10.4.0.3
#
# Exit: 0 plan ok / installed and verified; 2 precondition failed; 64 refused.
set -euo pipefail

for _tk_lib in "${TK_LIB_DIR:-$(dirname "${BASH_SOURCE[0]}")}" "$(dirname "${BASH_SOURCE[0]}")/../lib"; do
  [ -f "$_tk_lib/resolve-app-container.sh" ] && break
done
# shellcheck source=../lib/resolve-app-container.sh
. "$_tk_lib/resolve-app-container.sh" || { echo "container resolver not found (ops/lib/resolve-app-container.sh)" >&2; exit 1; }

MODE="${MODE:-plan}"
PG_CLIENT=tokenkey-postgres-client
REDIS_CLIENT=tokenkey-redis-client
# Next to the host .env: on prod /var/lib/tokenkey is the encrypted data mount,
# while /etc sits on the plain system disk.
SECRET_DIR="${TK_DATAPLANE_CLIENT_DIR:-/var/lib/tokenkey/dataplane-client}"

die() { echo "FAIL $*" >&2; exit 2; }

app="$(tk_resolve_app_container auto)" || die "app container unresolved"
# Read the env once. awk must not `exit` early: under pipefail the writer
# would die of SIGPIPE (rc 141) and set -e would abort the whole script.
app_envs="$(docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$app")" \
  || die "cannot inspect $app"
app_env() {
  printf '%s\n' "$app_envs" | awk -v k="$1" 'index($0, k "=") == 1 && !seen { print substr($0, length(k) + 2); seen = 1 }'
}
db_host="$(app_env DATABASE_HOST)"; db_port="$(app_env DATABASE_PORT)"
db_user="$(app_env DATABASE_USER)"; db_name="$(app_env DATABASE_DBNAME)"
db_pass="$(app_env DATABASE_PASSWORD)"
redis_host="$(app_env REDIS_HOST)"; redis_port="$(app_env REDIS_PORT)"
redis_pass="$(app_env REDIS_PASSWORD)"
db_port="${db_port:-5432}"; redis_port="${redis_port:-6379}"
db_user="${db_user:-tokenkey}"; db_name="${db_name:-tokenkey}"

# A host that names a local container (server names from the owner, plus the
# compose service alias) means the cutover has not happened yet.
is_local_host() {
  local host="$1" alias="$2" name
  shift 2
  [ -z "$host" ] || [ "$host" = "$alias" ] && return 0
  for name in "$@"; do [ "$host" = "$name" ] && return 0; done
  return 1
}
# shellcheck disable=SC2086  # owner candidate lists are fixed space-separated literals.
if is_local_host "$db_host" postgres $TK_POSTGRES_CONTAINER_CANDIDATES; then
  die "app $app still uses local Postgres (DATABASE_HOST=${db_host:-<empty>}); install only after the cutover"
fi
# shellcheck disable=SC2086
if is_local_host "$redis_host" redis $TK_REDIS_CONTAINER_CANDIDATES; then
  die "app $app still uses local Redis (REDIS_HOST=${redis_host:-<empty>}); install only after the cutover"
fi
[ -n "$db_pass" ] || die "app $app has no DATABASE_PASSWORD"

if pg_server="$(tk_resolve_postgres_server_container auto)"; then
  die "local Postgres server $pg_server is still running; stop it first"
fi
if redis_server="$(tk_resolve_redis_server_container auto)"; then
  die "local Redis server $redis_server is still running; stop it first"
fi
# The stopped server container (any of the owner's server names) supplies the
# client image. Existence, not running-ness, is the point here: it is stopped.
image_of_first() {
  local name
  for name in "$@"; do
    docker inspect --format '{{.Config.Image}}' "$name" 2>/dev/null && return 0
  done
  return 1
}
# Reuse the stopped server's image: same major version, already on the host, no pull.
# shellcheck disable=SC2086  # owner candidate lists are fixed space-separated literals.
pg_image="${PG_CLIENT_IMAGE:-$(image_of_first $TK_POSTGRES_CONTAINER_CANDIDATES || true)}"
# shellcheck disable=SC2086
redis_image="${REDIS_CLIENT_IMAGE:-$(image_of_first $TK_REDIS_CONTAINER_CANDIDATES || true)}"
[ -n "$pg_image" ] || die "no Postgres image found; set PG_CLIENT_IMAGE"
[ -n "$redis_image" ] || die "no Redis image found; set REDIS_CLIENT_IMAGE"
timeout 3 bash -c "</dev/tcp/$db_host/$db_port" 2>/dev/null || die "cannot reach $db_host:$db_port"
timeout 3 bash -c "</dev/tcp/$redis_host/$redis_port" 2>/dev/null || die "cannot reach $redis_host:$redis_port"

echo "== plan"
echo "  app=$app"
echo "  $PG_CLIENT image=$pg_image -> $db_user@$db_host:$db_port/$db_name"
echo "  $REDIS_CLIENT image=$redis_image -> $redis_host:$redis_port auth=$([ -n "$redis_pass" ] && echo yes || echo no)"
for name in "$PG_CLIENT" "$REDIS_CLIENT"; do
  docker inspect "$name" >/dev/null 2>&1 && echo "  replace existing $name"
done

want="install-dataplane-client-${db_host}"
if [ "$MODE" != apply ]; then
  echo "RESULT plan ok; to install: MODE=apply TK_DATAPLANE_CLIENT_CONFIRM=$want"
  exit 0
fi
[ "${TK_DATAPLANE_CLIENT_CONFIRM:-}" = "$want" ] || { echo "REFUSED: TK_DATAPLANE_CLIENT_CONFIRM must be $want" >&2; exit 64; }

umask 077
install -d -m 0700 "$SECRET_DIR"
printf 'PGHOST=%s\nPGPORT=%s\nPGUSER=%s\nPGDATABASE=%s\nPGPASSWORD=%s\nPOSTGRES_PASSWORD=%s\nPGSSLMODE=disable\nPGCONNECT_TIMEOUT=5\nTZ=UTC\n' \
  "$db_host" "$db_port" "$db_user" "$db_name" "$db_pass" "$db_pass" > "$SECRET_DIR/postgres.env"
printf '%s' "$redis_pass" > "$SECRET_DIR/redis-password"
# Probes call `redis-cli ARGS`. The official image puts /usr/local/sbin ahead of
# /usr/local/bin on PATH, so a wrapper mounted at /usr/local/sbin/redis-cli
# shadows the real binary for `docker exec ... redis-cli` and `sh -c "redis-cli"`
# alike. It adds the data machine endpoint and password unless the caller passed
# -h. `env -u REDISCLI_AUTH redis-cli` callers still work: the password comes
# from the mounted file, not the inherited env.
cat > "$SECRET_DIR/redis-cli" <<'SH'
#!/bin/sh
for a in "$@"; do [ "$a" = -h ] && exec /usr/local/bin/redis-cli "$@"; done
pass="$(cat /run/tokenkey/redis-password 2>/dev/null)"
if [ -n "$pass" ]; then REDISCLI_AUTH="$pass"; export REDISCLI_AUTH; fi
exec /usr/local/bin/redis-cli --no-auth-warning -h "$TK_REDIS_HOST" -p "$TK_REDIS_PORT" "$@"
SH
# docker exec runs as root in both images, so the 0600 secret files stay readable.
chmod 0755 "$SECRET_DIR/redis-cli"

docker rm -f "$PG_CLIENT" "$REDIS_CLIENT" >/dev/null 2>&1 || true
docker run -d --name "$PG_CLIENT" --restart unless-stopped --memory 256m --cpus 0.5 \
  --label tokenkey.role=dataplane-client --env-file "$SECRET_DIR/postgres.env" \
  --entrypoint sleep "$pg_image" infinity >/dev/null
docker run -d --name "$REDIS_CLIENT" --restart unless-stopped --memory 128m --cpus 0.25 \
  --label tokenkey.role=dataplane-client \
  -e TK_REDIS_HOST="$redis_host" -e TK_REDIS_PORT="$redis_port" \
  -v "$SECRET_DIR/redis-password:/run/tokenkey/redis-password:ro" \
  -v "$SECRET_DIR/redis-cli:/usr/local/sbin/redis-cli:ro" \
  --entrypoint sleep "$redis_image" infinity >/dev/null

echo "== verify"
TK_PG="$(tk_require_postgres_container auto)" || die "resolver did not pick $PG_CLIENT"
TK_REDIS="$(tk_require_redis_container auto)" || die "resolver did not pick $REDIS_CLIENT"
[ "$TK_PG" = "$PG_CLIENT" ] || die "resolver picked $TK_PG"
[ "$TK_REDIS" = "$REDIS_CLIENT" ] || die "resolver picked $TK_REDIS"
pg_recovery="$(docker exec "$TK_PG" psql -U "$db_user" -d "$db_name" -XAtc 'select pg_is_in_recovery()')"
[ "$pg_recovery" = f ] || die "data machine Postgres is in recovery (${pg_recovery}); promote it first"
echo "pg ok primary=$db_host:$db_port"
# Assigned first so a failing exec aborts under set -e; inside echo it would not.
# PONG also proves the wrapper is first on PATH (the bare binary would dial 127.0.0.1).
redis_ping="$(docker exec "$TK_REDIS" redis-cli PING | tr -d '\r')"
[ "$redis_ping" = PONG ] || die "redis-cli via $TK_REDIS answered '${redis_ping}', want PONG"
redis_role="$(docker exec "$TK_REDIS" redis-cli INFO replication | tr -d '\r' | awk -F: '/^role/{print $2}')"
[ "$redis_role" = master ] || die "data machine Redis role is '${redis_role}', want master"
echo "redis ping=$redis_ping role=$redis_role"
echo "RESULT installed"
