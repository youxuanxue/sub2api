#!/usr/bin/env bash
# Install the QA hourly boundary runtime and optionally switch from legacy cleanup.
set -euo pipefail

INSTANCE_ID="${1:-${INSTANCE_ID:-}}"
COMMENT="${2:-${SSM_COMMENT:-ops-qa-boundary-timer-sync}}"
TIMEOUT_SECONDS="${STAGE0_SSM_TIMEOUT_SECONDS:-300}"
DRAIN_TIMEOUT_SECONDS="${QA_SYNC_DRAIN_TIMEOUT_SECONDS:-300}"
OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.}"
TIMER_STATE="${QA_BOUNDARY_TIMER_STATE:-disabled}"
REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-us-east-1}}"

# QA_BOUNDARY_TIMER_STATE: disabled | enabled | auto
[[ "${INSTANCE_ID}" =~ ^i-[0-9a-f]{17}$ ]] || { echo "valid instance id required" >&2; exit 1; }
[[ "${DRAIN_TIMEOUT_SECONDS}" =~ ^[1-9][0-9]*$ ]] || { echo "QA_SYNC_DRAIN_TIMEOUT_SECONDS must be a positive integer" >&2; exit 1; }
case "${TIMER_STATE}" in
  disabled)
    owner_command="qa_apply_requested_owner"
    boundary_active=inactive
    ;;
  enabled|auto)
    owner_command="qa_apply_requested_owner"
    boundary_active=receipt-aware
    ;;
  *)
    echo "QA_BOUNDARY_TIMER_STATE must be disabled, enabled, or auto" >&2
    exit 1
    ;;
esac

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ -n "${QA_HOST_ARTIFACT_ROOT:-}" ]; then
  ARTIFACT_ROOT="$(cd "${QA_HOST_ARTIFACT_ROOT}" && pwd)"
else
  ARTIFACT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
fi
BOUNDARY_SRC="${ARTIFACT_ROOT}/deploy/aws/stage0/tokenkey-qa-boundary.sh"
RESOLVER_SRC="${ARTIFACT_ROOT}/ops/lib/resolve-app-container.sh"
for source in "${BOUNDARY_SRC}" "${RESOLVER_SRC}"; do
  [[ -f "${source}" ]] || { echo "missing ${source}" >&2; exit 1; }
done

boundary_payload="$(gzip -9n -c "${BOUNDARY_SRC}" | base64 | tr -d '\n')"
resolver_payload="$(gzip -9n -c "${RESOLVER_SRC}" | base64 | tr -d '\n')"
artifact_sha="${QA_HOST_ARTIFACT_SHA:-${GITHUB_SHA:-local}}"
mkdir -p "${OUTPUT_DIR}"
params="${OUTPUT_DIR}/ssm-params.json"
stdout="${OUTPUT_DIR}/stdout.txt"
stderr="${OUTPUT_DIR}/stderr.txt"

jq -n \
  --arg boundary "${boundary_payload}" \
  --arg resolver "${resolver_payload}" \
  --arg owner_command "${owner_command}" \
  --arg timer_state "${TIMER_STATE}" \
  --arg boundary_active "${boundary_active}" \
  --arg artifact_sha "${artifact_sha}" \
  --argjson drain_timeout "${DRAIN_TIMEOUT_SECONDS}" '
  def atomic_install($artifact; $destination; $mode):
    "sudo bash -c " + (
      (
        "set -euo pipefail; destination=" + ($destination | @sh)
        + "; directory=$(dirname \"$destination\"); install -d -m 0755 \"$directory\""
        + "; temporary=$(mktemp \"$directory/.${destination##*/}.XXXXXX\")"
        + "; trap \"rm -f \\\"$temporary\\\"\" EXIT"
        + "; printf %s " + ($artifact | @sh) + " | base64 -d | gunzip > \"$temporary\""
        + "; chmod " + $mode + " \"$temporary\"; mv -f \"$temporary\" \"$destination\""
      ) | @sh
    );
  {commands:[
    "set -euo pipefail",
    "sudo install -d -m 0755 /run/lock; exec 9>/run/lock/tokenkey-qa-lifecycle.lock; flock -x 9",
    ("qa_requested_timer_state=" + ($timer_state | @sh) + "; qa_query_single_owner() { local active; if ! active=$(docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1 -c \"SELECT count(*) FROM qa_lifecycle_receipts WHERE phase=concat(chr(115),chr(105),chr(110),chr(103),chr(108),chr(101),chr(95),chr(111),chr(119),chr(110),chr(101),chr(114),chr(95),chr(97),chr(99),chr(116),chr(105),chr(118),chr(97),chr(116),chr(101))\" | tr -d \"[:space:]\"); then echo \"single-owner activation receipt query failed\" >&2; return 1; fi; case \"${active}\" in 0|1) printf \"%s\\n\" \"${active}\" ;; *) echo \"invalid single-owner activation receipt count: ${active}\" >&2; return 1 ;; esac; }; qa_boundary_disabled_inactive() { local enabled active; if enabled=$(sudo systemctl is-enabled tokenkey-qa-boundary.timer 2>/dev/null); then :; else :; fi; if active=$(sudo systemctl is-active tokenkey-qa-boundary.timer 2>/dev/null); then :; else :; fi; [ \"${enabled}\" = disabled ] && [ \"${active}\" = inactive ]; }; qa_force_boundary_disabled() { if ! sudo systemctl disable --now tokenkey-qa-boundary.timer; then echo \"failed to disable QA boundary timer\" >&2; return 1; fi; if ! qa_boundary_disabled_inactive; then echo \"QA boundary timer is not verified disabled and inactive\" >&2; return 1; fi; }; qa_require_pre_activation() { local active; if ! active=$(qa_query_single_owner); then if qa_force_boundary_disabled; then :; else echo \"failed to force QA boundary disabled after receipt query failure\" >&2; fi; return 1; fi; if [ \"${active}\" != 0 ]; then if qa_force_boundary_disabled; then :; else echo \"failed to force activated QA boundary disabled\" >&2; fi; return 1; fi; }; qa_restore_enabled_state() { if ! qa_require_pre_activation; then return 1; fi; if [ \"${qa_boundary_enabled}\" = 1 ]; then if ! sudo systemctl enable tokenkey-qa-boundary.timer; then qa_force_boundary_disabled; return 1; fi; if ! sudo systemctl is-enabled --quiet tokenkey-qa-boundary.timer; then qa_force_boundary_disabled; return 1; fi; else if ! sudo systemctl disable tokenkey-qa-boundary.timer; then qa_force_boundary_disabled; return 1; fi; local enabled; if enabled=$(sudo systemctl is-enabled tokenkey-qa-boundary.timer 2>/dev/null); then :; else :; fi; if [ \"${enabled}\" != disabled ]; then qa_force_boundary_disabled; return 1; fi; fi; }; qa_restore_active_state() { if ! qa_require_pre_activation; then return 1; fi; if [ \"${qa_boundary_active}\" = 1 ]; then if ! sudo systemctl start tokenkey-qa-boundary.timer; then qa_force_boundary_disabled; return 1; fi; if ! sudo systemctl is-active --quiet tokenkey-qa-boundary.timer; then qa_force_boundary_disabled; return 1; fi; else if ! sudo systemctl stop tokenkey-qa-boundary.timer; then qa_force_boundary_disabled; return 1; fi; local active; if active=$(sudo systemctl is-active tokenkey-qa-boundary.timer 2>/dev/null); then :; else :; fi; if [ \"${active}\" != inactive ]; then qa_force_boundary_disabled; return 1; fi; fi; }; qa_apply_requested_owner() { local active; if ! active=$(qa_query_single_owner); then if qa_force_boundary_disabled; then :; else echo \"failed to force QA boundary disabled after owner query failure\" >&2; fi; return 1; fi; case \"${active}\" in 1) qa_force_boundary_disabled ;; 0) case \"${qa_requested_timer_state}\" in disabled) qa_force_boundary_disabled ;; enabled|auto) if ! sudo systemctl enable --now tokenkey-qa-boundary.timer; then qa_force_boundary_disabled; return 1; fi; if ! sudo systemctl is-enabled --quiet tokenkey-qa-boundary.timer || ! sudo systemctl is-active --quiet tokenkey-qa-boundary.timer; then qa_force_boundary_disabled; return 1; fi ;; esac ;; esac; }; if sudo systemctl is-enabled --quiet tokenkey-qa-boundary.timer; then qa_boundary_enabled=1; else qa_boundary_enabled=0; fi; if sudo systemctl is-active --quiet tokenkey-qa-boundary.timer; then qa_boundary_active=1; else qa_boundary_active=0; fi; if ! qa_single_owner_active=$(qa_query_single_owner); then if qa_force_boundary_disabled; then :; else echo \"failed to force QA boundary disabled after initial receipt query failure\" >&2; fi; exit 1; fi; if [ \"${qa_single_owner_active}\" = 1 ]; then qa_force_boundary_disabled; fi; qa_sync_committed=0; qa_sync_restore() { local qa_sync_rc=$?; trap - EXIT; if [ \"${qa_sync_committed}\" != 1 ]; then local active; if ! active=$(qa_query_single_owner); then if qa_force_boundary_disabled; then :; else echo \"failed to force QA boundary disabled during uncertain restore\" >&2; fi; exit 1; fi; if [ \"${active}\" = 1 ]; then if ! qa_force_boundary_disabled; then exit 1; fi; else if ! qa_restore_enabled_state || ! qa_restore_active_state; then exit 1; fi; fi; fi; exit \"${qa_sync_rc}\"; }; trap qa_sync_restore EXIT"),
    "if sudo systemctl list-unit-files tokenkey-qa-boundary.timer --no-legend 2>/dev/null | grep -q \"^tokenkey-qa-boundary[.]timer\"; then sudo systemctl disable --now tokenkey-qa-boundary.timer; fi",
    ("qa_sync_deadline=$(( $(date +%s) + " + ($drain_timeout | tostring) + " )); while sudo systemctl is-active --quiet tokenkey-qa-boundary.service; do if [ \"$(date +%s)\" -ge \"${qa_sync_deadline}\" ]; then echo \"timeout draining tokenkey-qa-boundary.service\" >&2; exit 1; fi; sleep 2; done"),
    "! sudo systemctl is-active --quiet tokenkey-qa-boundary.service",
    atomic_install($resolver; "/usr/local/lib/tokenkey/resolve-app-container.sh"; "0644"),
    atomic_install($boundary; "/usr/local/bin/tokenkey-qa-boundary.sh"; "0755"),
    "sudo test -d /var/lib/tokenkey/app/qa_blobs",
    "sudo test -d /var/lib/tokenkey/app/qa_dlq",
    "sudo /usr/local/bin/tokenkey-qa-boundary.sh --install-units",
    "sudo systemctl daemon-reload",
    $owner_command,
    (if $timer_state == "disabled" then
      "test \"$(sudo systemctl is-enabled tokenkey-qa-boundary.timer)\" = \"" + $timer_state + "\""
    else empty end),
    (if $timer_state == "disabled" then
      "test \"$(sudo systemctl is-active tokenkey-qa-boundary.timer)\" = \"" + $boundary_active + "\""
    else empty end),
    "qa_sync_committed=1",
    "trap - EXIT",
    ("echo Live qa-boundary units now match deploy/aws@" + $artifact_sha + " timer=" + $timer_state + " on $(hostname)")
  ]}' >"${params}"

command_id="$(aws --region "${REGION}" ssm send-command \
  --instance-ids "${INSTANCE_ID}" --document-name AWS-RunShellScript \
  --comment "${COMMENT}" --parameters "file://${params}" \
  --query Command.CommandId --output text)"
echo "ssm command-id=${command_id}"
deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
status=InProgress
while [[ "${status}" =~ ^(Pending|InProgress|Delayed)$ ]]; do
  (( $(date +%s) < deadline )) || { echo "timeout waiting for ${command_id}" >&2; exit 1; }
  sleep 3
  status="$(aws --region "${REGION}" ssm get-command-invocation --command-id "${command_id}" --instance-id "${INSTANCE_ID}" --query Status --output text)"
done
aws --region "${REGION}" ssm get-command-invocation --command-id "${command_id}" --instance-id "${INSTANCE_ID}" --query StandardOutputContent --output text >"${stdout}"
aws --region "${REGION}" ssm get-command-invocation --command-id "${command_id}" --instance-id "${INSTANCE_ID}" --query StandardErrorContent --output text >"${stderr}"
code="$(aws --region "${REGION}" ssm get-command-invocation --command-id "${command_id}" --instance-id "${INSTANCE_ID}" --query ResponseCode --output text)"
cat "${stdout}"
[[ ! -s "${stderr}" ]] || cat "${stderr}" >&2
[[ "${status}" == Success && "${code}" == 0 ]] || { echo "SSM failed status=${status} code=${code}" >&2; exit 1; }
