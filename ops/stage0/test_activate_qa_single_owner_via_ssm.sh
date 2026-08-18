#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="${ROOT}/ops/stage0/activate-qa-single-owner-via-ssm.sh"

tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT
fake_bin="${tmp}/bin"
mkdir -p "${fake_bin}"

cat >"${fake_bin}/aws" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${FAKE_AWS_LOG}"
args="$*"
case "${args}" in
  *'ssm send-command'*) echo test-command-id ;;
  *'ssm get-command-invocation'*'Status'*) echo Success ;;
  *'ssm get-command-invocation'*'ResponseCode'*) echo 0 ;;
  *'ssm get-command-invocation'*'StandardOutputContent'*)
    if grep -F -- '--activate-single-owner' "${FAKE_AWS_LOG}" >/dev/null; then
      echo '{"ok":true,"phase":"single_owner_activate","plan_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}'
    else
      echo '{"schema_version":"qa-single-owner-activation-plan-v1","plan_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}'
    fi
    ;;
  *'ssm get-command-invocation'*'StandardErrorContent'*) ;;
  *) echo "unexpected aws invocation: ${args}" >&2; exit 2 ;;
esac
EOF
chmod +x "${fake_bin}/aws"

export PATH="${fake_bin}:${PATH}"
export FAKE_AWS_LOG="${tmp}/aws.log"
export STAGE0_SSM_OUTPUT_DIR="${tmp}/output"

bash "${SCRIPT}" plan i-0123456789abcdef0 >"${tmp}/plan.out"
grep -F '"plan_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"' "${tmp}/plan.out" >/dev/null
grep -F -- '--plan-single-owner' "${FAKE_AWS_LOG}" >/dev/null

: >"${FAKE_AWS_LOG}"
if bash "${SCRIPT}" activate i-0123456789abcdef0 \
  --plan-hash=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa \
  --confirm=wrong >"${tmp}/invalid.out" 2>"${tmp}/invalid.err"; then
  echo 'FAIL: confirmation mismatch must fail' >&2
  exit 1
fi
test ! -s "${FAKE_AWS_LOG}"

bash "${SCRIPT}" activate i-0123456789abcdef0 \
  --plan-hash=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa \
  --confirm=tokenkey-prod-qa-single-owner-activate-v1:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa \
  >"${tmp}/activate.out"
grep -F '"phase":"single_owner_activate"' "${tmp}/activate.out" >/dev/null
grep -F -- '--activate-single-owner' "${FAKE_AWS_LOG}" >/dev/null

echo 'test_activate_qa_single_owner_via_ssm: ok'
