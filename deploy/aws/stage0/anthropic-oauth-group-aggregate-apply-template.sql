-- Anthropic OAuth group aggregate apply template (strict-redline mode)
-- Purpose: Aggregate available account redline (base_rpm + rpm_sticky_buffer)
-- under a target group and update group.rpm_limit so the group cap matches
-- the sum of per-account NotSchedulable thresholds.  This leaves room for
-- StickyOnly (yellow-zone) traffic the runtime account scheduler intends to
-- allow, rather than dropping it at the group gate.
--
-- Pre-flight: run ops/anthropic/check-account-group-rpm-alignment.py
--   --target <edge_id|prod> --strict-redline
-- and confirm 0 violations before applying this template.  The DO block at
-- the top is belt-and-suspenders: it aborts the transaction with a clear
-- message if any base_rpm-carrying account is missing rpm_sticky_buffer, so
-- a stale baseline cannot silently re-compute group.rpm_limit downward.
--
-- Usage (psql):
--   \set group_name 'default'
--   \i deploy/aws/stage0/anthropic-oauth-group-aggregate-apply-template.sql
--
-- Capacity aggregation operator: absorb-zero SUM keyed on base_rpm.
-- Runtime treats 0 as "unlimited" for account.base_rpm (and the buffer is
-- additive — meaningless on its own when base_rpm=0).  Naive SUM would
-- silently treat "unlimited" as a 0 datum and underestimate the group
-- ceiling; absorb-zero propagates the unlimited semantic: any member
-- account with base_rpm=0 makes the aggregate also 0 (unlimited), otherwise
-- SUM(base_rpm + rpm_sticky_buffer).

BEGIN;

-- Pre-flight: every base_rpm-carrying account must also carry
-- rpm_sticky_buffer > 0 (strict-redline mode requires manual override;
-- baseline tiers L1-L5 supply this).  Bridge the psql variable into the DO
-- block via a session GUC so :'group_name' is not expanded inside the
-- dollar-quoted PL/pgSQL body.
SELECT set_config('app.target_group_name', :'group_name', true);
DO $$
DECLARE
  tg TEXT := current_setting('app.target_group_name');
  drift TEXT;
BEGIN
  -- Group must exist; otherwise the WITH...UPDATE below silently no-ops
  -- (a typo in :'group_name' would leave the operator thinking the
  -- aggregate was written when it wasn't).
  IF NOT EXISTS (
    SELECT 1 FROM groups WHERE name = tg AND deleted_at IS NULL
  ) THEN
    RAISE EXCEPTION 'group % not found (or soft-deleted)', quote_literal(tg);
  END IF;

  -- Baseline drift: every base_rpm-carrying account must also carry
  -- rpm_sticky_buffer.
  SELECT string_agg(a.name, ', ') INTO drift
  FROM groups g
  JOIN account_groups ag ON ag.group_id = g.id
  JOIN accounts a ON a.id = ag.account_id
  WHERE g.name = tg
    AND g.deleted_at IS NULL
    AND a.platform = 'anthropic'
    AND a.type = 'oauth'
    AND a.deleted_at IS NULL
    AND COALESCE(a.status, '') NOT IN ('disabled', 'suspended')
    AND COALESCE(NULLIF(a.extra->>'base_rpm', '')::int, 0) > 0
    AND COALESCE(NULLIF(a.extra->>'rpm_sticky_buffer', '')::int, 0) <= 0;
  IF drift IS NOT NULL THEN
    RAISE EXCEPTION USING
      MESSAGE = 'baseline drift: rpm_sticky_buffer missing on account(s) in group "' || tg || '": [' || drift || ']',
      HINT = 'Run ops/anthropic/check-account-group-rpm-alignment.py --target <id> --strict-redline and update baseline per deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json before re-applying this template.';
  END IF;
END $$;

WITH input AS (
  SELECT :'group_name'::text AS group_name
),
target_group AS (
  SELECT g.id, g.name, g.rpm_limit AS rpm_limit_before
  FROM groups g
  JOIN input i ON g.name = i.group_name
  WHERE g.deleted_at IS NULL
  ORDER BY g.id
  LIMIT 1
),
available_accounts AS (
  SELECT
    a.id,
    a.name,
    a.concurrency,
    a.priority,
    COALESCE(NULLIF(a.extra->>'base_rpm', '')::int, 0) AS base_rpm,
    COALESCE(NULLIF(a.extra->>'rpm_sticky_buffer', '')::int, 0) AS rpm_sticky_buffer,
    COALESCE(NULLIF(a.extra->>'max_sessions', '')::int, 0) AS max_sessions,
    COALESCE(NULLIF(a.extra->>'window_cost_limit', '')::int, 0) AS window_cost_limit,
    NULLIF(a.extra->>'stability_tier', '') AS stability_tier
  FROM target_group g
  JOIN account_groups ag ON ag.group_id = g.id
  JOIN accounts a ON a.id = ag.account_id
  WHERE a.deleted_at IS NULL
    AND a.platform = 'anthropic'
    AND a.type = 'oauth'
    AND COALESCE(a.status, '') NOT IN ('disabled', 'suspended')
),
agg AS (
  -- absorb-zero SUM keyed on base_rpm; total_redline = Σ(base_rpm + buffer)
  -- with the same absorb-zero gate (buffer alone is meaningless when
  -- base_rpm=0, which means "unlimited" at runtime).  Empty group
  -- aggregates to 0 (no members ⇒ unlimited is the conservative/no-cap
  -- choice; operator should not be applying this template against an empty
  -- group anyway).
  SELECT
    COUNT(*)::int AS available_account_count,
    CASE WHEN bool_or(base_rpm = 0)
         THEN 0
         ELSE COALESCE(SUM(base_rpm), 0)::int END AS total_base_rpm,
    CASE WHEN bool_or(base_rpm = 0)
         THEN 0
         ELSE COALESCE(SUM(base_rpm + rpm_sticky_buffer), 0)::int END AS total_redline,
    CASE WHEN bool_or(max_sessions = 0)
         THEN 0
         ELSE COALESCE(SUM(max_sessions), 0)::int END AS total_max_sessions,
    CASE WHEN bool_or(window_cost_limit = 0)
         THEN 0
         ELSE COALESCE(SUM(window_cost_limit), 0)::int END AS total_window_cost_limit,
    CASE WHEN bool_or(concurrency = 0)
         THEN 0
         ELSE COALESCE(SUM(concurrency), 0)::int END AS effective_concurrency,
    COALESCE(MIN(priority), 0)::int AS min_priority,
    COALESCE(MAX(priority), 0)::int AS max_priority
  FROM available_accounts
),
updated_group AS (
  UPDATE groups g
  SET
    rpm_limit = a.total_redline,
    updated_at = NOW()
  FROM target_group tg, agg a
  WHERE g.id = tg.id
  RETURNING g.id, g.name, tg.rpm_limit_before, g.rpm_limit AS rpm_limit_after
),
tier_distribution AS (
  SELECT
    COALESCE(
      jsonb_object_agg(stability_tier, cnt ORDER BY stability_tier),
      '{}'::jsonb
    ) AS value
  FROM (
    SELECT COALESCE(stability_tier, 'unknown') AS stability_tier, COUNT(*)::int AS cnt
    FROM available_accounts
    GROUP BY COALESCE(stability_tier, 'unknown')
  ) t
)
SELECT jsonb_pretty(jsonb_build_object(
  'group', (SELECT jsonb_build_object('id', id, 'name', name) FROM target_group),
  'rpm_limit_update', (SELECT jsonb_build_object(
    'before', rpm_limit_before,
    'after', rpm_limit_after
  ) FROM updated_group),
  'group_agg', (SELECT jsonb_build_object(
    'available_account_count', available_account_count,
    'total_base_rpm', total_base_rpm,
    'total_redline', total_redline,
    'total_max_sessions', total_max_sessions,
    'total_window_cost_limit', total_window_cost_limit,
    'effective_concurrency', effective_concurrency,
    'min_priority', min_priority,
    'max_priority', max_priority,
    'tier_distribution', (SELECT value FROM tier_distribution)
  ) FROM agg),
  'members', COALESCE((
    SELECT jsonb_agg(jsonb_build_object(
      'id', id,
      'name', name,
      'base_rpm', base_rpm,
      'rpm_sticky_buffer', rpm_sticky_buffer,
      'redline', base_rpm + rpm_sticky_buffer,
      'max_sessions', max_sessions,
      'window_cost_limit', window_cost_limit,
      'concurrency', concurrency,
      'priority', priority,
      'stability_tier', stability_tier
    ) ORDER BY name)
    FROM available_accounts
  ), '[]'::jsonb)
));

COMMIT;
