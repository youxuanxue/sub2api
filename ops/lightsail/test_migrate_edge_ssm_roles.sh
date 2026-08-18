#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="${ROOT}/ops/lightsail/migrate-edge-ssm-roles.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT
mkdir -p "${tmp}/bin"

cat >"${tmp}/bin/backup-env" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
edge="${TK_ENV_SECRETS_PARAM#/tokenkey/edge/}"
edge="${edge%%/*}"
printf 'backup %s %s %s\n' "${edge}" "$1" "${AWS_REGION}" >>"${FAKE_ACTION_LOG}"
[[ "${FAKE_BACKUP_FAIL_EDGE:-}" != "${edge}" ]]
EOF

cat >"${tmp}/bin/run-probe" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf 'probe_args %s\n' "$*" >>"${FAKE_ACTION_LOG}"
target=''
instance=''
while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --target) target="$2"; shift 2 ;;
    --expected-instance-id) instance="$2"; shift 2 ;;
    *) shift ;;
  esac
done
edge="${target#edge:}"
printf 'recovery %s %s\n' "${edge}" "${instance}" >>"${FAKE_ACTION_LOG}"
if [[ "${FAKE_RECOVERY_FAIL_EDGE:-}" == "${edge}" ]]; then exit 88; fi
if [[ "${FAKE_INVALID_RECEIPT_EDGE:-}" == "${edge}" ]]; then
  echo '{"target":"edge:wrong","s3_round_trip_verified":false}'
else
  digest=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  printf '{"target":"edge:%s","s3_round_trip_verified":true,"source_local_sha256":"%s","artifact_sha256":"%s"}\n' "${edge}" "${digest}" "${digest}"
fi
EOF
chmod +x "${tmp}/bin/backup-env" "${tmp}/bin/run-probe"

cat >"${tmp}/bin/aws" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${FAKE_AWS_LOG}"
printf 'aws %s\n' "$*" >>"${FAKE_ACTION_LOG}"
arg_after() {
  local wanted="$1"; shift
  while [[ "$#" -gt 0 ]]; do
    if [[ "$1" == "${wanted}" ]]; then printf '%s\n' "$2"; return; fi
    shift
  done
  return 1
}
edge_from_text() {
  case "$*" in *us3*) echo us3;; *us4*) echo us4;; *us5*) echo us5;; *us6*) echo us6;; *) return 1;; esac
}
case "$*" in
  *"ssm get-parameter"*"ssm_managed_instance_id"*)
    edge="$(edge_from_text "$@")"
    if [[ "${FAKE_MISSING_EDGE:-}" == "${edge}" ]]; then echo ParameterNotFound >&2; exit 254; fi
    echo "mi-${edge}" ;;
  *"ssm list-tags-for-resource"*)
    resource="$(arg_after --resource-id "$@")"; echo "${resource#mi-}" ;;
  *"ssm describe-instance-information"*)
    edge="$(edge_from_text "$@")"
    if [[ -f "${FAKE_STATE_DIR}/${edge}" ]]; then echo "tokenkey-lightsail-ssm-hybrid-${edge}"; else echo tokenkey-lightsail-ssm-hybrid; fi ;;
  *"ssm update-managed-instance-role"*)
    instance="$(arg_after --instance-id "$@")"; edge="${instance#mi-}"
    if [[ "${FAKE_FAIL_EDGE:-}" == "${edge}" ]]; then exit 77; fi
    touch "${FAKE_STATE_DIR}/${edge}" ;;
  *"iam list-role-policies"*)
    if [[ -f "${FAKE_STATE_DIR}/deleted" ]]; then echo None; else echo EdgePgdumpPutOnly; fi ;;
  *"iam list-attached-role-policies"*) echo arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore ;;
  *"iam delete-role-policy"*) touch "${FAKE_STATE_DIR}/deleted" ;;
  *) echo "unexpected aws call: $*" >&2; exit 90 ;;
esac
EOF
chmod +x "${tmp}/bin/aws"

run_migration() {
  local name="$1" fail_edge="$2" missing_edge="$3" backup_fail_edge="${4:-}" recovery_fail_edge="${5:-}" invalid_receipt_edge="${6:-}"
  local case_dir="${tmp}/${name}"
  mkdir -p "${case_dir}/state"
  : >"${case_dir}/aws.log"
  PATH="${tmp}/bin:${PATH}" \
    FAKE_AWS_LOG="${case_dir}/aws.log" \
    FAKE_ACTION_LOG="${case_dir}/actions.log" \
    FAKE_STATE_DIR="${case_dir}/state" \
    FAKE_FAIL_EDGE="${fail_edge}" \
    FAKE_MISSING_EDGE="${missing_edge}" \
    FAKE_BACKUP_FAIL_EDGE="${backup_fail_edge}" \
    FAKE_RECOVERY_FAIL_EDGE="${recovery_fail_edge}" \
    FAKE_INVALID_RECEIPT_EDGE="${invalid_receipt_edge}" \
    EDGE_BACKUP_ENV_SCRIPT="${tmp}/bin/backup-env" \
    EDGE_RUN_PROBE_SCRIPT="${tmp}/bin/run-probe" \
    EDGE_SSM_ROLE_TIMEOUT_SECONDS=0 \
    EDGE_SSM_ROLE_POLL_SECONDS=0 \
    bash "${SCRIPT}" --apply
}

set +e
run_migration update-fails us5 '' >"${tmp}/update-fails.out" 2>"${tmp}/update-fails.err"
rc=$?
set -e
if [[ "${rc}" -eq 0 ]]; then
  echo 'FAIL: one failed role update must abort migration' >&2
  exit 1
fi
! grep -F 'iam delete-role-policy' "${tmp}/update-fails/aws.log" >/dev/null

set +e
run_migration missing-edge '' us4 >"${tmp}/missing.out" 2>"${tmp}/missing.err"
rc=$?
set -e
if [[ "${rc}" -eq 0 ]]; then
  echo 'FAIL: one missing managed instance must abort migration' >&2
  exit 1
fi
! grep -F 'iam delete-role-policy' "${tmp}/missing-edge/aws.log" >/dev/null

set +e
run_migration backup-fails '' '' us4 >"${tmp}/backup-fails.out" 2>"${tmp}/backup-fails.err"
rc=$?
set -e
if [[ "${rc}" -eq 0 ]]; then
  echo 'FAIL: rejected secret backup must abort migration' >&2
  exit 1
fi
! grep -F 'iam delete-role-policy' "${tmp}/backup-fails/aws.log" >/dev/null

set +e
run_migration recovery-fails '' '' '' us5 >"${tmp}/recovery-fails.out" 2>"${tmp}/recovery-fails.err"
rc=$?
set -e
if [[ "${rc}" -eq 0 ]]; then
  echo 'FAIL: failed dump round-trip or restore must abort migration' >&2
  exit 1
fi
! grep -F 'iam delete-role-policy' "${tmp}/recovery-fails/aws.log" >/dev/null

set +e
run_migration invalid-receipt '' '' '' '' us6 >"${tmp}/invalid-receipt.out" 2>"${tmp}/invalid-receipt.err"
rc=$?
set -e
if [[ "${rc}" -eq 0 ]]; then
  echo 'FAIL: invalid recovery receipt must abort migration' >&2
  exit 1
fi
! grep -F 'iam delete-role-policy' "${tmp}/invalid-receipt/aws.log" >/dev/null

run_migration success '' '' >"${tmp}/success.out"
grep -F 'iam delete-role-policy' "${tmp}/success/aws.log" >/dev/null
delete_line="$(grep -n 'iam delete-role-policy' "${tmp}/success/aws.log" | cut -d: -f1)"
for edge in us3 us4 us5 us6; do
  test -f "${tmp}/success/state/${edge}"
  role_line="$(grep -n "describe-instance-information.*mi-${edge}" "${tmp}/success/aws.log" | tail -1 | cut -d: -f1)"
  test "${role_line}" -lt "${delete_line}"
  grep -F "backup ${edge} mi-${edge}" "${tmp}/success/actions.log" >/dev/null
  grep -F "recovery ${edge} mi-${edge}" "${tmp}/success/actions.log" >/dev/null
  grep -F "probe_args --target edge:${edge} --expected-instance-id mi-${edge}" "${tmp}/success/actions.log" >/dev/null
  grep -F -- "--env CANARY_CREATE_DUMP=1" "${tmp}/success/actions.log" >/dev/null
  role_action_line="$(grep -n "aws ssm describe-instance-information.*mi-${edge}" "${tmp}/success/actions.log" | tail -1 | cut -d: -f1)"
  backup_line="$(grep -n "backup ${edge} mi-${edge}" "${tmp}/success/actions.log" | cut -d: -f1)"
  recovery_line="$(grep -n "recovery ${edge} mi-${edge}" "${tmp}/success/actions.log" | cut -d: -f1)"
  test "${role_action_line}" -lt "${backup_line}"
  test "${backup_line}" -lt "${recovery_line}"
  test "${recovery_line}" -lt "$(grep -n 'aws iam delete-role-policy' "${tmp}/success/actions.log" | cut -d: -f1)"
done
test -f "${tmp}/success/state/deleted"
grep -F 'shared role retirement verified' "${tmp}/success.out" >/dev/null

echo 'test_migrate_edge_ssm_roles: ok'
