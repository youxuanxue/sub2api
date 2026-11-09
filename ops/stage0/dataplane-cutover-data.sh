#!/usr/bin/env bash
# dataplane-cutover-data.sh — data-machine side of the data-plane cutover.
#
# Design: docs/approved/design-data-plane-split.md §每站实施步骤 / 5. 停写窗口.
# Runs ON the data machine (TAT RunCommand / SSM). The app-host side is
# ops/stage0/dataplane-cutover-app.sh; it calls the two write phases below in
# order, inside the approved window.
#
# Phases (PHASE=...):
#   check    read-only (default). Standby replays WAL, Redis replica is linked,
#            settings match the approved parameter table. Safe any time.
#   finish   WRITE, after the window. Reset postgresql.auto.conf leftovers,
#            create pg_stat_statements, force one WAL switch and prove the new
#            primary archives to COS.
#
# Promotion itself is not here. It runs inside the window from the app host
# (ops/stage0/dataplane-cutover-app.sh) over the private network, so the whole
# window is one remote invocation and fits Caddy's lb_try_duration.
#
# finish needs TK_DATAPLANE_CONFIRM=finish-<SITE>-<this host's private IP>.
#
# Exit: 0 ok; 2 precondition failed (nothing changed); 3 failed after a write
# started; 64 refused (confirm missing or wrong).
set -euo pipefail

PHASE="${PHASE:-check}"
SITE="${SITE:?SITE must be prod or cn-prod}"
case "$SITE" in prod | cn-prod) ;; *) echo "FAIL SITE must be prod or cn-prod" >&2; exit 2 ;; esac
PG=tokenkey-postgres
REDIS=tokenkey-redis

die() { echo "FAIL $*" >&2; exit "${2:-2}"; }
# TCP, not the Unix socket: tcp_keepalives_* read back as 0 on a socket session.
q() { docker exec --user postgres "$PG" psql -h 127.0.0.1 -U tokenkey -d tokenkey -XAt -v ON_ERROR_STOP=1 -c "$1"; }
rc() { docker exec "$REDIS" sh -c 'REDISCLI_AUTH="$(cat /run/requirepass)" exec redis-cli --no-auth-warning "$@"' redis-cli "$@" | tr -d '\r'; }
redis_field() { rc INFO replication | awk -F: -v k="$1" '$1 == k { print $2 }'; }

private_ip="$(ip -4 -o addr show scope global | awk '{print $4}' | cut -d/ -f1 | grep -E '^10\.' | head -1)"
[ -n "$private_ip" ] || die "no private 10.x address on this host"

# Approved parameters (design §数据库参数 建议值). Memory values differ per site.
expected_settings() {
  local sb ecs mwm
  if [ "$SITE" = prod ]; then sb=2GB ecs=5GB mwm=256MB; else sb=1GB ecs=2560MB mwm=128MB; fi
  cat <<EOF
shared_buffers=$sb
effective_cache_size=$ecs
maintenance_work_mem=$mwm
work_mem=16MB
max_connections=200
jit=off
wal_compression=lz4
max_wal_size=4GB
checkpoint_timeout=15min
tcp_keepalives_idle=30
tcp_keepalives_interval=10
tcp_keepalives_count=3
idle_in_transaction_session_timeout=10min
shared_preload_libraries=pg_stat_statements
timezone=UTC
archive_mode=on
EOF
}

check_settings() {
  local line name want got bad=0
  while IFS= read -r line; do
    name="${line%%=*}"; want="${line#*=}"
    got="$(q "show $name")"
    if [ "$got" != "$want" ]; then echo "  FAIL setting $name=$got want $want"; bad=1; fi
  done < <(expected_settings)
  return "$bad"
}

phase_check() {
  local fail=0 recovery lag link policy restore
  echo "== check site=$SITE host=$private_ip"
  [ "$(docker inspect -f '{{.State.Running}}' "$PG" 2>/dev/null)" = true ] || die "$PG is not running"
  [ "$(docker inspect -f '{{.State.Running}}' "$REDIS" 2>/dev/null)" = true ] || die "$REDIS is not running"
  recovery="$(q 'select pg_is_in_recovery()')"
  lag="$(q "select coalesce(extract(epoch from now() - pg_last_xact_replay_timestamp())::int, -1)")"
  restore="$(q "show restore_command")"
  echo "  pg in_recovery=$recovery replay_lag_s=$lag replay_lsn=$(q 'select pg_last_wal_replay_lsn()')"
  [ "$recovery" = t ] || { echo "  FAIL postgres is not a standby"; fail=1; }
  case "$restore" in *"--repo=2"*) ;; *) echo "  FAIL restore_command does not read COS repo2"; fail=1 ;; esac
  # archive_timeout is 5 min on the old primary, so a quiet standby trails by up
  # to ~10 min of commit time; anything older means archive-get is stuck.
  if [ "$lag" -lt 0 ] || [ "$lag" -gt "${MAX_REPLAY_LAG_S:-900}" ]; then echo "  FAIL replay lag ${lag}s"; fail=1; fi
  check_settings || fail=1
  link="$(redis_field master_link_status)"; policy="$(rc CONFIG GET maxmemory-policy | tail -1)"
  echo "  redis role=$(redis_field role) link=$link offset=$(redis_field slave_repl_offset) policy=$policy"
  [ "$(redis_field role)" = slave ] || { echo "  FAIL redis is not a replica"; fail=1; }
  [ "$link" = up ] || { echo "  FAIL redis replication link $link"; fail=1; }
  [ "$policy" = noeviction ] || { echo "  FAIL maxmemory-policy $policy"; fail=1; }
  [ "$(rc CONFIG GET appendonly | tail -1)" = yes ] || { echo "  FAIL redis AOF off"; fail=1; }
  [ "$fail" = 0 ] || die "check failed; nothing changed"
  echo "RESULT check ok"
}

require_confirm() {
  local want="$1-${SITE}-${private_ip}"
  [ "${TK_DATAPLANE_CONFIRM:-}" = "$want" ] || { echo "REFUSED: TK_DATAPLANE_CONFIRM must be $want" >&2; exit 64; }
}

phase_finish() {
  local before after i
  require_confirm finish
  [ "$(q 'select pg_is_in_recovery()')" = f ] || die "postgres is still a standby; the window has not promoted it"
  # postgresql.auto.conf carries the old primary's ALTER SYSTEM values and the
  # pgBackRest restore block. Command-line -c already wins; reset leaves one source.
  q "alter system reset all" >/dev/null
  q "select pg_reload_conf()" >/dev/null
  q "create extension if not exists pg_stat_statements" >/dev/null
  before="$(q "select coalesce(archived_count, 0) from pg_stat_archiver")"
  q "select pg_switch_wal()" >/dev/null
  for i in $(seq 1 60); do
    after="$(q "select coalesce(archived_count, 0) from pg_stat_archiver")"
    [ "$after" -gt "$before" ] && break
    sleep 1
  done
  [ "$after" -gt "$before" ] || die "new primary did not archive within 60s (pg_stat_archiver $before -> $after)" 3
  echo "archived_count $before -> $after last=$(q 'select last_archived_wal from pg_stat_archiver')"
  check_settings_primary
  echo "RESULT finished"
}

check_settings_primary() {
  local bad
  bad="$(check_settings || true)"
  [ -z "$bad" ] || { printf '%s\n' "$bad"; die "settings differ from the approved table" 3; }
  [ "$(q "select count(*) from pg_extension where extname = 'pg_stat_statements'")" = 1 ] \
    || die "pg_stat_statements missing" 3
}

case "$PHASE" in
  check) phase_check ;;
  finish) phase_finish ;;
  *) die "unknown PHASE=$PHASE (check|finish)" ;;
esac
