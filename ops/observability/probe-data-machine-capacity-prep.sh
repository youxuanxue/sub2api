#!/usr/bin/env bash
# probe-data-machine-capacity-prep.sh — read-only prep gate for prod data-machine
# 4C16G resize prep (see design-data-plane-split.md + docs/decisions/decided.md).
#
# MUST NOT stop/restart containers, alter PG, resize the host, or run backups.
#
#   bash ops/observability/run-probe.sh --target prod-data \
#     --script ops/observability/probe-data-machine-capacity-prep.sh \
#     --compressed-output
set -u

PG_CONTAINER="${TOKENKEY_PG_CONTAINER:-tokenkey-postgres}"
REDIS_CONTAINER="${TOKENKEY_REDIS_CONTAINER:-tokenkey-redis}"
MAINT_ROOT="${TOKENKEY_CAPACITY_PREP_DIR:-/var/lib/tokenkey/maintenance/prod-data-4c16g}"
DISK_METRICS_BIN="${TOKENKEY_DISK_METRICS_BIN:-/usr/local/bin/tokenkey-disk-metrics.sh}"
RESTORE_STATE_DIR="${TOKENKEY_RESTORE_STATE_DIR:-/var/lib/tokenkey/postgres-backup}"

echo "=== meta ==="
printf '{"hostname":"%s","utc":"%s","pg_container":"%s","maint_root":"%s","note":"read_only_prep_no_stop_write"}\n' \
  "$(hostname)" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "${PG_CONTAINER}" "${MAINT_ROOT}"

echo "=== HOST ==="
printf 'HOST {"nproc":%s,"uname":"%s"}\n' "$(nproc 2>/dev/null || echo 0)" "$(uname -r 2>/dev/null || echo unknown)"
awk 'BEGIN{t=0;a=0}
  /^MemTotal:/{t=$2}
  /^MemAvailable:/{a=$2}
  END{
    used=0; if(t>0) used=int((t-a)*100/t);
    printf "HOST_MEM {\"mem_total_kib\":%d,\"mem_available_kib\":%d,\"used_pct_by_available\":%d}\n", t,a,used
  }' /proc/meminfo
df -P -B1 / 2>/dev/null | awk 'NR==2{
  printf "DF_ROOT {\"total_bytes\":%s,\"used_bytes\":%s,\"avail_bytes\":%s,\"use_pct\":%s}\n", $2,$3,$4,$5
}'

echo "=== DOCKER_PG ==="
if docker inspect "${PG_CONTAINER}" >/dev/null 2>&1; then
  docker inspect "${PG_CONTAINER}" --format \
    'PG_INSPECT {"image":"{{.Config.Image}}","status":"{{.State.Status}}","running":{{.State.Running}},"oom_killed":{{.State.OOMKilled}},"memory_bytes":{{.HostConfig.Memory}},"memory_swap_bytes":{{.HostConfig.MemorySwap}},"shm_size_bytes":{{.HostConfig.ShmSize}},"started_at":"{{.State.StartedAt}}"}'
  docker inspect "${PG_CONTAINER}" --format '{{range .Mounts}}{{println .Source "->" .Destination}}{{end}}' \
    | awk '{printf "PG_MOUNT {\"line\":\"%s\"}\n", $0}'
else
  echo 'PG_INSPECT {"error":"missing"}'
fi

echo "=== DOCKER_REDIS ==="
if docker inspect "${REDIS_CONTAINER}" >/dev/null 2>&1; then
  docker inspect "${REDIS_CONTAINER}" --format \
    'REDIS_INSPECT {"image":"{{.Config.Image}}","status":"{{.State.Status}}","running":{{.State.Running}},"memory_bytes":{{.HostConfig.Memory}},"started_at":"{{.State.StartedAt}}"}'
else
  echo 'REDIS_INSPECT {"error":"missing"}'
fi

echo "=== PG_LIVE ==="
if docker inspect -f '{{.State.Running}}' "${PG_CONTAINER}" 2>/dev/null | grep -qx true; then
  docker exec --user postgres "${PG_CONTAINER}" psql -h 127.0.0.1 -U tokenkey -d tokenkey -XAt -c \
    "select 'PG_IDENTITY '||json_build_object(
       'system_identifier', system_identifier::text,
       'in_recovery', pg_is_in_recovery()
     ) from pg_control_system();" 2>/dev/null || echo 'PG_IDENTITY {"error":"psql_failed"}'
  docker exec --user postgres "${PG_CONTAINER}" psql -h 127.0.0.1 -U tokenkey -d tokenkey -XAt -c \
    "select 'PG_SETTING '||json_build_object(
       'shared_buffers', current_setting('shared_buffers'),
       'effective_cache_size', current_setting('effective_cache_size'),
       'work_mem', current_setting('work_mem'),
       'maintenance_work_mem', current_setting('maintenance_work_mem'),
       'max_connections', current_setting('max_connections'),
       'max_parallel_workers', current_setting('max_parallel_workers'),
       'max_parallel_workers_per_gather', current_setting('max_parallel_workers_per_gather'),
       'autovacuum_max_workers', current_setting('autovacuum_max_workers'),
       'autovacuum_work_mem', current_setting('autovacuum_work_mem'),
       'archive_mode', current_setting('archive_mode'),
       'archive_timeout', current_setting('archive_timeout')
     );" 2>/dev/null || echo 'PG_SETTING {"error":"psql_failed"}'
else
  echo 'PG_LIVE {"error":"container_not_running"}'
fi

echo "=== MAINT_BUNDLE ==="
if [ -d "${MAINT_ROOT}" ]; then
  printf 'MAINT_ROOT {"path":"%s","exists":true}\n' "${MAINT_ROOT}"
  # shellcheck disable=SC2012
  ls -1 "${MAINT_ROOT}" 2>/dev/null | while read -r name; do
    [ -n "${name}" ] || continue
    printf 'MAINT_ENTRY {"name":"%s"}\n' "${name}"
  done
  latest="$(ls -1dt "${MAINT_ROOT}"/*/ 2>/dev/null | head -1 || true)"
  if [ -n "${latest}" ]; then
    printf 'MAINT_LATEST {"path":"%s"}\n' "${latest%/}"
    find "${latest}" -maxdepth 2 -type f 2>/dev/null | head -40 | while read -r f; do
      sum="$(sha256sum "${f}" 2>/dev/null | awk '{print $1}')"
      printf 'MAINT_FILE {"path":"%s","sha256":"%s","bytes":%s}\n' \
        "${f}" "${sum:-}" "$(wc -c <"${f}" 2>/dev/null | tr -d ' ')"
    done
  fi
else
  printf 'MAINT_ROOT {"path":"%s","exists":false}\n' "${MAINT_ROOT}"
fi

echo "=== DISK_METRICS ==="
if [ -x "${DISK_METRICS_BIN}" ]; then
  if grep -q 'handle_pg_cgroup_state\|PG_ANON_WARN_MIB\|不可回收内存' "${DISK_METRICS_BIN}" 2>/dev/null; then
    echo 'DISK_METRICS_ANON {"installed":true,"anon_alert_present":true}'
  else
    echo 'DISK_METRICS_ANON {"installed":true,"anon_alert_present":false}'
  fi
  systemctl is-active tokenkey-disk-metrics.timer 2>/dev/null \
    | awk '{printf "DISK_METRICS_TIMER {\"active\":\"%s\"}\n", $0}'
else
  echo 'DISK_METRICS_ANON {"installed":false,"anon_alert_present":false}'
fi

echo "=== BACKUP_STATE ==="
if [ -d "${RESTORE_STATE_DIR}" ]; then
  for f in restore_acceptance.json repository_verification.json; do
    p="${RESTORE_STATE_DIR}/${f}"
    if [ -f "${p}" ]; then
      # Emit compact fields only; full JSON may contain host paths.
      python3 - "${p}" <<'PY' 2>/dev/null || printf 'BACKUP_FILE {"path":"%s","parse_error":true}\n' "${p}"
import json, sys
path = sys.argv[1]
try:
    data = json.load(open(path))
except Exception as e:
    print(f'BACKUP_FILE {{"path":"{path}","parse_error":true,"err":"{e}"}}')
    raise SystemExit(0)
status = data.get("status") or data.get("verdict") or data.get("result")
reasons = data.get("reasons")
ts = data.get("completed_at") or data.get("checked_at") or data.get("accepted_at") or data.get("started_at")
out = {
    "path": path,
    "status": status,
    "reasons": reasons,
    "completed_at": ts,
    "repo": data.get("repo"),
    "stanza": data.get("stanza"),
    "backup_count": data.get("backup_count"),
    "points_total": len(data["points"]) if isinstance(data.get("points"), list) else None,
}
print("BACKUP_FILE " + json.dumps(out, ensure_ascii=False))
PY
    else
      printf 'BACKUP_FILE {"path":"%s","exists":false}\n' "${p}"
    fi
  done
else
  printf 'BACKUP_STATE {"dir":"%s","exists":false}\n' "${RESTORE_STATE_DIR}"
fi

echo "=== PGBACKREST_INFO ==="
# Prefer on-box SSOT; `check` is read-only against stanza/repo — never invoke `backup`.
PGBR=""
for cand in \
  /usr/local/lib/tokenkey/tencent-pgbackrest/tencent_pgbackrest.py \
  /usr/local/lib/tokenkey/tencent_pgbackrest.py \
  /opt/tokenkey/tencent_pgbackrest.py \
  "$(cd "$(dirname "$0")/../.." && pwd)/deploy/tencent/tencent_pgbackrest.py"
do
  if [ -f "${cand}" ]; then PGBR="${cand}"; break; fi
done
if [ -n "${PGBR}" ]; then
  printf 'PGBACKREST_ENTRY {"path":"%s"}\n' "${PGBR}"
  python3 "${PGBR}" check 2>&1 | awk '{printf "PGBACKREST_CHECK %s\n", $0}' | head -80
elif command -v pgbackrest >/dev/null 2>&1; then
  pgbackrest --output=json info 2>/dev/null | head -c 4000 | awk '{printf "PGBACKREST_INFO_JSON %s\n", $0}'
else
  echo 'PGBACKREST {"error":"no_pgbackrest_entry"}'
fi

echo "=== PROFILE_HINT ==="
# Informational only — profile flip happens after approved resize.
printf 'PROFILE_HINT {"after_resize_set":"TOKENKEY_DATA_HOST_PROFILE=prod-4c16g","default_means":"live_2c8g_pg6g_expectations"}\n'

echo "=== PREP_DONE ==="
echo 'PREP_DONE {"stop_write":false,"resize":false,"container_recreate":false}'
exit 0
