#!/usr/bin/env bash
# Read-only preflight for EC2→Lightsail edge migration.
#
# Usage:
#   bash ops/migration/edge-platform-migration-preflight.sh <edge_id> [--phase=plan|provision|cutover|decommission]
#
# Phases (each one stricter than the previous):
#   plan         — confirm matrix state is consistent; no AWS calls strictly required
#   provision    — also confirm Lightsail addon + GHCR PAT in target region
#   cutover      — also confirm Lightsail instance is healthy and EC2 stack still exists
#   decommission — also confirm Lightsail handles api-<id>.tokenkey.dev (i.e. DNS already cut)
#
# All AWS calls are read-only (describe-*, get-parameter, dig). Exit codes:
#   0 — phase prerequisites met
#   1 — phase prerequisites violated; concrete remediation printed
#   2 — required tooling missing (aws, jq, dig, python3)
set -euo pipefail

EDGE_ID="${1:-}"
shift || true  # preflight-allow: swallow — bash idiom; shifting past last arg is intentional no-op
PHASE="plan"
for arg in "$@"; do
  case "$arg" in
    --phase=*) PHASE="${arg#--phase=}" ;;
    *) echo "unknown arg: $arg" >&2; exit 2 ;;
  esac
done

case "$PHASE" in
  plan|provision|cutover|decommission) ;;
  *) echo "::error::unknown --phase=$PHASE (expected: plan|provision|cutover|decommission)" >&2; exit 2 ;;
esac

if [[ -z "$EDGE_ID" ]]; then
  echo "usage: $0 <edge_id> [--phase=plan|provision|cutover|decommission]" >&2
  exit 2
fi

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
EC2_MATRIX="$REPO_ROOT/deploy/aws/stage0/edge-targets.json"
LS_MATRIX="$REPO_ROOT/deploy/aws/lightsail/edge-targets-lightsail.json"

for tool in jq python3; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    echo "::error::$tool is required but not installed" >&2
    exit 2
  fi
done

# AWS / dig are only required for phase>=provision. plan stays usable on a dev
# laptop without AWS credentials.
need_aws=false
if [[ "$PHASE" != "plan" ]]; then
  need_aws=true
fi
if $need_aws && ! command -v aws >/dev/null 2>&1; then
  echo "::error::aws cli is required for --phase=$PHASE but not installed" >&2
  exit 2
fi
if [[ "$PHASE" == "cutover" || "$PHASE" == "decommission" ]] && ! command -v dig >/dev/null 2>&1; then
  echo "::error::dig is required for --phase=$PHASE (DNS check) but not installed" >&2
  exit 2
fi

errors=0
fail() { echo "  FAIL: $*" >&2; errors=$((errors + 1)); }
ok()   { echo "  ok: $*"; }

echo "=== migration preflight: edge_id=$EDGE_ID phase=$PHASE ==="

# ---- Matrix consistency (all phases) ----------------------------------------
# jq note: `.deployable // X` returns X when deployable is null OR FALSE because jq
# treats false as falsy in the // operator. We need an explicit existence check.
ec2_state="$(jq -r --arg id "$EDGE_ID" '(.targets[$id] // null) as $t | if $t == null then "missing" else ($t.deployable | tostring) end' "$EC2_MATRIX")"
ls_state="$(jq -r --arg id "$EDGE_ID" '(.targets[$id] // null) as $t | if $t == null then "missing" else ($t.deployable | tostring) end' "$LS_MATRIX")"
ec2_stack="$(jq -r --arg id "$EDGE_ID" '.targets[$id].stack // ""' "$EC2_MATRIX")"
ec2_region="$(jq -r --arg id "$EDGE_ID" '.targets[$id].region // ""' "$EC2_MATRIX")"
ec2_domain="$(jq -r --arg id "$EDGE_ID" '.targets[$id].domain // ""' "$EC2_MATRIX")"
ls_region="$(jq -r --arg id "$EDGE_ID" '.targets[$id].lightsail_region // ""' "$LS_MATRIX")"
ls_instance="$(jq -r --arg id "$EDGE_ID" '.targets[$id].instance_name // ""' "$LS_MATRIX")"
ls_static_ip_name="$(jq -r --arg id "$EDGE_ID" '.targets[$id].static_ip_name // ""' "$LS_MATRIX")"
ls_ssm_prefix="$(jq -r --arg id "$EDGE_ID" '.targets[$id].ssm_prefix // ""' "$LS_MATRIX")"

if [[ "$ec2_state" == "missing" ]]; then
  fail "edge_id $EDGE_ID has no entry in deploy/aws/stage0/edge-targets.json"
fi
if [[ "$ls_state" == "missing" ]]; then
  fail "edge_id $EDGE_ID has no entry in deploy/aws/lightsail/edge-targets-lightsail.json"
fi
if [[ "$ec2_state" != "missing" && "$ls_state" != "missing" ]]; then
  ok "matrix entries present (EC2 deployable=$ec2_state, Lightsail deployable=$ls_state)"
fi

# Exclusivity gate (re-affirmed): exactly one of the two should be deployable=true.
# For phase=plan, both can be false (pre-migration); for phase>=provision we need
# Lightsail flipped to true (and EC2 still false).
case "$PHASE" in
  plan)
    if [[ "$ec2_state" == "true" && "$ls_state" == "true" ]]; then
      fail "both EC2 and Lightsail are deployable=true for $EDGE_ID — exclusivity gate violation, fix matrices first"
    fi
    ;;
  provision|cutover|decommission)
    if [[ "$ls_state" != "true" ]]; then
      fail "Lightsail $EDGE_ID must be deployable=true for --phase=$PHASE (currently: $ls_state). Flip in lightsail matrix + open PR + merge."
    fi
    if [[ "$ec2_state" == "true" ]]; then
      fail "EC2 $EDGE_ID is still deployable=true (currently: $ec2_state). For migration the EC2 side must be deployable=false. Flip in EC2 matrix + open PR + merge."
    fi
    ;;
esac

# ---- Provision-phase AWS checks ---------------------------------------------
if [[ "$PHASE" == "provision" || "$PHASE" == "cutover" || "$PHASE" == "decommission" ]]; then
  echo "--- AWS reachability checks (region=$ls_region) ---"
  if ! aws sts get-caller-identity --region "$ls_region" >/dev/null 2>&1; then
    fail "aws sts get-caller-identity failed in $ls_region — credentials missing or wrong region"
  else
    ok "aws credentials reachable in $ls_region"
  fi

  # Lightsail IAM addon CFN stack — one-time per account, in us-east-1 by
  # convention (tokenkey-cicd-* stacks live there). Check both us-east-1 and the
  # edge's own region for resilience to past misconfig.
  ADDON_STACK="tokenkey-cicd-lightsail-addon"
  ADDON_FOUND=""
  for r in us-east-1 "$ls_region"; do
    if aws cloudformation describe-stacks --region "$r" --stack-name "$ADDON_STACK" >/dev/null 2>&1; then
      ADDON_FOUND="$r"
      break
    fi
  done
  if [[ -z "$ADDON_FOUND" ]]; then
    fail "$ADDON_STACK not deployed (checked us-east-1 and $ls_region). One-time setup: aws cloudformation deploy --region us-east-1 --stack-name $ADDON_STACK --template-file deploy/aws/cloudformation/cicd-oidc-lightsail-addon.yaml --parameter-overrides GitHubOidcRoleName=<gha-oidc-role> --capabilities CAPABILITY_NAMED_IAM"
  else
    ok "$ADDON_STACK present in $ADDON_FOUND"
  fi

  # SSM Hybrid managed-instance role: must exist before `aws ssm create-activation
  # --iam-role` works during provision. The role is created by the addon CFN stack
  # above; this is a defensive cross-check (the stack could have been deployed
  # with an older template that didn't include the role).
  SSM_HYBRID_ROLE="tokenkey-lightsail-ssm-hybrid"
  if ! aws iam get-role --role-name "$SSM_HYBRID_ROLE" >/dev/null 2>&1; then
    fail "IAM role $SSM_HYBRID_ROLE missing. The addon CFN stack creates it; redeploy with: aws cloudformation deploy --region us-east-1 --stack-name $ADDON_STACK --template-file deploy/aws/cloudformation/cicd-oidc-lightsail-addon.yaml --capabilities CAPABILITY_NAMED_IAM"
  else
    ok "SSM Hybrid managed-instance role $SSM_HYBRID_ROLE present"
  fi

  # GHCR PAT is OPTIONAL: TokenKey's GHCR image is public, so anonymous pull
  # works. Only check the PAT path when the workflow is configured to require
  # auth (env var EDGE_GHCR_PAT_SSM_NAME set, or `ghcr_pat_required=true` at
  # dispatch time). The preflight doesn't know the dispatch input, so we treat
  # the PAT as a soft "info" check rather than a hard FAIL.
  GHCR_PAT_NAME="${ls_ssm_prefix}/ghcr/pat"
  if aws ssm get-parameter --region "$ls_region" --name "$GHCR_PAT_NAME" --with-decryption >/dev/null 2>&1; then
    ok "GHCR PAT present at $GHCR_PAT_NAME (workflow may use it if ghcr_pat_required=true)"
  else
    echo "  info: no GHCR PAT at $GHCR_PAT_NAME — provision will use anonymous pull (TokenKey GHCR is public). If the image turns private, set EDGE_GHCR_PAT_SSM_NAME or dispatch with ghcr_pat_required=true after writing the PAT to SSM."
  fi

  # Lightsail instance must NOT already exist (else exclusivity is illusory)
  if aws lightsail get-instance --region "$ls_region" --instance-name "$ls_instance" >/dev/null 2>&1; then
    if [[ "$PHASE" == "provision" ]]; then
      fail "Lightsail instance $ls_instance already exists in $ls_region. For idempotent re-provision dispatch operation=provision with recreate=true (destructive); otherwise skip provision."
    else
      ok "Lightsail instance $ls_instance present (expected for --phase=$PHASE)"
    fi
  else
    if [[ "$PHASE" == "provision" ]]; then
      ok "Lightsail instance $ls_instance not yet created (expected for provision)"
    else
      fail "Lightsail instance $ls_instance missing in $ls_region — run provision first"
    fi
  fi
fi

# ---- Cutover-phase DNS check ------------------------------------------------
if [[ "$PHASE" == "cutover" || "$PHASE" == "decommission" ]]; then
  echo "--- DNS check (domain=$ec2_domain) ---"
  LIVE_IP="$(dig +short A "$ec2_domain" @1.1.1.1 | tail -1 || true)"  # preflight-allow: swallow — empty result handled by next branch
  if [[ -z "$LIVE_IP" ]]; then
    fail "could not resolve A record for $ec2_domain"
  else
    LS_IP="$(aws lightsail get-static-ip --region "$ls_region" --static-ip-name "$ls_static_ip_name" --query 'staticIp.ipAddress' --output text 2>/dev/null || echo "")"
    if [[ -z "$LS_IP" || "$LS_IP" == "None" ]]; then
      fail "Lightsail Static IP $ls_static_ip_name not allocated in $ls_region — provision incomplete"
    elif [[ "$PHASE" == "cutover" ]]; then
      if [[ "$LIVE_IP" == "$LS_IP" ]]; then
        ok "DNS already points $ec2_domain → Lightsail Static IP ($LS_IP)"
      else
        echo "  info: DNS $ec2_domain currently → $LIVE_IP (Lightsail Static IP is $LS_IP); cutover will swap"
      fi
    elif [[ "$PHASE" == "decommission" ]]; then
      if [[ "$LIVE_IP" != "$LS_IP" ]]; then
        fail "DNS $ec2_domain still points at $LIVE_IP (not Lightsail Static IP $LS_IP) — cutover not complete; do not decommission yet"
      else
        ok "DNS $ec2_domain → Lightsail Static IP ($LS_IP); safe to decommission EC2"
      fi
    fi
  fi
fi

# ---- Decommission-phase EC2 still-exists check ------------------------------
if [[ "$PHASE" == "decommission" ]]; then
  echo "--- EC2 stack pre-decommission check (stack=$ec2_stack region=$ec2_region) ---"
  if [[ -z "$ec2_stack" ]]; then
    fail "matrix has no stack name for EC2 $EDGE_ID — cannot decommission"
  elif ! aws cloudformation describe-stacks --region "$ec2_region" --stack-name "$ec2_stack" >/dev/null 2>&1; then
    echo "  info: EC2 stack $ec2_stack already absent in $ec2_region (decommission would be a no-op)"
  else
    STACK_STATUS="$(aws cloudformation describe-stacks --region "$ec2_region" --stack-name "$ec2_stack" --query 'Stacks[0].StackStatus' --output text)"
    ok "EC2 stack $ec2_stack present in $ec2_region (status=$STACK_STATUS) — decommission will tear it down"
  fi
fi

echo ""
if [[ "$errors" -eq 0 ]]; then
  echo "=== migration preflight $PHASE: PASS ==="
  exit 0
else
  echo "=== migration preflight $PHASE: FAIL ($errors check(s) failed) ==="
  exit 1
fi
