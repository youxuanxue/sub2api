#!/usr/bin/env bash
set -euo pipefail

EDGE_ID="${EDGE_ID:-}"
EDGE_API_URL="${EDGE_API_URL:-}"
EDGE_INSTANCE_ID="${EDGE_INSTANCE_ID:-}"
EDGE_SSM_PREFIX="${EDGE_SSM_PREFIX:-}"
MAIN_GATEWAY_BASE_URL="${MAIN_GATEWAY_BASE_URL:-}"
MAIN_GATEWAY_EDGE_SMOKE_API_KEY="${MAIN_GATEWAY_EDGE_SMOKE_API_KEY:-}"
EDGE_SELF_SMOKE_MODE="${EDGE_SELF_SMOKE_MODE:-infra}"

if [[ -z "${EDGE_ID}" ]]; then
  echo "tk_edge_post_deploy_smoke: EDGE_ID is required" >&2
  exit 1
fi
if [[ -z "${EDGE_API_URL}" ]]; then
  echo "tk_edge_post_deploy_smoke: EDGE_API_URL is required" >&2
  exit 1
fi
if [[ -z "${EDGE_INSTANCE_ID}" ]]; then
  echo "tk_edge_post_deploy_smoke: EDGE_INSTANCE_ID is required" >&2
  exit 1
fi

EDGE_API_URL="${EDGE_API_URL%/}"
MAIN_GATEWAY_BASE_URL="${MAIN_GATEWAY_BASE_URL:-https://api.tokenkey.dev}"
MAIN_GATEWAY_BASE_URL="${MAIN_GATEWAY_BASE_URL%/}"

# Pin the chat model from the caller. Avoiding ops/stage0/post_deploy_smoke.sh's
# "first claude id from /v1/models" heuristic prevents 4xx model_not_found
# when upstream drops legacy snapshot ids from the catalog (hard control-plane
# fail, not covered by soft-degrade).
#
# Caller MUST set POST_DEPLOY_SMOKE_CHAT_MODEL — workflow yaml is the single
# source of truth for the default. See:
#   .github/workflows/deploy-edge-stage0.yml (vars.POST_DEPLOY_SMOKE_CHAT_MODEL || 'claude-sonnet-4-6')
#   .github/workflows/deploy-stage0.yml      (same)
# Encoding a third default here would create a three-place sync surface
# (Jobs anti-pattern: same information stored in two/three places).
export POST_DEPLOY_SMOKE_CHAT_MODEL="${POST_DEPLOY_SMOKE_CHAT_MODEL:?caller must set POST_DEPLOY_SMOKE_CHAT_MODEL — workflow yaml is single source of truth}"

command -v aws >/dev/null 2>&1 || { echo "tk_edge_post_deploy_smoke: aws CLI not on PATH" >&2; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "tk_edge_post_deploy_smoke: jq not on PATH" >&2; exit 1; }
command -v curl >/dev/null 2>&1 || { echo "tk_edge_post_deploy_smoke: curl not on PATH" >&2; exit 1; }

AWS_CLI_REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-}}"
if [[ -z "${AWS_CLI_REGION}" ]]; then
  echo "tk_edge_post_deploy_smoke: AWS_REGION or AWS_DEFAULT_REGION is required for SSM" >&2
  exit 1
fi

# After tag-targeted send-command, list-command-invocations supplies the mi-* for
# get-command-invocation (required per managed node).
resolve_primary_ssm_invocation_instance() {
  local cmd_id="$1"
  local cutoff=$(( $(date +%s) + 180 ))
  while [[ $(date +%s) -lt "${cutoff}" ]]; do
    local json n
    json="$(aws ssm list-command-invocations \
      --region "${AWS_CLI_REGION}" \
      --command-id "${cmd_id}" \
      --output json 2>/dev/null || echo '{"CommandInvocations":[]}')"
    n="$(echo "${json}" | jq '.CommandInvocations | length')"
    if [[ "${n}" -ge 1 ]]; then
      if [[ "${n}" -ne 1 ]]; then
        echo "tk_edge_post_deploy_smoke: expected exactly one SSM invocation for command=${cmd_id}, got ${n}" >&2
        echo "${json}" | jq '.' >&2
        exit 1
      fi
      echo "${json}" | jq -r '.CommandInvocations[0].InstanceId'
      return 0
    fi
    sleep 3
  done
  echo "tk_edge_post_deploy_smoke: timed out resolving invocation for command=${cmd_id}" >&2
  exit 1
}

tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

echo "tk_edge_post_deploy_smoke: edge=${EDGE_ID} edge_api=${EDGE_API_URL} mode=${EDGE_SELF_SMOKE_MODE} POST_DEPLOY_SMOKE_CHAT_MODEL=${POST_DEPLOY_SMOKE_CHAT_MODEL}"

edge_health_code="$(curl -sS -o /dev/null -w '%{http_code}' "${EDGE_API_URL}/health" || echo 000)"
echo "tk_edge_post_deploy_smoke: GET ${EDGE_API_URL}/health -> HTTP ${edge_health_code}"
if [[ "${edge_health_code}" != "200" ]]; then
  echo "tk_edge_post_deploy_smoke: edge external /health failed" >&2
  exit 1
fi

blocked_code="$(curl -sS -o "${tmpdir}/blocked.json" -w '%{http_code}' \
  -H 'Authorization: Bearer tk_edge_smoke_should_not_work' \
  "${EDGE_API_URL}/v1/models" || echo 000)"
echo "tk_edge_post_deploy_smoke: public runner GET ${EDGE_API_URL}/v1/models -> HTTP ${blocked_code}"
if [[ "${blocked_code}" != "403" ]]; then
  echo "tk_edge_post_deploy_smoke: edge API path should be blocked for non-allowlisted runner" >&2
  cat "${tmpdir}/blocked.json" >&2 || true
  exit 1
fi

# Match deploy_via_ssm.sh: always run compose from workspace dir so plugin + env interpolation match operator playbooks.
ssm_commands=(
  "set -euo pipefail"
  "cd /var/lib/tokenkey"
  "sudo docker compose version"
  "sudo docker compose -f docker-compose.yml --env-file .env ps"
  "sudo docker compose -f docker-compose.yml --env-file .env exec -T tokenkey wget -qO- http://localhost:8080/health"
)

if [[ "${EDGE_SELF_SMOKE_MODE}" == "api" ]]; then
  if [[ -z "${EDGE_SSM_PREFIX}" ]]; then
    echo "tk_edge_post_deploy_smoke: EDGE_SSM_PREFIX is required for EDGE_SELF_SMOKE_MODE=api" >&2
    exit 1
  fi
  ssm_commands+=(
    "EDGE_KEY=\$(aws ssm get-parameter --region \"${AWS_CLI_REGION}\" --name '${EDGE_SSM_PREFIX}/smoke/api-key' --with-decryption --query Parameter.Value --output text)"
    "sudo docker compose -f docker-compose.yml --env-file .env exec -T -e TOKENKEY_BASE_URL=http://localhost:8080 -e POST_DEPLOY_SMOKE_SKIP_FRONTEND=1 -e POST_DEPLOY_SMOKE_CHAT_MODEL=\"${POST_DEPLOY_SMOKE_CHAT_MODEL}\" -e POST_DEPLOY_SMOKE_API_KEY=\"\$EDGE_KEY\" tokenkey bash /app/ops/stage0/post_deploy_smoke.sh"
  )
else
  echo "tk_edge_post_deploy_smoke: edge API self-smoke skipped (set EDGE_SELF_SMOKE_MODE=api after Edge upstream/key setup)"
fi

jq -n --argjson commands "$(printf '%s\n' "${ssm_commands[@]}" | jq -R . | jq -s .)" '{commands:$commands}' > "${tmpdir}/edge-ssm.json"
# Lightsail Hybrid (mi-*): target by activation-derived tags EdgeId + Platform so
# we hit the live registration (Parameter Store mi-* alone can mismatch / undeliver).
eff_instance_id="${EDGE_INSTANCE_ID}"
declare -a send_targets_extra=()
if [[ "${EDGE_INSTANCE_ID}" == mi-* ]]; then
  send_targets_extra=(--targets "Key=tag:EdgeId,Values=${EDGE_ID}" "Key=tag:Platform,Values=lightsail")
else
  send_targets_extra=(--instance-ids "${EDGE_INSTANCE_ID}")
fi
cmd_id="$(aws ssm send-command \
  --region "${AWS_CLI_REGION}" \
  "${send_targets_extra[@]}" \
  --document-name AWS-RunShellScript \
  --comment "edge-self-smoke edge=${EDGE_ID}" \
  --parameters "file://${tmpdir}/edge-ssm.json" \
  --query 'Command.CommandId' --output text)"
if [[ "${EDGE_INSTANCE_ID}" == mi-* ]]; then
  eff_instance_id="$(resolve_primary_ssm_invocation_instance "${cmd_id}")"
  if [[ "${eff_instance_id}" != "${EDGE_INSTANCE_ID}" ]]; then
    echo "::warning::live SSM invocation instance ${eff_instance_id} != EDGE_INSTANCE_ID ${EDGE_INSTANCE_ID} (check /ssm_managed_instance_id Parameter Store)"
  fi
fi
echo "tk_edge_post_deploy_smoke: edge self-smoke ssm command-id=${cmd_id}"

deadline=$(( $(date +%s) + 180 ))
status="InProgress"
while true; do
  status="$(aws ssm get-command-invocation \
    --region "${AWS_CLI_REGION}" \
    --command-id "${cmd_id}" --instance-id "${eff_instance_id}" \
    --query 'Status' --output text 2>/dev/null || echo InProgress)"
  case "${status}" in
    Success|Failed|TimedOut|Cancelled) break ;;
  esac
  if [[ $(date +%s) -ge ${deadline} ]]; then
    status="TimedOut"
    break
  fi
  sleep 5
done
aws ssm get-command-invocation \
  --region "${AWS_CLI_REGION}" \
  --command-id "${cmd_id}" --instance-id "${eff_instance_id}" \
  --query 'StandardOutputContent' --output text > "${tmpdir}/edge-stdout.txt"
aws ssm get-command-invocation \
  --region "${AWS_CLI_REGION}" \
  --command-id "${cmd_id}" --instance-id "${eff_instance_id}" \
  --query 'StandardErrorContent' --output text > "${tmpdir}/edge-stderr.txt"
invoke_details="$(aws ssm get-command-invocation \
  --region "${AWS_CLI_REGION}" \
  --command-id "${cmd_id}" --instance-id "${eff_instance_id}" \
  --output json 2>/dev/null || echo '{}')"
echo '--- edge self-smoke invocation (Status / ResponseCode / StatusDetails) ---'
echo "${invoke_details}" | jq '{Status, ResponseCode, StatusDetails, ExecutionElapsedTime}'
echo '--- edge self-smoke stdout (last 4KB) ---'
tail -c 4096 "${tmpdir}/edge-stdout.txt"
echo
echo '--- edge self-smoke stderr (last 4KB) ---'
tail -c 4096 "${tmpdir}/edge-stderr.txt"
echo
if [[ "${status}" != "Success" ]]; then
  echo "tk_edge_post_deploy_smoke: edge self-smoke SSM status=${status}" >&2
  exit 1
fi

if [[ -z "${MAIN_GATEWAY_EDGE_SMOKE_API_KEY}" ]]; then
  echo "tk_edge_post_deploy_smoke: MAIN_GATEWAY_EDGE_SMOKE_API_KEY not set; skipping main-gateway-via-edge smoke"
  exit 0
fi

prefix="$(printf '%s' "${MAIN_GATEWAY_EDGE_SMOKE_API_KEY}" | head -c 6)"
suffix="$(printf '%s' "${MAIN_GATEWAY_EDGE_SMOKE_API_KEY}" | tail -c 4)"
echo "tk_edge_post_deploy_smoke: main_gateway=${MAIN_GATEWAY_BASE_URL} key_hint=${prefix}…${suffix}"

start_epoch="$(date -u +%s)"
TOKENKEY_BASE_URL="${MAIN_GATEWAY_BASE_URL}" \
POST_DEPLOY_SMOKE_SKIP_FRONTEND=1 \
POST_DEPLOY_SMOKE_API_KEY="${MAIN_GATEWAY_EDGE_SMOKE_API_KEY}" \
bash ops/stage0/post_deploy_smoke.sh

log_cmd="sudo docker logs tokenkey-caddy --since 5m 2>&1 | tail -200 || true; sudo docker logs tokenkey --since 5m 2>&1 | tail -200 || true; echo smoke_start_epoch=${start_epoch}"
jq -n --arg cmd "${log_cmd}" '{commands:["set -euo pipefail", $cmd]}' > "${tmpdir}/edge-log-ssm.json"
declare -a log_targets_extra=()
log_eff_instance="${EDGE_INSTANCE_ID}"
if [[ "${EDGE_INSTANCE_ID}" == mi-* ]]; then
  log_targets_extra=(--targets "Key=tag:EdgeId,Values=${EDGE_ID}" "Key=tag:Platform,Values=lightsail")
else
  log_targets_extra=(--instance-ids "${EDGE_INSTANCE_ID}")
fi
log_cmd_id="$(aws ssm send-command \
  --region "${AWS_CLI_REGION}" \
  "${log_targets_extra[@]}" \
  --document-name AWS-RunShellScript \
  --comment "edge-log-confirm edge=${EDGE_ID}" \
  --parameters "file://${tmpdir}/edge-log-ssm.json" \
  --query 'Command.CommandId' --output text)"
if [[ "${EDGE_INSTANCE_ID}" == mi-* ]]; then
  log_eff_instance="$(resolve_primary_ssm_invocation_instance "${log_cmd_id}")"
fi
echo "tk_edge_post_deploy_smoke: edge log confirmation command-id=${log_cmd_id}"
sleep 5
aws ssm get-command-invocation \
  --region "${AWS_CLI_REGION}" \
  --command-id "${log_cmd_id}" --instance-id "${log_eff_instance}" \
  --query 'StandardOutputContent' --output text > "${tmpdir}/edge-logs.txt" || true
if grep -E '(/v1/messages|/v1/chat/completions|/v1/models)' "${tmpdir}/edge-logs.txt" >/dev/null; then
  echo "tk_edge_post_deploy_smoke: confirmed recent Edge API traffic in ${EDGE_ID} logs"
else
  echo "::warning::main gateway smoke succeeded but recent Edge API log confirmation was inconclusive"
  tail -100 "${tmpdir}/edge-logs.txt" || true
fi
