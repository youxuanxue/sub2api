#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="${ROOT}/ops/lightsail/ensure-edge-ssm-role.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT
mkdir -p "${tmp}/bin"

cat >"${tmp}/bin/aws" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${FAKE_AWS_LOG}"
case "$*" in
  *"list-tags-for-resource"*) printf '%s\n' "${FAKE_EDGE_ID}" ;;
  *"update-managed-instance-role"*)
    printf '%s\n' updated >"${FAKE_UPDATED_FILE}"
    ;;
  *"describe-instance-information"*)
    if [[ -f "${FAKE_UPDATED_FILE}" && "${FAKE_UPDATE_STICKS}" == true ]]; then
      printf '%s\n' "${FAKE_DESIRED_ROLE}"
    else
      printf '%s\n' "${FAKE_CURRENT_ROLE}"
    fi
    ;;
  *) echo "unexpected aws call: $*" >&2; exit 90 ;;
esac
EOF
chmod +x "${tmp}/bin/aws"

run_case() {
  local name="$1" edge_id="$2" current_role="$3" update_sticks="$4"
  shift 4
  local case_dir="${tmp}/${name}"
  mkdir -p "${case_dir}"
  PATH="${tmp}/bin:${PATH}" \
    FAKE_AWS_LOG="${case_dir}/aws.log" \
    FAKE_UPDATED_FILE="${case_dir}/updated" \
    FAKE_EDGE_ID="${FAKE_EDGE_ID_OVERRIDE:-${edge_id}}" \
    FAKE_CURRENT_ROLE="${current_role}" \
    FAKE_DESIRED_ROLE="tokenkey-lightsail-ssm-hybrid-${edge_id}" \
    FAKE_UPDATE_STICKS="${update_sticks}" \
    EDGE_SSM_ROLE_TIMEOUT_SECONDS=0 \
    EDGE_SSM_ROLE_POLL_SECONDS=0 \
    bash "${SCRIPT}" "${edge_id}" mi-0123456789abcdef0 us-east-2 "$@"
}

run_case correct us3 tokenkey-lightsail-ssm-hybrid-us3 false >"${tmp}/correct.out"
grep -F 'role already correct' "${tmp}/correct.out" >/dev/null
! grep -F 'update-managed-instance-role' "${tmp}/correct/aws.log" >/dev/null

run_case update us4 tokenkey-lightsail-ssm-hybrid true >"${tmp}/update.out"
grep -F 'role verified' "${tmp}/update.out" >/dev/null
grep -F 'update-managed-instance-role' "${tmp}/update/aws.log" >/dev/null

if run_case timeout us5 tokenkey-lightsail-ssm-hybrid false >"${tmp}/timeout.out" 2>"${tmp}/timeout.err"; then
  echo 'FAIL: unchanged role must time out' >&2
  exit 1
fi
grep -F 'role update not observed' "${tmp}/timeout.err" >/dev/null

FAKE_EDGE_ID_OVERRIDE=us6
export FAKE_EDGE_ID_OVERRIDE
if run_case wrong-edge us5 tokenkey-lightsail-ssm-hybrid false >"${tmp}/wrong.out" 2>"${tmp}/wrong.err"; then
  echo 'FAIL: mismatched EdgeId tag must fail closed' >&2
  exit 1
fi
grep -F 'belongs to Edge us6, expected us5' "${tmp}/wrong.err" >/dev/null
! grep -F 'update-managed-instance-role' "${tmp}/wrong-edge/aws.log" >/dev/null

unset FAKE_EDGE_ID_OVERRIDE
if run_case check-only us6 tokenkey-lightsail-ssm-hybrid false --check >"${tmp}/check.out" 2>"${tmp}/check.err"; then
  echo 'FAIL: check mode must fail when the role is stale' >&2
  exit 1
fi
grep -F 'role mismatch' "${tmp}/check.err" >/dev/null
! grep -F 'update-managed-instance-role' "${tmp}/check-only/aws.log" >/dev/null

echo 'test_ensure_edge_ssm_role: ok'
