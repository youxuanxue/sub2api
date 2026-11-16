#!/usr/bin/env bash
# Read-only probe for the production Feishu alert configuration.
# It emits one JSON object and never prints secret values.
# PG container comes from the shared resolver (Stage0 or Tencent layout name).
set -euo pipefail

# PG/Redis container names differ between Stage0 (tokenkey-postgres/-redis) and
# the Tencent USW layout; ops/lib/resolve-app-container.sh owns the choice and
# run-probe.sh ships it next to this script.
for _tk_lib in "${TK_LIB_DIR:-$(dirname "${BASH_SOURCE[0]}")}" "$(dirname "${BASH_SOURCE[0]}")/../lib"; do
  [ -f "$_tk_lib/resolve-app-container.sh" ] && break
done
# shellcheck source=../lib/resolve-app-container.sh
. "$_tk_lib/resolve-app-container.sh" || { echo "container resolver not found (ops/lib/resolve-app-container.sh)" >&2; exit 1; }
PG="$(tk_require_postgres_container "${PG_CONTAINER:-${POSTGRES_CONTAINER:-auto}}")" || exit 1
DB="${PG_DB:-tokenkey}"
DBUSER="${PG_USER:-tokenkey}"

psql() { docker exec -i "$PG" psql -U "$DBUSER" -d "$DB" -X -A -t "$@"; }

db_json="$(psql -c "
WITH s AS (
  SELECT value::jsonb AS v FROM settings WHERE key = 'ops_email_notification_config'
)
SELECT json_build_object(
  'row_present', (SELECT count(*) FROM s) > 0,
  'feishu_enabled', (SELECT (v->'feishu'->>'enabled')::boolean FROM s),
  'feishu_webhook_present', (SELECT length(coalesce(v->'feishu'->>'webhook_url','')) > 0 FROM s),
  'feishu_secret_present', (SELECT length(coalesce(v->'feishu'->>'signing_secret','')) > 0 FROM s)
)::text;
")"

env_present() {
  local key="$1"
  [ -r /var/lib/tokenkey/.env ] || return 1
  awk -F= -v wanted="$key" '
    $0 !~ /^[[:space:]]*#/ && $1 ~ "^[[:space:]]*" wanted "[[:space:]]*$" {
      value = substr($0, index($0, "=") + 1)
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", value)
      if (value ~ /^".*"$/ || value ~ /^'\''.*'\''$/) {
        value = substr(value, 2, length(value) - 2)
      }
      if (value != "") found = 1
    }
    END { exit(found ? 0 : 1) }
  ' /var/lib/tokenkey/.env
}

env_webhook=false
env_secret=false
env_present TOKENKEY_FEISHU_WEBHOOK_URL && env_webhook=true
env_present TOKENKEY_FEISHU_WEBHOOK_SECRET && env_secret=true

python3 - "$db_json" "$env_webhook" "$env_secret" <<'PY'
import json
import sys

db = json.loads(sys.argv[1]) if sys.argv[1] else {"row_present": False}
print(json.dumps({
    "schema": "prod-feishu-config/v1",
    "db": db,
    "host_env": {
        "feishu_webhook_present": sys.argv[2] == "true",
        "feishu_secret_present": sys.argv[3] == "true",
    },
}, separators=(",", ":"), sort_keys=True))
PY
