#!/usr/bin/env bash
#
# Guard: the SSM host command script must parse as shell.
#
# deploy_via_ssm.sh and sync_caddyfile_via_ssm.sh build a jq `commands` array
# and hand it to AWS-RunShellScript, which JOINS the elements into ONE shell
# script and runs it on the host. A local "the JSON is valid" check does NOT
# catch host-shell syntax errors, because the array is never executed locally —
# only AWS runs it. That blind spot let the #512 unquoted-parens-in-echo bug
#
#     echo === sync Caddyfile (kind=$KIND ...) ===
#
# ship to origin/main; it only surfaced when a us1 canary returned
# `syntax error near unexpected token '('` from the live host.
#
# This guard closes the gap: run each script with a stubbed `aws` so it emits
# its params file WITHOUT touching AWS, then `bash -n` the joined commands.
#
# Scope (explicit, not silent): covers the two prod/edge MUTATION primitives.
# edge_post_deploy_smoke.sh also builds an SSM `commands` array, but is left out
# on purpose — its array is assembled inside functions behind many runtime env
# vars (no clean "args -> params file" entrypoint to stub), and an unparseable
# smoke probe fails LOUDLY in CI/deploy rather than silently breaking prod. Add
# it here if that calculus changes.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${HERE}/../.." && pwd)"
OPS="${REPO_ROOT}/ops/stage0"

tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT

stub="${tmp}/bin"
mkdir -p "${stub}"
cat > "${stub}/aws" <<'STUB'
#!/usr/bin/env bash
# Minimal aws stub: just enough for the SSM scripts to emit their params file
# and reach a clean exit without contacting AWS.
case "$*" in
  *send-command*)                    echo "cmd-stub" ;;
  *get-command-invocation*Status*)   echo "Success" ;;
  *list-command-invocations*)        echo '{"CommandInvocations":[{"InstanceId":"mi-stub"}]}' ;;
  *)                                 echo "stub" ;;
esac
STUB
chmod +x "${stub}/aws"

rc=0

# check_one <label> <out-subdir> <script-and-args...>
check_one() {
  local label="$1" out="$2"
  shift 2
  mkdir -p "${tmp}/${out}"
  # We only need the params file; the stub run may exit nonzero (no real AWS).
  # The missing-file case is handled explicitly below.
  PATH="${stub}:${PATH}" STAGE0_SSM_OUTPUT_DIR="${tmp}/${out}" AWS_REGION=us-east-1 \
    "$@" >/dev/null 2>"${tmp}/${out}/err" || true  # preflight-allow: swallow
  local pf="${tmp}/${out}/ssm-params.json"
  if [[ ! -f "${pf}" ]]; then
    echo "  FAIL: ${label} — no ssm-params.json emitted (stub run aborted before params generation)" >&2
    tail -3 "${tmp}/${out}/err" 2>/dev/null | sed 's/^/      /' >&2 || true  # preflight-allow: swallow (diagnostic only)
    rc=1
    return
  fi
  if ! jq -r '.commands[]' "${pf}" | bash -n 2>"${tmp}/${out}/parse"; then
    echo "  FAIL: ${label} — joined host command script has a shell syntax error:" >&2
    sed 's/^/      /' "${tmp}/${out}/parse" >&2
    rc=1
    return
  fi
  echo "  ok: ${label} host script parses"
}

check_one "deploy_via_ssm.sh"               deploy    bash "${OPS}/deploy_via_ssm.sh" 1.0.0 i-0stub probe
check_one "sync_caddyfile_via_ssm.sh prod"  sync-prod bash "${OPS}/sync_caddyfile_via_ssm.sh" prod i-0stub probe
check_one "sync_caddyfile_via_ssm.sh edge"  sync-edge bash "${OPS}/sync_caddyfile_via_ssm.sh" edge i-0stub probe

exit "${rc}"
