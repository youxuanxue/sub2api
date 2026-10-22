#!/bin/bash
# probe-account-emails.sh — Read-only fleet audit: non-deleted account OAuth email
# fields + tk_anthropic_request_normalize_enabled setting.
#
# Run via ops/observability/run-probe.sh on prod.
# Output: row_to_json lines (field names embedded).
set -u

# PG/Redis container names differ between Stage0 (tokenkey-postgres/-redis) and
# the Tencent USW layout; ops/lib/resolve-app-container.sh owns the choice and
# run-probe.sh ships it next to this script.
for _tk_lib in "${TK_LIB_DIR:-$(dirname "${BASH_SOURCE[0]}")}" "$(dirname "${BASH_SOURCE[0]}")/../lib"; do
  [ -f "$_tk_lib/resolve-app-container.sh" ] && break
done
# shellcheck source=../lib/resolve-app-container.sh
. "$_tk_lib/resolve-app-container.sh" || { echo "container resolver not found (ops/lib/resolve-app-container.sh)" >&2; exit 1; }
TK_PG="$(tk_require_postgres_container "${POSTGRES_CONTAINER:-auto}")" || exit 1
PSQL="docker exec $TK_PG psql -U tokenkey -d tokenkey -X -A -t"

echo "=== setting: tk_anthropic_request_normalize_enabled ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT key, value, updated_at
  FROM settings
  WHERE key = 'tk_anthropic_request_normalize_enabled'
) t;
" 2>&1

echo
echo "=== accounts (deleted_at IS NULL; email fields from extra + credentials) ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT
    a.id,
    a.name,
    a.platform,
    a.type,
    a.status,
    a.schedulable,
    NULLIF(trim(a.extra->>'email_address'), '') AS extra_email_address,
    NULLIF(trim(a.extra->>'email'), '') AS extra_email,
    NULLIF(trim(a.credentials->>'email'), '') AS cred_email,
    NULLIF(trim(a.credentials->>'email_address'), '') AS cred_email_address,
    COALESCE(
      NULLIF(trim(a.extra->>'email_address'), ''),
      NULLIF(trim(a.extra->>'email'), ''),
      NULLIF(trim(a.credentials->>'email_address'), ''),
      NULLIF(trim(a.credentials->>'email'), '')
    ) AS resolved_email
  FROM accounts a
  WHERE a.deleted_at IS NULL
  ORDER BY a.platform, a.type, a.id
) t;
" 2>&1

echo
echo "=== summary: schedulable OAuth accounts missing resolved_email ==="
$PSQL -c "
SELECT row_to_json(t) FROM (
  SELECT
    count(*) FILTER (
      WHERE a.type = 'oauth'
        AND a.schedulable = true
        AND COALESCE(
          NULLIF(trim(a.extra->>'email_address'), ''),
          NULLIF(trim(a.extra->>'email'), ''),
          NULLIF(trim(a.credentials->>'email_address'), ''),
          NULLIF(trim(a.credentials->>'email'), '')
        ) IS NULL
    ) AS oauth_schedulable_missing_email,
    count(*) FILTER (WHERE a.type = 'oauth' AND a.schedulable = true) AS oauth_schedulable_total
  FROM accounts a
  WHERE a.deleted_at IS NULL
) t;
" 2>&1
