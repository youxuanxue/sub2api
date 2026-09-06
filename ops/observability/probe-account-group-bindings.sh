#!/usr/bin/env bash
# Read-only prod account model_mapping -> group-binding peer check.
set -euo pipefail

SNAPSHOT_SQL=$(cat <<'SQL'
SELECT jsonb_build_object(
  'accounts', COALESCE((
    SELECT jsonb_agg(jsonb_build_object(
      'id', a.id,
      'name', a.name,
      'platform', a.platform,
      'model_ids', COALESCE((
        SELECT jsonb_agg(model_id ORDER BY model_id)
        FROM jsonb_object_keys(
          CASE
            WHEN jsonb_typeof(a.credentials->'model_mapping') = 'object'
              THEN a.credentials->'model_mapping'
            ELSE '{}'::jsonb
          END
        ) AS model_id
      ), '[]'::jsonb),
      'groups', COALESCE((
        SELECT jsonb_agg(jsonb_build_object(
          'id', g.id, 'name', g.name, 'status', g.status
        ) ORDER BY g.id)
        FROM account_groups ag
        JOIN groups g ON g.id = ag.group_id AND g.deleted_at IS NULL
        WHERE ag.account_id = a.id
      ), '[]'::jsonb)
    ) ORDER BY a.id)
    FROM accounts a
    WHERE a.deleted_at IS NULL
      AND a.status = 'active'
      AND a.schedulable = true
      AND (a.temp_unschedulable_until IS NULL OR a.temp_unschedulable_until <= NOW())
      AND (a.expires_at IS NULL OR a.expires_at > NOW() OR a.auto_pause_on_expired = false)
      AND (a.overload_until IS NULL OR a.overload_until <= NOW())
      AND (a.rate_limit_reset_at IS NULL OR a.rate_limit_reset_at <= NOW())
  ), '[]'::jsonb),
  'groups', COALESCE((
    SELECT jsonb_agg(jsonb_build_object(
      'id', g.id, 'name', g.name, 'status', g.status
    ) ORDER BY g.id)
    FROM groups g
    WHERE g.deleted_at IS NULL AND g.status = 'active'
  ), '[]'::jsonb)
)::text;
SQL
)

if ! SNAPSHOT="$(docker exec -i \
    -e 'PGOPTIONS=-c default_transaction_read_only=on -c lock_timeout=100ms -c statement_timeout=10s' \
    tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1 \
    -c "$SNAPSHOT_SQL")"; then
  echo '{"verdict":"setup_error","error":"snapshot_query_failed"}'
  exit 2
fi

printf '%s\n' "$SNAPSHOT" | python3 /tmp/account_group_binding_check.py --snapshot -
