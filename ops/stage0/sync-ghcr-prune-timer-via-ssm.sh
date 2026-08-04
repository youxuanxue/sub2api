#!/usr/bin/env bash
# Push tokenkey-ghcr-prune-daily.{sh,service,timer} onto a running Stage0 host
# (prod EC2 or Lightsail edge) via SSM Run-Command, WITHOUT rebuilding the instance.
#
# Why: deploy_via_ssm.sh prunes GHCR tags at deploy time only; without a daily timer
# stale tags pile up between deploys (2026-08 disk-full incident on us3/us4/us6).
# New prod boots get the timer from deploy/aws/stage0/stage0-ec2-bootstrap.sh; edges
# get it from ops/stage0/sync-edge-host-units-via-ssm.sh (edge deploy workflow).
# This one-shot backfills LIVE hosts provisioned before the timer landed.
#
# SSOT for the script body: deploy/aws/stage0/tokenkey-ghcr-prune-daily.sh
#
# Usage:
#   bash ops/stage0/sync-ghcr-prune-timer-via-ssm.sh <instance-id> [comment]
#   AWS_REGION=us-east-1 bash ops/stage0/sync-ghcr-prune-timer-via-ssm.sh i-0abc...

set -euo pipefail

INSTANCE_ID="${1:-${INSTANCE_ID:-}}"
COMMENT="${2:-${SSM_COMMENT:-ops-ghcr-prune-timer-sync}}"
TIMEOUT_SECONDS="${STAGE0_SSM_TIMEOUT_SECONDS:-300}"
OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.}"

if [ -z "${INSTANCE_ID}" ]; then
  echo "sync_ghcr_prune_timer_via_ssm: instance id is required" >&2
  exit 1
fi

ssm_region_args=()
if [ -n "${AWS_REGION:-${AWS_DEFAULT_REGION:-}}" ]; then
  ssm_region_args=(--region "${AWS_REGION:-${AWS_DEFAULT_REGION}}")
fi

mkdir -p "${OUTPUT_DIR}"
params_file="${OUTPUT_DIR}/ssm-params.json"
stdout_file="${OUTPUT_DIR}/stdout.txt"
stderr_file="${OUTPUT_DIR}/stderr.txt"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GHCR_SRC="${SCRIPT_DIR}/../../deploy/aws/stage0/tokenkey-ghcr-prune-daily.sh"
[[ -f "${GHCR_SRC}" ]] || { echo "missing ${GHCR_SRC}" >&2; exit 1; }

GHCR_SH_B64="$(base64 <"${GHCR_SRC}" | tr -d '\n')"

TEMPLATE_SHA="${GITHUB_SHA:-local}"

jq -n \
  --arg ghcrs "${GHCR_SH_B64}" \
  --arg sha "${TEMPLATE_SHA}" \
  '{
    commands: [
      "set -euo pipefail",
      "echo === ghcr-prune-daily timer sync ===",
      ("echo " + $ghcrs + " | base64 -d | sudo tee /usr/local/bin/tokenkey-ghcr-prune-daily.sh > /dev/null"),
      "sudo chmod +x /usr/local/bin/tokenkey-ghcr-prune-daily.sh",
      "sudo /usr/local/bin/tokenkey-ghcr-prune-daily.sh --selftest",
      "sudo /usr/local/bin/tokenkey-ghcr-prune-daily.sh --install-units",
      "sudo systemctl daemon-reload",
      "sudo systemctl enable --now tokenkey-ghcr-prune-daily.timer",
      "sudo systemctl restart tokenkey-ghcr-prune-daily.timer",
      "echo --- timer ---",
      "sudo systemctl list-timers tokenkey-ghcr-prune-daily.timer --no-pager || true",
      ("echo Live ghcr-prune-daily timer now matches deploy/aws@" + $sha + " on $(hostname)")
    ]
  }' > "${params_file}"

cmd_id="$(aws "${ssm_region_args[@]}" ssm send-command \
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
  status="$(aws "${ssm_region_args[@]}" ssm get-command-invocation \
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

aws "${ssm_region_args[@]}" ssm get-command-invocation \
  --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
  --query 'StandardOutputContent' --output text > "${stdout_file}"
aws "${ssm_region_args[@]}" ssm get-command-invocation \
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
