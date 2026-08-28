#!/usr/bin/env bash
#
# Stage0 prod/Edge blue/green SSM deploy primitive.
#
# Scope:
#   - Prod EC2 (i-*) and Lightsail Edge managed instances (mi-*).
#   - Single data layer: postgres/redis/caddy stay on the existing host/network.
#   - Two app colors: tokenkey-blue and tokenkey-green share the same data layer.
#   - First run migrates the requested tag directly from legacy tokenkey to blue.
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
case "${INSTANCE_ID}" in
  i-*) DEPLOY_PROFILE=prod ;;
  mi-*) DEPLOY_PROFILE=edge ;;
  *)
    echo "stage0_deploy_via_ssm_bluegreen: requires EC2 i-* or managed-instance mi-*, got ${INSTANCE_ID}" >&2
    exit 1
    ;;
esac
if [[ ! "${TIMEOUT_SECONDS}" =~ ^[0-9]+$ || ! "${EXECUTION_TIMEOUT_SECONDS}" =~ ^[0-9]+$ ]] \
  || (( TIMEOUT_SECONDS <= 0 || EXECUTION_TIMEOUT_SECONDS <= 0 )); then
  echo "stage0_deploy_via_ssm_bluegreen: timeout values must be positive integers" >&2
  exit 1
fi
if [[ "${DEPLOY_PROFILE}" = prod && "${QA_BUNDLE_ENABLED-}" = true ]]; then
  for key in QA_BUNDLE_QUEUE_URL QA_BUNDLE_STORAGE_DRIVER QA_BUNDLE_STORAGE_REGION QA_BUNDLE_STORAGE_BUCKET QA_BUNDLE_STORAGE_PREFIX; do
    if [[ -z "${!key-}" ]]; then
      echo "stage0_deploy_via_ssm_bluegreen: ${key} is required when QA_BUNDLE_ENABLED=true" >&2
      exit 1
    fi
  done
fi

CAPACITY_POLICY="$(cd "$(dirname "$0")" && pwd)/bluegreen-capacity-policy.env"
# shellcheck source=bluegreen-capacity-policy.env
source "${CAPACITY_POLICY}"
for key in EDGE_MIN_MEM_AVAILABLE_BYTES EDGE_ACTIVE_APP_HEADROOM_BYTES EDGE_MIN_ROOT_DISK_AVAILABLE_BYTES; do
  if [[ ! "${!key:-}" =~ ^[0-9]+$ ]]; then
    echo "stage0_deploy_via_ssm_bluegreen: ${key} missing or invalid in ${CAPACITY_POLICY}" >&2
    exit 1
  fi
done

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
DEPLOY_PROFILE="${DEPLOY_PROFILE:?DEPLOY_PROFILE is required}"
ROOT=/var/lib/tokenkey
ENV_FILE="${ROOT}/.env"
BG_COMPOSE="${ROOT}/docker-compose.bluegreen.yml"
ACTIVE_FILE="${ROOT}/active-color"
CADDY_DIR="${ROOT}/caddy"
LIVE_CADDY="${CADDY_DIR}/Caddyfile"

TARGET_CONTAINER=""
CUTOVER_COMMITTED=0
CUTOVER_AT=""
ENV_BACKUP=""
LEGACY_MIGRATED=0
ROUTE_SWITCHED=0

log() {
  echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] $*"
}

record_cutover() {
  local cutover_tmp
  CUTOVER_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  cutover_tmp="${ROOT}/.last-cutover-at.$$"
  printf '%s\n' "${CUTOVER_AT}" | sudo tee "${cutover_tmp}" >/dev/null
  sudo chmod 0644 "${cutover_tmp}"
  sudo mv "${cutover_tmp}" "${ROOT}/last-cutover-at"
  echo "tk_stage0_cutover: timestamp=${CUTOVER_AT}"
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

env_apply_if_supplied() {
  local key="$1" desired="$2" supplied="$3"
  if [[ "${supplied}" = true ]]; then
    env_set "${key}" "${desired}"
    log "applied desired ${key}"
  else
    log "${key} not supplied; preserving existing host value"
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
  if [[ "${CUTOVER_COMMITTED}" = 0 && "${ROUTE_SWITCHED}" = 0 && -n "${ENV_BACKUP}" && -f "${ENV_BACKUP}" ]]; then
    sudo cp -a "${ENV_BACKUP}" "${ENV_FILE}"
    log "restored .env from ${ENV_BACKUP}"
  fi
}

cleanup_on_exit() {
  local rc=$?
  trap - EXIT
  [[ "${rc}" -ne 0 ]] || return 0
  if [[ "${ROUTE_SWITCHED}" = 1 && "${CUTOVER_COMMITTED}" = 0 ]]; then
    CUTOVER_COMMITTED=1
    echo "::error::route switched without durable commit; preserving both colors for explicit recovery"
  fi
  echo "::warning::blue/green deploy failed (rc=${rc}, cutover_committed=${CUTOVER_COMMITTED})"
  if [[ "${CUTOVER_COMMITTED}" = 0 && -n "${TARGET_CONTAINER}" ]]; then
    sudo docker logs "${TARGET_CONTAINER}" --since 3m 2>&1 | tail -80 || true
    sudo docker rm -f "${TARGET_CONTAINER}" >/dev/null 2>&1 || true
    log "removed failed target ${TARGET_CONTAINER}; active color remains untouched"
  fi
  restore_env_if_safe
  if [[ "${CUTOVER_COMMITTED}" = 1 ]]; then
    echo "::warning::Caddy was already switched to the target color; not auto-rolling back"
  fi
  exit "${rc}"
}
trap cleanup_on_exit EXIT

clear_edge_profile_overrides() {
  [[ "${DEPLOY_PROFILE}" = edge ]] || return 0
  unset \
    QA_BUNDLE_ENABLED QA_BUNDLE_ENABLED_SET \
    QA_BUNDLE_QUEUE_URL QA_BUNDLE_QUEUE_URL_SET \
    QA_BUNDLE_STORAGE_DRIVER QA_BUNDLE_STORAGE_DRIVER_SET \
    QA_BUNDLE_STORAGE_REGION QA_BUNDLE_STORAGE_REGION_SET \
    QA_BUNDLE_STORAGE_BUCKET QA_BUNDLE_STORAGE_BUCKET_SET \
    QA_BUNDLE_STORAGE_PREFIX QA_BUNDLE_STORAGE_PREFIX_SET \
    QA_ARCHIVE_ENABLED QA_ARCHIVE_STORAGE_DRIVER QA_ARCHIVE_STORAGE_REGION \
    QA_ARCHIVE_STORAGE_BUCKET QA_ARCHIVE_STORAGE_PREFIX \
    TELEMETRY_ARCHIVE_ENABLED TELEMETRY_ARCHIVE_REGION TELEMETRY_ARCHIVE_BUCKET \
    TELEMETRY_ARCHIVE_PREFIX TELEMETRY_ARCHIVE_QUEUE_SIZE \
    TELEMETRY_ARCHIVE_QUEUE_MAX_BYTES TELEMETRY_ARCHIVE_MAX_EVENT_BYTES \
    TELEMETRY_ARCHIVE_BATCH_SIZE TELEMETRY_ARCHIVE_WORKER_COUNT \
    TELEMETRY_ARCHIVE_FLUSH_INTERVAL_SECONDS TELEMETRY_ARCHIVE_PUT_TIMEOUT_SECONDS \
    MEDIA_STORAGE_DRIVER MEDIA_STORAGE_REGION MEDIA_STORAGE_BUCKET \
    GATEWAY_IMAGE_CONCURRENCY_ENABLED GATEWAY_IMAGE_CONCURRENCY_MAX_CONCURRENT_REQUESTS \
    GATEWAY_IMAGE_CONCURRENCY_OVERFLOW_MODE
}

ensure_profile_environment() {
  [[ -f "${ENV_FILE}" ]] || die "missing ${ENV_FILE}; is this a Stage0 host?"
  clear_edge_profile_overrides

  local api_domain
  api_domain="$(env_get API_DOMAIN)"
  if [[ -z "$(env_get SERVER_FRONTEND_URL)" && -n "${api_domain}" ]]; then
    env_set SERVER_FRONTEND_URL "https://${api_domain}"
    log "ensured SERVER_FRONTEND_URL=https://${api_domain}"
  else
    log "SERVER_FRONTEND_URL already present or API_DOMAIN empty"
  fi

  env_default TOKENKEY_GHCR_KEEP_TAGS 3

  if [[ "${DEPLOY_PROFILE}" = edge ]]; then
    log "edge profile: preserving existing QA/archive/media/concurrency host values"
    return 0
  fi

  env_apply_if_supplied QA_BUNDLE_ENABLED "${QA_BUNDLE_ENABLED:-}" "${QA_BUNDLE_ENABLED_SET:-false}"
  env_apply_if_supplied QA_BUNDLE_QUEUE_URL "${QA_BUNDLE_QUEUE_URL:-}" "${QA_BUNDLE_QUEUE_URL_SET:-false}"
  env_apply_if_supplied QA_BUNDLE_STORAGE_DRIVER "${QA_BUNDLE_STORAGE_DRIVER:-}" "${QA_BUNDLE_STORAGE_DRIVER_SET:-false}"
  env_apply_if_supplied QA_BUNDLE_STORAGE_REGION "${QA_BUNDLE_STORAGE_REGION:-}" "${QA_BUNDLE_STORAGE_REGION_SET:-false}"
  env_apply_if_supplied QA_BUNDLE_STORAGE_BUCKET "${QA_BUNDLE_STORAGE_BUCKET:-}" "${QA_BUNDLE_STORAGE_BUCKET_SET:-false}"
  env_apply_if_supplied QA_BUNDLE_STORAGE_PREFIX "${QA_BUNDLE_STORAGE_PREFIX:-}" "${QA_BUNDLE_STORAGE_PREFIX_SET:-false}"
  if [[ "$(env_get QA_BUNDLE_ENABLED)" = true ]]; then
    for key in QA_BUNDLE_QUEUE_URL QA_BUNDLE_STORAGE_DRIVER QA_BUNDLE_STORAGE_REGION QA_BUNDLE_STORAGE_BUCKET QA_BUNDLE_STORAGE_PREFIX; do
      [[ -n "$(env_get "${key}")" ]] || die "${key} is required when QA_BUNDLE_ENABLED=true"
    done
  fi

  # Target: ops/qa/policy.yaml prod.archive.enabled. Rollout gate default true
  # after Phase 2 recovery closeout: ops/qa/deploy_rollout.yaml (SSOT).
  env_default QA_ARCHIVE_ENABLED "${QA_ARCHIVE_ENABLED:-true}"
  env_default QA_ARCHIVE_STORAGE_DRIVER "${QA_ARCHIVE_STORAGE_DRIVER:-s3}"
  env_default QA_ARCHIVE_STORAGE_REGION "${QA_ARCHIVE_STORAGE_REGION:-us-east-1}"
  env_default QA_ARCHIVE_STORAGE_BUCKET "${QA_ARCHIVE_STORAGE_BUCKET:-tokenkey-prod-qa-raw-archive-682751977094}"
  env_default QA_ARCHIVE_STORAGE_PREFIX "${QA_ARCHIVE_STORAGE_PREFIX:-raw/v1}"

  env_default TELEMETRY_ARCHIVE_ENABLED false
  env_default TELEMETRY_ARCHIVE_REGION "${TELEMETRY_ARCHIVE_REGION:-us-east-1}"
  env_default TELEMETRY_ARCHIVE_BUCKET "${TELEMETRY_ARCHIVE_BUCKET:-tokenkey-prod-archive-682751977094}"
  env_default TELEMETRY_ARCHIVE_PREFIX "${TELEMETRY_ARCHIVE_PREFIX:-prod/raw-telemetry}"
  env_default TELEMETRY_ARCHIVE_QUEUE_SIZE "${TELEMETRY_ARCHIVE_QUEUE_SIZE:-8192}"
  env_default TELEMETRY_ARCHIVE_QUEUE_MAX_BYTES "${TELEMETRY_ARCHIVE_QUEUE_MAX_BYTES:-33554432}"
  env_default TELEMETRY_ARCHIVE_MAX_EVENT_BYTES "${TELEMETRY_ARCHIVE_MAX_EVENT_BYTES:-1048576}"
  env_default TELEMETRY_ARCHIVE_BATCH_SIZE "${TELEMETRY_ARCHIVE_BATCH_SIZE:-256}"
  env_default TELEMETRY_ARCHIVE_WORKER_COUNT "${TELEMETRY_ARCHIVE_WORKER_COUNT:-4}"
  env_default TELEMETRY_ARCHIVE_FLUSH_INTERVAL_SECONDS "${TELEMETRY_ARCHIVE_FLUSH_INTERVAL_SECONDS:-5}"
  env_default TELEMETRY_ARCHIVE_PUT_TIMEOUT_SECONDS "${TELEMETRY_ARCHIVE_PUT_TIMEOUT_SECONDS:-10}"

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
  sudo docker inspect "$1" --format '{{if or (eq .State.Status "exited") (eq .State.Status "dead")}}{{.State.Status}}{{else if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' 2>/dev/null || echo missing
}

wait_healthy() {
  local container="$1" tries="${TOKENKEY_BLUEGREEN_HEALTH_TRIES:-60}" delay="${TOKENKEY_BLUEGREEN_HEALTH_DELAY_SECONDS:-5}"
  local unhealthy_limit="${TOKENKEY_BLUEGREEN_UNHEALTHY_LIMIT:-3}" unhealthy_streak=0 status i
  for i in $(seq 1 "${tries}"); do
    status="$(container_health "${container}")"
    log "health ${container}: ${status} try=${i}/${tries} unhealthy_streak=${unhealthy_streak}/${unhealthy_limit}"
    case "${status}" in
      healthy)
        return 0
        ;;
      exited|dead)
        echo "::error::${container} entered terminal state ${status}; failing health wait immediately"
        sudo docker logs "${container}" --since 3m 2>&1 | tail -80 || true
        return 1
        ;;
      unhealthy)
        unhealthy_streak=$((unhealthy_streak + 1))
        if [[ "${unhealthy_streak}" -ge "${unhealthy_limit}" ]]; then
          echo "::error::${container} remained unhealthy for ${unhealthy_streak} consecutive checks"
          sudo docker logs "${container}" --since 3m 2>&1 | tail -80 || true
          return 1
        fi
        ;;
      *)
        unhealthy_streak=0
        ;;
    esac
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

bytes_from_docker_mem() {
  local raw="$1" number unit multiplier
  raw="${raw// /}"
  number="$(printf '%s' "${raw}" | sed -nE 's/^([0-9]+([.][0-9]+)?).*/\1/p')"
  unit="${raw#"${number}"}"
  case "${unit}" in
    B|'') multiplier=1 ;;
    kB|KB) multiplier=1000 ;;
    KiB) multiplier=1024 ;;
    MB) multiplier=1000000 ;;
    MiB) multiplier=1048576 ;;
    GB) multiplier=1000000000 ;;
    GiB) multiplier=1073741824 ;;
    *) return 1 ;;
  esac
  [[ -n "${number}" ]] || return 1
  awk -v number="${number}" -v multiplier="${multiplier}" 'BEGIN { printf "%.0f\n", number * multiplier }'
}

admit_edge_candidate() {
  local active_container="$1" mem_available_bytes active_usage active_working_set_bytes
  local memory_floor_bytes="${EDGE_MIN_MEM_AVAILABLE_BYTES:?}" memory_required_bytes
  local memory_headroom_bytes="${EDGE_ACTIVE_APP_HEADROOM_BYTES:?}"
  local disk_floor_bytes="${EDGE_MIN_ROOT_DISK_AVAILABLE_BYTES:?}" disk_available_bytes
  [[ "${DEPLOY_PROFILE}" = edge ]] || return 0

  mem_available_bytes="$(awk '/^MemAvailable:/ {printf "%.0f\n", $2 * 1024; exit}' /proc/meminfo)"
  active_usage="$(sudo docker stats --no-stream --format '{{.MemUsage}}' "${active_container}" | cut -d/ -f1)"
  active_working_set_bytes="$(bytes_from_docker_mem "${active_usage}")" || die "could not parse active app working set: ${active_usage:-<empty>}"
  memory_required_bytes=$((active_working_set_bytes + memory_headroom_bytes))
  if (( memory_required_bytes < memory_floor_bytes )); then memory_required_bytes=${memory_floor_bytes}; fi
  disk_available_bytes="$(df -B1 --output=avail / | awk 'NR==2 {print $1}')"

  # Hard contract comes from ops/stage0/bluegreen-capacity-policy.env.
  log "edge admission: mem_available_bytes=${mem_available_bytes:-invalid} active_working_set_bytes=${active_working_set_bytes} memory_required_bytes=${memory_required_bytes} disk_available_bytes=${disk_available_bytes:-invalid}"
  log "edge admission audit: swap_free_kib=$(awk '/^SwapFree:/ {print $2; exit}' /proc/meminfo) load=$(cut -d' ' -f1-3 /proc/loadavg) recent_oom=$(sudo dmesg --since '24 hours ago' 2>/dev/null | grep -ciE 'out of memory|oom-kill' || true)"
  [[ "${mem_available_bytes}" =~ ^[0-9]+$ ]] || die "invalid MemAvailable"
  [[ "${disk_available_bytes}" =~ ^[0-9]+$ ]] || die "invalid root filesystem availability"
  (( mem_available_bytes >= memory_required_bytes )) || die "Edge memory admission failed: available=${mem_available_bytes} required=${memory_required_bytes}"
  (( disk_available_bytes >= disk_floor_bytes )) || die "Edge disk admission failed: available=${disk_available_bytes} required=${disk_floor_bytes}"
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
      - SKIP_DATA_CHOWN=1
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
      - QA_BUNDLE_ENABLED=${QA_BUNDLE_ENABLED:-}
      - QA_BUNDLE_QUEUE_URL=${QA_BUNDLE_QUEUE_URL:-}
      - QA_BUNDLE_STORAGE_DRIVER=${QA_BUNDLE_STORAGE_DRIVER:-}
      - QA_BUNDLE_STORAGE_REGION=${QA_BUNDLE_STORAGE_REGION:-}
      - QA_BUNDLE_STORAGE_BUCKET=${QA_BUNDLE_STORAGE_BUCKET:-}
      - QA_BUNDLE_STORAGE_PREFIX=${QA_BUNDLE_STORAGE_PREFIX:-}
      - QA_ARCHIVE_ENABLED=${QA_ARCHIVE_ENABLED:-}
      - QA_ARCHIVE_STORAGE_DRIVER=${QA_ARCHIVE_STORAGE_DRIVER:-}
      - QA_ARCHIVE_STORAGE_REGION=${QA_ARCHIVE_STORAGE_REGION:-}
      - QA_ARCHIVE_STORAGE_BUCKET=${QA_ARCHIVE_STORAGE_BUCKET:-}
      - QA_ARCHIVE_STORAGE_PREFIX=${QA_ARCHIVE_STORAGE_PREFIX:-}
      - TELEMETRY_ARCHIVE_ENABLED=${TELEMETRY_ARCHIVE_ENABLED:-}
      - TELEMETRY_ARCHIVE_REGION=${TELEMETRY_ARCHIVE_REGION:-}
      - TELEMETRY_ARCHIVE_BUCKET=${TELEMETRY_ARCHIVE_BUCKET:-}
      - TELEMETRY_ARCHIVE_PREFIX=${TELEMETRY_ARCHIVE_PREFIX:-}
      - TELEMETRY_ARCHIVE_QUEUE_SIZE=${TELEMETRY_ARCHIVE_QUEUE_SIZE:-}
      - TELEMETRY_ARCHIVE_QUEUE_MAX_BYTES=${TELEMETRY_ARCHIVE_QUEUE_MAX_BYTES:-}
      - TELEMETRY_ARCHIVE_MAX_EVENT_BYTES=${TELEMETRY_ARCHIVE_MAX_EVENT_BYTES:-}
      - TELEMETRY_ARCHIVE_BATCH_SIZE=${TELEMETRY_ARCHIVE_BATCH_SIZE:-}
      - TELEMETRY_ARCHIVE_WORKER_COUNT=${TELEMETRY_ARCHIVE_WORKER_COUNT:-}
      - TELEMETRY_ARCHIVE_FLUSH_INTERVAL_SECONDS=${TELEMETRY_ARCHIVE_FLUSH_INTERVAL_SECONDS:-}
      - TELEMETRY_ARCHIVE_PUT_TIMEOUT_SECONDS=${TELEMETRY_ARCHIVE_PUT_TIMEOUT_SECONDS:-}
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
      start_period: 120s

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
      - SKIP_DATA_CHOWN=1
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
      - QA_BUNDLE_ENABLED=${QA_BUNDLE_ENABLED:-}
      - QA_BUNDLE_QUEUE_URL=${QA_BUNDLE_QUEUE_URL:-}
      - QA_BUNDLE_STORAGE_DRIVER=${QA_BUNDLE_STORAGE_DRIVER:-}
      - QA_BUNDLE_STORAGE_REGION=${QA_BUNDLE_STORAGE_REGION:-}
      - QA_BUNDLE_STORAGE_BUCKET=${QA_BUNDLE_STORAGE_BUCKET:-}
      - QA_BUNDLE_STORAGE_PREFIX=${QA_BUNDLE_STORAGE_PREFIX:-}
      - QA_ARCHIVE_ENABLED=${QA_ARCHIVE_ENABLED:-}
      - QA_ARCHIVE_STORAGE_DRIVER=${QA_ARCHIVE_STORAGE_DRIVER:-}
      - QA_ARCHIVE_STORAGE_REGION=${QA_ARCHIVE_STORAGE_REGION:-}
      - QA_ARCHIVE_STORAGE_BUCKET=${QA_ARCHIVE_STORAGE_BUCKET:-}
      - QA_ARCHIVE_STORAGE_PREFIX=${QA_ARCHIVE_STORAGE_PREFIX:-}
      - TELEMETRY_ARCHIVE_ENABLED=${TELEMETRY_ARCHIVE_ENABLED:-}
      - TELEMETRY_ARCHIVE_REGION=${TELEMETRY_ARCHIVE_REGION:-}
      - TELEMETRY_ARCHIVE_BUCKET=${TELEMETRY_ARCHIVE_BUCKET:-}
      - TELEMETRY_ARCHIVE_PREFIX=${TELEMETRY_ARCHIVE_PREFIX:-}
      - TELEMETRY_ARCHIVE_QUEUE_SIZE=${TELEMETRY_ARCHIVE_QUEUE_SIZE:-}
      - TELEMETRY_ARCHIVE_QUEUE_MAX_BYTES=${TELEMETRY_ARCHIVE_QUEUE_MAX_BYTES:-}
      - TELEMETRY_ARCHIVE_MAX_EVENT_BYTES=${TELEMETRY_ARCHIVE_MAX_EVENT_BYTES:-}
      - TELEMETRY_ARCHIVE_BATCH_SIZE=${TELEMETRY_ARCHIVE_BATCH_SIZE:-}
      - TELEMETRY_ARCHIVE_WORKER_COUNT=${TELEMETRY_ARCHIVE_WORKER_COUNT:-}
      - TELEMETRY_ARCHIVE_FLUSH_INTERVAL_SECONDS=${TELEMETRY_ARCHIVE_FLUSH_INTERVAL_SECONDS:-}
      - TELEMETRY_ARCHIVE_PUT_TIMEOUT_SECONDS=${TELEMETRY_ARCHIVE_PUT_TIMEOUT_SECONDS:-}
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
      start_period: 120s

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
ACTIVE=
if [ -r "${ROOT}/active-color" ]; then
  ACTIVE="$(sed -n '1p' "${ROOT}/active-color" | tr -d '[:space:]')"
fi
case "${ACTIVE}" in blue|green) ;; *) ACTIVE= ;; esac
ROUTE=
if [ -r "${ROOT}/caddy/Caddyfile" ]; then
  ROUTE="$(awk '
    $1 == "reverse_proxy" && $2 ~ /^tokenkey-(blue|green):8080$/ {
      count += 1
      color = $2
      sub(/^tokenkey-/, "", color)
      sub(/:8080$/, "", color)
    }
    END { if (count == 1) print color }
  ' "${ROOT}/caddy/Caddyfile")"
fi
case "${ROUTE}" in blue|green) ;; *) ROUTE= ;; esac
cd "${ROOT}"
docker compose --env-file "${ROOT}/.env" up -d --no-deps postgres redis
if [ -n "${ACTIVE}" ] && [ "${ACTIVE}" = "${ROUTE}" ]; then
  docker compose --project-name tokenkey --env-file "${ROOT}/.env" -f "${ROOT}/docker-compose.bluegreen.yml" up -d --no-deps "tokenkey-${ACTIVE}"
else
  docker compose --project-name tokenkey --env-file "${ROOT}/.env" -f "${ROOT}/docker-compose.bluegreen.yml" up -d --no-deps tokenkey-blue tokenkey-green
fi
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
Description=tokenkey stack (docker compose, Stage0 blue/green)
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

preserve_target_cutover() {
  local color="$1" target_config="$2" reason="$3"
  echo "::error::${reason}; preserving target and both colors for explicit rollback"
  sudo sh -c "cat '${target_config}' > '${LIVE_CADDY}'" || true
  sudo docker exec tokenkey-caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile || true
  ROUTE_SWITCHED=1
  CUTOVER_COMMITTED=1
  write_active_color "${color}" || true
  record_cutover || true
}

preserve_unconfirmed_reload() {
  local reason="$1"
  echo "::error::${reason}; cleared active-color and preserving both colors for explicit recovery"
  sudo rm -f "${ACTIVE_FILE}" >/dev/null 2>&1 || true
  ROUTE_SWITCHED=1
  CUTOVER_COMMITTED=1
}

commit_cutover() {
  local color="$1" upstream tmp backup target_config active_backup ts
  upstream="$(color_container "${color}"):8080"
  [[ -f "${LIVE_CADDY}" ]] || die "missing live Caddyfile at ${LIVE_CADDY}"

  tmp="${CADDY_DIR}/Caddyfile.bluegreen-${color}.new"
  ts="$(date +%Y%m%d-%H%M%S)"
  backup="${CADDY_DIR}/Caddyfile.before-bluegreen-${color}-${ts}"
  target_config="${CADDY_DIR}/Caddyfile.committed-bluegreen-${color}-${ts}"

  if ! render_caddy_with_upstream "${upstream}" "${tmp}"; then
    sudo rm -f "${tmp}" >/dev/null 2>&1 || true
    die "could not rewrite exactly one reverse_proxy upstream in ${LIVE_CADDY}"
  fi

  if ! sudo docker run --rm -v "${tmp}:/tmp/Caddyfile:ro" caddy:2-alpine caddy validate --config /tmp/Caddyfile --adapter caddyfile; then
    sudo rm -f "${tmp}" >/dev/null 2>&1 || true
    return 1
  fi
  sudo cp -a "${LIVE_CADDY}" "${backup}"
  sudo cp -a "${tmp}" "${target_config}"
  active_backup="${ACTIVE_FILE}.before-cutover.$$"
  if [[ -f "${ACTIVE_FILE}" ]]; then sudo cp -a "${ACTIVE_FILE}" "${active_backup}"; fi

  if ! sudo sh -c "cat '${tmp}' > '${LIVE_CADDY}'"; then
    if ! sudo sh -c "cat '${backup}' > '${LIVE_CADDY}'"; then
      preserve_target_cutover "${color}" "${target_config}" \
        "could not restore Caddyfile after failed live write"
    fi
    return 1
  fi
  sudo rm -f "${tmp}" >/dev/null 2>&1 || true
  if ! sudo docker exec tokenkey-caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile; then
    if ! sudo sh -c "cat '${backup}' > '${LIVE_CADDY}'" \
      || ! sudo docker exec tokenkey-caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile; then
      preserve_unconfirmed_reload \
        "target Caddy reload could not be confirmed"
    fi
    return 1
  fi
  ROUTE_SWITCHED=1

  if write_active_color "${color}" && record_cutover; then
    CUTOVER_COMMITTED=1
    sudo rm -f "${active_backup}" >/dev/null 2>&1 || true
    log "caddy now routes new requests to ${upstream} (backup=${backup})"
    return 0
  else
    echo "::warning::cutover commit failed; restore previous Caddyfile and active-color"
    if ! sudo sh -c "cat '${backup}' > '${LIVE_CADDY}'"; then
      preserve_target_cutover "${color}" "${target_config}" \
        "could not restore Caddyfile after cutover persistence failure"
      return 1
    fi
    if sudo docker exec tokenkey-caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile; then
      if [[ -f "${active_backup}" ]]; then
        if ! sudo mv "${active_backup}" "${ACTIVE_FILE}"; then
          preserve_target_cutover "${color}" "${target_config}" \
            "could not restore active-color after cutover persistence failure"
          return 1
        fi
      else
        if ! sudo rm -f "${ACTIVE_FILE}" >/dev/null 2>&1; then
          preserve_target_cutover "${color}" "${target_config}" \
            "could not remove active-color after cutover persistence failure"
          return 1
        fi
      fi
      ROUTE_SWITCHED=0
      return 1
    else
      preserve_target_cutover "${color}" "${target_config}" \
        "old Caddy route reload failed"
      return 1
    fi
  fi
}

write_active_color() {
  local color="$1" tmp="${ACTIVE_FILE}.new"
  printf '%s\n' "${color}" | sudo tee "${tmp}" >/dev/null
  sudo mv "${tmp}" "${ACTIVE_FILE}"
  log "active-color=${color}"
}

observe_routed_health() {
  local color="$1" seconds="${TOKENKEY_BLUEGREEN_OBSERVE_SECONDS:-30}" domain i body
  domain="$(env_get API_DOMAIN)"
  [[ -n "${domain}" ]] || die "API_DOMAIN is required for routed health observation"
  for i in $(seq 1 "${seconds}"); do
    if ! body="$(sudo docker exec tokenkey-caddy wget -q -T 5 -O - "https://${domain}/health" 2>/dev/null)"; then
      echo "::error::routed /health failed after committed cutover color=${color} second=${i}/${seconds}"
      return 1
    fi
    log "routed /health color=${color} second=${i}/${seconds}: ${body}"
    sleep 1
  done
}

read_active_color() {
  if [[ -r "${ACTIVE_FILE}" ]]; then
    sed -n '1p' "${ACTIVE_FILE}" | tr -d '[:space:]'
  fi
}

caddy_active_color() {
  sudo awk '
    $1 == "reverse_proxy" && $2 ~ /^tokenkey-(blue|green):8080$/ {
      color = $2
      sub(/^tokenkey-/, "", color)
      sub(/:8080$/, "", color)
      print color
      count += 1
    }
    END { if (count != 1) exit 1 }
  ' "${LIVE_CADDY}"
}

assert_active_route_consistent() {
  local active="$1" routed
  routed="$(caddy_active_color)" || die "could not resolve exactly one blue/green Caddy route"
  [[ "${routed}" = "${active}" ]] \
    || die "active-color ${active} disagrees with Caddy route ${routed}; explicit recovery required"
}

ensure_legacy_cutover() {
  local active routed legacy_img repo blue_img
  active="$(read_active_color || true)"
  if [[ "${active}" =~ ^(blue|green)$ ]]; then
    assert_active_route_consistent "${active}"
    log "blue/green already initialized: active=${active}"
    return 0
  fi

  routed="$(caddy_active_color || true)"
  if [[ "${routed}" =~ ^(blue|green)$ ]]; then
    die "colored Caddy route ${routed} has no active-color; explicit recovery required"
  fi

  if ! sudo docker inspect tokenkey >/dev/null 2>&1; then
    die "no active-color and legacy tokenkey container missing; manual recovery required"
  fi

  log "initializing blue/green layout from legacy tokenkey container"
  legacy_img="$(container_image tokenkey)"
  if ! repo="$(image_repo "${legacy_img:-$(env_get TOKENKEY_IMAGE)}")"; then
    die "could not derive image repo from legacy tokenkey container"
  fi
  blue_img="${repo}:${TAG}"

  backup_env legacy
  env_set TOKENKEY_IMAGE_BLUE "${blue_img}"
  env_set TOKENKEY_IMAGE_GREEN "${blue_img}"
  env_set TOKENKEY_IMAGE "${blue_img}"
  write_bluegreen_compose

  TARGET_CONTAINER=tokenkey-blue
  admit_edge_candidate tokenkey
  compose_bg pull tokenkey-blue
  compose_bg up -d --no-deps --force-recreate tokenkey-blue
  wait_healthy tokenkey-blue
  wait_ready tokenkey-blue

  commit_cutover blue
  observe_routed_health blue
  install_bluegreen_systemd_unit

  drain_container tokenkey
  sudo docker stop -t 30 tokenkey >/dev/null
  sudo docker rm -f tokenkey >/dev/null 2>&1 || true
  log "legacy tokenkey container removed after cutover to blue"

  TARGET_CONTAINER=""
  LEGACY_MIGRATED=1
}

deploy_target_color() {
  local active target active_container active_img repo new_img target_container active_file_value
  active_file_value="$(read_active_color || true)"
  [[ "${active_file_value}" =~ ^(blue|green)$ ]] || die "invalid or missing active color: ${active_file_value:-<empty>}"
  active="${active_file_value}"
  assert_active_route_consistent "${active}"
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
  if [[ -n "${TELEMETRY_ARCHIVE_ENABLED:-}" ]]; then
    env_set TELEMETRY_ARCHIVE_ENABLED "${TELEMETRY_ARCHIVE_ENABLED}"
    log "set TELEMETRY_ARCHIVE_ENABLED=${TELEMETRY_ARCHIVE_ENABLED}"
  fi
  env_set "TOKENKEY_IMAGE_$(printf '%s' "${target}" | tr '[:lower:]' '[:upper:]')" "${new_img}"
  env_set TOKENKEY_IMAGE "${new_img}"
  write_bluegreen_compose

  admit_edge_candidate "${active_container}"
  compose_bg pull "${target_container}"
  compose_bg up -d --no-deps --force-recreate "${target_container}"
  wait_healthy "${target_container}"
  wait_ready "${target_container}"

  commit_cutover "${target}"
  observe_routed_health "${target}"
  install_bluegreen_systemd_unit

  drain_container "${active_container}"
  sudo docker stop -t 30 "${active_container}" >/dev/null
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
ensure_profile_environment
ensure_legacy_cutover
if [[ "${LEGACY_MIGRATED}" = 0 ]]; then deploy_target_color; fi
prune_images
log "=== blue/green deploy done ==="
compose_bg ps
active="$(read_active_color)"
active_container="$(color_container "${active}")"
sudo docker logs "${active_container}" --since 2m 2>&1 | tail -30 || true
[[ -n "${CUTOVER_AT}" ]] || die "target cutover timestamp was not recorded"
echo "tk_stage0_cutover: timestamp=${CUTOVER_AT}"
REMOTE

printf '%s\n' "${REMOTE_SCRIPT}" > "${remote_script_file}"

REMOTE_B64="$(printf '%s' "${REMOTE_SCRIPT}" | base64 | tr -d '\n')"
chunks_json="$(printf '%s' "${REMOTE_B64}" | fold -w 1000 | jq -R -s 'split("\n") | map(select(length > 0))')"

if [[ "${DEPLOY_PROFILE}" = prod ]]; then
  DELIVER_QA_ARCHIVE_ENABLED="${QA_ARCHIVE_ENABLED:-true}"
  DELIVER_QA_ARCHIVE_DRIVER="${QA_ARCHIVE_STORAGE_DRIVER:-s3}"
  DELIVER_QA_ARCHIVE_REGION="${QA_ARCHIVE_STORAGE_REGION:-us-east-1}"
  DELIVER_QA_ARCHIVE_BUCKET="${QA_ARCHIVE_STORAGE_BUCKET:-tokenkey-prod-qa-raw-archive-682751977094}"
  DELIVER_QA_ARCHIVE_PREFIX="${QA_ARCHIVE_STORAGE_PREFIX:-raw/v1}"
  DELIVER_TELEMETRY_REGION="${TELEMETRY_ARCHIVE_REGION:-us-east-1}"
  DELIVER_TELEMETRY_BUCKET="${TELEMETRY_ARCHIVE_BUCKET:-tokenkey-prod-archive-682751977094}"
  DELIVER_TELEMETRY_PREFIX="${TELEMETRY_ARCHIVE_PREFIX:-prod/raw-telemetry}"
  DELIVER_TELEMETRY_QUEUE_SIZE="${TELEMETRY_ARCHIVE_QUEUE_SIZE:-8192}"
  DELIVER_TELEMETRY_QUEUE_MAX_BYTES="${TELEMETRY_ARCHIVE_QUEUE_MAX_BYTES:-33554432}"
  DELIVER_TELEMETRY_MAX_EVENT_BYTES="${TELEMETRY_ARCHIVE_MAX_EVENT_BYTES:-1048576}"
  DELIVER_TELEMETRY_BATCH_SIZE="${TELEMETRY_ARCHIVE_BATCH_SIZE:-256}"
  DELIVER_TELEMETRY_WORKER_COUNT="${TELEMETRY_ARCHIVE_WORKER_COUNT:-4}"
  DELIVER_TELEMETRY_FLUSH_INTERVAL="${TELEMETRY_ARCHIVE_FLUSH_INTERVAL_SECONDS:-5}"
  DELIVER_TELEMETRY_PUT_TIMEOUT="${TELEMETRY_ARCHIVE_PUT_TIMEOUT_SECONDS:-10}"
  DELIVER_MEDIA_DRIVER="${MEDIA_STORAGE_DRIVER:-s3}"
  DELIVER_MEDIA_REGION="${MEDIA_STORAGE_REGION:-us-east-1}"
  DELIVER_MEDIA_BUCKET="${MEDIA_STORAGE_BUCKET:-tokenkey-prod-media-682751977094}"
  DELIVER_IMAGE_ENABLED="${GATEWAY_IMAGE_CONCURRENCY_ENABLED:-true}"
  DELIVER_IMAGE_MAX="${GATEWAY_IMAGE_CONCURRENCY_MAX_CONCURRENT_REQUESTS:-8}"
  DELIVER_IMAGE_OVERFLOW="${GATEWAY_IMAGE_CONCURRENCY_OVERFLOW_MODE:-reject}"
else
  DELIVER_QA_ARCHIVE_ENABLED="" DELIVER_QA_ARCHIVE_DRIVER="" DELIVER_QA_ARCHIVE_REGION="" DELIVER_QA_ARCHIVE_BUCKET="" DELIVER_QA_ARCHIVE_PREFIX=""
  DELIVER_TELEMETRY_REGION="" DELIVER_TELEMETRY_BUCKET="" DELIVER_TELEMETRY_PREFIX="" DELIVER_TELEMETRY_QUEUE_SIZE="" DELIVER_TELEMETRY_QUEUE_MAX_BYTES=""
  DELIVER_TELEMETRY_MAX_EVENT_BYTES="" DELIVER_TELEMETRY_BATCH_SIZE="" DELIVER_TELEMETRY_WORKER_COUNT="" DELIVER_TELEMETRY_FLUSH_INTERVAL="" DELIVER_TELEMETRY_PUT_TIMEOUT=""
  DELIVER_MEDIA_DRIVER="" DELIVER_MEDIA_REGION="" DELIVER_MEDIA_BUCKET=""
  DELIVER_IMAGE_ENABLED="" DELIVER_IMAGE_MAX="" DELIVER_IMAGE_OVERFLOW=""
fi

jq -n \
  --arg tag "${TAG}" \
  --arg deploy_profile "${DEPLOY_PROFILE}" \
  --arg execution_timeout "${EXECUTION_TIMEOUT_SECONDS}" \
  --arg qa_enabled "${QA_BUNDLE_ENABLED-}" \
  --arg qa_queue_url "${QA_BUNDLE_QUEUE_URL-}" \
  --arg qa_driver "${QA_BUNDLE_STORAGE_DRIVER-}" \
  --arg qa_region "${QA_BUNDLE_STORAGE_REGION-}" \
  --arg qa_bucket "${QA_BUNDLE_STORAGE_BUCKET-}" \
  --arg qa_prefix "${QA_BUNDLE_STORAGE_PREFIX-}" \
  --argjson qa_enabled_set "$([[ -n "${QA_BUNDLE_ENABLED+x}" ]] && echo true || echo false)" \
  --argjson qa_queue_url_set "$([[ -n "${QA_BUNDLE_QUEUE_URL+x}" ]] && echo true || echo false)" \
  --argjson qa_driver_set "$([[ -n "${QA_BUNDLE_STORAGE_DRIVER+x}" ]] && echo true || echo false)" \
  --argjson qa_region_set "$([[ -n "${QA_BUNDLE_STORAGE_REGION+x}" ]] && echo true || echo false)" \
  --argjson qa_bucket_set "$([[ -n "${QA_BUNDLE_STORAGE_BUCKET+x}" ]] && echo true || echo false)" \
  --argjson qa_prefix_set "$([[ -n "${QA_BUNDLE_STORAGE_PREFIX+x}" ]] && echo true || echo false)" \
  --arg qa_archive_enabled "${DELIVER_QA_ARCHIVE_ENABLED}" \
  --arg qa_archive_driver "${DELIVER_QA_ARCHIVE_DRIVER}" \
  --arg qa_archive_region "${DELIVER_QA_ARCHIVE_REGION}" \
  --arg qa_archive_bucket "${DELIVER_QA_ARCHIVE_BUCKET}" \
  --arg qa_archive_prefix "${DELIVER_QA_ARCHIVE_PREFIX}" \
  --arg telemetry_enabled "${TELEMETRY_ARCHIVE_ENABLED:-}" \
  --arg telemetry_region "${DELIVER_TELEMETRY_REGION}" \
  --arg telemetry_bucket "${DELIVER_TELEMETRY_BUCKET}" \
  --arg telemetry_prefix "${DELIVER_TELEMETRY_PREFIX}" \
  --arg telemetry_queue_size "${DELIVER_TELEMETRY_QUEUE_SIZE}" \
  --arg telemetry_queue_max_bytes "${DELIVER_TELEMETRY_QUEUE_MAX_BYTES}" \
  --arg telemetry_max_event_bytes "${DELIVER_TELEMETRY_MAX_EVENT_BYTES}" \
  --arg telemetry_batch_size "${DELIVER_TELEMETRY_BATCH_SIZE}" \
  --arg telemetry_worker_count "${DELIVER_TELEMETRY_WORKER_COUNT}" \
  --arg telemetry_flush_interval "${DELIVER_TELEMETRY_FLUSH_INTERVAL}" \
  --arg telemetry_put_timeout "${DELIVER_TELEMETRY_PUT_TIMEOUT}" \
  --arg media_driver "${DELIVER_MEDIA_DRIVER}" \
  --arg media_region "${DELIVER_MEDIA_REGION}" \
  --arg media_bucket "${DELIVER_MEDIA_BUCKET}" \
  --arg image_enabled "${DELIVER_IMAGE_ENABLED}" \
  --arg image_max "${DELIVER_IMAGE_MAX}" \
  --arg image_overflow "${DELIVER_IMAGE_OVERFLOW}" \
  --arg edge_min_mem "${EDGE_MIN_MEM_AVAILABLE_BYTES}" \
  --arg edge_app_headroom "${EDGE_ACTIVE_APP_HEADROOM_BYTES}" \
  --arg edge_min_disk "${EDGE_MIN_ROOT_DISK_AVAILABLE_BYTES}" \
  --argjson chunks "${chunks_json}" '{
  commands: ([
    "set -euo pipefail",
    "rm -f /tmp/tokenkey-bluegreen-deploy.b64 /tmp/tokenkey-bluegreen-deploy.sh"
  ] + ($chunks | map("printf %s " + (. | @sh) + " >> /tmp/tokenkey-bluegreen-deploy.b64")) + [
    "base64 -d /tmp/tokenkey-bluegreen-deploy.b64 > /tmp/tokenkey-bluegreen-deploy.sh",
    "chmod 700 /tmp/tokenkey-bluegreen-deploy.sh",
    (
      "TAG=" + ($tag|@sh)
      + " DEPLOY_PROFILE=" + ($deploy_profile|@sh)
      + " QA_BUNDLE_ENABLED=" + ($qa_enabled|@sh)
      + " QA_BUNDLE_ENABLED_SET=" + ($qa_enabled_set|tostring)
      + " QA_BUNDLE_QUEUE_URL=" + ($qa_queue_url|@sh)
      + " QA_BUNDLE_QUEUE_URL_SET=" + ($qa_queue_url_set|tostring)
      + " QA_BUNDLE_STORAGE_DRIVER=" + ($qa_driver|@sh)
      + " QA_BUNDLE_STORAGE_DRIVER_SET=" + ($qa_driver_set|tostring)
      + " QA_BUNDLE_STORAGE_REGION=" + ($qa_region|@sh)
      + " QA_BUNDLE_STORAGE_REGION_SET=" + ($qa_region_set|tostring)
      + " QA_BUNDLE_STORAGE_BUCKET=" + ($qa_bucket|@sh)
      + " QA_BUNDLE_STORAGE_BUCKET_SET=" + ($qa_bucket_set|tostring)
      + " QA_BUNDLE_STORAGE_PREFIX=" + ($qa_prefix|@sh)
      + " QA_BUNDLE_STORAGE_PREFIX_SET=" + ($qa_prefix_set|tostring)
      + " QA_ARCHIVE_ENABLED=" + ($qa_archive_enabled|@sh)
      + " QA_ARCHIVE_STORAGE_DRIVER=" + ($qa_archive_driver|@sh)
      + " QA_ARCHIVE_STORAGE_REGION=" + ($qa_archive_region|@sh)
      + " QA_ARCHIVE_STORAGE_BUCKET=" + ($qa_archive_bucket|@sh)
      + " QA_ARCHIVE_STORAGE_PREFIX=" + ($qa_archive_prefix|@sh)
      + " TELEMETRY_ARCHIVE_ENABLED=" + ($telemetry_enabled|@sh)
      + " TELEMETRY_ARCHIVE_REGION=" + ($telemetry_region|@sh)
      + " TELEMETRY_ARCHIVE_BUCKET=" + ($telemetry_bucket|@sh)
      + " TELEMETRY_ARCHIVE_PREFIX=" + ($telemetry_prefix|@sh)
      + " TELEMETRY_ARCHIVE_QUEUE_SIZE=" + ($telemetry_queue_size|@sh)
      + " TELEMETRY_ARCHIVE_QUEUE_MAX_BYTES=" + ($telemetry_queue_max_bytes|@sh)
      + " TELEMETRY_ARCHIVE_MAX_EVENT_BYTES=" + ($telemetry_max_event_bytes|@sh)
      + " TELEMETRY_ARCHIVE_BATCH_SIZE=" + ($telemetry_batch_size|@sh)
      + " TELEMETRY_ARCHIVE_WORKER_COUNT=" + ($telemetry_worker_count|@sh)
      + " TELEMETRY_ARCHIVE_FLUSH_INTERVAL_SECONDS=" + ($telemetry_flush_interval|@sh)
      + " TELEMETRY_ARCHIVE_PUT_TIMEOUT_SECONDS=" + ($telemetry_put_timeout|@sh)
      + " MEDIA_STORAGE_DRIVER=" + ($media_driver|@sh)
      + " MEDIA_STORAGE_REGION=" + ($media_region|@sh)
      + " MEDIA_STORAGE_BUCKET=" + ($media_bucket|@sh)
      + " GATEWAY_IMAGE_CONCURRENCY_ENABLED=" + ($image_enabled|@sh)
      + " GATEWAY_IMAGE_CONCURRENCY_MAX_CONCURRENT_REQUESTS=" + ($image_max|@sh)
      + " GATEWAY_IMAGE_CONCURRENCY_OVERFLOW_MODE=" + ($image_overflow|@sh)
      + " EDGE_MIN_MEM_AVAILABLE_BYTES=" + ($edge_min_mem|@sh)
      + " EDGE_ACTIVE_APP_HEADROOM_BYTES=" + ($edge_app_headroom|@sh)
      + " EDGE_MIN_ROOT_DISK_AVAILABLE_BYTES=" + ($edge_min_disk|@sh)
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

cutover_cmd_id="$(aws "${ssm_region_args[@]}" ssm send-command \
  --instance-ids "${INSTANCE_ID}" \
  --document-name AWS-RunShellScript \
  --comment "${COMMENT} read cutover timestamp" \
  --parameters 'commands=["sudo cat /var/lib/tokenkey/last-cutover-at"]' \
  --query 'Command.CommandId' --output text)"
cutover_deadline=$(( $(date +%s) + 30 ))
cutover_status="InProgress"
while true; do
  cutover_status="$(aws "${ssm_region_args[@]}" ssm get-command-invocation \
    --command-id "${cutover_cmd_id}" --instance-id "${INSTANCE_ID}" \
    --query 'Status' --output text 2>/dev/null || echo InProgress)"
  case "${cutover_status}" in
    Success|Failed|TimedOut|Cancelled) break ;;
  esac
  if [[ $(date +%s) -ge ${cutover_deadline} ]]; then
    cutover_status="TimedOut"
    break
  fi
  sleep 1
done
if [[ "${cutover_status}" != "Success" ]]; then
  echo "::error::could not read cutover timestamp: ssm command status=${cutover_status}" >&2
  exit 1
fi
cutover_at="$(aws "${ssm_region_args[@]}" ssm get-command-invocation \
  --command-id "${cutover_cmd_id}" --instance-id "${INSTANCE_ID}" \
  --query 'StandardOutputContent' --output text | tr -d '[:space:]')"
if [[ ! "${cutover_at}" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]]; then
  echo "::error::successful blue/green deploy did not return a valid cutover timestamp" >&2
  exit 1
fi
echo "cutover_at=${cutover_at}"
if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  echo "cutover_at=${cutover_at}" >> "${GITHUB_OUTPUT}"
fi
