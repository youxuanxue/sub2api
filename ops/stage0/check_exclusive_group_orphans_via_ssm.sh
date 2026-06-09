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
# wired into the edge deploy workflow.
#
# Usage:
#   ops/stage0/check_exclusive_group_orphans_via_ssm.sh <instance_id> [comment]
# Env:
#   AWS_REGION / AWS_DEFAULT_REGION  region (else AWS default chain)
#   PG_CONTAINER  postgres container name (default tokenkey-postgres)
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
PG_CONTAINER="${PG_CONTAINER:-tokenkey-postgres}"
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

# Read-only orphan COUNT + detail (same predicate as the backfill remedy above).
# Single-statement-per-line; piped in as base64 so no shell/JSON quoting of the
# SQL string literals ('subscription', 'orphans=', …) is needed.
read -r -d '' ORPHAN_SQL <<'SQL' || true
\pset pager off
\set ON_ERROR_STOP on
SELECT 'orphans=' || count(*)
FROM api_keys k
JOIN groups g ON g.id = k.group_id AND g.deleted_at IS NULL
JOIN users  u ON u.id = k.user_id  AND u.deleted_at IS NULL
WHERE k.deleted_at IS NULL AND k.group_id IS NOT NULL AND g.is_exclusive = true
  AND g.subscription_type IS DISTINCT FROM 'subscription'
  AND NOT EXISTS (SELECT 1 FROM user_allowed_groups a
                  WHERE a.user_id = k.user_id AND a.group_id = k.group_id);
SELECT 'orphan_detail|' || k.id || '|' || k.user_id || '|' || coalesce(u.email,'') || '|' || k.group_id || '|' || g.name
FROM api_keys k
JOIN groups g ON g.id = k.group_id AND g.deleted_at IS NULL
JOIN users  u ON u.id = k.user_id  AND u.deleted_at IS NULL
WHERE k.deleted_at IS NULL AND k.group_id IS NOT NULL AND g.is_exclusive = true
  AND g.subscription_type IS DISTINCT FROM 'subscription'
  AND NOT EXISTS (SELECT 1 FROM user_allowed_groups a
                  WHERE a.user_id = k.user_id AND a.group_id = k.group_id)
ORDER BY k.user_id, k.id LIMIT 50;
SQL

B64="$(printf '%s' "${ORPHAN_SQL}" | base64 | tr -d '\n')"

# One SSM command element → no unconditional-execution trap (a multi-element
# AWS-RunShellScript array runs every element without `set -e`; a single `&&`-
# chained element fails as a unit). The base64 → psql pipe is the whole job.
REMOTE_CMD="set -euo pipefail && printf %s '${B64}' | base64 -d | docker exec -i ${PG_CONTAINER} psql -U ${PG_USER} -d ${PG_DB} -At -F '|'"

params_file="$(mktemp)"
stdout_file="$(mktemp)"
trap 'rm -f "${params_file}" "${stdout_file}"' EXIT
jq -n --arg cmd "${REMOTE_CMD}" '{commands: [$cmd]}' > "${params_file}"

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
  echo "::warning::exclusive-group orphan check: SSM status=${status}; skipped — NOT a deploy blocker."
  exit 0
fi

count_line="$(grep -E '^orphans=' "${stdout_file}" | head -1 || true)"
orphans="${count_line#orphans=}"
if [[ -z "${orphans}" || ! "${orphans}" =~ ^[0-9]+$ ]]; then
  echo "::warning::exclusive-group orphan check: could not parse orphan count from psql output; skipped — NOT a deploy blocker."
  exit 0
fi

if [[ "${orphans}" -eq 0 ]]; then
  echo "exclusive-group orphan check: ok (orphans=0)"
  exit 0
fi

echo "::warning::exclusive-group orphan check: ${orphans} api key(s) bound to an exclusive standard group whose user is NOT in user_allowed_groups — they will 403 GROUP_NOT_ALLOWED. Confirm intended-revocation vs backfill the grant (remedy SQL in script header). Affected:"
grep -E '^orphan_detail\|' "${stdout_file}" | while IFS='|' read -r _tag key_id user_id email group_id group_name; do
  echo "::warning::  api_key=${key_id} user=${user_id} <${email}> group=${group_id} (${group_name})"
done
exit 0
