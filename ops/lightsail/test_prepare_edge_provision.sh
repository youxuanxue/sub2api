#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="${ROOT}/ops/lightsail/prepare-edge-provision.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT
mkdir -p "${tmp}/bin"

cat >"${tmp}/bin/aws" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${FAKE_AWS_LOG}"
case "$*" in
  *"lightsail get-instance"*)
    if [[ "${FAKE_INSTANCE_EXISTS}" == true ]]; then echo '{"instance":{"name":"tokenkey-edge-us3"}}'; else echo NotFoundException >&2; exit 254; fi ;;
  *"ssm get-parameter"*"/instance_name"*)
    if [[ "${FAKE_MARKER_EXISTS}" == true ]]; then echo tokenkey-edge-us3; else echo ParameterNotFound >&2; exit 254; fi ;;
  *"ssm get-parameter"*"/ssm_managed_instance_id"*) echo mi-0123456789abcdef0 ;;
  *"ssm list-tags-for-resource"*) echo us3 ;;
  *"ssm describe-instance-information"*) echo tokenkey-lightsail-ssm-hybrid-us3 ;;
  *"ssm send-command"*) echo cmd-1 ;;
  *"ssm get-command-invocation"*"StandardOutputContent"*) echo 'verify: 3 secret line(s)' ;;
  *"ssm get-command-invocation"*"StandardErrorContent"*) : ;;
  *"ssm get-command-invocation"*) echo "${FAKE_BACKUP_STATUS}" ;;
  *) echo "unexpected aws call: $*" >&2; exit 90 ;;
esac
EOF
chmod +x "${tmp}/bin/aws"

run_prepare() {
  local name="$1" instance_exists="$2" marker_exists="$3" recreate="$4" backup_status="$5"
  local case_dir="${tmp}/${name}"
  mkdir -p "${case_dir}"
  PATH="${tmp}/bin:${PATH}" \
    FAKE_AWS_LOG="${case_dir}/aws.log" \
    FAKE_INSTANCE_EXISTS="${instance_exists}" \
    FAKE_MARKER_EXISTS="${marker_exists}" \
    FAKE_BACKUP_STATUS="${backup_status}" \
    RECREATE="${recreate}" \
    GITHUB_ENV="${case_dir}/github.env" \
    STAGE0_SSM_OUTPUT_DIR="${case_dir}/backup" \
    bash "${SCRIPT}" us3 tokenkey-edge-us3 /tokenkey/lightsail/us3 us-east-2 \
      tokenkey-lightsail-ssm-hybrid-us3
}

run_prepare first false false false Success >"${tmp}/first.out"
grep -Fx 'ALLOW_SECRET_GENERATE=true' "${tmp}/first/github.env" >/dev/null
grep -Fx 'RECREATE_BACKUP_VERIFIED=false' "${tmp}/first/github.env" >/dev/null
! grep -F 'send-command' "${tmp}/first/aws.log" >/dev/null

run_prepare recreate true true true Success >"${tmp}/recreate.out"
grep -Fx 'ALLOW_SECRET_GENERATE=false' "${tmp}/recreate/github.env" >/dev/null
grep -Fx 'RECREATE_BACKUP_VERIFIED=true' "${tmp}/recreate/github.env" >/dev/null
describe_line="$(grep -n 'describe-instance-information' "${tmp}/recreate/aws.log" | cut -d: -f1)"
backup_line="$(grep -n 'send-command' "${tmp}/recreate/aws.log" | cut -d: -f1)"
test "${describe_line}" -lt "${backup_line}"

if run_prepare backup-fails true true true Failed >"${tmp}/fail.out" 2>"${tmp}/fail.err"; then
  echo 'FAIL: failed backup must abort preparation' >&2
  exit 1
fi
test ! -e "${tmp}/backup-fails/github.env" || ! grep -F 'RECREATE_BACKUP_VERIFIED=true' "${tmp}/backup-fails/github.env" >/dev/null

echo 'test_prepare_edge_provision: ok'
