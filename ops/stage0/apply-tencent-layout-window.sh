#!/usr/bin/env bash
# apply-tencent-layout-window.sh — Exception-path Stage0 layout cutover on Tencent USW.
#
# Default: refuse (dry inventory).
# APPLY=yes-layout-window-cutover — authorized maintenance window only.
#
# Does NOT: stop docker daemon, rewrite daemon.json data-root, rename PG container,
# pull images, or touch cn-prod.
set -euo pipefail

APPLY="${APPLY:-}"
ROOT=/var/lib/tokenkey
STAGING="${ROOT}/layout-staging"
READY_ENV="${ROOT}/.env.stage0-ready"
LIVE_ENV="${ROOT}/.env"
FORMAL_ENV="${ROOT}/formal/.env"
CADDYFILE="${ROOT}/formal/caddy/Caddyfile.cutover"
TS="$(date -u +%Y%m%dT%H%M%SZ)"
RECEIPT_DIR="${STAGING}/receipts"
PHASE="pre"

emit() {
  python3 -c 'import json,sys; print(json.dumps({"k":sys.argv[1],"v":sys.argv[2]}, ensure_ascii=False))' "$1" "$2"
}

# After gateway stop, any failure must attempt rollback (containers may be down).
rollback_if_past_stop() {
  case "$PHASE" in
    stop_gateway|install_env|caddyfile|network_aliases|rename|start)
      rollback || true
      ;;
  esac
}

die() {
  emit "error" "$1"
  rollback_if_past_stop
  exit 1
}

trap 'ec=$?; emit "trap_error" "phase=${PHASE} ec=${ec}"; rollback_if_past_stop; exit "$ec"' ERR

containers_list() {
  sudo docker ps -a --format '{{.Names}}' | paste -sd, -
}

public_health() {
  # Host may resolve api.tokenkey.dev to itself; fall back to app container.
  local code
  code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 5 https://127.0.0.1/health -H 'Host: api.tokenkey.dev' -k 2>/dev/null || echo 000)"
  if [ "$code" = "200" ]; then
    echo "$code"
    return 0
  fi
  if sudo docker ps --format '{{.Names}}' | grep -Eq '^(tokenkey|tk-cutover-prod-app)$'; then
    local name
    name="$(sudo docker ps --format '{{.Names}}' | grep -E '^(tokenkey|tk-cutover-prod-app)$' | head -1)"
    sudo docker exec "$name" wget -q -O - http://127.0.0.1:8080/health >/dev/null 2>&1 && echo 200 && return 0
  fi
  echo "$code"
  return 1
}

wait_app_healthy() {
  local name="$1"
  local i status
  for i in $(seq 1 36); do
    status="$(sudo docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$name" 2>/dev/null || echo missing)"
    emit "wait_app" "${name}:${status}:try${i}"
    if [ "$status" = "healthy" ] || [ "$status" = "running" ]; then
      # Prefer healthy; accept running if no healthcheck race
      if [ "$status" = "healthy" ]; then
        return 0
      fi
    fi
    if [ "$status" = "running" ] && sudo docker exec "$name" wget -q -O /dev/null http://127.0.0.1:8080/health/live 2>/dev/null; then
      return 0
    fi
    sleep 5
  done
  return 1
}

rollback() {
  emit "rollback_start" "$PHASE"
  # Best-effort restore cutover names and prior caddyfile / alert env.
  sudo docker stop -t 30 tokenkey tokenkey-caddy 2>/dev/null || true
  if sudo docker ps -a --format '{{.Names}}' | grep -qx tokenkey; then
    sudo docker rename tokenkey tk-cutover-prod-app 2>/dev/null || true
  fi
  if sudo docker ps -a --format '{{.Names}}' | grep -qx tokenkey-caddy; then
    sudo docker rename tokenkey-caddy tk-cutover-prod-caddy 2>/dev/null || true
  fi
  if [ -f "${STAGING}/Caddyfile.cutover.bak.${TS}" ]; then
    sudo cp -a "${STAGING}/Caddyfile.cutover.bak.${TS}" "$CADDYFILE"
  fi
  if [ -f "${STAGING}/env.alert-only.bak.${TS}" ]; then
    sudo cp -a "${STAGING}/env.alert-only.bak.${TS}" "$LIVE_ENV"
    sudo chmod 600 "$LIVE_ENV"
  fi
  # Reattach PG/redis to network without failing if already connected
  sudo docker start tk-archive-standby tk-cutover-prod-redis 2>/dev/null || true
  sudo docker start tk-cutover-prod-app tk-cutover-prod-caddy 2>/dev/null || true
  emit "rollback_containers" "$(containers_list)"
  emit "rollback_health" "$(public_health || true)"
}

inventory() {
  emit "hostname" "$(hostname)"
  emit "date_utc" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  emit "containers" "$(containers_list)"
  emit "ready_env" "$([ -f "$READY_ENV" ] && echo present || echo missing)"
  emit "docker_root" "$(sudo docker info --format '{{.DockerRootDir}}')"
  emit "daemon_untouched_expected" "true"
}

ensure_preconditions() {
  [ -f "$READY_ENV" ] || die "missing .env.stage0-ready"
  [ -f "$FORMAL_ENV" ] || die "missing formal/.env"
  [ -f "$CADDYFILE" ] || die "missing Caddyfile.cutover"
  sudo docker inspect tk-cutover-prod-app >/dev/null 2>&1 || die "missing tk-cutover-prod-app"
  sudo docker inspect tk-cutover-prod-caddy >/dev/null 2>&1 || die "missing tk-cutover-prod-caddy"
  sudo docker inspect tk-cutover-prod-redis >/dev/null 2>&1 || die "missing tk-cutover-prod-redis"
  sudo docker inspect tk-archive-standby >/dev/null 2>&1 || die "missing tk-archive-standby"
  # Refuse if already partially migrated
  if sudo docker ps -a --format '{{.Names}}' | grep -qx tokenkey; then
    die "container tokenkey already exists; refuse double-apply"
  fi
  local hc
  hc="$(public_health || true)"
  emit "pre_health" "$hc"
  [ "$hc" = "200" ] || die "pre-health not 200"
}

cutover() {
  sudo mkdir -p "$RECEIPT_DIR" "${ROOT}/caddy"
  sudo chmod 755 "$STAGING" "$RECEIPT_DIR" "${ROOT}/caddy"

  PHASE="backup"
  sudo cp -a "$LIVE_ENV" "${STAGING}/env.alert-only.bak.${TS}"
  sudo cp -a "$CADDYFILE" "${STAGING}/Caddyfile.cutover.bak.${TS}"
  emit "backed_up" "${TS}"

  PHASE="stop_gateway"
  # Stop ingress first, then app. Keep redis + PG up.
  sudo docker stop -t 60 tk-cutover-prod-caddy
  sudo docker stop -t 180 tk-cutover-prod-app
  emit "stopped" "caddy,app"

  PHASE="install_env"
  # Atomic-ish: write temp then mv
  sudo cp -a "$READY_ENV" "${LIVE_ENV}.new"
  sudo chmod 600 "${LIVE_ENV}.new"
  sudo mv -f "${LIVE_ENV}.new" "$LIVE_ENV"
  emit "live_env_installed" "true"
  emit "live_has_image" "$(sudo grep -c '^TOKENKEY_IMAGE=' "$LIVE_ENV" || true)"

  PHASE="caddyfile"
  sudo cp -a "$CADDYFILE" "${CADDYFILE}.new"
  sudo sed -i 's/reverse_proxy tk-cutover-prod-app:8080/reverse_proxy tokenkey:8080/' "${CADDYFILE}.new"
  if ! sudo grep -q 'reverse_proxy tokenkey:8080' "${CADDYFILE}.new"; then
    die "caddyfile rewrite failed"
  fi
  sudo mv -f "${CADDYFILE}.new" "$CADDYFILE"
  # Stage0-looking path copy for future sync tools
  sudo cp -a "$CADDYFILE" "${ROOT}/caddy/Caddyfile"
  emit "caddy_upstream" "tokenkey:8080"

  PHASE="network_aliases"
  # While app is down, refresh aliases so stock compose hostnames resolve.
  sudo docker network disconnect tk-cutover-prod tk-archive-standby || true
  sudo docker network connect \
    --alias tk-archive-standby --alias postgres \
    tk-cutover-prod tk-archive-standby
  sudo docker network disconnect tk-cutover-prod tk-cutover-prod-redis || true
  sudo docker network connect \
    --alias tk-cutover-prod-redis --alias redis --alias tokenkey-redis \
    tk-cutover-prod tk-cutover-prod-redis
  emit "aliases" "postgres->tk-archive-standby,redis->tk-cutover-prod-redis"

  PHASE="rename"
  sudo docker rename tk-cutover-prod-app tokenkey
  sudo docker rename tk-cutover-prod-caddy tokenkey-caddy
  emit "renamed" "tokenkey,tokenkey-caddy"

  PHASE="start"
  sudo docker start tokenkey
  wait_app_healthy tokenkey || {
    rollback
    die "app failed health after rename"
  }
  sudo docker start tokenkey-caddy
  sleep 3
  local hc
  hc="$(public_health || true)"
  emit "post_health" "$hc"
  if [ "$hc" != "200" ]; then
    # one more wait — caddy may need a moment
    sleep 5
    hc="$(public_health || true)"
    emit "post_health_retry" "$hc"
  fi
  if [ "$hc" != "200" ]; then
    rollback
    die "public health not 200 after cutover"
  fi

  PHASE="done"
  local receipt="${RECEIPT_DIR}/window-cutover-${TS}.json"
  sudo tee "$receipt" >/dev/null <<EOF
{
  "at_utc": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "apply": "yes-layout-window-cutover",
  "path": "exception_keep_docker_root",
  "docker_root": "$(sudo docker info --format '{{.DockerRootDir}}')",
  "env": "${LIVE_ENV}",
  "containers": {
    "app": "tokenkey",
    "caddy": "tokenkey-caddy",
    "redis": "tk-cutover-prod-redis",
    "postgres": "tk-archive-standby"
  },
  "network": "tk-cutover-prod",
  "aliases": ["postgres", "redis", "tokenkey-redis"],
  "caddy_upstream": "tokenkey:8080",
  "daemon_json_rewritten": false,
  "pg_renamed": false,
  "post_health": "${hc}",
  "rollback_artifacts": {
    "alert_env": "${STAGING}/env.alert-only.bak.${TS}",
    "caddyfile": "${STAGING}/Caddyfile.cutover.bak.${TS}"
  }
}
EOF
  emit "receipt" "$receipt"
  emit "containers_final" "$(sudo docker ps --format '{{.Names}}:{{.Status}}' | paste -sd, -)"
  emit "summary" "layout_window_cutover_ok"
}

inventory
case "$APPLY" in
  "")
    emit "mode" "inventory"
    emit "next" "APPLY=yes-layout-window-cutover"
    ;;
  yes-layout-window-cutover)
    ensure_preconditions
    cutover
    ;;
  *)
    die "unknown APPLY"
    ;;
esac
