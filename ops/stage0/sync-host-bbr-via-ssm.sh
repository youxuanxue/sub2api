#!/usr/bin/env bash
# Push deploy/host/ensure-tokenkey-bbr.sh to one managed host and apply.
# Daily transport: TAT via execute_remote_params.py (TOKENKEY_REMOTE_VIA default).
# Break-glass: TOKENKEY_REMOTE_VIA=ssm.
#
# New hosts get BBR from bootstrap (EC2 user-data / prod bootstrap renderer).
# deploy-stage0 is an image hot-swap and does NOT re-run bootstrap, so this
# heal brings a pre-BBR instance in sync without replacing the host.
#
# Live prod is the Tencent USW Lighthouse host (SSM Hybrid mi-*). Pass `prod`
# to resolve instance + ssm_region through ops/stage0/resolve_prod_ssm_target.py
# (same owner as run-probe / deploy). An explicit i-*/mi-* id needs AWS_REGION.
#
# admin/proxies egress BBR is verified by scripts/checks/proxy_bbr_live.py
# (not this sync path; Tencent egress hosts are not SSM-managed).
#
# Usage:
#   ops/stage0/sync-host-bbr-via-ssm.sh prod|<instance_id> [comment]
# Env:
#   AWS_REGION / AWS_DEFAULT_REGION   required for an explicit instance id
#   STAGE0_SSM_TIMEOUT_SECONDS   default 300
#   STAGE0_SSM_OUTPUT_DIR        default .
set -euo pipefail

INSTANCE_ID="${1:-${INSTANCE_ID:-}}"
COMMENT="${2:-${SSM_COMMENT:-ops-host-bbr}}"
OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SSM_REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-}}"

if [ -z "${INSTANCE_ID}" ]; then
  echo "sync_host_bbr_via_ssm: target is required (prod or an SSM instance id)" >&2
  echo "usage: $0 prod|<instance_id> [comment]" >&2
  exit 1
fi

if [ "${INSTANCE_ID}" = prod ]; then
  export PROD_SSM_NODE="${PROD_SSM_NODE:-app-old}"
  RESOLVE_PROD="${SYNC_BBR_RESOLVE_PROD:-${SCRIPT_DIR}/resolve_prod_ssm_target.py}"
  if ! resolved="$(python3 "${RESOLVE_PROD}" --node "${PROD_SSM_NODE}" --target "${PROD_SSM_TARGET:-auto}" --format kv)"; then
    echo "sync_host_bbr_via_ssm: resolve_prod_ssm_target failed; refusing to guess a host" >&2
    exit 2
  fi
  INSTANCE_ID="$(printf '%s\n' "${resolved}" | awk -F= '$1=="instance_id" {print $2; exit}')"
  SSM_REGION="$(printf '%s\n' "${resolved}" | awk -F= '$1=="ssm_region" {print $2; exit}')"
fi

if [[ ! "$INSTANCE_ID" =~ ^(i|mi)-[a-zA-Z0-9]+$ ]]; then
  echo "sync_host_bbr_via_ssm: not an SSM-managed id: '${INSTANCE_ID}'" >&2
  echo "Tencent egress proxies (usw-eval*, cn-ip*) are not SSM-managed; run" >&2
  echo "deploy/host/ensure-tokenkey-bbr.sh on the host (fleet: deploy/hetzner/README.md)." >&2
  exit 2
fi

if [ -z "${SSM_REGION}" ] && [[ "${TOKENKEY_REMOTE_VIA:-tat}" == "ssm" ]]; then
  echo "sync_host_bbr_via_ssm: no SSM region; set AWS_REGION or pass 'prod' to use the resolver" >&2
  exit 2
fi
SSOT="${SCRIPT_DIR}/../../deploy/host/ensure-tokenkey-bbr.sh"
[[ -f "${SSOT}" ]] || { echo "missing ${SSOT}" >&2; exit 1; }

BBR_B64="$(base64 < "${SSOT}" | tr -d '\n')"
TEMPLATE_SHA="${GITHUB_SHA:-local}"

mkdir -p "${OUTPUT_DIR}"
params_file="${OUTPUT_DIR}/ssm-params.json"
stdout_file="${OUTPUT_DIR}/stdout.txt"
stderr_file="${OUTPUT_DIR}/stderr.txt"

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

# Host-parse / dry-run: params only.
if [[ -n "${STAGE0_SSM_OUTPUT_DIR:-}" || "${STAGE0_RENDER_ONLY:-}" == "1" ]]; then
  echo "[info] wrote ${params_file} (render-only; no remote)"
  exit 0
fi

export TOKENKEY_REMOTE_VIA="${TOKENKEY_REMOTE_VIA:-tat}"
python3 "${SCRIPT_DIR}/execute_remote_params.py" \
  --params-file "${params_file}" \
  --instance-id "${INSTANCE_ID}" \
  --comment "${COMMENT}" \
  --stdout-file "${stdout_file}" \
  --stderr-file "${stderr_file}"

echo '--- remote stdout (last 8KB) ---'
tail -c 8192 "${stdout_file}"
echo
echo '--- remote stderr (last 8KB) ---'
tail -c 8192 "${stderr_file}"
echo
