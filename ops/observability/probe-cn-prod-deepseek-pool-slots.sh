#!/usr/bin/env bash
# Read-only: cn-prod seats for deepseek-flash loadtest planning.
# On cn-prod, deepseek-flash is commonly served by VolcEngine agent-plan
# accounts (channel_type=45) bound to china_deployed — not native DeepSeek 43.
# Seats = Σ COALESCE(load_factor, concurrency) for currently schedulable accounts.
set -euo pipefail

for _tk_lib in "${TK_LIB_DIR:-$(dirname "${BASH_SOURCE[0]}")}" "$(dirname "${BASH_SOURCE[0]}")/../lib"; do
  [ -f "$_tk_lib/resolve-app-container.sh" ] && break
done
# shellcheck source=../lib/resolve-app-container.sh
. "$_tk_lib/resolve-app-container.sh" || { echo '{"verdict":"setup_error","error":"container resolver not found"}' >&2; exit 1; }
TK_PG="$(tk_require_postgres_container "${POSTGRES_CONTAINER:-auto}")" || exit 1

docker exec -i \
  -e 'PGOPTIONS=-c default_transaction_read_only=on -c lock_timeout=100ms -c statement_timeout=10s' \
  "$TK_PG" psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1 <<'SQL'
SELECT jsonb_pretty(jsonb_build_object(
  'verdict', 'ok',
  'note', 'POOL_SLOTS for china_deployed deepseek-flash ≈ seats_schedulable_now on the serving channel',
  'china_deployed_group', COALESCE((
    SELECT jsonb_build_object('id', g.id, 'name', g.name, 'status', g.status, 'platform', g.platform)
    FROM groups g
    WHERE g.deleted_at IS NULL AND g.name = 'china_deployed'
    LIMIT 1
  ), 'null'::jsonb),
  'china_deployed_api_keys', COALESCE((
    SELECT jsonb_agg(jsonb_build_object(
      'key_id', k.id, 'name', k.name, 'status', k.status, 'user_id', k.user_id
    ) ORDER BY k.id)
    FROM api_keys k
    JOIN groups g ON g.id = k.group_id AND g.deleted_at IS NULL
    WHERE k.deleted_at IS NULL AND g.name = 'china_deployed'
  ), '[]'::jsonb),
  'pools', COALESCE((
    SELECT jsonb_agg(row_to_json(p) ORDER BY p.seats_schedulable_now DESC, p.group_id, p.channel_type)
    FROM (
      SELECT
        g.id AS group_id,
        g.name AS group_name,
        a.platform,
        a.channel_type,
        COUNT(*)::int AS accounts_bound,
        COUNT(*) FILTER (
          WHERE a.status = 'active'
            AND a.schedulable = true
            AND (a.temp_unschedulable_until IS NULL OR a.temp_unschedulable_until <= NOW())
            AND (a.rate_limit_reset_at IS NULL OR a.rate_limit_reset_at <= NOW())
            AND (a.overload_until IS NULL OR a.overload_until <= NOW())
        )::int AS schedulable_accounts,
        COALESCE(SUM(
          CASE
            WHEN a.status = 'active'
              AND a.schedulable = true
              AND (a.temp_unschedulable_until IS NULL OR a.temp_unschedulable_until <= NOW())
              AND (a.rate_limit_reset_at IS NULL OR a.rate_limit_reset_at <= NOW())
              AND (a.overload_until IS NULL OR a.overload_until <= NOW())
            THEN COALESCE(NULLIF(a.load_factor, 0), NULLIF(a.concurrency, 0), 0)
            ELSE 0
          END
        ), 0)::int AS seats_schedulable_now,
        BOOL_OR(
          COALESCE(a.credentials->'model_mapping' ? 'deepseek-flash', false)
          OR COALESCE(a.credentials->'model_mapping' ? 'deepseek-v4-flash', false)
          OR COALESCE(a.credentials->'model_mapping' ? 'deepseek-v4.1-flash', false)
        ) AS maps_deepseek_flash_surface
      FROM accounts a
      JOIN account_groups ag ON ag.account_id = a.id
      JOIN groups g ON g.id = ag.group_id AND g.deleted_at IS NULL
      WHERE a.deleted_at IS NULL
        AND (
          a.channel_type IN (43, 45)
          OR COALESCE(a.credentials->'model_mapping' ? 'deepseek-flash', false)
          OR COALESCE(a.credentials->'model_mapping' ? 'deepseek-v4-flash', false)
          OR COALESCE(a.credentials->'model_mapping' ? 'deepseek-v4.1-flash', false)
        )
        AND g.name IN ('china_deployed', 'china', 'kimiK3-rt')
      GROUP BY g.id, g.name, a.platform, a.channel_type
    ) p
  ), '[]'::jsonb),
  'native_deepseek_channel_43_bound', COALESCE((
    SELECT COUNT(*)::int FROM accounts a
    WHERE a.deleted_at IS NULL AND a.channel_type = 43
  ), 0)
));
SQL
