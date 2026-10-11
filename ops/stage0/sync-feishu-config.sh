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
#     bash ops/stage0/sync-feishu-config.sh <edge-id|prod> [--platform auto|lightsail|hetzner]
#
# Examples:
#   ... bash ops/stage0/sync-feishu-config.sh uk1        # auto → deployable Hetzner mi-*
#   ... bash ops/stage0/sync-feishu-config.sh prod       # prod control plane (resolve_prod_ssm_target)
#   ... bash ops/stage0/sync-feishu-config.sh --platform lightsail uk1  # LS standby
#
# Required env:
#   TK_FEISHU_WEBHOOK_URL     shared incoming-webhook URL (https://...)
#   TK_FEISHU_SIGNING_SECRET  shared HMAC-SHA256 signing secret
#   Either empty -> exit 1 (a misconfigured workflow env is caught here, not silently skipped).
#
# Behavior:
#   - Edges: edge_ssm_execution (auto prefers Hetzner Hybrid mi-*; explicit lightsail/hetzner).
#   - prod: resolve_prod_ssm_target (cutover-aware aws i-* / hetzner mi-*).
#   - Idempotent jsonb_set: sets feishu.{webhook_url,signing_secret,enabled,webhook_url_configured,
#     signing_secret_configured}; PRESERVES rate_limit_per_hour / cooldown_seconds /
#     account_incident_digest_seconds and every other existing field.
#   - Reads back and asserts enabled=true + webhook present + secret present; exits 1 otherwise.
#   - Never prints the webhook or secret.
#
# Requires: aws cli, jq, python3.

_OPS_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${_OPS_DIR}/../.." && pwd)"

usage() {
  cat <<'EOF'
Usage:
  TK_FEISHU_WEBHOOK_URL=... TK_FEISHU_SIGNING_SECRET=... \
    bash ops/stage0/sync-feishu-config.sh [--platform auto|lightsail|hetzner] <edge-id|prod>

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
auto | lightsail | hetzner) ;;
ec2)
  echo "[error] --platform ec2 is retired for edges; use auto|lightsail|hetzner" >&2
  exit 1
  ;;
*)
  echo "[error] invalid --platform: ${PLATFORM_PREF} (use auto|lightsail|hetzner)" >&2
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

INSTANCE_ID=""
EC2_STACK=""
if [[ "$EDGE_ID" == "prod" ]]; then
  RESOLVED_JSON="$(python3 "${_OPS_DIR}/resolve_prod_ssm_target.py" --format json)"
  INSTANCE_ID="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["instance_id"])' <<<"${RESOLVED_JSON}")"
  REGION="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["ssm_region"])' <<<"${RESOLVED_JSON}")"
  RES_MODE="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["target"])' <<<"${RESOLVED_JSON}")"
  EC2_STACK="resolve_prod_ssm_target"
  if [[ -z "${INSTANCE_ID:-}" || -z "${REGION:-}" ]]; then
    echo "[error] resolve_prod_ssm_target returned empty instance_id/ssm_region" >&2
    exit 1
  fi
else
  RESOLVED_JSON="$(python3 "${_OPS_DIR}/edge_ssm_execution.py" \
    --repo-root "${REPO_ROOT}" \
    --edge-id "${EDGE_ID}" \
    --platform "${PLATFORM_PREF}" \
    --format json)"
  RES_MODE="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["routing"])' <<<"${RESOLVED_JSON}")"
  REGION="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["region"])' <<<"${RESOLVED_JSON}")"
  INSTANCE_ID="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["instance_id"])' <<<"${RESOLVED_JSON}")"
  if [[ -z "${RES_MODE:-}" || -z "${REGION:-}" || -z "${INSTANCE_ID:-}" ]]; then
    echo "[error] edge_ssm_execution returned empty routing/region/instance_id" >&2
    exit 1
  fi
fi

echo "[info] edge_id=${EDGE_ID} platform=${RES_MODE} region=${REGION} instance_id=${INSTANCE_ID}"
echo "[info] syncing Feishu config via SSM (idempotent; secrets not printed)..."
# Build the SSM command array in Python so the webhook/secret are embedded as
# JSON literals inside a quoted heredoc (<<'EOSQL') — no shell expansion, no
# quoting hell, and arbitrary characters in the values pass through untouched.
# Ubuntu Hybrid (Hetzner) SSM agent runs under dash; wrap the body in bash -lc
# so `set -o pipefail` and heredocs match Amazon Linux EC2 behavior.
COMMANDS_JSON="$(python3 <<'PY'
import json
import os
import shlex

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

script = "\n".join(
    [
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
)
print(json.dumps(["bash -lc " + shlex.quote(script)]))
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

COMMAND_ID="$(aws ssm send-command \
  --region "$REGION" \
  --instance-ids "$INSTANCE_ID" \
  --document-name AWS-RunShellScript \
  --comment "sync feishu config (${EDGE_ID} ${RES_MODE})" \
  --parameters "file://${PARAM_BODY}" \
  --query 'Command.CommandId' \
  --output text)"
INSTANCE_ID_SSM="$INSTANCE_ID"

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
[[ -n "${EC2_STACK:-}" ]] && echo "EC2_STACK=${EC2_STACK}"
echo "SSM_PRIMARY_ID=${INSTANCE_ID_SSM}"
echo "VERIFY=${VERIFY_LINE}"
echo "[ok] feishu alerting enabled; webhook/secret were not printed"
