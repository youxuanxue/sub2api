#!/bin/bash
# Phase 2 QA UTC-hour boundary host runner.
set -euo pipefail

QA_BOUNDARY_DOCKER="${QA_BOUNDARY_DOCKER:-docker}"
QA_BOUNDARY_RESOLVER="${QA_BOUNDARY_RESOLVER:-/usr/local/lib/tokenkey/resolve-app-container.sh}"
QA_BOUNDARY_RUNTIME_DIR="${QA_BOUNDARY_RUNTIME_DIR:-/run/tokenkey-qa-boundary}"
QA_BOUNDARY_HOST_DATA_ROOT="${QA_BOUNDARY_HOST_DATA_ROOT:-/var/lib/tokenkey/app}"
QA_BOUNDARY_RECEIPT="${QA_BOUNDARY_RECEIPT:-/var/lib/tokenkey/qa-boundary-last-run.json}"
QA_BOUNDARY_SYSTEMD_DIR="${QA_BOUNDARY_SYSTEMD_DIR:-/etc/systemd/system}"
QA_BOUNDARY_UID=1000
QA_BOUNDARY_GID=1000

qa_docker() {
  # shellcheck disable=SC2086 # The configured command may be "sudo docker".
  ${QA_BOUNDARY_DOCKER} "$@"
}

qa_now() {
  date -u +%Y-%m-%dT%H:%M:%SZ
}

qa_log() {
  if command -v logger >/dev/null 2>&1; then
    logger -t tokenkey-qa-boundary -- "$*"
  fi
}

qa_fail() {
  ERROR_CODE="$1"
  local exit_code="$2"
  shift 2
  ERROR_MESSAGE="$*"
  printf 'tokenkey-qa-boundary: %s\n' "${ERROR_MESSAGE}" >&2
  return "${exit_code}"
}

install_qa_boundary_units() {
  local systemd_dir="${QA_BOUNDARY_SYSTEMD_DIR}"
  install -d -m 0755 "${systemd_dir}"
  cat >"${systemd_dir}/tokenkey-qa-boundary.service" <<'EOF'
[Unit]
Description=TokenKey hourly QA lifecycle boundary maintenance
After=network-online.target tokenkey.service
Wants=network-online.target

[Service]
Type=oneshot
EnvironmentFile=-/var/lib/tokenkey/.env
ExecStart=/usr/local/bin/tokenkey-qa-boundary.sh --trigger=timer
Nice=15
IOSchedulingClass=idle
CPUQuota=20%
MemoryMax=1G
TasksMax=128
TimeoutStartSec=2400
PrivateTmp=true
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
RuntimeDirectory=tokenkey-qa-boundary
RuntimeDirectoryMode=0700
ReadWritePaths=/var/lib/tokenkey /run/tokenkey-qa-boundary
EOF
  cat >"${systemd_dir}/tokenkey-qa-boundary.timer" <<'EOF'
[Unit]
Description=Hourly QA lifecycle boundary at :00 UTC

[Timer]
OnCalendar=*-*-* *:00:00
Persistent=true

[Install]
WantedBy=timers.target
EOF
}

APP_CONTAINER=""
APP_IMAGE=""
APP_DATA_SOURCE=""
ENV_FILE=""
CHILD_STDOUT=""
CHILD_STDERR=""

load_app_runtime() {
  install -d -m 0700 "${QA_BOUNDARY_RUNTIME_DIR}" ||
    qa_fail runtime_dir_unavailable 41 "cannot prepare runtime directory"
  if [ ! -r "${QA_BOUNDARY_RESOLVER}" ]; then
    qa_fail resolver_unavailable 42 "canonical app-container resolver unavailable"
    return
  fi
  TK_DOCKER="${QA_BOUNDARY_DOCKER}"
  ACTIVE_COLOR_FILE="${ACTIVE_COLOR_FILE:-/var/lib/tokenkey/active-color}"
  export TK_DOCKER ACTIVE_COLOR_FILE
  # shellcheck source=../../../ops/lib/resolve-app-container.sh
  . "${QA_BOUNDARY_RESOLVER}"
  APP_CONTAINER="$(tk_resolve_app_container auto)" || {
    qa_fail container_unresolved 43 "active app container is ambiguous or unavailable"
    return
  }
  APP_IMAGE="$(qa_docker inspect --format '{{.Image}}' "${APP_CONTAINER}" 2>/dev/null)" || APP_IMAGE=""
  case "${APP_IMAGE}" in
    sha256:*) ;;
    *)
      qa_fail image_unavailable 44 "active app immutable image unavailable"
      return
      ;;
  esac

  local mount_fact mount_type mount_source mount_rw
  mount_fact="$(qa_docker inspect --format '{{range .Mounts}}{{if eq .Destination "/app/data"}}{{printf "%s|%s|%t" .Type .Source .RW}}{{end}}{{end}}' "${APP_CONTAINER}" 2>/dev/null)" || mount_fact=""
  IFS='|' read -r mount_type mount_source mount_rw <<<"${mount_fact}"
  if [ "${mount_type}" != bind ] || [ "${mount_source}" != "${QA_BOUNDARY_HOST_DATA_ROOT}" ] || [ "${mount_rw}" != true ]; then
    qa_fail data_mount_invalid 45 "active /app/data bind source is invalid"
    return
  fi
  APP_DATA_SOURCE="${mount_source}"
  local hot_dir
  for hot_dir in qa_blobs qa_dlq; do
    if [ ! -d "${APP_DATA_SOURCE}/${hot_dir}" ] || [ -L "${APP_DATA_SOURCE}/${hot_dir}" ]; then
      qa_fail hot_mount_invalid 46 "approved ${hot_dir} directory is missing or unsafe"
      return
    fi
  done

  ENV_FILE="$(mktemp "${QA_BOUNDARY_RUNTIME_DIR}/env.XXXXXX")" || {
    qa_fail env_capture_failed 47 "cannot allocate environment file"
    return
  }
  chmod 0600 "${ENV_FILE}"
  if ! qa_docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "${APP_CONTAINER}" >"${ENV_FILE}"; then
    qa_fail env_capture_failed 47 "cannot capture active app environment"
    return
  fi
}

qa_container_run() {
  local name="$1"
  shift
  qa_docker run --rm --name="${name}" \
    --user="${QA_BOUNDARY_UID}:${QA_BOUNDARY_GID}" \
    --read-only --cap-drop=ALL --security-opt=no-new-privileges \
    --memory=1g --memory-swap=1g --cpus=0.20 --pids-limit=128 \
    --network="container:${APP_CONTAINER}" \
    --volume="${APP_DATA_SOURCE}:/app/data:ro" \
    --volume="${APP_DATA_SOURCE}/qa_blobs:/app/data/qa_blobs:rw" \
    --volume="${APP_DATA_SOURCE}/qa_dlq:/app/data/qa_dlq:rw" \
    --env-file="${ENV_FILE}" \
    --env="PGOPTIONS=-c lock_timeout=100ms -c statement_timeout=120s" \
    "$@"
}

cleanup_runtime_files() {
  [ -z "${ENV_FILE}" ] || rm -f -- "${ENV_FILE}"
  [ -z "${CHILD_STDOUT}" ] || rm -f -- "${CHILD_STDOUT}"
  [ -z "${CHILD_STDERR}" ] || rm -f -- "${CHILD_STDERR}"
}

validate_child_receipt() {
  QA_CHILD_RUN_ID="${RUN_ID}" QA_CHILD_TRIGGER="${TRIGGER}" \
    python3 - "${CHILD_STDOUT}" <<'PY'
import json
import os
import pathlib
import sys

child = None
for raw in pathlib.Path(sys.argv[1]).read_text(encoding="utf-8", errors="replace").splitlines():
    try:
        candidate = json.loads(raw.strip())
    except (json.JSONDecodeError, TypeError):
        continue
    if isinstance(candidate, dict):
        child = candidate
valid = (
    isinstance(child, dict)
    and child.get("receipt_version") == 1
    and child.get("ok") is True
    and child.get("job_name") == "qa-boundary"
    and child.get("run_id") == os.environ["QA_CHILD_RUN_ID"]
    and child.get("trigger") == os.environ["QA_CHILD_TRIGGER"]
    and isinstance(child.get("boundary"), dict)
)
raise SystemExit(0 if valid else 1)
PY
}

write_host_receipt() {
  local runner_exit="$1"
  local finished_at="$2"
  QA_RECEIPT_RUN_ID="${RUN_ID}" \
  QA_RECEIPT_TRIGGER="${TRIGGER}" \
  QA_RECEIPT_STARTED_AT="${STARTED_AT}" \
  QA_RECEIPT_FINISHED_AT="${finished_at}" \
  QA_RECEIPT_CONTAINER="${APP_CONTAINER}" \
  QA_RECEIPT_IMAGE="${APP_IMAGE}" \
  QA_RECEIPT_UID="${QA_BOUNDARY_UID}" \
  QA_RECEIPT_GID="${QA_BOUNDARY_GID}" \
  QA_RECEIPT_CHILD_EXIT="${CHILD_EXIT_CODE}" \
  QA_RECEIPT_RUNNER_EXIT="${runner_exit}" \
  QA_RECEIPT_ERROR_CODE="${ERROR_CODE}" \
  QA_RECEIPT_ERROR="${ERROR_MESSAGE}" \
    python3 - "${QA_BOUNDARY_RECEIPT}" "${CHILD_STDOUT}" <<'PY'
import json
import os
import pathlib
import sys
import tempfile

target = pathlib.Path(sys.argv[1])
child_path = pathlib.Path(sys.argv[2]) if sys.argv[2] else None
child = None
if child_path and child_path.is_file():
    for raw in child_path.read_text(encoding="utf-8", errors="replace").splitlines():
        try:
            candidate = json.loads(raw.strip())
        except (json.JSONDecodeError, TypeError):
            continue
        if isinstance(candidate, dict):
            child = candidate
payload = {
    "schema_version": "qa-boundary-runner-v1",
    "run_id": os.environ["QA_RECEIPT_RUN_ID"],
    "trigger": os.environ["QA_RECEIPT_TRIGGER"],
    "started_at": os.environ["QA_RECEIPT_STARTED_AT"],
    "finished_at": os.environ["QA_RECEIPT_FINISHED_AT"],
    "active_container": os.environ["QA_RECEIPT_CONTAINER"],
    "image": os.environ["QA_RECEIPT_IMAGE"],
    "runner_uid": int(os.environ["QA_RECEIPT_UID"]),
    "runner_gid": int(os.environ["QA_RECEIPT_GID"]),
    "child_exit_code": int(os.environ["QA_RECEIPT_CHILD_EXIT"]),
    "runner_exit_code": int(os.environ["QA_RECEIPT_RUNNER_EXIT"]),
    "error_code": os.environ["QA_RECEIPT_ERROR_CODE"],
    "error": os.environ["QA_RECEIPT_ERROR"][:500],
    "boundary": child.get("boundary") if child else None,
    "deletion_authorized": bool(child and child.get("deletion_authorized") is True),
}
target.parent.mkdir(parents=True, exist_ok=True)
fd, temp_name = tempfile.mkstemp(prefix=f".{target.name}.", dir=target.parent)
try:
    os.fchmod(fd, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as handle:
        json.dump(payload, handle, ensure_ascii=True, sort_keys=True, separators=(",", ":"))
        handle.write("\n")
        handle.flush()
        os.fsync(handle.fileno())
    os.replace(temp_name, target)
    dir_fd = os.open(target.parent, os.O_RDONLY)
    try:
        os.fsync(dir_fd)
    finally:
        os.close(dir_fd)
finally:
    if os.path.exists(temp_name):
        os.unlink(temp_name)
PY
}

finalize_boundary() {
  local runner_exit="$?"
  trap - EXIT
  local finished_at
  finished_at="$(qa_now)"
  if [ "${runner_exit}" -ne 0 ] && [ -z "${ERROR_CODE}" ]; then
    ERROR_CODE=runner_failed
    ERROR_MESSAGE="boundary runner failed"
  fi
  if ! write_host_receipt "${runner_exit}" "${finished_at}"; then
    qa_log "receipt_write_failed run_id=${RUN_ID}"
    if [ "${runner_exit}" -eq 0 ]; then
      runner_exit=70
    fi
  fi
  cleanup_runtime_files
  exit "${runner_exit}"
}

run_qa_boundary() {
  TRIGGER="$1"
  case "${TRIGGER}" in
    timer | operator) ;;
    *)
      printf 'tokenkey-qa-boundary: invalid trigger %s\n' "${TRIGGER}" >&2
      return 40
      ;;
  esac
  RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$$"
  STARTED_AT="$(qa_now)"
  CHILD_EXIT_CODE=-1
  ERROR_CODE=""
  ERROR_MESSAGE=""
  trap finalize_boundary EXIT

  load_app_runtime
  CHILD_STDOUT="$(mktemp "${QA_BOUNDARY_RUNTIME_DIR}/child.out.XXXXXX")"
  CHILD_STDERR="$(mktemp "${QA_BOUNDARY_RUNTIME_DIR}/child.err.XXXXXX")"
  chmod 0600 "${CHILD_STDOUT}" "${CHILD_STDERR}"
  set +e  # preflight-allow: swallow (capture child exit for the host receipt)
  qa_container_run "tokenkey-qa-boundary-${RUN_ID}" \
    --env="QA_MAINTENANCE_RUN_ID=${RUN_ID}" \
    --env="QA_MAINTENANCE_TRIGGER=${TRIGGER}" \
    "${APP_IMAGE}" /app/sub2api \
    --qa-boundary-once \
    --confirm=tokenkey-prod-qa-boundary-v1 \
    >"${CHILD_STDOUT}" 2>"${CHILD_STDERR}"
  CHILD_EXIT_CODE=$?
  set -e
  cat "${CHILD_STDOUT}"
  if [ "${CHILD_EXIT_CODE}" -ne 0 ]; then
    qa_fail child_failed "${CHILD_EXIT_CODE}" "boundary child exited ${CHILD_EXIT_CODE}"
    return
  fi
  if ! validate_child_receipt; then
    qa_fail child_receipt_invalid 52 "boundary child receipt is missing or contradictory"
    return
  fi
}

main() {
  case "${1:-}" in
    --install-units)
      [ "$#" -eq 1 ] || { printf 'tokenkey-qa-boundary: install accepts no arguments\n' >&2; return 40; }
      install_qa_boundary_units
      ;;
    --trigger=*)
      local trigger="${1#--trigger=}"
      [ "$#" -eq 1 ] || { printf 'tokenkey-qa-boundary: trigger accepts no arguments\n' >&2; return 40; }
      run_qa_boundary "${trigger}"
      ;;
    '')
      run_qa_boundary timer
      ;;
    *)
      printf 'tokenkey-qa-boundary: unknown argument %s\n' "$1" >&2
      return 40
      ;;
  esac
}

main "$@"
