#!/usr/bin/env bash
# Read-only Stage0 runtime resource configuration snapshot. Emits one JSON line
# for runtime_resource_config_verdict.py; no secrets or application payloads.
set -euo pipefail

# PG/Redis container names differ between Stage0 (tokenkey-postgres/-redis) and
# the Tencent USW layout; ops/lib/resolve-app-container.sh owns the choice and
# run-probe.sh ships it next to this script.
for _tk_lib in "${TK_LIB_DIR:-$(dirname "${BASH_SOURCE[0]}")}" "$(dirname "${BASH_SOURCE[0]}")/../lib"; do
  [ -f "$_tk_lib/resolve-app-container.sh" ] && break
done
# shellcheck source=../lib/resolve-app-container.sh
. "$_tk_lib/resolve-app-container.sh" || { echo "container resolver not found (ops/lib/resolve-app-container.sh)" >&2; exit 1; }
# Unresolved containers leave their fields empty; the verdict treats that as a
# failed observation instead of reading a stopped container or the control proxy.
app_container="$(tk_resolve_app_container auto || true)"
redis_container="$(tk_resolve_redis_container "${REDIS_CONTAINER:-auto}" || true)"

log_field() {
  local container="$1" field="$2"
  [ -n "$container" ] || return 0
  case "$field" in
    driver) docker inspect "$container" --format '{{.HostConfig.LogConfig.Type}}' 2>/dev/null || true ;;
    max_size) docker inspect "$container" --format '{{index .HostConfig.LogConfig.Config "max-size"}}' 2>/dev/null || true ;;
    max_file) docker inspect "$container" --format '{{index .HostConfig.LogConfig.Config "max-file"}}' 2>/dev/null || true ;;
  esac
}

redis_config() {
  [ -n "$redis_container" ] || return 0
  docker exec "$redis_container" sh -c "redis-cli --raw CONFIG GET '$1'" 2>/dev/null | tail -1
}

layout="${RUNTIME_RESOURCE_LAYOUT:-usw}"
jq -cn \
  --arg layout "$layout" \
  --arg app_container "$app_container" \
  --arg caddy_driver "$(log_field tokenkey-caddy driver)" \
  --arg caddy_max_size "$(log_field tokenkey-caddy max_size)" \
  --arg caddy_max_file "$(log_field tokenkey-caddy max_file)" \
  --arg app_driver "$(log_field "$app_container" driver)" \
  --arg app_max_size "$(log_field "$app_container" max_size)" \
  --arg app_max_file "$(log_field "$app_container" max_file)" \
  --arg redis_appendonly "$(redis_config appendonly)" \
  --arg redis_maxmemory "$(redis_config maxmemory)" \
  --arg redis_maxmemory_policy "$(redis_config maxmemory-policy)" \
  '{
    layout: $layout,
    docker_logs: {
      caddy: {driver:$caddy_driver,max_size:$caddy_max_size,max_file:$caddy_max_file},
      app: {container:$app_container,driver:$app_driver,max_size:$app_max_size,max_file:$app_max_file}
    },
    redis: {
      appendonly:$redis_appendonly,
      maxmemory:$redis_maxmemory,
      maxmemory_policy:$redis_maxmemory_policy
    }
  }'
