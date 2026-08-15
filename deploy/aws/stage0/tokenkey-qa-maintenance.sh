#!/bin/bash
# Single-owner QA lifecycle host runner.
set -euo pipefail

QA_MAINTENANCE_DOCKER="${QA_MAINTENANCE_DOCKER:-docker}"
QA_MAINTENANCE_RESOLVER="${QA_MAINTENANCE_RESOLVER:-/usr/local/lib/tokenkey/resolve-app-container.sh}"
QA_MAINTENANCE_RUNTIME_DIR="${QA_MAINTENANCE_RUNTIME_DIR:-/run/tokenkey-qa-maintenance}"
QA_MAINTENANCE_HOST_DATA_ROOT="${QA_MAINTENANCE_HOST_DATA_ROOT:-/var/lib/tokenkey/app}"
QA_MAINTENANCE_HOST_SCRATCH="${QA_MAINTENANCE_HOST_SCRATCH:-/var/lib/tokenkey/app/qa_archive_tmp}"
QA_MAINTENANCE_HOST_BLOB_ROOT="${QA_MAINTENANCE_HOST_BLOB_ROOT:-${QA_MAINTENANCE_HOST_DATA_ROOT}/qa_blobs}"
QA_MAINTENANCE_HOST_DLQ_ROOT="${QA_MAINTENANCE_HOST_DLQ_ROOT:-${QA_MAINTENANCE_HOST_DATA_ROOT}/qa_dlq}"
QA_MAINTENANCE_HOST_LEDGER_ROOT="${QA_MAINTENANCE_HOST_LEDGER_ROOT:-${QA_MAINTENANCE_HOST_DATA_ROOT}/qa_capture_ledger}"
QA_MAINTENANCE_CONTAINER_SCRATCH="/app/data/qa_archive_tmp"
QA_MAINTENANCE_RECEIPT="${QA_MAINTENANCE_RECEIPT:-/var/lib/tokenkey/qa-maintenance-last-run.json}"
QA_MAINTENANCE_SYSTEMD_DIR="${QA_MAINTENANCE_SYSTEMD_DIR:-/etc/systemd/system}"
QA_LIFECYCLE_LOCK_FILE="${QA_LIFECYCLE_LOCK_FILE:-/run/lock/tokenkey-qa-lifecycle.lock}"
QA_SINGLE_OWNER_ACTIVATION_DIR="${QA_SINGLE_OWNER_ACTIVATION_DIR:-${QA_MAINTENANCE_HOST_DATA_ROOT}/qa_single_owner_activation}"
QA_SINGLE_OWNER_CONTAINER_DIR="/app/data/qa_single_owner_activation"
QA_SINGLE_OWNER_DRAIN_TIMEOUT_SECONDS="${QA_SINGLE_OWNER_DRAIN_TIMEOUT_SECONDS:-300}"
QA_SINGLE_OWNER_ACK_TIMEOUT_SECONDS="${QA_SINGLE_OWNER_ACK_TIMEOUT_SECONDS:-60}"
QA_MAINTENANCE_UID=1000
QA_MAINTENANCE_GID=1000

qa_docker() {
  # shellcheck disable=SC2086 # The configured command may be "sudo docker".
  ${QA_MAINTENANCE_DOCKER} "$@"
}

qa_now() {
  date -u +%Y-%m-%dT%H:%M:%SZ
}

qa_log() {
  if command -v logger >/dev/null 2>&1; then
    logger -t tokenkey-qa-maintenance -- "$*"
  fi
}

qa_fail() {
  ERROR_CODE="$1"
  local exit_code="$2"
  shift 2
  printf 'tokenkey-qa-maintenance: %s\n' "$*" >&2
  return "${exit_code}"
}

with_qa_lifecycle_lock() {
  local lock_parent
  lock_parent="$(dirname "${QA_LIFECYCLE_LOCK_FILE}")"
  install -d -m 0755 "${lock_parent}"
  exec 9>"${QA_LIFECYCLE_LOCK_FILE}"
  if ! flock -n 9; then
    qa_fail lifecycle_lock_busy 75 "QA lifecycle host lock is already held"
    return
  fi
  "$@"
}

install_qa_maintenance_units() {
  local systemd_dir="${QA_MAINTENANCE_SYSTEMD_DIR}"
  install -d -m 0755 "${systemd_dir}"
  local changed=0
  install_unit_if_changed "${systemd_dir}/tokenkey-qa-maintenance.service" <<'EOF'
[Unit]
Description=TokenKey hourly QA lifecycle maintenance
After=network-online.target tokenkey.service
Wants=network-online.target

[Service]
Type=oneshot
EnvironmentFile=-/var/lib/tokenkey/.env
ExecStart=/usr/local/bin/tokenkey-qa-maintenance.sh --trigger=timer
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
RuntimeDirectory=tokenkey-qa-maintenance
RuntimeDirectoryMode=0700
ReadWritePaths=/var/lib/tokenkey /run/tokenkey-qa-maintenance
EOF
  [ "${QA_UNIT_INSTALL_CHANGED}" -eq 0 ] || changed=1
  install_unit_if_changed "${systemd_dir}/tokenkey-qa-maintenance.timer" <<'EOF'
[Unit]
Description=Hourly QA archive maintenance at :15 UTC

[Timer]
OnCalendar=*-*-* *:15:00
Persistent=true

[Install]
WantedBy=timers.target
EOF
  [ "${QA_UNIT_INSTALL_CHANGED}" -eq 0 ] || changed=1
  printf '{"changed":%s}\n' "$( [ "${changed}" -eq 1 ] && printf true || printf false )"
}

QA_UNIT_INSTALL_CHANGED=0

install_unit_if_changed() {
  local target="$1"
  local temp
  temp="$(mktemp "${target}.tmp.XXXXXX")"
  cat >"${temp}"
  chmod 0644 "${temp}"
  QA_UNIT_INSTALL_CHANGED=0
  if [ -f "${target}" ] && cmp -s -- "${temp}" "${target}"; then
    rm -f -- "${temp}"
    return
  fi
  mv -f -- "${temp}" "${target}"
  QA_UNIT_INSTALL_CHANGED=1
}

APP_CONTAINER=""
APP_IMAGE=""
APP_DATA_SOURCE=""
ENV_FILE=""

load_app_runtime() {
  install -d -m 0700 "${QA_MAINTENANCE_RUNTIME_DIR}" ||
    qa_fail runtime_dir_unavailable 41 "cannot prepare runtime directory"
  if [ ! -r "${QA_MAINTENANCE_RESOLVER}" ]; then
    qa_fail resolver_unavailable 42 "canonical app-container resolver unavailable"
    return
  fi
  TK_DOCKER="${QA_MAINTENANCE_DOCKER}"
  ACTIVE_COLOR_FILE="${ACTIVE_COLOR_FILE:-/var/lib/tokenkey/active-color}"
  export TK_DOCKER ACTIVE_COLOR_FILE
  # shellcheck source=../../../ops/lib/resolve-app-container.sh
  . "${QA_MAINTENANCE_RESOLVER}"
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
  if [ "${mount_type}" != bind ] || [ "${mount_source}" != "${QA_MAINTENANCE_HOST_DATA_ROOT}" ] || [ "${mount_rw}" != true ]; then
    qa_fail data_mount_invalid 45 "active /app/data bind source is invalid"
    return
  fi
  APP_DATA_SOURCE="${mount_source}"
  if [ ! -d "${QA_MAINTENANCE_HOST_SCRATCH}" ] || [ -L "${QA_MAINTENANCE_HOST_SCRATCH}" ]; then
    qa_fail scratch_invalid 46 "approved archive scratch directory is missing or unsafe"
    return
  fi
  local scratch_fact
  scratch_fact="$(stat -c '%u:%g:%a' "${QA_MAINTENANCE_HOST_SCRATCH}" 2>/dev/null)" || scratch_fact=""
  if [ "${scratch_fact}" != "${QA_MAINTENANCE_UID}:${QA_MAINTENANCE_GID}:700" ]; then
    qa_fail scratch_invalid 46 "approved archive scratch owner or mode is invalid"
    return
  fi
  local lifecycle_root lifecycle_fact
  for lifecycle_root in \
    "${QA_MAINTENANCE_HOST_BLOB_ROOT}" \
    "${QA_MAINTENANCE_HOST_DLQ_ROOT}" \
    "${QA_MAINTENANCE_HOST_LEDGER_ROOT}"; do
    if [ ! -d "${lifecycle_root}" ] || [ -L "${lifecycle_root}" ]; then
      qa_fail lifecycle_root_invalid 46 "QA lifecycle data directory is missing or unsafe"
      return
    fi
    lifecycle_fact="$(stat -c '%u:%g:%a' "${lifecycle_root}" 2>/dev/null)" || lifecycle_fact=""
    case "${lifecycle_fact}" in
      "${QA_MAINTENANCE_UID}:${QA_MAINTENANCE_GID}:700" | "${QA_MAINTENANCE_UID}:${QA_MAINTENANCE_GID}:750" | "${QA_MAINTENANCE_UID}:${QA_MAINTENANCE_GID}:755") ;;
      *)
        qa_fail lifecycle_root_invalid 46 "QA lifecycle data directory owner or mode is invalid"
        return
        ;;
    esac
  done

  ENV_FILE="$(mktemp "${QA_MAINTENANCE_RUNTIME_DIR}/env.XXXXXX")" || {
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
    --user="${QA_MAINTENANCE_UID}:${QA_MAINTENANCE_GID}" \
    --read-only --cap-drop=ALL --security-opt=no-new-privileges \
    --memory=1g --memory-swap=1g --cpus=0.20 --pids-limit=128 \
    --network="container:${APP_CONTAINER}" \
    --volume="${APP_DATA_SOURCE}:/app/data:ro" \
    --volume="${QA_MAINTENANCE_HOST_BLOB_ROOT}:/app/data/qa_blobs:rw" \
    --volume="${QA_MAINTENANCE_HOST_DLQ_ROOT}:/app/data/qa_dlq:rw" \
    --volume="${QA_MAINTENANCE_HOST_LEDGER_ROOT}:/app/data/qa_capture_ledger:ro" \
    --volume="${QA_MAINTENANCE_HOST_SCRATCH}:${QA_MAINTENANCE_CONTAINER_SCRATCH}:rw" \
    --env-file="${ENV_FILE}" \
    --env="TMPDIR=${QA_MAINTENANCE_CONTAINER_SCRATCH}" \
    "$@"
}

cleanup_runtime_files() {
  if [ -n "${ENV_FILE}" ]; then
    rm -f -- "${ENV_FILE}"
  fi
  if [ -n "${CHILD_STDOUT:-}" ]; then
    rm -f -- "${CHILD_STDOUT}"
  fi
  if [ -n "${CHILD_STDERR:-}" ]; then
    rm -f -- "${CHILD_STDERR}"
  fi
}

validate_child_receipt() {
  QA_CHILD_RUN_ID="${RUN_ID}" QA_CHILD_TRIGGER="${TRIGGER}" \
    python3 - "${CHILD_STDOUT}" <<'PY'
import json
import os
import pathlib
import sys

child = None
for raw in pathlib.Path(sys.argv[1]).read_text(
    encoding="utf-8", errors="replace"
).splitlines():
    try:
        candidate = json.loads(raw.strip())
    except (json.JSONDecodeError, TypeError):
        continue
    if isinstance(candidate, dict):
        child = candidate

valid = (
    isinstance(child, dict)
    and child.get("receipt_version") == 2
    and child.get("ok") is True
    and child.get("job_name") == "qa-maintenance"
	    and child.get("run_id") == os.environ["QA_CHILD_RUN_ID"]
	    and child.get("trigger") == os.environ["QA_CHILD_TRIGGER"]
	    and isinstance(child.get("plan"), dict)
	    and isinstance(child.get("deletion_authorized"), bool)
	)
raise SystemExit(0 if valid else 1)
PY
}

write_host_receipt() {
  local runner_exit="$1"
  local finished_at="$2"
  QA_RECEIPT_SCHEMA="qa-maintenance-runner-v1" \
  QA_RECEIPT_RUN_ID="${RUN_ID}" \
  QA_RECEIPT_TRIGGER="${TRIGGER}" \
  QA_RECEIPT_STARTED_AT="${STARTED_AT}" \
  QA_RECEIPT_FINISHED_AT="${finished_at}" \
  QA_RECEIPT_CONTAINER="${APP_CONTAINER}" \
  QA_RECEIPT_IMAGE="${APP_IMAGE}" \
  QA_RECEIPT_UID="${QA_MAINTENANCE_UID}" \
  QA_RECEIPT_GID="${QA_MAINTENANCE_GID}" \
  QA_RECEIPT_CHILD_EXIT="${CHILD_EXIT_CODE}" \
  QA_RECEIPT_RUNNER_EXIT="${runner_exit}" \
  QA_RECEIPT_ERROR_CODE="${ERROR_CODE}" \
  python3 - "${QA_MAINTENANCE_RECEIPT}" "${CHILD_STDOUT:-}" <<'PY'
import json
import os
import pathlib
import tempfile
import sys

target = pathlib.Path(sys.argv[1])
child_path = pathlib.Path(sys.argv[2]) if sys.argv[2] else None
child = None
if child_path and child_path.is_file():
    for raw in child_path.read_text(encoding="utf-8", errors="replace").splitlines():
        raw = raw.strip()
        if not raw:
            continue
        try:
            candidate = json.loads(raw)
        except json.JSONDecodeError:
            continue
        if isinstance(candidate, dict):
            if (
                candidate.get("receipt_version") == 2
	                and candidate.get("job_name") == "qa-maintenance"
	                and candidate.get("run_id") == os.environ["QA_RECEIPT_RUN_ID"]
	                and candidate.get("trigger") == os.environ["QA_RECEIPT_TRIGGER"]
	                and isinstance(candidate.get("deletion_authorized"), bool)
	                and isinstance(candidate.get("plan"), dict)
            ):
                child = candidate

payload = {
    "schema_version": os.environ["QA_RECEIPT_SCHEMA"],
    "run_id": os.environ["QA_RECEIPT_RUN_ID"],
    "trigger": os.environ["QA_RECEIPT_TRIGGER"],
    "started_at": os.environ["QA_RECEIPT_STARTED_AT"],
    "finished_at": os.environ["QA_RECEIPT_FINISHED_AT"],
    "active_container": os.environ["QA_RECEIPT_CONTAINER"],
    "image": os.environ["QA_RECEIPT_IMAGE"],
    "runner_uid": int(os.environ["QA_RECEIPT_UID"]),
    "runner_gid": int(os.environ["QA_RECEIPT_GID"]),
	    "normal": child.get("plan") if child else None,
	    "compensation": child.get("compensation") if child else None,
	    "normal_drop": child.get("normal_drop") if child else None,
	    "compensation_drop": child.get("compensation_drop") if child else None,
    "failure_stage": child.get("failure_stage") if child else None,
    "failure_code": child.get("failure_code") if child else None,
    "child_exit_code": int(os.environ["QA_RECEIPT_CHILD_EXIT"]),
    "runner_exit_code": int(os.environ["QA_RECEIPT_RUNNER_EXIT"]),
    "error_code": os.environ["QA_RECEIPT_ERROR_CODE"],
	    "deletion_authorized": child.get("deletion_authorized") if child else False,
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

finalize_maintenance() {
  local runner_exit="$?"
  trap - EXIT
  local finished_at
  finished_at="$(qa_now)"
  if [ "${runner_exit}" -ne 0 ] && [ -z "${ERROR_CODE}" ]; then
    ERROR_CODE=runner_failed
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

run_qa_maintenance() {
  TRIGGER="$1"
  case "${TRIGGER}" in
    timer | operator) ;;
    *)
      printf 'tokenkey-qa-maintenance: invalid trigger %s\n' "${TRIGGER}" >&2
      return 40
      ;;
  esac
  RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$$"
  STARTED_AT="$(qa_now)"
  CHILD_EXIT_CODE=-1
  ERROR_CODE=""
  CHILD_STDOUT=""
  CHILD_STDERR=""
  trap finalize_maintenance EXIT

  load_app_runtime
  CHILD_STDOUT="$(mktemp "${QA_MAINTENANCE_RUNTIME_DIR}/child.out.XXXXXX")"
  CHILD_STDERR="$(mktemp "${QA_MAINTENANCE_RUNTIME_DIR}/child.err.XXXXXX")"
  chmod 0600 "${CHILD_STDOUT}" "${CHILD_STDERR}"
  qa_log "archive_start run_id=${RUN_ID} trigger=${TRIGGER} container=${APP_CONTAINER}"
  set +e
  qa_container_run "tokenkey-qa-maintenance-${RUN_ID}" \
    --env="QA_MAINTENANCE_RUN_ID=${RUN_ID}" \
    --env="QA_MAINTENANCE_TRIGGER=${TRIGGER}" \
    "${APP_IMAGE}" /app/sub2api \
    --qa-maintenance-once \
    --confirm=tokenkey-prod-qa-maintenance-v1 \
    >"${CHILD_STDOUT}" 2>"${CHILD_STDERR}"
  CHILD_EXIT_CODE=$?
  set -e
  cat "${CHILD_STDOUT}"
  if [ "${CHILD_EXIT_CODE}" -ne 0 ]; then
    cat "${CHILD_STDERR}" >&2
    ERROR_CODE=child_failed
    qa_log "archive_failed run_id=${RUN_ID} child_exit=${CHILD_EXIT_CODE}"
    return "${CHILD_EXIT_CODE}"
  fi
  if ! validate_child_receipt; then
    ERROR_CODE=child_receipt_invalid
    qa_log "archive_failed run_id=${RUN_ID} error_code=${ERROR_CODE}"
    return 52
  fi
  qa_log "archive_done run_id=${RUN_ID}"
}

validate_activation_numbers() {
  [[ "${QA_SINGLE_OWNER_DRAIN_TIMEOUT_SECONDS}" =~ ^[1-9][0-9]*$ ]] ||
    qa_fail activation_config_invalid 40 "QA_SINGLE_OWNER_DRAIN_TIMEOUT_SECONDS must be a positive integer"
  [[ "${QA_SINGLE_OWNER_ACK_TIMEOUT_SECONDS}" =~ ^[1-9][0-9]*$ ]] ||
    qa_fail activation_config_invalid 40 "QA_SINGLE_OWNER_ACK_TIMEOUT_SECONDS must be a positive integer"
}

wait_for_boundary_drain() {
  systemctl disable --now tokenkey-qa-boundary.timer
  local deadline
  deadline=$(( $(date +%s) + QA_SINGLE_OWNER_DRAIN_TIMEOUT_SECONDS ))
  while systemctl is-active tokenkey-qa-boundary.service >/dev/null 2>&1; do
    if [ "$(date +%s)" -ge "${deadline}" ]; then
      qa_fail boundary_drain_timeout 61 "timed out waiting for tokenkey-qa-boundary.service to stop naturally"
      return
    fi
    sleep 1
  done
  if systemctl is-enabled tokenkey-qa-boundary.timer >/dev/null 2>&1 ||
    systemctl is-active tokenkey-qa-boundary.timer >/dev/null 2>&1 ||
    systemctl is-active tokenkey-qa-boundary.service >/dev/null 2>&1; then
    qa_fail boundary_not_inactive 62 "boundary owner is not fully disabled and inactive"
    return
  fi
}

write_activation_ack() {
  local ready_path="$1"
  local ack_path="$2"
  local run_id="$3"
  local plan_hash="$4"
  QA_ACTIVATION_RUN_ID="${run_id}" \
  QA_ACTIVATION_PLAN_HASH="${plan_hash}" \
  QA_ACTIVATION_CHECKED_AT="$(qa_now)" \
  python3 - "${ready_path}" "${ack_path}" <<'PY'
import json
import os
import pathlib
import tempfile
import sys

ready_path = pathlib.Path(sys.argv[1])
ack_path = pathlib.Path(sys.argv[2])
ready = json.loads(ready_path.read_text(encoding="utf-8"))
run_id = os.environ["QA_ACTIVATION_RUN_ID"]
plan_hash = os.environ["QA_ACTIVATION_PLAN_HASH"]
valid = (
    ready.get("schema_version") == "qa-single-owner-db-lock-ready-v1"
    and ready.get("run_id") == run_id
    and ready.get("plan_hash") == plan_hash
    and ready.get("database_lock_acquired") is True
    and isinstance(ready.get("nonce"), str)
    and bool(ready["nonce"])
)
if not valid:
    raise SystemExit("invalid single-owner database-lock ready receipt")
payload = {
    "schema_version": "qa-single-owner-host-ack-v1",
    "run_id": run_id,
    "nonce": ready["nonce"],
    "plan_hash": plan_hash,
    "boundary_timer_enabled": False,
    "boundary_timer_active": False,
    "boundary_service_active": False,
    "checked_at": os.environ["QA_ACTIVATION_CHECKED_AT"],
}
fd, temp_name = tempfile.mkstemp(prefix=f".{ack_path.name}.", dir=ack_path.parent)
try:
    if os.geteuid() == 0:
        os.fchown(fd, 1000, 1000)
    os.fchmod(fd, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as handle:
        json.dump(payload, handle, ensure_ascii=True, sort_keys=True, separators=(",", ":"))
        handle.write("\n")
        handle.flush()
        os.fsync(handle.fileno())
    os.replace(temp_name, ack_path)
finally:
    if os.path.exists(temp_name):
        os.unlink(temp_name)
PY
}

validate_activation_child_receipt() {
  local output_path="$1"
  local run_id="$2"
  local plan_hash="$3"
  QA_ACTIVATION_RUN_ID="${run_id}" QA_ACTIVATION_PLAN_HASH="${plan_hash}" \
    python3 - "${output_path}" <<'PY'
import json
import os
import pathlib
import sys

receipt = None
for raw in pathlib.Path(sys.argv[1]).read_text(encoding="utf-8", errors="replace").splitlines():
    try:
        candidate = json.loads(raw)
    except json.JSONDecodeError:
        continue
    if isinstance(candidate, dict):
        receipt = candidate
valid = (
    isinstance(receipt, dict)
    and receipt.get("ok") is True
    and receipt.get("phase") == "single_owner_activate"
    and receipt.get("run_id") == os.environ["QA_ACTIVATION_RUN_ID"]
    and receipt.get("plan_hash") == os.environ["QA_ACTIVATION_PLAN_HASH"]
)
raise SystemExit(0 if valid else 1)
PY
}

run_single_owner_activation() {
  local plan_hash="$1"
  local confirmation="$2"
  if [[ ! "${plan_hash}" =~ ^[0-9a-f]{64}$ ]]; then
    qa_fail activation_plan_hash_invalid 40 "single-owner activation plan hash is invalid"
    return
  fi
  if [ "${confirmation}" != "tokenkey-prod-qa-single-owner-activate-v1:${plan_hash}" ]; then
    qa_fail activation_confirmation_mismatch 40 "single-owner activation confirmation mismatch"
    return
  fi
  validate_activation_numbers
  wait_for_boundary_drain
  load_app_runtime
  if [ ! -e "${QA_SINGLE_OWNER_ACTIVATION_DIR}" ] && [ ! -L "${QA_SINGLE_OWNER_ACTIVATION_DIR}" ]; then
    install -d -m 0700 -o "${QA_MAINTENANCE_UID}" -g "${QA_MAINTENANCE_GID}" "${QA_SINGLE_OWNER_ACTIVATION_DIR}"
  fi
  if [ ! -d "${QA_SINGLE_OWNER_ACTIVATION_DIR}" ] || [ -L "${QA_SINGLE_OWNER_ACTIVATION_DIR}" ]; then
    qa_fail activation_dir_invalid 63 "single-owner activation directory is missing or unsafe"
    return
  fi

  local run_id child_name ready_path ack_path child_stdout child_stderr child_pid child_exit deadline
  run_id="single-owner-$(date -u +%Y%m%dT%H%M%SZ)-$$"
  child_name="tokenkey-qa-single-owner-${run_id}"
  ready_path="${QA_SINGLE_OWNER_ACTIVATION_DIR}/${run_id}.ready.json"
  ack_path="${QA_SINGLE_OWNER_ACTIVATION_DIR}/${run_id}.ack.json"
  child_stdout="$(mktemp "${QA_MAINTENANCE_RUNTIME_DIR}/activation.out.XXXXXX")"
  child_stderr="$(mktemp "${QA_MAINTENANCE_RUNTIME_DIR}/activation.err.XXXXXX")"
  chmod 0600 "${child_stdout}" "${child_stderr}"
  qa_container_run "${child_name}" \
    --volume="${QA_SINGLE_OWNER_ACTIVATION_DIR}:${QA_SINGLE_OWNER_CONTAINER_DIR}:rw" \
    --env="QA_SINGLE_OWNER_ACTIVATION_RUN_ID=${run_id}" \
    --env="QA_SINGLE_OWNER_ACTIVATION_DIR=${QA_SINGLE_OWNER_CONTAINER_DIR}" \
    --env="QA_SINGLE_OWNER_ACTIVATION_ACK_TIMEOUT_SECONDS=${QA_SINGLE_OWNER_ACK_TIMEOUT_SECONDS}" \
    "${APP_IMAGE}" /app/sub2api \
    --qa-single-owner-activate \
    --plan-hash="${plan_hash}" \
    --confirm="${confirmation}" \
    >"${child_stdout}" 2>"${child_stderr}" &
  child_pid=$!

  deadline=$(( $(date +%s) + QA_SINGLE_OWNER_ACK_TIMEOUT_SECONDS ))
  while [ ! -f "${ready_path}" ]; do
    if ! kill -0 "${child_pid}" >/dev/null 2>&1; then
      set +e
      wait "${child_pid}"
      child_exit=$?
      set -e
      cat "${child_stderr}" >&2
      rm -f -- "${child_stdout}" "${child_stderr}"
      cleanup_runtime_files
      qa_fail activation_child_failed "${child_exit}" "single-owner activation child exited before database-lock ready"
      return
    fi
    if [ "$(date +%s)" -ge "${deadline}" ]; then
      set +e
      wait "${child_pid}"
      child_exit=$?
      set -e
      cat "${child_stderr}" >&2
      rm -f -- "${child_stdout}" "${child_stderr}"
      cleanup_runtime_files
      qa_fail activation_ready_timeout "${child_exit}" "single-owner activation database-lock ready timed out"
      return
    fi
    sleep 0.1
  done

  if systemctl is-enabled tokenkey-qa-boundary.timer >/dev/null 2>&1 ||
    systemctl is-active tokenkey-qa-boundary.timer >/dev/null 2>&1 ||
    systemctl is-active tokenkey-qa-boundary.service >/dev/null 2>&1; then
    qa_fail boundary_reactivated 64 "boundary owner reactivated while database lock was held"
    return
  fi
  write_activation_ack "${ready_path}" "${ack_path}" "${run_id}" "${plan_hash}"
  set +e
  wait "${child_pid}"
  child_exit=$?
  set -e
  cat "${child_stdout}"
  if [ "${child_exit}" -ne 0 ]; then
    cat "${child_stderr}" >&2
    rm -f -- "${child_stdout}" "${child_stderr}"
    cleanup_runtime_files
    qa_fail activation_child_failed "${child_exit}" "single-owner activation child failed"
    return
  fi
  if ! validate_activation_child_receipt "${child_stdout}" "${run_id}" "${plan_hash}"; then
    rm -f -- "${child_stdout}" "${child_stderr}"
    cleanup_runtime_files
    qa_fail activation_receipt_invalid 65 "single-owner activation child receipt is invalid"
    return
  fi
  rm -f -- "${child_stdout}" "${child_stderr}"
  cleanup_runtime_files
}

run_selftest_container() {
  local name="$1"
  shift
  qa_container_run "${name}" "${APP_IMAGE}" "$@"
}

run_selftest() {
  ERROR_CODE=""
  CHILD_STDOUT=""
  CHILD_STDERR=""
  load_app_runtime
  local sentinel="${QA_MAINTENANCE_HOST_SCRATCH}/.qa-maintenance-selftest"
  rm -f -- "${sentinel}"
  run_selftest_container "tokenkey-qa-maintenance-selftest-create-$$" /bin/sh -ceu \
    'umask 077; printf %s qa-maintenance-selftest-ok > /app/data/qa_archive_tmp/.qa-maintenance-selftest; test "$(cat /app/data/qa_archive_tmp/.qa-maintenance-selftest)" = qa-maintenance-selftest-ok # qa-maintenance-selftest-create'
  if [ ! -f "${sentinel}" ] || [ "$(cat "${sentinel}")" != qa-maintenance-selftest-ok ]; then
    cleanup_runtime_files
    qa_fail selftest_host_visibility_failed 48 "selftest sentinel is not visible on the host"
    return
  fi
  if [ "$(stat -c '%u:%g:%a' "${sentinel}" 2>/dev/null)" != "${QA_MAINTENANCE_UID}:${QA_MAINTENANCE_GID}:600" ]; then
    cleanup_runtime_files
    qa_fail selftest_owner_mode_failed 49 "selftest sentinel owner or mode is invalid"
    return
  fi
  run_selftest_container "tokenkey-qa-maintenance-selftest-remove-$$" /bin/sh -ceu \
    'test -f /app/data/qa_archive_tmp/.qa-maintenance-selftest; rm -f /app/data/qa_archive_tmp/.qa-maintenance-selftest # qa-maintenance-selftest-remove'
  if [ -e "${sentinel}" ]; then
    cleanup_runtime_files
    qa_fail selftest_remove_failed 50 "selftest sentinel was not removed through the container mount"
    return
  fi
  cleanup_runtime_files
}

run_qa_archive_command() {
  if [ "$#" -eq 0 ]; then
    printf 'tokenkey-qa-maintenance: qa-archive command required\n' >&2
    return 40
  fi
  ERROR_CODE=""
  CHILD_STDOUT=""
  CHILD_STDERR=""
  load_app_runtime
  local -a restore_mount=()
  local argument restore_output="" expect_output=false output_seen=false
  for argument in "$@"; do
    if [ "${expect_output}" = true ]; then
      restore_output="${argument}"
      expect_output=false
      continue
    fi
    case "${argument}" in
      --output)
        if [ "${output_seen}" = true ]; then
          cleanup_runtime_files
          qa_fail restore_path_invalid 51 "restore output may be specified only once"
          return
        fi
        output_seen=true
        expect_output=true
        ;;
      --output=*)
        if [ "${output_seen}" = true ]; then
          cleanup_runtime_files
          qa_fail restore_path_invalid 51 "restore output may be specified only once"
          return
        fi
        output_seen=true
        restore_output="${argument#--output=}"
        ;;
    esac
  done
  if [ "${expect_output}" = true ] || { [ "${output_seen}" = true ] && [ -z "${restore_output}" ]; }; then
    cleanup_runtime_files
    qa_fail restore_path_invalid 51 "restore output value is missing"
    return
  fi
  if [ -n "${restore_output}" ]; then
    case "${restore_output}" in
      /app/data/qa_archive_restore/*)
        local restore_name="${restore_output#/app/data/qa_archive_restore/}"
        case "${restore_name}" in
          '' | . | .. | */*)
            cleanup_runtime_files
            qa_fail restore_path_invalid 51 "restore output must be one child directory"
            return
            ;;
        esac
        local restore_root="${APP_DATA_SOURCE}/qa_archive_restore"
        if [ ! -e "${restore_root}" ] && [ ! -L "${restore_root}" ]; then
          install -d -m 0700 -o "${QA_MAINTENANCE_UID}" -g "${QA_MAINTENANCE_GID}" "${restore_root}"
        fi
        if [ ! -d "${restore_root}" ] || [ -L "${restore_root}" ]; then
          cleanup_runtime_files
          qa_fail restore_path_invalid 51 "restore root is missing or unsafe"
          return
        fi
        local restore_fact
        restore_fact="$(stat -c '%u:%g:%a' "${restore_root}" 2>/dev/null)" || restore_fact=""
        if [ "${restore_fact}" != "${QA_MAINTENANCE_UID}:${QA_MAINTENANCE_GID}:700" ]; then
          cleanup_runtime_files
          qa_fail restore_path_invalid 51 "restore root owner or mode is invalid"
          return
        fi
        restore_mount+=(--volume="${restore_root}:/app/data/qa_archive_restore:rw")
        ;;
      *)
        cleanup_runtime_files
        qa_fail restore_path_invalid 51 "restore output is outside the isolated root"
        return
        ;;
    esac
  fi
  set +e
  if [ "${#restore_mount[@]}" -gt 0 ]; then
    qa_container_run "tokenkey-qa-archive-op-$$" "${restore_mount[@]}" \
      "${APP_IMAGE}" /app/qa-archive "$@"
  else
    qa_container_run "tokenkey-qa-archive-op-$$" \
      "${APP_IMAGE}" /app/qa-archive "$@"
  fi
  local result=$?
  set -e
  cleanup_runtime_files
  return "${result}"
}

main() {
  case "${1:-}" in
    --selftest)
      [ "$#" -eq 1 ] || { printf 'tokenkey-qa-maintenance: selftest accepts no arguments\n' >&2; return 40; }
      run_selftest
      ;;
    --install-units)
      [ "$#" -eq 1 ] || { printf 'tokenkey-qa-maintenance: install accepts no arguments\n' >&2; return 40; }
      install_qa_maintenance_units
      ;;
    --qa-archive)
      shift
      run_qa_archive_command "$@"
      ;;
    --activate-single-owner)
      shift
      local plan_hash="" confirmation="" argument
      for argument in "$@"; do
        case "${argument}" in
          --plan-hash=*) plan_hash="${argument#--plan-hash=}" ;;
          --confirm=*) confirmation="${argument#--confirm=}" ;;
          *) printf 'tokenkey-qa-maintenance: unknown activation argument %s\n' "${argument}" >&2; return 40 ;;
        esac
      done
      [ -n "${plan_hash}" ] && [ -n "${confirmation}" ] || {
        printf 'tokenkey-qa-maintenance: activation requires plan hash and confirmation\n' >&2
        return 40
      }
      with_qa_lifecycle_lock run_single_owner_activation "${plan_hash}" "${confirmation}"
      ;;
    --trigger=*)
      local trigger="${1#--trigger=}"
      [ "$#" -eq 1 ] || { printf 'tokenkey-qa-maintenance: trigger accepts no arguments\n' >&2; return 40; }
      with_qa_lifecycle_lock run_qa_maintenance "${trigger}"
      ;;
    '')
      with_qa_lifecycle_lock run_qa_maintenance timer
      ;;
    *)
      printf 'tokenkey-qa-maintenance: unknown argument %s\n' "$1" >&2
      return 40
      ;;
  esac
}

main "$@"
