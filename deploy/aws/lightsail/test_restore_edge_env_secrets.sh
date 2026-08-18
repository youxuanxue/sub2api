#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SCRIPT="${ROOT}/deploy/aws/lightsail/restore-edge-env-secrets.sh"

tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT
fake_bin="${tmp}/bin"
mkdir -p "${fake_bin}"

cat >"${fake_bin}/aws" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${FAKE_AWS_LOG}"
case "${FAKE_AWS_RESULT:-success}" in
  success)
    cat "${FAKE_SSM_VALUE}"
    printf '\n'
    ;;
  notfound)
    echo 'ParameterNotFound' >&2
    exit 254
    ;;
  denied)
    echo 'AccessDeniedException' >&2
    exit 254
    ;;
esac
EOF
chmod +x "${fake_bin}/aws"

valid_value="${tmp}/valid"
printf '%s\n%s\n%s' \
  'JWT_SECRET=1111111111111111111111111111111111111111111111111111111111111111' \
  'POSTGRES_PASSWORD=222222222222222222222222222222222222222222222222' \
  'TOTP_ENCRYPTION_KEY=3333333333333333333333333333333333333333333333333333333333333333' >"${valid_value}"
expected_restored="${tmp}/expected-restored"
printf '%s\n' \
  'JWT_SECRET=1111111111111111111111111111111111111111111111111111111111111111' \
  'POSTGRES_PASSWORD=222222222222222222222222222222222222222222222222' \
  'TOTP_ENCRYPTION_KEY=3333333333333333333333333333333333333333333333333333333333333333' >"${expected_restored}"

run_restore() {
  local result="$1"
  local output="$2"
  PATH="${fake_bin}:${PATH}" \
    FAKE_AWS_LOG="${tmp}/aws.log" \
    FAKE_AWS_RESULT="${result}" \
    FAKE_SSM_VALUE="${valid_value}" \
    AWS_REGION=us-east-2 \
    bash "${SCRIPT}" \
      --parameter /tokenkey/edge/us3/stage0/env-secrets-backup \
      --output "${output}"
}

# A valid off-box value is restored exactly and locked to owner-only access.
restored="${tmp}/restored.secret"
run_restore success "${restored}" >"${tmp}/restore.out"
cmp -s "${expected_restored}" "${restored}"
test "$(stat -f '%Lp' "${restored}" 2>/dev/null || stat -c '%a' "${restored}")" = 600
grep -F 'edge env secrets restored from SSM' "${tmp}/restore.out" >/dev/null

# Existing host state is authoritative and must not make a network call.
: >"${tmp}/aws.log"
run_restore denied "${restored}" >"${tmp}/existing.out"
test ! -s "${tmp}/aws.log"
cmp -s "${expected_restored}" "${restored}"

# Only ParameterNotFound authorizes first-boot generation.
generated="${tmp}/generated.secret"
run_restore notfound "${generated}" >"${tmp}/generated.out"
grep -F 'edge env secrets generated for first provision' "${tmp}/generated.out" >/dev/null
grep -Eq '^JWT_SECRET=[0-9a-f]{64}$' "${generated}"
grep -Eq '^POSTGRES_PASSWORD=[0-9a-f]{48}$' "${generated}"
grep -Eq '^TOTP_ENCRYPTION_KEY=[0-9a-f]{64}$' "${generated}"

# IAM/network failures cannot silently rotate secrets by falling back to generation.
denied="${tmp}/denied.secret"
if run_restore denied "${denied}" >"${tmp}/denied.out" 2>"${tmp}/denied.err"; then
  echo 'FAIL: AccessDenied must fail closed' >&2
  exit 1
fi
test ! -e "${denied}"

# A malformed SecureString cannot become host state.
malformed="${tmp}/malformed.secret"
printf '%s\n' 'POSTGRES_PASSWORD=only-one-key' >"${tmp}/malformed-value"
if PATH="${fake_bin}:${PATH}" \
  FAKE_AWS_LOG="${tmp}/aws.log" \
  FAKE_AWS_RESULT=success \
  FAKE_SSM_VALUE="${tmp}/malformed-value" \
  AWS_REGION=us-east-2 \
  bash "${SCRIPT}" \
    --parameter /tokenkey/edge/us3/stage0/env-secrets-backup \
    --output "${malformed}" >"${tmp}/malformed.out" 2>"${tmp}/malformed.err"; then
  echo 'FAIL: malformed SSM value must fail closed' >&2
  exit 1
fi
test ! -e "${malformed}"

echo 'test_restore_edge_env_secrets: ok'
