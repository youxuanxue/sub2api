#!/usr/bin/env bash
#
# Stage0 data-wrappers install primitive (idempotent, service-untouched).
#
# Why this exists:
#   The unified data-layer CLI seams (tokenkey-psql / tokenkey-pg_dump /
#   tokenkey-redis-cli, single source deploy/aws/stage0/tokenkey-data-wrappers.sh)
#   are installed at instance FIRST boot by stage0-ec2-bootstrap.sh — but the
#   existing fleet (prod + all edges) never re-runs bootstrap. This script ships
#   the SAME repo file over SSM and runs it, so ops scripts can switch from
#   `docker exec tokenkey-postgres psql` to `tokenkey-psql` only AFTER every
#   target host has the wrappers (PR2 gate; see
#   docs/deploy/aws-data-layer-migration.md 阶段 A).
#
# What it does on the host:
#   1. base64-deliver tokenkey-data-wrappers.sh → run it (install -m 0755 ×3).
#   2. Verify: `tokenkey-psql -c 'select 1'` returns 1 and
#      `tokenkey-redis-cli ping` returns PONG against the CURRENT data layer
#      (local containers today; RDS after cutover — same wrappers, no change).
#   No service restart, no compose touch — pure file install + read-only probe.
#
# Usage (per target; loop over the fleet in the SOP):
#   ops/stage0/install_data_wrappers_via_ssm.sh <instance_id> [comment]
#   EDGE_ID=<edge> ops/stage0/install_data_wrappers_via_ssm.sh <mi-id> [comment]
#
# Env:
#   AWS_REGION / AWS_DEFAULT_REGION   region for SSM (optional)
#   EDGE_ID                           Lightsail Hybrid edge id; when set and
#                                     instance_id is mi-*, targets by tag like
#                                     deploy_via_ssm.sh / sync_caddyfile_via_ssm.sh.
#   STAGE0_SSM_TIMEOUT_SECONDS        SSM poll timeout (default 240)
#   STAGE0_SSM_OUTPUT_DIR             where to drop ssm-params/stdout/stderr

set -euo pipefail

INSTANCE_ID="${1:-${INSTANCE_ID:-}}"
COMMENT="${2:-${SSM_COMMENT:-install-data-wrappers}}"
TIMEOUT_SECONDS="${STAGE0_SSM_TIMEOUT_SECONDS:-240}"
OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.}"

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${HERE}/../.." && pwd)"
WRAPPERS_SRC="${REPO_ROOT}/deploy/aws/stage0/tokenkey-data-wrappers.sh"

# Shared SSM "resolve managed-instance after tag-targeted send" helper.
# shellcheck source=ssm_resolve_invocation_mi.inc.sh
source "${HERE}/ssm_resolve_invocation_mi.inc.sh"

if [[ -z "${INSTANCE_ID}" ]]; then
  echo "install_data_wrappers_via_ssm: instance id is required" >&2
  exit 1
fi
if [[ ! -f "${WRAPPERS_SRC}" ]]; then
  echo "install_data_wrappers_via_ssm: missing ${WRAPPERS_SRC}" >&2
  exit 1
fi

WRAPPERS_B64="$(base64 < "${WRAPPERS_SRC}" | tr -d '\n')"

ssm_region_args=()
if [[ -n "${AWS_REGION:-${AWS_DEFAULT_REGION:-}}" ]]; then
  ssm_region_args=(--region "${AWS_REGION:-${AWS_DEFAULT_REGION}}")
fi

mkdir -p "${OUTPUT_DIR}"
params_file="${OUTPUT_DIR}/ssm-params.json"
stdout_file="${OUTPUT_DIR}/stdout.txt"
stderr_file="${OUTPUT_DIR}/stderr.txt"

jq -n --arg b64 "${WRAPPERS_B64}" '{
  commands: [
    "set -euo pipefail",
    "echo \"=== install tokenkey data wrappers ===\"",
    ("printf '\''%s'\'' \"" + $b64 + "\" | base64 -d > /tmp/tokenkey-data-wrappers.sh"),
    "sudo bash /tmp/tokenkey-data-wrappers.sh",
    "rm -f /tmp/tokenkey-data-wrappers.sh",
    "echo \"=== verify (read-only probes against the current data layer) ===\"",
    "ONE=$(sudo tokenkey-psql -X -A -t -c \"select 1\")",
    "[ \"$ONE\" = \"1\" ] || { echo \"::error::tokenkey-psql probe failed (got: $ONE)\"; exit 1; }",
    "PONG=$(sudo tokenkey-redis-cli ping)",
    "[ \"$PONG\" = \"PONG\" ] || { echo \"::error::tokenkey-redis-cli probe failed (got: $PONG)\"; exit 1; }",
    "echo \"=== wrappers installed and verified ===\""
  ]
}' > "${params_file}"

eff_instance_id="${INSTANCE_ID}"
if [[ "${INSTANCE_ID}" == mi-* && -n "${EDGE_ID:-}" ]]; then
  cmd_id="$(aws "${ssm_region_args[@]}" ssm send-command \
    --targets "Key=tag:EdgeId,Values=${EDGE_ID}" "Key=tag:Platform,Values=lightsail" \
    --document-name AWS-RunShellScript \
    --comment "${COMMENT}" \
    --parameters "file://${params_file}" \
    --query 'Command.CommandId' --output text)"
  eff_instance_id="$(ssm_resolve_invocation_mi "${AWS_REGION:-${AWS_DEFAULT_REGION:-}}" "${cmd_id}")"
  if [[ "${eff_instance_id}" != "${INSTANCE_ID}" ]]; then
    echo "::warning::SSM send resolved instance ${eff_instance_id}; caller passed ${INSTANCE_ID}"
  fi
else
  cmd_id="$(aws "${ssm_region_args[@]}" ssm send-command \
    --instance-ids "${INSTANCE_ID}" \
    --document-name AWS-RunShellScript \
    --comment "${COMMENT}" \
    --parameters "file://${params_file}" \
    --query 'Command.CommandId' --output text)"
fi

echo "ssm command-id=${cmd_id}"
if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  echo "command_id=${cmd_id}" >> "${GITHUB_OUTPUT}"
fi

deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
status="InProgress"
while true; do
  status="$(aws "${ssm_region_args[@]}" ssm get-command-invocation \
    --command-id "${cmd_id}" --instance-id "${eff_instance_id}" \
    --query 'Status' --output text 2>/dev/null || echo InProgress)"
  case "${status}" in
    Success|Failed|TimedOut|Cancelled) break ;;
  esac
  if [[ $(date +%s) -ge ${deadline} ]]; then
    echo "::error::ssm timeout" >&2
    status="TimedOut"
    break
  fi
  sleep 5
done

aws "${ssm_region_args[@]}" ssm get-command-invocation \
  --command-id "${cmd_id}" --instance-id "${eff_instance_id}" \
  --query 'StandardOutputContent' --output text > "${stdout_file}"
aws "${ssm_region_args[@]}" ssm get-command-invocation \
  --command-id "${cmd_id}" --instance-id "${eff_instance_id}" \
  --query 'StandardErrorContent' --output text > "${stderr_file}"

echo '--- ssm stdout (last 8KB) ---'
tail -c 8192 "${stdout_file}"
echo
echo '--- ssm stderr (last 8KB) ---'
tail -c 8192 "${stderr_file}"
echo

if [[ "${status}" != "Success" ]]; then
  echo "::error::ssm command status=${status}" >&2
  exit 1
fi
