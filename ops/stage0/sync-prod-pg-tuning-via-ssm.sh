#!/usr/bin/env bash
# Sync prod-only Postgres GUC overlay onto Stage0 prod, then recreate
# postgres so command-line -c flags take effect.
#
# Shared docker-compose.yml stays edge-safe (no GUC command). Prod uses
# docker-compose.prod-pg.yml beside it (never embedded in Lightsail user-data).
#
# Usage:
#   bash ops/stage0/sync-prod-pg-tuning-via-ssm.sh <prod-instance-id> [--apply]
set -euo pipefail

INSTANCE_ID="${1:-${INSTANCE_ID:-}}"
MODE="${2:-}"
REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-us-east-1}}"
COMMENT="${SSM_COMMENT:-sync-prod-pg-tuning}"
TIMEOUT_SECONDS="${STAGE0_SSM_TIMEOUT_SECONDS:-300}"
OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.}"

if [[ ! "${INSTANCE_ID}" =~ ^i-[a-zA-Z0-9]+$ ]]; then
  echo "usage: $0 <prod-instance-id> [--apply]" >&2
  exit 2
fi

APPLY=0
if [[ "${MODE}" == "--apply" ]]; then
  APPLY=1
elif [[ -n "${MODE}" ]]; then
  echo "unknown mode ${MODE}; use --apply or omit for dry-run" >&2
  exit 2
fi

EXPECTED_PROD="${TOKENKEY_PROD_INSTANCE_ID:-i-0e43099f831b03160}"
if [[ "${INSTANCE_ID}" != "${EXPECTED_PROD}" && "${TOKENKEY_ALLOW_NONPROD_PG_TUNING:-}" != "1" ]]; then
  echo "refusing non-prod instance ${INSTANCE_ID} (expected ${EXPECTED_PROD}; set TOKENKEY_ALLOW_NONPROD_PG_TUNING=1 to override)" >&2
  exit 2
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OVERLAY_SRC="${SCRIPT_DIR}/../../deploy/aws/stage0/docker-compose.prod-pg.yml"
[[ -f "${OVERLAY_SRC}" ]] || { echo "missing ${OVERLAY_SRC}" >&2; exit 1; }
OVERLAY_B64="$(base64 < "${OVERLAY_SRC}" | tr -d '\n')"

mkdir -p "${OUTPUT_DIR}"
params_file="${OUTPUT_DIR}/ssm-params-pg-tuning.json"
stdout_file="${OUTPUT_DIR}/stdout-pg-tuning.txt"
stderr_file="${OUTPUT_DIR}/stderr-pg-tuning.txt"

jq -n \
  --arg overlay "${OVERLAY_B64}" \
  --argjson apply "${APPLY}" \
  '{
  commands: (
    [
      "set -euo pipefail",
      "ROOT=/var/lib/tokenkey",
      "ENV_FILE=$ROOT/.env",
      "COMPOSE=$ROOT/docker-compose.yml",
      "OVERLAY=$ROOT/docker-compose.prod-pg.yml",
      "echo === current .env PG keys ===",
      "grep -E \"^POSTGRES_(MAX_CONNECTIONS|SHARED_BUFFERS|EFFECTIVE_CACHE_SIZE|MAINTENANCE_WORK_MEM|JIT|MAX_PARALLEL)\" \"$ENV_FILE\" || echo none",
      "echo === overlay present ===",
      "if [ -f \"$OVERLAY\" ]; then wc -c \"$OVERLAY\"; else echo missing; fi",
      "echo === live GUCs ===",
      "sudo docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -c \"SHOW shared_buffers;\" -c \"SHOW effective_cache_size;\" -c \"SHOW jit;\" -c \"SHOW max_parallel_workers;\" -c \"SHOW max_parallel_workers_per_gather;\" -c \"SHOW max_connections;\" || echo postgres_not_queryable"
    ]
    + (if $apply == 1 then [
      "echo === apply install overlay and recreate postgres ===",
      ("printf %s " + $overlay + " | base64 -d | sudo tee \"$OVERLAY\" >/dev/null"),
      "sudo docker compose --env-file \"$ENV_FILE\" -f \"$COMPOSE\" -f \"$OVERLAY\" config --quiet",
      "cd \"$ROOT\"",
      "sudo docker compose --env-file \"$ENV_FILE\" -f \"$COMPOSE\" -f \"$OVERLAY\" up -d --no-deps --force-recreate postgres",
      "for i in $(seq 1 30); do if sudo docker exec tokenkey-postgres pg_isready -U tokenkey -d tokenkey >/dev/null 2>&1; then break; fi; sleep 2; done",
      "sudo docker exec tokenkey-postgres pg_isready -U tokenkey -d tokenkey",
      "echo === .env after ===",
      "grep -E \"^POSTGRES_(MAX_CONNECTIONS|SHARED_BUFFERS|EFFECTIVE_CACHE_SIZE|MAINTENANCE_WORK_MEM|JIT|MAX_PARALLEL)\" \"$ENV_FILE\" || echo none",
      "echo === GUCs after ===",
      "sudo docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -c \"SHOW shared_buffers;\" -c \"SHOW effective_cache_size;\" -c \"SHOW jit;\" -c \"SHOW max_parallel_workers;\" -c \"SHOW max_parallel_workers_per_gather;\" -c \"SHOW max_connections;\""
    ] else [
      "echo dry-run only. pass --apply to install overlay and recreate postgres"
    ] end)
  )
}' >"${params_file}"

CMD_ID="$(aws ssm send-command \
  --region "${REGION}" \
  --instance-ids "${INSTANCE_ID}" \
  --document-name AWS-RunShellScript \
  --comment "${COMMENT}" \
  --parameters "file://${params_file}" \
  --timeout-seconds "${TIMEOUT_SECONDS}" \
  --query 'Command.CommandId' --output text)"

echo "ssm command_id=${CMD_ID}"
aws ssm wait command-executed --region "${REGION}" --command-id "${CMD_ID}" --instance-id "${INSTANCE_ID}" || true
aws ssm get-command-invocation \
  --region "${REGION}" \
  --command-id "${CMD_ID}" \
  --instance-id "${INSTANCE_ID}" \
  --query '{Status:Status,ResponseCode:ResponseCode,Stdout:StandardOutputContent,Stderr:StandardErrorContent}' \
  --output json | tee "${stdout_file}"
jq -r '.Stderr // empty' "${stdout_file}" >"${stderr_file}"
if ! jq -e '.Status == "Success" and .ResponseCode == 0' "${stdout_file}" >/dev/null; then
  echo "PG tuning SSM command did not succeed; see ${stdout_file}" >&2
  exit 1
fi
