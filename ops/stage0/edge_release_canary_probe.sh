#!/usr/bin/env bash
# Read-only Edge release canary facts. Emits exactly one strict JSON object.
set -u

POLICY=""
for candidate in \
  "${EDGE_CAPACITY_POLICY:-}" \
  "$(dirname "$0")/bluegreen-capacity-policy.env" \
  "/tmp/bluegreen-capacity-policy.env"
do
  if [ -n "$candidate" ] && [ -f "$candidate" ]; then POLICY="$candidate"; break; fi
done
[ -n "$POLICY" ] || { echo "edge_release_canary_probe: missing bluegreen-capacity-policy.env" >&2; exit 1; }
# shellcheck source=bluegreen-capacity-policy.env
. "$POLICY"
MEMORY_FLOOR_BYTES="${EDGE_MIN_MEM_AVAILABLE_BYTES:?}"
MEMORY_HEADROOM_BYTES="${EDGE_ACTIVE_APP_HEADROOM_BYTES:?}"
DISK_FLOOR_BYTES="${EDGE_MIN_ROOT_DISK_AVAILABLE_BYTES:?}"

integer_or_empty() {
  case "${1:-}" in
    ''|*[!0-9]*) return 1 ;;
    *) printf '%s\n' "$1" ;;
  esac
}

bytes_from_docker_mem() {
  local raw="${1// /}" number unit multiplier
  number="$(printf '%s' "$raw" | sed -nE 's/^([0-9]+([.][0-9]+)?).*/\1/p')"
  unit="${raw#"${number}"}"
  case "$unit" in
    B|'') multiplier=1 ;;
    kB|KB) multiplier=1000 ;;
    KiB) multiplier=1024 ;;
    MB) multiplier=1000000 ;;
    MiB) multiplier=1048576 ;;
    GB) multiplier=1000000000 ;;
    GiB) multiplier=1073741824 ;;
    *) return 1 ;;
  esac
  [ -n "$number" ] || return 1
  awk -v number="$number" -v multiplier="$multiplier" 'BEGIN { printf "%.0f\n", number * multiplier }'
}

if [ "${EDGE_RELEASE_CANARY_TEST_MODE:-0}" = 1 ]; then
  MEM_AVAILABLE_RAW="${TEST_MEM_AVAILABLE_BYTES:-}"
  ACTIVE_WORKING_SET_RAW="${TEST_ACTIVE_WORKING_SET_BYTES:-}"
  DISK_AVAILABLE_RAW="${TEST_DISK_AVAILABLE_BYTES:-}"
  COMPLETED_RAW="${TEST_COMPLETED_REQUESTS_30M:-}"
  OAUTH_RAW="${TEST_OAUTH_ACCOUNT_COUNT:-}"
else
  RESOLVER=""
  for candidate in "${TK_LIB_DIR:-/tmp}/resolve-app-container.sh" "$(dirname "$0")/../lib/resolve-app-container.sh"; do
    if [ -f "$candidate" ]; then RESOLVER="$candidate"; break; fi
  done
  CONTAINER=""
  if [ -n "$RESOLVER" ]; then
    TK_DOCKER="sudo docker"
    # shellcheck source=../lib/resolve-app-container.sh
    . "$RESOLVER"
    CONTAINER="$(tk_resolve_app_container auto 2>/dev/null || true)"
  fi

  MEM_AVAILABLE_RAW="$(awk '/^MemAvailable:/ {printf "%.0f\n", $2 * 1024; exit}' /proc/meminfo 2>/dev/null)"
  ACTIVE_WORKING_SET_RAW=""
  COMPLETED_RAW=""
  if [ -n "$CONTAINER" ]; then
    ACTIVE_USAGE="$(sudo docker stats --no-stream --format '{{.MemUsage}}' "$CONTAINER" 2>/dev/null | cut -d/ -f1)"
    ACTIVE_WORKING_SET_RAW="$(bytes_from_docker_mem "$ACTIVE_USAGE" || true)"
    LOG_FILE="$(mktemp /tmp/edge-release-canary.XXXXXX)"
    if sudo docker logs "$CONTAINER" --since 30m >"$LOG_FILE" 2>/dev/null; then
      COMPLETED_RAW="$(grep -cF 'http request completed' "$LOG_FILE" || true)"
    fi
    rm -f "$LOG_FILE"
  fi
  DISK_AVAILABLE_RAW="$(df -B1 --output=avail / 2>/dev/null | awk 'NR==2 {print $1}')"

  OAUTH_RAW=""
  OAUTH_PROBE=""
  for candidate in /tmp/edge_oauth_pool_probe.sh "$(dirname "$0")/edge_oauth_pool_probe.sh"; do
    if [ -f "$candidate" ]; then OAUTH_PROBE="$candidate"; break; fi
  done
  if [ -n "$OAUTH_PROBE" ]; then
    OAUTH_RAW="$(ANTHROPIC_SOURCE_GROUP="${ANTHROPIC_SOURCE_GROUP:-default}" bash "$OAUTH_PROBE" 2>/dev/null || true)"
  fi
fi

MEM_AVAILABLE="$(integer_or_empty "$MEM_AVAILABLE_RAW" || true)"
ACTIVE_WORKING_SET="$(integer_or_empty "$ACTIVE_WORKING_SET_RAW" || true)"
DISK_AVAILABLE="$(integer_or_empty "$DISK_AVAILABLE_RAW" || true)"
COMPLETED="$(integer_or_empty "$COMPLETED_RAW" || true)"
OAUTH_COUNT="$(integer_or_empty "$OAUTH_RAW" || true)"

MEMORY_REQUIRED=""
MEMORY_HEADROOM=""
if [ -n "$ACTIVE_WORKING_SET" ]; then
  MEMORY_REQUIRED=$((ACTIVE_WORKING_SET + MEMORY_HEADROOM_BYTES))
  if [ "$MEMORY_REQUIRED" -lt "$MEMORY_FLOOR_BYTES" ]; then MEMORY_REQUIRED=$MEMORY_FLOOR_BYTES; fi
fi
if [ -n "$MEM_AVAILABLE" ] && [ -n "$MEMORY_REQUIRED" ]; then
  MEMORY_HEADROOM=$((MEM_AVAILABLE - MEMORY_REQUIRED))
fi

REASONS=()
[ -n "$MEM_AVAILABLE" ] || REASONS+=(mem_available_unknown)
[ -n "$ACTIVE_WORKING_SET" ] || REASONS+=(active_working_set_unknown)
[ -n "$DISK_AVAILABLE" ] || REASONS+=(disk_available_unknown)
[ -n "$COMPLETED" ] || REASONS+=(completed_requests_30m_unknown)
if [ -n "$MEM_AVAILABLE" ] && [ -n "$MEMORY_REQUIRED" ] && [ "$MEM_AVAILABLE" -lt "$MEMORY_REQUIRED" ]; then
  REASONS+=(memory_below_required)
fi
if [ -n "$DISK_AVAILABLE" ] && [ "$DISK_AVAILABLE" -lt "$DISK_FLOOR_BYTES" ]; then
  REASONS+=(disk_below_required)
fi

if [ "${#REASONS[@]}" -eq 0 ]; then ELIGIBLE=true; else ELIGIBLE=false; fi
to_json_number() { if [ -n "${1:-}" ]; then printf '%s' "$1"; else printf 'null'; fi; }
REASONS_JSON="["
separator=""
for reason in "${REASONS[@]+"${REASONS[@]}"}"; do
  REASONS_JSON="${REASONS_JSON}${separator}\"${reason}\""
  separator=,
done
REASONS_JSON="${REASONS_JSON}]"

printf '{"mem_available_bytes":%s,"active_app_working_set_bytes":%s,"memory_required_bytes":%s,"memory_headroom_bytes":%s,"disk_available_bytes":%s,"completed_requests_30m":%s,"oauth_account_count":%s,"eligible":%s,"rejection_reasons":%s}\n' \
  "$(to_json_number "$MEM_AVAILABLE")" \
  "$(to_json_number "$ACTIVE_WORKING_SET")" \
  "$(to_json_number "$MEMORY_REQUIRED")" \
  "$(to_json_number "$MEMORY_HEADROOM")" \
  "$(to_json_number "$DISK_AVAILABLE")" \
  "$(to_json_number "$COMPLETED")" \
  "$(to_json_number "$OAUTH_COUNT")" \
  "$ELIGIBLE" \
  "$REASONS_JSON"
