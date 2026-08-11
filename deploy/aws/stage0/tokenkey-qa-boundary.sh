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
QA_EXPORT_ORPHAN_HELPER="${QA_EXPORT_ORPHAN_HELPER:-/usr/local/lib/tokenkey/qa-export-orphan.py}"

qa_docker() {
  # shellcheck disable=SC2086
  ${QA_BOUNDARY_DOCKER} "$@"
}

qa_now() {
  date -u +%Y-%m-%dT%H:%M:%SZ
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

load_app_runtime() {
  install -d -m 0700 "${QA_BOUNDARY_RUNTIME_DIR}"
  # shellcheck source=../../../ops/lib/resolve-app-container.sh
  . "${QA_BOUNDARY_RESOLVER}"
  APP_CONTAINER="$(tk_resolve_app_container auto)"
  APP_IMAGE="$(qa_docker inspect --format '{{.Image}}' "${APP_CONTAINER}")"
  APP_DATA_SOURCE="$(qa_docker inspect --format '{{range .Mounts}}{{if eq .Destination "/app/data"}}{{.Source}}{{end}}{{end}}' "${APP_CONTAINER}")"
  ENV_FILE="$(mktemp "${QA_BOUNDARY_RUNTIME_DIR}/env.XXXXXX")"
  chmod 0600 "${ENV_FILE}"
  qa_docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "${APP_CONTAINER}" >"${ENV_FILE}"
}

qa_container_run() {
  qa_docker run --rm \
    --user="${QA_BOUNDARY_UID}:${QA_BOUNDARY_GID}" \
    --read-only --cap-drop=ALL --security-opt=no-new-privileges \
    --memory=1g --memory-swap=1g --cpus=0.20 --pids-limit=128 \
    --network="container:${APP_CONTAINER}" \
    --volume="${APP_DATA_SOURCE}:/app/data:ro" \
    --env-file="${ENV_FILE}" \
    "${APP_IMAGE}" /app/sub2api "$@"
}

run_export_orphan_scheduled() {
  [[ -r "${QA_EXPORT_ORPHAN_HELPER}" ]] || return 0
  local cutoff runtime
  cutoff="$(date -u +%Y-%m-%dT%H:%M:%S.000000Z)"
  runtime="$(qa_docker inspect "${APP_CONTAINER}" | python3 "${QA_EXPORT_ORPHAN_HELPER}" resolve-runtime \
    --container "${APP_CONTAINER}" --default-host "${APP_DATA_SOURCE}/qa_exports_tmp")"
  python3 "${QA_EXPORT_ORPHAN_HELPER}" action --mode scheduled --cutoff "${cutoff}" \
    --runtime-json "${runtime}" --proc-root "${QA_STALE_PROC_ROOT:-/proc}" \
    --activation-marker /var/lib/tokenkey/qa-export-orphan-cleanup-activated.json || return 0
}

run_qa_boundary() {
  local trigger="$1"
  local run_id started_at child_out child_err child_exit=0
  run_id="$(date -u +%Y%m%dT%H%M%SZ)-$$"
  started_at="$(qa_now)"
  load_app_runtime
  child_out="$(mktemp "${QA_BOUNDARY_RUNTIME_DIR}/child.out.XXXXXX")"
  child_err="$(mktemp "${QA_BOUNDARY_RUNTIME_DIR}/child.err.XXXXXX")"
  chmod 0600 "${child_out}" "${child_err}"
  set +e
  qa_container_run \
    --env="QA_MAINTENANCE_RUN_ID=${run_id}" \
    --env="QA_MAINTENANCE_TRIGGER=${trigger}" \
    --qa-boundary-once \
    --confirm=tokenkey-prod-qa-boundary-v1 \
    >"${child_out}" 2>"${child_err}"
  child_exit=$?
  set -e
  cat "${child_out}"
  run_export_orphan_scheduled || true
  python3 - "${QA_BOUNDARY_RECEIPT}" "${child_out}" "${child_exit}" "${run_id}" "${trigger}" "${started_at}" <<'PY'
import json, os, pathlib, sys, tempfile, datetime
target = pathlib.Path(sys.argv[1])
child_path = pathlib.Path(sys.argv[2])
child_exit = int(sys.argv[3])
run_id = sys.argv[4]
trigger = sys.argv[5]
started_at = sys.argv[6]
child = None
for raw in child_path.read_text(encoding="utf-8", errors="replace").splitlines():
    try:
        candidate = json.loads(raw.strip())
    except json.JSONDecodeError:
        continue
    if isinstance(candidate, dict):
        child = candidate
payload = {
    "schema_version": "qa-boundary-runner-v1",
    "run_id": run_id,
    "trigger": trigger,
    "started_at": started_at,
    "finished_at": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
    "child_exit_code": child_exit,
    "runner_exit_code": 0 if child_exit == 0 else child_exit,
    "boundary": child.get("boundary") if child else None,
    "deletion_authorized": child.get("deletion_authorized") if child else False,
}
target.parent.mkdir(parents=True, exist_ok=True)
fd, temp = tempfile.mkstemp(prefix=f".{target.name}.", dir=target.parent)
os.fchmod(fd, 0o600)
with os.fdopen(fd, "w", encoding="utf-8") as handle:
    json.dump(payload, handle, ensure_ascii=True, sort_keys=True, separators=(",", ":"))
    handle.write("\n")
os.replace(temp, target)
PY
  rm -f -- "${ENV_FILE}" "${child_out}" "${child_err}"
  return "${child_exit}"
}

main() {
  case "${1:-}" in
    --install-units)
      install_qa_boundary_units
      ;;
    --trigger=*)
      run_qa_boundary "${1#--trigger=}"
      ;;
    '')
      run_qa_boundary timer
      ;;
    *)
      echo "tokenkey-qa-boundary: unknown argument $1" >&2
      return 40
      ;;
  esac
}

main "$@"
