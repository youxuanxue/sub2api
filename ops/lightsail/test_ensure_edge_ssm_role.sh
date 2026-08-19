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
  *"send-command"*)
    probe_count=0
    if [[ -f "${FAKE_PROBE_COUNT_FILE}" ]]; then
      probe_count="$(cat "${FAKE_PROBE_COUNT_FILE}")"
    fi
    probe_count=$((probe_count + 1))
    printf '%s\n' "${probe_count}" >"${FAKE_PROBE_COUNT_FILE}"
    printf 'cmd-%s\n' "${probe_count}"
    ;;
  *"get-command-invocation"*"StandardOutputContent"*)
    probe_count="$(cat "${FAKE_PROBE_COUNT_FILE}")"
    if [[ "${probe_count}" -ge "${FAKE_REMOTE_SWITCH_AFTER}" ]]; then
      printf 'arn:aws:sts::123456789012:assumed-role/%s/%s\n' "${FAKE_DESIRED_ROLE}" "${FAKE_INSTANCE_ID}"
    else
      printf 'arn:aws:sts::123456789012:assumed-role/%s/%s\n' "${FAKE_REMOTE_CURRENT_ROLE}" "${FAKE_INSTANCE_ID}"
    fi
    ;;
  *"get-command-invocation"*"Status"*) printf '%s\n' Success ;;
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
  local name="$1" edge_id="$2" current_role="$3" update_sticks="$4" remote_switch_after="$5"
  shift 5
  local case_dir="${tmp}/${name}"
  mkdir -p "${case_dir}"
  PATH="${tmp}/bin:${PATH}" \
    FAKE_AWS_LOG="${case_dir}/aws.log" \
    FAKE_UPDATED_FILE="${case_dir}/updated" \
    FAKE_PROBE_COUNT_FILE="${case_dir}/probe-count" \
    FAKE_EDGE_ID="${FAKE_EDGE_ID_OVERRIDE:-${edge_id}}" \
    FAKE_CURRENT_ROLE="${current_role}" \
    FAKE_REMOTE_CURRENT_ROLE=tokenkey-lightsail-ssm-hybrid \
    FAKE_DESIRED_ROLE="tokenkey-lightsail-ssm-hybrid-${edge_id}" \
    FAKE_INSTANCE_ID=mi-0123456789abcdef0 \
    FAKE_UPDATE_STICKS="${update_sticks}" \
    FAKE_REMOTE_SWITCH_AFTER="${remote_switch_after}" \
    EDGE_SSM_ROLE_TIMEOUT_SECONDS="${EDGE_SSM_ROLE_TIMEOUT_SECONDS_OVERRIDE:-2}" \
    EDGE_SSM_ROLE_POLL_SECONDS=0 \
    bash "${SCRIPT}" "${edge_id}" mi-0123456789abcdef0 us-east-2 "$@"
}

run_case correct us3 tokenkey-lightsail-ssm-hybrid-us3 false 2 >"${tmp}/correct.out"
grep -F 'role credentials verified' "${tmp}/correct.out" >/dev/null
! grep -F 'update-managed-instance-role' "${tmp}/correct/aws.log" >/dev/null
test "$(grep -c 'send-command' "${tmp}/correct/aws.log")" -eq 2

run_case update us4 tokenkey-lightsail-ssm-hybrid true 2 >"${tmp}/update.out"
grep -F 'role credentials verified' "${tmp}/update.out" >/dev/null
grep -F 'update-managed-instance-role' "${tmp}/update/aws.log" >/dev/null
test "$(grep -c 'send-command' "${tmp}/update/aws.log")" -eq 2

EDGE_SSM_ROLE_TIMEOUT_SECONDS_OVERRIDE=0
export EDGE_SSM_ROLE_TIMEOUT_SECONDS_OVERRIDE
if run_case timeout us5 tokenkey-lightsail-ssm-hybrid false 1 >"${tmp}/timeout.out" 2>"${tmp}/timeout.err"; then
  echo 'FAIL: unchanged role must time out' >&2
  exit 1
fi
grep -F 'role update not observed' "${tmp}/timeout.err" >/dev/null

if run_case credentials-timeout us5 tokenkey-lightsail-ssm-hybrid true 999 >"${tmp}/credentials-timeout.out" 2>"${tmp}/credentials-timeout.err"; then
  echo 'FAIL: stale remote credentials must time out' >&2
  exit 1
fi
grep -F 'role credentials not observed' "${tmp}/credentials-timeout.err" >/dev/null
grep -F 'assumed-role/tokenkey-lightsail-ssm-hybrid/' "${tmp}/credentials-timeout.err" >/dev/null

FAKE_EDGE_ID_OVERRIDE=us6
export FAKE_EDGE_ID_OVERRIDE
if run_case wrong-edge us5 tokenkey-lightsail-ssm-hybrid false 1 >"${tmp}/wrong.out" 2>"${tmp}/wrong.err"; then
  echo 'FAIL: mismatched EdgeId tag must fail closed' >&2
  exit 1
fi
grep -F 'belongs to Edge us6, expected us5' "${tmp}/wrong.err" >/dev/null
! grep -F 'update-managed-instance-role' "${tmp}/wrong-edge/aws.log" >/dev/null

unset FAKE_EDGE_ID_OVERRIDE EDGE_SSM_ROLE_TIMEOUT_SECONDS_OVERRIDE
if run_case check-only us6 tokenkey-lightsail-ssm-hybrid false 1 --check >"${tmp}/check.out" 2>"${tmp}/check.err"; then
  echo 'FAIL: check mode must fail when the role is stale' >&2
  exit 1
fi
grep -F 'role mismatch' "${tmp}/check.err" >/dev/null
! grep -F 'update-managed-instance-role' "${tmp}/check-only/aws.log" >/dev/null
! grep -F 'send-command' "${tmp}/check-only/aws.log" >/dev/null

run_case check-correct us6 tokenkey-lightsail-ssm-hybrid-us6 false 999 --check >"${tmp}/check-correct.out"
grep -F 'role already correct' "${tmp}/check-correct.out" >/dev/null
! grep -F 'send-command' "${tmp}/check-correct/aws.log" >/dev/null

echo 'test_ensure_edge_ssm_role: ok'
