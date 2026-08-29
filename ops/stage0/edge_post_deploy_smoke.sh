#!/usr/bin/env bash
# Edge post-deploy smoke — infra SSM probes + optional main-gateway-via-edge suite.
#
# Phases (EDGE_SMOKE_PHASE / workflow smoke_phase):
#   infra              — external /health, runner allowlist 403, SSM compose health
#   edge-native-oauth  — in-container per-account Anthropic OAuth probe (realistic CC shape)
#   main-via-edge      — legacy prod-gateway relay smoke (optional; no formulaic edge curl)
#   full               — infra + edge-native-oauth
#
# Gateway business probes live in ops/stage0/post_deploy_smoke.sh (single runner).
#
# GitHub edge-<id> Environment:
#   secret TK_SMOKE_API_KEY — main-via-edge (request via prod base URL)
#
# Fixed in smoke_env.sh (no per-edge GitHub var):
#   TK_SMOKE_EDGE_CANARY_BASE_URL=https://api.tokenkey.dev
#   TK_SMOKE_EDGE_LOCAL_CHAT_MODELS=claude-sonnet-4-6
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
EDGE_RUN_PROBE="${EDGE_RUN_PROBE:-${REPO_ROOT}/ops/observability/run-probe.sh}"
# shellcheck source=smoke_env.sh
source "${SCRIPT_DIR}/smoke_env.sh"
# Shared SSM "resolve managed-instance after tag-targeted send" helper.
# shellcheck source=ssm_resolve_invocation_mi.inc.sh
source "${SCRIPT_DIR}/ssm_resolve_invocation_mi.inc.sh"

EDGE_ID="${EDGE_ID:-}"
EDGE_API_URL="${EDGE_API_URL:-}"
EDGE_INSTANCE_ID="${EDGE_INSTANCE_ID:-}"
EDGE_SSM_PREFIX="${EDGE_SSM_PREFIX:-}"
MAIN_GATEWAY_BASE_URL="${MAIN_GATEWAY_BASE_URL:-${TK_SMOKE_EDGE_CANARY_BASE_URL}}"
EDGE_SELF_SMOKE_MODE="${EDGE_SELF_SMOKE_MODE:-infra}"
EDGE_SMOKE_PHASE="${EDGE_SMOKE_PHASE:-full}"
SKIP_EXTERNAL_HEALTH="${SKIP_EXTERNAL_HEALTH:-0}"

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

case "${EDGE_SMOKE_PHASE}" in
  infra|edge-native-oauth|main-via-edge|full) ;;
  *)
    echo "tk_edge_post_deploy_smoke: EDGE_SMOKE_PHASE must be infra|edge-native-oauth|main-via-edge|full, got ${EDGE_SMOKE_PHASE}" >&2
    exit 1
    ;;
esac

EDGE_API_URL="${EDGE_API_URL%/}"
MAIN_GATEWAY_BASE_URL="${MAIN_GATEWAY_BASE_URL:-${TK_SMOKE_EDGE_CANARY_BASE_URL}}"
MAIN_GATEWAY_BASE_URL="${MAIN_GATEWAY_BASE_URL%/}"

command -v aws >/dev/null 2>&1 || { echo "tk_edge_post_deploy_smoke: aws CLI not on PATH" >&2; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "tk_edge_post_deploy_smoke: jq not on PATH" >&2; exit 1; }
command -v curl >/dev/null 2>&1 || { echo "tk_edge_post_deploy_smoke: curl not on PATH" >&2; exit 1; }

AWS_CLI_REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-}}"
if [[ -z "${AWS_CLI_REGION}" ]]; then
  echo "tk_edge_post_deploy_smoke: AWS_REGION or AWS_DEFAULT_REGION is required for SSM" >&2
  exit 1
fi

run_infra_smoke() {
  if [[ "${SKIP_EXTERNAL_HEALTH}" != "1" ]]; then
    edge_health_code="$(curl -sS -o /dev/null -w '%{http_code}' "${EDGE_API_URL}/health" || echo 000)"
    echo "tk_edge_post_deploy_smoke: GET ${EDGE_API_URL}/health -> HTTP ${edge_health_code}"
    if [[ "${edge_health_code}" != "200" ]]; then
      echo "tk_edge_post_deploy_smoke: edge external /health failed" >&2
      exit 1
    fi
  else
    echo "tk_edge_post_deploy_smoke: external /health skipped (workflow already ran external_health.sh)"
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

  if [[ "${EDGE_SELF_SMOKE_MODE}" == "api" ]]; then
    echo "tk_edge_post_deploy_smoke: EDGE_SELF_SMOKE_MODE=api uses edge-native oauth smoke (not post_deploy_smoke)"
  else
    echo "tk_edge_post_deploy_smoke: edge API self-smoke skipped (set EDGE_SELF_SMOKE_MODE=api to enable native oauth probe)"
  fi

  bash "${EDGE_RUN_PROBE}" \
    --target "edge:${EDGE_ID}" \
    --comment "edge-self-smoke edge=${EDGE_ID}" \
    --timeout-seconds 180 \
    --script "${SCRIPT_DIR}/edge_infra_smoke.sh"
}

run_edge_native_anthropic_smoke() {
  echo "tk_edge_post_deploy_smoke: edge-native anthropic oauth smoke edge=${EDGE_ID} models=${TK_SMOKE_EDGE_LOCAL_CHAT_MODELS}"
  bash "${REPO_ROOT}/ops/observability/run-probe.sh" \
    --target "edge:${EDGE_ID}" \
    --comment "edge-native-anthropic-smoke edge=${EDGE_ID}" \
    --timeout-seconds 600 \
    --script "${SCRIPT_DIR}/edge_native_anthropic_smoke.sh" \
    --with "${SCRIPT_DIR}/probe_account_model.sh" \
    --with "${REPO_ROOT}/ops/pricing/probe_reserved_resources.sh" \
    --with "${SCRIPT_DIR}/probe_account_model_verdict.py" \
    --with "${SCRIPT_DIR}/smoke_anthropic_realistic.py" \
    --env "ANTHROPIC_MODELS=${TK_SMOKE_EDGE_LOCAL_CHAT_MODELS}" \
    --env "ANTHROPIC_SOURCE_GROUP=default" \
    --env "REQUEST_TIMEOUT_SECONDS=45" \
    --env "MAX_TOKENS=32"
}

run_main_via_edge_smoke() {
  if [[ -z "${TK_SMOKE_API_KEY}" ]]; then
    echo "tk_edge_post_deploy_smoke: TK_SMOKE_API_KEY not set; skipping main-gateway-via-edge smoke"
    return 0
  fi

  canary_key="${TK_SMOKE_API_KEY}"
  echo "tk_edge_post_deploy_smoke: main_gateway=${MAIN_GATEWAY_BASE_URL} key=configured"

  start_epoch="$(date -u +%s)"
  TOKENKEY_BASE_URL="${MAIN_GATEWAY_BASE_URL}" \
  GATEWAY_SMOKE_SUITE=main-via-edge \
  TK_SMOKE_SKIP_FRONTEND=1 \
  TK_SMOKE_API_KEY="${canary_key}" \
  TK_SMOKE_ANTHROPIC_MODELS="${TK_SMOKE_EDGE_LOCAL_CHAT_MODELS}" \
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
    log_eff_instance="$(ssm_resolve_invocation_mi "${AWS_CLI_REGION}" "${log_cmd_id}")"
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
}

tmpdir="$(mktemp -d)"
trap 'rm -rf "${tmpdir}"' EXIT

echo "tk_edge_post_deploy_smoke: edge=${EDGE_ID} edge_api=${EDGE_API_URL} phase=${EDGE_SMOKE_PHASE} self_mode=${EDGE_SELF_SMOKE_MODE} main_gateway=${MAIN_GATEWAY_BASE_URL} edge_local_models=${TK_SMOKE_EDGE_LOCAL_CHAT_MODELS} skip_external_health=${SKIP_EXTERNAL_HEALTH}"

case "${EDGE_SMOKE_PHASE}" in
  infra)
    run_infra_smoke
  if [[ "${EDGE_SELF_SMOKE_MODE}" == "api" ]]; then
    run_edge_native_anthropic_smoke
  fi
    ;;
  edge-native-oauth)
    run_edge_native_anthropic_smoke
    ;;
  main-via-edge)
    run_main_via_edge_smoke
    ;;
  full)
    run_infra_smoke
    run_edge_native_anthropic_smoke
    ;;
esac

echo "tk_edge_post_deploy_smoke: OK phase=${EDGE_SMOKE_PHASE}"
