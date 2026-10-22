#!/usr/bin/env bash
set -euo pipefail

# Idempotently enable the shared Feishu alert configuration on the production
# control plane and verify the remote result.

if [[ "${1:-}" != "prod" || "$#" -ne 1 ]]; then
  echo "usage: sync-feishu-config.sh prod" >&2
  exit 1
fi
if [[ -z "${TK_FEISHU_WEBHOOK_URL:-}" || -z "${TK_FEISHU_SIGNING_SECRET:-}" ]]; then
  echo "TK_FEISHU_WEBHOOK_URL and TK_FEISHU_SIGNING_SECRET are required" >&2
  exit 1
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
resolved="$(python3 "$ROOT/ops/stage0/resolve_prod_ssm_target.py" --format json)"
INSTANCE_ID="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["instance_id"])' <<<"$resolved")"
REGION="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["ssm_region"])' <<<"$resolved")"
[[ -n "$INSTANCE_ID" && -n "$REGION" ]] || { echo "prod resolver returned an incomplete identity" >&2; exit 1; }


RES_MODE="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["target"])' <<<"$resolved")"
# Build the SSM command array in Python so the webhook/secret are embedded as
# JSON literals inside a quoted heredoc (<<'EOSQL') — no shell expansion, no
# quoting hell, and arbitrary characters in the values pass through untouched.
# Ubuntu Hybrid (Hetzner) SSM agent runs under dash; wrap the body in bash -lc
# so `set -o pipefail` and heredocs match Amazon Linux EC2 behavior.
COMMANDS_JSON="$(TK_ROOT="$ROOT" python3 <<'PY'
import importlib.util
import json
import os
import shlex

_spec = importlib.util.spec_from_file_location(
    "tk_resolve_app_container", os.path.join(os.environ["TK_ROOT"], "ops/lib/resolve_app_container.py"))
_tk = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_tk)

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
        # Canonical resolver: Stage0 tokenkey-postgres or Tencent tk-archive-standby.
        _tk.data_container_shell_snippet("postgres", docker="sudo docker", variable="POSTGRES_CONTAINER"),
        "echo \"postgres_container=${POSTGRES_CONTAINER}\"",
        # Ensure the row exists (fresh node before the app has materialized defaults).
        "sudo docker exec \"$POSTGRES_CONTAINER\" psql -U tokenkey -d tokenkey -v ON_ERROR_STOP=1 "
        "-c \"INSERT INTO settings (key, value) SELECT 'ops_email_notification_config', '{}' "
        "WHERE NOT EXISTS (SELECT 1 FROM settings WHERE key = 'ops_email_notification_config');\" >/dev/null",
        # Apply webhook/secret/enabled (quoted heredoc -> values pass through literally).
        "sudo docker exec -i \"$POSTGRES_CONTAINER\" psql -U tokenkey -d tokenkey -v ON_ERROR_STOP=1 <<'EOSQL'",
        update_sql,
        "EOSQL",
        # Read back and verify (never prints the secret values themselves).
        "FEISHU_ENABLED=$(sudo docker exec \"$POSTGRES_CONTAINER\" psql -U tokenkey -d tokenkey -tA "
        "-c \"SELECT coalesce(value::jsonb#>>'{feishu,enabled}','false') FROM settings WHERE key='ops_email_notification_config';\")",
        "FEISHU_WEBHOOK_PRESENT=$(sudo docker exec \"$POSTGRES_CONTAINER\" psql -U tokenkey -d tokenkey -tA "
        "-c \"SELECT (coalesce(value::jsonb#>>'{feishu,webhook_url}','')<>'') FROM settings WHERE key='ops_email_notification_config';\")",
        "FEISHU_SECRET_PRESENT=$(sudo docker exec \"$POSTGRES_CONTAINER\" psql -U tokenkey -d tokenkey -tA "
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
  --comment "sync feishu config (prod ${RES_MODE})" \
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
echo "TARGET=prod"
echo "SSM_ROUTING=${RES_MODE}"
echo "REGION=${REGION}"
echo "SSM_PRIMARY_ID=${INSTANCE_ID_SSM}"
echo "VERIFY=${VERIFY_LINE}"
echo "[ok] feishu alerting enabled; webhook/secret were not printed"
