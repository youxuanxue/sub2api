-- ⚠️ DEPRECATED 2026-05-21 — DO NOT USE FOR NEW WRITES.
-- Replaced by deploy/aws/stage0/anthropic-prod-group-r3-unified-apply-template.sql
-- under R3-unified.  Reasons:
--   1. This template only writes group.rpm_limit; under R3-unified the
--      per-stub `accounts.extra.declared_rpm` must also be written in
--      the same transaction (mirror baseline + audit trail).
--   2. This template relied on R3 absorb-zero (mixed group → unlimited),
--      which is now a forbidden state.  Using this template on a mixed
--      group writes the wrong value (computed as absorb-zero SUM).
--   3. R3-unified guard kinds (r3_group_sum_mismatch / r3_declared_rpm_*)
--      cannot be cleared by this template.
-- This file is retained for audit-trail / git-blame continuity only.
-- All new R3 writes MUST use anthropic-prod-group-r3-unified-apply-template.sql.
--
-- ============================================================
-- Historical doc (R3 absorb-zero path, no longer in force):
-- ============================================================
-- Anthropic stub-only group RPM mirror apply template (R3 mirror path)
-- Purpose: write group.rpm_limit on a prod-side stub-only group to mirror
-- the upstream edge's default_group.rpm_limit, per the R3 rule in
-- SKILL §"prod 控制面：anthropic stub 主路径镜像规则".
--
-- Why this template exists: the strict-redline group-aggregate template
-- (anthropic-oauth-group-aggregate-apply-template.sql) writes
-- Σ(account.base_rpm + sticky_buffer), which is wrong for stub-only
-- groups — they have no OAuth members to sum, so the aggregate would
-- collapse to 0 and unintentionally make the group unlimited.  R3 mirror
-- requires writing the precomputed mirror value (= upstream edge default
-- group's rpm_limit, with absorb-zero across stubs) instead.
--
-- Pre-flight:
--   1. Run ops/anthropic/check-prod-stub-mirror.py --json against this
--      target and read `group_results[i].expected_rpm_limit` for the
--      target group; pass that integer as :new_rpm_limit below.
--   2. The DO block aborts if any OAuth-type account is bound to the
--      target group; such groups belong to the strict-redline aggregate
--      path (anthropic-oauth-group-aggregate-apply-template.sql), not
--      here.
--
-- Usage (psql):
--   \set group_name 'cc-edges'
--   \set new_rpm_limit 16
--   \i deploy/aws/stage0/anthropic-stub-mirror-rpm-apply-template.sql

BEGIN;

-- Pre-flight: refuse to run on a group that owns any OAuth account.  Such
-- groups are governed by Σ(redline) via the strict-redline aggregate
-- template, not by R3 mirror.  Bridge :'group_name' into the DO block via
-- a session GUC so it is not expanded inside the dollar-quoted body.
SELECT set_config('app.target_group_name', :'group_name', true);
DO $$
DECLARE
  tg TEXT := current_setting('app.target_group_name');
  oauth_member TEXT;
BEGIN
  -- Group must exist; otherwise the WITH...UPDATE below silently no-ops
  -- (a typo in :'group_name' would leave the operator thinking the
  -- mirror was written when it wasn't).
  IF NOT EXISTS (
    SELECT 1 FROM groups WHERE name = tg AND deleted_at IS NULL
  ) THEN
    RAISE EXCEPTION 'group % not found (or soft-deleted)', quote_literal(tg);
  END IF;

  -- Refuse stub-mirror writes on groups that own OAuth accounts; those
  -- are governed by Σ(redline) via the strict-redline aggregate template.
  SELECT a.name INTO oauth_member
  FROM groups g
  JOIN account_groups ag ON ag.group_id = g.id
  JOIN accounts a ON a.id = ag.account_id
  WHERE g.name = tg
    AND g.deleted_at IS NULL
    AND a.platform = 'anthropic'
    AND a.type = 'oauth'
    AND a.deleted_at IS NULL
  LIMIT 1;
  IF oauth_member IS NOT NULL THEN
    RAISE EXCEPTION USING
      MESSAGE = 'group "' || tg || '" contains OAuth account "' || oauth_member || '"; R3 mirror template applies only to stub-only groups',
      HINT = 'Use deploy/aws/stage0/anthropic-oauth-group-aggregate-apply-template.sql for OAuth-bearing groups (strict-redline aggregate path).';
  END IF;
END $$;

WITH target_group AS (
  SELECT g.id, g.name, g.rpm_limit AS rpm_limit_before
  FROM groups g
  WHERE g.name = :'group_name'
    AND g.deleted_at IS NULL
  ORDER BY g.id
  LIMIT 1
),
updated_group AS (
  UPDATE groups g
  SET rpm_limit = :new_rpm_limit,
      updated_at = NOW()
  FROM target_group tg
  WHERE g.id = tg.id
  RETURNING g.id, g.name, tg.rpm_limit_before, g.rpm_limit AS rpm_limit_after
),
stub_members AS (
  SELECT
    a.id,
    a.name,
    a.platform,
    a.type,
    a.concurrency,
    NULLIF(a.credentials->>'base_url', '') AS base_url
  FROM target_group g
  JOIN account_groups ag ON ag.group_id = g.id
  JOIN accounts a ON a.id = ag.account_id
  WHERE a.deleted_at IS NULL
)
SELECT jsonb_pretty(jsonb_build_object(
  'group', (SELECT jsonb_build_object('id', id, 'name', name) FROM target_group),
  'rpm_limit_update', (SELECT jsonb_build_object(
    'before', rpm_limit_before,
    'after', rpm_limit_after,
    'expected_per_r3_mirror', :new_rpm_limit
  ) FROM updated_group),
  'stub_members', COALESCE((
    SELECT jsonb_agg(jsonb_build_object(
      'id', id,
      'name', name,
      'platform', platform,
      'type', type,
      'concurrency', concurrency,
      'base_url', base_url
    ) ORDER BY name)
    FROM stub_members
  ), '[]'::jsonb)
));

COMMIT;
