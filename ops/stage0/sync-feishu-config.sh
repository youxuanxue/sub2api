#!/usr/bin/env bash
set -euo pipefail

# Sync the shared Feishu (飞书) webhook + signing secret into a node's
# ops_email_notification_config and enable Feishu alerting — idempotently, via
# SSM + `docker exec tokenkey-postgres psql`. Mirrors reset-edge-admin-password.sh.
#
# Why this exists (2026-06-06 us7 incident):
#   The per-node Feishu webhook/secret live in each node's DB
#   (settings.ops_email_notification_config.feishu); they are NOT in the image
#   and (per repo rule §7) cannot live in git. New edges adopted via the console
#   (us6/us7) skipped the one-off manual write and came up enabled=false /
#   webhook="" → account-incident + P0 cards silently never delivered
#   (TKAccountIncidentNotifier.sendNow short-circuits on !enabled || webhook=="").
#
#   This script is the deterministic injection: deploy workflows call it with the
#   shared webhook/secret from GitHub Actions repo secrets, so every node born via
#   a deploy is alert-capable. The write-back self-verify (below) makes a failed /
#   missing injection FAIL the deploy step — the gate that turns "remember to run
#   the manual step" into a stop-the-line check.
#
# Usage:
#   TK_FEISHU_WEBHOOK_URL=... TK_FEISHU_SIGNING_SECRET=... \
#     bash ops/stage0/sync-feishu-config.sh <edge-id|prod> [--platform auto|ec2|lightsail]
#
# Examples:
#   ... bash ops/stage0/sync-feishu-config.sh us6        # auto-resolves Lightsail vs EC2
#   ... bash ops/stage0/sync-feishu-config.sh prod       # prod Stage0 gateway
#   ... bash ops/stage0/sync-feishu-config.sh --platform ec2 fra1
#
# Required env:
#   TK_FEISHU_WEBHOOK_URL     shared incoming-webhook URL (https://...)
#   TK_FEISHU_SIGNING_SECRET  shared HMAC-SHA256 signing secret
#   Either empty -> exit 1 (a misconfigured workflow env is caught here, not silently skipped).
#
# Behavior:
#   - Resolves EC2 (CFN InstanceId) vs Lightsail (tag SSM) vs prod (fixed us-east-1) like the admin helpers.
#   - Idempotent jsonb_set: sets feishu.{webhook_url,signing_secret,enabled,webhook_url_configured,
#     signing_secret_configured}; PRESERVES rate_limit_per_hour / cooldown_seconds /
#     account_incident_digest_seconds and every other existing field.
#   - Reads back and asserts enabled=true + webhook present + secret present; exits 1 otherwise.
#   - Never prints the webhook or secret.
#
# Requires: aws cli, jq, python3.

_OPS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${_OPS_DIR}/../.." && pwd)"
# shellcheck source=ssm_resolve_invocation_mi.inc.sh
source "${_OPS_DIR}/ssm_resolve_invocation_mi.inc.sh"

usage() {
  cat <<'EOF'
Usage:
  TK_FEISHU_WEBHOOK_URL=... TK_FEISHU_SIGNING_SECRET=... \
    bash ops/stage0/sync-feishu-config.sh [--platform auto|ec2|lightsail] <edge-id|prod>

Sets the shared Feishu webhook+secret into the node's ops_email_notification_config
and enables Feishu alerting (idempotent), then verifies the write. Never prints secrets.
EOF
}

PLATFORM_PREF=auto
while [[ $# -gt 0 ]]; do
  case "$1" in
    -h | --help)
      usage
      exit 0
      ;;
    --platform=*)
      PLATFORM_PREF="${1#*=}"
      shift
      ;;
    --platform)
      PLATFORM_PREF="${2:?--platform requires a value}"
      shift 2
      ;;
    --)
      shift
      break
      ;;
    -*)
      echo "[error] unknown flag: $1" >&2
      usage >&2
      exit 1
      ;;
    *)
      break
      ;;
  esac
done

if [[ $# -ne 1 ]]; then
  usage >&2
  exit 1
fi

INPUT_TARGET="$1"
EDGE_ID="${INPUT_TARGET#edge-}"

case "${PLATFORM_PREF}" in
auto | ec2 | lightsail) ;;
*)
  echo "[error] invalid --platform: ${PLATFORM_PREF} (use auto|ec2|lightsail)" >&2
  exit 1
  ;;
esac

if [[ "$EDGE_ID" != "prod" && ! "$EDGE_ID" =~ ^[a-z]{2,4}[0-9]+$ ]]; then
  echo "[error] target must be 'prod' or an edge id matching ^[a-z]{2,4}[0-9]+$: $EDGE_ID" >&2
  exit 1
fi

# Required secrets — fail fast and loud (do NOT silently skip).
if [[ -z "${TK_FEISHU_WEBHOOK_URL:-}" || -z "${TK_FEISHU_SIGNING_SECRET:-}" ]]; then
  echo "[error] TK_FEISHU_WEBHOOK_URL and TK_FEISHU_SIGNING_SECRET must both be set (non-empty)." >&2
  echo "[error] Set them as GitHub Actions repository secrets, or export them locally." >&2
  exit 1
fi

RES_LINE="$(python3 "${_OPS_DIR}/edge_admin_resolve_target.py" "${REPO_ROOT}" "${EDGE_ID}" "${PLATFORM_PREF}" | tr -d '\r')"
IFS=$'\t' read -r RES_MODE REGION EC2_STACK <<<"$RES_LINE"

if [[ -z "${RES_MODE:-}" ]]; then
  echo "[error] resolver returned empty output" >&2
  exit 1
fi

if [[ "$RES_MODE" == ec2 ]]; then
  INSTANCE_ID_EC2=""
  INSTANCE_ID_EC2="$(aws cloudformation describe-stacks \
    --region "$REGION" \
    --stack-name "$EC2_STACK" \
    --query 'Stacks[0].Outputs[?OutputKey==`InstanceId`].OutputValue' \
    --output text)"
  if [[ -z "${INSTANCE_ID_EC2:-}" || "${INSTANCE_ID_EC2:-}" == "None" ]]; then
    echo "[error] could not resolve InstanceId from stack ${EC2_STACK} in ${REGION}" >&2
    exit 1
  fi
fi

echo "[info] edge_id=${EDGE_ID} platform=${RES_MODE} region=${REGION} stack=${EC2_STACK:-<lightsail-tags>}"
if [[ "$RES_MODE" == ec2 ]]; then
  echo "[info] ec2_instance_id=${INSTANCE_ID_EC2}"
fi
echo "[info] syncing Feishu config via SSM (idempotent; secrets not printed)..."

# Build the SSM command array in Python so the webhook/secret are embedded as
# JSON literals inside a quoted heredoc (<<'EOSQL') — no shell expansion, no
# quoting hell, and arbitrary characters in the values pass through untouched.
COMMANDS_JSON="$(python3 <<'PY'
import json
import os

webhook = os.environ["TK_FEISHU_WEBHOOK_URL"]
secret = os.environ["TK_FEISHU_SIGNING_SECRET"]

# json.dumps -> a JSON string literal (with surrounding quotes), safe to drop
# straight into a jsonb_set value via $tag$-dollar-quoting.
wj = json.dumps(webhook)
sj = json.dumps(secret)

update_sql = (
    "UPDATE settings SET value = "
    "jsonb_set(jsonb_set(jsonb_set(jsonb_set(jsonb_set("
    "coalesce(NULLIF(value, '')::jsonb, '{}'::jsonb), "
    "'{feishu,webhook_url}', $wj$" + wj + "$wj$::jsonb, true), "
    "'{feishu,signing_secret}', $sj$" + sj + "$sj$::jsonb, true), "
    "'{feishu,enabled}', 'true'::jsonb, true), "
    "'{feishu,webhook_url_configured}', 'true'::jsonb, true), "
    "'{feishu,signing_secret_configured}', 'true'::jsonb, true"
    ")::text, updated_at = now() WHERE key = 'ops_email_notification_config';"
)

commands = [
    "set -euo pipefail",
    # Ensure the row exists (fresh node before the app has materialized defaults).
    "sudo docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -v ON_ERROR_STOP=1 "
    "-c \"INSERT INTO settings (key, value) SELECT 'ops_email_notification_config', '{}' "
    "WHERE NOT EXISTS (SELECT 1 FROM settings WHERE key = 'ops_email_notification_config');\" >/dev/null",
    # Apply webhook/secret/enabled (quoted heredoc -> values pass through literally).
    "sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -v ON_ERROR_STOP=1 <<'EOSQL'",
    update_sql,
    "EOSQL",
    # Read back and verify (never prints the secret values themselves).
    "FEISHU_ENABLED=$(sudo docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -tA "
    "-c \"SELECT coalesce(value::jsonb#>>'{feishu,enabled}','false') FROM settings WHERE key='ops_email_notification_config';\")",
    "FEISHU_WEBHOOK_PRESENT=$(sudo docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -tA "
    "-c \"SELECT (coalesce(value::jsonb#>>'{feishu,webhook_url}','')<>'') FROM settings WHERE key='ops_email_notification_config';\")",
    "FEISHU_SECRET_PRESENT=$(sudo docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -tA "
    "-c \"SELECT (coalesce(value::jsonb#>>'{feishu,signing_secret}','')<>'') FROM settings WHERE key='ops_email_notification_config';\")",
    "echo \"FEISHU_ENABLED=${FEISHU_ENABLED} FEISHU_WEBHOOK_PRESENT=${FEISHU_WEBHOOK_PRESENT} FEISHU_SECRET_PRESENT=${FEISHU_SECRET_PRESENT}\"",
    "if [ \"${FEISHU_ENABLED}\" != \"true\" ] || [ \"${FEISHU_WEBHOOK_PRESENT}\" != \"t\" ] || [ \"${FEISHU_SECRET_PRESENT}\" != \"t\" ]; then echo '[error] feishu config not fully applied' >&2; exit 1; fi",
    "echo FEISHU_SYNC_OK=1",
    # Mirror webhook/secret into /var/lib/tokenkey/.env so the on-box disk-full
    # Feishu alert (tokenkey-disk-metrics.sh) can read them when the app/DB is
    # DOWN — which is exactly when a full disk strikes. The DB copy above feeds
    # the in-app alert path; this .env copy feeds the independent on-box timer.
    # Quoted heredoc => values pass through literally, never echoed.
    "sudo sed -i '/^TOKENKEY_FEISHU_WEBHOOK_URL=/d;/^TOKENKEY_FEISHU_WEBHOOK_SECRET=/d' /var/lib/tokenkey/.env",
    "sudo tee -a /var/lib/tokenkey/.env >/dev/null <<'EOENV'",
    "TOKENKEY_FEISHU_WEBHOOK_URL=" + webhook,
    "TOKENKEY_FEISHU_WEBHOOK_SECRET=" + secret,
    "EOENV",
    "echo ENV_FEISHU_SYNC_OK=1",
]
print(json.dumps(commands))
PY
)"

# Emit the SSM params file. Honor STAGE0_SSM_OUTPUT_DIR so the host-parse guard
# (scripts/checks/check-stage0-ssm-host-parse.sh) can stub `aws`, capture the
# rendered commands, and `bash -n` them without contacting AWS — same convention
# as deploy_via_ssm.sh.
if [[ -n "${STAGE0_SSM_OUTPUT_DIR:-}" ]]; then
  OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR}"
else
  OUTPUT_DIR="$(mktemp -d)"
  trap 'rm -rf "${OUTPUT_DIR}"' EXIT
fi
PARAM_BODY="${OUTPUT_DIR}/ssm-params.json"
printf '{"commands":%s}\n' "${COMMANDS_JSON}" >"${PARAM_BODY}"

COMMAND_ID=""
if [[ "$RES_MODE" == lightsail ]]; then
  COMMAND_ID="$(aws ssm send-command \
    --region "$REGION" \
    --targets "Key=tag:EdgeId,Values=${EDGE_ID}" "Key=tag:Platform,Values=lightsail" \
    --document-name AWS-RunShellScript \
    --comment "sync feishu config (${EDGE_ID} lightsail)" \
    --parameters "file://${PARAM_BODY}" \
    --query 'Command.CommandId' \
    --output text)"
  INSTANCE_ID_SSM=""
  INSTANCE_ID_SSM="$(ssm_resolve_invocation_mi "$REGION" "$COMMAND_ID")"
else
  COMMAND_ID="$(aws ssm send-command \
    --region "$REGION" \
    --instance-ids "$INSTANCE_ID_EC2" \
    --document-name AWS-RunShellScript \
    --comment "sync feishu config (${EDGE_ID} ec2)" \
    --parameters "file://${PARAM_BODY}" \
    --query 'Command.CommandId' \
    --output text)"
  INSTANCE_ID_SSM="$INSTANCE_ID_EC2"
fi

echo "[info] ssm_command_id=${COMMAND_ID}"
echo "[info] ssm_invocation_instance_id=${INSTANCE_ID_SSM}"

if ! aws ssm wait command-executed \
  --region "$REGION" \
  --command-id "$COMMAND_ID" \
  --instance-id "$INSTANCE_ID_SSM"; then
  echo "[warn] waiter reported non-success; fetching invocation details..." >&2
fi

STATUS="$(aws ssm get-command-invocation \
  --region "$REGION" \
  --command-id "$COMMAND_ID" \
  --instance-id "$INSTANCE_ID_SSM" \
  --query 'Status' \
  --output text)"

STDOUT_CONTENT="$(aws ssm get-command-invocation \
  --region "$REGION" \
  --command-id "$COMMAND_ID" \
  --instance-id "$INSTANCE_ID_SSM" \
  --query 'StandardOutputContent' \
  --output text)"

STDERR_CONTENT="$(aws ssm get-command-invocation \
  --region "$REGION" \
  --command-id "$COMMAND_ID" \
  --instance-id "$INSTANCE_ID_SSM" \
  --query 'StandardErrorContent' \
  --output text)"

if [[ "${STATUS:-}" != "Success" ]] || ! printf '%s\n' "$STDOUT_CONTENT" | grep -q '^FEISHU_SYNC_OK=1$'; then
  echo "[error] Feishu config sync failed: status=${STATUS:-empty}" >&2
  if [[ -n "${STDOUT_CONTENT:-}" ]]; then
    echo "[error] stdout:" >&2
    printf '%s\n' "$STDOUT_CONTENT" >&2
  fi
  if [[ -n "${STDERR_CONTENT:-}" ]]; then
    echo "[error] stderr:" >&2
    printf '%s\n' "$STDERR_CONTENT" >&2
  fi
  exit 1
fi

# Surface the verify line (enabled / webhook-present / secret-present booleans; no secret values).
VERIFY_LINE="$(printf '%s\n' "$STDOUT_CONTENT" | grep '^FEISHU_ENABLED=' | tail -n 1 || true)"

echo ""
echo "[ok] feishu config sync complete"
echo "EDGE_ID=${EDGE_ID}"
echo "SSM_ROUTING=${RES_MODE}"
echo "REGION=${REGION}"
[[ "$RES_MODE" == ec2 ]] && echo "EC2_STACK=${EC2_STACK}" && echo "EC2_INSTANCE_ID=${INSTANCE_ID_EC2}"
[[ "$RES_MODE" == lightsail ]] && echo "SSM_TAGS=EdgeId=${EDGE_ID},Platform=lightsail"
echo "SSM_PRIMARY_ID=${INSTANCE_ID_SSM}"
echo "VERIFY=${VERIFY_LINE}"
echo "[ok] feishu alerting enabled; webhook/secret were not printed"
