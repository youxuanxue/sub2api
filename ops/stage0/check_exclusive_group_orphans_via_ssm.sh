#!/usr/bin/env bash
#
# Stage0 prod-only post-deploy DATA-INVARIANT check: exclusive-group orphan keys.
#
# Why this exists:
#   #669 made `user_allowed_groups` load-bearing at request time — an API key
#   bound to an EXCLUSIVE, non-subscription ("standard") group whose user is NOT
#   in that group's allowed-list now gets 403 GROUP_NOT_ALLOWED. The sanctioned
#   admin-UI provisioning path (AdminUpdateAPIKeyGroupID / migrate-group) grants
#   `user_allowed_groups` atomically when it binds, and self-service key creation
#   gates on it — so NO code path can produce such an "orphan". The only way one
#   appears is a human hand-editing `api_keys.group_id` in the DB (bypassing the
#   UI), or legacy rows from before grant-on-bind existed.
#
#   Rather than run a standing reconciler to watch for a state the sanctioned
#   workflow already makes unreachable, this is a CHEAP, READ-ONLY check run at
#   the one moment the risk is real: a deploy (which often follows DB surgery).
#   It is ADVISORY ONLY — it NEVER fails the deploy. orphans>0 emits a
#   ::warning:: listing (key,user,group) so an operator can decide: backfill the
#   grant (legacy/provisioning slip) or confirm it's an intended revocation.
#
#   Remedy when it fires (idempotent, safe to re-run):
#     INSERT INTO user_allowed_groups (user_id, group_id, created_at)
#     SELECT DISTINCT k.user_id, k.group_id, now() FROM api_keys k
#       JOIN groups g ON g.id=k.group_id AND g.deleted_at IS NULL
#       JOIN users  u ON u.id=k.user_id  AND u.deleted_at IS NULL
#      WHERE k.deleted_at IS NULL AND k.group_id IS NOT NULL AND g.is_exclusive
#        AND g.subscription_type IS DISTINCT FROM 'subscription'
#        AND NOT EXISTS (SELECT 1 FROM user_allowed_groups a
#                        WHERE a.user_id=k.user_id AND a.group_id=k.group_id)
#     ON CONFLICT (user_id, group_id) DO NOTHING;
#   (only after confirming the affected users SHOULD retain access.)
#
# Prod-only: api_keys/users/groups live on the prod control-plane DB. Edge
# Stage0 stacks are anthropic-OAuth relays with no such tables, so this is NOT
# run before production releases.
#
# Usage:
#   ops/stage0/check_exclusive_group_orphans_via_ssm.sh <instance_id> [comment]
# Env:
#   AWS_REGION / AWS_DEFAULT_REGION  region (else AWS default chain)
#   PG_CONTAINER  postgres container name (default: resolved on host, Stage0 or Tencent name)
#   PG_USER       psql user (default tokenkey)
#   PG_DB         psql db   (default tokenkey)
#   SSM_TIMEOUT_SECONDS  invocation wait budget (default 120)
#
# Exit status: ALWAYS 0 except on usage error (missing instance_id). The data
# finding is surfaced via ::warning:: lines, never via a non-zero exit — the
# deploy must not be blocked by a data-layer observation.

set -euo pipefail

INSTANCE_ID="${1:-${INSTANCE_ID:-}}"
COMMENT="${2:-stage0 exclusive-group orphan check}"
PG_CONTAINER="${PG_CONTAINER:-}"
PG_USER="${PG_USER:-tokenkey}"
PG_DB="${PG_DB:-tokenkey}"
SSM_TIMEOUT_SECONDS="${SSM_TIMEOUT_SECONDS:-120}"

if [[ -z "${INSTANCE_ID}" ]]; then
  echo "usage: $0 <instance_id> [comment]" >&2
  exit 2
fi

ssm_region_args=()
_region="${AWS_REGION:-${AWS_DEFAULT_REGION:-}}"
[[ -n "${_region}" ]] && ssm_region_args=(--region "${_region}")

# Shared read-only query keeps the active-impact predicate identical in the
# count and detail paths. Dormant historical resources are inventory only.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ORPHAN_SQL="$(cat "$SCRIPT_DIR/exclusive_group_orphans.sql")"

B64="$(printf '%s' "${ORPHAN_SQL}" | base64 | tr -d '\n')"

# One SSM command element → no unconditional-execution trap (a multi-element
# AWS-RunShellScript array runs every element without `set -e`; a single `&&`-
# chained element fails as a unit). The base64 → psql pipe is the whole job.
if [ -n "${PG_CONTAINER}" ]; then
  PG_RESOLVE="TK_PG=${PG_CONTAINER}"
else
  # sudo: Hybrid SSM is not always in the docker group. The snippet's inspect
  # and the psql exec must use the same docker binary.
  PG_RESOLVE="$(python3 "$(dirname "${BASH_SOURCE[0]}")/../lib/resolve_app_container.py" snippet postgres --docker "sudo docker")"
fi
REMOTE_CMD="set -euo pipefail && ${PG_RESOLVE} && printf %s '${B64}' | base64 -d | sudo docker exec -i \"\$TK_PG\" psql -U ${PG_USER} -d ${PG_DB} -At -F '|'"

params_file="$(mktemp)"
stdout_file="$(mktemp)"
stderr_file="$(mktemp)"
trap 'rm -f "${params_file}" "${stdout_file}" "${stderr_file}"' EXIT
jq -n --arg cmd "${REMOTE_CMD}" '{commands: [$cmd]}' > "${params_file}"
# Hybrid RunShellScript starts under dash, which rejects pipefail.
python3 "${SCRIPT_DIR}/ssm_wrap_bash_commands.py" "${params_file}"

if [ "${EXCLUSIVE_GROUP_RENDER_ONLY:-0}" = "1" ]; then
  RENDER_DIR="${EXCLUSIVE_GROUP_RENDER_DIR:-.}"
  mkdir -p "${RENDER_DIR}"
  cp "${params_file}" "${RENDER_DIR}/ssm-params.json"
  echo "exclusive-group orphan check: render-only params=${RENDER_DIR}/ssm-params.json"
  exit 0
fi

cmd_id="$(aws "${ssm_region_args[@]}" ssm send-command \
  --instance-ids "${INSTANCE_ID}" \
  --document-name AWS-RunShellScript \
  --comment "${COMMENT}" \
  --parameters "file://${params_file}" \
  --query 'Command.CommandId' --output text 2>/dev/null || echo "")"

if [[ -z "${cmd_id}" || "${cmd_id}" == "None" ]]; then
  echo "::warning::exclusive-group orphan check: SSM send-command failed (instance=${INSTANCE_ID}); skipped — NOT a deploy blocker."
  exit 0
fi
echo "exclusive-group orphan check: ssm command-id=${cmd_id}"

deadline=$(( $(date +%s) + SSM_TIMEOUT_SECONDS ))
status="InProgress"
while true; do
  status="$(aws "${ssm_region_args[@]}" ssm get-command-invocation \
    --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
    --query 'Status' --output text 2>/dev/null || echo InProgress)"
  case "${status}" in Success|Failed|TimedOut|Cancelled) break ;; esac
  if [[ $(date +%s) -ge ${deadline} ]]; then status="TimedOut"; break; fi
  sleep 4
done

aws "${ssm_region_args[@]}" ssm get-command-invocation \
  --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
  --query 'StandardOutputContent' --output text > "${stdout_file}" 2>/dev/null || true

if [[ "${status}" != "Success" ]]; then
  aws "${ssm_region_args[@]}" ssm get-command-invocation \
    --command-id "${cmd_id}" --instance-id "${INSTANCE_ID}" \
    --query 'StandardErrorContent' --output text > "${stderr_file}" 2>/dev/null || true
  detail="$(tr '\n' ' ' < "${stderr_file}" | cut -c1-400)"
  echo "::warning::exclusive-group orphan check: SSM status=${status}; skipped — NOT a deploy blocker. ${detail}"
  exit 0
fi

count_line="$(grep -E '^orphans=' "${stdout_file}" | head -1 || true)"
orphans="${count_line#orphans=}"
if [[ -z "${orphans}" || ! "${orphans}" =~ ^[0-9]+$ ]]; then
  echo "::warning::exclusive-group orphan check: could not parse orphan count from psql output; skipped — NOT a deploy blocker."
  exit 0
fi

dormant_line="$(grep -E '^dormant_orphans=[0-9]+$' "${stdout_file}" | head -1 || true)"
[[ -z "$dormant_line" ]] || echo "exclusive-group orphan inventory: $dormant_line (inactive user/key/group; no active-access finding)"

if [[ "${orphans}" -eq 0 ]]; then
  echo "exclusive-group orphan check: ok (orphans=0)"
  exit 0
fi

echo "::warning::exclusive-group orphan check: ${orphans} api key(s) bound to an exclusive standard group whose user is NOT in user_allowed_groups — they will 403 GROUP_NOT_ALLOWED. Confirm intended-revocation vs backfill the grant (remedy SQL in script header). Affected:"
# `|| true` guard: under `set -e`+`pipefail` a no-match grep (exit 1) would abort
# the script before the final `exit 0`, breaking the "ALWAYS exit 0" contract.
{ grep -E '^orphan_detail\|' "${stdout_file}" || true; } | while IFS='|' read -r _tag key_id user_id email group_id group_name; do
  echo "::warning::  api_key=${key_id} user=${user_id} <${email}> group=${group_id} (${group_name})"
done
exit 0
