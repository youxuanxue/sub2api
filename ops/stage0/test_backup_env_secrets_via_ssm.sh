#!/usr/bin/env bash
# Behavioral regression checks for the rendered host-side secret backup payload.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="${ROOT}/ops/stage0/backup-env-secrets-via-ssm.sh"

tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT
local_bin="${tmp}/local-bin"
remote_bin="${tmp}/remote-bin"
output_dir="${tmp}/output"
work_dir="${tmp}/work"
mkdir -p "${local_bin}" "${remote_bin}" "${output_dir}" "${work_dir}"

cat >"${local_bin}/aws" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
args="$*"
case "${args}" in
  *"ssm send-command"*)
    echo test-command-id
    ;;
  *"ssm get-command-invocation"*"StandardOutputContent"*)
    ;;
  *"ssm get-command-invocation"*"StandardErrorContent"*)
    ;;
  *"ssm get-command-invocation"*"Status"*)
    echo Success
    ;;
  *)
    echo "unexpected local aws invocation: ${args}" >&2
    exit 2
    ;;
esac
EOF
chmod +x "${local_bin}/aws"

PATH="${local_bin}:${PATH}" \
  STAGE0_SSM_OUTPUT_DIR="${output_dir}" \
  AWS_REGION=us-east-2 \
  bash "${SCRIPT}" mi-test rendered-payload-test >"${tmp}/outer.out"

remote_line="$(jq -r '.commands[1]' "${output_dir}/ssm-params.json")"
host_b64="$(printf '%s\n' "${remote_line}" | awk '{print $2}')"
host_rendered="${tmp}/host-rendered.sh"
host_script="${tmp}/host-under-test.sh"
printf '%s' "${host_b64}" | base64 -d >"${host_rendered}"

secret_file="${tmp}/env.secret"
cat >"${secret_file}" <<'EOF'
TOTP_ENCRYPTION_KEY=totp-test-secret
POSTGRES_PASSWORD=postgres-test-secret
JWT_SECRET=jwt-test-secret
EOF
expected_file="${tmp}/expected.secret"
LC_ALL=C sort "${secret_file}" >"${expected_file}"
expected_parameter="${tmp}/expected.parameter"
awk 'NR > 1 { printf "\n" } { printf "%s", $0 }' \
  "${expected_file}" >"${expected_parameter}"
sed "s#/var/lib/tokenkey/.env#${secret_file}#g" \
  "${host_rendered}" >"${host_script}"

cat >"${remote_bin}/aws" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
args="$*"
case "${args}" in
  *"ssm get-parameter"*)
    if [ ! -f "${FAKE_SSM_STATE}" ]; then
      echo "ParameterNotFound" >&2
      exit 254
    fi
    # AWS CLI --output text appends one newline after a scalar value.
    cat "${FAKE_SSM_STATE}"
    printf '\n'
    ;;
  *"ssm put-parameter"*)
    if [ "${FAKE_PUT_RESULT:-ok}" = fail ]; then
      echo "AccessDeniedException" >&2
      exit 254
    fi
    value_arg=""
    while [ "$#" -gt 0 ]; do
      if [ "$1" = "--value" ]; then
        value_arg="${2:-}"
        break
      fi
      shift
    done
    case "${value_arg}" in
      file://*) cp "${value_arg#file://}" "${FAKE_SSM_STATE}" ;;
      *) echo "missing file:// value" >&2; exit 2 ;;
    esac
    count=0
    if [ -f "${FAKE_PUT_COUNT}" ]; then
      count="$(cat "${FAKE_PUT_COUNT}")"
    fi
    echo "$((count + 1))" >"${FAKE_PUT_COUNT}"
    echo '{"Version":1}'
    ;;
  *)
    echo "unexpected remote aws invocation: ${args}" >&2
    exit 2
    ;;
esac
EOF
chmod +x "${remote_bin}/aws"

state_file="${tmp}/parameter"
put_count_file="${tmp}/put-count"

assert_work_dir_empty() {
  if find "${work_dir}" -mindepth 1 -print -quit | grep -q .; then
    echo "FAIL: plaintext temporary files were not removed" >&2
    exit 1
  fi
}

run_host() {
  local label="$1"
  local put_result="$2"
  PATH="${remote_bin}:${PATH}" \
    LC_ALL=C \
    TMPDIR="${work_dir}" \
    FAKE_SSM_STATE="${state_file}" \
    FAKE_PUT_COUNT="${put_count_file}" \
    FAKE_PUT_RESULT="${put_result}" \
    bash "${host_script}" >"${tmp}/${label}.out" 2>"${tmp}/${label}.err"
}

# Regression: a rejected write must make the SSM host payload fail.
rm -f "${state_file}" "${put_count_file}"
if run_host failure fail; then
  echo "FAIL: rejected PutParameter must make the host payload fail" >&2
  exit 1
fi
if grep -F 'secrets off-boxed' "${tmp}/failure.out" >/dev/null; then
  echo "FAIL: rejected PutParameter must not report success" >&2
  exit 1
fi
assert_work_dir_empty

# A first write persists and verifies the exact three source assignments.
rm -f "${state_file}" "${put_count_file}"
run_host first-write ok
cmp -s "${expected_parameter}" "${state_file}"
test "$(cat "${put_count_file}")" = 1
grep -F 'secrets off-boxed' "${tmp}/first-write.out" >/dev/null
grep -F 'verify: 3 secret line(s)' "${tmp}/first-write.out" >/dev/null
assert_work_dir_empty

# An unchanged value remains a no-op and still verifies successfully.
cp "${expected_parameter}" "${state_file}"
rm -f "${put_count_file}"
run_host unchanged ok
test ! -e "${put_count_file}"
grep -F 'secrets unchanged; no new SSM version written' "${tmp}/unchanged.out" >/dev/null
grep -F 'verify: 3 secret line(s)' "${tmp}/unchanged.out" >/dev/null
assert_work_dir_empty

if grep -R -F -e 'postgres-test-secret' -e 'jwt-test-secret' -e 'totp-test-secret' \
    "${tmp}"/*.out "${tmp}"/*.err >/dev/null; then
  echo "FAIL: command output leaked a secret value" >&2
  exit 1
fi

echo "test_backup_env_secrets_via_ssm: ok"
