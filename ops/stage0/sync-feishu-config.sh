#!/usr/bin/env bash
set -euo pipefail

# Idempotently enable the shared Feishu alert configuration on the production
# data plane (settings live on the data machine) and verify the remote result.
# Daily transport: TAT via ssm_execution (TOKENKEY_REMOTE_VIA default tat).
# Break-glass: TOKENKEY_REMOTE_VIA=ssm (legacy deploy-stage0 OIDC path).

if [[ "${1:-}" != "prod" || "$#" -ne 1 ]]; then
  echo "usage: sync-feishu-config.sh prod" >&2
  exit 1
fi
if [[ -z "${TK_FEISHU_WEBHOOK_URL:-}" || -z "${TK_FEISHU_SIGNING_SECRET:-}" ]]; then
  echo "TK_FEISHU_WEBHOOK_URL and TK_FEISHU_SIGNING_SECRET are required" >&2
  exit 1
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# PG settings live on the data machine after the data-plane split.
export PROD_SSM_NODE="${PROD_SSM_NODE:-data}"
export TOKENKEY_REMOTE_VIA="${TOKENKEY_REMOTE_VIA:-tat}"

WORKDIR="$(mktemp -d)"
trap 'rm -rf "${WORKDIR}"' EXIT
REMOTE_SCRIPT_FILE="${WORKDIR}/remote.sh"

TK_ROOT="$ROOT" python3 - "${REMOTE_SCRIPT_FILE}" <<'PY'
import importlib.util
import json
import os
import sys

out_path = sys.argv[1]
_spec = importlib.util.spec_from_file_location(
    "tk_resolve_app_container",
    os.path.join(os.environ["TK_ROOT"], "ops/lib/resolve_app_container.py"),
)
_tk = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_tk)

webhook = os.environ["TK_FEISHU_WEBHOOK_URL"]
secret = os.environ["TK_FEISHU_SIGNING_SECRET"]
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

lines = [
    "set -euo pipefail",
    _tk.data_container_shell_snippet(
        "postgres", docker="sudo docker", variable="POSTGRES_CONTAINER"
    ),
    'echo "postgres_container=${POSTGRES_CONTAINER}"',
    (
        'sudo docker exec "$POSTGRES_CONTAINER" psql -U tokenkey -d tokenkey '
        "-v ON_ERROR_STOP=1 "
        "-c \"INSERT INTO settings (key, value) SELECT 'ops_email_notification_config', '{}' "
        "WHERE NOT EXISTS (SELECT 1 FROM settings WHERE key = 'ops_email_notification_config');\" "
        ">/dev/null"
    ),
    (
        'sudo docker exec -i "$POSTGRES_CONTAINER" psql -U tokenkey -d tokenkey '
        "-v ON_ERROR_STOP=1 <<'EOSQL'"
    ),
    update_sql,
    "EOSQL",
    (
        'FEISHU_ENABLED=$(sudo docker exec "$POSTGRES_CONTAINER" psql -U tokenkey -d tokenkey -tA '
        "-c \"SELECT coalesce(value::jsonb#>>'{feishu,enabled}','false') "
        "FROM settings WHERE key='ops_email_notification_config';\")"
    ),
    (
        'FEISHU_WEBHOOK_PRESENT=$(sudo docker exec "$POSTGRES_CONTAINER" psql -U tokenkey -d tokenkey -tA '
        "-c \"SELECT (coalesce(value::jsonb#>>'{feishu,webhook_url}','')<>'') "
        "FROM settings WHERE key='ops_email_notification_config';\")"
    ),
    (
        'FEISHU_SECRET_PRESENT=$(sudo docker exec "$POSTGRES_CONTAINER" psql -U tokenkey -d tokenkey -tA '
        "-c \"SELECT (coalesce(value::jsonb#>>'{feishu,signing_secret}','')<>'') "
        "FROM settings WHERE key='ops_email_notification_config';\")"
    ),
    (
        'echo "FEISHU_ENABLED=${FEISHU_ENABLED} '
        'FEISHU_WEBHOOK_PRESENT=${FEISHU_WEBHOOK_PRESENT} '
        'FEISHU_SECRET_PRESENT=${FEISHU_SECRET_PRESENT}"'
    ),
    (
        'if [ "${FEISHU_ENABLED}" != "true" ] || [ "${FEISHU_WEBHOOK_PRESENT}" != "t" ] '
        '|| [ "${FEISHU_SECRET_PRESENT}" != "t" ]; then '
        "echo '[error] feishu config not fully applied' >&2; exit 1; fi"
    ),
    "echo FEISHU_SYNC_OK=1",
    # Mirror into data-machine .env for on-box disk-full Feishu when DB is down.
    "sudo sed -i '/^TOKENKEY_FEISHU_WEBHOOK_URL=/d;/^TOKENKEY_FEISHU_WEBHOOK_SECRET=/d' /var/lib/tokenkey/.env",
    "sudo tee -a /var/lib/tokenkey/.env >/dev/null <<'EOENV'",
    "TOKENKEY_FEISHU_WEBHOOK_URL=" + webhook,
    "TOKENKEY_FEISHU_WEBHOOK_SECRET=" + secret,
    "EOENV",
    "echo ENV_FEISHU_SYNC_OK=1",
]
open(out_path, "w", encoding="utf-8").write("\n".join(lines) + "\n")
PY

# Host-parse / dry-run: emit commands JSON only (no remote).
if [[ -n "${STAGE0_SSM_OUTPUT_DIR:-}" ]]; then
  OUTPUT_DIR="${STAGE0_SSM_OUTPUT_DIR}"
  mkdir -p "${OUTPUT_DIR}"
  python3 - "${REMOTE_SCRIPT_FILE}" "${OUTPUT_DIR}/ssm-params.json" <<'PY'
import json
import shlex
import sys

script = open(sys.argv[1], encoding="utf-8").read()
json.dump(
    {"commands": ["bash -lc " + shlex.quote(script)]},
    open(sys.argv[2], "w", encoding="utf-8"),
    ensure_ascii=False,
)
print(f"[info] wrote {sys.argv[2]} (STAGE0_SSM_OUTPUT_DIR; no remote)")
PY
  exit 0
fi

STDOUT_CONTENT="$(
  TK_SYNC_FEISHU_ROOT="$ROOT" TK_SYNC_FEISHU_SCRIPT="$REMOTE_SCRIPT_FILE" python3 <<'PY'
import base64
import importlib.util
import os
import pathlib
import sys

root = pathlib.Path(os.environ["TK_SYNC_FEISHU_ROOT"])
script = pathlib.Path(os.environ["TK_SYNC_FEISHU_SCRIPT"]).read_text(encoding="utf-8")
spec = importlib.util.spec_from_file_location(
    "tk_ssm_execution", root / "ops" / "stage0" / "ssm_execution.py"
)
mod = importlib.util.module_from_spec(spec)
assert spec.loader is not None
spec.loader.exec_module(mod)
shell_b64 = base64.b64encode(script.encode()).decode("ascii")
instance_id = mod.resolve_prod_instance()
out = mod.run_shell_b64(
    instance_id,
    shell_b64,
    f"sync feishu config (prod node={os.environ.get('PROD_SSM_NODE')})",
    preamble=mod.data_container_preamble(),
)
sys.stdout.write(out if out.endswith("\n") else out + "\n")
if "FEISHU_SYNC_OK=1" not in out.splitlines():
    sys.stderr.write("[error] Feishu config sync missing FEISHU_SYNC_OK=1\n")
    sys.exit(1)
PY
)"

VERIFY_LINE="$(printf '%s\n' "$STDOUT_CONTENT" | grep '^FEISHU_ENABLED=' | tail -n 1 || true)"

echo ""
echo "[ok] feishu config sync complete"
echo "TARGET=prod"
echo "PROD_SSM_NODE=${PROD_SSM_NODE}"
echo "TOKENKEY_REMOTE_VIA=${TOKENKEY_REMOTE_VIA}"
echo "VERIFY=${VERIFY_LINE}"
echo "[ok] feishu alerting enabled; webhook/secret were not printed"
