#!/usr/bin/env bash
set -euo pipefail

MODE="${1:-}"
INSTANCE_ID="${2:-${INSTANCE_ID:-}}"
shift $(( $# >= 2 ? 2 : $# ))
REGION="${AWS_REGION:-${AWS_DEFAULT_REGION:-us-east-1}}"
TIMEOUT_SECONDS="${STAGE0_SSM_TIMEOUT_SECONDS:-900}"
PLAN_HASH=""
CONFIRM=""

for argument in "$@"; do
  case "${argument}" in
    --plan-hash=*) PLAN_HASH="${argument#--plan-hash=}" ;;
    --confirm=*) CONFIRM="${argument#--confirm=}" ;;
    *) echo "qa-single-owner-via-ssm: unknown argument ${argument}" >&2; exit 40 ;;
  esac
done

[[ "${INSTANCE_ID}" =~ ^i-[0-9a-f]{17}$ ]] || { echo "qa-single-owner-via-ssm: valid prod instance id required" >&2; exit 40; }
[[ "${TIMEOUT_SECONDS}" =~ ^[1-9][0-9]*$ ]] || { echo "qa-single-owner-via-ssm: timeout must be a positive integer" >&2; exit 40; }

case "${MODE}" in
  plan)
    [ -z "${PLAN_HASH}${CONFIRM}" ] || { echo "qa-single-owner-via-ssm: plan accepts no confirmation arguments" >&2; exit 40; }
    remote_command='sudo /usr/local/bin/tokenkey-qa-maintenance.sh --plan-single-owner'
    ;;
  activate)
    [[ "${PLAN_HASH}" =~ ^[0-9a-f]{64}$ ]] || { echo "qa-single-owner-via-ssm: valid plan hash required" >&2; exit 40; }
    expected="tokenkey-prod-qa-single-owner-activate-v1:${PLAN_HASH}"
    [ "${CONFIRM}" = "${expected}" ] || { echo "qa-single-owner-via-ssm: confirmation mismatch" >&2; exit 40; }
    remote_command="set -euo pipefail; sudo /usr/local/bin/tokenkey-qa-maintenance.sh --activate-single-owner --plan-hash=${PLAN_HASH} --confirm=${CONFIRM}; test \"\$(sudo systemctl show tokenkey-qa-boundary.timer --property=UnitFileState --value)\" = disabled; test \"\$(sudo systemctl show tokenkey-qa-boundary.timer --property=ActiveState --value)\" = inactive; sudo systemctl is-enabled --quiet tokenkey-qa-maintenance.timer; sudo systemctl is-active --quiet tokenkey-qa-maintenance.timer; test \"\$(docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1 -c \"SELECT count(*) FROM qa_lifecycle_receipts WHERE phase='single_owner_activate'\" | tr -d '[:space:]')\" = 1"
    ;;
  *)
    echo "usage: $0 plan|activate <prod-instance-id> [--plan-hash=<sha256> --confirm=<token>]" >&2
    exit 40
    ;;
esac

parameters="$(jq -cn --arg command "${remote_command}" '{commands:[$command]}')"
command_id="$(aws --region "${REGION}" ssm send-command \
  --instance-ids "${INSTANCE_ID}" \
  --document-name AWS-RunShellScript \
  --comment "prod QA single-owner ${MODE}" \
  --parameters "${parameters}" \
  --query Command.CommandId --output text)"
echo "ssm command-id=${command_id}" >&2

deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
status=InProgress
while [[ "${status}" =~ ^(Pending|InProgress|Delayed)$ ]]; do
  if [ "$(date +%s)" -ge "${deadline}" ]; then
    echo "qa-single-owner-via-ssm: timed out waiting for ${command_id}" >&2
    exit 1
  fi
  sleep 3
  status="$(aws --region "${REGION}" ssm get-command-invocation \
    --command-id "${command_id}" --instance-id "${INSTANCE_ID}" \
    --query Status --output text)"
done

stdout="$(aws --region "${REGION}" ssm get-command-invocation \
  --command-id "${command_id}" --instance-id "${INSTANCE_ID}" \
  --query StandardOutputContent --output text)"
stderr="$(aws --region "${REGION}" ssm get-command-invocation \
  --command-id "${command_id}" --instance-id "${INSTANCE_ID}" \
  --query StandardErrorContent --output text)"
code="$(aws --region "${REGION}" ssm get-command-invocation \
  --command-id "${command_id}" --instance-id "${INSTANCE_ID}" \
  --query ResponseCode --output text)"
printf '%s\n' "${stdout}"
[ -z "${stderr}" ] || printf '%s\n' "${stderr}" >&2
[[ "${status}" = Success && "${code}" = 0 ]] || {
  echo "qa-single-owner-via-ssm: SSM failed status=${status} code=${code}" >&2
  exit 1
}
