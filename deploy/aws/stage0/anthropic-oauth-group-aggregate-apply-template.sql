-- Anthropic OAuth group aggregate apply template
-- Purpose: Aggregate available account capacity under a target group and update group-level config.
-- Usage (psql):
--   \set group_name 'default'
--   \i deploy/aws/stage0/anthropic-oauth-group-aggregate-apply-template.sql
--
-- Capacity aggregation operator: absorb-zero SUM.
-- Runtime treats 0 as "unlimited" for account.base_rpm / max_sessions /
-- window_cost_limit / concurrency (see SKILL.md §"prod 控制面：anthropic
-- stub 主路径镜像规则" 的"容量约定").  Naive SUM would silently treat
-- "unlimited" as a 0 datum and underestimate the group ceiling; absorb-zero
-- propagates the unlimited semantic: any member account with field=0 makes
-- the aggregate also 0 (unlimited), otherwise SUM of positives.

BEGIN;

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
  -- absorb-zero SUM: any member with field=0 (runtime: unlimited) makes the
  -- aggregate also 0 (unlimited); otherwise SUM of positives.  Empty group
  -- aggregates to 0 (no members ⇒ unlimited is the conservative/no-cap
  -- choice; operator should not be applying this template against an empty
  -- group anyway).
  SELECT
    COUNT(*)::int AS available_account_count,
    CASE WHEN bool_or(base_rpm = 0)
         THEN 0
         ELSE COALESCE(SUM(base_rpm), 0)::int END AS total_base_rpm,
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
    rpm_limit = a.total_base_rpm,
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
