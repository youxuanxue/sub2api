#!/usr/bin/env bash
# Detect drift between a repo CloudFormation template and its LIVE stack.
#
# Why: the cicd-oidc-lightsail-addon / cicd-oidc stacks are NOT auto-deployed by
# any workflow — an operator deploys them by hand. So a template edit in git can
# silently never reach the live stack. On 2026-06-07 the addon template already
# carried `lightsail:OpenInstancePublicPorts` but the live stack was a stale
# older deploy, so workflow provisions hit AccessDenied opening 80/443 and edges
# came up firewall-closed. This check catches that class: "template edited in git
# but never redeployed".
#
# How: create a no-execute changeset from the repo template against the live
# stack and assert it contains NO changes. This is CloudFormation's own diff, so
# it catches resource/policy/parameter drift in either direction — no hand-rolled
# comparison to fall out of date.
#
# Scope: works for any of the hand-deployed CFN stacks. Pass UsePreviousValue
# parameters via a params file when the live stack uses per-stack
# --parameter-overrides (the OIDC stacks) so only template-body drift is flagged,
# not legitimate per-environment parameter values.
#
# Auth: needs cloudformation:CreateChangeSet/DescribeChangeSet/DeleteChangeSet
# plus the stack's CAPABILITY_NAMED_IAM. Run on demand by an operator with
# PowerUserAccess (e.g. Tech-Partner); NOT wired into ops-daily-diagnostics.yml
# because that OIDC role is scoped to DescribeStacks only and granting it
# CreateChangeSet would widen the CI attack surface for a low-frequency check.
#
# Exit codes: 0 = in sync, 1 = drift detected, 2 = AWS/usage error.
set -euo pipefail

usage() {
  echo "usage: $0 <stack-name> <region> <template-file> [params-json-file]" >&2
  echo "  params-json-file: optional; CFN --parameters JSON, typically all" >&2
  echo "    {\"ParameterKey\":...,\"UsePreviousValue\":true} to isolate template drift." >&2
  exit 2
}

STACK="${1:-}"; REGION="${2:-}"; TEMPLATE="${3:-}"; PARAMS_FILE="${4:-}"
[[ -n "$STACK" && -n "$REGION" && -n "$TEMPLATE" ]] || usage
[[ -f "$TEMPLATE" ]] || { echo "::error::template not found: $TEMPLATE" >&2; exit 2; }
[[ -z "$PARAMS_FILE" || -f "$PARAMS_FILE" ]] || { echo "::error::params file not found: $PARAMS_FILE" >&2; exit 2; }

# Changeset name: deterministic-ish, unique per run. PID + epoch avoids collision
# without needing a UUID tool.
CS="drift-check-$$-$(date +%s)"

cleanup() {
  aws cloudformation delete-change-set --region "$REGION" \
    --stack-name "$STACK" --change-set-name "$CS" >/dev/null 2>&1 || true
}
trap cleanup EXIT

cs_args=(--region "$REGION" --stack-name "$STACK" --change-set-name "$CS"
  --change-set-type UPDATE --template-body "file://${TEMPLATE}"
  --capabilities CAPABILITY_NAMED_IAM CAPABILITY_IAM)
[[ -n "$PARAMS_FILE" ]] && cs_args+=(--parameters "file://${PARAMS_FILE}")

if ! aws cloudformation create-change-set "${cs_args[@]}" >/dev/null 2>/tmp/cfn-drift.$$; then
  echo "::error::create-change-set failed for ${STACK} (${REGION}):" >&2
  cat /tmp/cfn-drift.$$ >&2; rm -f /tmp/cfn-drift.$$
  exit 2
fi
rm -f /tmp/cfn-drift.$$

# Wait for the changeset to finish computing. An EMPTY changeset (no drift) ends
# in status FAILED with a "didn't contain changes / No updates" reason — that is
# the in-sync signal, not an error.
aws cloudformation wait change-set-create-complete --region "$REGION" \
  --stack-name "$STACK" --change-set-name "$CS" >/dev/null 2>&1 || true

status="$(aws cloudformation describe-change-set --region "$REGION" \
  --stack-name "$STACK" --change-set-name "$CS" --query 'Status' --output text 2>/dev/null || echo UNKNOWN)"
reason="$(aws cloudformation describe-change-set --region "$REGION" \
  --stack-name "$STACK" --change-set-name "$CS" --query 'StatusReason' --output text 2>/dev/null || echo '')"

if [[ "$status" == "FAILED" ]] && grep -qiE "didn't contain changes|No updates are to be performed" <<<"$reason"; then
  echo "ok: ${STACK} (${REGION}) live == repo template (${TEMPLATE})"
  exit 0
fi

if [[ "$status" == "CREATE_COMPLETE" ]]; then
  echo "::error::DRIFT: ${STACK} (${REGION}) live differs from repo template ${TEMPLATE}." >&2
  echo "        Redeploy the template (the stack is not auto-deployed). Pending changes:" >&2
  aws cloudformation describe-change-set --region "$REGION" \
    --stack-name "$STACK" --change-set-name "$CS" \
    --query 'Changes[].ResourceChange.{action:Action,id:LogicalResourceId,replacement:Replacement,scope:Scope}' \
    --output table >&2 || true
  exit 1
fi

echo "::error::could not determine drift for ${STACK} (${REGION}): status=${status} reason=${reason}" >&2
exit 2
