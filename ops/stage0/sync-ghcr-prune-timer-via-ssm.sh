#!/usr/bin/env bash
# LEGACY thin shell retained for host-parse / break-glass.
# Daily default is TAT: ops/stage0/sync_ghcr_prune_timer.py +
# .github/workflows/ops-stage0-ghcr-prune-timer.yml (tencent-ops).
# Daily transport here: TAT via execute_remote_params.py (TOKENKEY_REMOTE_VIA default).
# Break-glass: TOKENKEY_REMOTE_VIA=ssm.
# Do not wire this script back into the workflow default
# (docs/approved/design-tencent-ops-control-plane.md).
#
# Why it existed: deploy_via_ssm.sh pruned GHCR tags at deploy time only; without
# a daily timer stale tags piled up (2026-08 disk-full on us3/us4/us6).
# New prod boots still get the timer from deploy/stage0/stage0-ec2-bootstrap.sh.
#
# SSOT for the script body: deploy/stage0/tokenkey-ghcr-prune-daily.sh
#
# Usage (explicit emergency only):
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
GHCR_SRC="${SCRIPT_DIR}/../../deploy/stage0/tokenkey-ghcr-prune-daily.sh"
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

python3 "${SCRIPT_DIR}/ssm_wrap_bash_commands.py" "${params_file}"

if [[ -n "${STAGE0_SSM_OUTPUT_DIR:-}" || "${STAGE0_RENDER_ONLY:-}" == "1" ]]; then
  echo "sync_ghcr_prune_timer_via_ssm: render-only params=${params_file}"
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
