#!/usr/bin/env bash
set -euo pipefail

EDGE_ID="${1:-${EDGE_ID:-}}"
INSTANCE_ID="${2:-${INSTANCE_ID:-}}"
REGION="${3:-${AWS_REGION:-${AWS_DEFAULT_REGION:-}}}"
TIMEOUT_SECONDS="${EDGE_SSM_ROLE_TIMEOUT_SECONDS:-300}"
POLL_SECONDS="${EDGE_SSM_ROLE_POLL_SECONDS:-2}"
MODE="${4:-}"

[[ "${EDGE_ID}" =~ ^[a-z]{2}[0-9]+$ ]] || { echo "ensure_edge_ssm_role: invalid Edge ID" >&2; exit 1; }
[[ "${INSTANCE_ID}" =~ ^mi-[A-Za-z0-9]+$ ]] || { echo "ensure_edge_ssm_role: invalid managed-instance ID" >&2; exit 1; }
[[ -n "${REGION}" ]] || { echo "ensure_edge_ssm_role: AWS region required" >&2; exit 1; }
[[ "${TIMEOUT_SECONDS}" =~ ^[0-9]+$ ]] || { echo "ensure_edge_ssm_role: invalid timeout" >&2; exit 1; }
[[ "${POLL_SECONDS}" =~ ^[0-9]+$ ]] || { echo "ensure_edge_ssm_role: invalid poll interval" >&2; exit 1; }
[[ -z "${MODE}" || "${MODE}" == --check ]] || { echo "ensure_edge_ssm_role: expected optional --check" >&2; exit 1; }

desired_role="tokenkey-lightsail-ssm-hybrid-${EDGE_ID}"
tagged_edge="$(aws ssm list-tags-for-resource \
  --region "${REGION}" \
  --resource-type ManagedInstance \
  --resource-id "${INSTANCE_ID}" \
  --query 'TagList[?Key==`EdgeId`].Value | [0]' \
  --output text)"
if [[ -z "${tagged_edge}" || "${tagged_edge}" == None || "${tagged_edge}" == null ]]; then
  echo "ensure_edge_ssm_role: ${INSTANCE_ID} has no EdgeId tag" >&2
  exit 1
fi
if [[ "${tagged_edge}" != "${EDGE_ID}" ]]; then
  echo "ensure_edge_ssm_role: ${INSTANCE_ID} belongs to Edge ${tagged_edge}, expected ${EDGE_ID}" >&2
  exit 1
fi

read_role() {
  aws ssm describe-instance-information \
    --region "${REGION}" \
    --filters "Key=InstanceIds,Values=${INSTANCE_ID}" \
    --query 'InstanceInformationList[0].IamRole' \
    --output text
}

read_remote_identity() {
  local command_id status
  if ! command_id="$(aws ssm send-command \
    --region "${REGION}" \
    --instance-ids "${INSTANCE_ID}" \
    --document-name AWS-RunShellScript \
    --comment "verify Edge SSM role credentials for ${EDGE_ID}" \
    --parameters 'commands=["aws sts get-caller-identity --query Arn --output text"]' \
    --query 'Command.CommandId' \
    --output text)"; then
    return 1
  fi

  while true; do
    if ! status="$(aws ssm get-command-invocation \
      --region "${REGION}" \
      --command-id "${command_id}" \
      --instance-id "${INSTANCE_ID}" \
      --query 'Status' \
      --output text 2>/dev/null)"; then
      status=InProgress
    fi
    case "${status}" in
      Success) break ;;
      Failed|TimedOut|Cancelled|Cancelling) return 1 ;;
    esac
    [[ $(date +%s) -ge ${deadline} ]] && return 1
    sleep "${POLL_SECONDS}"
  done

  aws ssm get-command-invocation \
    --region "${REGION}" \
    --command-id "${command_id}" \
    --instance-id "${INSTANCE_ID}" \
    --query 'StandardOutputContent' \
    --output text | tr -d '\r' | awk 'NF { value=$0 } END { print value }'
}

current_role="$(read_role)"
if [[ -z "${current_role}" || "${current_role}" == None || "${current_role}" == null ]]; then
  echo "ensure_edge_ssm_role: managed instance ${INSTANCE_ID} not found" >&2
  exit 1
fi
if [[ "${MODE}" == --check ]]; then
  if [[ "${current_role}" == "${desired_role}" ]]; then
    echo "managed instance ${INSTANCE_ID} role already correct: ${desired_role}"
    exit 0
  fi
  echo "ensure_edge_ssm_role: role mismatch for ${INSTANCE_ID}; got ${current_role}, expected ${desired_role}" >&2
  exit 1
fi

if [[ "${current_role}" != "${desired_role}" ]]; then
  aws ssm update-managed-instance-role \
    --region "${REGION}" \
    --instance-id "${INSTANCE_ID}" \
    --iam-role "${desired_role}" >/dev/null
else
  echo "managed instance ${INSTANCE_ID} control-plane role already correct: ${desired_role}"
fi

deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
while true; do
  current_role="$(read_role)"
  if [[ "${current_role}" == "${desired_role}" ]]; then
    break
  fi
  [[ $(date +%s) -ge ${deadline} ]] && break
  sleep "${POLL_SECONDS}"
done

if [[ "${current_role}" != "${desired_role}" ]]; then
  echo "ensure_edge_ssm_role: role update not observed for ${INSTANCE_ID}; got ${current_role}, expected ${desired_role}" >&2
  exit 1
fi

last_remote_arn="<probe failed>"
while true; do
  if remote_arn="$(read_remote_identity)"; then
    last_remote_arn="${remote_arn}"
    if [[ "${remote_arn}" == *":assumed-role/${desired_role}/"* ]]; then
      echo "managed instance ${INSTANCE_ID} role credentials verified: ${desired_role}"
      exit 0
    fi
  fi
  [[ $(date +%s) -ge ${deadline} ]] && break
  sleep "${POLL_SECONDS}"
done

echo "ensure_edge_ssm_role: role credentials not observed for ${INSTANCE_ID}; got ${last_remote_arn}, expected assumed-role/${desired_role}/" >&2
exit 1
