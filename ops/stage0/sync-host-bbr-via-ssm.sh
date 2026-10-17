#!/usr/bin/env bash
# Live EC2 prod only: push deploy/host/ensure-tokenkey-bbr.sh via SSM and apply.
#
# New hosts get BBR from bootstrap (EC2 user-data / Hetzner prod renderer).
# deploy-stage0 is an image hot-swap and does NOT re-run bootstrap, so this
# heal brings a pre-BBR instance in sync without replacing the host.
#
# Proxy/egress BBR is owned by network ops — not this script.
#
# Usage:
#   ops/stage0/sync-host-bbr-via-ssm.sh <instance_id> [comment]
# Env:
#   AWS_REGION / AWS_DEFAULT_REGION
#   STAGE0_SSM_TIMEOUT_SECONDS   default 300
#   STAGE0_SSM_OUTPUT_DIR        default .
set -euo pipefail

INSTANCE_ID="${1:-${INSTANCE_ID:-}}"
COMMENT="${2:-${SSM_COMMENT:-ops-host-bbr}}"
TIMEOUT_SECONDS="${STAGE0_SSM_TIMEOUT_SECONDS:-300}"
OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.}"

ssm_region_args=()
if [ -n "${AWS_REGION:-${AWS_DEFAULT_REGION:-}}" ]; then
  ssm_region_args=(--region "${AWS_REGION:-${AWS_DEFAULT_REGION}}")
fi

if [ -z "${INSTANCE_ID}" ]; then
  echo "sync_host_bbr_via_ssm: instance id is required" >&2
  echo "usage: $0 <instance_id> [comment]" >&2
  exit 1
fi

if [[ ! "$INSTANCE_ID" =~ ^i-[a-zA-Z0-9]+$ ]]; then
  echo "EC2 prod payload only; hcloud host BBR belongs to deploy/hetzner/render-prod-bootstrap.sh" >&2
  exit 2
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SSOT="${SCRIPT_DIR}/../../deploy/host/ensure-tokenkey-bbr.sh"
[[ -f "${SSOT}" ]] || { echo "missing ${SSOT}" >&2; exit 1; }

BBR_B64="$(base64 < "${SSOT}" | tr -d '\n')"
TEMPLATE_SHA="${GITHUB_SHA:-local}"

mkdir -p "${OUTPUT_DIR}"
params_file="${OUTPUT_DIR}/ssm-params.json"
stdout_file="${OUTPUT_DIR}/stdout.txt"
stderr_file="${OUTPUT_DIR}/stderr.txt"

aws_ssm() {
  if [ "${#ssm_region_args[@]}" -gt 0 ]; then
    aws "${ssm_region_args[@]}" ssm "$@"
  else
    aws ssm "$@"
  fi
}

jq -n \
  --arg bbr "${BBR_B64}" \
  --arg sha "${TEMPLATE_SHA}" \
  '{
    commands: [
      "set -euo pipefail",
      "echo === host bbr: apply deploy/host/ensure-tokenkey-bbr.sh ===",
      "echo --- before ---",
      "sysctl net.ipv4.tcp_congestion_control net.core.default_qdisc net.ipv4.tcp_available_congestion_control 2>/dev/null || true",
      ("echo " + $bbr + " | base64 -d | sudo tee /usr/local/bin/ensure-tokenkey-bbr.sh > /dev/null"),
      "sudo chmod 0755 /usr/local/bin/ensure-tokenkey-bbr.sh",
      "sudo /usr/local/bin/ensure-tokenkey-bbr.sh --apply",
      "echo --- after ---",
      "sysctl net.ipv4.tcp_congestion_control net.core.default_qdisc net.ipv4.tcp_available_congestion_control",
      "echo --- conf files ---",
      "sudo cat /etc/modules-load.d/99-tokenkey-bbr.conf",
      "sudo cat /etc/sysctl.d/91-tokenkey-bbr.conf",
      "sudo /usr/local/bin/ensure-tokenkey-bbr.sh --check",
      ("echo Live BBR now matches deploy/host/ensure-tokenkey-bbr.sh@" + $sha + " on $(hostname)")
    ]
  }' > "${params_file}"

python3 "${SCRIPT_DIR}/ssm_wrap_bash_commands.py" "${params_file}"

cmd_id="$(aws_ssm send-command \
  --instance-ids "${INSTANCE_ID}" \
  --document-name AWS-RunShellScript \
  --comment "${COMMENT}" \
  --parameters "file://${params_file}" \
  --query 'Command.CommandId' --output text)"

echo "ssm command-id=${cmd_id}"
if [ -n "${GITHUB_OUTPUT:-}" ]; then
  echo "command_id=${cmd_id}" >> "${GITHUB_OUTPUT}"
fi

deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
status="InProgress"
while true; do
  status="$(aws_ssm get-command-invocation \
    --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
    --query 'Status' --output text 2>/dev/null || echo InProgress)"
  case "${status}" in
    Success|Failed|TimedOut|Cancelled) break ;;
  esac
  if [ "$(date +%s)" -ge "${deadline}" ]; then
    echo "::error::ssm timeout" >&2
    status="TimedOut"
    break
  fi
  sleep 5
done

aws_ssm get-command-invocation \
  --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
  --query 'StandardOutputContent' --output text > "${stdout_file}"
aws_ssm get-command-invocation \
  --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
  --query 'StandardErrorContent' --output text > "${stderr_file}"

echo '--- ssm stdout (last 8KB) ---'
tail -c 8192 "${stdout_file}"
echo
echo '--- ssm stderr (last 8KB) ---'
tail -c 8192 "${stderr_file}"
echo

if [ "${status}" != "Success" ]; then
  echo "::error::ssm command status=${status}" >&2
  exit 1
fi
