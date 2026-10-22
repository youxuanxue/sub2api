#!/usr/bin/env bash
# Write-capable: set every OpenAI OAuth account.concurrency to a shared value.
# Image and text already share this slot once the process-local image limiter is off.
# Delivered via run-probe.sh. Requires APPLY=yes-openai-oauth-concurrency.
set -euo pipefail

# Post Tokyo→Tencent cutover the live PG container may be tk-archive-standby.
if docker ps --format '{{.Names}}' | grep -qx tokenkey-postgres; then
  _TK_PG=tokenkey-postgres
elif docker ps --format '{{.Names}}' | grep -qx tk-archive-standby; then
  _TK_PG=tk-archive-standby
else
  echo '{"verdict":"setup_error","error":"no tokenkey postgres container"}'
  exit 2
fi
PSQL="docker exec ${_TK_PG} psql -U tokenkey -d tokenkey -X -A -t"
APPLY="${APPLY:-}"
CONCURRENCY="${CONCURRENCY:-25}"

case "$CONCURRENCY" in
  ''|*[!0-9]*) echo '{"verdict":"setup_error","error":"CONCURRENCY must be a positive integer"}'; exit 2 ;;
esac
if [ "$CONCURRENCY" -lt 1 ]; then
  echo '{"verdict":"setup_error","error":"CONCURRENCY must be >= 1"}'
  exit 2
fi

echo "=== before openai oauth concurrency ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT id, name, type, status, schedulable, concurrency
  FROM accounts
  WHERE platform = 'openai' AND type = 'oauth' AND deleted_at IS NULL
  ORDER BY id
) t;"

if [ "$APPLY" != "yes-openai-oauth-concurrency" ]; then
  echo "=== dry-run ==="
  echo "{\"apply\":false,\"wanted_concurrency\":$CONCURRENCY}"
  exit 0
fi

echo "=== apply concurrency=$CONCURRENCY ==="
$PSQL -c "
UPDATE accounts
SET concurrency = ${CONCURRENCY}, updated_at = NOW()
WHERE platform = 'openai'
  AND type = 'oauth'
  AND deleted_at IS NULL
  AND concurrency IS DISTINCT FROM ${CONCURRENCY};
"

echo "=== after openai oauth concurrency ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT id, name, type, status, schedulable, concurrency
  FROM accounts
  WHERE platform = 'openai' AND type = 'oauth' AND deleted_at IS NULL
  ORDER BY id
) t;"
