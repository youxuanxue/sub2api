#!/usr/bin/env bash
# Read-only pre-deploy check, including disabled accounts that may be enabled later.
set -euo pipefail

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
    tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1 \
    -c "$SNAPSHOT_SQL")"; then
  echo '{"verdict":"setup_error","error":"snapshot_query_failed"}'
  exit 2
fi
printf '%s\n' "$SNAPSHOT" | python3 /tmp/gemini_web_relay_check.py
