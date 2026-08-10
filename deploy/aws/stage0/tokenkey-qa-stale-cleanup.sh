#!/usr/bin/env bash
# Fixed 24-hour QA retention. Archive completeness is intentionally independent.
set -euo pipefail

RETENTION_HOURS=24
CONFIRM_PREFIX="tokenkey-prod-qa-retention-apply-v1:"
EXPORT_CONFIRM_PREFIX="tokenkey-prod-qa-export-orphan-apply-v1:"
TOKENKEY_ROOT="${TOKENKEY_ROOT:-/var/lib/tokenkey}"
PROD_EXPORT_HOST_DIR="/var/lib/tokenkey/app/qa_exports_tmp"
BLOB_ROOT="${TOKENKEY_ROOT}/app/qa_blobs"
DLQ_ROOT="${TOKENKEY_ROOT}/app/qa_dlq"
FIRST_PLAN_MARKER="${TOKENKEY_ROOT}/qa-stale-first-plan.json"
EXPORT_ACTIVATION_MARKER="${TOKENKEY_ROOT}/qa-export-orphan-cleanup-activated.json"
EXPORT_LOCK="${TOKENKEY_ROOT}/qa-export-orphan-cleanup.lock"
EXPORT_ORPHAN_HELPER="${EXPORT_ORPHAN_HELPER:-/usr/local/lib/tokenkey/qa-export-orphan.py}"
DELETE_BATCH_SIZE=5000

fail() {
  echo "tokenkey-qa-stale-cleanup: $*" >&2
  exit 2
}

require_runtime() {
  docker ps --format '{{.Names}}' | grep -qx tokenkey-postgres \
    || fail "tokenkey-postgres is not running"
}

ensure_roots() {
  install -d -m 0755 "${BLOB_ROOT}" "${DLQ_ROOT}"
}

psql_value() {
  docker exec tokenkey-postgres psql -U tokenkey -d tokenkey \
    -X -q -t -A -P pager=off -v ON_ERROR_STOP=1 -c "$1"
}

validate_cutoff() {
  [[ "$1" =~ ^20[0-9]{2}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{6}Z$ ]] \
    || fail "cutoff must be canonical UTC with microseconds"
}

file_count_before() {
  local root="$1" cutoff="$2"
  find "${root}" -type f ! -newermt "${cutoff}" -print 2>/dev/null | wc -l | tr -d ' '
}

active_container() {
  local color
  color="$(tr -d '[:space:]' <"${TOKENKEY_ROOT}/active-color")"
  [[ "${color}" == blue || "${color}" == green ]] || fail "active color is invalid"
  printf 'tokenkey-%s\n' "${color}"
}

active_image() {
  docker inspect "$(active_container)" --format '{{.Config.Image}}'
}

export_runtime() {
  local container default_export_host
  container="$(active_container)"
  if [[ "${TOKENKEY_ROOT}" == /var/lib/tokenkey ]]; then
    default_export_host="${PROD_EXPORT_HOST_DIR}"
  else
    default_export_host="${TOKENKEY_ROOT}/app/qa_exports_tmp"
  fi
  [[ -r "${EXPORT_ORPHAN_HELPER}" ]] || fail "QA export orphan helper is missing"
  docker inspect "${container}" | python3 "${EXPORT_ORPHAN_HELPER}" resolve-runtime \
    --container "${container}" --default-host "${default_export_host}"
}

export_orphan_action() {
  local mode="$1" cutoff="$2" expected_hash="${3:-}" runtime
  runtime="$(export_runtime)" || fail "QA export temp resolution failed"
  python3 "${EXPORT_ORPHAN_HELPER}" action --mode "${mode}" --cutoff "${cutoff}" \
    --runtime-json "${runtime}" --proc-root "${QA_STALE_PROC_ROOT:-/proc}" \
    --expected-hash "${expected_hash}" --activation-marker "${EXPORT_ACTIVATION_MARKER}"
}

export_activation_ready() {
  [[ -f "${EXPORT_ACTIVATION_MARKER}" ]] || return 1
  jq -e '
    .schema_version == "qa-export-orphan-activation-v1" and
    (.activated_plan_hash | type == "string" and test("^[0-9a-f]{64}$")) and
    (.activated_at | type == "string" and length > 0)
  ' "${EXPORT_ACTIVATION_MARKER}" >/dev/null
}

require_first_apply_timers() {
  [[ "$(systemctl is-enabled tokenkey-qa-stale-cleanup.timer)" == disabled ]] \
    || fail "QA stale cleanup timer must be disabled before first apply"
  [[ "$(systemctl is-active tokenkey-qa-stale-cleanup.timer)" == inactive ]] \
    || fail "QA stale cleanup timer must be inactive before first apply"
}

delete_rows_before() {
  local cutoff="$1" total=0 batch
  while true; do
    batch="$(psql_value "BEGIN;
      SET LOCAL lock_timeout='100ms';
      SET LOCAL statement_timeout='2min';
      DO \$cleanup\$ BEGIN
        IF NOT pg_try_advisory_xact_lock(1363234113) THEN
          RAISE EXCEPTION 'QA maintenance advisory lock is active';
        END IF;
      END \$cleanup\$;
      WITH batch AS (
        SELECT id,created_at FROM qa_records
        WHERE created_at < TIMESTAMPTZ '${cutoff}'
        ORDER BY created_at,id LIMIT ${DELETE_BATCH_SIZE}
        FOR UPDATE SKIP LOCKED
      ), deleted AS (
        DELETE FROM qa_records q USING batch b
        WHERE q.id=b.id AND q.created_at=b.created_at RETURNING 1
      ) SELECT count(*) FROM deleted;
      COMMIT;")"
    [[ "${batch}" =~ ^[0-9]+$ ]] || fail "QA delete batch returned an invalid count"
    total=$((total + batch))
    (( batch > 0 )) || break
  done
  printf '%s\n' "${total}"
}

bind_first_plan() {
  local cutoff="$1" rows="$2" blobs="$3" dlq="$4" image="$5" current
  if [[ -f "${FIRST_PLAN_MARKER}" ]]; then
    jq -e --arg cutoff "${cutoff}" --argjson rows "${rows}" --argjson blobs "${blobs}" \
      --argjson dlq "${dlq}" --arg image "${image}" \
      '.cutoff==$cutoff and .expected_rows==$rows and .expected_blob_files==$blobs and
       .expected_dlq_files==$dlq and .expected_active_image==$image' \
      "${FIRST_PLAN_MARKER}" >/dev/null || fail "a different first-run plan is already in progress"
    return
  fi
  current="${FIRST_PLAN_MARKER}.new"
  jq -cn --arg cutoff "${cutoff}" --argjson rows "${rows}" --argjson blobs "${blobs}" \
    --argjson dlq "${dlq}" --arg image "${image}" \
    '{cutoff:$cutoff,expected_rows:$rows,expected_blob_files:$blobs,
      expected_dlq_files:$dlq,expected_active_image:$image}' >"${current}"
  chmod 0600 "${current}"
  mv "${current}" "${FIRST_PLAN_MARKER}"
}

install_units() {
  local systemd_dir="${1:-/etc/systemd/system}"
  install -d -m 0755 "${systemd_dir}"
  cat >"${systemd_dir}/tokenkey-qa-stale-cleanup.service" <<'EOF'
[Unit]
Description=Prune QA records and files older than 24 hours
After=network-online.target tokenkey.service
Wants=network-online.target
Requires=tokenkey.service

[Service]
Type=oneshot
ExecStart=/usr/local/bin/tokenkey-qa-stale-cleanup.sh
Nice=15
IOSchedulingClass=idle
CPUQuota=20%
MemoryMax=1G
TasksMax=128
TimeoutStartSec=30min
NoNewPrivileges=true
ProtectHome=true
EOF
  cat >"${systemd_dir}/tokenkey-qa-stale-cleanup.timer" <<'EOF'
[Unit]
Description=Hourly QA 24-hour age retention

[Timer]
OnCalendar=*-*-* *:45:00
RandomizedDelaySec=15min
Persistent=true

[Install]
WantedBy=timers.target
EOF
}

build_plan() {
  require_runtime
  local db_json cutoff blob_files dlq_files image export_plan
  db_json="$(psql_value "
WITH bounds AS (
  SELECT clock_timestamp() AS server_clock,
         clock_timestamp() - interval '${RETENTION_HOURS} hours' AS cutoff
)
SELECT json_build_object(
  'server_clock', to_char(server_clock AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"'),
  'cutoff', to_char(cutoff AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"'),
  'candidate_rows', (SELECT count(*) FROM qa_records WHERE created_at < cutoff),
  'oldest_created_at', (SELECT to_char(min(created_at) AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"') FROM qa_records WHERE created_at < cutoff),
  'newest_created_at', (SELECT to_char(max(created_at) AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"') FROM qa_records WHERE created_at < cutoff),
  'export_jobs', json_build_object(
    'total_rows', (SELECT count(*) FROM qa_export_jobs),
    'expired_rows', (SELECT count(*) FROM qa_export_jobs WHERE expires_at < server_clock),
    'status_counts', COALESCE((SELECT json_object_agg(status, count) FROM (
      SELECT status, count(*) AS count FROM qa_export_jobs GROUP BY status
    ) grouped), '{}'::json),
    'done_without_storage_key', (SELECT count(*) FROM qa_export_jobs WHERE status='done' AND btrim(storage_key)=''),
    'non_done_with_storage_key', (SELECT count(*) FROM qa_export_jobs WHERE status<>'done' AND btrim(storage_key)<>'')
  )
)::text FROM bounds;")"
  cutoff="$(jq -r '.cutoff' <<<"${db_json}")"
  validate_cutoff "${cutoff}"
  blob_files="$(file_count_before "${BLOB_ROOT}" "${cutoff}")"
  dlq_files="$(file_count_before "${DLQ_ROOT}" "${cutoff}")"
  image="$(active_image)"
  export_plan="$(export_orphan_action plan "${cutoff}")"
  jq -cn \
    --argjson db "${db_json}" \
    --argjson blob_files "${blob_files}" \
    --argjson dlq_files "${dlq_files}" \
    --arg confirm "${CONFIRM_PREFIX}${cutoff}" \
    --arg active_image "${image}" \
    --argjson export_plan "${export_plan}" \
    '{mode:"prod_qa_age_retention_plan",environment:"prod",retention_hours:24,
      server_clock:$db.server_clock,cutoff:$db.cutoff,active_image:$active_image,candidate_rows:$db.candidate_rows,
      oldest_created_at:$db.oldest_created_at,newest_created_at:$db.newest_created_at,
      candidate_blob_files:$blob_files,candidate_dlq_files:$dlq_files,
      export_tmp:$export_plan,export_jobs:$db.export_jobs,
      required_confirmation:$confirm,deletion_authorized:false}'
}

apply_export_orphans() {
  local cutoff="$1" expected_image="$2" expected_hash="$3" confirm="$4"
  validate_cutoff "${cutoff}"
  [[ -n "${expected_image}" ]] || fail "expected active image is required"
  [[ "${expected_hash}" =~ ^[0-9a-f]{64}$ ]] || fail "expected export plan hash is invalid"
  [[ "${confirm}" == "${EXPORT_CONFIRM_PREFIX}${expected_hash}" ]] \
    || fail "export orphan plan confirmation mismatch"
  require_runtime
  exec 8>"${EXPORT_LOCK}"
  flock -n 8 || fail "another export orphan cleanup is active"
  [[ "$(active_image)" == "${expected_image}" ]] || fail "active image changed"
  export_orphan_action apply-activate "${cutoff}" "${expected_hash}"
}

apply_cutoff() {
  local mode="$1" cutoff="$2" expected_rows="$3" expected_blob_files="$4" expected_dlq_files="$5" expected_image="$6" confirm="$7"
  validate_cutoff "${cutoff}"
  [[ "${expected_rows}" =~ ^[0-9]+$ ]] || fail "expected rows must be a non-negative integer"
  [[ "${expected_blob_files}" =~ ^[0-9]+$ ]] || fail "expected blob files must be a non-negative integer"
  [[ "${expected_dlq_files}" =~ ^[0-9]+$ ]] || fail "expected dlq files must be a non-negative integer"
  [[ -n "${expected_image}" ]] || fail "expected active image is required"
  [[ "${confirm}" == "${CONFIRM_PREFIX}${cutoff}" ]] || fail "first-run confirmation mismatch"
  require_runtime
  ensure_roots
  exec 9>"${FIRST_PLAN_MARKER}.lock"
  flock -n 9 || fail "another first-run cleanup is active"
  if [[ "${mode}" == resume ]]; then
    [[ -f "${FIRST_PLAN_MARKER}" ]] || fail "no first-run cleanup is available to resume"
  else
    [[ ! -f "${FIRST_PLAN_MARKER}" ]] || fail "first-run cleanup requires --resume-first"
  fi
  require_first_apply_timers
  [[ "$(active_image)" == "${expected_image}" ]] || fail "active image changed"

  local proof actual_rows actual_blob_files actual_dlq_files
  proof="$(psql_value "
SELECT json_build_object(
  'ready', clock_timestamp() >= TIMESTAMPTZ '${cutoff}' + interval '${RETENTION_HOURS} hours',
  'fresh', clock_timestamp() <= TIMESTAMPTZ '${cutoff}' + interval '${RETENTION_HOURS} hours 10 minutes',
  'candidate_rows', (SELECT count(*) FROM qa_records WHERE created_at < TIMESTAMPTZ '${cutoff}')
)::text;")"
  [[ "$(jq -r '.ready' <<<"${proof}")" == true ]] || fail "first-run cutoff is in the future"
  actual_rows="$(jq -r '.candidate_rows' <<<"${proof}")"
  actual_blob_files="$(file_count_before "${BLOB_ROOT}" "${cutoff}")"
  actual_dlq_files="$(file_count_before "${DLQ_ROOT}" "${cutoff}")"
  if [[ "${mode}" == resume ]]; then
    bind_first_plan "${cutoff}" "${expected_rows}" "${expected_blob_files}" "${expected_dlq_files}" "${expected_image}"
    (( actual_rows <= expected_rows )) || fail "QA row candidates increased during first apply"
    (( actual_blob_files <= expected_blob_files )) || fail "QA blob candidates increased during first apply"
    (( actual_dlq_files <= expected_dlq_files )) || fail "QA dlq candidates increased during first apply"
  else
    [[ "$(jq -r '.fresh' <<<"${proof}")" == true ]] || fail "first-run plan is stale"
    [[ "${actual_rows}" == "${expected_rows}" ]] || fail "QA row candidates changed"
    [[ "${actual_blob_files}" == "${expected_blob_files}" ]] || fail "QA blob candidates changed"
    [[ "${actual_dlq_files}" == "${expected_dlq_files}" ]] || fail "QA dlq candidates changed"
    bind_first_plan "${cutoff}" "${expected_rows}" "${expected_blob_files}" "${expected_dlq_files}" "${expected_image}"
  fi

  local deleted remaining_rows remaining_blob_files remaining_dlq_files
  deleted="$(delete_rows_before "${cutoff}")"
  find "${BLOB_ROOT}" -type f ! -newermt "${cutoff}" -delete
  find "${DLQ_ROOT}" -type f ! -newermt "${cutoff}" -delete
  find "${BLOB_ROOT}" "${DLQ_ROOT}" -depth -mindepth 1 -type d -empty -delete
  remaining_rows="$(psql_value "SELECT count(*) FROM qa_records WHERE created_at < TIMESTAMPTZ '${cutoff}';")"
  remaining_blob_files="$(file_count_before "${BLOB_ROOT}" "${cutoff}")"
  remaining_dlq_files="$(file_count_before "${DLQ_ROOT}" "${cutoff}")"
  [[ "${remaining_rows}" == 0 ]] || fail "QA rows remain after first apply"
  [[ "${remaining_blob_files}" == 0 ]] || fail "QA blob files remain after first apply"
  [[ "${remaining_dlq_files}" == 0 ]] || fail "QA dlq files remain after first apply"
  local receipt_clock marker_sha256
  marker_sha256="$(sha256sum "${FIRST_PLAN_MARKER}" | awk '{print $1}')"
  [[ "${marker_sha256}" =~ ^[0-9a-f]{64}$ ]] || fail "first-run marker checksum is invalid"
  receipt_clock="$(psql_value "SELECT json_build_object(
    'applied_at',to_char(clock_timestamp() AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"'),
    'authorization_expires_at',to_char((clock_timestamp()+interval '10 minutes') AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"')
  )::text;")"
  logger -t tokenkey-qa-stale-cleanup "cleanup_done cutoff=${cutoff} planned_rows=${expected_rows} deleted_rows_this_attempt=${deleted} planned_blob_files=${expected_blob_files} planned_dlq_files=${expected_dlq_files}"
  jq -cn --arg cutoff "${cutoff}" --arg marker_sha256 "${marker_sha256}" \
    --argjson planned_rows "${expected_rows}" --argjson deleted_this_attempt "${deleted}" \
    --argjson planned_blob_files "${expected_blob_files}" --argjson planned_dlq_files "${expected_dlq_files}" \
    --argjson clock "${receipt_clock}" \
    '{mode:"prod_qa_age_retention_first_apply",cutoff:$cutoff,applied_at:$clock.applied_at,
      authorization_expires_at:$clock.authorization_expires_at,planned_rows:$planned_rows,
      deleted_rows_this_attempt:$deleted_this_attempt,planned_blob_files:$planned_blob_files,
      planned_dlq_files:$planned_dlq_files,remaining_rows:0,remaining_blob_files:0,
      remaining_dlq_files:0,marker_sha256:$marker_sha256,deletion_authorized:true}'
}

run_scheduled() {
  require_runtime
  [[ ! -f "${FIRST_PLAN_MARKER}" ]] || fail "first-run cleanup is incomplete"
  ensure_roots
  local cutoff
  cutoff="$(psql_value "SELECT to_char((clock_timestamp()-interval '${RETENTION_HOURS} hours') AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"');")"
  validate_cutoff "${cutoff}"
  local rows blobs dlq export_plan export_result export_hash
  rows="$(delete_rows_before "${cutoff}")"
  blobs="$(file_count_before "${BLOB_ROOT}" "${cutoff}")"
  dlq="$(file_count_before "${DLQ_ROOT}" "${cutoff}")"
  find "${BLOB_ROOT}" -type f ! -newermt "${cutoff}" -delete
  find "${DLQ_ROOT}" -type f ! -newermt "${cutoff}" -delete
  find "${BLOB_ROOT}" "${DLQ_ROOT}" -depth -mindepth 1 -type d -empty -delete
  exec 8>"${EXPORT_LOCK}"
  flock -n 8 || fail "another export orphan cleanup is active"
  export_plan="$(export_orphan_action plan "${cutoff}")"
  export_hash="$(jq -r '.plan_hash' <<<"${export_plan}")"
  if export_activation_ready; then
    export_result="$(export_orphan_action apply "${cutoff}" "${export_hash}")"
  else
    export_result="$(jq -c '. + {mode:"prod_qa_export_orphan_inventory",activation_required:true,deleted_count:0,deleted_bytes:0}' <<<"${export_plan}")"
  fi
  logger -t tokenkey-qa-stale-cleanup "cleanup_done cutoff=${cutoff} deleted_rows=${rows} blob_files=${blobs} dlq_files=${dlq} export_orphans=$(jq -r '.count // .planned_count' <<<"${export_result}") export_deleted=$(jq -r '.deleted_count' <<<"${export_result}")"
  jq -cn --arg cutoff "${cutoff}" --argjson rows "${rows}" --argjson blobs "${blobs}" \
    --argjson dlq "${dlq}" --argjson export_tmp "${export_result}" \
    '{mode:"prod_qa_age_retention_scheduled",cutoff:$cutoff,deleted_rows:$rows,
      deleted_blob_files:$blobs,deleted_dlq_files:$dlq,export_tmp:$export_tmp}'
}

case "${1:-}" in
  --install-units)
    [[ "$#" -le 2 ]] || fail "--install-units accepts at most one directory"
    install_units "${2:-/etc/systemd/system}"
    ;;
  --plan)
    [[ "$#" -eq 1 ]] || fail "--plan accepts no additional arguments"
    build_plan
    ;;
  --apply-first|--resume-first)
    operation="${1#--}"
    [[ "$#" -eq 13 ]] || fail "${1} requires cutoff, expected counts, active image, and confirmation"
    shift
    cutoff=""; expected_rows=""; expected_blob_files=""; expected_dlq_files=""; expected_image=""; confirm=""
    while [[ "$#" -gt 0 ]]; do
      case "$1" in
        --cutoff) cutoff="${2:-}" ;;
        --expected-rows) expected_rows="${2:-}" ;;
        --expected-blob-files) expected_blob_files="${2:-}" ;;
        --expected-dlq-files) expected_dlq_files="${2:-}" ;;
        --expected-active-image) expected_image="${2:-}" ;;
        --confirm) confirm="${2:-}" ;;
        *) fail "unknown first-run argument: $1" ;;
      esac
      shift 2
    done
    mode=apply
    [[ "${operation}" == resume-first ]] && mode=resume
    apply_cutoff "${mode}" "${cutoff}" "${expected_rows}" "${expected_blob_files}" "${expected_dlq_files}" "${expected_image}" "${confirm}"
    ;;
  --apply-export-orphans)
    [[ "$#" -eq 9 ]] || fail "--apply-export-orphans requires cutoff, active image, plan hash, and confirmation"
    shift
    cutoff=""; expected_image=""; expected_plan_hash=""; confirm=""
    while [[ "$#" -gt 0 ]]; do
      case "$1" in
        --cutoff) cutoff="${2:-}" ;;
        --expected-active-image) expected_image="${2:-}" ;;
        --expected-plan-hash) expected_plan_hash="${2:-}" ;;
        --confirm) confirm="${2:-}" ;;
        *) fail "unknown export orphan argument: $1" ;;
      esac
      shift 2
    done
    apply_export_orphans "${cutoff}" "${expected_image}" "${expected_plan_hash}" "${confirm}"
    ;;
  '')
    run_scheduled
    ;;
  *)
    fail "unknown command: $1"
    ;;
esac
