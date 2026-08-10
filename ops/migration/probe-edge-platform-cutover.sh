#!/usr/bin/env bash
# Read-only host probe for one side of an Edge platform cutover.
set -euo pipefail

SINCE="${SINCE:-15m}"
CONTAINER_INPUT="${CONTAINER:-auto}"

resolver=""
for candidate in \
  "${TK_LIB_DIR:-$(dirname "$0")}/resolve-app-container.sh" \
  "$(dirname "$0")/../lib/resolve-app-container.sh"; do
  if [ -f "$candidate" ]; then
    resolver="$candidate"
    break
  fi
done
if [ -z "$resolver" ]; then
  echo "canonical app-container resolver not found" >&2
  exit 2
fi
# shellcheck source=../lib/resolve-app-container.sh
. "$resolver"

if ! app_container="$(tk_resolve_app_container "$CONTAINER_INPUT")"; then
  echo "app container unresolved" >&2
  exit 3
fi

running="$(docker inspect -f '{{.State.Running}}' "$app_container")"
health_status="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{end}}' "$app_container")"
docker_healthy=false
if [ "$running" = "true" ] && { [ -z "$health_status" ] || [ "$health_status" = "healthy" ]; }; then
  docker_healthy=true
fi

health_ok=false
if docker exec "$app_container" wget -q -T 5 -O /dev/null http://127.0.0.1:8080/health; then
  health_ok=true
fi

account_counts="$(docker exec tokenkey-postgres \
  psql -U tokenkey -d tokenkey -X -A -t -F '|' -c \
  "SELECT COUNT(*), COUNT(*) FILTER (WHERE status = 'active' AND schedulable) FROM accounts WHERE deleted_at IS NULL;")"
if ! printf '%s' "$account_counts" | grep -Eq '^[0-9]+\|[0-9]+$'; then
  echo "invalid account count output" >&2
  exit 4
fi
account_total="${account_counts%%|*}"
schedulable_accounts="${account_counts##*|}"

log_file="$(mktemp /tmp/tokenkey-cutover-log.XXXXXX)"
trap 'rm -f "$log_file"' EXIT
docker logs "$app_container" --since "$SINCE" >"$log_file" 2>&1

image="$(docker inspect -f '{{.Config.Image}}' "$app_container")"
app_tag="${image##*:}"

backup_path="$(find /var/lib/tokenkey/pgdump -maxdepth 1 -type f -name 'tokenkey-*.sql.gz' -printf '%T@\t%p\n' 2>/dev/null \
  | sort -n | tail -1 | cut -f2-)"
backup_verified=false
backup_size=0
backup_checksum=""
if [ -n "$backup_path" ]; then
  backup_size="$(stat -c %s "$backup_path")"
  if [ "$backup_size" -ge 2048 ] && gzip -t "$backup_path"; then
    read -r backup_sha _ < <(sha256sum "$backup_path")
    if [[ "$backup_sha" =~ ^[0-9a-f]{64}$ ]]; then
      backup_verified=true
      backup_checksum="sha256:${backup_sha}"
    fi
  fi
fi

python3 - "$log_file" "$account_total" "$schedulable_accounts" \
  "$docker_healthy" "$health_ok" "$app_tag" "$backup_verified" \
  "$backup_path" "$backup_size" "$backup_checksum" <<'PY'
import json
import math
import pathlib
import re
import sys

(
    path,
    total,
    schedulable,
    docker_healthy,
    health_ok,
    app_tag,
    backup_verified,
    backup_path,
    backup_size,
    backup_checksum,
) = sys.argv[1:]
status_re = re.compile(r'"status_code"\s*:\s*([0-9]+)')
latency_re = re.compile(r'"latency_ms"\s*:\s*([0-9]+(?:\.[0-9]+)?)')
business_requests = 0
served_requests = 0
server_errors = 0
latencies = []
for line in pathlib.Path(path).read_text(encoding="utf-8", errors="replace").splitlines():
    if "http request completed" not in line:
        continue
    status_match = status_re.search(line)
    if status_match is None:
        continue
    status = int(status_match.group(1))
    business_requests += 1
    if 200 <= status < 300:
        served_requests += 1
    if 500 <= status < 600:
        server_errors += 1
    latency_match = latency_re.search(line)
    if latency_match is not None:
        latencies.append(float(latency_match.group(1)))

p95 = None
if latencies:
    ordered = sorted(latencies)
    p95 = ordered[max(0, math.ceil(0.95 * len(ordered)) - 1)]

print(json.dumps({
    "account_total": int(total),
    "schedulable_accounts": int(schedulable),
    "docker_healthy": docker_healthy == "true",
    "health_ok": health_ok == "true",
    "business_requests": business_requests,
    "served_requests": served_requests,
    "server_errors": server_errors,
    "p95_latency_ms": p95,
    "app_tag": app_tag,
    "logical_backup": {
        "verified": backup_verified == "true",
        "path": backup_path,
        "size_bytes": int(backup_size),
        "checksum": backup_checksum,
    },
}, sort_keys=True))
PY
