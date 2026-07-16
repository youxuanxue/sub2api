#!/usr/bin/env bash
#
# Stage0 prod blue/green SSM deploy primitive.
#
# Scope:
#   - Prod EC2 only (i-*). Lightsail edges keep using deploy_via_ssm.sh.
#   - Single data layer: postgres/redis/caddy stay on the existing host/network.
#   - Two app colors: tokenkey-blue and tokenkey-green share the same data layer.
#   - First run migrates the legacy single tokenkey container to tokenkey-blue,
#     then deploys the requested tag to the other color.
#
# Cutover model:
#   1. Keep Caddy pointed at the current active color while the target color is
#      pulled, started, migrated, and health-checked.
#   2. Reload Caddy to point at the healthy target color. New requests go to the
#      target; existing streams on the old color continue through Caddy's
#      graceful reload path.
#   3. Send SIGUSR1 to the old color and wait for in-flight streams to drain
#      (bounded/plateaued), then stop/remove the old color.
#
# Failure before Caddy reload leaves the old color untouched and serving.
# Failure after Caddy reload deliberately does not auto-rollback: the target is
# already the live color, and an automated flip-flop would be riskier than a
# deliberate redeploy of the previous tag.

set -euo pipefail

TAG="${1:-${INPUT_TAG:-}}"
INSTANCE_ID="${2:-${INSTANCE_ID:-}}"
COMMENT="${3:-${SSM_COMMENT:-deploy-stage0-bluegreen}}"
TIMEOUT_SECONDS="${STAGE0_SSM_TIMEOUT_SECONDS:-1200}"
EXECUTION_TIMEOUT_SECONDS="${STAGE0_SSM_EXECUTION_TIMEOUT_SECONDS:-$TIMEOUT_SECONDS}"
OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.}"

if [[ -z "${TAG}" ]]; then
  echo "stage0_deploy_via_ssm_bluegreen: tag is required" >&2
  exit 1
fi
if [[ -z "${INSTANCE_ID}" ]]; then
  echo "stage0_deploy_via_ssm_bluegreen: instance id is required" >&2
  exit 1
fi
if [[ "${INSTANCE_ID}" != i-* ]]; then
  echo "stage0_deploy_via_ssm_bluegreen: prod-only primitive requires EC2 instance id (i-*), got ${INSTANCE_ID}" >&2
  exit 1
fi
if [[ ! "${TIMEOUT_SECONDS}" =~ ^[0-9]+$ || ! "${EXECUTION_TIMEOUT_SECONDS}" =~ ^[0-9]+$ ]] \
  || (( TIMEOUT_SECONDS <= 0 || EXECUTION_TIMEOUT_SECONDS <= 0 )); then
  echo "stage0_deploy_via_ssm_bluegreen: timeout values must be positive integers" >&2
  exit 1
fi

ssm_region_args=()
if [[ -n "${AWS_REGION:-${AWS_DEFAULT_REGION:-}}" ]]; then
  ssm_region_args=(--region "${AWS_REGION:-${AWS_DEFAULT_REGION}}")
fi

mkdir -p "${OUTPUT_DIR}"
params_file="${OUTPUT_DIR}/ssm-params.json"
stdout_file="${OUTPUT_DIR}/stdout.txt"
stderr_file="${OUTPUT_DIR}/stderr.txt"
remote_script_file="${OUTPUT_DIR}/bluegreen-remote.sh"

read -r -d '' REMOTE_SCRIPT <<'REMOTE' || true
#!/usr/bin/env bash
set -euo pipefail

TAG="${TAG:?TAG is required}"
ROOT=/var/lib/tokenkey
ENV_FILE="${ROOT}/.env"
BG_COMPOSE="${ROOT}/docker-compose.bluegreen.yml"
ACTIVE_FILE="${ROOT}/active-color"
CADDY_DIR="${ROOT}/caddy"
LIVE_CADDY="${CADDY_DIR}/Caddyfile"

TARGET_CONTAINER=""
CUTOVER_COMMITTED=0
ENV_BACKUP=""

log() {
  echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] $*"
}

die() {
  echo "::error::$*" >&2
  exit 1
}

env_get() {
  sed -n "s/^$1=//p" "${ENV_FILE}" | tail -1
}

env_set() {
  local key="$1" value="$2" escaped
  escaped="$(printf '%s' "${value}" | sed 's/[&|\\]/\\&/g')"
  if grep -q "^${key}=" "${ENV_FILE}"; then
    sudo sed -i "s|^${key}=.*|${key}=${escaped}|" "${ENV_FILE}"
  else
    printf '%s=%s\n' "${key}" "${value}" | sudo tee -a "${ENV_FILE}" >/dev/null
  fi
}

env_default() {
  local key="$1" value="$2"
  if grep -q "^${key}=" "${ENV_FILE}"; then
    log "${key} already present"
  else
    env_set "${key}" "${value}"
    log "ensured ${key}"
  fi
}

backup_env() {
  local phase="$1" ts
  ts="$(date +%Y%m%d-%H%M%S)"
  ENV_BACKUP="${ROOT}/.env.before-bluegreen-${phase}-${TAG}-${ts}"
  sudo cp -a "${ENV_FILE}" "${ENV_BACKUP}"
  log "backed up .env to ${ENV_BACKUP}"
}

restore_env_if_safe() {
  if [[ "${CUTOVER_COMMITTED}" = 0 && -n "${ENV_BACKUP}" && -f "${ENV_BACKUP}" ]]; then
    sudo cp -a "${ENV_BACKUP}" "${ENV_FILE}"
    log "restored .env from ${ENV_BACKUP}"
  fi
}

on_err() {
  local rc=$?
  echo "::warning::blue/green deploy failed (rc=${rc}, cutover_committed=${CUTOVER_COMMITTED})"
  if [[ "${CUTOVER_COMMITTED}" = 0 && -n "${TARGET_CONTAINER}" ]]; then
    sudo docker rm -f "${TARGET_CONTAINER}" >/dev/null 2>&1 || true
    log "removed failed target ${TARGET_CONTAINER}"
  fi
  restore_env_if_safe
  if [[ "${CUTOVER_COMMITTED}" = 1 ]]; then
    echo "::warning::Caddy was already switched to the target color; not auto-rolling back"
  fi
  exit "${rc}"
}
trap on_err ERR

ensure_prod_defaults() {
  [[ -f "${ENV_FILE}" ]] || die "missing ${ENV_FILE}; is this a Stage0 prod host?"

  local api_domain
  api_domain="$(env_get API_DOMAIN)"
  if [[ -z "$(env_get SERVER_FRONTEND_URL)" && -n "${api_domain}" ]]; then
    env_set SERVER_FRONTEND_URL "https://${api_domain}"
    log "ensured SERVER_FRONTEND_URL=https://${api_domain}"
  else
    log "SERVER_FRONTEND_URL already present or API_DOMAIN empty"
  fi

  env_default TOKENKEY_GHCR_KEEP_TAGS 3

  env_default QA_CAPTURE_EXPORT_STORAGE_DRIVER "${QA_CAPTURE_EXPORT_STORAGE_DRIVER:-s3}"
  env_default QA_CAPTURE_EXPORT_STORAGE_REGION "${QA_CAPTURE_EXPORT_STORAGE_REGION:-us-east-1}"
  env_default QA_CAPTURE_EXPORT_STORAGE_BUCKET "${QA_CAPTURE_EXPORT_STORAGE_BUCKET:-tokenkey-prod-qa-exports-682751977094}"
  env_default QA_CAPTURE_EXPORT_STORAGE_PREFIX "${QA_CAPTURE_EXPORT_STORAGE_PREFIX:-traj-exports}"

  env_default MEDIA_STORAGE_DRIVER "${MEDIA_STORAGE_DRIVER:-s3}"
  env_default MEDIA_STORAGE_REGION "${MEDIA_STORAGE_REGION:-us-east-1}"
  env_default MEDIA_STORAGE_BUCKET "${MEDIA_STORAGE_BUCKET:-tokenkey-prod-media-682751977094}"

  env_default GATEWAY_IMAGE_CONCURRENCY_ENABLED "${GATEWAY_IMAGE_CONCURRENCY_ENABLED:-true}"
  env_default GATEWAY_IMAGE_CONCURRENCY_MAX_CONCURRENT_REQUESTS "${GATEWAY_IMAGE_CONCURRENCY_MAX_CONCURRENT_REQUESTS:-8}"
  env_default GATEWAY_IMAGE_CONCURRENCY_OVERFLOW_MODE "${GATEWAY_IMAGE_CONCURRENCY_OVERFLOW_MODE:-reject}"
}

compose_bg() {
  (cd "${ROOT}" && sudo docker compose --project-name tokenkey --env-file .env -f "${BG_COMPOSE}" "$@")
}

color_container() {
  case "$1" in
    blue|green) printf 'tokenkey-%s' "$1" ;;
    *) die "invalid color: $1" ;;
  esac
}

other_color() {
  case "$1" in
    blue) echo green ;;
    green) echo blue ;;
    *) die "invalid active color: $1" ;;
  esac
}

image_repo() {
  local image="$1"
  [[ -n "${image}" && "${image}" == *:* ]] || return 1
  printf '%s' "${image%:*}"
}

container_image() {
  sudo docker inspect "$1" --format '{{.Config.Image}}' 2>/dev/null || true
}

container_health() {
  sudo docker inspect "$1" --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' 2>/dev/null || echo missing
}

wait_healthy() {
  local container="$1" tries="${TOKENKEY_BLUEGREEN_HEALTH_TRIES:-30}" delay="${TOKENKEY_BLUEGREEN_HEALTH_DELAY_SECONDS:-5}" status i
  for i in $(seq 1 "${tries}"); do
    status="$(container_health "${container}")"
    log "health ${container}: ${status} try=${i}/${tries}"
    [[ "${status}" = healthy ]] && return 0
    sleep "${delay}"
  done
  echo "::error::${container} did not reach healthy state"
  sudo docker logs "${container}" --since 3m 2>&1 | tail -80 || true
  return 1
}

wait_ready() {
  local container="$1" tries="${TOKENKEY_BLUEGREEN_READY_TRIES:-18}" delay="${TOKENKEY_BLUEGREEN_READY_DELAY_SECONDS:-5}" body i
  for i in $(seq 1 "${tries}"); do
    if body="$(sudo docker exec "${container}" wget -q -T 5 -O - http://localhost:8080/health 2>/dev/null)"; then
      log "ready ${container}: ${body} try=${i}/${tries}"
      return 0
    fi
    log "ready ${container}: not-ready try=${i}/${tries}"
    sleep "${delay}"
  done
  echo "::error::${container} did not become ready on /health"
  sudo docker logs "${container}" --since 3m 2>&1 | tail -80 || true
  return 1
}

drain_container() {
  local container="$1" status body n d prev=-1 stall=0 i
  status="$(container_health "${container}")"
  log "pre-drain ${container}: health=${status}"
  if [[ "${status}" != healthy ]]; then
    log "pre-drain skipped for ${container}: not healthy"
    return 0
  fi

  sudo docker kill -s USR1 "${container}" >/dev/null 2>&1 || true
  for i in $(seq 1 15); do
    body="$(sudo docker exec "${container}" wget -q -T 3 -O - http://localhost:8080/health/inflight 2>/dev/null || true)"
    n="$(printf '%s' "${body}" | sed -n 's/.*"in_flight":\([0-9]*\).*/\1/p')"
    if printf '%s' "${body}" | grep -q '"draining":true'; then d=true; else d=false; fi
    log "pre-drain ${container}: draining=${d} in_flight=${n:-?} try=${i}/15"
    [[ "${d}" = true && "${n:-1}" = 0 ]] && break
    if [[ -n "${n}" ]]; then
      if [[ "${prev}" -ge 0 && "${n}" -ge "${prev}" ]]; then
        stall=$((stall + 1))
      else
        stall=0
      fi
      prev="${n}"
      if [[ "${stall}" -ge 3 ]]; then
        log "pre-drain ${container}: in_flight plateaued at ${n}; stop waiting"
        break
      fi
    fi
    sleep 2
  done
}

write_bluegreen_compose() {
  local tmp="${BG_COMPOSE}.new"
  sudo tee "${tmp}" >/dev/null <<'YAML'
x-tokenkey-logging: &tokenkey-logging
  driver: json-file
  options:
    max-size: "100m"
    max-file: "5"

services:
  tokenkey-blue:
    image: ${TOKENKEY_IMAGE_BLUE:?TOKENKEY_IMAGE_BLUE is required}
    container_name: tokenkey-blue
    pull_policy: always
    restart: unless-stopped
    logging: *tokenkey-logging
    stop_grace_period: 180s
    ulimits:
      nofile:
        soft: 100000
        hard: 100000
    expose:
      - "8080"
    volumes:
      - /var/lib/tokenkey/app:/app/data
    environment:
      - AUTO_SETUP=true
      - SERVER_HOST=0.0.0.0
      - SERVER_PORT=8080
      - SERVER_MODE=${SERVER_MODE:-release}
      - SERVER_FRONTEND_URL=${SERVER_FRONTEND_URL:-}
      - RUN_MODE=${RUN_MODE:-standard}
      - DATABASE_HOST=tokenkey-postgres
      - DATABASE_PORT=5432
      - DATABASE_USER=${POSTGRES_USER:-tokenkey}
      - DATABASE_PASSWORD=${POSTGRES_PASSWORD:?POSTGRES_PASSWORD is required}
      - DATABASE_DBNAME=${POSTGRES_DB:-tokenkey}
      - DATABASE_SSLMODE=disable
      - DATABASE_MAX_OPEN_CONNS=${DATABASE_MAX_OPEN_CONNS:-50}
      - DATABASE_MAX_IDLE_CONNS=${DATABASE_MAX_IDLE_CONNS:-10}
      - REDIS_HOST=tokenkey-redis
      - REDIS_PORT=6379
      - REDIS_PASSWORD=${REDIS_PASSWORD:-}
      - REDIS_DB=${REDIS_DB:-0}
      - REDIS_POOL_SIZE=${REDIS_POOL_SIZE:-1024}
      - REDIS_MIN_IDLE_CONNS=${REDIS_MIN_IDLE_CONNS:-10}
      - ADMIN_EMAIL=${ADMIN_EMAIL:-admin@tokenkey.local}
      - ADMIN_PASSWORD=${ADMIN_PASSWORD:-}
      - JWT_SECRET=${JWT_SECRET:?JWT_SECRET is required}
      - JWT_EXPIRE_HOUR=${JWT_EXPIRE_HOUR:-1}
      - TOTP_ENCRYPTION_KEY=${TOTP_ENCRYPTION_KEY:?TOTP_ENCRYPTION_KEY is required}
      - TZ=${TZ:-UTC}
      - TOKENKEY_SHUTDOWN_TIMEOUT_SECONDS=${TOKENKEY_SHUTDOWN_TIMEOUT_SECONDS:-150}
      - GATEWAY_SCHEDULING_ANTHROPIC_CONFIG_RECONCILER_CONCURRENCY_MIRROR_ENABLED=${GATEWAY_SCHEDULING_ANTHROPIC_CONFIG_RECONCILER_CONCURRENCY_MIRROR_ENABLED:-true}
      - GATEWAY_SCHEDULING_ANTHROPIC_CONFIG_RECONCILER_BALANCE_FLOOR_ENABLED=${GATEWAY_SCHEDULING_ANTHROPIC_CONFIG_RECONCILER_BALANCE_FLOOR_ENABLED:-false}
      - QA_CAPTURE_EXPORT_STORAGE_DRIVER=${QA_CAPTURE_EXPORT_STORAGE_DRIVER:-}
      - QA_CAPTURE_EXPORT_STORAGE_REGION=${QA_CAPTURE_EXPORT_STORAGE_REGION:-}
      - QA_CAPTURE_EXPORT_STORAGE_BUCKET=${QA_CAPTURE_EXPORT_STORAGE_BUCKET:-}
      - QA_CAPTURE_EXPORT_STORAGE_PREFIX=${QA_CAPTURE_EXPORT_STORAGE_PREFIX:-}
      - MEDIA_STORAGE_DRIVER=${MEDIA_STORAGE_DRIVER:-}
      - MEDIA_STORAGE_REGION=${MEDIA_STORAGE_REGION:-}
      - MEDIA_STORAGE_BUCKET=${MEDIA_STORAGE_BUCKET:-}
      - GATEWAY_IMAGE_CONCURRENCY_ENABLED=${GATEWAY_IMAGE_CONCURRENCY_ENABLED:-}
      - GATEWAY_IMAGE_CONCURRENCY_MAX_CONCURRENT_REQUESTS=${GATEWAY_IMAGE_CONCURRENCY_MAX_CONCURRENT_REQUESTS:-}
      - GATEWAY_IMAGE_CONCURRENCY_OVERFLOW_MODE=${GATEWAY_IMAGE_CONCURRENCY_OVERFLOW_MODE:-}
    networks:
      - tokenkey-network
    healthcheck:
      test: ["CMD", "wget", "-q", "-T", "5", "-O", "/dev/null", "http://localhost:8080/health/live"]
      interval: 10s
      timeout: 5s
      retries: 3
      start_period: 60s

  tokenkey-green:
    image: ${TOKENKEY_IMAGE_GREEN:?TOKENKEY_IMAGE_GREEN is required}
    container_name: tokenkey-green
    pull_policy: always
    restart: unless-stopped
    logging: *tokenkey-logging
    stop_grace_period: 180s
    ulimits:
      nofile:
        soft: 100000
        hard: 100000
    expose:
      - "8080"
    volumes:
      - /var/lib/tokenkey/app:/app/data
    environment:
      - AUTO_SETUP=true
      - SERVER_HOST=0.0.0.0
      - SERVER_PORT=8080
      - SERVER_MODE=${SERVER_MODE:-release}
      - SERVER_FRONTEND_URL=${SERVER_FRONTEND_URL:-}
      - RUN_MODE=${RUN_MODE:-standard}
      - DATABASE_HOST=tokenkey-postgres
      - DATABASE_PORT=5432
      - DATABASE_USER=${POSTGRES_USER:-tokenkey}
      - DATABASE_PASSWORD=${POSTGRES_PASSWORD:?POSTGRES_PASSWORD is required}
      - DATABASE_DBNAME=${POSTGRES_DB:-tokenkey}
      - DATABASE_SSLMODE=disable
      - DATABASE_MAX_OPEN_CONNS=${DATABASE_MAX_OPEN_CONNS:-50}
      - DATABASE_MAX_IDLE_CONNS=${DATABASE_MAX_IDLE_CONNS:-10}
      - REDIS_HOST=tokenkey-redis
      - REDIS_PORT=6379
      - REDIS_PASSWORD=${REDIS_PASSWORD:-}
      - REDIS_DB=${REDIS_DB:-0}
      - REDIS_POOL_SIZE=${REDIS_POOL_SIZE:-1024}
      - REDIS_MIN_IDLE_CONNS=${REDIS_MIN_IDLE_CONNS:-10}
      - ADMIN_EMAIL=${ADMIN_EMAIL:-admin@tokenkey.local}
      - ADMIN_PASSWORD=${ADMIN_PASSWORD:-}
      - JWT_SECRET=${JWT_SECRET:?JWT_SECRET is required}
      - JWT_EXPIRE_HOUR=${JWT_EXPIRE_HOUR:-1}
      - TOTP_ENCRYPTION_KEY=${TOTP_ENCRYPTION_KEY:?TOTP_ENCRYPTION_KEY is required}
      - TZ=${TZ:-UTC}
      - TOKENKEY_SHUTDOWN_TIMEOUT_SECONDS=${TOKENKEY_SHUTDOWN_TIMEOUT_SECONDS:-150}
      - GATEWAY_SCHEDULING_ANTHROPIC_CONFIG_RECONCILER_CONCURRENCY_MIRROR_ENABLED=${GATEWAY_SCHEDULING_ANTHROPIC_CONFIG_RECONCILER_CONCURRENCY_MIRROR_ENABLED:-true}
      - GATEWAY_SCHEDULING_ANTHROPIC_CONFIG_RECONCILER_BALANCE_FLOOR_ENABLED=${GATEWAY_SCHEDULING_ANTHROPIC_CONFIG_RECONCILER_BALANCE_FLOOR_ENABLED:-false}
      - QA_CAPTURE_EXPORT_STORAGE_DRIVER=${QA_CAPTURE_EXPORT_STORAGE_DRIVER:-}
      - QA_CAPTURE_EXPORT_STORAGE_REGION=${QA_CAPTURE_EXPORT_STORAGE_REGION:-}
      - QA_CAPTURE_EXPORT_STORAGE_BUCKET=${QA_CAPTURE_EXPORT_STORAGE_BUCKET:-}
      - QA_CAPTURE_EXPORT_STORAGE_PREFIX=${QA_CAPTURE_EXPORT_STORAGE_PREFIX:-}
      - MEDIA_STORAGE_DRIVER=${MEDIA_STORAGE_DRIVER:-}
      - MEDIA_STORAGE_REGION=${MEDIA_STORAGE_REGION:-}
      - MEDIA_STORAGE_BUCKET=${MEDIA_STORAGE_BUCKET:-}
      - GATEWAY_IMAGE_CONCURRENCY_ENABLED=${GATEWAY_IMAGE_CONCURRENCY_ENABLED:-}
      - GATEWAY_IMAGE_CONCURRENCY_MAX_CONCURRENT_REQUESTS=${GATEWAY_IMAGE_CONCURRENCY_MAX_CONCURRENT_REQUESTS:-}
      - GATEWAY_IMAGE_CONCURRENCY_OVERFLOW_MODE=${GATEWAY_IMAGE_CONCURRENCY_OVERFLOW_MODE:-}
    networks:
      - tokenkey-network
    healthcheck:
      test: ["CMD", "wget", "-q", "-T", "5", "-O", "/dev/null", "http://localhost:8080/health/live"]
      interval: 10s
      timeout: 5s
      retries: 3
      start_period: 60s

networks:
  tokenkey-network:
    external: true
    name: tokenkey_tokenkey-network
YAML
  sudo mv "${tmp}" "${BG_COMPOSE}"
  log "wrote ${BG_COMPOSE}"
}

install_bluegreen_systemd_unit() {
  sudo tee /usr/local/bin/tokenkey-bluegreen-systemd-start.sh >/dev/null <<'SH'
#!/bin/bash
set -euo pipefail
ROOT=/var/lib/tokenkey
ACTIVE=blue
if [ -r "${ROOT}/active-color" ]; then
  ACTIVE="$(sed -n '1p' "${ROOT}/active-color" | tr -d '[:space:]')"
fi
case "${ACTIVE}" in blue|green) ;; *) ACTIVE=blue ;; esac
cd "${ROOT}"
docker compose --env-file "${ROOT}/.env" up -d --no-deps postgres redis
docker compose --project-name tokenkey --env-file "${ROOT}/.env" -f "${ROOT}/docker-compose.bluegreen.yml" up -d --no-deps "tokenkey-${ACTIVE}"
docker compose --env-file "${ROOT}/.env" up -d --no-deps caddy
docker rm -f tokenkey >/dev/null 2>&1 || true
SH
  sudo tee /usr/local/bin/tokenkey-bluegreen-systemd-stop.sh >/dev/null <<'SH'
#!/bin/bash
set +e
ROOT=/var/lib/tokenkey
cd "${ROOT}" || exit 0
docker compose --project-name tokenkey --env-file "${ROOT}/.env" -f "${ROOT}/docker-compose.bluegreen.yml" stop -t 180 tokenkey-blue tokenkey-green
docker compose --env-file "${ROOT}/.env" stop -t 60 caddy
docker compose --env-file "${ROOT}/.env" stop -t 60 postgres redis
exit 0
SH
  sudo chmod 0755 /usr/local/bin/tokenkey-bluegreen-systemd-start.sh /usr/local/bin/tokenkey-bluegreen-systemd-stop.sh

  sudo tee /etc/systemd/system/tokenkey.service >/dev/null <<'UNIT'
[Unit]
Description=tokenkey stack (docker compose, prod blue/green)
Requires=docker.service
After=docker.service network-online.target
Wants=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
WorkingDirectory=/var/lib/tokenkey
ExecStart=/usr/local/bin/tokenkey-bluegreen-systemd-start.sh
ExecStartPost=-/usr/local/bin/tokenkey-prune-ghcr-app-tags.sh
ExecStop=/usr/local/bin/tokenkey-bluegreen-systemd-stop.sh
TimeoutStartSec=0
TimeoutStopSec=240

[Install]
WantedBy=multi-user.target
UNIT
  sudo systemctl daemon-reload
  sudo systemctl enable tokenkey.service >/dev/null
  log "installed blue/green tokenkey.service restart policy"
}

render_caddy_with_upstream() {
  local upstream="$1" tmp="$2"
  sudo awk -v upstream="${upstream}" '
    /^[[:space:]]*reverse_proxy[[:space:]]+/ && $0 ~ /\{[[:space:]]*$/ {
      count += 1
      if (count == 1) {
        match($0, /[^[:space:]]/)
        indent = RSTART > 1 ? substr($0, 1, RSTART - 1) : ""
        print indent "reverse_proxy " upstream " {"
      } else {
        print
      }
      next
    }
    { print }
    END { if (count != 1) exit 7 }
  ' "${LIVE_CADDY}" | sudo tee "${tmp}" >/dev/null
}

write_caddy_for_color() {
  local color="$1" upstream tmp backup ts
  upstream="$(color_container "${color}"):8080"
  [[ -f "${LIVE_CADDY}" ]] || die "missing live Caddyfile at ${LIVE_CADDY}"

  tmp="${CADDY_DIR}/Caddyfile.bluegreen-${color}.new"
  ts="$(date +%Y%m%d-%H%M%S)"
  backup="${CADDY_DIR}/Caddyfile.before-bluegreen-${color}-${ts}"

  if ! render_caddy_with_upstream "${upstream}" "${tmp}"; then
    sudo rm -f "${tmp}" >/dev/null 2>&1 || true
    die "could not rewrite exactly one reverse_proxy upstream in ${LIVE_CADDY}"
  fi

  sudo docker run --rm -v "${tmp}:/tmp/Caddyfile:ro" caddy:2-alpine caddy validate --config /tmp/Caddyfile --adapter caddyfile
  sudo cp -a "${LIVE_CADDY}" "${backup}"
  sudo sh -c "cat '${tmp}' > '${LIVE_CADDY}'"
  sudo rm -f "${tmp}"
  if ! sudo docker exec tokenkey-caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile; then
    echo "::warning::caddy reload failed; restoring previous Caddyfile"
    sudo sh -c "cat '${backup}' > '${LIVE_CADDY}'"
    sudo docker exec tokenkey-caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile || true
    return 1
  fi
  log "caddy now routes new requests to ${upstream} (backup=${backup})"
}

write_active_color() {
  local color="$1" tmp="${ACTIVE_FILE}.new"
  printf '%s\n' "${color}" | sudo tee "${tmp}" >/dev/null
  sudo mv "${tmp}" "${ACTIVE_FILE}"
  log "active-color=${color}"
}

read_active_color() {
  if [[ -r "${ACTIVE_FILE}" ]]; then
    sed -n '1p' "${ACTIVE_FILE}" | tr -d '[:space:]'
  fi
}

ensure_legacy_cutover() {
  local active legacy_img blue_img
  active="$(read_active_color || true)"
  if [[ "${active}" =~ ^(blue|green)$ ]]; then
    log "blue/green already initialized: active=${active}"
    return 0
  fi

  if ! sudo docker inspect tokenkey >/dev/null 2>&1; then
    die "no active-color and legacy tokenkey container missing; manual recovery required"
  fi

  log "initializing blue/green layout from legacy tokenkey container"
  legacy_img="$(container_image tokenkey)"
  blue_img="${legacy_img:-$(env_get TOKENKEY_IMAGE)}"
  [[ -n "${blue_img}" ]] || die "could not derive legacy tokenkey image"

  backup_env legacy
  env_set TOKENKEY_IMAGE_BLUE "${blue_img}"
  env_set TOKENKEY_IMAGE_GREEN "${blue_img}"
  env_set TOKENKEY_IMAGE "${blue_img}"
  write_bluegreen_compose

  TARGET_CONTAINER=tokenkey-blue
  compose_bg up -d --no-deps --force-recreate tokenkey-blue
  wait_healthy tokenkey-blue
  wait_ready tokenkey-blue

  write_caddy_for_color blue
  CUTOVER_COMMITTED=1
  write_active_color blue
  install_bluegreen_systemd_unit

  drain_container tokenkey
  sudo docker stop -t 30 tokenkey >/dev/null 2>&1 || true
  sudo docker rm -f tokenkey >/dev/null 2>&1 || true
  log "legacy tokenkey container removed after cutover to blue"

  TARGET_CONTAINER=""
  CUTOVER_COMMITTED=0
  backup_env active-blue
}

deploy_target_color() {
  local active target active_container active_img repo new_img target_container active_file_value
  active_file_value="$(read_active_color || true)"
  [[ "${active_file_value}" =~ ^(blue|green)$ ]] || die "invalid or missing active color: ${active_file_value:-<empty>}"
  active="${active_file_value}"
  target="$(other_color "${active}")"
  active_container="$(color_container "${active}")"
  target_container="$(color_container "${target}")"

  active_img="$(container_image "${active_container}")"
  if ! repo="$(image_repo "${active_img}")"; then
    if ! repo="$(image_repo "$(env_get TOKENKEY_IMAGE)")"; then
      die "could not derive image repo from active image (${active_img}) or TOKENKEY_IMAGE"
    fi
  fi
  new_img="${repo}:${TAG}"

  log "deploy target=${target} image=${new_img} active=${active} active_image=${active_img:-unknown}"
  TARGET_CONTAINER="${target_container}"
  CUTOVER_COMMITTED=0
  backup_env "target-${target}"
  env_set "TOKENKEY_IMAGE_$(printf '%s' "${target}" | tr '[:lower:]' '[:upper:]')" "${new_img}"
  env_set TOKENKEY_IMAGE "${new_img}"
  write_bluegreen_compose

  compose_bg pull "${target_container}"
  compose_bg up -d --no-deps --force-recreate "${target_container}"
  wait_healthy "${target_container}"
  wait_ready "${target_container}"

  write_caddy_for_color "${target}"
  CUTOVER_COMMITTED=1
  write_active_color "${target}"
  install_bluegreen_systemd_unit

  drain_container "${active_container}"
  sudo docker stop -t 30 "${active_container}" >/dev/null 2>&1 || true
  sudo docker rm -f "${active_container}" >/dev/null 2>&1 || true
  log "stopped previous color ${active_container}"

  TARGET_CONTAINER=""
}

prune_images() {
  log "prune stale ghcr image tags (non-fatal)"
  local prune=/usr/local/bin/tokenkey-prune-ghcr-app-tags-core.sh
  [[ -x "${prune}" ]] || prune=/usr/local/bin/tokenkey-prune-ghcr-app-tags.sh
  if [[ -x "${prune}" ]]; then
    sudo env TOKENKEY_GHCR_KEEP_TAGS="$(env_get TOKENKEY_GHCR_KEEP_TAGS || echo 3)" "${prune}" || echo "::warning::ghcr prune failed (non-fatal)"
  else
    log "no ghcr prune script on box; skipping"
  fi
  sudo docker image prune -f >/dev/null 2>&1 || true
}

log "=== blue/green deploy tag=${TAG} ==="
ensure_prod_defaults
ensure_legacy_cutover
deploy_target_color
trap - ERR
prune_images
log "=== blue/green deploy done ==="
compose_bg ps
active="$(read_active_color)"
active_container="$(color_container "${active}")"
sudo docker logs "${active_container}" --since 2m 2>&1 | tail -30 || true
REMOTE

printf '%s\n' "${REMOTE_SCRIPT}" > "${remote_script_file}"

REMOTE_B64="$(printf '%s' "${REMOTE_SCRIPT}" | base64 | tr -d '\n')"
chunks_json="$(printf '%s' "${REMOTE_B64}" | fold -w 1000 | jq -R -s 'split("\n") | map(select(length > 0))')"

jq -n \
  --arg tag "${TAG}" \
  --arg execution_timeout "${EXECUTION_TIMEOUT_SECONDS}" \
  --arg qa_driver "${QA_CAPTURE_EXPORT_STORAGE_DRIVER:-s3}" \
  --arg qa_region "${QA_CAPTURE_EXPORT_STORAGE_REGION:-us-east-1}" \
  --arg qa_bucket "${QA_CAPTURE_EXPORT_STORAGE_BUCKET:-tokenkey-prod-qa-exports-682751977094}" \
  --arg qa_prefix "${QA_CAPTURE_EXPORT_STORAGE_PREFIX:-traj-exports}" \
  --arg media_driver "${MEDIA_STORAGE_DRIVER:-s3}" \
  --arg media_region "${MEDIA_STORAGE_REGION:-us-east-1}" \
  --arg media_bucket "${MEDIA_STORAGE_BUCKET:-tokenkey-prod-media-682751977094}" \
  --arg image_enabled "${GATEWAY_IMAGE_CONCURRENCY_ENABLED:-true}" \
  --arg image_max "${GATEWAY_IMAGE_CONCURRENCY_MAX_CONCURRENT_REQUESTS:-8}" \
  --arg image_overflow "${GATEWAY_IMAGE_CONCURRENCY_OVERFLOW_MODE:-reject}" \
  --argjson chunks "${chunks_json}" '{
  commands: ([
    "set -euo pipefail",
    "rm -f /tmp/tokenkey-bluegreen-deploy.b64 /tmp/tokenkey-bluegreen-deploy.sh"
  ] + ($chunks | map("printf %s " + (. | @sh) + " >> /tmp/tokenkey-bluegreen-deploy.b64")) + [
    "base64 -d /tmp/tokenkey-bluegreen-deploy.b64 > /tmp/tokenkey-bluegreen-deploy.sh",
    "chmod 700 /tmp/tokenkey-bluegreen-deploy.sh",
    (
      "TAG=" + ($tag|@sh)
      + " QA_CAPTURE_EXPORT_STORAGE_DRIVER=" + ($qa_driver|@sh)
      + " QA_CAPTURE_EXPORT_STORAGE_REGION=" + ($qa_region|@sh)
      + " QA_CAPTURE_EXPORT_STORAGE_BUCKET=" + ($qa_bucket|@sh)
      + " QA_CAPTURE_EXPORT_STORAGE_PREFIX=" + ($qa_prefix|@sh)
      + " MEDIA_STORAGE_DRIVER=" + ($media_driver|@sh)
      + " MEDIA_STORAGE_REGION=" + ($media_region|@sh)
      + " MEDIA_STORAGE_BUCKET=" + ($media_bucket|@sh)
      + " GATEWAY_IMAGE_CONCURRENCY_ENABLED=" + ($image_enabled|@sh)
      + " GATEWAY_IMAGE_CONCURRENCY_MAX_CONCURRENT_REQUESTS=" + ($image_max|@sh)
      + " GATEWAY_IMAGE_CONCURRENCY_OVERFLOW_MODE=" + ($image_overflow|@sh)
      + " /tmp/tokenkey-bluegreen-deploy.sh"
    )
  ]),
  executionTimeout: [$execution_timeout]
}' > "${params_file}"

if [[ -n "${STAGE0_RENDER_ONLY:-}" ]]; then
  echo "stage0_deploy_via_ssm_bluegreen: STAGE0_RENDER_ONLY set; wrote ${params_file} and ${remote_script_file}; exiting" >&2
  exit 0
fi

cmd_id="$(aws "${ssm_region_args[@]}" ssm send-command \
  --instance-ids "${INSTANCE_ID}" \
  --document-name AWS-RunShellScript \
  --comment "${COMMENT} tag=${TAG}" \
  --parameters "file://${params_file}" \
  --query 'Command.CommandId' --output text)"

echo "ssm command-id=${cmd_id}"
if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  echo "command_id=${cmd_id}" >> "${GITHUB_OUTPUT}"
fi

deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
status="InProgress"
while true; do
  status="$(aws "${ssm_region_args[@]}" ssm get-command-invocation \
    --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
    --query 'Status' --output text 2>/dev/null || echo InProgress)"
  case "${status}" in
    Success|Failed|TimedOut|Cancelled) break ;;
  esac
  if [[ $(date +%s) -ge ${deadline} ]]; then
    echo "::error::ssm timeout" >&2
    status="TimedOut"
    break
  fi
  sleep 5
done

aws "${ssm_region_args[@]}" ssm get-command-invocation \
  --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
  --query 'StandardOutputContent' --output text > "${stdout_file}"
aws "${ssm_region_args[@]}" ssm get-command-invocation \
  --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
  --query 'StandardErrorContent' --output text > "${stderr_file}"

echo '--- ssm stdout (last 12KB) ---'
tail -c 12288 "${stdout_file}"
echo
echo '--- ssm stderr (last 12KB) ---'
tail -c 12288 "${stderr_file}"
echo

if [[ "${status}" != "Success" ]]; then
  echo "::error::ssm command status=${status}" >&2
  exit 1
fi
