#!/usr/bin/env bash
# Update an EC2 migration candidate image without exposing an empty-data app.

set -euo pipefail

TAG="${1:-${INPUT_TAG:-}}"
INSTANCE_ID="${2:-${INSTANCE_ID:-}}"
COMMENT="${3:-${SSM_COMMENT:-update-ec2-edge-candidate}}"
OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR:-.}"
TIMEOUT_SECONDS="${STAGE0_SSM_TIMEOUT_SECONDS:-480}"

[[ -n "${TAG}" ]] || { echo "candidate update: tag is required" >&2; exit 1; }
[[ "${INSTANCE_ID}" == i-* ]] || { echo "candidate update: EC2 instance id is required" >&2; exit 1; }
bash "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/validate-deploy-tag.sh" "${TAG}" >/dev/null

mkdir -p "${OUTPUT_DIR}"
params_file="${OUTPUT_DIR}/ssm-params.json"
stdout_file="${OUTPUT_DIR}/stdout.txt"
stderr_file="${OUTPUT_DIR}/stderr.txt"

jq -n --arg tag "${TAG}" '{commands: [
  "set -euo pipefail",
  "sudo install -d -m 0700 /var/lib/tokenkey/migration; exec 9>/var/lib/tokenkey/migration/.action.lock; flock -n 9 || { echo '\''migration action is running; candidate update refused'\'' >&2; exit 75; }; test ! -e /var/lib/tokenkey/migration/.write-owner-locked; test ! -e /var/lib/tokenkey/migration/.target-write-owner-active; test ! -e /var/lib/tokenkey/migration/.target-proxy-retained",
  "sudo systemctl disable --now tokenkey.service tokenkey-pgdump.timer",
  "cd /var/lib/tokenkey",
  "sudo docker compose --env-file .env stop tokenkey caddy >/dev/null 2>&1 || true",
  "CUR=$(sed -n '\''s/^TOKENKEY_IMAGE=//p'\'' .env | head -1); [ -n \"$CUR\" ] || { echo '\''TOKENKEY_IMAGE is missing'\'' >&2; exit 1; }",
  ("REPO=${CUR%:*}; NEW=${REPO}:" + $tag),
  ("sudo cp -a .env .env.before-candidate-" + $tag),
  "sudo sed -i \"s|^TOKENKEY_IMAGE=.*|TOKENKEY_IMAGE=${NEW}|\" .env",
  "sudo docker compose --env-file .env pull tokenkey",
  "test \"$(sudo docker inspect -f '{{.State.Running}}' tokenkey 2>/dev/null || echo false)\" = false",
  "test \"$(sudo docker inspect -f '{{.State.Running}}' tokenkey-caddy 2>/dev/null || echo false)\" = false",
  "test \"$(sudo docker inspect -f '{{.State.Health.Status}}' tokenkey-postgres)\" = healthy",
  "test \"$(sudo docker inspect -f '{{.State.Health.Status}}' tokenkey-redis)\" = healthy",
  "echo CANDIDATE_READY"
]}' >"${params_file}"

if [[ -n "${STAGE0_RENDER_ONLY:-}" ]]; then
  echo "candidate update: STAGE0_RENDER_ONLY set; wrote ${params_file}" >&2
  exit 0
fi

region_args=()
if [[ -n "${AWS_REGION:-${AWS_DEFAULT_REGION:-}}" ]]; then
  region_args=(--region "${AWS_REGION:-${AWS_DEFAULT_REGION}}")
fi

command_id="$(aws ssm send-command \
  "${region_args[@]}" \
  --instance-ids "${INSTANCE_ID}" \
  --document-name AWS-RunShellScript \
  --comment "${COMMENT} tag=${TAG}" \
  --parameters "file://${params_file}" \
  --timeout-seconds "${TIMEOUT_SECONDS}" \
  --query Command.CommandId \
  --output text)"

deadline=$((SECONDS + TIMEOUT_SECONDS))
while (( SECONDS < deadline )); do
  status="$(aws ssm get-command-invocation \
    "${region_args[@]}" \
    --command-id "${command_id}" \
    --instance-id "${INSTANCE_ID}" \
    --query Status \
    --output text 2>/dev/null || true)"
  case "${status}" in
    Success) break ;;
    Failed|TimedOut|Cancelled|Cancelling) break ;;
  esac
  sleep 5
done

aws ssm get-command-invocation \
  "${region_args[@]}" \
  --command-id "${command_id}" \
  --instance-id "${INSTANCE_ID}" \
  --query StandardOutputContent \
  --output text >"${stdout_file}" || true
aws ssm get-command-invocation \
  "${region_args[@]}" \
  --command-id "${command_id}" \
  --instance-id "${INSTANCE_ID}" \
  --query StandardErrorContent \
  --output text >"${stderr_file}" || true

if [[ "${status:-}" != Success ]]; then
  cat "${stderr_file}" >&2
  echo "candidate update failed: status=${status:-timeout} command_id=${command_id}" >&2
  exit 1
fi

cat "${stdout_file}"
