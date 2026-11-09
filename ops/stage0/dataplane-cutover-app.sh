#!/usr/bin/env bash
# dataplane-cutover-app.sh — the stop-write window of the data-plane cutover,
# run ON the app host (prod or cn-prod) through run-probe.
#
# Design: docs/approved/design-data-plane-split.md §5 停写窗口. The live color is
# recreated in place with the data-machine addresses (user decision 2026-11-09),
# so the Caddyfile, active-color and the Gemini control alias never change.
# While the color is down, Caddy holds new requests for up to lb_try_duration.
#
# PHASE=check (default) is read-only and safe at any time. It prints every
# precondition and the exact plan. PHASE=apply runs the window and needs
# TK_DATAPLANE_CONFIRM=cutover-<SITE>-to-<DATA_IP>.
#
# Window order (apply):
#   1  stop the other writers on this host (backup / archive-check / identity / QA / https-watch timers)
#   2  drain the live color: SIGUSR1, at most DRAIN_SECONDS (10, user decision), then stop
#   3  prove no client is left on the old Postgres and the old Redis
#   4  pg_switch_wal on the old primary, wait until that segment is archived
#   5  wait until the data-machine replicas have caught up (WAL LSN, Redis offset)
#   6  Redis REPLICAOF NO ONE, then pg_promote        <- point of no return
#   7  stop the old Postgres / Redis, docker update --restart=no (no double write)
#   8  point the live color at the data machine, recreate it, wait for /health
#   9  restart QA timers; retire this host's backup timers and receipts
# Any failure before step 6 restarts the old color and the stopped timers,
# leaving the site exactly as it was. After step 6 the script never goes back.
#
# Exit: 0 ok; 2 precondition failed (nothing changed); 3 aborted before step 6
# (site restored); 4 failed after step 6 (needs an operator); 64 refused.
set -euo pipefail

for _tk_lib in "${TK_LIB_DIR:-$(dirname "${BASH_SOURCE[0]}")}" "$(dirname "${BASH_SOURCE[0]}")/../lib"; do
  [ -f "$_tk_lib/resolve-app-container.sh" ] && break
done
# shellcheck source=../lib/resolve-app-container.sh
. "$_tk_lib/resolve-app-container.sh" || { echo "container resolver not found (ops/lib/resolve-app-container.sh)" >&2; exit 1; }

PHASE="${PHASE:-check}"
SITE="${SITE:?SITE must be prod or cn-prod}"
DATA="${DATA_IP:?DATA_IP (data machine private IP) required}"
DRAIN_SECONDS="${DRAIN_SECONDS:-10}"
# From stopping the live color to promotion, new requests wait in Caddy for at
# most lb_try_duration (30s). Abort and restore before that runs out, so a slow
# archive or replay costs latency instead of 502s.
PRE_PROMOTE_BUDGET_S="${PRE_PROMOTE_BUDGET_S:-20}"
case "$SITE" in
  prod) ROOT=/var/lib/tokenkey ENV_FILE=/var/lib/tokenkey/.env
        TIMER_RE='^tokenkey-(postgres-backup|postgres-archive-check|backup-identity|qa-provision|qa-cleanup|qa-archive|https-watch)\.timer$' ;;
  cn-prod) ROOT=/opt/tokenkey ENV_FILE=/opt/tokenkey/.env
        TIMER_RE='^tokenkey-(cn-postgres-backup|cn-postgres-archive-check|cn-backup-identity|qa-provision|qa-cleanup|qa-archive|https-watch)\.timer$' ;;
  *) echo "FAIL SITE must be prod or cn-prod" >&2; exit 2 ;;
esac
case "$DATA" in 10.*) ;; *) echo "FAIL DATA_IP must be a private 10.x address" >&2; exit 2 ;; esac
# prod moves to a new Redis password (design §5 step 2); cn-prod keeps its own.
NEW_REDIS_PASSWORD_FILE="${NEW_REDIS_PASSWORD_FILE:-$ROOT/dataplane-redis-password}"
BACKUP_STATE=/var/lib/tokenkey/postgres-backup

T0="$(date +%s%N)"
ms() { echo $(( ($(date +%s%N) - T0) / 1000000 )); }
DOWN_AT=""
# within_budget: true while the color has been down for less than the budget.
within_budget() { [ $(( ($(date +%s%N) - DOWN_AT) / 1000000000 )) -lt "$PRE_PROMOTE_BUDGET_S" ]; }
log() { echo "[$(ms)ms] $*"; }
die() { echo "FAIL $*" >&2; exit "${2:-2}"; }

app="$(tk_resolve_app_container auto)" || die "app container unresolved"
old_pg="$(tk_resolve_postgres_server_container auto)" || die "local Postgres server unresolved"
old_redis="$(tk_resolve_redis_server_container auto)" || die "local Redis server unresolved"
app_envs="$(docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$app")" || die "cannot inspect $app"
app_env() { printf '%s\n' "$app_envs" | awk -v k="$1" 'index($0, k "=") == 1 && !seen { print substr($0, length(k) + 2); seen = 1 }'; }
db_user="$(app_env DATABASE_USER)"; db_user="${db_user:-tokenkey}"
db_name="$(app_env DATABASE_DBNAME)"; db_name="${db_name:-tokenkey}"
db_pass="$(app_env DATABASE_PASSWORD)"
[ -n "$db_pass" ] || die "$app has no DATABASE_PASSWORD"
# Checked (not fatal) here so PHASE=check still reports every other item.
new_redis_pass_problem=""
if [ "$SITE" = prod ]; then
  if [ ! -s "$NEW_REDIS_PASSWORD_FILE" ]; then
    new_redis_pass_problem="missing $NEW_REDIS_PASSWORD_FILE (new prod Redis password from the custody bucket, mode 0600)"
  elif [ "$(stat -c %a "$NEW_REDIS_PASSWORD_FILE")" != 600 ]; then
    new_redis_pass_problem="$NEW_REDIS_PASSWORD_FILE must be mode 0600"
  fi
  new_redis_pass="$(cat "$NEW_REDIS_PASSWORD_FILE" 2>/dev/null || true)"
else
  new_redis_pass="$(app_env REDIS_PASSWORD)"
fi

# Old servers: local socket/loopback. Data machine: the old server containers'
# own clients over the private network (firewall admits only this host).
old_q() { docker exec --user postgres "$old_pg" psql -U tokenkey -d tokenkey -XAt -v ON_ERROR_STOP=1 -c "$1"; }
old_rc() { docker exec "$old_redis" sh -c 'exec redis-cli ${REDISCLI_AUTH:+--no-auth-warning} "$@"' redis-cli "$@" | tr -d '\r'; }
data_q() { docker exec -e PGPASSWORD="$db_pass" -e PGCONNECT_TIMEOUT=5 "$old_pg" psql -h "$DATA" -U "$db_user" -d "$db_name" -XAt -v ON_ERROR_STOP=1 -c "$1"; }
data_rc() { docker exec -e REDISCLI_AUTH="$new_redis_pass" "$old_redis" redis-cli --no-auth-warning -h "$DATA" -p 6379 "$@" | tr -d '\r'; }
field() { awk -F: -v k="$1" '$1 == k { print $2 }'; }

writer_timers() {
  systemctl list-timers --all --no-pager --plain 2>/dev/null | grep -o -E 'tokenkey-[a-z0-9-]+\.timer' | sort -u | grep -E "$TIMER_RE" || true
}

# --- live color recreate, per site -------------------------------------------
prod_compose_ok() {
  grep -q 'DATABASE_HOST=${DATABASE_HOST:-' "$ROOT/docker-compose.bluegreen.yml" \
    && grep -q 'REDIS_HOST=${REDIS_HOST:-' "$ROOT/docker-compose.bluegreen.yml"
}
cn_compose_file() { docker inspect -f '{{index .Config.Labels "com.docker.compose.project.config_files"}}' "$app"; }
cn_override_ok() {
  grep -q 'DATABASE_HOST: "${DATABASE_HOST:-postgres}"' "$ROOT/compose.override.yml" \
    && grep -q "'DATABASE_HOST'," "$ROOT/releases/artifacts-ssm/bluegreen.py"
}

env_set() { # KEY VALUE: replace or append in ENV_FILE, keeping its mode
  local key="$1" value="$2" tmp
  tmp="$(mktemp "$ENV_FILE.XXXXXX")"
  awk -v k="$key" -v v="$value" 'BEGIN{d=0} index($0, k "=") == 1 { if (!d) print k "=" v; d=1; next } { print } END { if (!d) print k "=" v }' "$ENV_FILE" > "$tmp"
  chmod --reference="$ENV_FILE" "$tmp"; chown --reference="$ENV_FILE" "$tmp"
  mv "$tmp" "$ENV_FILE"
}

point_env_at_data() {
  cp -p "$ENV_FILE" "$ENV_FILE.pre-dataplane-$(date -u +%Y%m%dT%H%M%SZ)"
  env_set DATABASE_HOST "$DATA"; env_set DATABASE_PORT 5432
  env_set REDIS_HOST "$DATA"; env_set REDIS_PORT 6379
  [ "$SITE" = prod ] && env_set REDIS_PASSWORD "$new_redis_pass"
  return 0
}

recreate_live_color() {
  if [ "$SITE" = prod ]; then
    docker compose --project-name tokenkey --env-file "$ENV_FILE" -f "$ROOT/docker-compose.bluegreen.yml" \
      up -d --no-deps --force-recreate --pull never "$app" >/dev/null
  else
    # cn-prod colors run from a per-release compose.json with a literal env.
    # Copy it into its own state dir with the data-machine endpoints; the
    # release receipt stays untouched. Future releases inherit from the live
    # container and overlay compose.override.yml, which now reads the same .env.
    local src dst
    src="$(cn_compose_file)"; dst="$ROOT/releases/dataplane-$(date -u +%Y%m%dT%H%M%SZ)"
    install -d -m 0700 "$dst"
    python3 - "$src" "$dst/compose.json" "$app" "$DATA" <<'PY'
import json, sys
src, dst, svc, data = sys.argv[1:]
c = json.load(open(src))
env = c["services"][svc]["environment"]
env.update(DATABASE_HOST=data, DATABASE_PORT="5432", REDIS_HOST=data, REDIS_PORT="6379")
with open(dst, "w") as f:
    json.dump(c, f)
PY
    chmod 0600 "$dst/compose.json"
    docker compose -p cn-prod-bluegreen -f "$dst/compose.json" up -d --no-deps --force-recreate --pull never "$app" >/dev/null
  fi
}

wait_live() {
  local i
  for i in $(seq 1 90); do
    docker exec "$app" wget -q -T 2 -O /dev/null http://localhost:8080/health 2>/dev/null && return 0
    [ "$(docker inspect -f '{{.State.Status}}' "$app" 2>/dev/null)" = exited ] && return 1
    sleep 1
  done
  return 1
}

# --- check ---------------------------------------------------------------------
phase_check() {
  local fail=0 caddy recovery lag
  ok() { echo "  ok   $1"; }
  bad() { echo "  FAIL $1"; fail=1; }
  echo "== check site=$SITE app=$app old_pg=$old_pg old_redis=$old_redis data=$DATA"
  [ -z "$new_redis_pass_problem" ] && ok "target Redis password present" || bad "$new_redis_pass_problem"
  [ "$(docker inspect -f '{{.State.Health.Status}}' "$app")" = healthy ] && ok "app healthy" || bad "app not healthy"
  [ "$(old_q 'select pg_is_in_recovery()')" = f ] && ok "old postgres is primary" || bad "old postgres is not primary"
  [ "$(old_q "select coalesce(last_failed_time < last_archived_time, true) from pg_stat_archiver")" = t ] \
    && ok "old archiver healthy" || bad "old archiver failing"
  recovery="$(data_q 'select pg_is_in_recovery()' 2>/dev/null || echo unreachable)"
  [ "$recovery" = t ] && ok "data postgres is a standby" || bad "data postgres in_recovery=$recovery"
  lag="$(data_q "select coalesce(extract(epoch from now() - pg_last_xact_replay_timestamp())::int, -1)" 2>/dev/null || echo -1)"
  [ "$lag" -ge 0 ] && [ "$lag" -le "${MAX_REPLAY_LAG_S:-900}" ] && ok "data replay lag ${lag}s" || bad "data replay lag ${lag}s"
  # Without the target password the AUTH result says nothing about the replica;
  # the missing file is already reported above.
  if [ -z "$new_redis_pass_problem" ]; then
    [ "$(data_rc PING 2>/dev/null)" = PONG ] && ok "data redis AUTH with the target password" || bad "data redis AUTH failed"
    [ "$(data_rc INFO replication 2>/dev/null | field master_link_status)" = up ] && ok "data redis replica link up" || bad "data redis replica link not up"
  else
    echo "  skip data redis checks (no target password yet)"
  fi
  caddy="$(docker exec tokenkey-caddy cat /etc/caddy/Caddyfile 2>/dev/null)"
  printf '%s' "$caddy" | grep -q "reverse_proxy $app:8080" && ok "caddy routes $app" || bad "caddy does not route $app"
  printf '%s' "$caddy" | grep -q 'lb_try_duration 30s' && ok "caddy lb_try_duration 30s" || bad "caddy has no lb_try_duration 30s"
  if [ "$SITE" = prod ]; then
    prod_compose_ok && ok "bluegreen compose reads DATABASE_HOST/REDIS_HOST from .env" \
      || bad "bluegreen compose hardcodes the local hosts; deploy a release that contains #3114 first"
  else
    cn_override_ok && ok "compose.override and bluegreen.py carry the data-plane keys" || bad "cn-prod controls predate #3114; run sync-controls"
    [ -f "$(cn_compose_file)" ] && ok "live compose $(cn_compose_file)" || bad "live compose file missing"
  fi
  local busy="" t
  for t in $(writer_timers); do
    systemctl is-active --quiet "${t%.timer}.service" && busy="$busy ${t%.timer}"
  done
  [ -z "$busy" ] && ok "no writer job running" || bad "writer jobs running:$busy"
  echo "  writers to pause: $(writer_timers | tr '\n' ' ')"
  echo "  old pg clients now: $(old_q "select count(*) from pg_stat_activity where backend_type = 'client backend' and pid <> pg_backend_pid()")"
  [ "$fail" = 0 ] || die "check failed; nothing changed"
  echo "RESULT check ok; to run the window: PHASE=apply TK_DATAPLANE_CONFIRM=cutover-$SITE-to-$DATA"
}

# --- apply ---------------------------------------------------------------------
paused=""
abort() { # before the point of no return: put the site back
  local restored=ok
  echo "ABORT $*; restoring $app and writer timers" >&2
  docker start "$app" >/dev/null || restored="docker start $app failed"
  wait_live || restored="$app did not come back healthy"
  # shellcheck disable=SC2086
  if [ -n "$paused" ] && ! systemctl start $paused; then restored="$restored; timers not restarted:$paused"; fi
  [ "$restored" = ok ] || { echo "ABORT RESTORE INCOMPLETE: $restored; fix by hand now" >&2; exit 4; }
  echo "ABORT site restored" >&2
  exit 3
}

phase_apply() {
  local want="cutover-${SITE}-to-${DATA}" t i n lsn seg redis_offset redis_master_host redis_master_port
  [ "${TK_DATAPLANE_CONFIRM:-}" = "$want" ] || { echo "REFUSED: TK_DATAPLANE_CONFIRM must be $want" >&2; exit 64; }
  phase_check
  T0="$(date +%s%N)"

  log "1 pause writers"
  for t in $(writer_timers); do
    systemctl is-active --quiet "$t" && { systemctl stop "$t"; paused="$paused $t"; }
  done
  for t in $paused; do systemctl is-active --quiet "${t%.timer}.service" && abort "${t%.timer} started meanwhile"; done

  log "2 drain $app (max ${DRAIN_SECONDS}s)"
  docker kill -s USR1 "$app" >/dev/null || abort "cannot signal $app"
  for i in $(seq 1 "$DRAIN_SECONDS"); do
    n="$(docker exec "$app" wget -q -T 1 -O - http://localhost:8080/health/inflight 2>/dev/null | sed -n 's/.*"in_flight":\([0-9]*\).*/\1/p')"
    [ "$n" = 0 ] && break
    sleep 1
  done
  # The drain budget is spent; anything still in flight is cut (user decision
  # 2026-11-09). Report which, so the window record shows how many were cut.
  if [ "$n" = 0 ]; then log "  drained cleanly"; else log "  cutting in_flight=${n:-unknown}"; fi
  docker stop -t 1 "$app" >/dev/null || abort "cannot stop $app"
  DOWN_AT="$(date +%s%N)"

  log "3 no clients left"
  n="$(old_q "select count(*) from pg_stat_activity where backend_type = 'client backend' and pid <> pg_backend_pid()")"
  [ "$n" = 0 ] || abort "old postgres still has $n client(s): $(old_q "select string_agg(distinct coalesce(client_addr::text, 'local') || '/' || application_name, ',') from pg_stat_activity where backend_type = 'client backend' and pid <> pg_backend_pid()")"
  # Left on the old Redis: our own CLIENT LIST and the replica link (flags=S).
  n="$(old_rc CLIENT LIST | grep -v -E 'flags=S|cmd=client' | grep -c . || true)"
  [ "$n" = 0 ] || abort "old redis still has $n client(s)"

  log "4 switch and archive the last WAL"
  lsn="$(old_q 'select pg_switch_wal()')"
  seg="$(old_q "select pg_walfile_name('$lsn')")"
  while within_budget; do
    [ "$(old_q "select coalesce(last_archived_wal >= '$seg', false) from pg_stat_archiver")" = t ] && break
    sleep 0.25
  done
  [ "$(old_q "select coalesce(last_archived_wal >= '$seg', false) from pg_stat_archiver")" = t ] || abort "segment $seg not archived within ${PRE_PROMOTE_BUDGET_S}s"

  log "5 wait for the replicas (lsn=$lsn)"
  while within_budget; do
    [ "$(data_q "select pg_last_wal_replay_lsn() >= '$lsn'::pg_lsn")" = t ] && break
    sleep 0.25
  done
  [ "$(data_q "select pg_last_wal_replay_lsn() >= '$lsn'::pg_lsn")" = t ] || abort "data standby did not replay through $lsn within ${PRE_PROMOTE_BUDGET_S}s"
  redis_offset="$(old_rc INFO replication | field master_repl_offset)"
  while within_budget; do
    [ "$(data_rc INFO replication | field slave_repl_offset)" -ge "$redis_offset" ] && break
    sleep 0.25
  done
  [ "$(data_rc INFO replication | field slave_repl_offset)" -ge "$redis_offset" ] || abort "data redis behind offset $redis_offset"

  log "6 promote (point of no return)"
  # Remember where the replica pulls from, so a failed promote can re-attach it.
  redis_master_host="$(data_rc INFO replication | field master_host)"
  redis_master_port="$(data_rc INFO replication | field master_port)"
  [ -n "$redis_master_host" ] && [ -n "$redis_master_port" ] || abort "data redis master address unknown"
  [ "$(data_rc REPLICAOF NO ONE)" = OK ] || abort "REPLICAOF NO ONE refused"
  if [ "$(data_q 'select pg_promote(wait => true, wait_seconds => 60)')" != t ] \
    || [ "$(data_q 'select pg_is_in_recovery()')" != f ]; then
    # Redis already left replication; Postgres did not promote. Re-attach Redis
    # and restore the old site: nothing has written to the data machine yet.
    [ "$(data_rc REPLICAOF "$redis_master_host" "$redis_master_port")" = OK ] \
      || echo "WARN data redis not re-attached to $redis_master_host:$redis_master_port; re-run the replica setup before retrying" >&2
    abort "pg_promote failed"
  fi
  log "  promoted; timeline $(data_q 'select timeline_id from pg_control_checkpoint()')"

  log "7 retire the old servers"
  docker stop -t 30 "$old_pg" "$old_redis" >/dev/null || die "cannot stop old servers; stop them by hand NOW (double-write risk)" 4
  docker update --restart=no "$old_pg" "$old_redis" >/dev/null || die "cannot set --restart=no on old servers" 4

  log "8 recreate $app against $DATA"
  point_env_at_data || die "cannot update $ENV_FILE" 4
  recreate_live_color || die "recreate $app failed" 4
  wait_live || die "$app did not become ready" 4
  app_envs="$(docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$app")"
  [ "$(app_env DATABASE_HOST)" = "$DATA" ] && [ "$(app_env REDIS_HOST)" = "$DATA" ] || die "$app env does not point at $DATA" 4
  log "  $app ready on $DATA"

  log "9 timers"
  local timer_problems=""
  for t in $paused; do
    case "$t" in
      tokenkey-qa-* | tokenkey-https-watch.timer) systemctl start "$t" || timer_problems="$timer_problems start:$t" ;;
      *) systemctl disable --now "$t" >/dev/null || timer_problems="$timer_problems disable:$t" ;;
    esac
  done
  # The app-host watcher keys backup monitoring on these receipts; the data
  # machine owns them now (#3117). Kept, renamed, for the rollback record.
  for f in archive_check.json enabled.json backup.json; do
    [ -f "$BACKUP_STATE/$f" ] && mv "$BACKUP_STATE/$f" "$BACKUP_STATE/$f.pre-dataplane"
  done
  # The data plane is already live; a timer problem does not undo the cutover,
  # but it must not pass silently either.
  [ -z "$timer_problems" ] || die "cutover done, but timers need attention:$timer_problems" 4
  log "RESULT cutover done"
  echo "next: on the data machine PHASE=finish ops/stage0/dataplane-cutover-data.sh; then install-dataplane-client.sh here; then the ${SITE}-data watcher and backup timers on the data machine"
}

case "$PHASE" in
  check) phase_check ;;
  apply) phase_apply ;;
  *) die "unknown PHASE=$PHASE (check|apply)" ;;
esac
