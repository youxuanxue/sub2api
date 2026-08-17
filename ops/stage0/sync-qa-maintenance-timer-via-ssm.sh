#!/usr/bin/env bash
# Install tokenkey-qa-maintenance.{sh,service,timer} on prod Stage0 via SSM.
#
# Usage:
#   QA_MAINTENANCE_TIMER_STATE=disabled \
#     bash ops/stage0/sync-qa-maintenance-timer-via-ssm.sh <instance-id> [comment]
#
# Default is fail-safe sync-only: install the runtime and keep the timer stopped.

set -euo pipefail

INSTANCE_ID="${1:-${INSTANCE_ID:-}}"
COMMENT="${2:-${SSM_COMMENT:-ops-qa-maintenance-timer-sync}}"
TIMEOUT_SECONDS="${STAGE0_SSM_TIMEOUT_SECONDS:-300}"
DRAIN_TIMEOUT_SECONDS="${QA_SYNC_DRAIN_TIMEOUT_SECONDS:-300}"
OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.}"
TIMER_STATE="${QA_MAINTENANCE_TIMER_STATE:-disabled}"

if [ -z "${INSTANCE_ID}" ]; then
  echo "sync_qa_maintenance_timer_via_ssm: instance id is required" >&2
  exit 1
fi
[[ "${DRAIN_TIMEOUT_SECONDS}" =~ ^[1-9][0-9]*$ ]] || {
  echo "QA_SYNC_DRAIN_TIMEOUT_SECONDS must be a positive integer" >&2
  exit 1
}
case "${TIMER_STATE}" in
  disabled)
    timer_command="sudo systemctl disable --now tokenkey-qa-maintenance.timer"
    timer_active_state="inactive"
    ;;
  enabled)
    timer_command="sudo systemctl enable --now tokenkey-qa-maintenance.timer"
    timer_active_state="active"
    ;;
  *)
    echo "QA_MAINTENANCE_TIMER_STATE must be disabled or enabled" >&2
    exit 1
    ;;
esac

REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-us-east-1}}"
ssm_region_args=(--region "${REGION}")

mkdir -p "${OUTPUT_DIR}"
params_file="${OUTPUT_DIR}/ssm-params.json"
stdout_file="${OUTPUT_DIR}/stdout.txt"
stderr_file="${OUTPUT_DIR}/stderr.txt"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ -n "${QA_HOST_ARTIFACT_ROOT:-}" ]; then
  ARTIFACT_ROOT="$(cd "${QA_HOST_ARTIFACT_ROOT}" && pwd)"
else
  ARTIFACT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
fi
MAINT_SRC="${ARTIFACT_ROOT}/deploy/aws/stage0/tokenkey-qa-maintenance.sh"
RESOLVER_SRC="${ARTIFACT_ROOT}/ops/lib/resolve-app-container.sh"
[[ -f "${MAINT_SRC}" ]] || { echo "missing ${MAINT_SRC}" >&2; exit 1; }
[[ -f "${RESOLVER_SRC}" ]] || { echo "missing ${RESOLVER_SRC}" >&2; exit 1; }

MAINT_SH_B64="$(base64 <"${MAINT_SRC}" | tr -d '\n')"
RESOLVER_SH_B64="$(base64 <"${RESOLVER_SRC}" | tr -d '\n')"
TEMPLATE_SHA="${QA_HOST_ARTIFACT_SHA:-${GITHUB_SHA:-local}}"

jq -n \
  --arg maint "${MAINT_SH_B64}" \
  --arg resolver "${RESOLVER_SH_B64}" \
  --arg sha "${TEMPLATE_SHA}" \
  --arg timer_command "${timer_command}" \
  --arg timer_state "${TIMER_STATE}" \
  --arg timer_active_state "${timer_active_state}" \
  --argjson drain_timeout "${DRAIN_TIMEOUT_SECONDS}" \
  '{
    commands: [
      "set -euo pipefail",
      "echo === qa-maintenance timer sync ===",
      "if sudo systemctl is-enabled --quiet tokenkey-qa-maintenance.timer; then qa_timer_enabled=1; else qa_timer_enabled=0; fi; if sudo systemctl is-active --quiet tokenkey-qa-maintenance.timer; then qa_timer_active=1; else qa_timer_active=0; fi; qa_sync_committed=0; qa_sync_restore() { qa_sync_rc=$?; trap - EXIT; if [ \"${qa_sync_committed}\" != 1 ]; then if [ \"${qa_timer_enabled}\" = 1 ]; then sudo systemctl enable tokenkey-qa-maintenance.timer >/dev/null 2>&1 || true; else sudo systemctl disable tokenkey-qa-maintenance.timer >/dev/null 2>&1 || true; fi; if [ \"${qa_timer_active}\" = 1 ]; then sudo systemctl start tokenkey-qa-maintenance.timer >/dev/null 2>&1 || true; else sudo systemctl stop tokenkey-qa-maintenance.timer >/dev/null 2>&1 || true; fi; fi; exit \"${qa_sync_rc}\"; }; trap qa_sync_restore EXIT",
      "if sudo systemctl list-unit-files tokenkey-qa-maintenance.timer --no-legend 2>/dev/null | grep -q \"^tokenkey-qa-maintenance[.]timer\"; then sudo systemctl disable --now tokenkey-qa-maintenance.timer; fi",
      "! sudo systemctl is-active --quiet tokenkey-qa-maintenance.timer",
      ("qa_sync_deadline=$(( $(date +%s) + " + ($drain_timeout | tostring) + " )); while sudo systemctl is-active --quiet tokenkey-qa-maintenance.service; do if [ \"$(date +%s)\" -ge \"${qa_sync_deadline}\" ]; then echo \"timeout draining tokenkey-qa-maintenance.service\" >&2; exit 1; fi; sleep 2; done"),
      "! sudo systemctl is-active --quiet tokenkey-qa-maintenance.service",
      ("echo " + $maint + " | base64 -d | sudo tee /usr/local/bin/tokenkey-qa-maintenance.sh > /dev/null"),
      "sudo chmod +x /usr/local/bin/tokenkey-qa-maintenance.sh",
      "sudo install -d -m 0755 /usr/local/lib/tokenkey",
      ("echo " + $resolver + " | base64 -d | sudo tee /usr/local/lib/tokenkey/resolve-app-container.sh > /dev/null"),
      "sudo chmod 0644 /usr/local/lib/tokenkey/resolve-app-container.sh",
      "sudo test -e /var/lib/tokenkey/app/qa_archive_tmp || sudo install -d -m 0700 -o 1000 -g 1000 /var/lib/tokenkey/app/qa_archive_tmp",
      "sudo test -e /var/lib/tokenkey/app/qa_blobs || sudo install -d -m 0700 -o 1000 -g 1000 /var/lib/tokenkey/app/qa_blobs",
      "sudo test -e /var/lib/tokenkey/app/qa_dlq || sudo install -d -m 0700 -o 1000 -g 1000 /var/lib/tokenkey/app/qa_dlq",
      "sudo test -e /var/lib/tokenkey/app/qa_capture_ledger || sudo install -d -m 0700 -o 1000 -g 1000 /var/lib/tokenkey/app/qa_capture_ledger",
      "sudo /usr/local/bin/tokenkey-qa-maintenance.sh --selftest",
      "unit_install_result=\"$(sudo /usr/local/bin/tokenkey-qa-maintenance.sh --install-units)\"; unit_install_changed=\"$(printf '%s' \"${unit_install_result}\" | python3 -c \u0027import json, sys; result = json.load(sys.stdin); valid = isinstance(result, dict) and set(result) == {\"changed\"} and isinstance(result[\"changed\"], bool); valid or sys.exit(\"invalid --install-units result\"); print(\"true\" if result[\"changed\"] else \"false\")\u0027)\" || { echo \"invalid tokenkey-qa-maintenance --install-units result\" >&2; exit 1; }; if [ \"${unit_install_changed}\" = \"true\" ]; then sudo systemctl daemon-reload; fi",
      $timer_command,
      ("test \"$(sudo systemctl is-enabled tokenkey-qa-maintenance.timer)\" = \"" + $timer_state + "\""),
      ("test \"$(sudo systemctl is-active tokenkey-qa-maintenance.timer)\" = \"" + $timer_active_state + "\""),
      "qa_sync_committed=1",
      "trap - EXIT",
      "sudo systemctl list-timers tokenkey-qa-maintenance.timer --no-pager || true",
      ("echo Live qa-maintenance units now match deploy/aws@" + $sha + " timer=" + $timer_state + " on $(hostname)")
    ]
  }' > "${params_file}"

cmd_id="$(aws "${ssm_region_args[@]}" ssm send-command \
  --instance-ids "${INSTANCE_ID}" \
  --document-name AWS-RunShellScript \
  --comment "${COMMENT}" \
  --parameters "file://${params_file}" \
  --query 'Command.CommandId' --output text)"

echo "ssm command-id=${cmd_id}"

deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
status="InProgress"
while [ "${status}" = "InProgress" ] || [ "${status}" = "Pending" ] || [ "${status}" = "Delayed" ]; do
  if [ "$(date +%s)" -ge "${deadline}" ]; then
    echo "timeout waiting for ssm command ${cmd_id}" >&2
    exit 1
  fi
  sleep 3
  status="$(aws "${ssm_region_args[@]}" ssm get-command-invocation \
    --command-id "${cmd_id}" \
    --instance-id "${INSTANCE_ID}" \
    --query Status --output text)"
done

aws "${ssm_region_args[@]}" ssm get-command-invocation \
  --command-id "${cmd_id}" \
  --instance-id "${INSTANCE_ID}" \
  --query StandardOutputContent --output text > "${stdout_file}"
aws "${ssm_region_args[@]}" ssm get-command-invocation \
  --command-id "${cmd_id}" \
  --instance-id "${INSTANCE_ID}" \
  --query StandardErrorContent --output text > "${stderr_file}"
code="$(aws "${ssm_region_args[@]}" ssm get-command-invocation \
  --command-id "${cmd_id}" \
  --instance-id "${INSTANCE_ID}" \
  --query ResponseCode --output text)"

cat "${stdout_file}"
if [ -s "${stderr_file}" ]; then
  echo "--- stderr ---" >&2
  cat "${stderr_file}" >&2
fi
if [ "${status}" != "Success" ] || [ "${code}" != "0" ]; then
  echo "ssm command failed status=${status} code=${code}" >&2
  exit 1
fi
