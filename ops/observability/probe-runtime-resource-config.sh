#!/usr/bin/env bash
# Read-only Stage0 runtime resource configuration snapshot. Emits one JSON line
# for runtime_resource_config_verdict.py; no secrets or application payloads.
set -euo pipefail

active=""
if [ -r /var/lib/tokenkey/active-color ]; then
  active="$(tr -d '[:space:]' < /var/lib/tokenkey/active-color)"
fi
case "$active" in
  blue|green) app_container="tokenkey-${active}" ;;
  *) app_container="tokenkey" ;;
esac

log_field() {
  local container="$1" field="$2"
  case "$field" in
    driver) docker inspect "$container" --format '{{.HostConfig.LogConfig.Type}}' 2>/dev/null || true ;;
    max_size) docker inspect "$container" --format '{{index .HostConfig.LogConfig.Config "max-size"}}' 2>/dev/null || true ;;
    max_file) docker inspect "$container" --format '{{index .HostConfig.LogConfig.Config "max-file"}}' 2>/dev/null || true ;;
  esac
}

redis_config() {
  docker exec tokenkey-redis sh -c "redis-cli --raw CONFIG GET '$1'" 2>/dev/null | tail -1
}

jq -cn \
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
