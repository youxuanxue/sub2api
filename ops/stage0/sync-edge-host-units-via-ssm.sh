#!/usr/bin/env bash
# Bring a running Lightsail EDGE's host-level systemd units up to prod parity via
# SSM Run-Command. Mirrors ops/stage0/pg_dump_refresh_via_ssm.sh.
#
# Phase 1 closeout removed all Edge QA wiring (capture=false, no QA timer/script).
# This script now pushes only non-QA host units:
#   1. on-box disk-full + memory-pressure Feishu alerts (tokenkey-disk-metrics.sh)
#   2. Daily GHCR tag prune (tokenkey-ghcr-prune-daily.sh)
#
# Single source of record for the unit payloads:
#   - deploy/aws/lightsail/tokenkey-disk-metrics-edge.sh
#   - deploy/aws/stage0/tokenkey-ghcr-prune-daily.sh
#
# Usage:
#   bash ops/stage0/sync-edge-host-units-via-ssm.sh <instance-id|mi-...> [comment]

set -euo pipefail

INSTANCE_ID="${1:-${INSTANCE_ID:-}}"
COMMENT="${2:-${SSM_COMMENT:-ops-edge-host-units-sync}}"
TIMEOUT_SECONDS="${STAGE0_SSM_TIMEOUT_SECONDS:-300}"
OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.}"

if [ -z "${INSTANCE_ID}" ]; then
  echo "stage0_sync_edge_host_units_via_ssm: instance id is required" >&2
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
DM_SRC="${SCRIPT_DIR}/../../deploy/aws/lightsail/tokenkey-disk-metrics-edge.sh"
GHCR_SRC="${SCRIPT_DIR}/../../deploy/aws/stage0/tokenkey-ghcr-prune-daily.sh"
[[ -f "${DM_SRC}" ]] || { echo "missing ${DM_SRC}" >&2; exit 1; }
[[ -f "${GHCR_SRC}" ]] || { echo "missing ${GHCR_SRC}" >&2; exit 1; }

DM_SH_B64="$(base64 <"${DM_SRC}" | tr -d '\n')"
GHCR_SH_B64="$(base64 <"${GHCR_SRC}" | tr -d '\n')"

DM_SERVICE_B64="$(cat <<'DMSEOF' | base64 | tr -d '\n'
[Unit]
Description=tokenkey EDGE on-box disk-full and memory-pressure Feishu alerts
After=network-online.target tokenkey.service
Wants=network-online.target

[Service]
Type=oneshot
EnvironmentFile=-/var/lib/tokenkey/.env
ExecStart=/usr/local/bin/tokenkey-disk-metrics.sh
DMSEOF
)"

DM_TIMER_B64="$(cat <<'DMTEOF' | base64 | tr -d '\n'
[Unit]
Description=Fire tokenkey EDGE disk/memory pressure alerts every 5 minutes

[Timer]
OnBootSec=3min
OnUnitActiveSec=5min
RandomizedDelaySec=30
Persistent=true

[Install]
WantedBy=timers.target
DMTEOF
)"

TEMPLATE_SHA="${GITHUB_SHA:-local}"

jq -n \
  --arg dmsh "${DM_SH_B64}" \
  --arg dmsvc "${DM_SERVICE_B64}" \
  --arg dmtmr "${DM_TIMER_B64}" \
  --arg ghcrs "${GHCR_SH_B64}" \
  --arg sha "${TEMPLATE_SHA}" \
  '{
    commands: [
      "set -euo pipefail",
      "echo === edge host-units sync: disk/memory pressure alerts + GHCR daily prune, no QA ===",
      "sudo install -d -m 0755 /etc/tokenkey",
      ("echo " + $dmsh + " | base64 -d | sudo tee /usr/local/bin/tokenkey-disk-metrics.sh > /dev/null"),
      "sudo chmod +x /usr/local/bin/tokenkey-disk-metrics.sh",
      "grep -E -c '\''memory-pressure alert|MemAvailable|磁盘压力已恢复'\'' /usr/local/bin/tokenkey-disk-metrics.sh || true",
      "sudo /usr/local/bin/tokenkey-disk-metrics.sh --selftest",
      ("echo " + $dmsvc + " | base64 -d | sudo tee /etc/systemd/system/tokenkey-disk-metrics.service > /dev/null"),
      ("echo " + $dmtmr + " | base64 -d | sudo tee /etc/systemd/system/tokenkey-disk-metrics.timer > /dev/null"),
      ("echo " + $ghcrs + " | base64 -d | sudo tee /usr/local/bin/tokenkey-ghcr-prune-daily.sh > /dev/null"),
      "sudo chmod +x /usr/local/bin/tokenkey-ghcr-prune-daily.sh",
      "sudo /usr/local/bin/tokenkey-ghcr-prune-daily.sh --selftest",
      "sudo /usr/local/bin/tokenkey-ghcr-prune-daily.sh --install-units",
      "sudo systemctl daemon-reload",
      "sudo systemctl enable --now tokenkey-disk-metrics.timer",
      "sudo systemctl enable --now tokenkey-ghcr-prune-daily.timer",
      "sudo systemctl restart tokenkey-disk-metrics.timer tokenkey-ghcr-prune-daily.timer",
      "echo --- timers ---",
      "sudo systemctl list-timers tokenkey-disk-metrics.timer tokenkey-ghcr-prune-daily.timer --no-pager || true",
      "echo --- feishu webhook present in .env -- disk alert no-ops without it --",
      "grep -cE '\''^TOKENKEY_FEISHU_WEBHOOK_URL='\'' /var/lib/tokenkey/.env || true",
      ("echo Live edge host units now match deploy/aws@" + $sha + " on $(hostname)")
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
