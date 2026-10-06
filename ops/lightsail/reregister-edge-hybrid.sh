#!/usr/bin/env bash
# Rebind a Lightsail Hybrid node to tokenkey-lightsail-ssm-hybrid-<edge>.
#
# UpdateManagedInstanceRole can update the control-plane IamRole while the
# agent still vends the shared tokenkey-lightsail-ssm-hybrid credentials
# (console-adopt). This script is a no-op when remote STS already matches
# the per-Edge role. Otherwise it mints a new activation on that role and
# re-registers the agent.
set -euo pipefail

EDGE_ID="${1:-}"
INSTANCE_ID="${2:-}"
REGION="${3:-${AWS_REGION:-}}"
SSM_PREFIX="${4:-}"
TIMEOUT_SECONDS="${EDGE_HYBRID_REREGISTER_TIMEOUT_SECONDS:-180}"
POLL_SECONDS="${EDGE_HYBRID_REREGISTER_POLL_SECONDS:-5}"

[[ "${EDGE_ID}" =~ ^[a-z]{2}[0-9]+$ ]] || { echo "reregister_edge_hybrid: invalid Edge ID" >&2; exit 1; }
[[ "${INSTANCE_ID}" =~ ^mi-[A-Za-z0-9]+$ ]] || { echo "reregister_edge_hybrid: invalid managed-instance ID" >&2; exit 1; }
[[ -n "${REGION}" && -n "${SSM_PREFIX}" ]] || { echo "reregister_edge_hybrid: region and ssm prefix required" >&2; exit 1; }
[[ "${TIMEOUT_SECONDS}" =~ ^[0-9]+$ ]] || { echo "reregister_edge_hybrid: invalid timeout" >&2; exit 1; }
[[ "${POLL_SECONDS}" =~ ^[0-9]+$ ]] || { echo "reregister_edge_hybrid: invalid poll interval" >&2; exit 1; }

desired_role="tokenkey-lightsail-ssm-hybrid-${EDGE_ID}"

wait_invocation() {
  local command_id="$1" instance_id="$2"
  local deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
  local status
  while true; do
    if ! status="$(aws ssm get-command-invocation \
      --region "${REGION}" \
      --command-id "${command_id}" \
      --instance-id "${instance_id}" \
      --query 'Status' \
      --output text 2>/dev/null)"; then
      status=InProgress
    fi
    case "${status}" in
      Success) return 0 ;;
      Failed|TimedOut|Cancelled|Cancelling) return 1 ;;
    esac
    [[ $(date +%s) -ge ${deadline} ]] && return 1
    sleep "${POLL_SECONDS}"
  done
}

probe_arn() {
  local instance_id="$1"
  local command_id
  command_id="$(aws ssm send-command \
    --region "${REGION}" \
    --instance-ids "${instance_id}" \
    --document-name AWS-RunShellScript \
    --comment "probe Hybrid role credentials for ${EDGE_ID}" \
    --parameters 'commands=["aws sts get-caller-identity --query Arn --output text"]' \
    --query 'Command.CommandId' \
    --output text)"
  wait_invocation "${command_id}" "${instance_id}" || return 1
  aws ssm get-command-invocation \
    --region "${REGION}" \
    --command-id "${command_id}" \
    --instance-id "${instance_id}" \
    --query 'StandardOutputContent' \
    --output text | tr -d '\r' | awk 'NF { value=$0 } END { print value }'
}

emit_id() {
  local managed_id="$1"
  echo "managed instance ${managed_id} Hybrid credentials bound to ${desired_role}"
  if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
    echo "managed_instance_id=${managed_id}" >>"${GITHUB_OUTPUT}"
  fi
}

current_arn="$(probe_arn "${INSTANCE_ID}" || true)"
if [[ "${current_arn}" == *":assumed-role/${desired_role}/"* ]]; then
  echo "hybrid credentials already bound to ${desired_role}"
  emit_id "${INSTANCE_ID}"
  exit 0
fi

echo "hybrid credentials stale on ${INSTANCE_ID}; re-registering onto ${desired_role}"

activation_json="$(aws ssm create-activation \
  --region "${REGION}" \
  --iam-role "${desired_role}" \
  --description "tokenkey lightsail edge ${EDGE_ID} hybrid rebind" \
  --default-instance-name "${EDGE_ID}" \
  --registration-limit 1 \
  --tags "Key=Project,Value=tokenkey" "Key=EdgeId,Value=${EDGE_ID}" "Key=Platform,Value=lightsail")"
activation_id="$(echo "${activation_json}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["ActivationId"])')"
activation_code="$(echo "${activation_json}" | python3 -c 'import json,sys; print(json.load(sys.stdin)["ActivationCode"])')"
[[ "${activation_id}" != "" && "${activation_id}" != "null" ]] || {
  echo "reregister_edge_hybrid: create-activation failed" >&2
  exit 1
}

params_file="$(mktemp)"
trap 'rm -f "${params_file}"' EXIT
python3 - "${activation_id}" "${activation_code}" "${REGION}" "${params_file}" <<'PY'
import json
import shlex
import sys

activation_id, activation_code, region, path = sys.argv[1:5]
commands = [
    "set -euo pipefail",
    "sudo systemctl stop amazon-ssm-agent",
    "sudo rm -f /var/lib/amazon/ssm/registration",
    "sudo /usr/bin/amazon-ssm-agent -register -y -id "
    + shlex.quote(activation_id)
    + " -code "
    + shlex.quote(activation_code)
    + " -region "
    + shlex.quote(region),
    "sudo systemctl start amazon-ssm-agent",
    "systemctl is-active amazon-ssm-agent",
]
with open(path, "w", encoding="utf-8") as fh:
    json.dump({"commands": [" && ".join(commands)]}, fh)
PY

register_cmd="$(aws ssm send-command \
  --region "${REGION}" \
  --instance-ids "${INSTANCE_ID}" \
  --document-name AWS-RunShellScript \
  --comment "rebind Hybrid activation for ${EDGE_ID}" \
  --parameters "file://${params_file}" \
  --query 'Command.CommandId' \
  --output text)"
rm -f "${params_file}"
trap - EXIT
# Stopping amazon-ssm-agent mid-command makes the invocation Failed/TimedOut even
# when -register succeeded. Treat that as "continue and look up the activation".
if wait_invocation "${register_cmd}" "${INSTANCE_ID}"; then
  echo "agent re-register command completed on ${INSTANCE_ID}"
else
  echo "agent re-register invocation ended before Success on ${INSTANCE_ID}; polling activation ${activation_id}"
fi

new_id=""
deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
while [[ $(date +%s) -lt ${deadline} ]]; do
  new_id="$(aws ssm describe-instance-information \
    --region "${REGION}" \
    --filters "Key=ActivationIds,Values=${activation_id}" \
    --query 'InstanceInformationList[0].InstanceId' \
    --output text 2>/dev/null || true)"
  if [[ "${new_id}" == mi-* ]]; then
    break
  fi
  sleep "${POLL_SECONDS}"
done
if [[ "${new_id}" != mi-* ]]; then
  echo "reregister_edge_hybrid: new managed instance not visible for activation ${activation_id}" >&2
  exit 1
fi

aws ssm put-parameter \
  --region "${REGION}" \
  --name "${SSM_PREFIX}/ssm_managed_instance_id" \
  --type String \
  --value "${new_id}" \
  --overwrite >/dev/null

if [[ "${new_id}" != "${INSTANCE_ID}" ]]; then
  echo "deregistering previous managed instance ${INSTANCE_ID}"
  aws ssm deregister-managed-instance --region "${REGION}" --instance-id "${INSTANCE_ID}" >/dev/null 2>&1 || true
fi

bound_arn=""
deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
while [[ $(date +%s) -lt ${deadline} ]]; do
  bound_arn="$(probe_arn "${new_id}" || true)"
  if [[ "${bound_arn}" == *":assumed-role/${desired_role}/"* ]]; then
    emit_id "${new_id}"
    exit 0
  fi
  sleep "${POLL_SECONDS}"
done

echo "reregister_edge_hybrid: credentials still stale after re-register; got ${bound_arn:-<probe failed>}, expected assumed-role/${desired_role}/" >&2
exit 1
