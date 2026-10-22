#!/usr/bin/env bash
# Read-only pre-deploy check, including disabled accounts that may be enabled later.
set -euo pipefail

# Stage0 compose uses tokenkey-postgres; Tencent USW layout exception keeps
# primary as tk-archive-standby (network alias postgres).
POSTGRES_CONTAINER="${POSTGRES_CONTAINER:-}"
if [ -z "$POSTGRES_CONTAINER" ]; then
  if docker ps --format '{{.Names}}' 2>/dev/null | grep -qx tokenkey-postgres; then
    POSTGRES_CONTAINER=tokenkey-postgres
  elif docker ps --format '{{.Names}}' 2>/dev/null | grep -qx tk-archive-standby; then
    POSTGRES_CONTAINER=tk-archive-standby
  else
    echo '{"verdict":"setup_error","error":"no_postgres_container"}'
    exit 2
  fi
fi

CHECK_PY="${GEMINI_WEB_RELAY_CHECK:-/tmp/gemini_web_relay_check.py}"

SNAPSHOT_SQL=$(cat <<'SQL'
SELECT jsonb_build_object('accounts', COALESCE(jsonb_agg(jsonb_build_object(
    'id', id, 'platform', platform, 'type', type,
    'declared_web', COALESCE(extra->>'relay_kind' = 'gemini_web', false),
    'marker', credentials->'gemini_web_relay'
) ORDER BY id), '[]'::jsonb))::text
FROM accounts
WHERE deleted_at IS NULL
  AND (extra->>'relay_kind' = 'gemini_web' OR credentials ? 'gemini_web_relay');
SQL
)

if ! SNAPSHOT="$(docker exec -i \
    -e 'PGOPTIONS=-c default_transaction_read_only=on -c lock_timeout=100ms -c statement_timeout=10s' \
    "$POSTGRES_CONTAINER" psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1 \
    -c "$SNAPSHOT_SQL")"; then
  echo '{"verdict":"setup_error","error":"snapshot_query_failed"}'
  exit 2
fi
printf '%s\n' "$SNAPSHOT" | python3 "$CHECK_PY"
