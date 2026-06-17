#!/usr/bin/env bash
# Push the host memory-pressure defenses into a running Stage0 prod EC2 via SSM
# Run-Command, WITHOUT rebuilding the instance (a CFN/user-data rebuild would
# replace the instance — see rule §9 / pin-AMI #804).
#
# Why this exists (2026-06-17 prod P0): an unbounded in-RAM qa export drove an
# 8 GiB box with NO swap into a page-cache-thrash half-deadlock that needed a
# reboot. #811 added two defenses to deploy/aws/stage0/stage0-ec2-bootstrap.sh:
#   (1) a /swapfile release valve + vm.swappiness/vfs_cache_pressure sysctl
#   (2) a memory-pressure Feishu alert embedded in tokenkey-disk-metrics.sh
# But those only run at instance bootstrap. `deploy-stage0` is an SSM image hot-
# swap — it does NOT re-run bootstrap — so an instance provisioned before #811
# stays without swap and runs the stale disk-metrics.sh (no mem alert) until it
# is recreated. This one-shot brings the LIVE instance in sync. The runtime
# root-cause fix (#808 async streamed qa export) ships in the image; this is the
# defense-in-depth layer.
#
# Single source of truth: both payloads are EXTRACTED from
# deploy/aws/stage0/stage0-ec2-bootstrap.sh at run time (the swap block between
# its "1b. swap"/"2a. attach" markers, and the tokenkey-disk-metrics.sh heredoc
# body). There is NO second copy of the swap/alert logic to drift — a future
# bootstrap edit is automatically what this pushes. Empty extraction = hard fail.
#
# Idempotent: the swap block skips if /swapfile is already active; the disk-
# metrics script is overwritten in place (its timer re-runs it every 5 min).
#
# Prod-only: the swap incident + the tokenkey-disk-metrics systemd timer are
# EC2/prod constructs. Edge Stage0 stacks are Lightsail anthropic-OAuth relays
# with a separate bootstrap and no such timer, so this is NOT wired for edges.
#
# Usage:
#   ops/stage0/sync-host-mem-guard-via-ssm.sh <instance_id> [comment]
# Env:
#   AWS_REGION / AWS_DEFAULT_REGION   region (else AWS default chain)
#   STAGE0_SSM_TIMEOUT_SECONDS        invocation wait budget (default 300)
#   STAGE0_SSM_OUTPUT_DIR             artifact dir (default .)

set -euo pipefail

INSTANCE_ID="${1:-${INSTANCE_ID:-}}"
COMMENT="${2:-${SSM_COMMENT:-ops-host-mem-guard}}"
TIMEOUT_SECONDS="${STAGE0_SSM_TIMEOUT_SECONDS:-300}"
OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.}"

if [ -z "${INSTANCE_ID}" ]; then
  echo "sync_host_mem_guard_via_ssm: instance id is required" >&2
  echo "usage: $0 <instance_id> [comment]" >&2
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BOOTSTRAP_SRC="${SCRIPT_DIR}/../../deploy/aws/stage0/stage0-ec2-bootstrap.sh"
[[ -f "${BOOTSTRAP_SRC}" ]] || { echo "missing ${BOOTSTRAP_SRC}" >&2; exit 1; }

# --- extract the canonical swap block (between the bootstrap markers) ---------
# Prints from "# --- 1b. swap" up to (not including) "# --- 2a. attach".
SWAP_BLOCK="$(awk '/# --- 1b\. swap \(memory-pressure release valve\)/{f=1} /# --- 2a\. attach \+ mount persistent data volume/{f=0} f' "${BOOTSTRAP_SRC}")"
if [ -z "${SWAP_BLOCK}" ]; then
  echo "::error::could not extract swap block from ${BOOTSTRAP_SRC} (markers moved?)" >&2
  exit 2
fi

# --- extract the canonical tokenkey-disk-metrics.sh body (heredoc payload) ----
# Prints the script body between the install heredoc opener and its DISKEOF.
DISK_METRICS_BODY="$(awk "/^install -m 0755 \/dev\/stdin \/usr\/local\/bin\/tokenkey-disk-metrics\.sh <<'DISKEOF'/{f=1;next} /^DISKEOF\$/{f=0} f" "${BOOTSTRAP_SRC}")"
if [ -z "${DISK_METRICS_BODY}" ]; then
  echo "::error::could not extract tokenkey-disk-metrics.sh body from ${BOOTSTRAP_SRC} (markers moved?)" >&2
  exit 2
fi
# Sanity: the body must carry the memory-pressure alert we are here to deliver.
if ! printf '%s' "${DISK_METRICS_BODY}" | grep -q 'memory-pressure alert'; then
  echo "::error::extracted disk-metrics.sh body lacks the memory-pressure alert — refusing to push a stale payload" >&2
  exit 2
fi

# Encode payloads to single-line base64 so embedding into the JSON command array
# is shell-quoting-safe. tr -d strips both GNU and BSD base64 wrapping.
SWAP_B64="$(printf '%s\n' "${SWAP_BLOCK}" | base64 | tr -d '\n')"
DM_B64="$(printf '%s\n' "${DISK_METRICS_BODY}" | base64 | tr -d '\n')"
TEMPLATE_SHA="${GITHUB_SHA:-local}"

mkdir -p "${OUTPUT_DIR}"
params_file="${OUTPUT_DIR}/ssm-params.json"
stdout_file="${OUTPUT_DIR}/stdout.txt"
stderr_file="${OUTPUT_DIR}/stderr.txt"

jq -n \
  --arg swap "${SWAP_B64}" \
  --arg dm "${DM_B64}" \
  --arg sha "${TEMPLATE_SHA}" \
  '{
    commands: [
      "set -euo pipefail",
      "echo === host mem-guard: swap release valve + memory-pressure alert ===",
      "echo === 1) swap setup (idempotent; skips if /swapfile already active) ===",
      ("echo " + $swap + " | base64 -d | sudo bash"),
      "echo --- swapon --show ---",
      "sudo swapon --show || echo NO_SWAP_ACTIVE",
      "echo --- /etc/sysctl.d/90-tokenkey-swap.conf ---",
      "cat /etc/sysctl.d/90-tokenkey-swap.conf 2>/dev/null || echo NO_SYSCTL_CONF",
      "echo --- meminfo ---",
      "grep -E '\''MemTotal|MemAvailable|SwapTotal'\'' /proc/meminfo",
      "echo === 2) refresh tokenkey-disk-metrics.sh (adds memory-pressure alert) ===",
      ("echo " + $dm + " | base64 -d | sudo tee /usr/local/bin/tokenkey-disk-metrics.sh > /dev/null"),
      "sudo chmod 0755 /usr/local/bin/tokenkey-disk-metrics.sh",
      "echo --- memory-pressure alert present in live script? (expect >=1) ---",
      "grep -c '\''memory-pressure alert'\'' /usr/local/bin/tokenkey-disk-metrics.sh || true",
      "echo --- tokenkey-disk-metrics.timer status ---",
      "sudo systemctl is-active tokenkey-disk-metrics.timer || echo TIMER_NOT_ACTIVE",
      "echo --- run disk-metrics once now to surface metric + arm alert ---",
      "sudo /usr/local/bin/tokenkey-disk-metrics.sh || true",
      "echo --- in-place sync trace ---",
      ("echo Live /swapfile + tokenkey-disk-metrics.sh now match deploy/aws/stage0/stage0-ec2-bootstrap.sh@" + $sha + " on $(hostname)")
    ]
  }' > "${params_file}"

cmd_id="$(aws ssm send-command \
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
  status="$(aws ssm get-command-invocation \
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

aws ssm get-command-invocation \
  --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
  --query 'StandardOutputContent' --output text > "${stdout_file}"
aws ssm get-command-invocation \
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
