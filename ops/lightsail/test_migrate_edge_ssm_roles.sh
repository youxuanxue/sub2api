#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="${ROOT}/ops/lightsail/migrate-edge-ssm-roles.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT
mkdir -p "${tmp}/bin"

cat >"${tmp}/bin/aws" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${FAKE_AWS_LOG}"
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
  local name="$1" fail_edge="$2" missing_edge="$3"
  local case_dir="${tmp}/${name}"
  mkdir -p "${case_dir}/state"
  : >"${case_dir}/aws.log"
  PATH="${tmp}/bin:${PATH}" \
    FAKE_AWS_LOG="${case_dir}/aws.log" \
    FAKE_STATE_DIR="${case_dir}/state" \
    FAKE_FAIL_EDGE="${fail_edge}" \
    FAKE_MISSING_EDGE="${missing_edge}" \
    EDGE_SSM_ROLE_TIMEOUT_SECONDS=0 \
    EDGE_SSM_ROLE_POLL_SECONDS=0 \
    bash "${SCRIPT}" --apply
}

if run_migration update-fails us5 '' >"${tmp}/update-fails.out" 2>"${tmp}/update-fails.err"; then
  echo 'FAIL: one failed role update must abort migration' >&2
  exit 1
fi
! grep -F 'iam delete-role-policy' "${tmp}/update-fails/aws.log" >/dev/null

if run_migration missing-edge '' us4 >"${tmp}/missing.out" 2>"${tmp}/missing.err"; then
  echo 'FAIL: one missing managed instance must abort migration' >&2
  exit 1
fi
! grep -F 'iam delete-role-policy' "${tmp}/missing-edge/aws.log" >/dev/null

run_migration success '' '' >"${tmp}/success.out"
grep -F 'iam delete-role-policy' "${tmp}/success/aws.log" >/dev/null
delete_line="$(grep -n 'iam delete-role-policy' "${tmp}/success/aws.log" | cut -d: -f1)"
for edge in us3 us4 us5 us6; do
  test -f "${tmp}/success/state/${edge}"
  role_line="$(grep -n "describe-instance-information.*mi-${edge}" "${tmp}/success/aws.log" | tail -1 | cut -d: -f1)"
  test "${role_line}" -lt "${delete_line}"
done
test -f "${tmp}/success/state/deleted"
grep -F 'shared role retirement verified' "${tmp}/success.out" >/dev/null

echo 'test_migrate_edge_ssm_roles: ok'
