#!/usr/bin/env bash
# probe-data-machine-memory.sh — read-only data-host memory / PG cgroup snapshot.
#
# Run via:
#   bash ops/observability/run-probe.sh --target prod-data \
#     --script ops/observability/probe-data-machine-memory.sh
#   bash ops/observability/run-probe.sh --target cn-prod-data \
#     --script ops/observability/probe-data-machine-memory.sh
#
# Emits tagged JSON lines for humans and future verdict helpers. Does not alert;
# alerting owner is deploy/hetzner/tokenkey-disk-metrics-prod.sh (anon cgroup).
# Design: docs/approved/design-data-plane-split.md §数据机观测
set -u

PG_CONTAINER="${TOKENKEY_PG_CONTAINER:-tokenkey-postgres}"
REDIS_CONTAINER="${TOKENKEY_REDIS_CONTAINER:-tokenkey-redis}"

echo "=== meta ==="
printf '{"hostname":"%s","utc":"%s","pg_container":"%s"}\n' \
  "$(hostname)" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "${PG_CONTAINER}"

echo "=== HOST_MEM ==="
awk 'BEGIN{t=0;a=0;f=0;c=0;an=0}
  /^MemTotal:/{t=$2}
  /^MemAvailable:/{a=$2}
  /^MemFree:/{f=$2}
  /^Cached:/{c=$2}
  /^AnonPages:/{an=$2}
  END{
    used=0; if(t>0) used=int((t-a)*100/t);
    printf "HOST_MEM {\"mem_total_kib\":%d,\"mem_available_kib\":%d,\"mem_free_kib\":%d,\"cached_kib\":%d,\"anon_pages_kib\":%d,\"used_pct_by_available\":%d}\n", t,a,f,c,an,used
  }' /proc/meminfo

echo "=== LOAD ==="
read -r l1 l5 l15 _ </proc/loadavg || true
printf 'LOAD {"nproc":%s,"load1":%s,"load5":%s,"load15":%s}\n' \
  "$(nproc 2>/dev/null || echo 0)" "${l1:-0}" "${l5:-0}" "${l15:-0}"

echo "=== DF_ROOT ==="
df -P -B1 / 2>/dev/null | awk 'NR==2{
  printf "DF_ROOT {\"total_bytes\":%s,\"used_bytes\":%s,\"avail_bytes\":%s,\"use_pct\":%s,\"mount\":\"%s\"}\n", $2,$3,$4,$5,$6
}'

echo "=== DOCKER_STATS ==="
if command -v docker >/dev/null 2>&1; then
  docker stats --no-stream --format '{{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}\t{{.MemPerc}}' 2>/dev/null \
    | while IFS=$'\t' read -r name cpu mem mempct; do
        printf 'DOCKER_STAT {"name":"%s","cpu":"%s","mem":"%s","mem_pct":"%s"}\n' \
          "${name}" "${cpu}" "${mem}" "${mempct}"
      done
fi

echo "=== PG_CGROUP ==="
if docker inspect -f '{{.State.Running}}' "${PG_CONTAINER}" 2>/dev/null | grep -qx true; then
  cid="$(docker inspect -f '{{.Id}}' "${PG_CONTAINER}")"
  scope="/sys/fs/cgroup/system.slice/docker-${cid}.scope"
  if [ ! -d "${scope}" ]; then
    scope="$(find /sys/fs/cgroup -type d -name "docker-${cid}.scope" 2>/dev/null | head -1)"
  fi
  if [ -n "${scope}" ] && [ -d "${scope}" ]; then
    current="$(cat "${scope}/memory.current" 2>/dev/null || echo 0)"
    max="$(cat "${scope}/memory.max" 2>/dev/null || echo 0)"
    case "${max}" in max|"") max=0 ;; esac
    anon="$(awk '/^anon /{print $2; exit}' "${scope}/memory.stat" 2>/dev/null || echo 0)"
    file="$(awk '/^file /{print $2; exit}' "${scope}/memory.stat" 2>/dev/null || echo 0)"
    shmem="$(awk '/^shmem /{print $2; exit}' "${scope}/memory.stat" 2>/dev/null || echo 0)"
    oom="$(awk '/^oom /{print $2; exit}' "${scope}/memory.events" 2>/dev/null || echo 0)"
    peak="$(cat "${scope}/memory.peak" 2>/dev/null || echo 0)"
    fill=0
    if [ "${max}" -gt 0 ] 2>/dev/null; then fill=$(( current * 100 / max )); fi
    printf 'PG_CGROUP {"current_bytes":%s,"peak_bytes":%s,"max_bytes":%s,"fill_pct":%s,"anon_bytes":%s,"file_bytes":%s,"shmem_bytes":%s,"oom_events":%s}\n' \
      "${current}" "${peak}" "${max}" "${fill}" "${anon}" "${file}" "${shmem}" "${oom}"
  else
    echo 'PG_CGROUP {"error":"cgroup_not_found"}'
  fi
  docker inspect "${PG_CONTAINER}" --format \
    'PG_LIMIT {"memory_bytes":{{.HostConfig.Memory}},"started_at":"{{.State.StartedAt}}","oom_killed":{{.State.OOMKilled}}}'
else
  echo 'PG_CGROUP {"error":"container_not_running"}'
fi

echo "=== PG_SETTINGS ==="
if docker inspect -f '{{.State.Running}}' "${PG_CONTAINER}" 2>/dev/null | grep -qx true; then
  docker exec --user postgres "${PG_CONTAINER}" psql -h 127.0.0.1 -U tokenkey -d tokenkey -XAt -c \
    "select 'PG_SETTING '||json_build_object(
       'shared_buffers', current_setting('shared_buffers'),
       'effective_cache_size', current_setting('effective_cache_size'),
       'work_mem', current_setting('work_mem'),
       'maintenance_work_mem', current_setting('maintenance_work_mem'),
       'max_connections', current_setting('max_connections'),
       'max_parallel_workers', current_setting('max_parallel_workers'),
       'max_parallel_workers_per_gather', current_setting('max_parallel_workers_per_gather')
     );" 2>/dev/null || echo 'PG_SETTING {"error":"psql_failed"}'
  docker exec --user postgres "${PG_CONTAINER}" psql -h 127.0.0.1 -U tokenkey -d tokenkey -XAt -c \
    "select 'PG_ACTIVITY '||json_build_object(
       'total', count(*),
       'active', count(*) filter (where state='active'),
       'idle', count(*) filter (where state='idle'),
       'idle_in_tx', count(*) filter (where state='idle in transaction')
     ) from pg_stat_activity;" 2>/dev/null || true
fi

echo "=== REDIS_HINT ==="
if docker inspect -f '{{.State.Running}}' "${REDIS_CONTAINER}" 2>/dev/null | grep -qx true; then
  docker exec "${REDIS_CONTAINER}" sh -c \
    'REDISCLI_AUTH="$(cat /run/requirepass 2>/dev/null)"; export REDISCLI_AUTH; redis-cli --no-auth-warning INFO memory' 2>/dev/null \
    | awk -F: '/^(used_memory_human|maxmemory_human|mem_fragmentation_ratio)/{gsub(/\r/,"",$2); printf "REDIS %s=%s\n",$1,$2}'
fi

exit 0
