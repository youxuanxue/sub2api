#!/bin/bash
# Phase 2 QA archive host runner. It never deletes QA data.
set -euo pipefail

QA_MAINTENANCE_DOCKER="${QA_MAINTENANCE_DOCKER:-docker}"
QA_MAINTENANCE_RESOLVER="${QA_MAINTENANCE_RESOLVER:-/usr/local/lib/tokenkey/resolve-app-container.sh}"
QA_MAINTENANCE_RUNTIME_DIR="${QA_MAINTENANCE_RUNTIME_DIR:-/run/tokenkey-qa-maintenance}"
QA_MAINTENANCE_HOST_DATA_ROOT="${QA_MAINTENANCE_HOST_DATA_ROOT:-/var/lib/tokenkey/app}"
QA_MAINTENANCE_HOST_SCRATCH="${QA_MAINTENANCE_HOST_SCRATCH:-/var/lib/tokenkey/app/qa_archive_tmp}"
QA_MAINTENANCE_CONTAINER_SCRATCH="/app/data/qa_archive_tmp"
QA_MAINTENANCE_RECEIPT="${QA_MAINTENANCE_RECEIPT:-/var/lib/tokenkey/qa-maintenance-last-run.json}"
QA_MAINTENANCE_SYSTEMD_DIR="${QA_MAINTENANCE_SYSTEMD_DIR:-/etc/systemd/system}"
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

install_qa_maintenance_units() {
  local systemd_dir="${QA_MAINTENANCE_SYSTEMD_DIR}"
  install -d -m 0755 "${systemd_dir}"
  cat >"${systemd_dir}/tokenkey-qa-maintenance.service" <<'EOF'
[Unit]
Description=TokenKey hourly QA raw archive maintenance
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
PrivateTmp=true
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
RuntimeDirectory=tokenkey-qa-maintenance
RuntimeDirectoryMode=0700
ReadWritePaths=/var/lib/tokenkey /run/tokenkey-qa-maintenance
EOF
  cat >"${systemd_dir}/tokenkey-qa-maintenance.timer" <<'EOF'
[Unit]
Description=Hourly QA archive maintenance at :15 UTC

[Timer]
OnCalendar=*-*-* *:15:00
Persistent=true

[Install]
WantedBy=timers.target
EOF
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
    and child.get("deletion_authorized") is False
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
    "child_exit_code": int(os.environ["QA_RECEIPT_CHILD_EXIT"]),
    "runner_exit_code": int(os.environ["QA_RECEIPT_RUNNER_EXIT"]),
    "error_code": os.environ["QA_RECEIPT_ERROR_CODE"],
    "deletion_authorized": False,
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

run_partition_maintenance() {
  local partition_stdout partition_stderr partition_exit
  partition_stdout="$(mktemp "${QA_MAINTENANCE_RUNTIME_DIR}/partition.out.XXXXXX")"
  partition_stderr="$(mktemp "${QA_MAINTENANCE_RUNTIME_DIR}/partition.err.XXXXXX")"
  chmod 0600 "${partition_stdout}" "${partition_stderr}"
  qa_log "partition_start run_id=${RUN_ID} trigger=${TRIGGER} container=${APP_CONTAINER}"
  set +e
  qa_container_run "tokenkey-partition-maintenance-${RUN_ID}" \
    "${APP_IMAGE}" /app/sub2api \
    --partition-maintenance-once \
    --confirm=tokenkey-prod-partition-maintenance-v1 \
    >"${partition_stdout}" 2>"${partition_stderr}"
  partition_exit=$?
  set -e
  cat "${partition_stdout}"
  if [ "${partition_exit}" -ne 0 ]; then
    ERROR_CODE=partition_maintenance_failed
    qa_log "partition_failed run_id=${RUN_ID} child_exit=${partition_exit}"
    rm -f -- "${partition_stdout}" "${partition_stderr}"
    return "${partition_exit}"
  fi
  if ! PARTITION_STDOUT="${partition_stdout}" validate_partition_receipt; then
    ERROR_CODE=partition_receipt_invalid
    qa_log "partition_failed run_id=${RUN_ID} error_code=${ERROR_CODE}"
    rm -f -- "${partition_stdout}" "${partition_stderr}"
    return 53
  fi
  rm -f -- "${partition_stdout}" "${partition_stderr}"
  qa_log "partition_done run_id=${RUN_ID}"
}

validate_partition_receipt() {
  PARTITION_STDOUT="${PARTITION_STDOUT:?}" python3 - <<'PY'
import json
import os
import pathlib
import sys

path = pathlib.Path(os.environ["PARTITION_STDOUT"])
child = None
for raw in path.read_text(encoding="utf-8", errors="replace").splitlines():
    try:
        candidate = json.loads(raw.strip())
    except (json.JSONDecodeError, TypeError):
        continue
    if isinstance(candidate, dict):
        child = candidate

valid = (
    isinstance(child, dict)
    and child.get("receipt_version") == 1
    and child.get("mode") == "partition_maintenance"
    and child.get("ok") is True
    and child.get("job_name") == "ops_partition_maintenance"
    and child.get("deletion_authorized") is False
    and isinstance(child.get("tables"), list)
    and len(child.get("tables")) >= 1
)
raise SystemExit(0 if valid else 1)
PY
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
  run_partition_maintenance || return $?
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
    --trigger=*)
      local trigger="${1#--trigger=}"
      [ "$#" -eq 1 ] || { printf 'tokenkey-qa-maintenance: trigger accepts no arguments\n' >&2; return 40; }
      run_qa_maintenance "${trigger}"
      ;;
    '')
      run_qa_maintenance timer
      ;;
    *)
      printf 'tokenkey-qa-maintenance: unknown argument %s\n' "$1" >&2
      return 40
      ;;
  esac
}

main "$@"
