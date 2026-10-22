#!/usr/bin/env bash
# probe-tencent-layout-prep.sh — Read-only (default) inventory for Tencent USW
# Stage0 layout-convergence exception path. Optional APPLY stages side-path
# artifacts without stopping containers or changing live routes.
#
# Default: inventory only (safe).
# APPLY=yes-prepare-layout-staging:
#   - write /var/lib/tokenkey/layout-staging/ (compose copies, runbook, marker)
#   - write /var/lib/tokenkey/.env.stage0-ready from formal/.env + Feishu keys
#   - NEVER stop/recreate containers, NEVER rewrite live .env or daemon.json
#
# Stdout: JSON lines + final summary object. Secrets are never printed.
set -euo pipefail

APPLY="${APPLY:-}"
ROOT=/var/lib/tokenkey
FORMAL_ENV="${ROOT}/formal/.env"
ALERT_ENV="${ROOT}/.env"
STAGING="${ROOT}/layout-staging"
READY_ENV="${ROOT}/.env.stage0-ready"
MARKER="${STAGING}/DOCKER_ROOT_EXCEPTION.json"
RUNBOOK="${STAGING}/WINDOW_RUNBOOK.md"

json_escape() {
  python3 -c 'import json,sys; print(json.dumps(sys.stdin.read().rstrip("\n")))' <<<"$1"
}

emit() {
  local key="$1"
  local val="$2"
  printf '{"k":%s,"v":%s}\n' "$(json_escape "$key")" "$(json_escape "$val")"
}

require_root_paths() {
  [ -d "$ROOT" ] || { echo '{"error":"missing /var/lib/tokenkey"}' >&2; exit 1; }
  [ -f "$FORMAL_ENV" ] || { echo '{"error":"missing formal/.env"}' >&2; exit 1; }
}

inventory() {
  emit "hostname" "$(hostname 2>/dev/null || true)"
  emit "date_utc" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  emit "luks_mapper" "$(findmnt -n -o SOURCE --target "$ROOT" 2>/dev/null || true)"
  emit "mount_tokenkey" "$(findmnt -n -o TARGET,SOURCE --target "$ROOT" 2>/dev/null || true)"
  emit "mount_rehearsal" "$(findmnt -n -o TARGET,SOURCE --target /srv/tokenkey-rehearsal 2>/dev/null || true)"
  emit "docker_root" "$(sudo docker info --format '{{.DockerRootDir}}' 2>/dev/null || true)"
  emit "formal_env_exists" "$([ -f "$FORMAL_ENV" ] && echo true || echo false)"
  emit "alert_env_exists" "$([ -f "$ALERT_ENV" ] && echo true || echo false)"
  emit "ready_env_exists" "$([ -f "$READY_ENV" ] && echo true || echo false)"
  emit "staging_exists" "$([ -d "$STAGING" ] && echo true || echo false)"

  if [ -f "$FORMAL_ENV" ]; then
    emit "formal_env_bytes" "$(sudo wc -c <"$FORMAL_ENV" | tr -d ' ')"
    emit "formal_has_tokenkey_image" "$(sudo grep -c '^TOKENKEY_IMAGE=' "$FORMAL_ENV" || true)"
    emit "formal_tokenkey_image_tag" "$(sudo sed -n 's/^TOKENKEY_IMAGE=.*://p' "$FORMAL_ENV" | head -1)"
  fi
  if [ -f "$ALERT_ENV" ]; then
    emit "alert_env_keys" "$(sudo grep -E '^[A-Z0-9_]+=' "$ALERT_ENV" | cut -d= -f1 | paste -sd, -)"
  fi

  emit "containers" "$(sudo docker ps --format '{{.Names}}' 2>/dev/null | paste -sd, -)"
  while IFS= read -r name; do
    [ -n "$name" ] || continue
    local img net health
    img="$(sudo docker inspect -f '{{.Config.Image}}' "$name" 2>/dev/null || true)"
    net="$(sudo docker inspect -f '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}' "$name" 2>/dev/null || true)"
    health="$(sudo docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$name" 2>/dev/null || true)"
    emit "container.$name.image" "$img"
    emit "container.$name.networks" "$(echo "$net" | xargs)"
    emit "container.$name.health" "$health"
  done < <(sudo docker ps --format '{{.Names}}' 2>/dev/null)

  # How app reaches DB/redis (hostnames only — no secrets)
  if sudo docker ps --format '{{.Names}}' | grep -qx 'tk-cutover-prod-app'; then
    emit "app_database_host" "$(sudo docker exec tk-cutover-prod-app printenv DATABASE_HOST 2>/dev/null || true)"
    emit "app_redis_host" "$(sudo docker exec tk-cutover-prod-app printenv REDIS_HOST 2>/dev/null || true)"
    emit "app_compose_workdir_guess" "$(sudo docker inspect -f '{{index .Config.Labels "com.docker.compose.project.working_dir"}}' tk-cutover-prod-app 2>/dev/null || true)"
    emit "app_compose_project" "$(sudo docker inspect -f '{{index .Config.Labels "com.docker.compose.project"}}' tk-cutover-prod-app 2>/dev/null || true)"
    emit "app_compose_config_files" "$(sudo docker inspect -f '{{index .Config.Labels "com.docker.compose.project.config_files"}}' tk-cutover-prod-app 2>/dev/null || true)"
  fi

  emit "daemon_json_data_root" "$(sudo python3 -c 'import json; p="/etc/docker/daemon.json";
import pathlib
p=pathlib.Path(p)
print(json.loads(p.read_text()).get("data-root","") if p.exists() else "")' 2>/dev/null || true)"

  # Backup unit references (names only)
  if systemctl cat tokenkey-postgres-backup.service >/dev/null 2>&1; then
    emit "backup_service_mentions_pg_container" "$(systemctl cat tokenkey-postgres-backup.service 2>/dev/null | grep -E 'tk-archive-standby|postgres|docker exec' | head -5 | tr '\n' '|' || true)"
  fi
}

merge_env_to_ready() {
  # Merge on-host only. Never echo values.
  local tmp
  tmp="$(mktemp)"
  sudo cp -a "$FORMAL_ENV" "$tmp"
  sudo chmod 600 "$tmp"
  if [ -f "$ALERT_ENV" ]; then
    while IFS= read -r line; do
      case "$line" in
        TOKENKEY_FEISHU_*=*)
          key="${line%%=*}"
          # drop existing key then append alert value
          sudo sed -i "/^${key}=/d" "$tmp"
          printf '%s\n' "$line" | sudo tee -a "$tmp" >/dev/null
          ;;
      esac
    done < <(sudo grep -E '^TOKENKEY_FEISHU_[A-Z0-9_]+=' "$ALERT_ENV" || true)
  fi
  sudo mv -f "$tmp" "$READY_ENV"
  sudo chmod 600 "$READY_ENV"
  sudo chown root:root "$READY_ENV"
}

stage_artifacts() {
  sudo mkdir -p "$STAGING/compose" "$STAGING/receipts"
  sudo chmod 755 "$STAGING" "$STAGING/compose" "$STAGING/receipts"

  # Capture live compose paths for the window operator (copy if readable)
  local cfg
  cfg="$(sudo docker inspect -f '{{index .Config.Labels "com.docker.compose.project.config_files"}}' tk-cutover-prod-app 2>/dev/null || true)"
  if [ -n "$cfg" ]; then
    emit "staging_source_compose" "$cfg"
    IFS=',' read -r -a parts <<<"$cfg"
    local i=0
    for p in "${parts[@]}"; do
      p="$(echo "$p" | xargs)"
      [ -n "$p" ] || continue
      if [ -f "$p" ]; then
        sudo cp -a "$p" "$STAGING/compose/live-$(basename "$p").copy"
        emit "staged_compose_copy.$i" "$STAGING/compose/live-$(basename "$p").copy"
        i=$((i + 1))
      fi
    done
  fi

  merge_env_to_ready
  emit "ready_env_bytes" "$(sudo wc -c <"$READY_ENV" | tr -d ' ')"
  emit "ready_has_tokenkey_image" "$(sudo grep -c '^TOKENKEY_IMAGE=' "$READY_ENV" || true)"
  emit "ready_has_feishu" "$(sudo grep -c '^TOKENKEY_FEISHU_' "$READY_ENV" || true)"

  local docker_root live_tag app_image
  docker_root="$(sudo docker info --format '{{.DockerRootDir}}' 2>/dev/null || true)"
  live_tag="$(sudo sed -n 's/^TOKENKEY_IMAGE=.*://p' "$FORMAL_ENV" | head -1)"
  app_image="$(sudo docker inspect -f '{{.Config.Image}}' tk-cutover-prod-app 2>/dev/null || true)"

  sudo tee "$MARKER" >/dev/null <<EOF
{
  "schema_version": 1,
  "path": "exception",
  "decision": "keep_docker_data_root",
  "docker_root_live": "${docker_root}",
  "docker_root_stage0_ideal": "/var/lib/tokenkey/docker",
  "same_luks_dual_mount": true,
  "note": "/var/lib/tokenkey/docker and /srv/tokenkey-rehearsal/docker are the same tree via dual mount; exception means do NOT restart Docker / rewrite daemon.json.",
  "rationale": "Changing data-root still requires Docker restart (PG down). Keep daemon.json as-is to minimize downtime.",
  "live_app_image": "${app_image}",
  "live_tokenkey_image_tag": "${live_tag}",
  "live_network": "tk-cutover-prod",
  "live_database_host": "tk-archive-standby",
  "live_redis_host": "tk-cutover-prod-redis",
  "window_must_not": [
    "systemctl stop docker",
    "rewrite /etc/docker/daemon.json data-root",
    "rename tk-archive-standby in this window",
    "drop stock deploy/aws/stage0/docker-compose.yml unchanged (DATABASE_HOST=postgres would miss live PG name)"
  ],
  "window_must": [
    "stop only app+caddy (+redis if required)",
    "atomic flip .env.stage0-ready -> /var/lib/tokenkey/.env (preserve formal/.env and alert keys)",
    "recreate gateway containers with Stage0 names OR keep cutover names but install Stage0 .env path — see WINDOW_RUNBOOK.md",
    "keep DATABASE_HOST=tk-archive-standby and network tk-cutover-prod unless aliases added",
    "use SAME image digests (no pull)",
    "restore /health before deferred smoke"
  ]
}
EOF

  sudo tee "$RUNBOOK" >/dev/null <<'EOF'
# Tencent USW · Stage0 layout window (exception path)

Status: **staging only** until an operator explicitly runs the window steps.
Do **not** treat presence of `.env.stage0-ready` as authorization to cut over.

## Inventory facts (pre-window)
- Dual mount same LUKS: `/var/lib/tokenkey` ≡ `/srv/tokenkey-rehearsal`.
- Docker Root Dir (daemon.json): `/srv/tokenkey-rehearsal/docker` — **keep** (exception).
- Live stack: `tk-cutover-prod-{app,caddy,redis}` + `tk-archive-standby` on network `tk-cutover-prod`.
- App `DATABASE_HOST=tk-archive-standby`, `REDIS_HOST=tk-cutover-prod-redis`.
- No compose project labels on live containers; Caddyfile at `formal/caddy/Caddyfile.cutover`.
- Business env today: `formal/.env` (tag in TOKENKEY_IMAGE). Alert-only Feishu keys live in `/var/lib/tokenkey/.env`.

## Pre-checked exceptions
- Keep Docker Root Dir / daemon.json unchanged (do not `systemctl stop docker`).
- Keep PG container name `tk-archive-standby` this window.

## Window critical path (minimize downtime)
1. Confirm `/health` ok; freeze other deploys.
2. `docker stop` **tk-cutover-prod-app** and **tk-cutover-prod-caddy** (redis only if required for rename).
3. Keep **tk-archive-standby** running.
4. Install `.env.stage0-ready` → `/var/lib/tokenkey/.env` (atomic replace). Keep `formal/.env` for rollback.
5. Recreate **app+caddy(+redis)** with **same image digests**, still on `tk-cutover-prod`, still resolving PG as `tk-archive-standby`. Prefer Stage0 container names (`tokenkey`, `tokenkey-caddy`, `tokenkey-redis`) **only if** Caddy upstream + env hostnames are updated together; otherwise first flip `.env` path while keeping cutover container names, then rename in a follow-up.
6. When `/health` is 200, restore traffic; deferred smoke after.
7. Update SSOT (docker-root exception + clear layout debt) before enabling stock `deploy-stage0`. Stock compose assumes `DATABASE_HOST=postgres` — do not enable until aliases/names match or bluegreen init is planned.

## Rollback
- Stop new containers; start previous `tk-cutover-prod-*`; continue using `formal/.env`.
EOF

  local receipt
  receipt="${STAGING}/receipts/prepare-$(date -u +%Y%m%dT%H%M%SZ).json"
  sudo tee "$receipt" >/dev/null <<EOF
{
  "prepared_at_utc": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "apply": "yes-prepare-layout-staging",
  "ready_env": "$READY_ENV",
  "marker": "$MARKER",
  "runbook": "$RUNBOOK",
  "containers_untouched": true,
  "daemon_json_untouched": true,
  "live_env_untouched": true
}
EOF
  emit "receipt" "$receipt"
}

require_root_paths
inventory

case "$APPLY" in
  "")
    emit "mode" "inventory"
    emit "next" "Re-run with APPLY=yes-prepare-layout-staging to write staging artifacts only"
    ;;
  yes-prepare-layout-staging)
    stage_artifacts
    emit "mode" "staged"
    emit "containers_untouched" "true"
    ;;
  *)
    echo "{\"error\":\"unknown APPLY (want empty or yes-prepare-layout-staging)\"}" >&2
    exit 1
    ;;
esac

printf '{"summary":"tencent_layout_prep","apply":%s,"staging":%s,"ready_env":%s}\n' \
  "$(json_escape "${APPLY:-inventory}")" \
  "$(json_escape "$STAGING")" \
  "$(json_escape "$READY_ENV")"
