#!/usr/bin/env bash
# Install the fixed QA age-retention unit. Default keeps deletion disabled.
set -euo pipefail

INSTANCE_ID="${1:-${INSTANCE_ID:-}}"
COMMENT="${2:-${SSM_COMMENT:-ops-qa-stale-cleanup-timer-sync}}"
TIMEOUT_SECONDS="${STAGE0_SSM_TIMEOUT_SECONDS:-300}"
OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.}"
TIMER_STATE="${QA_STALE_TIMER_STATE:-disabled}"
ACTIVATION_RECEIPT="${QA_STALE_ACTIVATION_RECEIPT:-}"
REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-us-east-1}}"

[[ "${INSTANCE_ID}" =~ ^i-[0-9a-f]{17}$ ]] || { echo "valid instance id required" >&2; exit 1; }
case "${TIMER_STATE}" in
  disabled)
    timer_command="sudo systemctl disable --now tokenkey-qa-stale-cleanup.timer"
    active_state=inactive
    ;;
  enabled)
    [[ -f "${ACTIVATION_RECEIPT}" ]] || { echo "QA_STALE_ACTIVATION_RECEIPT is required to enable" >&2; exit 1; }
    jq -e --arg instance_id "${INSTANCE_ID}" '
      .mode == "prod_qa_age_retention_first_apply" and
      .instance_id == $instance_id and
      .deletion_authorized == true and
      (.cutoff | type == "string") and
      (.applied_at | sub("\\.[0-9]+Z$";"Z") | fromdateiso8601) <= now and
      (.authorization_expires_at | sub("\\.[0-9]+Z$";"Z") | fromdateiso8601) >= now and
      (.planned_rows | type == "number") and
      .remaining_rows == 0 and
      .remaining_blob_files == 0 and
      .remaining_dlq_files == 0 and
      (.marker_sha256 | type == "string" and test("^[0-9a-f]{64}$"))
    ' "${ACTIVATION_RECEIPT}" >/dev/null || { echo "invalid or expired QA stale activation receipt" >&2; exit 1; }
    marker_sha256="$(jq -r '.marker_sha256' "${ACTIVATION_RECEIPT}")"
    timer_command="sudo bash -c 'set -e; marker=/var/lib/tokenkey/qa-stale-first-plan.json; expected=${marker_sha256}; verify_marker() { test -f \"\${marker}\" && test \"\$(sha256sum \"\${marker}\" | awk \"{print \\\$1}\")\" = \"\${expected}\"; }; if test \"\$(systemctl is-enabled tokenkey-qa-stale-cleanup.timer)\" = enabled && test \"\$(systemctl is-active tokenkey-qa-stale-cleanup.timer)\" = active; then if test -f \"\${marker}\"; then verify_marker; fi; rm -f \"\${marker}\"; exit 0; fi; verify_marker; if systemctl enable --now tokenkey-qa-stale-cleanup.timer && test \"\$(systemctl is-enabled tokenkey-qa-stale-cleanup.timer)\" = enabled && test \"\$(systemctl is-active tokenkey-qa-stale-cleanup.timer)\" = active; then rm -f \"\${marker}\"; else systemctl disable --now tokenkey-qa-stale-cleanup.timer || true; exit 1; fi'"
    active_state=active
    ;;
  *)
    echo "QA_STALE_TIMER_STATE must be disabled or enabled" >&2
    exit 1
    ;;
esac

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SOURCE="${SCRIPT_DIR}/../../deploy/aws/stage0/tokenkey-qa-stale-cleanup.sh"
HELPER_SOURCE="${SCRIPT_DIR}/../../deploy/aws/stage0/tokenkey-qa-export-orphan.py"
[[ -f "${SOURCE}" && -f "${HELPER_SOURCE}" ]] || { echo "missing QA stale cleanup distribution source" >&2; exit 1; }
payload="$(gzip -9n -c "${SOURCE}" | base64 | tr -d '\n')"
helper_payload="$(gzip -9n -c "${HELPER_SOURCE}" | base64 | tr -d '\n')"
mkdir -p "${OUTPUT_DIR}"
params="${OUTPUT_DIR}/ssm-params.json"
stdout="${OUTPUT_DIR}/stdout.txt"
stderr="${OUTPUT_DIR}/stderr.txt"

jq -n --arg payload "${payload}" --arg helper_payload "${helper_payload}" --arg command "${timer_command}" \
  --arg timer_state "${TIMER_STATE}" --arg active_state "${active_state}" '
  def atomic_install($artifact; $destination):
    "sudo bash -c " + (
      (
        "set -euo pipefail; destination=" + ($destination | @sh)
        + "; directory=$(dirname \"$destination\"); install -d -m 0755 \"$directory\""
        + "; temporary=$(mktemp \"$directory/.${destination##*/}.XXXXXX\")"
        + "; trap \"rm -f \\\"$temporary\\\"\" EXIT"
        + "; printf %s " + ($artifact | @sh) + " | base64 -d | gunzip > \"$temporary\""
        + "; chmod 0755 \"$temporary\"; mv -f \"$temporary\" \"$destination\""
      ) | @sh
    );
  {commands:[
    "set -euo pipefail",
    atomic_install($helper_payload; "/usr/local/lib/tokenkey/qa-export-orphan.py"),
    atomic_install($payload; "/usr/local/bin/tokenkey-qa-stale-cleanup.sh"),
    "sudo /usr/local/bin/tokenkey-qa-stale-cleanup.sh --install-units",
    "sudo systemctl daemon-reload",
    $command,
    ("test \"$(sudo systemctl is-enabled tokenkey-qa-stale-cleanup.timer)\" = \""+$timer_state+"\""),
    ("test \"$(sudo systemctl is-active tokenkey-qa-stale-cleanup.timer)\" = \""+$active_state+"\"")
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
