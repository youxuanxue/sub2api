#!/usr/bin/env bash
set -euo pipefail

TAG="${1:-${INPUT_TAG:-}}"
INSTANCE_ID="${2:-${INSTANCE_ID:-}}"
COMMENT="${3:-${SSM_COMMENT:-deploy-stage0}}"
TIMEOUT_SECONDS="${STAGE0_SSM_TIMEOUT_SECONDS:-300}"
OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.}"

if [[ -z "${TAG}" ]]; then
  echo "stage0_deploy_via_ssm: tag is required" >&2
  exit 1
fi
if [[ -z "${INSTANCE_ID}" ]]; then
  echo "stage0_deploy_via_ssm: instance id is required" >&2
  exit 1
fi

ssm_region_args=()
if [[ -n "${AWS_REGION:-${AWS_DEFAULT_REGION:-}}" ]]; then
  ssm_region_args=(--region "${AWS_REGION:-${AWS_DEFAULT_REGION}}")
fi

# After send-command with tag targets (Lightsail Hybrid), discover the concrete
# managed-instance id because get-command-invocation requires it explicitly.
resolve_ssm_primary_invocation_instance() {
  local cmd_id="$1"
  local cutoff=$(( $(date +%s) + 180 ))
  while [[ $(date +%s) -lt "${cutoff}" ]]; do
    local json n
    json="$(aws "${ssm_region_args[@]}" ssm list-command-invocations \
      --command-id "${cmd_id}" --output json 2>/dev/null || echo '{"CommandInvocations":[]}')"
    n="$(echo "${json}" | jq '.CommandInvocations | length')"
    if [[ "${n}" -ge 1 ]]; then
      if [[ "${n}" -ne 1 ]]; then
        echo "stage0_deploy_via_ssm: expected exactly one SSM invocation for command=${cmd_id}, got ${n}" >&2
        echo "${json}" | jq '.' >&2
        exit 1
      fi
      echo "${json}" | jq -r '.CommandInvocations[0].InstanceId'
      return 0
    fi
    sleep 3
  done
  echo "stage0_deploy_via_ssm: timed out resolving invocation instance for command=${cmd_id}" >&2
  exit 1
}

mkdir -p "${OUTPUT_DIR}"
params_file="${OUTPUT_DIR}/ssm-params.json"
stdout_file="${OUTPUT_DIR}/stdout.txt"
stderr_file="${OUTPUT_DIR}/stderr.txt"

jq -n --arg tag "${TAG}" '{
  commands: [
    "set -euo pipefail",
    ("echo === deploy stage0 to tag=" + $tag + " ==="),
    ("BACKUP=/var/lib/tokenkey/.env.before-" + $tag),
    "sudo cp -a /var/lib/tokenkey/.env \"$BACKUP\"",
    "rollback() { rc=$?; echo \"::warning::deploy failed; restoring previous tokenkey image\"; if [ -f \"$BACKUP\" ]; then sudo cp -a \"$BACKUP\" /var/lib/tokenkey/.env; cd /var/lib/tokenkey && sudo docker compose --env-file .env up -d --no-deps tokenkey || true; for i in 1 2 3 4 5 6 7 8 9 10 11 12; do s=$(sudo docker inspect tokenkey --format '\''{{.State.Health.Status}}'\'' 2>/dev/null || echo missing); echo \"rollback try $i: $s\"; [ \"$s\" = healthy ] && break; sleep 5; done; sudo docker logs tokenkey --since 2m 2>&1 | tail -50 || true; fi; exit $rc; }",
    "trap rollback ERR",
    ("sudo sed -i '\''s|sub2api:[^[:space:]]*|sub2api:" + $tag + "|'\'' /var/lib/tokenkey/.env"),
    "cd /var/lib/tokenkey && sudo docker compose --env-file .env pull tokenkey",
    "cd /var/lib/tokenkey && sudo docker compose --env-file .env up -d --no-deps tokenkey",
    "for i in 1 2 3 4 5 6 7 8 9 10 11 12; do s=$(sudo docker inspect tokenkey --format '\''{{.State.Health.Status}}'\'' 2>/dev/null || echo missing); echo \"try $i: $s\"; [ \"$s\" = healthy ] && break; sleep 5; done",
    "FINAL=$(sudo docker inspect tokenkey --format '\''{{.State.Health.Status}}'\'' 2>/dev/null || echo missing)",
    "if [ \"$FINAL\" != \"healthy\" ]; then echo \"::error::container did not reach healthy state (final=$FINAL)\"; sudo docker logs tokenkey --since 2m 2>&1 | tail -50; exit 1; fi",
    "trap - ERR",
    "cd /var/lib/tokenkey && sudo docker compose ps",
    "sudo docker logs tokenkey --since 2m 2>&1 | tail -20"
  ]
}' > "${params_file}"

eff_instance_id="${INSTANCE_ID}"
if [[ "${INSTANCE_ID}" == mi-* && -n "${EDGE_ID:-}" ]]; then
  # Hybrid managed nodes minted via create-activation carry tags EdgeId + Platform
  # (see deploy/aws/lightsail/provision-edge.sh). Targeting by tag reaches the
  # live registration even when Parameter Store ssm_managed_instance_id lags.
  cmd_id="$(aws "${ssm_region_args[@]}" ssm send-command \
    --targets "Key=tag:EdgeId,Values=${EDGE_ID}" "Key=tag:Platform,Values=lightsail" \
    --document-name AWS-RunShellScript \
    --comment "${COMMENT} tag=${TAG}" \
    --parameters "file://${params_file}" \
    --query 'Command.CommandId' --output text)"
  eff_instance_id="$(resolve_ssm_primary_invocation_instance "${cmd_id}")"
  if [[ "${eff_instance_id}" != "${INSTANCE_ID}" ]]; then
    echo "::warning::SSM send resolved instance ${eff_instance_id}; caller passed ${INSTANCE_ID} (check SSM parameter /ssm_managed_instance_id)"
  fi
else
  cmd_id="$(aws "${ssm_region_args[@]}" ssm send-command \
    --instance-ids "${INSTANCE_ID}" \
    --document-name AWS-RunShellScript \
    --comment "${COMMENT} tag=${TAG}" \
    --parameters "file://${params_file}" \
    --query 'Command.CommandId' --output text)"
fi

echo "ssm command-id=${cmd_id}"
if [[ -n "${GITHUB_OUTPUT:-}" ]]; then
  echo "command_id=${cmd_id}" >> "${GITHUB_OUTPUT}"
fi

deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
status="InProgress"
while true; do
  status="$(aws "${ssm_region_args[@]}" ssm get-command-invocation \
    --command-id "${cmd_id}" --instance-id "${eff_instance_id}" \
    --query 'Status' --output text 2>/dev/null || echo InProgress)"
  case "${status}" in
    Success|Failed|TimedOut|Cancelled) break ;;
  esac
  if [[ $(date +%s) -ge ${deadline} ]]; then
    echo "::error::ssm timeout" >&2
    status="TimedOut"
    break
  fi
  sleep 5
done

aws "${ssm_region_args[@]}" ssm get-command-invocation \
  --command-id "${cmd_id}" --instance-id "${eff_instance_id}" \
  --query 'StandardOutputContent' --output text > "${stdout_file}"
aws "${ssm_region_args[@]}" ssm get-command-invocation \
  --command-id "${cmd_id}" --instance-id "${eff_instance_id}" \
  --query 'StandardErrorContent' --output text > "${stderr_file}"

echo '--- ssm stdout (last 8KB) ---'
tail -c 8192 "${stdout_file}"
echo
echo '--- ssm stderr (last 8KB) ---'
tail -c 8192 "${stderr_file}"
echo

if [[ "${status}" != "Success" ]]; then
  echo "::error::ssm command status=${status}" >&2
  exit 1
fi
