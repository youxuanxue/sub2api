-- Anthropic OAuth priority rebalance apply template.
-- Write surface: accounts.priority (smaller wins) for a single
-- anthropic+oauth account on the local edge DB.
--
-- Scope: this template ONLY writes priority. It does not touch
-- tier baseline (concurrency / base_rpm / sticky_buffer / max_sessions /
-- window_cost_limit / stability_tier / credentials / extra). Tier
-- baseline is the write surface of
--   ops/anthropic/manage-anthropic-config.py + the tier-baseline
--   apply template. The two pipelines must not co-write any field.
--
-- Usage (psql):
--   \set account_name 'en-ld-ec2-16-1-b'
--   \set new_priority 22
--   \i deploy/aws/stage0/anthropic-oauth-priority-rebalance-apply-template.sql
--
-- Caller responsibility (Python orchestrator):
--   - choose new_priority within the same stability tier band
--     (l1=10..19, l2=20..29, l3=30..39, l4=40..49, l5=50..59) so the
--     rebalance never crosses tier boundaries
--   - re-apply this template after every tier-baseline apply, since
--     the tier-baseline template resets priority to the tier base

CREATE OR REPLACE FUNCTION pg_temp.pg_raise(fmt text, val text)
RETURNS text AS $$
BEGIN
  RAISE EXCEPTION USING MESSAGE = format(fmt, val);
END;
$$ LANGUAGE plpgsql;

BEGIN;

WITH input AS (
  SELECT
    :'account_name'::text AS account_name,
    :new_priority::int    AS new_priority
),
range_check AS (
  SELECT
    CASE
      WHEN (SELECT new_priority FROM input) < 1
        OR (SELECT new_priority FROM input) > 999 THEN
        pg_temp.pg_raise(
          'new_priority out of safe range (expected 1..999): %s',
          (SELECT new_priority::text FROM input))
      ELSE 'ok'
    END AS ok
),
target AS (
  SELECT a.id, a.name, a.priority AS priority_before
  FROM accounts a
  JOIN input i ON a.name = i.account_name
  WHERE a.platform = 'anthropic'
    AND a.type = 'oauth'
    AND a.deleted_at IS NULL
  ORDER BY a.id
  LIMIT 1
),
target_check AS (
  SELECT
    CASE
      WHEN NOT EXISTS (SELECT 1 FROM target) THEN
        pg_temp.pg_raise(
          'no live anthropic+oauth account named %s on this edge',
          (SELECT account_name FROM input))
      ELSE 'ok'
    END AS ok
),
updated AS (
  UPDATE accounts a
  SET
    priority = i.new_priority,
    updated_at = NOW()
  FROM input i, target t, range_check r, target_check tc
  WHERE a.id = t.id AND r.ok = 'ok' AND tc.ok = 'ok'
  RETURNING a.id, a.name, t.priority_before, a.priority AS priority_after
)
SELECT
  id || '|' || name || '|' ||
  COALESCE(priority_before::text, 'null') || '|' || priority_after::text
FROM updated;

COMMIT;
