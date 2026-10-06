#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="${ROOT}/ops/lightsail/reregister-edge-hybrid.sh"
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT
mkdir -p "${tmp}/bin"
chmod 755 "${SCRIPT}"

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
case "$*" in
  *"send-command"*"probe Hybrid role credentials"*)
    echo cmd-probe
    ;;
  *"send-command"*"rebind Hybrid activation"*)
    echo cmd-register
    printf '%s\n' "$*" >>"${FAKE_REGISTER_LOG}"
    ;;
  *"get-command-invocation"*cmd-register*"Status"*) echo Failed ;;
  *"get-command-invocation"*"Status"*) echo Success ;;
  *"get-command-invocation"*"StandardOutputContent"*)
    instance="$(arg_after --instance-id "$@")"
    if [[ -f "${FAKE_BOUND_FILE}" ]]; then
      printf 'arn:aws:sts::123456789012:assumed-role/tokenkey-lightsail-ssm-hybrid-uk1/%s\n' "${instance}"
    else
      printf 'arn:aws:sts::123456789012:assumed-role/tokenkey-lightsail-ssm-hybrid/%s\n' "${instance}"
    fi
    ;;
  *"ssm create-activation"*)
    echo '{"ActivationId":"act-new","ActivationCode":"code-secret"}'
    touch "${FAKE_BOUND_FILE:-/tmp/tk-hybrid-bound}"
    ;;
  *"describe-instance-information"*"ActivationIds"*)
    echo mi-newuk1aaaaaaaaaaa
    ;;
  *"ssm put-parameter"*)
    printf '%s\n' "$*" >>"${FAKE_PUT_LOG}"
    ;;
  *"deregister-managed-instance"*)
    printf '%s\n' "$*" >>"${FAKE_DEREG_LOG}"
    ;;
  *) echo "unexpected aws call: $*" >&2; exit 90 ;;
esac
EOF
chmod +x "${tmp}/bin/aws"

run_script() {
  local name="$1"
  shift
  local case_dir="${tmp}/${name}"
  mkdir -p "${case_dir}"
  : >"${case_dir}/aws.log"
  : >"${case_dir}/register.log"
  : >"${case_dir}/put.log"
  : >"${case_dir}/dereg.log"
  PATH="${tmp}/bin:${PATH}" \
    FAKE_AWS_LOG="${case_dir}/aws.log" \
    FAKE_REGISTER_LOG="${case_dir}/register.log" \
    FAKE_PUT_LOG="${case_dir}/put.log" \
    FAKE_DEREG_LOG="${case_dir}/dereg.log" \
    FAKE_BOUND_FILE="${FAKE_BOUND_FILE:-${case_dir}/bound}" \
    EDGE_HYBRID_REREGISTER_TIMEOUT_SECONDS=2 \
    EDGE_HYBRID_REREGISTER_POLL_SECONDS=0 \
    bash "${SCRIPT}" uk1 mi-olduk1aaaaaaaaaaa eu-west-2 /tokenkey/lightsail/uk1 "$@"
}

FAKE_BOUND_FILE="${tmp}/already-bound"
touch "${FAKE_BOUND_FILE}"
run_script already >"${tmp}/already.out"
grep -F 'hybrid credentials already bound to tokenkey-lightsail-ssm-hybrid-uk1' "${tmp}/already.out" >/dev/null
! grep -F 'create-activation' "${tmp}/already/aws.log" >/dev/null
unset FAKE_BOUND_FILE

run_script rebind >"${tmp}/rebind.out"
grep -F 're-registering onto tokenkey-lightsail-ssm-hybrid-uk1' "${tmp}/rebind.out" >/dev/null
grep -F 'polling activation act-new' "${tmp}/rebind.out" >/dev/null
grep -F 'create-activation' "${tmp}/rebind/aws.log" >/dev/null
grep -F '/tokenkey/lightsail/uk1/ssm_managed_instance_id' "${tmp}/rebind/put.log" >/dev/null
grep -F 'mi-newuk1aaaaaaaaaaa' "${tmp}/rebind/put.log" >/dev/null
grep -F 'mi-olduk1aaaaaaaaaaa' "${tmp}/rebind/dereg.log" >/dev/null
grep -F 'managed instance mi-newuk1aaaaaaaaaaa Hybrid credentials bound' "${tmp}/rebind.out" >/dev/null
# Activation code must not leak to stdout.
! grep -F 'code-secret' "${tmp}/rebind.out" >/dev/null

echo 'test_reregister_edge_hybrid: ok'
